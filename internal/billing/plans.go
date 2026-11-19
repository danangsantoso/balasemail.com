// Package billing defines the subscription plans, enforces their limits and
// lets tenants ask for an upgrade. Plans are the single source of truth for
// the landing page, the dashboard and the platform console.
package billing

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Unlimited marks a plan limit that is not enforced.
const Unlimited = -1

// Plan keys stored in workspaces.plan.
const (
	Trial      = "trial"
	Starter    = "starter"
	Growth     = "growth"
	Business   = "business"
	Enterprise = "enterprise"
)

// Plan is one subscription tier. Prices are in rupiah; PriceMonthly is 0 for
// the free trial and -1 when the price is negotiated.
type Plan struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Tagline      string `json:"tagline"`
	PriceMonthly int    `json:"price_monthly"`
	PriceYearly  int    `json:"price_yearly"` // total for 12 months
	TrialDays    int    `json:"trial_days,omitempty"`
	DailyEmails  int    `json:"daily_emails"`
	Contacts     int    `json:"contacts"`
	Users        int    `json:"users"`
	Domains      int    `json:"domains"`
	Popular      bool   `json:"popular,omitempty"`
}

// TrialDays and TrialDailyEmails are the free-trial terms shown everywhere.
const (
	TrialDays        = 3
	TrialDailyEmails = 30
)

// yearly bills ten months for twelve.
func yearly(monthly int) int { return monthly * 10 }

var catalog = []Plan{
	{
		Key: Trial, Name: "Coba Gratis", Tagline: "Coba semua fitur tanpa kartu kredit",
		TrialDays: TrialDays, DailyEmails: TrialDailyEmails, Contacts: 100, Users: 1, Domains: 1,
	},
	{
		Key: Starter, Name: "Starter", Tagline: "Untuk tim kecil yang baru mulai outreach",
		PriceMonthly: 99_000, PriceYearly: yearly(99_000),
		DailyEmails: 500, Contacts: 2_500, Users: 2, Domains: 1,
	},
	{
		Key: Growth, Name: "Growth", Tagline: "Untuk tim sales yang sedang tumbuh",
		PriceMonthly: 299_000, PriceYearly: yearly(299_000),
		DailyEmails: 2_500, Contacts: 15_000, Users: 5, Domains: 3, Popular: true,
	},
	{
		Key: Business, Name: "Business", Tagline: "Untuk volume besar dan banyak brand",
		PriceMonthly: 799_000, PriceYearly: yearly(799_000),
		DailyEmails: 10_000, Contacts: 75_000, Users: 15, Domains: 10,
	},
	{
		Key: Enterprise, Name: "Enterprise", Tagline: "Kuota dan ketentuan sesuai kebutuhan",
		PriceMonthly: -1, PriceYearly: -1,
		DailyEmails: Unlimited, Contacts: Unlimited, Users: Unlimited, Domains: Unlimited,
	},
}

// Included lists what every plan gets: plans differ in quota, not in features.
var Included = []string{
	"Kampanye email massal dengan pelacakan buka & klik",
	"Follow-up otomatis multi-layer yang berhenti saat prospek membalas",
	"Cari email perusahaan & validasi email aktif",
	"Inbox terpadu, auto-reply, dan auto-forward",
	"OTP lewat email bisnis",
	"SMTP gateway, API key, dan webhook",
	"Alias email unik per divisi",
	"SMS lewat SMTP (kredit SMS dibeli terpisah)",
	"Impor kontak dari Excel, CSV, atau TXT",
	"Pemeriksaan SPF, DKIM, dan DMARC",
}

// Rupiah formats an amount as "Rp99.000".
func Rupiah(n int) string { return "Rp" + thousands(n) }

func thousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func quota(n int, unit string) string {
	if n == Unlimited {
		return strings.ToUpper(unit[:1]) + unit[1:] + " sesuai kebutuhan"
	}
	return thousands(n) + " " + unit
}

// Limits describes the plan's quotas as short bullet points.
func (p Plan) Limits() []string {
	out := []string{
		quota(p.DailyEmails, "email per hari"),
		quota(p.Contacts, "kontak"),
		quota(p.Users, "pengguna"),
		quota(p.Domains, "domain pengirim"),
	}
	if p.TrialDays > 0 {
		out = append([]string{fmt.Sprintf("Berlaku %d hari", p.TrialDays)}, out...)
	}
	return out
}

// Catalog returns every plan in display order.
func Catalog() []Plan { return catalog }

// Get looks a plan up by key.
func Get(key string) (Plan, bool) {
	for _, p := range catalog {
		if p.Key == key {
			return p, true
		}
	}
	return Plan{}, false
}

// Keys lists the valid plan keys.
func Keys() []string {
	keys := make([]string, len(catalog))
	for i, p := range catalog {
		keys[i] = p.Key
	}
	return keys
}

// SendLimit is the daily bulk-email quota a workspace gets on the plan.
// Unlimited plans are capped high rather than left open.
func (p Plan) SendLimit() int {
	if p.DailyEmails == Unlimited {
		return 100_000
	}
	return p.DailyEmails
}

// Period returns when a plan started at now ends: the trial length, the paid
// months, or nil for plans without an end (enterprise).
func (p Plan) Period(now time.Time, months int) *time.Time {
	var end time.Time
	switch p.Key {
	case Trial:
		end = now.AddDate(0, 0, p.TrialDays)
	case Enterprise:
		return nil
	default:
		end = now.AddDate(0, max(months, 1), 0)
	}
	return &end
}
