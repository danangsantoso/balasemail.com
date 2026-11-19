package contact

import (
	"strings"
	"testing"
)

func TestParseCSV(t *testing.T) {
	data := "\ufeffEmail,Nama Depan,Perusahaan,Jabatan,Kota,Tags\n" +
		"Budi@Maju.co.id,Budi,PT Maju,Direktur,Jakarta,vip;b2b\n" +
		"bukan-email,X,Y,Z,,\n" +
		"sari@sejahtera.id,Sari,CV Sejahtera,,Bandung,\n"
	inputs, errs, err := ParseCSV(strings.NewReader(data), []string{"Import-Okt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 || len(errs) != 1 || errs[0].Row != 3 {
		t.Fatalf("inputs=%d errs=%+v", len(inputs), errs)
	}
	b := inputs[0]
	if b.Email != "budi@maju.co.id" || b.FirstName != "Budi" || b.Company != "PT Maju" || b.Title != "Direktur" {
		t.Errorf("mapping wrong: %+v", b)
	}
	if b.Fields["kota"] != "Jakarta" {
		t.Errorf("custom field missing: %+v", b.Fields)
	}
	if strings.Join(b.Tags, ",") != "import-okt,vip,b2b" {
		t.Errorf("tags = %v", b.Tags)
	}
}

func TestParseCSVRequiresEmailColumn(t *testing.T) {
	if _, _, err := ParseCSV(strings.NewReader("name,company\nA,B\n"), nil); err == nil {
		t.Fatal("expected error without email column")
	}
}

func TestVars(t *testing.T) {
	c := Contact{FirstName: "Budi", LastName: "Santoso", Title: "CTO", Fields: map[string]string{"kota": "Solo", "email": "override"}}
	v := c.Vars()
	if v["full_name"] != "Budi Santoso" || v["jabatan"] != "CTO" || v["kota"] != "Solo" || v["email"] != "" {
		t.Errorf("vars = %v", v)
	}
}
