#!/usr/bin/env python3
"""khoomei: one low drone, many overtones.

A hub-and-caller messaging layer for personal AI agents on a Tailscale network.
One machine that can listen on its tailnet address runs the hub; agents that can
only make outbound connections (Muse, for example) are callers and talk to the
hub over HTTP. Standard library only, Python 3.8+.

Usage: khoomei.py [--home DIR] COMMAND ...
Run `khoomei.py --help` for the command list.
"""

import argparse
import base64
import ipaddress
import json
import os
import platform
import re
import secrets
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

VERSION = "0.1.0"
PROTOCOL = 1
DEFAULT_PORT = 7767
MAX_BODY = 256 * 1024
AGENT_RE = re.compile(r"^[^\s/\x00-\x1f][^/\x00-\x1f]{0,62}[^\s/\x00-\x1f]$|^[^\s/\x00-\x1f]$")
ID_RE = re.compile(r"^[A-Za-z0-9_-]{8,64}$")
TAILNET_V4 = ipaddress.ip_network("100.64.0.0/10")
TAILNET_V6 = ipaddress.ip_network("fd7a:115c:a1e0::/48")
WAKE_LEVELS = ("none", "batch", "now")
ENVELOPE_FIELDS = {"v", "id", "from_agent", "to_agent", "ts", "kind", "text", "reply_to", "wake", "data"}


# --------------------------------------------------------------------------- utilities


def now_iso():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def new_id():
    return base64.urlsafe_b64encode(secrets.token_bytes(12)).decode().rstrip("=")


def die(msg, code=2):
    print("khoomei: " + msg, file=sys.stderr)
    sys.exit(code)


def home_dir(args):
    return os.path.abspath(args.home or os.environ.get("KHOOMEI_HOME") or os.path.expanduser("~/.khoomei"))


def read_json(path, default=None):
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return default


def write_json(path, obj):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(obj, f, ensure_ascii=False, indent=2)
    os.replace(tmp, path)


def valid_agent(name):
    return isinstance(name, str) and bool(AGENT_RE.match(name))


# --------------------------------------------------------------------------- config


class Config:
    """~/.khoomei/config.json

    {
      "role": "hub" | "caller",
      "agent": "muse",                          # this machine's own agent name
      "hub": "grok-box.tail1234.ts.net:7767",   # callers only
      "listen": "100.101.102.103",              # hub only; tailnet IP or "auto"
      "port": 7767,
      "tailscale_cmd": "tailscale",
      "policy": {"mode": "tailnet", "deny": []},
      "agents": {
        "grok": {"node": "grok-box.tail1234.ts.net", "local": true,
                 "wake": {"type": "webhook", "url_file": "...", "secret_file": "..."}},
        "muse": {"node": "muse-vm.tail1234.ts.net"}
      }
    }
    """

    def __init__(self, home):
        self.home = home
        self.path = os.path.join(home, "config.json")
        self.data = read_json(self.path, {}) or {}

    def save(self):
        if os.path.exists(self.path):
            shutil.copy2(self.path, self.path + ".bak")
        write_json(self.path, self.data)

    @property
    def role(self):
        return self.data.get("role", "caller")

    @property
    def agent(self):
        return self.data.get("agent")

    @property
    def agents(self):
        return self.data.setdefault("agents", {})

    @property
    def port(self):
        return int(self.data.get("port", DEFAULT_PORT))

    @property
    def tailscale_cmd(self):
        cmd = self.data.get("tailscale_cmd")
        if cmd:
            return cmd
        mac = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
        if platform.system() == "Darwin" and not shutil.which("tailscale") and os.path.exists(mac):
            return mac
        return "tailscale"

    def hub_url(self):
        hub = self.data.get("hub")
        if not hub:
            die("no hub configured; run: khoomei setup --caller --hub HOST[:PORT] --agent NAME")
        if ":" not in hub.rsplit("]", 1)[-1]:
            hub = "%s:%d" % (hub, DEFAULT_PORT)
        return "http://" + hub


