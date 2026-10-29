package validator

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

type fakeDNS map[string][]*net.MX

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	mx, ok := f[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return mx, nil
}

// fakeProber accepts addresses listed in exists; acceptAll simulates catch-all.
type fakeProber struct {
	exists    map[string]bool
	acceptAll bool
	tempFail  bool
	down      bool
}

func (f fakeProber) Probe(_ context.Context, _ string, rcpts []string) ([]int, error) {
	if f.down {
		return nil, errors.New("connection refused")
	}
	codes := make([]int, len(rcpts))
	for i, r := range rcpts {
		switch {
		case f.tempFail:
			codes[i] = 451
		case f.acceptAll || f.exists[r]:
			codes[i] = 250
		default:
			codes[i] = 550
		}
	}
	return codes, nil
}

var dns = fakeDNS{
	"acme.co.id":  {{Host: "mx1.acme.co.id.", Pref: 10}},
	"catchall.id": {{Host: "mx.catchall.id.", Pref: 10}},
	"nullmx.id":   {{Host: ".", Pref: 0}},
}

func TestValidate(t *testing.T) {
	prober := fakeProber{exists: map[string]bool{"budi@acme.co.id": true, "info@acme.co.id": true}}
	tests := []struct {
		name   string
		v      *Validator
		email  string
		status Status
		reason string
	}{
		{"valid mailbox", New(dns, prober), "Budi@Acme.co.id", StatusValid, "mailbox_exists"},
		{"unknown mailbox", New(dns, prober), "nobody@acme.co.id", StatusInvalid, "mailbox_not_found"},
		{"role mailbox", New(dns, prober), "info@acme.co.id", StatusRisky, "role_based"},
		{"bad syntax", New(dns, prober), "budi@@acme", StatusInvalid, "invalid_syntax"},
		{"disposable", New(dns, prober), "x@mailinator.com", StatusDisposable, "disposable_domain"},
		{"no mx", New(dns, prober), "x@nodomain.id", StatusInvalid, "no_mx_record"},
		{"null mx", New(dns, prober), "x@nullmx.id", StatusInvalid, "no_mx_record"},
		{"catch-all", New(dns, fakeProber{acceptAll: true}), "x@catchall.id", StatusRisky, "catch_all_domain"},
		{"greylisted", New(dns, fakeProber{tempFail: true}), "x@acme.co.id", StatusUnknown, "greylisted_or_temporary"},
		{"mx down", New(dns, fakeProber{down: true}), "x@acme.co.id", StatusUnknown, "smtp_unreachable"},
		{"probe disabled", New(dns, nil), "budi@acme.co.id", StatusUnknown, "smtp_probe_disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.v.Validate(context.Background(), tt.email)
			if got.Status != tt.status || got.Reason != tt.reason {
				t.Fatalf("got %s/%s, want %s/%s", got.Status, got.Reason, tt.status, tt.reason)
			}
			if got.Status == StatusValid && got.Email != strings.ToLower(tt.email) {
				t.Errorf("email not normalized: %s", got.Email)
			}
		})
	}
}

func TestCatchAllCache(t *testing.T) {
	v := New(dns, fakeProber{acceptAll: true})
	if _, known := v.IsCatchAll("catchall.id"); known {
		t.Fatal("should be unknown before probing")
	}
	v.Validate(context.Background(), "a@catchall.id")
	if val, known := v.IsCatchAll("catchall.id"); !known || !val {
		t.Fatal("catch-all should be cached as true")
	}
}
