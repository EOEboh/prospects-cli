package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/model"
)

func newStatusCmd(e *env) *cobra.Command {
	var (
		set  string
		note string
	)

	cmd := &cobra.Command{
		Use:   "status <business-id>",
		Short: "Show or update a prospect's outreach status",
		Long: `Without --set, print the current status and the history behind it.

With --set, move the prospect to a new status. Transitions are appended to a
history table rather than overwriting, so the sequence of touches stays
reconstructable: "emailed twice, no reply" is a different situation from
"emailed once last week".

Statuses: ` + joinStatuses(),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseBusinessID(args[0])
			if err != nil {
				return err
			}
			if set == "" {
				return showStatus(cmd, e, id)
			}
			status, err := model.LookupOutreachStatus(set)
			if err != nil {
				return err
			}
			return setStatus(cmd, e, id, status, note)
		},
	}

	f := cmd.Flags()
	f.StringVar(&set, "set", "", "new status: "+joinStatuses())
	f.StringVar(&note, "note", "", "free-text note recorded with the transition")

	return cmd
}

func showStatus(cmd *cobra.Command, e *env, id int64) error {
	ctx := cmd.Context()

	b, err := e.db.AnyBusinessByID(ctx, id)
	if err != nil {
		return err
	}
	current, err := e.db.OutreachFor(ctx, id)
	if err != nil {
		return err
	}
	history, err := e.db.OutreachHistory(ctx, id)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s (#%d)\n", b.Name, b.ID)
	fmt.Fprintf(out, "  status: %s\n", current.Status)
	if current.Notes != "" {
		fmt.Fprintf(out, "  note:   %s\n", current.Notes)
	}
	if b.Email != "" {
		fmt.Fprintf(out, "  email:  %s\n", b.Email)
	}

	if suppressed, reason, err := e.db.IsSuppressed(ctx, id); err != nil {
		return err
	} else if suppressed {
		fmt.Fprintf(out, "\n  SUPPRESSED: %s\n", reason)
		fmt.Fprintln(out, "  This business is excluded from every list, export and brief.")
	}

	if len(history) == 0 {
		fmt.Fprintln(out, "\nNo outreach recorded yet.")
		return nil
	}

	fmt.Fprintln(out, "\nHistory:")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, ev := range history {
		from := string(ev.From)
		if from == "" {
			from = "-"
		}
		fmt.Fprintf(w, "  %s\t%s → %s\t%s\n",
			ev.At.Format("2006-01-02 15:04"), from, ev.To, ev.Note)
	}
	return w.Flush()
}

func setStatus(cmd *cobra.Command, e *env, id int64, status model.OutreachStatus, note string) error {
	ctx := cmd.Context()

	b, err := e.db.AnyBusinessByID(ctx, id)
	if err != nil {
		return err
	}

	// Moving a suppressed business back into the pipeline is almost certainly
	// a mistake, so it is refused rather than quietly allowed.
	if suppressed, reason, err := e.db.IsSuppressed(ctx, id); err != nil {
		return err
	} else if suppressed && status != model.StatusDead {
		return fmt.Errorf("business %d is suppressed (%s); it cannot be moved back into the pipeline", id, reason)
	}

	from, err := e.db.SetOutreachStatus(ctx, id, status, note)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if from == status {
		fmt.Fprintf(out, "%s (#%d) was already %s.\n", b.Name, id, status)
		return nil
	}
	fmt.Fprintf(out, "%s (#%d): %s → %s\n", b.Name, id, from, status)
	return nil
}
