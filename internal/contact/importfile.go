package contact

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
)

// MaxImportRows bounds a synchronous import; MaxImportBytes bounds the upload.
const (
	MaxImportRows  = 100_000
	MaxImportBytes = 50 << 20
)

// Column aliases accepted in headers (English and Indonesian).
var columnAliases = map[string]string{
	"email": "email", "e-mail": "email", "alamat email": "email", "surel": "email",
	"first_name": "first_name", "firstname": "first_name", "first name": "first_name", "nama depan": "first_name", "nama": "first_name",
	"last_name": "last_name", "lastname": "last_name", "last name": "last_name", "nama belakang": "last_name",
	"company": "company", "perusahaan": "company", "organization": "company", "nama perusahaan": "company",
	"title": "title", "job title": "title", "jabatan": "title", "position": "title",
	"phone": "phone", "telepon": "phone", "hp": "phone", "no hp": "phone", "no. hp": "phone", "mobile": "phone", "whatsapp": "phone",
	"tags": "tags", "tag": "tags",
}

// ParseFile detects the format (xlsx, delimited text, or free text) and returns contact inputs.
func ParseFile(filename string, data []byte, defaultTags []string) ([]Input, []RowError, string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0}) || ext == ".xls":
		return nil, nil, "", apperr.Invalid("unsupported_xls", "old Excel .xls files are not supported; save the file as .xlsx (Excel Workbook) or CSV")
	case bytes.HasPrefix(data, []byte("PK\x03\x04")) || ext == ".xlsx":
		rows, err := readXLSX(data)
		if err != nil {
			return nil, nil, "xlsx", apperr.Invalid("invalid_xlsx", "the Excel file could not be read: "+err.Error())
		}
		table := make([][]string, len(rows))
		lines := make([]int, len(rows))
		for i, r := range rows {
			table[i], lines[i] = r.cells, r.num
		}
		ins, errs, err := fromTable(table, lines, defaultTags)
		return ins, errs, "xlsx", err
	}

	text, err := decodeText(data)
	if err != nil {
		return nil, nil, "", err
	}
	if delim, ok := detectDelimited(text); ok {
		ins, errs, err := parseDelimited(strings.NewReader(text), delim, defaultTags)
		format := "csv"
		if delim == '\t' {
			format = "tsv"
		}
		return ins, errs, format, err
	}
	ins, errs, err := parseFreeText(text, defaultTags)
	return ins, errs, "txt", err
}

// ParseCSV parses comma-separated data with a header row (kept for callers and tests).
func ParseCSV(r io.Reader, defaultTags []string) ([]Input, []RowError, error) {
	return parseDelimited(r, ',', defaultTags)
}

// decodeText handles UTF-8 (with or without BOM) and UTF-16 exports from Excel/Notepad.
func decodeText(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		le := data[0] == 0xFF
		data = data[2:]
		u := make([]uint16, len(data)/2)
		for i := range u {
			if le {
				u[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
			} else {
				u[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
			}
		}
		return string(utf16.Decode(u)), nil
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) {
		// Legacy Windows-1252/Latin-1 exports: map bytes 1:1 to runes.
		r := make([]rune, len(data))
		for i, b := range data {
			r[i] = rune(b)
		}
		return string(r), nil
	}
	return string(data), nil
}

// detectDelimited reports whether the first non-empty line is a header row
// containing an email column, and which delimiter it uses.
func detectDelimited(text string) (rune, bool) {
	var first string
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) != "" {
			first = strings.TrimSpace(line)
			break
		}
	}
	best, bestN := rune(0), 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if n := strings.Count(first, string(d)); n > bestN {
			best, bestN = d, n
		}
	}
	cols := []string{first}
	if best != 0 {
		cols = strings.Split(first, string(best))
	}
	for _, c := range cols {
		if columnAliases[normalizeHeader(c)] == "email" {
			return cmpDelim(best), true
		}
	}
	return 0, false
}

func cmpDelim(d rune) rune {
	if d == 0 {
		return ','
	}
	return d
}

func normalizeHeader(h string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(h), "\"'"))
}

