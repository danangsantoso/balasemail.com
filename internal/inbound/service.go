package inbound

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/alias"
	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/suppression"
	"github.com/danangsantoso/balasemail.com/internal/template"
)

type DomainLookup interface {
	Lookup(ctx context.Context, domain string) (wsID, domainID string, ok bool)
}

type Service struct {
	db         *pgxpool.Pool
	delivery   *delivery.Service
	supp       *suppression.Service
	aliases    *alias.Service
	domains    DomainLookup
	classifier Classifier
}

func NewService(db *pgxpool.Pool, d *delivery.Service, supp *suppression.Service, a *alias.Service, dl DomainLookup, c Classifier) *Service {
	return &Service{db: db, delivery: d, supp: supp, aliases: a, domains: dl, classifier: c}
}

// Accepts reports whether rcpt belongs to a verified customer domain.
func (s *Service) Accepts(ctx context.Context, rcpt string) bool {
	addr, err := emailaddr.Parse(rcpt)
	if err != nil {
		return false
	}
	_, _, ok := s.domains.Lookup(ctx, addr.Domain)
	return ok
}

// Receive stores, classifies and routes one inbound message.
func (s *Service) Receive(ctx context.Context, rcpt string, raw []byte) error {
	e, err := Parse(rcpt, raw)
	if err != nil {
		return apperr.Invalid("unparseable", "message could not be parsed")
	}
	addr, err := emailaddr.Parse(e.Rcpt)
	if err != nil {
		return apperr.Invalid("bad_rcpt", "invalid recipient")
	}
	wsID, _, ok := s.domains.Lookup(ctx, addr.Domain)
	if !ok {
		return apperr.NotFound("unknown_domain", "recipient domain is not hosted here")
	}

	automated := IsAutomated(e)
	class := s.classifier.Classify(ctx, e.Subject, e.Text)
	if automated && class != ClassOutOfOffice {
		class = "automated"
	}
	metrics.InboundEmails.WithLabelValues(class).Inc()

	// Reply detection via In-Reply-To / References against our Message-IDs.
	related := ""
	for _, ref := range e.InReplyTo {
		if id, owner, ok := s.delivery.FindByMessageIDHeader(ctx, ref); ok && owner == wsID {
			related = id
			break
		}
	}

	if _, err := s.db.Exec(ctx, `INSERT INTO inbound_emails (workspace_id, rcpt, from_email, from_name, subject, text_body,
		message_id_header, in_reply_to, classification, related_message_id, automated, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,$11,$12)`,
		wsID, e.Rcpt, e.From, e.FromName, e.Subject, e.Text, e.MessageID, strings.Join(e.InReplyTo, " "), class, related, automated, raw); err != nil {
		return apperr.Internal(err)
	}

	if related != "" && !automated {
		switch class {
		case ClassUnsubscribe:
			if err := s.delivery.Unsubscribe(ctx, related); err != nil {
				slog.Error("unsubscribe from reply", "err", err)
			}
		default:
			if err := s.delivery.RecordReply(ctx, related); err != nil {
				slog.Error("record reply", "err", err)
			}
		}
	} else if class == ClassUnsubscribe && !automated && e.From != "" {
		_ = s.supp.Add(ctx, wsID, e.From, suppression.ReasonUnsubscribe)
	}

	if e.Header.Get("X-BalasEmail-Loop") != "" {
		return nil // our own forwarded/auto-replied mail coming back: stop here
	}

	// Alias-level forwarding (PRD F8 + F10).
	if a, ok := s.aliases.Find(ctx, e.Rcpt); ok && len(a.ForwardTo) > 0 {
		for _, to := range a.ForwardTo {
			s.forward(ctx, wsID, e, to, "alias")
		}
	}

	rules, err := s.rules(ctx, wsID)
	if err != nil {
		return apperr.Internal(err)
	}
	now := time.Now()
	for _, r := range rules {
		if !r.Matches(e, class, now) {
			continue
		}
		switch r.Action.Type {
		case ActAutoReply:
			s.autoReply(ctx, wsID, r, e, automated)
		case ActForward:
			targets := r.Action.ForwardTo
			if r.Action.RoundRobin && len(targets) > 1 {
				var cursor int
				if err := s.db.QueryRow(ctx, `UPDATE inbound_rules SET rr_cursor=rr_cursor+1 WHERE id=$1 RETURNING rr_cursor`, r.ID).Scan(&cursor); err == nil {
					targets = []string{targets[(cursor-1)%len(targets)]}
				}
			}
			for _, to := range targets {
				s.forward(ctx, wsID, e, to, r.ID)
			}
		}
		if r.StopProcessing {
			break
		}
	}
	return nil
}

