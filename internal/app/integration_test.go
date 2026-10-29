//go:build integration

// Integration tests run the real modules against PostgreSQL:
//
//	DATABASE_URL=postgres://... go test -tags integration ./internal/app/
//
// They exercise the end-to-end flows behind the PRD acceptance criteria.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/config"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
)

type harness struct {
	t   *testing.T
	a   *App
	srv *httptest.Server
	tok string
}

func setup(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Bind first so tracking/unsubscribe links point at the test server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.PublicBaseURL = "http://" + ln.Addr().String()
	ctx := context.Background()
	a, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `TRUNCATE workspaces, jobs CASCADE`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(a.HTTPHandler())
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()

	wctx, cancel := context.WithCancel(ctx)
	w := queue.NewWorker(a.DB)
	w.PollEvery = 100 * time.Millisecond
	a.RegisterJobs(w)
	done := make(chan struct{})
	go func() { _ = w.Run(wctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close(); a.Close() })
	return &harness{t: t, a: a, srv: srv}
}

func (h *harness) do(method, path string, body any, want int) map[string]any {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	if h.tok != "" {
		req.Header.Set("Authorization", "Bearer "+h.tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		h.t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (h *harness) sql(q string, args ...any) {
	h.t.Helper()
	if _, err := h.a.DB.Exec(context.Background(), q, args...); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) scalar(q string, args ...any) string {
	h.t.Helper()
	var s string
	if err := h.a.DB.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for range 50 {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// onboard registers a workspace and a (pre-verified) sending domain.
func (h *harness) onboard() {
	h.do("POST", "/v1/auth/register", map[string]string{"company_name": "X", "full_name": "Y", "email": "y@gmail.com", "password": "password-123"}, 422)
	sess := h.do("POST", "/v1/auth/register", map[string]string{"company_name": "PT Maju", "full_name": "Budi",
		"email": "budi@maju.co.id", "password": "password-kuat-123", "postal_address": "Jakarta"}, 201)
	h.tok = sess["token"].(string)
	d := h.do("POST", "/v1/domains", map[string]string{"name": "maju.co.id"}, 201)
	h.sql(`UPDATE sender_domains SET verified=true, spf_ok=true, dkim_ok=true WHERE id=$1`, d["id"])
}

func TestCampaignFlow(t *testing.T) {
	h := setup(t)
	h.onboard()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "c.csv")
	fw.Write([]byte("email,nama depan,perusahaan\nandi@acme.id,Andi,PT Acme\nsari@sejahtera.id,Sari,CV S\nbad,x,y\n"))
	mw.WriteField("tags", "b2b")
	mw.Close()
	req, _ := http.NewRequest("POST", h.srv.URL+"/v1/contacts/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+h.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("import: %v %v", err, resp.Status)
	}
	h.do("POST", "/v1/suppressions", map[string]any{"values": []string{"sari@sejahtera.id"}}, 201)

	c := h.do("POST", "/v1/campaigns", map[string]any{"name": "Q4", "from_email": "budi@maju.co.id",
		"subject": "Halo {{first_name}}", "html": `<p>Untuk {{company}} <a href="https://maju.co.id/p">lihat</a></p>`, "tag_filter": []string{"b2b"}}, 201)
	id := c["id"].(string)
	res := h.do("POST", "/v1/campaigns/"+id+"/launch", nil, 202)
	if res["queued"].(float64) != 1 {
		t.Fatalf("expected 1 queued (suppressed contact excluded): %v", res)
	}
	eventually(t, func() bool {
		return h.scalar(`SELECT count(*)::text FROM messages WHERE campaign_id=$1 AND status='sent'`, id) == "1"
	})

	html := h.scalar(`SELECT html FROM messages WHERE campaign_id=$1`, id)
	if !strings.Contains(html, "PT Acme") || !strings.Contains(html, "Berhenti berlangganan") {
		t.Fatalf("personalization/footer missing: %s", html)
	}
	click := regexp.MustCompile(`href="([^"]+/t/c/[^"]+)"`).FindStringSubmatch(html)[1]
	unsub := regexp.MustCompile(`href="([^"]+/u/[^"]+)"`).FindStringSubmatch(html)[1]
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if r, _ := noRedirect.Get(click); r.StatusCode != 302 || r.Header.Get("Location") != "https://maju.co.id/p" {
		t.Fatalf("click redirect: %v", r.Status)
	}
	if r, _ := http.Post(unsub, "", nil); r.StatusCode != 200 {
		t.Fatalf("unsubscribe: %v", r.Status)
	}
	st := h.do("GET", "/v1/campaigns/"+id+"/stats", nil, 200)
	if st["clicked"].(float64) != 1 || st["opened"].(float64) != 1 || st["unsubscribed"].(float64) != 1 {
		t.Fatalf("stats: %v", st)
	}
	if h.scalar(`SELECT reason FROM suppressions WHERE value='andi@acme.id'`) != "unsubscribe" {
		t.Fatal("unsubscribe must add a suppression entry")
	}
}

func TestOTPFlow(t *testing.T) {
	h := setup(t)
	h.onboard()
	o := h.do("POST", "/v1/otp/send", map[string]string{"to": "andi@acme.id", "from": "no-reply@maju.co.id", "brand": "Maju"}, 202)
	id := o["id"].(string)
	eventually(t, func() bool {
		return h.scalar(`SELECT count(*)::text FROM messages WHERE category='otp' AND status='sent'`) == "1"
	})
	code := regexp.MustCompile(`^\d{6}`).FindString(h.scalar(`SELECT subject FROM messages WHERE category='otp'`))
	h.do("POST", "/v1/otp/verify", map[string]string{"id": id, "code": "000000"}, 422)
	h.do("POST", "/v1/otp/verify", map[string]string{"id": id, "code": code}, 200)
	if r := h.do("POST", "/v1/otp/verify", map[string]string{"id": id, "code": code}, 422); r["reason"] != "already_used" {
		t.Fatalf("reuse: %v", r)
	}
	for range 4 {
		h.do("POST", "/v1/otp/send", map[string]string{"to": "andi@acme.id", "from": "no-reply@maju.co.id"}, 202)
	}
	h.do("POST", "/v1/otp/send", map[string]string{"to": "andi@acme.id", "from": "no-reply@maju.co.id"}, 429)
}

func TestSequenceStopsOnReply(t *testing.T) {
	h := setup(t)
	h.onboard()
	h.do("POST", "/v1/contacts", map[string]any{"email": "andi@acme.id", "first_name": "Andi", "tags": []string{"seq"}}, 200)
	s := h.do("POST", "/v1/sequences", map[string]any{"name": "F", "from_email": "budi@maju.co.id", "definition": map[string]any{
		"start": "e1", "nodes": []map[string]any{
			{"id": "e1", "type": "email", "subject": "Halo {{first_name}}", "text": "Hi", "next": "w1"},
			{"id": "w1", "type": "wait", "wait": map[string]int{"days": 3}, "next": "e2"},
			{"id": "e2", "type": "email", "subject": "Follow-up", "text": "?"},
		}}}, 201)
	id := s["id"].(string)
	h.do("POST", "/v1/sequences/"+id+"/activate", nil, 200)
	h.do("POST", "/v1/sequences/"+id+"/enroll", map[string]string{"tag": "seq"}, 200)
	if err := h.a.Sequences.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return h.scalar(`SELECT count(*)::text FROM messages WHERE category='sequence' AND status='sent'`) == "1"
	})

	hdr := h.scalar(`SELECT message_id_header FROM messages WHERE category='sequence'`)
	reply := "From: Andi <andi@acme.id>\r\nTo: budi@maju.co.id\r\nSubject: Re: Halo\r\nIn-Reply-To: <" + hdr + ">\r\n\r\nSaya tertarik!\r\n"
	if err := h.a.Inbound.Receive(context.Background(), "budi@maju.co.id", []byte(reply)); err != nil {
		t.Fatal(err)
	}
	if got := h.scalar(`SELECT status||'/'||stop_reason FROM enrollments`); got != "stopped/reply" {
		t.Fatalf("enrollment = %s", got)
	}
	if got := h.scalar(`SELECT classification FROM inbound_emails`); got != "interested" {
		t.Fatalf("classification = %s", got)
	}
}
