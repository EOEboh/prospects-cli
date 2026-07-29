package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/EOEboh/prospects-cli/internal/dedup"
	"github.com/EOEboh/prospects-cli/internal/model"
)

// UpsertResult says what happened to one incoming record.
type UpsertResult int

const (
	Inserted   UpsertResult = iota
	Updated                 // matched an existing business; new details merged in
	Unchanged               // matched, and had nothing new to add
	Suppressed              // domain is on the suppression list; not written
)

func (r UpsertResult) String() string {
	switch r {
	case Inserted:
		return "inserted"
	case Updated:
		return "updated"
	case Unchanged:
		return "unchanged"
	case Suppressed:
		return "suppressed"
	default:
		return "unknown"
	}
}

// ErrNoIdentity means a record has neither a usable website nor a name, so
// there is no way to tell it apart from anything else.
var ErrNoIdentity = errors.New("record has no website and no name: nothing to deduplicate on")

// UpsertBusiness inserts a business or merges it into the existing row that
// shares its identity.
//
// Matching is by normalized domain first and normalized name plus city second,
// mirroring the unique indexes so the application and the database never
// disagree about what "same business" means.
//
// Merging never overwrites a known value with an empty one: a CSV row carrying
// only a name must not wipe the email that enrichment found.
func (db *DB) UpsertBusiness(ctx context.Context, b *model.Business) (int64, UpsertResult, error) {
	domain, nameKey, keyErr := dedup.Keys(b.Website, b.Name, b.City)
	if domain == "" && nameKey == "" {
		return 0, 0, ErrNoIdentity
	}
	b.Domain, b.NameKey = domain, nameKey

	var (
		id     int64
		result UpsertResult
	)
	err := db.WithTx(ctx, func(tx *sql.Tx) error {
		// Suppression is permanent and applies to every future run, so a
		// re-seeded or re-discovered suppressed domain is never recreated.
		if domain != "" {
			var suppressed int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM suppression_domains WHERE domain = ?`, domain).Scan(&suppressed); err != nil {
				return fmt.Errorf("check suppression: %w", err)
			}
			if suppressed > 0 {
				result = Suppressed
				return nil
			}
		}

		existing, err := findBusinessTx(ctx, tx, domain, nameKey)
		if err != nil {
			return err
		}
		if existing == nil {
			id, err = insertBusinessTx(ctx, tx, b)
			result = Inserted
			return err
		}

		id = existing.ID
		merged, changed := mergeBusiness(existing, b)
		if !changed {
			result = Unchanged
			return nil
		}
		result = Updated
		return updateBusinessTx(ctx, tx, merged)
	})
	if err != nil {
		return 0, 0, err
	}
	// The key error is reported alongside a successful write: the row landed
	// on its name key, but the operator should still fix the website column.
	return id, result, keyErr
}

func findBusinessTx(ctx context.Context, tx *sql.Tx, domain, nameKey string) (*model.Business, error) {
	const query = `SELECT id, name, website, domain, name_key, email, phone, address,
	                      city, region, country, rating, review_count, place_id, source
	               FROM businesses WHERE `

	if domain != "" {
		b, err := scanBusiness(tx.QueryRowContext(ctx, query+`domain = ?`, domain))
		if err != nil || b != nil {
			return b, err
		}
	}
	if nameKey != "" {
		// Only domainless rows are matched by name: a business with a known
		// website is identified by that website, and a name collision with it
		// is a different business.
		return scanBusiness(tx.QueryRowContext(ctx, query+`domain = '' AND name_key = ?`, nameKey))
	}
	return nil, nil
}

func scanBusiness(row *sql.Row) (*model.Business, error) {
	var (
		b           model.Business
		rating      sql.NullFloat64
		reviewCount sql.NullInt64
		placeID     sql.NullString
	)
	err := row.Scan(&b.ID, &b.Name, &b.Website, &b.Domain, &b.NameKey, &b.Email, &b.Phone,
		&b.Address, &b.City, &b.Region, &b.Country, &rating, &reviewCount, &placeID, &b.Source)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan business: %w", err)
	}

	if rating.Valid {
		b.Rating = &rating.Float64
	}
	if reviewCount.Valid {
		n := int(reviewCount.Int64)
		b.ReviewCount = &n
	}
	b.PlaceID = placeID.String
	return &b, nil
}

// mergeBusiness fills gaps in the stored record from the incoming one and
// reports whether anything actually changed.
//
// "Changed" has to mean changed, not merely "written again". Re-importing the
// same spreadsheet must be a no-op, or updated_at churns on every run and
// stops answering the question it exists to answer.
func mergeBusiness(existing, incoming *model.Business) (*model.Business, bool) {
	merged := *existing
	changed := false

	// keepFirst is for fields where any stored value is as good as any other.
	// A CSV holding both "acme.com" and "https://www.acme.com/contact" would
	// otherwise overwrite one with the other on every single import.
	keepFirst := func(dst *string, src string) {
		if *dst == "" && src != "" {
			*dst = src
			changed = true
		}
	}

	// correct is for fields where a later value is a genuine correction.
	// Comparison ignores case and surrounding space, so "Austin" arriving as
	// "austin" is recognised as the same city rather than a new one.
	correct := func(dst *string, src string) {
		if src == "" || strings.EqualFold(strings.TrimSpace(*dst), strings.TrimSpace(src)) {
			return
		}
		*dst = src
		changed = true
	}

	// A longer name is usually the more complete one ("Acme" vs "Acme
	// Recruiting"), and both normalize to the same key anyway. Length is a
	// stable tiebreak: the shorter form can never win it back.
	if incoming.Name != "" && len(incoming.Name) > len(merged.Name) {
		merged.Name = incoming.Name
		changed = true
	}

	// The stored URL is only a display value — the domain key is what
	// identifies the business, and every spelling of it resolves the same.
	keepFirst(&merged.Website, incoming.Website)

	correct(&merged.Email, incoming.Email)
	correct(&merged.Phone, incoming.Phone)
	correct(&merged.Address, incoming.Address)
	correct(&merged.City, incoming.City)
	correct(&merged.Region, incoming.Region)
	correct(&merged.Country, incoming.Country)
	correct(&merged.PlaceID, incoming.PlaceID)

	// A record that arrives with a website gains a domain key it did not have.
	if incoming.Domain != "" && merged.Domain != incoming.Domain {
		merged.Domain = incoming.Domain
		changed = true
	}
	if incoming.NameKey != "" && merged.NameKey != incoming.NameKey {
		merged.NameKey = incoming.NameKey
		changed = true
	}

	if incoming.Rating != nil && (merged.Rating == nil || *merged.Rating != *incoming.Rating) {
		merged.Rating = incoming.Rating
		changed = true
	}
	if incoming.ReviewCount != nil && (merged.ReviewCount == nil || *merged.ReviewCount != *incoming.ReviewCount) {
		merged.ReviewCount = incoming.ReviewCount
		changed = true
	}

	return &merged, changed
}

func insertBusinessTx(ctx context.Context, tx *sql.Tx, b *model.Business) (int64, error) {
	now := FormatTime(Now())
	res, err := tx.ExecContext(ctx,
		`INSERT INTO businesses
		   (name, website, domain, name_key, email, phone, address, city, region,
		    country, rating, review_count, place_id, source, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.Name, b.Website, b.Domain, b.NameKey, b.Email, b.Phone, b.Address,
		b.City, b.Region, b.Country, b.Rating, b.ReviewCount,
		nullString(b.PlaceID), b.Source, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert business %q: %w", b.Name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Every business starts in the outreach pipeline, so list --status
	// not_contacted does not need an outer join to find new prospects.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO outreach (business_id, status, updated_at) VALUES (?, ?, ?)`,
		id, model.StatusNotContacted, now); err != nil {
		return 0, fmt.Errorf("initialise outreach for business %d: %w", id, err)
	}
	return id, nil
}

func updateBusinessTx(ctx context.Context, tx *sql.Tx, b *model.Business) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE businesses SET name=?, website=?, domain=?, name_key=?, email=?, phone=?,
		        address=?, city=?, region=?, country=?, rating=?, review_count=?,
		        place_id=?, updated_at=?
		 WHERE id=?`,
		b.Name, b.Website, b.Domain, b.NameKey, b.Email, b.Phone, b.Address,
		b.City, b.Region, b.Country, b.Rating, b.ReviewCount,
		nullString(b.PlaceID), FormatTime(Now()), b.ID)
	if err != nil {
		return fmt.Errorf("update business %d: %w", b.ID, err)
	}
	return nil
}

// CountBusinesses reports how many active (non-suppressed) businesses exist.
func (db *DB) CountBusinesses(ctx context.Context) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v_active_businesses`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count businesses: %w", err)
	}
	return n, nil
}

// nullString keeps place_id NULL rather than ”, so the unique index on it
// ignores businesses that have none.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
