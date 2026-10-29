// Package emailaddr parses, normalizes and classifies email addresses.
package emailaddr

import (
	"errors"
	"net/mail"
	"strings"
)

// ErrInvalid is returned for syntactically invalid addresses.
var ErrInvalid = errors.New("invalid email address")

// Address is a normalized email address.
type Address struct {
	Local  string
	Domain string
}

func (a Address) String() string { return a.Local + "@" + a.Domain }

// Parse validates syntax (RFC 5322 addr-spec, practical subset) and lowercases.
// It accepts "Name <a@b.c>" and bare "a@b.c".
func Parse(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 320 {
		return Address{}, ErrInvalid
	}
	pa, err := mail.ParseAddress(s)
	if err != nil {
		return Address{}, ErrInvalid
	}
	at := strings.LastIndexByte(pa.Address, '@')
	if at <= 0 || at == len(pa.Address)-1 {
		return Address{}, ErrInvalid
	}
	local, domain := pa.Address[:at], strings.ToLower(strings.TrimSuffix(pa.Address[at+1:], "."))
	if len(local) > 64 || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return Address{}, ErrInvalid
	}
	if !validDomain(domain) {
		return Address{}, ErrInvalid
	}
	return Address{Local: strings.ToLower(local), Domain: domain}, nil
}

// Normalize returns the lowercase canonical form or an error.
func Normalize(s string) (string, error) {
	a, err := Parse(s)
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

func validDomain(d string) bool {
	if len(d) < 3 || len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for label := range strings.SplitSeq(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	tld := d[strings.LastIndexByte(d, '.')+1:]
	return len(tld) >= 2 && strings.Trim(tld, "0123456789") != ""
}

// ValidDomain reports whether d looks like a registrable hostname.
func ValidDomain(d string) bool { return validDomain(strings.ToLower(strings.TrimSpace(d))) }

// IsFreeProvider reports whether domain is a consumer mailbox provider.
func IsFreeProvider(domain string) bool { _, ok := freeProviders[strings.ToLower(domain)]; return ok }

// IsDisposable reports whether domain is a known throwaway provider.
func IsDisposable(domain string) bool { _, ok := disposable[strings.ToLower(domain)]; return ok }

// IsRole reports whether the local part is a shared role mailbox.
func IsRole(local string) bool {
	base, _, _ := strings.Cut(strings.ToLower(local), "+")
	_, ok := roleLocals[base]
	return ok
}

// IsBusiness reports whether the address may be used to register/send (PRD F5).
func IsBusiness(a Address) bool { return !IsFreeProvider(a.Domain) && !IsDisposable(a.Domain) }

// IsNoReply reports addresses that must never receive automated replies.
func IsNoReply(local string) bool {
	l := strings.ToLower(local)
	for _, p := range []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "mailer-daemon", "postmaster", "bounce", "bounces"} {
		if l == p || strings.HasPrefix(l, p+"+") || strings.HasPrefix(l, p+"-") {
			return true
		}
	}
	return false
}

// SplitPlus splits "reply+tag@x" into ("reply", "tag").
func SplitPlus(local string) (base, tag string) {
	base, tag, _ = strings.Cut(local, "+")
	return base, tag
}

// DomainMatches reports whether host equals domain or is a subdomain of it.
func DomainMatches(host, domain string) bool {
	host, domain = strings.ToLower(host), strings.ToLower(domain)
	return host == domain || strings.HasSuffix(host, "."+domain)
}
