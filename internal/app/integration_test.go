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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/config"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/webhook"
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
	if _, err := a.DB.Exec(ctx, `TRUNCATE workspaces, jobs, suppressions, rate_buckets CASCADE`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(a.HTTPHandler())
	_ = srv.Listener.Close()
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
	defer func() { _ = resp.Body.Close() }()
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
	_, _ = fw.Write([]byte("email,nama depan,perusahaan\nandi@acme.id,Andi,PT Acme\nsari@sejahtera.id,Sari,CV S\nbad,x,y\n"))
	_ = mw.WriteField("tags", "b2b")
	_ = mw.Close()
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

func TestAsyncBounceComplaintAndWebhooks(t *testing.T) {
	h := setup(t)
	h.onboard()

	var mu sync.Mutex
	var got []string
	var secret string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if !webhook.Verify(secret, r.Header.Get(webhook.SignatureHeader), body, time.Minute, time.Now()) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var p webhook.Payload
		_ = json.Unmarshal(body, &p)
		got = append(got, p.Type+":"+p.Data.To)
	}))
	defer hook.Close()
	ep := h.do("POST", "/v1/webhooks", map[string]any{"url": hook.URL, "events": []string{"bounce", "complaint"}}, 201)
	mu.Lock()
	secret = ep["secret"].(string)
	mu.Unlock()

	for _, e := range []string{"andi@acme.id", "budi@yahoo.com"} {
		h.do("POST", "/v1/contacts", map[string]any{"email": e, "tags": []string{"b2b"}}, 200)
	}
	c := h.do("POST", "/v1/campaigns", map[string]any{"name": "Q4", "from_email": "budi@maju.co.id",
		"subject": "Halo", "text": "Penawaran", "tag_filter": []string{"b2b"}}, 201)
	id := c["id"].(string)
	h.do("POST", "/v1/campaigns/"+id+"/launch", nil, 202)
	eventually(t, func() bool {
		return h.scalar(`SELECT count(*)::text FROM messages WHERE campaign_id=$1 AND status='sent'`, id) == "2"
	})

	ctx := context.Background()
	andi := h.scalar(`SELECT id FROM messages WHERE to_email='andi@acme.id'`)
	dsn := "From: mailer-daemon@acme.id\r\nSubject: Delivery Status Notification (Failure)\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"X\"\r\n\r\n" +
		"--X\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; acme.id\r\n\r\n" +
		"Final-Recipient: rfc822; andi@acme.id\r\nAction: failed\r\nStatus: 5.1.1\r\n--X--\r\n"
	if _, err := h.a.Bounces.Process(ctx, h.a.Bounces.VERP.Address(andi), []byte(dsn)); err != nil {
		t.Fatal(err)
	}

	hdr := h.scalar(`SELECT message_id_header FROM messages WHERE to_email='budi@yahoo.com'`)
	arf := "From: feedback@yahoo.com\r\nMIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=feedback-report; boundary=\"F\"\r\n\r\n" +
		"--F\r\nContent-Type: message/feedback-report\r\n\r\nFeedback-Type: abuse\r\nVersion: 1\r\n" +
		"--F\r\nContent-Type: message/rfc822\r\n\r\nMessage-ID: <" + hdr + ">\r\n\r\nx\r\n--F--\r\n"
	if _, err := h.a.Bounces.Process(ctx, "fbl@"+h.a.Cfg.SMTP.BounceDomain, []byte(arf)); err != nil {
		t.Fatal(err)
	}

	if r := h.scalar(`SELECT string_agg(value||'='||reason, ',' ORDER BY value) FROM suppressions`); r != "andi@acme.id=hard_bounce,budi@yahoo.com=complaint" {
		t.Fatalf("suppressions = %s", r)
	}
	st := h.do("GET", "/v1/campaigns/"+id+"/stats", nil, 200)
	if st["bounced"].(float64) != 1 {
		t.Fatalf("stats = %v", st)
	}
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(got, "bounce:andi@acme.id") || !slices.Contains(got, "complaint:budi@yahoo.com") {
		t.Fatalf("webhooks = %v", got)
	}
}

