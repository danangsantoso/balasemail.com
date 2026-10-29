// Package bounce parses asynchronous delivery failures (RFC 3464 DSN) and
// spam complaints (RFC 5965 ARF feedback loops) that arrive at our VERP
// return-path addresses, and maps them back to the original message.
package bounce

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

type Kind string

const (
	KindHard      Kind = "hard"      // permanent failure: suppress the address
	KindSoft      Kind = "soft"      // transient: record only
	KindComplaint Kind = "complaint" // recipient marked as spam
	KindUnknown   Kind = "unknown"   // not a bounce (e.g. a human replied to the return-path)
)

// Report is the normalized result of parsing a bounce or complaint.
type Report struct {
	Kind              Kind
	Recipient         string
	Status            string // enhanced status code, e.g. 5.1.1
	Diagnostic        string
	FeedbackType      string // ARF: abuse, fraud, ...
	OriginalMessageID string // from the embedded original headers
}

// ---- VERP return-path tokens ----

// VERP encodes a message id into a return-path local part:
// "b" + 32 hex (uuid) + 16 hex (truncated HMAC) — 49 chars, under the
// 64-char local-part limit and case-insensitive.
type VERP struct {
	Signer *security.Signer
	Domain string
}

func (v VERP) tag(id32 string) string { return hex.EncodeToString(v.Signer.MAC("b:" + id32)[:8]) }

// Address returns the return-path for msgID (a UUID).
func (v VERP) Address(msgID string) string {
	id32 := strings.ReplaceAll(msgID, "-", "")
	return "b" + id32 + v.tag(id32) + "@" + v.Domain
}

// Parse returns the message id encoded in a VERP address, if authentic.
func (v VERP) Parse(addr string) (string, bool) {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || !strings.EqualFold(addr[at+1:], v.Domain) {
		return "", false
	}
	local := strings.ToLower(addr[:at])
	if len(local) != 49 || local[0] != 'b' {
		return "", false
	}
	id32, tag := local[1:33], local[33:]
	if !hmac.Equal([]byte(tag), []byte(v.tag(id32))) {
		return "", false
	}
	return id32[0:8] + "-" + id32[8:12] + "-" + id32[12:16] + "-" + id32[16:20] + "-" + id32[20:], true
}

// ---- parsing ----

var (
	statusRe   = regexp.MustCompile(`\b([245])\.(\d{1,3})\.(\d{1,3})\b`)
	smtpCodeRe = regexp.MustCompile(`\b(5[0-5]\d|4[0-5]\d)\b`)
	msgIDRe    = regexp.MustCompile(`(?im)^message-id:\s*<?([^>\s]+)>?`)
)

// Parse inspects a raw message received at a bounce/FBL address.
func Parse(raw []byte) Report {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Report{Kind: KindUnknown}
	}
	body, _ := io.ReadAll(io.LimitReader(m.Body, 5<<20))
	mt, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if mt == "multipart/report" {
		switch strings.ToLower(params["report-type"]) {
		case "delivery-status":
			return parseDSN(body, params["boundary"])
		case "feedback-report":
			return parseARF(body, params["boundary"])
		}
	}
	return heuristic(m.Header, body)
}

func parts(body []byte, boundary string) map[string][]byte {
	out := map[string][]byte{}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return out
		}
		ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		b, _ := io.ReadAll(io.LimitReader(p, 2<<20))
		if _, exists := out[ct]; !exists {
			out[ct] = b
		}
	}
}

// readFields parses one or more header-style blocks separated by blank lines.
func readFields(b []byte) []textproto.MIMEHeader {
	var out []textproto.MIMEHeader
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimSpace(b), '\n', '\n'))))
	for {
		h, err := r.ReadMIMEHeader()
		if len(h) > 0 {
			out = append(out, h)
		}
		if err != nil {
			return out
		}
	}
}

func originalMessageID(ps map[string][]byte) string {
	for _, ct := range []string{"text/rfc822-headers", "message/rfc822", "message/rfc822-headers"} {
		if b, ok := ps[ct]; ok {
			if m := msgIDRe.FindSubmatch(b); m != nil {
				return string(m[1])
			}
		}
	}
	return ""
}

func stripType(v string) string {
	if _, after, ok := strings.Cut(v, ";"); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(v)
}

func parseDSN(body []byte, boundary string) Report {
	ps := parts(body, boundary)
	rep := Report{Kind: KindUnknown, OriginalMessageID: originalMessageID(ps)}
	status, ok := ps["message/delivery-status"]
	if !ok {
		status = ps["message/global-delivery-status"]
	}
	for _, f := range readFields(status) {
		action := strings.ToLower(f.Get("Action"))
		if action == "" {
			continue // per-message block
		}
		rep.Recipient = strings.ToLower(stripType(f.Get("Final-Recipient")))
		rep.Status = strings.TrimSpace(f.Get("Status"))
		rep.Diagnostic = stripType(f.Get("Diagnostic-Code"))
		switch {
		case action == "failed" && strings.HasPrefix(rep.Status, "5"):
			rep.Kind = KindHard
		case action == "failed" || action == "delayed":
			rep.Kind = KindSoft
		}
		if rep.Kind == KindHard {
			break
		}
	}
	return rep
}

func parseARF(body []byte, boundary string) Report {
	ps := parts(body, boundary)
	rep := Report{Kind: KindComplaint, OriginalMessageID: originalMessageID(ps)}
	for _, f := range readFields(ps["message/feedback-report"]) {
		if v := f.Get("Feedback-Type"); v != "" {
			rep.FeedbackType = strings.ToLower(v)
		}
		if v := f.Get("Original-Rcpt-To"); v != "" {
			rep.Recipient = strings.ToLower(strings.Trim(v, "<> "))
		}
	}
	if rep.FeedbackType == "not-spam" {
		rep.Kind = KindUnknown
	}
	return rep
}

// heuristic handles legacy MTAs that send plain-text bounces.
func heuristic(h mail.Header, body []byte) Report {
	subj := strings.ToLower(h.Get("Subject"))
	from := strings.ToLower(h.Get("From"))
	bouncy := strings.Contains(from, "mailer-daemon") || strings.Contains(from, "postmaster") ||
		strings.Contains(subj, "undeliver") || strings.Contains(subj, "delivery status notification") ||
		strings.Contains(subj, "delivery failure") || strings.Contains(subj, "returned mail") ||
		strings.Contains(subj, "gagal terkirim")
	if !bouncy {
		return Report{Kind: KindUnknown}
	}
	rep := Report{Kind: KindSoft, OriginalMessageID: ""}
	text := string(body)
	if m := msgIDRe.FindStringSubmatch(text); m != nil {
		rep.OriginalMessageID = m[1]
	}
	if m := statusRe.FindStringSubmatch(text); m != nil {
		rep.Status = m[0]
		if m[1] == "5" {
			rep.Kind = KindHard
		}
	} else if m := smtpCodeRe.FindString(text); strings.HasPrefix(m, "5") {
		rep.Status, rep.Kind = m, KindHard
	}
	return rep
}
