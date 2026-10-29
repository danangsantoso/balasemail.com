// Package sequence implements multi-layer follow-up funnels (PRD F3): a
// directed acyclic graph of email, wait, condition and action nodes with a
// configurable number of layers, A/B variants, send windows and auto-stop.
package sequence

import (
	"fmt"
	"strings"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
)

type NodeType string

const (
	NodeEmail     NodeType = "email"
	NodeWait      NodeType = "wait"
	NodeCondition NodeType = "condition"
	NodeAction    NodeType = "action"
	NodeEnd       NodeType = "end"
)

// Definition is the funnel graph stored as JSONB.
type Definition struct {
	Start    string   `json:"start"`
	Nodes    []Node   `json:"nodes"`
	Settings Settings `json:"settings"`
}

type Settings struct {
	// StopOnReply ends the enrollment when the prospect replies (default true).
	StopOnReply *bool `json:"stop_on_reply,omitempty"`
	// StopOnClick ends it on the first link click (e.g. booked a demo).
	StopOnClick bool `json:"stop_on_click,omitempty"`
}

func (s Settings) stopOnReply() bool { return s.StopOnReply == nil || *s.StopOnReply }

type Node struct {
	ID   string   `json:"id"`
	Type NodeType `json:"type"`
	Name string   `json:"name,omitempty"`
	Next string   `json:"next,omitempty"`

	// email
	Subject  string    `json:"subject,omitempty"`
	HTML     string    `json:"html,omitempty"`
	Text     string    `json:"text,omitempty"`
	Variants []Variant `json:"variants,omitempty"` // A/B(/C...) alternatives to the base content
	// ThreadReply sends this email as a reply in the same thread as the previous one.
	ThreadReply bool `json:"thread_reply,omitempty"`

	// wait
	Wait *Duration `json:"wait,omitempty"`

	// condition
	Condition *Condition `json:"condition,omitempty"`
	Yes       string     `json:"yes,omitempty"`
	No        string     `json:"no,omitempty"`

	// action
	Action *Action `json:"action,omitempty"`
}

type Variant struct {
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text,omitempty"`
}

type Duration struct {
	Minutes int `json:"minutes,omitempty"`
	Hours   int `json:"hours,omitempty"`
	Days    int `json:"days,omitempty"`
}

func (d Duration) D() time.Duration {
	return time.Duration(d.Minutes)*time.Minute + time.Duration(d.Hours)*time.Hour + time.Duration(d.Days)*24*time.Hour
}

type ConditionType string

const (
	CondOpened      ConditionType = "opened"  // last email opened
	CondClicked     ConditionType = "clicked" // last email clicked
	CondReplied     ConditionType = "replied" // last email replied
	CondHasTag      ConditionType = "has_tag"
	CondFieldEquals ConditionType = "field_equals"
)

type Condition struct {
	Type  ConditionType `json:"type"`
	Tag   string        `json:"tag,omitempty"`
	Field string        `json:"field,omitempty"`
	Value string        `json:"value,omitempty"`
}

type ActionType string

const (
	ActAddTag    ActionType = "add_tag"
	ActRemoveTag ActionType = "remove_tag"
	ActSendSMS   ActionType = "send_sms"
	ActWebhook   ActionType = "webhook"
	ActNotify    ActionType = "notify"
	ActStop      ActionType = "stop"
)

type Action struct {
	Type        ActionType `json:"type"`
	Tags        []string   `json:"tags,omitempty"`
	SMSBody     string     `json:"sms_body,omitempty"`
	URL         string     `json:"url,omitempty"`
	NotifyEmail string     `json:"notify_email,omitempty"`
}

// Limits bound funnel size per plan.
type Limits struct {
	MaxNodes  int
	MaxEmails int // the "number of layers" in PRD terms
}

var DefaultLimits = Limits{MaxNodes: 60, MaxEmails: 20}

func (d Definition) node(id string) *Node {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			return &d.Nodes[i]
		}
	}
	return nil
}

func (n Node) edges() []string {
	switch n.Type {
	case NodeCondition:
		return []string{n.Yes, n.No}
	case NodeEnd:
		return nil
	default:
		return []string{n.Next}
	}
}

