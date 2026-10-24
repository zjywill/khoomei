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

const (
	museNode      = "muse-vm.tail1234.ts.net"
	aliceMuseNode = "alice-muse.tail9999.ts.net"
	aliceLogin    = "alice@github"
)

// Test headers let a client pose as a tailnet node with a login; production hubs never read them.
const (
	peerHeader  = "X-Test-Peer"
	loginHeader = "X-Test-Login"
)

// The hub belongs to junyi, who runs grok locally and muse on a VM. alice is a friend whose
// muse connects from her own (shared) node. bob has an agent registered but no login.
func newTestHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.Owner, cfg.Agent = config.RoleHub, "junyi", "grok"
	cfg.Owners = map[string]*config.OwnerSpec{"alice": {Login: aliceLogin}}
	cfg.Agents = map[string]*config.AgentSpec{
		"grok":        {Local: true}, // bare: normalized to junyi/grok on Save/Load; Spec() handles it here
		"junyi/muse":  {Node: museNode},
		"junyi/codex": {Node: "laptop"},
		"alice/muse":  {Node: aliceMuseNode},
		"bob/claude":  {Node: "bob-laptop"},
	}
	cfg.Agents["junyi/grok"] = cfg.Agents["grok"]
	delete(cfg.Agents, "grok")
	h := New(cfg, mailbox.New(cfg.Home), nil, t.Logf)
	h.Identify = func(r *http.Request) (Identity, error) {
		if p := r.Header.Get(peerHeader); p != "" {
			return Identity{Node: p, Login: strings.ToLower(r.Header.Get(loginHeader))}, nil
		}
		return Identity{Local: true}, nil
	}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return h, srv
}

type peerTransport struct{ node, login string }

