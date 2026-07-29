// Package source defines the boundary between where facts come from and what
// scoring does with them. The scoring engine never imports a concrete source,
// so adding or swapping one — CSV, website, Places, Ad Library — touches
// nothing downstream.
package source

import (
	"context"
	"log/slog"

	"github.com/EOEboh/prospects-cli/internal/model"
)

// Source observes public facts about a business.
//
// Implementations arrive with their phases: csvseed and website in phases 2-3
// with no API key required, places and metaads later behind key gates.
type Source interface {
	// Name identifies the source in logs and in signal rows.
	Name() model.SignalSource

	// Enabled reports whether the source can run. A missing API key returns
	// false; it is never an error and must never end a run.
	Enabled() bool

	// Collect observes one business. Returning an error fails this business
	// only — the caller logs and moves to the next.
	Collect(ctx context.Context, b *model.Business) ([]model.Signal, error)
}

// Discoverer finds businesses that are not in the database yet. Separate from
// Source because most sources enrich a known business rather than produce new
// ones, and only discovery spends money per query.
type Discoverer interface {
	Source

	// Discover returns businesses matching a niche and location. Limit is a
	// ceiling, not a target: fewer real results means fewer rows.
	Discover(ctx context.Context, niche, location string, limit int) ([]model.Business, error)

	// EstimateCalls reports the worst-case number of billable calls Discover
	// would make, for --dry-run. Worst case rather than exact: paged APIs only
	// reveal how many pages exist by being called.
	EstimateCalls(niche, location string, limit int) int
}

// Run collects from every enabled source, skipping the rest with a warning.
// One source failing on one business never stops the others.
func Run(ctx context.Context, log *slog.Logger, sources []Source, b *model.Business) []model.Signal {
	var out []model.Signal
	for _, s := range sources {
		if !s.Enabled() {
			log.Warn("source disabled, skipping", "source", s.Name(), "business_id", b.ID)
			continue
		}
		signals, err := s.Collect(ctx, b)
		if err != nil {
			log.Error("source failed for business",
				"source", s.Name(), "business_id", b.ID, "name", b.Name, "error", err)
			continue
		}
		out = append(out, signals...)
	}
	return out
}
