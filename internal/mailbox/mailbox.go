// Package mailbox stores records as inbox/<owner>/<agent>/<id>.json and acked/<owner>/<agent>/<id>.json
// (SPEC.md §5); a bare agent address simply has no owner directory.
package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zjywill/khoomei/internal/protocol"
)

type Mailbox struct {
	home string
	mu   sync.Mutex
	wake chan struct{} // closed and replaced on every store, so waiters can select on it
}

func New(home string) *Mailbox {
	return &Mailbox{home: home, wake: make(chan struct{})}
}

// dir maps an address to a directory; address parts are validated names, so the slash is the only separator.
func (m *Mailbox) dir(kind, agent string) string {
	return filepath.Join(m.home, kind, filepath.FromSlash(agent))
}

func (m *Mailbox) exists(agent, id string) bool {
	for _, k := range []string{"inbox", "acked"} {
		if _, err := os.Stat(filepath.Join(m.dir(k, agent), id+".json")); err == nil {
			return true
		}
	}
	return false
}

// Store writes the envelope into the recipient's inbox and returns the stored record. fresh is false
// when the id was already seen.
func (m *Mailbox) Store(env *protocol.Envelope, peer, login string) (rec protocol.Record, fresh bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exists(env.ToAgent, env.ID) {
		return rec, false, nil
	}
	rec = protocol.Record{Envelope: *env, Received: protocol.Received{At: protocol.Now(), Seq: time.Now().UnixNano(), Peer: peer, Login: login}}
	if err := writeJSON(m.Path("inbox", env.ToAgent, env.ID), rec); err != nil {
		return rec, false, err
	}
	close(m.wake)
	m.wake = make(chan struct{})
	return rec, true, nil
}

// Path is where a record of kind "inbox" or "acked" lives.
func (m *Mailbox) Path(kind, agent, id string) string {
	return filepath.Join(m.dir(kind, agent), id+".json")
}

// Read loads one record, looking in acked/ first and then inbox/, so a concurrent ack does not lose it.
func (m *Mailbox) Read(agent, id string) (protocol.Record, bool, error) {
	var rec protocol.Record
	for _, k := range []string{"acked", "inbox"} {
		b, err := os.ReadFile(m.Path(k, agent, id))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return rec, false, err
		}
		return rec, k == "acked", json.Unmarshal(b, &rec)
	}
	return rec, false, os.ErrNotExist
}

// Scan calls fn for every stored record, pending and acked, with its file size. Unreadable files are skipped.
func (m *Mailbox) Scan(fn func(rec protocol.Record, acked bool, size int64)) error {
	for _, k := range []string{"inbox", "acked"} {
		root := filepath.Join(m.home, k)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var rec protocol.Record
			if json.Unmarshal(b, &rec) == nil && rec.Envelope.ID != "" {
				fn(rec, k == "acked", int64(len(b)))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes records of kind for agent and prunes directories left empty. It returns the ids
// actually removed and the bytes freed.
func (m *Mailbox) Remove(kind, agent string, ids []string) (done []string, freed int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	done = []string{}
	for _, id := range ids {
		p := m.Path(kind, agent, id)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if err := os.Remove(p); err != nil {
			return done, freed, err
		}
		done = append(done, id)
		freed += st.Size()
	}
	// Remove the agent's directory and then its owner's, if they are now empty; os.Remove refuses non-empty ones.
	root := filepath.Join(m.home, kind)
	for d := m.dir(kind, agent); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return done, freed, nil
}

// Pending lists unacknowledged records for agent, oldest first.
func (m *Mailbox) Pending(agent string) ([]protocol.Record, error) {
	entries, err := os.ReadDir(m.dir("inbox", agent))
	if errors.Is(err, os.ErrNotExist) {
		return []protocol.Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []protocol.Record{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.dir("inbox", agent), e.Name()))
		if err != nil {
			continue
		}
		var rec protocol.Record
		if json.Unmarshal(b, &rec) == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Received, out[j].Received
		if a.At != b.At {
			return a.At < b.At
		}
		return a.Seq < b.Seq
	})
	return out, nil
}

// Wait blocks until agent has pending records, the timeout passes, or ctx ends.
func (m *Mailbox) Wait(ctx context.Context, agent string, timeout time.Duration) ([]protocol.Record, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		wake := m.wake
		m.mu.Unlock()
		items, err := m.Pending(agent)
		if err != nil || len(items) > 0 {
			return items, err
		}
		select {
		case <-wake:
		case <-deadline.C:
			return []protocol.Record{}, nil
		case <-ctx.Done():
			return []protocol.Record{}, nil
		}
	}
}

// Ack moves the given ids from inbox to acked and returns the ones actually moved.
func (m *Mailbox) Ack(agent string, ids []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	done := []string{}
	for _, id := range ids {
		src := filepath.Join(m.dir("inbox", agent), id+".json")
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := m.dir("acked", agent)
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return done, err
		}
		if err := os.Rename(src, filepath.Join(dst, id+".json")); err != nil {
			return done, err
		}
		done = append(done, id)
	}
	return done, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
