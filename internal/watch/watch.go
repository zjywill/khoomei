// Package watch is the caller-side wake loop: long-poll the hub and run a local command when new
// messages arrive. It is how an agent without a webhook (a coding agent on a laptop) gets woken.
package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/protocol"
)

type Watcher struct {
	Client  *client.Client
	Agent   string
	Command string        // run with sh -c; the pending messages arrive as JSON on stdin
	Poll    time.Duration // hub-side long-poll length
	Repeat  time.Duration // how long to stay quiet while already-fired messages remain unacked
	Logf    func(string, ...any)
	// Handler overrides command execution (tests).
	Handler func(ctx context.Context, items []protocol.Record) error
}

// Run loops until ctx ends. The command fires once per batch of previously unseen message ids;
// messages the command leaves unacknowledged do not re-fire until Repeat has passed.
func (w *Watcher) Run(ctx context.Context) error {
	fired := map[string]time.Time{}
	quiet := false // true while every pending message has already been fired
	for ctx.Err() == nil {
		var resp *client.Response
		var err error
		if quiet {
			// Wait would return at once while anything is pending, so poll gently instead; the
			// interval keeps a new message's latency small compared with Repeat.
			if !sleep(ctx, w.quietInterval()) {
				return nil
			}
			resp, err = w.Client.Inbox(ctx, w.Agent)
		} else {
			resp, err = w.Client.Wait(ctx, w.Agent, w.Poll)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.Logf("wait failed: %v; retrying in 10s", err)
			if !sleep(ctx, 10*time.Second) {
				return nil
			}
			continue
		}
		if !resp.OK {
			w.Logf("hub refused: %s", resp.Error)
			if !sleep(ctx, 30*time.Second) {
				return nil
			}
			continue
		}
		pending := map[string]bool{}
		var fresh []protocol.Record
		now := time.Now()
		for _, rec := range resp.Messages {
			id := rec.Envelope.ID
			pending[id] = true
			if at, seen := fired[id]; !seen || now.Sub(at) >= w.Repeat {
				fresh = append(fresh, rec)
			}
		}
		for id := range fired { // forget acked messages
			if !pending[id] {
				delete(fired, id)
			}
		}
		quiet = len(fresh) == 0 && len(resp.Messages) > 0
		if len(fresh) == 0 {
			continue
		}
		for _, rec := range fresh {
			fired[rec.Envelope.ID] = now
		}
		w.Logf("%d new message(s) for %s; running handler", len(fresh), w.Agent)
		if err := w.run(ctx, fresh); err != nil {
			w.Logf("handler failed: %v", err)
		}
	}
	return nil
}

func (w *Watcher) run(ctx context.Context, items []protocol.Record) error {
	if w.Handler != nil {
		return w.Handler(ctx, items)
	}
	body, _ := json.Marshal(items)
	cmd := exec.CommandContext(ctx, "sh", "-c", w.Command)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "KHOOMEI_AGENT="+w.Agent, "KHOOMEI_COUNT="+itoa(len(items)), "KHOOMEI_MSG_ID="+items[0].Envelope.ID)
	return cmd.Run()
}

func (w *Watcher) quietInterval() time.Duration {
	d := w.Repeat / 4
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	if d < 200*time.Millisecond {
		d = 200 * time.Millisecond
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
