package cli

import (
	"github.com/spf13/cobra"
)

func newBriefCmd(e *env) *cobra.Command {
	var (
		limit    int
		minScore int
	)

	cmd := &cobra.Command{
		Use:   "brief",
		Short: "The morning read: top uncontacted prospects with their reasoning",
		Long: `Print the top uncontacted prospects formatted for a terminal, one block each:

  * name and score
  * the plain-English explanation of what produced that score
  * the public contact email
  * whether a manual ad check is still pending — the Ad Library web UI is the
    reliable way to confirm ads, so this flags who to check next
  * a suggested opening observation for a cold email, drawn from the
    highest-contributing signal

Suppressed businesses never appear.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return notImplemented("brief", 5)
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", 10, "how many prospects to include")
	f.IntVar(&minScore, "min-score", 0, "only prospects scoring at least this (0-100)")

	return cmd
}
