package app

import (
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
)

// adminOnly lists tenant routes reserved for workspace admins: anything that
// changes who can send, from where, or where data flows out of the tenant.
// Everything else under /v1 is available to regular users.
var adminOnly = map[string]bool{
	"POST /v1/domains":                 true,
	"POST /v1/domains/{id}/check":      true,
	"GET /v1/api-keys":                 true,
	"POST /v1/api-keys":                true,
	"DELETE /v1/api-keys/{id}":         true,
	"GET /v1/webhooks":                 true,
	"POST /v1/webhooks":                true,
	"DELETE /v1/webhooks/{id}":         true,
	"POST /v1/team":                    true,
	"PATCH /v1/team/{id}":              true,
	"POST /v1/aliases":                 true,
	"POST /v1/aliases/generate":        true,
	"DELETE /v1/aliases/{id}":          true,
	"POST /v1/inbound/rules":           true,
	"PUT /v1/inbound/rules/{id}":       true,
	"DELETE /v1/inbound/rules/{id}":    true,
	"DELETE /v1/suppressions/{value}":  true,
	"POST /v1/billing/upgrade-request": true,
}

// needsActivePlan lists what stops working once a trial or paid period has
// ended: sending and the prospecting tools. Reading data, managing the
// workspace and asking for an upgrade stay available.
var needsActivePlan = map[string]bool{
	"POST /v1/campaigns/{id}/launch":   true,
	"POST /v1/campaigns/{id}/test":     true,
	"POST /v1/sequences/{id}/activate": true,
	"POST /v1/sequences/{id}/enroll":   true,
	"POST /v1/otp/send":                true,
	"POST /v1/sms":                     true,
	"POST /v1/finder/domain":           true,
	"POST /v1/finder/person":           true,
	"POST /v1/validations":             true,
	"POST /v1/validations/batch":       true,
	"POST /v1/contacts/validate":       true,
}

// Authorize is the role policy applied to every authenticated route.
func Authorize(p authctx.Principal, pattern string) error {
	_, path, _ := strings.Cut(pattern, " ")
	switch {
	case strings.HasPrefix(path, "/v1/admin/"):
		if !p.IsSuperAdmin() {
			return apperr.Forbidden("superadmin_required", "platform super admin role required")
		}
	case adminOnly[pattern]:
		if !p.IsAdmin() {
			return apperr.Forbidden("admin_required", "only workspace admins can do this")
		}
	}
	if needsActivePlan[pattern] && p.PlanExpired && !p.IsSuperAdmin() {
		return apperr.PaymentRequired("plan_expired", "your trial or subscription has ended; choose a plan to continue")
	}
	return nil
}
