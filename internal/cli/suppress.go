package cli

import (
	"github.com/spf13/cobra"
)

func newSuppressCmd(e *env) *cobra.Command {
	var (
		reason     string
		domainOnly bool
	)

	cmd := &cobra.Command{
		Use:   "suppress <business-id>",
		Short: "Permanently exclude a business from every future run",
		Long: `Add a business to the suppression list. Once suppressed, always excluded:
list, export and brief all read a view that filters suppressed businesses, so
no future command can accidentally include them.

The business's domain is suppressed alongside its id. Without that, re-running
discover would recreate the business under a fresh id and it would reappear in
tomorrow's brief.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := parseBusinessID(args[0]); err != nil {
				return err
			}
			return notImplemented("suppress", 5)
		},
	}

	f := cmd.Flags()
	f.StringVar(&reason, "reason", "", "why this business was suppressed (required)")
	f.BoolVar(&domainOnly, "id-only", false, "suppress only this row, not the domain (rarely what you want)")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}
