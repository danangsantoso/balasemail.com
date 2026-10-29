package smtpgw

import (
	"context"
	"errors"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"

	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/sms"
)

type fakes struct {
	mu      sync.Mutex
	relayed []string
	smsTo   []string
	smsBody []string
	inbound []string
}

func (f *fakes) AuthenticateAPIKey(_ context.Context, key string) (authctx.Principal, error) {
	if key == "bm_good_key" {
		return authctx.Principal{WorkspaceID: "ws1"}, nil
	}
	return authctx.Principal{}, errors.New("bad key")
}

func (f *fakes) AuthorizeSenderErr(_ context.Context, _, from string) error {
	if strings.HasSuffix(from, "@maju.co.id") {
		return nil
	}
	return errors.New("domain_not_registered")
}

func (f *fakes) QueueRelay(_ context.Context, _, _, to string, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(strings.ToLower(string(raw)), "bcc:") {
		return errors.New("bcc leaked")
	}
	f.relayed = append(f.relayed, to)
	return nil
}

func (f *fakes) FromEmail(_ context.Context, _, rcpt, _, subject, text string) (sms.SMS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.smsTo = append(f.smsTo, rcpt)
	f.smsBody = append(f.smsBody, sms.BodyFromEmail(subject, text))
	return sms.SMS{}, nil
}

func (f *fakes) Accepts(_ context.Context, rcpt string) bool {
	return strings.HasSuffix(rcpt, "@maju.co.id")
}

func (f *fakes) Receive(_ context.Context, rcpt string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound = append(f.inbound, rcpt)
	return nil
}

type fakeBounces struct {
	mu   sync.Mutex
	seen []string
}

func (b *fakeBounces) Accepts(rcpt string) bool { return strings.HasSuffix(rcpt, "@bounces.test") }
func (b *fakeBounces) Process(_ context.Context, rcpt string, _ []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, rcpt)
	return nil
}

var bounces = &fakeBounces{}

func start(t *testing.T, inboundMode bool) (string, *fakes) {
	t.Helper()
	f := &fakes{}
	g := New(Config{Hostname: "mx.test", SMSDomain: "sms.test", MaxMessageBytes: 1 << 20, AllowInsecureAuth: true}, f, f, f, f, f, bounces)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := g.SubmissionServer("")
	if inboundMode {
		srv = g.InboundServer("")
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), f
}

const msg = "From: Sales <sales@maju.co.id>\r\nTo: budi@acme.id\r\nBcc: secret@x.id\r\nSubject: Halo\r\n\r\nKode Anda 1234\r\n"

func TestSubmissionRelayAndSMS(t *testing.T) {
	addr, f := start(t, false)
	auth := smtp.PlainAuth("", "apikey", "bm_good_key", "127.0.0.1")
	err := smtp.SendMail(addr, auth, "sales@maju.co.id", []string{"budi@acme.id", "08123456789@sms.test"}, []byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.relayed) != 1 || f.relayed[0] != "budi@acme.id" {
		t.Errorf("relayed = %v", f.relayed)
	}
	if len(f.smsTo) != 1 || f.smsBody[0] != "Kode Anda 1234" {
		t.Errorf("sms = %v %v", f.smsTo, f.smsBody)
	}
}

func TestSubmissionRejects(t *testing.T) {
	addr, _ := start(t, false)
	if err := smtp.SendMail(addr, nil, "sales@maju.co.id", []string{"budi@acme.id"}, []byte(msg)); err == nil {
		t.Error("unauthenticated relay must be rejected")
	}
	bad := smtp.PlainAuth("", "apikey", "bm_wrong", "127.0.0.1")
	if err := smtp.SendMail(addr, bad, "sales@maju.co.id", []string{"budi@acme.id"}, []byte(msg)); err == nil {
		t.Error("bad API key must be rejected")
	}
	good := smtp.PlainAuth("", "apikey", "bm_good_key", "127.0.0.1")
	if err := smtp.SendMail(addr, good, "x@gmail.com", []string{"budi@acme.id"}, []byte(msg)); err == nil {
		t.Error("unverified sender domain must be rejected")
	}
	spoof := strings.Replace(msg, "sales@maju.co.id", "ceo@bank.co.id", 1)
	if err := smtp.SendMail(addr, good, "sales@maju.co.id", []string{"budi@acme.id"}, []byte(spoof)); err == nil {
		t.Error("mismatched From header must be rejected")
	}
}

func TestInboundMX(t *testing.T) {
	addr, f := start(t, true)
	reply := "From: budi@acme.id\r\nTo: sales@maju.co.id\r\nSubject: Re: Halo\r\n\r\nTertarik\r\n"
	if err := smtp.SendMail(addr, nil, "budi@acme.id", []string{"sales@maju.co.id"}, []byte(reply)); err != nil {
		t.Fatal(err)
	}
	if len(f.inbound) != 1 {
		t.Errorf("inbound = %v", f.inbound)
	}
	if err := smtp.SendMail(addr, nil, "x@spam.id", []string{"victim@other.com"}, []byte(reply)); err == nil {
		t.Error("open relay: foreign recipients must be rejected")
	}
	withoutKey := "From: app@acme.id\r\nSubject: hi\r\n\r\nbody\r\n"
	if err := smtp.SendMail(addr, nil, "app@acme.id", []string{"628123456789@sms.test"}, []byte(withoutKey)); err == nil {
		t.Error("SMS bridge without key must be rejected")
	}
	withKey := "From: app@acme.id\r\nX-BalasEmail-Key: bm_good_key\r\nSubject: hi\r\n\r\nPesanan dikirim\r\n"
	if err := smtp.SendMail(addr, nil, "app@acme.id", []string{"628123456789@sms.test"}, []byte(withKey)); err != nil {
		t.Fatal(err)
	}
	if len(f.smsBody) != 1 || f.smsBody[0] != "Pesanan dikirim" {
		t.Errorf("sms = %v", f.smsBody)
	}
}

func TestInboundBounceRouting(t *testing.T) {
	addr, f := start(t, true)
	dsn := "From: MAILER-DAEMON@acme.id\r\nSubject: Undelivered\r\n\r\n550 5.1.1 no such user\r\n"
	if err := smtp.SendMail(addr, nil, "", []string{"b123@bounces.test"}, []byte(dsn)); err != nil {
		t.Fatal(err)
	}
	if len(bounces.seen) == 0 || bounces.seen[len(bounces.seen)-1] != "b123@bounces.test" || len(f.inbound) != 0 {
		t.Fatalf("bounce not routed: %v / inbound %v", bounces.seen, f.inbound)
	}
}

func TestPrepareRelay(t *testing.T) {
	raw := []byte("From: a@maju.co.id\r\nBcc: x@y.id,\r\n z@y.id\r\nSubject: s\r\n\r\nbody\r\n")
	out := string(PrepareRelay(raw, "a@maju.co.id"))
	if strings.Contains(out, "Bcc") || strings.Contains(out, "z@y.id") {
		t.Errorf("bcc not stripped: %q", out)
	}
	if !strings.Contains(out, "Message-ID: <") || !strings.Contains(out, "@maju.co.id>") || !strings.Contains(out, "Date: ") {
		t.Errorf("missing generated headers: %q", out)
	}
	if !strings.HasSuffix(out, "\r\n\r\nbody\r\n") {
		t.Errorf("body damaged: %q", out)
	}
}
