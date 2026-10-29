// Package queue is a durable PostgreSQL-backed job queue (FOR UPDATE SKIP LOCKED).
// Using Postgres keeps the MVP to one stateful dependency; the Queue/Handler
// contract allows swapping in Redis/RabbitMQ later without touching modules.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
)

// Priorities. OTP must never wait behind bulk mail (PRD F4).
const (
	PriorityBulk          = 0
	PriorityTransactional = 50
	PriorityOTP           = 100
)

// Job is a claimed unit of work.
type Job struct {
	ID       int64
	Kind     string
	Payload  json.RawMessage
	Attempts int
}

// Decode unmarshals the job payload.
func (j Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// Handler processes a job. Returning a Permanent error skips retries.
type Handler func(ctx context.Context, job Job) error

type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks err as non-retryable.
func Permanent(err error) error { return permanentError{err} }

// Options for Enqueue.
type Options struct {
	Priority    int
	RunAt       time.Time
	MaxAttempts int
}

// Queue enqueues jobs.
type Queue struct{ db database.DBTX }

func New(db database.DBTX) *Queue { return &Queue{db: db} }

// WithTx returns a Queue that enqueues inside the given transaction.
func (q *Queue) WithTx(tx database.DBTX) *Queue { return &Queue{db: tx} }

// Enqueue adds a job.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opt Options) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if opt.RunAt.IsZero() {
		opt.RunAt = time.Now()
	}
	if opt.MaxAttempts == 0 {
		opt.MaxAttempts = 8
	}
	_, err = q.db.Exec(ctx,
		`INSERT INTO jobs (kind, payload, priority, run_at, max_attempts) VALUES ($1,$2,$3,$4,$5)`,
		kind, b, opt.Priority, opt.RunAt, opt.MaxAttempts)
	return err
}

// Worker polls and executes jobs.
type Worker struct {
	pool        *pgxpool.Pool
	handlers    map[string]Handler
	Concurrency int
	PollEvery   time.Duration
	StaleAfter  time.Duration
}

func NewWorker(pool *pgxpool.Pool) *Worker {
	return &Worker{pool: pool, handlers: map[string]Handler{}, Concurrency: 8, PollEvery: time.Second, StaleAfter: 15 * time.Minute}
}

// Handle registers a handler for a job kind.
func (w *Worker) Handle(kind string, h Handler) { w.handlers[kind] = h }

// Run blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	kinds := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		kinds = append(kinds, k)
	}
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	ticker := time.NewTicker(w.PollEvery)
	defer ticker.Stop()
	lastReap := time.Time{}

	for {
		if time.Since(lastReap) > time.Minute {
			w.reapStale(ctx)
			lastReap = time.Now()
		}
		free := w.Concurrency - len(sem)
		if free > 0 {
			jobs, err := w.claim(ctx, kinds, free)
			if err != nil && ctx.Err() == nil {
				slog.Error("queue claim failed", "err", err)
			}
			for _, j := range jobs {
				sem <- struct{}{}
				wg.Add(1)
				go func(j Job) {
					defer func() { <-sem; wg.Done() }()
					w.execute(ctx, j)
				}(j)
			}
			if len(jobs) == free {
				continue // likely more work ready; skip the wait
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) claim(ctx context.Context, kinds []string, n int) ([]Job, error) {
	rows, err := w.pool.Query(ctx, `
		UPDATE jobs SET status='running', locked_at=now(), attempts=attempts+1, updated_at=now()
		WHERE id IN (
			SELECT id FROM jobs
			WHERE status='queued' AND run_at <= now() AND kind = ANY($1)
			ORDER BY priority DESC, run_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, payload, attempts`, kinds, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (w *Worker) execute(ctx context.Context, j Job) {
	start := time.Now()
	h := w.handlers[j.Kind]
	err := safeCall(ctx, h, j)
	metrics.JobDuration.WithLabelValues(j.Kind).Observe(time.Since(start).Seconds())
	// Use a fresh context so status is persisted even during shutdown.
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err == nil {
		metrics.JobsProcessed.WithLabelValues(j.Kind, "done").Inc()
		_, _ = w.pool.Exec(sctx, `UPDATE jobs SET status='done', last_error=NULL, updated_at=now() WHERE id=$1`, j.ID)
		return
	}
	var perm permanentError
	if errors.As(err, &perm) {
		metrics.JobsProcessed.WithLabelValues(j.Kind, "dead").Inc()
		slog.Warn("job failed permanently", "kind", j.Kind, "id", j.ID, "err", err)
		_, _ = w.pool.Exec(sctx, `UPDATE jobs SET status='dead', last_error=$2, updated_at=now() WHERE id=$1`, j.ID, err.Error())
		return
	}
	metrics.JobsProcessed.WithLabelValues(j.Kind, "retry").Inc()
	slog.Warn("job failed, will retry", "kind", j.Kind, "id", j.ID, "attempt", j.Attempts, "err", err)
	_, _ = w.pool.Exec(sctx, `
		UPDATE jobs SET
			status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'queued' END,
			run_at = now() + $2::interval, last_error=$3, locked_at=NULL, updated_at=now()
		WHERE id=$1`, j.ID, Backoff(j.Attempts).String(), err.Error())
}

func safeCall(ctx context.Context, h Handler, j Job) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return h(ctx, j)
}

func (w *Worker) reapStale(ctx context.Context) {
	_, err := w.pool.Exec(ctx, `
		UPDATE jobs SET status='queued', locked_at=NULL, updated_at=now()
		WHERE status='running' AND locked_at < now() - $1::interval`, w.StaleAfter.String())
	if err != nil && ctx.Err() == nil {
		slog.Error("reap stale jobs", "err", err)
	}
}

// Backoff is exponential with a 1h cap: 10s, 20s, 40s, ...
func Backoff(attempt int) time.Duration {
	d := time.Duration(float64(10*time.Second) * math.Pow(2, float64(max(attempt-1, 0))))
	return min(d, time.Hour)
}

// Periodic runs fn every interval until ctx is cancelled (used for schedulers).
func Periodic(ctx context.Context, name string, every time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			slog.Error("periodic task failed", "task", name, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
