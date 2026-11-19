package sms

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
)

const JobSend = "sms.send"

// MaxSegments caps cost per message.
const MaxSegments = 10

type SMS struct {
	ID        string     `json:"id"`
	To        string     `json:"to"`
	SenderID  string     `json:"sender_id"`
	Body      string     `json:"body"`
	Segments  int        `json:"segments"`
	Encoding  string     `json:"encoding"`
	Source    string     `json:"source"`
	Status    string     `json:"status"`
	Provider  string     `json:"provider"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
}

type SendInput struct {
	To       string `json:"to"`
	Body     string `json:"body"`
	SenderID string `json:"sender_id"`
	Source   string `json:"-"`
	Priority int    `json:"-"`
}

type Service struct {
	db     *pgxpool.Pool
	q      *queue.Queue
	router *Router
}

func NewService(db *pgxpool.Pool, q *queue.Queue, r *Router) *Service {
	return &Service{db: db, q: q, router: r}
}

// Send validates, reserves credits and queues an SMS.
func (s *Service) Send(ctx context.Context, wsID string, in SendInput) (SMS, error) {
	to, err := Normalize(in.To)
	if err != nil {
		return SMS{}, apperr.Invalid("invalid_phone", "phone number is not valid")
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		return SMS{}, apperr.Invalid("empty_body", "SMS body is empty")
	}
	enc, parts := Segments(body)
	if parts > MaxSegments {
		return SMS{}, apperr.Invalid("too_long", "SMS body exceeds 10 segments")
	}
	if in.Source == "" {
		in.Source = "api"
	}
	if in.Priority == 0 {
		in.Priority = queue.PriorityTransactional
	}
	var out SMS
	err = database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE workspaces SET sms_credits = sms_credits - $2 WHERE id=$1 AND sms_credits >= $2`, wsID, parts)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.PaymentRequired("insufficient_sms_credits", "not enough SMS credits; top up your balance")
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO sms_messages (workspace_id, to_phone, sender_id, body, segments, encoding, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING id, to_phone, sender_id, body, segments, encoding, source, status, provider, created_at`,
			wsID, to, in.SenderID, body, parts, enc, in.Source).
			Scan(&out.ID, &out.To, &out.SenderID, &out.Body, &out.Segments, &out.Encoding, &out.Source, &out.Status, &out.Provider, &out.CreatedAt); err != nil {
			return err
		}
		return s.q.WithTx(tx).Enqueue(ctx, JobSend, map[string]string{"id": out.ID}, queue.Options{Priority: in.Priority, MaxAttempts: 5})
	})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return SMS{}, ae
		}
		return SMS{}, apperr.Internal(err)
	}
	return out, nil
}

// SendFromWorkflow is used by sequence action nodes.
func (s *Service) SendFromWorkflow(ctx context.Context, wsID, to, body string) error {
	_, err := s.Send(ctx, wsID, SendInput{To: to, Body: body, Source: "sequence"})
	return err
}

// SendOTP sends with the highest queue priority.
func (s *Service) SendOTP(ctx context.Context, wsID, to, body string) error {
	_, err := s.Send(ctx, wsID, SendInput{To: to, Body: body, Source: "otp", Priority: queue.PriorityOTP})
	return err
}

// FromEmail is the Email-to-SMS bridge entry point (PRD F11).
func (s *Service) FromEmail(ctx context.Context, wsID, rcpt, smsDomain, subject, text string) (SMS, error) {
	phone, ok, err := ParseAddress(rcpt, smsDomain)
	if !ok || err != nil {
		return SMS{}, apperr.Invalid("invalid_phone", "recipient is not a valid <number>@"+smsDomain+" address")
	}
	return s.Send(ctx, wsID, SendInput{To: phone, Body: BodyFromEmail(subject, text), Source: "smtp"})
}

// HandleSend is the queue handler for JobSend.
func (s *Service) HandleSend(ctx context.Context, job queue.Job) error {
	var p struct {
		ID string `json:"id"`
	}
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	var m SMS
	var wsID string
	err := s.db.QueryRow(ctx, `SELECT id, workspace_id, to_phone, sender_id, body, segments, status FROM sms_messages WHERE id=$1`, p.ID).
		Scan(&m.ID, &wsID, &m.To, &m.SenderID, &m.Body, &m.Segments, &m.Status)
	if database.IsNoRows(err) || m.Status != "queued" {
		return nil
	}
	if err != nil {
		return err
	}
	provider, pid, err := s.router.Send(ctx, Message{To: m.To, SenderID: m.SenderID, Body: m.Body})
	if err == nil {
		metrics.SMSSent.WithLabelValues(provider, "sent").Inc()
		_, err = s.db.Exec(ctx, `UPDATE sms_messages SET status='sent', provider=$2, provider_message_id=$3, sent_at=now() WHERE id=$1`, m.ID, provider, pid)
		return err
	}
	if isPermanent(err) || job.Attempts >= 5 {
		metrics.SMSSent.WithLabelValues(provider, "failed").Inc()
		return database.InTx(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE sms_messages SET status='failed', provider=$2, error=$3 WHERE id=$1`, m.ID, provider, err.Error()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE workspaces SET sms_credits = sms_credits + $2 WHERE id=$1`, wsID, m.Segments) // refund
			return err
		})
	}
	metrics.SMSSent.WithLabelValues(provider, "retry").Inc()
	return err
}

func (s *Service) List(ctx context.Context, wsID string, p httpx.Page) ([]SMS, error) {
	rows, err := s.db.Query(ctx, `SELECT id, to_phone, sender_id, body, segments, encoding, source, status, provider, error, created_at, sent_at
		FROM sms_messages WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, wsID, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SMS, error) {
		var m SMS
		err := r.Scan(&m.ID, &m.To, &m.SenderID, &m.Body, &m.Segments, &m.Encoding, &m.Source, &m.Status, &m.Provider, &m.Error, &m.CreatedAt, &m.SentAt)
		return m, err
	})
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("POST /v1/sms", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in SendInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		m, err := s.Send(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
		httpx.Respond(w, r, http.StatusAccepted, m, err)
	})))
	mux.Handle("GET /v1/sms", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms, err := s.List(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": ms}, err)
	})))
}
