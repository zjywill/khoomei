package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zjywill/khoomei/internal/hub"
)

// authReq sends one panel request with an optional session cookie and returns the status, the
// decoded body and the session cookie the response set, if any.
func (f *fixture) authReq(method, path, body, cookie string, hdr map[string]string) (int, map[string]any, *http.Cookie) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Khoomei-Panel", "1")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			return resp.StatusCode, out, c
		}
	}
	return resp.StatusCode, out, nil
}

func (f *fixture) as(id hub.Identity) {
	f.h.Identify = func(*http.Request) (hub.Identity, error) { return id, nil }
}

const ownerLogin = "junyi@github"

// laptop is the owner's browser on another tailnet machine: the owner by login while no password is
// set, a stranger to the panel once one is.
var laptop = hub.Identity{Node: "junyi-mbp.tail1234.ts.net", Login: ownerLogin}

// fromLaptop makes f's requests come from laptop, with the hub knowing junyi's login.
func (f *fixture) fromLaptop() *fixture {
	f.h.SelfLogin = ownerLogin
	f.as(laptop)
	return f
}

func TestPanelOpenWithoutPassword(t *testing.T) {
	f := newFixture(t).fromLaptop()
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", "", nil); code != 200 {
		t.Fatalf("rooms without a password: %d", code)
	}
	_, st, _ := f.authReq("GET", "/admin/api/auth", "", "", nil)
	if st["password_set"] != false || st["can_set"] != true {
		t.Fatalf("owner's laptop status: %v", st)
	}

	// A friend sees the reminder but may not set the first password.
	f.as(hub.Identity{Node: aliceNode, Login: aliceLogin})
	_, st, _ = f.authReq("GET", "/admin/api/auth", "", "", nil)
	if st["can_set"] != false {
		t.Fatalf("friend may set: %v", st)
	}
	if code, body, _ := f.authReq("POST", "/admin/api/auth/password", `{"new":"correct horse"}`, "", nil); code != 403 || body["code"] != "not_owner" {
		t.Fatalf("friend set the password: %d %v", code, body)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Home, PasswordFile)); err == nil {
		t.Fatal("password file written")
	}
}

