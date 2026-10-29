package sequence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/campaign"
	"github.com/danangsantoso/balasemail.com/internal/contact"
	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
	"github.com/danangsantoso/balasemail.com/internal/template"
)

const JobWebhook = "sequence.webhook"

var errWindow = apperr.Invalid("invalid_send_window", "send window hours or timezone are invalid")

type Sequence struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	FromName   string     `json:"from_name"`
	FromEmail  string     `json:"from_email"`
	ReplyTo    string     `json:"reply_to"`
	Definition Definition `json:"definition"`
	SendWindow SendWindow `json:"send_window"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Input struct {
	Name       string     `json:"name"`
	FromName   string     `json:"from_name"`
	FromEmail  string     `json:"from_email"`
	ReplyTo    string     `json:"reply_to"`
	Definition Definition `json:"definition"`
	SendWindow SendWindow `json:"send_window"`
}

type SenderAuthorizer = campaign.SenderAuthorizer

type ContactTagger interface {
	AddTags(ctx context.Context, wsID, id string, tags []string) error
	RemoveTags(ctx context.Context, wsID, id string, tags []string) error
}

// SMSSender is implemented by the sms package (PRD F11 action node).
type SMSSender interface {
	SendFromWorkflow(ctx context.Context, wsID, toPhone, body string) error
}

type Service struct {
	db       *pgxpool.Pool
	delivery *delivery.Service
	domains  SenderAuthorizer
	contacts ContactTagger
	sms      SMSSender
	q        *queue.Queue
	signer   *security.Signer
	limits   Limits
	client   *http.Client
}

func NewService(db *pgxpool.Pool, d *delivery.Service, domains SenderAuthorizer, contacts ContactTagger,
	sms SMSSender, q *queue.Queue, signer *security.Signer, client *http.Client) *Service {
	s := &Service{db: db, delivery: d, domains: domains, contacts: contacts, sms: sms, q: q, signer: signer,
		limits: DefaultLimits, client: client}
	d.OnEvent(s.onDeliveryEvent)
	return s
}

const cols = `id, name, from_name, from_email, reply_to, definition, send_window, status, created_at`

func scan(r pgx.Row) (Sequence, error) {
	var s Sequence
	err := r.Scan(&s.ID, &s.Name, &s.FromName, &s.FromEmail, &s.ReplyTo, &s.Definition, &s.SendWindow, &s.Status, &s.CreatedAt)
	return s, err
}

func (s *Service) validate(in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return apperr.Invalid("missing_name", "name is required")
	}
	if in.FromEmail == "" {
		return apperr.Invalid("missing_from", "from_email is required")
	}
	if err := in.SendWindow.Validate(); err != nil {
		return err
	}
	return in.Definition.Validate(s.limits)
}

func (s *Service) Create(ctx context.Context, wsID string, in Input) (Sequence, error) {
	if err := s.validate(in); err != nil {
		return Sequence{}, err
	}
	seq, err := scan(s.db.QueryRow(ctx, `
		INSERT INTO sequences (workspace_id, name, from_name, from_email, reply_to, definition, send_window)
		VALUES ($1,$2,$3,lower($4),$5,$6,$7) RETURNING `+cols,
		wsID, in.Name, in.FromName, in.FromEmail, in.ReplyTo, in.Definition, in.SendWindow))
	if err != nil {
		return seq, apperr.Internal(err)
	}
	return seq, nil
}

// Update replaces the definition. Active enrollments whose current node no
// longer exists are completed on their next tick.
func (s *Service) Update(ctx context.Context, wsID, id string, in Input) (Sequence, error) {
	if err := s.validate(in); err != nil {
		return Sequence{}, err
	}
	seq, err := scan(s.db.QueryRow(ctx, `
		UPDATE sequences SET name=$3, from_name=$4, from_email=lower($5), reply_to=$6, definition=$7, send_window=$8, updated_at=now()
		WHERE id=$1 AND workspace_id=$2 RETURNING `+cols,
		id, wsID, in.Name, in.FromName, in.FromEmail, in.ReplyTo, in.Definition, in.SendWindow))
	if database.IsNoRows(err) {
		return seq, apperr.NotFound("sequence_not_found", "sequence not found")
	}
	if err != nil {
		return seq, apperr.Internal(err)
	}
	return seq, nil
}

func (s *Service) Get(ctx context.Context, wsID, id string) (Sequence, error) {
	seq, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM sequences WHERE id=$1 AND workspace_id=$2`, id, wsID))
	if database.IsNoRows(err) {
		return seq, apperr.NotFound("sequence_not_found", "sequence not found")
	}
	if err != nil {
		return seq, apperr.Internal(err)
	}
	return seq, nil
}

