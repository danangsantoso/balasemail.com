package contact

import (
	"net/http"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/contacts", authed(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /v1/contacts", authed(http.HandlerFunc(s.handleUpsert)))
	mux.Handle("GET /v1/contacts/{id}", authed(http.HandlerFunc(s.handleGet)))
	mux.Handle("DELETE /v1/contacts/{id}", authed(http.HandlerFunc(s.handleDelete)))
	mux.Handle("POST /v1/contacts/import", authed(http.HandlerFunc(s.handleImport)))
	mux.Handle("POST /v1/contacts/validate", authed(http.HandlerFunc(s.handleValidate)))
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := httpx.PageFrom(r)
	cs, err := s.List(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID,
		Filter{Tag: q.Get("tag"), Status: q.Get("validation_status"), Query: q.Get("q")}, p)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.List[Contact]{Data: cs, Limit: p.Limit, Offset: p.Offset})
}

func (s *Service) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	c, err := s.Upsert(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	c, err := s.Get(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

// DELETE /v1/contacts/{id}?suppress=true handles data-erasure requests.
func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	suppress := r.URL.Query().Get("suppress") == "true"
	if err := s.Delete(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"), suppress); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/contacts/import — multipart form with "file" (.csv, .xlsx or .txt) and optional "tags".
func (s *Service) handleImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	// Body size is capped by MaxBytesReader above; 8MB is held in memory, the rest spills to disk.
	if err := r.ParseMultipartForm(8 << 20); err != nil { //nolint:gosec // G120: bounded by MaxBytesReader
		httpx.Error(w, r, apperr.Invalid("invalid_form", "expected multipart/form-data with a 'file' field (.csv, .xlsx or .txt, max 50MB)"))
		return
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, r, apperr.Invalid("missing_file", "field 'file' is required"))
		return
	}
	defer func() { _ = f.Close() }()
	var tags []string
	if t := r.FormValue("tags"); t != "" {
		tags = strings.Split(t, ",")
	}
	rep, err := s.Import(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, fh.Filename, f, tags)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rep)
}

func (s *Service) handleValidate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tag string `json:"tag"`
	}
	if r.ContentLength > 0 {
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	if err := s.EnqueueValidation(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in.Tag); err != nil {
		httpx.Error(w, r, apperr.Internal(err))
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
