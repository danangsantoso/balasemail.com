package contact

import (
	"archive/zip"
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

// The downloadable samples must always import cleanly.
func TestSampleFilesImport(t *testing.T) {
	for _, name := range []string{"kontak-contoh.csv", "kontak-contoh.xlsx", "kontak-contoh.txt"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../web/static/contoh/" + name)
			if err != nil {
				t.Fatal(err)
			}
			ins, errs, _, err := ParseFile(name, data, []string{"contoh"})
			if err != nil || len(errs) != 0 || len(ins) != 5 {
				t.Fatalf("inputs=%d errs=%v err=%v", len(ins), errs, err)
			}
			if !slices.Contains(ins[0].Tags, "contoh") {
				t.Errorf("default tag missing: %v", ins[0].Tags)
			}
			if name == "kontak-contoh.txt" {
				if ins[2].FirstName != "Andi" || ins[2].LastName != "Pratama" {
					t.Errorf("name from 'Nama <email>' not kept: %+v", ins[2])
				}
				return
			}
			b := ins[0]
			if b.Email != "budi.santoso@example.com" || b.FirstName != "Budi" || b.Company != "PT Maju Jaya" ||
				b.Title != "Direktur Utama" || b.Phone != "081234567890" || b.Fields["kota"] != "Jakarta" {
				t.Errorf("row mapping: %+v", b)
			}
			if !slices.Contains(ins[2].Tags, "vip") {
				t.Errorf("tags column not split: %v", ins[2].Tags)
			}
		})
	}
}

// LibreOffice writes shared strings and stores phone numbers as numbers.
func TestXLSXFromLibreOffice(t *testing.T) {
	data, err := os.ReadFile("testdata/libreoffice.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	ins, errs, format, err := ParseFile("export.xlsx", data, nil)
	if err != nil || format != "xlsx" || len(ins) != 5 || len(errs) != 0 {
		t.Fatalf("inputs=%d errs=%v err=%v", len(ins), errs, err)
	}
	if ins[3].Email != "dewi.lestari@example.com" || ins[3].Company != "PT Sinar Logistik" {
		t.Errorf("row 4: %+v", ins[3])
	}
	if p := ins[0].Phone; p != "081234567890" && p != "81234567890" {
		t.Errorf("phone = %q", p)
	}
}

func TestSemicolonCSVFromIndonesianExcel(t *testing.T) {
	data := "\xEF\xBB\xBFAlamat Email;Nama;Perusahaan\r\nbudi@example.com;Budi;PT Maju\r\n;;\r\nsalah;X;Y\r\nBUDI@example.com;Budi;dup\r\n"
	ins, errs, format, err := ParseFile("kontak.csv", []byte(data), nil)
	if err != nil || format != "csv" {
		t.Fatal(err, format)
	}
	if len(ins) != 1 || ins[0].Company != "PT Maju" || len(errs) != 2 {
		t.Fatalf("inputs=%+v errs=%v", ins, errs)
	}
	if errs[0].Row != 4 || !strings.Contains(errs[1].Error, "duplicate") {
		t.Errorf("errors = %+v", errs)
	}
}

func TestFreeTextTXT(t *testing.T) {
	data := "Daftar dari pameran:\nbudi@example.com, sari@example.net; andi@example.org\n# komentar\n\nhubungi: dewi@example.com (Direktur)\nbudi@example.com\n"
	ins, errs, format, err := ParseFile("daftar.txt", []byte(data), nil)
	if err != nil || format != "txt" {
		t.Fatal(err, format)
	}
	var got []string
	for _, in := range ins {
		got = append(got, in.Email)
	}
	if !slices.Equal(got, []string{"budi@example.com", "sari@example.net", "andi@example.org", "dewi@example.com"}) {
		t.Fatalf("emails = %v", got)
	}
	// "Daftar dari pameran:" has no address; the repeated budi is a duplicate.
	if len(errs) != 2 || errs[0].Row != 1 || !strings.Contains(errs[1].Error, "duplicate") {
		t.Errorf("errs = %+v", errs)
	}
}

func TestUTF16TextFromNotepad(t *testing.T) {
	s := "email\r\nbudi@example.com\r\n"
	b := []byte{0xFF, 0xFE}
	for _, r := range s {
		b = append(b, byte(r), 0)
	}
	ins, _, _, err := ParseFile("kontak.txt", b, nil)
	if err != nil || len(ins) != 1 || ins[0].Email != "budi@example.com" {
		t.Fatalf("got %+v %v", ins, err)
	}
}

func TestRejectsOldXLSAndBrokenXLSX(t *testing.T) {
	if _, _, _, err := ParseFile("lama.xls", []byte{0xD0, 0xCF, 0x11, 0xE0, 0, 0}, nil); err == nil || !strings.Contains(err.Error(), ".xlsx") {
		t.Errorf("xls: %v", err)
	}
	if _, _, _, err := ParseFile("rusak.xlsx", []byte("PK\x03\x04garbage"), nil); err == nil {
		t.Error("broken xlsx must fail")
	}
	if _, _, _, err := ParseFile("kosong.txt", []byte("\n# hanya komentar\n"), nil); err == nil {
		t.Error("file without addresses must fail")
	}
}

// Hand-built workbook: inline strings, sparse cells, numbers in scientific notation.
func TestXLSXInlineAndSparseCells(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	add("xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId7"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId7" Type="worksheet" Target="worksheets/data.xml"/></Relationships>`)
	add("xl/worksheets/data.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
		<row r="1"><c r="A1" t="inlineStr"><is><t>Email</t></is></c><c r="C1" t="inlineStr"><is><t>HP</t></is></c></row>
		<row r="3"><c r="A3" t="inlineStr"><is><t>budi@example.com</t></is></c><c r="C3"><v>6.28123456789E+12</v></c></row>
	</sheetData></worksheet>`)
	_ = zw.Close()
	ins, errs, _, err := ParseFile("x.xlsx", buf.Bytes(), nil)
	if err != nil || len(errs) != 0 || len(ins) != 1 {
		t.Fatalf("got %+v %v %v", ins, errs, err)
	}
	if ins[0].Phone != "6281234567890" {
		t.Errorf("phone = %q", ins[0].Phone)
	}
}
