// Package campaign implements one-shot mass email campaigns (PRD F7).
package campaign

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/contact"
	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/senderdomain"
	"github.com/danangsantoso/balasemail.com/internal/template"
)

type Campaign struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	FromName      string     `json:"from_name"`
	FromEmail     string     `json:"from_email"`
	ReplyTo       string     `json:"reply_to"`
	Subject       string     `json:"subject"`
	HTML          string     `json:"html"`
	Text          string     `json:"text"`
	TagFilter     []string   `json:"tag_filter"`
	SendPerMinute int        `json:"send_per_minute"`
	Status        string     `json:"status"`
	LaunchedAt    *time.Time `json:"launched_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Input struct {
	Name          string   `json:"name"`
	FromName      string   `json:"from_name"`
	FromEmail     string   `json:"from_email"`
	ReplyTo       string   `json:"reply_to"`
	Subject       string   `json:"subject"`
	HTML          string   `json:"html"`
	Text          string   `json:"text"`
	TagFilter     []string `json:"tag_filter"`
	SendPerMinute int      `json:"send_per_minute"`
}

func (in *Input) validate() error {
	switch {
	case strings.TrimSpace(in.Name) == "":
		return apperr.Invalid("missing_name", "name is required")
	case strings.TrimSpace(in.Subject) == "":
		return apperr.Invalid("missing_subject", "subject is required")
	case strings.TrimSpace(in.HTML) == "" && strings.TrimSpace(in.Text) == "":
		return apperr.Invalid("missing_body", "html or text body is required")
	}
	if in.SendPerMinute <= 0 {
		in.SendPerMinute = 60
	}
	in.SendPerMinute = min(in.SendPerMinute, 6000)
	for i, t := range in.TagFilter {
		in.TagFilter[i] = strings.ToLower(strings.TrimSpace(t))
	}
	if in.TagFilter == nil {
		in.TagFilter = []string{}
	}
	return nil
}

type Stats struct {
	Total        int     `json:"total"`
	Queued       int     `json:"queued"`
	Sent         int     `json:"sent"`
	Failed       int     `json:"failed"`
	Bounced      int     `json:"bounced"`
	Suppressed   int     `json:"suppressed"`
	Opened       int     `json:"opened"`
	Clicked      int     `json:"clicked"`
	Replied      int     `json:"replied"`
	Unsubscribed int     `json:"unsubscribed"`
	OpenRate     float64 `json:"open_rate"`
	ClickRate    float64 `json:"click_rate"`
	ReplyRate    float64 `json:"reply_rate"`
	BounceRate   float64 `json:"bounce_rate"`
}

type LaunchResult struct {
	Queued    int    `json:"queued"`
	Skipped   int    `json:"skipped_suppressed"`
	Remaining int    `json:"remaining"`
	Status    string `json:"status"`
	Note      string `json:"note,omitempty"`
}

type SenderAuthorizer interface {
	AuthorizeSender(ctx context.Context, wsID, fromEmail string) (senderdomain.Domain, error)
}

type Service struct {
	db       *pgxpool.Pool
	delivery *delivery.Service
	domains  SenderAuthorizer
}

func NewService(db *pgxpool.Pool, d *delivery.Service, domains SenderAuthorizer) *Service {
	return &Service{db: db, delivery: d, domains: domains}
}

const cols = `id, name, from_name, from_email, reply_to, subject, html, text_body, tag_filter, send_per_minute, status, launched_at, created_at`

func scan(r pgx.Row) (Campaign, error) {
	var c Campaign
	err := r.Scan(&c.ID, &c.Name, &c.FromName, &c.FromEmail, &c.ReplyTo, &c.Subject, &c.HTML, &c.Text, &c.TagFilter,
		&c.SendPerMinute, &c.Status, &c.LaunchedAt, &c.CreatedAt)
	return c, err
}

func (s *Service) Create(ctx context.Context, wsID string, in Input) (Campaign, error) {
	if err := in.validate(); err != nil {
		return Campaign{}, err
	}
	c, err := scan(s.db.QueryRow(ctx, `
		INSERT INTO campaigns (workspace_id, name, from_name, from_email, reply_to, subject, html, text_body, tag_filter, send_per_minute)
		VALUES ($1,$2,$3,lower($4),$5,$6,$7,$8,$9,$10) RETURNING `+cols,
		wsID, in.Name, in.FromName, in.FromEmail, in.ReplyTo, in.Subject, in.HTML, in.Text, in.TagFilter, in.SendPerMinute))
	if err != nil {
		return c, apperr.Internal(err)
	}
	return c, nil
}

func (s *Service) Update(ctx context.Context, wsID, id string, in Input) (Campaign, error) {
	if err := in.validate(); err != nil {
		return Campaign{}, err
	}
	c, err := scan(s.db.QueryRow(ctx, `
		UPDATE campaigns SET name=$3, from_name=$4, from_email=lower($5), reply_to=$6, subject=$7, html=$8, text_body=$9,
			tag_filter=$10, send_per_minute=$11, updated_at=now()
		WHERE id=$1 AND workspace_id=$2 AND status='draft' RETURNING `+cols,
		id, wsID, in.Name, in.FromName, in.FromEmail, in.ReplyTo, in.Subject, in.HTML, in.Text, in.TagFilter, in.SendPerMinute))
	if database.IsNoRows(err) {
		return c, apperr.Conflict("not_editable", "campaign not found or no longer a draft")
	}
	if err != nil {
		return c, apperr.Internal(err)
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, wsID, id string) (Campaign, error) {
	c, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM campaigns WHERE id=$1 AND workspace_id=$2`, id, wsID))
	if database.IsNoRows(err) {
		return c, apperr.NotFound("campaign_not_found", "campaign not found")
	}
	if err != nil {
		return c, apperr.Internal(err)
	}
	return c, nil
}

