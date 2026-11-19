// Package listcheck validates whole email lists uploaded as CSV, TXT or Excel
// (PRD F2) in the background and exports the results in the same formats.
package listcheck

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	"github.com/danangsantoso/balasemail.com/internal/validator"
)

const (
	JobRun = "validation.list"
	// MaxRows bounds one uploaded list; MaxBytes bounds the upload.
	MaxRows  = 20_000
	MaxBytes = 20 << 20
	// pageSize rows are checked per job run, parallel at a time.
	pageSize = 100
	parallel = 8
)

// Validator is the subset of *validator.Validator used here.
type Validator interface {
	ValidateMany(ctx context.Context, emails []string, parallel int) []validator.Result
}

type Service struct {
	db *pgxpool.Pool
	q  *queue.Queue
	v  Validator
}

func NewService(db *pgxpool.Pool, q *queue.Queue, v Validator) *Service {
	return &Service{db: db, q: q, v: v}
}

// Counts summarises results by status; Pending rows are not checked yet.
type Counts struct {
	Valid      int `json:"valid"`
	Risky      int `json:"risky"`
	Unknown    int `json:"unknown"`
	Invalid    int `json:"invalid"`
	Disposable int `json:"disposable"`
	Pending    int `json:"pending"`
}

type List struct {
	ID         string     `json:"id"`
	Filename   string     `json:"filename"`
	Format     string     `json:"format"`
	Status     string     `json:"status"`
	Total      int        `json:"total"`
	Skipped    int        `json:"skipped"`
	Processed  int        `json:"processed"`
	Counts     Counts     `json:"counts"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Upload parses the file and queues every row for validation. Malformed
// addresses are recorded as invalid straight away; duplicates are skipped.
func (s *Service) Upload(ctx context.Context, p authctx.Principal, filename string, r io.Reader) (List, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return List{}, apperr.Invalid("invalid_file", "could not read the uploaded file")
	}
	if len(data) > MaxBytes {
		return List{}, apperr.Invalid("file_too_large", "file is larger than 20MB")
	}
	inputs, rowErrs, format, err := contact.ParseFile(filename, data, nil)
	if err != nil {
		return List{}, err
	}
	type item struct {
		row                         int
		email, first, last, company string
		bad                         bool
	}
	var items []item
	skipped := 0
	for i, in := range inputs {
		items = append(items, item{row: i + 1, email: in.Email, first: in.FirstName, last: in.LastName, company: in.Company})
	}
	for _, e := range rowErrs {
		if e.Email == "" {
			skipped++ // duplicates and rows without an address
			continue
		}
		items = append(items, item{row: len(items) + 1, email: e.Email, first: e.Contact.FirstName, last: e.Contact.LastName, company: e.Contact.Company, bad: true})
	}
	if len(items) == 0 {
		return List{}, apperr.Invalid("empty_file", "the file contains no email addresses")
	}
	if len(items) > MaxRows {
		return List{}, apperr.Invalid("too_many_rows", fmt.Sprintf("a list can hold at most %d addresses; split the file", MaxRows))
	}

	var id string
	err = database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		var by any
		if p.UserID != "" {
			by = p.UserID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO validation_lists (workspace_id, created_by, filename, format, total, skipped)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, p.WorkspaceID, by, truncate(filename, 200), format, len(items), skipped).Scan(&id); err != nil {
			return err
		}
		now := time.Now()
		rows := make([][]any, len(items))
		for i, it := range items {
			status, reason, checked := "", "", any(nil)
			if it.bad {
				status, reason, checked = string(validator.StatusInvalid), "invalid_syntax", now
			}
			rows[i] = []any{id, it.row, truncate(it.email, 320), truncate(it.first, 100), truncate(it.last, 100), truncate(it.company, 200), status, reason, checked}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"validation_list_items"},
			[]string{"list_id", "row_no", "email", "first_name", "last_name", "company", "status", "reason", "checked_at"},
			pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		return queue.New(tx).Enqueue(ctx, JobRun, runPayload{ListID: id}, queue.Options{Priority: queue.PriorityBulk})
	})
	if err != nil {
		return List{}, apperr.Internal(err)
	}
	return s.Get(ctx, p.WorkspaceID, id)
}

type runPayload struct {
	ListID string `json:"list_id"`
}

// HandleRun checks one page of a list and queues itself until the list is done.
func (s *Service) HandleRun(ctx context.Context, job queue.Job) error {
	var p runPayload
	if err := job.Decode(&p); err != nil {
		return queue.Permanent(err)
	}
	rows, err := s.db.Query(ctx, `SELECT row_no, email FROM validation_list_items
		WHERE list_id=$1 AND status='' ORDER BY row_no LIMIT $2`, p.ListID, pageSize)
	if err != nil {
		return err
	}
	type pending struct {
		row   int
		email string
	}
	page, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
		var it pending
		return it, r.Scan(&it.row, &it.email)
	})
	if err != nil {
		return err
	}
	if len(page) == 0 {
		_, err := s.db.Exec(ctx, `UPDATE validation_lists SET status='done', finished_at=now() WHERE id=$1 AND status<>'done'`, p.ListID)
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE validation_lists SET status='running' WHERE id=$1 AND status='queued'`, p.ListID); err != nil {
		return err
	}
	emails := make([]string, len(page))
	for i, it := range page {
		emails[i] = it.email
	}
	results := s.v.ValidateMany(ctx, emails, parallel)
	batch := &pgx.Batch{}
	for i, res := range results {
		c := res.Checks
		batch.Queue(`UPDATE validation_list_items SET status=$3, reason=$4, score=$5, mx_host=$6, disposable=$7,
			role_based=$8, free_provider=$9, catch_all=$10, checked_at=$11 WHERE list_id=$1 AND row_no=$2`,
			p.ListID, page[i].row, string(res.Status), res.Reason, res.Score, c.MXHost, c.Disposable, c.RoleBased, c.FreeProvider, c.CatchAll, res.CheckedAt)
	}
	if err := s.db.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	return s.q.Enqueue(ctx, JobRun, p, queue.Options{Priority: queue.PriorityBulk})
}

