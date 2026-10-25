// Package protocol defines the khoomei v1 envelope and its validation rules (SPEC.md §3).
package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	Version = 1
	MaxBody = 256 * 1024
)

// Wake levels, lowest to highest.
const (
	WakeNone  = "none"
	WakeBatch = "batch"
	WakeNow   = "now"
)

var (
	idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	tsRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)
)

// Envelope is one message. Unknown fields are rejected on decode.
type Envelope struct {
	V         int             `json:"v"`
	ID        string          `json:"id"`
	FromAgent string          `json:"from_agent"`
	ToAgent   string          `json:"to_agent"`
	TS        string          `json:"ts"`
	Kind      string          `json:"kind,omitempty"`
	Text      string          `json:"text,omitempty"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	Wake      string          `json:"wake,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Received is what the hub records next to a stored envelope.
type Received struct {
	At    string `json:"at"`
	Seq   int64  `json:"seq"` // orders messages that land within the same second of At
	Peer  string `json:"peer"`
	Login string `json:"login,omitempty"` // Tailscale login of the sending node, when known
}

// Record is the stored form: inbox/<agent>/<id>.json.
type Record struct {
	Envelope Envelope `json:"envelope"`
	Received Received `json:"received"`
}

// New returns an envelope with a fresh id and timestamp; wake defaults to batch.
func New(from, to, text string) *Envelope {
	return &Envelope{V: Version, ID: NewID(), FromAgent: from, ToAgent: to, TS: Now(), Kind: "text", Text: text, Wake: WakeBatch}
}

// Decode reads one envelope from r, rejecting unknown fields and oversized bodies.
func Decode(r io.Reader) (*Envelope, error) {
	dec := json.NewDecoder(io.LimitReader(r, MaxBody+1))
	dec.DisallowUnknownFields()
	var e Envelope
	if err := dec.Decode(&e); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return nil, fmt.Errorf("unknown field: %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return nil, errors.New("invalid json")
	}
	return &e, nil
}

// Validate applies SPEC §3. It does not check that the agents exist; the hub does that.
func (e *Envelope) Validate() error {
	switch {
	case e.V != Version:
		return fmt.Errorf("v must be %d", Version)
	case !idRe.MatchString(e.ID):
		return errors.New("bad id")
	case !ValidAddress(e.FromAgent):
		return errors.New("bad from_agent")
	case !ValidAddress(e.ToAgent):
		return errors.New("bad to_agent")
	case !tsRe.MatchString(e.TS):
		return errors.New("bad ts")
	case e.Wake != "" && WakeRank(e.Wake) < 0:
		return errors.New("bad wake")
	case len(e.Kind) > 32:
		return errors.New("bad kind")
	case e.ReplyTo != "" && !idRe.MatchString(e.ReplyTo):
		return errors.New("bad reply_to")
	case strings.TrimSpace(e.Text) == "" && (len(e.Data) == 0 || string(e.Data) == "null"):
		return errors.New("empty message: text or data is required")
	}
	return nil
}

// EffectiveKind returns Kind or the default "text".
func (e *Envelope) EffectiveKind() string {
	if e.Kind == "" {
		return "text"
	}
	return e.Kind
}

// RequestedWake returns Wake or the default "batch".
func (e *Envelope) RequestedWake() string {
	if e.Wake == "" {
		return WakeBatch
	}
	return e.Wake
}

// ValidAgent: one name part (an agent or an owner handle): 1–64 characters, any Unicode, no "/" or
// control characters, no leading or trailing space, and not "." or ".." (names become directories).
func ValidAgent(name string) bool {
	n := len([]rune(name))
	if n < 1 || n > 64 {
		return false
	}
	if strings.TrimSpace(name) != name || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r == '/' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidAddress accepts "agent" or "owner/agent", each part a valid name.
func ValidAddress(addr string) bool {
	owner, agent, ok := SplitAddress(addr)
	return ok && (owner == "" || ValidAgent(owner)) && ValidAgent(agent)
}

// SplitAddress separates "owner/agent" into its parts; owner is "" for a bare agent name.
// ok is false when there is more than one slash or an empty part around it.
func SplitAddress(addr string) (owner, agent string, ok bool) {
	i := strings.IndexByte(addr, '/')
	if i < 0 {
		return "", addr, true
	}
	owner, agent = addr[:i], addr[i+1:]
	if owner == "" || agent == "" || strings.Contains(agent, "/") {
		return "", "", false
	}
	return owner, agent, true
}

// Qualify prefixes a bare agent name with owner; a full address and an empty owner pass through.
func Qualify(addr, owner string) string {
	if owner == "" || strings.Contains(addr, "/") {
		return addr
	}
	return owner + "/" + addr
}

// ValidID reports whether s is a well-formed message id.
func ValidID(s string) bool { return idRe.MatchString(s) }

// WakeRank orders none < batch < now; -1 for an unknown level.
func WakeRank(level string) int {
	switch level {
	case WakeNone:
		return 0
	case WakeBatch:
		return 1
	case WakeNow:
		return 2
	}
	return -1
}

// MinWake returns the lower of two levels.
func MinWake(a, b string) string {
	if WakeRank(a) <= WakeRank(b) {
		return a
	}
	return b
}

// NewID returns 16 URL-safe characters from 12 random bytes.
func NewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Now is the current UTC time in the envelope format.
func Now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }
