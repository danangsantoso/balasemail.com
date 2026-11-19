// Command smtpd runs the SMTP Gateway: authenticated submission relay and
// inbound MX (replies, aliases, Email-to-SMS bridge).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danangsantoso/balasemail.com/internal/app"
	"github.com/danangsantoso/balasemail.com/internal/config"
	"github.com/danangsantoso/balasemail.com/internal/platform/logger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("smtpd exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger.Setup(cfg.Env, "smtpd")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()

	gw, err := a.Gateway()
	if err != nil {
		return err
	}
	submission := gw.SubmissionServer(cfg.SMTP.SubmissionAddr)
	inbound := gw.InboundServer(cfg.SMTP.InboundAddr)

	metricsAddr := os.Getenv("METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":9092"
	}
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: app.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 3)
	go func() { errCh <- submission.ListenAndServe() }()
	go func() { errCh <- inbound.ListenAndServe() }()
	go func() { errCh <- metricsSrv.ListenAndServe() }()
	slog.Info("smtpd listening", "submission", cfg.SMTP.SubmissionAddr, "inbound", cfg.SMTP.InboundAddr, "sms_domain", cfg.SMTP.SMSDomain)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, smtp.ErrServerClosed) && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = submission.Shutdown(shutdownCtx)
	_ = inbound.Shutdown(shutdownCtx)
	return metricsSrv.Shutdown(shutdownCtx)
}
