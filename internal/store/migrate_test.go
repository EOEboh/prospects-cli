package store

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// Open must leave the connection in the state the schema assumes: WAL for
// concurrent reads, and foreign_keys ON or every REFERENCES clause is inert.
func TestOpenAppliesPragmas(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Error("foreign_keys is off; REFERENCES clauses would not be enforced")
	}
}

// Migrate runs unconditionally at startup, so applying twice must be a no-op.
func TestMigrateIsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	log := discardLogger()

	if err := db.Migrate(ctx, log); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	firstVersion, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if firstVersion < 1 {
		t.Fatalf("schema version = %d after migrating, want >= 1", firstVersion)
	}

	if err := db.Migrate(ctx, log); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var rows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&rows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if want := len(mustLoadMigrations(t)); rows != want {
		t.Errorf("schema_migrations has %d rows after two runs, want %d", rows, want)
	}
}

func TestSchemaVersionOnFreshDatabase(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// SchemaVersion is also called before Migrate in some paths; a table with
	// no rows must read as 0, not error.
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations"); err != nil {
		t.Fatalf("clear schema_migrations: %v", err)
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 0 {
		t.Errorf("SchemaVersion on an empty table = %d, want 0", v)
	}
}

// Every table, view and index the rest of the tool relies on must exist after
// migrating. This is the guard against a migration that parses but omits
// something.
func TestMigrateCreatesSchema(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	tests := []struct {
		kind string
		name string
	}{
		{"table", "businesses"},
		{"table", "signals"},
		{"table", "scores"},
		{"table", "outreach"},
		{"table", "outreach_events"},
		{"table", "suppression"},
		{"table", "suppression_domains"},
		{"table", "runs"},
		{"table", "run_items"},
		{"table", "api_calls"},
		{"view", "v_active_businesses"},
		{"view", "v_current_signals"},
		{"view", "v_latest_scores"},
		{"index", "ux_businesses_domain"},
		{"index", "ux_businesses_name_key"},
		{"index", "ux_signals_current"},
	}
	for _, tc := range tests {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			var n int
			err := db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`,
				tc.kind, tc.name).Scan(&n)
			if err != nil {
				t.Fatalf("query sqlite_master: %v", err)
			}
			if n != 1 {
				t.Errorf("%s %q not found", tc.kind, tc.name)
			}
		})
	}
}

// Dedup is enforced by the database rather than by application discipline.
func TestBusinessDedupIndexes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	insert := func(name, domain, nameKey string) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO businesses (name, domain, name_key, source, created_at, updated_at)
			 VALUES (?, ?, ?, 'csv', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			name, domain, nameKey)
		return err
	}

	if err := insert("Acme", "acme.com", "acme|austin"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("Acme Duplicate", "acme.com", "other|austin"); err == nil {
		t.Error("a second business with the same domain should be rejected")
	}

	// Two businesses with no website are distinguished by name+city only.
	if err := insert("Bright Path", "", "bright path|austin"); err != nil {
		t.Fatalf("insert without domain: %v", err)
	}
	if err := insert("Bright Path", "", "bright path|austin"); err == nil {
		t.Error("a second domainless business with the same name key should be rejected")
	}
	if err := insert("Bright Path", "", "bright path|dallas"); err != nil {
		t.Errorf("same name in a different city should be allowed: %v", err)
	}
}

// automation_tag is multi-valued: HubSpot and Calendly are two current facts,
// not one overwriting the other. Single-valued types stay single by being
// superseded on change, which the partial index permits.
func TestCurrentSignalUniqueness(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO businesses (id, name, domain, source, created_at, updated_at)
		 VALUES (1, 'Acme', 'acme.com', 'csv', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed business: %v", err)
	}

	insert := func(sigType, value string, superseded any) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO signals (business_id, source, type, value, observed_at, last_seen_at, superseded_at)
			 VALUES (1, 'website', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', ?)`,
			sigType, value, superseded)
		return err
	}

	if err := insert("automation_tag", "hubspot", nil); err != nil {
		t.Fatalf("insert hubspot: %v", err)
	}
	if err := insert("automation_tag", "calendly", nil); err != nil {
		t.Errorf("a business can run two automation tools at once: %v", err)
	}
	if err := insert("automation_tag", "hubspot", nil); err == nil {
		t.Error("the same current tag twice should be rejected")
	}

	// A changed single-valued signal: the old row is superseded, so the new
	// one does not collide.
	if err := insert("running_ads", "false", "2026-02-01T00:00:00Z"); err != nil {
		t.Fatalf("insert superseded signal: %v", err)
	}
	if err := insert("running_ads", "true", nil); err != nil {
		t.Errorf("a new value after superseding the old should be allowed: %v", err)
	}

	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM v_current_signals WHERE type = 'running_ads'`).Scan(&current); err != nil {
		t.Fatalf("count current signals: %v", err)
	}
	if current != 1 {
		t.Errorf("v_current_signals has %d running_ads rows, want 1", current)
	}
}

// Suppression is structural: list, export and brief read this view, so the
// exclusion cannot be forgotten by a command added later.
func TestActiveBusinessesViewExcludesSuppressed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const ts = "2026-01-01T00:00:00Z"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO businesses (id, name, domain, source, created_at, updated_at) VALUES
		 (1, 'Kept',        'kept.com',    'csv', ?, ?),
		 (2, 'ById',        'byid.com',    'csv', ?, ?),
		 (3, 'ByDomain',    'bydomain.com','csv', ?, ?),
		 (4, 'Rediscovered','bydomain.com','csv', ?, ?)`,
		ts, ts, ts, ts, ts, ts, ts, ts,
	); err == nil {
		t.Fatal("the unique domain index should reject the rediscovered duplicate")
	}

	// Insert them separately: the rediscovered row is what a fresh discover
	// run would produce after the original was deleted.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO businesses (id, name, domain, source, created_at, updated_at) VALUES
		 (1, 'Kept',     'kept.com',     'csv', ?, ?),
		 (2, 'ById',     'byid.com',     'csv', ?, ?),
		 (3, 'ByDomain', 'bydomain.com', 'csv', ?, ?)`,
		ts, ts, ts, ts, ts, ts,
	); err != nil {
		t.Fatalf("seed businesses: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO suppression (business_id, reason, created_at) VALUES (2, 'asked to be removed', ?)`, ts,
	); err != nil {
		t.Fatalf("suppress by id: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO suppression_domains (domain, reason, created_at) VALUES ('bydomain.com', 'asked to be removed', ?)`, ts,
	); err != nil {
		t.Fatalf("suppress by domain: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT id FROM v_active_businesses ORDER BY id`)
	if err != nil {
		t.Fatalf("query view: %v", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("v_active_businesses returned %v, want [1]", ids)
	}
}

// v_latest_scores must return exactly one row per business even when two
// scoring runs land in the same second.
func TestLatestScoresView(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const ts = "2026-01-01T00:00:00Z"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO businesses (id, name, domain, source, created_at, updated_at)
		 VALUES (1, 'Acme', 'acme.com', 'csv', ?, ?)`, ts, ts); err != nil {
		t.Fatalf("seed business: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runs (id, command, status, started_at) VALUES (1, 'score', 'completed', ?)`, ts); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	for _, score := range []int{40, 85} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO scores (business_id, run_id, score, raw_score, max_possible, confidence,
			                     breakdown, explanation, weights_hash, created_at)
			 VALUES (1, 1, ?, ?, 110, 0.5, '[]', 'because', 'abc', ?)`,
			score, score, ts); err != nil {
			t.Fatalf("insert score %d: %v", score, err)
		}
	}

	var count, latest int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(score) FROM v_latest_scores WHERE business_id = 1`).Scan(&count, &latest); err != nil {
		t.Fatalf("query view: %v", err)
	}
	if count != 1 {
		t.Errorf("v_latest_scores returned %d rows for one business, want 1", count)
	}
	if latest != 85 {
		t.Errorf("latest score = %d, want 85 (the most recently inserted)", latest)
	}
}

// The score column is a normalized 0..100 value; anything else is a bug in
// the scoring engine and should not reach disk.
func TestScoreRangeConstraint(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const ts = "2026-01-01T00:00:00Z"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO businesses (id, name, domain, source, created_at, updated_at)
		 VALUES (1, 'Acme', 'acme.com', 'csv', ?, ?)`, ts, ts); err != nil {
		t.Fatalf("seed business: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runs (id, command, status, started_at) VALUES (1, 'score', 'completed', ?)`, ts); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	for _, score := range []int{-1, 101} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO scores (business_id, run_id, score, raw_score, max_possible, confidence,
			                     breakdown, explanation, weights_hash, created_at)
			 VALUES (1, 1, ?, 0, 110, 0.5, '[]', '', 'abc', ?)`, score, ts)
		if err == nil {
			t.Errorf("score %d should violate the 0..100 constraint", score)
		}
	}
}

// Cascades only work because Open turns foreign_keys on.
func TestDeleteBusinessCascades(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const ts = "2026-01-01T00:00:00Z"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO businesses (id, name, domain, source, created_at, updated_at)
		 VALUES (1, 'Acme', 'acme.com', 'csv', ?, ?)`, ts, ts); err != nil {
		t.Fatalf("seed business: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO signals (business_id, source, type, value, observed_at, last_seen_at)
		 VALUES (1, 'website', 'contact_form', 'true', ?, ?)`, ts, ts); err != nil {
		t.Fatalf("seed signal: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM businesses WHERE id = 1`); err != nil {
		t.Fatalf("delete business: %v", err)
	}

	var orphans int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM signals`).Scan(&orphans); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d orphaned signals survived the cascade", orphans)
	}
}

func TestLoadMigrationsAreWellFormed(t *testing.T) {
	migrations := mustLoadMigrations(t)
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range migrations {
		if m.version < 1 {
			t.Errorf("migration %d has version %d, want >= 1", i, m.version)
		}
		if m.name == "" || m.sql == "" {
			t.Errorf("migration %d is missing a name or body", m.version)
		}
		if i > 0 && migrations[i-1].version >= m.version {
			t.Errorf("migrations out of order: %d before %d", migrations[i-1].version, m.version)
		}
	}
}

func mustLoadMigrations(t *testing.T) []migration {
	t.Helper()
	m, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	return m
}
