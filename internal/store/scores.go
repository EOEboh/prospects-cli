package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// SaveScore appends a score for a business. Scores are never updated in place:
// each run leaves its own row, so a retune can be compared against what came
// before rather than overwriting it.
func (db *DB) SaveScore(ctx context.Context, s *model.Score) error {
	breakdown, err := json.Marshal(s.Breakdown)
	if err != nil {
		return fmt.Errorf("encode score breakdown: %w", err)
	}

	res, err := db.ExecContext(ctx,
		`INSERT INTO scores
		   (business_id, run_id, score, raw_score, max_possible, confidence,
		    needs_manual_check, breakdown, explanation, weights_hash, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		s.BusinessID, s.RunID, s.Score, s.Raw, s.MaxPossible, s.Confidence,
		boolToInt(s.NeedsManualCheck), string(breakdown), s.Explanation,
		s.WeightsHash, FormatTime(Now()))
	if err != nil {
		return fmt.Errorf("save score for business %d: %w", s.BusinessID, err)
	}
	s.ID, err = res.LastInsertId()
	return err
}

// ScoredBusiness pairs a business with its latest score, which is what every
// listing command needs.
type ScoredBusiness struct {
	Business model.Business
	Score    model.Score
	Status   model.OutreachStatus
}

// BusinessesToScore lists active businesses eligible for scoring.
//
// Businesses with no signals at all are included: "nothing known yet" is a
// legitimate score of zero, and excluding them would hide the work still to do.
func (db *DB) BusinessesToScore(ctx context.Context, businessID int64) ([]model.Business, error) {
	query := `
		SELECT id, name, website, domain, name_key, email, phone, address,
		       city, region, country, rating, review_count, place_id, source
		FROM v_active_businesses`
	args := []any{}
	if businessID != 0 {
		query += ` WHERE id = ?`
		args = append(args, businessID)
	}
	query += ` ORDER BY id`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list businesses to score: %w", err)
	}
	defer rows.Close()

	var out []model.Business
	for rows.Next() {
		b, err := scanBusinessRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func scanBusinessRow(rows *sql.Rows) (*model.Business, error) {
	var (
		b           model.Business
		rating      sql.NullFloat64
		reviewCount sql.NullInt64
		placeID     sql.NullString
	)
	if err := rows.Scan(&b.ID, &b.Name, &b.Website, &b.Domain, &b.NameKey, &b.Email,
		&b.Phone, &b.Address, &b.City, &b.Region, &b.Country,
		&rating, &reviewCount, &placeID, &b.Source); err != nil {
		return nil, err
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

// ListFilter narrows a listing. Suppressed businesses are excluded by the
// underlying view, not by anything here, so no caller can forget.
type ListFilter struct {
	MinScore        int
	MaxScore        int
	Status          model.OutreachStatus
	NeedsAdCheck    bool
	UncontactedOnly bool
	Limit           int
}

// ListScored returns businesses with their latest score, highest first.
func (db *DB) ListScored(ctx context.Context, f ListFilter) ([]ScoredBusiness, error) {
	query := `
		SELECT b.id, b.name, b.website, b.domain, b.name_key, b.email, b.phone,
		       b.address, b.city, b.region, b.country, b.rating, b.review_count,
		       b.place_id, b.source,
		       s.score, s.raw_score, s.max_possible, s.confidence,
		       s.needs_manual_check, s.breakdown, s.explanation, s.weights_hash,
		       COALESCE(o.status, 'not_contacted')
		FROM v_active_businesses b
		JOIN v_latest_scores s ON s.business_id = b.id
		LEFT JOIN outreach o ON o.business_id = b.id
		WHERE s.score >= ?`
	args := []any{f.MinScore}

	if f.MaxScore > 0 && f.MaxScore < 100 {
		query += ` AND s.score <= ?`
		args = append(args, f.MaxScore)
	}
	if f.Status != "" {
		query += ` AND COALESCE(o.status, 'not_contacted') = ?`
		args = append(args, string(f.Status))
	}
	if f.UncontactedOnly {
		query += ` AND COALESCE(o.status, 'not_contacted') = 'not_contacted'`
	}
	if f.NeedsAdCheck {
		query += ` AND s.needs_manual_check = 1`
	}

	query += ` ORDER BY s.score DESC, b.id`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list scored businesses: %w", err)
	}
	defer rows.Close()

	var out []ScoredBusiness
	for rows.Next() {
		var (
			sb          ScoredBusiness
			rating      sql.NullFloat64
			reviewCount sql.NullInt64
			placeID     sql.NullString
			needsCheck  int
			breakdown   string
			status      string
		)
		if err := rows.Scan(
			&sb.Business.ID, &sb.Business.Name, &sb.Business.Website, &sb.Business.Domain,
			&sb.Business.NameKey, &sb.Business.Email, &sb.Business.Phone, &sb.Business.Address,
			&sb.Business.City, &sb.Business.Region, &sb.Business.Country,
			&rating, &reviewCount, &placeID, &sb.Business.Source,
			&sb.Score.Score, &sb.Score.Raw, &sb.Score.MaxPossible, &sb.Score.Confidence,
			&needsCheck, &breakdown, &sb.Score.Explanation, &sb.Score.WeightsHash,
			&status,
		); err != nil {
			return nil, err
		}

		if rating.Valid {
			sb.Business.Rating = &rating.Float64
		}
		if reviewCount.Valid {
			n := int(reviewCount.Int64)
			sb.Business.ReviewCount = &n
		}
		sb.Business.PlaceID = placeID.String
		sb.Score.BusinessID = sb.Business.ID
		sb.Score.NeedsManualCheck = needsCheck == 1
		sb.Status = model.OutreachStatus(status)

		if breakdown != "" {
			if err := json.Unmarshal([]byte(breakdown), &sb.Score.Breakdown); err != nil {
				return nil, fmt.Errorf("decode breakdown for business %d: %w", sb.Business.ID, err)
			}
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}
