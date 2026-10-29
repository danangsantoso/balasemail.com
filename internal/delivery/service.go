// Package delivery is the single outbound pipeline every module uses:
// suppression check → persist → queue → shared throttle → DKIM sign → relay with
// failover → bounce handling. It also serves open/click tracking and
// one-click unsubscribe (PRD F6, F7, §8).
package delivery

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/bounce"
	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/mailer"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/platform/ratelimit"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
	"github.com/danangsantoso/balasemail.com/internal/suppression"
)

const JobSend = "email.send"

// UnsubscribePlaceholder may be placed in templates; it is replaced with the
// per-message one-click URL. If absent, a compliant footer is appended.
const UnsubscribePlaceholder = "{{unsubscribe_url}}"

type Category string

const (
	CategoryMarketing     Category = "marketing"
	CategorySequence      Category = "sequence"
	CategoryTransactional Category = "transactional"
	CategoryOTP           Category = "otp"
	CategoryAutoReply     Category = "auto_reply"
	CategoryForward       Category = "forward"
	CategoryRelay         Category = "relay"
)

// bulk categories require unsubscribe, tracking and daily quota.
func (c Category) bulk() bool { return c == CategoryMarketing || c == CategorySequence }

// ErrSuppressed is returned when the recipient is on the suppression list.
var ErrSuppressed = errors.New("recipient is suppressed")

// Outgoing describes one email to queue.
type Outgoing struct {
	WorkspaceID  string
	Category     Category
	CampaignID   string
	EnrollmentID string
	StepID       string
	ContactID    string
	FromEmail    string
	FromName     string
	ReplyTo      string
	ToEmail      string
	Subject      string
	HTML         string
	Text         string
	InReplyTo    string
	Headers      map[string]string
	Raw          []byte // pre-built MIME (SMTP relay); bypasses rendering
	Priority     int
	RunAt        time.Time
	MaxAttempts  int
}

// Event is emitted for engagement and delivery outcomes.
type Event struct {
	Type         string // sent|open|click|reply|bounce|unsubscribe
	MessageID    string
	WorkspaceID  string
	EnrollmentID string
	ContactID    string
}

// DomainKeys provides DKIM keys for verified domains.
type DomainKeys interface {
	SigningKey(ctx context.Context, domain string) (string, crypto.Signer, error)
}

type Service struct {
	db      *pgxpool.Pool
	q       *queue.Queue
	supp    *suppression.Service
	keys    DomainKeys
	router  *mailer.Router
	limiter ratelimit.Limiter
	limits  DomainLimits
	verp    bounce.VERP
	signer  *security.Signer
	baseURL string

	mu    sync.RWMutex
	hooks []func(context.Context, Event)
}

// DomainLimits caps sends per recipient domain per minute across all workers.
type DomainLimits struct {
	Default   int
	Overrides map[string]int
}

func (d DomainLimits) For(domain string) int {
	if n, ok := d.Overrides[domain]; ok {
		return n
	}
	return d.Default
}

func NewService(db *pgxpool.Pool, q *queue.Queue, supp *suppression.Service, keys DomainKeys, router *mailer.Router,
	limiter ratelimit.Limiter, limits DomainLimits, verp bounce.VERP, signer *security.Signer, baseURL string) *Service {
	return &Service{db: db, q: q, supp: supp, keys: keys, router: router, limiter: limiter, limits: limits,
		verp: verp, signer: signer, baseURL: baseURL}
}

// OnEvent registers a hook (e.g. the sequence engine stopping on reply).
func (s *Service) OnEvent(fn func(context.Context, Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, fn)
}

func (s *Service) emit(ctx context.Context, ev Event) {
	metrics.EmailEvents.WithLabelValues(ev.Type).Inc()
	_, err := s.db.Exec(ctx, `INSERT INTO message_events (message_id, workspace_id, type) VALUES ($1,$2,$3)`,
		ev.MessageID, ev.WorkspaceID, ev.Type)
	if err != nil {
		slog.Error("record event", "err", err, "type", ev.Type)
	}
	s.mu.RLock()
	hooks := s.hooks
	s.mu.RUnlock()
	for _, h := range hooks {
		h(ctx, ev)
	}
}

