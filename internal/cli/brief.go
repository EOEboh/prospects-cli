package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/scoring"
	"github.com/EOEboh/prospects-cli/internal/store"
)

// briefWidth keeps wrapped prose readable in a standard terminal.
const briefWidth = 76

func newBriefCmd(e *env) *cobra.Command {
	var (
		limit    int
		minScore int
		all      bool
	)

	cmd := &cobra.Command{
		Use:   "brief",
		Short: "The morning read: top uncontacted prospects with their reasoning",
		Long: `Print the top uncontacted prospects formatted for a terminal, one block each:

  * name and score
  * the plain-English explanation of what produced that score
  * the public contact email
  * whether a manual ad check is still pending, with the command to record it
  * a suggested opening observation for a cold email, drawn from the
    highest-scoring signal

The ad check matters: the Meta Ad Library web UI is the reliable way to confirm
whether a business is advertising, and that signal is worth more than every
other one combined. The prospects flagged here are the ones where five minutes
of checking could move the score most.

Suppressed businesses never appear.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := scoring.Load(e.cfg.WeightsPath)
			if err != nil {
				return err
			}

			filter := store.ListFilter{
				MinScore:        minScore,
				Limit:           limit,
				UncontactedOnly: !all,
			}
			rows, err := e.db.ListScored(cmd.Context(), filter)
			if err != nil {
				return err
			}
			printBrief(cmd.OutOrStdout(), rows, cfg, all)
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", 10, "how many prospects to include")
	f.IntVar(&minScore, "min-score", 0, "only prospects scoring at least this (0-100)")
	f.BoolVar(&all, "all", false, "include prospects you have already contacted")

	return cmd
}

func printBrief(out io.Writer, rows []store.ScoredBusiness, cfg *scoring.Config, all bool) {
	if len(rows) == 0 {
		if all {
			fmt.Fprintln(out, "No scored prospects yet. Run 'prospect score' after seeding and enriching.")
		} else {
			fmt.Fprintln(out, "Nothing uncontacted to work through. Try --all, or seed and enrich more businesses.")
		}
		return
	}

	pending := 0
	for _, r := range rows {
		if r.Score.NeedsManualCheck {
			pending++
		}
	}

	scope := "uncontacted"
	if all {
		scope = "scored"
	}
	fmt.Fprintf(out, "Morning brief — top %d %s prospect(s)", len(rows), scope)
	if pending > 0 {
		fmt.Fprintf(out, ", %d still need an ad check", pending)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, strings.Repeat("─", briefWidth))

	for i, r := range rows {
		printBriefEntry(out, i+1, r, cfg)
	}

	if pending > 0 {
		fmt.Fprintf(out, "\nCheck the %d flagged prospect(s) at https://www.facebook.com/ads/library/\n", pending)
		fmt.Fprintln(out, "then re-run 'prospect score' to fold the answers in.")
	}
}

func printBriefEntry(out io.Writer, n int, r store.ScoredBusiness, cfg *scoring.Config) {
	b := r.Business

	fmt.Fprintf(out, "\n%2d. %-40s %3d/100\n", n, truncate(b.Name, 40), r.Score.Score)

	contact := []string{}
	if b.Email != "" {
		contact = append(contact, b.Email)
	}
	if b.Phone != "" {
		contact = append(contact, b.Phone)
	}
	if b.Website != "" {
		contact = append(contact, b.Website)
	}
	if len(contact) > 0 {
		fmt.Fprintf(out, "    %s\n", strings.Join(contact, "  ·  "))
	} else {
		fmt.Fprintln(out, "    (no contact details yet — enrich this business)")
	}

	if r.Score.Explanation != "" {
		fmt.Fprintln(out)
		writeWrapped(out, "    Why:  ", r.Score.Explanation)
	}

	if observation := cfg.ObservationFor(r.Score.Breakdown); observation != "" {
		fmt.Fprintln(out)
		writeWrapped(out, "    Open: ", observation)
	}

	if r.Score.NeedsManualCheck {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "    ! Ad check pending — the strongest signal is still unknown.")
		fmt.Fprintf(out, "      prospect signal %d --type running_ads --value true|false\n", b.ID)
	}
	fmt.Fprintln(out, strings.Repeat("─", briefWidth))
}

// writeWrapped prints a label followed by prose wrapped to the terminal width,
// with continuation lines aligned under the text rather than the label.
func writeWrapped(out io.Writer, label, text string) {
	indent := strings.Repeat(" ", len(label))
	width := briefWidth - len(label)
	if width < 20 {
		width = 20
	}

	for i, line := range wrapText(text, width) {
		if i == 0 {
			fmt.Fprintf(out, "%s%s\n", label, line)
			continue
		}
		fmt.Fprintf(out, "%s%s\n", indent, line)
	}
}

func wrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var (
		lines []string
		line  strings.Builder
	)
	for _, word := range words {
		if line.Len() > 0 && line.Len()+1+len(word) > width {
			lines = append(lines, line.String())
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(word)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}
