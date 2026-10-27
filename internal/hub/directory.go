package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/protocol"
)

// The directory is the hub's address book (docs/GROUPS.md#报到): who may connect and as which
// agents. It has two sources. config.json holds what the owner wrote with `khoomei owner` / `khoomei
// agent`, read once at start. contacts.json holds the people and agents that joined by themselves
// over POST /join, written by the running hub. Lookups see both; config.json wins on a clash.
const ContactsFile = "contacts.json"

const (
	maxPeople         = 256
	maxAgentsAPerson  = 16
	defaultJoinedRate = 6
)

// Person is someone who joined: a Tailscale login under a handle they picked or were given.
type Person struct {
	Login  string `json:"login"`
	Joined string `json:"joined"`
}

// JoinedAgent is an agent that joined by itself, bound to the node it joined from.
type JoinedAgent struct {
	Node   string `json:"node"`
	Joined string `json:"joined"`
	Wake   *struct {
		URL    string `json:"url"`
		Secret string `json:"secret,omitempty"`
	} `json:"wake,omitempty"`
}

type Directory struct {
	cfg  *config.Config // static after start
	path string
	err  error // contacts.json unreadable: nobody may join, and it is never overwritten

	mu      sync.RWMutex
	people  map[string]*Person
	agents  map[string]*JoinedAgent
	blocked []string // logins refused at the door
}

type contactsFile struct {
	People  map[string]*Person      `json:"people"`
	Agents  map[string]*JoinedAgent `json:"agents"`
	Blocked []string                `json:"blocked,omitempty"`
}

func LoadDirectory(cfg *config.Config) *Directory {
	d := &Directory{cfg: cfg, path: filepath.Join(cfg.Home, ContactsFile), people: map[string]*Person{}, agents: map[string]*JoinedAgent{}}
	b, err := os.ReadFile(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return d
	}
	if err == nil {
		var f contactsFile
		if err = json.Unmarshal(b, &f); err == nil {
			for h, p := range f.People {
				if p != nil && protocol.ValidAgent(h) {
					d.people[h] = p
				}
			}
			for a, ag := range f.Agents {
				if ag != nil && protocol.ValidAddress(a) {
					d.agents[a] = ag
				}
			}
			d.blocked = f.Blocked
			return d
		}
	}
	d.err = fmt.Errorf("%s: %w", d.path, err)
	return d
}

func (d *Directory) Err() error { return d.err }

// save writes contacts.json; the caller holds d.mu for writing.
func (d *Directory) save() error {
	b, err := json.MarshalIndent(contactsFile{People: d.people, Agents: d.agents, Blocked: d.blocked}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}

// --------------------------------------------------------------------------- lookups

// Agent returns the spec of a registered agent, from config.json or from a join; nil if unknown.
func (d *Directory) Agent(addr string) *config.AgentSpec {
	if s := d.cfg.Agents[addr]; s != nil {
		return s
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return joinedSpec(d.agents[addr])
}

func joinedSpec(ag *JoinedAgent) *config.AgentSpec {
	if ag == nil {
		return nil
	}
	s := &config.AgentSpec{Node: ag.Node, WakeRate: defaultJoinedRate}
	if ag.Wake != nil {
		s.Wake = &config.WakeAdapter{Type: "webhook", URL: ag.Wake.URL, Secret: ag.Wake.Secret}
	}
	return s
}

// Agents is every registered agent; the map is a copy.
func (d *Directory) Agents() map[string]*config.AgentSpec {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]*config.AgentSpec, len(d.cfg.Agents)+len(d.agents))
	for a, ag := range d.agents {
		out[a] = joinedSpec(ag)
	}
	maps.Copy(out, d.cfg.Agents)
	return out
}

