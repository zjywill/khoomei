package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/protocol"
)

const defaultWakeRate = 6

// Waker applies SPEC §4: the effective level only ever goes down, and `now` is rate-limited
// per (from_agent, to_agent) per hour. Adapters run asynchronously; a failed wake is logged
// and the message simply stays in the inbox.
type Waker struct {
	cfg  *config.Config
	logf func(string, ...any)
	mu   sync.Mutex
	hits map[[2]string]hourCount
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

// Effective computes the level for env and consumes one `now` credit if it grants one.
func (w *Waker) Effective(env *protocol.Envelope) string {
	spec := w.cfg.Agents[env.ToAgent]
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
	spec := w.cfg.Agents[env.ToAgent]
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
		url, err := readSecret(a.URLFile)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if a.SecretFile != "" {
			secret, err := readSecret(a.SecretFile)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := http.DefaultClient.Do(req)
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
