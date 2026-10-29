// Package auth implements registration (business email only, PRD F5),
// login, JWT sessions and API keys for the REST API and SMTP gateway.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

const apiKeyPrefix = "bm_"

type Workspace struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Plan           string    `json:"plan"`
	DailySendLimit int       `json:"daily_send_limit"`
	SMSCredits     int       `json:"sms_credits"`
	PostalAddress  string    `json:"postal_address"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

type User struct {
	ID           string     `json:"id"`
	WorkspaceID  string     `json:"workspace_id"`
	Email        string     `json:"email"`
	FullName     string     `json:"full_name"`
	Role         string     `json:"role"`
	PlatformRole string     `json:"platform_role,omitempty"`
	Disabled     bool       `json:"disabled"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

const userCols = `id, workspace_id, email, full_name, role, platform_role, disabled_at IS NOT NULL, last_login_at, created_at`

func scanUser(row pgx.Row, u *User, extra ...any) error {
	return row.Scan(append([]any{&u.ID, &u.WorkspaceID, &u.Email, &u.FullName, &u.Role, &u.PlatformRole, &u.Disabled, &u.LastLoginAt, &u.CreatedAt}, extra...)...)
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
}

type RegisterInput struct {
	CompanyName   string `json:"company_name"`
	FullName      string `json:"full_name"`
	Email         string `json:"email"`
	Password      string `json:"password"`
	PostalAddress string `json:"postal_address"`
}

type Service struct {
	db  *pgxpool.Pool
	jwt *security.JWTIssuer
	// superAdmins are emails promoted to platform super admin on sign-in (SUPERADMIN_EMAILS).
	superAdmins map[string]bool
}

func NewService(db *pgxpool.Pool, jwt *security.JWTIssuer, superAdminEmails []string) *Service {
	sa := map[string]bool{}
	for _, e := range superAdminEmails {
		if n, err := emailaddr.Normalize(e); err == nil {
			sa[n] = true
		}
	}
	return &Service{db: db, jwt: jwt, superAdmins: sa}
}

// ValidateRegistration enforces the business-email-only rule (PRD F5).
func ValidateRegistration(in RegisterInput) (emailaddr.Address, error) {
	addr, err := emailaddr.Parse(in.Email)
	if err != nil {
		return addr, apperr.Invalid("invalid_email", "email address is not valid")
	}
	if emailaddr.IsDisposable(addr.Domain) {
		return addr, apperr.Invalid("disposable_email", "disposable email addresses are not allowed")
	}
	if emailaddr.IsFreeProvider(addr.Domain) {
		return addr, apperr.Invalid("business_email_required", "please register with your company email (not "+addr.Domain+")")
	}
	if strings.TrimSpace(in.CompanyName) == "" || strings.TrimSpace(in.FullName) == "" {
		return addr, apperr.Invalid("missing_fields", "company_name and full_name are required")
	}
	if len(in.Password) < 10 {
		return addr, apperr.Invalid("weak_password", "password must be at least 10 characters")
	}
	return addr, nil
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	addr, err := ValidateRegistration(in)
	if err != nil {
		return Session{}, err
	}
	hash, err := security.HashPassword(in.Password)
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	var u User
	err = database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		var wsID string
		if err := tx.QueryRow(ctx, `INSERT INTO workspaces (name, postal_address) VALUES ($1,$2) RETURNING id`,
			strings.TrimSpace(in.CompanyName), strings.TrimSpace(in.PostalAddress)).Scan(&wsID); err != nil {
			return err
		}
		return scanUser(tx.QueryRow(ctx, `
			INSERT INTO users (workspace_id, email, full_name, password_hash, role)
			VALUES ($1,$2,$3,$4,'admin')
			RETURNING `+userCols,
			wsID, addr.String(), strings.TrimSpace(in.FullName), hash), &u)
	})
	if database.IsUniqueViolation(err) {
		return Session{}, apperr.Conflict("email_taken", "an account with this email already exists")
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return s.session(u)
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	norm, _ := emailaddr.Normalize(email)
	var u User
	var hash string
	err := scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+`, password_hash FROM users WHERE email=$1`, norm), &u, &hash)
	if err != nil && !database.IsNoRows(err) {
		return Session{}, apperr.Internal(err)
	}
	if err != nil || !security.CheckPassword(hash, password) {
		return Session{}, apperr.Unauthorized("invalid_credentials", "email or password is incorrect")
	}
	if u.Disabled {
		return Session{}, apperr.Forbidden("user_disabled", "this account has been disabled by your administrator")
	}
	_, _ = s.db.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, u.ID)
	return s.session(u)
}

func (s *Service) session(u User) (Session, error) {
	if s.superAdmins[u.Email] && u.PlatformRole != authctx.RoleSuperAdmin {
		if _, err := s.db.Exec(context.Background(), `UPDATE users SET platform_role='superadmin' WHERE id=$1`, u.ID); err == nil {
			u.PlatformRole = authctx.RoleSuperAdmin
		}
	}
	tok, exp, err := s.jwt.Issue(u.ID, u.WorkspaceID, u.Role)
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return Session{Token: tok, ExpiresAt: exp, User: u}, nil
}

