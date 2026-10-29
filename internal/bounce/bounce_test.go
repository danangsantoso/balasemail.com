package bounce

import (
	"context"
	"strings"
	"testing"

	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

const dsnHard = "From: Mail Delivery Subsystem <mailer-daemon@googlemail.com>\r\n" +
	"To: b0@bounces.test\r\n" +
	"Subject: Delivery Status Notification (Failure)\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status; boundary=\"XX\"\r\n\r\n" +
	"--XX\r\nContent-Type: text/plain\r\n\r\nAddress not found.\r\n" +
	"--XX\r\nContent-Type: message/delivery-status\r\n\r\n" +
	"Reporting-MTA: dns; googlemail.com\r\nArrival-Date: Mon, 2 Nov 2026 09:00:00 +0700\r\n\r\n" +
	"Final-Recipient: rfc822; Andi@Acme.id\r\nAction: failed\r\nStatus: 5.1.1\r\n" +
	"Diagnostic-Code: smtp; 550-5.1.1 The email account that you tried to reach does not exist.\r\n" +
	"--XX\r\nContent-Type: text/rfc822-headers\r\n\r\n" +
	"From: sales@maju.co.id\r\nMessage-ID: <abc.123@maju.co.id>\r\nSubject: Halo\r\n" +
	"--XX--\r\n"

const dsnDelayed = "From: MAILER-DAEMON@mx.acme.id\r\nSubject: Delayed Mail\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status; boundary=\"B\"\r\n\r\n" +
	"--B\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.acme.id\r\n\r\n" +
	"Final-Recipient: rfc822; sari@acme.id\r\nAction: delayed\r\nStatus: 4.4.7\r\n--B--\r\n"

const arf = "From: feedback@arf.mail.yahoo.com\r\nTo: fbl@bounces.test\r\nSubject: FW: Penawaran\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=feedback-report; boundary=\"F\"\r\n\r\n" +
	"--F\r\nContent-Type: text/plain\r\n\r\nThis is an email abuse report.\r\n" +
	"--F\r\nContent-Type: message/feedback-report\r\n\r\nFeedback-Type: abuse\r\nUser-Agent: Yahoo!-Mail-Feedback/2.0\r\n" +
	"Version: 1\r\nOriginal-Rcpt-To: <budi@yahoo.com>\r\n" +
	"--F\r\nContent-Type: message/rfc822\r\n\r\nFrom: sales@maju.co.id\r\nMessage-ID: <xyz.9@maju.co.id>\r\n\r\nbody\r\n--F--\r\n"

const legacy = "From: postmaster@old-mta.id\r\nSubject: Returned mail: see transcript for details\r\n\r\n" +
	"----- Transcript -----\r\n550 User unknown\r\n"

const human = "From: someone@acme.id\r\nSubject: Re: Halo\r\n\r\nThanks!\r\n"

func TestParse(t *testing.T) {
	cases := []struct {
		name, raw string
		kind      Kind
		rcpt      string
		status    string
		origID    string
	}{
		{"gmail hard bounce", dsnHard, KindHard, "andi@acme.id", "5.1.1", "abc.123@maju.co.id"},
		{"delayed", dsnDelayed, KindSoft, "sari@acme.id", "4.4.7", ""},
		{"yahoo complaint", arf, KindComplaint, "budi@yahoo.com", "", "xyz.9@maju.co.id"},
		{"legacy text bounce", legacy, KindHard, "", "550", ""},
		{"human reply", human, KindUnknown, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse([]byte(c.raw))
			if r.Kind != c.kind || r.Recipient != c.rcpt || r.Status != c.status || r.OriginalMessageID != c.origID {
				t.Fatalf("got %+v", r)
			}
		})
	}
	if r := Parse([]byte(dsnHard)); !strings.Contains(r.Diagnostic, "does not exist") {
		t.Errorf("diagnostic = %q", r.Diagnostic)
	}
}

func TestVERP(t *testing.T) {
	v := VERP{Signer: security.NewSigner("k"), Domain: "bounces.test"}
	id := "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	addr := v.Address(id)
	local := addr[:strings.IndexByte(addr, '@')]
	if len(local) > 64 {
		t.Fatalf("local part too long: %d", len(local))
	}
	got, ok := v.Parse(strings.ToUpper(addr[:5]) + addr[5:]) // MTAs may change case
	if !ok || got != id {
		t.Fatalf("parse = %q %v", got, ok)
	}
	tampered := strings.Replace(addr, "3f25", "3f26", 1)
	if _, ok := v.Parse(tampered); ok {
		t.Fatal("forged VERP address accepted")
	}
	if _, ok := v.Parse(local + "@other.test"); ok {
		t.Fatal("wrong domain accepted")
	}
}

type fakeRec struct {
	hard, soft, complaint []string
	byHeader              map[string]string
}

func (f *fakeRec) RecordBounce(_ context.Context, id string, hard bool, _ string) error {
	if hard {
		f.hard = append(f.hard, id)
	} else {
		f.soft = append(f.soft, id)
	}
	return nil
}
func (f *fakeRec) RecordComplaint(_ context.Context, id, _ string) error {
	f.complaint = append(f.complaint, id)
	return nil
}
func (f *fakeRec) FindByMessageIDHeader(_ context.Context, h string) (string, string, bool) {
	id, ok := f.byHeader[h]
	return id, "ws", ok
}

func TestProcessor(t *testing.T) {
	v := VERP{Signer: security.NewSigner("k"), Domain: "bounces.test"}
	rec := &fakeRec{byHeader: map[string]string{"xyz.9@maju.co.id": "msg-complaint"}}
	p := &Processor{VERP: v, Rec: rec}
	id := "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

	if !p.Accepts("fbl@BOUNCES.test") || p.Accepts("fbl@maju.co.id") {
		t.Fatal("Accepts")
	}
	inputs := []struct{ rcpt, raw string }{
		{v.Address(id), dsnHard},
		{v.Address(id), dsnDelayed},
		{"fbl@bounces.test", arf},     // resolved via original Message-ID
		{"fbl@bounces.test", dsnHard}, // unattributable: dropped, not an error
	}
	for _, in := range inputs {
		if _, err := p.Process(context.Background(), in.rcpt, []byte(in.raw)); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.hard) != 1 || rec.hard[0] != id || len(rec.soft) != 1 || len(rec.complaint) != 1 || rec.complaint[0] != "msg-complaint" {
		t.Fatalf("recorded hard=%v soft=%v complaint=%v", rec.hard, rec.soft, rec.complaint)
	}
}
