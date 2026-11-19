package finder

import (
	"context"
	"net/http"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/contact"
	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/validator"
)

type Validator interface {
	Validate(ctx context.Context, email string) validator.Result
}

type Suppressor interface {
	Check(ctx context.Context, wsID, email string) (string, error)
}

type ContactSaver interface {
	Upsert(ctx context.Context, wsID string, in contact.Input) (contact.Contact, error)
}

type Service struct {
	crawler  *Crawler
	v        Validator
	supp     Suppressor
	contacts ContactSaver
}

func NewService(c *Crawler, v Validator, s Suppressor, cs ContactSaver) *Service {
	return &Service{crawler: c, v: v, supp: s, contacts: cs}
}

func cleanDomain(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	d, _, _ = strings.Cut(d, "/")
	d = strings.TrimPrefix(d, "www.")
	if !emailaddr.ValidDomain(d) {
		return "", apperr.Invalid("invalid_domain", "domain is not valid")
	}
	if emailaddr.IsFreeProvider(d) {
		return "", apperr.Invalid("not_a_company_domain", "finder works on company domains only")
	}
	return d, nil
}

// SearchDomain crawls the company website and scores each address.
func (s *Service) SearchDomain(ctx context.Context, wsID, domain string) ([]Found, error) {
	d, err := cleanDomain(domain)
	if err != nil {
		return nil, err
	}
	found, err := s.crawler.Crawl(ctx, "www."+d, d)
	if len(found) == 0 {
		// Some sites serve only the apex.
		found, err = s.crawler.Crawl(ctx, d, d)
	}
	if err != nil && len(found) == 0 {
		metrics.FinderResults.WithLabelValues("domain", "error").Inc()
		return nil, apperr.Invalid("crawl_failed", "could not fetch the website")
	}
	out := found[:0]
	for _, f := range found {
		if reason, _ := s.supp.Check(ctx, wsID, f.Email); reason != "" {
			continue
		}
		local, _, _ := strings.Cut(f.Email, "@")
		f.FirstName, f.LastName = GuessName(local)
		f.RoleBased = emailaddr.IsRole(local)
		f.Confidence = 90
		if f.RoleBased {
			f.Confidence = 75
		}
		out = append(out, f)
	}
	metrics.FinderResults.WithLabelValues("domain", outcome(len(out))).Inc()
	return out, nil
}

// FindPerson tries pattern candidates and returns the best verified address.
func (s *Service) FindPerson(ctx context.Context, wsID, first, last, domain string) (Found, error) {
	d, err := cleanDomain(domain)
	if err != nil {
		return Found{}, err
	}
	cands := Candidates(first, last, d)
	if len(cands) == 0 {
		return Found{}, apperr.Invalid("missing_name", "first_name is required")
	}
	var firstRisky *Found
	for i, e := range cands {
		if i >= 6 { // bound SMTP load per lookup
			break
		}
		if reason, _ := s.supp.Check(ctx, wsID, e); reason != "" {
			return Found{}, apperr.NotFound("suppressed", "this person has asked not to be contacted")
		}
		res := s.v.Validate(ctx, e)
		f := Found{Email: e, FirstName: first, LastName: last, Method: "pattern", Status: string(res.Status)}
		switch res.Status {
		case validator.StatusValid:
			f.Confidence = 95 - i*2
			metrics.FinderResults.WithLabelValues("person", "found").Inc()
			return f, nil
		case validator.StatusRisky, validator.StatusUnknown:
			if firstRisky == nil {
				f.Confidence = 40
				firstRisky = &f
			}
			if res.Checks.CatchAll || res.Reason == "smtp_probe_disabled" {
				// Server cannot confirm individual mailboxes: best guess is the most common pattern.
				metrics.FinderResults.WithLabelValues("person", "guessed").Inc()
				return *firstRisky, nil
			}
		case validator.StatusInvalid:
			if res.Reason == "no_mx_record" {
				return Found{}, apperr.Invalid("no_mx", "domain does not receive email")
			}
		}
	}
	if firstRisky != nil {
		metrics.FinderResults.WithLabelValues("person", "guessed").Inc()
		return *firstRisky, nil
	}
	metrics.FinderResults.WithLabelValues("person", "not_found").Inc()
	return Found{}, apperr.NotFound("not_found", "no deliverable address found for this person")
}

func outcome(n int) string {
	if n == 0 {
		return "empty"
	}
	return "found"
}

func (s *Service) save(ctx context.Context, wsID, company string, tags []string, fs []Found) int {
	n := 0
	for _, f := range fs {
		_, err := s.contacts.Upsert(ctx, wsID, contact.Input{
			Email: f.Email, FirstName: f.FirstName, LastName: f.LastName, Title: f.Title, Company: company,
			Tags: tags, Source: "finder", SourceURL: f.SourceURL,
		})
		if err == nil {
			n++
		}
	}
	return n
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("POST /v1/finder/domain", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Domain       string   `json:"domain"`
			Company      string   `json:"company"`
			SaveContacts bool     `json:"save_contacts"`
			Tags         []string `json:"tags"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		ws := authctx.MustFrom(r.Context()).WorkspaceID
		fs, err := s.SearchDomain(r.Context(), ws, in.Domain)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		saved := 0
		if in.SaveContacts {
			saved = s.save(r.Context(), ws, in.Company, in.Tags, fs)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"data": fs, "saved": saved})
	})))
	mux.Handle("POST /v1/finder/person", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FirstName    string   `json:"first_name"`
			LastName     string   `json:"last_name"`
			Domain       string   `json:"domain"`
			Company      string   `json:"company"`
			SaveContacts bool     `json:"save_contacts"`
			Tags         []string `json:"tags"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		ws := authctx.MustFrom(r.Context()).WorkspaceID
		f, err := s.FindPerson(r.Context(), ws, in.FirstName, in.LastName, in.Domain)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if in.SaveContacts {
			s.save(r.Context(), ws, in.Company, in.Tags, []Found{f})
		}
		httpx.JSON(w, http.StatusOK, f)
	})))
}
