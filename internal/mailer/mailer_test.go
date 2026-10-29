package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/mail"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

func sample() Message {
	return Message{
		From:               mail.Address{Name: "PT Maju", Address: "sales@maju.co.id"},
		To:                 mail.Address{Address: "budi@acme.id"},
		Subject:            "Penawaran kerja sama ✓",
		HTML:               "<p>Halo <b>Budi</b></p>",
		MessageID:          "abc@maju.co.id",
		ListUnsubscribeURL: "https://app.balasemail.com/u/tok",
		Headers:            map[string]string{"X-Campaign": "c1", "Bcc": "evil@x.com", "X-Inject": "a\r\nBcc: evil@x.com"},
	}
}

func TestMessageBytes(t *testing.T) {
	raw, err := sample().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
		t.Error("missing one-click unsubscribe header")
	}
	if !strings.Contains(m.Header.Get("List-Unsubscribe"), "https://app.balasemail.com/u/tok") {
		t.Error("missing List-Unsubscribe")
	}
	if m.Header.Get("Bcc") != "" {
		t.Error("reserved/injected headers must not be emitted")
	}
	if m.Header.Get("Message-Id") != "<abc@maju.co.id>" {
		t.Errorf("message-id = %q", m.Header.Get("Message-Id"))
	}
	dec := new(headerDecoder)
	if s, _ := dec.decode(m.Header.Get("Subject")); s != "Penawaran kerja sama ✓" {
		t.Errorf("subject = %q", s)
	}
	if !strings.Contains(string(raw), "text/plain") || !strings.Contains(string(raw), "text/html") {
		t.Error("expected both alternatives")
	}
}

func TestSignDKIMVerifies(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	raw, _ := sample().Bytes()
	signed, err := SignDKIM(raw, "maju.co.id", "bm1", key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(signed, []byte("DKIM-Signature:")) {
		t.Fatal("signature header missing")
	}
	// Verify with a lookup that returns our public key.
	pub := key.Public().(*rsa.PublicKey)
	verifications, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(string) ([]string, error) { return []string{dkimRecord(pub)}, nil },
	})
	if err != nil || len(verifications) != 1 || verifications[0].Err != nil {
		t.Fatalf("dkim verify failed: %v %+v", err, verifications)
	}
}

func TestHTMLToText(t *testing.T) {
	if got := HTMLToText("<p>Halo&nbsp;Budi</p><p>Salam</p>"); got != "Halo Budi\n\nSalam" {
		t.Errorf("got %q", got)
	}
}

type fakeSender struct {
	name  string
	err   error
	calls int
}

func (f *fakeSender) Name() string { return f.name }
func (f *fakeSender) Send(context.Context, Envelope, []byte) error {
	f.calls++
	return f.err
}

func TestRouterFailover(t *testing.T) {
	primary := &fakeSender{name: "ses", err: &SendError{Stage: StageConnect, Err: errors.New("down")}}
	backup := &fakeSender{name: "sendgrid"}
	r := NewRouter(primary, backup)
	for range 5 {
		name, err := r.Send(context.Background(), Envelope{}, nil)
		if err != nil || name != "sendgrid" {
			t.Fatalf("got %s, %v", name, err)
		}
	}
	if primary.calls != 3 {
		t.Errorf("circuit should open after 3 failures, primary called %d times", primary.calls)
	}
}

func TestRouterNoFailoverOnHardBounce(t *testing.T) {
	primary := &fakeSender{name: "ses", err: &SendError{Stage: StageRecipient, Code: 550, Err: errors.New("no such user")}}
	backup := &fakeSender{name: "sendgrid"}
	_, err := NewRouter(primary, backup).Send(context.Background(), Envelope{}, nil)
	if !IsHardBounce(err) || backup.calls != 0 {
		t.Fatalf("hard bounce must not fail over: err=%v backup=%d", err, backup.calls)
	}
}
