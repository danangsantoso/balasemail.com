package finder

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/contact"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/platform/sheet"
)

const (
	JobSearch = "finder.search"
	// MaxPlaces bounds one search; DailySearches bounds searches per workspace per day.
	MaxPlaces     = 100
	DailySearches = 20
	// crawlBatch websites are visited per job run, one after another.
	crawlBatch = 5
	// searchAttempts tries the place source before the search is marked failed.
	searchAttempts = 4
)

// SearchInput starts a region + category search.
type SearchInput struct {
	Region       string   `json:"region"`
	Category     string   `json:"category"`
	Limit        int      `json:"limit"`
	Crawl        *bool    `json:"crawl_websites"` // default true
	SaveContacts bool     `json:"save_contacts"`
	Tags         []string `json:"tags"`
}

type Search struct {
	ID            string     `json:"id"`
	Region        string     `json:"region"`
	Category      string     `json:"category"`
	CategoryLabel string     `json:"category_label"`
	Limit         int        `json:"limit"`
	Crawl         bool       `json:"crawl_websites"`
	SaveContacts  bool       `json:"save_contacts"`
	Tags          []string   `json:"tags"`
	Status        string     `json:"status"`
	Error         string     `json:"error,omitempty"`
	PlacesFound   int        `json:"places_found"`
	WithEmail     int        `json:"places_with_email"`
	Emails        int        `json:"emails"`
	ToCrawl       int        `json:"websites_pending"`
	Saved         int        `json:"saved"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Places        []Place    `json:"places,omitempty"`
}

// SiteFinder returns addresses published on a company's website.
type SiteFinder func(ctx context.Context, wsID, domain string) ([]Found, error)

type SearchService struct {
	db       *pgxpool.Pool
	q        *queue.Queue
	source   PlaceSource
	site     SiteFinder
	contacts ContactSaver
}

func NewSearchService(db *pgxpool.Pool, q *queue.Queue, src PlaceSource, site SiteFinder, cs ContactSaver) *SearchService {
	return &SearchService{db: db, q: q, source: src, site: site, contacts: cs}
}

// Start validates the request and queues the search.
func (s *SearchService) Start(ctx context.Context, p authctx.Principal, in SearchInput) (Search, error) {
	region, err := CleanRegion(in.Region)
	if err != nil {
		return Search{}, err
	}
	if _, ok := CategoryByKey(in.Category); !ok {
		return Search{}, apperr.Invalid("invalid_category", "choose one of the business categories")
	}
	if in.Limit <= 0 {
		in.Limit = 30
	}
	in.Limit = min(in.Limit, MaxPlaces)
	crawl := in.Crawl == nil || *in.Crawl
	var today int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM finder_searches WHERE workspace_id=$1 AND created_at > now() - interval '1 day'`,
		p.WorkspaceID).Scan(&today); err != nil {
		return Search{}, apperr.Internal(err)
	}
	if today >= DailySearches {
		return Search{}, apperr.RateLimited("search_limit", fmt.Sprintf("at most %d region searches per day", DailySearches))
	}
	tags := slices.DeleteFunc(slices.Clone(in.Tags), func(t string) bool { return strings.TrimSpace(t) == "" })
	var id string
	err = database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		var by any
		if p.UserID != "" {
			by = p.UserID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO finder_searches (workspace_id, created_by, region, category, max_places, crawl, save_contacts, tags)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.WorkspaceID, by, region, in.Category, in.Limit, crawl, in.SaveContacts, tags).Scan(&id); err != nil {
			return err
		}
		return queue.New(tx).Enqueue(ctx, JobSearch, searchJob{ID: id}, queue.Options{Priority: queue.PriorityBulk, MaxAttempts: searchAttempts})
	})
	if err != nil {
		return Search{}, apperr.Internal(err)
	}
	return s.Get(ctx, p.WorkspaceID, id, false)
}

type searchJob struct {
	ID string `json:"id"`
}

// HandleJob runs a search in steps: list the places, then visit their
// websites a few at a time, then save contacts if asked.
func (s *SearchService) HandleJob(ctx context.Context, job queue.Job) error {
	var j searchJob
	if err := job.Decode(&j); err != nil {
		return queue.Permanent(err)
	}
	var wsID, region, category, status string
	var limit int
	var crawl, save bool
	var tags []string
	err := s.db.QueryRow(ctx, `SELECT workspace_id, region, category, max_places, crawl, save_contacts, tags, status
		FROM finder_searches WHERE id=$1`, j.ID).Scan(&wsID, &region, &category, &limit, &crawl, &save, &tags, &status)
	if database.IsNoRows(err) {
		return nil // deleted meanwhile
	}
	if err != nil {
		return err
	}
	switch status {
	case "queued", "searching":
		return s.findPlaces(ctx, job, j, region, category, limit, crawl)
	case "crawling":
		return s.crawlSome(ctx, j, wsID, save, tags)
	}
	return nil
}

