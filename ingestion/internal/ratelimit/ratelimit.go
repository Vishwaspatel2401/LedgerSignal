// Package ratelimit implements a simple in-memory token bucket, and an HTTP
// middleware built on top of it. Deliberately its own package — same
// "one concern per package" pattern as crypto/storage/worker — so it stays
// reusable on any route, not tied to the webhook handler specifically.
//
// In-memory, not Redis-backed: correct for a single process. If this service
// ever runs as multiple instances behind a load balancer, each instance
// would have its own separate bucket, which defeats the point — that's the
// point where this would need to move to the Redis instance already in the
// stack (currently used only for caching), so every instance shares one
// count instead of each enforcing its own limit independently.
package ratelimit

import (
	"net/http"
	"sync"
	"time"
)

// Bucket is a token bucket: it starts full, each allowed request consumes
// one token, and tokens refill continuously over time up to maxTokens.
// Letting tokens refill continuously (rather than resetting to full at
// fixed clock boundaries) is what avoids the fixed-window problem of two
// bursts landing back-to-back right at a window edge.
type Bucket struct {
	mu sync.Mutex // guards every field below — net/http runs each request
	// on its own goroutine, so concurrent webhook deliveries could call
	// Allow() at the same instant without this.

	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens added per second
	lastRefill time.Time
}

// NewBucket creates a bucket starting completely full (maxTokens available
// immediately), so the very first burst of legitimate traffic isn't
// penalized just because the server only just started.
func NewBucket(maxTokens, refillRatePerSecond float64) *Bucket {
	return &Bucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRatePerSecond,
		lastRefill: time.Now(),
	}
}

// Allow reports whether one more request should be let through right now.
// If so, it also consumes one token as a side effect — this is why callers
// must not call it "just to check" without acting on the result.
func (b *Bucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.lastRefill = now

	// Refill based on however much real time passed since the last call,
	// not a fixed tick — this is what makes the bucket accurate even if
	// Allow() is called irregularly (bursts, then silence, then bursts again).
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

// Middleware wraps an http.Handler so a request is only passed through if
// the bucket allows it — otherwise it's rejected with 429 before the real
// handler (and everything it would have done, like Enqueue) ever runs.
//
// onRejected is called (if non-nil) for every rejected request, before the
// 429 is written — the hook for a caller that wants to record rejections
// somewhere (an audit log, a metric) without this package needing to know
// what Postgres or any other storage even is. Pass nil if you don't need one.
func Middleware(bucket *Bucket, onRejected func(*http.Request)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !bucket.Allow() {
				if onRejected != nil {
					onRejected(r)
				}
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
