package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

const museNode = "muse-vm.tail1234.ts.net"

// peerHeader lets tests pose as a tailnet node; production hubs never read it.
const peerHeader = "X-Test-Peer"

func newTestHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.Agent = config.RoleHub, "grok"
	cfg.Agents = map[string]*config.AgentSpec{
		"grok":  {Local: true},
		"muse":  {Node: museNode},
		"codex": {Node: "laptop"},
	}
	h := New(cfg, mailbox.New(cfg.Home), nil, t.Logf)
	h.Identify = func(r *http.Request) (Identity, error) {
		if p := r.Header.Get(peerHeader); p != "" {
			return Identity{Node: p}, nil
		}
		return Identity{Local: true}, nil
	}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return h, srv
}

type peerTransport struct{ node string }

func (p peerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(peerHeader, p.node)
	return http.DefaultTransport.RoundTrip(r)
}

func asPeer(srv *httptest.Server, node string) *client.Client {
	return client.New(srv.URL, &http.Client{Transport: peerTransport{node}})
}

func TestRoundTrip(t *testing.T) {
	_, srv := newTestHub(t)
	ctx := context.Background()
	muse := asPeer(srv, museNode)
	owner := client.New(srv.URL, nil)

	// Muse asks Grok, waking it.
	ask := protocol.New("muse", "grok", "大家在讨论什么？")
	ask.Wake = protocol.WakeNow
	resp, err := muse.Send(ctx, ask)
	if err != nil || resp.Status != 202 {
		t.Fatalf("send: %v %+v", err, resp)
	}
	// Same id again is idempotent.
	if resp, _ = muse.Send(ctx, ask); resp.Status != 200 || !resp.Duplicate {
		t.Fatalf("duplicate: %+v", resp)
	}
	again := protocol.New("muse", "grok", "again")
	if resp, _ = muse.Send(ctx, again); resp.Status != 202 {
		t.Fatalf("second send: %+v", resp)
	}

	// Grok (owner, loopback) reads both in order, replies, acks.
	resp, _ = owner.Inbox(ctx, "grok")
	if got := texts(resp.Messages); strings.Join(got, "|") != "大家在讨论什么？|again" {
		t.Fatalf("inbox order: %v", got)
	}
	reply := protocol.New("grok", "muse", "三件事，见 data")
	reply.ReplyTo, reply.Data = ask.ID, json.RawMessage(`{"items":3}`)
	if resp, _ = owner.Send(ctx, reply); resp.Status != 202 {
		t.Fatalf("reply: %+v", resp)
	}
	resp, _ = owner.Ack(ctx, "grok", []string{ask.ID, again.ID})
	if len(resp.Acked) != 2 {
		t.Fatalf("ack: %+v", resp)
	}
	if resp, _ = owner.Inbox(ctx, "grok"); len(resp.Messages) != 0 {
		t.Fatalf("inbox not empty after ack: %+v", resp.Messages)
	}

	// Muse waits and gets the reply at once.
	resp, _ = muse.Wait(ctx, "muse", 5*time.Second)
	if len(resp.Messages) != 1 || resp.Messages[0].Envelope.ReplyTo != ask.ID || string(resp.Messages[0].Envelope.Data) != `{"items":3}` {
		t.Fatalf("wait: %+v", resp)
	}
	if resp.Messages[0].Received.Peer != "hub" {
		t.Fatalf("peer recorded as %q", resp.Messages[0].Received.Peer)
	}
	muse.Ack(ctx, "muse", []string{resp.Messages[0].Envelope.ID})
	if resp, _ = muse.Wait(ctx, "muse", time.Second); len(resp.Messages) != 0 {
		t.Fatalf("expected empty wait: %+v", resp)
	}
}

