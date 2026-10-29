package auth

import (
	"net/http"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

// Routes registers auth endpoints. Public endpoints are rate limited per IP.
func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	limited := httpx.RateLimitByIP(20, 10)
	mux.Handle("POST /v1/auth/register", limited(http.HandlerFunc(s.handleRegister)))
	mux.Handle("POST /v1/auth/login", limited(http.HandlerFunc(s.handleLogin)))
	mux.Handle("GET /v1/me", authed(http.HandlerFunc(s.handleMe)))
	mux.Handle("GET /v1/api-keys", authed(http.HandlerFunc(s.handleListKeys)))
	mux.Handle("POST /v1/api-keys", authed(http.HandlerFunc(s.handleCreateKey)))
	mux.Handle("DELETE /v1/api-keys/{id}", authed(http.HandlerFunc(s.handleRevokeKey)))
	s.teamRoutes(mux, authed)
}

func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	sess, err := s.Register(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, sess)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	sess, err := s.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sess)
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ws, err := s.Me(r.Context(), authctx.MustFrom(r.Context()))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "workspace": ws})
}

func requireAdmin(p authctx.Principal) error {
	if !p.IsAdmin() {
		return apperr.Forbidden("forbidden", "admin role required")
	}
	return nil
}

func (s *Service) handleListKeys(w http.ResponseWriter, r *http.Request) {
	p := authctx.MustFrom(r.Context())
	keys, err := s.ListAPIKeys(r.Context(), p.WorkspaceID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": keys})
}

func (s *Service) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	p := authctx.MustFrom(r.Context())
	if err := requireAdmin(p); err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	plain, k, err := s.CreateAPIKey(r.Context(), p.WorkspaceID, in.Name)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"key": plain, "api_key": k,
		"note": "store this key now; it will not be shown again. Use it as Bearer token or SMTP password."})
}

func (s *Service) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	p := authctx.MustFrom(r.Context())
	if err := requireAdmin(p); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := s.RevokeAPIKey(r.Context(), p.WorkspaceID, r.PathValue("id")); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
