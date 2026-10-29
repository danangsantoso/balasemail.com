// Package app wires configuration, infrastructure and modules into one
// container shared by cmd/api, cmd/worker and cmd/smtpd.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/alias"
	"github.com/danangsantoso/balasemail.com/internal/auth"
	"github.com/danangsantoso/balasemail.com/internal/bounce"
	"github.com/danangsantoso/balasemail.com/internal/campaign"
	"github.com/danangsantoso/balasemail.com/internal/config"
	"github.com/danangsantoso/balasemail.com/internal/contact"
	"github.com/danangsantoso/balasemail.com/internal/delivery"
	"github.com/danangsantoso/balasemail.com/internal/finder"
	"github.com/danangsantoso/balasemail.com/internal/inbound"
	"github.com/danangsantoso/balasemail.com/internal/mailer"
	"github.com/danangsantoso/balasemail.com/internal/otp"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/netguard"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
	"github.com/danangsantoso/balasemail.com/internal/platform/ratelimit"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
	"github.com/danangsantoso/balasemail.com/internal/senderdomain"
	"github.com/danangsantoso/balasemail.com/internal/sequence"
	"github.com/danangsantoso/balasemail.com/internal/sms"
	"github.com/danangsantoso/balasemail.com/internal/smtpgw"
	"github.com/danangsantoso/balasemail.com/internal/suppression"
	"github.com/danangsantoso/balasemail.com/internal/validator"
	"github.com/danangsantoso/balasemail.com/internal/web"
	"github.com/danangsantoso/balasemail.com/internal/webhook"
	"github.com/danangsantoso/balasemail.com/migrations"
)

type App struct {
	Cfg   config.Config
	DB    *pgxpool.Pool
	Queue *queue.Queue

	Auth        *auth.Service
	Domains     *senderdomain.Service
	Validator   *validator.Validator
	Finder      *finder.Service
	Contacts    *contact.Service
	Suppression *suppression.Service
	Delivery    *delivery.Service
	Campaigns   *campaign.Service
	Sequences   *sequence.Service
	Aliases     *alias.Service
	Inbound     *inbound.Service
	OTP         *otp.Service
	SMS         *sms.Service
	Webhooks    *webhook.Service
	Bounces     *bounce.Processor
	Limiter     *ratelimit.Postgres
}

// New connects to the database, applies migrations and builds every module.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	cipher, err := security.NewCipher(cfg.EncryptionKey)
	if err != nil {
		db.Close()
		return nil, err
	}
	signer := security.NewSigner(cfg.SigningSecret)
	q := queue.New(db)
	resolver := net.DefaultResolver

	a := &App{Cfg: cfg, DB: db, Queue: q}
	a.Auth = auth.NewService(db, security.NewJWTIssuer(cfg.JWTSecret, cfg.JWTTTL))
	a.Domains = senderdomain.NewService(db, cipher, resolver, cfg.SPFInclude, cfg.DKIMSelector)
	a.Domains.SkipDNS = cfg.DevSkipDNSCheck
	a.Suppression = suppression.NewService(db)

	var prober validator.Prober
	if cfg.Validator.SMTPProbe {
		prober = &validator.SMTPProber{HeloName: cfg.Validator.HeloName, MailFrom: cfg.Validator.MailFrom, Timeout: cfg.Validator.Timeout}
	}
	a.Validator = validator.New(resolver, prober)
	a.Contacts = contact.NewService(db, a.Suppression, q, a.Validator)
	outbound := netguard.Client(10*time.Second, cfg.AllowPrivateNetworks)
	a.Finder = finder.NewService(&finder.Crawler{
		Client:    outbound,
		UserAgent: cfg.Finder.UserAgent, MaxPages: cfg.Finder.MaxPages, Delay: 500 * time.Millisecond,
	}, a.Validator, a.Suppression, a.Contacts)

	a.Limiter = ratelimit.NewPostgres(db)
	verp := bounce.VERP{Signer: signer, Domain: cfg.SMTP.BounceDomain}
	a.Delivery = delivery.NewService(db, q, a.Suppression, a.Domains, mailRouter(cfg), a.Limiter, delivery.DomainLimits{
		Default:   600,
		Overrides: map[string]int{"gmail.com": 300, "googlemail.com": 300, "yahoo.com": 200, "outlook.com": 200, "hotmail.com": 200},
	}, verp, signer, cfg.PublicBaseURL)
	a.Bounces = &bounce.Processor{VERP: verp, Rec: a.Delivery}
	a.Webhooks = webhook.NewService(db, q, cipher, outbound)
	a.Webhooks.AllowHTTP = cfg.Env != "production"
	a.Delivery.OnEvent(a.Webhooks.OnDeliveryEvent)
	a.Campaigns = campaign.NewService(db, a.Delivery, a.Domains)
	a.SMS = sms.NewService(db, q, smsRouter(cfg))
	a.Sequences = sequence.NewService(db, a.Delivery, a.Domains, a.Contacts, a.SMS, q, signer, outbound)
	a.Aliases = alias.NewService(db, a.Domains)
	a.Inbound = inbound.NewService(db, a.Delivery, a.Suppression, a.Aliases, a.Domains, inbound.KeywordClassifier{})
	a.OTP = otp.NewService(db, a.Delivery, a.authorizeSender, a.SMS, q, signer, cipher)
	return a, nil
}

func (a *App) Close() { a.DB.Close() }

func (a *App) authorizeSender(ctx context.Context, wsID, from string) error {
	_, err := a.Domains.AuthorizeSender(ctx, wsID, from)
	return err
}

