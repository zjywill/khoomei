// Package panel is the hub's web panel (docs/PANEL.md): an in-memory index of the mailboxes grouped
// into rooms, member presence, a live event stream, pruning, and the embedded front end. The hub
// feeds it through hub.Observer and mounts Handler under /admin.
package panel

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/zjywill/khoomei/internal/blobs"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

const (
	eventRing   = 2000
	onlineGrace = 90 * time.Second // a caller between two /wait calls, or a routine mid-run, still counts as online
)

type Panel struct {
	cfg   *config.Config
	box   *mailbox.Mailbox
	blobs *blobs.Store
	logf  func(string, ...any)

	mu       sync.Mutex
	byKey    map[string]*entry
	rooms    map[string][]*entry
	refs     map[string]int // sha256 → number of indexed messages attaching it
	presence map[string]*presence
	events   []Event // ring of the latest eventRing events, ordered by Seq
	nextSeq  int64
	notify   chan struct{} // closed and replaced on every event
	lastRun  time.Time     // last automatic prune
	lastGone int

	now func() time.Time // tests move the clock
}

type presence struct {
	last    time.Time
	waiting int
}

// Event is one entry of /admin/api/events.
type Event struct {
	Seq     int64    `json:"seq"`
	Type    string   `json:"type"` // message | ack | wake | presence | pruned
	Room    string   `json:"room,omitempty"`
	Record  *Record  `json:"record,omitempty"`
	Agent   string   `json:"agent,omitempty"`
	IDs     []string `json:"ids,omitempty"`
	ID      string   `json:"id,omitempty"`
	ToAgent string   `json:"to_agent,omitempty"`
	Result  string   `json:"result,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	State   string   `json:"state,omitempty"`
}

// Record is a stored record as the panel shows it.
type Record struct {
	Envelope protocol.Envelope `json:"envelope"`
	Received protocol.Received `json:"received"`
	Seq      int64             `json:"seq"`
	Room     string            `json:"room"`
	Acked    bool              `json:"acked"`
	Wake     string            `json:"wake"`
	Gone     []string          `json:"files_gone,omitempty"` // attachments already cleaned up, by sha256
}

// New builds the panel and indexes everything already on disk.
func New(cfg *config.Config, box *mailbox.Mailbox, bl *blobs.Store, logf func(string, ...any)) (*Panel, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := &Panel{cfg: cfg, box: box, blobs: bl, logf: logf, byKey: map[string]*entry{}, rooms: map[string][]*entry{}, refs: map[string]int{},
		presence: map[string]*presence{}, nextSeq: 1, notify: make(chan struct{}), now: time.Now}
	err := box.Scan(func(rec protocol.Record, acked bool, size int64) {
		e := newEntry(rec, p.roomOf(rec.Envelope.FromAgent, rec.Envelope.ToAgent), acked, size)
		if old := p.byKey[e.key()]; old != nil {
			// A crash between write and rename can leave both copies; acked wins.
			old.Acked = old.Acked || acked
			return
		}
		p.add(e)
	})
	return p, err
}

// --------------------------------------------------------------------------- hub.Observer

func (p *Panel) Stored(rec protocol.Record) {
	var size int64
	if st, err := os.Stat(p.box.Path("inbox", rec.Envelope.ToAgent, rec.Envelope.ID)); err == nil {
		size = st.Size()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e := newEntry(rec, p.roomOf(rec.Envelope.FromAgent, rec.Envelope.ToAgent), false, size)
	p.add(e)
	if e.Room != RoomHidden {
		r := p.record(e, rec)
		p.push(Event{Type: "message", Room: e.Room, Record: &r})
	}
}

func (p *Panel) Acked(agent string, ids []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	byRoom := map[string][]string{}
	for _, id := range ids {
		if e := p.byKey[entryKey(agent, id)]; e != nil {
			e.Acked = true
			if e.Room != RoomHidden {
				byRoom[e.Room] = append(byRoom[e.Room], id)
			}
		}
	}
	for _, room := range sortedKeys(byRoom) {
		p.push(Event{Type: "ack", Room: room, Agent: agent, IDs: byRoom[room]})
	}
}

func (p *Panel) Seen(agent string, waiting int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := p.presence[agent]
	if pr == nil {
		pr = &presence{}
		p.presence[agent] = pr
	}
	before := p.state(agent, pr)
	pr.last = p.now()
	pr.waiting = max(pr.waiting+waiting, 0)
	if after := p.state(agent, pr); after != before {
		p.push(Event{Type: "presence", Agent: agent, State: after})
	}
}

func (p *Panel) Woke(env *protocol.Envelope, result, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byKey[entryKey(env.ToAgent, env.ID)]
	if e == nil {
		return
	}
	e.Wake = result
	if e.Room != RoomHidden {
		p.push(Event{Type: "wake", Room: e.Room, ID: e.ID, ToAgent: e.To, Result: result, Detail: detail})
	}
}

// --------------------------------------------------------------------------- presence

// state: online while a /wait is open or the agent touched its mailbox within onlineGrace; sleeping when
// the hub can wake it; offline otherwise. Grace expiry raises no event, so the front end re-polls presence.
func (p *Panel) state(agent string, pr *presence) string {
	if pr != nil && (pr.waiting > 0 || (!pr.last.IsZero() && p.now().Sub(pr.last) < onlineGrace)) {
		return "online"
	}
	if spec := p.cfg.Agents[agent]; spec != nil && spec.Wake != nil {
		return "sleeping"
	}
	return "offline"
}

// --------------------------------------------------------------------------- events

// push appends an event and wakes long-pollers; the caller holds p.mu.
func (p *Panel) push(ev Event) {
	ev.Seq = p.nextSeq
	p.nextSeq++
	p.events = append(p.events, ev)
	if len(p.events) > eventRing {
		p.events = append(p.events[:0:0], p.events[len(p.events)-eventRing:]...)
	}
	close(p.notify)
	p.notify = make(chan struct{})
}

// Since returns the events from seq after onward. reset is true when after has already left the ring.
// With after == 0 it returns no events, only where the stream stands.
func (p *Panel) Since(ctx context.Context, after int64, timeout time.Duration) (events []Event, next int64, reset bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		next, ch := p.nextSeq, p.notify
		switch {
		case after <= 0:
			p.mu.Unlock()
			return []Event{}, next, false
		case after > next:
			p.mu.Unlock()
			return []Event{}, next, true // the hub restarted under the client
		case len(p.events) > 0 && after < p.events[0].Seq:
			p.mu.Unlock()
			return []Event{}, next, true
		}
		i := sort.Search(len(p.events), func(i int) bool { return p.events[i].Seq >= after })
		if i < len(p.events) {
			out := append([]Event{}, p.events[i:]...)
			p.mu.Unlock()
			return out, next, false
		}
		p.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			return []Event{}, next, false
		case <-ctx.Done():
			return []Event{}, next, false
		}
	}
}

// --------------------------------------------------------------------------- helpers

// record pairs an index entry with its stored form; the caller holds p.mu.
func (p *Panel) record(e *entry, rec protocol.Record) Record {
	return Record{Envelope: rec.Envelope, Received: rec.Received, Seq: e.Seq, Room: e.Room, Acked: e.Acked, Wake: e.Wake, Gone: p.gone(rec.Envelope.Files)}
}

// gone lists the attachments no longer on the hub.
func (p *Panel) gone(files []protocol.FileRef) []string {
	var out []string
	for _, f := range files {
		if p.blobs == nil {
			break
		}
		if _, err := p.blobs.Stat(f.SHA256); err != nil {
			out = append(out, f.SHA256)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
