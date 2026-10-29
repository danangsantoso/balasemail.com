package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Envelope is the SMTP envelope (MAIL FROM / RCPT TO).
type Envelope struct {
	From string
	To   []string
}

// Sender delivers a raw message.
type Sender interface {
	Name() string
	Send(ctx context.Context, env Envelope, raw []byte) error
}

// Stage tells whether a failure is the provider's fault (failover) or the recipient's (bounce).
type Stage string

const (
	StageConnect   Stage = "connect"
	StageAuth      Stage = "auth"
	StageRecipient Stage = "recipient"
	StageData      Stage = "data"
)

// SendError carries SMTP reply details for bounce classification.
type SendError struct {
	Stage Stage
	Code  int
	Err   error
}

func (e *SendError) Error() string {
	return fmt.Sprintf("smtp %s failed (code %d): %v", e.Stage, e.Code, e.Err)
}
func (e *SendError) Unwrap() error { return e.Err }

// Permanent reports a hard bounce: a 5xx answer about the recipient or content.
func (e *SendError) Permanent() bool {
	return e.Code >= 500 && (e.Stage == StageRecipient || e.Stage == StageData)
}

// IsHardBounce reports whether err is a permanent recipient failure.
func IsHardBounce(err error) bool {
	var se *SendError
	return errors.As(err, &se) && se.Permanent()
}

func codeOf(err error) int {
	var tp *textproto.Error
	if errors.As(err, &tp) {
		return tp.Code
	}
	return 0
}

// SMTPRelay sends through an authenticated upstream (SES, SendGrid, Mailgun, own MTA).
type SMTPRelay struct {
	Label    string
	Host     string
	Port     int
	Username string
	Password string
	HeloName string
	Timeout  time.Duration
}

func (r *SMTPRelay) Name() string { return r.Label }

func (r *SMTPRelay) Send(ctx context.Context, env Envelope, raw []byte) error {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
	tlsCfg := &tls.Config{ServerName: r.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	d := &net.Dialer{}
	if r.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return &SendError{Stage: StageConnect, Err: err}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, r.Host)
	if err != nil {
		conn.Close()
		return &SendError{Stage: StageConnect, Code: codeOf(err), Err: err}
	}
	defer c.Close()
	if r.HeloName != "" {
		if err := c.Hello(r.HeloName); err != nil {
			return &SendError{Stage: StageConnect, Code: codeOf(err), Err: err}
		}
	}
	if r.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return &SendError{Stage: StageConnect, Err: err}
			}
		} else if r.Username != "" {
			return &SendError{Stage: StageConnect, Err: errors.New("relay does not offer STARTTLS; refusing to send credentials")}
		}
	}
	if r.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", r.Username, r.Password, r.Host)); err != nil {
			return &SendError{Stage: StageAuth, Code: codeOf(err), Err: err}
		}
	}
	if err := c.Mail(env.From); err != nil {
		return &SendError{Stage: StageAuth, Code: codeOf(err), Err: err}
	}
	for _, to := range env.To {
		if err := c.Rcpt(to); err != nil {
			return &SendError{Stage: StageRecipient, Code: codeOf(err), Err: err}
		}
	}
	w, err := c.Data()
	if err != nil {
		return &SendError{Stage: StageData, Code: codeOf(err), Err: err}
	}
	if _, err := w.Write(raw); err != nil {
		return &SendError{Stage: StageData, Err: err}
	}
	if err := w.Close(); err != nil {
		return &SendError{Stage: StageData, Code: codeOf(err), Err: err}
	}
	return c.Quit()
}

// LogSender records messages instead of sending them (development/tests).
type LogSender struct {
	mu   sync.Mutex
	Sent []Sent
}

type Sent struct {
	Envelope Envelope
	Raw      []byte
}

func (l *LogSender) Name() string { return "log" }

func (l *LogSender) Send(_ context.Context, env Envelope, raw []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Sent) > 1000 {
		l.Sent = l.Sent[1:]
	}
	l.Sent = append(l.Sent, Sent{Envelope: env, Raw: raw})
	slog.Info("email (log sender)", "from", env.From, "to", env.To, "bytes", len(raw))
	return nil
}

// Router tries senders in order, skipping any whose circuit is open (PRD F6 failover).
type Router struct {
	senders   []Sender
	threshold int
	cooldown  time.Duration

	mu    sync.Mutex
	state map[string]*circuit
}

type circuit struct {
	failures  int
	openUntil time.Time
}

func NewRouter(senders ...Sender) *Router {
	return &Router{senders: senders, threshold: 3, cooldown: time.Minute, state: map[string]*circuit{}}
}

// Send returns the name of the provider that accepted the message.
func (r *Router) Send(ctx context.Context, env Envelope, raw []byte) (string, error) {
	if len(r.senders) == 0 {
		return "", errors.New("no senders configured")
	}
	var lastErr error
	tried := 0
	for _, s := range r.senders {
		if r.open(s.Name()) && tried < len(r.senders)-1 {
			continue
		}
		tried++
		err := s.Send(ctx, env, raw)
		if err == nil {
			r.record(s.Name(), true)
			return s.Name(), nil
		}
		if IsHardBounce(err) {
			return s.Name(), err // recipient problem: another provider won't help
		}
		r.record(s.Name(), false)
		slog.Warn("provider failed, failing over", "provider", s.Name(), "err", err)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("all providers unavailable")
	}
	return "", lastErr
}

func (r *Router) open(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.state[name]
	return c != nil && time.Now().Before(c.openUntil)
}

func (r *Router) record(name string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.state[name]
	if c == nil {
		c = &circuit{}
		r.state[name] = c
	}
	if ok {
		c.failures = 0
		return
	}
	c.failures++
	if c.failures >= r.threshold {
		c.openUntil = time.Now().Add(r.cooldown)
		c.failures = 0
	}
}

// DomainThrottle limits sends per recipient domain to protect reputation
// (e.g. bursts to gmail.com). Per process; size worker pools accordingly.
type DomainThrottle struct {
	mu        sync.Mutex
	perMinute map[string]int
	def       int
	limiters  map[string]*rate.Limiter
}

func NewDomainThrottle(defaultPerMinute int, overrides map[string]int) *DomainThrottle {
	return &DomainThrottle{perMinute: overrides, def: defaultPerMinute, limiters: map[string]*rate.Limiter{}}
}

func (t *DomainThrottle) Wait(ctx context.Context, domain string) error {
	t.mu.Lock()
	l, ok := t.limiters[domain]
	if !ok {
		n := t.def
		if v, ok := t.perMinute[domain]; ok {
			n = v
		}
		l = rate.NewLimiter(rate.Limit(float64(n)/60), max(n/10, 1))
		t.limiters[domain] = l
	}
	t.mu.Unlock()
	return l.Wait(ctx)
}
