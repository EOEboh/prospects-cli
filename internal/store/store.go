// Package store owns the SQLite connection, schema migrations and the typed
// repositories the commands read and write through.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // CGo-free driver; keeps `go build` single-binary
)

// TimeFormat is the on-disk timestamp representation: RFC3339 in UTC with
// nanosecond precision, so text comparison equals chronological order.
const TimeFormat = time.RFC3339Nano

// DB wraps *sql.DB with the helpers every repository shares.
type DB struct {
	*sql.DB
	path string
}

// Open connects to a SQLite database, applies the pragmas this tool depends
// on, and verifies they took effect.
//
// foreign_keys is off by default in SQLite, which would make every REFERENCES
// clause in the schema decorative, so it is verified rather than assumed.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := path + "?" + url.Values{
		"_pragma": {
			"journal_mode(WAL)",   // concurrent reads during a long enrich run
			"busy_timeout(5000)",  // wait rather than fail on a locked write
			"foreign_keys(ON)",    // cascade deletes actually cascade
			"synchronous(NORMAL)", // safe under WAL, much faster than FULL
		},
	}.Encode()

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// Single writer. SQLite serializes writes anyway, and the bounded worker
	// pool does its concurrency in HTTP, not in the database.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	db := &DB{DB: sqlDB, path: path}
	if err := db.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("connect %s: %w", path, err)
	}
	if err := db.verifyPragmas(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) verifyPragmas(ctx context.Context) error {
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf("journal_mode is %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys is off; the schema's REFERENCES clauses would not be enforced")
	}
	return nil
}

// Path is the database file, for log lines and error messages.
func (db *DB) Path() string { return db.path }

// WithTx runs fn in a transaction, committing on success and rolling back on
// error or panic. Nested use is not supported; pass the *sql.Tx down instead.
func (db *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Now returns the current time in the precision and location the schema uses.
// Every write timestamp goes through here so tests can reason about ordering.
func Now() time.Time { return time.Now().UTC() }

// FormatTime and ParseTime convert between Go times and the TEXT columns.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeFormat, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", s, err)
	}
	return t, nil
}