func (s *Service) List(ctx context.Context, wsID string) ([]Sequence, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM sequences WHERE workspace_id=$1 ORDER BY created_at DESC`, wsID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Sequence, error) { return scan(r) })
}

// SetStatus activates (after sender checks) or pauses a sequence.
func (s *Service) SetStatus(ctx context.Context, wsID, id, status string) (Sequence, error) {
	seq, err := s.Get(ctx, wsID, id)
	if err != nil {
		return seq, err
	}
	if status == "active" {
		if _, err := s.domains.AuthorizeSender(ctx, wsID, seq.FromEmail); err != nil {
			return seq, err
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE sequences SET status=$2, updated_at=now() WHERE id=$1`, id, status); err != nil {
		return seq, apperr.Internal(err)
	}
	seq.Status = status
	return seq, nil
}

// Enroll adds contacts (by id and/or tag) at the start node. Unsubscribed or
// invalid contacts are skipped; re-enrolling is a no-op.
func (s *Service) Enroll(ctx context.Context, wsID, id string, contactIDs []string, tag string) (int, error) {
	seq, err := s.Get(ctx, wsID, id)
	if err != nil {
		return 0, err
	}
	if contactIDs == nil {
		contactIDs = []string{}
	}
	tag = strings.ToLower(strings.TrimSpace(tag))
	if len(contactIDs) == 0 && tag == "" {
		return 0, apperr.Invalid("missing_target", "provide contact_ids or tag")
	}
	tagCmd, err := s.db.Exec(ctx, `
		INSERT INTO enrollments (workspace_id, sequence_id, contact_id, current_node)
		SELECT $1, $2, c.id, $3 FROM contacts c
		WHERE c.workspace_id=$1 AND c.unsubscribed_at IS NULL AND c.validation_status NOT IN ('invalid','disposable')
		  AND (c.id::text = ANY($4) OR ($5 <> '' AND $5 = ANY(c.tags)))
		ON CONFLICT (sequence_id, contact_id) DO NOTHING`, wsID, seq.ID, seq.Definition.Start, contactIDs, tag)
	if err != nil {
		return 0, apperr.Internal(err)
	}
	return int(tagCmd.RowsAffected()), nil
}

// Tick advances due enrollments; run periodically by the worker.
func (s *Service) Tick(ctx context.Context) error {
	for {
		n, err := s.tickBatch(ctx, 100)
		if err != nil || n < 100 {
			return err
		}
	}
}

type dueEnrollment struct {
	id, wsID, node, lastMsg string
	seq                     Sequence
	ct                      contact.Contact
}

