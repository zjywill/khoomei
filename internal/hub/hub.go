// Package hub is the HTTP service of SPEC.md §1–§2: identity, authorization, mailboxes and wake.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/zjywill/khoomei/internal/blobs"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

const Version = "0.7.0"

var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Identity is who a connection is: the hub's own loopback, or a tailnet node and the login it belongs to.
type Identity struct {
	Local bool
	Node  string // FQDN without trailing dot; empty for Local
	Login string // Tailscale login of the node's owner (alice@github); empty when unknown
}

// Peer is what a Resolver learns about a tailnet address.
type Peer struct {
	Node  string
	Login string
}

// Resolver turns a tailnet address into its node and login (tsnet's WhoIs in production).
type Resolver interface {
	Resolve(ctx context.Context, ip netip.Addr) (Peer, error)
}

// ErrUnavailable means identity lookup is temporarily impossible (503 + Retry-After).
var ErrUnavailable = errors.New("identity lookup unavailable")

// ForbiddenError is a 403 with a reason.
type ForbiddenError string

func (e ForbiddenError) Error() string { return string(e) }

// BadRequestError is a 400 with a reason.
type BadRequestError string

func (e BadRequestError) Error() string { return string(e) }

type Hub struct {
	Cfg      *config.Config
	Box      *mailbox.Mailbox
	Blobs    *blobs.Store
	Groups   *Groups
	Dir      *Directory // who may connect: config.json plus contacts.json
	Resolver Resolver
	Waker    *Waker
	Logf     func(string, ...any)
	// Hosts are the names and addresses the hub answers to besides loopback (short node name, FQDN,
	// tailnet IPs); /admin rejects any other Host header. serve fills them in once the node is up.
	Hosts []string
	// SelfLogin is the Tailscale login the hub's own node runs under; serve fills it in. See IsOwner.
	SelfLogin string
	// Observer, when set, hears about every stored record, ack, mailbox visit and wake (docs/PANEL.md).
	Observer Observer
	// Admin serves everything under /admin once withAdmin has let the request through; nil shows a placeholder.
	Admin http.Handler
	// Identify overrides the remote-address path; tests use it to pose as a peer.
	Identify func(r *http.Request) (Identity, error)
}

func New(cfg *config.Config, box *mailbox.Mailbox, res Resolver, logf func(string, ...any)) *Hub {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	h := &Hub{Cfg: cfg, Box: box, Blobs: blobs.New(cfg.Home), Groups: LoadGroups(cfg.Home), Dir: LoadDirectory(cfg), Resolver: res, Waker: NewWaker(cfg, logf), Logf: logf}
	h.Waker.Lookup = h.Dir.Agent
	if err := h.Groups.Err(); err != nil {
		logf("groups unreadable, every group is off until it is fixed: %v", err)
	}
	if err := h.Dir.Err(); err != nil {
		logf("contacts unreadable, nobody can join until it is fixed: %v", err)
	}
	h.Identify = h.identifyRemote
	h.Waker.OnWake = func(env *protocol.Envelope, result, detail string) {
		if h.Observer != nil {
			h.Observer.Woke(env, result, detail)
		}
	}
	return h
}

func (h *Hub) identifyRemote(r *http.Request) (Identity, error) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return Identity{}, ForbiddenError("unparseable remote address")
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() {
		return Identity{Local: true}, nil
	}
	if !tailnetV4.Contains(ip) && !tailnetV6.Contains(ip) {
		return Identity{}, ForbiddenError("not a tailnet address")
	}
	if h.Resolver == nil {
		return Identity{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	peer, err := h.Resolver.Resolve(ctx, ip)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			return Identity{}, ErrUnavailable
		}
		return Identity{}, ForbiddenError("unknown peer")
	}
	id := Identity{Node: strings.TrimSuffix(peer.Node, "."), Login: strings.ToLower(peer.Login)}
	for _, rule := range h.Cfg.Policy.Deny {
		if nodeMatches(rule, id.Node) || (id.Login != "" && strings.EqualFold(rule, id.Login)) {
			return Identity{}, ForbiddenError("denied by policy")
		}
	}
	return id, nil
}

