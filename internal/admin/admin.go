// Package admin is the platform super-admin console: every tenant
// (workspace) on the platform, its usage, and controls for plan, quota,
// SMS credits and suspension. Routes live under /v1/admin/ and are gated
// to platform super admins by the app's role policy.
package admin

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/stats"
)

var Plans = []string{"starter", "growth", "business", "enterprise"}

type Tenant struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Plan           string    `json:"plan"`
	Status         string    `json:"status"`
	DailySendLimit int       `json:"daily_send_limit"`
	SMSCredits     int       `json:"sms_credits"`
	AdminEmail     string    `json:"admin_email"`
	Users          int       `json:"users"`
	Contacts       int       `json:"contacts"`
	Sent30d        int       `json:"sent_30d"`
	Bounced30d     int       `json:"bounced_30d"`
	CreatedAt      time.Time `json:"created_at"`
}

type TenantPatch struct {
	Status         *string `json:"status"`
	Plan           *string `json:"plan"`
	DailySendLimit *int    `json:"daily_send_limit"`
	AddSMSCredits  *int    `json:"add_sms_credits"`
}

type DayCount struct {
	Date    string `json:"date"`
	Sent    int    `json:"sent"`
	Signups int    `json:"signups"`
}

type Platform struct {
	Days       int        `json:"days"`
	Tenants    int        `json:"tenants"`
	Active     int        `json:"active"`
	Suspended  int        `json:"suspended"`
	Users      int        `json:"users"`
	Sent       int        `json:"sent"`
	Bounced    int        `json:"bounced"`
	Complaints int        `json:"complaints"`
	Series     []DayCount `json:"series"`
	TopTenants []Tenant   `json:"top_tenants"`
	ByPlan     []struct {
		Plan  string `json:"plan"`
		Count int    `json:"count"`
	} `json:"by_plan"`
}

type Service struct{ db *pgxpool.Pool }

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

const tenantSelect = `
	SELECT w.id, w.name, w.plan, w.status, w.daily_send_limit, w.sms_credits,
	       coalesce((SELECT u.email FROM users u WHERE u.workspace_id=w.id AND u.role='admin' ORDER BY u.created_at LIMIT 1), ''),
	       (SELECT count(*) FROM users u WHERE u.workspace_id=w.id),
	       (SELECT count(*) FROM contacts c WHERE c.workspace_id=w.id),
	       coalesce(s.sent, 0), coalesce(s.bounced, 0), w.created_at
	FROM workspaces w
	LEFT JOIN (
		SELECT workspace_id, count(*) FILTER (WHERE status IN ('sent','bounced')) AS sent, count(*) FILTER (WHERE status='bounced') AS bounced
		FROM messages WHERE sent_at > now() - interval '30 days' GROUP BY workspace_id
	) s ON s.workspace_id = w.id`

func scanTenant(r pgx.Row) (Tenant, error) {
	var t Tenant
	err := r.Scan(&t.ID, &t.Name, &t.Plan, &t.Status, &t.DailySendLimit, &t.SMSCredits, &t.AdminEmail, &t.Users, &t.Contacts,
		&t.Sent30d, &t.Bounced30d, &t.CreatedAt)
	return t, err
}

// Tenants lists workspaces, filtered by name/admin email and status.
func (s *Service) Tenants(ctx context.Context, q, status string, p httpx.Page) ([]Tenant, error) {
	rows, err := s.db.Query(ctx, tenantSelect+`
		WHERE ($1 = '' OR w.name ILIKE '%'||$1||'%' OR EXISTS (SELECT 1 FROM users u WHERE u.workspace_id=w.id AND u.email ILIKE '%'||$1||'%'))
		  AND ($2 = '' OR w.status = $2)
		ORDER BY w.created_at DESC LIMIT $3 OFFSET $4`, strings.TrimSpace(q), status, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Tenant, error) { return scanTenant(r) })
}

