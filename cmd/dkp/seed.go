package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/seed"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// `dkp seed` — generate a synthetic guild into a development database. Phase 1, issue #190.
//
// Wiring only, per the repo map: the profiles and the generator live in internal/seed, and this file
// resolves flags, opens the store and prints a summary. It is what `make seed` runs.
//
// IT IS A DEVELOPMENT COMMAND THAT SHIPS IN THE OPERATOR'S BINARY, which deserves a sentence rather
// than a shrug. Two things make that acceptable: it refuses to write into a pool that already has
// batches (seed.ErrPoolNotEmpty), so it cannot append synthetic history to a guild's real ledger by
// accident; and everything it writes goes through the ordinary commit path, so what it produces is
// ordinary, reversible, auditable history rather than a special category of row. What it is NOT is
// undoable — the ledger is append-only — so the refusal is the whole safety story and the help text
// says so.

// profileNames are the profiles `--profile` accepts. Only one of them is implemented; the other two
// are named here so the error a developer gets says which phase implements them, rather than
// "unknown profile".
const (
	profilePerf  = "perf"
	profileSmall = "small"
	profileDemo  = "demo"
)

// newSeedCmd builds `dkp seed`.
func newSeedCmd() *cobra.Command {
	var (
		profileName string
		raids       int
		verbose     bool
	)

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Generate a synthetic guild into a development database",
		Long: "Generate a synthetic guild into a development database.\n\n" +
			"The perf profile is a realistic-guild-scale ledger — 280 accounts, 3,400 raids and\n" +
			"about 520,000 entries — written through the real ledger commit path. It takes a\n" +
			"couple of minutes and produces a database of a few hundred megabytes.\n\n" +
			"It REFUSES to write into a pool that already has batches. The ledger is append-only,\n" +
			"so seeded history cannot be removed afterwards; delete the database file and start\n" +
			"again.\n\n" +
			"DKP_DB_PATH selects the database. Run `dkp migrate` first.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := resolveProfile(profileName, raids)
			if err != nil {
				return err
			}

			dbPath := os.Getenv(dbPathEnv)
			if dbPath == "" {
				return fmt.Errorf("%s is not set", dbPathEnv)
			}

			// The per-batch "committed ledger batch" line is right for a raid night and wrong for
			// twenty thousand batches in a row, so the default is quieter and --verbose restores it.
			// Set before the store is opened, so nothing slips out at the old level.
			//
			// This is the only place in the binary that installs a handler, and it is the wiring
			// layer, which is where a logger is supposed to be configured. Progress does NOT go
			// through it — it is a callback printed to stdout below — so quietening the log cannot
			// leave a two-minute command looking hung.
			slog.SetDefault(slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
				Level: seedLogLevel(verbose),
			})))

			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("open %s: %w", dbPath, err)
			}
			defer func() {
				if closeErr := s.Close(); closeErr != nil {
					slog.Error("close the database", "path", dbPath, "error", closeErr)
				}
			}()

			progress := func(done, total int) {
				// Deliberately discarded: a failed progress write is not a reason to abandon a
				// two-minute generation, and there is nowhere to report it that is any more likely
				// to work than the stream that just failed.
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %d/%d batches\n", done, total)
			}

			report, err := seed.Generate(cmd.Context(), s, clock.System{}, profile, progress)
			if err != nil {
				return err
			}

			if _, err := fmt.Fprintf(cmd.OutOrStdout(),
				"seeded %s: %d accounts, %d batches, %d entries, head seq %d\n",
				report.Profile.Name, report.Accounts, report.Batches, report.Entries, report.HeadSeq,
			); err != nil {
				return fmt.Errorf("write summary: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", profilePerf,
		"which dataset to generate (perf; small and demo arrive in Phase 2 and Phase 3)")
	cmd.Flags().IntVar(&raids, "raids", 0,
		"override the profile's raid count, for a smaller dataset (0 keeps the profile's own)")
	cmd.Flags().BoolVar(&verbose, "verbose", false,
		"log every committed batch, not just progress")

	return cmd
}

// resolveProfile turns the flags into a profile, or says which phase implements the one that was
// asked for.
//
// An unimplemented profile is an ERROR rather than a fallback to perf. A developer who typed
// `--profile demo` and silently got a 520,000-entry performance dataset would have to work out for
// themselves why their dev guild had 3,400 raids in it.
func resolveProfile(name string, raids int) (seed.Profile, error) {
	switch name {
	case profilePerf:
		p := seed.Perf()
		if raids > 0 {
			p = p.Scaled(raids)
		}

		return p, nil

	case profileSmall:
		return seed.Profile{}, fmt.Errorf(
			"the %q profile arrives with the roster in Phase 2; only %q exists today", profileSmall, profilePerf)

	case profileDemo:
		return seed.Profile{}, fmt.Errorf(
			"the %q profile arrives with the demo deploy in Phase 3; only %q exists today", profileDemo, profilePerf)

	default:
		return seed.Profile{}, fmt.Errorf("unknown profile %q; known profiles are %s, %s and %s",
			name, profilePerf, profileSmall, profileDemo)
	}
}

// seedLogLevel is INFO with --verbose and WARN without it.
func seedLogLevel(verbose bool) slog.Level {
	if verbose {
		return slog.LevelInfo
	}

	return slog.LevelWarn
}
