// Package inbound processes received mail: reply detection (stopping
// sequences), classification, auto-reply (PRD F9) and auto-forward (PRD F10).
package inbound

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/mailer"
)

// Email is the parsed view of an inbound message.
type Email struct {
	Rcpt      string
	From      string
	FromName  string
	Subject   string
	Text      string
	MessageID string
	InReplyTo []string // In-Reply-To + References, most recent first
	Header    mail.Header
	Raw       []byte
}

var wordDecoder = new(mime.WordDecoder)

// Parse reads an RFC 5322 message and extracts the best plain-text body.
func Parse(rcpt string, raw []byte) (Email, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Email{}, err
	}
	e := Email{Rcpt: strings.ToLower(rcpt), Header: m.Header, Raw: raw}
	if from, err := mail.ParseAddress(m.Header.Get("From")); err == nil {
		e.From, e.FromName = strings.ToLower(from.Address), from.Name
	}
	if s, err := wordDecoder.DecodeHeader(m.Header.Get("Subject")); err == nil {
		e.Subject = s
	} else {
		e.Subject = m.Header.Get("Subject")
	}
	e.MessageID = strings.Trim(m.Header.Get("Message-Id"), "<> ")
	e.InReplyTo = messageIDs(m.Header.Get("In-Reply-To"))
	refs := messageIDs(m.Header.Get("References"))
	for i := len(refs) - 1; i >= 0; i-- {
		e.InReplyTo = append(e.InReplyTo, refs[i])
	}
	body, _ := io.ReadAll(io.LimitReader(m.Body, 10<<20))
	e.Text = extractText(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), body, 0)
	return e, nil
}

var msgIDRe = regexp.MustCompile(`<([^<>]+)>`)

func messageIDs(h string) []string {
	var out []string
	for _, m := range msgIDRe.FindAllStringSubmatch(h, -1) {
		out = append(out, m[1])
	}
	return out
}

func decodeBody(cte string, body []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		if b, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body))); err == nil {
			return b
		}
	case "base64":
		if b, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(bytes.ReplaceAll(body, []byte("\r\n"), nil)))); err == nil {
			return b
		}
	}
	return body
}

// extractText walks MIME parts preferring text/plain over text/html.
func extractText(ctype, cte string, body []byte, depth int) string {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || ctype == "" {
		mt = "text/plain"
	}
	switch {
	case strings.HasPrefix(mt, "multipart/") && depth < 5:
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		var htmlFallback string
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			pb, _ := io.ReadAll(io.LimitReader(p, 5<<20))
			t := extractText(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), pb, depth+1)
			pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if t != "" && pt != "text/html" {
				return t
			}
			if htmlFallback == "" {
				htmlFallback = t
			}
		}
		return htmlFallback
	case mt == "text/plain":
		return strings.TrimSpace(strings.ReplaceAll(string(decodeBody(cte, body)), "\r\n", "\n"))
	case mt == "text/html":
		return mailer.HTMLToText(string(decodeBody(cte, body)))
	}
	return ""
}

var quoteHeaderRe = regexp.MustCompile(`(?im)^(on .+ wrote:|pada .+ menulis:|-{2,}\s*original message\s*-{2,}|-{2,}\s*pesan asli\s*-{2,}|from:\s.+)$`)

// StripQuoted returns only the new part of a reply (drops quoted history).
func StripQuoted(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if loc := quoteHeaderRe.FindStringIndex(text); loc != nil {
		text = text[:loc[0]]
	}
	var keep []string
	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), ">") {
			keep = append(keep, line)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// IsAutomated detects auto-replies, bounces and list mail (RFC 3834) so we
// never answer them — the core loop-prevention rule.
func IsAutomated(e Email) bool {
	h := e.Header
	if v := strings.ToLower(h.Get("Auto-Submitted")); v != "" && v != "no" {
		return true
	}
	switch strings.ToLower(h.Get("Precedence")) {
	case "bulk", "junk", "list", "auto_reply":
		return true
	}
	if h.Get("List-Id") != "" || h.Get("List-Unsubscribe") != "" || h.Get("X-Autoreply") != "" ||
		h.Get("X-Autorespond") != "" || h.Get("X-BalasEmail-Loop") != "" {
		return true
	}
	if strings.TrimSpace(h.Get("Return-Path")) == "<>" {
		return true
	}
	local, _, _ := strings.Cut(e.From, "@")
	if local == "" {
		return true
	}
	base, _, _ := strings.Cut(local, "+")
	return isNoReply(base)
}

func isNoReply(local string) bool {
	for _, p := range []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "mailer-daemon", "postmaster", "bounce", "bounces", "notifications", "notification"} {
		if local == p || strings.HasPrefix(local, p) {
			return true
		}
	}
	return false
}
