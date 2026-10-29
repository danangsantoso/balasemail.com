package inbound

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/sequence"
)

const replyMIME = "From: Budi Santoso <Budi@Acme.id>\r\n" +
	"To: sales@maju.co.id\r\n" +
	"Subject: =?utf-8?q?Re:_Penawaran_kerja_sama?=\r\n" +
	"Message-ID: <r1@acme.id>\r\n" +
	"In-Reply-To: <abc.123@maju.co.id>\r\n" +
	"References: <first@maju.co.id> <abc.123@maju.co.id>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" +
	"--b1\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
	"Saya tertarik, boleh jadwalkan demo minggu depan=3F\r\n\r\nPada Sen, 2 Nov 2026 Sales menulis:\r\n> Halo Budi\r\n" +
	"--b1\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html version</p>\r\n--b1--\r\n"

func TestParse(t *testing.T) {
	e, err := Parse("Sales@Maju.co.id", []byte(replyMIME))
	if err != nil {
		t.Fatal(err)
	}
	if e.From != "budi@acme.id" || e.FromName != "Budi Santoso" || e.Subject != "Re: Penawaran kerja sama" {
		t.Errorf("headers: %+v", e)
	}
	if len(e.InReplyTo) != 3 || e.InReplyTo[0] != "abc.123@maju.co.id" || e.InReplyTo[2] != "first@maju.co.id" {
		t.Errorf("in-reply-to = %v", e.InReplyTo)
	}
	if !strings.HasPrefix(e.Text, "Saya tertarik, boleh jadwalkan demo minggu depan?") {
		t.Errorf("text = %q", e.Text)
	}
	if got := StripQuoted(e.Text); strings.Contains(got, "Halo Budi") || strings.Contains(got, "menulis") {
		t.Errorf("quoted text not stripped: %q", got)
	}
}

func TestClassify(t *testing.T) {
	c := KeywordClassifier{}
	cases := map[string]string{
		"Saya tertarik, boleh jadwalkan demo?":       ClassInterested,
		"Maaf, kami tidak tertarik saat ini.":        ClassNotInterested,
		"Tolong hapus saya dari daftar ini":          ClassUnsubscribe,
		"Saya sedang cuti hingga 10 November":        ClassOutOfOffice,
		"Pak Andi sudah tidak bekerja di sini":       ClassWrongPerson,
		"Apakah produk ini mendukung integrasi SAP?": ClassQuestion,
		"Terima kasih.": ClassOther,
		"OK\n\nPada Sen Sales menulis:\n> Anda tertarik?": ClassOther, // quoted text ignored
	}
	for body, want := range cases {
		if got := c.Classify(context.Background(), "", body); got != want {
			t.Errorf("Classify(%q) = %s, want %s", body, got, want)
		}
	}
}

func TestIsAutomated(t *testing.T) {
	mk := func(from string, hdr map[string]string) Email {
		e := Email{From: from, Header: map[string][]string{}}
		for k, v := range hdr {
			e.Header[k] = []string{v}
		}
		return e
	}
	auto := []Email{
		mk("budi@acme.id", map[string]string{"Auto-Submitted": "auto-replied"}),
		mk("budi@acme.id", map[string]string{"Precedence": "bulk"}),
		mk("news@acme.id", map[string]string{"List-Id": "<news.acme.id>"}),
		mk("mailer-daemon@acme.id", nil),
		mk("no-reply@acme.id", nil),
		mk("budi@acme.id", map[string]string{"X-Balasemail-Loop": "ws1"}),
		mk("budi@acme.id", map[string]string{"Return-Path": "<>"}),
	}
	for i, e := range auto {
		if !IsAutomated(e) {
			t.Errorf("case %d should be automated", i)
		}
	}
	if IsAutomated(mk("budi@acme.id", map[string]string{"Auto-Submitted": "no"})) {
		t.Error("human mail flagged as automated")
	}
}

func TestRuleMatches(t *testing.T) {
	e := Email{Rcpt: "sales+promo@maju.co.id", From: "budi@acme.id", FromName: "Budi", Subject: "Tanya harga", Text: "Berapa harga paket?"}
	now := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC) // Saturday 10:00 WIB
	weekdays := &sequence.SendWindow{Days: []time.Weekday{1, 2, 3, 4, 5}, StartHour: 8, EndHour: 17, Timezone: "Asia/Jakarta"}

	cases := []struct {
		name  string
		rule  Rule
		class string
		want  bool
	}{
		{"match all", Rule{Enabled: true}, ClassOther, true},
		{"disabled", Rule{}, ClassOther, false},
		{"to base local part", Rule{Enabled: true, Match: Match{To: []string{"sales"}}}, ClassOther, true},
		{"to other alias", Rule{Enabled: true, Match: Match{To: []string{"hr@maju.co.id"}}}, ClassOther, false},
		{"subject keyword", Rule{Enabled: true, Match: Match{SubjectContains: []string{"HARGA"}}}, ClassOther, true},
		{"classification", Rule{Enabled: true, Match: Match{Classification: []string{ClassInterested}}}, ClassQuestion, false},
		{"outside hours on weekend", Rule{Enabled: true, Match: Match{OutsideHours: weekdays}}, ClassOther, true},
	}
	for _, c := range cases {
		if got := c.rule.Matches(e, c.class, now); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	monday := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	if (Rule{Enabled: true, Match: Match{OutsideHours: weekdays}}).Matches(e, ClassOther, monday) {
		t.Error("outside-hours rule must not fire during business hours")
	}
}

func TestRuleValidate(t *testing.T) {
	bad := []Rule{
		{Name: "", Action: RuleAct{Type: ActAutoReply, Body: "x"}},
		{Name: "a", Action: RuleAct{Type: ActAutoReply}},
		{Name: "a", Action: RuleAct{Type: ActForward}},
		{Name: "a", Action: RuleAct{Type: "delete"}},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}
