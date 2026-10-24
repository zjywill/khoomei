// khoomei: one low drone, many overtones.
//
// A hub-and-caller messaging layer for personal AI agents on a Tailscale network, with the
// tailnet node embedded in the binary (tsnet): no tailscaled, no TUN device, no root.
package main

import (
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
	"strings"
	"syscall"
	"time"

	"tailscale.com/tsnet"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

const usage = `khoomei: one low drone, many overtones.

Usage: khoomei [--home DIR] COMMAND [flags]

Commands:
  setup    configure this machine as hub or caller (prints a plan; --apply writes)
  agent    hub owner: register an agent, bind it to a tailnet node, set its wake adapter
  serve    run the hub as an embedded tailnet node
  send     send a message to an agent
  inbox    list pending messages
  wait     long-poll until a message arrives (exit 4 on timeout)
  ack      acknowledge handled messages
  doctor   read-only status report

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
	}
	fn, ok := cmds[rest[0]]
	if !ok {
		global.Usage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
	return fn(ctx, cfg, rest[1:])
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

func (w whois) Resolve(ctx context.Context, ip netip.Addr) (string, error) {
	lc, err := w.s.LocalClient()
	if err != nil {
		return "", hub.ErrUnavailable
	}
	resp, err := lc.WhoIs(ctx, ip.String())
	if err != nil {
		if ctx.Err() != nil {
			return "", hub.ErrUnavailable
		}
		return "", err
	}
	if resp.Node == nil {
		return "", errors.New("no node")
	}
	return resp.Node.Name, nil
}

// --------------------------------------------------------------------------- commands

func cmdSetup(_ context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	hubRole := fs.Bool("hub-role", false, "this machine runs the hub")
	hubAddr := fs.String("hub", "", "this machine is a caller of that hub (HOST[:PORT])")
	agent := fs.String("agent", "", "this machine's own agent name")
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
	if *agent != "" {
		if !protocol.ValidAgent(*agent) {
			return errors.New("bad agent name")
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
	fs.Parse(args)
	if fs.NArg() != 1 || !protocol.ValidAgent(fs.Arg(0)) {
		return errors.New("usage: khoomei agent NAME [flags]")
	}
	name := fs.Arg(0)
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

func cmdServe(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", cfg.Port, "port to listen on")
	fs.Parse(args)
	if cfg.Role != config.RoleHub {
		return errors.New("this machine is configured as a caller; run setup --hub-role first")
	}

	node := newNode(cfg)
	defer node.Close()
	logf("bringing up tailnet node %q (state in %s)", node.Hostname, node.Dir)
	status, err := node.Up(ctx)
	if err != nil {
		return fmt.Errorf("tailnet: %w", err)
	}
	h := hub.New(cfg, mailbox.New(cfg.Home), whois{node}, logf)
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
	hc := &http.Client{Transport: &http.Transport{DialContext: node.Dial}}
	return client.New("http://"+addr, hc), func() { node.Close() }, nil
}

func agentName(cfg *config.Config, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if cfg.Agent == "" {
		return "", errors.New("no agent name: pass --as NAME or set one with setup --agent NAME")
	}
	return cfg.Agent, nil
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
	fs.Parse(args)
	if *to == "" {
		return errors.New("--to is required")
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
	if err := env.Validate(); err != nil {
		return err
	}
	c, done, err := hubClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
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

func cmdDoctor(ctx context.Context, cfg *config.Config, _ []string) error {
	report := map[string]any{
		"home": cfg.Home, "config": cfg.Exists(), "role": cfg.Role, "agent": cfg.Agent,
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
