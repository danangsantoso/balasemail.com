package contact

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// A minimal, dependency-free reader for the first worksheet of an .xlsx file
// (Office Open XML). It understands shared strings, inline strings, booleans
// and numbers — everything a contact list needs — and enforces size limits so
// a crafted file (zip bomb) cannot exhaust memory.

const (
	maxXLSXPartBytes = 64 << 20 // uncompressed size of any single XML part
	maxXLSXColumns   = 200
)

var errNotXLSX = errors.New("file is not a valid .xlsx workbook")

// xlsxRow is one non-empty worksheet row with its 1-based spreadsheet row number.
type xlsxRow struct {
	num   int
	cells []string
}

func readXLSX(data []byte) ([]xlsxRow, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errNotXLSX
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.TrimPrefix(f.Name, "/")] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
		if f.UncompressedSize64 > maxXLSXPartBytes {
			return nil, errors.New("worksheet is too large")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, maxXLSXPartBytes+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxXLSXPartBytes {
			return nil, errors.New("worksheet is too large")
		}
		return b, nil
	}

	sheetPath, err := firstSheetPath(read)
	if err != nil {
		return nil, err
	}
	var shared []string
	if b, err := read("xl/sharedStrings.xml"); err == nil {
		if shared, err = parseSharedStrings(b); err != nil {
			return nil, errNotXLSX
		}
	}
	sheet, err := read(sheetPath)
	if err != nil {
		return nil, errNotXLSX
	}
	return parseSheet(sheet, shared)
}

// firstSheetPath resolves the first <sheet> in workbook.xml through its relationship.
func firstSheetPath(read func(string) ([]byte, error)) (string, error) {
	const fallback = "xl/worksheets/sheet1.xml"
	wb, err := read("xl/workbook.xml")
	if err != nil {
		return "", errNotXLSX
	}
	var workbook struct {
		Sheets []struct {
			RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wb, &workbook); err != nil || len(workbook.Sheets) == 0 {
		return fallback, nil
	}
	rels, err := read("xl/_rels/workbook.xml.rels")
	if err != nil {
		return fallback, nil
	}
	var relationships struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(rels, &relationships); err != nil {
		return fallback, nil
	}
	for _, r := range relationships.Rel {
		if r.ID == workbook.Sheets[0].RID {
			if strings.HasPrefix(r.Target, "/") {
				return strings.TrimPrefix(r.Target, "/"), nil
			}
			return path.Join("xl", r.Target), nil
		}
	}
	return fallback, nil
}

// parseSharedStrings concatenates the text runs of each <si>, skipping phonetic hints.
func parseSharedStrings(b []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []string
	var cur strings.Builder
	inSI, inT, inPhonetic := false, false, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "rPh":
				inPhonetic = true
			case "t":
				inT = inSI && !inPhonetic
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				inSI = false
				out = append(out, cur.String())
			case "rPh":
				inPhonetic = false
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
}

// colIndex converts the letters of a cell reference ("C12") to a 0-based column.
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}

func parseSheet(b []byte, shared []string) ([]xlsxRow, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var rows []xlsxRow
	var row *xlsxRow
	var cellType, cellRef string
	var value strings.Builder
	nextCol, rowNum := 0, 0
	inValue := false

	flushCell := func() {
		col := nextCol
		if cellRef != "" {
			if c := colIndex(cellRef); c >= 0 {
				col = c
			}
		}
		nextCol = col + 1
		if col >= maxXLSXColumns {
			return
		}
		v := value.String()
		switch cellType {
		case "s":
			if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && i >= 0 && i < len(shared) {
				v = shared[i]
			} else {
				v = ""
			}
		case "b":
			v = map[string]string{"1": "TRUE", "0": "FALSE"}[strings.TrimSpace(v)]
		case "", "n":
			v = formatNumber(strings.TrimSpace(v))
		}
		for len(row.cells) <= col {
			row.cells = append(row.cells, "")
		}
		row.cells[col] = v
	}

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, errNotXLSX
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				rowNum++
				for _, a := range t.Attr {
					if a.Name.Local == "r" {
						if n, err := strconv.Atoi(a.Value); err == nil {
							rowNum = n
						}
					}
				}
				row = &xlsxRow{num: rowNum}
				nextCol = 0
			case "c":
				cellType, cellRef = "", ""
				value.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						cellType = a.Value
					case "r":
						cellRef = a.Value
					}
				}
			case "v", "t":
				inValue = row != nil
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				if row != nil {
					flushCell()
				}
			case "row":
				if row != nil && !blank(row.cells) {
					rows = append(rows, *row)
				}
				row = nil
			}
		case xml.CharData:
			if inValue {
				value.Write(t)
			}
		}
	}
}

// formatNumber renders numeric cells without scientific notation (phone numbers).
func formatNumber(v string) string {
	if v == "" || !strings.ContainsAny(v, "eE.") {
		return v
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return v
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func blank(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}
