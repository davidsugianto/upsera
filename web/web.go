// Package web embeds the built dashboard (dist/app) into the server binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; " +
	"frame-ancestors 'none'; form-action 'self'"

// notBuiltPage is served when the binary was built without the dashboard.
const notBuiltPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Upsera</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>Upsera</h1>
<p>The Upsera dashboard is not built into this binary. Run <code>npm --prefix web ci &amp;&amp; npm --prefix web run build</code>, then rebuild the server.</p>
<p>API docs: <a href="/api/docs">/api/docs</a></p>
</body></html>
`

// Handler serves the dashboard with history-mode fallback.
func Handler() http.Handler { return newHandler(dist) }

func newHandler(fsys fs.FS) http.Handler {
	app, err := fs.Sub(fsys, "dist/app")
	var index []byte
	if err == nil {
		index, err = fs.ReadFile(app, "index.html")
	}
	built := err == nil

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")

		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"title":"Not Found","status":404}`))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if !built {
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(notBuiltPage))
			return
		}

		if p := strings.TrimPrefix(path.Clean(r.URL.Path), "/"); p != "" && p != "index.html" {
			if fi, err := fs.Stat(app, p); err == nil && fi.Mode().IsRegular() {
				if strings.HasPrefix(p, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					h.Set("Cache-Control", "no-cache")
				}
				http.ServeFileFS(w, r, app, p)
				return
			}
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(index)
	})
}