// nodeMatches: a bound name may be the short host name or the FQDN.
func nodeMatches(bound, node string) bool {
	bound = strings.TrimSuffix(bound, ".")
	return bound == node || bound == strings.SplitN(node, ".", 2)[0] || strings.HasPrefix(node, bound+".")
}

// Qualify resolves a bare agent name to the hub owner's handle.
func (h *Hub) Qualify(addr string) string { return protocol.Qualify(addr, h.Cfg.Owner) }

// Authorize: may this identity act as addr (SPEC §1)?
//   - Loopback (the hub owner) may act as any of the owner's local agents, never as a remote one.
//   - A peer may act only as an agent bound to its node, and when that agent belongs to an owner with a
//     registered login, the node's login must match. Agents of other owners always need such a login.
func (h *Hub) Authorize(id Identity, addr string) error {
	addr = h.Qualify(addr)
	owner, _, _ := protocol.SplitAddress(addr)
	spec := h.Dir.Agent(addr)
	if id.Local {
		if owner != h.Cfg.Owner {
			return ForbiddenError(fmt.Sprintf("%s belongs to %s, not to this hub's owner", addr, owner))
		}
		if spec == nil || spec.Local || spec.Node == "" {
			return nil
		}
		return ForbiddenError(fmt.Sprintf("%s is bound to node %s, not to this hub", addr, spec.Node))
	}
	if spec == nil {
		return ForbiddenError(fmt.Sprintf("unknown agent %s; join this hub first (khoomei join)", addr))
	}
	if spec.Node == "" {
		return ForbiddenError(fmt.Sprintf("%s has no node binding", addr))
	}
	if !nodeMatches(spec.Node, id.Node) {
		return ForbiddenError(fmt.Sprintf("%s is bound to %s, not %s", addr, spec.Node, id.Node))
	}
	wantLogin := h.Dir.Login(owner)
	if owner != h.Cfg.Owner && wantLogin == "" {
		return ForbiddenError(fmt.Sprintf("owner %s has no registered login", owner))
	}
	if wantLogin != "" && id.Login != wantLogin {
		return ForbiddenError(fmt.Sprintf("%s belongs to %s, but the node is logged in as %q", addr, owner, id.Login))
	}
	return nil
}

// Receive validates, authorizes and stores an envelope, then wakes the recipient. Bare names in the
// envelope are qualified to the hub owner before anything else. fresh is false on a duplicate id.
func (h *Hub) Receive(id Identity, env *protocol.Envelope) (fresh bool, err error) {
	if err := env.Validate(); err != nil {
		return false, BadRequestError(err.Error())
	}
	env.FromAgent, env.ToAgent = h.Qualify(env.FromAgent), h.Qualify(env.ToAgent)
	if err := h.Authorize(id, env.FromAgent); err != nil {
		return false, err
	}
	if h.Dir.Agent(env.ToAgent) == nil {
		return false, BadRequestError("unknown to_agent " + env.ToAgent)
	}
	if err := h.route(env); err != nil {
		return false, err
	}
	if err := h.checkFiles(env); err != nil {
		return false, err
	}
	peer := id.Node
	if id.Local {
		peer = "hub"
	}
	rec, fresh, err := h.Box.Store(env, peer, id.Login)
	if err != nil {
		return false, err
	}
	if fresh {
		if len(env.Files) > 0 {
			if err := h.Blobs.Attach(env.Files, env.FromAgent, env.ToAgent); err != nil {
				h.Logf("attach files of %s: %v", env.ID, err)
			}
		}
		if h.Observer != nil {
			h.Observer.Stored(rec)
		}
		h.Waker.Wake(env)
	}
	return fresh, nil
}

