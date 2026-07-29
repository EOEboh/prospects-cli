package store

import (
	"context"
	"fmt"

	"github.com/EOEboh/prospects-cli/internal/quota"
)

// BillableCalls counts charged calls for a SKU in a month. Cache hits are
// excluded — they never reached the network and were never billed.
//
// Implements quota.Counter.
func (db *DB) BillableCalls(ctx context.Context, sku quota.SKU, month string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_calls
		 WHERE provider = ? AND sku = ? AND month = ? AND billable = 1`,
		sku.Provider, sku.Name, month).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count billable calls for %s in %s: %w", sku, month, err)
	}
	return n, nil
}

// RecordCall appends one call to the local ledger.
//
// Implements quota.Counter.
func (db *DB) RecordCall(ctx context.Context, sku quota.SKU, endpoint string, billable, cached bool, runID *int64) error {
	now := Now()
	_, err := db.ExecContext(ctx,
		`INSERT INTO api_calls (provider, sku, endpoint, billable, cached, run_id, called_at, month)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sku.Provider, sku.Name, endpoint, boolToInt(billable), boolToInt(cached),
		runID, FormatTime(now), quota.Month(now))
	if err != nil {
		return fmt.Errorf("record call for %s: %w", sku, err)
	}
	return nil
}

// UsageForMonth reports every SKU's position against its ceiling, including
// SKUs with no calls yet — a zero row is the useful answer for "how much have
// I got left", not an absent one.
func (db *DB) UsageForMonth(ctx context.Context, limits quota.Limits, month string) ([]quota.Usage, error) {
	type counts struct{ billable, cached int }
	seen := make(map[string]counts)

	rows, err := db.QueryContext(ctx,
		`SELECT sku,
		        SUM(CASE WHEN billable = 1 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN cached   = 1 THEN 1 ELSE 0 END)
		 FROM api_calls WHERE month = ? GROUP BY sku`, month)
	if err != nil {
		return nil, fmt.Errorf("read usage for %s: %w", month, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var c counts
		if err := rows.Scan(&name, &c.billable, &c.cached); err != nil {
			return nil, err
		}
		seen[name] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	all := quota.AllSKUs()
	usage := make([]quota.Usage, 0, len(all))
	for _, sku := range all {
		c := seen[sku.Name]
		usage = append(usage, quota.Usage{
			SKU:         sku,
			Month:       month,
			Billable:    c.billable,
			Cached:      c.cached,
			Ceiling:     limits.Ceiling(sku),
			FreeMonthly: sku.Tier.FreeMonthly,
		})
	}
	return usage, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
