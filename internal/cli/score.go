package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/scoring"
)

func newScoreCmd(e *env) *cobra.Command {
	var (
		weightsPath string
		businessID  int64
		explain     bool
		printConfig bool
	)

	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score every business from its current signals",
		Long: `Compute a prospect score for each business from the signals recorded against
it, writing the score, its component breakdown and a plain-English explanation.

Weights come from a YAML file rather than from constants, so they can be tuned
as you learn what converts. Without a weights.yaml the built-in defaults are
used, so this works out of the box; run --print-config to see them or to start
your own file.

The reported score is normalized to 0..100 against the sum of positive weights
in the active config. The raw sum and that maximum are both stored, so retuning
does not silently change what '--min-score 60' selects.

Scores are computable from partial data. A rule that cannot be evaluated
contributes nothing and lowers confidence instead of blocking the score, and
the explanation says which signals were never checked. A business whose ad
status has never been recorded is flagged for a manual check.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printConfig {
				_, err := cmd.OutOrStdout().Write(scoring.DefaultYAML())
				return err
			}
			return runScore(cmd, e, weightsPath, businessID, explain)
		},
	}

	f := cmd.Flags()
	f.StringVar(&weightsPath, "config", "", "weights YAML file (default PROSPECT_WEIGHTS_PATH, then built-in)")
	f.Int64Var(&businessID, "business-id", 0, "score a single business instead of all")
	f.BoolVar(&explain, "explain", false, "print each business's breakdown as it is scored")
	f.BoolVar(&printConfig, "print-config", false, "print the built-in weights and exit (a starting point for weights.yaml)")

	return cmd
}

type scoreStats struct {
	Scored        int `json:"scored"`
	LowConfidence int `json:"low_confidence"`
	NeedsAdCheck  int `json:"needs_ad_check"`
	Zero          int `json:"zero"`
}

func runScore(cmd *cobra.Command, e *env, weightsPath string, businessID int64, explain bool) error {
	ctx := cmd.Context()

	if weightsPath == "" {
		weightsPath = e.cfg.WeightsPath
	}
	cfg, err := scoring.Load(weightsPath)
	if err != nil {
		return err
	}
	e.log.Info("scoring configuration loaded",
		"source", scoring.DescribeSource(weightsPath),
		"rules", len(cfg.Rules), "max_possible", cfg.MaxPossible(), "hash", cfg.Hash())

	businesses, err := e.db.BusinessesToScore(ctx, businessID)
	if err != nil {
		return err
	}
	if len(businesses) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No businesses to score.")
		return nil
	}

	runID, err := e.db.StartRun(ctx, "score", map[string]any{
		"weights": scoring.DescribeSource(weightsPath),
		"hash":    cfg.Hash(),
	})
	if err != nil {
		return err
	}

	stats, err := scoreAll(cmd, e, cfg, businesses, runID, explain)
	if finishErr := e.db.FinishRun(ctx, runID, stats, err); finishErr != nil {
		e.log.Error("could not finalise run record", "run_id", runID, "error", finishErr)
	}
	if err != nil {
		return err
	}

	printScoreSummary(cmd, stats)
	return nil
}

func scoreAll(cmd *cobra.Command, e *env, cfg *scoring.Config, businesses []model.Business, runID int64, explain bool) (scoreStats, error) {
	ctx := cmd.Context()
	var stats scoreStats

	for i := range businesses {
		b := businesses[i]

		signals, err := e.db.CurrentSignals(ctx, b.ID)
		if err != nil {
			return stats, err
		}

		res := cfg.Score(scoring.Input{Business: b, Signals: signals})

		score := &model.Score{
			BusinessID:       b.ID,
			RunID:            runID,
			Score:            res.Score,
			Raw:              res.Raw,
			MaxPossible:      res.MaxPossible,
			Confidence:       res.Confidence,
			NeedsManualCheck: res.NeedsManualCheck,
			Breakdown:        res.Components,
			Explanation:      res.Explanation,
			WeightsHash:      cfg.Hash(),
		}
		if err := e.db.SaveScore(ctx, score); err != nil {
			return stats, err
		}

		stats.Scored++
		if res.LowConfidence {
			stats.LowConfidence++
		}
		if res.NeedsManualCheck {
			stats.NeedsAdCheck++
		}
		if res.Score == 0 {
			stats.Zero++
		}

		if explain {
			printBreakdown(cmd, b, res)
		}
	}
	return stats, nil
}

func printBreakdown(cmd *cobra.Command, b model.Business, res scoring.Result) {
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "\n%s  —  %d/100", b.Name, res.Score)
	if res.LowConfidence {
		fmt.Fprintf(out, "  [low confidence: %.0f%%]", res.Confidence*100)
	}
	if res.NeedsManualCheck {
		fmt.Fprint(out, "  [needs ad check]")
	}
	fmt.Fprintln(out)

	if len(res.Components) == 0 {
		fmt.Fprintln(out, "  (no signals matched)")
	} else {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, c := range res.Components {
			evidence := c.Evidence
			if evidence != "" {
				evidence = "— " + evidence
			}
			fmt.Fprintf(w, "  %+d\t%s\t%s\n", c.Points, c.Label, evidence)
		}
		_ = w.Flush()
	}
	fmt.Fprintf(out, "  raw %d of %d\n", res.Raw, res.MaxPossible)
	fmt.Fprintf(out, "  %s\n", res.Explanation)
}

func printScoreSummary(cmd *cobra.Command, stats scoreStats) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\nScored %d business(es).\n", stats.Scored)
	if stats.LowConfidence > 0 {
		fmt.Fprintf(out, "  %d low-confidence (not enough signals checked yet)\n", stats.LowConfidence)
	}
	if stats.NeedsAdCheck > 0 {
		fmt.Fprintf(out, "  %d still need a manual ad check — run 'prospect brief' to see which\n", stats.NeedsAdCheck)
	}
	if stats.Zero > 0 {
		fmt.Fprintf(out, "  %d scored zero (try 'prospect enrich --all-pending' first)\n", stats.Zero)
	}
}
