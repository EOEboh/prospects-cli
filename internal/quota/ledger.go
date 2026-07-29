package quota

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// DefaultSafetyMargin leaves headroom between this tool's ceiling and the real
// free allowance. The local counter can drift below Google's: retried requests,
// calls made from another machine sharing the key, or usage from anything else
// on the same Cloud project. Stopping at 90% absorbs that drift.
const DefaultSafetyMargin = 0.9

// DefaultUnmeteredCap bounds APIs that do not bill per call. The Meta Ad
// Library is free but rate-limited; this stops a runaway loop from earning a
// throttle.
const DefaultUnmeteredCap = 5_000

// ErrCeilingReached stops a run before it spends money. It is deliberately not
// wrapped as a transient failure: retrying is exactly the wrong response.
var ErrCeilingReached = errors.New("monthly call ceiling reached")

// CeilingError carries enough detail to act on without opening the console.
type CeilingError struct {
	SKU     SKU
	Month   string
	Used    int
	Ceiling int
}

func (e *CeilingError) Error() string {
	return fmt.Sprintf(
		"%s: %d of %d calls used in %s — stopping to stay inside the free tier "+
			"(raise PROSPECT_PLACES_MONTHLY_MAX and set PROSPECT_ALLOW_PAID_APIS=true to go past it)",
		e.SKU, e.Used, e.Ceiling, e.Month)
}

func (e *CeilingError) Is(target error) bool { return target == ErrCeilingReached }

// Counter is the persistence the ledger needs. Implemented by store.DB.
type Counter interface {
	// BillableCalls counts calls already charged for a SKU in a month.
	// Cache hits are excluded: they never reached the network.
	BillableCalls(ctx context.Context, sku SKU, month string) (int, error)

	// RecordCall appends one call, billable or served from cache.
	RecordCall(ctx context.Context, sku SKU, endpoint string, billable, cached bool, runID *int64) error
}

// Limits resolves a ceiling per SKU.
type Limits struct {
	// AllowPaid is the master switch. While false — the default — no ceiling
	// may exceed the free allowance, whatever else is configured. Spending
	// money requires an explicit opt-in, not a large number in a config file.
	AllowPaid bool

	// SafetyMargin scales the free allowance, 0 < margin <= 1.
	SafetyMargin float64

	// Overrides sets an absolute ceiling for a SKU by name. Ignored where it
	// would exceed the free allowance and AllowPaid is false.
	Overrides map[string]int
}

// DefaultLimits stays inside the free tier.
func DefaultLimits() Limits {
	return Limits{AllowPaid: false, SafetyMargin: DefaultSafetyMargin}
}

// Ceiling is the maximum billable calls allowed for a SKU this month.
func (l Limits) Ceiling(sku SKU) int {
	margin := l.SafetyMargin
	if margin <= 0 || margin > 1 {
		margin = DefaultSafetyMargin
	}

	// Unmetered APIs cost nothing, so the cap is about politeness and the
	// paid switch is irrelevant.
	if sku.Tier.FreeMonthly == 0 {
		if v, ok := l.Overrides[sku.Name]; ok && v > 0 {
			return v
		}
		return DefaultUnmeteredCap
	}

	free := int(float64(sku.Tier.FreeMonthly) * margin)
	if v, ok := l.Overrides[sku.Name]; ok && v > 0 {
		if !l.AllowPaid && v > free {
			return free // clamped: see AllowPaid
		}
		return v
	}
	return free
}

// Clamped reports whether an override was reduced to stay free, so the caller
// can say so once at startup rather than silently ignoring configuration.
func (l Limits) Clamped(sku SKU) (int, bool) {
	v, ok := l.Overrides[sku.Name]
	if !ok || v <= 0 || l.AllowPaid || sku.Tier.FreeMonthly == 0 {
		return 0, false
	}
	if free := l.Ceiling(sku); v > free {
		return v, true
	}
	return 0, false
}

// Ledger enforces the ceiling against recorded usage.
type Ledger struct {
	counter Counter
	limits  Limits
	log     *slog.Logger
	now     func() time.Time // injectable for tests
}

func NewLedger(counter Counter, limits Limits, log *slog.Logger) *Ledger {
	return &Ledger{counter: counter, limits: limits, log: log, now: time.Now}
}

// Month is the bucket usage is counted in, matching the calendar month
// Google resets on.
func Month(t time.Time) string { return t.UTC().Format("2006-01") }

// CurrentMonth is the bucket for now.
func (l *Ledger) CurrentMonth() string { return Month(l.now()) }

// Check refuses a call that would exceed the ceiling. Call it before spending,
// then Record after the response lands.
//
// Separate from Record because a call can fail after being charged, and
// because --dry-run needs the check without the spend.
func (l *Ledger) Check(ctx context.Context, sku SKU, want int) error {
	if want <= 0 {
		return nil
	}
	month := l.CurrentMonth()
	used, err := l.counter.BillableCalls(ctx, sku, month)
	if err != nil {
		return fmt.Errorf("read usage for %s: %w", sku, err)
	}
	ceiling := l.limits.Ceiling(sku)

	if used+want > ceiling {
		return &CeilingError{SKU: sku, Month: month, Used: used, Ceiling: ceiling}
	}
	if remaining := ceiling - used - want; remaining <= ceiling/10 {
		l.log.Warn("approaching monthly call ceiling",
			"sku", sku.String(), "used", used, "ceiling", ceiling, "remaining_after", remaining)
	}
	return nil
}

// Record appends a call. Cached responses are recorded as non-billable so
// `prospect quota` can show what the cache saved.
func (l *Ledger) Record(ctx context.Context, sku SKU, endpoint string, cached bool, runID *int64) error {
	return l.counter.RecordCall(ctx, sku, endpoint, !cached, cached, runID)
}

// Usage is one SKU's position against its ceiling, for `prospect quota`.
type Usage struct {
	SKU         SKU
	Month       string
	Billable    int
	Cached      int
	Ceiling     int
	FreeMonthly int
}

// Remaining is how many more calls are allowed, never negative.
func (u Usage) Remaining() int {
	if r := u.Ceiling - u.Billable; r > 0 {
		return r
	}
	return 0
}

// PercentUsed is progress toward the ceiling, for the quota report.
func (u Usage) PercentUsed() float64 {
	if u.Ceiling <= 0 {
		return 0
	}
	return float64(u.Billable) / float64(u.Ceiling) * 100
}

// Limits exposes the resolved limits, so callers can report clamping.
func (l *Ledger) Limits() Limits { return l.limits }
