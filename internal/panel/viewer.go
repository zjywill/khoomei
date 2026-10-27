package panel

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/protocol"
)

// A viewer is who is looking at the panel (docs/PANEL.md#谁看到什么): the hub owner sees every room;
// anyone else is a guest and sees only the groups that hold an agent of theirs.
type viewer struct {
	owner  bool
	friend string // a guest's handle, when they are a registered friend
}

type viewerKey struct{}

func viewerFrom(ctx context.Context) viewer {
	v, _ := ctx.Value(viewerKey{}).(viewer)
	return v
}

// viewerOf decides who r is. The owner: loopback, a logged-in session, or, while no password is
// set, a node logged in as the owner. Everyone else is a guest.
func (p *Panel) viewerOf(r *http.Request, hash string) viewer {
	id, _ := hub.IdentityFrom(r.Context())
	switch {
	case id.Local:
		return viewer{owner: true}
	case hash != "" && p.auth.valid(r, hash):
		return viewer{owner: true}
	case hash == "" && p.isOwner(r):
		return viewer{owner: true}
	}
	v := viewer{}
	if p.Hub != nil {
		v.friend = p.Hub.FriendOf(id)
	}
	return v
}

// agents is every registered agent: config.json plus the ones that joined by themselves.
func (p *Panel) agents() map[string]*config.AgentSpec {
	if p.Hub == nil {
		return p.cfg.Agents
	}
	return p.Hub.Dir.Agents()
}

// people maps every friend's handle to their login.
func (p *Panel) people() map[string]string {
	if p.Hub == nil {
		out := map[string]string{}
		for h, o := range p.cfg.Owners {
			if o != nil && h != p.cfg.Owner {
				out[h] = o.Login
			}
		}
		return out
	}
	return p.Hub.Dir.People()
}

func (p *Panel) groups() map[string]hub.Group {
	if p.Hub == nil || p.Hub.Groups == nil {
		return map[string]hub.Group{}
	}
	return p.Hub.Groups.All()
}

// guestGroups lists the groups holding at least one agent of friend.
func guestGroups(all map[string]hub.Group, friend string) map[string]bool {
	out := map[string]bool{}
	if friend == "" {
		return out
	}
	for name, g := range all {
		for _, m := range g.Members {
			if o, _, _ := protocol.SplitAddress(m); o == friend {
				out[name] = true
				break
			}
		}
	}
	return out
}

// sees returns a predicate over room ids for v.
func (p *Panel) sees(v viewer) func(room string) bool {
	if v.owner {
		return func(room string) bool { return room != RoomHidden }
	}
	mine := guestGroups(p.groups(), v.friend)
	return func(room string) bool {
		name, ok := strings.CutPrefix(room, groupPrefix)
		return ok && mine[name]
	}
}

// visibleAgents is every member of a room v sees; the owner sees all agents. The caller holds p.mu.
func (p *Panel) visibleAgents(v viewer) map[string]bool {
	out := map[string]bool{}
	if v.owner {
		for a := range p.agents() {
			out[a] = true
		}
		for a := range p.presence {
			out[a] = true
		}
		return out
	}
	all := p.groups()
	for name := range guestGroups(all, v.friend) {
		for _, m := range all[name].Members {
			out[m] = true
		}
	}
	return out
}

// ownerOnly answers 403 to guests.
func ownerOnly(w http.ResponseWriter, r *http.Request) bool {
	if viewerFrom(r.Context()).owner {
		return true
	}
	writeJSON(w, 403, map[string]any{"ok": false, "error": "only the hub owner may do this", "code": "owner_only"})
	return false
}

// --------------------------------------------------------------------------- rooms

type roomOut struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"` // private | group | earlier
	Name    string   `json:"name,omitempty"`
	Title   string   `json:"title"`
	Deleted bool     `json:"deleted,omitempty"` // a group that no longer exists; its messages remain
	Members []string `json:"members"`
	Count   int      `json:"count"`
	Pending int      `json:"pending"`
	LastSeq int64    `json:"last_seq"`
}

