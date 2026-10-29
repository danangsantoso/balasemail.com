// Package contact manages prospect/customer records, CSV import and
// background validation (PRD F1, F2, F7).
package contact

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/suppression"
	"github.com/danangsantoso/balasemail.com/internal/validator"
)

const JobValidate = "contacts.validate"

type Contact struct {
	ID               string            `json:"id"`
	Email            string            `json:"email"`
	FirstName        string            `json:"first_name"`
	LastName         string            `json:"last_name"`
	Company          string            `json:"company"`
	Title            string            `json:"title"`
	Phone            string            `json:"phone"`
	Tags             []string          `json:"tags"`
	Fields           map[string]string `json:"fields"`
	Source           string            `json:"source"`
	SourceURL        string            `json:"source_url,omitempty"`
	LegalBasis       string            `json:"legal_basis"`
	ValidationStatus string            `json:"validation_status"`
	ValidationScore  *int              `json:"validation_score,omitempty"`
	ValidatedAt      *time.Time        `json:"validated_at,omitempty"`
	UnsubscribedAt   *time.Time        `json:"unsubscribed_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

// Vars returns template variables for personalization.
func (c Contact) Vars() map[string]string {
	v := map[string]string{
		"email": c.Email, "first_name": c.FirstName, "last_name": c.LastName,
		"full_name": strings.TrimSpace(c.FirstName + " " + c.LastName),
		"company":   c.Company, "title": c.Title, "jabatan": c.Title, "phone": c.Phone,
	}
	for k, val := range c.Fields {
		if _, exists := v[k]; !exists {
			v[k] = val
		}
	}
	return v
}

type Input struct {
	Email      string            `json:"email"`
	FirstName  string            `json:"first_name"`
	LastName   string            `json:"last_name"`
	Company    string            `json:"company"`
	Title      string            `json:"title"`
	Phone      string            `json:"phone"`
	Tags       []string          `json:"tags"`
	Fields     map[string]string `json:"fields"`
	Source     string            `json:"source"`
	SourceURL  string            `json:"source_url"`
	LegalBasis string            `json:"legal_basis"`
}

type Filter struct {
	Tag    string
	Status string
	Query  string
}

type Validator interface {
	Validate(ctx context.Context, email string) validator.Result
}

type Service struct {
	db   *pgxpool.Pool
	supp *suppression.Service
	q    *queue.Queue
	v    Validator
}

func NewService(db *pgxpool.Pool, supp *suppression.Service, q *queue.Queue, v Validator) *Service {
	return &Service{db: db, supp: supp, q: q, v: v}
}

const cols = `id, email, first_name, last_name, company, title, phone, tags, fields, source, source_url,
	legal_basis, validation_status, validation_score, validated_at, unsubscribed_at, created_at`

func scan(row pgx.Row) (Contact, error) {
	var c Contact
	err := row.Scan(&c.ID, &c.Email, &c.FirstName, &c.LastName, &c.Company, &c.Title, &c.Phone, &c.Tags, &c.Fields,
		&c.Source, &c.SourceURL, &c.LegalBasis, &c.ValidationStatus, &c.ValidationScore, &c.ValidatedAt, &c.UnsubscribedAt, &c.CreatedAt)
	if c.Fields == nil {
		c.Fields = map[string]string{}
	}
	return c, err
}

func normalizeInput(in *Input) error {
	e, err := emailaddr.Normalize(in.Email)
	if err != nil {
		return apperr.Invalid("invalid_email", fmt.Sprintf("invalid email %q", in.Email))
	}
	in.Email = e
	in.Tags = normalizeTags(in.Tags)
	if in.Fields == nil {
		in.Fields = map[string]string{}
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.LegalBasis == "" {
		in.LegalBasis = "legitimate_interest"
	}
	return nil
}

func normalizeTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

const upsertSQL = `
	INSERT INTO contacts (workspace_id, email, first_name, last_name, company, title, phone, tags, fields, source, source_url, legal_basis)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	ON CONFLICT (workspace_id, email) DO UPDATE SET
		first_name = COALESCE(NULLIF(EXCLUDED.first_name,''), contacts.first_name),
		last_name  = COALESCE(NULLIF(EXCLUDED.last_name,''), contacts.last_name),
		company    = COALESCE(NULLIF(EXCLUDED.company,''), contacts.company),
		title      = COALESCE(NULLIF(EXCLUDED.title,''), contacts.title),
		phone      = COALESCE(NULLIF(EXCLUDED.phone,''), contacts.phone),
		tags       = (SELECT ARRAY(SELECT DISTINCT unnest(contacts.tags || EXCLUDED.tags))),
		fields     = contacts.fields || EXCLUDED.fields,
		updated_at = now()
	RETURNING ` + cols + `, (xmax = 0) AS inserted`

func upsertArgs(wsID string, in Input) []any {
	return []any{wsID, in.Email, in.FirstName, in.LastName, in.Company, in.Title, in.Phone, in.Tags, in.Fields, in.Source, in.SourceURL, in.LegalBasis}
}

// Upsert creates or merges a contact by email.
func (s *Service) Upsert(ctx context.Context, wsID string, in Input) (Contact, error) {
	if err := normalizeInput(&in); err != nil {
		return Contact{}, err
	}
	var c Contact
	var inserted bool
	err := s.db.QueryRow(ctx, upsertSQL, upsertArgs(wsID, in)...).Scan(
		&c.ID, &c.Email, &c.FirstName, &c.LastName, &c.Company, &c.Title, &c.Phone, &c.Tags, &c.Fields,
		&c.Source, &c.SourceURL, &c.LegalBasis, &c.ValidationStatus, &c.ValidationScore, &c.ValidatedAt, &c.UnsubscribedAt, &c.CreatedAt, &inserted)
	if err != nil {
		return c, apperr.Internal(err)
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, wsID, id string) (Contact, error) {
	c, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM contacts WHERE id=$1 AND workspace_id=$2`, id, wsID))
	if database.IsNoRows(err) {
		return c, apperr.NotFound("contact_not_found", "contact not found")
	}
	if err != nil {
		return c, apperr.Internal(err)
	}
	return c, nil
}

