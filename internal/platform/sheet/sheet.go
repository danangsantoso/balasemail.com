// Package sheet writes tabular exports as CSV or as a minimal Excel workbook
// (.xlsx, one sheet, inline strings) without third-party dependencies.
package sheet

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// CSV writes rows with a UTF-8 BOM so Excel opens accented text correctly.
// Cells that a spreadsheet would run as a formula are prefixed with a quote.
func CSV(w io.Writer, rows [][]string) error {
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	for _, r := range rows {
		safe := make([]string, len(r))
		for i, c := range r {
			safe[i] = defuse(c)
		}
		if err := cw.Write(safe); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// defuse neutralises spreadsheet formula injection (=, +, -, @, tab, CR).
func defuse(c string) string {
	if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
		return "'" + c
	}
	return c
}

// XLSX builds a workbook with one sheet named name. The first row is styled
// as a bold header and frozen.
func XLSX(name string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct{ path, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + escape(sheetName(name)) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
		{"xl/worksheets/sheet1.xml", sheetXML(rows)},
	}
	for _, f := range files {
		w, err := zw.Create(f.path)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sheetXML(rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(rows) > 1 {
		b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	}
	if len(rows) > 0 && len(rows[0]) > 0 {
		fmt.Fprintf(&b, `<cols><col min="1" max="%d" width="22" customWidth="1"/></cols>`, len(rows[0]))
	}
	b.WriteString(`<sheetData>`)
	for i, r := range rows {
		fmt.Fprintf(&b, `<row r="%d">`, i+1)
		for j, c := range r {
			style := ""
			if i == 0 {
				style = ` s="1"`
			}
			// Inline strings keep every value as text: leading zeros in phone numbers survive.
			fmt.Fprintf(&b, `<c r="%s%d" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, column(j), i+1, style, escape(c))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

// column converts a zero-based index to a column name: 0 → A, 26 → AA.
func column(i int) string {
	name := ""
	for i++; i > 0; i = (i - 1) / 26 {
		name = string(rune('A'+(i-1)%26)) + name
	}
	return name
}

func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1 // control characters are not allowed in XML
		}
		return r
	}, s)))
	return b.String()
}

// sheetName applies Excel's rules: at most 31 characters, none of []:*?/\.
func sheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	if s == "" {
		s = "Sheet1"
	}
	return s
}
