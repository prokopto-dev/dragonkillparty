package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// The readiness half of the append-only check: #39 made the boot path refuse a migration that drops a
// trigger, and #59 is what happens afterwards to a database that ARRIVED without one. That database
// boots — deliberately, because refusing would close an officer's upgrade path over damage no version
// of the binary can undo — and before these tests the only trace was one error log during a restart
// nobody watched.
//
// migrate.Status is what /readyz reads on every probe, so Status carrying the answer is what turns a
// detection into a notification. These tests are about the state Status reports; cmd/dkp/ready_test.go
// asserts the HTTP behaviour that hangs off it.

// migratedRunner stands a fully-migrated database up at dbPath and returns a runner over the same
// (whole) migration set, which is the shape /readyz always sees: nothing pending, everything applied.
func migratedRunner(tb testing.TB, dataDir, dbPath string) *migrate.Runner {
	tb.Helper()

	runner, err := migrate.New(migrationDir(tb), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(tb, err)
	require.NoError(tb, runner.Migrate(tb.Context()))

	return runner
}

// TestStatus_FreshInstall_ReportsTheProtectionIntact is the control every test below depends on.
//
// Without it, a check that reported every database degraded would satisfy all three of them and would
// take every healthy instance out of its load balancer forever — which is a worse outage than the one
// the check exists to report.
func TestStatus_FreshInstall_ReportsTheProtectionIntact(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	status, err := migratedRunner(t, dataDir, dbPath).Status(t.Context())
	require.NoError(t, err)

	require.Equal(t, migrate.StateUpToDate, status.State)
	require.NoError(t, status.Protection.Err)
	require.False(t, status.Protection.Degraded(),
		"a fresh install reported its ledger unprotected: %s", status.Protection.Detail())
	require.Empty(t, status.Protection.MissingTriggers)
	require.Empty(t, status.Protection.MissingTables)
	require.Empty(t, status.Protection.Detail(),
		"an intact database must have nothing to say — Detail is what /readyz shows an operator")
}

// TestStatus_DegradedDatabase_ReportsTheMissingTriggers is the failure the whole mechanism exists for,
// reached the way a real one is: the trigger is gone and nothing else about the database is wrong.
//
// The two checks the boot path used to run still pass on this file, which is why the degraded state was
// invisible between restarts. Status has to disagree with a database that looks perfect.
func TestStatus_DegradedDatabase_ReportsTheMissingTriggers(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	tests := []struct {
		name string
		drop []string
		want []string
	}{
		{
			name: "one trigger, as a forgetful rebuild of one table leaves it",
			drop: []string{"DROP TRIGGER trg_ledger_entry_no_update"},
			want: []string{"trg_ledger_entry_no_update"},
		},
		{
			name: "a whole table's pair, which is what a DROP TABLE takes with it",
			drop: []string{"DROP TRIGGER trg_ledger_batch_no_update", "DROP TRIGGER trg_ledger_batch_no_delete"},
			want: []string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dataDir := t.TempDir()
			dbPath := filepath.Join(dataDir, "dkp.db")
			runner := migratedRunner(t, dataDir, dbPath)

			// Behind the boot path's back, the way a fork's build, a patched image or a support session
			// with a SQLite client reaches this state. goose's bookkeeping is untouched, so the database
			// still reports itself fully migrated.
			withRawFK(t, dbPath, func(handle *sql.DB) { applyStatements(t, handle, tc.drop) })

			status, err := runner.Status(t.Context())
			require.NoError(t, err)

			require.Equal(t, migrate.StateUpToDate, status.State,
				"dropping a trigger must not disturb the schema version — that is what makes this "+
					"damage invisible to every other check")
			require.True(t, status.Protection.Degraded(),
				"a database whose ledger history can be rewritten reported its protection intact")
			require.Equal(t, tc.want, status.Protection.MissingTriggers)
			require.Empty(t, status.Protection.MissingTables)

			for _, name := range tc.want {
				require.Contains(t, status.Protection.Detail(), name,
					"the detail an operator reads must name every missing trigger")
			}
		})
	}
}

// TestStatus_DroppedLedgerTable_ReportsTheMissingTable closes the way THROUGH a triggers-only check.
//
// Every trigger on a table that does not exist is vacuously present — the exemption that lets a fresh
// install apply migration 000001 without demanding triggers for tables 000003 creates. Drop the table
// on a fully-migrated database and a triggers-only readiness check reports a healthy instance while
// every ledger row the guild ever recorded is gone.
func TestStatus_DroppedLedgerTable_ReportsTheMissingTable(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")
	runner := migratedRunner(t, dataDir, dbPath)

	withRawFK(t, dbPath, func(handle *sql.DB) {
		applyStatements(t, handle, []string{"DROP TABLE ledger_entry"})

		require.Equal(t,
			[]string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
			ledgerTriggerNames(t, handle),
			"the table took its triggers with it, so nothing is 'missing' — that is the hole this test "+
				"is about")
	})

	status, err := runner.Status(t.Context())
	require.NoError(t, err)

	require.Equal(t, migrate.StateUpToDate, status.State)
	require.True(t, status.Protection.Degraded(),
		"a fully-migrated database with no ledger_entry table reported itself protected")
	require.Equal(t, []string{"ledger_entry"}, status.Protection.MissingTables)
	require.Empty(t, status.Protection.MissingTriggers,
		"a trigger whose table is gone is not reported as missing — the table is the finding")
	require.Contains(t, status.Protection.Detail(), "ledger_entry")
}

// TestStatus_LedgerNotYetCreated_ReportsNothingMissing is the negative control that keeps the check
// usable, and it is the one that is easy to break by being strict.
//
// This database stands at a release before the ledger existed, with newer migrations pending: exactly
// where a mid-upgrade or DKP_AUTO_MIGRATE=false instance sits. Its ledger tables are absent because
// they have not been created yet, not because anybody removed them. A readiness check that could not
// tell those apart would report every fresh install and every maintenance window as tampered, and a
// check that cries wolf on first boot is a check an operator learns to ignore — which would cost more
// than the one it was added to catch.
func TestStatus_LedgerNotYetCreated_ReportsNothingMissing(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	// Stand the database up at 000002_guild: the newest release that predates the ledger.
	installed, err := migrate.New(migrationDirUpTo(t, 2), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, installed.Migrate(t.Context()))

	withRawFK(t, dbPath, func(handle *sql.DB) {
		require.Empty(t, ledgerTriggerNames(t, handle),
			"the ledger migration must NOT have run here, or this test proves nothing")
	})

	// A newer binary, carrying the ledger migration, with automatic migration off — so it reports
	// rather than applies.
	pending, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: false,
	})
	require.NoError(t, err)

	status, err := pending.Status(t.Context())
	require.NoError(t, err)

	require.Equal(t, migrate.StatePending, status.State)
	require.NoError(t, status.Protection.Err)
	require.False(t, status.Protection.Degraded(),
		"a database whose ledger migration has not run yet was reported as having lost its "+
			"protection: %s", status.Protection.Detail())
	require.Empty(t, status.Protection.MissingTables)
	require.Empty(t, status.Protection.MissingTriggers)
}
