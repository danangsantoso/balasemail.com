package inbound

import (
	"context"
	"strings"
)

// Classifications used for routing, reporting and auto-stop.
const (
	ClassInterested    = "interested"
	ClassNotInterested = "not_interested"
	ClassQuestion      = "question"
	ClassOutOfOffice   = "out_of_office"
	ClassWrongPerson   = "wrong_person"
	ClassUnsubscribe   = "unsubscribe"
	ClassOther         = "other"
)

// Classifier labels a reply. The default is keyword-based (Indonesian +
// English); an LLM-backed implementation can be plugged in behind the same
// interface without touching the pipeline.
type Classifier interface {
	Classify(ctx context.Context, subject, body string) string
}

type KeywordClassifier struct{}

// Order matters: negatives are checked before positives ("tidak tertarik" contains "tertarik").
var keywordRules = []struct {
	class    string
	keywords []string
}{
	{ClassUnsubscribe, []string{"unsubscribe", "berhenti berlangganan", "jangan kirim", "jangan email", "stop email", "stop sending",
		"remove me", "hapus saya", "hapus email saya", "take me off"}},
	{ClassOutOfOffice, []string{"out of office", "out-of-office", "automatic reply", "auto reply", "auto-reply", "balasan otomatis",
		"sedang cuti", "sedang tidak di kantor", "on leave", "on vacation", "away from the office"}},
	{ClassWrongPerson, []string{"wrong person", "salah orang", "bukan orang yang tepat", "no longer with", "sudah tidak bekerja",
		"sudah resign", "not the right person"}},
	{ClassNotInterested, []string{"tidak tertarik", "not interested", "no thanks", "no thank you", "tidak perlu", "belum butuh",
		"belum membutuhkan", "tidak membutuhkan", "tidak berminat", "not a fit", "pass on this"}},
	{ClassInterested, []string{"tertarik", "interested", "berminat", "jadwalkan", "schedule", "meeting", "demo", "let's talk",
		"telepon saya", "hubungi saya", "call me", "kirimkan penawaran", "send me", "pricing", "harga", "proposal", "boleh"}},
}

func (KeywordClassifier) Classify(_ context.Context, subject, body string) string {
	text := strings.ToLower(subject + "\n" + StripQuoted(body))
	for _, r := range keywordRules {
		for _, k := range r.keywords {
			if strings.Contains(text, k) {
				return r.class
			}
		}
	}
	if strings.Contains(StripQuoted(body), "?") {
		return ClassQuestion
	}
	return ClassOther
}