func ruleVars(e Email) map[string]string {
	name := e.FromName
	if name == "" {
		name = "Bapak/Ibu"
	}
	return map[string]string{"subject": e.Subject, "from_name": name, "from_email": e.From, "to": e.Rcpt}
}

func (s *Service) autoReply(ctx context.Context, wsID string, r Rule, e Email, automated bool) {
	if automated || e.From == "" || e.From == e.Rcpt {
		metrics.InboundActions.WithLabelValues("auto_reply", "skipped_loop_guard").Inc()
		return
	}
	// One auto-reply per rule+sender per 24h.
	var stamp time.Time
	err := s.db.QueryRow(ctx, `INSERT INTO auto_reply_log (rule_id, sender) VALUES ($1,$2)
		ON CONFLICT (rule_id, sender) DO UPDATE SET sent_at=now() WHERE auto_reply_log.sent_at < now() - interval '24 hours'
		RETURNING sent_at`, r.ID, e.From).Scan(&stamp)
	if database.IsNoRows(err) {
		metrics.InboundActions.WithLabelValues("auto_reply", "skipped_rate_limit").Inc()
		return
	}
	if err != nil {
		slog.Error("auto-reply log", "err", err)
		return
	}
	vars := ruleVars(e)
	subject := r.Action.Subject
	if subject == "" {
		subject = "Re: {{subject}}"
	}
	from := e.Rcpt
	if base, _ := emailaddr.SplitPlus(strings.Split(from, "@")[0]); base != "" {
		from = base + "@" + strings.Split(from, "@")[1]
	}
	_, err = s.delivery.Queue(ctx, s.db, delivery.Outgoing{
		WorkspaceID: wsID, Category: delivery.CategoryAutoReply, FromEmail: from, ToEmail: e.From,
		Subject: template.Render(subject, vars, false), Text: template.Render(r.Action.Body, vars, false),
		InReplyTo: e.MessageID, Headers: map[string]string{"X-BalasEmail-Loop": wsID},
	})
	outcome := "sent"
	if err != nil {
		outcome = "error"
		slog.Warn("auto-reply queue failed", "err", err)
	}
	metrics.InboundActions.WithLabelValues("auto_reply", outcome).Inc()
}

func (s *Service) forward(ctx context.Context, wsID string, e Email, to, source string) {
	if strings.EqualFold(to, e.Rcpt) || strings.EqualFold(to, e.From) {
		return
	}
	from := e.Rcpt
	if base, _ := emailaddr.SplitPlus(strings.Split(from, "@")[0]); base != "" {
		from = base + "@" + strings.Split(from, "@")[1]
	}
	name := e.FromName
	if name == "" {
		name = e.From
	}
	body := fmt.Sprintf("---------- Pesan diteruskan / Forwarded message ----------\nDari: %s <%s>\nKepada: %s\nSubjek: %s\n\n%s",
		e.FromName, e.From, e.Rcpt, e.Subject, e.Text)
	// Re-sent from the customer's own verified domain so SPF/DKIM/DMARC align
	// (avoids the SRS problem of classic forwarding); Reply-To keeps the thread with the sender.
	_, err := s.delivery.Queue(ctx, s.db, delivery.Outgoing{
		WorkspaceID: wsID, Category: delivery.CategoryForward, FromEmail: from, FromName: name + " (via BalasEmail)",
		ReplyTo: e.From, ToEmail: to, Subject: "Fwd: " + e.Subject, Text: body,
		Headers: map[string]string{"X-BalasEmail-Loop": wsID, "X-Forwarded-For": e.From, "X-BalasEmail-Rule": source},
	})
	outcome := "sent"
	if err != nil {
		outcome = "error"
		slog.Warn("forward queue failed", "err", err)
	}
	metrics.InboundActions.WithLabelValues("forward", outcome).Inc()
}

// ---- rules CRUD & unified inbox ----

