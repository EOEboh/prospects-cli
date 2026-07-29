package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/quota"
)

func newQuotaCmd(e *env) *cobra.Command {
	var month string

	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Billable API calls used this month against the local ceiling",
		Long: `Report billable calls made this month per SKU, against the ceiling enforced
locally before every call.

By default the tool never spends money: each ceiling is that SKU's free monthly
allowance, scaled by PROSPECT_QUOTA_SAFETY_MARGIN (0.9) to absorb drift between
this counter and the provider's. Going past the free tier takes an explicit
PROSPECT_ALLOW_PAID_APIS=true — a large PROSPECT_PLACES_MONTHLY_MAX on its own
is clamped, not honored.

Google's free allowances are per SKU per calendar month, and the SKU is decided
by the field mask: the highest tier among the fields requested bills the whole
call. discover needs places.websiteUri, which is an Enterprise field, so
discovery is billed at Text Search Enterprise and its free allowance is the
smallest of the three.

Cache hits are reported separately. They never reached the network and were
never billed.

Set per-API quotas in the Cloud Console as well. Budget alerts notify after the
fact and do not stop usage; this ceiling and a console quota do.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if month == "" {
				month = quota.Month(time.Now())
			} else if _, err := time.Parse("2006-01", month); err != nil {
				return fmt.Errorf("--month: want YYYY-MM, got %q", month)
			}

			limits := e.cfg.QuotaLimits()
			usage, err := e.db.UsageForMonth(cmd.Context(), limits, month)
			if err != nil {
				return err
			}
			return printQuota(cmd, usage, limits, month)
		},
	}

	cmd.Flags().StringVar(&month, "month", "", "month to report as YYYY-MM (default current)")

	return cmd
}

func printQuota(cmd *cobra.Command, usage []quota.Usage, limits quota.Limits, month string) error {
	out := cmd.OutOrStdout()

	mode := "free tier only"
	if limits.AllowPaid {
		mode = "PAID CALLS ENABLED"
	}
	fmt.Fprintf(out, "Billable API usage for %s (%s)\n\n", month, mode)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SKU\tTIER\tUSED\tCEILING\tFREE/MO\tREMAINING\tCACHED")

	var anyUsage bool
	for _, u := range usage {
		if u.Billable > 0 || u.Cached > 0 {
			anyUsage = true
		}
		free := "unmetered"
		if u.FreeMonthly > 0 {
			free = fmt.Sprintf("%d", u.FreeMonthly)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%d\t%d\n",
			u.SKU.Name, u.SKU.Tier.Name, u.Billable, u.Ceiling, free, u.Remaining(), u.Cached)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if !anyUsage {
		fmt.Fprintf(out, "\nNo API calls recorded in %s.\n", month)
	}

	// Say once, clearly, when configuration was overruled — a silently ignored
	// setting is worse than a rejected one. Once, though: the same clamp
	// repeated per SKU buries the message it is trying to deliver.
	var clampedSKUs []string
	configured := 0
	for _, u := range usage {
		if want, clamped := limits.Clamped(u.SKU); clamped {
			clampedSKUs = append(clampedSKUs, u.SKU.Name)
			configured = want
		}
	}
	if len(clampedSKUs) > 0 {
		fmt.Fprintf(out,
			"\nnote: PROSPECT_PLACES_MONTHLY_MAX=%d exceeds the free allowance for %d SKU(s):\n"+
				"      %s\n"+
				"      Each is capped at its own free tier instead. Set PROSPECT_ALLOW_PAID_APIS=true\n"+
				"      to allow paid calls.\n",
			configured, len(clampedSKUs), strings.Join(clampedSKUs, ", "))
	}
	return nil
}
