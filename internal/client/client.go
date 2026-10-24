// Package client talks to a hub: used by callers over the tailnet and by the hub's own CLI over loopback.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/zjywill/khoomei/internal/protocol"
)

type Client struct {
	Base string
	HTTP *http.Client
}

// Response is the hub's JSON answer plus the HTTP status.
type Response struct {
	Status    int
	OK        bool              `json:"ok"`
	Error     string            `json:"error,omitempty"`
	Code      string            `json:"code,omitempty"`
	ID        string            `json:"id,omitempty"`
	Duplicate bool              `json:"duplicate,omitempty"`
	Agent     string            `json:"agent,omitempty"`
	Messages  []protocol.Record `json:"messages,omitempty"`
	Acked     []string          `json:"acked,omitempty"`
	Version   string            `json:"version,omitempty"`
}

// New returns a client. Connections are not reused: the path to a hub often runs through a DERP
// relay or a sandbox proxy that silently drops idle connections, and a reused dead connection
// surfaces as EOF on the next call.
func New(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	} else if t, ok := hc.Transport.(*http.Transport); ok {
		t.DisableKeepAlives = true
	}
	return &Client{Base: base, HTTP: hc}
}

func (c *Client) call(ctx context.Context, method, path string, body any, timeout time.Duration) (*Response, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// One retry on a connection-level failure. Every call is idempotent: /msg by message id,
	// the rest by nature.
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err = c.HTTP.Do(req)
		if err == nil || ctx.Err() != nil || !transient(err) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.Base, err)
	}
	defer resp.Body.Close()
	out := &Response{Status: resp.StatusCode}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("%s: bad response (%d)", path, resp.StatusCode)
	}
	return out, nil
}

// transient reports connection-level failures worth one retry: EOF, reset, refused, dial failures.
func transient(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false // the deadline is the caller's budget; do not spend it twice
	}
	var oe *net.OpError
	return errors.As(err, &oe)
}

func (c *Client) Health(ctx context.Context) (*Response, error) {
	return c.call(ctx, http.MethodGet, "/health", nil, 5*time.Second)
}

func (c *Client) Send(ctx context.Context, env *protocol.Envelope) (*Response, error) {
	return c.call(ctx, http.MethodPost, "/msg", env, 30*time.Second)
}

func (c *Client) Inbox(ctx context.Context, agent string) (*Response, error) {
	return c.call(ctx, http.MethodGet, "/inbox?agent="+url.QueryEscape(agent), nil, 30*time.Second)
}

func (c *Client) Wait(ctx context.Context, agent string, timeout time.Duration) (*Response, error) {
	q := "/wait?agent=" + url.QueryEscape(agent) + "&timeout=" + strconv.Itoa(int(timeout.Seconds()))
	return c.call(ctx, http.MethodGet, q, nil, timeout+15*time.Second)
}

func (c *Client) Ack(ctx context.Context, agent string, ids []string) (*Response, error) {
	return c.call(ctx, http.MethodPost, "/ack", map[string]any{"agent": agent, "ids": ids}, 30*time.Second)
}
