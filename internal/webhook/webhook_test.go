package webhook

import (
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	body := []byte(`{"type":"bounce"}`)
	now := time.Unix(1_800_000_000, 0)
	h := Sign("whsec_x", now.Unix(), body)
	if !Verify("whsec_x", h, body, 5*time.Minute, now.Add(time.Minute)) {
		t.Fatal("valid signature rejected")
	}
	if Verify("whsec_y", h, body, 5*time.Minute, now) {
		t.Fatal("wrong secret accepted")
	}
	if Verify("whsec_x", h, []byte(`{"type":"open"}`), 5*time.Minute, now) {
		t.Fatal("tampered body accepted")
	}
	if Verify("whsec_x", h, body, 5*time.Minute, now.Add(10*time.Minute)) {
		t.Fatal("replayed (stale) signature accepted")
	}
}

func TestValidate(t *testing.T) {
	s := &Service{}
	if _, err := s.validate("http://example.com/hook", nil); err == nil {
		t.Error("http must be rejected in production mode")
	}
	if _, err := s.validate("https://example.com/hook", []string{"bounce", "explode"}); err == nil {
		t.Error("unknown event must be rejected")
	}
	evs, err := s.validate("https://example.com/hook", []string{"Bounce", "bounce", "open"})
	if err != nil || len(evs) != 2 {
		t.Fatalf("got %v %v", evs, err)
	}
	s.AllowHTTP = true
	if _, err := s.validate("http://localhost:9000/hook", nil); err != nil {
		t.Error("http allowed in dev mode")
	}
}
