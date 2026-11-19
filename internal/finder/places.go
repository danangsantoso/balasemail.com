package finder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
)

// Place is a business found in a region, with whatever contact details its
// public listing has. Emails start with those published in the listing;
// crawling the website adds more.
type Place struct {
	Name      string   `json:"name"`
	Category  string   `json:"category"`
	Address   string   `json:"address,omitempty"`
	City      string   `json:"city,omitempty"`
	Phone     string   `json:"phone,omitempty"`
	Website   string   `json:"website,omitempty"`
	Emails    []string `json:"emails"`
	SourceURL string   `json:"source_url"`
	// Crawled and Note report the website visit in a search's results.
	Crawled bool   `json:"crawled"`
	Note    string `json:"note,omitempty"`
}

// PlaceSource lists businesses of a category in a region.
type PlaceSource interface {
	Places(ctx context.Context, region string, c Category, limit int) ([]Place, error)
}

// Category is a business category as shown to users, mapped to the
// OpenStreetMap tags that describe it.
type Category struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Group string   `json:"group"`
	Tags  []string `json:"-"` // key=value, OR-ed
}

// Categories is the catalog offered in the dashboard, grouped for display.
var Categories = []Category{
	{"perusahaan", "Kantor perusahaan (umum)", "Bisnis & profesional", []string{"office=company"}},
	{"konsultan", "Konsultan & manajemen", "Bisnis & profesional", []string{"office=consulting"}},
	{"hukum", "Kantor hukum & notaris", "Bisnis & profesional", []string{"office=lawyer", "office=notary"}},
	{"akuntan", "Akuntan & pajak", "Bisnis & profesional", []string{"office=accountant", "office=tax_advisor"}},
	{"it", "IT, software & telekomunikasi", "Bisnis & profesional", []string{"office=it", "office=telecommunication"}},
	{"periklanan", "Periklanan & media", "Bisnis & profesional", []string{"office=advertising_agency", "office=newspaper"}},
	{"arsitek", "Arsitek & desain interior", "Bisnis & profesional", []string{"office=architect", "shop=interior_decoration"}},
	{"asuransi", "Asuransi", "Keuangan & properti", []string{"office=insurance"}},
	{"keuangan", "Jasa keuangan", "Keuangan & properti", []string{"office=financial", "office=financial_advisor"}},
	{"properti", "Agen properti & developer", "Keuangan & properti", []string{"office=estate_agent", "office=property_management"}},
	{"pabrik", "Pabrik & manufaktur", "Industri & logistik", []string{"man_made=works", "industrial=factory"}},
	{"logistik", "Logistik & ekspedisi", "Industri & logistik", []string{"office=logistics", "office=courier", "industrial=warehouse"}},
	{"konstruksi", "Kontraktor & konstruksi", "Industri & logistik", []string{"office=construction_company", "craft=builder"}},
	{"bahan_bangunan", "Toko bahan bangunan", "Industri & logistik", []string{"shop=hardware", "shop=doityourself", "shop=trade"}},
	{"percetakan", "Percetakan", "Industri & logistik", []string{"craft=printer", "shop=copyshop"}},
	{"hotel", "Hotel & penginapan", "Pariwisata & kuliner", []string{"tourism=hotel", "tourism=guest_house"}},
	{"travel", "Agen perjalanan", "Pariwisata & kuliner", []string{"shop=travel_agency", "office=travel_agent"}},
	{"restoran", "Restoran", "Pariwisata & kuliner", []string{"amenity=restaurant"}},
	{"kafe", "Kafe & kedai kopi", "Pariwisata & kuliner", []string{"amenity=cafe"}},
	{"katering", "Katering", "Pariwisata & kuliner", []string{"craft=caterer", "shop=caterer"}},
	{"klinik", "Klinik & dokter", "Kesehatan & pendidikan", []string{"amenity=clinic", "amenity=doctors", "amenity=dentist"}},
	{"rumah_sakit", "Rumah sakit", "Kesehatan & pendidikan", []string{"amenity=hospital"}},
	{"apotek", "Apotek", "Kesehatan & pendidikan", []string{"amenity=pharmacy"}},
	{"sekolah", "Sekolah", "Kesehatan & pendidikan", []string{"amenity=school"}},
	{"kampus", "Universitas & kursus", "Kesehatan & pendidikan", []string{"amenity=university", "amenity=college", "amenity=language_school"}},
	{"otomotif", "Dealer & bengkel kendaraan", "Ritel & jasa", []string{"shop=car", "shop=car_repair", "shop=motorcycle"}},
	{"elektronik", "Toko elektronik & komputer", "Ritel & jasa", []string{"shop=electronics", "shop=computer", "shop=mobile_phone"}},
	{"furnitur", "Furnitur", "Ritel & jasa", []string{"shop=furniture"}},
	{"salon", "Salon & kecantikan", "Ritel & jasa", []string{"shop=hairdresser", "shop=beauty"}},
	{"kebugaran", "Gym & kebugaran", "Ritel & jasa", []string{"leisure=fitness_centre"}},
	{"coworking", "Coworking space", "Ritel & jasa", []string{"amenity=coworking_space", "office=coworking"}},
	{"organisasi", "Asosiasi & LSM", "Ritel & jasa", []string{"office=association", "office=ngo", "office=foundation"}},
}