// RemainingQuota returns how many bulk emails the workspace may still queue today.
func (s *Service) RemainingQuota(ctx context.Context, wsID string) (int, error) {
	var limit, used int
	err := s.db.QueryRow(ctx, `
		SELECT w.daily_send_limit,
		       (SELECT count(*) FROM messages m WHERE m.workspace_id=w.id
		          AND m.category IN ('marketing','sequence') AND m.queued_at >= date_trunc('day', now()))
		FROM workspaces w WHERE w.id=$1`, wsID).Scan(&limit, &used)
	return max(limit-used, 0), err
}

// Queue persists the message and schedules delivery. db may be a transaction.
func (s *Service) Queue(ctx context.Context, db database.DBTX, out Outgoing) (string, error) {
	to, err := emailaddr.Parse(out.ToEmail)
	if err != nil {
		return "", apperr.Invalid("invalid_recipient", "recipient address is not valid")
	}
	from, err := emailaddr.Parse(out.FromEmail)
	if err != nil {
		return "", apperr.Invalid("invalid_from", "from address is not valid")
	}
	out.ToEmail, out.FromEmail = to.String(), from.String()
	id := security.UUID()
	msgIDHeader := security.RandomHex(12) + "." + id[:8] + "@" + from.Domain

	status := "queued"
	if out.Category != CategoryOTP && out.Category != CategoryTransactional {
		reason, err := s.supp.WithTx(db).Check(ctx, out.WorkspaceID, out.ToEmail)
		if err != nil {
			return "", apperr.Internal(err)
		}
		if reason != "" {
			status = "suppressed"
		}
	}

	if out.Category.bulk() && out.Raw == nil {
		var name, addr string
		if err := db.QueryRow(ctx, `SELECT name, postal_address FROM workspaces WHERE id=$1`, out.WorkspaceID).Scan(&name, &addr); err != nil {
			return "", apperr.Internal(err)
		}
		unsub := s.UnsubscribeURL(id)
		out.HTML = withUnsubscribe(out.HTML, unsub, name, addr, true)
		out.Text = withUnsubscribe(out.Text, unsub, name, addr, false)
		out.HTML = s.rewriteLinks(id, out.HTML, unsub) + s.openPixel(id)
	}
	if out.Headers == nil {
		out.Headers = map[string]string{}
	}

	_, err = db.Exec(ctx, `
		INSERT INTO messages (id, workspace_id, category, campaign_id, enrollment_id, step_id, contact_id,
			from_email, from_name, reply_to, to_email, subject, html, text_body, headers, in_reply_to, raw, message_id_header, status)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,NULLIF($7,'')::uuid,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		id, out.WorkspaceID, string(out.Category), out.CampaignID, out.EnrollmentID, out.StepID, out.ContactID,
		out.FromEmail, out.FromName, out.ReplyTo, out.ToEmail, out.Subject, out.HTML, out.Text, out.Headers, out.InReplyTo,
		out.Raw, msgIDHeader, status)
	if err != nil {
		return "", apperr.Internal(err)
	}
	if status == "suppressed" {
		return id, ErrSuppressed
	}
	if out.Priority == 0 && !out.Category.bulk() {
		out.Priority = queue.PriorityTransactional
	}
	err = s.q.WithTx(db).Enqueue(ctx, JobSend, sendPayload{MessageID: id},
		queue.Options{Priority: out.Priority, RunAt: out.RunAt, MaxAttempts: cmp(out.MaxAttempts, 8)})
	if err != nil {
		return "", apperr.Internal(err)
	}
	return id, nil
}

func cmp(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func withUnsubscribe(body, url, company, address string, html bool) string {
	if body == "" {
		return body
	}
	if strings.Contains(body, UnsubscribePlaceholder) {
		return strings.ReplaceAll(body, UnsubscribePlaceholder, url)
	}
	ident := strings.Trim(company+" · "+address, " ·")
	if html {
		return body + fmt.Sprintf(`<p style="font-size:12px;color:#888;margin-top:24px">%s<br>`+
			`<a href="%s" style="color:#888">Berhenti berlangganan / Unsubscribe</a></p>`, htmlEscape(ident), url)
	}
	return body + "\n\n--\n" + ident + "\nBerhenti berlangganan / Unsubscribe: " + url + "\n"
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// ---- tracking ----

func (s *Service) UnsubscribeURL(msgID string) string {
	return s.baseURL + "/u/" + s.signer.Sign("u:"+msgID)
}

func (s *Service) openPixel(msgID string) string {
	return `<img src="` + s.baseURL + "/t/o/" + s.signer.Sign("o:"+msgID) + `" width="1" height="1" alt="" style="display:none">`
}

var hrefRe = regexp.MustCompile(`href="(https?://[^"]+)"`)

