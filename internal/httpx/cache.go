package httpx

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Cache stores fetched responses on disk so a rerun does not re-fetch what was
// pulled yesterday — politeness toward the sites we crawl as much as speed.
//
// It lives in its own SQLite file, separate from the prospect database, so
// deleting it to force a refetch can never touch collected data.
type Cache struct {
	db  *sql.DB
	ttl time.Duration
}

// Entry is a cached response.
type Entry struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
	FetchedAt   time.Time
}

// OpenCache opens or creates the cache database.
func OpenCache(ctx context.Context, path string, ttl time.Duration) (*Cache, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cache %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS http_cache (
			key          TEXT PRIMARY KEY,
			url          TEXT NOT NULL,
			status       INTEGER NOT NULL,
			content_type TEXT NOT NULL DEFAULT '',
			body         BLOB NOT NULL,
			fetched_at   TEXT NOT NULL,
			expires_at   TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS ix_http_cache_expiry ON http_cache(expires_at);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create cache schema: %w", err)
	}
	return &Cache{db: db, ttl: ttl}, nil
}

func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// CacheKey identifies a request. Callers pass any extra input that changes the
// response — an API field mask, for instance — so two different requests to
// one URL never share an entry.
func CacheKey(method, url string, extra ...string) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(url))
	for _, e := range extra {
		h.Write([]byte{0})
		h.Write([]byte(e))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns a cached entry, or nil when there is no live one. An expired
// entry is a miss, not an error.
func (c *Cache) Get(ctx context.Context, key string) (*Entry, error) {
	if c == nil {
		return nil, nil
	}

	var (
		e         Entry
		fetchedAt string
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT url, status, content_type, body, fetched_at FROM http_cache
		 WHERE key = ? AND expires_at > ?`,
		key, formatCacheTime(time.Now())).
		Scan(&e.URL, &e.Status, &e.ContentType, &e.Body, &fetchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}

	t, err := time.Parse(time.RFC3339Nano, fetchedAt)
	if err != nil {
		return nil, fmt.Errorf("parse cache timestamp: %w", err)
	}
	e.FetchedAt = t
	return &e, nil
}

// Put stores a response, replacing any existing entry for the key.
func (c *Cache) Put(ctx context.Context, key string, e *Entry) error {
	if c == nil {
		return nil
	}
	now := time.Now()
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO http_cache (key, url, status, content_type, body, fetched_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   url=excluded.url, status=excluded.status, content_type=excluded.content_type,
		   body=excluded.body, fetched_at=excluded.fetched_at, expires_at=excluded.expires_at`,
		key, e.URL, e.Status, e.ContentType, e.Body,
		formatCacheTime(now), formatCacheTime(now.Add(c.ttl)))
	if err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}

// Purge deletes expired entries and reports how many went.
func (c *Cache) Purge(ctx context.Context) (int64, error) {
	if c == nil {
		return 0, nil
	}
	res, err := c.db.ExecContext(ctx,
		`DELETE FROM http_cache WHERE expires_at <= ?`, formatCacheTime(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("purge cache: %w", err)
	}
	return res.RowsAffected()
}

func formatCacheTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
