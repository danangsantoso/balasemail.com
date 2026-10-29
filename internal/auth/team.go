package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

// Team management: tenant admins add users to their own workspace and
// change their role or access. Every query is scoped by workspace_id.

type MemberInput struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

type MemberPatch struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

func validRole(r string) bool { return r == authctx.RoleAdmin || r == authctx.RoleUser }

func (s *Service) Team(ctx context.Context, wsID string) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users WHERE workspace_id=$1 ORDER BY role, created_at`, wsID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		return u, scanUser(r, &u)
	})
}

// AddMember creates a user in the caller's workspace with an initial password.
func (s *Service) AddMember(ctx context.Context, wsID string, in MemberInput) (User, error) {
	addr, err := emailaddr.Parse(in.Email)
	if err != nil {
		return User{}, apperr.Invalid("invalid_email", "email address is not valid")
	}
	if !emailaddr.IsBusiness(addr) {
		return User{}, apperr.Invalid("business_email_required", "team members must use a company email")
	}
	if in.Role == "" {
		in.Role = authctx.RoleUser
	}
	if !validRole(in.Role) {
		return User{}, apperr.Invalid("invalid_role", "role must be admin or user")
	}
	if strings.TrimSpace(in.FullName) == "" {
		return User{}, apperr.Invalid("missing_fields", "full_name is required")
	}
	if len(in.Password) < 10 {
		return User{}, apperr.Invalid("weak_password", "password must be at least 10 characters")
	}
	hash, err := security.HashPassword(in.Password)
	if err != nil {
		return User{}, apperr.Internal(err)
	}
	var u User
	err = scanUser(s.db.QueryRow(ctx, `INSERT INTO users (workspace_id, email, full_name, password_hash, role)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+userCols, wsID, addr.String(), strings.TrimSpace(in.FullName), hash, in.Role), &u)
	if database.IsUniqueViolation(err) {
		return User{}, apperr.Conflict("email_taken", "an account with this email already exists")
	}
	if err != nil {
		return User{}, apperr.Internal(err)
	}
	return u, nil
}

// UpdateMember changes role and/or access. A workspace always keeps at least
// one active admin, and admins cannot lock themselves out.
func (s *Service) UpdateMember(ctx context.Context, p authctx.Principal, id string, patch MemberPatch) (User, error) {
	if patch.Role != nil && !validRole(*patch.Role) {
		return User{}, apperr.Invalid("invalid_role", "role must be admin or user")
	}
	if id == p.UserID && ((patch.Role != nil && *patch.Role != authctx.RoleAdmin) || (patch.Disabled != nil && *patch.Disabled)) {
		return User{}, apperr.Invalid("self_lockout", "you cannot demote or disable your own account")
	}
	var u User
	err := database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		// Lock the workspace's admins so concurrent demotions cannot remove the last one.
		if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE workspace_id=$1 AND role='admin' FOR UPDATE`, p.WorkspaceID); err != nil {
			return err
		}
		err := scanUser(tx.QueryRow(ctx, `UPDATE users SET
				role = COALESCE($3, role),
				disabled_at = CASE WHEN $4::boolean IS NULL THEN disabled_at WHEN $4 THEN coalesce(disabled_at, now()) ELSE NULL END
			WHERE id=$1 AND workspace_id=$2 RETURNING `+userCols, id, p.WorkspaceID, patch.Role, patch.Disabled), &u)
		if database.IsNoRows(err) {
			return apperr.NotFound("user_not_found", "user not found in this workspace")
		}
		if err != nil {
			return err
		}
		var admins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE workspace_id=$1 AND role='admin' AND disabled_at IS NULL`, p.WorkspaceID).Scan(&admins); err != nil {
			return err
		}
		if admins == 0 {
			return apperr.Invalid("last_admin", "the workspace must keep at least one active admin")
		}
		return nil
	})
	if err != nil {
		return User{}, apperr.As(err)
	}
	return u, nil
}

func (s *Service) teamRoutes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/team", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		users, err := s.Team(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID)
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": users}, err)
	})))
	mux.Handle("POST /v1/team", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in MemberInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		u, err := s.AddMember(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
		httpx.Respond(w, r, http.StatusCreated, u, err)
	})))
	mux.Handle("PATCH /v1/team/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in MemberPatch
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		u, err := s.UpdateMember(r.Context(), authctx.MustFrom(r.Context()), r.PathValue("id"), in)
		httpx.Respond(w, r, http.StatusOK, u, err)
	})))
}