func (s *Service) rewriteLinks(msgID, html, unsubURL string) string {
	return hrefRe.ReplaceAllStringFunc(html, func(m string) string {
		u := hrefRe.FindStringSubmatch(m)[1]
		if u == unsubURL || strings.HasPrefix(u, s.baseURL+"/u/") {
			return m
		}
		return `href="` + s.baseURL + "/t/c/" + s.signer.Sign("c:"+msgID+"\n"+u) + `"`
	})
}

// ---- sending (worker) ----

type sendPayload struct {
	MessageID string `json:"message_id"`
}

type storedMessage struct {
	ID, WorkspaceID, Category, EnrollmentID, ContactID string
	From, FromName, ReplyTo, To, Subject, HTML, Text   string
	InReplyTo                                          string
	Headers                                            map[string]string
	Raw                                                []byte
	MessageIDHeader, Status, WorkspaceStatus           string
	QueuedAt                                           time.Time
}

// HandleSend is the queue handler for JobSend.
func (s *Service) HandleSend(ctx context.Context, job queue.Job) error {
	var p sendPayload
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	var m storedMessage
	err := s.db.QueryRow(ctx, `
		SELECT m.id, m.workspace_id, m.category, coalesce(m.enrollment_id::text,''), coalesce(m.contact_id::text,''),
		       m.from_email, m.from_name, m.reply_to, m.to_email, m.subject, m.html, m.text_body, m.in_reply_to, m.headers, m.raw,
		       m.message_id_header, m.status, w.status, m.queued_at
		FROM messages m JOIN workspaces w ON w.id=m.workspace_id WHERE m.id=$1`, p.MessageID).Scan(
		&m.ID, &m.WorkspaceID, &m.Category, &m.EnrollmentID, &m.ContactID, &m.From, &m.FromName, &m.ReplyTo, &m.To,
		&m.Subject, &m.HTML, &m.Text, &m.InReplyTo, &m.Headers, &m.Raw, &m.MessageIDHeader, &m.Status, &m.WorkspaceStatus, &m.QueuedAt)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.Status != "queued" {
		return nil // idempotent on retries
	}
	if m.WorkspaceStatus != "active" {
		return s.fail(ctx, m, "workspace_suspended")
	}
	if Category(m.Category) != CategoryOTP && Category(m.Category) != CategoryTransactional {
		if reason, _ := s.supp.Check(ctx, m.WorkspaceID, m.To); reason != "" {
			_, err := s.db.Exec(ctx, `UPDATE messages SET status='suppressed', error=$2 WHERE id=$1`, m.ID, reason)
			return err
		}
	}

	raw, err := s.build(m)
	if err != nil {
		return s.fail(ctx, m, "build: "+err.Error())
	}
	fromDomain := m.From[strings.LastIndexByte(m.From, '@')+1:]
	if sel, key, err := s.keys.SigningKey(ctx, fromDomain); err == nil {
		if signed, err := mailer.SignDKIM(raw, fromDomain, sel, key); err == nil {
			raw = signed
		} else {
			slog.Warn("dkim sign failed", "domain", fromDomain, "err", err)
		}
	}

	toDomain := m.To[strings.LastIndexByte(m.To, '@')+1:]
	if Category(m.Category) != CategoryOTP {
		ok, retryIn, err := s.limiter.Allow(ctx, "rcpt-domain:"+toDomain, s.limits.For(toDomain), time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			metrics.Throttled.WithLabelValues("recipient_domain").Inc()
			return queue.RetryAfter(retryIn + time.Second)
		}
	}
	// VERP return-path: asynchronous bounces come back to us, tagged with the message id.
	provider, err := s.router.Send(ctx, mailer.Envelope{From: s.verp.Address(m.ID), To: []string{m.To}}, raw)
	switch {
	case err == nil:
		metrics.EmailsSent.WithLabelValues(m.Category, provider, "sent").Inc()
		if Category(m.Category) == CategoryOTP {
			metrics.OTPDeliveryLatency.Observe(time.Since(m.QueuedAt).Seconds())
		}
		if _, err := s.db.Exec(ctx, `UPDATE messages SET status='sent', provider=$2, sent_at=now(), error='' WHERE id=$1`, m.ID, provider); err != nil {
			return err
		}
		s.emit(ctx, Event{Type: "sent", MessageID: m.ID, WorkspaceID: m.WorkspaceID, EnrollmentID: m.EnrollmentID, ContactID: m.ContactID})
		return nil
	case mailer.IsHardBounce(err):
		metrics.EmailsSent.WithLabelValues(m.Category, provider, "bounced").Inc()
		metrics.Bounces.WithLabelValues("hard", "sync").Inc()
		if _, dbErr := s.db.Exec(ctx, `UPDATE messages SET status='bounced', provider=$2, bounced_at=now(), error=$3, bounce_reason=$3 WHERE id=$1`,
			m.ID, provider, err.Error()); dbErr != nil {
			return dbErr
		}
		_ = s.supp.Add(ctx, m.WorkspaceID, m.To, suppression.ReasonHardBounce)
		s.emit(ctx, Event{Type: "bounce", MessageID: m.ID, WorkspaceID: m.WorkspaceID, EnrollmentID: m.EnrollmentID, ContactID: m.ContactID})
		return nil
	default:
		metrics.EmailsSent.WithLabelValues(m.Category, provider, "deferred").Inc()
		if job.Attempts >= 8 {
			return s.fail(ctx, m, err.Error())
		}
		return err // retried with backoff by the queue
	}
}

