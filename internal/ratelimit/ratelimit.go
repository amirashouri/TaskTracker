// Package ratelimit is a small, in-process limiter used to slow down sign-in
// attempts. It is deliberately not shared across instances: this deployment is
// one binary, and a limiter that needed a shared store would be a second
// dependency for no gain. An operator running several replicas multiplies the
// allowance — a documented tradeoff, not a bug.
package ratelimit

import (
	"sync"
	"time"
)

// evictThreshold is the map size at which expired windows are swept, so a
// stream of distinct keys cannot grow the map without bound.
const evictThreshold = 1024

// Limiter counts attempts per key inside a fixed window. A zero or negative
// limit disables limiting, and a nil *Limiter allows everything, so callers do
// not need a branch of their own.
type Limiter struct {
	mu     sync.Mutex
	counts map[string]*window
	limit  int
	period time.Duration
	now    func() time.Time
}

type window struct {
	count int
	reset time.Time
}

// New returns a limiter allowing limit attempts per key per period.
func New(limit int, period time.Duration) *Limiter {
	return &Limiter{
		counts: make(map[string]*window),
		limit:  limit,
		period: period,
		now:    time.Now,
	}
}

// Allow records an attempt for key and reports whether it is within the limit.
// When it is not, retryAfter is how long until the window resets.
func (l *Limiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	if l == nil || l.limit <= 0 || l.period <= 0 {
		return true, 0
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	current, ok := l.counts[key]
	if !ok || !now.Before(current.reset) {
		if len(l.counts) >= evictThreshold {
			l.evict(now)
		}
		l.counts[key] = &window{count: 1, reset: now.Add(l.period)}
		return true, 0
	}
	if current.count >= l.limit {
		return false, current.reset.Sub(now)
	}
	current.count++
	return true, 0
}

// Reset forgets a key's attempts, so a successful sign-in does not count
// against that account's next attempt.
func (l *Limiter) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.counts, key)
}

// evict drops expired windows. It runs only when the map has grown past the
// threshold, so the common path stays a single lookup.
func (l *Limiter) evict(now time.Time) {
	for key, w := range l.counts {
		if !now.Before(w.reset) {
			delete(l.counts, key)
		}
	}
}
