// Command worker executes background jobs (delivery, validation, SMS, OTP
// fallback, webhooks) and periodic schedulers (sequence engine, reputation guard).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/app"
	"github.com/danangsantoso/balasemail.com/internal/config"
	"github.com/danangsantoso/balasemail.com/internal/platform/logger"
	"github.com/danangsantoso/balasemail.com/internal/platform/queue"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger.Setup(cfg.Env, "worker")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()

	w := queue.NewWorker(a.DB)
	if n, err := strconv.Atoi(os.Getenv("WORKER_CONCURRENCY")); err == nil && n > 0 {
		w.Concurrency = n
	}
	a.RegisterJobs(w)

	metricsAddr := os.Getenv("METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":9091"
	}
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: app.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = metricsSrv.ListenAndServe() }()

	var wg sync.WaitGroup
	for _, s := range a.Schedulers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			queue.Periodic(ctx, s.Name, s.Every, s.Fn)
		}()
	}
	slog.Info("worker started", "concurrency", w.Concurrency, "metrics", metricsAddr)
	err = w.Run(ctx)
	wg.Wait()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(shutdownCtx)
	return err
}
