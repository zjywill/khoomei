package panel

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zjywill/khoomei/internal/protocol"
)

// Handler serves /admin: the front end and /admin/api/*. The hub's admin check runs before it.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/rooms", p.handleRooms)
	mux.HandleFunc("GET /admin/api/messages", p.handleMessages)
	mux.HandleFunc("GET /admin/api/message", p.handleMessage)
	mux.HandleFunc("GET /admin/api/presence", p.handlePresence)
	mux.HandleFunc("GET /admin/api/events", p.handleEvents)
	mux.HandleFunc("GET /admin/api/storage", p.handleStorage)
	mux.HandleFunc("POST /admin/api/prune", p.handlePrune)
	mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no such api"})
	})
	mux.Handle("/admin/", staticHandler())
	mux.Handle("/admin", staticHandler())
	return mux
}

type roomOut struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Peer    string   `json:"peer,omitempty"` // the friend's handle in a peer room
	Members []string `json:"members"`
	Count   int      `json:"count"`
	Pending int      `json:"pending"`
	LastSeq int64    `json:"last_seq"`
}

func (p *Panel) handleRooms(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := map[string]bool{RoomSelf: true}
	for h := range p.cfg.Owners {
		ids[peerPrefix+h] = true
	}
	for id := range p.rooms {
		if id != RoomHidden {
			ids[id] = true
		}
	}
	out := []roomOut{}
	for _, id := range sortedKeys(ids) {
		out = append(out, p.room(id))
	}
	// Self first, then friends by name.
	slices.SortStableFunc(out, func(a, b roomOut) int {
		if (a.ID == RoomSelf) != (b.ID == RoomSelf) {
			if a.ID == RoomSelf {
				return -1
			}
			return 1
		}
		return 0
	})
	writeJSON(w, 200, map[string]any{"ok": true, "owner": p.cfg.Owner, "rooms": out, "events_next": p.nextSeq})
}

// room summarizes one room; the caller holds p.mu.
func (p *Panel) room(id string) roomOut {
	owner := p.cfg.Owner
	ro := roomOut{ID: id, Title: owner, Members: []string{}}
	if ro.Title == "" {
		ro.Title = "hub"
	}
	peer, isPeer := strings.CutPrefix(id, peerPrefix)
	if isPeer {
		ro.Peer = peer
		ro.Title += " × " + peer
	}
	members := map[string]bool{}
	for addr := range p.cfg.Agents {
		o, _, _ := protocol.SplitAddress(addr)
		if (!isPeer && o == owner) || (isPeer && o == peer) {
			members[addr] = true
		}
	}
	for _, e := range p.rooms[id] {
		members[e.From], members[e.To] = true, true
		ro.Count++
		if !e.Acked {
			ro.Pending++
		}
		ro.LastSeq = e.Seq
	}
	ro.Members = sortedKeys(members)
	return ro
}

func (p *Panel) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	room := q.Get("room")
	if room == "" || room == RoomHidden {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad room"})
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
	return Record{Envelope: rec.Envelope, Received: rec.Received, Seq: e.Seq, Room: e.Room, Acked: acked || e.Acked, Wake: e.Wake}, true
}

// handleMessage finds one message by id, for reply quotes whose original is not on the loaded page.
func (p *Panel) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !protocol.ValidID(id) {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	p.mu.Lock()
	var found *entry
	for _, e := range p.byKey {
		if e.ID == id && e.Room != RoomHidden && (found == nil || e.Seq < found.Seq) {
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
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]presenceOut{}
	agents := map[string]bool{}
	for a := range p.cfg.Agents {
		agents[a] = true
	}
	for a := range p.presence {
		agents[a] = true
	}
	for a := range agents {
		pr := p.presence[a]
		po := presenceOut{State: p.state(a, pr)}
		if pr != nil {
			po.Waiting = pr.waiting
			if !pr.last.IsZero() {
				po.LastSeen = pr.last.UTC().Format(time.RFC3339)
			}
		}
		if spec := p.cfg.Agents[a]; spec != nil && spec.Wake != nil {
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
	writeJSON(w, 200, map[string]any{"ok": true, "next": next, "reset": reset, "events": events})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
