package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/store"
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
			id, err := parseBusinessID(args[0])
			if err != nil {
				return err
			}
			spec, err := model.LookupSignalType(sigType)
			if err != nil {
				return err
			}
			if confidence < 0 || confidence > 1 {
				return fmt.Errorf("--confidence must be between 0 and 1, got %v", confidence)
			}
			return runSignal(cmd, e, id, spec, value, note, confidence)
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

// runSignal records a hand-observed fact. Manual entries land in the same
// table as automated ones and carry the same weight; only the source differs.
func runSignal(cmd *cobra.Command, e *env, businessID int64, spec model.SignalSpec, value, note string, confidence float64) error {
	ctx := cmd.Context()

	b, err := e.db.BusinessByID(ctx, businessID)
	if err != nil {
		return err
	}

	if err := validateSignalValue(spec, value); err != nil {
		return err
	}

	signal := model.Signal{
		BusinessID: businessID,
		Source:     model.SourceManual,
		Type:       spec.Type,
		Value:      strings.TrimSpace(value),
		Note:       note,
		Confidence: confidence,
	}

	outcome, err := e.db.RecordSignal(ctx, &signal, nil)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	switch outcome {
	case store.SignalTransition:
		// The transition is the interesting part: a business that just started
		// running ads is the moment worth acting on.
		fmt.Fprintf(out, "Recorded %s=%s for %s (#%d) — this CHANGED from the previous value.\n",
			spec.Type, signal.Value, b.Name, businessID)
		fmt.Fprintln(out, "Re-run 'prospect score' to pick up the new value.")
	case store.SignalConfirmed:
		fmt.Fprintf(out, "Confirmed %s=%s for %s (#%d); it was already on file.\n",
			spec.Type, signal.Value, b.Name, businessID)
	default:
		fmt.Fprintf(out, "Recorded %s=%s for %s (#%d).\n", spec.Type, signal.Value, b.Name, businessID)
		fmt.Fprintln(out, "Re-run 'prospect score' to pick it up.")
	}
	return nil
}

// validateSignalValue rejects values the scoring engine cannot read. Catching
// "yes" instead of "true" at entry beats discovering it as a silently missing
// 40-point signal later.
func validateSignalValue(spec model.SignalSpec, value string) error {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return fmt.Errorf("--value must not be empty")
	}

	switch spec.Type {
	case model.TypeRunningAds, model.TypeHiringLeadRole, model.TypeContactForm, model.TypeChatWidget:
		if v != "true" && v != "false" {
			return fmt.Errorf("--value for %s must be true or false, got %q", spec.Type, value)
		}
	case model.TypeSizeBand:
		switch v {
		case model.SizeMicro, model.SizeSMB, model.SizeMid, model.SizeLarge:
		default:
			return fmt.Errorf("--value for %s must be one of micro|smb|mid|large, got %q", spec.Type, value)
		}
	case model.TypeResponseTimeHours:
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("--value for %s must be a positive number of hours, got %q", spec.Type, value)
		}
	case model.TypeContactEmail:
		if !strings.Contains(v, "@") {
			return fmt.Errorf("--value for %s must be an email address, got %q", spec.Type, value)
		}
	}
	return nil
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
