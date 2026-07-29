package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newEnrichCmd(e *env) *cobra.Command {
	var (
		businessID int64
		allPending bool
		force      bool
		workers    int
		resume     bool
		enableMeta bool
	)

	cmd := &cobra.Command{
		Use:   "enrich",
		Short: "Fetch business websites and extract lead-handling signals",
		Long: `Fetch each business's homepage and obvious contact pages, then record what
they reveal about how the business handles inbound leads:

  * a publicly listed contact email
  * a contact form, and the endpoint its action points at
  * a live chat widget
  * CRM and marketing tags in the page source (HubSpot, Calendly, Intercom,
    Mailchimp, Typeform, Salesforce and similar)
  * a stated response-time promise in the page text

Crawling rules, enforced in code rather than left to discipline:

  * robots.txt is honored on every fetch. A disallowed page is skipped and the
    reason recorded as a signal, so "not fetched" stays distinguishable from
    "nothing found".
  * The User-Agent names the tool and PROSPECT_USER_AGENT_EMAIL, which is
    required — there is no anonymous fallback.
  * At most one request per second per host, with a timeout on every call.
  * Contact forms are detected, never submitted. Bulk submission is spam.

Only publicly listed business contact details are collected.

Runs are resumable: each business is checkpointed as it completes, so a run
that dies at business 34 of 50 picks up at 34.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (businessID != 0) == allPending {
				return fmt.Errorf("--business-id and --all-pending: %w (pass exactly one)", errExclusiveFlags)
			}
			// Fetching identifies itself truthfully or not at all.
			if err := e.cfg.RequireFetchable(); err != nil {
				return err
			}
			return notImplemented("enrich", 3)
		},
	}

	f := cmd.Flags()
	f.Int64Var(&businessID, "business-id", 0, "enrich a single business")
	f.BoolVar(&allPending, "all-pending", false, "enrich every business not yet enriched")
	f.BoolVar(&force, "force", false, "re-enrich even if signals already exist (ignores the HTTP cache)")
	f.IntVar(&workers, "workers", 0, "concurrent workers (default PROSPECT_WORKERS)")
	f.BoolVar(&resume, "resume", true, "skip businesses already completed in the last incomplete run")
	f.BoolVar(&enableMeta, "with-meta-ads", false, "also query the Meta Ad Library API (optional, coverage is unreliable)")

	return cmd
}