// Validate checks structure, references, limits and acyclicity.
func (d Definition) Validate(l Limits) error {
	bad := func(format string, a ...any) error {
		return apperr.Invalid("invalid_sequence", fmt.Sprintf(format, a...))
	}
	if len(d.Nodes) == 0 {
		return bad("sequence needs at least one node")
	}
	if len(d.Nodes) > l.MaxNodes {
		return bad("sequence has %d nodes; the limit is %d", len(d.Nodes), l.MaxNodes)
	}
	ids := map[string]bool{}
	emails := 0
	for _, n := range d.Nodes {
		if n.ID == "" || ids[n.ID] {
			return bad("node ids must be unique and non-empty (%q)", n.ID)
		}
		ids[n.ID] = true
	}
	if !ids[d.Start] {
		return bad("start node %q does not exist", d.Start)
	}
	for _, n := range d.Nodes {
		for _, e := range n.edges() {
			if e != "" && !ids[e] {
				return bad("node %q points to unknown node %q", n.ID, e)
			}
		}
		switch n.Type {
		case NodeEmail:
			emails++
			if strings.TrimSpace(n.Subject) == "" || (strings.TrimSpace(n.HTML) == "" && strings.TrimSpace(n.Text) == "") {
				return bad("email node %q needs a subject and a body", n.ID)
			}
			for i, v := range n.Variants {
				if strings.TrimSpace(v.Subject) == "" {
					return bad("variant %d of node %q needs a subject", i+1, n.ID)
				}
			}
			if next := d.node(n.Next); next != nil && next.Type == NodeEmail {
				return bad("add a wait between emails %q and %q", n.ID, next.ID)
			}
		case NodeWait:
			if n.Wait == nil || n.Wait.D() < time.Minute || n.Wait.D() > 180*24*time.Hour {
				return bad("wait node %q needs a duration between 1 minute and 180 days", n.ID)
			}
		case NodeCondition:
			if n.Condition == nil {
				return bad("condition node %q needs a condition", n.ID)
			}
			switch n.Condition.Type {
			case CondOpened, CondClicked, CondReplied:
			case CondHasTag:
				if n.Condition.Tag == "" {
					return bad("has_tag condition on %q needs a tag", n.ID)
				}
			case CondFieldEquals:
				if n.Condition.Field == "" {
					return bad("field_equals condition on %q needs a field", n.ID)
				}
			default:
				return bad("unknown condition type %q", n.Condition.Type)
			}
		case NodeAction:
			if n.Action == nil {
				return bad("action node %q needs an action", n.ID)
			}
			switch n.Action.Type {
			case ActAddTag, ActRemoveTag:
				if len(n.Action.Tags) == 0 {
					return bad("tag action on %q needs tags", n.ID)
				}
			case ActSendSMS:
				if n.Action.SMSBody == "" {
					return bad("send_sms action on %q needs sms_body", n.ID)
				}
			case ActWebhook:
				if !strings.HasPrefix(n.Action.URL, "https://") {
					return bad("webhook action on %q needs an https url", n.ID)
				}
			case ActNotify:
				if n.Action.NotifyEmail == "" {
					return bad("notify action on %q needs notify_email", n.ID)
				}
			case ActStop:
			default:
				return bad("unknown action type %q", n.Action.Type)
			}
		case NodeEnd:
		default:
			return bad("unknown node type %q", n.Type)
		}
	}
	if emails == 0 {
		return bad("sequence needs at least one email node")
	}
	if emails > l.MaxEmails {
		return bad("sequence has %d emails; your plan allows %d layers", emails, l.MaxEmails)
	}
	// Cycle detection (white/grey/black DFS).
	color := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		if id == "" {
			return nil
		}
		switch color[id] {
		case 1:
			return bad("sequence contains a loop through node %q", id)
		case 2:
			return nil
		}
		color[id] = 1
		for _, e := range d.node(id).edges() {
			if err := visit(e); err != nil {
				return err
			}
		}
		color[id] = 2
		return nil
	}
	return visit(d.Start)
}