func TestSharedRateLimiter(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	for i := range 3 {
		ok, retry, err := h.a.Limiter.Allow(ctx, "rcpt-domain:test.id", 2, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if want := i < 2; ok != want || (!ok && retry <= 0) {
			t.Fatalf("call %d: ok=%v retry=%v", i, ok, retry)
		}
	}
}

func TestQueueRetryAfterKeepsAttempts(t *testing.T) {
	h := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan int, 4)
	w := queue.NewWorker(h.a.DB)
	w.PollEvery = 50 * time.Millisecond
	w.Handle("test.throttled", func(context.Context, queue.Job) error {
		calls <- 1
		return queue.RetryAfter(time.Hour)
	})
	go w.Run(ctx)
	if err := h.a.Queue.Enqueue(ctx, "test.throttled", map[string]string{}, queue.Options{}); err != nil {
		t.Fatal(err)
	}
	<-calls
	eventually(t, func() bool {
		return h.scalar(`SELECT status||'/'||attempts||'/'||(run_at > now() + interval '50 minutes') FROM jobs WHERE kind='test.throttled'`) == "queued/0/true"
	})
}

func (h *harness) login(email, password string) string {
	h.t.Helper()
	h.tok = ""
	s := h.do("POST", "/v1/auth/login", map[string]string{"email": email, "password": password}, 200)
	h.tok = s["token"].(string)
	return h.tok
}

func (h *harness) register(company, email string) string {
	h.t.Helper()
	h.tok = ""
	s := h.do("POST", "/v1/auth/register", map[string]string{"company_name": company, "full_name": "Admin " + company,
		"email": email, "password": "password-kuat-123"}, 201)
	h.tok = s["token"].(string)
	return h.tok
}

