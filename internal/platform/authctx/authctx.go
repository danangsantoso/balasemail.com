// Package authctx carries the authenticated principal through a request context.
package authctx

import "context"

// Principal is the authenticated caller: a dashboard user (JWT) or an API key.
type Principal struct {
	UserID      string
	WorkspaceID string
	Role        string
	APIKeyID    string
}

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
