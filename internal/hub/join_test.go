package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

func join(t *testing.T, srv *httptest.Server, node, login, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/join", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if node != "" {
		req.Header.Set(peerHeader, node)
		req.Header.Set(loginHeader, login)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const (
	carolNode  = "khoomei-carol-grok.tail7777.ts.net"
	carolLogin = "carol@github"
)

// A friend's agent comes in on its own: the hub learns who it is from WhoIs, nobody types anything.
func TestJoinByItself(t *testing.T) {
	h, srv := newTestHub(t)
	code, out := join(t, srv, carolNode, carolLogin, `{"agent":"grok"}`)
	if code != 200 || out["address"] != "carol/grok" || out["new_person"] != true || out["new_agent"] != true || out["hub_owner"] != "junyi" {
		t.Fatalf("first join: %d %v", code, out)
	}
	if h.Dir.Login("carol") != carolLogin || h.Dir.Agent("carol/grok").Node != carolNode {
		t.Fatalf("not registered: %q %+v", h.Dir.Login("carol"), h.Dir.Agent("carol/grok"))
	}
	if err := h.Authorize(Identity{Node: carolNode, Login: carolLogin}, "carol/grok"); err != nil {
		t.Fatalf("joined agent not authorized: %v", err)
	}
	if h.FriendOf(Identity{Node: carolNode, Login: carolLogin}) != "carol" {
		t.Fatal("joined person is no friend")
	}
	// In the contacts, not yet able to talk: no group.
	if err := h.route(&protocol.Envelope{FromAgent: "carol/grok", ToAgent: "junyi/grok"}); err == nil {
		t.Fatal("joined agent may talk without a group")
	}

	// Reinstalled on a new node: same person, the binding follows.
	code, out = join(t, srv, "khoomei-carol-grok-1.tail7777.ts.net", carolLogin, `{"agent":"grok"}`)
	if code != 200 || out["new_agent"] == true || out["new_person"] == true || h.Dir.Agent("carol/grok").Node != "khoomei-carol-grok-1.tail7777.ts.net" {
		t.Fatalf("rejoin: %d %v", code, out)
	}
	// One login, one handle.
	if code, _ := join(t, srv, carolNode, carolLogin, `{"agent":"muse","owner":"caz"}`); code != 400 {
		t.Fatalf("second handle for one login: %d", code)
	}
	// Someone else whose login would also make "carol" picks another.
	if code, out := join(t, srv, "other.tail8888.ts.net", "carol@gitlab", `{"agent":"grok"}`); code != 409 || out["code"] != "taken" {
		t.Fatalf("handle clash: %d %v", code, out)
	}
	if code, out := join(t, srv, "other.tail8888.ts.net", "carol@gitlab", `{"agent":"grok","owner":"carol2"}`); code != 200 || out["address"] != "carol2/grok" {
		t.Fatalf("own handle: %d %v", code, out)
	}
	// The owner's handle and registered friends' handles are not up for grabs.
	for _, o := range []string{"junyi", "alice"} {
		if code, _ := join(t, srv, "x.tail8888.ts.net", "x@github", `{"agent":"grok","owner":"`+o+`"}`); code != 409 {
			t.Fatalf("took %s: %d", o, code)
		}
	}
}

func TestJoinRules(t *testing.T) {
	h, srv := newTestHub(t)
	h.SelfLogin = "junyi@github"

	// The owner's own new agent joins the private space.
	if code, out := join(t, srv, "junyi-mbp.tail1234.ts.net", "junyi@github", `{"agent":"claude"}`); code != 200 || out["address"] != "junyi/claude" || out["new_person"] == true {
		t.Fatalf("owner's agent: %d %v", code, out)
	}
	// Tagged nodes belong to nobody; loopback agents are configured, not joined.
	if code, _ := join(t, srv, "grokbot.tail1234.ts.net", "tagged-devices", `{"agent":"grok"}`); code != 403 {
		t.Fatalf("tagged: %d", code)
	}
	if code, _ := join(t, srv, "", "", `{"agent":"grok"}`); code != 400 {
		t.Fatalf("loopback: %d", code)
	}
	// config.json wins: alice/muse is configured for her node.
	if code, _ := join(t, srv, "elsewhere.tail9999.ts.net", aliceLogin, `{"agent":"muse"}`); code != 409 {
		t.Fatalf("configured agent from another node: %d", code)
	}
	if code, out := join(t, srv, aliceMuseNode, aliceLogin, `{"agent":"muse"}`); code != 200 || out["new_agent"] == true {
		t.Fatalf("configured agent from its node: %d %v", code, out)
	}
	// Bad bodies.
	for _, body := range []string{`{"agent":"a/b"}`, `{"agent":""}`, `{"agent":"x","extra":1}`} {
		if code, _ := join(t, srv, carolNode, carolLogin, body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	// Webhooks a guest hands over must be https to a public host.
	for _, u := range []string{"http://example.com/x", "https://127.0.0.1/x", "https://10.0.0.1/x", "https://100.80.1.2/x", "https://example.com:8443/x", "https://u:p@example.com/x"} {
		if code, _ := join(t, srv, carolNode, carolLogin, `{"agent":"grok","wake":{"url":"`+u+`","secret":"s"}}`); code != 400 {
			t.Errorf("webhook %s accepted: %d", u, code)
		}
	}
	if code, _ := join(t, srv, carolNode, carolLogin, `{"agent":"grok","wake":{"url":"https://hooks.example.com/w","secret":"s"}}`); code != 200 {
		t.Fatalf("public webhook: %d", code)
	}
	if s := h.Dir.Agent("carol/grok"); s.Wake == nil || s.Wake.URL != "https://hooks.example.com/w" {
		t.Fatalf("webhook not kept: %+v", s)
	}
}

// An open group takes every agent that arrives; the owner can remove and block a guest.
func TestJoinOpenGroupAndRemove(t *testing.T) {
	h, srv := newTestHub(t)
	group(t, h, "lobby", "junyi/grok")
	if err := h.Groups.SetOpen("lobby", true); err != nil {
		t.Fatal(err)
	}
	if code, out := join(t, srv, carolNode, carolLogin, `{"agent":"grok"}`); code != 200 || len(out["groups"].([]any)) != 1 {
		t.Fatalf("join: %d %v", code, out)
	}
	if g, _ := h.Groups.Get("lobby"); strings.Join(g.Members, ",") != "carol/grok,junyi/grok" {
		t.Fatalf("lobby: %v", g.Members)
	}

	gone, err := h.Dir.Remove("carol", true)
	if err != nil || strings.Join(gone, ",") != "carol/grok" {
		t.Fatalf("remove: %v %v", gone, err)
	}
	h.LeaveGroups(gone)
	if g, _ := h.Groups.Get("lobby"); strings.Join(g.Members, ",") != "junyi/grok" {
		t.Fatalf("lobby after remove: %v", g.Members)
	}
	if code, _ := join(t, srv, carolNode, carolLogin, `{"agent":"grok"}`); code != 403 {
		t.Fatalf("blocked login joined again: %d", code)
	}
	if _, err := h.Dir.Remove("alice", false); err == nil {
		t.Fatal("removed a friend from config.json")
	}
}

// contacts.json survives a restart; single-owner hubs take no guests.
func TestJoinPersistsAndSingleOwner(t *testing.T) {
	h, srv := newTestHub(t)
	join(t, srv, carolNode, carolLogin, `{"agent":"grok"}`)
	again := LoadDirectory(h.Cfg)
	if again.Err() != nil || again.Login("carol") != carolLogin || again.Agent("carol/grok") == nil {
		t.Fatalf("reloaded: %v %q", again.Err(), again.Login("carol"))
	}

	cfg, _ := config.Load(t.TempDir())
	cfg.Role = config.RoleHub
	single := New(cfg, mailbox.New(cfg.Home), nil, t.Logf)
	if _, err := single.Dir.join(carolLogin, carolNode, false, JoinRequest{Agent: "grok"}, time.Now()); err == nil {
		t.Fatal("single-owner hub took a guest")
	}
}
