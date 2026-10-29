package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPublic(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "::1", "fe80::1", "0.0.0.0"} {
		if IsPublic(net.ParseIP(s)) {
			t.Errorf("%s must not be public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "203.0.113.10", "2606:4700::1111"} {
		if !IsPublic(net.ParseIP(s)) {
			t.Errorf("%s should be public", s)
		}
	}
}

func TestClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if _, err := Client(time.Second, false).Get(srv.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
	if _, err := Client(time.Second, true).Get(srv.URL); err != nil {
		t.Fatalf("allowPrivate client failed: %v", err)
	}
}