// Observer is told what happens in the mailboxes. Calls are synchronous and must be quick.
type Observer interface {
	Stored(rec protocol.Record)                         // a fresh record landed in an inbox
	Acked(agent string, ids []string)                   // these ids actually moved to acked/
	Seen(agent string, waiting int)                     // agent visited its mailbox; waiting: +1 entering /wait, -1 leaving, 0 otherwise
	Woke(env *protocol.Envelope, result, detail string) // result: ok | failed | downgraded
	Joined(res JoinResult)                              // an agent joined by itself (or rejoined)
}

func (h *Hub) seen(agent string, waiting int) {
	if h.Observer != nil {
		h.Observer.Seen(agent, waiting)
	}
}

// --------------------------------------------------------------------------- HTTP

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "v": protocol.Version, "version": Version, "owner": h.Cfg.Owner, "features": []string{"blob", "groups", "join"}})
	})
	mux.HandleFunc("POST /msg", h.withIdentity(h.handleMsg))
	mux.HandleFunc("GET /inbox", h.withIdentity(h.handleInbox))
	mux.HandleFunc("GET /wait", h.withIdentity(h.handleInbox))
	mux.HandleFunc("POST /ack", h.withIdentity(h.handleAck))
	mux.HandleFunc("PUT /blob", h.withIdentity(h.handleBlobPut))
	mux.HandleFunc("GET /blob/{sha}", h.withIdentity(h.handleBlobGet)) // GET patterns also answer HEAD
	mux.HandleFunc("GET /contacts", h.withIdentity(h.handleContacts))
	mux.HandleFunc("POST /join", h.withIdentity(h.handleJoin))
	mux.HandleFunc("/admin", h.withAdmin(h.handleAdmin))
	mux.HandleFunc("/admin/", h.withAdmin(h.handleAdmin))
	mux.HandleFunc("GET /agents", h.withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		if !id.Local {
			fail(w, ForbiddenError("owner only"))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "owner": h.Cfg.Owner, "owners": h.Dir.People(), "agents": h.Dir.Agents()})
	}))
	return mux
}

// statusWriter records the status code so every request can be logged in one line.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (h *Hub) withIdentity(fn func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		id, err := h.Identify(r)
		if err == nil && !id.Local && h.Dir.Blocked(id.Login) {
			err = ForbiddenError("the hub owner has blocked " + id.Login)
		}
		who := "?"
		if err != nil {
			fail(sw, err)
		} else {
			who = id.Node
			if id.Local {
				who = "local"
			}
			if id.Login != "" {
				who += " (" + id.Login + ")"
			}
			fn(sw, r, id)
		}
		h.Logf("%s %s %s from %s -> %d in %s", r.Method, r.URL.Path, r.URL.RawQuery, who, sw.code, time.Since(start).Round(time.Millisecond))
	}
}

func (h *Hub) handleMsg(w http.ResponseWriter, r *http.Request, id Identity) {
	if r.ContentLength > protocol.MaxBody {
		writeJSON(w, 413, map[string]any{"ok": false, "error": "too large"})
		return
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, 415, map[string]any{"ok": false, "error": "expected application/json"})
		return
	}
	env, err := protocol.Decode(r.Body)
	if err != nil {
		fail(w, BadRequestError(err.Error()))
		return
	}
	fresh, err := h.Receive(id, env)
	if err != nil {
		fail(w, err)
		return
	}
	code := 200
	if fresh {
		code = 202
	}
	writeJSON(w, code, map[string]any{"ok": true, "id": env.ID, "duplicate": !fresh, "to_agent": env.ToAgent})
}

func (h *Hub) handleInbox(w http.ResponseWriter, r *http.Request, id Identity) {
	agent := r.URL.Query().Get("agent")
	if !protocol.ValidAddress(agent) {
		fail(w, BadRequestError("bad agent"))
		return
	}
	agent = h.Qualify(agent)
	if err := h.Authorize(id, agent); err != nil {
		fail(w, err)
		return
	}
	var items []protocol.Record
	var err error
	h.seen(agent, 0)
	if r.URL.Path == "/wait" {
		timeout := 50.0
		if t := r.URL.Query().Get("timeout"); t != "" {
			timeout, _ = strconv.ParseFloat(t, 64)
		}
		timeout = min(max(timeout, 0), 120)
		h.seen(agent, +1)
		items, err = h.Box.Wait(r.Context(), agent, time.Duration(timeout*float64(time.Second)))
		h.seen(agent, -1)
	} else {
		items, err = h.Box.Pending(agent)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "agent": agent, "messages": items})
}

