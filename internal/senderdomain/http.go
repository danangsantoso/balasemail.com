package senderdomain

import (
	"net/http"

	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/domains", authed(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /v1/domains", authed(http.HandlerFunc(s.handleAdd)))
	mux.Handle("GET /v1/domains/{id}", authed(http.HandlerFunc(s.handleGet)))
	mux.Handle("POST /v1/domains/{id}/check", authed(http.HandlerFunc(s.handleCheck)))
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	ds, err := s.List(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": ds})
}

func (s *Service) handleAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	d, err := s.Add(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in.Name)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, d)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	d, err := s.Get(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) {
	d, err := s.Check(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}
