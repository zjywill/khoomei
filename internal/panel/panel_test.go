package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

const (
	aliceNode  = "alice-muse.tail9999.ts.net"
	aliceLogin = "alice@github"
)

// fixture: junyi's hub with grok and muse on the hub, alice's muse and codex on her node.
type fixture struct {
	t   *testing.T
	cfg *config.Config
	box *mailbox.Mailbox
	h   *hub.Hub
	p   *Panel
	srv *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.Owner = config.RoleHub, "junyi"
	cfg.Owners = map[string]*config.OwnerSpec{"alice": {Login: aliceLogin}}
	cfg.Agents = map[string]*config.AgentSpec{
		"junyi/grok":  {Local: true, Wake: &config.WakeAdapter{Type: "exec", Argv: []string{"true"}}},
		"junyi/muse":  {Local: true},
		"alice/muse":  {Node: aliceNode},
		"alice/codex": {Node: aliceNode},
	}
	f := &fixture{t: t, cfg: cfg, box: mailbox.New(cfg.Home)}
	f.start()
	return f
}

// start (re)builds hub and panel over the same home, as a restart would.
func (f *fixture) start() {
	f.t.Helper()
	if f.srv != nil {
		f.srv.Close()
	}
	f.h = hub.New(f.cfg, f.box, nil, f.t.Logf)
	p, err := New(f.cfg, f.box, f.t.Logf)
	if err != nil {
		f.t.Fatal(err)
	}
	f.p = p
	f.h.Observer, f.h.Admin = p, p.Handler()
	f.h.Identify = func(r *http.Request) (hub.Identity, error) { return hub.Identity{Local: true}, nil }
	f.srv = httptest.NewServer(f.h.Handler())
	f.t.Cleanup(f.srv.Close)
}

func (f *fixture) send(id hub.Identity, from, to, text string) *protocol.Envelope {
	f.t.Helper()
	env := protocol.New(from, to, text)
	if _, err := f.h.Receive(id, env); err != nil {
		f.t.Fatalf("send %s→%s: %v", from, to, err)
	}
	return env
}

var (
	local = hub.Identity{Local: true}
	alice = hub.Identity{Node: aliceNode, Login: aliceLogin}
)

func (f *fixture) get(path string, out any) int {
	f.t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			f.t.Fatalf("%s: %v", path, err)
		}
	}
	return resp.StatusCode
}

func (f *fixture) prune(body string, hdr map[string]string) (int, map[string]any) {
	f.t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/admin/api/prune", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Khoomei-Panel", "1")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

type messagesResp struct {
	Messages []Record `json:"messages"`
	More     bool     `json:"more"`
}

func TestRooms(t *testing.T) {
	f := newFixture(t)
	f.send(local, "junyi/muse", "junyi/grok", "self")
	f.send(local, "junyi/grok", "alice/muse", "to alice")
	f.send(alice, "alice/muse", "junyi/grok", "from alice")
	f.send(alice, "alice/muse", "alice/codex", "between alice's own agents")

	var rooms struct {
		Rooms []roomOut `json:"rooms"`
	}
	f.get("/admin/api/rooms", &rooms)
	if len(rooms.Rooms) != 2 || rooms.Rooms[0].ID != RoomSelf || rooms.Rooms[1].ID != "peer/alice" {
		t.Fatalf("rooms: %+v", rooms.Rooms)
	}
	if rooms.Rooms[0].Count != 1 || rooms.Rooms[1].Count != 2 || rooms.Rooms[1].Title != "junyi × alice" {
		t.Fatalf("counts: %+v", rooms.Rooms)
	}
	for _, r := range rooms.Rooms {
		for _, m := range r.Members {
			if m == "alice/codex" && r.ID == RoomSelf {
				t.Fatalf("friend agent in self room: %+v", r)
			}
		}
	}
	var msgs messagesResp
	f.get("/admin/api/messages?room=peer/alice", &msgs)
	for _, m := range msgs.Messages {
		if m.Envelope.Text == "between alice's own agents" {
			t.Fatal("friend-to-friend message shown")
		}
	}
	if code := f.get("/admin/api/messages?room=hidden", nil); code != 400 {
		t.Fatalf("hidden room readable: %d", code)
	}
}