func (s *SearchService) findPlaces(ctx context.Context, job queue.Job, j searchJob, region, category string, limit int, crawl bool) error {
	_, _ = s.db.Exec(ctx, `UPDATE finder_searches SET status='searching' WHERE id=$1`, j.ID)
	c, _ := CategoryByKey(category)
	places, err := s.source.Places(ctx, region, c, limit)
	if err != nil {
		slog.Warn("finder search: places", "id", j.ID, "err", err)
		if job.Attempts >= searchAttempts { // Attempts counts this run
			_, _ = s.db.Exec(ctx, `UPDATE finder_searches SET status='failed', error='source_unavailable', finished_at=now() WHERE id=$1`, j.ID)
			return nil
		}
		return err // retried with the queue's backoff
	}
	err = database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM finder_places WHERE search_id=$1`, j.ID); err != nil {
			return err
		}
		rows := make([][]any, len(places))
		for i, p := range places {
			// Places without a crawlable company site are done straight away.
			done := !crawl || CompanyDomain(p.Website) == ""
			note := ""
			if crawl && p.Website != "" && CompanyDomain(p.Website) == "" {
				note = "platform_site"
			}
			rows[i] = []any{j.ID, i, clip(p.Name, 200), p.Category, clip(p.Address, 300), clip(p.City, 100), clip(p.Phone, 60),
				clip(p.Website, 500), p.SourceURL, p.Emails, done, note}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"finder_places"},
			[]string{"search_id", "idx", "name", "category", "address", "city", "phone", "website", "source_url", "emails", "crawled", "note"},
			pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE finder_searches SET status='crawling' WHERE id=$1`, j.ID)
		return err
	})
	if err != nil {
		return err
	}
	return s.q.Enqueue(ctx, JobSearch, j, queue.Options{Priority: queue.PriorityBulk})
}

func (s *SearchService) crawlSome(ctx context.Context, j searchJob, wsID string, save bool, tags []string) error {
	rows, err := s.db.Query(ctx, `SELECT idx, website, emails FROM finder_places WHERE search_id=$1 AND NOT crawled ORDER BY idx LIMIT $2`,
		j.ID, crawlBatch)
	if err != nil {
		return err
	}
	type todo struct {
		idx     int
		website string
		emails  []string
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (todo, error) {
		var t todo
		return t, r.Scan(&t.idx, &t.website, &t.emails)
	})
	if err != nil {
		return err
	}
	if len(batch) == 0 {
		return s.finish(ctx, j.ID, wsID, save, tags)
	}
	for _, t := range batch {
		note := ""
		found, err := s.site(ctx, wsID, CompanyDomain(t.website))
		switch {
		case err != nil:
			note = "website_unreachable"
		case len(found) == 0:
			note = "no_email_on_website"
		}
		for _, f := range found {
			if !slices.Contains(t.emails, f.Email) {
				t.emails = append(t.emails, f.Email)
			}
		}
		if _, err := s.db.Exec(ctx, `UPDATE finder_places SET emails=$3, crawled=true, note=$4 WHERE search_id=$1 AND idx=$2`,
			j.ID, t.idx, t.emails, note); err != nil {
			return err
		}
	}
	return s.q.Enqueue(ctx, JobSearch, j, queue.Options{Priority: queue.PriorityBulk})
}

