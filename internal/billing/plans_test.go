package billing

import (
	"testing"
	"time"
)

func TestCatalog(t *testing.T) {
	trial, ok := Get(Trial)
	if !ok || trial.TrialDays != 3 || trial.DailyEmails != 30 || trial.PriceMonthly != 0 {
		t.Fatalf("trial must be 3 days at 30 emails/day, got %+v", trial)
	}
	prev := 0
	for _, p := range Catalog() {
		if p.Key == Enterprise {
			continue
		}
		if p.SendLimit() < prev {
			t.Errorf("%s: daily quota must not shrink as plans go up", p.Key)
		}
		prev = p.SendLimit()
		if p.PriceMonthly > 0 && p.PriceYearly != p.PriceMonthly*10 {
			t.Errorf("%s: yearly price should bill 10 months, got %d", p.Key, p.PriceYearly)
		}
	}
	if _, ok := Get("gold"); ok {
		t.Error("unknown plan must not resolve")
	}
}

func TestPeriod(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	trial, _ := Get(Trial)
	if end := trial.Period(now, 0); end == nil || !end.Equal(now.AddDate(0, 0, 3)) {
		t.Errorf("trial ends after 3 days, got %v", end)
	}
	growth, _ := Get(Growth)
	if end := growth.Period(now, 12); end == nil || !end.Equal(now.AddDate(1, 0, 0)) {
		t.Errorf("12 paid months end a year later, got %v", end)
	}
	if end := growth.Period(now, 0); end == nil || !end.Equal(now.AddDate(0, 1, 0)) {
		t.Errorf("paid plans default to one month, got %v", end)
	}
	ent, _ := Get(Enterprise)
	if ent.Period(now, 1) != nil {
		t.Error("enterprise has no fixed end")
	}
}

func TestRupiahAndLimits(t *testing.T) {
	for n, want := range map[int]string{0: "Rp0", 99_000: "Rp99.000", 2_990_000: "Rp2.990.000"} {
		if got := Rupiah(n); got != want {
			t.Errorf("Rupiah(%d) = %q, want %q", n, got, want)
		}
	}
	trial, _ := Get(Trial)
	if l := trial.Limits(); l[0] != "Berlaku 3 hari" || l[1] != "30 email per hari" {
		t.Errorf("trial limits = %v", l)
	}
}
