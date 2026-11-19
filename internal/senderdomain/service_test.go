package senderdomain

import (
	"context"
	"testing"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return f[name], nil
}

func TestCheckDNS(t *testing.T) {
	d := Domain{Name: "acme.co.id", VerificationToken: "tok", DKIMSelector: "bm1", DKIMPublicKey: "PUBKEY"}
	r := fakeResolver{
		"_balasemail.acme.co.id":    {"balasemail-verification=tok"},
		"acme.co.id":                {"google-site-verification=x", "v=spf1 include:_spf.google.com include:_spf.balasemail.com ~all"},
		"bm1._domainkey.acme.co.id": {"v=DKIM1; k=rsa; p=PUBKEY"},
	}
	got := map[string]bool{}
	for _, rec := range CheckDNS(context.Background(), r, d, "_spf.balasemail.com") {
		got[rec.Purpose] = rec.OK
	}
	want := map[string]bool{"verification": true, "spf": true, "dkim": true, "dmarc": false}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestCheckDNSRejectsWrongValues(t *testing.T) {
	d := Domain{Name: "acme.id", VerificationToken: "tok", DKIMSelector: "bm1", DKIMPublicKey: "PUBKEY"}
	r := fakeResolver{
		"_balasemail.acme.id":    {"balasemail-verification=other"},
		"acme.id":                {"v=spf1 include:_spf.google.com ~all"},
		"bm1._domainkey.acme.id": {"v=DKIM1; k=rsa; p=OTHER"},
		"_dmarc.acme.id":         {"v=DMARC1; p=reject"},
	}
	for _, rec := range CheckDNS(context.Background(), r, d, "_spf.balasemail.com") {
		if rec.Purpose != "dmarc" && rec.OK {
			t.Errorf("%s should fail", rec.Purpose)
		}
		if rec.Purpose == "dmarc" && !rec.OK {
			t.Error("dmarc should pass")
		}
	}
}
