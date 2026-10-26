package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/protocol"
)

func TestGroupRules(t *testing.T) {
	h, srv := newTestHub(t)
	ctx := context.Background()
	owner := client.New(srv.URL, nil)
	alice := asPeer(srv, aliceMuseNode, aliceLogin)
	send := func(c *client.Client, env *protocol.Envelope) *client.Response {
		t.Helper()
		resp, err := c.Send(ctx, env)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// The owner's own agents always reach each other, no group needed.
	if r := send(owner, protocol.New("grok", "junyi/codex", "private")); r.Status != 202 {
		t.Fatalf("private: %+v", r)
	}
	// A friend reaches nobody before the owner puts them in a group, and the owner's agents cannot
	// reach the friend either.
	if r := send(alice, protocol.New("alice/muse", "grok", "hi")); r.Status != 403 || !strings.Contains(r.Error, "share no group") {
		t.Fatalf("no group: %+v", r)
	}
	if r := send(owner, protocol.New("grok", "alice/muse", "hi")); r.Status != 403 {
		t.Fatalf("owner to a friend without a group: %+v", r)
	}

	group(t, h, "proj", "junyi/grok", "alice/muse")
	if r := send(alice, protocol.New("alice/muse", "grok", "hi")); r.Status != 202 {
		t.Fatalf("in a group: %+v", r)
	}
	in, _ := owner.Inbox(ctx, "grok")
	if len(in.Messages) != 1 || in.Messages[0].Envelope.Group != "proj" {
		t.Fatalf("hub did not record the group: %+v", in.Messages)
	}
	// junyi/codex is not in proj: still out of alice's reach.
	if r := send(alice, protocol.New("alice/muse", "codex", "hi")); r.Status != 403 {
		t.Fatalf("non-member: %+v", r)
	}

	// Two shared groups: the sender has to say which.
	group(t, h, "family", "junyi/grok", "alice/muse")
	if r := send(alice, protocol.New("alice/muse", "grok", "which?")); r.Status != 400 || !strings.Contains(r.Error, "share groups") {
		t.Fatalf("ambiguous: %+v", r)
	}
	named := protocol.New("alice/muse", "grok", "family it is")
	named.Group = "family"
	if r := send(alice, named); r.Status != 202 {
		t.Fatalf("named group: %+v", r)
	}
	// Naming a group one end is not in, or that does not exist, fails.
	wrong := protocol.New("grok", "junyi/codex", "x")
	wrong.Group = "proj"
	if r := send(owner, wrong); r.Status != 403 {
		t.Fatalf("named group without the recipient: %+v", r)
	}
	missing := protocol.New("grok", "junyi/codex", "x")
	missing.Group = "nope"
	if r := send(owner, missing); r.Status != 400 {
		t.Fatalf("unknown group: %+v", r)
	}
	// The owner's agents may talk inside a group too, when they say so.
	both := protocol.New("grok", "junyi/codex", "in family")
	both.Group = "family"
	if _, err := h.ChangeGroup("family", []string{"codex"}, nil); err != nil {
		t.Fatal(err)
	}
	if r := send(owner, both); r.Status != 202 {
		t.Fatalf("owner's agents in a group: %+v", r)
	}
}

func TestContactsEndpoint(t *testing.T) {
	h, srv := newTestHub(t)
	group(t, h, "proj", "junyi/grok", "alice/muse")
	get := func(c *http.Client, agent string) (int, map[string]any) {
		resp, err := c.Get(srv.URL + "/contacts?agent=" + agent)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	aliceHTTP := &http.Client{Transport: peerTransport{aliceMuseNode, aliceLogin}}

	code, out := get(http.DefaultClient, "grok") // loopback acts as junyi/grok
	if code != 200 {
		t.Fatalf("grok contacts: %d %v", code, out)
	}
	b, _ := json.Marshal(out["contacts"])
	if got := string(b); !strings.Contains(got, `{"address":"alice/muse","groups":["proj"]}`) || !strings.Contains(got, `{"address":"junyi/muse","private":true}`) {
		t.Fatalf("grok contacts: %s", got)
	}

	code, out = get(aliceHTTP, "alice/muse")
	b, _ = json.Marshal(out["contacts"])
	if code != 200 || string(b) != `[{"address":"junyi/grok","groups":["proj"]}]` {
		t.Fatalf("alice contacts: %d %s", code, b)
	}
	// Only as itself.
	if code, _ := get(aliceHTTP, "grok"); code != 403 {
		t.Fatalf("alice asking as grok: %d", code)
	}
}

// A groups.json that cannot be read turns every group off and is never overwritten.
func TestGroupsFileUnreadable(t *testing.T) {
	h, _ := newTestHub(t)
	path := filepath.Join(h.Cfg.Home, GroupsFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := LoadGroups(h.Cfg.Home)
	if g.Err() == nil {
		t.Fatal("no error for a broken file")
	}
	if err := g.Create("x"); err == nil {
		t.Fatal("created a group over a broken file")
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatalf("broken file overwritten: %q", b)
	}
}
