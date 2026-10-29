package delivery

import (
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
)

// 1x1 transparent GIF.
var pixel, _ = base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")

var unsubPage = template.Must(template.New("u").Parse(`<!doctype html><html lang="id"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Berhenti berlangganan</title>
<style>body{font-family:system-ui,sans-serif;max-width:480px;margin:64px auto;padding:0 16px;color:#222}
button{padding:10px 18px;font-size:16px;cursor:pointer}</style></head><body>
{{if .Done}}<h1>Anda telah berhenti berlangganan</h1><p>You have been unsubscribed. Anda tidak akan menerima email ini lagi.</p>
{{else}}<h1>Berhenti berlangganan?</h1><p>Unsubscribe from these emails?</p>
<form method="post"><button type="submit">Ya, berhenti berlangganan / Yes, unsubscribe</button></form>{{end}}
</body></html>`))

// PublicRoutes registers unauthenticated tracking and unsubscribe endpoints.
func (s *Service) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /t/o/{token}", s.handleOpen)
	mux.HandleFunc("GET /t/c/{token}", s.handleClick)
	mux.HandleFunc("GET /u/{token}", s.handleUnsubPage)
	mux.HandleFunc("POST /u/{token}", s.handleUnsub) // RFC 8058 one-click target
}

func (s *Service) verify(token, prefix string) (string, bool) {
	payload, err := s.signer.Verify(token)
	if err != nil {
		return "", false
	}
	return strings.CutPrefix(payload, prefix)
}

func (s *Service) handleOpen(w http.ResponseWriter, r *http.Request) {
	if id, ok := s.verify(r.PathValue("token"), "o:"); ok {
		if err := s.RecordOpen(r.Context(), id); err != nil {
			slog.Error("record open", "err", err)
		}
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	_, _ = w.Write(pixel)
}

func (s *Service) handleClick(w http.ResponseWriter, r *http.Request) {
	payload, ok := s.verify(r.PathValue("token"), "c:")
	id, target, found := strings.Cut(payload, "\n")
	if !ok || !found || !(strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://")) {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	if err := s.RecordClick(r.Context(), id); err != nil {
		slog.Error("record click", "err", err)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Service) handleUnsubPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.verify(r.PathValue("token"), "u:"); !ok {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	// GET never unsubscribes: link scanners prefetch URLs.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = unsubPage.Execute(w, map[string]bool{"Done": false})
}

func (s *Service) handleUnsub(w http.ResponseWriter, r *http.Request) {
	id, ok := s.verify(r.PathValue("token"), "u:")
	if !ok {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	if err := s.Unsubscribe(r.Context(), id); err != nil {
		slog.Error("unsubscribe", "err", err)
		http.Error(w, "please try again", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = unsubPage.Execute(w, map[string]bool{"Done": true})
}
