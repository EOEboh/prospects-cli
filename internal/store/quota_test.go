package store

import (
	"context"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/quota"
)

func migratedDB(t *testing.T) *DB {
	t.Helper()
	db := testDB(t)
	if err := db.Migrate(context.Background(), discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// Cache hits must never count toward the ceiling: they cost nothing.
func TestBillableCallsExcludesCacheHits(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	sku := quota.SKUTextSearchEnterpise

	for i := 0; i < 3; i++ {
		if err := db.RecordCall(ctx, sku, "searchText", true, false, nil); err != nil {
			t.Fatalf("record live call: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := db.RecordCall(ctx, sku, "searchText", false, true, nil); err != nil {
			t.Fatalf("record cache hit: %v", err)
		}
	}

	month := quota.Month(Now())
	got, err := db.BillableCalls(ctx, sku, month)
	if err != nil {
		t.Fatalf("BillableCalls: %v", err)
	}
	if got != 3 {
		t.Errorf("BillableCalls = %d, want 3 (5 cache hits must not count)", got)
	}
}

// Each SKU has its own allowance, so counts must not bleed across SKUs.
func TestBillableCallsIsPerSKU(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if err := db.RecordCall(ctx, quota.SKUTextSearchEnterpise, "searchText", true, false, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := db.RecordCall(ctx, quota.SKUPlaceDetailsPro, "places/get", true, false, nil); err != nil {
		t.Fatalf("record: %v", err)
	}

	month := quota.Month(Now())
	for _, tc := range []struct {
		sku  quota.SKU
		want int
	}{
		{quota.SKUTextSearchEnterpise, 1},
		{quota.SKUPlaceDetailsPro, 1},
		{quota.SKUTextSearchPro, 0},
	} {
		got, err := db.BillableCalls(ctx, tc.sku, month)
		if err != nil {
			t.Fatalf("BillableCalls(%s): %v", tc.sku, err)
		}
		if got != tc.want {
			t.Errorf("BillableCalls(%s) = %d, want %d", tc.sku, got, tc.want)
		}
	}
}

func TestBillableCallsIsPerMonth(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	sku := quota.SKUTextSearchEnterpise

	if err := db.RecordCall(ctx, sku, "searchText", true, false, nil); err != nil {
		t.Fatalf("record: %v", err)
	}

	got, err := db.BillableCalls(ctx, sku, "2020-01")
	if err != nil {
		t.Fatalf("BillableCalls: %v", err)
	}
	if got != 0 {
		t.Errorf("a past month reported %d calls, want 0", got)
	}
}

// The report must list every SKU, including untouched ones — "how much have I
// got left" is answered by a zero row, not by an absent one.
func TestUsageForMonthListsEverySKU(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	month := quota.Month(Now())

	if err := db.RecordCall(ctx, quota.SKUTextSearchEnterpise, "searchText", true, false, nil); err != nil {
		t.Fatalf("record live: %v", err)
	}
	if err := db.RecordCall(ctx, quota.SKUTextSearchEnterpise, "searchText", false, true, nil); err != nil {
		t.Fatalf("record cached: %v", err)
	}

	usage, err := db.UsageForMonth(ctx, quota.DefaultLimits(), month)
	if err != nil {
		t.Fatalf("UsageForMonth: %v", err)
	}
	if len(usage) != len(quota.AllSKUs()) {
		t.Fatalf("UsageForMonth returned %d rows, want %d", len(usage), len(quota.AllSKUs()))
	}

	for _, u := range usage {
		switch u.SKU {
		case quota.SKUTextSearchEnterpise:
			if u.Billable != 1 || u.Cached != 1 {
				t.Errorf("text search enterprise: billable=%d cached=%d, want 1 and 1", u.Billable, u.Cached)
			}
			if u.Ceiling != 900 {
				t.Errorf("ceiling = %d, want 900 (free tier, default margin)", u.Ceiling)
			}
			if u.Remaining() != 899 {
				t.Errorf("remaining = %d, want 899", u.Remaining())
			}
		default:
			if u.Billable != 0 || u.Cached != 0 {
				t.Errorf("%s should be untouched, got billable=%d cached=%d", u.SKU, u.Billable, u.Cached)
			}
		}
	}
}

// The ledger and the store must agree; this is the seam where a mismatch would
// silently let a run spend past the ceiling.
func TestLedgerEnforcesAgainstStoredCalls(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	sku := quota.SKUTextSearchEnterpise

	// A tight ceiling makes the boundary cheap to reach in a test.
	limits := quota.Limits{Overrides: map[string]int{sku.Name: 3}}
	ledger := quota.NewLedger(db, limits, discardLogger())

	for i := 0; i < 3; i++ {
		if err := ledger.Check(ctx, sku, 1); err != nil {
			t.Fatalf("call %d refused early: %v", i+1, err)
		}
		if err := ledger.Record(ctx, sku, "searchText", false, nil); err != nil {
			t.Fatalf("record call %d: %v", i+1, err)
		}
	}

	if err := ledger.Check(ctx, sku, 1); err == nil {
		t.Error("the fourth call should have been refused at a ceiling of 3")
	}

	// A cache hit past the ceiling is still fine: it spends nothing.
	if err := ledger.Record(ctx, sku, "searchText", true, nil); err != nil {
		t.Fatalf("record cache hit: %v", err)
	}
	got, err := db.BillableCalls(ctx, sku, quota.Month(Now()))
	if err != nil {
		t.Fatalf("BillableCalls: %v", err)
	}
	if got != 3 {
		t.Errorf("billable count = %d, want 3 (the cache hit must not count)", got)
	}
}