func TestPanelPasswordFlow(t *testing.T) {
	f := newFixture(t).fromLaptop()

	if code, body, _ := f.authReq("POST", "/admin/api/auth/password", `{"new":"short"}`, "", nil); code != 400 || body["code"] != "weak_password" {
		t.Fatalf("short password: %d %v", code, body)
	}
	code, _, setC := f.authReq("POST", "/admin/api/auth/password", `{"new":"correct horse"}`, "", nil)
	if code != 200 || setC == nil {
		t.Fatalf("first password: %d, cookie %v", code, setC)
	}
	if !setC.HttpOnly || setC.SameSite != http.SameSiteStrictMode || setC.Path != "/admin" || setC.MaxAge <= 0 {
		t.Fatalf("cookie attributes: %+v", setC)
	}
	if st, _ := os.Stat(filepath.Join(f.cfg.Home, PasswordFile)); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("password file mode: %v", st)
	}

	// Locked: data needs a session, the page itself does not.
	if code, body, _ := f.authReq("GET", "/admin/api/rooms", "", "", nil); code != 401 || body["code"] != "login_required" {
		t.Fatalf("rooms without session: %d %v", code, body)
	}
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", "forged", nil); code != 401 {
		t.Fatalf("rooms with a forged cookie: %d", code)
	}
	if code, _, _ := f.authReq("GET", "/admin/", "", "", nil); code != 200 {
		t.Fatalf("page without session: %d", code)
	}
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", setC.Value, nil); code != 200 {
		t.Fatalf("rooms with the setter's session: %d", code)
	}
	// Setting it again without logging in is not possible now.
	if code, _, _ := f.authReq("POST", "/admin/api/auth/password", `{"new":"another one"}`, "", nil); code != 401 {
		t.Fatalf("reset without session: %d", code)
	}

	// Log in from a second browser.
	if code, body, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"wrong horse"}`, "", nil); code != 401 || body["code"] != "wrong_password" {
		t.Fatalf("wrong password: %d %v", code, body)
	}
	code, _, b2 := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil)
	if code != 200 || b2 == nil {
		t.Fatalf("login: %d", code)
	}
	_, st, _ := f.authReq("GET", "/admin/api/auth", "", b2.Value, nil)
	if st["password_set"] != true || st["logged_in"] != true || st["can_set"] != false {
		t.Fatalf("status after login: %v", st)
	}

	// Change from browser 2: the current password is required, and browser 1 is logged out.
	if code, _, _ := f.authReq("POST", "/admin/api/auth/password", `{"current":"nope nope","new":"battery staple"}`, b2.Value, nil); code != 403 {
		t.Fatalf("change with a wrong current password: %d", code)
	}
	code, _, b2new := f.authReq("POST", "/admin/api/auth/password", `{"current":"correct horse","new":"battery staple"}`, b2.Value, nil)
	if code != 200 || b2new == nil {
		t.Fatalf("change: %d", code)
	}
	for name, c := range map[string]string{"browser 1": setC.Value, "browser 2 before": b2.Value} {
		if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", c, nil); code != 401 {
			t.Errorf("%s still in after the change: %d", name, code)
		}
	}
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", b2new.Value, nil); code != 200 {
		t.Fatalf("new session: %d", code)
	}
	if code, _, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil); code != 401 {
		t.Fatalf("old password still works: %d", code)
	}

	// Logout ends the session and clears the cookie.
	code, _, cleared := f.authReq("POST", "/admin/api/auth/logout", `{}`, b2new.Value, nil)
	if code != 200 || cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout: %d %+v", code, cleared)
	}
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", b2new.Value, nil); code != 401 {
		t.Fatalf("session survived logout: %d", code)
	}
}

// Loopback is the hub owner: the CLI's prune and group commands work with a password set.
func TestPanelLoopbackSkipsPassword(t *testing.T) {
	f := newFixture(t)
	if err := SetPassword(f.cfg.Home, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.authReq("GET", "/admin/api/storage", "", "", nil); code != 200 {
		t.Fatalf("loopback storage: %d", code)
	}
	if code, body, _ := f.authReq("POST", "/admin/api/prune", `{"what":"acked","dry_run":true}`, "", nil); code != 200 {
		t.Fatalf("loopback prune: %d %v", code, body)
	}
}

// The CLI writes the file under a running hub: sessions end at once, and --clear opens the panel.
func TestPanelPasswordFromCLI(t *testing.T) {
	f := newFixture(t).fromLaptop()
	if err := SetPassword(f.cfg.Home, "correct horse"); err != nil {
		t.Fatal(err)
	}
	_, _, c := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil)
	if c == nil {
		t.Fatal("no session")
	}
	if err := SetPassword(f.cfg.Home, "battery staple"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", c.Value, nil); code != 401 {
		t.Fatalf("session survived a reset: %d", code)
	}
	if err := ClearPassword(f.cfg.Home); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", "", nil); code != 200 {
		t.Fatalf("cleared password still locks: %d", code)
	}
	if err := ClearPassword(f.cfg.Home); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}

func TestPanelSessionExpiry(t *testing.T) {
	f := newFixture(t).fromLaptop()
	now := time.Now()
	f.p.now = func() time.Time { return now }
	if err := SetPassword(f.cfg.Home, "correct horse"); err != nil {
		t.Fatal(err)
	}
	_, _, c := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil)
	now = now.Add(6 * 24 * time.Hour)
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", c.Value, nil); code != 200 {
		t.Fatalf("6 days idle: %d", code)
	}
	now = now.Add(sessionIdle + time.Minute)
	if code, _, _ := f.authReq("GET", "/admin/api/rooms", "", c.Value, nil); code != 401 {
		t.Fatalf("past the idle limit: %d", code)
	}
	if len(f.p.auth.sessions) != 0 {
		t.Fatalf("expired session kept: %d", len(f.p.auth.sessions))
	}
}

func TestPanelLoginLockout(t *testing.T) {
	f := newFixture(t).fromLaptop()
	now := time.Now()
	f.p.now = func() time.Time { return now }
	if err := SetPassword(f.cfg.Home, "correct horse"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < failFree; i++ {
		if code, _, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"guess guess"}`, "", nil); code != 401 {
			t.Fatalf("try %d: %d", i, code)
		}
	}
	// Locked: even the right password waits, and another node is not affected.
	if code, body, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil); code != 429 || body["code"] != "locked" {
		t.Fatalf("while locked: %d %v", code, body)
	}
	f.as(hub.Identity{Node: aliceNode, Login: aliceLogin})
	if code, _, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil); code != 200 {
		t.Fatalf("other node: %d", code)
	}
	f.as(laptop)
	now = now.Add(lockMin + time.Second)
	if code, _, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", nil); code != 200 {
		t.Fatalf("after the lockout: %d", code)
	}
}

func TestPanelAuthWritesNeedPanelHeaders(t *testing.T) {
	f := newFixture(t).fromLaptop()
	if err := SetPassword(f.cfg.Home, "correct horse"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"same origin", map[string]string{"Origin": f.srv.URL}, 200},
		{"no panel header", map[string]string{"X-Khoomei-Panel": ""}, 403},
		{"form post", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 403},
		{"other origin", map[string]string{"Origin": "https://evil.example"}, 403},
	}
	for _, c := range cases {
		if code, _, _ := f.authReq("POST", "/admin/api/auth/login", `{"password":"correct horse"}`, "", c.hdr); code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, code, c.want)
		}
	}
}

func TestVerifyPasswordRejectsJunk(t *testing.T) {
	phc, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(phc, "correct horse") || verifyPassword(phc, "correct horsE") {
		t.Fatal("verify")
	}
	for _, junk := range []string{"", "plain", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$a2V5", "$argon2id$v=19$m=99999999,t=2,p=1$c2FsdA$a2V5"} {
		if verifyPassword(junk, "correct horse") {
			t.Errorf("accepted %q", junk)
		}
	}
}
