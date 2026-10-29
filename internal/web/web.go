// Package web serves the dashboard: a dependency-free single-page app that
// talks to the same REST API as customers do.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var files embed.FS

// csp only allows our own scripts/styles; the dashboard never loads third-party code.
const csp = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// Routes mounts the dashboard at / and its assets at /static/.
func Routes(mux *http.ServeMux) {
	static, _ := fs.Sub(files, "static")
	assets := http.FileServer(http.FS(static))
	mux.Handle("GET /static/", secure(http.StripPrefix("/static/", assets)))
	mux.Handle("GET /{$}", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})))
}
