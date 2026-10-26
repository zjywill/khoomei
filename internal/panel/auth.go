package panel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/zjywill/khoomei/internal/hub"
)

// The panel password (docs/PANEL.md#密码). Without one the panel is open to every node that reaches the
// hub, and the front end says so on every visit. With one, /admin/api answers 401 until the browser
// logs in; the page itself and its assets stay public, they hold no data.
//
// The hash lives in <home>/panel_password, apart from config.json, so that the running hub and
// `khoomei panel-password` can both change it without a restart. Sessions live in memory: a restart
// logs everyone out. Each session remembers the hash it was made under, so any password change,
// from the panel or the CLI, ends every older session.

const (
	PasswordFile = "panel_password"
	CookieName   = "khoomei_session"

	MinPassword = 8
	MaxPassword = 256 // bytes; keeps the hashing cost bounded

	sessionTTL  = 30 * 24 * time.Hour // absolute
	sessionIdle = 7 * 24 * time.Hour  // unused this long ends it too
	maxSessions = 64                  // oldest go first

	failFree = 5                // wrong passwords allowed per source before a lockout
	lockMin  = time.Minute      // first lockout, doubled on every further miss
	lockMax  = 15 * time.Minute //
)

// argon2id, the OWASP minimum (19 MiB, t=2, p=1): a 1-CPU VPS hashes in well under a second.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

type session struct {
	hash    string // the password file this session was made under
	created time.Time
	seen    time.Time
}

type failures struct {
	n     int
	until time.Time
}

type auth struct {
	path string
	now  func() time.Time
	sem  chan struct{} // at most two hashes at once: each takes 19 MiB

	mu       sync.Mutex
	sessions map[string]*session // by sha256 of the cookie value
	fails    map[string]*failures
}

func newAuth(home string, now func() time.Time) *auth {
	return &auth{path: filepath.Join(home, PasswordFile), now: now, sem: make(chan struct{}, 2),
		sessions: map[string]*session{}, fails: map[string]*failures{}}
}

// --------------------------------------------------------------------------- password file

// HashPassword returns the argon2id hash in PHC form: $argon2id$v=19$m=…,t=…,p=…$salt$key.
func HashPassword(pw string) (string, error) {
	if err := checkPassword(pw); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64(salt), b64(key)), nil
}

func checkPassword(pw string) error {
	switch {
	case !utf8.ValidString(pw):
		return errors.New("password is not valid UTF-8")
	case utf8.RuneCountInString(pw) < MinPassword:
		return fmt.Errorf("password needs at least %d characters", MinPassword)
	case len(pw) > MaxPassword:
		return fmt.Errorf("password is longer than %d bytes", MaxPassword)
	}
	return nil
}

// verifyPassword compares pw with a PHC hash in constant time.
func verifyPassword(phc, pw string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 16 || p == 0 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(key) == 0 || len(key) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}

// SetPassword writes a new hash for home (the CLI and the panel both come through here).
func SetPassword(home, pw string) error {
	phc, err := HashPassword(pw)
	if err != nil {
		return err
	}
	return writePassword(filepath.Join(home, PasswordFile), phc)
}

