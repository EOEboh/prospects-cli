package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
	"github.com/EOEboh/prospects-cli/internal/store"
)

func newExportCmd(e *env) *cobra.Command {
	var (
		format   string
		minScore int
		status   string
		out      string
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export prospects to CSV or JSON",
		Long: `Export the ranked prospect list.

Each row carries its score's explanation and component breakdown, so the
reasoning travels with the data rather than staying locked in the database.
A row you paste into a spreadsheet still tells you why it is there.

Suppressed businesses are excluded, in exports as everywhere else.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format = strings.ToLower(format)
			if format != "csv" && format != "json" {
				return fmt.Errorf("--format: want csv or json, got %q", format)
			}

			filter := store.ListFilter{MinScore: minScore, Limit: limit}
			if status != "" {
				parsed, err := model.LookupOutreachStatus(status)
				if err != nil {
					return err
				}
				filter.Status = parsed
			}

			rows, err := e.db.ListScored(cmd.Context(), filter)
			if err != nil {
				return err
			}

			w, closeOut, err := exportWriter(cmd, out)
			if err != nil {
				return err
			}
			defer closeOut()

			if format == "json" {
				err = writeJSON(w, rows)
			} else {
				err = writeCSV(w, rows)
			}
			if err != nil {
				return err
			}

			if out != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Exported %d prospect(s) to %s.\n", len(rows), out)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&format, "format", "csv", "output format: csv|json")
	f.IntVar(&minScore, "min-score", 0, "only prospects scoring at least this (0-100)")
	f.StringVar(&status, "status", "", "filter by outreach status: "+joinStatuses())
	f.StringVar(&out, "out", "", "output file (default stdout)")
	f.IntVar(&limit, "limit", 0, "maximum rows (0 = all)")

	return cmd
}

func exportWriter(cmd *cobra.Command, path string) (io.Writer, func(), error) {
	if path == "" {
		return cmd.OutOrStdout(), func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", path, err)
	}
	return f, func() { f.Close() }, nil
}

// exportColumns is the CSV header. The explanation sits near the front because
// it is the reason to open the file at all.
var exportColumns = []string{
	"id", "name", "score", "explanation", "email", "website", "phone",
	"city", "region", "country", "status", "confidence", "needs_ad_check",
	"raw_score", "max_possible", "top_signals",
}

func writeCSV(w io.Writer, rows []store.ScoredBusiness) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportColumns); err != nil {
		return err
	}

	for _, r := range rows {
		record := []string{
			strconv.FormatInt(r.Business.ID, 10),
			r.Business.Name,
			strconv.Itoa(r.Score.Score),
			r.Score.Explanation,
			r.Business.Email,
			r.Business.Website,
			r.Business.Phone,
			r.Business.City,
			r.Business.Region,
			r.Business.Country,
			string(r.Status),
			strconv.FormatFloat(r.Score.Confidence, 'f', 2, 64),
			strconv.FormatBool(r.Score.NeedsManualCheck),
			strconv.Itoa(r.Score.Raw),
			strconv.Itoa(r.Score.MaxPossible),
			summarizeComponents(r.Score.Breakdown),
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}

	cw.Flush()
	return cw.Error()
}

// summarizeComponents flattens the breakdown into one cell, so a spreadsheet
// still shows what drove the number.
func summarizeComponents(components []model.Component) string {
	parts := make([]string, 0, len(components))
	for _, c := range components {
		parts = append(parts, fmt.Sprintf("%s %+d", c.Label, c.Points))
	}
	return strings.Join(parts, "; ")
}

// exportRow is the JSON shape, which keeps the breakdown structured rather
// than flattening it as CSV must.
type exportRow struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Score        int               `json:"score"`
	Explanation  string            `json:"explanation"`
	Email        string            `json:"email,omitempty"`
	Website      string            `json:"website,omitempty"`
	Phone        string            `json:"phone,omitempty"`
	City         string            `json:"city,omitempty"`
	Region       string            `json:"region,omitempty"`
	Country      string            `json:"country,omitempty"`
	Status       string            `json:"status"`
	Confidence   float64           `json:"confidence"`
	NeedsAdCheck bool              `json:"needs_ad_check"`
	RawScore     int               `json:"raw_score"`
	MaxPossible  int               `json:"max_possible"`
	Breakdown    []model.Component `json:"breakdown"`
}

func writeJSON(w io.Writer, rows []store.ScoredBusiness) error {
	out := make([]exportRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, exportRow{
			ID:           r.Business.ID,
			Name:         r.Business.Name,
			Score:        r.Score.Score,
			Explanation:  r.Score.Explanation,
			Email:        r.Business.Email,
			Website:      r.Business.Website,
			Phone:        r.Business.Phone,
			City:         r.Business.City,
			Region:       r.Business.Region,
			Country:      r.Business.Country,
			Status:       string(r.Status),
			Confidence:   r.Score.Confidence,
			NeedsAdCheck: r.Score.NeedsManualCheck,
			RawScore:     r.Score.Raw,
			MaxPossible:  r.Score.MaxPossible,
			Breakdown:    r.Score.Breakdown,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