func mailRouter(cfg config.Config) *mailer.Router {
	u := cfg.Upstream
	if u.Host == "" {
		return mailer.NewRouter(&mailer.LogSender{})
	}
	senders := []mailer.Sender{&mailer.SMTPRelay{Label: "primary:" + u.Host, Host: u.Host, Port: u.Port,
		Username: u.Username, Password: u.Password, HeloName: cfg.SMTP.Hostname}}
	if u.FallbackHost != "" {
		senders = append(senders, &mailer.SMTPRelay{Label: "fallback:" + u.FallbackHost, Host: u.FallbackHost, Port: u.FallbackPort,
			Username: u.FallbackUsername, Password: u.FallbackPassword, HeloName: cfg.SMTP.Hostname})
	}
	return mailer.NewRouter(senders...)
}

func smsRouter(cfg config.Config) *sms.Router {
	var ps []sms.Provider
	for _, name := range cfg.SMS.Providers {
		switch name {
		case "twilio":
			ps = append(ps, &sms.Twilio{AccountSID: cfg.SMS.TwilioAccountSID, AuthToken: cfg.SMS.TwilioAuthToken, From: cfg.SMS.TwilioFrom})
		case "zenziva":
			ps = append(ps, &sms.Zenziva{UserKey: cfg.SMS.ZenzivaUserKey, PassKey: cfg.SMS.ZenzivaPassKey})
		case "log":
			ps = append(ps, sms.LogProvider{})
		}
	}
	if len(ps) == 0 {
		ps = append(ps, sms.LogProvider{})
	}
	return sms.NewRouter(ps...)
}

// HTTPHandler builds the public REST API.
func (a *App) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	authed := httpx.RequireAuth(a.Auth)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.DB.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})

	a.Auth.Routes(mux, authed)
	a.Domains.Routes(mux, authed)
	a.Validator.Routes(mux, authed)
	a.Finder.Routes(mux, authed)
	a.Contacts.Routes(mux, authed)
	a.Suppression.Routes(mux, authed)
	a.Campaigns.Routes(mux, authed)
	a.Sequences.Routes(mux, authed)
	a.Aliases.Routes(mux, authed)
	a.Inbound.Routes(mux, authed)
	a.OTP.Routes(mux, authed)
	a.SMS.Routes(mux, authed)
	a.Webhooks.Routes(mux, authed)
	a.Delivery.PublicRoutes(mux)
	web.Routes(mux)

	return httpx.Chain(mux, httpx.Recover, httpx.RequestID, httpx.Observe)
}

// MetricsHandler serves Prometheus metrics (bind to an internal port).
func MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	return mux
}

// RegisterJobs attaches every background job handler to the worker.
func (a *App) RegisterJobs(w *queue.Worker) {
	w.Handle(delivery.JobSend, a.Delivery.HandleSend)
	w.Handle(contact.JobValidate, a.Contacts.HandleValidateJob)
	w.Handle(sms.JobSend, a.SMS.HandleSend)
	w.Handle(otp.JobFallback, a.OTP.HandleFallback)
	w.Handle(sequence.JobWebhook, a.Sequences.HandleWebhook)
	w.Handle(webhook.JobDeliver, a.Webhooks.HandleDeliver)
}

// Scheduler is a periodic task run by the worker.
type Scheduler struct {
	Name  string
	Every time.Duration
	Fn    func(context.Context) error
}

func (a *App) Schedulers() []Scheduler {
	return []Scheduler{
		{"sequence.tick", 30 * time.Second, a.Sequences.Tick},
		{"campaign.complete", time.Minute, a.Campaigns.CompleteFinished},
		{"reputation.enforce", 15 * time.Minute, a.Delivery.EnforceReputation},
		{"domains.recheck", 30 * time.Minute, a.Domains.RecheckAll},
		{"ratelimit.cleanup", 30 * time.Minute, a.Limiter.Cleanup},
	}
}

// ---- SMTP gateway adapters ----

type senderAdapter struct{ a *App }

func (s senderAdapter) AuthorizeSenderErr(ctx context.Context, wsID, from string) error {
	return s.a.authorizeSender(ctx, wsID, from)
}

type relayAdapter struct{ a *App }

func (r relayAdapter) QueueRelay(ctx context.Context, wsID, from, to string, raw []byte) error {
	_, err := r.a.Delivery.Queue(ctx, r.a.DB, delivery.Outgoing{
		WorkspaceID: wsID, Category: delivery.CategoryRelay, FromEmail: from, ToEmail: to, Raw: raw,
	})
	if errors.Is(err, delivery.ErrSuppressed) {
		return nil // accepted and dropped, like any ESP
	}
	return err
}

type bounceAdapter struct{ p *bounce.Processor }

func (b bounceAdapter) Accepts(rcpt string) bool { return b.p.Accepts(rcpt) }
func (b bounceAdapter) Process(ctx context.Context, rcpt string, raw []byte) error {
	_, err := b.p.Process(ctx, rcpt, raw)
	return err
}

// Gateway builds the SMTP gateway.
func (a *App) Gateway() (*smtpgw.Gateway, error) {
	var tlsCfg *tls.Config
	if a.Cfg.SMTP.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(a.Cfg.SMTP.TLSCertFile, a.Cfg.SMTP.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load smtp tls: %w", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	return smtpgw.New(smtpgw.Config{
		Hostname:          a.Cfg.SMTP.Hostname,
		SMSDomain:         a.Cfg.SMTP.SMSDomain,
		MaxMessageBytes:   a.Cfg.SMTP.MaxMessageBytes,
		TLS:               tlsCfg,
		AllowInsecureAuth: a.Cfg.Env != "production",
	}, a.Auth, senderAdapter{a}, relayAdapter{a}, a.SMS, a.Inbound, bounceAdapter{a.Bounces}), nil
}
