// Package ratelimit implements fixed-window counters shared by every
// process through PostgreSQL, so limits hold across many worker instances.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/danangsantoso/balasemail.com/internal/platform/database"
)

// Limiter allows at most limit events per window for key.
type Limiter interface {
	// Allow records one event and reports whether it is within the limit.
	// When it is not, retryIn is the time until the current window ends.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ok bool, retryIn time.Duration, err error)
}

func windowStart(now time.Time, window time.Duration) time.Time { return now.Truncate(window) }

// Postgres is a Limiter backed by the rate_buckets table.
type Postgres struct{ db database.DBTX }

func NewPostgres(db database.DBTX) *Postgres { return &Postgres{db: db} }

func (p *Postgres) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	now := time.Now()
	start := windowStart(now, window)
	var n int
	err := p.db.QueryRow(ctx, `
		INSERT INTO rate_buckets (key, window_start, count) VALUES ($1, $2, 1)
		ON CONFLICT (key, window_start) DO UPDATE SET count = rate_buckets.count + 1
		RETURNING count`, key, start).Scan(&n)
	if err != nil {
		return false, 0, err
	}
	if n > limit {
		return false, start.Add(window).Sub(now), nil
	}
	return true, 0, nil
}

// Cleanup removes expired buckets; run periodically.
func (p *Postgres) Cleanup(ctx context.Context) error {
	_, err := p.db.Exec(ctx, `DELETE FROM rate_buckets WHERE window_start < now() - interval '2 hours'`)
	return err
}

// Memory is an in-process Limiter for tests and single-instance tools.
type Memory struct {
	mu     sync.Mutex
	counts map[string]int
	Now    func() time.Time
}

func NewMemory() *Memory { return &Memory{counts: map[string]int{}, Now: time.Now} }

func (m *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()
	start := windowStart(now, window)
	k := key + "@" + start.Format(time.RFC3339Nano)
	m.counts[k]++
	if m.counts[k] > limit {
		return false, start.Add(window).Sub(now), nil
	}
	return true, 0, nil
}