func (s *Service) List(ctx context.Context, wsID string, p httpx.Page) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM campaigns WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		wsID, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Campaign, error) { return scan(r) })
}

// Vars builds personalization variables for a contact.
func Vars(c contact.Contact) map[string]string {
	v := c.Vars()
	v["unsubscribe_url"] = delivery.UnsubscribePlaceholder
	return v
}

// Launch queues messages for every eligible contact, paced by send_per_minute
// and capped by the workspace daily quota. Re-launching a paused campaign
// resumes with contacts not yet messaged.
func (s *Service) Launch(ctx context.Context, wsID, id string) (LaunchResult, error) {
	c, err := s.Get(ctx, wsID, id)
	if err != nil {
		return LaunchResult{}, err
	}
	if !slices.Contains([]string{"draft", "paused"}, c.Status) {
		return LaunchResult{}, apperr.Conflict("already_launched", "campaign is already "+c.Status)
	}
	if _, err := s.domains.AuthorizeSender(ctx, wsID, c.FromEmail); err != nil {
		return LaunchResult{}, err
	}
	quota, err := s.delivery.RemainingQuota(ctx, wsID)
	if err != nil {
		return LaunchResult{}, apperr.Internal(err)
	}
	if quota == 0 {
		return LaunchResult{}, apperr.RateLimited("daily_limit_reached", "daily sending limit reached; try again tomorrow or upgrade your plan")
	}

	rows, err := s.db.Query(ctx, `SELECT c.id, c.email, c.first_name, c.last_name, c.company, c.title, c.phone, c.fields
		FROM contacts c
		WHERE c.workspace_id=$1 AND c.tags @> $2 AND c.unsubscribed_at IS NULL
		  AND c.validation_status NOT IN ('invalid','disposable')
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.campaign_id=$3 AND m.contact_id=c.id)
		ORDER BY c.created_at`, wsID, c.TagFilter, c.ID)
	if err != nil {
		return LaunchResult{}, apperr.Internal(err)
	}
	contacts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (contact.Contact, error) {
		var ct contact.Contact
		err := r.Scan(&ct.ID, &ct.Email, &ct.FirstName, &ct.LastName, &ct.Company, &ct.Title, &ct.Phone, &ct.Fields)
		return ct, err
	})
	if err != nil {
		return LaunchResult{}, apperr.Internal(err)
	}
	if len(contacts) == 0 {
		return LaunchResult{}, apperr.Invalid("no_recipients", "no eligible contacts match this campaign's tag filter")
	}

	res := LaunchResult{}
	batch := contacts[:min(len(contacts), quota)]
	res.Remaining = len(contacts) - len(batch)
	interval := time.Minute / time.Duration(c.SendPerMinute)
	start := time.Now()

	for chunk := range slices.Chunk(batch, 500) {
		err := database.InTx(ctx, s.db, func(tx pgx.Tx) error {
			for _, ct := range chunk {
				seed := c.ID + ct.ID
				vars := Vars(ct)
				_, err := s.delivery.Queue(ctx, tx, delivery.Outgoing{
					WorkspaceID: wsID, Category: delivery.CategoryMarketing, CampaignID: c.ID, ContactID: ct.ID,
					FromEmail: c.FromEmail, FromName: c.FromName, ReplyTo: c.ReplyTo, ToEmail: ct.Email,
					Subject: template.Personalize(c.Subject, vars, seed, false),
					HTML:    template.Personalize(c.HTML, vars, seed, true),
					Text:    template.Personalize(c.Text, vars, seed, false),
					RunAt:   start.Add(time.Duration(res.Queued+res.Skipped) * interval),
					Headers: map[string]string{"X-Campaign-ID": c.ID},
				})
				switch {
				case errors.Is(err, delivery.ErrSuppressed):
					res.Skipped++
				case err != nil:
					return err
				default:
					res.Queued++
				}
			}
			return nil
		})
		if err != nil {
			return res, apperr.Internal(err)
		}
	}

	res.Status = "sending"
	if res.Remaining > 0 {
		res.Status = "paused"
		res.Note = fmt.Sprintf("daily limit reached: %d contacts remain; launch again tomorrow to resume", res.Remaining)
	}
	if _, err := s.db.Exec(ctx, `UPDATE campaigns SET status=$2, launched_at=coalesce(launched_at, now()), updated_at=now() WHERE id=$1`,
		c.ID, res.Status); err != nil {
		return res, apperr.Internal(err)
	}
	return res, nil
}

