package cli

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/httpx"
	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/quota"
	"github.com/EOEboh/prospects-cli/internal/source/metaads"
	"github.com/EOEboh/prospects-cli/internal/source/website"
)

func newEnrichCmd(e *env) *cobra.Command {
	var (
		businessID int64
		allPending bool
		force      bool
		workers    int
		limit      int
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
  * At most one request per second per host, and a site asking for a longer
    Crawl-delay gets it. Every request has a timeout and a size cap.
  * Contact forms are detected, never submitted. Bulk submission is spam.

Only publicly listed business contact details are collected.

At most three pages are fetched per business: the homepage plus up to two
contact pages it links to. Responses are cached, so re-running is nearly free
and costs the sites nothing.

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
			if enableMeta && !e.cfg.MetaAdsAvailable() {
				return fmt.Errorf("--with-meta-ads needs META_ADS_ACCESS_TOKEN and PROSPECT_ENABLE_META_ADS=true;\n" +
					"leave it off and record ad status by hand instead: prospect signal <id> --type running_ads --value true")
			}
			if workers <= 0 {
				workers = e.cfg.Workers
			}
			return runEnrich(cmd, e, enrichOptions{
				businessID: businessID,
				force:      force,
				workers:    workers,
				limit:      limit,
				resume:     resume,
				metaAds:    enableMeta,
			})
		},
	}

	f := cmd.Flags()
	f.Int64Var(&businessID, "business-id", 0, "enrich a single business")
	f.BoolVar(&allPending, "all-pending", false, "enrich every business not yet enriched")
	f.BoolVar(&force, "force", false, "re-enrich even if signals already exist")
	f.IntVar(&workers, "workers", 0, "concurrent workers (default PROSPECT_WORKERS)")
	f.IntVar(&limit, "limit", 0, "stop after this many businesses (0 = no limit)")
	f.BoolVar(&resume, "resume", true, "skip businesses already completed in the last incomplete run")
	f.BoolVar(&enableMeta, "with-meta-ads", false, "also query the Meta Ad Library API (optional, coverage is unreliable)")

	return cmd
}

type enrichOptions struct {
	businessID int64
	force      bool
	workers    int
	limit      int
	resume     bool
	metaAds    bool
}

type enrichStats struct {
	Considered  int `json:"considered"`
	Enriched    int `json:"enriched"`
	Skipped     int `json:"skipped"`
	Failed      int `json:"failed"`
	Disallowed  int `json:"disallowed"`
	Signals     int `json:"signals"`
	Transitions int `json:"transitions"`
}

func runEnrich(cmd *cobra.Command, e *env, opts enrichOptions) error {
	ctx := cmd.Context()

	targets, err := enrichTargets(ctx, e, opts)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Nothing to enrich.")
		return nil
	}

	cache, err := httpx.OpenCache(ctx, e.cfg.CacheDBPath, e.cfg.CacheTTL)
	if err != nil {
		return err
	}
	defer cache.Close()

	client, err := httpx.New(httpx.Config{
		UserAgent:   e.cfg.UserAgent(),
		Timeout:     e.cfg.HTTPTimeout,
		RatePerHost: e.cfg.RatePerHost,
		Cache:       cache,
		Logger:      e.log,
	})
	if err != nil {
		return err
	}

	runID, err := e.db.StartRun(ctx, "enrich", map[string]any{
		"business_id": opts.businessID,
		"force":       opts.force,
		"workers":     opts.workers,
	})
	if err != nil {
		return err
	}

	// Resume against the previous incomplete run, so a run that died partway
	// does not re-fetch what it already finished.
	done := map[string]bool{}
	if opts.resume {
		if prev, err := e.db.LastIncompleteRun(ctx, "enrich"); err != nil {
			e.log.Warn("could not read the previous run; starting fresh", "error", err)
		} else if prev != nil && prev.ID != runID {
			if done, err = e.db.CompletedItems(ctx, prev.ID); err != nil {
				return err
			}
			if len(done) > 0 {
				e.log.Info("resuming previous run", "run_id", prev.ID, "already_done", len(done))
			}
		}
	}

	// The Ad Library source is best-effort and additive: it is queried after
	// the website, and its failures never fail a business.
	var ads *metaads.Source
	if opts.metaAds {
		ads = metaads.New(metaads.Config{
			Client:    client,
			Ledger:    quota.NewLedger(e.db, e.cfg.QuotaLimits(), e.log),
			Token:     e.cfg.MetaAdsToken,
			Countries: e.cfg.MetaAdsCountries,
			Logger:    e.log,
			RunID:     &runID,
		})
		e.log.Info("meta ad library enabled",
			"countries", e.cfg.MetaAdsCountries,
			"note", "results are name-matched hints; confirm in the web UI")
	}

	stats := enrichWorkerPool(ctx, e, website.New(client, e.log), ads, targets, done, runID, opts.workers)

	if finishErr := e.db.FinishRun(ctx, runID, stats, ctx.Err()); finishErr != nil {
		e.log.Error("could not finalise run record", "run_id", runID, "error", finishErr)
	}

	printEnrichSummary(cmd, stats)
	return ctx.Err()
}

