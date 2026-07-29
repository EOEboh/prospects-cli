package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func newSignalCmd(e *env) *cobra.Command {
	var (
		sigType    string
		value      string
		note       string
		confidence float64
	)

	cmd := &cobra.Command{
		Use:   "signal <business-id>",
		Short: "Record a signal observed by hand",
		Long: `Record a fact gathered manually. Hand-entered signals land in the same table
as automated ones and carry the same weight in scoring — the source is
recorded, but nothing downstream treats it differently.

This is the primary path for the ad signal. The Meta Ad Library API's coverage
of non-EU commercial ads is unreliable, so the intended workflow is to check
the Ad Library web UI for the prospects at the top of your list and record what
you find:

  prospect signal 42 --type running_ads --value true --note "checked ad library"

'prospect brief' flags which prospects still need that check.

` + signalTypeHelp(),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := parseBusinessID(args[0]); err != nil {
				return err
			}
			if _, err := model.LookupSignalType(sigType); err != nil {
				return err
			}
			if confidence < 0 || confidence > 1 {
				return fmt.Errorf("--confidence must be between 0 and 1, got %v", confidence)
			}
			return notImplemented("signal", 3)
		},
	}

	f := cmd.Flags()
	f.StringVar(&sigType, "type", "", "signal type (required); see the list below")
	f.StringVar(&value, "value", "", "signal value (required)")
	f.StringVar(&note, "note", "", "free-text note recorded with the signal")
	f.Float64Var(&confidence, "confidence", 1.0, "confidence 0..1; hand-checked facts default to 1.0")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("value")

	return cmd
}

// signalTypeHelp renders the vocabulary from the registry, so help text cannot
// drift from what the scoring engine actually accepts.
func signalTypeHelp() string {
	var b strings.Builder
	b.WriteString("Signal types:\n")
	for _, s := range model.SignalSpecs() {
		marker := " "
		if s.Manual {
			marker = "*" // usually entered by hand
		}
		fmt.Fprintf(&b, "  %s %-20s %s\n", marker, s.Type, s.Desc)
	}
	b.WriteString("\n  (*) typically recorded manually")
	return b.String()
}
