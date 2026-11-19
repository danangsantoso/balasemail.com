// Package web serves the public landing page at / and the dashboard at /app:
// a dependency-free single-page app that talks to the same REST API as
// customers do.
package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/billing"
)

//go:embed static
var files embed.FS

//go:embed landing.tmpl
var landingSrc string

var landingTmpl = template.Must(template.New("landing").Funcs(template.FuncMap{"rupiah": billing.Rupiah}).Parse(landingSrc))

// Options are the public contact details shown on the landing page.
type Options struct {
	ContactEmail    string // sales/support address
	ContactWhatsApp string // digits only, international format; empty hides the link
}

// landing renders the page once: plans and contact details only change on deploy.
func landing(o Options) []byte {
	if o.ContactEmail == "" {
		o.ContactEmail = "sales@balasemail.com"
	}
	trial, _ := billing.Get(billing.Trial)
	var paid []billing.Plan
	for _, p := range billing.Catalog() {
		if p.Key != billing.Trial {
			paid = append(paid, p)
		}
	}
	data := map[string]any{
		"Trial": trial, "Paid": paid, "Included": billing.Included,
		"ContactEmail": o.ContactEmail,
		"ContactHref":  template.URL("mailto:" + o.ContactEmail + "?subject=" + url.QueryEscape("Paket Enterprise BalasEmail")), //nolint:gosec // operator-configured address
		"Year":         time.Now().Year(),
	}
	if wa := strings.TrimLeft(o.ContactWhatsApp, "+"); wa != "" {
		data["WhatsAppHref"] = "https://wa.me/" + url.PathEscape(wa)
	}
	var buf bytes.Buffer
	if err := landingTmpl.Execute(&buf, data); err != nil {
		panic("web: render landing page: " + err.Error())
	}
	return buf.Bytes()
}

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

// Routes mounts the landing page at /, the dashboard at /app and their assets
// at /static/ (import samples under /static/contoh/).
func Routes(mux *http.ServeMux, o Options) {
	static, _ := fs.Sub(files, "static")
	assets := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	mux.Handle("GET /static/", secure(assets))
	mux.Handle("GET /static/contoh/", secure(download(assets)))
	mux.Handle("GET /favicon.ico", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "brand/logo-32.png")
	})))
	page := landing(o)
	mux.Handle("GET /{$}", secure(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(page)
	})))
	mux.Handle("GET /app", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})))
}