func enrichTargets(ctx context.Context, e *env, opts enrichOptions) ([]model.Business, error) {
	if opts.businessID != 0 {
		b, err := e.db.BusinessByID(ctx, opts.businessID)
		if err != nil {
			return nil, err
		}
		if b.Website == "" {
			return nil, fmt.Errorf("business %d has no website to fetch", opts.businessID)
		}
		return []model.Business{*b}, nil
	}
	return e.db.BusinessesToEnrich(ctx, opts.force, opts.limit)
}

// enrichWorkerPool runs a bounded number of workers over the target list.
//
// The pool is bounded so the tool never hammers anything, and per-host rate
// limiting inside the client means two workers landing on the same host still
// queue behind each other.
func enrichWorkerPool(
	ctx context.Context,
	e *env,
	src *website.Source,
	ads *metaads.Source,
	targets []model.Business,
	done map[string]bool,
	runID int64,
	workers int,
) enrichStats {
	var (
		mu    sync.Mutex
		stats = enrichStats{Considered: len(targets)}
		wg    sync.WaitGroup
	)

	jobs := make(chan model.Business)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				result := enrichOne(ctx, e, src, ads, b, runID)
				mu.Lock()
				stats.Enriched += result.enriched
				stats.Failed += result.failed
				stats.Disallowed += result.disallowed
				stats.Signals += result.signals
				stats.Transitions += result.transitions
				mu.Unlock()
			}
		}()
	}

	for _, b := range targets {
		key := "business:" + strconv.FormatInt(b.ID, 10)
		if done[key] {
			mu.Lock()
			stats.Skipped++
			mu.Unlock()
			continue
		}
		select {
		case <-ctx.Done():
			// Ctrl-C stops between businesses with checkpoints intact.
			close(jobs)
			wg.Wait()
			return stats
		case jobs <- b:
		}
	}
	close(jobs)
	wg.Wait()
	return stats
}

type enrichResult struct {
	enriched, failed, disallowed, signals, transitions int
}

// enrichOne processes a single business. An error here is logged against that
// business and never propagated: one bad site does not fail a run.
func enrichOne(ctx context.Context, e *env, src *website.Source, ads *metaads.Source, b model.Business, runID int64) enrichResult {
	key := "business:" + strconv.FormatInt(b.ID, 10)

	signals, err := src.Collect(ctx, &b)
	if err != nil {
		e.log.Error("enrichment failed",
			"business_id", b.ID, "name", b.Name, "website", b.Website, "error", err)
		if markErr := e.db.MarkRunItem(ctx, runID, key, model.ItemFailed, err); markErr != nil {
			e.log.Error("could not checkpoint failure", "business_id", b.ID, "error", markErr)
		}
		return enrichResult{failed: 1}
	}

	// The Ad Library is queried only after the website succeeded, and its
	// failures are logged rather than propagated: an optional source with
	// unreliable coverage must never cost a business its enrichment.
	if ads != nil && ads.Enabled() {
		adSignals, adErr := ads.Collect(ctx, &b)
		if adErr != nil {
			e.log.Warn("ad library lookup failed; record ad status by hand instead",
				"business_id", b.ID, "name", b.Name, "error", adErr)
		} else {
			signals = append(signals, adSignals...)
		}
	}

	transitions, err := e.db.RecordSignals(ctx, signals, &runID)
	if err != nil {
		e.log.Error("could not record signals", "business_id", b.ID, "error", err)
		if markErr := e.db.MarkRunItem(ctx, runID, key, model.ItemFailed, err); markErr != nil {
			e.log.Error("could not checkpoint failure", "business_id", b.ID, "error", markErr)
		}
		return enrichResult{failed: 1}
	}

	result := enrichResult{enriched: 1, signals: len(signals), transitions: transitions}
	for _, s := range signals {
		switch s.Type {
		case model.TypeRobotsDisallowed:
			result.disallowed = 1
			result.enriched = 0
		case model.TypeContactEmail:
			// Promote the address onto the business so brief and export can
			// show it without joining signals.
			if err := e.db.UpdateBusinessEmail(ctx, b.ID, s.Value); err != nil {
				e.log.Warn("could not store contact email", "business_id", b.ID, "error", err)
			}
		}
	}

	if err := e.db.MarkRunItem(ctx, runID, key, model.ItemDone, nil); err != nil {
		e.log.Error("could not checkpoint completion", "business_id", b.ID, "error", err)
	}
	return result
}

func printEnrichSummary(cmd *cobra.Command, stats enrichStats) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Considered %d business(es):\n", stats.Considered)
	fmt.Fprintf(out, "  %d enriched (%d signals recorded)\n", stats.Enriched, stats.Signals)
	if stats.Transitions > 0 {
		fmt.Fprintf(out, "  %d signal(s) changed since last time\n", stats.Transitions)
	}
	if stats.Skipped > 0 {
		fmt.Fprintf(out, "  %d skipped (already done in the resumed run)\n", stats.Skipped)
	}
	if stats.Disallowed > 0 {
		fmt.Fprintf(out, "  %d skipped (robots.txt)\n", stats.Disallowed)
	}
	if stats.Failed > 0 {
		fmt.Fprintf(out, "  %d failed (see the log)\n", stats.Failed)
	}
}
