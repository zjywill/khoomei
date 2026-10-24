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

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

const Version = "0.2.0"

var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Identity is who a connection is: the hub's own loopback, or a tailnet node.
type Identity struct {
	Local bool
	Node  string // FQDN without trailing dot; empty for Local
}

// Resolver turns a tailnet address into a node name (tsnet's WhoIs in production).
type Resolver interface {
	Resolve(ctx context.Context, ip netip.Addr) (node string, err error)
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
	Resolver Resolver
	Waker    *Waker
	Logf     func(string, ...any)
	// Identify overrides the remote-address path; tests use it to pose as a peer.
	Identify func(r *http.Request) (Identity, error)
}

func New(cfg *config.Config, box *mailbox.Mailbox, res Resolver, logf func(string, ...any)) *Hub {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	h := &Hub{Cfg: cfg, Box: box, Resolver: res, Waker: NewWaker(cfg, logf), Logf: logf}
	h.Identify = h.identifyRemote
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
	node, err := h.Resolver.Resolve(ctx, ip)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			return Identity{}, ErrUnavailable
		}
		return Identity{}, ForbiddenError("unknown peer")
	}
	node = strings.TrimSuffix(node, ".")
	for _, rule := range h.Cfg.Policy.Deny {
		if nodeMatches(rule, node) {
			return Identity{}, ForbiddenError("denied by policy")
		}
	}
	return Identity{Node: node}, nil
}

// nodeMatches: a bound name may be the short host name or the FQDN.
func nodeMatches(bound, node string) bool {
	bound = strings.TrimSuffix(bound, ".")
	return bound == node || bound == strings.SplitN(node, ".", 2)[0] || strings.HasPrefix(node, bound+".")
}

// Authorize: may this identity act as agent? Loopback may act as any local agent; a peer only as
// agents bound to its node. Nobody may act as an unregistered agent except the owner on their own hub.
func (h *Hub) Authorize(id Identity, agent string) error {
	spec := h.Cfg.Agents[agent]
	if id.Local {
		if spec == nil || spec.Local || spec.Node == "" {
			return nil
		}
		return ForbiddenError(fmt.Sprintf("%s is bound to node %s, not to this hub", agent, spec.Node))
	}
	if spec == nil {
		return ForbiddenError(fmt.Sprintf("unknown agent %s; the hub owner must register it", agent))
	}
	if spec.Node == "" {
		return ForbiddenError(fmt.Sprintf("%s has no node binding", agent))
	}
	if !nodeMatches(spec.Node, id.Node) {
		return ForbiddenError(fmt.Sprintf("%s is bound to %s, not %s", agent, spec.Node, id.Node))
	}
	return nil
}

// Receive validates, authorizes and stores an envelope, then wakes the recipient. fresh is false on a duplicate id.
func (h *Hub) Receive(id Identity, env *protocol.Envelope) (fresh bool, err error) {
	if err := env.Validate(); err != nil {
		return false, BadRequestError(err.Error())
	}
	if err := h.Authorize(id, env.FromAgent); err != nil {
		return false, err
	}
	if _, ok := h.Cfg.Agents[env.ToAgent]; !ok {
		return false, BadRequestError("unknown to_agent " + env.ToAgent)
	}
	peer := id.Node
	if id.Local {
		peer = "hub"
	}
	fresh, err = h.Box.Store(env, peer)
	if err != nil {
		return false, err
	}
	if fresh {
		h.Waker.Wake(env)
	}
	return fresh, nil
}

// --------------------------------------------------------------------------- HTTP

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "v": protocol.Version, "version": Version})
	})
	mux.HandleFunc("POST /msg", h.withIdentity(h.handleMsg))
	mux.HandleFunc("GET /inbox", h.withIdentity(h.handleInbox))
	mux.HandleFunc("GET /wait", h.withIdentity(h.handleInbox))
	mux.HandleFunc("POST /ack", h.withIdentity(h.handleAck))
	mux.HandleFunc("GET /agents", h.withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		if !id.Local {
			fail(w, ForbiddenError("owner only"))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "agents": h.Cfg.Agents})
	}))
	return mux
}

func (h *Hub) withIdentity(fn func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := h.Identify(r)
		if err != nil {
			fail(w, err)
			return
		}
		fn(w, r, id)
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
	writeJSON(w, code, map[string]any{"ok": true, "id": env.ID, "duplicate": !fresh})
}

func (h *Hub) handleInbox(w http.ResponseWriter, r *http.Request, id Identity) {
	agent := r.URL.Query().Get("agent")
	if !protocol.ValidAgent(agent) {
		fail(w, BadRequestError("bad agent"))
		return
	}
	if err := h.Authorize(id, agent); err != nil {
		fail(w, err)
		return
	}
	var items []protocol.Record
	var err error
	if r.URL.Path == "/wait" {
		timeout := 50.0
		if t := r.URL.Query().Get("timeout"); t != "" {
			timeout, _ = strconv.ParseFloat(t, 64)
		}
		timeout = min(max(timeout, 0), 120)
		items, err = h.Box.Wait(r.Context(), agent, time.Duration(timeout*float64(time.Second)))
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !protocol.ValidAgent(body.Agent) || len(body.IDs) == 0 {
		fail(w, BadRequestError("bad agent or ids"))
		return
	}
	for _, x := range body.IDs {
		if !protocol.ValidID(x) {
			fail(w, BadRequestError("bad agent or ids"))
			return
		}
	}
	if err := h.Authorize(id, body.Agent); err != nil {
		fail(w, err)
		return
	}
	done, err := h.Box.Ack(body.Agent, body.IDs)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "acked": done})
}

func fail(w http.ResponseWriter, err error) {
	var forbidden ForbiddenError
	var bad BadRequestError
	switch {
	case errors.Is(err, ErrUnavailable):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, 503, map[string]any{"ok": false, "error": ErrUnavailable.Error(), "code": "identity_unavailable"})
	case errors.As(err, &forbidden):
		writeJSON(w, 403, map[string]any{"ok": false, "error": err.Error()})
	case errors.As(err, &bad):
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
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
