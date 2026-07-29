package quota

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeCounter struct {
	billable map[string]int
	recorded []recordedCall
	err      error
}

type recordedCall struct {
	sku      SKU
	endpoint string
	billable bool
	cached   bool
}

func newFakeCounter() *fakeCounter {
	return &fakeCounter{billable: make(map[string]int)}
}

func (f *fakeCounter) BillableCalls(_ context.Context, sku SKU, month string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.billable[sku.Name+"|"+month], nil
}

func (f *fakeCounter) RecordCall(_ context.Context, sku SKU, endpoint string, billable, cached bool, _ *int64) error {
	f.recorded = append(f.recorded, recordedCall{sku, endpoint, billable, cached})
	return nil
}

func testLedger(t *testing.T, c Counter, limits Limits) *Ledger {
	t.Helper()
	l := NewLedger(c, limits, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.now = func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) }
	return l
}

// The default posture is free-only: the ceiling is the free allowance scaled
// by the safety margin, never more.
func TestDefaultCeilingsStayFree(t *testing.T) {
	limits := DefaultLimits()

	tests := []struct {
		sku  SKU
		want int
	}{
		{SKUTextSearchIDsOnly, 9000},  // essentials 10k * 0.9
		{SKUTextSearchPro, 4500},      // pro 5k * 0.9
		{SKUTextSearchEnterpise, 900}, // enterprise 1k * 0.9 — what discover uses
		{SKUPlaceDetailsEnterprise, 900},
	}
	for _, tc := range tests {
		if got := limits.Ceiling(tc.sku); got != tc.want {
			t.Errorf("Ceiling(%s) = %d, want %d", tc.sku.Name, got, tc.want)
		}
	}
}

// This is the property the whole design turns on: no configuration short of
// the explicit opt-in can produce a bill.
func TestOverrideCannotExceedFreeTierWithoutOptIn(t *testing.T) {
	limits := Limits{
		AllowPaid:    false,
		SafetyMargin: DefaultSafetyMargin,
		Overrides:    map[string]int{SKUTextSearchEnterpise.Name: 50_000},
	}

	if got := limits.Ceiling(SKUTextSearchEnterpise); got != 900 {
		t.Errorf("a 50000 override with AllowPaid=false gave a ceiling of %d, want 900", got)
	}
	want, clamped := limits.Clamped(SKUTextSearchEnterpise)
	if !clamped || want != 50_000 {
		t.Errorf("Clamped() = (%d, %v), want (50000, true) so the operator is told", want, clamped)
	}

	// The same number with the opt-in is honored.
	limits.AllowPaid = true
	if got := limits.Ceiling(SKUTextSearchEnterpise); got != 50_000 {
		t.Errorf("with AllowPaid=true, ceiling = %d, want 50000", got)
	}
	if _, clamped := limits.Clamped(SKUTextSearchEnterpise); clamped {
		t.Error("nothing is clamped once paid calls are allowed")
	}
}

// An override below the free allowance is honored without the opt-in: tighter
// than free is always fine.
func TestOverrideBelowFreeTierIsHonored(t *testing.T) {
	limits := Limits{
		AllowPaid:    false,
		SafetyMargin: DefaultSafetyMargin,
		Overrides:    map[string]int{SKUTextSearchEnterpise.Name: 100},
	}
	if got := limits.Ceiling(SKUTextSearchEnterpise); got != 100 {
		t.Errorf("Ceiling = %d, want 100", got)
	}
	if _, clamped := limits.Clamped(SKUTextSearchEnterpise); clamped {
		t.Error("a ceiling below the free allowance is not a clamp")
	}
}

func TestSafetyMarginApplies(t *testing.T) {
	tests := []struct {
		margin float64
		want   int
	}{
		{1.0, 1000},
		{0.9, 900},
		{0.5, 500},
		{0, 900},   // invalid falls back to the default
		{1.5, 900}, // ditto: a margin above 1 would exceed the free tier
	}
	for _, tc := range tests {
		limits := Limits{SafetyMargin: tc.margin}
		if got := limits.Ceiling(SKUTextSearchEnterpise); got != tc.want {
			t.Errorf("margin %v: ceiling = %d, want %d", tc.margin, got, tc.want)
		}
	}
}

// The Ad Library is free but rate-limited, so its cap is politeness rather
// than cost and the paid switch is irrelevant to it.
func TestUnmeteredSKUCap(t *testing.T) {
	if got := DefaultLimits().Ceiling(SKUAdsArchive); got != DefaultUnmeteredCap {
		t.Errorf("Ceiling(ads_archive) = %d, want %d", got, DefaultUnmeteredCap)
	}
	limits := Limits{Overrides: map[string]int{SKUAdsArchive.Name: 42}}
	if got := limits.Ceiling(SKUAdsArchive); got != 42 {
		t.Errorf("override ignored for unmetered SKU: got %d, want 42", got)
	}
}

