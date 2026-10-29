package otp

import (
	"testing"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

func TestCheck(t *testing.T) {
	s := &Service{signer: security.NewSigner("secret")}
	now := time.Now()
	good := s.hash("otp1", "123456")
	base := record{ID: "otp1", Hash: good, MaxAttempts: 5, ExpiresAt: now.Add(time.Minute)}

	if r := check(base, s.hash("otp1", "123456"), now); !r.Valid {
		t.Fatalf("correct code rejected: %+v", r)
	}
	if r := check(base, s.hash("otp1", "000000"), now); r.Valid || r.Reason != "invalid_code" || r.AttemptsRemaining != 4 {
		t.Fatalf("wrong code: %+v", r)
	}
	if r := check(base, s.hash("otp2", "123456"), now); r.Valid {
		t.Fatal("hash must be bound to the OTP id")
	}
	expired := base
	expired.ExpiresAt = now.Add(-time.Second)
	if r := check(expired, good, now); r.Reason != "expired" {
		t.Fatalf("expired: %+v", r)
	}
	used := base
	used.VerifiedAt = &now
	if r := check(used, good, now); r.Reason != "already_used" {
		t.Fatalf("reuse: %+v", r)
	}
	locked := base
	locked.Attempts = 5
	if r := check(locked, good, now); r.Reason != "too_many_attempts" {
		t.Fatalf("lockout: %+v", r)
	}
}

func TestAlphanumericCaseInsensitive(t *testing.T) {
	s := &Service{signer: security.NewSigner("secret")}
	if string(s.hash("x", "ab12")) != string(s.hash("x", "AB12")) {
		t.Fatal("alphanumeric codes should be case-insensitive")
	}
}

func TestNormalizeInput(t *testing.T) {
	in := SendInput{To: "Budi@Acme.id", From: "no-reply@maju.co.id"}
	if err := in.normalize(); err != nil {
		t.Fatal(err)
	}
	if in.Channel != "email" || in.Length != 6 || in.TTLSeconds != 300 || in.To != "budi@acme.id" {
		t.Fatalf("defaults: %+v", in)
	}
	bad := []SendInput{
		{Channel: "email", To: "budi@acme.id"},                          // missing from
		{Channel: "sms", To: "123"},                                     // invalid phone
		{Channel: "fax", To: "x"},                                       // invalid channel
		{Channel: "email", To: "a@b.id", From: "x@y.id", Length: 12},    // length
		{Channel: "email", To: "a@b.id", From: "x@y.id", TTLSeconds: 5}, // ttl
	}
	for i, b := range bad {
		if err := b.normalize(); err == nil {
			t.Errorf("case %d should fail: %+v", i, b)
		}
	}
	sms := SendInput{Channel: "sms", To: "0812-3456-789", FallbackPhone: "08123456789", FallbackAfter: 5}
	if err := sms.normalize(); err != nil || sms.To != "+628123456789" || sms.FallbackAfter != 30 {
		t.Fatalf("sms normalize: %+v %v", sms, err)
	}
}
