package cli

import (
	"github.com/spf13/cobra"
)

func newDiscoverCmd(e *env) *cobra.Command {
	var (
		niche    string
		location string
		limit    int
		dryRun   bool
		resume   bool
	)

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Find businesses via the Google Places API (requires GOOGLE_PLACES_API_KEY)",
		Long: `Search Google Places for businesses in a niche and location.

Billing discipline is enforced, not advisory:

  * Every request sends a FieldMask requesting the minimum useful set of
    fields. Billing is at the highest SKU among the fields requested, and
    places.websiteUri — the dedup key, and what enrich fetches — is an
    Enterprise field. Discovery is therefore billed at Text Search Enterprise,
    whose free allowance is 1,000 calls per month, the smallest of the three
    tiers. Rating and review count sit in that same tier, so the size heuristic
    rides along at no extra cost and is always requested.
  * Responses are cached to disk (default TTL 7 days). A rerun does not
    re-fetch what was pulled yesterday.
  * Every billable call is counted locally and checked against the monthly
    ceiling before it is made. By default that ceiling is the free allowance,
    so this command cannot spend money until PROSPECT_ALLOW_PAID_APIS=true.
    Google's budget alerts notify but do not stop usage, so the stop lives
    here. Run 'prospect quota' to see where you stand.

Without GOOGLE_PLACES_API_KEY this command reports the source as disabled and
exits cleanly. Use 'prospect seed' instead.

Runs are resumable: pages already retrieved are served from cache rather than
re-requested. Note that Places pagination tokens are short-lived, so resuming
past the cached pages after a long gap costs a fresh call.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return notImplemented("discover", 6)
		},
	}

	f := cmd.Flags()
	f.StringVar(&niche, "niche", "", `business type to search for, e.g. "recruiting agency" (required)`)
	f.StringVar(&location, "location", "", `where to search, e.g. "Austin, TX" (required)`)
	f.IntVar(&limit, "limit", 50, "maximum businesses to return")
	f.BoolVar(&dryRun, "dry-run", false, "report the worst-case billable call count without making any calls")
	f.BoolVar(&resume, "resume", true, "continue the most recent incomplete run for this query")
	_ = cmd.MarkFlagRequired("niche")
	_ = cmd.MarkFlagRequired("location")

	return cmd
}
