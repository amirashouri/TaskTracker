package ratelimit

import (
	"testing"
	"time"
)

// fakeClock advances only when the test says so, so a window can be crossed
// without sleeping.
type fakeClock struct{ at time.Time }

func (c *fakeClock) Now() time.Time { return c.at }
func (c *fakeClock) advance(d time.Duration) {
	c.at = c.at.Add(d)
}

func newTestLimiter(limit int, period time.Duration) (*Limiter, *fakeClock) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	limiter := New(limit, period)
	limiter.now = clock.Now
	return limiter, clock
}

func TestAllowPermitsUpToTheLimitThenDenies(t *testing.T) {
	limiter, _ := newTestLimiter(3, time.Minute)

	for attempt := 1; attempt <= 3; attempt++ {
		if allowed, _ := limiter.Allow("ip:1.2.3.4"); !allowed {
			t.Fatalf("attempt %d should be allowed", attempt)
		}
	}

	allowed, retryAfter := limiter.Allow("ip:1.2.3.4")
	if allowed {
		t.Fatal("the attempt past the limit was allowed")
	}
	if retryAfter != time.Minute {
		t.Errorf("retryAfter = %v, want %v", retryAfter, time.Minute)
	}
}

func TestAllowCountsEachKeySeparately(t *testing.T) {
	limiter, _ := newTestLimiter(1, time.Minute)

	if allowed, _ := limiter.Allow("ip:a"); !allowed {
		t.Fatal("first key's first attempt was denied")
	}
	if allowed, _ := limiter.Allow("ip:b"); !allowed {
		t.Error("a second key shares the first key's counter")
	}
	if allowed, _ := limiter.Allow("ip:a"); allowed {
		t.Error("the first key was not limited")
	}
}

func TestAllowResetsAfterTheWindow(t *testing.T) {
	limiter, clock := newTestLimiter(1, time.Minute)

	if allowed, _ := limiter.Allow("ip:a"); !allowed {
		t.Fatal("first attempt was denied")
	}
	if allowed, _ := limiter.Allow("ip:a"); allowed {
		t.Fatal("second attempt inside the window was allowed")
	}

	clock.advance(time.Minute)

	if allowed, _ := limiter.Allow("ip:a"); !allowed {
		t.Error("the window did not reset")
	}
}

// A successful sign-in must not leave the account throttled by its own typos.
func TestResetClearsAKey(t *testing.T) {
	limiter, _ := newTestLimiter(1, time.Minute)

	if allowed, _ := limiter.Allow("email:a@example.com"); !allowed {
		t.Fatal("first attempt was denied")
	}
	limiter.Reset("email:a@example.com")
	if allowed, _ := limiter.Allow("email:a@example.com"); !allowed {
		t.Error("reset did not clear the counter")
	}
}

func TestDisabledAndNilLimitersAllowEverything(t *testing.T) {
	var nilLimiter *Limiter
	if allowed, _ := nilLimiter.Allow("anything"); !allowed {
		t.Error("a nil limiter denied an attempt")
	}

	disabled := New(0, time.Minute)
	for i := 0; i < 100; i++ {
		if allowed, _ := disabled.Allow("anything"); !allowed {
			t.Fatal("a zero limit denied an attempt")
		}
	}
}

// Distinct keys must not grow the map without bound.
func TestExpiredWindowsAreEvicted(t *testing.T) {
	limiter, clock := newTestLimiter(1, time.Minute)

	for i := 0; i < evictThreshold; i++ {
		limiter.Allow(string(rune(i)))
	}
	clock.advance(2 * time.Minute)
	// The next new key crosses the threshold and sweeps the expired windows.
	limiter.Allow("fresh")

	if size := len(limiter.counts); size > 2 {
		t.Errorf("map holds %d windows after eviction, want the live ones only", size)
	}
}
