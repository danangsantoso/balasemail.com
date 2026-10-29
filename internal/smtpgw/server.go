// Package smtpgw is the SMTP Gateway (PRD F6): an authenticated submission
// relay for customer applications and an inbound MX that receives replies
// for customer domains and bridges <number>@sms.<domain> mail to SMS (PRD F11).
package smtpgw

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/inbound"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
	"github.com/danangsantoso/balasemail.com/internal/sms"
)

// Dependencies are expressed as small interfaces so the gateway is testable
// without a database.
type (
	APIKeyAuth interface {
		AuthenticateAPIKey(ctx context.Context, key string) (authctx.Principal, error)
	}
	SenderAuth interface {
		AuthorizeSenderErr(ctx context.Context, wsID, from string) error
	}
	Relay interface {
		QueueRelay(ctx context.Context, wsID, from, to string, raw []byte) error
	}
	SMSBridge interface {
		FromEmail(ctx context.Context, wsID, rcpt, smsDomain, subject, text string) (sms.SMS, error)
	}
	Inbound interface {
		Accepts(ctx context.Context, rcpt string) bool
		Receive(ctx context.Context, rcpt string, raw []byte) error
	}
	// Bounces handles DSN/ARF mail sent to VERP return-paths and FBL mailboxes.
	Bounces interface {
		Accepts(rcpt string) bool
		Process(ctx context.Context, rcpt string, raw []byte) error
	}
)

type Config struct {
	Hostname          string
	SMSDomain         string
	MaxMessageBytes   int64
	TLS               *tls.Config
	AllowInsecureAuth bool // development only
}

type Gateway struct {
	cfg     Config
	keys    APIKeyAuth
	senders SenderAuth
	relay   Relay
	sms     SMSBridge
	inbound Inbound
	bounces Bounces
}

func New(cfg Config, keys APIKeyAuth, senders SenderAuth, relay Relay, s SMSBridge, in Inbound, b Bounces) *Gateway {
	return &Gateway{cfg: cfg, keys: keys, senders: senders, relay: relay, sms: s, inbound: in, bounces: b}
}

func (g *Gateway) newServer(addr string, be smtp.Backend) *smtp.Server {
	s := smtp.NewServer(be)
	s.Addr = addr
	s.Domain = g.cfg.Hostname
	s.MaxMessageBytes = g.cfg.MaxMessageBytes
	s.MaxRecipients = 50
	s.ReadTimeout = 60 * time.Second
	s.WriteTimeout = 60 * time.Second
	s.TLSConfig = g.cfg.TLS
	s.AllowInsecureAuth = g.cfg.AllowInsecureAuth
	return s
}

// SubmissionServer is the authenticated relay (port 587).
func (g *Gateway) SubmissionServer(addr string) *smtp.Server {
	return g.newServer(addr, smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &submissionSession{g: g}, nil
	}))
}

// InboundServer is the MX for customer domains and the SMS bridge (port 25).
func (g *Gateway) InboundServer(addr string) *smtp.Server {
	return g.newServer(addr, smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &inboundSession{g: g}, nil
	}))
}

func smtpErr(code int, ec smtp.EnhancedCode, msg string) *smtp.SMTPError {
	return &smtp.SMTPError{Code: code, EnhancedCode: ec, Message: msg}
}

var (
	errRelayDenied = smtpErr(550, smtp.EnhancedCode{5, 7, 1}, "Relaying denied")
	errBadAddress  = smtpErr(553, smtp.EnhancedCode{5, 1, 3}, "Invalid address")
	errTemp        = smtpErr(451, smtp.EnhancedCode{4, 3, 0}, "Temporary failure, try again later")
)

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// ---- submission ----

type submissionSession struct {
	g         *Gateway
	principal *authctx.Principal
	from      string
	rcpts     []string
	smsRcpts  []string
}

func (s *submissionSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *submissionSession) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		ctx, cancel := opCtx()
		defer cancel()
		p, err := s.g.keys.AuthenticateAPIKey(ctx, password)
		if err != nil {
			return smtp.ErrAuthFailed
		}
		s.principal = &p
		return nil
	}), nil
}

func (s *submissionSession) Mail(from string, _ *smtp.MailOptions) error {
	if s.principal == nil {
		return smtp.ErrAuthRequired
	}
	addr, err := emailaddr.Normalize(from)
	if err != nil {
		return errBadAddress
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := s.g.senders.AuthorizeSenderErr(ctx, s.principal.WorkspaceID, addr); err != nil {
		return smtpErr(550, smtp.EnhancedCode{5, 7, 1}, "Sender not allowed: "+err.Error())
	}
	s.from = addr
	return nil
}

func (s *submissionSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	addr, err := emailaddr.Normalize(to)
	if err != nil {
		return errBadAddress
	}
	if _, isSMS, err := sms.ParseAddress(addr, s.g.cfg.SMSDomain); isSMS {
		if err != nil {
			return smtpErr(553, smtp.EnhancedCode{5, 1, 3}, "Invalid phone number in SMS address")
		}
		s.smsRcpts = append(s.smsRcpts, addr)
		return nil
	}
	s.rcpts = append(s.rcpts, addr)
	return nil
}

func (s *submissionSession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return smtpErr(554, smtp.EnhancedCode{5, 6, 0}, "Malformed message")
	}
	// DMARC alignment: the visible From must be on the authenticated sender's domain.
	hdrFrom, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil || !sameDomain(hdrFrom.Address, s.from) {
		return smtpErr(550, smtp.EnhancedCode{5, 7, 1}, "From header must use the same domain as MAIL FROM")
	}
	ctx, cancel := opCtx()
	defer cancel()

	if len(s.smsRcpts) > 0 {
		parsed, _ := inbound.Parse("", raw)
		for _, rc := range s.smsRcpts {
			if _, err := s.g.sms.FromEmail(ctx, s.principal.WorkspaceID, rc, s.g.cfg.SMSDomain, parsed.Subject, parsed.Text); err != nil {
				return smtpErr(550, smtp.EnhancedCode{5, 7, 0}, "SMS rejected: "+err.Error())
			}
		}
	}
	if len(s.rcpts) > 0 {
		clean := PrepareRelay(raw, s.from)
		for _, rc := range s.rcpts {
			if err := s.g.relay.QueueRelay(ctx, s.principal.WorkspaceID, s.from, rc, clean); err != nil {
				slog.Error("relay queue failed", "err", err)
				return errTemp
			}
		}
	}
	return nil
}

