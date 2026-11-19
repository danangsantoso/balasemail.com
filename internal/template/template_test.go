package template

import (
	"slices"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	vars := map[string]string{"first_name": "Budi", "company": "<PT Maju>"}
	cases := []struct{ tpl, want string }{
		{"Halo {{first_name}}", "Halo Budi"},
		{"Halo {{ first_name }}!", "Halo Budi!"},
		{`Halo {{title|"Bapak/Ibu"}}`, "Halo Bapak/Ibu"},
		{"Halo {{title|Rekan}}", "Halo Rekan"},
		{"{{missing}}x", "x"},
	}
	for _, c := range cases {
		if got := Render(c.tpl, vars, false); got != c.want {
			t.Errorf("Render(%q) = %q, want %q", c.tpl, got, c.want)
		}
	}
	if got := Render("<b>{{company}}</b>", vars, true); got != "<b>&lt;PT Maju&gt;</b>" {
		t.Errorf("html escape: %q", got)
	}
}

func TestSpin(t *testing.T) {
	tpl := "{Halo|Hai|Selamat pagi} {{first_name}}, {apa kabar|semoga sehat {selalu|ya}}?"
	seen := map[string]bool{}
	for _, seed := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		out := Spin(tpl, seed)
		if strings.ContainsAny(strings.ReplaceAll(out, "{{first_name}}", ""), "{}|") {
			t.Fatalf("unresolved spintax: %q", out)
		}
		if !strings.Contains(out, "{{first_name}}") {
			t.Fatalf("variable must survive spinning: %q", out)
		}
		seen[out] = true
	}
	if len(seen) < 2 {
		t.Error("expected variation across seeds")
	}
	if first, second := Spin(tpl, "same"), Spin(tpl, "same"); first != second {
		t.Error("spin must be deterministic per seed")
	}
}

func TestSpinLeavesCSSAlone(t *testing.T) {
	css := "<style>p{color:red}</style>"
	if got := Spin(css, "x"); got != css {
		t.Errorf("css changed: %q", got)
	}
}

func TestVariables(t *testing.T) {
	got := Variables("{{a}} {{ b|x }} {{a}}")
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("got %v", got)
	}
}
