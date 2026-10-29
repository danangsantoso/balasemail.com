// Package metrics declares the Prometheus metrics that back the PRD KPIs
// (deliverability, bounce rate, OTP latency, validation accuracy, uptime).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ns = "balasemail"

var (
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "http_requests_total", Help: "HTTP requests by route and status.",
	}, []string{"route", "status"})

	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "http_request_duration_seconds", Help: "HTTP latency by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	// EmailsSent counts delivery attempts. status: sent|deferred|bounced|failed.
	EmailsSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "emails_sent_total", Help: "Outbound email attempts by category, provider and status.",
	}, []string{"category", "provider", "status"})

	// EmailEvents counts engagement events: open|click|reply|unsubscribe|complaint.
	EmailEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "email_events_total", Help: "Recipient engagement events.",
	}, []string{"type"})

	Validations = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "email_validations_total", Help: "Email validations by result status.",
	}, []string{"status"})

	FinderResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "finder_searches_total", Help: "Email finder searches by type and outcome.",
	}, []string{"type", "outcome"})

	OTPSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "otp_sent_total", Help: "OTP codes issued by channel.",
	}, []string{"channel"})

	OTPVerify = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "otp_verify_total", Help: "OTP verifications by result.",
	}, []string{"result"})

	// OTPDeliveryLatency is the KPI "OTP p95 < 10s": issue time to provider acceptance.
	OTPDeliveryLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns, Name: "otp_delivery_seconds", Help: "Time from OTP issue to handoff to provider.",
		Buckets: []float64{0.25, 0.5, 1, 2, 5, 10, 20, 30},
	})

	SMSSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "sms_sent_total", Help: "SMS sends by provider and status.",
	}, []string{"provider", "status"})

	InboundEmails = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "inbound_emails_total", Help: "Inbound emails by classification.",
	}, []string{"classification"})

	InboundActions = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "inbound_actions_total", Help: "Auto-reply/forward actions by outcome.",
	}, []string{"action", "outcome"})

	JobsProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "jobs_processed_total", Help: "Background jobs by kind and outcome.",
	}, []string{"kind", "outcome"})

	JobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "job_duration_seconds", Help: "Background job duration by kind.",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind"})

	SequenceTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "sequence_transitions_total", Help: "Sequence engine node executions by node type.",
	}, []string{"node_type"})
)

// Handler exposes /metrics.
func Handler() http.Handler { return promhttp.Handler() }
