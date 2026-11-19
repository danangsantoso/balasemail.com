package sequence

import (
	"testing"
	"time"
)

// A 3-layer funnel:
//
//	e1 → wait 3d → replied? ─yes→ end
//	                        └no→ opened? ─yes→ e2(case study) → wait 2d → tag hot
//	                                      └no→ e3(new subject) → wait 4d → end
func funnel() Definition {
	return Definition{
		Start: "e1",
		Nodes: []Node{
			{ID: "e1", Type: NodeEmail, Subject: "Perkenalan", HTML: "<p>Halo</p>", Next: "w1",
				Variants: []Variant{{Subject: "Kerja sama?"}}},
			{ID: "w1", Type: NodeWait, Wait: &Duration{Days: 3}, Next: "c1"},
			{ID: "c1", Type: NodeCondition, Condition: &Condition{Type: CondReplied}, Yes: "end", No: "c2"},
			{ID: "c2", Type: NodeCondition, Condition: &Condition{Type: CondOpened}, Yes: "e2", No: "e3"},
			{ID: "e2", Type: NodeEmail, Subject: "Studi kasus", Text: "...", Next: "w2"},
			{ID: "w2", Type: NodeWait, Wait: &Duration{Days: 2}, Next: "a1"},
			{ID: "a1", Type: NodeAction, Action: &Action{Type: ActAddTag, Tags: []string{"hot"}}},
			{ID: "e3", Type: NodeEmail, Subject: "Masih tertarik?", Text: "...", Next: "w3"},
			{ID: "w3", Type: NodeWait, Wait: &Duration{Days: 4}, Next: "end"},
			{ID: "end", Type: NodeEnd},
		},
	}
}

var monday9 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // a Monday

func TestValidate(t *testing.T) {
	if err := funnel().Validate(DefaultLimits); err != nil {
		t.Fatalf("valid funnel rejected: %v", err)
	}
	cases := map[string]func(*Definition){
		"unknown ref":     func(d *Definition) { d.Nodes[0].Next = "nope" },
		"missing start":   func(d *Definition) { d.Start = "x" },
		"loop":            func(d *Definition) { d.Nodes[8].Next = "c1" },
		"email→email":     func(d *Definition) { d.Nodes[0].Next = "e2" },
		"bad wait":        func(d *Definition) { d.Nodes[1].Wait = &Duration{} },
		"empty subject":   func(d *Definition) { d.Nodes[4].Subject = "" },
		"dup id":          func(d *Definition) { d.Nodes[1].ID = "e1" },
		"webhook no tls":  func(d *Definition) { d.Nodes[6].Action = &Action{Type: ActWebhook, URL: "http://x"} },
		"too many layers": func(d *Definition) {},
	}
	for name, mutate := range cases {
		d := funnel()
		d.Nodes = append([]Node(nil), d.Nodes...)
		mutate(&d)
		limits := DefaultLimits
		if name == "too many layers" {
			limits.MaxEmails = 2
		}
		if err := d.Validate(limits); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestAdvanceFirstEmailThenWait(t *testing.T) {
	r := Advance(funnel(), "e1", Facts{}, monday9, SendWindow{}, "enr-1")
	if r.Status != StatusActive || r.Node != "c1" || !r.RunAt.Equal(monday9.Add(72*time.Hour)) {
		t.Fatalf("got %+v", r)
	}
	if len(r.Effects) != 1 || r.Effects[0].Kind != EffectEmail || r.Effects[0].Node.ID != "e1" {
		t.Fatalf("effects = %+v", r.Effects)
	}
}

func TestAdvanceBranches(t *testing.T) {
	d := funnel()
	tests := []struct {
		name      string
		facts     Facts
		wantEmail string
		wantNode  string
		status    Status
	}{
		{"replied ends funnel", Facts{Replied: true}, "", "end", StatusCompleted},
		{"opened gets case study", Facts{Opened: true}, "e2", "a1", StatusActive},
		{"clicked counts as opened", Facts{Clicked: true}, "e2", "a1", StatusActive},
		{"ignored gets new angle", Facts{}, "e3", "end", StatusActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Advance(d, "c1", tt.facts, monday9, SendWindow{}, "s")
			if r.Status != tt.status || r.Node != tt.wantNode {
				t.Fatalf("status=%s node=%s", r.Status, r.Node)
			}
			got := ""
			if len(r.Effects) > 0 {
				got = r.Effects[0].Node.ID
			}
			if got != tt.wantEmail {
				t.Fatalf("email = %q, want %q", got, tt.wantEmail)
			}
		})
	}
}

func TestAdvanceActionsAndCompletion(t *testing.T) {
	r := Advance(funnel(), "a1", Facts{}, monday9, SendWindow{}, "s")
	if r.Status != StatusCompleted || len(r.Effects) != 1 || r.Effects[0].Node.Action.Type != ActAddTag {
		t.Fatalf("got %+v", r)
	}
	stop := Definition{Start: "e", Nodes: []Node{
		{ID: "e", Type: NodeEmail, Subject: "s", Text: "t", Next: "x"},
		{ID: "x", Type: NodeAction, Action: &Action{Type: ActStop}},
	}}
	if r := Advance(stop, "e", Facts{}, monday9, SendWindow{}, "s"); r.Status != StatusStopped || len(r.Effects) != 1 {
		t.Fatalf("stop action: %+v", r)
	}
}

func TestSendWindowDefersEmail(t *testing.T) {
	w := SendWindow{Days: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		StartHour: 8, EndHour: 17, Timezone: "Asia/Jakarta"}
	saturday := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	r := Advance(funnel(), "e1", Facts{}, saturday, w, "s")
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	want := time.Date(2026, 11, 9, 8, 0, 0, 0, jkt) // Monday 08:00 WIB
	if len(r.Effects) != 0 || r.Node != "e1" || !r.RunAt.Equal(want) {
		t.Fatalf("got node=%s runAt=%v effects=%d, want %v", r.Node, r.RunAt, len(r.Effects), want)
	}
	// 10:00 WIB on a Monday is inside the window.
	if !w.Open(time.Date(2026, 11, 9, 10, 0, 0, 0, jkt)) {
		t.Error("expected window open")
	}
	if w.Open(time.Date(2026, 11, 9, 17, 0, 0, 0, jkt)) {
		t.Error("end hour is exclusive")
	}
}

func TestVariantsSplitEvenly(t *testing.T) {
	n := funnel().Nodes[0]
	counts := map[int]int{}
	for i := range 1000 {
		counts[pickVariant(string(rune('a'+i%26))+time.Duration(i).String(), n)]++
	}
	if counts[0] < 400 || counts[1] < 400 {
		t.Errorf("uneven split: %v", counts)
	}
	e := Effect{Node: n, Variant: 1}
	if s, html, _ := e.Content(); s != "Kerja sama?" || html != "<p>Halo</p>" {
		t.Errorf("variant content = %q %q (body should fall back to base)", s, html)
	}
}

func TestConditionTagsAndFields(t *testing.T) {
	f := Facts{Tags: []string{"vip"}, Fields: map[string]string{"industri": "Manufaktur "}}
	if !evaluate(Condition{Type: CondHasTag, Tag: "VIP"}, f) || evaluate(Condition{Type: CondHasTag, Tag: "x"}, f) {
		t.Error("has_tag")
	}
	if !evaluate(Condition{Type: CondFieldEquals, Field: "industri", Value: "manufaktur"}, f) {
		t.Error("field_equals should be case/space-insensitive")
	}
}
