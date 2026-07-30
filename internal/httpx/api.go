package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// StatusError is a non-success HTTP status from an API, carrying the provider's
// response body.
//
// It exists so a caller can render an actionable message after retries are
// exhausted. A 429 that survives every retry is usually an exhausted quota
// rather than a momentary rate limit, and those have different fixes.
type StatusError struct {
	URL    string
	Status int
	Body   []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.URL, e.Status)
}

// APIRequest describes a call to an official API, as opposed to fetching
// somebody's web page.
//
// robots.txt is deliberately not consulted here. It governs crawlers reading a
// site's public pages; an official API accessed with a key issued for that
// purpose is not crawling, and applying robots.txt to googleapis.com would
// block a call Google published the endpoint to receive. Everything else — the
// rate limit, the cache, the timeout, the backoff — still applies.
type APIRequest struct {
	Method string
	URL    string
	Body   []byte

	// Headers carry credentials and the field mask. They are not part of the
	// cache key; CacheKeyExtra is, so a changed field mask is a different
	// entry while a rotated key is not.
	Headers map[string]string

	// CacheKeyExtra is any input beyond method and URL that changes the
	// response — the request body and the field mask, for Places.
	CacheKeyExtra []string

	// BeforeNetwork runs only when the request is actually about to go out,
	// after a cache miss. This is where a quota ceiling belongs: a cache hit
	// costs nothing and must not be counted or refused.
	BeforeNetwork func() error

	// AfterNetwork runs once a live call has been made, whether or not it
	// succeeded, so the call can be recorded as spent.
	AfterNetwork func()
}

// API performs a request against an official API, serving it from cache when
// possible.
func (c *Client) API(ctx context.Context, req APIRequest) (*Response, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", req.URL, err)
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}

	key := CacheKey(req.Method, u.String(), req.CacheKeyExtra...)
	if entry, err := c.cache.Get(ctx, key); err != nil {
		c.log.Warn("cache read failed, calling live", "url", req.URL, "error", err)
	} else if entry != nil {
		return &Response{
			URL: entry.URL, Status: entry.Status, ContentType: entry.ContentType,
			Body: entry.Body, FromCache: true,
		}, nil
	}

	// Only now is money about to be spent.
	if req.BeforeNetwork != nil {
		if err := req.BeforeNetwork(); err != nil {
			return nil, err
		}
	}

	resp, err := c.apiFetch(ctx, u, req)
	if req.AfterNetwork != nil {
		req.AfterNetwork()
	}
	if err != nil {
		return nil, err
	}

	// Only cache a usable answer. Caching an error would turn one transient
	// failure into a week of them.
	if resp.Status == http.StatusOK {
		if err := c.cache.Put(ctx, key, &Entry{
			URL: resp.URL, Status: resp.Status, ContentType: resp.ContentType, Body: resp.Body,
		}); err != nil {
			c.log.Warn("cache write failed", "url", req.URL, "error", err)
		}
	}
	return resp, nil
}

func (c *Client) apiFetch(ctx context.Context, u *url.URL, req APIRequest) (*Response, error) {
	var lastErr error

	for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := c.limiter.Wait(ctx, u.Host); err != nil {
			return nil, err
		}

		resp, retryAfter, err := c.doAPI(ctx, u, req)
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
		if retryAfter > 0 {
			delay = retryAfter
		}
		c.log.Debug("retrying API call after backoff",
			"url", u.String(), "attempt", attempt, "delay", delay, "reason", re.Error())

		if err := sleepCtx(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("giving up on %s after %d attempts: %w", u, c.cfg.MaxRetries, lastErr)
}

func (c *Client) doAPI(ctx context.Context, u *url.URL, req APIRequest) (*Response, time.Duration, error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), body)
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("User-Agent", c.cfg.UserAgent)
	if len(req.Body) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, 0, &retryableError{err: err}
	}
	defer resp.Body.Close()

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))

	// A 4xx from an API is an answer about the request — a bad key, a bad
	// query — and retrying it spends quota to be told the same thing again.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		// The body is read even on a retryable failure so that the eventual
		// give-up carries the provider's explanation. A 429 that survives every
		// retry is usually an exhausted quota, and "returned 429" alone does
		// not tell the operator where to look.
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes))
		return nil, retryAfter, &retryableError{err: &StatusError{
			URL:    u.String(),
			Status: resp.StatusCode,
			Body:   payload,
		}}
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes))
	if err != nil {
		return nil, 0, &retryableError{err: fmt.Errorf("read body of %s: %w", u, err)}
	}

	return &Response{
		URL:         resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        payload,
	}, 0, nil
}
