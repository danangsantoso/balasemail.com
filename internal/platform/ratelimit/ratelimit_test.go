package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryFixedWindow(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 10, 0, time.UTC)
	m := NewMemory()
	m.Now = func() time.Time { return now }
	for i := range 3 {
		if ok, _, _ := m.Allow(context.Background(), "gmail.com", 3, time.Minute); !ok {
			t.Fatalf("event %d should be allowed", i)
		}
	}
	ok, retry, _ := m.Allow(context.Background(), "gmail.com", 3, time.Minute)
	if ok || retry != 50*time.Second {
		t.Fatalf("4th event: ok=%v retry=%v", ok, retry)
	}
	if ok, _, _ := m.Allow(context.Background(), "yahoo.com", 3, time.Minute); !ok {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Minute)
	if ok, _, _ := m.Allow(context.Background(), "gmail.com", 3, time.Minute); !ok {
		t.Fatal("new window should reset")
	}
}