func TestCheckStopsAtCeiling(t *testing.T) {
	tests := []struct {
		name    string
		used    int
		want    int
		wantErr bool
	}{
		{"well inside", 0, 1, false},
		{"exactly at the ceiling", 899, 1, false},
		{"one past the ceiling", 900, 1, true},
		{"batch that would overshoot", 895, 10, true},
		{"batch that just fits", 890, 10, false},
		{"already over", 5000, 1, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeCounter()
			c.billable[SKUTextSearchEnterpise.Name+"|2026-07"] = tc.used
			l := testLedger(t, c, DefaultLimits())

			err := l.Check(context.Background(), SKUTextSearchEnterpise, tc.want)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("used=%d want=%d: expected refusal", tc.used, tc.want)
				}
				// Callers must be able to distinguish "stop" from "retry".
				if !errors.Is(err, ErrCeilingReached) {
					t.Errorf("error %v does not match ErrCeilingReached", err)
				}
				var ce *CeilingError
				if !errors.As(err, &ce) {
					t.Fatalf("error %v is not a *CeilingError", err)
				}
				if ce.Used != tc.used || ce.Ceiling != 900 {
					t.Errorf("CeilingError{Used:%d, Ceiling:%d}, want {%d, 900}", ce.Used, ce.Ceiling, tc.used)
				}
			} else if err != nil {
				t.Fatalf("used=%d want=%d: unexpected refusal: %v", tc.used, tc.want, err)
			}
		})
	}
}

func TestCheckZeroCallsAlwaysPasses(t *testing.T) {
	c := newFakeCounter()
	c.billable[SKUTextSearchEnterpise.Name+"|2026-07"] = 100_000
	l := testLedger(t, c, DefaultLimits())

	// A dry run asking for nothing must not be refused.
	if err := l.Check(context.Background(), SKUTextSearchEnterpise, 0); err != nil {
		t.Errorf("Check for zero calls: %v", err)
	}
}

// Usage is counted per calendar month, matching when the allowance resets.
func TestCheckIsScopedToTheCurrentMonth(t *testing.T) {
	c := newFakeCounter()
	c.billable[SKUTextSearchEnterpise.Name+"|2026-06"] = 5000 // last month, exhausted
	l := testLedger(t, c, DefaultLimits())

	if err := l.Check(context.Background(), SKUTextSearchEnterpise, 1); err != nil {
		t.Errorf("last month's usage blocked this month: %v", err)
	}
}

func TestCheckPropagatesCounterErrors(t *testing.T) {
	c := newFakeCounter()
	c.err = errors.New("database is locked")
	l := testLedger(t, c, DefaultLimits())

	err := l.Check(context.Background(), SKUTextSearchEnterpise, 1)
	if err == nil {
		t.Fatal("a counter failure must not read as permission to spend")
	}
	if errors.Is(err, ErrCeilingReached) {
		t.Error("a counter failure is not a ceiling error")
	}
}

// Cache hits are recorded as non-billable so the quota report can show what
// the cache saved without inflating the spend.
func TestRecordMarksCacheHitsNonBillable(t *testing.T) {
	c := newFakeCounter()
	l := testLedger(t, c, DefaultLimits())
	ctx := context.Background()

	if err := l.Record(ctx, SKUTextSearchEnterpise, "searchText", false, nil); err != nil {
		t.Fatalf("Record live call: %v", err)
	}
	if err := l.Record(ctx, SKUTextSearchEnterpise, "searchText", true, nil); err != nil {
		t.Fatalf("Record cache hit: %v", err)
	}

	if len(c.recorded) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(c.recorded))
	}
	if !c.recorded[0].billable || c.recorded[0].cached {
		t.Error("a live call should be billable and not cached")
	}
	if c.recorded[1].billable || !c.recorded[1].cached {
		t.Error("a cache hit should be cached and not billable")
	}
}

func TestMonthFormat(t *testing.T) {
	got := Month(time.Date(2026, 7, 29, 23, 59, 0, 0, time.FixedZone("UTC+13", 13*3600)))
	// Normalized to UTC so a late-evening run does not land in the wrong bucket.
	if got != "2026-07" {
		t.Errorf("Month = %q, want 2026-07", got)
	}
}

func TestUsageArithmetic(t *testing.T) {
	tests := []struct {
		name          string
		usage         Usage
		wantRemaining int
		wantPercent   float64
	}{
		{"fresh", Usage{Billable: 0, Ceiling: 900}, 900, 0},
		{"half", Usage{Billable: 450, Ceiling: 900}, 450, 50},
		{"exhausted", Usage{Billable: 900, Ceiling: 900}, 0, 100},
		{"over never goes negative", Usage{Billable: 1200, Ceiling: 900}, 0, 133.33333333333331},
		{"zero ceiling", Usage{Billable: 5, Ceiling: 0}, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.usage.Remaining(); got != tc.wantRemaining {
				t.Errorf("Remaining = %d, want %d", got, tc.wantRemaining)
			}
			if got := tc.usage.PercentUsed(); got != tc.wantPercent {
				t.Errorf("PercentUsed = %v, want %v", got, tc.wantPercent)
			}
		})
	}
}
