// Package authctx carries the authenticated principal through a request context.
package authctx

import "context"

// Principal is the authenticated caller: a dashboard user (JWT) or an API key.
type Principal struct {
	UserID       string
	WorkspaceID  string
	Role         string // tenant role: admin | user
	PlatformRole string // "superadmin" for platform operators, otherwise empty
	APIKeyID     string
}

// Tenant roles and the platform role.
const (
	RoleAdmin      = "admin"
	RoleUser       = "user"
	RoleSuperAdmin = "superadmin"
)

// IsAdmin reports whether the caller administers its tenant (API keys act as admin).
func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

// IsSuperAdmin reports whether the caller operates the whole platform.
func (p Principal) IsSuperAdmin() bool { return p.PlatformRole == RoleSuperAdmin && p.APIKeyID == "" }

// ViaAPIKey reports whether the caller authenticated with an API key.
func (p Principal) ViaAPIKey() bool { return p.APIKeyID != "" }

type ctxKey struct{}

func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustFrom returns the principal or panics; use only behind auth middleware.
func MustFrom(ctx context.Context) Principal {
	p, ok := From(ctx)
	if !ok {
		panic("authctx: no principal in context")
	}
	return p
}
