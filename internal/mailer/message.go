// Package mailer builds RFC 5322 messages, signs them with DKIM and hands
// them to upstream relays with failover (PRD F6).
package mailer

import (
	"bytes"
	"crypto"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

// Message is a fully rendered outbound email.
type Message struct {
	From      mail.Address
	To        mail.Address
	ReplyTo   string
	Subject   string
	HTML      string
	Text      string
	MessageID string // without angle brackets
	InReplyTo string
	Headers   map[string]string

	// One-click unsubscribe (RFC 8058) — mandatory for marketing mail.
	ListUnsubscribeURL    string
	ListUnsubscribeMailto string
}

func sanitize(v string) string { return strings.NewReplacer("\r", "", "\n", " ").Replace(v) }

// Bytes renders the message as multipart/alternative MIME.
func (m Message) Bytes() ([]byte, error) {
	if m.From.Address == "" || m.To.Address == "" {
		return nil, fmt.Errorf("from and to are required")
	}
	var buf bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, sanitize(v)) }
	h("From", m.From.String())
	h("To", m.To.String())
	if m.ReplyTo != "" {
		h("Reply-To", m.ReplyTo)
	}
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+m.MessageID+">")
	if m.InReplyTo != "" {
		h("In-Reply-To", "<"+strings.Trim(m.InReplyTo, "<>")+">")
		h("References", "<"+strings.Trim(m.InReplyTo, "<>")+">")
	}
	if m.ListUnsubscribeURL != "" || m.ListUnsubscribeMailto != "" {
		var parts []string
		if m.ListUnsubscribeMailto != "" {
			parts = append(parts, "<mailto:"+m.ListUnsubscribeMailto+">")
		}
		if m.ListUnsubscribeURL != "" {
			parts = append(parts, "<"+m.ListUnsubscribeURL+">")
			h("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
		}
		h("List-Unsubscribe", strings.Join(parts, ", "))
	}
	keys := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if isReserved(k) {
			continue
		}
		h(textproto.CanonicalMIMEHeaderKey(sanitize(k)), m.Headers[k])
	}
	h("MIME-Version", "1.0")

	mw := multipart.NewWriter(&buf)
	h("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	buf.WriteString("\r\n")
	text := m.Text
	if text == "" {
		text = HTMLToText(m.HTML)
	}
	if err := writePart(mw, "text/plain; charset=utf-8", text); err != nil {
		return nil, err
	}
	if m.HTML != "" {
		if err := writePart(mw, "text/html; charset=utf-8", m.HTML); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func isReserved(k string) bool {
	switch strings.ToLower(k) {
	case "from", "to", "subject", "date", "message-id", "mime-version", "content-type", "reply-to",
		"list-unsubscribe", "list-unsubscribe-post", "dkim-signature", "bcc", "cc", "in-reply-to", "references":
		return true
	}
	return false
}

func writePart(mw *multipart.Writer, ctype, body string) error {
	pw, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {ctype},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(pw)
	if _, err := qp.Write([]byte(body)); err != nil {
		return err
	}
	return qp.Close()
}

// HTMLToText is a minimal fallback converter for the text/plain part.
func HTMLToText(h string) string {
	r := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n", "</div>", "\n", "</li>", "\n", "</h1>", "\n\n", "</h2>", "\n\n", "</tr>", "\n")
	s := r.Replace(h)
	var out strings.Builder
	in := false
	for _, c := range s {
		switch {
		case c == '<':
			in = true
		case c == '>':
			in = false
		case !in:
			out.WriteRune(c)
		}
	}
	return strings.TrimSpace(htmlUnescape(out.String()))
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
}

// SignDKIM prepends a DKIM-Signature header (relaxed/relaxed, rsa-sha256).
func SignDKIM(raw []byte, domain, selector string, key crypto.Signer) ([]byte, error) {
	var out bytes.Buffer
	err := dkim.Sign(&out, bytes.NewReader(raw), &dkim.SignOptions{
		Domain:                 domain,
		Selector:               selector,
		Signer:                 key,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             []string{"From", "To", "Subject", "Date", "Message-ID", "Reply-To", "List-Unsubscribe", "List-Unsubscribe-Post", "MIME-Version", "Content-Type"},
	})
	return out.Bytes(), err
}
