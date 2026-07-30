package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/source/csvseed"
	"github.com/EOEboh/prospects-cli/internal/store"
)

func newSeedCmd(e *env) *cobra.Command {
	var (
		csvPath string
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Import businesses from a CSV file (no API key required)",
		Long: `Create business rows from a CSV of names and websites.

This is the zero-API path and a first-class source: it exists so the scoring
engine can be proven end to end before billing is enabled anywhere.

A header row is required; column order does not matter and unknown columns are
ignored. Header names are matched loosely, so "Business Name", "business_name"
and "name" are the same column.

  name      required (a row with only a website is accepted; the URL stands in)
  website   optional but strongly recommended — it is the dedup key
  city      optional — scopes the fallback dedup key when website is absent
  email, phone, address, region, country   optional

Rows are deduplicated against existing businesses on normalized website domain,
falling back to normalized name plus city. Re-importing an updated file merges
new details into the existing rows without overwriting anything with a blank.

Suppressed domains are never recreated. One malformed row is reported and
skipped, never fatal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSeed(cmd, e, csvPath, dryRun)
		},
	}

	cmd.Flags().StringVar(&csvPath, "csv", "", "path to the CSV file (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse and report what would be imported without writing")
	_ = cmd.MarkFlagRequired("csv")

	return cmd
}

// seedStats is persisted as the run's stats blob and printed as the summary.
type seedStats struct {
	Parsed     int `json:"parsed"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	Suppressed int `json:"suppressed"`
	Failed     int `json:"failed"`
	BlankRows  int `json:"blank_rows"`
}

func runSeed(cmd *cobra.Command, e *env, csvPath string, dryRun bool) error {
	ctx := cmd.Context()

	f, err := os.Open(csvPath)
	if err != nil {
		return fmt.Errorf("open CSV: %w", err)
	}
	defer f.Close()

	parsed, err := csvseed.Parse(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", csvPath, err)
	}

	stats := seedStats{Parsed: len(parsed.Businesses), BlankRows: parsed.Skipped}
	for _, rowErr := range parsed.Errors {
		e.log.Warn("skipping malformed row", "file", csvPath, "line", rowErr.Line, "error", rowErr.Err)
		stats.Failed++
	}

	if dryRun {
		return reportSeedDryRun(cmd, e, parsed, stats)
	}

	runID, err := e.db.StartRun(ctx, "seed", map[string]any{"csv": csvPath})
	if err != nil {
		return err
	}

	stats, err = importBusinesses(ctx, e, parsed.Businesses, runID, stats)
	if finishErr := e.db.FinishRun(ctx, runID, stats, err); finishErr != nil {
		e.log.Error("could not finalise run record", "run_id", runID, "error", finishErr)
	}
	if err != nil {
		return err
	}

	printSeedSummary(cmd, stats, false)
	return nil
}

func importBusinesses(ctx context.Context, e *env, businesses []model.Business, runID int64, stats seedStats) (seedStats, error) {
	for i := range businesses {
		b := businesses[i]
		key := fmt.Sprintf("row:%d", i)

		id, result, err := e.db.UpsertBusiness(ctx, &b)
		if err != nil {
			// One bad record never fails an import.
			e.log.Error("skipping business", "name", b.Name, "website", b.Website, "error", err)
			stats.Failed++
			if markErr := e.db.MarkRunItem(ctx, runID, key, model.ItemFailed, err); markErr != nil {
				return stats, markErr
			}
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

		e.log.Debug("imported business", "id", id, "name", b.Name, "result", result.String())
		if err := e.db.MarkRunItem(ctx, runID, key, model.ItemDone, nil); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// reportSeedDryRun resolves each record against the database without writing,
// so the operator sees what an import would do before it happens.
func reportSeedDryRun(cmd *cobra.Command, e *env, parsed *csvseed.Result, stats seedStats) error {
	total, err := e.db.CountBusinesses(cmd.Context())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Dry run: %d parsable row(s), %d malformed, %d blank.\n",
		stats.Parsed, stats.Failed, stats.BlankRows)
	fmt.Fprintf(out, "Database currently holds %d business(es).\n\n", total)

	for _, rowErr := range parsed.Errors {
		fmt.Fprintf(out, "  line %d: %v\n", rowErr.Line, rowErr.Err)
	}
	if len(parsed.Errors) > 0 {
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "Nothing was written. Re-run without --dry-run to import.")
	return nil
}

func printSeedSummary(cmd *cobra.Command, stats seedStats, dryRun bool) {
	out := cmd.OutOrStdout()
	prefix := "Imported"
	if dryRun {
		prefix = "Would import"
	}
	fmt.Fprintf(out, "%s from %d parsable row(s):\n", prefix, stats.Parsed)
	fmt.Fprintf(out, "  %d new\n", stats.Inserted)
	fmt.Fprintf(out, "  %d updated\n", stats.Updated)
	fmt.Fprintf(out, "  %d already up to date\n", stats.Unchanged)
	if stats.Suppressed > 0 {
		fmt.Fprintf(out, "  %d skipped (suppressed)\n", stats.Suppressed)
	}
	if stats.Failed > 0 {
		fmt.Fprintf(out, "  %d skipped (malformed)\n", stats.Failed)
	}
	if stats.BlankRows > 0 {
		fmt.Fprintf(out, "  %d blank rows ignored\n", stats.BlankRows)
	}
}
