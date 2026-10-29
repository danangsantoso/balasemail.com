// Package finder discovers B2B email addresses from public company websites
// and from naming patterns verified over SMTP (PRD F1).
package finder

import (
	"regexp"
	"slices"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
)

// Found is one discovered address with provenance (kept for audit/erasure).
type Found struct {
	Email      string `json:"email"`
	FirstName  string `json:"first_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	Title      string `json:"title,omitempty"`
	SourceURL  string `json:"source_url,omitempty"`
	Method     string `json:"method"`     // website|pattern
	Confidence int    `json:"confidence"` // 0..100
	RoleBased  bool   `json:"role_based"`
	Status     string `json:"validation_status,omitempty"`
}

var (
	emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,24}`)
	// Common obfuscations: "budi [at] acme [dot] com", "budi(at)acme.com".
	atRe  = regexp.MustCompile(`(?i)\s*[\[\(\{]\s*(at|et)\s*[\]\)\}]\s*`)
	dotRe = regexp.MustCompile(`(?i)\s*[\[\(\{]\s*(dot|titik)\s*[\]\)\}]\s*`)
	// Asset filenames that look like addresses (logo@2x.png).
	assetSuffix = []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".css", ".js"}
)

// ExtractEmails returns unique addresses on domain (or its subdomains) found in content.
func ExtractEmails(content, domain string) []string {
	text := atRe.ReplaceAllString(content, "@")
	text = dotRe.ReplaceAllString(text, ".")
	text = strings.NewReplacer("&#64;", "@", "&#x40;", "@", "%40", "@", "&#46;", ".").Replace(text)
	var out []string
	for _, m := range emailRe.FindAllString(text, -1) {
		m = strings.Trim(strings.ToLower(m), ".")
		if slices.ContainsFunc(assetSuffix, func(s string) bool { return strings.HasSuffix(m, s) }) {
			continue
		}
		addr, err := emailaddr.Parse(m)
		if err != nil || !emailaddr.DomainMatches(addr.Domain, domain) {
			continue
		}
		if !slices.Contains(out, addr.String()) {
			out = append(out, addr.String())
		}
	}
	return out
}

// Patterns are the most common corporate address formats, ordered by
// observed frequency; the first one verified wins.
var Patterns = []string{
	"{first}.{last}", "{first}", "{f}{last}", "{first}{last}", "{f}.{last}",
	"{first}_{last}", "{last}.{first}", "{first}{l}", "{last}", "{first}-{last}",
}

// Candidates expands patterns for a person at domain.
func Candidates(first, last, domain string) []string {
	first, last = slug(first), slug(last)
	if first == "" || domain == "" {
		return nil
	}
	var out []string
	for _, p := range Patterns {
		if last == "" && strings.Contains(p, "{l") {
			continue
		}
		local := strings.NewReplacer(
			"{first}", first, "{last}", last,
			"{f}", first[:1], "{l}", firstRune(last),
		).Replace(p)
		e := local + "@" + strings.ToLower(domain)
		if _, err := emailaddr.Parse(e); err == nil && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func firstRune(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}

// slug lowercases and strips everything but a-z0-9 (names like "Sri Wahyuni" → "sriwahyuni").
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GuessName infers first/last name from a personal local part ("budi.santoso").
func GuessName(local string) (first, last string) {
	if emailaddr.IsRole(local) {
		return "", ""
	}
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	if len(parts) == 0 || len(parts) > 3 {
		return "", ""
	}
	title := func(s string) string {
		if s == "" || strings.ContainsAny(s, "0123456789") {
			return ""
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
	first = title(parts[0])
	if len(parts) > 1 {
		last = title(parts[len(parts)-1])
	}
	return first, last
}
