package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/authctx"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so the first one is outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

type requestIDKey struct{}

// RequestID assigns an X-Request-ID to every request.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// Observe logs each request and records HTTP metrics keyed by route pattern.
func Observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(elapsed.Seconds())
		slog.InfoContext(r.Context(), "http",
			"method", r.Method, "route", route, "status", rec.status,
			"dur_ms", elapsed.Milliseconds(), "req_id", r.Context().Value(requestIDKey{}))
	})
}

// Recover turns panics into 500 responses.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.ErrorContext(r.Context(), "panic", "value", v, "path", r.URL.Path)
				JSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]string{"code": "internal", "message": "internal error"},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Authenticator resolves a bearer credential (JWT or API key) to a principal.
type Authenticator interface {
	Authenticate(ctx context.Context, bearer string) (authctx.Principal, error)
}

// RequireAuth rejects requests without a valid bearer credential.
func RequireAuth(a Authenticator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok || token == "" {
				Error(w, r, apperr.Unauthorized("missing_token", "Authorization: Bearer <token> is required"))
				return
			}
			p, err := a.Authenticate(r.Context(), token)
			if err != nil {
				Error(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(authctx.With(r.Context(), p)))
		})
	}
}

// RateLimitByIP is a coarse per-IP token bucket for abuse-prone endpoints.
// It is per-process; put a shared limiter at the edge for multi-instance setups.
func RateLimitByIP(perMinute, burst int) Middleware {
	var mu sync.Mutex
	limiters := map[string]*rate.Limiter{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			mu.Lock()
			l, ok := limiters[ip]
			if !ok {
				if len(limiters) > 100_000 {
					limiters = map[string]*rate.Limiter{}
				}
				l = rate.NewLimiter(rate.Limit(float64(perMinute)/60), burst)
				limiters[ip] = l
			}
			mu.Unlock()
			if !l.Allow() {
				Error(w, r, apperr.RateLimited("rate_limited", "too many requests"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
