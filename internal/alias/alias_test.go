package alias

import (
	"strings"
	"testing"
)

func TestRenderPattern(t *testing.T) {
	vars := map[string]string{"first": "Budi", "last": "Santoso", "division": "Sales Jakarta", "campaign": "Q4-2026"}
	cases := map[string]string{
		"{first}.{division}": "budi.sales-jakarta",
		"{f}{last}":          "bsantoso",
		"promo-{campaign}":   "promo-q4-2026",
		"{first}..{last}":    "budi.santoso",
	}
	for p, want := range cases {
		got, err := RenderPattern(p, vars)
		if err != nil || got != want {
			t.Errorf("RenderPattern(%q) = %q, %v; want %q", p, got, err, want)
		}
	}
	if got, err := RenderPattern("reply-{random}", nil); err != nil || !strings.HasPrefix(got, "reply-") || len(got) != 12 {
		t.Errorf("random: %q %v", got, err)
	}
	if _, err := RenderPattern("{first}.{division}", map[string]string{"first": "Budi"}); err == nil {
		t.Error("missing token value should fail")
	}
	if _, err := RenderPattern("static", vars); err == nil {
		t.Error("pattern without tokens should fail")
	}
}