func (s *Service) List(ctx context.Context, wsID string, f Filter, p httpx.Page) ([]Contact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM contacts
		WHERE workspace_id=$1
		  AND ($2 = '' OR $2 = ANY(tags))
		  AND ($3 = '' OR validation_status = $3)
		  AND ($4 = '' OR email ILIKE '%'||$4||'%' OR company ILIKE '%'||$4||'%')
		ORDER BY created_at DESC LIMIT $5 OFFSET $6`, wsID, strings.ToLower(f.Tag), f.Status, f.Query, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Contact, error) { return scan(r) })
}

// Delete erases a contact (UU PDP right to erasure). With suppress=true the
// address is also added to the suppression list so it is never re-contacted.
func (s *Service) Delete(ctx context.Context, wsID, id string, suppress bool) error {
	var email string
	err := s.db.QueryRow(ctx, `DELETE FROM contacts WHERE id=$1 AND workspace_id=$2 RETURNING email`, id, wsID).Scan(&email)
	if database.IsNoRows(err) {
		return apperr.NotFound("contact_not_found", "contact not found")
	}
	if err != nil {
		return apperr.Internal(err)
	}
	if suppress {
		return s.supp.Add(ctx, wsID, email, suppression.ReasonErasure)
	}
	return nil
}

func (s *Service) AddTags(ctx context.Context, wsID, id string, tags []string) error {
	_, err := s.db.Exec(ctx, `UPDATE contacts SET tags=(SELECT ARRAY(SELECT DISTINCT unnest(tags || $3::text[]))), updated_at=now()
		WHERE id=$1 AND workspace_id=$2`, id, wsID, normalizeTags(tags))
	return err
}

func (s *Service) RemoveTags(ctx context.Context, wsID, id string, tags []string) error {
	_, err := s.db.Exec(ctx, `UPDATE contacts SET tags=(SELECT ARRAY(SELECT unnest(tags) EXCEPT SELECT unnest($3::text[]))), updated_at=now()
		WHERE id=$1 AND workspace_id=$2`, id, wsID, normalizeTags(tags))
	return err
}

// ---- CSV import ----

type RowError struct {
	Row   int    `json:"row"`
	Error string `json:"error"`
}

type ImportReport struct {
	Total    int        `json:"total"`
	Imported int        `json:"imported"`
	Updated  int        `json:"updated"`
	Skipped  int        `json:"skipped"`
	Errors   []RowError `json:"errors,omitempty"`
}

// Column aliases accepted in CSV headers (English and Indonesian).
var columnAliases = map[string]string{
	"email": "email", "e-mail": "email", "alamat email": "email",
	"first_name": "first_name", "firstname": "first_name", "first name": "first_name", "nama depan": "first_name", "nama": "first_name",
	"last_name": "last_name", "lastname": "last_name", "last name": "last_name", "nama belakang": "last_name",
	"company": "company", "perusahaan": "company", "organization": "company",
	"title": "title", "job title": "title", "jabatan": "title", "position": "title",
	"phone": "phone", "telepon": "phone", "hp": "phone", "no hp": "phone", "mobile": "phone",
	"tags": "tags", "tag": "tags",
}

// MaxImportRows bounds a synchronous import.
const MaxImportRows = 100_000

// ParseCSV converts CSV rows into inputs. Unknown columns become custom fields.
func ParseCSV(r io.Reader, defaultTags []string) ([]Input, []RowError, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, nil, apperr.Invalid("invalid_csv", "CSV header row is missing")
	}
	mapping := make([]string, len(header))
	hasEmail := false
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if canon, ok := columnAliases[key]; ok {
			mapping[i] = canon
		} else {
			mapping[i] = "field:" + strings.ReplaceAll(key, " ", "_")
		}
		hasEmail = hasEmail || mapping[i] == "email"
	}
	if !hasEmail {
		return nil, nil, apperr.Invalid("invalid_csv", "CSV must contain an 'email' column")
	}
	var inputs []Input
	var rowErrs []RowError
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rowErrs = append(rowErrs, RowError{Row: line, Error: err.Error()})
			continue
		}
		if len(inputs) >= MaxImportRows {
			return nil, nil, apperr.Invalid("too_many_rows", fmt.Sprintf("import is limited to %d rows", MaxImportRows))
		}
		in := Input{Fields: map[string]string{}, Tags: slices.Clone(defaultTags), Source: "import"}
		for i, val := range rec {
			if i >= len(mapping) {
				break
			}
			val = strings.TrimSpace(val)
			switch m := mapping[i]; m {
			case "email":
				in.Email = val
			case "first_name":
				in.FirstName = val
			case "last_name":
				in.LastName = val
			case "company":
				in.Company = val
			case "title":
				in.Title = val
			case "phone":
				in.Phone = val
			case "tags":
				in.Tags = append(in.Tags, strings.FieldsFunc(val, func(r rune) bool { return r == ';' || r == '|' || r == ',' })...)
			default:
				if val != "" {
					in.Fields[strings.TrimPrefix(m, "field:")] = val
				}
			}
		}
		if err := normalizeInput(&in); err != nil {
			rowErrs = append(rowErrs, RowError{Row: line, Error: apperr.As(err).Message})
			continue
		}
		inputs = append(inputs, in)
	}
	return inputs, rowErrs, nil
}

// Import upserts parsed rows in batches.
func (s *Service) Import(ctx context.Context, wsID string, r io.Reader, defaultTags []string) (ImportReport, error) {
	inputs, rowErrs, err := ParseCSV(r, defaultTags)
	if err != nil {
		return ImportReport{}, err
	}
	rep := ImportReport{Total: len(inputs) + len(rowErrs), Skipped: len(rowErrs), Errors: rowErrs}
	if len(rep.Errors) > 100 {
		rep.Errors = rep.Errors[:100]
	}
	for chunk := range slices.Chunk(inputs, 500) {
		batch := &pgx.Batch{}
		for _, in := range chunk {
			batch.Queue(`WITH up AS (`+upsertSQL+`) SELECT inserted FROM up`, upsertArgs(wsID, in)...)
		}
		br := s.db.SendBatch(ctx, batch)
		for range chunk {
			var inserted bool
			if err := br.QueryRow().Scan(&inserted); err != nil {
				br.Close()
				return rep, apperr.Internal(err)
			}
			if inserted {
				rep.Imported++
			} else {
				rep.Updated++
			}
		}
		if err := br.Close(); err != nil {
			return rep, apperr.Internal(err)
		}
	}
	return rep, nil
}

// ---- background validation (PRD F2: bulk + periodic re-validation) ----

type validatePayload struct {
	WorkspaceID string `json:"workspace_id"`
	Tag         string `json:"tag"`
}

// EnqueueValidation schedules validation of unverified/stale contacts.
func (s *Service) EnqueueValidation(ctx context.Context, wsID, tag string) error {
	return s.q.Enqueue(ctx, JobValidate, validatePayload{WorkspaceID: wsID, Tag: strings.ToLower(tag)}, queue.Options{Priority: queue.PriorityBulk})
}

// HandleValidateJob processes one page of contacts and re-enqueues itself until done.
func (s *Service) HandleValidateJob(ctx context.Context, job queue.Job) error {
	var p validatePayload
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	rows, err := s.db.Query(ctx, `SELECT id, email FROM contacts
		WHERE workspace_id=$1 AND ($2='' OR $2 = ANY(tags))
		  AND (validated_at IS NULL OR validated_at < now() - interval '90 days')
		ORDER BY created_at LIMIT 200`, p.WorkspaceID, p.Tag)
	if err != nil {
		return err
	}
	type item struct{ id, email string }
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) {
		var it item
		return it, r.Scan(&it.id, &it.email)
	})
	if err != nil {
		return err
	}
	for _, it := range items {
		res := s.v.Validate(ctx, it.email)
		if _, err := s.db.Exec(ctx, `UPDATE contacts SET validation_status=$2, validation_score=$3, validated_at=now(), updated_at=now() WHERE id=$1`,
			it.id, string(res.Status), res.Score); err != nil {
			return err
		}
	}
	if len(items) == 200 {
		return s.q.Enqueue(ctx, JobValidate, p, queue.Options{Priority: queue.PriorityBulk})
	}
	return nil
}