// ClearPassword removes the password; the panel is open again.
func ClearPassword(home string) error {
	err := os.Remove(filepath.Join(home, PasswordFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writePassword(path, phc string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(phc+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// current reads the hash; "" means no password. A file that cannot be read keeps the panel shut
// rather than open: a permissions slip must not undo the password.
func (a *auth) current() (string, error) {
	b, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// --------------------------------------------------------------------------- sessions

func tokenKey(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// valid reports whether r carries a live session under hash, refreshing its idle clock.
func (a *auth) valid(r *http.Request, hash string) bool {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expire()
	s := a.sessions[tokenKey(c.Value)]
	if s == nil || subtle.ConstantTimeCompare([]byte(s.hash), []byte(hash)) != 1 {
		return false
	}
	s.seen = a.now()
	return true
}

// expire drops dead sessions and stale failure counters; the caller holds a.mu.
func (a *auth) expire() {
	now := a.now()
	for k, s := range a.sessions {
		if now.Sub(s.created) > sessionTTL || now.Sub(s.seen) > sessionIdle {
			delete(a.sessions, k)
		}
	}
	for k, f := range a.fails {
		if now.After(f.until.Add(lockMax)) {
			delete(a.fails, k)
		}
	}
}

// login starts a session under hash and sets the cookie.
func (a *auth) login(w http.ResponseWriter, r *http.Request, hash string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	a.mu.Lock()
	a.expire()
	for len(a.sessions) >= maxSessions {
		var oldest string
		for k, s := range a.sessions {
			if oldest == "" || s.seen.Before(a.sessions[oldest].seen) {
				oldest = k
			}
		}
		delete(a.sessions, oldest)
	}
	now := a.now()
	a.sessions[tokenKey(tok)] = &session{hash: hash, created: now, seen: now}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: tok, Path: "/admin", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	return nil
}

func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, tokenKey(c.Value))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/admin", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

// dropAll ends every session; the password changed.
func (a *auth) dropAll() {
	a.mu.Lock()
	clear(a.sessions)
	a.mu.Unlock()
}

// --------------------------------------------------------------------------- lockout

func sourceKey(id hub.Identity) string {
	if id.Local {
		return "local"
	}
	return id.Node
}

// locked returns how long src must still wait.
func (a *auth) locked(src string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.fails[src]; f != nil {
		if d := f.until.Sub(a.now()); d > 0 {
			return d
		}
	}
	return 0
}

// missed counts a wrong password from src; past failFree each miss locks it out for longer.
func (a *auth) missed(src string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[src]
	if f == nil {
		f = &failures{}
		a.fails[src] = f
	}
	f.n++
	if f.n >= failFree {
		d := lockMin << min(f.n-failFree, 4)
		f.until = a.now().Add(min(d, lockMax))
	}
}

func (a *auth) succeeded(src string) {
	a.mu.Lock()
	delete(a.fails, src)
	a.mu.Unlock()
}

// check verifies pw against hash, at most two at a time, and counts a miss against src.
func (a *auth) check(src, hash, pw string) bool {
	a.sem <- struct{}{}
	ok := len(pw) <= MaxPassword && verifyPassword(hash, pw)
	<-a.sem
	if ok {
		a.succeeded(src)
	} else {
		a.missed(src)
	}
	return ok
}

// --------------------------------------------------------------------------- HTTP

// gate lets /admin/api through only with a session once a password is set. The page and its assets
// are always served; the auth endpoints handle themselves.
func (p *Panel) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/api/") || strings.HasPrefix(r.URL.Path, "/admin/api/auth") {
			next.ServeHTTP(w, r)
			return
		}
		hash, err := p.auth.current()
		if err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": "cannot read the panel password: " + err.Error()})
			return
		}
		if hash != "" && !p.auth.valid(r, hash) {
			writeJSON(w, 401, map[string]any{"ok": false, "error": "login required", "code": "login_required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isOwner says whether the request comes from the hub owner: loopback, or whatever the hub decides.
func (p *Panel) isOwner(r *http.Request) bool {
	id, ok := hub.IdentityFrom(r.Context())
	if !ok {
		return false
	}
	if p.IsOwner != nil {
		return p.IsOwner(id)
	}
	return id.Local
}

func (p *Panel) handleAuth(w http.ResponseWriter, r *http.Request) {
	hash, err := p.auth.current()
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": "cannot read the panel password: " + err.Error()})
		return
	}
	set := hash != ""
	writeJSON(w, 200, map[string]any{"ok": true, "password_set": set, "logged_in": set && p.auth.valid(r, hash),
		"can_set": !set && p.isOwner(r), "min_length": MinPassword})
}

func (p *Panel) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !panelWrite(w, r, &req) {
		return
	}
	hash, err := p.auth.current()
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": "cannot read the panel password: " + err.Error()})
		return
	}
	if hash == "" {
		writeJSON(w, 409, map[string]any{"ok": false, "error": "no password is set", "code": "no_password"})
		return
	}
	if !p.allowTry(w, r) {
		return
	}
	if !p.auth.check(source(r), hash, req.Password) {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "wrong password", "code": "wrong_password"})
		return
	}
	if err := p.auth.login(w, r, hash); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (p *Panel) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !panelWrite(w, r, &req) {
		return
	}
	p.auth.logout(w, r)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handlePassword sets the first password (hub owner only) or changes it (logged in, and the current
// password again). Either way every other session ends and this browser is logged in under the new one.
func (p *Panel) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !panelWrite(w, r, &req) {
		return
	}
	p.pwMu.Lock()
	defer p.pwMu.Unlock()
	hash, err := p.auth.current()
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": "cannot read the panel password: " + err.Error()})
		return
	}
	if hash == "" {
		if !p.isOwner(r) {
			writeJSON(w, 403, map[string]any{"ok": false, "error": "only the hub owner may set the first password; run khoomei panel-password on the hub", "code": "not_owner"})
			return
		}
	} else {
		if !p.auth.valid(r, hash) {
			writeJSON(w, 401, map[string]any{"ok": false, "error": "login required", "code": "login_required"})
			return
		}
		if !p.allowTry(w, r) {
			return
		}
		if !p.auth.check(source(r), hash, req.Current) {
			writeJSON(w, 403, map[string]any{"ok": false, "error": "current password is wrong", "code": "wrong_password"})
			return
		}
	}
	phc, err := HashPassword(req.New)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "weak_password"})
		return
	}
	if err := writePassword(p.auth.path, phc); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p.auth.dropAll()
	if err := p.auth.login(w, r, phc); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p.logf("panel password %s", map[bool]string{true: "set", false: "changed"}[hash == ""])
	writeJSON(w, 200, map[string]any{"ok": true})
}

// allowTry answers 429 while the source is locked out.
func (p *Panel) allowTry(w http.ResponseWriter, r *http.Request) bool {
	if d := p.auth.locked(source(r)); d > 0 {
		secs := int(d.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, 429, map[string]any{"ok": false, "error": fmt.Sprintf("too many wrong passwords; try again in %d s", secs), "code": "locked", "retry_after": secs})
		return false
	}
	return true
}

func source(r *http.Request) string {
	id, _ := hub.IdentityFrom(r.Context())
	return sourceKey(id)
}

// panelWrite applies the rules of every panel write: a JSON body with the X-Khoomei-Panel header,
// which a cross-site page cannot send without a CORS preflight that the hub never answers, and an
// Origin, when present, that matches Host. It decodes the body into v.
func panelWrite(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("X-Khoomei-Panel") != "1" {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "panel writes need Content-Type: application/json and X-Khoomei-Panel: 1"})
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeJSON(w, 403, map[string]any{"ok": false, "error": "cross-origin request"})
			return false
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid json"})
		return false
	}
	return true
}