const listCols = `l.id, l.filename, l.format, l.status, l.total, l.skipped, l.created_at, l.finished_at,
	count(*) FILTER (WHERE i.status='valid'), count(*) FILTER (WHERE i.status='risky'),
	count(*) FILTER (WHERE i.status='unknown'), count(*) FILTER (WHERE i.status='invalid'),
	count(*) FILTER (WHERE i.status='disposable'), count(*) FILTER (WHERE i.status='')`

func scanList(r pgx.Row) (List, error) {
	var l List
	c := &l.Counts
	err := r.Scan(&l.ID, &l.Filename, &l.Format, &l.Status, &l.Total, &l.Skipped, &l.CreatedAt, &l.FinishedAt,
		&c.Valid, &c.Risky, &c.Unknown, &c.Invalid, &c.Disposable, &c.Pending)
	l.Processed = l.Total - c.Pending
	return l, err
}

func (s *Service) Get(ctx context.Context, wsID, id string) (List, error) {
	l, err := scanList(s.db.QueryRow(ctx, `SELECT `+listCols+` FROM validation_lists l
		LEFT JOIN validation_list_items i ON i.list_id=l.id
		WHERE l.id=$1 AND l.workspace_id=$2 GROUP BY l.id`, id, wsID))
	if database.IsNoRows(err) {
		return l, apperr.NotFound("list_not_found", "validation list not found")
	}
	if err != nil {
		return l, apperr.Internal(err)
	}
	return l, nil
}

