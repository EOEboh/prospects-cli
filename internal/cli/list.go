package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/store"
)

func newListCmd(e *env) *cobra.Command {
	var (
		minScore int
		maxScore int
		status   string
		limit    int
		needsAds bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List scored prospects, highest first",
		Long: `List prospects ranked by score.

Only businesses that have been scored appear; run 'prospect score' first.
Suppressed businesses are excluded structurally — this command reads a view
that already filters them, so the exclusion cannot be forgotten.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter := store.ListFilter{
				MinScore:     minScore,
				MaxScore:     maxScore,
				NeedsAdCheck: needsAds,
				Limit:        limit,
			}
			if status != "" {
				parsed, err := model.LookupOutreachStatus(status)
				if err != nil {
					return err
				}
				filter.Status = parsed
			}
			if minScore < 0 || minScore > 100 || maxScore < 0 || maxScore > 100 {
				return fmt.Errorf("--min-score and --max-score must be between 0 and 100")
			}

			rows, err := e.db.ListScored(cmd.Context(), filter)
			if err != nil {
				return err
			}
			printList(cmd, rows, filter)
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&minScore, "min-score", 0, "only prospects scoring at least this (0-100)")
	f.IntVar(&maxScore, "max-score", 100, "only prospects scoring at most this (0-100)")
	f.StringVar(&status, "status", "", "filter by outreach status: "+joinStatuses())
	f.IntVar(&limit, "limit", 20, "maximum rows to print")
	f.BoolVar(&needsAds, "needs-ad-check", false, "only prospects whose ad status has never been recorded")

	return cmd
}

func printList(cmd *cobra.Command, rows []store.ScoredBusiness, filter store.ListFilter) {
	out := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintln(out, noMatchesHint(filter))
		return
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSCORE\tNAME\tSTATUS\tEMAIL\tFLAGS")
	for _, r := range rows {
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\n",
			r.Business.ID,
			r.Score.Score,
			truncate(r.Business.Name, 34),
			r.Status,
			orDash(r.Business.Email),
			listFlags(r),
		)
	}
	_ = w.Flush()

	fmt.Fprintf(out, "\n%d prospect(s).\n", len(rows))
}

// listFlags marks what still needs attention, so a listing doubles as a
// worklist rather than only a ranking.
func listFlags(r store.ScoredBusiness) string {
	var flags []string
	if r.Score.NeedsManualCheck {
		flags = append(flags, "ad?")
	}
	if r.Score.Confidence < 0.7 {
		flags = append(flags, "low-conf")
	}
	if r.Business.Email == "" {
		flags = append(flags, "no-email")
	}
	if len(flags) == 0 {
		return "-"
	}
	return strings.Join(flags, " ")
}

// noMatchesHint explains an empty result in terms of the likely cause, since
// "no rows" on its own does not say whether to widen the filter or run
// something first.
func noMatchesHint(filter store.ListFilter) string {
	switch {
	case filter.NeedsAdCheck:
		return "No prospects are waiting on an ad check."
	case filter.Status != "":
		return fmt.Sprintf("No prospects with status %q.", filter.Status)
	case filter.MinScore > 0:
		return fmt.Sprintf("No prospects scoring %d or above. Try a lower --min-score, or enrich and score more businesses.", filter.MinScore)
	default:
		return "No scored prospects yet. Run 'prospect score' after seeding and enriching."
	}
}

func joinStatuses() string {
	return strings.Join(model.OutreachStatusNames(), "|")
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
