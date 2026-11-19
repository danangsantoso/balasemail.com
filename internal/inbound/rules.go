package inbound

import (
	"slices"
	"strings"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/sequence"
)

type Rule struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Priority       int       `json:"priority"`
	Enabled        bool      `json:"enabled"`
	Match          Match     `json:"match"`
	Action         RuleAct   `json:"action"`
	StopProcessing bool      `json:"stop_processing"`
	CreatedAt      time.Time `json:"created_at"`
}

// Match conditions are ANDed; each list is ORed. Empty = match all.
type Match struct {
	To              []string `json:"to,omitempty"` // recipient addresses or local parts
	FromContains    []string `json:"from_contains,omitempty"`
	SubjectContains []string `json:"subject_contains,omitempty"`
	BodyContains    []string `json:"body_contains,omitempty"`
	Classification  []string `json:"classification,omitempty"`
	// OutsideHours matches only when the message arrives outside this window (out-of-office replies).
	OutsideHours *sequence.SendWindow `json:"outside_hours,omitempty"`
}

type ActionType string

const (
	ActAutoReply ActionType = "auto_reply"
	ActForward   ActionType = "forward"
)

type RuleAct struct {
	Type       ActionType `json:"type"`
	Subject    string     `json:"subject,omitempty"` // supports {{subject}}, {{from_name}}, {{from_email}}
	Body       string     `json:"body,omitempty"`
	ForwardTo  []string   `json:"forward_to,omitempty"`
	RoundRobin bool       `json:"round_robin,omitempty"` // distribute leads across sales reps
}

func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return apperr.Invalid("missing_name", "name is required")
	}
	switch r.Action.Type {
	case ActAutoReply:
		if strings.TrimSpace(r.Action.Body) == "" {
			return apperr.Invalid("missing_body", "auto_reply needs a body")
		}
	case ActForward:
		if len(r.Action.ForwardTo) == 0 || len(r.Action.ForwardTo) > 10 {
			return apperr.Invalid("invalid_forward_to", "forward needs 1..10 forward_to addresses")
		}
	default:
		return apperr.Invalid("invalid_action", "action.type must be auto_reply or forward")
	}
	if w := r.Match.OutsideHours; w != nil {
		if err := w.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func containsAny(haystack string, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	h := strings.ToLower(haystack)
	return slices.ContainsFunc(needles, func(n string) bool { return n != "" && strings.Contains(h, strings.ToLower(n)) })
}

// Matches evaluates the rule against a parsed email (pure; unit-tested).
func (r Rule) Matches(e Email, class string, now time.Time) bool {
	if !r.Enabled {
		return false
	}
	m := r.Match
	if len(m.To) > 0 {
		local, _, _ := strings.Cut(e.Rcpt, "@")
		base, _, _ := strings.Cut(local, "+")
		if !slices.ContainsFunc(m.To, func(t string) bool {
			t = strings.ToLower(strings.TrimSpace(t))
			return t == e.Rcpt || t == local || t == base
		}) {
			return false
		}
	}
	if !containsAny(e.From+" "+e.FromName, m.FromContains) || !containsAny(e.Subject, m.SubjectContains) ||
		!containsAny(StripQuoted(e.Text), m.BodyContains) {
		return false
	}
	if len(m.Classification) > 0 && !slices.Contains(m.Classification, class) {
		return false
	}
	if m.OutsideHours != nil && m.OutsideHours.Open(now) {
		return false
	}
	return true
}
