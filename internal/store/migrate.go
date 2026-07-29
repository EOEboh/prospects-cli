package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies every unapplied migration in version order, each in its own
// transaction alongside the schema_migrations row that records it. Applying
// twice is a no-op, so it runs unconditionally at startup.
func (db *DB) Migrate(ctx context.Context, log *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := db.apply(ctx, m); err != nil {
			return err
		}
		log.Info("migration applied", "version", m.version, "name", m.name)
	}
	return nil
}

func (db *DB) apply(ctx context.Context, m migration) error {
	// A migration and its bookkeeping row commit together: a crash mid-way
	// leaves the database at the previous version, never half-migrated.
	err := db.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("exec: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, FormatTime(Now()))
		return err
	})
	if err != nil {
		return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// SchemaVersion is the highest applied migration, 0 on a fresh database.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

// loadMigrations parses the embedded files, which are named NNNN_name.sql.
// Duplicate version numbers are a build-time mistake worth failing loudly on:
// two migrations claiming version 3 means one silently never runs.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var migrations []migration
	seen := make(map[int]string)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, rest, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: want NNNN_name.sql", e.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: %q is not a version number", e.Name(), prefix)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", other, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", e.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: rest, sql: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}
