// Package alias creates unique business addresses on customer domains —
// per salesperson, per campaign or per contact (PRD F8).
package alias

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

type Alias struct {
	ID          string    `json:"id"`
	DomainID    string    `json:"domain_id"`
	Address     string    `json:"address"`
	DisplayName string    `json:"display_name"`
	ForwardTo   []string  `json:"forward_to"`
	CreatedAt   time.Time `json:"created_at"`
}

var (
	tokenRe     = regexp.MustCompile(`\{([a-z_]+)\}`)
	separatorRe = regexp.MustCompile(`[.\-_]{2,}`)
)

// RenderPattern builds a local part from a pattern such as "{first}.{division}"
// or "promo-{campaign}". Tokens: any key in vars, plus {f} {l} (initials) and
// {random} (6 hex chars). Values are slugged to [a-z0-9].
func RenderPattern(pattern string, vars map[string]string) (string, error) {
	if !strings.Contains(pattern, "{") {
		return "", fmt.Errorf("pattern must contain at least one {token}")
	}
	var missing []string
	local := tokenRe.ReplaceAllStringFunc(strings.ToLower(pattern), func(m string) string {
		key := m[1 : len(m)-1]
		switch key {
		case "random":
			return security.RandomHex(3)
		case "f":
			return initial(vars["first"])
		case "l":
			return initial(vars["last"])
		}
		v := slug(vars[key])
		if v == "" {
			missing = append(missing, key)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing value for %s", strings.Join(missing, ", "))
	}
	local = strings.Trim(separatorRe.ReplaceAllString(local, "."), ".-_")
	if local == "" || len(local) > 64 {
		return "", fmt.Errorf("pattern produced an invalid address")
	}
	if _, err := emailaddr.Parse(local + "@example.com"); err != nil {
		return "", fmt.Errorf("pattern produced an invalid address %q", local)
	}
	return local, nil
}

func initial(s string) string {
	s = slug(s)
	if s == "" {
		return ""
	}
	return s[:1]
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// DomainLookup verifies that a domain belongs to the workspace.
type DomainLookup interface {
	Lookup(ctx context.Context, domain string) (wsID, domainID string, ok bool)
}

type Service struct {
	db      *pgxpool.Pool
	domains DomainLookup
}

func NewService(db *pgxpool.Pool, d DomainLookup) *Service { return &Service{db: db, domains: d} }

type CreateInput struct {
	Address     string   `json:"address"`
	DisplayName string   `json:"display_name"`
	ForwardTo   []string `json:"forward_to"`
}

func (s *Service) Create(ctx context.Context, wsID string, in CreateInput) (Alias, error) {
	addr, err := emailaddr.Parse(in.Address)
	if err != nil {
		return Alias{}, apperr.Invalid("invalid_address", "address is not valid")
	}
	owner, domainID, ok := s.domains.Lookup(ctx, addr.Domain)
	if !ok || owner != wsID {
		return Alias{}, apperr.Forbidden("domain_not_verified", "verify "+addr.Domain+" before creating aliases on it")
	}
	fwd := make([]string, 0, len(in.ForwardTo))
	for _, f := range in.ForwardTo {
		n, err := emailaddr.Normalize(f)
		if err != nil {
			return Alias{}, apperr.Invalid("invalid_forward_to", "forward_to contains an invalid address")
		}
		if n == addr.String() {
			return Alias{}, apperr.Invalid("forward_loop", "an alias cannot forward to itself")
		}
		fwd = append(fwd, n)
	}
	if len(fwd) > 10 {
		return Alias{}, apperr.Invalid("too_many_targets", "at most 10 forward targets")
	}
	var a Alias
	err = s.db.QueryRow(ctx, `INSERT INTO aliases (workspace_id, domain_id, address, display_name, forward_to) VALUES ($1,$2,$3,$4,$5)
		RETURNING id, domain_id, address, display_name, forward_to, created_at`, wsID, domainID, addr.String(), in.DisplayName, fwd).
		Scan(&a.ID, &a.DomainID, &a.Address, &a.DisplayName, &a.ForwardTo, &a.CreatedAt)
	if database.IsUniqueViolation(err) {
		return a, apperr.Conflict("alias_exists", "address already exists")
	}
	if err != nil {
		return a, apperr.Internal(err)
	}
	return a, nil
}

type GenerateInput struct {
	Domain    string              `json:"domain"`
	Pattern   string              `json:"pattern"`
	Rows      []map[string]string `json:"rows"` // e.g. [{"first":"Budi","last":"Santoso","division":"sales","display_name":"Budi (Sales)"}]
	ForwardTo []string            `json:"forward_to"`
}

type GenerateResult struct {
	Created []Alias        `json:"created"`
	Errors  map[int]string `json:"errors,omitempty"`
}

// Generate bulk-creates aliases from a pattern; on collision a numeric suffix is added.
func (s *Service) Generate(ctx context.Context, wsID string, in GenerateInput) (GenerateResult, error) {
	if len(in.Rows) == 0 || len(in.Rows) > 1000 {
		return GenerateResult{}, apperr.Invalid("invalid_rows", "rows must contain 1..1000 entries")
	}
	res := GenerateResult{Errors: map[int]string{}}
	for i, row := range in.Rows {
		local, err := RenderPattern(in.Pattern, row)
		if err != nil {
			res.Errors[i] = err.Error()
			continue
		}
		var a Alias
		for n := 0; n < 20; n++ {
			candidate := local
			if n > 0 {
				candidate = fmt.Sprintf("%s%d", local, n+1)
			}
			a, err = s.Create(ctx, wsID, CreateInput{Address: candidate + "@" + in.Domain, DisplayName: row["display_name"], ForwardTo: in.ForwardTo})
			if !apperr.Is(err, apperr.KindConflict) {
				break
			}
		}
		if err != nil {
			res.Errors[i] = apperr.As(err).Message
			if apperr.Is(err, apperr.KindForbidden) {
				return res, err
			}
			continue
		}
		res.Created = append(res.Created, a)
	}
	return res, nil
}

func (s *Service) List(ctx context.Context, wsID string, p httpx.Page) ([]Alias, error) {
	rows, err := s.db.Query(ctx, `SELECT id, domain_id, address, display_name, forward_to, created_at FROM aliases
		WHERE workspace_id=$1 ORDER BY address LIMIT $2 OFFSET $3`, wsID, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Alias, error) {
		var a Alias
		err := r.Scan(&a.ID, &a.DomainID, &a.Address, &a.DisplayName, &a.ForwardTo, &a.CreatedAt)
		return a, err
	})
}

func (s *Service) Delete(ctx context.Context, wsID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM aliases WHERE id=$1 AND workspace_id=$2`, id, wsID)
	if err != nil {
		return apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("alias_not_found", "alias not found")
	}
	return nil
}

// Find returns the alias for an exact address (plus-tags stripped).
func (s *Service) Find(ctx context.Context, address string) (Alias, bool) {
	addr, err := emailaddr.Parse(address)
	if err != nil {
		return Alias{}, false
	}
	base, _ := emailaddr.SplitPlus(addr.Local)
	var a Alias
	err = s.db.QueryRow(ctx, `SELECT id, domain_id, address, display_name, forward_to, created_at FROM aliases WHERE address=$1`,
		base+"@"+addr.Domain).Scan(&a.ID, &a.DomainID, &a.Address, &a.DisplayName, &a.ForwardTo, &a.CreatedAt)
	return a, err == nil
}

func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/aliases", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		as, err := s.List(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": as}, err)
	})))
	mux.Handle("POST /v1/aliases", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		a, err := s.Create(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
		httpx.Respond(w, r, http.StatusCreated, a, err)
	})))
	mux.Handle("POST /v1/aliases/generate", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in GenerateInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		res, err := s.Generate(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, in)
		httpx.Respond(w, r, http.StatusOK, res, err)
	})))
	mux.Handle("DELETE /v1/aliases/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id")); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
