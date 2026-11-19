// Package suppression maintains the do-not-contact list (PRD §8). Every
// send path must consult it; entries can be workspace-scoped or global.
package suppression

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

const (
	ReasonUnsubscribe = "unsubscribe"
	ReasonHardBounce  = "hard_bounce"
	ReasonComplaint   = "complaint"
	ReasonManual      = "manual"
	ReasonErasure     = "erasure_request"
)

type Entry struct {
	Value     string    `json:"value"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason"`
	Global    bool      `json:"global"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct{ db database.DBTX }

func NewService(db database.DBTX) *Service { return &Service{db: db} }

// WithTx returns a Service bound to a transaction.
func (s *Service) WithTx(tx database.DBTX) *Service { return &Service{db: tx} }

// normalize returns the stored value and its kind (email or domain).
func normalize(v string) (string, string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if strings.Contains(v, "@") {
		n, err := emailaddr.Normalize(v)
		return n, "email", err
	}
	if !emailaddr.ValidDomain(v) {
		return "", "", emailaddr.ErrInvalid
	}
	return v, "domain", nil
}

// Add suppresses an email or domain. wsID "" means global (platform-wide).
func (s *Service) Add(ctx context.Context, wsID, value, reason string) error {
	v, kind, err := normalize(value)
	if err != nil {
		return apperr.Invalid("invalid_value", "value must be an email address or a domain")
	}
	var ws any
	if wsID != "" {
		ws = wsID
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO suppressions (workspace_id, value, kind, reason) VALUES ($1,$2,$3,$4)
		ON CONFLICT ON CONSTRAINT suppressions_ws_value_key DO NOTHING`, ws, v, kind, reason)
	if err != nil {
		return apperr.Internal(err)
	}
	if kind == "email" && wsID != "" {
		_, err = s.db.Exec(ctx, `UPDATE contacts SET unsubscribed_at=coalesce(unsubscribed_at, now()), updated_at=now()
			WHERE workspace_id=$1 AND email=$2`, wsID, v)
	}
	return err
}

// Check returns the suppression reason for email ("" when allowed).
func (s *Service) Check(ctx context.Context, wsID, email string) (string, error) {
	addr, err := emailaddr.Parse(email)
	if err != nil {
		return "invalid_address", nil
	}
	var reason string
	err = s.db.QueryRow(ctx, `
		SELECT reason FROM suppressions
		WHERE (workspace_id=$1 OR workspace_id IS NULL) AND value IN ($2,$3)
		LIMIT 1`, wsID, addr.String(), addr.Domain).Scan(&reason)
	if database.IsNoRows(err) {
		return "", nil
	}
	return reason, err
}

func (s *Service) List(ctx context.Context, wsID string, p httpx.Page) ([]Entry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT value, kind, reason, workspace_id IS NULL, created_at FROM suppressions
		WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, wsID, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := r.Scan(&e.Value, &e.Kind, &e.Reason, &e.Global, &e.CreatedAt)
		return e, err
	})
}

func (s *Service) Remove(ctx context.Context, wsID, value string) error {
	v, _, err := normalize(value)
	if err != nil {
		return apperr.Invalid("invalid_value", "value must be an email address or a domain")
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM suppressions WHERE workspace_id=$1 AND value=$2 AND reason NOT IN ('complaint','erasure_request')`, wsID, v)
	if err != nil {
		return apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("not_found", "entry not found or cannot be removed (complaints and erasure requests are permanent)")
	}
	return nil
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/suppressions", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		es, err := s.List(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, httpx.PageFrom(r))
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"data": es})
	})))
	mux.Handle("POST /v1/suppressions", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Values []string `json:"values"`
			Reason string   `json:"reason"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if in.Reason == "" {
			in.Reason = ReasonManual
		}
		ws := authctx.MustFrom(r.Context()).WorkspaceID
		for _, v := range in.Values {
			if err := s.Add(r.Context(), ws, v, in.Reason); err != nil {
				httpx.Error(w, r, err)
				return
			}
		}
		httpx.JSON(w, http.StatusCreated, map[string]int{"added": len(in.Values)})
	})))
	mux.Handle("DELETE /v1/suppressions/{value}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Remove(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("value")); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
