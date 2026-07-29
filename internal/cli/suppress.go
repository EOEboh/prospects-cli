package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSuppressCmd(e *env) *cobra.Command {
	var (
		reason string
		idOnly bool
	)

	cmd := &cobra.Command{
		Use:   "suppress <business-id>",
		Short: "Permanently exclude a business from every future run",
		Long: `Add a business to the suppression list. Once suppressed, always excluded:
list, export and brief all read a view that filters suppressed businesses, so
no future command can accidentally include them, and enrich will not fetch
their pages again.

The business's domain is suppressed alongside its id. Without that, re-running
discover or re-importing a CSV would recreate the business under a fresh id and
it would reappear in tomorrow's brief. Use --id-only to suppress just this row,
which is rarely what you want.

The outreach entry is closed at the same time, so the history records why this
prospect stopped rather than showing it simply going quiet.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseBusinessID(args[0])
			if err != nil {
				return err
			}

			b, err := e.db.AnyBusinessByID(cmd.Context(), id)
			if err != nil {
				return err
			}

			domain, err := e.db.Suppress(cmd.Context(), id, reason, idOnly)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Suppressed %s (#%d): %s\n", b.Name, id, reason)
			switch {
			case idOnly:
				fmt.Fprintln(out, "Only this row is suppressed. A re-import under a new id would reappear.")
			case domain != "":
				fmt.Fprintf(out, "The domain %s is suppressed too, so it will not be recreated.\n", domain)
			default:
				fmt.Fprintln(out, "This business has no domain, so only the row could be suppressed.")
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&reason, "reason", "", "why this business was suppressed (required)")
	f.BoolVar(&idOnly, "id-only", false, "suppress only this row, not the domain (rarely what you want)")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}
