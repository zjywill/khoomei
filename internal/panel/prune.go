package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zjywill/khoomei/internal/blobs"
	"github.com/zjywill/khoomei/internal/disk"
	"github.com/zjywill/khoomei/internal/protocol"
)

// MinPruneAge protects dedup: the hub recognizes a retried id only while its file exists, and senders
// retry within minutes, so nothing younger than a day is ever removed.
const MinPruneAge = 24 * time.Hour

// PruneRequest selects records to remove. Room and Agent are alternatives; both empty means everything.
type PruneRequest struct {
	Room           string `json:"room"`
	Agent          string `json:"agent"`
	What           string `json:"what"` // acked | pending
	OlderThanHours int    `json:"older_than_hours"`
	DryRun         bool   `json:"dry_run"`
}

type PruneResult struct {
	DryRun bool  `json:"dry_run"`
	Count  int   `json:"count"`
	Bytes  int64 `json:"bytes"`
}

// validate applies the rules of docs/PANEL.md#规则 and normalizes the request.
func (p *Panel) validate(req *PruneRequest) error {
	switch req.What {
	case "acked", "pending":
	default:
		return fmt.Errorf("what must be acked or pending")
	}
	if req.Room != "" && req.Agent != "" {
		return fmt.Errorf("give room or agent, not both")
	}
	if req.Agent != "" {
		if !protocol.ValidAddress(req.Agent) {
			return fmt.Errorf("bad agent")
		}
		req.Agent = protocol.Qualify(req.Agent, p.cfg.Owner)
	}
	if req.What == "pending" {
		o, _, _ := protocol.SplitAddress(req.Agent)
		if req.Agent == "" || o != p.cfg.Owner {
			return fmt.Errorf("pending records can only be pruned for one of %s's own agents", p.ownerName())
		}
	}
	req.OlderThanHours = max(req.OlderThanHours, int(MinPruneAge/time.Hour))
	return nil
}

func (p *Panel) ownerName() string {
	if p.cfg.Owner == "" {
		return "the hub owner"
	}
	return p.cfg.Owner
}

// Prune removes what req selects, or only counts it on a dry run.
func (p *Panel) Prune(req PruneRequest, why string) (PruneResult, error) {
	if err := p.validate(&req); err != nil {
		return PruneResult{}, err
	}
	cutoff := p.now().Add(-time.Duration(req.OlderThanHours) * time.Hour)
	p.mu.Lock()
	var picked []entry
	for _, e := range p.byKey {
		if e.Acked != (req.What == "acked") || !e.received().Before(cutoff) {
			continue
		}
		if (req.Room != "" && e.Room != req.Room) || (req.Agent != "" && e.To != req.Agent) {
			continue
		}
		picked = append(picked, *e)
	}
	p.mu.Unlock()
	return p.remove(picked, req.DryRun, why)
}

