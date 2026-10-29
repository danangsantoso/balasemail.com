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
	Routes(mux)
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
	if resp.StatusCode != 200 || !strings.Contains(body, `<script src="/static/app.js">`) {
		t.Fatalf("index: %d %q", resp.StatusCode, body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	for _, asset := range []string{"/static/app.js", "/static/app.css"} {
		if resp, _ := get(asset); resp.StatusCode != 200 {
			t.Errorf("%s = %d", asset, resp.StatusCode)
		}
	}
	if _, body := get("/v1/me"); body != "api" {
		t.Error("dashboard must not shadow API routes")
	}
	if resp, _ := get("/does-not-exist"); resp.StatusCode != 404 {
		t.Errorf("unknown path = %d, want 404", resp.StatusCode)
	}
}
