// Package web serves the dashboard: a dependency-free single-page app that
// talks to the same REST API as customers do.
package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
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

// Distroless images ship no /etc/mime.types, so register what the samples need.
func init() {
	_ = mime.AddExtensionType(".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	_ = mime.AddExtensionType(".csv", "text/csv; charset=utf-8")
	_ = mime.AddExtensionType(".txt", "text/plain; charset=utf-8")
}

// download makes sample files save to disk instead of opening in the browser.
func download(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(r.URL.Path)+`"`)
		h.ServeHTTP(w, r)
	})
}

// Routes mounts the dashboard at / and its assets at /static/ (import samples under /static/contoh/).
func Routes(mux *http.ServeMux) {
	static, _ := fs.Sub(files, "static")
	assets := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	mux.Handle("GET /static/", secure(assets))
	mux.Handle("GET /static/contoh/", secure(download(assets)))
	mux.Handle("GET /{$}", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})))
}
