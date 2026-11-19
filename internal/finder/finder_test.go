package finder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/danangsantoso/balasemail.com/internal/validator"
)

func TestExtractEmails(t *testing.T) {
	html := `<a href="mailto:Sales@Acme.co.id">Sales</a> budi.santoso [at] acme [dot] co [dot] id
		<img src="logo@2x.png"> other@gmail.com hr&#64;mail.acme.co.id`
	got := ExtractEmails(html, "acme.co.id")
	want := []string{"sales@acme.co.id", "budi.santoso@acme.co.id", "hr@mail.acme.co.id"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCandidates(t *testing.T) {
	c := Candidates("Budi", "Santoso", "acme.id")
	if c[0] != "budi.santoso@acme.id" || !slices.Contains(c, "bsantoso@acme.id") || !slices.Contains(c, "budi@acme.id") {
		t.Fatalf("candidates = %v", c)
	}
	if c := Candidates("Sri", "", "acme.id"); slices.Contains(c, "sri.@acme.id") || c[0] != "sri@acme.id" {
		t.Fatalf("single-name candidates = %v", c)
	}
}

func TestGuessName(t *testing.T) {
	if f, l := GuessName("budi.santoso"); f != "Budi" || l != "Santoso" {
		t.Errorf("got %s %s", f, l)
	}
	if f, _ := GuessName("info"); f != "" {
		t.Error("role mailbox should not yield a name")
	}
}

func TestRobots(t *testing.T) {
	r := parseRobots("User-agent: *\nDisallow: /private\nAllow: /private/contact\n\nUser-agent: OtherBot\nDisallow: /", "BalasEmailBot/1.0")
	if r.allowed("/private/x") || !r.allowed("/private/contact") || !r.allowed("/contact") {
		t.Fatalf("rules = %+v", r)
	}
	r = parseRobots("User-agent: BalasEmailBot\nDisallow: /\n", "BalasEmailBot/1.0")
	if r.allowed("/contact") {
		t.Fatal("specific disallow-all must block")
	}
}

func TestCrawl(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /karir\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a href="/leadership-team">Team</a> <a href="https://evil.com/contact">x</a> info@acme.id`))
	})
	mux.HandleFunc("/leadership-team", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`Budi: budi@acme.id`))
	})
	mux.HandleFunc("/karir", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`secret@acme.id`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	c := &Crawler{Client: srv.Client(), UserAgent: "BalasEmailBot/1.0", Scheme: "http"}
	found, err := c.Crawl(context.Background(), u.Host, "acme.id")
	if err != nil {
		t.Fatal(err)
	}
	var emails []string
	for _, f := range found {
		emails = append(emails, f.Email)
	}
	if !slices.Equal(emails, []string{"info@acme.id", "budi@acme.id"}) {
		t.Fatalf("emails = %v (robots-disallowed page must be skipped)", emails)
	}
}

type fakeValidator map[string]validator.Status

func (f fakeValidator) Validate(_ context.Context, e string) validator.Result {
	s, ok := f[e]
	if !ok {
		s = validator.StatusInvalid
	}
	return validator.Result{Email: e, Status: s}
}

type noSupp struct{}

func (noSupp) Check(context.Context, string, string) (string, error) { return "", nil }

func TestFindPerson(t *testing.T) {
	s := NewService(nil, fakeValidator{"bsantoso@acme.id": validator.StatusValid}, noSupp{}, nil)
	f, err := s.FindPerson(context.Background(), "ws", "Budi", "Santoso", "https://www.acme.id/about")
	if err != nil || f.Email != "bsantoso@acme.id" || f.Confidence < 80 {
		t.Fatalf("got %+v, %v", f, err)
	}
	if _, err := s.FindPerson(context.Background(), "ws", "Budi", "Santoso", "gmail.com"); err == nil {
		t.Fatal("free provider domain must be rejected")
	}
}