func (s *Service) Lists(ctx context.Context, wsID string, p httpx.Page) ([]List, error) {
	rows, err := s.db.Query(ctx, `SELECT `+listCols+` FROM validation_lists l
		LEFT JOIN validation_list_items i ON i.list_id=l.id
		WHERE l.workspace_id=$1 GROUP BY l.id ORDER BY l.created_at DESC LIMIT $2 OFFSET $3`, wsID, p.Limit, p.Offset)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (List, error) { return scanList(r) })
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, wsID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM validation_lists WHERE id=$1 AND workspace_id=$2`, id, wsID)
	if err != nil {
		return apperr.Internal(err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("list_not_found", "validation list not found")
	}
	return nil
}

// Filters for exports: which statuses to include.
var filters = map[string][]string{
	"all":         nil,
	"valid":       {"valid"},
	"deliverable": {"valid", "risky"}, // safe to send, catch-all and role mailboxes included
	"invalid":     {"invalid", "disposable"},
}

// ReasonText explains each validator reason in Indonesian for exported files.
var ReasonText = map[string]string{
	"mailbox_exists":          "Kotak surat aktif",
	"mailbox_not_found":       "Kotak surat tidak ditemukan",
	"invalid_syntax":          "Format alamat salah",
	"no_mx_record":            "Domain tidak menerima email",
	"dns_error":               "Domain tidak bisa dicek (DNS)",
	"disposable_domain":       "Email sementara",
	"role_based":              "Alamat peran (mis. info@, sales@)",
	"catch_all_domain":        "Domain menerima semua alamat",
	"smtp_unreachable":        "Server email tidak bisa dihubungi",
	"greylisted_or_temporary": "Server menolak sementara",
	"smtp_probe_disabled":     "Domain valid; kotak surat tidak diperiksa",
}

var statusText = map[string]string{
	"valid": "Valid", "risky": "Berisiko", "unknown": "Tidak pasti", "invalid": "Tidak valid", "disposable": "Sementara", "": "Belum dicek",
}

func yesNo(b bool) string {
	if b {
		return "ya"
	}
	return "tidak"
}

// Export writes the results as csv, xlsx or txt. TXT lists only addresses,
// one per line, which suits re-importing the clean list elsewhere.
func (s *Service) Export(ctx context.Context, wsID, id, format, filter string) (name, contentType string, body []byte, err error) {
	l, err := s.Get(ctx, wsID, id)
	if err != nil {
		return "", "", nil, err
	}
	statuses, ok := filters[filter]
	if !ok {
		return "", "", nil, apperr.Invalid("invalid_filter", "filter must be all, valid, deliverable or invalid")
	}
	if format != "csv" && format != "xlsx" && format != "txt" {
		return "", "", nil, apperr.Invalid("invalid_format", "format must be csv, xlsx or txt")
	}
	rows, err := s.db.Query(ctx, `SELECT email, first_name, last_name, company, status, reason, score, mx_host,
			disposable, role_based, free_provider, catch_all, checked_at
		FROM validation_list_items WHERE list_id=$1 AND ($2::text[] IS NULL OR status = ANY($2))
		ORDER BY row_no`, id, statuses)
	if err != nil {
		return "", "", nil, apperr.Internal(err)
	}
	table := [][]string{{"email", "nama depan", "nama belakang", "perusahaan", "status", "keterangan", "skor", "server mx",
		"email sementara", "alamat peran", "email gratis", "catch-all", "dicek"}}
	var emails []string
	for rows.Next() {
		var email, first, last, company, status, reason, mx string
		var score int
		var disp, role, free, catchAll bool
		var checked *time.Time
		if err := rows.Scan(&email, &first, &last, &company, &status, &reason, &score, &mx, &disp, &role, &free, &catchAll, &checked); err != nil {
			rows.Close()
			return "", "", nil, apperr.Internal(err)
		}
		at := ""
		if checked != nil {
			at = checked.In(jakarta).Format("2006-01-02 15:04")
		}
		explain := ReasonText[reason]
		if explain == "" {
			explain = reason
		}
		table = append(table, []string{email, first, last, company, statusText[status], explain, fmt.Sprint(score), mx,
			yesNo(disp), yesNo(role), yesNo(free), yesNo(catchAll), at})
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return "", "", nil, apperr.Internal(err)
	}

	base := strings.TrimSuffix(l.Filename, extOf(l.Filename))
	name = fmt.Sprintf("hasil-validasi-%s-%s.%s", slug(base), filter, format)
	switch format {
	case "xlsx":
		body, err = sheet.XLSX("Hasil validasi", table)
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "txt":
		body = []byte(strings.Join(emails, "\n") + "\n")
		contentType = "text/plain; charset=utf-8"
	default:
		var buf bytes.Buffer
		err = sheet.CSV(&buf, table)
		body, contentType = buf.Bytes(), "text/csv; charset=utf-8"
	}
	if err != nil {
		return "", "", nil, apperr.Internal(err)
	}
	return name, contentType, body, nil
}

var jakarta = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Jakarta"); err == nil {
		return loc
	}
	return time.FixedZone("WIB", 7*3600)
}()

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}

// slug keeps letters, digits and dashes so the name is safe in a header.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "daftar"
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Routes mounts the list validation endpoints.
func (s *Service) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("POST /v1/validations/lists", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBytes+1<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil { //nolint:gosec // G120: bounded by MaxBytesReader
			httpx.Error(w, r, apperr.Invalid("invalid_form", "expected multipart/form-data with a 'file' field (.csv, .xlsx or .txt, max 20MB)"))
			return
		}
		f, fh, err := r.FormFile("file")
		if err != nil {
			httpx.Error(w, r, apperr.Invalid("missing_file", "field 'file' is required"))
			return
		}
		defer func() { _ = f.Close() }()
		l, err := s.Upload(r.Context(), authctx.MustFrom(r.Context()), fh.Filename, f)
		httpx.Respond(w, r, http.StatusAccepted, l, err)
	})))
	mux.Handle("GET /v1/validations/lists", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls, err := s.Lists(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, httpx.PageFrom(r))
		httpx.Respond(w, r, http.StatusOK, map[string]any{"data": ls}, err)
	})))
	mux.Handle("GET /v1/validations/lists/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l, err := s.Get(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"))
		httpx.Respond(w, r, http.StatusOK, l, err)
	})))
	mux.Handle("DELETE /v1/validations/lists/{id}", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id")); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /v1/validations/lists/{id}/export", authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		format, filter := q.Get("format"), q.Get("filter")
		if format == "" {
			format = "csv"
		}
		if filter == "" {
			filter = "all"
		}
		name, ctype, body, err := s.Export(r.Context(), authctx.MustFrom(r.Context()).WorkspaceID, r.PathValue("id"), format, filter)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := w.Write(body); err != nil { //nolint:gosec // G705: file served as an attachment with nosniff, never rendered as HTML
			slog.Debug("write export", "err", err)
		}
	})))
}
