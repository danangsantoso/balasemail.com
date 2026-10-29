package finder

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Crawler fetches a small, polite set of public pages from a company website.
// It honours robots.txt, identifies itself, rate-limits per host and never
// logs in or follows off-site links.
type Crawler struct {
	Client    *http.Client
	UserAgent string
	MaxPages  int
	Delay     time.Duration // between requests to the same host
	MaxBytes  int64
	// Scheme lets tests point at an httptest server; defaults to https.
	Scheme string
}

// CommonPaths are pages where companies usually publish contact addresses.
var CommonPaths = []string{
	"/", "/contact", "/contact-us", "/kontak", "/hubungi-kami", "/about", "/about-us",
	"/tentang-kami", "/team", "/tim", "/our-team", "/careers", "/karir", "/impressum",
}

var hrefRe = regexp.MustCompile(`(?i)href\s*=\s*["']([^"'#]+)["']`)
var interesting = []string{"contact", "kontak", "about", "tentang", "team", "tim", "karir", "career", "people", "leadership", "management"}

func (c *Crawler) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Crawl visits candidate pages and returns found addresses with source URLs.
func (c *Crawler) Crawl(ctx context.Context, host, domain string) ([]Found, error) {
	scheme := c.Scheme
	if scheme == "" {
		scheme = "https"
	}
	base := &url.URL{Scheme: scheme, Host: host}
	rules := c.robots(ctx, base)

	queue := slices.Clone(CommonPaths)
	seen := map[string]bool{}
	byEmail := map[string]*Found{}
	var order []string
	pages := 0
	maxPages := c.MaxPages
	if maxPages == 0 {
		maxPages = 12
	}

	for len(queue) > 0 && pages < maxPages {
		path := queue[0]
		queue = queue[1:]
		if seen[path] || !rules.allowed(path) {
			continue
		}
		seen[path] = true
		if pages > 0 && c.Delay > 0 {
			select {
			case <-ctx.Done():
				return collect(byEmail, order), ctx.Err()
			case <-time.After(c.Delay):
			}
		}
		u := base.ResolveReference(&url.URL{Path: path})
		body, ok := c.fetch(ctx, u.String())
		if !ok {
			continue
		}
		pages++
		for _, e := range ExtractEmails(body, domain) {
			if _, dup := byEmail[e]; !dup {
				byEmail[e] = &Found{Email: e, SourceURL: u.String(), Method: "website"}
				order = append(order, e)
			}
		}
		if path == "/" {
			queue = append(queue, sameSiteLinks(body, base)...)
		}
	}
	return collect(byEmail, order), nil
}

func collect(m map[string]*Found, order []string) []Found {
	out := make([]Found, 0, len(order))
	for _, e := range order {
		out = append(out, *m[e])
	}
	return out
}

func (c *Crawler) fetch(ctx context.Context, u string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html,text/plain")
	resp, err := c.client().Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "text") {
		return "", false
	}
	limit := c.MaxBytes
	if limit == 0 {
		limit = 2 << 20
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// sameSiteLinks returns relative/same-host paths whose URL hints at contact or team pages.
func sameSiteLinks(body string, base *url.URL) []string {
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(body, 200) {
		u, err := base.Parse(strings.TrimSpace(m[1]))
		if err != nil || u.Host != base.Host {
			continue
		}
		p := strings.ToLower(u.Path)
		if p == "" || p == "/" || slices.Contains(out, u.Path) {
			continue
		}
		if slices.ContainsFunc(interesting, func(k string) bool { return strings.Contains(p, k) }) {
			out = append(out, u.Path)
		}
	}
	return out
}

// robotsRules is a minimal robots.txt evaluator (User-agent * and ours; Disallow/Allow prefixes).
type robotsRules struct{ allow, disallow []string }

func (r robotsRules) allowed(path string) bool {
	best, allowed := -1, true
	for _, a := range r.allow {
		if strings.HasPrefix(path, a) && len(a) > best {
			best, allowed = len(a), true
		}
	}
	for _, d := range r.disallow {
		if d != "" && strings.HasPrefix(path, d) && len(d) > best {
			best, allowed = len(d), false
		}
	}
	return allowed
}

func (c *Crawler) robots(ctx context.Context, base *url.URL) robotsRules {
	body, ok := c.fetch(ctx, base.ResolveReference(&url.URL{Path: "/robots.txt"}).String())
	if !ok {
		return robotsRules{}
	}
	return parseRobots(body, c.UserAgent)
}

func parseRobots(body, userAgent string) robotsRules {
	agent := strings.ToLower(strings.SplitN(userAgent, "/", 2)[0])
	var generic, specific robotsRules
	var cur []*robotsRules
	inGroup := false
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "user-agent":
			if inGroup {
				cur, inGroup = nil, false
			}
			ua := strings.ToLower(v)
			if ua == "*" {
				cur = append(cur, &generic)
			} else if agent != "" && strings.Contains(agent, ua) {
				cur = append(cur, &specific)
			}
		case "disallow", "allow":
			inGroup = true
			for _, g := range cur {
				if k == "allow" {
					g.allow = append(g.allow, v)
				} else {
					g.disallow = append(g.disallow, v)
				}
			}
		}
	}
	if len(specific.allow)+len(specific.disallow) > 0 {
		return specific
	}
	return generic
}
