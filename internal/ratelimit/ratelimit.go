// Package ratelimit is the relay's abuse floor: per-IP token
// buckets with lazy GC. Zero-value buckets are not usable; use New.
package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a per-key token bucket.
type Bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a keyed bucket set.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*Bucket
	rate    float64 // tokens per second
	burst   float64 // bucket capacity
	maxKeys int     // when exceeded, stale buckets are swept
}

// New builds a limiter: rate tokens/second, burst tokens max.
func New(rate, burst float64) *Limiter {
	return &Limiter{
		buckets: make(map[string]*Bucket),
		rate:    rate,
		burst:   burst,
		maxKeys: 8192,
	}
}

// Allow spends one token for key. Returns (allowed, remaining,
// resetIn) — remaining/resetIn feed X-RateLimit headers.
func (l *Limiter) Allow(key string) (ok bool, remaining int, resetIn time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buckets) >= l.maxKeys {
		l.sweepLocked()
	}

	b, exists := l.buckets[key]
	now := time.Now()
	if !exists {
		b = &Bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens += now.Sub(b.last).Seconds() * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.last = now

	if b.tokens < 1 {
		// empty: when does the next token arrive?
		deficit := 1 - b.tokens
		resetIn = time.Duration(deficit / l.rate * float64(time.Second))
		return false, 0, resetIn
	}
	b.tokens--
	return true, int(b.tokens), time.Duration((l.burst - b.tokens) / l.rate * float64(time.Second))
}

// Burst returns the bucket capacity (for X-RateLimit-Limit).
func (l *Limiter) Burst() float64 { return l.burst }

// sweepLocked drops buckets idle for > 2*burst/rate.
func (l *Limiter) sweepLocked() {
	idle := time.Duration(2 * l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if time.Since(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}
