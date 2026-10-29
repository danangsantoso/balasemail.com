package sms

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"08123456789":      "+628123456789",
		"8123456789":       "+628123456789",
		"628123456789":     "+628123456789",
		"+62 812-3456-789": "+628123456789",
		"(0812) 3456 789":  "+628123456789",
		"+6591234567":      "+6591234567",
	}
	for in, want := range ok {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "12", "0211234567", "62211234", "08abc", "++628123"} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) should fail", in)
		}
	}
}

func TestParseAddress(t *testing.T) {
	phone, isSMS, err := ParseAddress("08123456789@SMS.balasemail.com", "sms.balasemail.com")
	if !isSMS || err != nil || phone != "+628123456789" {
		t.Fatalf("got %q %v %v", phone, isSMS, err)
	}
	if _, isSMS, _ := ParseAddress("budi@acme.id", "sms.balasemail.com"); isSMS {
		t.Fatal("regular address is not SMS")
	}
}

func TestSegments(t *testing.T) {
	cases := []struct {
		body  string
		enc   string
		parts int
	}{
		{"Kode OTP Anda 123456", "GSM-7", 1},
		{strings.Repeat("a", 160), "GSM-7", 1},
		{strings.Repeat("a", 161), "GSM-7", 2},
		{strings.Repeat("a", 306), "GSM-7", 2},
		{strings.Repeat("€", 80), "GSM-7", 1}, // extension chars count double: 160 septets
		{"Terima kasih 🙏", "UCS-2", 1},
		{strings.Repeat("ü", 10) + strings.Repeat("中", 71), "UCS-2", 2},
	}
	for _, c := range cases {
		enc, parts := Segments(c.body)
		if enc != c.enc || parts != c.parts {
			t.Errorf("Segments(%.20q…) = %s/%d, want %s/%d", c.body, enc, parts, c.enc, c.parts)
		}
	}
}

func TestBodyFromEmail(t *testing.T) {
	if got := BodyFromEmail("Subj", "Halo, pesanan siap.\r\n-- \r\nPT Maju\r\n"); got != "Halo, pesanan siap." {
		t.Errorf("got %q", got)
	}
	if got := BodyFromEmail("Kode: 1234", "  "); got != "Kode: 1234" {
		t.Errorf("subject fallback: %q", got)
	}
}

type fakeProvider struct {
	name  string
	err   error
	calls int
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Send(context.Context, Message) (string, error) {
	f.calls++
	return "id-" + f.name, f.err
}

func TestRouterFailover(t *testing.T) {
	a := &fakeProvider{name: "twilio", err: errors.New("timeout")}
	b := &fakeProvider{name: "zenziva"}
	p, id, err := NewRouter(a, b).Send(context.Background(), Message{})
	if err != nil || p != "zenziva" || id != "id-zenziva" {
		t.Fatalf("got %s %s %v", p, id, err)
	}
	a.err = &PermanentError{errors.New("invalid number")}
	b.calls = 0
	if _, _, err := NewRouter(a, b).Send(context.Background(), Message{}); !isPermanent(err) || b.calls != 0 {
		t.Fatal("permanent errors must not fail over")
	}
}

func TestTwilioProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if u, p, _ := r.BasicAuth(); u != "AC1" || p != "tok" || r.Form.Get("To") != "+628123456789" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"sid":"SM123"}`))
	}))
	defer srv.Close()
	tw := &Twilio{AccountSID: "AC1", AuthToken: "tok", From: "BALAS", BaseURL: srv.URL}
	id, err := tw.Send(context.Background(), Message{To: "+628123456789", Body: "hi"})
	if err != nil || id != "SM123" {
		t.Fatalf("got %q %v", id, err)
	}
}
