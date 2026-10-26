package hub

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type identityKey struct{}

// IdentityFrom returns the identity the hub settled on for an /admin request.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// taggedLogin is what WhoIs reports as the login of a tagged node: nobody in particular.
const taggedLogin = "tagged-devices"

// IsOwner says whether id is the hub owner in person, which only matters for setting the first panel
// password: loopback, or a node logged in as the owner, that is the login registered for the owner
// handle, or else the login the hub's own node runs under (SelfLogin). A tagged node is nobody's.
func (h *Hub) IsOwner(id Identity) bool {
	if id.Local {
		return true
	}
	login := strings.ToLower(id.Login)
	if login == "" || login == taggedLogin {
		return false
	}
	if o := h.Cfg.Owners[h.Cfg.Owner]; o != nil && o.Login != "" && strings.EqualFold(o.Login, login) {
		return true
	}
	self := strings.ToLower(h.SelfLogin)
	return self != "" && self != taggedLogin && self == login
}

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

// FriendOf returns the handle of the registered friend id is logged in as, or "".
func (h *Hub) FriendOf(id Identity) string {
	login := strings.ToLower(id.Login)
	if id.Local || login == "" || login == taggedLogin {
		return ""
	}
	for handle, o := range h.Cfg.Owners {
		if handle != h.Cfg.Owner && o != nil && strings.EqualFold(o.Login, login) {
			return handle
		}
	}
	return ""
}

// Without a panel handler (tests, or a hub built without one) /admin answers with a placeholder.
const adminPlaceholder = `<!doctype html>
<html lang="zh-Hans"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>khoomei</title></head>
<body style="font-family: system-ui, sans-serif; margin: 2rem">
<p>khoomei hub 面板还在开发中。</p>
</body></html>
`

func (h *Hub) handleAdmin(w http.ResponseWriter, r *http.Request, id Identity) {
	if h.Admin != nil {
		h.Admin.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
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
