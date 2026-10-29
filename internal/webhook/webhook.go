// Package webhook pushes delivery and engagement events to customer
// endpoints (PRD F6): signed, retried with backoff, SSRF-guarded.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

const JobDeliver = "webhook.deliver"

// SignatureHeader carries "t=<unix>,v1=<hex hmac-sha256(secret, t + "." + body)>".
const SignatureHeader = "X-BalasEmail-Signature"

// EventTypes customers can subscribe to.
var EventTypes = []string{"sent", "open", "click", "reply", "bounce", "soft_bounce", "complaint", "unsubscribe"}

type Endpoint struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// Payload is the JSON body sent to endpoints.
type Payload struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      EventData `json:"data"`
}

type EventData struct {
	MessageID    string `json:"message_id"`
	To           string `json:"to"`
	Category     string `json:"category"`
	CampaignID   string `json:"campaign_id,omitempty"`
	EnrollmentID string `json:"enrollment_id,omitempty"`
	ContactID    string `json:"contact_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type Service struct {
	db     *pgxpool.Pool
	q      *queue.Queue
	cipher *security.Cipher
	client *http.Client
	// AllowHTTP permits http:// endpoints (development only).
	AllowHTTP bool
}

func NewService(db *pgxpool.Pool, q *queue.Queue, c *security.Cipher, client *http.Client) *Service {
	return &Service{db: db, q: q, cipher: c, client: client}
}

// Sign computes the signature header value.
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(m, "%d.", ts)
	_, _ = m.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(m.Sum(nil)))
}

// Verify checks a signature header (exported so customers' Go SDK can reuse it).
func Verify(secret, header string, body []byte, tolerance time.Duration, now time.Time) bool {
	var ts int64
	var sig string
	for p := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sig = v
		}
	}
	if ts == 0 || sig == "" || now.Sub(time.Unix(ts, 0)).Abs() > tolerance {
		return false
	}
	expected := Sign(secret, ts, body)
	return hmac.Equal([]byte(expected), []byte(fmt.Sprintf("t=%d,v1=%s", ts, sig)))
}

func (s *Service) validate(rawURL string, events []string) ([]string, error) {
	u, err := url.Parse(rawURL)
	devHTTP := s.AllowHTTP && u != nil && u.Scheme == "http"
	if err != nil || u.Host == "" || (u.Scheme != "https" && !devHTTP) {
		return nil, apperr.Invalid("invalid_url", "url must be an absolute https:// URL")
	}
	out := []string{}
	for _, e := range events {
		e = strings.ToLower(strings.TrimSpace(e))
		if !slices.Contains(EventTypes, e) {
			return nil, apperr.Invalid("invalid_event", "unknown event type "+e)
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Create registers an endpoint and returns its signing secret once.
func (s *Service) Create(ctx context.Context, wsID, rawURL string, events []string) (Endpoint, string, error) {
	evs, err := s.validate(rawURL, events)
	if err != nil {
		return Endpoint{}, "", err
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM webhook_endpoints WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		return Endpoint{}, "", apperr.Internal(err)
	}
	if n >= 10 {
		return Endpoint{}, "", apperr.Invalid("too_many_endpoints", "at most 10 webhook endpoints per workspace")
	}
	secret := "whsec_" + security.RandomHex(24)
	sealed, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return Endpoint{}, "", apperr.Internal(err)
	}
	var e Endpoint
	err = s.db.QueryRow(ctx, `INSERT INTO webhook_endpoints (workspace_id, url, secret_enc, events) VALUES ($1,$2,$3,$4)
		RETURNING id, url, events, enabled, created_at`, wsID, rawURL, sealed, evs).Scan(&e.ID, &e.URL, &e.Events, &e.Enabled, &e.CreatedAt)
	if err != nil {
		return e, "", apperr.Internal(err)
	}
	return e, secret, nil
}

func (s *Service) List(ctx context.Context, wsID string) ([]Endpoint, error) {
	rows, err := s.db.Query(ctx, `SELECT id, url, events, enabled, created_at FROM webhook_endpoints WHERE workspace_id=$1 ORDER BY created_at`, wsID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Endpoint, error) {
		var e Endpoint
		err := r.Scan(&e.ID, &e.URL, &e.Events, &e.Enabled, &e.CreatedAt)
		return e, err
	})
}

func (s *Service) Delete(ctx context.Context, wsID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM webhook_endpoints WHERE id=$1 AND workspace_id=$2`, id, wsID)
	if err != nil {
		return apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("webhook_not_found", "webhook endpoint not found")
	}
	return nil
}

