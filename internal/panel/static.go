package panel

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist is the built front end (web/, `npm run build`). It is committed so that go build needs no Node.
//
//go:embed all:dist
var dist embed.FS

// staticHandler serves the front end under /admin/: hashed assets are cached for good, and every other
// path falls back to index.html so a reload keeps the view.
func staticHandler() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/admin")
		name = strings.TrimPrefix(name, "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(sub, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + name
				files.ServeHTTP(w, r2)
				return
			}
		}
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "panel not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
}
