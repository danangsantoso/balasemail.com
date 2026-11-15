package billing

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

// Usage is what a workspace has consumed of its plan.
type Usage struct {
	EmailsToday int `json:"emails_today"`
	Contacts    int `json:"contacts"`
	Users       int `json:"users"`
	Domains     int `json:"domains"`
}

// Status is the billing view of a workspace: its plan, period and usage.
type Status struct {
	Plan           Plan       `json:"plan"`
	DailySendLimit int        `json:"daily_send_limit"`
	PlanEndsAt     *time.Time `json:"plan_ends_at"`
	Expired        bool       `json:"expired"`
	DaysLeft       int        `json:"days_left"` // whole days until the period ends; 0 when expired or open-ended
	RequestedPlan  string     `json:"requested_plan,omitempty"`
	RequestedCycle string     `json:"requested_cycle,omitempty"`
	RequestedAt    *time.Time `json:"requested_at,omitempty"`
	Usage          Usage      `json:"usage"`
	Plans          []Plan     `json:"plans"`
}

type Service struct{ db *pgxpool.Pool }

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Status reports the workspace's plan, remaining period and usage.
func (s *Service) Status(ctx context.Context, wsID string) (Status, error) {
	st := Status{Plans: Catalog()}
	var key string
	err := s.db.QueryRow(ctx, `
		SELECT w.plan, w.daily_send_limit, w.plan_ends_at, w.requested_plan, w.requested_cycle, w.requested_at,
		       (SELECT count(*) FROM messages m WHERE m.workspace_id=w.id
		          AND m.category IN ('marketing','sequence') AND m.queued_at >= date_trunc('day', now())),
		       (SELECT count(*) FROM contacts c WHERE c.workspace_id=w.id),
		       (SELECT count(*) FROM users u WHERE u.workspace_id=w.id AND u.disabled_at IS NULL),
		       (SELECT count(*) FROM sender_domains d WHERE d.workspace_id=w.id)
		FROM workspaces w WHERE w.id=$1`, wsID).Scan(
		&key, &st.DailySendLimit, &st.PlanEndsAt, &st.RequestedPlan, &st.RequestedCycle, &st.RequestedAt,
		&st.Usage.EmailsToday, &st.Usage.Contacts, &st.Usage.Users, &st.Usage.Domains)
	if database.IsNoRows(err) {
		return st, apperr.NotFound("workspace_not_found", "workspace not found")
	}
	if err != nil {
		return st, apperr.Internal(err)
	}
	st.Plan, _ = Get(key)
	if st.PlanEndsAt != nil {
		left := time.Until(*st.PlanEndsAt)
		st.Expired = left <= 0
		if !st.Expired {
			st.DaysLeft = int((left + 24*time.Hour - 1) / (24 * time.Hour))
		}
	}
	return st, nil
}

// RequestUpgrade records which paid plan the tenant wants. A platform admin
// activates it from the console once payment is confirmed.
func (s *Service) RequestUpgrade(ctx context.Context, wsID, plan, cycle string) (Status, error) {
	if p, ok := Get(plan); !ok || p.Key == Trial {
		return Status{}, apperr.Invalid("invalid_plan", "choose one of the paid plans")
	}
	if cycle == "" {
		cycle = "monthly"
	}
	if cycle != "monthly" && cycle != "yearly" {
		return Status{}, apperr.Invalid("invalid_cycle", "cycle must be monthly or yearly")
	}
	if _, err := s.db.Exec(ctx, `UPDATE workspaces SET requested_plan=$2, requested_cycle=$3, requested_at=now() WHERE id=$1`,
		wsID, plan, cycle); err != nil {
		return Status{}, apperr.Internal(err)
	}
	return s.Status(ctx, wsID)
}

// ---- limits ----

// planOf returns the workspace's plan; unknown plans are treated as unlimited.
func planOf(ctx context.Context, db database.DBTX, wsID string) (Plan, error) {
	var key string
	if err := db.QueryRow(ctx, `SELECT plan FROM workspaces WHERE id=$1`, wsID).Scan(&key); err != nil {
		return Plan{}, apperr.Internal(err)
	}
	if p, ok := Get(key); ok {
		return p, nil
	}
	return Plan{Key: key, Name: key, DailyEmails: Unlimited, Contacts: Unlimited, Users: Unlimited, Domains: Unlimited}, nil
}

func over(code, what string, p Plan, limit int) error {
	return apperr.PaymentRequired(code, fmt.Sprintf("the %s plan allows %d %s; upgrade to add more", p.Name, limit, what))
}

// CheckSeat fails when the workspace already has as many active users as its plan allows.
func CheckSeat(ctx context.Context, db database.DBTX, wsID string) error {
	p, err := planOf(ctx, db, wsID)
	if err != nil || p.Users == Unlimited {
		return err
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM users WHERE workspace_id=$1 AND disabled_at IS NULL`, wsID).Scan(&n); err != nil {
		return apperr.Internal(err)
	}
	if n >= p.Users {
		return over("plan_limit_users", "users", p, p.Users)
	}
	return nil
}

// CheckDomain fails when the workspace already has as many sender domains as its plan allows.
func CheckDomain(ctx context.Context, db database.DBTX, wsID string) error {
	p, err := planOf(ctx, db, wsID)
	if err != nil || p.Domains == Unlimited {
		return err
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM sender_domains WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		return apperr.Internal(err)
	}
	if n >= p.Domains {
		return over("plan_limit_domains", "sender domains", p, p.Domains)
	}
	return nil
}

// CheckContacts fails when adding the given emails would exceed the plan's
// contact limit. Emails already in the workspace do not count as new.
func CheckContacts(ctx context.Context, db database.DBTX, wsID string, emails []string) error {
	p, err := planOf(ctx, db, wsID)
	if err != nil || p.Contacts == Unlimited {
		return err
	}
	uniq := map[string]struct{}{}
	for _, e := range emails {
		uniq[e] = struct{}{}
	}
	var total, existing int
	if err := db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE email = ANY($2)) FROM contacts WHERE workspace_id=$1`,
		wsID, emails).Scan(&total, &existing); err != nil {
		return apperr.Internal(err)
	}
	if added := len(uniq) - existing; added > 0 && total+added > p.Contacts {
		return apperr.PaymentRequired("plan_limit_contacts", fmt.Sprintf(
			"the %s plan allows %d contacts and %d are in use; this would add %d more", p.Name, p.Contacts, total, added))
	}
	return nil
}

// ---- HTTP ----

// Routes mounts the public plan catalog and the tenant's billing endpoints.
func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.HandleFunc("GET /v1/plans", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"plans": Catalog(), "included": Included})
	})
	mux.Handle("GET /v1/billing", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Status(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID)
		httpx.Respond(w, r, http.StatusOK, st, err)
	})))
	mux.Handle("POST /v1/billing/upgrade-request", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Plan  string `json:"plan"`
			Cycle string `json:"cycle"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		st, err := s.RequestUpgrade(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in.Plan, in.Cycle)
		httpx.Respond(w, r, http.StatusOK, st, err)
	})))
}
