package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

func TestFiresOncePerMessageUntilRepeat(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	cfg.Role = config.RoleHub
	cfg.Agents = map[string]*config.AgentSpec{"grok": {Local: true}, "claude": {Local: true}}
	h := hub.New(cfg, mailbox.New(cfg.Home), nil, t.Logf)
	h.Identify = func(*http.Request) (hub.Identity, error) { return hub.Identity{Local: true}, nil }
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	fired := make(chan []protocol.Record, 10)
	w := &Watcher{Client: c, Agent: "claude", Poll: 2 * time.Second, Repeat: 1500 * time.Millisecond, Logf: t.Logf,
		Handler: func(_ context.Context, items []protocol.Record) error { fired <- items; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(200 * time.Millisecond)
	first := protocol.New("grok", "claude", "one")
	c.Send(ctx, first)
	batch := <-fired
	if len(batch) != 1 || batch[0].Envelope.Text != "one" {
		t.Fatalf("first fire: %+v", batch)
	}

	// Unacked message must not refire immediately; a new message fires alone.
	select {
	case b := <-fired:
		t.Fatalf("refired too early: %+v", b)
	case <-time.After(500 * time.Millisecond):
	}
	second := protocol.New("grok", "claude", "two")
	c.Send(ctx, second)
	batch = <-fired
	if len(batch) != 1 || batch[0].Envelope.Text != "two" {
		t.Fatalf("second fire should carry only the new message: %+v", batch)
	}

	// Once Repeat has passed, the still-unacked "one" fires again on its own schedule; after ack, silence.
	batch = <-fired
	if len(batch) != 1 || batch[0].Envelope.Text != "one" {
		t.Fatalf("repeat fire should carry the message whose Repeat elapsed: %+v", batch)
	}
	c.Ack(ctx, "claude", []string{first.ID, second.ID})
	select {
	case b := <-fired:
		t.Fatalf("fired after ack: %+v", b)
	case <-time.After(2 * time.Second):
	}
}