// UpdateTenant applies plan, quota, credit and status changes.
func (s *Service) UpdateTenant(ctx context.Context, id string, in TenantPatch) (Tenant, error) {
	if in.Status != nil && *in.Status != "active" && *in.Status != "suspended" {
		return Tenant{}, apperr.Invalid("invalid_status", "status must be active or suspended")
	}
	if in.Plan != nil && !slices.Contains(Plans, *in.Plan) {
		return Tenant{}, apperr.Invalid("invalid_plan", "plan must be one of "+strings.Join(Plans, ", "))
	}
	if in.DailySendLimit != nil && (*in.DailySendLimit < 0 || *in.DailySendLimit > 10_000_000) {
		return Tenant{}, apperr.Invalid("invalid_limit", "daily_send_limit must be between 0 and 10,000,000")
	}
	if in.AddSMSCredits != nil && (*in.AddSMSCredits < -1_000_000 || *in.AddSMSCredits > 1_000_000) {
		return Tenant{}, apperr.Invalid("invalid_credits", "add_sms_credits must be between -1,000,000 and 1,000,000")
	}
	tag, err := s.db.Exec(ctx, `UPDATE workspaces SET
			status = COALESCE($2, status), plan = COALESCE($3, plan),
			daily_send_limit = COALESCE($4, daily_send_limit),
			sms_credits = greatest(sms_credits + COALESCE($5, 0), 0)
		WHERE id=$1`, id, in.Status, in.Plan, in.DailySendLimit, in.AddSMSCredits)
	if err != nil {
		return Tenant{}, apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return Tenant{}, apperr.NotFound("tenant_not_found", "tenant not found")
	}
	t, err := scanTenant(s.db.QueryRow(ctx, tenantSelect+` WHERE w.id=$1`, id))
	if database.IsNoRows(err) {
		return t, apperr.NotFound("tenant_not_found", "tenant not found")
	}
	if err != nil {
		return t, apperr.Internal(err)
	}
	return t, nil
}

// Overview returns platform-wide totals and daily series.
func (s *Service) Overview(ctx context.Context, days int) (Platform, error) {
	out := Platform{Days: days}
	if err := s.db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='active'), count(*) FILTER (WHERE status='suspended'),
		(SELECT count(*) FROM users) FROM workspaces`).Scan(&out.Tenants, &out.Active, &out.Suspended, &out.Users); err != nil {
		return out, apperr.Internal(err)
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('sent','bounced')), count(*) FILTER (WHERE status='bounced'),
		count(complained_at) FROM messages WHERE sent_at > now() - make_interval(days => $1)`, days).
		Scan(&out.Sent, &out.Bounced, &out.Complaints); err != nil {
		return out, apperr.Internal(err)
	}
	rows, err := s.db.Query(ctx, `
		WITH d AS (SELECT generate_series((now() AT TIME ZONE $2)::date - ($1 - 1), (now() AT TIME ZONE $2)::date, interval '1 day')::date AS day)
		SELECT to_char(d.day, 'YYYY-MM-DD'),
		       (SELECT count(*) FROM messages m WHERE m.status IN ('sent','bounced')
		          AND m.sent_at >= d.day::timestamp AT TIME ZONE $2 AND m.sent_at < (d.day + 1)::timestamp AT TIME ZONE $2),
		       (SELECT count(*) FROM workspaces w
		          WHERE w.created_at >= d.day::timestamp AT TIME ZONE $2 AND w.created_at < (d.day + 1)::timestamp AT TIME ZONE $2)
		FROM d ORDER BY d.day`, days, stats.Timezone)
	if err != nil {
		return out, apperr.Internal(err)
	}
	if out.Series, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DayCount, error) {
		var d DayCount
		return d, r.Scan(&d.Date, &d.Sent, &d.Signups)
	}); err != nil {
		return out, apperr.Internal(err)
	}
	rows, err = s.db.Query(ctx, tenantSelect+` ORDER BY coalesce(s.sent, 0) DESC, w.created_at DESC LIMIT 5`)
	if err != nil {
		return out, apperr.Internal(err)
	}
	if out.TopTenants, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Tenant, error) { return scanTenant(r) }); err != nil {
		return out, apperr.Internal(err)
	}
	rows, err = s.db.Query(ctx, `SELECT plan, count(*) FROM workspaces GROUP BY plan ORDER BY 2 DESC, 1`)
	if err != nil {
		return out, apperr.Internal(err)
	}
	for rows.Next() {
		var p struct {
			Plan  string `json:"plan"`
			Count int    `json:"count"`
		}
		if err := rows.Scan(&p.Plan, &p.Count); err != nil {
			rows.Close()
			return out, apperr.Internal(err)
		}
		out.ByPlan = append(out.ByPlan, p)
	}
	rows.Close()
	return out, nil
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/admin/overview", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := s.Overview(r.Context(), stats.ClampDays(r.URL.Query().Get("days")))
		httpx.Respond(w, r, http.StatusOK, o, err)
	})))
	mux.Handle("GET /v1/admin/tenants", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ts, err := s.Tenants(r.Context(), q.Get("q"), q.Get("status"), httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": ts}, err)
	})))
	mux.Handle("PATCH /v1/admin/tenants/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in TenantPatch
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		t, err := s.UpdateTenant(r.Context(), r.PathValue("id"), in)
		httpx.Respond(w, r, http.StatusOK, t, err)
	})))
}
