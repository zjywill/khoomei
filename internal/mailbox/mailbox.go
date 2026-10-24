// Package mailbox stores records as inbox/<agent>/<id>.json and acked/<agent>/<id>.json (SPEC.md §5).
package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
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

func (m *Mailbox) dir(kind, agent string) string { return filepath.Join(m.home, kind, agent) }

func (m *Mailbox) exists(agent, id string) bool {
	for _, k := range []string{"inbox", "acked"} {
		if _, err := os.Stat(filepath.Join(m.dir(k, agent), id+".json")); err == nil {
			return true
		}
	}
	return false
}

// Store writes the envelope into the recipient's inbox. It returns false when the id was already seen.
func (m *Mailbox) Store(env *protocol.Envelope, peer string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exists(env.ToAgent, env.ID) {
		return false, nil
	}
	rec := protocol.Record{Envelope: *env, Received: protocol.Received{At: protocol.Now(), Seq: time.Now().UnixNano(), Peer: peer}}
	if err := writeJSON(filepath.Join(m.dir("inbox", env.ToAgent), env.ID+".json"), rec); err != nil {
		return false, err
	}
	close(m.wake)
	m.wake = make(chan struct{})
	return true, nil
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