type deliverPayload struct {
	EndpointID string  `json:"endpoint_id"`
	Payload    Payload `json:"payload"`
}

// OnDeliveryEvent fans an event out to subscribed endpoints (registered as a delivery hook).
func (s *Service) OnDeliveryEvent(ctx context.Context, ev delivery.Event) {
	if !slices.Contains(EventTypes, ev.Type) {
		return
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM webhook_endpoints
		WHERE workspace_id=$1 AND enabled AND (events = '{}' OR $2 = ANY(events))`, ev.WorkspaceID, ev.Type)
	if err != nil {
		slog.Error("webhook fan-out", "err", err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return
	}
	var data EventData
	err = s.db.QueryRow(ctx, `SELECT id, to_email, category, coalesce(campaign_id::text,''), coalesce(enrollment_id::text,''),
		coalesce(contact_id::text,''), bounce_reason FROM messages WHERE id=$1`, ev.MessageID).
		Scan(&data.MessageID, &data.To, &data.Category, &data.CampaignID, &data.EnrollmentID, &data.ContactID, &data.Reason)
	if err != nil {
		slog.Error("webhook load message", "err", err)
		return
	}
	p := Payload{ID: "evt_" + security.RandomHex(10), Type: ev.Type, CreatedAt: time.Now().UTC(), Data: data}
	for _, id := range ids {
		if err := s.q.Enqueue(ctx, JobDeliver, deliverPayload{EndpointID: id, Payload: p},
			queue.Options{Priority: queue.PriorityTransactional, MaxAttempts: 8}); err != nil {
			slog.Error("webhook enqueue", "err", err)
		}
	}
}

// HandleDeliver POSTs one event; 2xx = done, 5xx/429/network = retry, other 4xx = give up.
func (s *Service) HandleDeliver(ctx context.Context, job queue.Job) error {
	var p deliverPayload
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	var target string
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT url, secret_enc FROM webhook_endpoints WHERE id=$1 AND enabled`, p.EndpointID).Scan(&target, &sealed)
	if database.IsNoRows(err) {
		return nil // endpoint removed or disabled
	}
	if err != nil {
		return err
	}
	secret, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return queue.Permanent(err)
	}
	body, _ := json.Marshal(p.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return queue.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "BalasEmail-Webhooks/1.0")
	req.Header.Set(SignatureHeader, Sign(string(secret), time.Now().Unix(), body))
	resp, err := s.client.Do(req)
	code := 0
	if resp != nil {
		code = resp.StatusCode
		_ = resp.Body.Close()
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO webhook_deliveries (endpoint_id, event_type, status_code, error, attempt) VALUES ($1,$2,NULLIF($3,0),$4,$5)`,
		p.EndpointID, p.Payload.Type, code, errText, job.Attempts)
	switch {
	case err != nil:
		metrics.WebhookDeliveries.WithLabelValues("retry").Inc()
		return err
	case code >= 200 && code < 300:
		metrics.WebhookDeliveries.WithLabelValues("delivered").Inc()
		return nil
	case code >= 500 || code == http.StatusTooManyRequests:
		metrics.WebhookDeliveries.WithLabelValues("retry").Inc()
		return fmt.Errorf("endpoint returned %d", code)
	default:
		metrics.WebhookDeliveries.WithLabelValues("rejected").Inc()
		return queue.Permanent(fmt.Errorf("endpoint returned %d", code))
	}
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	ws := func(r *http.Request) string { return authctx.MustFrom(r.Context()).WorkspaceID }
	mux.Handle("GET /v1/webhooks", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		es, err := s.List(r.Context(), ws(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": es}, err)
	})))
	mux.Handle("POST /v1/webhooks", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			URL    string   `json:"url"`
			Events []string `json:"events"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		e, secret, err := s.Create(r.Context(), ws(r), in.URL, in.Events)
		httpx.Respond(w, r, http.StatusCreated, map[string]any{"endpoint": e, "secret": secret,
			"note": "store the secret now; verify " + SignatureHeader + " on every request"}, err)
	})))
	mux.Handle("DELETE /v1/webhooks/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.Context(), ws(r), r.PathValue("id")); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