func parseDelimited(r io.Reader, delim rune, defaultTags []string) ([]Input, []RowError, error) {
	cr := csv.NewReader(r)
	cr.Comma = delim
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	cr.LazyQuotes = true
	var table [][]string
	var lines []int
	var rowErrs []RowError
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			rowErrs = append(rowErrs, RowError{Row: line, Error: err.Error()})
			continue
		}
		if blank(rec) {
			continue
		}
		table = append(table, rec)
		lines = append(lines, line)
	}
	if len(table) > 0 {
		table[0][0] = strings.TrimPrefix(table[0][0], "\ufeff")
	}
	ins, errs, err := fromTable(table, lines, defaultTags)
	return ins, append(rowErrs, errs...), err
}

// fromTable maps a header row + data rows to inputs. Unknown columns become custom fields.
func fromTable(table [][]string, lines []int, defaultTags []string) ([]Input, []RowError, error) {
	if len(table) == 0 {
		return nil, nil, apperr.Invalid("empty_file", "the file contains no rows")
	}
	header := table[0]
	mapping := make([]string, len(header))
	hasEmail := false
	for i, h := range header {
		key := normalizeHeader(h)
		if canon, ok := columnAliases[key]; ok {
			mapping[i] = canon
		} else if key != "" {
			mapping[i] = "field:" + strings.ReplaceAll(key, " ", "_")
		}
		hasEmail = hasEmail || mapping[i] == "email"
	}
	if !hasEmail {
		return nil, nil, apperr.Invalid("invalid_csv", "the first row must be a header with an 'email' column")
	}
	var inputs []Input
	var rowErrs []RowError
	seen := map[string]bool{}
	for k, rec := range table[1:] {
		line := lines[k+1]
		if len(inputs) >= MaxImportRows {
			return nil, nil, apperr.Invalid("too_many_rows", fmt.Sprintf("import is limited to %d rows", MaxImportRows))
		}
		in := Input{Fields: map[string]string{}, Tags: slices.Clone(defaultTags), Source: "import"}
		for i, val := range rec {
			if i >= len(mapping) || mapping[i] == "" {
				continue
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
		if seen[in.Email] {
			rowErrs = append(rowErrs, RowError{Row: line, Error: "duplicate email " + in.Email})
			continue
		}
		seen[in.Email] = true
		inputs = append(inputs, in)
	}
	return inputs, rowErrs, nil
}

var looseEmailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,24}`)

// parseFreeText accepts one address per line ("budi@x.id", "Budi Santoso <budi@x.id>")
// or any text containing addresses; lines starting with # are comments.
func parseFreeText(text string, defaultTags []string) ([]Input, []RowError, error) {
	var inputs []Input
	var rowErrs []RowError
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		found := false
		// "Nama <email>" entries keep the display name.
		if strings.Contains(line, "<") {
			if list, err := mail.ParseAddressList(line); err == nil {
				for _, a := range list {
					inputs, rowErrs = addFree(inputs, rowErrs, seen, i+1, a.Address, a.Name, defaultTags)
				}
				continue
			}
		}
		for _, m := range looseEmailRe.FindAllString(line, -1) {
			found = true
			inputs, rowErrs = addFree(inputs, rowErrs, seen, i+1, m, "", defaultTags)
		}
		if !found {
			rowErrs = append(rowErrs, RowError{Row: i + 1, Error: fmt.Sprintf("no email address found in %q", truncate(line, 60))})
		}
		if len(inputs) > MaxImportRows {
			return nil, nil, apperr.Invalid("too_many_rows", fmt.Sprintf("import is limited to %d rows", MaxImportRows))
		}
	}
	if len(inputs) == 0 && len(rowErrs) == 0 {
		return nil, nil, apperr.Invalid("empty_file", "the file contains no email addresses")
	}
	return inputs, rowErrs, nil
}

func addFree(inputs []Input, errs []RowError, seen map[string]bool, line int, email, name string, tags []string) ([]Input, []RowError) {
	in := Input{Email: email, Tags: slices.Clone(tags), Source: "import"}
	if name != "" {
		parts := strings.Fields(name)
		in.FirstName = parts[0]
		if len(parts) > 1 {
			in.LastName = strings.Join(parts[1:], " ")
		}
	}
	if err := normalizeInput(&in); err != nil {
		return inputs, append(errs, RowError{Row: line, Error: apperr.As(err).Message})
	}
	if seen[in.Email] {
		return inputs, append(errs, RowError{Row: line, Error: "duplicate email " + in.Email})
	}
	seen[in.Email] = true
	return append(inputs, in), errs
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
