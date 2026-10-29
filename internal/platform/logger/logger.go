// Package logger configures the process-wide structured logger.
package logger

import (
	"log/slog"
	"os"
)

// Setup installs a JSON logger in production and a text logger otherwise.
func Setup(env, service string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler
	if env == "production" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		opts.Level = slog.LevelDebug
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	l := slog.New(h).With("service", service)
	slog.SetDefault(l)
	return l
}
