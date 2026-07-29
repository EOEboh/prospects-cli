package cli

import (
	"github.com/spf13/cobra"
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

Expected columns (header row required, order irrelevant, extras ignored):

  name      required
  website   optional but strongly recommended — it is the dedup key
  city      optional — used as the fallback dedup key when website is absent
  email, phone, address, region, country   optional

Rows are deduplicated against existing businesses on normalized website
domain, falling back to normalized name plus city.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return notImplemented("seed", 2)
		},
	}

	cmd.Flags().StringVar(&csvPath, "csv", "", "path to the CSV file (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse and report what would be imported without writing")
	_ = cmd.MarkFlagRequired("csv")

	return cmd
}