func (p peerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(peerHeader, p.node)
	if p.login != "" {
		r.Header.Set(loginHeader, p.login)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func asPeer(srv *httptest.Server, node, login string) *client.Client {
	return client.New(srv.URL, &http.Client{Transport: peerTransport{node, login}})
}

func TestRoundTripWithBareNames(t *testing.T) {
	_, srv := newTestHub(t)
	ctx := context.Background()
	muse := asPeer(srv, museNode, "junyi@github")
	owner := client.New(srv.URL, nil)

	// Bare names: the hub qualifies them to its owner.
	ask := protocol.New("muse", "grok", "大家在讨论什么？")
	ask.Wake = protocol.WakeNow
	resp, err := muse.Send(ctx, ask)
	if err != nil || resp.Status != 202 {
		t.Fatalf("send: %v %+v", err, resp)
	}
	if resp, _ = muse.Send(ctx, ask); resp.Status != 200 || !resp.Duplicate {
		t.Fatalf("duplicate: %+v", resp)
	}
	again := protocol.New("muse", "grok", "again")
	if resp, _ = muse.Send(ctx, again); resp.Status != 202 {
		t.Fatalf("second send: %+v", resp)
	}

	resp, _ = owner.Inbox(ctx, "grok")
	if resp.Agent != "junyi/grok" {
		t.Fatalf("inbox agent not qualified: %q", resp.Agent)
	}
	if got := texts(resp.Messages); strings.Join(got, "|") != "大家在讨论什么？|again" {
		t.Fatalf("inbox order: %v", got)
	}
	if e := resp.Messages[0].Envelope; e.FromAgent != "junyi/muse" || e.ToAgent != "junyi/grok" {
		t.Fatalf("stored envelope not qualified: %+v", e)
	}
	if r := resp.Messages[0].Received; r.Peer != museNode || r.Login != "junyi@github" {
		t.Fatalf("received identity: %+v", r)
	}

	reply := protocol.New("grok", "muse", "三件事，见 data")
	reply.ReplyTo, reply.Data = ask.ID, json.RawMessage(`{"items":3}`)
	if resp, _ = owner.Send(ctx, reply); resp.Status != 202 {
		t.Fatalf("reply: %+v", resp)
	}
	if resp, _ = owner.Ack(ctx, "grok", []string{ask.ID, again.ID}); len(resp.Acked) != 2 {
		t.Fatalf("ack: %+v", resp)
	}
	if resp, _ = owner.Inbox(ctx, "junyi/grok"); len(resp.Messages) != 0 {
		t.Fatalf("inbox not empty after ack: %+v", resp.Messages)
	}

	resp, _ = muse.Wait(ctx, "muse", 5*time.Second)
	if len(resp.Messages) != 1 || resp.Messages[0].Envelope.ReplyTo != ask.ID || string(resp.Messages[0].Envelope.Data) != `{"items":3}` {
		t.Fatalf("wait: %+v", resp)
	}
	if resp.Messages[0].Received.Peer != "hub" {
		t.Fatalf("peer recorded as %q", resp.Messages[0].Received.Peer)
	}
	muse.Ack(ctx, "junyi/muse", []string{resp.Messages[0].Envelope.ID})
	if resp, _ = muse.Wait(ctx, "muse", time.Second); len(resp.Messages) != 0 {
		t.Fatalf("expected empty wait: %+v", resp)
	}
}

func TestFriendOwner(t *testing.T) {
	_, srv := newTestHub(t)
	ctx := context.Background()
	owner := client.New(srv.URL, nil)

	// alice's muse, from her node with her login, reaches junyi's grok.
	alice := asPeer(srv, aliceMuseNode, "Alice@GitHub") // login compared case-insensitively
	msg := protocol.New("alice/muse", "grok", "hi from alice")
	if resp, _ := alice.Send(ctx, msg); resp.Status != 202 {
		t.Fatalf("alice send: %+v", resp)
	}
	resp, _ := owner.Inbox(ctx, "grok")
	if len(resp.Messages) != 1 || resp.Messages[0].Envelope.FromAgent != "alice/muse" || resp.Messages[0].Received.Login != aliceLogin {
		t.Fatalf("grok inbox: %+v", resp.Messages)
	}
	// junyi replies to alice/muse; alice reads her own inbox, not junyi's muse.
	owner.Send(ctx, protocol.New("grok", "alice/muse", "hi alice"))
	if resp, _ = alice.Inbox(ctx, "alice/muse"); len(resp.Messages) != 1 || resp.Messages[0].Envelope.Text != "hi alice" {
		t.Fatalf("alice inbox: %+v", resp.Messages)
	}
	if resp, _ = alice.Inbox(ctx, "muse"); resp.Status != 403 { // bare = junyi/muse, bound to another node
		t.Fatalf("alice reading junyi/muse: %d", resp.Status)
	}

	// Same node, wrong login: rejected.
	impostor := asPeer(srv, aliceMuseNode, "mallory@github")
	if resp, _ := impostor.Send(ctx, protocol.New("alice/muse", "grok", "x")); resp.Status != 403 || !strings.Contains(resp.Error, "logged in as") {
		t.Fatalf("wrong login: %+v", resp)
	}
	// Right login, wrong node: rejected.
	if resp, _ := asPeer(srv, "other.tail9999.ts.net", aliceLogin).Send(ctx, protocol.New("alice/muse", "grok", "x")); resp.Status != 403 {
		t.Fatalf("wrong node: %+v", resp)
	}
	// alice's node may not speak as junyi's agents.
	if resp, _ := alice.Send(ctx, protocol.New("muse", "grok", "x")); resp.Status != 403 {
		t.Fatalf("alice as junyi/muse: %+v", resp)
	}
	// bob has an agent but no registered login: his node cannot connect at all.
	if resp, _ := asPeer(srv, "bob-laptop.tail5555.ts.net", "bob@github").Send(ctx, protocol.New("bob/claude", "grok", "x")); resp.Status != 403 || !strings.Contains(resp.Error, "no registered login") {
		t.Fatalf("owner without login: %+v", resp)
	}
	// The hub owner on loopback may not act as alice's agent.
	if resp, _ := owner.Inbox(ctx, "alice/muse"); resp.Status != 403 {
		t.Fatalf("owner reading alice/muse: %d", resp.Status)
	}
	// Sending to an unknown owner's agent is a 400, not a silent drop.
	if resp, _ := owner.Send(ctx, protocol.New("grok", "carol/muse", "x")); resp.Status != 400 {
		t.Fatalf("unknown recipient owner: %+v", resp)
	}
}

func TestWaitWakesOnArrival(t *testing.T) {
	_, srv := newTestHub(t)
	ctx := context.Background()
	muse := asPeer(srv, museNode, "")
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

func TestAuthorizationOwnAgents(t *testing.T) {
	h, srv := newTestHub(t)
	ctx := context.Background()
	muse := asPeer(srv, museNode, "")

	for _, as := range []string{"grok", "codex", "stranger", "junyi/grok"} {
		if resp, _ := muse.Send(ctx, protocol.New(as, "grok", "x")); resp.Status != 403 {
			t.Errorf("send as %s: want 403, got %d", as, resp.Status)
		}
		if resp, _ := muse.Inbox(ctx, as); resp.Status != 403 {
			t.Errorf("inbox of %s: want 403, got %d", as, resp.Status)
		}
	}
	if err := h.Authorize(Identity{Node: "laptop.tail1234.ts.net"}, "codex"); err != nil {
		t.Error(err)
	}
	if err := h.Authorize(Identity{Local: true}, "muse"); err == nil {
		t.Error("owner impersonated muse")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/agents", nil)
	req.Header.Set(peerHeader, museNode)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 403 {
		t.Errorf("/agents as peer: %d", resp.StatusCode)
	}
}

func TestSingleOwnerModeStillWorks(t *testing.T) {
	// No owner handle: bare names stay bare, as in 0.2.
	cfg, _ := config.Load(t.TempDir())
	cfg.Role = config.RoleHub
	cfg.Agents = map[string]*config.AgentSpec{"grok": {Local: true}, "muse": {Node: museNode}}
	h := New(cfg, mailbox.New(cfg.Home), nil, t.Logf)
	if err := h.Authorize(Identity{Node: museNode}, "muse"); err != nil {
		t.Fatal(err)
	}
	if h.Qualify("grok") != "grok" {
		t.Fatal("bare name should stay bare without an owner")
	}
}

func TestIdentifyRemote(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	cfg.Policy.Deny = []string{"bad-node", "evil@github"}
	h := New(cfg, mailbox.New(cfg.Home), fakeResolver{Peer{Node: "bad-node.tail1234.ts.net."}}, nil)
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
		t.Errorf("deny by node not applied: %v", err)
	}
	h.Resolver = fakeResolver{Peer{Node: "good.tail1234.ts.net.", Login: "Evil@GitHub"}}
	if _, err := try("100.100.1.1:5000"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("deny by login not applied: %v", err)
	}
	h.Resolver = fakeResolver{Peer{Node: "good.tail1234.ts.net.", Login: "Alice@GitHub"}}
	if id, err := try("100.100.1.1:5000"); err != nil || id.Node != "good.tail1234.ts.net" || id.Login != "alice@github" {
		t.Errorf("normalization: %+v %v", id, err)
	}
	h.Resolver = nil
	if _, err := try("100.100.1.1:5000"); err != ErrUnavailable {
		t.Errorf("no resolver should be 503: %v", err)
	}
}

type fakeResolver struct{ peer Peer }

func (f fakeResolver) Resolve(context.Context, netip.Addr) (Peer, error) { return f.peer, nil }

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
	for _, bad := range []*protocol.Envelope{
		protocol.New("grok", "nobody", "x"),
		protocol.New("grok", "a/b/c", "x"),
		protocol.New("grok", "../muse", "x"),
	} {
		b, _ = json.Marshal(bad)
		if code := post(string(b)); code != 400 {
			t.Errorf("%q should be 400, got %d", bad.ToAgent, code)
		}
	}
	bad := protocol.New("grok", "muse", "x")
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