func (s *Service) tickBatch(ctx context.Context, limit int) (int, error) {
	processed := 0
	err := database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.id, e.workspace_id, e.current_node, coalesce(e.last_message_id::text,''),
			       q.id, q.name, q.from_name, q.from_email, q.reply_to, q.definition, q.send_window, q.status, q.created_at,
			       c.id, c.email, c.first_name, c.last_name, c.company, c.title, c.phone, c.tags, c.fields
			FROM enrollments e
			JOIN sequences q ON q.id=e.sequence_id AND q.status='active'
			JOIN contacts c ON c.id=e.contact_id
			WHERE e.status='active' AND e.next_run_at <= now()
			ORDER BY e.next_run_at LIMIT $1
			FOR UPDATE OF e SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		due, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (dueEnrollment, error) {
			var d dueEnrollment
			err := r.Scan(&d.id, &d.wsID, &d.node, &d.lastMsg,
				&d.seq.ID, &d.seq.Name, &d.seq.FromName, &d.seq.FromEmail, &d.seq.ReplyTo, &d.seq.Definition, &d.seq.SendWindow, &d.seq.Status, &d.seq.CreatedAt,
				&d.ct.ID, &d.ct.Email, &d.ct.FirstName, &d.ct.LastName, &d.ct.Company, &d.ct.Title, &d.ct.Phone, &d.ct.Tags, &d.ct.Fields)
			return d, err
		})
		if err != nil {
			return err
		}
		processed = len(due)
		for _, d := range due {
			if err := s.advance(ctx, tx, d); err != nil {
				slog.Error("sequence advance failed", "enrollment", d.id, "err", err)
				_, _ = tx.Exec(ctx, `UPDATE enrollments SET next_run_at=now()+interval '15 minutes', updated_at=now() WHERE id=$1`, d.id)
			}
		}
		return nil
	})
	return processed, err
}

func (s *Service) facts(ctx context.Context, tx pgx.Tx, d dueEnrollment) (Facts, string, error) {
	f := Facts{Tags: d.ct.Tags, Fields: d.ct.Fields}
	if d.lastMsg == "" {
		return f, "", nil
	}
	var hdr string
	err := tx.QueryRow(ctx, `SELECT opened_at IS NOT NULL, clicked_at IS NOT NULL, replied_at IS NOT NULL, message_id_header
		FROM messages WHERE id=$1`, d.lastMsg).Scan(&f.Opened, &f.Clicked, &f.Replied, &hdr)
	if database.IsNoRows(err) {
		return f, "", nil
	}
	return f, hdr, err
}

func (s *Service) advance(ctx context.Context, tx pgx.Tx, d dueEnrollment) error {
	facts, lastHeader, err := s.facts(ctx, tx, d)
	if err != nil {
		return err
	}
	now := time.Now()
	res := Advance(d.seq.Definition, d.node, facts, now, d.seq.SendWindow, d.id)
	for _, t := range res.Visited {
		metrics.SequenceTransitions.WithLabelValues(string(t)).Inc()
	}

	lastMsg := d.lastMsg
	for _, eff := range res.Effects {
		switch eff.Kind {
		case EffectEmail:
			quota, err := s.delivery.RemainingQuota(ctx, d.wsID)
			if err != nil {
				return err
			}
			if quota == 0 {
				// Retry this node in an hour without losing the position.
				_, err := tx.Exec(ctx, `UPDATE enrollments SET current_node=$2, next_run_at=now()+interval '1 hour', updated_at=now() WHERE id=$1`,
					d.id, eff.Node.ID)
				return err
			}
			id, err := s.queueEmail(ctx, tx, d, eff, lastHeader)
			if errors.Is(err, delivery.ErrSuppressed) {
				return s.finish(ctx, tx, d.id, StatusStopped, "suppressed", lastMsg)
			}
			if err != nil {
				return err
			}
			lastMsg = id
		case EffectAction:
			s.runAction(ctx, d, *eff.Node.Action)
		}
	}
	if res.Status != StatusActive {
		return s.finish(ctx, tx, d.id, res.Status, res.StopReason, lastMsg)
	}
	_, err = tx.Exec(ctx, `UPDATE enrollments SET current_node=$2, next_run_at=$3, last_message_id=NULLIF($4,'')::uuid, updated_at=now() WHERE id=$1`,
		d.id, res.Node, res.RunAt, lastMsg)
	return err
}

func (s *Service) finish(ctx context.Context, tx pgx.Tx, id string, st Status, reason, lastMsg string) error {
	_, err := tx.Exec(ctx, `UPDATE enrollments SET status=$2, stop_reason=$3, last_message_id=NULLIF($4,'')::uuid, updated_at=now() WHERE id=$1`,
		id, string(st), reason, lastMsg)
	return err
}

