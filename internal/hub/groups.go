package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/zjywill/khoomei/internal/protocol"
)

// Groups are the hub owner's shared spaces (docs/GROUPS.md). The owner's own agents always reach
// each other (the private space); an agent of a friend reaches an agent only when both are members
// of one group. The owner creates groups and picks every member, friends' agents included.
//
// Groups live in <home>/groups.json, apart from config.json: the panel and `khoomei group` change
// them while the hub runs, and the change applies to the next message.
const GroupsFile = "groups.json"

type Group struct {
	Members []string `json:"members"` // full agent addresses, sorted
	Created string   `json:"created"`
}

type Groups struct {
	path string
	err  error // the file could not be read: no group exists, and nothing may overwrite it

	mu sync.RWMutex
	m  map[string]*Group
}

// LoadGroups reads <home>/groups.json; a missing file is an empty set.
func LoadGroups(home string) *Groups {
	g := &Groups{path: filepath.Join(home, GroupsFile), m: map[string]*Group{}}
	b, err := os.ReadFile(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return g
	}
	if err == nil {
		var file struct {
			Groups map[string]*Group `json:"groups"`
		}
		if err = json.Unmarshal(b, &file); err == nil {
			for name, gr := range file.Groups {
				if gr != nil && protocol.ValidAgent(name) {
					g.m[name] = gr
				}
			}
			return g
		}
	}
	g.err = fmt.Errorf("%s: %w", g.path, err)
	return g
}

// Err is the load error, if any; the hub logs it at start.
func (g *Groups) Err() error { return g.err }

// save writes the file; the caller holds g.mu for writing.
func (g *Groups) save() error {
	b, err := json.MarshalIndent(map[string]any{"groups": g.m}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o700); err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

// All returns a copy of every group by name.
func (g *Groups) All() map[string]Group {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]Group, len(g.m))
	for name, gr := range g.m {
		out[name] = Group{Members: slices.Clone(gr.Members), Created: gr.Created}
	}
	return out
}

func (g *Groups) Get(name string) (Group, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	gr, ok := g.m[name]
	if !ok {
		return Group{}, false
	}
	return Group{Members: slices.Clone(gr.Members), Created: gr.Created}, true
}

// Of lists the groups addr is a member of, by name.
func (g *Groups) Of(addr string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []string
	for name, gr := range g.m {
		if slices.Contains(gr.Members, addr) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Shared lists the groups both a and b are members of.
func (g *Groups) Shared(a, b string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []string
	for name, gr := range g.m {
		if slices.Contains(gr.Members, a) && slices.Contains(gr.Members, b) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (g *Groups) Create(name string) error {
	if !protocol.ValidAgent(name) {
		return BadRequestError("bad group name: 1–64 characters, no /")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	if g.m[name] != nil {
		return BadRequestError("group " + name + " already exists")
	}
	g.m[name] = &Group{Members: []string{}, Created: time.Now().UTC().Format(time.RFC3339)}
	if err := g.save(); err != nil {
		delete(g.m, name)
		return err
	}
	return nil
}

// Delete removes the group. Its messages stay; only the hub owner still sees them.
func (g *Groups) Delete(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	old := g.m[name]
	if old == nil {
		return BadRequestError("no group " + name)
	}
	delete(g.m, name)
	if err := g.save(); err != nil {
		g.m[name] = old
		return err
	}
	return nil
}

// Change adds and removes members; the caller has checked that added addresses are registered.
func (g *Groups) Change(name string, add, remove []string) (Group, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return Group{}, g.err
	}
	gr := g.m[name]
	if gr == nil {
		return Group{}, BadRequestError("no group " + name)
	}
	old := slices.Clone(gr.Members)
	set := map[string]bool{}
	for _, m := range gr.Members {
		set[m] = true
	}
	for _, m := range add {
		set[m] = true
	}
	for _, m := range remove {
		delete(set, m)
	}
	gr.Members = make([]string, 0, len(set))
	for m := range set {
		gr.Members = append(gr.Members, m)
	}
	sort.Strings(gr.Members)
	if err := g.save(); err != nil {
		gr.Members = old
		return Group{}, err
	}
	return Group{Members: slices.Clone(gr.Members), Created: gr.Created}, nil
}

// --------------------------------------------------------------------------- rules

// IsOwnAgent: addr (already qualified) belongs to the hub owner. In single-owner mode every
// address is bare and so is everyone's.
func (h *Hub) IsOwnAgent(addr string) bool {
	o, _, _ := protocol.SplitAddress(addr)
	return o == h.Cfg.Owner
}

// ChangeGroup checks that every added address is a registered agent, then applies the change.
func (h *Hub) ChangeGroup(name string, add, remove []string) (Group, error) {
	for i, a := range add {
		add[i] = h.Qualify(a)
		if h.Cfg.Agents[add[i]] == nil {
			return Group{}, BadRequestError("unknown agent " + add[i] + "; register it first")
		}
	}
	for i, a := range remove {
		remove[i] = h.Qualify(a)
	}
	return h.Groups.Change(name, add, remove)
}

// route decides where a message lives and whether it may be sent (docs/GROUPS.md#规则):
//   - with a group: both ends must be members of it;
//   - without one, between two of the owner's agents: the private space;
//   - otherwise the two must share a group, exactly one, which the hub writes into the envelope.
func (h *Hub) route(env *protocol.Envelope) error {
	from, to := env.FromAgent, env.ToAgent
	if env.Group != "" {
		g, ok := h.Groups.Get(env.Group)
		if !ok {
			return BadRequestError("no group " + env.Group)
		}
		for _, a := range []string{from, to} {
			if !slices.Contains(g.Members, a) {
				return ForbiddenError(fmt.Sprintf("%s is not a member of group %s", a, env.Group))
			}
		}
		return nil
	}
	if h.IsOwnAgent(from) && h.IsOwnAgent(to) {
		return nil
	}
	shared := h.Groups.Shared(from, to)
	switch len(shared) {
	case 0:
		return ForbiddenError(fmt.Sprintf("%s and %s share no group; the hub owner has to put both in one", from, to))
	case 1:
		env.Group = shared[0]
		return nil
	}
	return BadRequestError(fmt.Sprintf("%s and %s share groups %v; say which one with \"group\"", from, to, shared))
}

// Contact is one address an agent may send to.
type Contact struct {
	Address string   `json:"address"`
	Private bool     `json:"private,omitempty"` // reachable without a group: both are the owner's
	Groups  []string `json:"groups,omitempty"`  // groups the two share
}

// Contacts lists whom addr may message: the owner's other agents when addr is one of them, and
// everyone it shares a group with.
func (h *Hub) Contacts(addr string) []Contact {
	byAddr := map[string]*Contact{}
	get := func(a string) *Contact {
		if c := byAddr[a]; c != nil {
			return c
		}
		c := &Contact{Address: a}
		byAddr[a] = c
		return c
	}
	if h.IsOwnAgent(addr) {
		for a := range h.Cfg.Agents {
			if a != addr && h.IsOwnAgent(a) {
				get(a).Private = true
			}
		}
	}
	for name, g := range h.Groups.All() {
		if !slices.Contains(g.Members, addr) {
			continue
		}
		for _, m := range g.Members {
			if m != addr {
				c := get(m)
				c.Groups = append(c.Groups, name)
			}
		}
	}
	out := make([]Contact, 0, len(byAddr))
	for _, c := range byAddr {
		sort.Strings(c.Groups)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