func TestWaitWakesOnArrival(t *testing.T) {
	_, srv := newTestHub(t)
	ctx := context.Background()
	muse := asPeer(srv, museNode)
	owner := client.New(srv.URL, nil)

	got := make(chan *client.Response, 1)
	go func() {
		r, _ := muse.Wait(ctx, "muse", 10*time.Second)
		got <- r
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	owner.Send(ctx, protocol.New("grok", "muse", "ping"))
	select {
	case r := <-got:
		if time.Since(start) > 2*time.Second || len(r.Messages) != 1 || r.Messages[0].Envelope.Text != "ping" {
			t.Fatalf("wait did not return promptly with the message: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait never returned")
	}
}

func TestAuthorization(t *testing.T) {
	h, srv := newTestHub(t)
	ctx := context.Background()
	muse := asPeer(srv, museNode)

	// A caller may not act as the hub's own agent, another caller, or a stranger.
	for _, as := range []string{"grok", "codex", "stranger"} {
		env := protocol.New(as, "grok", "x")
		if resp, _ := muse.Send(ctx, env); resp.Status != 403 {
			t.Errorf("send as %s: want 403, got %d", as, resp.Status)
		}
		if resp, _ := muse.Inbox(ctx, as); resp.Status != 403 {
			t.Errorf("inbox of %s: want 403, got %d", as, resp.Status)
		}
	}
	// Short-name binding matches the FQDN.
	if err := h.Authorize(Identity{Node: "laptop.tail1234.ts.net"}, "codex"); err != nil {
		t.Error(err)
	}
	// The owner on loopback may not impersonate a remote agent.
	if err := h.Authorize(Identity{Local: true}, "muse"); err == nil {
		t.Error("owner impersonated muse")
	}
	// Owner-only endpoint.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/agents", nil)
	req.Header.Set(peerHeader, museNode)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 403 {
		t.Errorf("/agents as peer: %d", resp.StatusCode)
	}
}

func TestIdentifyRemote(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	cfg.Policy.Deny = []string{"bad-node"}
	h := New(cfg, mailbox.New(cfg.Home), fakeResolver{"bad-node.tail1234.ts.net."}, nil)
	try := func(addr string) (Identity, error) {
		return h.identifyRemote(&http.Request{RemoteAddr: addr})
	}
	if id, err := try("127.0.0.1:5000"); err != nil || !id.Local {
		t.Errorf("loopback: %+v %v", id, err)
	}
	if _, err := try("8.8.8.8:5000"); err == nil {
		t.Error("public address accepted")
	}
	if _, err := try("100.100.1.1:5000"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("deny list not applied: %v", err)
	}
	h.Resolver = fakeResolver{"good.tail1234.ts.net."}
	if id, err := try("100.100.1.1:5000"); err != nil || id.Node != "good.tail1234.ts.net" {
		t.Errorf("trailing dot not stripped: %+v %v", id, err)
	}
	h.Resolver = nil
	if _, err := try("100.100.1.1:5000"); err != ErrUnavailable {
		t.Errorf("no resolver should be 503: %v", err)
	}
}

type fakeResolver struct{ node string }

func (f fakeResolver) Resolve(context.Context, netip.Addr) (string, error) { return f.node, nil }

func TestValidation(t *testing.T) {
	_, srv := newTestHub(t)
	post := func(body string) int {
		resp, err := http.Post(srv.URL+"/msg", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	ok := protocol.New("grok", "muse", "x")
	b, _ := json.Marshal(ok)
	if code := post(string(b)); code != 202 {
		t.Errorf("valid: %d", code)
	}
	if code := post(strings.TrimSuffix(string(b), "}") + `,"extra":1}`); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	bad := protocol.New("grok", "nobody", "x")
	b, _ = json.Marshal(bad)
	if code := post(string(b)); code != 400 {
		t.Errorf("unknown recipient: %d", code)
	}
	bad = protocol.New("grok", "muse", "x")
	bad.Wake = "loud"
	b, _ = json.Marshal(bad)
	if code := post(string(b)); code != 400 {
		t.Errorf("bad wake: %d", code)
	}
	if code := post("not json"); code != 400 {
		t.Errorf("not json: %d", code)
	}
}

func TestWakeRateLimit(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	cfg.Agents = map[string]*config.AgentSpec{"grok": {Local: true, WakeMax: protocol.WakeNow}}
	w := NewWaker(cfg, nil)
	env := protocol.New("muse", "grok", "x")
	env.Wake = protocol.WakeNow
	var levels []string
	for i := 0; i < 8; i++ {
		levels = append(levels, w.Effective(env))
	}
	if got := strings.Join(levels, ","); got != "now,now,now,now,now,now,batch,batch" {
		t.Fatalf("rate limit: %s", got)
	}
	cfg.Agents["grok"].WakeMax = protocol.WakeBatch
	if w.Effective(env) != protocol.WakeBatch {
		t.Fatal("wake_max cap not applied")
	}
}

func texts(items []protocol.Record) []string {
	out := make([]string, len(items))
	for i, r := range items {
		out[i] = r.Envelope.Text
	}
	return out
}
