package cli

import (
	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func newStatusCmd(e *env) *cobra.Command {
	var (
		set  string
		note string
	)

	cmd := &cobra.Command{
		Use:   "status <business-id>",
		Short: "Show or update a prospect's outreach status",
		Long: `Without --set, print the current status and its history.

With --set, move the prospect to a new status. Transitions are appended to a
history table rather than overwriting, so the sequence of touches stays
reconstructable.

Statuses: ` + joinStatuses(),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := parseBusinessID(args[0]); err != nil {
				return err
			}
			if set != "" {
				if _, err := model.LookupOutreachStatus(set); err != nil {
					return err
				}
			}
			return notImplemented("status", 5)
		},
	}

	f := cmd.Flags()
	f.StringVar(&set, "set", "", "new status: "+joinStatuses())
	f.StringVar(&note, "note", "", "free-text note recorded with the transition")

	return cmd
}
