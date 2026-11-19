package sheet

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCSVDefusesFormulas(t *testing.T) {
	var b bytes.Buffer
	if err := CSV(&b, [][]string{{"email", "catatan"}, {"a@b.co.id", "=HYPERLINK(\"x\")"}, {"c@d.co.id", "-1"}}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "\ufeffemail,catatan\n") || !strings.Contains(got, `"'=HYPERLINK(""x"")"`) || !strings.Contains(got, "'-1") {
		t.Fatalf("csv = %q", got)
	}
}

func TestXLSX(t *testing.T) {
	data, err := XLSX("Hasil: validasi/2026", [][]string{{"email", "no hp"}, {"budi@maju.co.id", "0812<3>&"}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		parts[f.Name] = string(b)
	}
	sheet := parts["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sheet, `r="B2" t="inlineStr"`) || !strings.Contains(sheet, "0812&lt;3&gt;&amp;") {
		t.Fatalf("sheet = %s", sheet)
	}
	if !strings.Contains(parts["xl/workbook.xml"], `name="Hasil- validasi-2026"`) {
		t.Fatalf("workbook = %s", parts["xl/workbook.xml"])
	}
	if column(0) != "A" || column(25) != "Z" || column(26) != "AA" || column(701) != "ZZ" {
		t.Fatal("column names")
	}
}
