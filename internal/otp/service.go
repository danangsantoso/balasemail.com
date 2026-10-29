// Package otp issues and verifies one-time passcodes delivered from the
// customer's own business domain, with optional SMS fallback (PRD F4).
package otp

import (
	"context"
	"crypto/hmac"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
	"github.com/danangsantoso/balasemail.com/internal/sms"
	"github.com/danangsantoso/balasemail.com/internal/template"
)

const JobFallback = "otp.sms_fallback"

// Per-recipient issuance limit (anti-abuse / SMS pumping protection).
const (
	maxPerWindow = 5
	window       = 10 * time.Minute
)

type SendInput struct {
	Channel       string `json:"channel"` // email|sms
	To            string `json:"to"`
	Purpose       string `json:"purpose"`
	Length        int    `json:"length"`
	Alphanumeric  bool   `json:"alphanumeric"`
	TTLSeconds    int    `json:"ttl_seconds"`
	MaxAttempts   int    `json:"max_attempts"`
	From          string `json:"from"`  // required for email: an address on a verified domain
	Brand         string `json:"brand"` // shown in subject/body
	Subject       string `json:"subject"`
	Body          string `json:"body"` // supports {{code}}, {{minutes}}, {{brand}}
	FallbackPhone string `json:"fallback_phone"`
	FallbackAfter int    `json:"fallback_after_seconds"`
}

type SendResult struct {
	ID        string    `json:"id"`
	Channel   string    `json:"channel"`
	ExpiresAt time.Time `json:"expires_at"`
}

type VerifyInput struct {
	ID      string `json:"id"`
	To      string `json:"to"`
	Purpose string `json:"purpose"`
	Code    string `json:"code"`
}

type VerifyResult struct {
	Valid             bool   `json:"valid"`
	Reason            string `json:"reason,omitempty"`
	AttemptsRemaining int    `json:"attempts_remaining"`
}

type SMSSender interface {
	SendOTP(ctx context.Context, wsID, to, body string) error
}

type Service struct {
	db        *pgxpool.Pool
	delivery  *delivery.Service
	authorize func(ctx context.Context, wsID, from string) error
	sms       SMSSender
	q         *queue.Queue
	signer    *security.Signer
	cipher    *security.Cipher
}

func NewService(db *pgxpool.Pool, d *delivery.Service, authorize func(ctx context.Context, wsID, from string) error,
	s SMSSender, q *queue.Queue, signer *security.Signer, c *security.Cipher) *Service {
	return &Service{db: db, delivery: d, authorize: authorize, sms: s, q: q, signer: signer, cipher: c}
}

func (in *SendInput) normalize() error {
	in.Channel = strings.ToLower(strings.TrimSpace(in.Channel))
	if in.Channel == "" {
		in.Channel = "email"
	}
	if in.Purpose == "" {
		in.Purpose = "login"
	}
	if in.Length == 0 {
		in.Length = 6
	}
	if in.Length < 4 || in.Length > 8 {
		return apperr.Invalid("invalid_length", "length must be between 4 and 8")
	}
	if in.TTLSeconds == 0 {
		in.TTLSeconds = 300
	}
	if in.TTLSeconds < 60 || in.TTLSeconds > 3600 {
		return apperr.Invalid("invalid_ttl", "ttl_seconds must be between 60 and 3600")
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 5
	}
	in.MaxAttempts = min(in.MaxAttempts, 10)
	if in.Brand == "" {
		in.Brand = "Kami"
	}
	switch in.Channel {
	case "email":
		to, err := emailaddr.Normalize(in.To)
		if err != nil {
			return apperr.Invalid("invalid_recipient", "to must be a valid email address")
		}
		in.To = to
		if in.From == "" {
			return apperr.Invalid("missing_from", "from is required for the email channel (e.g. no-reply@yourcompany.com)")
		}
	case "sms":
		to, err := sms.Normalize(in.To)
		if err != nil {
			return apperr.Invalid("invalid_recipient", "to must be a valid phone number")
		}
		in.To = to
	default:
		return apperr.Invalid("invalid_channel", "channel must be email or sms")
	}
	if in.FallbackPhone != "" {
		p, err := sms.Normalize(in.FallbackPhone)
		if err != nil {
			return apperr.Invalid("invalid_fallback_phone", "fallback_phone is not valid")
		}
		in.FallbackPhone = p
		if in.FallbackAfter == 0 {
			in.FallbackAfter = 60
		}
		in.FallbackAfter = min(max(in.FallbackAfter, 30), in.TTLSeconds-30)
	}
	return nil
}

