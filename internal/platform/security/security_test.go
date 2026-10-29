package security

import (
	"strings"
	"testing"
	"time"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("rahasia-123")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "rahasia-123") || CheckPassword(h, "salah") {
		t.Fatal("password check mismatch")
	}
}

func TestJWTRoundTrip(t *testing.T) {
	j := NewJWTIssuer("test-secret-test-secret", time.Hour)
	tok, _, err := j.Issue("u1", "w1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	c, err := j.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "u1" || c.WorkspaceID != "w1" || c.Role != "owner" {
		t.Fatalf("claims = %+v", c)
	}
	if _, err := NewJWTIssuer("other-secret-other", time.Hour).Parse(tok); err == nil {
		t.Fatal("token signed with another secret must be rejected")
	}
	expired := NewJWTIssuer("test-secret-test-secret", -time.Minute)
	tok2, _, _ := expired.Issue("u1", "w1", "owner")
	if _, err := j.Parse(tok2); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestSigner(t *testing.T) {
	s := NewSigner("k")
	tok := s.Sign("msg:123")
	got, err := s.Verify(tok)
	if err != nil || got != "msg:123" {
		t.Fatalf("verify = %q, %v", got, err)
	}
	tampered := strings.Replace(tok, tok[:2], "xx", 1)
	if _, err := s.Verify(tampered); err == nil {
		t.Fatal("tampered token must fail")
	}
}

func TestCipher(t *testing.T) {
	c, err := NewCipher("0123456789abcdef0123")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Encrypt([]byte("dkim-private-key"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Decrypt(sealed)
	if err != nil || string(plain) != "dkim-private-key" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := c.Decrypt(sealed); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestCodes(t *testing.T) {
	if c := NumericCode(6); len(c) != 6 || strings.Trim(c, "0123456789") != "" {
		t.Fatalf("numeric code = %q", c)
	}
	if c := AlphanumericCode(8); len(c) != 8 {
		t.Fatalf("alnum code = %q", c)
	}
}