func (s *Service) fail(ctx context.Context, m storedMessage, reason string) error {
	metrics.EmailsSent.WithLabelValues(m.Category, "", "failed").Inc()
	_, err := s.db.Exec(ctx, `UPDATE messages SET status='failed', error=$2 WHERE id=$1`, m.ID, reason)
	return err
}

func (s *Service) build(m storedMessage) ([]byte, error) {
	if len(m.Raw) > 0 {
		return m.Raw, nil
	}
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	msg := mailer.Message{
		From:      mail.Address{Name: m.FromName, Address: m.From},
		To:        mail.Address{Address: m.To},
		ReplyTo:   m.ReplyTo,
		Subject:   m.Subject,
		HTML:      m.HTML,
		Text:      m.Text,
		MessageID: m.MessageIDHeader,
		InReplyTo: m.InReplyTo,
		Headers:   m.Headers,
	}
	cat := Category(m.Category)
	if cat.bulk() {
		msg.ListUnsubscribeURL = s.UnsubscribeURL(m.ID)
		msg.Headers["Precedence"] = "bulk"
	}
	if cat == CategoryAutoReply {
		msg.Headers["Auto-Submitted"] = "auto-replied"
		msg.Headers["X-Auto-Response-Suppress"] = "All"
	}
	msg.Headers["Feedback-ID"] = m.ID[:8] + ":" + m.Category + ":balasemail"
	return msg.Bytes()
}

// ---- engagement recording ----

func (s *Service) markOnce(ctx context.Context, msgID, column, evType string) error {
	var ws, enr, contact string
	err := s.db.QueryRow(ctx, `UPDATE messages SET `+column+`=now() WHERE id=$1 AND `+column+` IS NULL
		RETURNING workspace_id, coalesce(enrollment_id::text,''), coalesce(contact_id::text,'')`, msgID).Scan(&ws, &enr, &contact)
	if database.IsNoRows(err) {
		return nil // already recorded
	}
	if err != nil {
		return err
	}
	s.emit(ctx, Event{Type: evType, MessageID: msgID, WorkspaceID: ws, EnrollmentID: enr, ContactID: contact})
	return nil
}

func (s *Service) RecordOpen(ctx context.Context, msgID string) error {
	return s.markOnce(ctx, msgID, "opened_at", "open")
}

func (s *Service) RecordClick(ctx context.Context, msgID string) error {
	_ = s.markOnce(ctx, msgID, "opened_at", "open") // a click implies an open (images may be blocked)
	return s.markOnce(ctx, msgID, "clicked_at", "click")
}

func (s *Service) RecordReply(ctx context.Context, msgID string) error {
	return s.markOnce(ctx, msgID, "replied_at", "reply")
}

// Unsubscribe suppresses the recipient of msgID for its workspace.
func (s *Service) Unsubscribe(ctx context.Context, msgID string) error {
	var ws, to string
	if err := s.db.QueryRow(ctx, `SELECT workspace_id, to_email FROM messages WHERE id=$1`, msgID).Scan(&ws, &to); err != nil {
		return err
	}
	if err := s.supp.Add(ctx, ws, to, suppression.ReasonUnsubscribe); err != nil {
		return err
	}
	return s.markOnce(ctx, msgID, "unsubscribed_at", "unsubscribe")
}

