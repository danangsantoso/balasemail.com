// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is shared by cmd/api, cmd/worker and cmd/smtpd.
type Config struct {
	Env           string // development|production
	HTTPAddr      string
	PublicBaseURL string // used for unsubscribe & tracking links
	DatabaseURL   string

	JWTSecret     string
	JWTTTL        time.Duration
	SigningSecret string // HMAC for unsubscribe/tracking/OTP hashes
	EncryptionKey string // AES-GCM for DKIM keys & secrets at rest

	// SuperAdminEmails are promoted to platform super admin when they sign in.
	SuperAdminEmails []string

	// DevSkipDNSCheck treats sender-domain DNS checks as passing (ignored in production).
	DevSkipDNSCheck bool

	// AllowPrivateNetworks lets webhooks/crawler reach private IPs (development only).
	AllowPrivateNetworks bool

	SPFInclude   string // e.g. "_spf.balasemail.com"
	DKIMSelector string

	SMTP      SMTPConfig
	Upstream  UpstreamConfig
	SMS       SMSConfig
	Validator ValidatorConfig
	Finder    FinderConfig
}

type SMTPConfig struct {
	Hostname        string // EHLO/banner name, e.g. mx.balasemail.com
	SubmissionAddr  string // authenticated relay (587)
	InboundAddr     string // MX for replies, aliases and SMS bridge (25)
	SMSDomain       string // e.g. sms.balasemail.com
	BounceDomain    string // VERP return-path domain, MX must point at smtpd (e.g. bounces.balasemail.com)
	TLSCertFile     string
	TLSKeyFile      string
	MaxMessageBytes int64
}

// UpstreamConfig is the relay provider list. Empty Host means "log only" (dev).
type UpstreamConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// Optional secondary relay for failover (PRD F6).
	FallbackHost     string
	FallbackPort     int
	FallbackUsername string
	FallbackPassword string
}

type SMSConfig struct {
	Providers        []string // ordered, e.g. "twilio,zenziva" — first is primary
	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFrom       string
	ZenzivaUserKey   string
	ZenzivaPassKey   string
}

type ValidatorConfig struct {
	SMTPProbe bool // outbound port 25 is required; disable in clouds that block it
	HeloName  string
	MailFrom  string
	Timeout   time.Duration
}

type FinderConfig struct {
	UserAgent string
	MaxPages  int
	Timeout   time.Duration
}

// Load reads the environment and validates required settings.
func Load() (Config, error) {
	c := Config{
		Env:           env("APP_ENV", "development"),
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		PublicBaseURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:   env("DATABASE_URL", "postgres://balasemail:balasemail@localhost:5432/balasemail?sslmode=disable"),
		JWTSecret:     env("JWT_SECRET", ""),
		JWTTTL:        envDuration("JWT_TTL", 24*time.Hour),
		SigningSecret: env("SIGNING_SECRET", ""),
		EncryptionKey: env("ENCRYPTION_KEY", ""),
		SPFInclude:    env("SPF_INCLUDE", "_spf.balasemail.com"),
		DKIMSelector:  env("DKIM_SELECTOR", "bm1"),
		SMTP: SMTPConfig{
			Hostname:        env("SMTP_HOSTNAME", "localhost"),
			SubmissionAddr:  env("SMTP_SUBMISSION_ADDR", ":2587"),
			InboundAddr:     env("SMTP_INBOUND_ADDR", ":2525"),
			SMSDomain:       strings.ToLower(env("SMS_DOMAIN", "sms.balasemail.local")),
			BounceDomain:    strings.ToLower(env("BOUNCE_DOMAIN", "bounces.balasemail.local")),
			TLSCertFile:     env("SMTP_TLS_CERT", ""),
			TLSKeyFile:      env("SMTP_TLS_KEY", ""),
			MaxMessageBytes: int64(envInt("SMTP_MAX_MESSAGE_BYTES", 25<<20)),
		},
		Upstream: UpstreamConfig{
			Host:             env("UPSTREAM_SMTP_HOST", ""),
			Port:             envInt("UPSTREAM_SMTP_PORT", 587),
			Username:         env("UPSTREAM_SMTP_USERNAME", ""),
			Password:         env("UPSTREAM_SMTP_PASSWORD", ""),
			FallbackHost:     env("FALLBACK_SMTP_HOST", ""),
			FallbackPort:     envInt("FALLBACK_SMTP_PORT", 587),
			FallbackUsername: env("FALLBACK_SMTP_USERNAME", ""),
			FallbackPassword: env("FALLBACK_SMTP_PASSWORD", ""),
		},
		SMS: SMSConfig{
			Providers:        splitList(env("SMS_PROVIDERS", "log")),
			TwilioAccountSID: env("TWILIO_ACCOUNT_SID", ""),
			TwilioAuthToken:  env("TWILIO_AUTH_TOKEN", ""),
			TwilioFrom:       env("TWILIO_FROM", ""),
			ZenzivaUserKey:   env("ZENZIVA_USERKEY", ""),
			ZenzivaPassKey:   env("ZENZIVA_PASSKEY", ""),
		},
		Validator: ValidatorConfig{
			SMTPProbe: envBool("VALIDATOR_SMTP_PROBE", false),
			HeloName:  env("VALIDATOR_HELO", "verify.balasemail.com"),
			MailFrom:  env("VALIDATOR_MAIL_FROM", "verify@balasemail.com"),
			Timeout:   envDuration("VALIDATOR_TIMEOUT", 10*time.Second),
		},
		Finder: FinderConfig{
			UserAgent: env("FINDER_USER_AGENT", "BalasEmailBot/1.0 (+https://balasemail.com/bot)"),
			MaxPages:  envInt("FINDER_MAX_PAGES", 12),
			Timeout:   envDuration("FINDER_TIMEOUT", 25*time.Second),
		},
	}

	c.SuperAdminEmails = splitList(env("SUPERADMIN_EMAILS", ""))
	c.AllowPrivateNetworks = envBool("OUTBOUND_ALLOW_PRIVATE", c.Env == "development")
	c.DevSkipDNSCheck = c.Env != "production" && envBool("DEV_SKIP_DNS_CHECK", false)
	var errs []error
	if c.Env == "development" {
		// Convenience defaults for local runs only.
		c.JWTSecret = orDefault(c.JWTSecret, "dev-jwt-secret-change-me")
		c.SigningSecret = orDefault(c.SigningSecret, "dev-signing-secret-change-me")
		c.EncryptionKey = orDefault(c.EncryptionKey, "dev-encryption-key-change-me")
	}
	for name, v := range map[string]string{"JWT_SECRET": c.JWTSecret, "SIGNING_SECRET": c.SigningSecret, "ENCRYPTION_KEY": c.EncryptionKey} {
		if len(v) < 16 {
			errs = append(errs, fmt.Errorf("%s must be set (min 16 chars)", name))
		}
	}
	return c, errors.Join(errs...)
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
