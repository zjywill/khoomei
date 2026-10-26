package panel

import (
	"sort"
	"time"

	"github.com/zjywill/khoomei/internal/protocol"
)

// Rooms are where a message shows up (docs/PANEL.md#群怎么分).
const (
	RoomPrivate = "private" // two of the hub owner's agents, no group named: only the owner sees it
	RoomEarlier = "earlier" // a friend at one end, sent before groups existed: only the owner sees it
	RoomHidden  = "hidden"  // friends among themselves, before groups: counted, never shown
	groupPrefix = "group/"  // a group, by the name after the slash: the owner and the group's people see it
)

// entry is what the index keeps per stored record; the text stays on disk.
type entry struct {
	ID, From, To, Kind string
	Seq                int64
	Room               string
	Acked              bool
	Size               int64
	Wake               string   // "", ok, failed or downgraded; only known for messages since the hub started
	Files              []string // sha256 of each attachment
}

func (e *entry) key() string { return e.To + "\x00" + e.ID }

func (e *entry) received() time.Time { return time.Unix(0, e.Seq) }

func entryKey(to, id string) string { return to + "\x00" + id }

// roomOf maps a message to its room: its group when it has one, otherwise by the owners at both
// ends. In single-owner mode every address is bare, so everything without a group is private.
func (p *Panel) roomOf(env protocol.Envelope) string {
	if env.Group != "" {
		return groupPrefix + env.Group
	}
	owner := p.cfg.Owner
	fo, _, _ := protocol.SplitAddress(env.FromAgent)
	tob, _, _ := protocol.SplitAddress(env.ToAgent)
	switch {
	case fo == owner && tob == owner:
		return RoomPrivate
	case fo == owner || tob == owner:
		return RoomEarlier
	}
	return RoomHidden
}

// add inserts e keeping its room ordered by seq; the caller holds p.mu.
func (p *Panel) add(e *entry) {
	if old := p.byKey[e.key()]; old != nil {
		return
	}
	p.byKey[e.key()] = e
	for _, sha := range e.Files {
		p.refs[sha]++
	}
	list := p.rooms[e.Room]
	i := sort.Search(len(list), func(i int) bool { return list[i].Seq > e.Seq })
	list = append(list, nil)
	copy(list[i+1:], list[i:])
	list[i] = e
	p.rooms[e.Room] = list
}

// drop removes the given entries from the index; the caller holds p.mu.
func (p *Panel) drop(gone map[string]bool) {
	for k := range gone {
		if e := p.byKey[k]; e != nil {
			for _, sha := range e.Files {
				if p.refs[sha]--; p.refs[sha] <= 0 {
					delete(p.refs, sha)
				}
			}
		}
		delete(p.byKey, k)
	}
	for room, list := range p.rooms {
		kept := list[:0]
		for _, e := range list {
			if !gone[e.key()] {
				kept = append(kept, e)
			}
		}
		clear(list[len(kept):])
		p.rooms[room] = kept
	}
}

func newEntry(rec protocol.Record, room string, acked bool, size int64) *entry {
	env := rec.Envelope
	seq := rec.Received.Seq
	if seq == 0 { // records written before seq existed
		if t, err := time.Parse(time.RFC3339, rec.Received.At); err == nil {
			seq = t.UnixNano()
		}
	}
	e := &entry{ID: env.ID, From: env.FromAgent, To: env.ToAgent, Kind: env.EffectiveKind(),
		Seq: seq, Room: room, Acked: acked, Size: size}
	for _, f := range env.Files {
		e.Files = append(e.Files, f.SHA256)
	}
	return e
}
