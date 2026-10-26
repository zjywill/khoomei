// khoomei: one low drone, many overtones.
//
// A hub-and-caller messaging layer for personal AI agents on a Tailscale network, with the
// tailnet node embedded in the binary (tsnet): no tailscaled, no TUN device, no root.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"tailscale.com/tsnet"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/mcp"
	"github.com/zjywill/khoomei/internal/panel"
	"github.com/zjywill/khoomei/internal/protocol"
	"github.com/zjywill/khoomei/internal/watch"
)

const usage = `khoomei: one low drone, many overtones.

Usage: khoomei [--home DIR] COMMAND [flags]

Commands:
  setup    configure this machine as hub or caller (prints a plan; --apply writes)
  agent    hub owner: register an agent (NAME or OWNER/NAME), bind it to a tailnet node, set its wake adapter
  owner    hub owner: register another person's handle and Tailscale login so their agents may connect
  prune    hub owner: delete acked (or, for one own agent, pending) records older than a cutoff
  panel-password  hub owner: set, reset (forgotten) or --clear the web panel's password
  serve    run the hub as an embedded tailnet node
  send     send a message to an agent
  inbox    list pending messages
  wait     long-poll until a message arrives (exit 4 on timeout)
  ack      acknowledge handled messages
  fetch    download the files attached to a pending message
  watch    long-poll in a loop and run a local command when messages arrive (caller-side wake)
  mcp      serve the four tools over stdio for MCP hosts such as Claude Code and Codex
  doctor   read-only status report
  version  print the version

Data directory: --home, $KHOOMEI_HOME, or ~/.khoomei.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			os.Exit(int(ec))
		}
		fmt.Fprintln(os.Stderr, "khoomei:", err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func run(args []string) error {
	global := flag.NewFlagSet("khoomei", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	home := global.String("home", "", "data directory")
	if err := global.Parse(args); err != nil {
		return exitCode(2)
	}
	rest := global.Args()
	if len(rest) == 0 {
		global.Usage()
		return exitCode(2)
	}
	cfg, err := config.Load(resolveHome(*home))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmds := map[string]func(context.Context, *config.Config, []string) error{
		"setup": cmdSetup, "agent": cmdAgent, "serve": cmdServe, "send": cmdSend,
		"inbox": cmdInbox, "wait": cmdWait, "ack": cmdAck, "doctor": cmdDoctor,
		"watch": cmdWatch, "mcp": cmdMCP, "owner": cmdOwner, "prune": cmdPrune, "fetch": cmdFetch,
		"panel-password": cmdPanelPassword,
		"version":        func(context.Context, *config.Config, []string) error { fmt.Println("khoomei", hub.Version); return nil },
	}
	fn, ok := cmds[rest[0]]
	if !ok {
		global.Usage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
	return fn(ctx, cfg, rest[1:])
}

// nameFirst lets a subcommand take its positional NAME before its flags ("agent muse --node x"):
// Go's flag package stops at the first non-flag, so the leading non-flag is moved to the end.
func nameFirst(args []string) []string {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return append(append([]string{}, args[1:]...), args[0])
	}
	return args
}

func resolveHome(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("KHOOMEI_HOME"); env != "" {
		return env
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".khoomei")
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// --------------------------------------------------------------------------- tsnet

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", args...)
}

// newNode builds the embedded tailnet node. The auth key, if any, is read from a file the user
// wrote; without one tsnet prints a login URL for the user to open.
func newNode(cfg *config.Config) *tsnet.Server {
	s := &tsnet.Server{Dir: cfg.TSNetDir(), Hostname: cfg.TSNetHostname(), UserLogf: logf}
	if os.Getenv("KHOOMEI_DEBUG") == "" {
		s.Logf = func(string, ...any) {}
	}
	if cfg.TSNet.AuthKeyFile != "" {
		if b, err := os.ReadFile(cfg.TSNet.AuthKeyFile); err == nil {
			s.AuthKey = strings.TrimSpace(string(b))
		}
	}
	return s
}

// whois resolves a tailnet address through the embedded node's local client.
type whois struct{ s *tsnet.Server }

func (w whois) Resolve(ctx context.Context, ip netip.Addr) (hub.Peer, error) {
	lc, err := w.s.LocalClient()
	if err != nil {
		return hub.Peer{}, hub.ErrUnavailable
	}
	resp, err := lc.WhoIs(ctx, ip.String())
	if err != nil {
		if ctx.Err() != nil {
			return hub.Peer{}, hub.ErrUnavailable
		}
		return hub.Peer{}, err
	}
	if resp.Node == nil {
		return hub.Peer{}, errors.New("no node")
	}
	peer := hub.Peer{Node: resp.Node.Name}
	if resp.UserProfile != nil {
		peer.Login = resp.UserProfile.LoginName
	}
	return peer, nil
}

// --------------------------------------------------------------------------- commands

func cmdSetup(_ context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	hubRole := fs.Bool("hub-role", false, "this machine runs the hub")
	hubAddr := fs.String("hub", "", "this machine is a caller of that hub (HOST[:PORT])")
	agent := fs.String("agent", "", "this machine's own agent name")
	owner := fs.String("owner", "", "this machine's owner handle; bare agent names qualify to it (owner/agent)")
	transport := fs.String("transport", "", "caller transport: direct (host routes to the tailnet) or tsnet (own embedded node)")
	port := fs.Int("port", 0, "hub port")
	hostname := fs.String("hostname", "", "embedded node name on the tailnet")
	authKey := fs.String("auth-key-file", "", "file holding a Tailscale auth key for the embedded node")
	apply := fs.Bool("apply", false, "write the config")
	fs.Parse(args)

	if *hubRole {
		cfg.Role = config.RoleHub
	}
	if *hubAddr != "" {
		cfg.Role, cfg.Hub = config.RoleCaller, *hubAddr
	}
	if *owner != "" {
		if !protocol.ValidAgent(*owner) {
			return errors.New("bad owner handle")
		}
		cfg.Owner = *owner
	}
	if *agent != "" {
		if !protocol.ValidAgent(*agent) {
			return errors.New("bad agent name (one part; the owner comes from --owner)")
		}
		cfg.Agent = *agent
		if cfg.Role == config.RoleHub {
			cfg.Spec(*agent).Local = true
		}
	}
	if *transport != "" {
		if *transport != config.TransportDirect && *transport != config.TransportTSNet {
			return errors.New("transport must be direct or tsnet")
		}
		cfg.Transport = *transport
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *hostname != "" {
		cfg.TSNet.Hostname = *hostname
	}
	if *authKey != "" {
		cfg.TSNet.AuthKeyFile = *authKey
	}
	fmt.Println(cfg.Pretty())
	if *apply {
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "saved", cfg.Path)
	} else {
		fmt.Fprintf(os.Stderr, "(plan only; add --apply to write %s)\n", cfg.Path)
	}
	return nil
}

func cmdAgent(_ context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	node := fs.String("node", "", "tailnet node the agent connects from (short name or FQDN)")
	local := fs.Bool("local", false, "agent lives on the hub machine")
	webhookURL := fs.String("wake-webhook-url-file", "", "file holding the wake webhook URL")
	webhookSecret := fs.String("wake-webhook-secret-file", "", "file holding the webhook bearer secret")
	wakeExec := fs.String("wake-exec", "", "program to run on wake (space-separated argv)")
	wakeMax := fs.String("wake-max", "", "cap: none | batch | now")
	remove := fs.Bool("remove", false, "unregister the agent")
	apply := fs.Bool("apply", false, "write the config")
	fs.Parse(nameFirst(args))
	if fs.NArg() != 1 || !protocol.ValidAddress(fs.Arg(0)) {
		return errors.New("usage: khoomei agent NAME|OWNER/NAME [flags]")
	}
	name := protocol.Qualify(fs.Arg(0), cfg.Owner)
	if o, _, _ := protocol.SplitAddress(name); o != "" && o != cfg.Owner && cfg.Owners[o] == nil {
		return fmt.Errorf("owner %s is not registered; run: khoomei owner %s --login LOGIN --apply", o, o)
	}
	if *remove {
		delete(cfg.Agents, name)
	} else {
		spec := cfg.Spec(name)
		if *node != "" {
			spec.Node = *node
		}
		if *local {
			spec.Local = true
		}
		if *webhookURL != "" {
			spec.Wake = &config.WakeAdapter{Type: "webhook", URLFile: *webhookURL, SecretFile: *webhookSecret}
		}
		if *wakeExec != "" {
			spec.Wake = &config.WakeAdapter{Type: "exec", Argv: strings.Fields(*wakeExec)}
		}
		if *wakeMax != "" {
			if protocol.WakeRank(*wakeMax) < 0 {
				return errors.New("wake-max must be none, batch or now")
			}
			spec.WakeMax = *wakeMax
		}
	}
	printJSON(cfg.Agents)
	if *apply {
		return cfg.Save()
	}
	fmt.Fprintln(os.Stderr, "(plan only; add --apply)")
	return nil
}

func cmdOwner(_ context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("owner", flag.ExitOnError)
	login := fs.String("login", "", "the person's Tailscale login as WhoIs reports it (alice@github, bob@example.com)")
	remove := fs.Bool("remove", false, "unregister the owner (their agents stay registered but can no longer connect)")
	apply := fs.Bool("apply", false, "write the config")
	fs.Parse(nameFirst(args))
	if fs.NArg() != 1 || !protocol.ValidAgent(fs.Arg(0)) {
		return errors.New("usage: khoomei owner HANDLE --login LOGIN [--apply]")
	}
	handle := fs.Arg(0)
	if cfg.Owners == nil {
		cfg.Owners = map[string]*config.OwnerSpec{}
	}
	if *remove {
		delete(cfg.Owners, handle)
	} else {
		if *login == "" && cfg.Owners[handle] == nil {
			return errors.New("--login is required for a new owner")
		}
		if *login != "" {
			cfg.Owners[handle] = &config.OwnerSpec{Login: strings.ToLower(*login)}
		}
	}
	printJSON(cfg.Owners)
	if *apply {
		return cfg.Save()
	}
	fmt.Fprintln(os.Stderr, "(plan only; add --apply)")
	return nil
}

// cmdPanelPassword writes <home>/panel_password. The running hub reads that file on every request,
// so the change takes effect at once and logs out every browser; no restart.
func cmdPanelPassword(_ context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("panel-password", flag.ExitOnError)
	clearPw := fs.Bool("clear", false, "remove the password: the panel is open to every node that reaches the hub again")
	fs.Parse(args)
	if cfg.Role != config.RoleHub {
		return errors.New("panel-password runs on the hub")
	}
	if *clearPw {
		if err := panel.ClearPassword(cfg.Home); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "panel password removed; the panel is open to every node that reaches the hub")
		return nil
	}
	pw, err := readNewPassword()
	if err != nil {
		return err
	}
	if err := panel.SetPassword(cfg.Home, pw); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "panel password set; every browser has to log in again")
	return nil
}

// readNewPassword asks twice without echo on a terminal, or reads one line from a pipe.
func readNewPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on stdin")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	pw, err := ask(fmt.Sprintf("new panel password (at least %d characters): ", panel.MinPassword))
	if err != nil {
		return "", err
	}
	again, err := ask("again: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("the two passwords differ")
	}
	return pw, nil
}

func cmdPrune(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	older := fs.String("older-than", "24h", "age cutoff: 36h, 7d …; anything under 24h counts as 24h")
	agent := fs.String("agent", "", "only records addressed to this agent")
	pending := fs.Bool("pending", false, "delete unacknowledged records instead (needs --agent, one of your own)")
	apply := fs.Bool("apply", false, "delete; without it only count")
	fs.Parse(args)
	if cfg.Role != config.RoleHub {
		return errors.New("prune runs on the hub")
	}
	hours, err := parseAge(*older)
	if err != nil {
		return err
	}
	what := "acked"
	if *pending {
		what = "pending"
	}
	c, closeNode, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeNode()
	resp, err := c.Prune(ctx, panel.PruneRequest{Agent: *agent, What: what, OlderThanHours: hours, DryRun: !*apply})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("prune: %s (%d)", resp.Error, resp.Status)
	}
	verb := "would delete"
	if !resp.DryRun {
		verb = "deleted"
	}
	fmt.Printf("%s %d records, %.1f MB\n", verb, resp.Count, float64(resp.Bytes)/(1<<20))
	if resp.DryRun {
		fmt.Fprintln(os.Stderr, "(dry run; add --apply)")
	}
	return nil
}

// parseAge reads "36h" or "7d" as whole hours.
func parseAge(s string) (int, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad age %q", s)
		}
		return n * 24, nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil || dur < 0 {
		return 0, fmt.Errorf("bad age %q: use 36h or 7d", s)
	}
	return int(dur.Hours()), nil
}

func cmdServe(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", cfg.Port, "port to listen on")
	fs.Parse(args)
	if cfg.Role != config.RoleHub {
		return errors.New("this machine is configured as a caller; run setup --hub-role first")
	}
	if cfg.Owner == "" {
		logf("no owner handle set; running in single-owner mode (setup --owner HANDLE enables owner/agent addresses)")
	}

	node := newNode(cfg)
	defer node.Close()
	logf("bringing up tailnet node %q (state in %s)", node.Hostname, node.Dir)
	status, err := node.Up(ctx)
	if err != nil {
		return fmt.Errorf("tailnet: %w", err)
	}
	box := mailbox.New(cfg.Home)
	h := hub.New(cfg, box, whois{node}, logf)
	pan, err := panel.New(cfg, box, h.Blobs, logf)
	if err != nil {
		return fmt.Errorf("panel index: %w", err)
	}
	pan.IsOwner = h.IsOwner
	h.Observer, h.Admin = pan, pan.Handler()
	go pan.Run(ctx)
	if status.Self != nil {
		if u, ok := status.User[status.Self.UserID]; ok {
			h.SelfLogin = u.LoginName
		}
	}
	if fqdn := strings.TrimSuffix(status.Self.DNSName, "."); fqdn != "" {
		h.Hosts = append(h.Hosts, fqdn, strings.SplitN(fqdn, ".", 2)[0])
	}
	for _, ip := range status.TailscaleIPs {
		h.Hosts = append(h.Hosts, ip.String())
	}
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}

	tsLn, err := node.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		return err
	}
	loLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	for _, ip := range status.TailscaleIPs {
		logf("hub %s listening on %s:%d (%s)", hub.Version, ip, *port, strings.TrimSuffix(status.Self.DNSName, "."))
	}
	logf("owner CLI on 127.0.0.1:%d", *port)
	go srv.Serve(tsLn)
	go srv.Serve(loLn)
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// hubClient returns a client for this machine's role: loopback on the hub, the configured
// transport on a caller. The returned closer tears down an embedded node, if one was started.
func hubClient(ctx context.Context, cfg *config.Config) (*client.Client, func(), error) {
	noop := func() {}
	if cfg.Role == config.RoleHub {
		return client.New(fmt.Sprintf("http://127.0.0.1:%d", cfg.Port), nil), noop, nil
	}
	addr, err := cfg.HubAddr()
	if err != nil {
		return nil, noop, err
	}
	if cfg.Transport != config.TransportTSNet {
		return client.New("http://"+addr, nil), noop, nil
	}
	node := newNode(cfg)
	if _, err := node.Up(ctx); err != nil {
		node.Close()
		return nil, noop, fmt.Errorf("tailnet: %w", err)
	}
	// Up returns as soon as the restored node is Running, before the netmap and MagicDNS are
	// usable, so a dial by name can hang. Wait for the hub to show up as a peer and dial its IP.
	host, port, _ := net.SplitHostPort(addr)
	target := addr
	if net.ParseIP(host) == nil {
		ip, err := waitForPeer(ctx, node, host, 45*time.Second)
		if err != nil {
			node.Close()
			return nil, noop, fmt.Errorf("tailnet: %w", err)
		}
		target = net.JoinHostPort(ip, port)
		if os.Getenv("KHOOMEI_DEBUG") != "" {
			logf("hub %s is peer %s", host, ip)
		}
	}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) { return node.Dial(ctx, network, target) }
	hc := &http.Client{Transport: &http.Transport{DialContext: dial}}
	return client.New("http://"+addr, hc), func() { node.Close() }, nil
}

// waitForPeer polls the embedded node's status until a peer named host (short name or FQDN)
// appears, and returns its tailnet IPv4.
func waitForPeer(ctx context.Context, node *tsnet.Server, host string, timeout time.Duration) (string, error) {
	lc, err := node.LocalClient()
	if err != nil {
		return "", err
	}
	want := strings.ToLower(strings.TrimSuffix(host, "."))
	deadline := time.Now().Add(timeout)
	for {
		st, err := lc.Status(ctx)
		if err == nil {
			for _, p := range st.Peer {
				dns := strings.ToLower(strings.TrimSuffix(p.DNSName, "."))
				if strings.EqualFold(p.HostName, want) || dns == want || strings.HasPrefix(dns, want+".") {
					for _, ip := range p.TailscaleIPs {
						if ip.Is4() {
							return ip.String(), nil
						}
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("hub %q not seen on the tailnet within %s (is the hub running, and is this node allowed to reach it?)", host, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// agentName is the address this command acts as: --as (qualified with this machine's owner when bare),
// else the configured owner/agent.
func agentName(cfg *config.Config, override string) (string, error) {
	if override != "" {
		if !protocol.ValidAddress(override) {
			return "", errors.New("bad --as address")
		}
		return protocol.Qualify(override, cfg.Owner), nil
	}
	if cfg.Agent == "" {
		return "", errors.New("no agent name: pass --as NAME or set one with setup --agent NAME")
	}
	return cfg.SelfAddress(), nil
}

func cmdSend(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "", "recipient agent")
	text := fs.String("text", "", "message body")
	data := fs.String("data", "", "JSON payload")
	kind := fs.String("kind", "text", "short label: text, request, reply, notice")
	replyTo := fs.String("reply-to", "", "id of the message this answers")
	wake := fs.String("wake", protocol.WakeBatch, "requested wake level: now | batch | none")
	as := fs.String("as", "", "send as this agent")
	var files []string
	fs.Func("file", "attach a local file (repeatable)", func(p string) error { files = append(files, p); return nil })
	fs.Parse(args)
	if *to == "" || !protocol.ValidAddress(*to) {
		return errors.New("--to is required and must be NAME or OWNER/NAME")
	}
	if strings.TrimSpace(*text) == "" && *data == "" && len(files) == 0 {
		return errors.New("--text, --data or --file is required; the hub refuses empty messages")
	}
	if len(files) > protocol.MaxFiles {
		return fmt.Errorf("at most %d files per message", protocol.MaxFiles)
	}
	from, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	env := protocol.New(from, *to, *text)
	env.Kind, env.ReplyTo, env.Wake = *kind, *replyTo, *wake
	if *data != "" {
		if !json.Valid([]byte(*data)) {
			return errors.New("--data is not valid JSON")
		}
		env.Data = json.RawMessage(*data)
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	if len(files) > 0 {
		// Upload everything first; a message is only sent once all its files are on the hub.
		if err := c.RequireBlobs(ctx); err != nil {
			return err
		}
		for _, p := range files {
			ref, err := c.UploadFile(ctx, from, p)
			if err != nil {
				return err
			}
			env.Files = append(env.Files, ref)
		}
	}
	if err := env.Validate(); err != nil {
		return err
	}
	resp, err := c.Send(ctx, env)
	if err != nil {
		return err
	}
	printJSON(resp)
	if resp.Status != 200 && resp.Status != 202 {
		return exitCode(1)
	}
	return nil
}

func printMessages(items []protocol.Record, asJSON bool) {
	if asJSON {
		printJSON(items)
		return
	}
	if len(items) == 0 {
		fmt.Println("(no pending messages)")
	}
	for _, rec := range items {
		e := rec.Envelope
		head := fmt.Sprintf("[%s] %s  from %s  kind=%s", e.ID, rec.Received.At, e.FromAgent, e.EffectiveKind())
		if e.ReplyTo != "" {
			head += "  reply_to=" + e.ReplyTo
		}
		fmt.Println(head)
		if e.Text != "" {
			fmt.Println("    " + strings.ReplaceAll(e.Text, "\n", "\n    "))
		}
		if len(e.Data) > 0 {
			fmt.Println("    data: " + string(e.Data))
		}
		for _, f := range e.Files {
			fmt.Printf("    file: %s (%d bytes, sha256 %s…)  →  khoomei fetch %s\n", f.Name, f.Size, f.SHA256[:12], e.ID)
		}
	}
}

func cmdInbox(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ExitOnError)
	as := fs.String("as", "", "agent whose inbox to read")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	agent, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	resp, err := c.Inbox(ctx, agent)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return errors.New(resp.Error)
	}
	printMessages(resp.Messages, *asJSON)
	return nil
}

func cmdWait(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	as := fs.String("as", "", "agent whose inbox to wait on")
	asJSON := fs.Bool("json", false, "print JSON")
	timeout := fs.Int("timeout", 50, "seconds to wait (max 120)")
	fs.Parse(args)
	agent, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	resp, err := c.Wait(ctx, agent, time.Duration(*timeout)*time.Second)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return errors.New(resp.Error)
	}
	printMessages(resp.Messages, *asJSON)
	if len(resp.Messages) == 0 {
		return exitCode(4)
	}
	return nil
}

func cmdAck(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("ack", flag.ExitOnError)
	as := fs.String("as", "", "agent acknowledging")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("usage: khoomei ack ID [ID ...]")
	}
	agent, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	resp, err := c.Ack(ctx, agent, fs.Args())
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return errors.New(resp.Error)
	}
	printJSON(resp)
	return nil
}

func cmdFetch(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	as := fs.String("as", "", "agent whose message it is")
	out := fs.String("out", "", "directory to save into (default $KHOOMEI_HOME/files/<id>); with --sha256, the file to write")
	sha := fs.String("sha256", "", "fetch one file by hash instead of a pending message's attachments")
	fs.Parse(nameFirst(args))
	agent, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	if *sha != "" {
		if *out == "" || !protocol.ValidSHA256(*sha) {
			return errors.New("usage: khoomei fetch --sha256 HASH --out FILE")
		}
		paths, err := c.FetchFiles(ctx, agent, []protocol.FileRef{{SHA256: *sha, Name: filepath.Base(*out), Size: -1}}, filepath.Dir(*out))
		if err != nil {
			return err
		}
		printJSON(map[string]any{"ok": true, "files": paths})
		return nil
	}
	if fs.NArg() != 1 {
		return errors.New("usage: khoomei fetch MESSAGE_ID [--out DIR]")
	}
	id := fs.Arg(0)
	resp, err := c.Inbox(ctx, agent)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return errors.New(resp.Error)
	}
	for _, rec := range resp.Messages {
		if rec.Envelope.ID != id {
			continue
		}
		if len(rec.Envelope.Files) == 0 {
			return fmt.Errorf("message %s has no files", id)
		}
		dir := *out
		if dir == "" {
			root := filepath.Join(cfg.Home, "files")
			client.CleanFetched(root, client.KeepFetched)
			dir = filepath.Join(root, client.SafeName(id))
		}
		paths, err := c.FetchFiles(ctx, agent, rec.Envelope.Files, dir)
		if err != nil {
			return err
		}
		printJSON(map[string]any{"ok": true, "files": paths})
		return nil
	}
	return fmt.Errorf("message %s is not in %s's inbox; fetch before you ack, or use --sha256", id, agent)
}

func cmdWatch(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	as := fs.String("as", "", "agent whose inbox to watch")
	command := fs.String("exec", "", "shell command to run when new messages arrive (they arrive as JSON on stdin)")
	poll := fs.Int("poll", 50, "seconds per long-poll (max 120)")
	repeat := fs.Duration("repeat", 10*time.Minute, "re-run the command for messages still unacknowledged after this long")
	fs.Parse(args)
	if *command == "" {
		return errors.New("--exec is required")
	}
	agent, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	logf("watching inbox of %s; handler: %s", agent, *command)
	w := &watch.Watcher{Client: c, Agent: agent, Command: *command, Poll: time.Duration(*poll) * time.Second, Repeat: *repeat, Logf: logf}
	return w.Run(ctx)
}

func cmdMCP(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	as := fs.String("as", "", "agent this server speaks as")
	fs.Parse(args)
	agent, err := agentName(cfg, *as)
	if err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	s := &mcp.Server{Client: c, Agent: agent, Version: hub.Version, FilesDir: filepath.Join(cfg.Home, "files")}
	return s.Serve(ctx, os.Stdin, os.Stdout)
}

func cmdDoctor(ctx context.Context, cfg *config.Config, _ []string) error {
	report := map[string]any{
		"home": cfg.Home, "config": cfg.Exists(), "role": cfg.Role, "owner": cfg.Owner, "agent": cfg.Agent, "address": cfg.SelfAddress(),
		"transport": cfg.Transport, "tsnet_dir": cfg.TSNetDir(), "tsnet_hostname": cfg.TSNetHostname(),
		"version": hub.Version, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
	}
	if cfg.Role == config.RoleCaller {
		if addr, err := cfg.HubAddr(); err == nil {
			report["hub"] = addr
		}
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		report["hub_health"] = err.Error()
	} else {
		defer done()
		if resp, err := c.Health(ctx); err != nil {
			report["hub_health"] = "unreachable: " + err.Error()
		} else {
			report["hub_health"] = resp
		}
	}
	printJSON(report)
	return nil
}
