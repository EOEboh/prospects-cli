package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// OutreachFor returns a business's current pipeline position, defaulting to
// not_contacted for a business that has never been touched.
func (db *DB) OutreachFor(ctx context.Context, businessID int64) (*model.Outreach, error) {
	var (
		o         model.Outreach
		updatedAt string
	)
	err := db.QueryRowContext(ctx,
		`SELECT business_id, status, notes, updated_at FROM outreach WHERE business_id = ?`,
		businessID).Scan(&o.BusinessID, &o.Status, &o.Notes, &updatedAt)
	if err == sql.ErrNoRows {
		return &model.Outreach{BusinessID: businessID, Status: model.StatusNotContacted}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read outreach for business %d: %w", businessID, err)
	}
	if o.UpdatedAt, err = ParseTime(updatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

// SetOutreachStatus moves a business along the pipeline and appends the
// transition to its history.
//
// The transition is recorded rather than only the new state, so a sequence of
// touches stays reconstructable: "emailed twice, no reply" is a different
// situation from "emailed once last week".
func (db *DB) SetOutreachStatus(ctx context.Context, businessID int64, status model.OutreachStatus, note string) (from model.OutreachStatus, err error) {
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		now := FormatTime(Now())

		from = model.StatusNotContacted
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM outreach WHERE business_id = ?`, businessID).Scan(&from)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read current status: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outreach (business_id, status, notes, updated_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(business_id) DO UPDATE SET
			   status = excluded.status,
			   notes  = CASE WHEN excluded.notes <> '' THEN excluded.notes ELSE outreach.notes END,
			   updated_at = excluded.updated_at`,
			businessID, status, note, now); err != nil {
			return fmt.Errorf("set outreach status: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outreach_events (business_id, from_status, to_status, note, at)
			 VALUES (?, ?, ?, ?, ?)`,
			businessID, from, status, note, now); err != nil {
			return fmt.Errorf("record outreach event: %w", err)
		}
		return nil
	})
	return from, err
}

// OutreachHistory returns a business's transitions, oldest first, which is how
// the sequence of touches is read back.
func (db *DB) OutreachHistory(ctx context.Context, businessID int64) ([]model.OutreachEvent, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, business_id, from_status, to_status, note, at
		 FROM outreach_events WHERE business_id = ? ORDER BY at, id`, businessID)
	if err != nil {
		return nil, fmt.Errorf("read outreach history: %w", err)
	}
	defer rows.Close()

	var out []model.OutreachEvent
	for rows.Next() {
		var (
			e  model.OutreachEvent
			at string
		)
		if err := rows.Scan(&e.ID, &e.BusinessID, &e.From, &e.To, &e.Note, &at); err != nil {
			return nil, err
		}
		if e.At, err = ParseTime(at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Suppress excludes a business from every future run, export and brief.
//
// The domain is suppressed alongside the row unless idOnly is set. Suppressing
// only the id leaks: re-running discover recreates the business under a fresh
// id and it reappears in tomorrow's brief.
func (db *DB) Suppress(ctx context.Context, businessID int64, reason string, idOnly bool) (domain string, err error) {
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		now := FormatTime(Now())

		var name string
		if err := tx.QueryRowContext(ctx,
			`SELECT domain, name FROM businesses WHERE id = ?`, businessID).Scan(&domain, &name); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("business %d not found", businessID)
			}
			return fmt.Errorf("load business %d: %w", businessID, err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO suppression (business_id, reason, created_at) VALUES (?, ?, ?)
			 ON CONFLICT(business_id) DO UPDATE SET reason = excluded.reason`,
			businessID, reason, now); err != nil {
			return fmt.Errorf("suppress business: %w", err)
		}

		if !idOnly && domain != "" {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO suppression_domains (domain, reason, created_at) VALUES (?, ?, ?)
				 ON CONFLICT(domain) DO UPDATE SET reason = excluded.reason`,
				domain, reason, now); err != nil {
				return fmt.Errorf("suppress domain: %w", err)
			}
		}

		// Close the pipeline entry too, so the outreach history explains why
		// this prospect stopped rather than simply going quiet.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outreach (business_id, status, notes, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(business_id) DO UPDATE SET
			   status = excluded.status, notes = excluded.notes, updated_at = excluded.updated_at`,
			businessID, model.StatusDead, "suppressed: "+reason, now); err != nil {
			return fmt.Errorf("close outreach entry: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outreach_events (business_id, from_status, to_status, note, at)
			 VALUES (?, ?, ?, ?, ?)`,
			businessID, "", model.StatusDead, "suppressed: "+reason, now); err != nil {
			return fmt.Errorf("record suppression event: %w", err)
		}
		return nil
	})
	return domain, err
}

// IsSuppressed reports whether a business is excluded, and why.
func (db *DB) IsSuppressed(ctx context.Context, businessID int64) (bool, string, error) {
	var reason string
	err := db.QueryRowContext(ctx,
		`SELECT reason FROM suppression WHERE business_id = ?`, businessID).Scan(&reason)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("check suppression: %w", err)
	}
	return true, reason, nil
}

// AnyBusinessByID loads a business whether or not it is suppressed, which
// `suppress` and `status` need in order to act on one.
func (db *DB) AnyBusinessByID(ctx context.Context, id int64) (*model.Business, error) {
	var (
		b       model.Business
		placeID sql.NullString
	)
	err := db.QueryRowContext(ctx,
		`SELECT id, name, website, domain, name_key, email, phone, address,
		        city, region, country, place_id, source
		 FROM businesses WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Website, &b.Domain, &b.NameKey, &b.Email, &b.Phone,
			&b.Address, &b.City, &b.Region, &b.Country, &placeID, &b.Source)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("business %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load business %d: %w", id, err)
	}
	b.PlaceID = placeID.String
	return &b, nil
}