// Login is the Tailscale login registered for a handle, "" if none.
func (d *Directory) Login(handle string) string {
	if o := d.cfg.Owners[handle]; o != nil {
		return strings.ToLower(o.Login)
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if p := d.people[handle]; p != nil {
		return p.Login
	}
	return ""
}

// People maps every known friend's handle to their login (the hub owner is not among them).
func (d *Directory) People() map[string]string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := map[string]string{}
	for h, p := range d.people {
		out[h] = p.Login
	}
	for h, o := range d.cfg.Owners {
		if o != nil {
			out[h] = strings.ToLower(o.Login)
		}
	}
	delete(out, d.cfg.Owner)
	return out
}

// Joined reports whether handle came in by itself (and so may be removed from the panel), and when.
func (d *Directory) Joined(handle string) (string, bool) {
	if d.cfg.Owners[handle] != nil {
		return "", false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if p := d.people[handle]; p != nil {
		return p.Joined, true
	}
	return "", false
}

// JoinedAgentAt says whether addr came in by itself, and when.
func (d *Directory) JoinedAgentAt(addr string) (string, bool) {
	if d.cfg.Agents[addr] != nil {
		return "", false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if ag := d.agents[addr]; ag != nil {
		return ag.Joined, true
	}
	return "", false
}

// Blocked reports whether login has been shown the door.
func (d *Directory) Blocked(login string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return login != "" && slices.Contains(d.blocked, strings.ToLower(login))
}

// handleOf finds the handle a login already uses; the caller holds d.mu.
func (d *Directory) handleOf(login string) string {
	for h, o := range d.cfg.Owners {
		if o != nil && strings.EqualFold(o.Login, login) {
			return h
		}
	}
	for h, p := range d.people {
		if p.Login == login {
			return h
		}
	}
	return ""
}

// --------------------------------------------------------------------------- joining

// JoinRequest is the body of POST /join.
type JoinRequest struct {
	Agent string `json:"agent"`
	Owner string `json:"owner,omitempty"` // the handle to use; default: taken from the login
	Wake  *struct {
		URL    string `json:"url"`
		Secret string `json:"secret,omitempty"`
	} `json:"wake,omitempty"`
}

// JoinResult says where the agent landed.
type JoinResult struct {
	Address   string `json:"address"`
	Owner     string `json:"owner"`
	HubOwner  string `json:"hub_owner"`
	NewPerson bool   `json:"new_person,omitempty"`
	NewAgent  bool   `json:"new_agent,omitempty"`
}

var handleJunk = regexp.MustCompile(`[^a-z0-9_-]+`)

// handleFromLogin turns alice@github or Alice.Smith@example.com into alice / alice-smith.
func handleFromLogin(login string) string {
	local, _, _ := strings.Cut(strings.ToLower(login), "@")
	h := strings.Trim(handleJunk.ReplaceAllString(strings.ReplaceAll(local, ".", "-"), ""), "-")
	if len(h) > 32 {
		h = h[:32]
	}
	return h
}

// join registers agent for the person logged in as login on node. owner is true when login is the
// hub owner's: the agent then joins the owner's own agents. See docs/GROUPS.md#报到 for the rules.
func (d *Directory) join(login, node string, owner bool, req JoinRequest, now time.Time) (JoinResult, error) {
	if !protocol.ValidAgent(req.Agent) {
		return JoinResult{}, BadRequestError("bad agent name")
	}
	if req.Owner != "" && !protocol.ValidAgent(req.Owner) {
		return JoinResult{}, BadRequestError("bad owner handle")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return JoinResult{}, d.err
	}
	res := JoinResult{HubOwner: d.cfg.Owner}
	stamp := now.UTC().Format(time.RFC3339)
	if d.cfg.Owner == "" && !owner {
		return res, ForbiddenError("this hub has no owner handle (single-owner mode), so it takes no guests; its owner can set one with khoomei setup --owner")
	}

	// Whose agent is it?
	var handle string
	switch {
	case owner:
		handle = d.cfg.Owner
		if req.Owner != "" && req.Owner != handle {
			return res, BadRequestError(fmt.Sprintf("this node is logged in as the hub owner %s; leave --owner out", handle))
		}
	case d.handleOf(login) != "":
		handle = d.handleOf(login)
		if req.Owner != "" && req.Owner != handle {
			return res, BadRequestError(fmt.Sprintf("you joined this hub as %s before; keep that handle", handle))
		}
	default:
		handle = req.Owner
		if handle == "" {
			handle = handleFromLogin(login)
		}
		if !protocol.ValidAgent(handle) {
			return res, BadRequestError("cannot make a handle from your login; pass one with --owner")
		}
		if handle == d.cfg.Owner || d.cfg.Owners[handle] != nil || d.people[handle] != nil {
			return res, &JoinConflict{fmt.Sprintf("the handle %s is taken on this hub; pick another with --owner", handle)}
		}
		if len(d.people) >= maxPeople {
			return res, ForbiddenError("this hub has no room for more people")
		}
		d.people[handle] = &Person{Login: login, Joined: stamp}
		res.NewPerson = true
	}
	res.Owner = handle
	res.Address = protocol.Qualify(req.Agent, handle)

	// Which agent, from where?
	if s := d.cfg.Agents[res.Address]; s != nil {
		if nodeMatches(s.Node, node) {
			return res, nil // registered in config.json already
		}
		return res, &JoinConflict{fmt.Sprintf("%s is registered in the hub's config for node %s; the hub owner changes that with khoomei agent", res.Address, s.Node)}
	}
	ag := d.agents[res.Address]
	if ag == nil {
		n := 0
		for a := range d.agents {
			if o, _, _ := protocol.SplitAddress(a); o == handle {
				n++
			}
		}
		if n >= maxAgentsAPerson {
			return res, ForbiddenError(fmt.Sprintf("at most %d agents per person", maxAgentsAPerson))
		}
		ag = &JoinedAgent{Joined: stamp}
		d.agents[res.Address] = ag
		res.NewAgent = true
	}
	// A rejoin from another node is the same person reinstalling: the binding follows them.
	ag.Node = node
	ag.Wake = nil
	if req.Wake != nil && req.Wake.URL != "" {
		if err := checkWebhookURL(req.Wake.URL); err != nil {
			return res, BadRequestError(err.Error())
		}
		ag.Wake = &struct {
			URL    string `json:"url"`
			Secret string `json:"secret,omitempty"`
		}{req.Wake.URL, req.Wake.Secret}
	}
	if err := d.save(); err != nil {
		return res, err
	}
	return res, nil
}

// JoinConflict is a 409: the handle or agent is taken.
type JoinConflict struct{ msg string }

func (e *JoinConflict) Error() string { return e.msg }

// Remove takes a person who joined, and their agents, out of the directory. block also refuses
// their login from now on, everywhere on the hub. It returns the agents removed.
func (d *Directory) Remove(handle string, block bool) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	if d.cfg.Owners[handle] != nil {
		return nil, BadRequestError(handle + " is registered in the hub's config; remove with khoomei owner " + handle + " --remove")
	}
	p := d.people[handle]
	if p == nil {
		return nil, BadRequestError("no such person " + handle)
	}
	var gone []string
	for a := range d.agents {
		if o, _, _ := protocol.SplitAddress(a); o == handle {
			gone = append(gone, a)
			delete(d.agents, a)
		}
	}
	delete(d.people, handle)
	if block && !slices.Contains(d.blocked, p.Login) {
		d.blocked = append(d.blocked, p.Login)
	}
	if err := d.save(); err != nil {
		return nil, err
	}
	slices.Sort(gone)
	return gone, nil
}

// RemoveAgent takes one agent that joined out of the directory.
func (d *Directory) RemoveAgent(addr string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	if d.cfg.Agents[addr] != nil {
		return BadRequestError(addr + " is registered in the hub's config; remove with khoomei agent " + addr + " --remove")
	}
	if d.agents[addr] == nil {
		return BadRequestError("no such agent " + addr)
	}
	delete(d.agents, addr)
	return d.save()
}
