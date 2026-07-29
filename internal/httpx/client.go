// Package httpx is the only way this tool talks to the network.
//
// Everything the crawling guardrails require lives here rather than at the
// call sites: a truthful User-Agent, robots.txt enforcement, per-host rate
// limiting, a response cache, a timeout and a size cap on every request, and
// backoff with jitter on 429 and 5xx. A caller cannot forget them because
// there is no other client to reach for.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults chosen to be unremarkable in someone else's access log.
const (
	DefaultMaxBodyBytes = 4 << 20 // 4 MiB; a homepage that exceeds this is not a lead
	DefaultMaxRetries   = 3
	DefaultBaseBackoff  = time.Second
	DefaultMaxBackoff   = 30 * time.Second
	maxRedirects        = 5
)

// ErrDisallowed reports that robots.txt forbade a fetch. It is a normal
// outcome, not a failure: the caller records the reason and moves on.
type ErrDisallowed struct {
	URL  string
	Rule string
}

func (e *ErrDisallowed) Error() string {
	if e.Rule == "" {
		return fmt.Sprintf("robots.txt disallows %s", e.URL)
	}
	return fmt.Sprintf("robots.txt disallows %s (%s)", e.URL, e.Rule)
}

// Response is a fetched page.
type Response struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
	FromCache   bool
}

// IsHTML reports whether the body is worth parsing as a page.
func (r *Response) IsHTML() bool {
	return strings.Contains(strings.ToLower(r.ContentType), "html")
}

// Config configures a Client.
type Config struct {
	UserAgent    string
	Timeout      time.Duration
	RatePerHost  time.Duration
	MaxBodyBytes int64
	MaxRetries   int
	Cache        *Cache
	Logger       *slog.Logger
}

// Client is a polite HTTP client.
type Client struct {
	http    *http.Client
	cfg     Config
	limiter *RateLimiter
	cache   *Cache
	log     *slog.Logger

	robotsMu sync.Mutex
	robots   map[string]*Robots // by scheme://host; nil value means "allow all"
}

// New builds a Client. The User-Agent is required: this tool identifies itself
// truthfully on every request or it does not make one.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.UserAgent) == "" {
		return nil, errors.New("httpx: a User-Agent is required")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("httpx: a positive timeout is required; no request may be unbounded")
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = DefaultMaxRetries
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          50,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		cfg:     cfg,
		limiter: NewRateLimiter(cfg.RatePerHost),
		cache:   cfg.Cache,
		log:     cfg.Logger,
		robots:  make(map[string]*Robots),
	}, nil
}

// Get fetches a URL, honoring robots.txt, the cache and the rate limit.
//
// A disallowed URL returns *ErrDisallowed and is never requested.
func (c *Client) Get(ctx context.Context, rawURL string) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q in %s", u.Scheme, rawURL)
	}

	allowed, rule, err := c.checkRobots(ctx, u)
	if err != nil {
		// An unreachable robots.txt is not permission to ignore it, but it is
		// also not evidence of a prohibition. Log and proceed, as crawlers do.
		c.log.Debug("robots.txt unavailable, proceeding", "url", rawURL, "error", err)
	}
	if !allowed {
		return nil, &ErrDisallowed{URL: rawURL, Rule: rule}
	}

	key := CacheKey(http.MethodGet, u.String())
	if entry, err := c.cache.Get(ctx, key); err != nil {
		c.log.Warn("cache read failed, fetching live", "url", rawURL, "error", err)
	} else if entry != nil {
		return &Response{
			URL: entry.URL, Status: entry.Status, ContentType: entry.ContentType,
			Body: entry.Body, FromCache: true,
		}, nil
	}

	resp, err := c.fetch(ctx, u)
	if err != nil {
		return nil, err
	}

	if err := c.cache.Put(ctx, key, &Entry{
		URL: resp.URL, Status: resp.Status, ContentType: resp.ContentType, Body: resp.Body,
	}); err != nil {
		c.log.Warn("cache write failed", "url", rawURL, "error", err)
	}
	return resp, nil
}

