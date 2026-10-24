// Package client talks to a hub: used by callers over the tailnet and by the hub's own CLI over loopback.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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

func New(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{Base: base, HTTP: hc}
}

func (c *Client) call(ctx context.Context, method, path string, body any, timeout time.Duration) (*Response, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, &buf)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
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