type msgRef struct{ ws, to, enrollment, contact string }

func (s *Service) ref(ctx context.Context, q string, args ...any) (msgRef, bool, error) {
	var r msgRef
	err := s.db.QueryRow(ctx, q, args...).Scan(&r.ws, &r.to, &r.enrollment, &r.contact)
	if database.IsNoRows(err) {
		return r, false, nil
	}
	return r, err == nil, err
}

const refCols = `workspace_id, to_email, coalesce(enrollment_id::text,''), coalesce(contact_id::text,'')`

// RecordBounce applies an asynchronous DSN. Hard bounces suppress the address
// and stop sequences; soft bounces are only recorded.
func (s *Service) RecordBounce(ctx context.Context, msgID string, hard bool, reason string) error {
	if !hard {
		r, ok, err := s.ref(ctx, `SELECT `+refCols+` FROM messages WHERE id=$1`, msgID)
		if ok {
			s.emit(ctx, Event{Type: "soft_bounce", MessageID: msgID, WorkspaceID: r.ws, EnrollmentID: r.enrollment, ContactID: r.contact})
		}
		return err
	}
	r, ok, err := s.ref(ctx, `UPDATE messages SET status='bounced', bounced_at=now(), bounce_reason=$2
		WHERE id=$1 AND bounced_at IS NULL RETURNING `+refCols, msgID, reason)
	if !ok {
		return err // unknown or already recorded
	}
	if err := s.supp.Add(ctx, r.ws, r.to, suppression.ReasonHardBounce); err != nil {
		return err
	}
	s.emit(ctx, Event{Type: "bounce", MessageID: msgID, WorkspaceID: r.ws, EnrollmentID: r.enrollment, ContactID: r.contact})
	return nil
}

// RecordComplaint applies a feedback-loop spam report: permanent suppression.
func (s *Service) RecordComplaint(ctx context.Context, msgID, feedbackType string) error {
	r, ok, err := s.ref(ctx, `UPDATE messages SET complained_at=now() WHERE id=$1 AND complained_at IS NULL RETURNING `+refCols, msgID)
	if !ok {
		return err
	}
	if err := s.supp.Add(ctx, r.ws, r.to, suppression.ReasonComplaint); err != nil {
		return err
	}
	slog.Info("spam complaint recorded", "message_id", msgID, "feedback_type", feedbackType)
	s.emit(ctx, Event{Type: "complaint", MessageID: msgID, WorkspaceID: r.ws, EnrollmentID: r.enrollment, ContactID: r.contact})
	return nil
}

// FindByMessageIDHeader maps an In-Reply-To/References value to our message id.
func (s *Service) FindByMessageIDHeader(ctx context.Context, header string) (id, wsID string, ok bool) {
	h := strings.Trim(strings.TrimSpace(header), "<>")
	if h == "" {
		return "", "", false
	}
	err := s.db.QueryRow(ctx, `SELECT id, workspace_id FROM messages WHERE message_id_header=$1`, h).Scan(&id, &wsID)
	return id, wsID, err == nil
}

// ---- reputation guard (PRD §8) ----

// EnforceReputation suspends workspaces whose 7-day hard-bounce rate exceeds
// 5% or complaint rate exceeds 0.3% (min. 200 sends to avoid noise).
func (s *Service) EnforceReputation(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `
		WITH stats AS (
			SELECT m.workspace_id,
			       count(*) FILTER (WHERE m.status IN ('sent','bounced')) AS sent,
			       count(*) FILTER (WHERE m.status='bounced') AS bounced,
			       (SELECT count(*) FROM message_events e WHERE e.workspace_id=m.workspace_id
			          AND e.type='complaint' AND e.created_at > now()-interval '7 days') AS complaints
			FROM messages m
			WHERE m.queued_at > now() - interval '7 days' AND m.category IN ('marketing','sequence')
			GROUP BY m.workspace_id)
		UPDATE workspaces w SET status='suspended'
		FROM stats WHERE w.id=stats.workspace_id AND w.status='active' AND stats.sent >= 200
		  AND (stats.bounced::float / stats.sent > 0.05 OR stats.complaints::float / stats.sent > 0.003)
		RETURNING w.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		slog.Warn("workspace suspended for poor sending reputation", "workspace_id", id)
	}
	return rows.Err()
}