func (h *Hub) handleAck(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Agent string   `json:"agent"`
		IDs   []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !protocol.ValidAddress(body.Agent) || len(body.IDs) == 0 {
		fail(w, BadRequestError("bad agent or ids"))
		return
	}
	for _, x := range body.IDs {
		if !protocol.ValidID(x) {
			fail(w, BadRequestError("bad agent or ids"))
			return
		}
	}
	agent := h.Qualify(body.Agent)
	if err := h.Authorize(id, agent); err != nil {
		fail(w, err)
		return
	}
	done, err := h.Box.Ack(agent, body.IDs)
	if err != nil {
		fail(w, err)
		return
	}
	h.seen(agent, 0)
	if h.Observer != nil && len(done) > 0 {
		h.Observer.Acked(agent, done)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "acked": done})
}

// handleContacts answers whom an agent may message, with the groups each one shares with it.
func (h *Hub) handleContacts(w http.ResponseWriter, r *http.Request, id Identity) {
	agent := h.Qualify(r.URL.Query().Get("agent"))
	if !protocol.ValidAddress(agent) {
		fail(w, BadRequestError("bad agent"))
		return
	}
	if err := h.Authorize(id, agent); err != nil {
		fail(w, err)
		return
	}
	groups := h.Groups.Of(agent)
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "agent": agent, "groups": groups, "contacts": h.Contacts(agent)})
}

// Join lets an agent register itself (docs/GROUPS.md#报到). The hub owner's invitation is the
// Tailscale share that let this node reach the hub at all; WhoIs says who it is. A new agent also
// lands in every open group.
func (h *Hub) Join(id Identity, req JoinRequest) (JoinResult, error) {
	if id.Local {
		return JoinResult{}, BadRequestError("agents on the hub machine are registered with khoomei agent NAME --local")
	}
	login := strings.ToLower(id.Login)
	if login == "" || login == taggedLogin {
		return JoinResult{}, ForbiddenError("this node is tagged, so it belongs to no person and cannot join; give khoomei an auth key without tags")
	}
	res, err := h.Dir.join(login, id.Node, h.IsOwner(id), req, time.Now())
	if err != nil {
		return res, err
	}
	if res.NewAgent {
		for _, g := range h.Groups.OpenGroups() {
			if _, err := h.Groups.Change(g, []string{res.Address}, nil); err != nil {
				h.Logf("add %s to open group %s: %v", res.Address, g, err)
			}
		}
	}
	h.Logf("join: %s from %s (%s) new_person=%v new_agent=%v", res.Address, id.Node, login, res.NewPerson, res.NewAgent)
	if h.Observer != nil {
		h.Observer.Joined(res)
	}
	return res, nil
}

func (h *Hub) handleJoin(w http.ResponseWriter, r *http.Request, id Identity) {
	var req JoinRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		fail(w, BadRequestError("invalid json"))
		return
	}
	res, err := h.Join(id, req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "address": res.Address, "owner": res.Owner, "hub_owner": res.HubOwner,
		"new_person": res.NewPerson, "new_agent": res.NewAgent, "groups": nonNil(h.Groups.Of(res.Address))})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func fail(w http.ResponseWriter, err error) {
	var forbidden ForbiddenError
	var bad BadRequestError
	var conflict *JoinConflict
	switch {
	case errors.Is(err, ErrUnavailable):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, 503, map[string]any{"ok": false, "error": ErrUnavailable.Error(), "code": "identity_unavailable"})
	case errors.As(err, &forbidden):
		writeJSON(w, 403, map[string]any{"ok": false, "error": err.Error()})
	case errors.As(err, &bad):
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
	case errors.As(err, &conflict):
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error(), "code": "taken"})
	default:
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