// Authenticate implements httpx.Authenticator for JWTs and API keys.
func (s *Service) Authenticate(ctx context.Context, bearer string) (authctx.Principal, error) {
	if strings.HasPrefix(bearer, apiKeyPrefix) {
		return s.AuthenticateAPIKey(ctx, bearer)
	}
	c, err := s.jwt.Parse(bearer)
	if err != nil {
		return authctx.Principal{}, apperr.Unauthorized("invalid_token", "session token is invalid or expired")
	}
	// Role and status are read fresh so demotions and deactivations apply immediately.
	p := authctx.Principal{UserID: c.Subject, WorkspaceID: c.WorkspaceID}
	var disabled bool
	var wsStatus string
	err = s.db.QueryRow(ctx, `SELECT u.role, u.platform_role, u.disabled_at IS NOT NULL, w.status
		FROM users u JOIN workspaces w ON w.id = u.workspace_id WHERE u.id=$1 AND u.workspace_id=$2`, c.Subject, c.WorkspaceID).
		Scan(&p.Role, &p.PlatformRole, &disabled, &wsStatus)
	switch {
	case database.IsNoRows(err):
		return p, apperr.Unauthorized("invalid_token", "account no longer exists")
	case err != nil:
		return p, apperr.Internal(err)
	case disabled:
		return p, apperr.Forbidden("user_disabled", "this account has been disabled by your administrator")
	case wsStatus != "active" && !p.IsSuperAdmin():
		return p, apperr.Forbidden("workspace_suspended", "workspace is suspended; contact support")
	}
	return p, nil
}

// AuthenticateAPIKey validates "bm_<prefix>_<secret>" keys (also used as SMTP passwords).
func (s *Service) AuthenticateAPIKey(ctx context.Context, key string) (authctx.Principal, error) {
	bad := apperr.Unauthorized("invalid_api_key", "API key is invalid or revoked")
	rest, ok := strings.CutPrefix(key, apiKeyPrefix)
	if !ok {
		return authctx.Principal{}, bad
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok || prefix == "" || secret == "" {
		return authctx.Principal{}, bad
	}
	var id, wsID, hash string
	var revoked *time.Time
	err := s.db.QueryRow(ctx, `SELECT id, workspace_id, secret_hash, revoked_at FROM api_keys WHERE prefix=$1`, prefix).
		Scan(&id, &wsID, &hash, &revoked)
	if err != nil || revoked != nil || !security.ConstantTimeEqual(hash, security.SHA256(secret)) {
		return authctx.Principal{}, bad
	}
	_, _ = s.db.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
	return authctx.Principal{WorkspaceID: wsID, Role: authctx.RoleAdmin, APIKeyID: id}, s.checkActive(ctx, wsID)
}

func (s *Service) checkActive(ctx context.Context, wsID string) error {
	var status string
	if err := s.db.QueryRow(ctx, `SELECT status FROM workspaces WHERE id=$1`, wsID).Scan(&status); err != nil {
		return apperr.Unauthorized("workspace_not_found", "workspace no longer exists")
	}
	if status != "active" {
		return apperr.Forbidden("workspace_suspended", "workspace is suspended; contact support")
	}
	return nil
}

func (s *Service) Me(ctx context.Context, p authctx.Principal) (User, Workspace, error) {
	var u User
	var w Workspace
	if p.UserID != "" {
		if err := scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, p.UserID), &u); err != nil {
			return u, w, apperr.NotFound("user_not_found", "user not found")
		}
	}
	w, err := s.Workspace(ctx, p.WorkspaceID)
	return u, w, err
}

func (s *Service) Workspace(ctx context.Context, id string) (Workspace, error) {
	var w Workspace
	err := s.db.QueryRow(ctx, `
		SELECT id, name, plan, daily_send_limit, sms_credits, postal_address, status, created_at
		FROM workspaces WHERE id=$1`, id).
		Scan(&w.ID, &w.Name, &w.Plan, &w.DailySendLimit, &w.SMSCredits, &w.PostalAddress, &w.Status, &w.CreatedAt)
	if database.IsNoRows(err) {
		return w, apperr.NotFound("workspace_not_found", "workspace not found")
	}
	if err != nil {
		return w, apperr.Internal(err)
	}
	return w, nil
}

// CreateAPIKey returns the plaintext key once; only its hash is stored.
func (s *Service) CreateAPIKey(ctx context.Context, wsID, name string) (string, APIKey, error) {
	if strings.TrimSpace(name) == "" {
		return "", APIKey{}, apperr.Invalid("missing_name", "name is required")
	}
	prefix, secret := security.RandomHex(6), security.RandomHex(16)
	var k APIKey
	err := s.db.QueryRow(ctx, `
		INSERT INTO api_keys (workspace_id, name, prefix, secret_hash) VALUES ($1,$2,$3,$4)
		RETURNING id, name, prefix, created_at`, wsID, name, prefix, security.SHA256(secret),
	).Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt)
	if err != nil {
		return "", k, apperr.Internal(err)
	}
	return apiKeyPrefix + prefix + "_" + secret, k, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, wsID string) ([]APIKey, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, prefix, last_used_at, revoked_at, created_at FROM api_keys
		WHERE workspace_id=$1 ORDER BY created_at DESC`, wsID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) {
		var k APIKey
		err := r.Scan(&k.ID, &k.Name, &k.Prefix, &k.LastUsedAt, &k.RevokedAt, &k.CreatedAt)
		return k, err
	})
}

func (s *Service) RevokeAPIKey(ctx context.Context, wsID, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND workspace_id=$2 AND revoked_at IS NULL`, id, wsID)
	if err != nil {
		return apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("api_key_not_found", "API key not found")
	}
	return nil
}
