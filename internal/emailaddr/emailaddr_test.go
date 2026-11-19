package emailaddr

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Budi@Perusahaan.CO.ID", "budi@perusahaan.co.id", true},
		{"Budi Santoso <budi.santoso@acme.com>", "budi.santoso@acme.com", true},
		{"reply+abc@acme.com", "reply+abc@acme.com", true},
		{"no-at-sign", "", false},
		{"a@b", "", false},
		{"a..b@acme.com", "", false},
		{".a@acme.com", "", false},
		{"a@-acme.com", "", false},
		{"a@acme.123", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, err := Normalize(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("Normalize(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestClassification(t *testing.T) {
	if !IsFreeProvider("gmail.com") || IsFreeProvider("acme.co.id") {
		t.Error("free provider detection")
	}
	if !IsDisposable("mailinator.com") {
		t.Error("disposable detection")
	}
	if !IsRole("info") || !IsRole("sales+promo") || IsRole("budi") {
		t.Error("role detection")
	}
	if IsBusiness(Address{"x", "yahoo.co.id"}) || !IsBusiness(Address{"x", "acme.id"}) {
		t.Error("business detection")
	}
	if !IsNoReply("no-reply") || !IsNoReply("MAILER-DAEMON") || IsNoReply("budi") {
		t.Error("noreply detection")
	}
	if !DomainMatches("mail.acme.com", "acme.com") || DomainMatches("notacme.com", "acme.com") {
		t.Error("domain matching")
	}
}
