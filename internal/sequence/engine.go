package sequence

import (
	"hash/fnv"
	"slices"
	"strings"
	"time"
)

// Facts are what conditions evaluate: engagement with the most recent email
// of this enrollment plus contact attributes.
type Facts struct {
	Opened  bool
	Clicked bool
	Replied bool
	Tags    []string
	Fields  map[string]string
}

type EffectKind string

const (
	EffectEmail  EffectKind = "email"
	EffectAction EffectKind = "action"
)

// Effect is a side effect the service must perform.
type Effect struct {
	Kind    EffectKind
	Node    Node
	Variant int // 0 = base content, i>0 = Variants[i-1]
}

type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusStopped   Status = "stopped"
)

// Result is the enrollment's new state after Advance.
type Result struct {
	Node       string
	RunAt      time.Time
	Status     Status
	StopReason string
	Effects    []Effect
	Visited    []NodeType
}

// maxSteps guards against runaway evaluation (definitions are acyclic anyway).
const maxSteps = 100

// Advance runs the funnel from nodeID until it must wait, completes or stops.
// It is pure: no I/O, so every branch is unit-testable.
func Advance(def Definition, nodeID string, facts Facts, now time.Time, w SendWindow, seed string) Result {
	res := Result{Status: StatusActive}
	id := nodeID
	for range maxSteps {
		n := def.node(id)
		if n == nil || n.Type == NodeEnd {
			res.Status, res.Node = StatusCompleted, id
			return res
		}
		res.Visited = append(res.Visited, n.Type)
		switch n.Type {
		case NodeEmail:
			if !w.Open(now) {
				res.Node, res.RunAt = id, w.Next(now)
				res.Visited = res.Visited[:len(res.Visited)-1]
				return res
			}
			res.Effects = append(res.Effects, Effect{Kind: EffectEmail, Node: *n, Variant: pickVariant(seed, *n)})
			facts = Facts{Tags: facts.Tags, Fields: facts.Fields} // new email: no engagement yet
			id = n.Next
		case NodeWait:
			res.Node, res.RunAt = n.Next, now.Add(n.Wait.D())
			if n.Next == "" {
				res.Status = StatusCompleted
			}
			return res
		case NodeCondition:
			if evaluate(*n.Condition, facts) {
				id = n.Yes
			} else {
				id = n.No
			}
		case NodeAction:
			if n.Action.Type == ActStop {
				res.Status, res.Node, res.StopReason = StatusStopped, id, "stop_action"
				return res
			}
			res.Effects = append(res.Effects, Effect{Kind: EffectAction, Node: *n})
			switch n.Action.Type {
			case ActAddTag:
				facts.Tags = appendUnique(facts.Tags, n.Action.Tags...)
			case ActRemoveTag:
				facts.Tags = slices.DeleteFunc(slices.Clone(facts.Tags), func(t string) bool { return slices.Contains(n.Action.Tags, t) })
			}
			id = n.Next
		}
		if id == "" {
			res.Status = StatusCompleted
			return res
		}
	}
	res.Status, res.StopReason = StatusStopped, "too_many_steps"
	return res
}

func evaluate(c Condition, f Facts) bool {
	switch c.Type {
	case CondOpened:
		return f.Opened || f.Clicked
	case CondClicked:
		return f.Clicked
	case CondReplied:
		return f.Replied
	case CondHasTag:
		return slices.Contains(f.Tags, strings.ToLower(c.Tag))
	case CondFieldEquals:
		return strings.EqualFold(strings.TrimSpace(f.Fields[c.Field]), strings.TrimSpace(c.Value))
	}
	return false
}

// pickVariant splits recipients evenly and deterministically across variants.
func pickVariant(seed string, n Node) int {
	if len(n.Variants) == 0 {
		return 0
	}
	h := fnv.New32a()
	h.Write([]byte(seed + "/" + n.ID))
	return int(h.Sum32() % uint32(len(n.Variants)+1))
}

// Content returns subject/html/text for the chosen variant.
func (e Effect) Content() (subject, html, text string) {
	if e.Variant > 0 && e.Variant <= len(e.Node.Variants) {
		v := e.Node.Variants[e.Variant-1]
		html, text = v.HTML, v.Text
		if html == "" && text == "" {
			html, text = e.Node.HTML, e.Node.Text
		}
		return v.Subject, html, text
	}
	return e.Node.Subject, e.Node.HTML, e.Node.Text
}

func appendUnique(s []string, vs ...string) []string {
	out := slices.Clone(s)
	for _, v := range vs {
		v = strings.ToLower(v)
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// SendWindow restricts when emails go out (e.g. Mon–Fri 08:00–17:00 WIB).
type SendWindow struct {
	Days      []time.Weekday `json:"days,omitempty"` // empty = every day
	StartHour int            `json:"start_hour"`
	EndHour   int            `json:"end_hour"` // exclusive; 0 with StartHour 0 = all day
	Timezone  string         `json:"timezone,omitempty"`
}

func (w SendWindow) loc() *time.Location {
	if w.Timezone == "" {
		return time.UTC
	}
	l, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return time.UTC
	}
	return l
}

func (w SendWindow) allDay() bool { return w.StartHour == 0 && (w.EndHour == 0 || w.EndHour >= 24) }

func (w SendWindow) dayOK(d time.Weekday) bool { return len(w.Days) == 0 || slices.Contains(w.Days, d) }

// Open reports whether t falls inside the window.
func (w SendWindow) Open(t time.Time) bool {
	lt := t.In(w.loc())
	if !w.dayOK(lt.Weekday()) {
		return false
	}
	if w.allDay() {
		return true
	}
	return lt.Hour() >= w.StartHour && lt.Hour() < w.EndHour
}

// Next returns the earliest time >= t inside the window.
func (w SendWindow) Next(t time.Time) time.Time {
	if w.Open(t) {
		return t
	}
	loc := w.loc()
	lt := t.In(loc)
	for i := range 8 {
		day := time.Date(lt.Year(), lt.Month(), lt.Day()+i, w.StartHour, 0, 0, 0, loc)
		if w.dayOK(day.Weekday()) && day.After(t) {
			return day
		}
	}
	return t.Add(24 * time.Hour)
}

// Validate checks the window configuration.
func (w SendWindow) Validate() error {
	if w.StartHour < 0 || w.StartHour > 23 || w.EndHour < 0 || w.EndHour > 24 || (!w.allDay() && w.EndHour <= w.StartHour) {
		return errWindow
	}
	if w.Timezone != "" {
		if _, err := time.LoadLocation(w.Timezone); err != nil {
			return errWindow
		}
	}
	return nil
}