// remove deletes the given entries from disk and the index, then tells the open panels.
func (p *Panel) remove(picked []entry, dryRun bool, why string) (PruneResult, error) {
	res := PruneResult{DryRun: dryRun}
	if dryRun {
		for _, e := range picked {
			res.Count++
			res.Bytes += e.Size
		}
		return res, nil
	}
	type group struct{ kind, agent string }
	groups := map[group][]string{}
	for _, e := range picked {
		kind := "inbox"
		if e.Acked {
			kind = "acked"
		}
		g := group{kind, e.To}
		groups[g] = append(groups[g], e.ID)
	}
	gone := map[string]bool{}
	var firstErr error
	for g, ids := range groups {
		done, freed, err := p.box.Remove(g.kind, g.agent, ids)
		for _, id := range done {
			gone[entryKey(g.agent, id)] = true
		}
		res.Count += len(done)
		res.Bytes += freed
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	p.mu.Lock()
	byRoom := map[string][]string{}
	for k := range gone {
		if e := p.byKey[k]; e != nil && e.Room != RoomHidden {
			byRoom[e.Room] = append(byRoom[e.Room], e.ID)
		}
	}
	var maybe []string // attachments of the removed messages
	for k := range gone {
		if e := p.byKey[k]; e != nil {
			maybe = append(maybe, e.Files...)
		}
	}
	p.drop(gone)
	var orphaned []string
	for _, sha := range maybe {
		if p.refs[sha] == 0 && !slices.Contains(orphaned, sha) {
			orphaned = append(orphaned, sha)
		}
	}
	for _, room := range sortedKeys(byRoom) {
		p.push(Event{Type: "pruned", Room: room, IDs: byRoom[room]})
	}
	p.mu.Unlock()
	// A file goes with the last message that attached it.
	for _, sha := range orphaned {
		freed, err := p.blobs.Remove(sha)
		res.Bytes += freed
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if res.Count > 0 || firstErr != nil {
		p.logf("prune (%s): removed %d records, %d bytes; err=%v", why, res.Count, res.Bytes, firstErr)
	}
	return res, firstErr
}

// Retain applies config.retention once: acked records past acked_days, then the oldest acked records
// while acked/ is over max_mb (down to 90% of it). Pending records are never touched.
func (p *Panel) Retain() (PruneResult, error) {
	total := PruneResult{}
	ret := p.cfg.Retention
	if days := ret.AckedDaysOrDefault(); days > 0 {
		res, err := p.Prune(PruneRequest{What: "acked", OlderThanHours: days * 24}, fmt.Sprintf("retention %dd", days))
		total.Count, total.Bytes = res.Count, res.Bytes
		if err != nil {
			return total, err
		}
	}
	if ret.MaxMB > 0 {
		limit := int64(ret.MaxMB) << 20
		p.mu.Lock()
		var acked []entry
		var size int64
		for _, e := range p.byKey {
			if e.Acked {
				acked = append(acked, *e)
				size += e.Size
			}
		}
		p.mu.Unlock()
		if size > limit {
			sort.Slice(acked, func(i, j int) bool { return acked[i].Seq < acked[j].Seq })
			cutoff := p.now().Add(-MinPruneAge)
			var picked []entry
			for _, e := range acked {
				if size <= limit*9/10 || !e.received().Before(cutoff) {
					break
				}
				picked = append(picked, e)
				size -= e.Size
			}
			res, err := p.remove(picked, false, fmt.Sprintf("retention max %d MB", ret.MaxMB))
			total.Count += res.Count
			total.Bytes += res.Bytes
			if err != nil {
				return total, err
			}
		}
	}
	n, freed, err := p.pruneOrphans()
	total.Bytes += freed
	if err != nil {
		return total, err
	}
	if n > 0 {
		p.logf("prune (orphaned files): removed %d files, %d bytes", n, freed)
	}
	p.mu.Lock()
	p.lastRun, p.lastGone = p.now(), total.Count
	p.mu.Unlock()
	return total, nil
}

// pruneOrphans removes files no indexed message attaches, once they are a day old: uploads whose
// message never got sent, and leftovers of an interrupted prune.
func (p *Panel) pruneOrphans() (n int, freed int64, err error) {
	if p.blobs == nil {
		return 0, 0, nil
	}
	cutoff := p.now().Add(-MinPruneAge)
	var stale []string
	err = p.blobs.Each(func(sha string, m blobs.Meta) {
		p.mu.Lock()
		used := p.refs[sha] > 0
		p.mu.Unlock()
		if !used && m.Seen().Before(cutoff) {
			stale = append(stale, sha)
		}
	})
	for _, sha := range stale {
		p.mu.Lock()
		used := p.refs[sha] > 0 // attached again since the scan
		p.mu.Unlock()
		if used {
			continue
		}
		f, rerr := p.blobs.Remove(sha)
		if rerr != nil && err == nil {
			err = rerr
		}
		n++
		freed += f
	}
	return n, freed, err
}

// Run applies retention a minute after start and then every hour until ctx ends.
func (p *Panel) Run(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := p.Retain(); err != nil {
				p.logf("retention: %v", err)
			}
			t.Reset(time.Hour)
		}
	}
}

