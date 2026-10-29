package sequence

import (
	"net/http"

	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	h := func(fn func(w http.ResponseWriter, r *http.Request, ws string)) http.Handler {
		return authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fn(w, r, authctx.MustFrom(r.Context()).WorkspaceID)
		}))
	}
	decode := func(w http.ResponseWriter, r *http.Request) (Input, bool) {
		var in Input
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return in, false
		}
		return in, true
	}
	mux.Handle("GET /v1/sequences", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		seqs, err := s.List(r.Context(), ws)
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": seqs}, err)
	}))
	mux.Handle("POST /v1/sequences", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		if in, ok := decode(w, r); ok {
			seq, err := s.Create(r.Context(), ws, in)
			httpx.Respond(w, r, http.StatusCreated, seq, err)
		}
	}))
	mux.Handle("GET /v1/sequences/{id}", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		seq, err := s.Get(r.Context(), ws, r.PathValue("id"))
		httpx.Respond(w, r, http.StatusOK, seq, err)
	}))
	mux.Handle("PUT /v1/sequences/{id}", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		if in, ok := decode(w, r); ok {
			seq, err := s.Update(r.Context(), ws, r.PathValue("id"), in)
			httpx.Respond(w, r, http.StatusOK, seq, err)
		}
	}))
	mux.Handle("POST /v1/sequences/{id}/activate", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		seq, err := s.SetStatus(r.Context(), ws, r.PathValue("id"), "active")
		httpx.Respond(w, r, http.StatusOK, seq, err)
	}))
	mux.Handle("POST /v1/sequences/{id}/pause", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		seq, err := s.SetStatus(r.Context(), ws, r.PathValue("id"), "paused")
		httpx.Respond(w, r, http.StatusOK, seq, err)
	}))
	mux.Handle("POST /v1/sequences/{id}/enroll", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		var in struct {
			ContactIDs []string `json:"contact_ids"`
			Tag        string   `json:"tag"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		n, err := s.Enroll(r.Context(), ws, r.PathValue("id"), in.ContactIDs, in.Tag)
		httpx.Respond(w, r, http.StatusOK, map[string]int{"enrolled": n}, err)
	}))
	mux.Handle("GET /v1/sequences/{id}/report", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		rep, err := s.Report(r.Context(), ws, r.PathValue("id"))
		httpx.Respond(w, r, http.StatusOK, rep, err)
	}))
}
