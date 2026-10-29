// Package stats serves the time series behind the tenant dashboard charts.
package stats

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

// Timezone used to bucket days for Indonesian customers.
const Timezone = "Asia/Jakarta"

// Day is one point of the engagement series. Counts are per send-day cohort:
// of the emails sent that day, how many were later opened, clicked or replied.
type Day struct {
	Date    string `json:"date"`
	Sent    int    `json:"sent"`
	Opened  int    `json:"opened"`
	Clicked int    `json:"clicked"`
	Replied int    `json:"replied"`
	Bounced int    `json:"bounced"`
}

type Totals struct {
	Sent         int `json:"sent"`
	Opened       int `json:"opened"`
	Clicked      int `json:"clicked"`
	Replied      int `json:"replied"`
	Bounced      int `json:"bounced"`
	Unsubscribed int `json:"unsubscribed"`
}

type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type Overview struct {
	Days     int     `json:"days"`
	Series   []Day   `json:"series"`
	Totals   Totals  `json:"totals"`
	Previous Totals  `json:"previous"` // the equally long period before, for deltas
	Replies  []Count `json:"replies"`  // reply classification
	Contacts []Count `json:"contacts"` // validation status distribution
}

type Service struct{ db *pgxpool.Pool }

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// ClampDays bounds the requested window.
func ClampDays(s string) int {
	d, err := strconv.Atoi(s)
	if err != nil || d <= 0 {
		return 14
	}
	return min(d, 90)
}

const totalsSQL = `
	SELECT count(*) FILTER (WHERE status IN ('sent','bounced')), count(opened_at), count(clicked_at),
	       count(replied_at), count(*) FILTER (WHERE status='bounced'), count(unsubscribed_at)
	FROM messages
	WHERE workspace_id=$1 AND category IN ('marketing','sequence') AND sent_at >= $2 AND sent_at < $3`

func (s *Service) totals(ctx context.Context, wsID string, from, to time.Time) (Totals, error) {
	var t Totals
	err := s.db.QueryRow(ctx, totalsSQL, wsID, from, to).Scan(&t.Sent, &t.Opened, &t.Clicked, &t.Replied, &t.Bounced, &t.Unsubscribed)
	return t, err
}

// Overview returns the last `days` days (today included) in Asia/Jakarta.
func (s *Service) Overview(ctx context.Context, wsID string, days int) (Overview, error) {
	loc, _ := time.LoadLocation(Timezone)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from := today.AddDate(0, 0, -(days - 1))
	to := today.AddDate(0, 0, 1)
	out := Overview{Days: days}

	rows, err := s.db.Query(ctx, `
		WITH d AS (SELECT generate_series($2::date, $3::date, interval '1 day')::date AS day)
		SELECT to_char(d.day, 'YYYY-MM-DD'),
		       count(m.id) FILTER (WHERE m.status IN ('sent','bounced')), count(m.opened_at), count(m.clicked_at),
		       count(m.replied_at), count(m.id) FILTER (WHERE m.status='bounced')
		FROM d LEFT JOIN messages m
		  ON m.workspace_id=$1 AND m.category IN ('marketing','sequence')
		 AND m.sent_at >= d.day::timestamp AT TIME ZONE $4 AND m.sent_at < (d.day + 1)::timestamp AT TIME ZONE $4
		GROUP BY d.day ORDER BY d.day`, wsID, from.Format("2006-01-02"), today.Format("2006-01-02"), Timezone)
	if err != nil {
		return out, apperr.Internal(err)
	}
	out.Series, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Day, error) {
		var d Day
		return d, r.Scan(&d.Date, &d.Sent, &d.Opened, &d.Clicked, &d.Replied, &d.Bounced)
	})
	if err != nil {
		return out, apperr.Internal(err)
	}
	if out.Totals, err = s.totals(ctx, wsID, from, to); err != nil {
		return out, apperr.Internal(err)
	}
	if out.Previous, err = s.totals(ctx, wsID, from.AddDate(0, 0, -days), from); err != nil {
		return out, apperr.Internal(err)
	}
	counts := func(q string, args ...any) ([]Count, error) {
		rows, err := s.db.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Count, error) {
			var c Count
			return c, r.Scan(&c.Key, &c.Count)
		})
	}
	if out.Replies, err = counts(`SELECT classification, count(*) FROM inbound_emails
		WHERE workspace_id=$1 AND NOT automated AND received_at >= $2 GROUP BY 1 ORDER BY 2 DESC, 1`, wsID, from); err != nil {
		return out, apperr.Internal(err)
	}
	if out.Contacts, err = counts(`SELECT validation_status, count(*) FROM contacts WHERE workspace_id=$1 GROUP BY 1 ORDER BY 2 DESC, 1`, wsID); err != nil {
		return out, apperr.Internal(err)
	}
	return out, nil
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/stats/overview", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := s.Overview(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, ClampDays(r.URL.Query().Get("days")))
		httpx.Respond(w, r, http.StatusOK, o, err)
	})))
}
