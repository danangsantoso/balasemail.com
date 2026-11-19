package finder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCleanRegion(t *testing.T) {
	for in, want := range map[string]string{"  Jakarta   Selatan ": "Jakarta Selatan", "Kab. Bogor": "Kab. Bogor", "Bandung": "Bandung"} {
		if got, err := CleanRegion(in); err != nil || got != want {
			t.Errorf("CleanRegion(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "x", `Bandung"]; out;`, "Jakarta\\", strings.Repeat("a", 80)} {
		if _, err := CleanRegion(bad); err == nil {
			t.Errorf("CleanRegion(%q) should fail", bad)
		}
	}
}

func TestOverpassQuery(t *testing.T) {
	c, _ := CategoryByKey("hukum")
	q := (&Overpass{}).Query("Kota Bandung", c, 25)
	for _, want := range []string{
		`["name"~"^((Kota|Kabupaten|Kab\\.|Provinsi|Daerah Khusus Ibukota) )?Bandung$",i]->.a;`,
		`nwr["office"="lawyer"](area.a)`, `nwr["office"="notary"](area.a)`,
		`[~"^(website|contact:website|url|email|contact:email)$"~"."]`, "out tags center 25;",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query is missing %s:\n%s", want, q)
		}
	}
	if q := (&Overpass{}).Query("Kab. Bogor", c, 5); !strings.Contains(q, `Kab\\. Bogor$`) {
		t.Errorf("dots must be escaped for the regex inside the QL string:\n%s", q)
	}
}

func TestOverpassPlaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !strings.Contains(r.Form.Get("data"), `"amenity"="restaurant"`) || r.UserAgent() != "BalasEmailBot/test" {
			http.Error(w, "bad query", 400)
			return
		}
		_, _ = w.Write([]byte(`{"elements":[
			{"type":"node","id":1,"tags":{"name":"Rumah Makan Sunda","website":"rmsunda.co.id","email":"info@rmsunda.co.id; mailto:Sales@RMSunda.co.id",
				"addr:street":"Jl. Braga","addr:housenumber":"12","addr:city":"Bandung","phone":"+62 22 123"}},
			{"type":"way","id":2,"tags":{"name":"Rumah Makan Sunda","website":"rmsunda.co.id"}},
			{"type":"node","id":3,"tags":{"name":"Kedai Instagram","contact:website":"https://instagram.com/kedai"}},
			{"type":"node","id":4,"tags":{"website":"https://noname.co.id"}}
		]}`))
	}))
	defer srv.Close()
	c, _ := CategoryByKey("restoran")
	places, err := (&Overpass{Endpoint: srv.URL, UserAgent: "BalasEmailBot/test"}).Places(context.Background(), "Bandung", c, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 2 {
		t.Fatalf("places = %+v (want the duplicate and the nameless one dropped)", places)
	}
	p := places[0]
	if p.Website != "https://rmsunda.co.id" || p.City != "Bandung" || p.Address != "Jl. Braga 12" || p.SourceURL != "https://www.openstreetmap.org/node/1" ||
		len(p.Emails) != 2 || p.Emails[1] != "sales@rmsunda.co.id" || p.Category != "Restoran" {
		t.Fatalf("place = %+v", p)
	}
	if places[1].City != "Bandung" {
		t.Errorf("city falls back to the searched region, got %q", places[1].City)
	}
}

func TestCompanyDomain(t *testing.T) {
	for site, want := range map[string]string{
		"https://www.maju.co.id/kontak": "maju.co.id", "https://instagram.com/kedai": "", "https://m.facebook.com/x": "",
		"https://linktr.ee/x": "", "https://toko.blogspot.com": "", "https://gmail.com": "", "": "",
	} {
		if got := CompanyDomain(site); got != want {
			t.Errorf("CompanyDomain(%q) = %q, want %q", site, got, want)
		}
	}
	if u, _ := url.Parse(normalizeWebsite("javascript:alert(1)")); u.String() != "" {
		t.Error("only http(s) websites are kept")
	}
}
