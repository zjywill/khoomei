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

	"golang.org/x/net/http/httpproxy"

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
	DryRun    bool              `json:"dry_run,omitempty"`
	Features  []string          `json:"features,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Count     int               `json:"count,omitempty"`
	Bytes     int64             `json:"bytes,omitempty"`
	Contacts  []Contact         `json:"contacts,omitempty"`
	Address   string            `json:"address,omitempty"` // /join
	Owner     string            `json:"owner,omitempty"`
	HubOwner  string            `json:"hub_owner,omitempty"`
	NewPerson bool              `json:"new_person,omitempty"`
	NewAgent  bool              `json:"new_agent,omitempty"`
	Groups    json.RawMessage   `json:"groups,omitempty"` // /contacts: names; /admin/api/groups: objects
}

// Contact is one address an agent may message (GET /contacts).
type Contact struct {
	Address string   `json:"address"`
	Private bool     `json:"private,omitempty"`
	Groups  []string `json:"groups,omitempty"`
}

// New returns a client. Connections are not reused: the path to a hub often runs through a DERP
// relay or a sandbox proxy that silently drops idle connections, and a reused dead connection
// surfaces as EOF on the next call. The default transport honours HTTP(S)_PROXY / NO_PROXY, which
// sandboxes such as Muse's rely on to reach the tailnet at all.
func New(base string, hc *http.Client) *Client {
	if hc == nil {
		// httpproxy reads the environment now; http.ProxyFromEnvironment caches it once per process.
		proxy := httpproxy.FromEnvironment().ProxyFunc()
		hc = &http.Client{Transport: &http.Transport{
			Proxy:             func(r *http.Request) (*url.URL, error) { return proxy(r.URL) },
			DisableKeepAlives: true,
		}}
	} else if t, ok := hc.Transport.(*http.Transport); ok {
		t.DisableKeepAlives = true
	}
	return &Client{Base: base, HTTP: hc}
}

func (c *Client) call(ctx context.Context, method, path string, body any, timeout time.Duration) (*Response, error) {
	return c.callWith(ctx, method, path, body, timeout, nil)
}

func (c *Client) callWith(ctx context.Context, method, path string, body any, timeout time.Duration, hdr map[string]string) (*Response, error) {
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
		for k, v := range hdr {
			req.Header.Set(k, v)
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
	// 30s: a sandbox such as Muse's may hold the first connection to a host until the user approves it.
	return c.call(ctx, http.MethodGet, "/health", nil, 30*time.Second)
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

// Prune asks the hub's panel to remove records (docs/PANEL.md#清理); only the hub owner may.
func (c *Client) Prune(ctx context.Context, req any) (*Response, error) {
	return c.callWith(ctx, http.MethodPost, "/admin/api/prune", req, 60*time.Second, map[string]string{"X-Khoomei-Panel": "1"})
}

// Contacts lists whom agent may message and the groups it is in (docs/GROUPS.md).
func (c *Client) Contacts(ctx context.Context, agent string) (*Response, error) {
	return c.call(ctx, http.MethodGet, "/contacts?agent="+url.QueryEscape(agent), nil, 30*time.Second)
}

// Join registers agent with the hub by itself (docs/GROUPS.md#报到); owner may be empty.
func (c *Client) Join(ctx context.Context, agent, owner, wakeURL, wakeSecret string) (*Response, error) {
	body := map[string]any{"agent": agent}
	if owner != "" {
		body["owner"] = owner
	}
	if wakeURL != "" {
		body["wake"] = map[string]any{"url": wakeURL, "secret": wakeSecret}
	}
	return c.call(ctx, http.MethodPost, "/join", body, 30*time.Second)
}

// Group calls are the hub owner's, made from the hub over loopback (docs/GROUPS.md#命令).
func (c *Client) Groups(ctx context.Context) (*Response, error) {
	return c.call(ctx, http.MethodGet, "/admin/api/groups", nil, 30*time.Second)
}

func (c *Client) GroupCreate(ctx context.Context, name string, members []string) (*Response, error) {
	return c.panelPost(ctx, "/admin/api/groups", map[string]any{"name": name, "members": members})
}

func (c *Client) GroupDelete(ctx context.Context, name string) (*Response, error) {
	return c.panelPost(ctx, "/admin/api/groups/delete", map[string]any{"name": name})
}

func (c *Client) GroupMembers(ctx context.Context, name string, add, remove []string) (*Response, error) {
	return c.panelPost(ctx, "/admin/api/groups/members", map[string]any{"name": name, "add": add, "remove": remove})
}

func (c *Client) GroupOpen(ctx context.Context, name string, open bool) (*Response, error) {
	return c.panelPost(ctx, "/admin/api/groups/open", map[string]any{"name": name, "open": open})
}

func (c *Client) panelPost(ctx context.Context, path string, body any) (*Response, error) {
	return c.callWith(ctx, http.MethodPost, path, body, 30*time.Second, map[string]string{"X-Khoomei-Panel": "1"})
}

// HasFeature reports whether a /health response lists feature f; hubs before 0.5 list none.
func (r *Response) HasFeature(f string) bool {
	for _, x := range r.Features {
		if x == f {
			return true
		}
	}
	return false
}

// blobTimeout gives a transfer of size bytes at least a minute plus a second per 64 KB, enough for a
// slow DERP relay.
func blobTimeout(size int64) time.Duration {
	return time.Minute + time.Duration(size/(64<<10))*time.Second
}

// PutBlob uploads size bytes from r as agent (docs/FILES.md). sha, when known, lets the hub verify
// the content; typ is a hint. Not retried: the caller decides, since r may not be rewindable.
func (c *Client) PutBlob(ctx context.Context, agent string, r io.Reader, size int64, sha, typ string) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, blobTimeout(size))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.Base+"/blob?agent="+url.QueryEscape(agent), r)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if typ == "" {
		typ = "application/octet-stream"
	}
	req.Header.Set("Content-Type", typ)
	if sha != "" {
		req.Header.Set("X-Khoomei-SHA256", sha)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.Base, err)
	}
	defer resp.Body.Close()
	out := &Response{Status: resp.StatusCode}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("/blob: bad response (%d)", resp.StatusCode)
	}
	return out, nil
}

// HasBlob asks whether agent may already fetch sha from the hub, which also means it may attach it.
func (c *Client) HasBlob(ctx context.Context, agent, sha string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.blobURL(agent, sha, ""), nil)
	if err != nil {
		return false, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("cannot reach %s: %w", c.Base, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return true, nil
	case 404:
		return false, nil
	}
	return false, fmt.Errorf("/blob: HTTP %d", resp.StatusCode)
}

// GetBlob downloads sha into w, which receives exactly size bytes or the call fails; a negative size
// takes whatever the hub sends.
func (c *Client) GetBlob(ctx context.Context, agent, sha, name string, size int64, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, blobTimeout(max(size, 25<<20)))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.blobURL(agent, sha, name), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", c.Base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e Response
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("/blob/%s: HTTP %d %s", sha[:12], resp.StatusCode, e.Error)
	}
	var body io.Reader = resp.Body
	if size >= 0 {
		body = io.LimitReader(resp.Body, size+1)
	}
	n, err := io.Copy(w, body)
	if err != nil {
		return err
	}
	if size >= 0 && n != size {
		return fmt.Errorf("/blob/%s: got %d bytes, expected %d", sha[:12], n, size)
	}
	return nil
}

func (c *Client) blobURL(agent, sha, name string) string {
	u := c.Base + "/blob/" + sha + "?agent=" + url.QueryEscape(agent)
	if name != "" {
		u += "&name=" + url.QueryEscape(name)
	}
	return u
}
