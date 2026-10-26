package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/protocol"
)

// Handler serves /admin: the front end and /admin/api/*. The hub's admin check runs before it, the
// password gate right after.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/auth", p.handleAuth)
	mux.HandleFunc("POST /admin/api/auth/login", p.handleLogin)
	mux.HandleFunc("POST /admin/api/auth/logout", p.handleLogout)
	mux.HandleFunc("POST /admin/api/auth/password", p.handlePassword)
	mux.HandleFunc("GET /admin/api/rooms", p.handleRooms)
	mux.HandleFunc("GET /admin/api/messages", p.handleMessages)
	mux.HandleFunc("GET /admin/api/message", p.handleMessage)
	mux.HandleFunc("GET /admin/api/presence", p.handlePresence)
	mux.HandleFunc("GET /admin/api/events", p.handleEvents)
	mux.HandleFunc("GET /admin/api/storage", p.handleStorage)
	mux.HandleFunc("POST /admin/api/prune", p.handlePrune)
	mux.HandleFunc("GET /admin/api/blob/{sha}", p.handleBlob)
	mux.HandleFunc("GET /admin/api/contacts", p.handleContacts)
	mux.HandleFunc("GET /admin/api/groups", p.handleGroups)
	mux.HandleFunc("POST /admin/api/groups", p.handleGroupCreate)
	mux.HandleFunc("POST /admin/api/groups/delete", p.handleGroupDelete)
	mux.HandleFunc("POST /admin/api/groups/members", p.handleGroupMembers)
	mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no such api"})
	})
	mux.Handle("/admin/", staticHandler())
	mux.Handle("/admin", staticHandler())
	return p.gate(mux)
}

func (p *Panel) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	room := q.Get("room")
	if room == "" || !p.sees(viewerFrom(r.Context()))(room) {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no such room"})
		return
	}
	member := q.Get("member")
	kinds := map[string]bool{}
	for _, k := range strings.Split(q.Get("kinds"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds[k] = true
		}
	}
	unread := q.Get("unread") == "1"
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)

	p.mu.Lock()
	list := p.rooms[room]
	picked := []*entry{}
	more := false
	for i := len(list) - 1; i >= 0; i-- {
		e := list[i]
		if before > 0 && e.Seq >= before {
			continue
		}
		if member != "" && e.From != member && e.To != member {
			continue
		}
		if len(kinds) > 0 && !kinds[e.Kind] {
			continue
		}
		if unread && e.Acked {
			continue
		}
		if len(picked) == limit {
			more = true
			break
		}
		picked = append(picked, e)
	}
	snap := make([]entry, len(picked))
	for i, e := range picked {
		snap[i] = *e
	}
	p.mu.Unlock()

	out := []Record{}
	for i := len(snap) - 1; i >= 0; i-- { // oldest first, as the timeline draws them
		if rec, ok := p.load(&snap[i]); ok {
			out = append(out, rec)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "room": room, "messages": out, "more": more})
}

// load reads an entry's record from disk; false when it was pruned in the meantime.
func (p *Panel) load(e *entry) (Record, bool) {
	rec, acked, err := p.box.Read(e.To, e.ID)
	if err != nil {
		return Record{}, false
	}
	return Record{Envelope: rec.Envelope, Received: rec.Received, Seq: e.Seq, Room: e.Room, Acked: acked || e.Acked, Wake: e.Wake,
		Gone: p.gone(rec.Envelope.Files)}, true
}

// handleMessage finds one message by id, for reply quotes whose original is not on the loaded page.
func (p *Panel) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !protocol.ValidID(id) {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	see := p.sees(viewerFrom(r.Context()))
	p.mu.Lock()
	var found *entry
	for _, e := range p.byKey {
		if e.ID == id && see(e.Room) && (found == nil || e.Seq < found.Seq) {
			found = e
		}
	}
	var snap entry
	if found != nil {
		snap = *found
	}
	p.mu.Unlock()
	if found == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found or pruned"})
		return
	}
	rec, ok := p.load(&snap)
	if !ok {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found or pruned"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": rec})
}

type presenceOut struct {
	State    string `json:"state"`
	Waiting  int    `json:"waiting"`
	LastSeen string `json:"last_seen,omitempty"`
	Wake     string `json:"wake,omitempty"` // adapter type
}

func (p *Panel) handlePresence(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r.Context())
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]presenceOut{}
	for a := range p.visibleAgents(v) {
		pr := p.presence[a]
		po := presenceOut{State: p.state(a, pr)}
		if pr != nil {
			po.Waiting = pr.waiting
			if !pr.last.IsZero() {
				po.LastSeen = pr.last.UTC().Format(time.RFC3339)
			}
		}
		if spec := p.cfg.Agents[a]; v.owner && spec != nil && spec.Wake != nil {
			po.Wake = spec.Wake.Type
		}
		out[a] = po
	}
	writeJSON(w, 200, map[string]any{"ok": true, "members": out})
}

func (p *Panel) handleEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	timeout := 50.0
	if t := r.URL.Query().Get("timeout"); t != "" {
		timeout, _ = strconv.ParseFloat(t, 64)
	}
	timeout = min(max(timeout, 0), 60)
	events, next, reset := p.Since(r.Context(), after, time.Duration(timeout*float64(time.Second)))
	if v := viewerFrom(r.Context()); !v.owner {
		see := p.sees(v)
		p.mu.Lock()
		agents := p.visibleAgents(v)
		p.mu.Unlock()
		kept := events[:0]
		for _, ev := range events {
			switch {
			case ev.Room != "" && !see(ev.Room):
			case ev.Type == "presence" && !agents[ev.Agent]:
			default:
				kept = append(kept, ev)
			}
		}
		events = kept
	}
	writeJSON(w, 200, map[string]any{"ok": true, "next": next, "reset": reset, "events": events})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Images the panel will show inline, judged by sniffing the bytes, never by the sender's label.
// SVG is not among them: it can carry script.
var inlineImages = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// handleBlob serves an attachment of a message this viewer can see; nothing else, not even whether
// the file exists. ?inline=1 previews it when it is one of inlineImages, otherwise it is always a download.
func (p *Panel) handleBlob(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	notFound := func() { writeJSON(w, 404, map[string]any{"ok": false, "error": "no such file"}) }
	if !protocol.ValidSHA256(sha) || p.blobs == nil {
		notFound()
		return
	}
	see := p.sees(viewerFrom(r.Context()))
	p.mu.Lock()
	visible := false
	for _, e := range p.byKey {
		if see(e.Room) && slices.Contains(e.Files, sha) {
			visible = true
			break
		}
	}
	p.mu.Unlock()
	if !visible {
		notFound()
		return
	}
	path := p.blobs.Path(sha)
	name := r.URL.Query().Get("name")
	if !protocol.ValidFileName(name) {
		name = sha
	}
	if r.URL.Query().Get("inline") == "1" {
		if typ := sniff(path); inlineImages[typ] {
			hub.ServeBlob(w, r, path, sha, name, typ, false)
			return
		}
	}
	hub.ServeBlob(w, r, path, sha, name, "application/octet-stream", true)
}

func sniff(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	return http.DetectContentType(buf[:n])
}
