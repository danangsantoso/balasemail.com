package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("api")) })
	Routes(mux, Options{ContactWhatsApp: "+628123456789"})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := get("/")
	if resp.StatusCode != 200 {
		t.Fatalf("landing: %d", resp.StatusCode)
	}
	for _, want := range []string{"Coba Gratis 3 Hari", "30 email per hari", "Rp99.000", "Rp2.990.000", `class="plan popular"`,
		`href="/app#/daftar?paket=growth"`, "https://wa.me/628123456789", "mailto:sales@balasemail.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page is missing %q", want)
		}
	}
	if strings.Contains(body, "style=") {
		t.Error("landing page must not use inline styles (blocked by the CSP)")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("landing CSP = %q", csp)
	}

	resp, body = get("/app")
	if resp.StatusCode != 200 || !strings.Contains(body, `<script src="/static/app.js">`) {
		t.Fatalf("dashboard: %d %q", resp.StatusCode, body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	for _, asset := range []string{"/static/app.js", "/static/app.css", "/static/landing.css", "/static/landing.js", "/static/brand/dashboard.jpg", "/static/brand/logo-mark.svg", "/static/brand/favicon.svg", "/favicon.ico"} {
		if resp, _ := get(asset); resp.StatusCode != 200 {
			t.Errorf("%s = %d", asset, resp.StatusCode)
		}
	}
	samples := map[string]string{
		"/static/contoh/kontak-contoh.xlsx": "spreadsheetml",
		"/static/contoh/kontak-contoh.csv":  "text/csv",
		"/static/contoh/kontak-contoh.txt":  "text/plain",
	}
	for p, ctype := range samples {
		resp, body := get(p)
		if resp.StatusCode != 200 || len(body) == 0 || !strings.Contains(resp.Header.Get("Content-Type"), ctype) ||
			!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: %d %q %q", p, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
		}
	}
	if _, body := get("/v1/me"); body != "api" {
		t.Error("dashboard must not shadow API routes")
	}
	if resp, _ := get("/does-not-exist"); resp.StatusCode != 404 {
		t.Errorf("unknown path = %d, want 404", resp.StatusCode)
	}
}
