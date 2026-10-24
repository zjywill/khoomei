"""End-to-end over loopback: a hub with two registered agents, a caller sending,
the hub-side agent replying, the caller waking from `wait`, and acknowledgement.

The hub runs with --no-whois because loopback has no Tailscale identity; the
authorization path for peers is exercised separately in test_hub_auth.
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, ROOT)
import khoomei  # noqa: E402

CLI = [sys.executable, os.path.join(ROOT, "khoomei.py")]


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class LoopbackTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.port = free_port()
        # The hub machine is "grok"; "muse" is a remote caller bound to a fake node.
        self.hub_home = os.path.join(self.tmp.name, "hub")
        self.caller_home = os.path.join(self.tmp.name, "caller")
        self.run_cli(self.hub_home, "setup", "--hub-role", "--agent", "grok", "--port", str(self.port),
                     "--listen", "127.0.0.1", "--apply")
        self.run_cli(self.hub_home, "agent", "muse", "--node", "muse-vm", "--apply")
        # Caller home: knows the hub address and its own name.
        self.run_cli(self.caller_home, "setup", "--hub", "127.0.0.1:%d" % self.port, "--agent", "muse", "--apply")

        cfg = khoomei.Config(self.hub_home)
        hub = khoomei.Hub(cfg, khoomei.Mailbox(self.hub_home), khoomei.Tailscale("tailscale"), no_whois=True)
        self.server = khoomei.HubServer(("127.0.0.1", self.port), hub)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def tearDown(self):
        self.server.shutdown()
        self.tmp.cleanup()

    def run_cli(self, home, *argv, check=True):
        # The caller poses as the node muse is bound to; the hub only honours this under --no-whois.
        env = dict(os.environ)
        if home == self.caller_home:
            env["KHOOMEI_DEV_PEER"] = "muse-vm.tail1234.ts.net"
        r = subprocess.run(CLI + ["--home", home, *argv], capture_output=True, text=True, env=env)
        if check and r.returncode != 0:
            self.fail("cli failed: %s\n%s\n%s" % (argv, r.stdout, r.stderr))
        return r

    def test_round_trip(self):
        r = self.run_cli(self.caller_home, "send", "--to", "grok", "--text", "大家在讨论什么？", "--wake", "now")
        sent = json.loads(r.stdout)
        self.assertEqual(sent["status"], 202)

        # Duplicate id is idempotent.
        env = khoomei.build_envelope(khoomei.Config(self.caller_home), "grok", "again")
        client = khoomei.Client("http://127.0.0.1:%d" % self.port, dev_peer="muse-vm.tail1234.ts.net")
        self.assertEqual(client.send(env)[0], 202)
        self.assertEqual(client.send(env)[0], 200)

        # Hub-side agent sees both, replies to the first, acks.
        r = self.run_cli(self.hub_home, "inbox", "--json")
        inbox = json.loads(r.stdout)
        self.assertEqual([m["envelope"]["text"] for m in inbox], ["大家在讨论什么？", "again"])
        self.run_cli(self.hub_home, "send", "--to", "muse", "--text", "三件事，见 data", "--reply-to", sent["id"],
                     "--data", json.dumps({"items": 3}))
        r = self.run_cli(self.hub_home, "ack", sent["id"], env["id"])
        self.assertEqual(sorted(json.loads(r.stdout)["acked"]), sorted([sent["id"], env["id"]]))
        self.assertEqual(json.loads(self.run_cli(self.hub_home, "inbox", "--json").stdout), [])

        # Caller long-polls and gets the reply at once.
        r = self.run_cli(self.caller_home, "wait", "--timeout", "5", "--json")
        got = json.loads(r.stdout)
        self.assertEqual(len(got), 1)
        self.assertEqual(got[0]["envelope"]["reply_to"], sent["id"])
        self.assertEqual(got[0]["envelope"]["data"], {"items": 3})
        self.run_cli(self.caller_home, "ack", got[0]["envelope"]["id"])

        # Empty wait times out with exit code 4.
        r = self.run_cli(self.caller_home, "wait", "--timeout", "1", check=False)
        self.assertEqual(r.returncode, 4)

    def test_wait_wakes_on_arrival(self):
        client = khoomei.Client("http://127.0.0.1:%d" % self.port)  # hub owner, sends as grok
        muse = khoomei.Client("http://127.0.0.1:%d" % self.port, dev_peer="muse-vm.tail1234.ts.net")
        result = {}

        def waiter():
            result["resp"] = muse.wait("muse", 10)

        t = threading.Thread(target=waiter)
        t.start()
        time.sleep(0.3)
        t0 = time.time()
        client.send(khoomei.build_envelope(khoomei.Config(self.hub_home), "muse", "ping"))
        t.join(10)
        self.assertLess(time.time() - t0, 3, "wait should return as soon as a message lands")
        self.assertEqual(result["resp"][1]["messages"][0]["envelope"]["text"], "ping")

    def test_validation(self):
        client = khoomei.Client("http://127.0.0.1:%d" % self.port)
        env = khoomei.build_envelope(khoomei.Config(self.hub_home), "muse", "x")
        env["extra"] = 1
        code, resp = client.send(env)
        self.assertEqual(code, 400)
        self.assertIn("unknown fields", resp["error"])
        env = khoomei.build_envelope(khoomei.Config(self.hub_home), "nobody", "x")
        self.assertEqual(client.send(env)[0], 400)


class HubAuthTest(unittest.TestCase):
    """Peer authorization without a network: drive Hub.authorize_agent directly."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        cfg = khoomei.Config(self.tmp.name)
        cfg.data = {"role": "hub", "agent": "grok", "agents": {
            "grok": {"local": True},
            "muse": {"node": "muse-vm.tail1234.ts.net"},
            "codex": {"node": "laptop"},
        }}
        self.hub = khoomei.Hub(cfg, khoomei.Mailbox(self.tmp.name), None)

    def tearDown(self):
        self.tmp.cleanup()

    def test_peer_bound_to_node(self):
        muse = ("peer", "muse-vm.tail1234.ts.net")
        self.assertTrue(self.hub.authorize_agent(muse, "muse"))
        with self.assertRaises(PermissionError):
            self.hub.authorize_agent(muse, "grok")  # a caller may not act as the hub's own agent
        with self.assertRaises(PermissionError):
            self.hub.authorize_agent(muse, "codex")  # nor as another caller
        with self.assertRaises(PermissionError):
            self.hub.authorize_agent(muse, "stranger")
        # Short name binding matches the FQDN.
        self.assertTrue(self.hub.authorize_agent(("peer", "laptop.tail1234.ts.net"), "codex"))

    def test_loopback_is_owner(self):
        local = ("local", None)
        self.assertTrue(self.hub.authorize_agent(local, "grok"))
        with self.assertRaises(PermissionError):
            self.hub.authorize_agent(local, "muse")  # owner may not impersonate a remote agent

    def test_identify_rejects_non_tailnet(self):
        with self.assertRaises(PermissionError):
            self.hub.identify("8.8.8.8")
        self.assertEqual(self.hub.identify("127.0.0.1"), ("local", None))

    def test_wake_rate_limit(self):
        waker = khoomei.Waker(self.hub.cfg)
        env = {"from_agent": "muse", "to_agent": "grok", "wake": "now"}
        levels = [waker.effective_level(env) for _ in range(8)]
        self.assertEqual(levels, ["now"] * 6 + ["batch"] * 2)


if __name__ == "__main__":
    unittest.main()