func (s *Service) rules(ctx context.Context, wsID string) ([]Rule, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, priority, enabled, match, action, stop_processing, created_at
		FROM inbound_rules WHERE workspace_id=$1 ORDER BY priority, created_at`, wsID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rule, error) {
		var rl Rule
		err := r.Scan(&rl.ID, &rl.Name, &rl.Priority, &rl.Enabled, &rl.Match, &rl.Action, &rl.StopProcessing, &rl.CreatedAt)
		return rl, err
	})
}

func (s *Service) SaveRule(ctx context.Context, wsID, id string, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	for i, f := range r.Action.ForwardTo {
		n, err := emailaddr.Normalize(f)
		if err != nil {
			return r, apperr.Invalid("invalid_forward_to", "forward_to contains an invalid address")
		}
		r.Action.ForwardTo[i] = n
	}
	if r.Priority == 0 {
		r.Priority = 100
	}
	var err error
	if id == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO inbound_rules (workspace_id, name, priority, enabled, match, action, stop_processing)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`, wsID, r.Name, r.Priority, r.Enabled, r.Match, r.Action, r.StopProcessing).
			Scan(&r.ID, &r.CreatedAt)
	} else {
		err = s.db.QueryRow(ctx, `UPDATE inbound_rules SET name=$3, priority=$4, enabled=$5, match=$6, action=$7, stop_processing=$8
			WHERE id=$1 AND workspace_id=$2 RETURNING id, created_at`, id, wsID, r.Name, r.Priority, r.Enabled, r.Match, r.Action, r.StopProcessing).
			Scan(&r.ID, &r.CreatedAt)
	}
	if database.IsNoRows(err) {
		return r, apperr.NotFound("rule_not_found", "rule not found")
	}
	if err != nil {
		return r, apperr.Internal(err)
	}
	return r, nil
}

type InboxItem struct {
	ID               string    `json:"id"`
	Rcpt             string    `json:"rcpt"`
	From             string    `json:"from"`
	FromName         string    `json:"from_name"`
	Subject          string    `json:"subject"`
	Snippet          string    `json:"snippet"`
	Classification   string    `json:"classification"`
	RelatedMessageID *string   `json:"related_message_id,omitempty"`
	ReceivedAt       time.Time `json:"received_at"`
}

func (s *Service) Inbox(ctx context.Context, wsID, class string, p httpx.Page) ([]InboxItem, error) {
	rows, err := s.db.Query(ctx, `SELECT id, rcpt, from_email, from_name, subject, left(text_body, 200), classification,
		related_message_id::text, received_at FROM inbound_emails
		WHERE workspace_id=$1 AND ($2='' OR classification=$2) AND NOT automated
		ORDER BY received_at DESC LIMIT $3 OFFSET $4`, wsID, class, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (InboxItem, error) {
		var it InboxItem
		err := r.Scan(&it.ID, &it.Rcpt, &it.From, &it.FromName, &it.Subject, &it.Snippet, &it.Classification, &it.RelatedMessageID, &it.ReceivedAt)
		return it, err
	})
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	ws := func(r *http.Request) string { return authctx.MustFrom(r.Context()).WorkspaceID }
	mux.Handle("GET /v1/inbox", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Inbox(r.Context(), ws(r), r.URL.Query().Get("classification"), httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": items}, err)
	})))
	mux.Handle("GET /v1/inbound/rules", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.rules(r.Context(), ws(r))
		if err != nil {
			err = apperr.Internal(err)
		}
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": rules}, err)
	})))
	save := func(w http.ResponseWriter, r *http.Request, id string, status int) {
		rule := Rule{Enabled: true}
		if err := httpx.Decode(w, r, &rule); err != nil {
			httpx.Error(w, r, err)
			return
		}
		out, err := s.SaveRule(r.Context(), ws(r), id, rule)
		httpx.Respond(w, r, status, out, err)
	}
	mux.Handle("POST /v1/inbound/rules", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		save(w, r, "", http.StatusCreated)
	})))
	mux.Handle("PUT /v1/inbound/rules/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		save(w, r, r.PathValue("id"), http.StatusOK)
	})))
	mux.Handle("DELETE /v1/inbound/rules/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag, err := s.db.Exec(r.Context(), `DELETE FROM inbound_rules WHERE id=$1 AND workspace_id=$2`, r.PathValue("id"), ws(r))
		if err == nil && tag.RowsAffected() == 0 {
			err = apperr.NotFound("rule_not_found", "rule not found")
		}
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