// --------------------------------------------------------------------------- HTTP

type usage struct {
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
	Oldest string `json:"oldest,omitempty"`
}

type roomUsage struct {
	ID      string `json:"id"`
	Pending usage  `json:"pending"`
	Acked   usage  `json:"acked"`
	Files   usage  `json:"files"` // distinct attachments of this room's messages; a file in two rooms counts in both
}

func (p *Panel) handleStorage(w http.ResponseWriter, r *http.Request) {
	sizes := map[string]int64{}
	var files usage
	if p.blobs != nil {
		_ = p.blobs.Each(func(sha string, m blobs.Meta) {
			sizes[sha] = m.Size
			files.Count++
			files.Bytes += m.Size
		})
	}
	p.mu.Lock()
	seen := map[string]map[string]bool{} // room → attachments counted
	byRoom := map[string]*roomUsage{RoomSelf: {ID: RoomSelf}}
	oldest := map[string]int64{}
	for _, e := range p.byKey {
		ru := byRoom[e.Room]
		if ru == nil {
			ru = &roomUsage{ID: e.Room}
			byRoom[e.Room] = ru
		}
		u, k := &ru.Pending, e.Room+"/pending"
		if e.Acked {
			u, k = &ru.Acked, e.Room+"/acked"
		}
		u.Count++
		u.Bytes += e.Size
		if o, ok := oldest[k]; !ok || e.Seq < o {
			oldest[k] = e.Seq
			u.Oldest = e.received().UTC().Format(time.RFC3339)
		}
		for _, sha := range e.Files {
			if seen[e.Room] == nil {
				seen[e.Room] = map[string]bool{}
			}
			if size, ok := sizes[sha]; ok && !seen[e.Room][sha] {
				seen[e.Room][sha] = true
				ru.Files.Count++
				ru.Files.Bytes += size
			}
		}
	}
	rooms := []roomUsage{*byRoom[RoomSelf]} // self first, then friends by name, then the hidden rest
	for _, id := range sortedKeys(byRoom) {
		if id != RoomSelf && id != RoomHidden {
			rooms = append(rooms, *byRoom[id])
		}
	}
	if h := byRoom[RoomHidden]; h != nil {
		rooms = append(rooms, *h)
	}
	ret := map[string]any{"acked_days": p.cfg.Retention.AckedDaysOrDefault(), "max_mb": p.cfg.Retention.MaxMB, "last_pruned": p.lastGone}
	if !p.lastRun.IsZero() {
		ret["last_run"] = p.lastRun.UTC().Format(time.RFC3339)
	}
	p.mu.Unlock()
	free, total := disk.Space(p.cfg.Home)
	writeJSON(w, 200, map[string]any{"ok": true, "disk": map[string]any{"free_bytes": free, "total_bytes": total},
		"retention": ret, "rooms": rooms, "files": files})
}

// handlePrune is the panel's only write. Besides the hub's Host check it insists on a JSON
// body and the X-Khoomei-Panel header, which a cross-site page cannot send without a CORS preflight
// that the hub never answers, and on an Origin, when present, that matches Host.
func (p *Panel) handlePrune(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("X-Khoomei-Panel") != "1" {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "prune needs Content-Type: application/json and X-Khoomei-Panel: 1"})
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeJSON(w, 403, map[string]any{"ok": false, "error": "cross-origin request"})
			return
		}
	}
	var req PruneRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	if req.Room == RoomHidden && req.What != "acked" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "hidden records can only be pruned once acked"})
		return
	}
	if check := req; p.validate(&check) != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": p.validate(&check).Error()})
		return
	}
	res, err := p.Prune(req, "manual")
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "dry_run": res.DryRun, "count": res.Count, "bytes": res.Bytes})
}