# --------------------------------------------------------------------------- tailscale


class Tailscale:
    def __init__(self, cmd):
        self.cmd = cmd
        self._cache = {}

    def _run(self, *argv, timeout=3):
        return subprocess.run([self.cmd, *argv], capture_output=True, text=True, timeout=timeout)

    def self_ip(self):
        r = self._run("ip", "-4")
        if r.returncode != 0:
            raise RuntimeError(r.stderr.strip() or "tailscale ip failed")
        return r.stdout.split()[0]

    def self_name(self):
        r = self._run("status", "--json")
        if r.returncode != 0:
            raise RuntimeError(r.stderr.strip() or "tailscale status failed")
        return json.loads(r.stdout)["Self"]["DNSName"].rstrip(".")

    def whois(self, ip):
        """Return the tailnet node name (FQDN, no trailing dot) for ip, or raise."""
        hit = self._cache.get(ip)
        if hit and hit[0] > time.time():
            return hit[1]
        try:
            r = self._run("whois", "--json", ip)
        except subprocess.TimeoutExpired:
            raise IdentityUnavailable("whois timed out")
        if r.returncode != 0:
            err = (r.stderr or r.stdout).strip()
            if "connect" in err.lower() or "tailscaled" in err.lower():
                raise IdentityUnavailable(err)
            raise PermissionError(err or "unknown peer")
        name = json.loads(r.stdout)["Node"]["Name"].rstrip(".")
        self._cache[ip] = (time.time() + 60, name)
        return name


class IdentityUnavailable(Exception):
    pass


# --------------------------------------------------------------------------- mailbox


class Mailbox:
    """inbox/<agent>/<id>.json, plus acked/<agent>/<id>.json after acknowledgement."""

    def __init__(self, home):
        self.home = home
        self.lock = threading.Lock()
        self.cond = threading.Condition(self.lock)

    def _dir(self, kind, agent):
        return os.path.join(self.home, kind, agent)

    def exists(self, agent, mid):
        return any(os.path.exists(os.path.join(self._dir(k, agent), mid + ".json")) for k in ("inbox", "acked"))

    def store(self, envelope, peer):
        agent = envelope["to_agent"]
        # seq orders messages that land within the same second of `at`.
        record = {"envelope": envelope, "received": {"at": now_iso(), "seq": time.time_ns(), "peer": peer}}
        with self.cond:
            if self.exists(agent, envelope["id"]):
                return False
            write_json(os.path.join(self._dir("inbox", agent), envelope["id"] + ".json"), record)
            self.cond.notify_all()
        return True

    def pending(self, agent):
        d = self._dir("inbox", agent)
        if not os.path.isdir(d):
            return []
        out = []
        for name in sorted(os.listdir(d)):
            if name.endswith(".json"):
                rec = read_json(os.path.join(d, name))
                if rec:
                    out.append(rec)
        out.sort(key=lambda r: (r["received"]["at"], r["received"].get("seq", 0)))
        return out

    def wait(self, agent, timeout):
        deadline = time.time() + timeout
        with self.cond:
            while True:
                items = self.pending(agent)
                if items:
                    return items
                left = deadline - time.time()
                if left <= 0:
                    return []
                self.cond.wait(min(left, 5))

    def ack(self, agent, ids):
        done = []
        with self.lock:
            for mid in ids:
                src = os.path.join(self._dir("inbox", agent), mid + ".json")
                if os.path.exists(src):
                    dst_dir = self._dir("acked", agent)
                    os.makedirs(dst_dir, exist_ok=True)
                    os.replace(src, os.path.join(dst_dir, mid + ".json"))
                    done.append(mid)
        return done


# --------------------------------------------------------------------------- wake adapters


