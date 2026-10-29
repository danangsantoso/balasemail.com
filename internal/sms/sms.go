// Package sms sends SMS through third-party gateways, including the
// Email-to-SMS bridge where mail to <number>@sms.<domain> becomes an SMS (PRD F11).
package sms

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ErrInvalidNumber is returned for numbers that cannot be normalized to E.164.
var ErrInvalidNumber = errors.New("invalid phone number")

// Normalize converts local Indonesian formats (08xx, 8xx, 628xx, +62 8xx) and
// other international numbers to E.164 ("+628123456789").
func Normalize(raw string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", ErrInvalidNumber
		}
	}
	d := b.String()
	switch {
	case strings.HasPrefix(d, "0"):
		d = "62" + strings.TrimLeft(d, "0")
	case strings.HasPrefix(d, "8") && len(d) >= 9 && len(d) <= 13:
		d = "62" + d
	}
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return "", ErrInvalidNumber
	}
	if strings.HasPrefix(d, "62") && (len(d) < 10 || d[2] != '8') {
		return "", ErrInvalidNumber // Indonesian mobiles are +62 8xx
	}
	return "+" + d, nil
}

// ParseAddress extracts the phone number from "<number>@<smsDomain>".
func ParseAddress(rcpt, smsDomain string) (phone string, isSMS bool, err error) {
	at := strings.LastIndexByte(rcpt, '@')
	if at < 0 || !strings.EqualFold(rcpt[at+1:], smsDomain) {
		return "", false, nil
	}
	phone, err = Normalize(rcpt[:at])
	return phone, true, err
}

// gsm7 holds the GSM 03.38 basic character set; anything else forces UCS-2.
const gsm7 = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
const gsm7ext = "^{}\\[~]|€"

// Segments returns the encoding and number of SMS parts the body needs.
func Segments(body string) (encoding string, parts int) {
	septets := 0
	for _, r := range body {
		switch {
		case strings.ContainsRune(gsm7, r):
			septets++
		case strings.ContainsRune(gsm7ext, r):
			septets += 2
		default:
			n := utf16Len(body)
			if n <= 70 {
				return "UCS-2", 1
			}
			return "UCS-2", (n + 66) / 67
		}
	}
	if septets <= 160 {
		return "GSM-7", 1
	}
	return "GSM-7", (septets + 152) / 153
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// BodyFromEmail derives SMS text from an email: plain body without the
// signature, falling back to the subject.
func BodyFromEmail(subject, text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if i := strings.Index(text, "\n-- \n"); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = strings.TrimSpace(subject)
	}
	if utf8.RuneCountInString(text) > 1530 {
		text = string([]rune(text)[:1530])
	}
	return text
}

// Message is one SMS to send.
type Message struct {
	To       string
	SenderID string
	Body     string
}

// Provider is an SMS gateway (Twilio, Zenziva, Vonage, ...).
type Provider interface {
	Name() string
	Send(ctx context.Context, m Message) (providerID string, err error)
}

// PermanentError marks failures another provider cannot fix (bad number, content rejected).
type PermanentError struct{ Err error }

func (p *PermanentError) Error() string { return p.Err.Error() }
func (p *PermanentError) Unwrap() error { return p.Err }

func isPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// Router fails over between providers with a simple circuit breaker.
type Router struct {
	providers []Provider
	mu        sync.Mutex
	openUntil map[string]time.Time
	failures  map[string]int
}

func NewRouter(ps ...Provider) *Router {
	return &Router{providers: ps, openUntil: map[string]time.Time{}, failures: map[string]int{}}
}

func (r *Router) Send(ctx context.Context, m Message) (provider, id string, err error) {
	if len(r.providers) == 0 {
		return "", "", errors.New("no sms providers configured")
	}
	for i, p := range r.providers {
		r.mu.Lock()
		open := time.Now().Before(r.openUntil[p.Name()])
		r.mu.Unlock()
		if open && i < len(r.providers)-1 {
			continue
		}
		id, err = p.Send(ctx, m)
		r.mu.Lock()
		if err == nil {
			r.failures[p.Name()] = 0
		} else if !isPermanent(err) {
			r.failures[p.Name()]++
			if r.failures[p.Name()] >= 3 {
				r.openUntil[p.Name()] = time.Now().Add(time.Minute)
				r.failures[p.Name()] = 0
			}
		}
		r.mu.Unlock()
		if err == nil || isPermanent(err) {
			return p.Name(), id, err
		}
		slog.Warn("sms provider failed, failing over", "provider", p.Name(), "err", err)
	}
	return "", "", err
}

// LogProvider prints messages instead of sending (development).
type LogProvider struct{}

func (LogProvider) Name() string { return "log" }
func (LogProvider) Send(_ context.Context, m Message) (string, error) {
	slog.Info("sms (log provider)", "to", m.To, "sender", m.SenderID, "body", m.Body)
	return "log-" + time.Now().Format("150405.000"), nil
}
