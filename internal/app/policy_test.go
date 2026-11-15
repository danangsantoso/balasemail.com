package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danangsantoso/balasemail.com/internal/admin"
	"github.com/danangsantoso/balasemail.com/internal/alias"
	"github.com/danangsantoso/balasemail.com/internal/auth"
	"github.com/danangsantoso/balasemail.com/internal/billing"
	"github.com/danangsantoso/balasemail.com/internal/campaign"
	"github.com/danangsantoso/balasemail.com/internal/contact"
	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/finder"
	"github.com/danangsantoso/balasemail.com/internal/inbound"
	"github.com/danangsantoso/balasemail.com/internal/otp"
	"github.com/danangsantoso/balasemail.com/internal/senderdomain"
	"github.com/danangsantoso/balasemail.com/internal/sequence"
	"github.com/danangsantoso/balasemail.com/internal/sms"
	"github.com/danangsantoso/balasemail.com/internal/stats"
	"github.com/danangsantoso/balasemail.com/internal/suppression"
	"github.com/danangsantoso/balasemail.com/internal/validator"
	"github.com/danangsantoso/balasemail.com/internal/webhook"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
)

func TestAuthorize(t *testing.T) {
	user := authctx.Principal{UserID: "u", Role: authctx.RoleUser}
	admin := authctx.Principal{UserID: "a", Role: authctx.RoleAdmin}
	super := authctx.Principal{UserID: "s", Role: authctx.RoleAdmin, PlatformRole: authctx.RoleSuperAdmin}
	apiKey := authctx.Principal{Role: authctx.RoleAdmin, PlatformRole: authctx.RoleSuperAdmin, APIKeyID: "k"}

	cases := []struct {
		p       authctx.Principal
		pattern string
		ok      bool
	}{
		{user, "POST /v1/campaigns", true},
		{user, "POST /v1/contacts/import", true},
		{user, "POST /v1/domains", false},
		{user, "POST /v1/api-keys", false},
		{user, "PATCH /v1/team/{id}", false},
		{user, "GET /v1/team", true},
		{admin, "POST /v1/domains", true},
		{admin, "GET /v1/admin/tenants", false},
		{super, "GET /v1/admin/tenants", true},
		{super, "PATCH /v1/admin/tenants/{id}", true},
		{apiKey, "GET /v1/admin/tenants", false}, // API keys never act as super admin
		{user, "POST /v1/billing/upgrade-request", false},
		{admin, "POST /v1/billing/upgrade-request", true},
	}
	for _, c := range cases {
		err := Authorize(c.p, c.pattern)
		if (err == nil) != c.ok {
			t.Errorf("%+v %s: err=%v want ok=%v", c.p, c.pattern, err, c.ok)
		}
		if err != nil && !apperr.Is(err, apperr.KindForbidden) {
			t.Errorf("want forbidden, got %v", err)
		}
	}
}

// An ended trial or subscription blocks sending and prospecting, nothing else.
func TestAuthorizePlanExpired(t *testing.T) {
	expired := authctx.Principal{UserID: "a", Role: authctx.RoleAdmin, PlanExpired: true}
	super := authctx.Principal{UserID: "s", Role: authctx.RoleAdmin, PlatformRole: authctx.RoleSuperAdmin, PlanExpired: true}
	for pattern, ok := range map[string]bool{
		"POST /v1/campaigns/{id}/launch":   false,
		"POST /v1/otp/send":                false,
		"POST /v1/finder/domain":           false,
		"GET /v1/campaigns":                true,
		"GET /v1/billing":                  true,
		"POST /v1/billing/upgrade-request": true,
		"POST /v1/contacts/import":         true,
	} {
		err := Authorize(expired, pattern)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v want ok=%v", pattern, err, ok)
		}
		if err != nil && !apperr.Is(err, apperr.KindPaymentRequired) {
			t.Errorf("%s: want payment required, got %v", pattern, err)
		}
	}
	if err := Authorize(super, "POST /v1/campaigns/{id}/launch"); err != nil {
		t.Errorf("platform operators are not blocked by their own plan: %v", err)
	}
}

// Every admin-only entry must name a real route, otherwise a typo silently
// leaves the real route open to regular users.
func TestAdminOnlyPatternsExist(t *testing.T) {
	a := &App{Auth: &auth.Service{}, Domains: &senderdomain.Service{}, Validator: &validator.Validator{}, Finder: &finder.Service{},
		Contacts: &contact.Service{}, Suppression: &suppression.Service{}, Campaigns: &campaign.Service{}, Sequences: &sequence.Service{},
		Aliases: &alias.Service{}, Inbound: &inbound.Service{}, OTP: &otp.Service{}, SMS: &sms.Service{}, Webhooks: &webhook.Service{},
		Stats: &stats.Service{}, Admin: &admin.Service{}, Delivery: &delivery.Service{}, Billing: &billing.Service{}}
	mux := http.NewServeMux()
	a.routes(mux)
	patterns := map[string]bool{}
	for p := range adminOnly {
		patterns[p] = true
	}
	for p := range needsActivePlan {
		patterns[p] = true
	}
	for pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		path = strings.NewReplacer("{id}", "x", "{value}", "x").Replace(path)
		if _, got := mux.Handler(httptest.NewRequest(method, path, nil)); got != pattern {
			t.Errorf("policy lists %q but the request is routed to %q", pattern, got)
		}
	}
}
