package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testClient(t *testing.T, cache *Cache) *Client {
	t.Helper()
	c, err := New(Config{
		UserAgent:   ourAgent,
		Timeout:     5 * time.Second,
		RatePerHost: 0, // tests must not sleep
		Cache:       cache,
		Logger:      discard(),
		MaxRetries:  3,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func testCache(t *testing.T, ttl time.Duration) *Cache {
	t.Helper()
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "cache.db"), ttl)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// The User-Agent is not optional: it is how a site operator reaches a human.
func TestNewRequiresUserAgentAndTimeout(t *testing.T) {
	if _, err := New(Config{Timeout: time.Second}); err == nil {
		t.Error("a client without a User-Agent must be refused")
	}
	if _, err := New(Config{UserAgent: ourAgent}); err == nil {
		t.Error("a client without a timeout must be refused: no request may be unbounded")
	}
}

func TestGetSendsTruthfulUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		got = r.Header.Get("User-Agent")
		fmt.Fprint(w, "<html></html>")
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	if _, err := c.Get(context.Background(), srv.URL+"/"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != ourAgent {
		t.Errorf("User-Agent = %q, want %q", got, ourAgent)
	}
	if !strings.Contains(got, "mailto:") {
		t.Error("the User-Agent must carry a contact address")
	}
}

// A disallowed URL must never be requested at all.
func TestGetHonorsRobotsDisallow(t *testing.T) {
	var fetched atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /private")
			return
		}
		fetched.Store(true)
		fmt.Fprint(w, "<html>secret</html>")
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	_, err := c.Get(context.Background(), srv.URL+"/private/page")

	var disallowed *ErrDisallowed
	if !errors.As(err, &disallowed) {
		t.Fatalf("error = %v, want *ErrDisallowed", err)
	}
	if disallowed.Rule != "Disallow: /private" {
		t.Errorf("Rule = %q, want the matched rule recorded", disallowed.Rule)
	}
	if fetched.Load() {
		t.Error("a disallowed URL was requested anyway")
	}
}

func TestGetAllowsWhenRobotsPermits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /admin")
			return
		}
		fmt.Fprint(w, "<html>hello</html>")
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	resp, err := c.Get(context.Background(), srv.URL+"/contact")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(string(resp.Body), "hello") {
		t.Errorf("body = %q", resp.Body)
	}
}

// A missing robots.txt means no restrictions, not a blocked crawl.
func TestMissingRobotsAllowsFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "<html>ok</html>")
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	if _, err := c.Get(context.Background(), srv.URL+"/"); err != nil {
		t.Fatalf("a missing robots.txt should not block a fetch: %v", err)
	}
}

// robots.txt is fetched once per host, not once per page.
func TestRobotsFetchedOncePerHost(t *testing.T) {
	var robotsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			robotsHits.Add(1)
			fmt.Fprint(w, "User-agent: *\nAllow: /")
			return
		}
		fmt.Fprint(w, "<html>ok</html>")
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	for _, path := range []string{"/", "/contact", "/about"} {
		if _, err := c.Get(context.Background(), srv.URL+path); err != nil {
			t.Fatalf("Get(%s): %v", path, err)
		}
	}
	if n := robotsHits.Load(); n != 1 {
		t.Errorf("robots.txt fetched %d times, want 1", n)
	}
}

// A rerun must not re-fetch what was pulled yesterday.
func TestCacheServesRepeatRequests(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, "<html>page</html>")
	}))
	defer srv.Close()

	cache := testCache(t, time.Hour)
	c := testClient(t, cache)
	ctx := context.Background()

	first, err := c.Get(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if first.FromCache {
		t.Error("the first fetch cannot come from cache")
	}

	second, err := c.Get(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !second.FromCache {
		t.Error("the second fetch should have been served from cache")
	}
	if string(second.Body) != string(first.Body) {
		t.Error("cached body differs from the live one")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("origin hit %d times, want 1", n)
	}
}

func TestCacheExpiry(t *testing.T) {
	ctx := context.Background()
	cache := testCache(t, -time.Second) // everything written is already stale

	if err := cache.Put(ctx, "k", &Entry{URL: "u", Status: 200, Body: []byte("x")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := cache.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("an expired entry must read as a miss")
	}

	purged, err := cache.Purge(ctx)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged %d entries, want 1", purged)
	}
}

func TestCacheKeyDistinguishesRequests(t *testing.T) {
	a := CacheKey("GET", "https://example.com/")
	b := CacheKey("GET", "https://example.com/other")
	if a == b {
		t.Error("different URLs share a cache key")
	}
	// Extra inputs exist so an API field mask changes the key.
	c := CacheKey("GET", "https://example.com/", "mask:id")
	d := CacheKey("GET", "https://example.com/", "mask:id,website")
	if c == d {
		t.Error("different field masks share a cache key")
	}
	if a != CacheKey("GET", "https://example.com/") {
		t.Error("cache keys are not stable")
	}
}

// 5xx and 429 are retried; the eventual success is what the caller sees.
func TestRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "<html>finally</html>")
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	// Keep the test fast: the backoff formula is exercised separately.
	resp, err := c.Get(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(string(resp.Body), "finally") {
		t.Errorf("body = %q", resp.Body)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("%d attempts, want 3", n)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	c.cfg.MaxRetries = 2
	if _, err := c.Get(context.Background(), srv.URL+"/"); err == nil {
		t.Error("a permanently failing server should eventually fail the fetch")
	}
}

// A 404 is an answer, not a transient failure, so it must not be retried.
func TestClientErrorsAreNotRetried(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	resp, err := c.Get(context.Background(), srv.URL+"/missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.Status)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("%d attempts for a 404, want 1", n)
	}
}

// One oversized page must not be able to exhaust memory on the VPS.
func TestBodySizeIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		for i := 0; i < 1000; i++ {
			fmt.Fprint(w, strings.Repeat("x", 1024))
		}
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	c.cfg.MaxBodyBytes = 4096

	resp, err := c.Get(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(resp.Body) > 4096 {
		t.Errorf("read %d bytes, want at most 4096", len(resp.Body))
	}
}

func TestRejectsNonHTTPSchemes(t *testing.T) {
	c := testClient(t, testCache(t, time.Hour))
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "mailto:a@b.com"} {
		if _, err := c.Get(context.Background(), u); err == nil {
			t.Errorf("Get(%q) should be refused", u)
		}
	}
}

func TestContextCancellationStopsFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	c := testClient(t, testCache(t, time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := c.Get(ctx, srv.URL+"/"); err == nil {
		t.Error("a cancelled context should stop the fetch")
	}
}

func TestRateLimiterSpacesRequestsPerHost(t *testing.T) {
	rl := NewRateLimiter(100 * time.Millisecond)

	var (
		mu      sync.Mutex
		slept   []time.Duration
		now     = time.Unix(0, 0)
		nowLock sync.Mutex
	)
	rl.now = func() time.Time {
		nowLock.Lock()
		defer nowLock.Unlock()
		return now
	}
	rl.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		nowLock.Lock()
		now = now.Add(d)
		nowLock.Unlock()
		return nil
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := rl.Wait(ctx, "example.com"); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
	// The first request goes immediately; the next two each wait an interval.
	if len(slept) != 2 {
		t.Fatalf("slept %d times, want 2 (%v)", len(slept), slept)
	}
	for _, d := range slept {
		if d != 100*time.Millisecond {
			t.Errorf("slept %v, want 100ms", d)
		}
	}

	// A different host is not made to wait behind the first.
	if err := rl.Wait(ctx, "other.com"); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(slept) != 2 {
		t.Errorf("a second host waited behind the first: %v", slept)
	}
}

// A site asking to be crawled more slowly is obeyed; one asking to be crawled
// faster than our default is not.
func TestCrawlDelayOverridesOnlyUpward(t *testing.T) {
	rl := NewRateLimiter(time.Second)
	rl.SetHostDelay("slow.com", 5*time.Second)
	rl.SetHostDelay("fast.com", 10*time.Millisecond)

	var slept []time.Duration
	now := time.Unix(0, 0)
	rl.now = func() time.Time { return now }
	rl.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	ctx := context.Background()
	_ = rl.Wait(ctx, "slow.com")
	_ = rl.Wait(ctx, "slow.com")
	if len(slept) != 1 || slept[0] != 5*time.Second {
		t.Errorf("slow host delays = %v, want one 5s wait", slept)
	}

	slept = nil
	_ = rl.Wait(ctx, "fast.com")
	_ = rl.Wait(ctx, "fast.com")
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("fast host delays = %v, want one 1s wait (our default floor)", slept)
	}
}

// Without jitter a batch that hit a rate limit together would retry together,
// reproducing the burst that caused it.
func TestBackoffGrowsAndJitters(t *testing.T) {
	base, max := time.Second, 30*time.Second

	for attempt := 1; attempt <= 6; attempt++ {
		var minSeen, maxSeen time.Duration
		for i := 0; i < 200; i++ {
			d := Backoff(attempt, base, max)
			if d <= 0 {
				t.Fatalf("attempt %d produced a non-positive delay %v", attempt, d)
			}
			if d > max {
				t.Fatalf("attempt %d produced %v, above the %v ceiling", attempt, d, max)
			}
			if minSeen == 0 || d < minSeen {
				minSeen = d
			}
			if d > maxSeen {
				maxSeen = d
			}
		}
		if minSeen == maxSeen {
			t.Errorf("attempt %d produced no jitter (always %v)", attempt, minSeen)
		}
	}

	// Growth: later attempts wait longer on average.
	early := Backoff(1, base, max)
	late := Backoff(5, base, max)
	if late <= early {
		t.Errorf("backoff did not grow: attempt 1 = %v, attempt 5 = %v", early, late)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{" 30 ", 30 * time.Second},
		{"not a number", 0},
		{"-1", 0},
	}
	for _, tc := range tests {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
