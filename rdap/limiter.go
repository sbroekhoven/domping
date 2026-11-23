package rdap

import (
	"context"
	"sync"
	"time"
)

// hostLimiter spaces requests to the same host at least interval apart.
// Each caller reserves the next free slot, so waiters are served in order.
type hostLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     map[string]time.Time
}

func newHostLimiter(interval time.Duration) *hostLimiter {
	return &hostLimiter{interval: interval, next: map[string]time.Time{}}
}

// Wait blocks until host may be contacted or ctx is done.
func (l *hostLimiter) Wait(ctx context.Context, host string) error {
	if l == nil || l.interval <= 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	at := l.next[host]
	if at.Before(now) {
		at = now
	}
	l.next[host] = at.Add(l.interval)
	l.mu.Unlock()

	d := time.Until(at)
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

// Delay pushes back the next slot for host by at least d, used when a
// server answers 429 so that all workers back off, not just the caller.
func (l *hostLimiter) Delay(host string, d time.Duration) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := time.Now().Add(d); l.next[host].Before(until) {
		l.next[host] = until
	}
}
