package hub

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/config"
)

const (
	laptopNode  = "junyi-mbp.tail1234.ts.net"
	junyiLogin  = "junyi@github"
	grokTSNNode = "khoomei-junyi-grok.tail1234.ts.net" // a tsnet caller started with junyi's auth key
)

// getAdmin requests path as node/login (empty node = loopback) with the given Host header.
func getAdmin(t *testing.T, base, path, host, node, login string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	if host != "" {
		req.Host = host
	}
	if node != "" {
		req.Header.Set(peerHeader, node)
		req.Header.Set(loginHeader, login)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestAdminAccess(t *testing.T) {
	h, srv := newTestHub(t)
	h.Cfg.Admins = []config.AdminSpec{{Node: "junyi-mbp", Login: junyiLogin}}
	h.Hosts = []string{"khoomei-junyi.tail1234.ts.net", "khoomei-junyi", "100.64.0.1"}

	cases := []struct {
		name, path, host, node, login string
		want                          int
	}{
		{"loopback", "/admin", "", "", "", 200},
		{"loopback with slash", "/admin/", "localhost:7767", "", "", 200},
		{"registered node, short name binding", "/admin", "khoomei-junyi:7767", laptopNode, junyiLogin, 200},
		{"login case does not matter", "/admin", "khoomei-junyi.tail1234.ts.net", laptopNode, "Junyi@GitHub", 200},
		{"tailnet IP as host", "/admin", "100.64.0.1:7767", laptopNode, junyiLogin, 200},
		{"same login, other node", "/admin", "khoomei-junyi", grokTSNNode, junyiLogin, 403},
		{"registered node, other login", "/admin", "khoomei-junyi", laptopNode, aliceLogin, 403},
		{"agent node", "/admin", "khoomei-junyi", museNode, junyiLogin, 403},
		{"rebinding host", "/admin", "evil.example", "", "", 403},
		{"rebinding host as admin", "/admin", "evil.example:7767", laptopNode, junyiLogin, 403},
		{"prefix of our name", "/admin", "khoomei-junyi.attacker.example", laptopNode, junyiLogin, 403},
		{"unknown panel path", "/admin/nope", "", "", "", 404},
		{"unknown path, not admin", "/admin/nope", "", grokTSNNode, junyiLogin, 403},
	}
	for _, c := range cases {
		resp := getAdmin(t, srv.URL, c.path, c.host, c.node, c.login)
		if resp.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: missing CSP, got %q", c.name, csp)
		}
	}
}

func TestAdminNeedsLogin(t *testing.T) {
	h, srv := newTestHub(t)
	h.Cfg.Admins = []config.AdminSpec{{Node: "junyi-mbp"}} // hand-edited config without a login
	if resp := getAdmin(t, srv.URL, "/admin", "", laptopNode, ""); resp.StatusCode != 403 {
		t.Fatalf("admin without login let in: %d", resp.StatusCode)
	}
}

// The hub's default node name is allowed even before serve fills in Hosts.
func TestAdminDefaultHostname(t *testing.T) {
	_, srv := newTestHub(t)
	if resp := getAdmin(t, srv.URL, "/admin", "khoomei-junyi:7767", "", ""); resp.StatusCode != 200 {
		t.Fatalf("default hostname rejected: %d", resp.StatusCode)
	}
}
