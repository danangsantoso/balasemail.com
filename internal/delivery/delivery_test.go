package delivery

import (
	"strings"
	"testing"

	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

func TestWithUnsubscribe(t *testing.T) {
	got := withUnsubscribe(`<p>Hi</p><a href="{{unsubscribe_url}}">stop</a>`, "https://x/u/t", "PT Maju", "Jakarta", true)
	if !strings.Contains(got, `href="https://x/u/t"`) || strings.Contains(got, "Berhenti") {
		t.Errorf("placeholder should be replaced without footer: %s", got)
	}
	got = withUnsubscribe("<p>Hi</p>", "https://x/u/t", "PT <Maju>", "Jakarta", true)
	if !strings.Contains(got, "https://x/u/t") || !strings.Contains(got, "PT &lt;Maju&gt; · Jakarta") {
		t.Errorf("footer missing or unescaped: %s", got)
	}
	if got := withUnsubscribe("Hi", "https://x/u/t", "PT Maju", "", false); !strings.Contains(got, "Unsubscribe: https://x/u/t") {
		t.Errorf("text footer: %s", got)
	}
}

func TestRewriteLinksAndTokens(t *testing.T) {
	s := &Service{signer: security.NewSigner("k"), baseURL: "https://app.test"}
	unsub := s.UnsubscribeURL("m1")
	html := `<a href="https://acme.id/promo?a=1">promo</a> <a href="` + unsub + `">u</a> <a href="mailto:x@y.z">m</a>`
	out := s.rewriteLinks("m1", html, unsub)
	if strings.Contains(out, `href="https://acme.id/promo`) {
		t.Error("external link should be rewritten")
	}
	if !strings.Contains(out, unsub) || !strings.Contains(out, "mailto:x@y.z") {
		t.Error("unsubscribe and mailto links must be untouched")
	}
	tok := out[strings.Index(out, "/t/c/")+5:]
	tok = tok[:strings.IndexByte(tok, '"')]
	payload, ok := s.verify(tok, "c:")
	if !ok || payload != "m1\nhttps://acme.id/promo?a=1" {
		t.Errorf("click token payload = %q, %v", payload, ok)
	}
	if _, ok := s.verify(tok, "u:"); ok {
		t.Error("click token must not be accepted as unsubscribe token")
	}
}
