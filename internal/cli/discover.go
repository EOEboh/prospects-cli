package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/quota"
	"github.com/EOEboh/prospects-cli/internal/source/places"
	"github.com/EOEboh/prospects-cli/internal/store"
)

func newDiscoverCmd(e *env) *cobra.Command {
	var (
		niche    string
		location string
		limit    int
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Find businesses via the Google Places API (requires GOOGLE_PLACES_API_KEY)",
		Long: `Search Google Places for businesses in a niche and location, and create
business rows for them. Run 'prospect enrich' afterwards to gather signals.

Billing discipline is enforced, not advisory:

  * Every request sends a FieldMask requesting the minimum useful set of
    fields. Billing is at the highest SKU among the fields requested, and
    places.websiteUri — the dedup key, and what enrich fetches — is an
    Enterprise field. Discovery is therefore billed at Text Search Enterprise,
    whose free allowance is 1,000 calls per month, the smallest of the three
    tiers. Rating, review count and address components sit at or below that
    tier, so they ride along at no extra cost.
  * Responses are cached (default TTL 7 days). A rerun does not re-fetch what
    was pulled yesterday, and cached pages are neither counted nor billed.
  * The monthly ceiling is checked immediately before each live call and hard
    stops the run. By default that ceiling is the free allowance, so this
    command cannot spend money until PROSPECT_ALLOW_PAID_APIS=true. Google's
    budget alerts notify but do not stop usage, so the stop lives here.

Each call returns up to 20 results, so --limit 50 costs at most 3 calls.
Run 'prospect quota' to see where you stand.

Without GOOGLE_PLACES_API_KEY this command reports the source as disabled and
exits cleanly. Use 'prospect seed --csv' instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return fmt.Errorf("--limit must be positive, got %d", limit)
			}
			return runDiscover(cmd, e, niche, location, limit, dryRun)
		},
	}

	f := cmd.Flags()
	f.StringVar(&niche, "niche", "", `business type to search for, e.g. "recruiting agency" (required)`)
	f.StringVar(&location, "location", "", `where to search, e.g. "Austin, TX" (required)`)
	f.IntVar(&limit, "limit", 50, "maximum businesses to return")
	f.BoolVar(&dryRun, "dry-run", false, "report the worst-case billable call count without making any calls")
	_ = cmd.MarkFlagRequired("niche")
	_ = cmd.MarkFlagRequired("location")

	return cmd
}

type discoverStats struct {
	Found      int `json:"found"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	Suppressed int `json:"suppressed"`
	Failed     int `json:"failed"`
}

func runDiscover(cmd *cobra.Command, e *env, niche, location string, limit int, dryRun bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	// A missing key is a configuration state, not a failure. Exit cleanly and
	// point at the path that needs no key.
	if !e.cfg.PlacesEnabled() {
		e.log.Warn("google places is disabled: GOOGLE_PLACES_API_KEY is not set")
		fmt.Fprintln(out, "Google Places is not configured, so there is nothing to discover.")
		fmt.Fprintln(out, "Set GOOGLE_PLACES_API_KEY to enable it, or use 'prospect seed --csv' which needs no key.")
		return nil
	}

	cache, err := httpx.OpenCache(ctx, e.cfg.CacheDBPath, e.cfg.CacheTTL)
	if err != nil {
		return err
	}
	defer cache.Close()

	client, err := httpx.New(httpx.Config{
		UserAgent:   e.cfg.UserAgent(),
		Timeout:     e.cfg.HTTPTimeout,
		RatePerHost: e.cfg.RatePerHost,
		Cache:       cache,
		Logger:      e.log,
	})
	if err != nil {
		return err
	}

	limits := e.cfg.QuotaLimits()
	ledger := quota.NewLedger(e.db, limits, e.log)
	src := places.New(places.Config{
		Client: client, Ledger: ledger, APIKey: e.cfg.PlacesAPIKey, Logger: e.log,
	})

	if dryRun {
		return reportDiscoverDryRun(cmd, e, src, limits, niche, location, limit)
	}

	runID, err := e.db.StartRun(ctx, "discover", map[string]any{
		"niche": niche, "location": location, "limit": limit,
	})
	if err != nil {
		return err
	}
	// The run id ties each billable call to the invocation that made it.
	src = places.New(places.Config{
		Client: client, Ledger: ledger, APIKey: e.cfg.PlacesAPIKey, Logger: e.log, RunID: &runID,
	})

	businesses, discoverErr := src.Discover(ctx, niche, location, limit)
	stats := discoverStats{Found: len(businesses)}

	// Results gathered before an error are real and worth keeping, so they are
	// stored before the error is surfaced.
	stats, storeErr := storeDiscovered(ctx, e, businesses, runID, stats)

	err = errors.Join(discoverErr, storeErr)
	if finishErr := e.db.FinishRun(ctx, runID, stats, err); finishErr != nil {
		e.log.Error("could not finalise run record", "run_id", runID, "error", finishErr)
	}

	printDiscoverSummary(cmd, stats)

	if err != nil {
		// main prints the error itself, so add only what it cannot know: where
		// to look next.
		var ceiling *quota.CeilingError
		if errors.As(err, &ceiling) {
			fmt.Fprintln(out, "\nRun 'prospect quota' for the full picture.")
		}
		return err
	}
	return nil
}

func storeDiscovered(ctx context.Context, e *env, businesses []model.Business, runID int64, stats discoverStats) (discoverStats, error) {
	for i := range businesses {
		b := businesses[i]

		id, result, err := e.db.UpsertBusiness(ctx, &b)
		if err != nil {
			// One unusable result never discards the rest of a paid page.
			e.log.Error("skipping discovered business",
				"name", b.Name, "website", b.Website, "error", err)
			stats.Failed++
			continue
		}

		switch result {
		case store.Inserted:
			stats.Inserted++
		case store.Updated:
			stats.Updated++
		case store.Unchanged:
			stats.Unchanged++
		case store.Suppressed:
			stats.Suppressed++
			e.log.Info("skipping suppressed domain", "name", b.Name, "website", b.Website)
		}

		key := "business:" + strconv.FormatInt(id, 10)
		if err := e.db.MarkRunItem(ctx, runID, key, model.ItemDone, nil); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// reportDiscoverDryRun says what a run would cost, and where the ceiling
// currently sits, without making a call.
func reportDiscoverDryRun(cmd *cobra.Command, e *env, src *places.Source, limits quota.Limits, niche, location string, limit int) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	sku, culprit, err := src.SKU()
	if err != nil {
		return err
	}
	calls := src.EstimateCalls(limit)

	month := quota.Month(store.Now())
	used, err := e.db.BillableCalls(ctx, sku, month)
	if err != nil {
		return err
	}
	ceiling := limits.Ceiling(sku)

	fmt.Fprintf(out, "Dry run: %q\n\n", places.Query(niche, location))
	fmt.Fprintf(out, "  billed as     %s (%s tier)\n", sku.Name, sku.Tier.Name)
	if culprit != "" {
		fmt.Fprintf(out, "  tier set by   %s\n", culprit)
	}
	fmt.Fprintf(out, "  worst case    %d billable call(s) for up to %d result(s)\n", calls, limit)
	fmt.Fprintf(out, "  used in %s  %d of %d\n", month, used, ceiling)
	fmt.Fprintf(out, "  remaining     %d\n", max(ceiling-used, 0))

	fmt.Fprintln(out)
	switch {
	case used+calls > ceiling:
		fmt.Fprintf(out, "This would exceed the ceiling and be refused after %d call(s).\n", max(ceiling-used, 0))
		if !limits.AllowPaid {
			fmt.Fprintln(out, "The ceiling is the free monthly allowance. Raising it needs PROSPECT_ALLOW_PAID_APIS=true.")
		}
	default:
		fmt.Fprintln(out, "This fits inside the ceiling.")
	}
	fmt.Fprintln(out, "\nWorst case, not exact: cached pages cost nothing, and a query with few")
	fmt.Fprintln(out, "results stops paging early. No calls were made.")
	return nil
}

func printDiscoverSummary(cmd *cobra.Command, stats discoverStats) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Found %d business(es):\n", stats.Found)
	fmt.Fprintf(out, "  %d new\n", stats.Inserted)
	fmt.Fprintf(out, "  %d updated\n", stats.Updated)
	fmt.Fprintf(out, "  %d already up to date\n", stats.Unchanged)
	if stats.Suppressed > 0 {
		fmt.Fprintf(out, "  %d skipped (suppressed)\n", stats.Suppressed)
	}
	if stats.Failed > 0 {
		fmt.Fprintf(out, "  %d skipped (unusable)\n", stats.Failed)
	}
	if stats.Inserted > 0 {
		fmt.Fprintln(out, "\nNext: 'prospect enrich --all-pending' to gather signals, then 'prospect score'.")
	}
}