func TestPagingAndFilters(t *testing.T) {
	f := newFixture(t)
	for i := range 7 {
		from, to := "junyi/muse", "junyi/grok"
		if i%2 == 1 {
			from, to = to, from
		}
		f.send(local, from, to, fmt.Sprint(i))
	}
	var page messagesResp
	f.get("/admin/api/messages?room=self&limit=3", &page)
	if len(page.Messages) != 3 || !page.More || page.Messages[0].Envelope.Text != "4" || page.Messages[2].Envelope.Text != "6" {
		t.Fatalf("first page: %+v", page)
	}
	var older messagesResp
	f.get(fmt.Sprintf("/admin/api/messages?room=self&limit=3&before=%d", page.Messages[0].Seq), &older)
	if len(older.Messages) != 3 || older.Messages[2].Envelope.Text != "3" || !older.More {
		t.Fatalf("second page: %+v", older)
	}
	var last messagesResp
	f.get(fmt.Sprintf("/admin/api/messages?room=self&limit=3&before=%d", older.Messages[0].Seq), &last)
	if len(last.Messages) != 1 || last.More || last.Messages[0].Envelope.Text != "0" {
		t.Fatalf("last page: %+v", last)
	}
	var toMuse messagesResp
	f.get("/admin/api/messages?room=self&member=junyi/grok&kinds=text", &toMuse)
	if len(toMuse.Messages) != 7 {
		t.Fatalf("member filter: %d", len(toMuse.Messages))
	}
	var none messagesResp
	f.get("/admin/api/messages?room=self&kinds=request", &none)
	if len(none.Messages) != 0 {
		t.Fatalf("kind filter: %d", len(none.Messages))
	}
}