class Waker:
    def __init__(self, cfg):
        self.cfg = cfg
        self.counts = {}  # (from_agent, to_agent) -> [hour_bucket, n]

    def effective_level(self, envelope):
        requested = envelope.get("wake", "batch")
        target = self.cfg.agents.get(envelope["to_agent"], {})
        cap = target.get("wake_max", "now")
        level = min(requested, cap, key=WAKE_LEVELS.index)
        if level == "now":
            key = (envelope.get("from_agent"), envelope["to_agent"])
            bucket = int(time.time() // 3600)
            hour, n = self.counts.get(key, (bucket, 0))
            if hour != bucket:
                hour, n = bucket, 0
            if n >= int(target.get("wake_rate", 6)):
                level = "batch"
            else:
                self.counts[key] = (hour, n + 1)
        return level

    def wake(self, envelope):
        adapter = self.cfg.agents.get(envelope["to_agent"], {}).get("wake")
        if not adapter or self.effective_level(envelope) != "now":
            return
        threading.Thread(target=self._fire, args=(adapter, envelope), daemon=True).start()

    def _fire(self, adapter, envelope):
        summary = {"id": envelope["id"], "from_agent": envelope.get("from_agent"),
                   "to_agent": envelope["to_agent"], "kind": envelope.get("kind", "text"),
                   "preview": (envelope.get("text") or "")[:200]}
        try:
            if adapter.get("type") == "exec":
                env = dict(os.environ, KHOOMEI_MSG_ID=envelope["id"], KHOOMEI_TO_AGENT=envelope["to_agent"])
                subprocess.run(adapter["argv"], env=env, timeout=60, input=json.dumps(summary), text=True)
            elif adapter.get("type") == "webhook":
                url = open(adapter["url_file"]).read().strip()
                headers = {"Content-Type": "application/json"}
                if adapter.get("secret_file"):
                    headers["Authorization"] = "Bearer " + open(adapter["secret_file"]).read().strip()
                req = urllib.request.Request(url, data=json.dumps(summary).encode(), headers=headers, method="POST")
                urllib.request.urlopen(req, timeout=20).read()
        except Exception as e:  # a failed wake never loses the message; it stays in the inbox
            print("wake %s failed: %s" % (envelope["to_agent"], e), file=sys.stderr)


# --------------------------------------------------------------------------- hub server


def validate_envelope(env):
    if not isinstance(env, dict):
        return "envelope must be an object"
    extra = set(env) - ENVELOPE_FIELDS
    if extra:
        return "unknown fields: " + ", ".join(sorted(extra))
    if env.get("v") != PROTOCOL:
        return "v must be %d" % PROTOCOL
    if not ID_RE.match(str(env.get("id", ""))):
        return "bad id"
    for k in ("from_agent", "to_agent"):
        if not valid_agent(env.get(k)):
            return "bad " + k
    if not re.match(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$", str(env.get("ts", ""))):
        return "bad ts"
    if env.get("wake", "batch") not in WAKE_LEVELS:
        return "bad wake"
    if "kind" in env and not (isinstance(env["kind"], str) and 1 <= len(env["kind"]) <= 32):
        return "bad kind"
    if "text" in env and not isinstance(env["text"], str):
        return "text must be a string"
    if "reply_to" in env and not ID_RE.match(str(env["reply_to"])):
        return "bad reply_to"
    return None


class Hub:
    def __init__(self, cfg, mailbox, tailscale, trust_loopback=True, no_whois=False):
        self.cfg = cfg
        self.mailbox = mailbox
        self.ts = tailscale
        self.waker = Waker(cfg)
        self.trust_loopback = trust_loopback
        self.no_whois = no_whois

    # identity --------------------------------------------------------------
    def identify(self, ip, dev_peer=None):
        """Return ("local", None) for the hub's own loopback, or ("peer", node_name).

        dev_peer is honoured only under --no-whois: it lets loopback tests pose as a
        remote node. A real hub never trusts a caller-supplied identity."""
        addr = ipaddress.ip_address(ip)
        if self.no_whois and dev_peer:
            return ("peer", dev_peer)
        if addr.is_loopback:
            if self.trust_loopback:
                return ("local", None)
            raise PermissionError("loopback not trusted")
        if addr not in TAILNET_V4 and addr not in TAILNET_V6:
            raise PermissionError("not a tailnet address")
        if self.no_whois:
            return ("peer", ip)
        node = self.ts.whois(ip)
        for rule in self.cfg.data.get("policy", {}).get("deny", []):
            if rule == node or rule == node.split(".")[0]:
                raise PermissionError("denied by policy")
        return ("peer", node)

    def authorize_agent(self, identity, agent):
        """May this connection act as `agent`? Loopback may act as any local agent;
        a peer may act only as agents bound to its node."""
        kind, node = identity
        spec = self.cfg.agents.get(agent)
        if kind == "local":
            if spec is None or spec.get("local", False) or not spec.get("node"):
                return True
            raise PermissionError("%s is bound to node %s, not to this hub" % (agent, spec["node"]))
        if spec is None:
            raise PermissionError("unknown agent %s; the hub owner must register it" % agent)
        bound = spec.get("node")
        if not bound:
            raise PermissionError("%s has no node binding" % agent)
        if bound == node or bound == node.split(".")[0] or node.startswith(bound + "."):
            return True
        raise PermissionError("%s is bound to %s, not %s" % (agent, bound, node))

    # operations ------------------------------------------------------------
    def receive(self, identity, envelope):
        err = validate_envelope(envelope)
        if err:
            raise ValueError(err)
        self.authorize_agent(identity, envelope["from_agent"])
        if envelope["to_agent"] not in self.cfg.agents:
            raise ValueError("unknown to_agent %s" % envelope["to_agent"])
        peer = identity[1] or "hub"
        fresh = self.mailbox.store(envelope, peer)
        if fresh:
            self.waker.wake(envelope)
        return fresh


class Handler(BaseHTTPRequestHandler):
    server_version = "khoomei/" + VERSION
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        if os.environ.get("KHOOMEI_DEBUG"):
            super().log_message(fmt, *args)

    @property
    def hub(self):
        return self.server.hub

    def _send(self, code, obj, extra=None):
        body = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def _identity(self):
        try:
            return self.hub.identify(self.client_address[0], self.headers.get("X-Khoomei-Dev-Peer"))
        except IdentityUnavailable as e:
            self._send(503, {"ok": False, "error": "identity lookup unavailable", "code": "identity_unavailable",
                             "detail": str(e)}, {"Retry-After": "1"})
        except PermissionError as e:
            self._send(403, {"ok": False, "error": str(e)})
        return None

    def _body(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY:
            self._send(413, {"ok": False, "error": "too large"})
            return None
        if "application/json" not in (self.headers.get("Content-Type") or ""):
            self._send(415, {"ok": False, "error": "expected application/json"})
            return None
        try:
            return json.loads(self.rfile.read(length).decode("utf-8"))
        except Exception:
            self._send(400, {"ok": False, "error": "invalid json"})
            return None

    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        q = urllib.parse.parse_qs(url.query)
        if url.path == "/health":
            return self._send(200, {"ok": True, "v": PROTOCOL, "version": VERSION})
        identity = self._identity()
        if identity is None:
            return
        if url.path in ("/inbox", "/wait"):
            agent = (q.get("agent") or [None])[0]
            if not valid_agent(agent):
                return self._send(400, {"ok": False, "error": "bad agent"})
            try:
                self.hub.authorize_agent(identity, agent)
            except PermissionError as e:
                return self._send(403, {"ok": False, "error": str(e)})
            if url.path == "/wait":
                timeout = min(float((q.get("timeout") or ["50"])[0]), 120)
                items = self.hub.mailbox.wait(agent, timeout)
            else:
                items = self.hub.mailbox.pending(agent)
            return self._send(200, {"ok": True, "agent": agent, "messages": items})
        if url.path == "/agents":
            if identity[0] != "local":
                return self._send(403, {"ok": False, "error": "owner only"})
            return self._send(200, {"ok": True, "agents": self.hub.cfg.agents})
        self._send(404, {"ok": False, "error": "not found"})

    def do_POST(self):
        identity = self._identity()
        if identity is None:
            return
        body = self._body()
        if body is None:
            return
        if self.path == "/msg":
            try:
                fresh = self.hub.receive(identity, body)
            except ValueError as e:
                return self._send(400, {"ok": False, "error": str(e)})
            except PermissionError as e:
                return self._send(403, {"ok": False, "error": str(e)})
            return self._send(202 if fresh else 200, {"ok": True, "id": body["id"], "duplicate": not fresh})
        if self.path == "/ack":
            agent, ids = body.get("agent"), body.get("ids")
            if not valid_agent(agent) or not isinstance(ids, list) or not all(ID_RE.match(str(i)) for i in ids):
                return self._send(400, {"ok": False, "error": "bad agent or ids"})
            try:
                self.hub.authorize_agent(identity, agent)
            except PermissionError as e:
                return self._send(403, {"ok": False, "error": str(e)})
            return self._send(200, {"ok": True, "acked": self.hub.mailbox.ack(agent, ids)})
        self._send(404, {"ok": False, "error": "not found"})


class HubServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, addr, hub):
        self.hub = hub
        super().__init__(addr, Handler)


def serve(cfg, listen=None, port=None, no_whois=False):
    ts = Tailscale(cfg.tailscale_cmd)
    listen = listen or cfg.data.get("listen", "auto")
    if listen == "auto":
        listen = ts.self_ip()
    hub = Hub(cfg, Mailbox(cfg.home), ts, no_whois=no_whois)
    servers = []
    for addr in {listen, "127.0.0.1"}:
        srv = HubServer((addr, port or cfg.port), hub)
        servers.append(srv)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        print("khoomei hub %s listening on %s:%d" % (VERSION, addr, srv.server_address[1]), flush=True)
    if no_whois:
        print("WARNING: --no-whois: peers are identified by IP only. Development use only.", flush=True)
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        for s in servers:
            s.shutdown()


# --------------------------------------------------------------------------- client


class Client:
    """HTTP client used by callers (against the hub) and by the hub's own CLI (against loopback)."""

    def __init__(self, base, dev_peer=None):
        self.base = base.rstrip("/")
        self.dev_peer = dev_peer or os.environ.get("KHOOMEI_DEV_PEER")

    def _call(self, method, path, body=None, timeout=30):
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json"} if data else {}
        if self.dev_peer:
            headers["X-Khoomei-Dev-Peer"] = self.dev_peer
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.status, json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            try:
                return e.code, json.loads(e.read().decode())
            except Exception:
                return e.code, {"ok": False, "error": e.reason}
        except urllib.error.URLError as e:
            die("cannot reach %s: %s" % (self.base, e.reason), 3)

    def send(self, envelope):
        return self._call("POST", "/msg", envelope)

    def inbox(self, agent):
        return self._call("GET", "/inbox?agent=" + urllib.parse.quote(agent))

    def wait(self, agent, timeout):
        return self._call("GET", "/wait?agent=%s&timeout=%d" % (urllib.parse.quote(agent), timeout), timeout=timeout + 15)

    def ack(self, agent, ids):
        return self._call("POST", "/ack", {"agent": agent, "ids": ids})

    def health(self):
        return self._call("GET", "/health", timeout=5)


def client_for(cfg):
    if cfg.role == "hub":
        return Client("http://127.0.0.1:%d" % cfg.port)
    return Client(cfg.hub_url())


def build_envelope(cfg, to_agent, text, kind="text", reply_to=None, wake="batch", from_agent=None, data=None):
    env = {"v": PROTOCOL, "id": new_id(), "from_agent": from_agent or cfg.agent, "to_agent": to_agent,
           "ts": now_iso(), "kind": kind, "wake": wake}
    if text is not None:
        env["text"] = text
    if reply_to:
        env["reply_to"] = reply_to
    if data is not None:
        env["data"] = data
    if not env["from_agent"]:
        die("no agent name: pass --as NAME or set one with setup --agent NAME")
    return env


# --------------------------------------------------------------------------- commands


def cmd_setup(args, cfg):
    if args.hub_role:
        cfg.data["role"] = "hub"
        if args.listen:
            cfg.data["listen"] = args.listen
    if args.hub:
        cfg.data["role"] = "caller"
        cfg.data["hub"] = args.hub
    if args.agent:
        cfg.data["agent"] = args.agent
        spec = cfg.agents.setdefault(args.agent, {})
        if cfg.data.get("role") == "hub":
            spec["local"] = True
    if args.port:
        cfg.data["port"] = args.port
    if args.tailscale_cmd:
        cfg.data["tailscale_cmd"] = args.tailscale_cmd
    cfg.data.setdefault("policy", {"mode": "tailnet", "deny": []})
    cfg.data.setdefault("agents", {})
    print(json.dumps(cfg.data, ensure_ascii=False, indent=2))
    if args.apply:
        cfg.save()
        print("saved " + cfg.path, file=sys.stderr)
    else:
        print("(plan only; add --apply to write %s)" % cfg.path, file=sys.stderr)


def cmd_agent(args, cfg):
    """Hub owner: register or update a remote agent and its node binding / wake adapter."""
    spec = cfg.agents.setdefault(args.name, {})
    if args.node:
        spec["node"] = args.node
    if args.local:
        spec["local"] = True
    if args.wake_webhook:
        spec["wake"] = {"type": "webhook", "url_file": args.wake_webhook[0], "secret_file": args.wake_webhook[1]}
    if args.wake_exec:
        spec["wake"] = {"type": "exec", "argv": args.wake_exec}
    if args.wake_max:
        spec["wake_max"] = args.wake_max
    if args.remove:
        cfg.agents.pop(args.name, None)
    print(json.dumps(cfg.agents, ensure_ascii=False, indent=2))
    if args.apply:
        cfg.save()
    else:
        print("(plan only; add --apply)", file=sys.stderr)


def cmd_serve(args, cfg):
    if cfg.role != "hub":
        die("this machine is configured as a caller; run setup --hub-role first")
    serve(cfg, listen=args.listen, port=args.port, no_whois=args.no_whois)


def cmd_send(args, cfg):
    env = build_envelope(cfg, args.to, args.text, kind=args.kind, reply_to=args.reply_to, wake=args.wake,
                         from_agent=args.as_agent, data=json.loads(args.data) if args.data else None)
    code, resp = client_for(cfg).send(env)
    print(json.dumps({"status": code, **resp}, ensure_ascii=False))
    if code not in (200, 202):
        sys.exit(1)


def _print_messages(items, as_json):
    if as_json:
        print(json.dumps(items, ensure_ascii=False, indent=2))
        return
    if not items:
        print("(no pending messages)")
    for rec in items:
        e = rec["envelope"]
        head = "[%s] %s  from %s  kind=%s" % (e["id"], rec["received"]["at"], e["from_agent"], e.get("kind", "text"))
        if e.get("reply_to"):
            head += "  reply_to=" + e["reply_to"]
        print(head)
        if e.get("text"):
            print("    " + e["text"].replace("\n", "\n    "))
        if e.get("data") is not None:
            print("    data: " + json.dumps(e["data"], ensure_ascii=False))


def cmd_inbox(args, cfg):
    agent = args.as_agent or cfg.agent or die("no agent name")
    code, resp = client_for(cfg).inbox(agent)
    if code != 200:
        die(resp.get("error", "inbox failed"), 1)
    _print_messages(resp["messages"], args.json)


def cmd_wait(args, cfg):
    agent = args.as_agent or cfg.agent or die("no agent name")
    code, resp = client_for(cfg).wait(agent, args.timeout)
    if code != 200:
        die(resp.get("error", "wait failed"), 1)
    _print_messages(resp["messages"], args.json)
    sys.exit(0 if resp["messages"] else 4)


def cmd_ack(args, cfg):
    agent = args.as_agent or cfg.agent or die("no agent name")
    code, resp = client_for(cfg).ack(agent, args.ids)
    if code != 200:
        die(resp.get("error", "ack failed"), 1)
    print(json.dumps(resp, ensure_ascii=False))


def cmd_doctor(args, cfg):
    report = {"home": cfg.home, "config": os.path.exists(cfg.path), "role": cfg.role, "agent": cfg.agent,
              "python": platform.python_version(), "platform": platform.platform()}
    ts = Tailscale(cfg.tailscale_cmd)
    try:
        report["tailscale_ip"] = ts.self_ip()
        report["tailscale_name"] = ts.self_name()
    except Exception as e:
        report["tailscale_error"] = str(e)
    try:
        code, resp = client_for(cfg).health()
        report["hub_health"] = resp
    except SystemExit:
        report["hub_health"] = "unreachable"
    print(json.dumps(report, ensure_ascii=False, indent=2))


def main(argv=None):
    p = argparse.ArgumentParser(prog="khoomei", description=__doc__.split("\n\n")[1])
    p.add_argument("--home", help="data directory (default $KHOOMEI_HOME or ~/.khoomei)")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("setup", help="configure this machine as hub or caller (plan; --apply writes)")
    s.add_argument("--hub-role", action="store_true", help="this machine runs the hub (needs a listening tailnet IP)")
    s.add_argument("--hub", metavar="HOST[:PORT]", help="this machine is a caller of that hub")
    s.add_argument("--agent", help="this machine's own agent name")
    s.add_argument("--listen", help="hub: tailnet IP to bind, or 'auto'")
    s.add_argument("--port", type=int)
    s.add_argument("--tailscale-cmd")
    s.add_argument("--apply", action="store_true")
    s.set_defaults(fn=cmd_setup)

    s = sub.add_parser("agent", help="hub owner: register a remote agent, bind it to a node, set its wake adapter")
    s.add_argument("name")
    s.add_argument("--node", help="tailnet node name the agent connects from (short or FQDN)")
    s.add_argument("--local", action="store_true", help="agent lives on the hub machine")
    s.add_argument("--wake-webhook", nargs=2, metavar=("URL_FILE", "SECRET_FILE"))
    s.add_argument("--wake-exec", nargs="+", metavar="ARGV")
    s.add_argument("--wake-max", choices=WAKE_LEVELS)
    s.add_argument("--remove", action="store_true")
    s.add_argument("--apply", action="store_true")
    s.set_defaults(fn=cmd_agent)

    s = sub.add_parser("serve", help="run the hub")
    s.add_argument("--listen")
    s.add_argument("--port", type=int)
    s.add_argument("--no-whois", action="store_true", help="DEV ONLY: skip tailscale whois")
    s.set_defaults(fn=cmd_serve)

    s = sub.add_parser("send", help="send a message to an agent")
    s.add_argument("--to", required=True, metavar="AGENT")
    s.add_argument("--text")
    s.add_argument("--data", help="JSON payload")
    s.add_argument("--kind", default="text")
    s.add_argument("--reply-to", metavar="ID")
    s.add_argument("--wake", choices=WAKE_LEVELS, default="batch")
    s.add_argument("--as", dest="as_agent", metavar="AGENT")
    s.set_defaults(fn=cmd_send)

    for name, fn, help_ in (("inbox", cmd_inbox, "list pending messages"),
                            ("wait", cmd_wait, "long-poll until a message arrives (exit 4 on timeout)")):
        s = sub.add_parser(name, help=help_)
        s.add_argument("--as", dest="as_agent", metavar="AGENT")
        s.add_argument("--json", action="store_true")
        if name == "wait":
            s.add_argument("--timeout", type=int, default=50)
        s.set_defaults(fn=fn)

    s = sub.add_parser("ack", help="acknowledge handled messages")
    s.add_argument("ids", nargs="+")
    s.add_argument("--as", dest="as_agent", metavar="AGENT")
    s.set_defaults(fn=cmd_ack)

    s = sub.add_parser("doctor", help="read-only status report")
    s.set_defaults(fn=cmd_doctor)

    args = p.parse_args(argv)
    cfg = Config(home_dir(args))
    args.fn(args, cfg)


if __name__ == "__main__":
    main()