// CategoryByKey looks a category up.
func CategoryByKey(key string) (Category, bool) {
	i := slices.IndexFunc(Categories, func(c Category) bool { return c.Key == key })
	if i < 0 {
		return Category{}, false
	}
	return Categories[i], true
}

// Regions suggested in the dashboard; any administrative area name works.
var Regions = []string{
	"DKI Jakarta", "Jakarta Pusat", "Jakarta Selatan", "Jakarta Barat", "Jakarta Timur", "Jakarta Utara",
	"Bogor", "Depok", "Tangerang", "Tangerang Selatan", "Bekasi", "Bandung", "Cirebon", "Semarang", "Solo", "Yogyakarta",
	"Surabaya", "Malang", "Sidoarjo", "Denpasar", "Badung", "Medan", "Pekanbaru", "Batam", "Padang", "Palembang",
	"Bandar Lampung", "Pontianak", "Balikpapan", "Samarinda", "Banjarmasin", "Makassar", "Manado", "Jayapura",
	"Jawa Barat", "Jawa Tengah", "Jawa Timur", "Banten", "Bali", "Sumatera Utara", "Sulawesi Selatan", "Kalimantan Timur",
}

// regionRe keeps region names to letters, digits, spaces and . ' - so they
// can be placed in an Overpass query safely.
var regionRe = regexp.MustCompile(`^[\p{L}0-9][\p{L}0-9 .'\-]{1,59}$`)

// CleanRegion validates a region name typed by the user.
func CleanRegion(r string) (string, error) {
	r = strings.Join(strings.Fields(r), " ")
	if !regionRe.MatchString(r) {
		return "", apperr.Invalid("invalid_region", "region must be a city, regency or province name (letters, digits, spaces)")
	}
	return r, nil
}

// Overpass queries the OpenStreetMap Overpass API. Data © OpenStreetMap
// contributors, available under the Open Database License.
type Overpass struct {
	Endpoint  string // e.g. https://overpass-api.de/api/interpreter
	Client    *http.Client
	UserAgent string
}

