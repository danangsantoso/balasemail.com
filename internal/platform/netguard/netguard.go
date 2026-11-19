// Package netguard provides an HTTP client that refuses to connect to
// private, loopback and link-local addresses. Use it for every request whose
// URL comes from a customer (webhooks) or the internet (finder crawler) to
// prevent SSRF into internal infrastructure.
package netguard

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// ErrBlocked is returned when the destination address is not public.
var ErrBlocked = errors.New("destination address is not allowed")

// IsPublic reports whether ip is a globally routable unicast address.
func IsPublic(ip net.IP) bool {
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsMulticast(), ip.IsUnspecified(), ip.IsInterfaceLocalMulticast(),
		ip.Equal(net.IPv4bcast), isCGNAT(ip):
		return false
	}
	return true
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isCGNAT(ip net.IP) bool { return cgnat.Contains(ip) }

// Client returns an HTTP client that checks the resolved IP at connect time
// (so DNS rebinding cannot bypass it). allowPrivate disables the check (tests/dev).
func Client(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !IsPublic(ip) {
				return ErrBlocked
			}
			return nil
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	tr.Proxy = nil // a proxy would hide the real destination from the check
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}