// fetch performs the request with rate limiting and retries.
func (c *Client) fetch(ctx context.Context, u *url.URL) (*Response, error) {
	var lastErr error

	for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := c.limiter.Wait(ctx, u.Host); err != nil {
			return nil, err
		}

		resp, retryAfter, err := c.do(ctx, u)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		var re *retryableError
		if !errors.As(err, &re) {
			return nil, err
		}
		if attempt == c.cfg.MaxRetries {
			break
		}

		delay := Backoff(attempt, DefaultBaseBackoff, DefaultMaxBackoff)
		// A server that says how long to wait is telling us something the
		// backoff formula cannot know.
		if retryAfter > 0 {
			delay = retryAfter
		}
		c.log.Debug("retrying after backoff",
			"url", u.String(), "attempt", attempt, "delay", delay, "reason", re.Error())

		if err := sleepCtx(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("giving up on %s after %d attempts: %w", u, c.cfg.MaxRetries, lastErr)
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func (c *Client) do(ctx context.Context, u *url.URL) (*Response, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en")

	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failures are usually transient: DNS blips, resets, timeouts.
		return nil, 0, &retryableError{err: err}
	}
	defer resp.Body.Close()

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, retryAfter, &retryableError{
			err: fmt.Errorf("%s returned %s", u, resp.Status),
		}
	}

	// A cap on every response: an unbounded read is how one bad URL exhausts
	// memory on the VPS.
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes))
	if err != nil {
		return nil, 0, &retryableError{err: fmt.Errorf("read body of %s: %w", u, err)}
	}

	return &Response{
		URL:         resp.Request.URL.String(), // post-redirect
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
	}, 0, nil
}

// checkRobots fetches and caches a host's robots.txt, then applies it.
func (c *Client) checkRobots(ctx context.Context, u *url.URL) (allowed bool, rule string, err error) {
	origin := u.Scheme + "://" + u.Host

	c.robotsMu.Lock()
	robots, known := c.robots[origin]
	c.robotsMu.Unlock()

	if !known {
		robots, err = c.fetchRobots(ctx, origin)
		c.robotsMu.Lock()
		c.robots[origin] = robots // cache the nil too: do not refetch a 404
		c.robotsMu.Unlock()
	}

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return robots.Allows(c.cfg.UserAgent, path), robots.MatchedRule(c.cfg.UserAgent, path), err
}

func (c *Client) fetchRobots(ctx context.Context, origin string) (*Robots, error) {
	robotsURL := origin + "/robots.txt"
	key := CacheKey(http.MethodGet, robotsURL)

	if entry, err := c.cache.Get(ctx, key); err == nil && entry != nil {
		if entry.Status != http.StatusOK {
			return nil, nil
		}
		return ParseRobots(strings.NewReader(string(entry.Body))), nil
	}

	u, err := url.Parse(robotsURL)
	if err != nil {
		return nil, err
	}

	// robots.txt is itself rate limited: it is a request to their server.
	if err := c.limiter.Wait(ctx, u.Host); err != nil {
		return nil, err
	}
	resp, _, err := c.do(ctx, u)
	if err != nil {
		return nil, err
	}

	if err := c.cache.Put(ctx, key, &Entry{
		URL: resp.URL, Status: resp.Status, ContentType: resp.ContentType, Body: resp.Body,
	}); err != nil {
		c.log.Debug("could not cache robots.txt", "url", robotsURL, "error", err)
	}

	// 4xx means no robots.txt, which means no restrictions.
	if resp.Status != http.StatusOK {
		return nil, nil
	}

	robots := ParseRobots(strings.NewReader(string(resp.Body)))
	if delay := robots.CrawlDelay(c.cfg.UserAgent); delay > 0 {
		c.limiter.SetHostDelay(u.Host, delay)
		c.log.Debug("honoring crawl-delay", "host", u.Host, "delay", delay)
	}
	return robots, nil
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