func (s *submissionSession) Reset() {
	s.from, s.rcpts, s.smsRcpts = "", nil, nil
}

func (s *submissionSession) Logout() error { return nil }

func sameDomain(a, b string) bool {
	ai, bi := strings.LastIndexByte(a, '@'), strings.LastIndexByte(b, '@')
	return ai > 0 && bi > 0 && strings.EqualFold(a[ai+1:], b[bi+1:])
}

// PrepareRelay removes Bcc headers (must never be transmitted) and adds
// Message-ID/Date when the client omitted them.
func PrepareRelay(raw []byte, from string) []byte {
	sep := []byte("\r\n\r\n")
	idx := bytes.Index(raw, sep)
	if idx < 0 {
		sep = []byte("\n\n")
		idx = bytes.Index(raw, sep)
	}
	if idx < 0 {
		return raw
	}
	header, body := raw[:idx], raw[idx:]
	var out bytes.Buffer
	skipping := false
	hasMsgID, hasDate := false, false
	for _, line := range bytes.SplitAfter(header, []byte("\n")) {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if !skipping {
				out.Write(line)
			}
			continue
		}
		lower := strings.ToLower(string(line))
		skipping = strings.HasPrefix(lower, "bcc:")
		hasMsgID = hasMsgID || strings.HasPrefix(lower, "message-id:")
		hasDate = hasDate || strings.HasPrefix(lower, "date:")
		if !skipping {
			out.Write(line)
		}
	}
	h := out.Bytes()
	if len(h) > 0 && h[len(h)-1] != '\n' {
		out.WriteString("\r\n")
	}
	if !hasMsgID {
		domain := from[strings.LastIndexByte(from, '@')+1:]
		_, _ = fmt.Fprintf(&out, "Message-ID: <%s@%s>\r\n", security.RandomHex(12), domain)
	}
	if !hasDate {
		_, _ = fmt.Fprintf(&out, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	}
	res := bytes.TrimRight(out.Bytes(), "\r\n")
	return append(res, body...)
}

// ---- inbound MX ----

type inboundSession struct {
	g           *Gateway
	rcpts       []string
	smsRcpts    []string
	bounceRcpts []string
}

func (s *inboundSession) Mail(string, *smtp.MailOptions) error { return nil }

func (s *inboundSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	addr, err := emailaddr.Normalize(to)
	if err != nil {
		return errBadAddress
	}
	if _, isSMS, err := sms.ParseAddress(addr, s.g.cfg.SMSDomain); isSMS {
		if err != nil {
			return errBadAddress
		}
		s.smsRcpts = append(s.smsRcpts, addr)
		return nil
	}
	if s.g.bounces.Accepts(addr) {
		s.bounceRcpts = append(s.bounceRcpts, addr)
		return nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	if !s.g.inbound.Accepts(ctx, addr) {
		return errRelayDenied
	}
	s.rcpts = append(s.rcpts, addr)
	return nil
}

// SMSKeyHeader authorizes Email-to-SMS from external mail systems (PRD F11).
const SMSKeyHeader = "X-BalasEmail-Key"

func (s *inboundSession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if len(s.smsRcpts) > 0 {
		parsed, err := inbound.Parse("", raw)
		if err != nil {
			return smtpErr(554, smtp.EnhancedCode{5, 6, 0}, "Malformed message")
		}
		p, err := s.g.keys.AuthenticateAPIKey(ctx, strings.TrimSpace(parsed.Header.Get(SMSKeyHeader)))
		if err != nil {
			return smtpErr(550, smtp.EnhancedCode{5, 7, 1}, "SMS bridge requires a valid "+SMSKeyHeader+" header or SMTP AUTH on the submission port")
		}
		for _, rc := range s.smsRcpts {
			if _, err := s.g.sms.FromEmail(ctx, p.WorkspaceID, rc, s.g.cfg.SMSDomain, parsed.Subject, parsed.Text); err != nil {
				return smtpErr(550, smtp.EnhancedCode{5, 7, 0}, "SMS rejected: "+err.Error())
			}
		}
	}
	var errs []error
	for _, rc := range s.bounceRcpts {
		if err := s.g.bounces.Process(ctx, rc, raw); err != nil {
			errs = append(errs, err)
		}
	}
	for _, rc := range s.rcpts {
		if err := s.g.inbound.Receive(ctx, rc, raw); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		slog.Error("inbound processing failed", "err", errors.Join(errs...))
		return errTemp
	}
	return nil
}

func (s *inboundSession) Reset()        { s.rcpts, s.smsRcpts, s.bounceRcpts = nil, nil, nil }
func (s *inboundSession) Logout() error { return nil }
