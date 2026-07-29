package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
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

Suppressed businesses are excluded structurally: this command reads a view
that already filters them, so the exclusion cannot be forgotten.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if status != "" {
				if _, err := model.LookupOutreachStatus(status); err != nil {
					return err
				}
			}
			if minScore < 0 || minScore > 100 || maxScore < 0 || maxScore > 100 {
				return fmt.Errorf("--min-score and --max-score must be between 0 and 100")
			}
			return notImplemented("list", 5)
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

func joinStatuses() string {
	return strings.Join(model.OutreachStatusNames(), "|")
}