func (s *Service) queueEmail(ctx context.Context, tx pgx.Tx, d dueEnrollment, eff Effect, lastHeader string) (string, error) {
	subject, html, text := eff.Content()
	vars := campaign.Vars(d.ct)
	seed := d.id + eff.Node.ID
	out := delivery.Outgoing{
		WorkspaceID: d.wsID, Category: delivery.CategorySequence, EnrollmentID: d.id, StepID: eff.Node.ID, ContactID: d.ct.ID,
		FromEmail: d.seq.FromEmail, FromName: d.seq.FromName, ReplyTo: d.seq.ReplyTo, ToEmail: d.ct.Email,
		Subject: template.Personalize(subject, vars, seed, false),
		HTML:    template.Personalize(html, vars, seed, true),
		Text:    template.Personalize(text, vars, seed, false),
		Headers: map[string]string{"X-Sequence-ID": d.seq.ID, "X-Sequence-Step": eff.Node.ID, "X-Variant": fmt.Sprint(eff.Variant)},
	}
	if eff.Node.ThreadReply && lastHeader != "" {
		out.InReplyTo = lastHeader
		if !strings.HasPrefix(strings.ToLower(out.Subject), "re:") {
			out.Subject = "Re: " + out.Subject
		}
	}
	return s.delivery.Queue(ctx, tx, out)
}

// runAction performs best-effort side effects; failures are logged, not fatal.
func (s *Service) runAction(ctx context.Context, d dueEnrollment, a Action) {
	var err error
	switch a.Type {
	case ActAddTag:
		err = s.contacts.AddTags(ctx, d.wsID, d.ct.ID, a.Tags)
	case ActRemoveTag:
		err = s.contacts.RemoveTags(ctx, d.wsID, d.ct.ID, a.Tags)
	case ActSendSMS:
		if d.ct.Phone == "" || s.sms == nil {
			return
		}
		err = s.sms.SendFromWorkflow(ctx, d.wsID, d.ct.Phone, template.Render(a.SMSBody, d.ct.Vars(), false))
	case ActWebhook:
		err = s.q.Enqueue(ctx, JobWebhook, webhookPayload{URL: a.URL, Body: map[string]any{
			"event": "sequence.step", "sequence_id": d.seq.ID, "enrollment_id": d.id, "contact": d.ct,
		}}, queue.Options{Priority: queue.PriorityTransactional, MaxAttempts: 5})
	case ActNotify:
		_, err = s.delivery.Queue(ctx, s.db, delivery.Outgoing{
			WorkspaceID: d.wsID, Category: delivery.CategoryTransactional, FromEmail: d.seq.FromEmail, FromName: "BalasEmail",
			ToEmail: a.NotifyEmail, Subject: fmt.Sprintf("[%s] %s mencapai tahap berikutnya", d.seq.Name, d.ct.Email),
			Text: fmt.Sprintf("Kontak %s %s (%s, %s) mencapai node aksi di sequence %q.", d.ct.FirstName, d.ct.LastName, d.ct.Email, d.ct.Company, d.seq.Name),
		})
	}
	if err != nil {
		slog.Warn("sequence action failed", "action", a.Type, "enrollment", d.id, "err", err)
	}
}

type webhookPayload struct {
	URL  string         `json:"url"`
	Body map[string]any `json:"body"`
}

// HandleWebhook delivers a signed JSON webhook (X-BalasEmail-Signature).
func (s *Service) HandleWebhook(ctx context.Context, job queue.Job) error {
	var p webhookPayload
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	body, _ := json.Marshal(p.Body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return queue.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BalasEmail-Signature", fmt.Sprintf("%x", s.signer.MAC(string(body))))
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return queue.Permanent(fmt.Errorf("webhook returned %d", resp.StatusCode))
	}
	return nil
}

