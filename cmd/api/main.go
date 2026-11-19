// Command api serves the BalasEmail REST API, tracking and unsubscribe endpoints.
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

	"github.com/danangsantoso/balasemail.com/internal/app"
	"github.com/danangsantoso/balasemail.com/internal/config"
	"github.com/danangsantoso/balasemail.com/internal/platform/logger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger.Setup(cfg.Env, "api")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           a.HTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	metricsSrv := &http.Server{Addr: env("METRICS_ADDR", ":9090"), Handler: app.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 2)
	go func() { errCh <- srv.ListenAndServe() }()
	go func() { errCh <- metricsSrv.ListenAndServe() }()
	slog.Info("api listening", "addr", cfg.HTTPAddr, "metrics", metricsSrv.Addr)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(shutdownCtx)
	return srv.Shutdown(shutdownCtx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