func TestAckRebuildAndUnread(t *testing.T) {
	f := newFixture(t)
	a := f.send(local, "junyi/muse", "junyi/grok", "a")
	f.send(local, "junyi/muse", "junyi/grok", "b")
	if _, err := f.box.Ack("junyi/grok", []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	f.p.Acked("junyi/grok", []string{a.ID})

	check := func(when string) {
		var unread messagesResp
		f.get("/admin/api/messages?room=self&unread=1", &unread)
		if len(unread.Messages) != 1 || unread.Messages[0].Envelope.Text != "b" {
			t.Fatalf("%s: unread %+v", when, unread.Messages)
		}
		var all messagesResp
		f.get("/admin/api/messages?room=self", &all)
		if len(all.Messages) != 2 || !all.Messages[0].Acked || all.Messages[1].Acked {
			t.Fatalf("%s: acked flags %+v", when, all.Messages)
		}
	}
	check("live")
	f.start()
	check("after restart")
}

func TestAckThroughHTTP(t *testing.T) {
	f := newFixture(t)
	env := f.send(local, "junyi/muse", "junyi/grok", "x")
	body := strings.NewReader(fmt.Sprintf(`{"agent":"junyi/grok","ids":[%q]}`, env.ID))
	resp, err := http.Post(f.srv.URL+"/ack", "application/json", body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ack: %v %v", err, resp)
	}
	var msg struct {
		Message Record `json:"message"`
	}
	if code := f.get("/admin/api/message?id="+env.ID, &msg); code != 200 || !msg.Message.Acked {
		t.Fatalf("message after ack: %d %+v", code, msg)
	}
}

func TestEvents(t *testing.T) {
	f := newFixture(t)
	var start struct {
		Next int64 `json:"next"`
	}
	f.get("/admin/api/events", &start)

	got := make(chan []Event, 1)
	go func() {
		var out struct {
			Events []Event `json:"events"`
		}
		f.get(fmt.Sprintf("/admin/api/events?after=%d&timeout=10", start.Next), &out)
		got <- out.Events
	}()
	time.Sleep(100 * time.Millisecond)
	f.send(local, "junyi/muse", "junyi/grok", "live")
	f.send(alice, "alice/muse", "alice/codex", "hidden")
	select {
	case evs := <-got:
		if len(evs) == 0 || evs[0].Type != "message" || evs[0].Record.Envelope.Text != "live" {
			t.Fatalf("events: %+v", evs)
		}
		for _, e := range evs {
			if e.Record != nil && e.Record.Envelope.Text == "hidden" {
				t.Fatal("hidden message streamed")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll did not return on a new message")
	}

	// A cursor that fell out of the ring, or one from before a restart, asks for a reset.
	f.p.mu.Lock()
	for range eventRing + 5 {
		f.p.push(Event{Type: "presence"})
	}
	f.p.mu.Unlock()
	var stale struct {
		Reset bool `json:"reset"`
	}
	f.get(fmt.Sprintf("/admin/api/events?after=%d&timeout=1", start.Next), &stale)
	if !stale.Reset {
		t.Fatal("stale cursor not reset")
	}
	stale.Reset = false
	f.get("/admin/api/events?after=999999999&timeout=1", &stale)
	if !stale.Reset {
		t.Fatal("future cursor not reset")
	}
}

func TestPresence(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/wait?agent=junyi/muse&timeout=10", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	var pr struct {
		Members map[string]presenceOut `json:"members"`
	}
	for time.Now().Before(deadline) {
		f.get("/admin/api/presence", &pr)
		if pr.Members["junyi/muse"].Waiting == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m := pr.Members["junyi/muse"]; m.State != "online" || m.Waiting != 1 {
		t.Fatalf("muse: %+v", m)
	}
	if g := pr.Members["junyi/grok"]; g.State != "sleeping" || g.Wake != "exec" {
		t.Fatalf("grok: %+v", g)
	}
	if a := pr.Members["alice/codex"]; a.State != "offline" {
		t.Fatalf("alice/codex: %+v", a)
	}
}

func ackAll(t *testing.T, f *fixture, agent string) {
	t.Helper()
	pending, _ := f.box.Pending(agent)
	ids := []string{}
	for _, r := range pending {
		ids = append(ids, r.Envelope.ID)
	}
	done, _ := f.box.Ack(agent, ids)
	f.p.Acked(agent, done)
}

func TestPruneRules(t *testing.T) {
	f := newFixture(t)
	f.send(local, "junyi/muse", "junyi/grok", "acked")
	ackAll(t, f, "junyi/grok")
	f.send(local, "junyi/grok", "junyi/muse", "pending")
	f.send(alice, "alice/muse", "alice/codex", "hidden pending")

	// Younger than a day: nothing goes, even when asked for 1 hour.
	if _, out := f.prune(`{"what":"acked","older_than_hours":1}`, nil); out["count"] != 0.0 {
		t.Fatalf("young record pruned: %v", out)
	}
	f.p.now = func() time.Time { return time.Now().Add(25 * time.Hour) }

	if _, out := f.prune(`{"what":"acked","older_than_hours":1,"dry_run":true}`, nil); out["count"] != 1.0 || out["dry_run"] != true {
		t.Fatalf("dry run: %v", out)
	}
	if code, _ := f.prune(`{"what":"pending"}`, nil); code != 400 {
		t.Fatalf("pending without agent: %d", code)
	}
	if code, _ := f.prune(`{"what":"pending","agent":"alice/codex"}`, nil); code != 400 {
		t.Fatalf("pending of a friend's agent: %d", code)
	}
	if code, _ := f.prune(`{"what":"pending","room":"hidden"}`, nil); code != 400 {
		t.Fatalf("hidden pending: %d", code)
	}
	if code, _ := f.prune(`{"what":"acked","room":"self","agent":"junyi/grok"}`, nil); code != 400 {
		t.Fatalf("room and agent: %d", code)
	}

	// Automatic retention only ever removes acked records.
	f.p.now = func() time.Time { return time.Now().Add(4 * 24 * time.Hour) }
	res, err := f.p.Retain()
	if err != nil || res.Count != 1 {
		t.Fatalf("retain: %+v %v", res, err)
	}
	if p, _ := f.box.Pending("junyi/muse"); len(p) != 1 {
		t.Fatal("retention touched a pending record")
	}
	if p, _ := f.box.Pending("alice/codex"); len(p) != 1 {
		t.Fatal("retention touched a hidden pending record")
	}

	// Manual pending prune of an own agent.
	if code, out := f.prune(`{"what":"pending","agent":"muse"}`, nil); code != 200 || out["count"] != 1.0 {
		t.Fatalf("pending prune: %d %v", code, out)
	}
	f.start()
	var rooms struct {
		Rooms []roomOut `json:"rooms"`
	}
	f.get("/admin/api/rooms", &rooms)
	if rooms.Rooms[0].Count != 0 {
		t.Fatalf("index and disk disagree after restart: %+v", rooms.Rooms[0])
	}
}

func TestRetentionMaxMB(t *testing.T) {
	f := newFixture(t)
	big := strings.Repeat("x", 200<<10)
	var first *protocol.Envelope
	for i := range 8 { // ~1.6 MB of acked records
		env := f.send(local, "junyi/muse", "junyi/grok", fmt.Sprint(i, big))
		if i == 0 {
			first = env
		}
	}
	ackAll(t, f, "junyi/grok")
	zero := 0
	f.cfg.Retention = config.Retention{AckedDays: &zero, MaxMB: 1}
	f.p.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	res, err := f.p.Retain()
	if err != nil || res.Count < 4 {
		t.Fatalf("max_mb: %+v %v", res, err)
	}
	if _, _, err := f.box.Read("junyi/grok", first.ID); err == nil {
		t.Fatal("oldest record survived")
	}
	var left int64
	f.p.mu.Lock()
	for _, e := range f.p.byKey {
		left += e.Size
	}
	f.p.mu.Unlock()
	if left > (1<<20)*9/10 {
		t.Fatalf("still %d bytes", left)
	}
}

func TestPruneCSRF(t *testing.T) {
	f := newFixture(t)
	body := `{"what":"acked","dry_run":true}`
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"ok", nil, 200},
		{"same origin", map[string]string{"Origin": f.srv.URL}, 200},
		{"no panel header", map[string]string{"X-Khoomei-Panel": ""}, 403},
		{"text/plain", map[string]string{"Content-Type": "text/plain"}, 403},
		{"form", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 403},
		{"other origin", map[string]string{"Origin": "https://evil.example"}, 403},
	}
	for _, c := range cases {
		if code, out := f.prune(body, c.hdr); code != c.want {
			t.Errorf("%s: %d %v", c.name, code, out)
		}
	}
	resp, err := http.Get(f.srv.URL + "/admin/api/prune")
	if err != nil || resp.StatusCode == 200 {
		t.Fatalf("GET prune: %v %v", err, resp.StatusCode)
	}
}

func TestStorage(t *testing.T) {
	f := newFixture(t)
	f.send(local, "junyi/muse", "junyi/grok", "a")
	f.send(alice, "alice/muse", "alice/codex", "hidden")
	var st struct {
		Rooms     []roomUsage    `json:"rooms"`
		Retention map[string]any `json:"retention"`
	}
	f.get("/admin/api/storage", &st)
	if len(st.Rooms) != 2 || st.Rooms[0].ID != RoomSelf || st.Rooms[1].ID != RoomHidden || st.Rooms[0].Pending.Count != 1 || st.Rooms[1].Pending.Bytes == 0 {
		t.Fatalf("storage: %+v", st.Rooms)
	}
	if st.Retention["acked_days"] != float64(config.DefaultAckedDays) {
		t.Fatalf("retention: %v", st.Retention)
	}
}

func TestStaticFallback(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/admin", "/admin/", "/admin/some/view"} {
		resp, err := http.Get(f.srv.URL + p)
		if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %v %v", p, err, resp)
		}
		resp.Body.Close()
	}
	if code := f.get("/admin/api/nope", nil); code != 404 {
		t.Fatalf("unknown api: %d", code)
	}
}