// onDeliveryEvent implements auto-stop (PRD F3): reply, unsubscribe and bounce
// end the enrollment immediately; click ends it when configured.
func (s *Service) onDeliveryEvent(ctx context.Context, ev delivery.Event) {
	if ev.EnrollmentID == "" {
		if ev.Type == "unsubscribe" && ev.ContactID != "" {
			_, _ = s.db.Exec(ctx, `UPDATE enrollments SET status='stopped', stop_reason='unsubscribe', updated_at=now()
				WHERE contact_id=$1 AND status='active'`, ev.ContactID)
		}
		return
	}
	var reason string
	switch ev.Type {
	case "reply", "bounce", "complaint", "unsubscribe":
		reason = ev.Type
	case "click":
		reason = "click"
	default:
		return
	}
	_, err := s.db.Exec(ctx, `
		UPDATE enrollments e SET status='stopped', stop_reason=$2, updated_at=now()
		FROM sequences q
		WHERE e.id=$1 AND q.id=e.sequence_id AND e.status='active'
		  AND ($2 IN ('bounce','complaint','unsubscribe')
		       OR ($2='reply' AND coalesce((q.definition->'settings'->>'stop_on_reply')::boolean, true))
		       OR ($2='click' AND coalesce((q.definition->'settings'->>'stop_on_click')::boolean, false)))`,
		ev.EnrollmentID, reason)
	if err != nil {
		slog.Error("auto-stop enrollment", "err", err)
	}
	if (reason == "unsubscribe" || reason == "complaint") && ev.ContactID != "" {
		// Unsubscribing (or complaining) about one sequence stops all of them for that contact.
		_, _ = s.db.Exec(ctx, `UPDATE enrollments SET status='stopped', stop_reason='unsubscribe', updated_at=now()
			WHERE contact_id=$1 AND status='active'`, ev.ContactID)
	}
}

// StepReport is one row of the funnel report.
type StepReport struct {
	NodeID  string  `json:"node_id"`
	Sent    int     `json:"sent"`
	Opened  int     `json:"opened"`
	Clicked int     `json:"clicked"`
	Replied int     `json:"replied"`
	Bounced int     `json:"bounced"`
	Variant string  `json:"variant"`
	Reply   float64 `json:"reply_rate"`
}

type Report struct {
	Enrollments map[string]int `json:"enrollments"` // by status
	Waiting     map[string]int `json:"waiting_at"`  // active enrollments by node
	StopReasons map[string]int `json:"stop_reasons"`
	Steps       []StepReport   `json:"steps"`
}

// Report returns the per-layer funnel metrics (PRD F3 reporting).
func (s *Service) Report(ctx context.Context, wsID, id string) (Report, error) {
	if _, err := s.Get(ctx, wsID, id); err != nil {
		return Report{}, err
	}
	rep := Report{Enrollments: map[string]int{}, Waiting: map[string]int{}, StopReasons: map[string]int{}}
	rows, err := s.db.Query(ctx, `SELECT status, current_node, stop_reason, count(*) FROM enrollments
		WHERE sequence_id=$1 GROUP BY 1,2,3`, id)
	if err != nil {
		return rep, apperr.Internal(err)
	}
	for rows.Next() {
		var st, node, reason string
		var n int
		if err := rows.Scan(&st, &node, &reason, &n); err != nil {
			rows.Close()
			return rep, apperr.Internal(err)
		}
		rep.Enrollments[st] += n
		if st == "active" {
			rep.Waiting[node] += n
		}
		if reason != "" {
			rep.StopReasons[reason] += n
		}
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `
		SELECT m.step_id, coalesce(m.headers->>'X-Variant','0'),
		       count(*) FILTER (WHERE m.status IN ('sent','bounced')), count(m.opened_at), count(m.clicked_at),
		       count(m.replied_at), count(*) FILTER (WHERE m.status='bounced')
		FROM messages m JOIN enrollments e ON e.id=m.enrollment_id
		WHERE e.sequence_id=$1 GROUP BY 1,2 ORDER BY 1,2`, id)
	if err != nil {
		return rep, apperr.Internal(err)
	}
	rep.Steps, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (StepReport, error) {
		var sr StepReport
		err := r.Scan(&sr.NodeID, &sr.Variant, &sr.Sent, &sr.Opened, &sr.Clicked, &sr.Replied, &sr.Bounced)
		if sr.Sent > 0 {
			sr.Reply = float64(sr.Replied) / float64(sr.Sent)
		}
		return sr, err
	})
	return rep, err
}
