package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// SignalWrite says what happened to one observation.
type SignalWrite int

const (
	SignalNew        SignalWrite = iota // first time this fact was seen
	SignalConfirmed                     // same value again; only last_seen_at moved
	SignalTransition                    // the value changed; the old row was superseded
)

func (w SignalWrite) String() string {
	switch w {
	case SignalNew:
		return "new"
	case SignalConfirmed:
		return "confirmed"
	case SignalTransition:
		return "transition"
	default:
		return "unknown"
	}
}

// RecordSignal writes one observation, appending only when something changed.
//
// Re-observing a value bumps last_seen_at on the existing row. A different
// value supersedes the old row and inserts a new one, which is what preserves
// the transition — a business that starts running ads is the hot buying moment,
// and it is invisible without that history.
//
// Multi-valued types coexist instead of superseding: a business running HubSpot
// and Calendly has two current facts, not one that replaces the other.
func (db *DB) RecordSignal(ctx context.Context, s *model.Signal, runID *int64) (SignalWrite, error) {
	spec, err := model.LookupSignalType(string(s.Type))
	if err != nil {
		return 0, err
	}
	if s.Confidence <= 0 {
		s.Confidence = 1.0
	}

	var outcome SignalWrite
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		now := FormatTime(Now())

		// Same value already current: confirm it and stop.
		var id int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM signals
			 WHERE business_id = ? AND source = ? AND type = ? AND value = ? AND superseded_at IS NULL`,
			s.BusinessID, s.Source, s.Type, s.Value).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE signals SET last_seen_at = ?, confidence = ?, detail = ?, run_id = ?
				 WHERE id = ?`,
				now, s.Confidence, s.Detail, runID, id); err != nil {
				return fmt.Errorf("confirm signal: %w", err)
			}
			s.ID = id
			outcome = SignalConfirmed
			return nil
		case err != sql.ErrNoRows:
			return fmt.Errorf("look up current signal: %w", err)
		}

		// A single-valued type has at most one current row, so a differing
		// value replaces it. Multi-valued types leave their siblings alone.
		outcome = SignalNew
		if !spec.Multi {
			res, err := tx.ExecContext(ctx,
				`UPDATE signals SET superseded_at = ?
				 WHERE business_id = ? AND source = ? AND type = ? AND superseded_at IS NULL`,
				now, s.BusinessID, s.Source, s.Type)
			if err != nil {
				return fmt.Errorf("supersede previous signal: %w", err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				outcome = SignalTransition
			}
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO signals
			   (business_id, source, type, value, confidence, detail, note,
			    observed_at, last_seen_at, run_id)
			 VALUES (?,?,?,?,?,?,?,?,?,?)`,
			s.BusinessID, s.Source, s.Type, s.Value, s.Confidence, s.Detail, s.Note,
			now, now, runID)
		if err != nil {
			return fmt.Errorf("insert signal: %w", err)
		}
		s.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	return outcome, nil
}

// RecordSignals writes a batch, reporting how many were transitions so a run
// summary can say "3 businesses started running ads" rather than just "wrote
// 40 rows".
func (db *DB) RecordSignals(ctx context.Context, signals []model.Signal, runID *int64) (transitions int, err error) {
	for i := range signals {
		outcome, err := db.RecordSignal(ctx, &signals[i], runID)
		if err != nil {
			return transitions, err
		}
		if outcome == SignalTransition {
			transitions++
		}
	}
	return transitions, nil
}

// CurrentSignals returns present belief about a business: one row per current
// fact, with superseded history left behind.
func (db *DB) CurrentSignals(ctx context.Context, businessID int64) ([]model.Signal, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, business_id, source, type, value, confidence, detail, note,
		        observed_at, last_seen_at
		 FROM v_current_signals WHERE business_id = ?
		 ORDER BY type, value`, businessID)
	if err != nil {
		return nil, fmt.Errorf("read signals for business %d: %w", businessID, err)
	}
	defer rows.Close()

	var out []model.Signal
	for rows.Next() {
		var (
			s                      model.Signal
			observedAt, lastSeenAt string
		)
		if err := rows.Scan(&s.ID, &s.BusinessID, &s.Source, &s.Type, &s.Value,
			&s.Confidence, &s.Detail, &s.Note, &observedAt, &lastSeenAt); err != nil {
			return nil, err
		}
		if s.ObservedAt, err = ParseTime(observedAt); err != nil {
			return nil, err
		}
		if s.LastSeenAt, err = ParseTime(lastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SignalHistory returns every observation of one type for a business, newest
// first, which is how a transition is inspected after the fact.
func (db *DB) SignalHistory(ctx context.Context, businessID int64, t model.SignalType) ([]model.Signal, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, business_id, source, type, value, confidence, detail, note,
		        observed_at, last_seen_at, superseded_at
		 FROM signals WHERE business_id = ? AND type = ?
		 ORDER BY observed_at DESC, id DESC`, businessID, t)
	if err != nil {
		return nil, fmt.Errorf("read signal history: %w", err)
	}
	defer rows.Close()

	var out []model.Signal
	for rows.Next() {
		var (
			s                      model.Signal
			observedAt, lastSeenAt string
			supersededAt           sql.NullString
		)
		if err := rows.Scan(&s.ID, &s.BusinessID, &s.Source, &s.Type, &s.Value,
			&s.Confidence, &s.Detail, &s.Note, &observedAt, &lastSeenAt, &supersededAt); err != nil {
			return nil, err
		}
		if s.ObservedAt, err = ParseTime(observedAt); err != nil {
			return nil, err
		}
		if s.LastSeenAt, err = ParseTime(lastSeenAt); err != nil {
			return nil, err
		}
		if supersededAt.Valid {
			t, err := ParseTime(supersededAt.String)
			if err != nil {
				return nil, err
			}
			s.SupersededAt = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// BusinessesToEnrich lists businesses due for a website fetch, newest first.
//
// Suppressed businesses are excluded structurally, and businesses with no
// website are skipped: there is nothing to fetch.
func (db *DB) BusinessesToEnrich(ctx context.Context, force bool, limit int) ([]model.Business, error) {
	query := `
		SELECT b.id, b.name, b.website, b.domain, b.name_key, b.email, b.phone,
		       b.address, b.city, b.region, b.country, b.place_id, b.source
		FROM v_active_businesses b
		WHERE b.website <> ''`

	// Without --force, a business that already has website signals is left
	// alone. The HTTP cache would make a refetch cheap, but not free, and the
	// point of enrich --all-pending is to fill gaps.
	if !force {
		query += ` AND NOT EXISTS (
			SELECT 1 FROM v_current_signals s
			WHERE s.business_id = b.id AND s.source = 'website')`
	}
	query += ` ORDER BY b.id`

	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list businesses to enrich: %w", err)
	}
	defer rows.Close()

	var out []model.Business
	for rows.Next() {
		var (
			b       model.Business
			placeID sql.NullString
		)
		if err := rows.Scan(&b.ID, &b.Name, &b.Website, &b.Domain, &b.NameKey, &b.Email,
			&b.Phone, &b.Address, &b.City, &b.Region, &b.Country, &placeID, &b.Source); err != nil {
			return nil, err
		}
		b.PlaceID = placeID.String
		out = append(out, b)
	}
	return out, rows.Err()
}

// BusinessByID loads one active business. A suppressed business is not found,
// so no command can operate on one by accident.
func (db *DB) BusinessByID(ctx context.Context, id int64) (*model.Business, error) {
	var (
		b       model.Business
		placeID sql.NullString
	)
	err := db.QueryRowContext(ctx,
		`SELECT id, name, website, domain, name_key, email, phone, address,
		        city, region, country, place_id, source
		 FROM v_active_businesses WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Website, &b.Domain, &b.NameKey, &b.Email, &b.Phone,
			&b.Address, &b.City, &b.Region, &b.Country, &placeID, &b.Source)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("business %d not found (it may be suppressed)", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load business %d: %w", id, err)
	}
	b.PlaceID = placeID.String
	return &b, nil
}

// UpdateBusinessEmail promotes a discovered address onto the business record,
// so list and export can show it without joining signals. It never overwrites
// an address already on file.
func (db *DB) UpdateBusinessEmail(ctx context.Context, businessID int64, email string) error {
	if email == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE businesses SET email = ?, updated_at = ? WHERE id = ? AND email = ''`,
		email, FormatTime(Now()), businessID)
	if err != nil {
		return fmt.Errorf("set email for business %d: %w", businessID, err)
	}
	return nil
}
