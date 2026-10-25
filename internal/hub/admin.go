package hub

import (
	"net"
	"net/http"
	"strings"
)

// hostAllowed checks the Host header against the hub's own names, so a page served from another
// origin cannot reach /admin through DNS rebinding. Matching is exact: a prefix rule would let
// khoomei-junyi.attacker.example through.
func (h *Hub) hostAllowed(hostport string) bool {
	host := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		host = hh
	}
	host = normHost(host)
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	for _, n := range append([]string{h.Cfg.TSNetHostname()}, h.Hosts...) {
		if n = normHost(n); n != "" && host == n {
			return true
		}
	}
	return false
}

func normHost(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(s, "[]"), "."))
}

// withAdmin guards every /admin path. The panel is open to every node that may reach the hub at all
// (tailnet address, not denied by policy, as withIdentity checks); the only extra rule is the Host check.
func (h *Hub) withAdmin(fn func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return h.withIdentity(func(w http.ResponseWriter, r *http.Request, id Identity) {
		hdr := w.Header()
		hdr.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Cache-Control", "no-store")
		if !h.hostAllowed(r.Host) {
			fail(w, ForbiddenError("unexpected Host "+r.Host))
			return
		}
		fn(w, r, id)
	})
}

// Without a panel handler (tests, or a hub built without one) /admin answers with a placeholder.
const adminPlaceholder = `<!doctype html>
<html lang="zh-Hans"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>khoomei</title></head>
<body style="font-family: system-ui, sans-serif; margin: 2rem">
<p>khoomei hub 面板还在开发中。</p>
</body></html>
`

func (h *Hub) handleAdmin(w http.ResponseWriter, r *http.Request, _ Identity) {
	if h.Admin != nil {
		h.Admin.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no such panel path"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminPlaceholder))
}
