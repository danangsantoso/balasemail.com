// Package template renders personalised email content: {{variable}} and
// {{variable|"fallback"}} placeholders plus {option a|option b} spintax
// (PRD F3/F7). The syntax is intentionally simpler and safer than
// text/template because it is authored by end users.
package template

import (
	"hash/fnv"
	"html"
	"math/rand/v2"
	"regexp"
	"strings"
)

var varRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*(?:\|\s*(?:"([^"]*)"|([^}]*?)))?\s*\}\}`)

// Render substitutes variables. When escapeHTML is true, values (not the
// template) are HTML-escaped so contact data cannot inject markup.
func Render(tpl string, vars map[string]string, escapeHTML bool) string {
	return varRe.ReplaceAllStringFunc(tpl, func(m string) string {
		sub := varRe.FindStringSubmatch(m)
		v := strings.TrimSpace(vars[sub[1]])
		if v == "" {
			v = sub[2]
			if v == "" {
				v = strings.TrimSpace(sub[3])
			}
		}
		if escapeHTML {
			return html.EscapeString(v)
		}
		return v
	})
}

// Variables lists the placeholder names used in tpl (for UI validation).
func Variables(tpl string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range varRe.FindAllStringSubmatch(tpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// Spin resolves spintax deterministically for a given seed so that a
// recipient always gets the same variant (stable across retries).
func Spin(tpl, seed string) string {
	h := fnv.New64a()
	h.Write([]byte(seed))
	r := rand.New(rand.NewPCG(h.Sum64(), 0x9e3779b97f4a7c15))
	return spin(tpl, r)
}

func spin(s string, r *rand.Rand) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "{{") {
			j := strings.Index(s[i:], "}}")
			if j < 0 {
				out.WriteString(s[i:])
				break
			}
			out.WriteString(s[i : i+j+2])
			i += j + 2
			continue
		}
		if s[i] == '{' {
			end := matchBrace(s, i)
			if end > 0 {
				opts := splitTop(s[i+1 : end])
				if len(opts) > 1 {
					out.WriteString(spin(opts[r.IntN(len(opts))], r))
					i = end + 1
					continue
				}
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// matchBrace returns the index of the brace closing s[start], skipping {{vars}}.
func matchBrace(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			j := strings.Index(s[i:], "}}")
			if j < 0 {
				return -1
			}
			i += j + 1
		case s[i] == '{':
			depth++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits on '|' at nesting depth 0. A group without '|' is not
// spintax (e.g. CSS rules in <style>) and is returned as a single option.
func splitTop(s string) []string {
	var parts []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			if j := strings.Index(s[i:], "}}"); j >= 0 {
				i += j + 1
			}
		case s[i] == '{':
			depth++
		case s[i] == '}':
			depth--
		case s[i] == '|' && depth == 0:
			parts = append(parts, s[last:i])
			last = i + 1
		}
	}
	return append(parts, s[last:])
}

// Personalize applies spintax then variables — the order used by every send path.
func Personalize(tpl string, vars map[string]string, seed string, escapeHTML bool) string {
	return Render(Spin(tpl, seed), vars, escapeHTML)
}
