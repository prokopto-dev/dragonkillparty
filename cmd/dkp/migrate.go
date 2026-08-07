package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/db"
	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

const (
	// dataDirEnv is the directory holding the database, backups and artifacts.
	dataDirEnv = "DKP_DATA_DIR"

	// autoMigrateEnv gates migrate-on-boot. Default true; "false" is the only value that disables
	// it, matching docs/operations/configuration.md.
	autoMigrateEnv = "DKP_AUTO_MIGRATE"
)

// resolveDataDir picks the directory that holds backups/.
//
// DKP_DATA_DIR wins. With it unset the fallback is the directory containing the database file,
// which equals the documented /data default in the container (where DKP_DB_PATH is /data/dkp.db)
// and is strictly better for a binary install: an operator who put their database in
// /var/lib/dkp gets /var/lib/dkp/backups rather than pre-migration snapshots landing in a
// container-shaped /data that may not exist and may not be writable — at the one moment a snapshot
// is the only thing standing between them and losing the database.
func resolveDataDir(dbPath string) string {
	if dir := os.Getenv(dataDirEnv); dir != "" {
		return dir
	}

	if dbPath == "" {
		return ""
	}

	return filepath.Dir(dbPath)
}

// autoMigrateEnabled reports DKP_AUTO_MIGRATE.
//
// Default TRUE, and only the exact string "false" turns it off. The default is the important half:
// docs/design/06-cicd-and-release.md:505 argues it at length — a required manual `dkp migrate`
// guarantees that some fraction of volunteer operators pull, restart, and get a container that
// refuses to start with an error they cannot act on at 1 a.m. after a raid. The risk of automatic
// migration is answered by making it safe (snapshot, per-migration integrity check, auto-restore),
// not by making it manual.
func autoMigrateEnabled() bool { return os.Getenv(autoMigrateEnv) != "false" }

// newMigrator builds the boot-time migrator from the environment.
func newMigrator(dbPath string, autoMigrate bool) (*migrate.Runner, error) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}

	return migrate.New(fsys, migrate.Config{
		DBPath:        dbPath,
		DataDir:       resolveDataDir(dbPath),
		BinaryVersion: version,
		AutoMigrate:   autoMigrate,
	})
}

// newMigrateCmd builds `dkp migrate`.
//
// The manual counterpart to migrate-on-boot, for operators running with DKP_AUTO_MIGRATE=false and
// for the command /readyz names in its 503 body. It always migrates, regardless of that variable —
// running it IS the explicit consent the variable withholds.
func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Long: "Apply pending database migrations.\n\n" +
			"A snapshot is taken before anything is applied, each migration is followed by\n" +
			"PRAGMA integrity_check, and any failure restores the snapshot and exits non-zero.\n\n" +
			"DKP_DB_PATH selects the database. DKP_DATA_DIR holds backups/.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dbPath := os.Getenv(dbPathEnv)
			if dbPath == "" {
				return fmt.Errorf("%s is not set", dbPathEnv)
			}

			// AutoMigrate is forced on: the operator typed the command.
			runner, err := newMigrator(dbPath, true)
			if err != nil {
				return err
			}

			if err := runner.Migrate(cmd.Context()); err != nil {
				return err
			}

			status, err := runner.Status(cmd.Context())
			if err != nil {
				return err
			}

			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "schema version: %d\n", status.Applied); err != nil {
				return fmt.Errorf("write status: %w", err)
			}

			return nil
		},
	}
}

// migrateOnBoot runs the upgrade before the listener opens, and decides whether a failure is fatal.
//
// The split is the whole point and it is not symmetric:
//
//   - A FAILED MIGRATION or a DOWNGRADE is fatal. The database has either been restored to its
//     pre-upgrade state or never touched, and in both cases continuing would mean serving from a
//     database this binary has already concluded it must not write to.
//
//   - ANY OTHER ERROR — the file is unreadable, its directory does not exist, the path is nonsense
//     — is logged and the server starts anyway. That is canonical §13 and it is asserted by
//     TestServe_UnreadableDatabasePath_HealthzStillReturns200: /healthz must answer 200 so that
//     Docker's HEALTHCHECK does not kill the container, while /readyz reports the failure so a load
//     balancer takes it out of rotation and an operator can reach the process to see why.
//
// Returning an error from here aborts the boot; returning nil serves.
func migrateOnBoot(ctx context.Context, runner *migrate.Runner) error {
	err := runner.Migrate(ctx)
	if err == nil {
		return nil
	}

	var failed *migrate.FailedError
	if errors.As(err, &failed) {
		return err
	}

	if errors.Is(err, migrate.ErrSchemaAhead) {
		return err
	}

	// Logged at ERROR, never discarded. The process is about to serve traffic in a state where it
	// cannot reach its database, and this line plus the /readyz body are the only two places that
	// say so.
	slog.ErrorContext(ctx, "could not prepare the database; serving anyway so /healthz stays up",
		"error", err, "readyz", "will report failed")

	return nil
}