func TestMultiTenantRoles(t *testing.T) {
	t.Setenv("SUPERADMIN_EMAILS", "root@platform.co.id")
	h := setup(t)

	// Tenant A: admin adds a regular user.
	adminA := h.register("PT Maju", "budi@maju.co.id")
	h.sql(`UPDATE workspaces SET plan='growth', plan_ends_at=NULL WHERE name='PT Maju'`) // the trial allows a single user
	h.do("POST", "/v1/contacts", map[string]any{"email": "klien@contoh.co.id"}, 200)
	member := h.do("POST", "/v1/team", map[string]string{"email": "sari@maju.co.id", "full_name": "Sari", "role": "user", "password": "password-sari-123"}, 201)
	h.do("POST", "/v1/team", map[string]string{"email": "x@gmail.com", "full_name": "X", "password": "password-sari-123"}, 422)
	me := h.do("GET", "/v1/me", nil, 200)
	selfID := me["user"].(map[string]any)["id"].(string)
	h.do("PATCH", "/v1/team/"+selfID, map[string]any{"role": "user"}, 422) // cannot demote yourself

	// Regular user: operational routes yes, admin routes no.
	userA := h.login("sari@maju.co.id", "password-sari-123")
	h.do("POST", "/v1/contacts", map[string]any{"email": "prospek@contoh.co.id"}, 200)
	h.do("GET", "/v1/team", nil, 200)
	h.do("POST", "/v1/domains", map[string]string{"name": "maju.co.id"}, 403)
	h.do("POST", "/v1/api-keys", map[string]string{"name": "k"}, 403)
	h.do("PATCH", "/v1/team/"+selfID, map[string]any{"role": "user"}, 403)
	h.do("GET", "/v1/admin/tenants", nil, 403)

	// Tenant B cannot see tenant A's data.
	h.register("PT Acme", "andi@acme.co.id")
	if got := h.do("GET", "/v1/contacts", nil, 200)["data"].([]any); len(got) != 0 {
		t.Fatalf("tenant B sees %d contacts of tenant A", len(got))
	}
	h.do("GET", "/v1/admin/overview", nil, 403)
	tokB := h.tok

	// Admin A disables the user; the existing session stops working at once.
	h.tok = adminA
	h.do("PATCH", "/v1/team/"+member["id"].(string), map[string]any{"disabled": true}, 200)
	h.tok = userA
	h.do("GET", "/v1/contacts", nil, 403)

	// Super admin manages tenants.
	h.register("Platform", "root@platform.co.id")
	if me := h.do("GET", "/v1/me", nil, 200); me["user"].(map[string]any)["platform_role"] != "superadmin" {
		t.Fatalf("superadmin not promoted: %v", me["user"])
	}
	tenants := h.do("GET", "/v1/admin/tenants", nil, 200)["data"].([]any)
	if len(tenants) != 3 {
		t.Fatalf("tenants = %d, want 3", len(tenants))
	}
	var acmeID, majuID string
	for _, x := range tenants {
		m := x.(map[string]any)
		switch m["name"] {
		case "PT Acme":
			acmeID = m["id"].(string)
		case "PT Maju":
			majuID = m["id"].(string)
			if m["contacts"].(float64) != 2 || m["users"].(float64) != 2 {
				t.Errorf("PT Maju usage = %v", m)
			}
		}
	}
	h.do("PATCH", "/v1/admin/tenants/"+acmeID, map[string]any{"status": "suspended"}, 200)
	updated := h.do("PATCH", "/v1/admin/tenants/"+majuID, map[string]any{"plan": "growth", "daily_send_limit": 5000, "add_sms_credits": 250}, 200)
	if updated["plan"] != "growth" || updated["sms_credits"].(float64) != 250 || updated["daily_send_limit"].(float64) != 5000 {
		t.Fatalf("update = %v", updated)
	}
	h.do("PATCH", "/v1/admin/tenants/"+majuID, map[string]any{"plan": "gratis"}, 422)
	ov := h.do("GET", "/v1/admin/overview?days=7", nil, 200)
	if ov["tenants"].(float64) != 3 || ov["suspended"].(float64) != 1 || len(ov["series"].([]any)) != 7 {
		t.Fatalf("overview = %v", ov)
	}

	// The suspended tenant is locked out.
	h.tok = tokB
	h.do("GET", "/v1/contacts", nil, 403)

	// Tenant dashboard stats.
	h.tok = adminA
	st := h.do("GET", "/v1/stats/overview?days=14", nil, 200)
	if len(st["series"].([]any)) != 14 {
		t.Fatalf("stats series = %v", st["series"])
	}
}

