package panel

import (
	"context"
	"fmt"
	"net/http"
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
	n, freed, err := p.pruneFiles()
	total.Bytes += freed
	if err != nil {
		return total, err
	}
	if n > 0 {
		p.logf("prune (files): removed %d files, %d bytes", n, freed)
	}
	p.mu.Lock()
	p.lastRun, p.lastGone = p.now(), total.Count
	p.mu.Unlock()
	return total, nil
}

// fileState is how the index sees one stored file.
type fileState struct {
	sha      string
	seen     time.Time
	size     int64
	refs     int  // messages attaching it
	allAcked bool // every one of them is acked
}

// pruneFiles applies the file rules of docs/FILES.md#清理, independent of the text's retention:
//   - no message attaches it and it is a day old (an upload whose message never went out);
//   - every message attaching it is acked, and it is older than blob.keep_hours: the recipients
//     fetch before they ack, so after that nobody needs the bytes;
//   - while all files together exceed blob.total_mb, the oldest fully acked ones go first.
//
// Files of pending messages are never removed here; when they alone fill the cap, uploads get 507.
// It also sweeps crash leftovers older than a day.
func (p *Panel) pruneFiles() (n int, freed int64, err error) {
	if p.blobs == nil {
		return 0, 0, nil
	}
	now := p.now()
	orphanCutoff, keepCutoff := now.Add(-MinPruneAge), now.Add(-p.cfg.Blob.Keep())
	n, freed = p.blobs.Sweep(orphanCutoff)

	var files []fileState
	var used int64
	err = p.blobs.Each(func(sha string, m blobs.Meta) {
		files = append(files, fileState{sha: sha, seen: m.Seen(), size: m.Size})
		used += m.Size
	})
	if err != nil {
		return n, freed, err
	}
	p.mu.Lock()
	for i := range files {
		files[i].refs, files[i].allAcked = p.fileRefs(files[i].sha)
	}
	p.mu.Unlock()

	remove := func(f fileState) {
		p.mu.Lock()
		refs, allAcked := p.fileRefs(f.sha) // the index may have moved since the scan
		p.mu.Unlock()
		if refs != f.refs || allAcked != f.allAcked {
			return
		}
		got, rerr := p.blobs.Remove(f.sha)
		if rerr != nil {
			if err == nil {
				err = rerr
			}
			return
		}
		n++
		freed += got
		used -= got
	}
	var acked []fileState
	for _, f := range files {
		switch {
		case f.refs == 0 && f.seen.Before(orphanCutoff):
			remove(f)
		case f.refs > 0 && f.allAcked && f.seen.Before(keepCutoff):
			remove(f)
		case f.refs > 0 && f.allAcked:
			acked = append(acked, f)
		}
	}
	if limit := p.cfg.Blob.TotalBytes(); used > limit {
		sort.Slice(acked, func(i, j int) bool { return acked[i].seen.Before(acked[j].seen) })
		for _, f := range acked {
			if used <= limit*9/10 {
				break
			}
			remove(f)
		}
	}
	return n, freed, err
}

// fileRefs counts the messages attaching sha and whether all of them are acked; the caller holds p.mu.
func (p *Panel) fileRefs(sha string) (refs int, allAcked bool) {
	if p.refs[sha] == 0 {
		return 0, false
	}
	allAcked = true
	for _, e := range p.byKey {
		if slices.Contains(e.Files, sha) {
			refs++
			allAcked = allAcked && e.Acked
		}
	}
	return refs, allAcked
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
	if !ownerOnly(w, r) {
		return
	}
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
	byRoom := map[string]*roomUsage{RoomPrivate: {ID: RoomPrivate}}
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
	rooms := []roomUsage{*byRoom[RoomPrivate]} // private first, then groups by name, then earlier and hidden
	for _, id := range sortedKeys(byRoom) {
		if strings.HasPrefix(id, groupPrefix) {
			rooms = append(rooms, *byRoom[id])
		}
	}
	for _, id := range []string{RoomEarlier, RoomHidden} {
		if u := byRoom[id]; u != nil {
			rooms = append(rooms, *u)
		}
	}
	ret := map[string]any{"acked_days": p.cfg.Retention.AckedDaysOrDefault(), "max_mb": p.cfg.Retention.MaxMB, "last_pruned": p.lastGone}
	if !p.lastRun.IsZero() {
		ret["last_run"] = p.lastRun.UTC().Format(time.RFC3339)
	}
	p.mu.Unlock()
	free, total := disk.Space(p.cfg.Home)
	writeJSON(w, 200, map[string]any{"ok": true, "disk": map[string]any{"free_bytes": free, "total_bytes": total},
		"retention": ret, "rooms": rooms, "files": files,
		"blob": map[string]any{"total_mb": p.cfg.Blob.TotalBytes() >> 20, "keep_hours": int(p.cfg.Blob.Keep().Hours()), "max_mb": p.cfg.Blob.MaxBytes() >> 20}})
}

// handlePrune: besides the hub's Host check (and the password, when one is set) it follows the
// rules of every panel write, see panelWrite.
func (p *Panel) handlePrune(w http.ResponseWriter, r *http.Request) {
	var req PruneRequest
	if !panelWrite(w, r, &req) || !ownerOnly(w, r) {
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