// Query builds the Overpass QL for businesses of c inside region that publish
// a website or an email address.
func (o *Overpass) Query(region string, c Category, limit int) string {
	// Areas are matched with or without their administrative prefix (Kota Bandung, Kabupaten Bogor, Provinsi Bali).
	name := regexp.QuoteMeta(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(region, "Kota "), "Kabupaten ")))
	name = strings.ReplaceAll(name, `\`, `\\`) // QL string escape
	var b strings.Builder
	fmt.Fprintf(&b, "[out:json][timeout:90];\n")
	fmt.Fprintf(&b, "area[\"boundary\"=\"administrative\"][\"name\"~\"^((Kota|Kabupaten|Kab\\\\.|Provinsi|Daerah Khusus Ibukota) )?%s$\",i]->.a;\n(\n", name)
	for _, t := range c.Tags {
		k, v, _ := strings.Cut(t, "=")
		fmt.Fprintf(&b, "  nwr[%q=%q](area.a)[~\"^(website|contact:website|url|email|contact:email)$\"~\".\"];\n", k, v)
	}
	fmt.Fprintf(&b, ");\nout tags center %d;\n", limit)
	return b.String()
}

type overpassResponse struct {
	Elements []struct {
		Type string            `json:"type"`
		ID   int64             `json:"id"`
		Tags map[string]string `json:"tags"`
	} `json:"elements"`
	Remark string `json:"remark"`
}

func (o *Overpass) Places(ctx context.Context, region string, c Category, limit int) ([]Place, error) {
	form := url.Values{"data": {o.Query(region, c, limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", o.UserAgent)
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("overpass: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusGatewayTimeout {
		return nil, fmt.Errorf("overpass busy: %s", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass: %s", resp.Status)
	}
	var out overpassResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 20<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("overpass: decode: %w", err)
	}
	if strings.Contains(out.Remark, "runtime error") {
		return nil, fmt.Errorf("overpass: %s", out.Remark)
	}
	var places []Place
	seen := map[string]bool{}
	for _, e := range out.Elements {
		p := placeFromTags(e.Tags, c, region)
		if p.Name == "" {
			continue
		}
		// One business mapped as both a point and a building shows up twice.
		dedupe := strings.ToLower(p.Name + "|" + p.Website)
		if seen[dedupe] {
			continue
		}
		seen[dedupe] = true
		p.SourceURL = fmt.Sprintf("https://www.openstreetmap.org/%s/%d", e.Type, e.ID)
		places = append(places, p)
		if len(places) == limit {
			break
		}
	}
	return places, nil
}

func first(tags map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(tags[k]); v != "" {
			return v
		}
	}
	return ""
}

func placeFromTags(t map[string]string, c Category, region string) Place {
	p := Place{
		Name:     first(t, "name", "name:id", "brand", "operator"),
		Category: c.Label,
		Phone:    first(t, "phone", "contact:phone", "contact:mobile", "mobile"),
		Website:  normalizeWebsite(first(t, "website", "contact:website", "url")),
		City:     first(t, "addr:city", "addr:regency", "addr:district"),
		Emails:   []string{},
	}
	if p.City == "" {
		p.City = region
	}
	street := strings.TrimSpace(first(t, "addr:street") + " " + first(t, "addr:housenumber"))
	p.Address = strings.TrimSpace(strings.Join(slices.DeleteFunc([]string{street, first(t, "addr:suburb", "addr:subdistrict"), first(t, "addr:postcode")},
		func(s string) bool { return s == "" }), ", "))
	for _, raw := range strings.FieldsFunc(first(t, "email", "contact:email"), func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
		if e, err := emailaddr.Normalize(strings.TrimPrefix(raw, "mailto:")); err == nil && !slices.Contains(p.Emails, e) {
			p.Emails = append(p.Emails, e)
		}
	}
	return p
}

// normalizeWebsite returns an absolute http(s) URL or "".
func normalizeWebsite(w string) string {
	w = strings.TrimSpace(strings.Split(w, ";")[0])
	if w == "" {
		return ""
	}
	if !strings.Contains(w, "://") {
		w = "https://" + w
	}
	u, err := url.Parse(w)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	return u.String()
}

// notCompanySites host business pages but not a company's own site: crawling
// them would collect the platform's addresses, not the business's.
var notCompanySites = []string{
	"facebook.com", "fb.com", "instagram.com", "twitter.com", "x.com", "tiktok.com", "youtube.com", "linkedin.com",
	"wa.me", "whatsapp.com", "linktr.ee", "bit.ly", "google.com", "goo.gl", "g.page", "business.site", "tokopedia.com",
	"shopee.co.id", "bukalapak.com", "blibli.com", "lazada.co.id", "gofood.co.id", "grab.com", "traveloka.com",
	"tripadvisor.com", "booking.com", "agoda.com", "wikipedia.org", "blogspot.com", "wordpress.com", "wixsite.com",
}

// CompanyDomain returns the domain to crawl for a website, or "" when the
// site is a social network, marketplace or other shared platform.
func CompanyDomain(website string) string {
	u, err := url.Parse(website)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if !emailaddr.ValidDomain(host) || emailaddr.IsFreeProvider(host) {
		return ""
	}
	for _, s := range notCompanySites {
		if host == s || strings.HasSuffix(host, "."+s) {
			return ""
		}
	}
	return host
}
