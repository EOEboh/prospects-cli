package cli

import (
	"github.com/spf13/cobra"
)

func newScoreCmd(e *env) *cobra.Command {
	var (
		weightsPath string
		businessID  int64
		explain     bool
	)

	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score every business from its current signals",
		Long: `Compute a prospect score for each business from the signals recorded against
it, writing the score, its component breakdown and a plain-English explanation.

Weights come from a YAML file (default ./weights.yaml, see weights.example.yaml)
rather than from constants, so they can be tuned as you learn what converts.

The reported score is normalized to 0..100 against the sum of positive weights
in the active config. The raw weighted sum and that maximum are both stored, so
retuning the weights does not silently change what '--min-score 60' selects.

Scores are computable from partial data: a business with only website signals
still gets a score, marked low-confidence. A business whose ad status has never
been recorded is flagged as needing a manual check.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return notImplemented("score", 4)
		},
	}

	f := cmd.Flags()
	f.StringVar(&weightsPath, "config", "", "weights YAML file (default PROSPECT_WEIGHTS_PATH)")
	f.Int64Var(&businessID, "business-id", 0, "score a single business instead of all")
	f.BoolVar(&explain, "explain", false, "print each component's contribution to stdout")

	return cmd
}
