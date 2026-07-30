// Package cli defines the command surface. Every command is declared here in
// full — flags, help text, validation — with the work itself arriving phase by
// phase, so the shape of the tool is reviewable before any of it runs.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/EOEboh/prospects-cli/internal/config"
	"github.com/EOEboh/prospects-cli/internal/logging"
	"github.com/EOEboh/prospects-cli/internal/store"
)

// env carries what every command needs. Built once in the root's
// PersistentPreRunE so each command body starts with a migrated database.
type env struct {
	cfg *config.Config
	db  *store.DB
	log *slog.Logger
}

// notImplemented marks a command whose wiring exists but whose body lands in a
// later phase. It fails loudly rather than silently succeeding.
func notImplemented(cmd string, phase int) error {
	return fmt.Errorf("%s: not implemented yet (phase %d)", cmd, phase)
}

// Execute builds the command tree and runs it. Returns an error rather than
// exiting so main owns the exit code.
func Execute(ctx context.Context, version string) error {
	return newRootCmd(version).ExecuteContext(ctx)
}

// newRootCmd assembles the command tree.
//
// Separate from Execute so tests can drive the real tree — flags, validation,
// output and all — against a temporary database, rather than testing the
// command bodies through a side door that production never uses.
func newRootCmd(version string) *cobra.Command {
	var (
		e          env
		dotenvPath string
		dbPath     string
		logLevel   string
		logFormat  string
	)

	root := &cobra.Command{
		Use:   "prospect",
		Short: "Discover, score and track lead-capture prospects",
		Long: `prospect turns a niche and a location into a scored, deduplicated list of
businesses to sell lead-capture automation to.

The score breakdown is the output that matters: every score carries the
reasoning behind it, which is what opens the cold email.

Works with zero API keys. Start with:

  prospect seed --csv businesses.csv
  prospect enrich --all-pending
  prospect score
  prospect brief

Paid sources (Google Places, Meta Ad Library) are an upgrade, not a
requirement, and stay disabled until their keys are set.`,
		Version:       version,
		SilenceUsage:  true, // a runtime failure is not a usage error
		SilenceErrors: true, // main prints it once
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// --help and --version need neither config nor a database.
			if cmd.Name() == "help" {
				return nil
			}

			// Flags override the environment for the three knobs worth
			// changing per invocation.
			overrideEnv("PROSPECT_DB_PATH", dbPath)
			overrideEnv("PROSPECT_LOG_LEVEL", logLevel)
			overrideEnv("PROSPECT_LOG_FORMAT", logFormat)

			cfg, err := config.Load(dotenvPath)
			if err != nil {
				return fmt.Errorf("configuration:\n%w", err)
			}
			e.cfg = cfg
			e.log = logging.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)
			e.log.Debug("configuration loaded", "config", cfg)

			db, err := store.Open(cmd.Context(), cfg.DBPath)
			if err != nil {
				return err
			}
			e.db = db

			if err := db.Migrate(cmd.Context(), e.log); err != nil {
				db.Close()
				return err
			}
			return nil
		},
		PersistentPostRun: func(*cobra.Command, []string) {
			if e.db != nil {
				e.db.Close()
			}
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&dotenvPath, "env-file", ".env", "path to a .env file (real env vars win)")
	pf.StringVar(&dbPath, "db", "", "SQLite database path (overrides PROSPECT_DB_PATH)")
	pf.StringVar(&logLevel, "log-level", "", "debug|info|warn|error (overrides PROSPECT_LOG_LEVEL)")
	pf.StringVar(&logFormat, "log-format", "", "text|json (overrides PROSPECT_LOG_FORMAT)")

	root.AddCommand(
		newSeedCmd(&e),
		newDiscoverCmd(&e),
		newEnrichCmd(&e),
		newSignalCmd(&e),
		newScoreCmd(&e),
		newListCmd(&e),
		newExportCmd(&e),
		newStatusCmd(&e),
		newSuppressCmd(&e),
		newBriefCmd(&e),
		newQuotaCmd(&e),
	)

	return root
}

// overrideEnv lets a flag win over the environment by writing it back before
// config.Load reads it, keeping one resolution path instead of two.
func overrideEnv(key, val string) {
	if val != "" {
		_ = os.Setenv(key, val)
	}
}

// parseBusinessID validates a positional business-id argument.
func parseBusinessID(arg string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(arg, "%d", &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("business-id: %q is not a positive integer", arg)
	}
	return id, nil
}

var errExclusiveFlags = errors.New("flags are mutually exclusive")