func (s *SearchService) finish(ctx context.Context, id, wsID string, save bool, tags []string) error {
	saved := 0
	if save {
		places, err := s.places(ctx, id)
		if err != nil {
			return err
		}
		for _, p := range places {
			for _, e := range p.Emails {
				fields := map[string]string{"kota": p.City, "kategori": p.Category}
				if p.Address != "" {
					fields["alamat"] = p.Address
				}
				if p.Website != "" {
					fields["website"] = p.Website
				}
				src := p.Website
				if src == "" {
					src = p.SourceURL
				}
				if _, err := s.contacts.Upsert(ctx, wsID, contact.Input{Email: e, Company: p.Name, Phone: p.Phone, Tags: tags,
					Fields: fields, Source: "finder", SourceURL: src}); err == nil {
					saved++
				} else if apperr.Is(err, apperr.KindPaymentRequired) {
					break // plan contact limit reached; the rest stay in the results
				}
			}
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE finder_searches SET status='done', saved=$2, finished_at=now() WHERE id=$1`, id, saved)
	return err
}

func (s *SearchService) places(ctx context.Context, id string) ([]Place, error) {
	rows, err := s.db.Query(ctx, `SELECT name, category, address, city, phone, website, source_url, emails, crawled, note
		FROM finder_places WHERE search_id=$1 ORDER BY cardinality(emails) = 0, idx`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Place, error) {
		var p Place
		return p, r.Scan(&p.Name, &p.Category, &p.Address, &p.City, &p.Phone, &p.Website, &p.SourceURL, &p.Emails, &p.Crawled, &p.Note)
	})
}

const searchCols = `s.id, s.region, s.category, s.max_places, s.crawl, s.save_contacts, s.tags, s.status, s.error, s.saved,
	s.created_at, s.finished_at, count(p.idx), count(p.idx) FILTER (WHERE cardinality(p.emails) > 0),
	coalesce(sum(cardinality(p.emails)), 0), count(p.idx) FILTER (WHERE NOT p.crawled)`

func scanSearch(r pgx.Row) (Search, error) {
	var s Search
	err := r.Scan(&s.ID, &s.Region, &s.Category, &s.Limit, &s.Crawl, &s.SaveContacts, &s.Tags, &s.Status, &s.Error, &s.Saved,
		&s.CreatedAt, &s.FinishedAt, &s.PlacesFound, &s.WithEmail, &s.Emails, &s.ToCrawl)
	if c, ok := CategoryByKey(s.Category); ok {
		s.CategoryLabel = c.Label
	}
	return s, err
}

// Get returns a search; withPlaces adds the businesses found, those with an email first.
func (s *SearchService) Get(ctx context.Context, wsID, id string, withPlaces bool) (Search, error) {
	out, err := scanSearch(s.db.QueryRow(ctx, `SELECT `+searchCols+` FROM finder_searches s
		LEFT JOIN finder_places p ON p.search_id=s.id WHERE s.id=$1 AND s.workspace_id=$2 GROUP BY s.id`, id, wsID))
	if database.IsNoRows(err) {
		return out, apperr.NotFound("search_not_found", "search not found")
	}
	if err != nil {
		return out, apperr.Internal(err)
	}
	if withPlaces {
		if out.Places, err = s.places(ctx, id); err != nil {
			return out, apperr.Internal(err)
		}
	}
	return out, nil
}

func (s *SearchService) List(ctx context.Context, wsID string, p httpx.Page) ([]Search, error) {
	rows, err := s.db.Query(ctx, `SELECT `+searchCols+` FROM finder_searches s
		LEFT JOIN finder_places p ON p.search_id=s.id WHERE s.workspace_id=$1
		GROUP BY s.id ORDER BY s.created_at DESC LIMIT $2 OFFSET $3`, wsID, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Search, error) { return scanSearch(r) })
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return out, nil
}

// Export writes the businesses (one row per email, or one row without) as csv or xlsx.
func (s *SearchService) Export(ctx context.Context, wsID, id, format string) (string, string, []byte, error) {
	if format != "csv" && format != "xlsx" {
		return "", "", nil, apperr.Invalid("invalid_format", "format must be csv or xlsx")
	}
	sr, err := s.Get(ctx, wsID, id, true)
	if err != nil {
		return "", "", nil, err
	}
	table := [][]string{{"email", "perusahaan", "kategori", "alamat", "kota", "telepon", "website", "sumber"}}
	for _, p := range sr.Places {
		emails := p.Emails
		if len(emails) == 0 {
			emails = []string{""}
		}
		for _, e := range emails {
			table = append(table, []string{e, p.Name, p.Category, p.Address, p.City, p.Phone, p.Website, p.SourceURL})
		}
	}
	name := fmt.Sprintf("prospek-%s-%s.%s", strings.ReplaceAll(sr.Category, "_", "-"), slugify(sr.Region), format)
	if format == "xlsx" {
		b, err := sheet.XLSX("Prospek", table)
		if err != nil {
			return "", "", nil, apperr.Internal(err)
		}
		return name, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", b, nil
	}
	var buf bytes.Buffer
	if err := sheet.CSV(&buf, table); err != nil {
		return "", "", nil, apperr.Internal(err)
	}
	return name, "text/csv; charset=utf-8", buf.Bytes(), nil
}

func (s *SearchService) Delete(ctx context.Context, wsID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM finder_searches WHERE id=$1 AND workspace_id=$2`, id, wsID)
	if err != nil {
		return apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("search_not_found", "search not found")
	}
	return nil
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Routes mounts the region + category search endpoints and the catalog they use.
func (s *SearchService) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("GET /v1/finder/categories", authed(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"categories": Categories, "regions": Regions,
			"attribution": "Data bisnis © kontributor OpenStreetMap (ODbL)"})
	})))
	mux.Handle("POST /v1/finder/searches", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in SearchInput
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		out, err := s.Start(r.Context(), authctx.MustFrom(r.Context()), in)
		httpx.Respond(w, r, http.StatusAccepted, out, err)
	})))
	mux.Handle("GET /v1/finder/searches", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.List(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": out}, err)
	})))
	mux.Handle("GET /v1/finder/searches/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Get(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"), true)
		httpx.Respond(w, r, http.StatusOK, out, err)
	})))
	mux.Handle("DELETE /v1/finder/searches/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id")); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /v1/finder/searches/{id}/export", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ctype, body, err := s.Export(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"), r.URL.Query().Get("format"))
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body) //nolint:gosec // G705: CSV/XLSX served as an attachment with nosniff, never rendered as HTML
	})))
}
