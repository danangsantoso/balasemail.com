package bounce

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
)

// Recorder applies outcomes to the message log (implemented by delivery.Service).
type Recorder interface {
	RecordBounce(ctx context.Context, msgID string, hard bool, reason string) error
	RecordComplaint(ctx context.Context, msgID, feedbackType string) error
	FindByMessageIDHeader(ctx context.Context, header string) (id, wsID string, ok bool)
}

// Processor handles mail delivered to the bounce domain: VERP return-paths
// (b<id><tag>@) and feedback-loop mailboxes (e.g. fbl@, abuse@).
type Processor struct {
	VERP VERP
	Rec  Recorder
}

// Accepts reports whether rcpt is on the bounce domain.
func (p *Processor) Accepts(rcpt string) bool {
	at := strings.LastIndexByte(rcpt, '@')
	return at > 0 && strings.EqualFold(rcpt[at+1:], p.VERP.Domain)
}

// Process parses raw and records the outcome. Unattributable reports are
// dropped (logged) rather than rejected, so remote MTAs do not retry forever.
func (p *Processor) Process(ctx context.Context, rcpt string, raw []byte) (Report, error) {
	rep := Parse(raw)
	msgID, ok := p.VERP.Parse(rcpt)
	if !ok && rep.OriginalMessageID != "" {
		msgID, _, ok = p.Rec.FindByMessageIDHeader(ctx, rep.OriginalMessageID)
	}
	metrics.Bounces.WithLabelValues(string(rep.Kind), "async").Inc()
	if !ok {
		if rep.Kind != KindUnknown {
			slog.Warn("bounce not attributable to a message", "rcpt", rcpt, "kind", rep.Kind)
		}
		return rep, nil
	}
	reason := strings.TrimSpace(rep.Status + " " + rep.Diagnostic)
	switch rep.Kind {
	case KindHard:
		return rep, p.Rec.RecordBounce(ctx, msgID, true, reason)
	case KindSoft:
		return rep, p.Rec.RecordBounce(ctx, msgID, false, reason)
	case KindComplaint:
		return rep, p.Rec.RecordComplaint(ctx, msgID, rep.FeedbackType)
	}
	return rep, nil
}