func (s *Service) hash(id, code string) []byte {
	return s.signer.MAC("otp:" + id + ":" + strings.ToUpper(code))
}

// Send issues a code and queues it on the priority lane.
func (s *Service) Send(ctx context.Context, wsID string, in SendInput) (SendResult, error) {
	if err := in.normalize(); err != nil {
		return SendResult{}, err
	}
	if in.Channel == "email" {
		if err := s.authorize(ctx, wsID, in.From); err != nil {
			return SendResult{}, err
		}
	}
	var recent int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM otps WHERE workspace_id=$1 AND recipient=$2 AND created_at > now() - $3::interval`,
		wsID, in.To, window.String()).Scan(&recent); err != nil {
		return SendResult{}, apperr.Internal(err)
	}
	if recent >= maxPerWindow {
		return SendResult{}, apperr.RateLimited("otp_rate_limited", "too many codes requested for this recipient; try again later")
	}

	code := security.NumericCode(in.Length)
	if in.Alphanumeric {
		code = security.AlphanumericCode(in.Length)
	}
	id := security.UUID()
	expires := time.Now().Add(time.Duration(in.TTLSeconds) * time.Second)
	var enc []byte
	if in.FallbackPhone != "" {
		var err error
		if enc, err = s.cipher.Encrypt([]byte(code)); err != nil {
			return SendResult{}, apperr.Internal(err)
		}
	}
	vars := map[string]string{"code": code, "minutes": fmt.Sprint(in.TTLSeconds / 60), "brand": in.Brand}

	err := database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO otps (id, workspace_id, channel, recipient, purpose, code_hash, code_enc, max_attempts, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, wsID, in.Channel, in.To, in.Purpose, s.hash(id, code), enc, in.MaxAttempts, expires); err != nil {
			return err
		}
		if in.FallbackPhone != "" {
			if err := s.q.WithTx(tx).Enqueue(ctx, JobFallback, fallbackPayload{OTPID: id, WorkspaceID: wsID, Phone: in.FallbackPhone, Brand: in.Brand},
				queue.Options{Priority: queue.PriorityOTP, RunAt: time.Now().Add(time.Duration(in.FallbackAfter) * time.Second), MaxAttempts: 3}); err != nil {
				return err
			}
		}
		if in.Channel == "sms" {
			return nil // sent after commit (separate credit transaction)
		}
		subject := in.Subject
		if subject == "" {
			subject = "{{code}} adalah kode verifikasi {{brand}}"
		}
		body := in.Body
		if body == "" {
			body = "Kode verifikasi {{brand}} Anda: {{code}}\n\nKode berlaku {{minutes}} menit. Jangan bagikan kode ini kepada siapa pun, termasuk pihak yang mengaku dari {{brand}}."
		}
		html := `<div style="font-family:system-ui,sans-serif;max-width:420px"><p>Kode verifikasi ` + template.Render("{{brand}}", vars, true) +
			` Anda:</p><p style="font-size:32px;letter-spacing:6px;font-weight:700">` + code + `</p><p style="color:#666">Berlaku ` +
			vars["minutes"] + ` menit. Jangan bagikan kode ini kepada siapa pun.</p></div>`
		if in.Body != "" {
			html = ""
		}
		_, err := s.delivery.Queue(ctx, tx, delivery.Outgoing{
			WorkspaceID: wsID, Category: delivery.CategoryOTP, FromEmail: in.From, FromName: in.Brand, ToEmail: in.To,
			Subject: template.Render(subject, vars, false), Text: template.Render(body, vars, false), HTML: html,
			Priority: queue.PriorityOTP, MaxAttempts: 3,
		})
		return err
	})
	if err != nil {
		return SendResult{}, apperr.As(err)
	}
	if in.Channel == "sms" {
		if err := s.sms.SendOTP(ctx, wsID, in.To, template.Render("Kode {{brand}}: {{code}}. Berlaku {{minutes}} menit. JANGAN BAGIKAN kode ini.", vars, false)); err != nil {
			_, _ = s.db.Exec(ctx, `DELETE FROM otps WHERE id=$1`, id)
			return SendResult{}, err
		}
	}
	metrics.OTPSent.WithLabelValues(in.Channel).Inc()
	return SendResult{ID: id, Channel: in.Channel, ExpiresAt: expires}, nil
}

type record struct {
	ID          string
	Hash        []byte
	Attempts    int
	MaxAttempts int
	ExpiresAt   time.Time
	VerifiedAt  *time.Time
}

// check is the pure verification decision (unit-tested).
func check(r record, providedHash []byte, now time.Time) VerifyResult {
	remaining := max(r.MaxAttempts-r.Attempts-1, 0)
	switch {
	case r.VerifiedAt != nil:
		return VerifyResult{Reason: "already_used"}
	case now.After(r.ExpiresAt):
		return VerifyResult{Reason: "expired"}
	case r.Attempts >= r.MaxAttempts:
		return VerifyResult{Reason: "too_many_attempts"}
	case !hmac.Equal(r.Hash, providedHash):
		return VerifyResult{Reason: "invalid_code", AttemptsRemaining: remaining}
	}
	return VerifyResult{Valid: true}
}

// Verify checks a code by OTP id, or by recipient+purpose (latest code).
func (s *Service) Verify(ctx context.Context, wsID string, in VerifyInput) (VerifyResult, error) {
	code := strings.TrimSpace(in.Code)
	if code == "" {
		return VerifyResult{}, apperr.Invalid("missing_code", "code is required")
	}
	if in.Purpose == "" {
		in.Purpose = "login"
	}
	var res VerifyResult
	err := database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		var r record
		var err error
		if in.ID != "" {
			err = tx.QueryRow(ctx, `SELECT id, code_hash, attempts, max_attempts, expires_at, verified_at FROM otps
				WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, in.ID, wsID).Scan(&r.ID, &r.Hash, &r.Attempts, &r.MaxAttempts, &r.ExpiresAt, &r.VerifiedAt)
		} else {
			to := strings.ToLower(strings.TrimSpace(in.To))
			if p, perr := sms.Normalize(in.To); perr == nil && !strings.Contains(in.To, "@") {
				to = p
			}
			err = tx.QueryRow(ctx, `SELECT id, code_hash, attempts, max_attempts, expires_at, verified_at FROM otps
				WHERE workspace_id=$1 AND recipient=$2 AND purpose=$3 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
				wsID, to, in.Purpose).Scan(&r.ID, &r.Hash, &r.Attempts, &r.MaxAttempts, &r.ExpiresAt, &r.VerifiedAt)
		}
		if database.IsNoRows(err) {
			res = VerifyResult{Reason: "not_found"}
			return nil
		}
		if err != nil {
			return err
		}
		res = check(r, s.hash(r.ID, code), time.Now())
		switch {
		case res.Valid:
			_, err = tx.Exec(ctx, `UPDATE otps SET verified_at=now(), attempts=attempts+1, code_enc=NULL WHERE id=$1`, r.ID)
		case res.Reason == "invalid_code":
			_, err = tx.Exec(ctx, `UPDATE otps SET attempts=attempts+1 WHERE id=$1`, r.ID)
		}
		return err
	})
	if err != nil {
		return res, apperr.Internal(err)
	}
	result := res.Reason
	if res.Valid {
		result = "valid"
	}
	metrics.OTPVerify.WithLabelValues(result).Inc()
	return res, nil
}

type fallbackPayload struct {
	OTPID       string `json:"otp_id"`
	WorkspaceID string `json:"workspace_id"`
	Phone       string `json:"phone"`
	Brand       string `json:"brand"`
}

// HandleFallback sends the same code by SMS if it has not been verified yet.
func (s *Service) HandleFallback(ctx context.Context, job queue.Job) error {
	var p fallbackPayload
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	var enc []byte
	var verified *time.Time
	var expires time.Time
	err := s.db.QueryRow(ctx, `SELECT code_enc, verified_at, expires_at FROM otps WHERE id=$1`, p.OTPID).Scan(&enc, &verified, &expires)
	if database.IsNoRows(err) || verified != nil || time.Now().After(expires) || enc == nil {
		return nil
	}
	if err != nil {
		return err
	}
	code, err := s.cipher.Decrypt(enc)
	if err != nil {
		return queue.Permanent(err)
	}
	mins := max(int(time.Until(expires).Minutes()), 1)
	metrics.OTPSent.WithLabelValues("sms_fallback").Inc()
	return s.sms.SendOTP(ctx, p.WorkspaceID, p.Phone, fmt.Sprintf("Kode %s: %s. Berlaku %d menit. JANGAN BAGIKAN kode ini.", p.Brand, code, mins))
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("POST /v1/otp/send", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in SendInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		res, err := s.Send(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
		httpx.Respond(w, r, http.StatusAccepted, res, err)
	})))
	mux.Handle("POST /v1/otp/verify", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in VerifyInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		res, err := s.Verify(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
		status := http.StatusOK
		if err == nil && !res.Valid {
			status = http.StatusUnprocessableEntity
		}
		httpx.Respond(w, r, status, res, err)
	})))
}