func TestTrialAndPlans(t *testing.T) {
	t.Setenv("SUPERADMIN_EMAILS", "root@platform.co.id")
	h := setup(t)

	// The catalog is public: the landing page and the dashboard read the same plans.
	if plans := h.do("GET", "/v1/plans", nil, 200)["plans"].([]any); len(plans) != 5 {
		t.Fatalf("plans = %d, want 5", len(plans))
	}

	// A new tenant starts on the 3-day trial at 30 emails a day.
	admin := h.register("PT Coba", "dina@coba.co.id")
	b := h.do("GET", "/v1/billing", nil, 200)
	if b["plan"].(map[string]any)["key"] != "trial" || b["daily_send_limit"].(float64) != 30 || b["days_left"].(float64) != 3 || b["expired"] != false {
		t.Fatalf("new tenant billing = %v", b)
	}
	// The same company cannot restart the trial with another mailbox.
	h.tok = ""
	h.do("POST", "/v1/auth/register", map[string]string{"company_name": "PT Coba 2", "full_name": "Eko",
		"email": "eko@coba.co.id", "password": "password-kuat-123"}, 409)

	// Trial limits: one user, one sender domain, 100 contacts.
	h.tok = admin
	h.do("POST", "/v1/team", map[string]string{"email": "eko@coba.co.id", "full_name": "Eko", "password": "password-kuat-123"}, 402)
	h.do("POST", "/v1/domains", map[string]string{"name": "coba.co.id"}, 201)
	h.do("POST", "/v1/domains", map[string]string{"name": "coba-lain.co.id"}, 402)
	h.sql(`INSERT INTO contacts (workspace_id, email) SELECT id, 'k' || n || '@klien.co.id' FROM workspaces, generate_series(1, 100) n WHERE name='PT Coba'`)
	h.do("POST", "/v1/contacts", map[string]any{"email": "baru@klien.co.id"}, 402)
	h.do("POST", "/v1/contacts", map[string]any{"email": "k1@klien.co.id", "first_name": "Kiki"}, 200) // updating an existing contact is fine

	// Asking for an upgrade is recorded for the platform admin.
	h.do("POST", "/v1/billing/upgrade-request", map[string]string{"plan": "trial"}, 422)
	if b := h.do("POST", "/v1/billing/upgrade-request", map[string]string{"plan": "growth", "cycle": "yearly"}, 200); b["requested_plan"] != "growth" {
		t.Fatalf("upgrade request = %v", b)
	}

	// When the trial ends, sending and prospecting stop; data stays readable.
	h.sql(`UPDATE workspaces SET plan_ends_at = now() - interval '1 hour' WHERE name='PT Coba'`)
	h.do("POST", "/v1/finder/person", map[string]string{"first_name": "A", "last_name": "B", "domain": "klien.co.id"}, 402)
	h.do("POST", "/v1/otp/send", map[string]string{"to": "a@klien.co.id"}, 402)
	h.do("GET", "/v1/contacts", nil, 200)
	if b := h.do("GET", "/v1/billing", nil, 200); b["expired"] != true {
		t.Fatalf("expired trial billing = %v", b)
	}
	var wsID string
	if err := h.a.DB.QueryRow(context.Background(), `SELECT id FROM workspaces WHERE name='PT Coba'`).Scan(&wsID); err != nil {
		t.Fatal(err)
	}
	if left, err := h.a.Delivery.RemainingQuota(context.Background(), wsID); err != nil || left != 0 {
		t.Fatalf("expired trial quota = %d, %v", left, err)
	}

	// The platform admin activates the requested plan for a year.
	h.register("Platform", "root@platform.co.id")
	if b := h.do("GET", "/v1/billing", nil, 200); b["plan"].(map[string]any)["key"] != "enterprise" || b["plan_ends_at"] != nil {
		t.Fatalf("platform operators are not put on a trial: %v", b)
	}
	up := h.do("PATCH", "/v1/admin/tenants/"+wsID, map[string]any{"plan": "growth", "months": 12}, 200)
	if up["plan"] != "growth" || up["daily_send_limit"].(float64) != 2500 || up["requested_plan"] != nil {
		t.Fatalf("activated tenant = %v", up)
	}
	ends, err := time.Parse(time.RFC3339, up["plan_ends_at"].(string))
	if err != nil || time.Until(ends) < 360*24*time.Hour || time.Until(ends) > 370*24*time.Hour {
		t.Fatalf("plan_ends_at = %v (%v), want about a year from now", up["plan_ends_at"], err)
	}
	// Extending adds to the end of the current period.
	ext := h.do("PATCH", "/v1/admin/tenants/"+wsID, map[string]any{"months": 1}, 200)
	if ends2, _ := time.Parse(time.RFC3339, ext["plan_ends_at"].(string)); ends2.Sub(ends) < 27*24*time.Hour {
		t.Fatalf("extension moved the end from %v to %v", ends, ends2)
	}

	// The tenant is unblocked and gets the bigger limits.
	h.tok = admin
	if b := h.do("GET", "/v1/billing", nil, 200); b["expired"] != false || b["requested_plan"] != nil {
		t.Fatalf("billing after activation = %v", b)
	}
	h.do("POST", "/v1/team", map[string]string{"email": "eko@coba.co.id", "full_name": "Eko", "password": "password-kuat-123"}, 201)
	h.do("POST", "/v1/contacts", map[string]any{"email": "baru@klien.co.id"}, 200)
}
