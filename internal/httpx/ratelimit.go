package httpx

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// RateLimiter spaces requests per host. The bounded worker pool runs several
// businesses at once, and without per-host spacing two workers landing on the
// same host would double its request rate.
type RateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     map[string]time.Time
	// perHost holds site-specific delays from robots.txt Crawl-delay, which
	// override the default when they are longer.
	perHost map[string]time.Duration
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

// NewRateLimiter spaces requests to one host by at least interval.
func NewRateLimiter(interval time.Duration) *RateLimiter {
	return &RateLimiter{
		interval: interval,
		next:     make(map[string]time.Time),
		perHost:  make(map[string]time.Duration),
		now:      time.Now,
		sleep:    sleepCtx,
	}
}

// SetHostDelay records a site's requested crawl delay. A site asking to be
// crawled more slowly than our default is always honored; one asking to be
// crawled faster is not.
func (rl *RateLimiter) SetHostDelay(host string, delay time.Duration) {
	if delay <= 0 {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.perHost[host] = delay
}

// Wait blocks until the caller may issue a request to host, or the context
// ends. It reserves the slot before sleeping, so concurrent workers queue
// behind each other rather than all waking at the same instant.
func (rl *RateLimiter) Wait(ctx context.Context, host string) error {
	rl.mu.Lock()
	interval := rl.interval
	if d, ok := rl.perHost[host]; ok && d > interval {
		interval = d
	}

	now := rl.now()
	earliest := rl.next[host]
	if earliest.Before(now) {
		earliest = now
	}
	rl.next[host] = earliest.Add(interval)
	rl.mu.Unlock()

	if delay := earliest.Sub(now); delay > 0 {
		return rl.sleep(ctx, delay)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Backoff computes the delay before retry number attempt (1-based), growing
// exponentially with jitter.
//
// The jitter matters: without it, a batch of requests that all hit a rate limit
// together would retry together, reproducing the burst that caused it.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt && delay < max; i++ {
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	// Full jitter over [delay/2, delay].
	half := delay / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}