func (p *Panel) handleRooms(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r.Context())
	all := p.groups()
	see := p.sees(v)
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := map[string]bool{}
	if v.owner {
		ids[RoomPrivate] = true
	}
	for name := range all {
		ids[groupPrefix+name] = true
	}
	for id, list := range p.rooms {
		if len(list) > 0 && (id != RoomEarlier || v.owner) {
			ids[id] = true
		}
	}
	out := []roomOut{}
	for _, id := range sortedKeys(ids) {
		if see(id) {
			out = append(out, p.room(id, all))
		}
	}
	// Private first, then groups by name, then the earlier traffic.
	rank := map[string]int{"private": 0, "group": 1, "earlier": 2}
	slices.SortStableFunc(out, func(a, b roomOut) int { return rank[a.Kind] - rank[b.Kind] })
	writeJSON(w, 200, map[string]any{"ok": true, "owner": p.cfg.Owner, "viewer": viewerJSON(v), "rooms": out, "events_next": p.nextSeq})
}

func viewerJSON(v viewer) map[string]any {
	if v.owner {
		return map[string]any{"role": "owner"}
	}
	return map[string]any{"role": "guest", "handle": v.friend}
}

// room summarizes one room; the caller holds p.mu.
func (p *Panel) room(id string, all map[string]hub.Group) roomOut {
	ro := roomOut{ID: id, Members: []string{}}
	members := map[string]bool{}
	switch {
	case id == RoomPrivate:
		ro.Kind, ro.Title = "private", p.cfg.Owner
		if ro.Title == "" {
			ro.Title = "hub"
		}
		for addr := range p.agents() {
			if o, _, _ := protocol.SplitAddress(addr); o == p.cfg.Owner {
				members[addr] = true
			}
		}
	case id == RoomEarlier:
		ro.Kind, ro.Title = "earlier", "earlier"
	default:
		ro.Kind = "group"
		ro.Name = strings.TrimPrefix(id, groupPrefix)
		ro.Title = ro.Name
		if g, ok := all[ro.Name]; ok {
			for _, m := range g.Members {
				members[m] = true
			}
		} else {
			ro.Deleted = true
		}
	}
	for _, e := range p.rooms[id] {
		if ro.Kind != "group" || ro.Deleted { // a live group lists its members, not past senders
			members[e.From], members[e.To] = true, true
		}
		ro.Count++
		if !e.Acked {
			ro.Pending++
		}
		ro.LastSeq = e.Seq
	}
	ro.Members = sortedKeys(members)
	return ro
}

// --------------------------------------------------------------------------- contacts

type contactAgent struct {
	Address string   `json:"address"`
	Groups  []string `json:"groups"`
	Node    string   `json:"node,omitempty"`
	Local   bool     `json:"local,omitempty"`
	Wake    string   `json:"wake,omitempty"`
	Joined  string   `json:"joined,omitempty"` // came in by itself (POST /join), at this time
}

type person struct {
	Handle string         `json:"handle"`
	Me     bool           `json:"me,omitempty"` // the hub owner
	Login  string         `json:"login,omitempty"`
	Joined string         `json:"joined,omitempty"` // came in by itself; the owner may remove them from the panel
	Agents []contactAgent `json:"agents"`
}

// handleContacts is the address book: the owner, every friend who joined or was registered, and their
// agents with the groups each one is in. A friend appears here as soon as their agent joins, before
// any group.
// A guest sees only the people and agents of the groups they are in, and no nodes or logins.
func (p *Panel) handleContacts(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r.Context())
	all := p.groups()
	mine := guestGroups(all, v.friend)
	groupsOf := map[string][]string{}
	for name, g := range all {
		if !v.owner && !mine[name] {
			continue
		}
		for _, m := range g.Members {
			groupsOf[m] = append(groupsOf[m], name)
		}
	}
	people := map[string]*person{}
	logins := p.people()
	get := func(handle string) *person {
		if pp := people[handle]; pp != nil {
			return pp
		}
		pp := &person{Handle: handle, Me: handle == p.cfg.Owner, Agents: []contactAgent{}}
		if v.owner {
			if l := logins[handle]; l != "" {
				pp.Login = l
			} else if pp.Me && p.Hub != nil {
				pp.Login = p.Hub.SelfLogin
			}
			if p.Hub != nil {
				pp.Joined, _ = p.Hub.Dir.Joined(handle)
			}
		}
		people[handle] = pp
		return pp
	}
	if v.owner {
		get(p.cfg.Owner)
		for handle := range logins {
			get(handle)
		}
	}
	for addr, spec := range p.agents() {
		if !v.owner && len(groupsOf[addr]) == 0 {
			continue
		}
		handle, _, _ := protocol.SplitAddress(addr)
		ca := contactAgent{Address: addr, Groups: groupsOf[addr]}
		if ca.Groups == nil {
			ca.Groups = []string{}
		}
		sort.Strings(ca.Groups)
		if v.owner && spec != nil {
			ca.Node, ca.Local = spec.Node, spec.Local
			if spec.Wake != nil {
				ca.Wake = spec.Wake.Type
			}
			if p.Hub != nil {
				ca.Joined, _ = p.Hub.Dir.JoinedAgentAt(addr)
			}
		}
		pp := get(handle)
		pp.Agents = append(pp.Agents, ca)
	}
	out := make([]*person, 0, len(people))
	for _, pp := range people {
		sort.Slice(pp.Agents, func(i, j int) bool { return pp.Agents[i].Address < pp.Agents[j].Address })
		out = append(out, pp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Me != out[j].Me {
			return out[i].Me
		}
		return out[i].Handle < out[j].Handle
	})
	writeJSON(w, 200, map[string]any{"ok": true, "people": out})
}

