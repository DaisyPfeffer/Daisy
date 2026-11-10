package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Limiter is a small, nil-safe bandwidth limiter shared by many goroutines.
type Limiter struct {
	rate atomic.Int64 // bytes per second, <= 0 means unlimited
	mu   sync.Mutex
	next time.Time
}

func NewLimiter(bytesPerSec int64) *Limiter {
	l := &Limiter{}
	l.rate.Store(bytesPerSec)
	return l
}

func (l *Limiter) SetRate(bytesPerSec int64) {
	if l != nil {
		l.rate.Store(bytesPerSec)
	}
}

func (l *Limiter) Rate() int64 {
	if l == nil {
		return 0
	}
	return l.rate.Load()
}

// Wait blocks long enough so that n bytes fit inside the configured rate.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	rate := l.rate.Load()
	if rate <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wake := l.next
	l.next = l.next.Add(time.Duration(float64(n) / float64(rate) * float64(time.Second)))
	l.mu.Unlock()

	d := time.Until(wake)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