// SendTest sends a preview to a member of the workspace only (prevents abuse as a free relay).
func (s *Service) SendTest(ctx context.Context, wsID, id, to string) error {
	c, err := s.Get(ctx, wsID, id)
	if err != nil {
		return err
	}
	if _, err := s.domains.AuthorizeSender(ctx, wsID, c.FromEmail); err != nil {
		return err
	}
	var member bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE workspace_id=$1 AND email=lower($2))`, wsID, to).Scan(&member); err != nil {
		return apperr.Internal(err)
	}
	if !member {
		return apperr.Forbidden("not_a_member", "test emails can only be sent to members of your workspace")
	}
	sample := Vars(contact.Contact{Email: to, FirstName: "Budi", LastName: "Contoh", Company: "PT Contoh", Title: "Direktur"})
	_, err = s.delivery.Queue(ctx, s.db, delivery.Outgoing{
		WorkspaceID: wsID, Category: delivery.CategoryTransactional, FromEmail: c.FromEmail, FromName: c.FromName,
		ReplyTo: c.ReplyTo, ToEmail: to,
		Subject: "[TEST] " + template.Personalize(c.Subject, sample, "test", false),
		HTML:    template.Personalize(c.HTML, sample, "test", true),
		Text:    template.Personalize(c.Text, sample, "test", false),
	})
	return err
}

func (s *Service) Stats(ctx context.Context, wsID, id string) (Stats, error) {
	var st Stats
	err := s.db.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status='queued'), count(*) FILTER (WHERE status='sent'),
		       count(*) FILTER (WHERE status='failed'), count(*) FILTER (WHERE status='bounced'),
		       count(*) FILTER (WHERE status='suppressed'),
		       count(opened_at), count(clicked_at), count(replied_at), count(unsubscribed_at)
		FROM messages WHERE campaign_id=$1 AND workspace_id=$2`, id, wsID).Scan(
		&st.Total, &st.Queued, &st.Sent, &st.Failed, &st.Bounced, &st.Suppressed, &st.Opened, &st.Clicked, &st.Replied, &st.Unsubscribed)
	if err != nil {
		return st, apperr.Internal(err)
	}
	if delivered := st.Sent; delivered > 0 {
		st.OpenRate = ratio(st.Opened, delivered)
		st.ClickRate = ratio(st.Clicked, delivered)
		st.ReplyRate = ratio(st.Replied, delivered)
	}
	if attempted := st.Sent + st.Bounced; attempted > 0 {
		st.BounceRate = ratio(st.Bounced, attempted)
	}
	return st, nil
}

func ratio(a, b int) float64 { return float64(int(float64(a)/float64(b)*10000)) / 10000 }

// CompleteFinished marks sending campaigns with nothing left in the queue as sent.
func (s *Service) CompleteFinished(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE campaigns c SET status='sent', updated_at=now()
		WHERE c.status='sending' AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.campaign_id=c.id AND m.status='queued')`)
	return err
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	h := func(fn func(w http.ResponseWriter, r *http.Request, ws string)) http.Handler {
		return authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fn(w, r, authctx.MustFrom(r.Context()).WorkspaceID)
		}))
	}
	mux.Handle("GET /v1/campaigns", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		cs, err := s.List(r.Context(), ws, httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": cs}, err)
	}))
	mux.Handle("POST /v1/campaigns", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		var in Input
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		c, err := s.Create(r.Context(), ws, in)
		httpx.Respond(w, r, http.StatusCreated, c, err)
	}))
	mux.Handle("GET /v1/campaigns/{id}", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		c, err := s.Get(r.Context(), ws, r.PathValue("id"))
		httpx.Respond(w, r, http.StatusOK, c, err)
	}))
	mux.Handle("PUT /v1/campaigns/{id}", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		var in Input
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		c, err := s.Update(r.Context(), ws, r.PathValue("id"), in)
		httpx.Respond(w, r, http.StatusOK, c, err)
	}))
	mux.Handle("POST /v1/campaigns/{id}/launch", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		res, err := s.Launch(r.Context(), ws, r.PathValue("id"))
		httpx.Respond(w, r, http.StatusAccepted, res, err)
	}))
	mux.Handle("POST /v1/campaigns/{id}/test", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		var in struct {
			To string `json:"to"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		err := s.SendTest(r.Context(), ws, r.PathValue("id"), in.To)
		httpx.Respond(w, r, http.StatusAccepted, map[string]string{"status": "queued"}, err)
	}))
	mux.Handle("GET /v1/campaigns/{id}/stats", h(func(w http.ResponseWriter, r *http.Request, ws string) {
		st, err := s.Stats(r.Context(), ws, r.PathValue("id"))
		httpx.Respond(w, r, http.StatusOK, st, err)
	}))
}