// --------------------------------------------------------------------------- groups

type groupOut struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
	Created string   `json:"created"`
	Open    bool     `json:"open,omitempty"`
}

func (p *Panel) handleGroups(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r.Context())
	all := p.groups()
	mine := guestGroups(all, v.friend)
	out := []groupOut{}
	for _, name := range sortedKeys(all) {
		if v.owner || mine[name] {
			out = append(out, groupOut{Name: name, Members: all[name].Members, Created: all[name].Created, Open: v.owner && all[name].Open})
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "groups": out})
}

// groupWrite runs one owner-only change to the groups and tells the open panels.
func (p *Panel) groupWrite(w http.ResponseWriter, r *http.Request, req any, apply func(*hub.Hub) error) {
	if !panelWrite(w, r, req) || !ownerOnly(w, r) {
		return
	}
	if p.Hub == nil {
		writeJSON(w, 503, map[string]any{"ok": false, "error": "groups are not available"})
		return
	}
	if err := apply(p.Hub); err != nil {
		var bad hub.BadRequestError
		code := 500
		if errors.As(err, &bad) {
			code = 400
		}
		writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p.mu.Lock()
	p.push(Event{Type: "groups"})
	p.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (p *Panel) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	p.groupWrite(w, r, &req, func(h *hub.Hub) error {
		req.Name = strings.TrimSpace(req.Name)
		if err := h.Groups.Create(req.Name); err != nil {
			return err
		}
		if len(req.Members) > 0 {
			if _, err := h.ChangeGroup(req.Name, req.Members, nil); err != nil {
				_ = h.Groups.Delete(req.Name)
				return err
			}
		}
		p.logf("group %s created", req.Name)
		return nil
	})
}

func (p *Panel) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	p.groupWrite(w, r, &req, func(h *hub.Hub) error {
		if err := h.Groups.Delete(req.Name); err != nil {
			return err
		}
		p.logf("group %s deleted", req.Name)
		return nil
	})
}

func (p *Panel) handleGroupMembers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	p.groupWrite(w, r, &req, func(h *hub.Hub) error {
		g, err := h.ChangeGroup(req.Name, req.Add, req.Remove)
		if err == nil {
			p.logf("group %s members: %v", req.Name, g.Members)
		}
		return err
	})
}

func (p *Panel) handleGroupOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Open bool   `json:"open"`
	}
	p.groupWrite(w, r, &req, func(h *hub.Hub) error {
		if err := h.Groups.SetOpen(req.Name, req.Open); err != nil {
			return err
		}
		p.logf("group %s open=%v", req.Name, req.Open)
		return nil
	})
}

// handlePersonRemove takes someone who joined by themselves out of the home: their agents leave the
// directory and every group. block also turns their Tailscale login away from now on. Their old
// messages stay.
func (p *Panel) handlePersonRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Handle string `json:"handle"`
		Block  bool   `json:"block"`
	}
	p.groupWrite(w, r, &req, func(h *hub.Hub) error {
		gone, err := h.Dir.Remove(req.Handle, req.Block)
		if err != nil {
			return err
		}
		h.LeaveGroups(gone)
		p.logf("removed %s (block=%v): %v", req.Handle, req.Block, gone)
		return nil
	})
}

// handleAgentRemove takes one agent that joined by itself out of the home.
func (p *Panel) handleAgentRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	p.groupWrite(w, r, &req, func(h *hub.Hub) error {
		if err := h.Dir.RemoveAgent(req.Address); err != nil {
			return err
		}
		h.LeaveGroups([]string{req.Address})
		p.logf("removed agent %s", req.Address)
		return nil
	})
}
