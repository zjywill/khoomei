package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/protocol"
)

const defaultWakeRate = 6

// Waker applies SPEC §4: the effective level only ever goes down, and `now` is rate-limited
// per (from_agent, to_agent) per hour. Adapters run asynchronously; a failed wake is logged
// and the message simply stays in the inbox.
type Waker struct {
	cfg *config.Config
	// Lookup finds the recipient's spec; the hub points it at its directory so agents that joined
	// by themselves are woken too. Nil means config.json only.
	Lookup func(addr string) *config.AgentSpec
	logf   func(string, ...any)
	mu     sync.Mutex
	hits   map[[2]string]hourCount
	// OnWake, when set, hears the outcome of every wake the recipient's adapter was due:
	// ok, failed (detail is the error) or downgraded (the hourly `now` budget ran out).
	OnWake func(env *protocol.Envelope, result, detail string)
}

type hourCount struct {
	hour int64
	n    int
}

func NewWaker(cfg *config.Config, logf func(string, ...any)) *Waker {
	return &Waker{cfg: cfg, logf: logf, hits: map[[2]string]hourCount{}}
}

func (w *Waker) spec(addr string) *config.AgentSpec {
	if w.Lookup != nil {
		return w.Lookup(addr)
	}
	return w.cfg.Agents[addr]
}

// Effective computes the level for env and consumes one `now` credit if it grants one.
func (w *Waker) Effective(env *protocol.Envelope) string {
	spec := w.spec(env.ToAgent)
	level := env.RequestedWake()
	if spec != nil && spec.WakeMax != "" {
		level = protocol.MinWake(level, spec.WakeMax)
	}
	if level != protocol.WakeNow {
		return level
	}
	rate := defaultWakeRate
	if spec != nil && spec.WakeRate > 0 {
		rate = spec.WakeRate
	}
	key := [2]string{env.FromAgent, env.ToAgent}
	hour := time.Now().Unix() / 3600
	w.mu.Lock()
	defer w.mu.Unlock()
	hc := w.hits[key]
	if hc.hour != hour {
		hc = hourCount{hour: hour}
	}
	if hc.n >= rate {
		return protocol.WakeBatch
	}
	hc.n++
	w.hits[key] = hc
	return protocol.WakeNow
}

// Wake fires the recipient's adapter when the effective level is `now`.
func (w *Waker) Wake(env *protocol.Envelope) {
	spec := w.spec(env.ToAgent)
	if spec == nil || spec.Wake == nil {
		return
	}
	if w.Effective(env) != protocol.WakeNow {
		if env.RequestedWake() == protocol.WakeNow && (spec.WakeMax == "" || spec.WakeMax == protocol.WakeNow) {
			w.report(env, "downgraded", "")
		}
		return
	}
	go func() {
		start := time.Now()
		if err := w.fire(spec.Wake, env); err != nil {
			w.logf("wake %s failed: %v", env.ToAgent, err)
			w.report(env, "failed", err.Error())
			return
		}
		w.logf("wake %s via %s ok in %s (msg %s)", env.ToAgent, spec.Wake.Type, time.Since(start).Round(time.Millisecond), env.ID)
		w.report(env, "ok", "")
	}()
}

func (w *Waker) report(env *protocol.Envelope, result, detail string) {
	if w.OnWake != nil {
		w.OnWake(env, result, detail)
	}
}

type summary struct {
	ID        string        `json:"id"`
	FromAgent string        `json:"from_agent"`
	ToAgent   string        `json:"to_agent"`
	Kind      string        `json:"kind"`
	Preview   string        `json:"preview"`
	Files     []fileSummary `json:"files,omitempty"`
}

type fileSummary struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (w *Waker) fire(a *config.WakeAdapter, env *protocol.Envelope) error {
	preview := []rune(env.Text)
	if len(preview) > 200 {
		preview = preview[:200]
	}
	sum := summary{ID: env.ID, FromAgent: env.FromAgent, ToAgent: env.ToAgent, Kind: env.EffectiveKind(), Preview: string(preview)}
	for _, f := range env.Files {
		sum.Files = append(sum.Files, fileSummary{f.Name, f.Size})
	}
	body, _ := json.Marshal(sum)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	switch a.Type {
	case "exec":
		cmd := exec.CommandContext(ctx, a.Argv[0], a.Argv[1:]...)
		cmd.Env = append(os.Environ(), "KHOOMEI_MSG_ID="+env.ID, "KHOOMEI_TO_AGENT="+env.ToAgent)
		cmd.Stdin = bytes.NewReader(body)
		return cmd.Run()
	case "webhook":
		// The owner's own adapters name files; a joined agent's webhook came over the network and is
		// called only through the guarded client.
		client, target, secret := http.DefaultClient, a.URL, a.Secret
		if target == "" {
			var err error
			if target, err = readSecret(a.URLFile); err != nil {
				return err
			}
			if a.SecretFile != "" {
				if secret, err = readSecret(a.SecretFile); err != nil {
					return err
				}
			}
		} else {
			if err := checkWebhookURL(target); err != nil {
				return err
			}
			client = guestClient
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("webhook returned %d", resp.StatusCode)
		}
		return nil
	}
	return nil
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

// --------------------------------------------------------------------------- guests' webhooks

// A webhook URL handed over by a guest makes the hub send a request somewhere the guest chose. So
// it must be https on the default port, and the address it resolves to must be public: never
// loopback, private, link-local, CGNAT (which includes the tailnet) or otherwise special. The
// check runs again on every dial, so DNS cannot swap in an inside address later, and redirects
// are not followed.
func checkWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || len(raw) > 2048:
		return errors.New("bad webhook url")
	case u.Scheme != "https":
		return errors.New("a webhook url must be https")
	case u.User != nil:
		return errors.New("a webhook url may not carry a user or password")
	case u.Port() != "" && u.Port() != "443":
		return errors.New("a webhook url must use the default https port")
	case u.Hostname() == "":
		return errors.New("bad webhook host")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicAddr(ip) {
		return errors.New("a webhook url must point at a public address")
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip) && !tailnetV6.Contains(ip)
}

var guestClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil || !publicAddr(ap.Addr()) {
					return fmt.Errorf("webhook address %s is not public", address)
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	},
}
