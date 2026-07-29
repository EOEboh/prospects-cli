package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func newExportCmd(e *env) *cobra.Command {
	var (
		format   string
		minScore int
		status   string
		out      string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export prospects to CSV or JSON",
		Long: `Export the ranked prospect list, including each score's explanation so the
reasoning travels with the row.

Suppressed businesses are excluded, in exports as everywhere else.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "csv" && format != "json" {
				return fmt.Errorf("--format: want csv or json, got %q", format)
			}
			if status != "" {
				if _, err := model.LookupOutreachStatus(status); err != nil {
					return err
				}
			}
			return notImplemented("export", 5)
		},
	}

	f := cmd.Flags()
	f.StringVar(&format, "format", "csv", "output format: csv|json")
	f.IntVar(&minScore, "min-score", 0, "only prospects scoring at least this (0-100)")
	f.StringVar(&status, "status", "", "filter by outreach status: "+joinStatuses())
	f.StringVar(&out, "out", "", "output file (default stdout)")

	return cmd
}
