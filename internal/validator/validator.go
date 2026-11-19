// Package validator checks whether an email address can receive mail
// (PRD F2): syntax, MX, disposable/role/free detection, SMTP RCPT probe and
// catch-all detection.
package validator

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

type Status string

const (
	StatusValid      Status = "valid"
	StatusInvalid    Status = "invalid"
	StatusRisky      Status = "risky" // catch-all or role mailbox: deliverable but uncertain
	StatusUnknown    Status = "unknown"
	StatusDisposable Status = "disposable"
)

type Checks struct {
	Syntax       bool   `json:"syntax"`
	MX           bool   `json:"mx"`
	MXHost       string `json:"mx_host,omitempty"`
	Disposable   bool   `json:"disposable"`
	RoleBased    bool   `json:"role_based"`
	FreeProvider bool   `json:"free_provider"`
	CatchAll     bool   `json:"catch_all"`
	SMTPChecked  bool   `json:"smtp_checked"`
	SMTPCode     int    `json:"smtp_code,omitempty"`
}

type Result struct {
	Email     string    `json:"email"`
	Status    Status    `json:"status"`
	Reason    string    `json:"reason"`
	Score     int       `json:"score"` // 0..100 deliverability confidence
	Checks    Checks    `json:"checks"`
	CheckedAt time.Time `json:"checked_at"`
}

// MXResolver is the subset of *net.Resolver used here.
type MXResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Prober performs an SMTP dialogue up to RCPT TO and returns one reply code per recipient.
type Prober interface {
	Probe(ctx context.Context, mxHost string, rcpts []string) ([]int, error)
}

type Validator struct {
	resolver MXResolver
	prober   Prober // nil disables SMTP probing

	mu       sync.Mutex
	catchAll map[string]catchAllEntry // per-domain cache to avoid hammering servers
}

type catchAllEntry struct {
	value bool
	at    time.Time
}

func New(r MXResolver, p Prober) *Validator {
	return &Validator{resolver: r, prober: p, catchAll: map[string]catchAllEntry{}}
}

// Validate runs every check and never returns an error: failures map to statuses.
func (v *Validator) Validate(ctx context.Context, email string) Result {
	res := v.validate(ctx, email)
	res.CheckedAt = time.Now().UTC()
	metrics.Validations.WithLabelValues(string(res.Status)).Inc()
	return res
}

func (v *Validator) validate(ctx context.Context, email string) Result {
	res := Result{Email: strings.TrimSpace(email)}
	addr, err := emailaddr.Parse(email)
	if err != nil {
		return finish(res, StatusInvalid, "invalid_syntax")
	}
	res.Email = addr.String()
	res.Checks.Syntax = true
	res.Checks.RoleBased = emailaddr.IsRole(addr.Local)
	res.Checks.FreeProvider = emailaddr.IsFreeProvider(addr.Domain)
	if emailaddr.IsDisposable(addr.Domain) {
		res.Checks.Disposable = true
		return finish(res, StatusDisposable, "disposable_domain")
	}

	mxs, err := v.lookupMX(ctx, addr.Domain)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return finish(res, StatusInvalid, "no_mx_record")
		}
		return finish(res, StatusUnknown, "dns_error")
	}
	if len(mxs) == 0 || (len(mxs) == 1 && mxs[0] == ".") {
		return finish(res, StatusInvalid, "no_mx_record") // RFC 7505 null MX
	}
	res.Checks.MX = true
	res.Checks.MXHost = mxs[0]

	if v.prober == nil {
		if res.Checks.RoleBased {
			return finish(res, StatusRisky, "role_based")
		}
		return finish(res, StatusUnknown, "smtp_probe_disabled")
	}

	probeAddr := "bm-" + security.RandomHex(6) + "@" + addr.Domain
	codes, err := v.probe(ctx, mxs, []string{res.Email, probeAddr})
	if err != nil {
		return finish(res, StatusUnknown, "smtp_unreachable")
	}
	res.Checks.SMTPChecked = true
	res.Checks.SMTPCode = codes[0]
	switch {
	case isReject(codes[0]):
		return finish(res, StatusInvalid, "mailbox_not_found")
	case isTemp(codes[0]):
		return finish(res, StatusUnknown, "greylisted_or_temporary")
	}
	catchAll := len(codes) > 1 && isAccept(codes[1])
	v.mu.Lock()
	v.catchAll[addr.Domain] = catchAllEntry{value: catchAll, at: time.Now()}
	v.mu.Unlock()
	if catchAll {
		res.Checks.CatchAll = true
		return finish(res, StatusRisky, "catch_all_domain")
	}
	if res.Checks.RoleBased {
		return finish(res, StatusRisky, "role_based")
	}
	return finish(res, StatusValid, "mailbox_exists")
}

// IsCatchAll reports a cached catch-all verdict for domain (used by the finder).
func (v *Validator) IsCatchAll(domain string) (value, known bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.catchAll[domain]
	if !ok || time.Since(e.at) > 24*time.Hour {
		return false, false
	}
	return e.value, true
}

func (v *Validator) lookupMX(ctx context.Context, domain string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	recs, err := v.resolver.LookupMX(ctx, domain)
	if err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Pref < recs[j].Pref })
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, strings.TrimSuffix(r.Host, "."))
	}
	if len(out) == 1 && out[0] == "" {
		out[0] = "."
	}
	return out, nil
}

func (v *Validator) probe(ctx context.Context, mxs []string, rcpts []string) ([]int, error) {
	var lastErr error
	for i, mx := range mxs {
		if i >= 2 {
			break
		}
		codes, err := v.prober.Probe(ctx, mx, rcpts)
		if err == nil {
			return codes, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func isAccept(c int) bool { return c == 250 || c == 251 }
func isReject(c int) bool { return c >= 550 && c <= 553 }
func isTemp(c int) bool   { return c >= 400 && c < 500 }

func finish(r Result, s Status, reason string) Result {
	r.Status, r.Reason = s, reason
	switch s {
	case StatusValid:
		r.Score = 95
	case StatusRisky:
		r.Score = 55
	case StatusUnknown:
		r.Score = 40
		if r.Checks.MX {
			r.Score = 50
		}
	default:
		r.Score = 0
	}
	if r.Score > 0 {
		if r.Checks.RoleBased {
			r.Score -= 10
		}
		if r.Checks.FreeProvider {
			r.Score -= 5
		}
	}
	return r
}
