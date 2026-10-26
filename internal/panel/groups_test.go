package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
)

const bobLogin = "bob@github"

// withBob registers a second friend, bob, with one agent on his own node.
func (f *fixture) withBob() *fixture {
	f.cfg.Owners["bob"] = &config.OwnerSpec{Login: bobLogin}
	f.cfg.Agents["bob/claude"] = &config.AgentSpec{Node: "bob-laptop"}
	return f
}

func roomIDs(f *fixture) []string {
	var rooms struct {
		Rooms  []roomOut      `json:"rooms"`
		Viewer map[string]any `json:"viewer"`
	}
	f.get("/admin/api/rooms", &rooms)
	ids := []string{}
	for _, r := range rooms.Rooms {
		ids = append(ids, r.ID)
	}
	return ids
}

// A friend sees only the groups that hold an agent of theirs, never the private space, the earlier
// traffic, storage, or the group controls.
func TestGuestSeesOnlyTheirGroups(t *testing.T) {
	f := newFixture(t).withBob()
	if err := f.h.Groups.Create("bob-only"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ChangeGroup("bob-only", []string{"junyi/muse", "bob/claude"}, nil); err != nil {
		t.Fatal(err)
	}
	f.send(local, "junyi/muse", "junyi/grok", "private plan")
	f.send(alice, "alice/muse", "junyi/grok", "in proj")
	f.send(local, "junyi/muse", "bob/claude", "for bob")
	f.legacy("alice/codex", "junyi/muse", "before groups")

	f.as(alice)
	if got := strings.Join(roomIDs(f), ","); got != "group/proj" {
		t.Fatalf("alice's rooms: %s", got)
	}
	for _, room := range []string{"private", "earlier", "group/bob-only", "hidden"} {
		if code := f.get("/admin/api/messages?room="+room, nil); code != 404 {
			t.Errorf("alice reads %s: %d", room, code)
		}
	}
	var msgs messagesResp
	if code := f.get("/admin/api/messages?room=group/proj", &msgs); code != 200 || len(msgs.Messages) != 1 {
		t.Fatalf("alice in proj: %d %+v", code, msgs)
	}
	var pr struct {
		Members map[string]presenceOut `json:"members"`
	}
	f.get("/admin/api/presence", &pr)
	if _, ok := pr.Members["junyi/muse"]; ok || len(pr.Members) != 2 {
		t.Fatalf("alice sees presence of %v", pr.Members)
	}
	if code := f.get("/admin/api/storage", nil); code != 403 {
		t.Fatalf("alice reads storage: %d", code)
	}
	if code, _, _ := f.authReq("POST", "/admin/api/groups", `{"name":"mine"}`, "", nil); code != 403 {
		t.Fatalf("alice creates a group: %d", code)
	}
	if code, _ := f.prune(`{"what":"acked","dry_run":true}`, nil); code != 403 {
		t.Fatalf("alice prunes: %d", code)
	}

	// Without a group of theirs, a friend sees nothing; a stranger neither.
	if _, err := f.h.ChangeGroup("proj", nil, []string{"alice/muse"}); err != nil {
		t.Fatal(err)
	}
	if got := roomIDs(f); len(got) != 0 {
		t.Fatalf("alice removed, still sees %v", got)
	}
	f.as(hub.Identity{Node: "stranger.tail1234.ts.net", Login: "eve@github"})
	if got := roomIDs(f); len(got) != 0 {
		t.Fatalf("stranger sees %v", got)
	}
}

// With a password set, a friend still gets their groups without it; a stranger has to log in.
func TestGuestWithPassword(t *testing.T) {
	f := newFixture(t)
	if err := SetPassword(f.cfg.Home, "correct horse"); err != nil {
		t.Fatal(err)
	}
	f.as(alice)
	if code := f.get("/admin/api/rooms", nil); code != 200 {
		t.Fatalf("friend with a password set: %d", code)
	}
	f.as(hub.Identity{Node: "stranger.tail1234.ts.net", Login: "eve@github"})
	if code := f.get("/admin/api/rooms", nil); code != 401 {
		t.Fatalf("stranger with a password set: %d", code)
	}
}

func TestGroupControls(t *testing.T) {
	f := newFixture(t)
	post := func(path, body string) (int, map[string]any) {
		code, out, _ := f.authReq("POST", path, body, "", nil)
		return code, out
	}
	if code, out := post("/admin/api/groups", `{"name":"家人","members":["grok","alice/muse"]}`); code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, _ := post("/admin/api/groups", `{"name":"家人"}`); code != 400 {
		t.Fatalf("duplicate: %d", code)
	}
	if code, _ := post("/admin/api/groups", `{"name":"a/b"}`); code != 400 {
		t.Fatalf("slash in name: %d", code)
	}
	if code, _ := post("/admin/api/groups/members", `{"name":"家人","add":["mallory/x"]}`); code != 400 {
		t.Fatalf("unregistered member: %d", code)
	}
	if code, _ := post("/admin/api/groups/members", `{"name":"家人","add":["alice/codex"],"remove":["alice/muse"]}`); code != 200 {
		t.Fatalf("members: %d", code)
	}
	g, _ := f.h.Groups.Get("家人")
	if strings.Join(g.Members, ",") != "alice/codex,junyi/grok" {
		t.Fatalf("members now %v", g.Members)
	}
	// Groups outlive a restart: they are in groups.json, not in memory.
	f.start()
	if _, ok := f.h.Groups.Get("家人"); !ok {
		t.Fatal("group lost on restart")
	}

	// Messages of a deleted group stay with the owner, marked deleted.
	f.send(alice, "alice/codex", "junyi/grok", "hello family")
	if code, _ := post("/admin/api/groups/delete", `{"name":"家人"}`); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	var rooms struct {
		Rooms []roomOut `json:"rooms"`
	}
	f.get("/admin/api/rooms", &rooms)
	var gone *roomOut
	for i := range rooms.Rooms {
		if rooms.Rooms[i].ID == "group/家人" {
			gone = &rooms.Rooms[i]
		}
	}
	if gone == nil || !gone.Deleted || gone.Count != 1 {
		t.Fatalf("deleted group: %+v", rooms.Rooms)
	}
	f.as(alice)
	if got := roomIDs(f); strings.Contains(strings.Join(got, ","), "家人") {
		t.Fatalf("friend still sees the deleted group: %v", got)
	}
}

// The address book lists every registered friend, groups or not; a guest sees only fellow members.
func TestContacts(t *testing.T) {
	f := newFixture(t).withBob()
	var book struct {
		People []person `json:"people"`
	}
	f.get("/admin/api/contacts", &book)
	handles := []string{}
	for _, p := range book.People {
		handles = append(handles, p.Handle)
	}
	if strings.Join(handles, ",") != "junyi,alice,bob" || !book.People[0].Me || book.People[1].Login != aliceLogin {
		t.Fatalf("owner's book: %+v", book.People)
	}
	bob := book.People[2]
	if len(bob.Agents) != 1 || len(bob.Agents[0].Groups) != 0 {
		t.Fatalf("bob before any group: %+v", bob)
	}

	f.as(alice)
	book.People = nil
	f.get("/admin/api/contacts", &book)
	b, _ := json.Marshal(book.People)
	if got := string(b); strings.Contains(got, "bob") || strings.Contains(got, "junyi/muse") || strings.Contains(got, aliceNode) || strings.Contains(got, aliceLogin) {
		t.Fatalf("alice's book shows too much: %s", got)
	}
	if len(book.People) != 2 {
		t.Fatalf("alice's book: %s", b)
	}
}
