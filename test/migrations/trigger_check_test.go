package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// The boot path's third post-migration check: did this migration take an append-only trigger with it?
//
// integrity_check answers "is the file sound" and foreign_key_check answers "does anything dangle".
// Neither has an opinion about a trigger, and SQLite's DROP TABLE removes every trigger attached to
// the table while re-creating nothing. TestMigrate_FullStack_ForgetfulRebuildLosesTheTriggers is the
// proof that the resulting database looks perfect to both of them and is editable; these are the
// tests of what the product now DOES about that.
//
// The two tests below are the two halves of one decision, and the decision is the interesting part.
// A migration that loses a trigger is refused and the snapshot goes back. A database that ARRIVED
// without one is logged loudly and still boots, and still upgrades. Failing in the second case would
// close an officer's upgrade path permanently over damage that predates the binary — the escape
// would be DKP_AUTO_MIGRATE=false, which is to say no escape at all — and it would punish the guild
// that most needs the next migration to land.

// TestMigrate_ForgetfulRebuild_BootRefusesAndRestores is the check doing its job on the exact
// migration the repository keeps as its reproduction of the failure.
//
// The fixture is the one from #28: a correct 12-step rebuild of ledger_entry with the trigger
// re-creation removed, which is the line a real migration omits because Atlas cannot see a trigger
// and never emits one. Before this check existed it applied cleanly and the officer's ledger became
// editable in silence.
func TestMigrate_ForgetfulRebuild_BootRefusesAndRestores(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	installed, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, installed.Migrate(t.Context()))

	var baseline ledgerSnapshot

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedLedger(t, handle)
		baseline = snapshotLedger(t, handle)
	})

	upgraded, err := migrate.New(migrationDir(t, ledgerRebuildNoTriggersFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	runErr := upgraded.Migrate(t.Context())
	require.Error(t, runErr,
		"a migration that dropped ledger_entry's append-only triggers was accepted. Every check the "+
			"boot path runs reported success and the ledger it handed back can be rewritten — which "+
			"is the whole of issue #39.")

	// The sentinel, not the prose: a caller has to be able to tell "your rows are fine but your
	// ledger is no longer protected" from "your database is corrupt".
	require.ErrorIs(t, runErr, migrate.ErrAppendOnlyTriggerLost)
	require.ErrorIs(t, runErr, migrate.ErrMigrationFailed,
		"a lost trigger must take the same failure path as a failed integrity check, including the "+
			"automatic restore")

	var failed *migrate.FailedError
	require.ErrorAs(t, runErr, &failed)
	require.Equal(t, fixtureName(t, ledgerRebuildNoTriggersFixture, 0), failed.Migration.Source,
		"the failure must name the migration that dropped the trigger — per-migration checking is "+
			"what makes that possible and it is the entire actionable content of the message")
	require.True(t, failed.Restored, "the migrator reported that it did not restore")

	for _, name := range []string{"trg_ledger_entry_no_update", "trg_ledger_entry_no_delete"} {
		require.ErrorContains(t, runErr, name, "the failure must name every trigger that was lost")
	}

	// Nothing was lost, and the ledger is protected again — the restore put back a database that
	// still has all four triggers and every row.
	restored := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, migrate.Decompress(failed.Snapshot, restored))
	require.Equal(t, fileSHA256(t, restored), fileSHA256(t, dbPath),
		"the database is not byte-identical to the pre-migration snapshot after the trigger check "+
			"refused a migration")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		requireLedgerIntact(t, handle, baseline, "the refused forgetful rebuild")

		// The added column must be gone with it. A restore that kept the migration's schema change
		// while restoring its data would be a third state, neither before nor after.
		var note int
		require.NoError(t, handle.QueryRowContext(t.Context(),
			`SELECT count(*) FROM pragma_table_info('ledger_entry') WHERE name = 'note'`).Scan(&note))
		require.Zero(t, note, "the refused migration's new column survived the restore")
	})
}

// TestMigrate_DroppedLedgerTable_BootRefusesAndRestores closes the way THROUGH the check above.
//
// A check that only counts triggers cannot see this, and the reason is the same exemption that makes
// it usable: a trigger on a table that does not exist is vacuously present, because a fresh install
// has to be able to apply migration 000001 without ledger_entry existing yet. Drop the table
// afterwards and that exemption reports a healthy database — integrity_check passes, foreign_key_check
// passes (ledger_entry is the child, so nothing dangles), no trigger is missing because neither has a
// table to be missing from, and every entry the guild ever recorded is gone.
//
// So the boot path compares the ledger's TABLE SET across each migration as well. Legitimate rebuilds
// are unaffected: a 12-step rebuild drops and re-creates the table under the same name inside one
// migration, and the comparison only looks at the state before and after the whole file — which is
// exactly what TestMigrate_FullStack_LedgerDataSurvivesUpgrade and the ledger_batch rebuild tests
// demonstrate by passing.
func TestMigrate_DroppedLedgerTable_BootRefusesAndRestores(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	installed, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, installed.Migrate(t.Context()))

	var baseline ledgerSnapshot

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedLedger(t, handle)
		baseline = snapshotLedger(t, handle)
	})

	upgraded, err := migrate.New(migrationDir(t, dropLedgerEntryFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	runErr := upgraded.Migrate(t.Context())
	require.Error(t, runErr,
		"a migration that DROPPED ledger_entry was accepted. Every entry the guild ever recorded is "+
			"gone, and integrity_check, foreign_key_check and a trigger-only check all report a "+
			"perfectly healthy database — a table with no rows has no triggers to be missing.")

	require.ErrorIs(t, runErr, migrate.ErrLedgerTableDropped)
	require.ErrorIs(t, runErr, migrate.ErrMigrationFailed, "it must take the auto-restore path")
	require.ErrorContains(t, runErr, "ledger_entry", "the failure must name the table that went")

	var failed *migrate.FailedError
	require.ErrorAs(t, runErr, &failed)
	require.Equal(t, fixtureName(t, dropLedgerEntryFixture, 0), failed.Migration.Source)
	require.True(t, failed.Restored, "the migrator reported that it did not restore")

	restored := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, migrate.Decompress(failed.Snapshot, restored))
	require.Equal(t, fileSHA256(t, restored), fileSHA256(t, dbPath),
		"the database is not byte-identical to the pre-migration snapshot after a dropped ledger table")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		requireLedgerIntact(t, handle, baseline, "the refused DROP TABLE")
	})
}

// TestMigrate_AlreadyDegradedDatabase_StillUpgrades is the other half of the decision, and it is the
// half that is easy to get wrong by being strict.
//
// The database here arrives missing two triggers — the state the forgetful rebuild leaves, reached
// here without the boot path's involvement, exactly as a fork's build or a support session with a
// SQLite client would reach it. The next migration is unrelated and harmless.
//
// It must apply. A check that compared against the full catalogue rather than against the state
// before each migration would refuse it, and refusing it would mean this officer can never upgrade
// again: every future release fails on the first migration, restores, and exits 1, over damage no
// version of this binary can undo. The loud log is the right response to a fait accompli; the
// refusal is reserved for damage this boot is about to cause and can still take back.
func TestMigrate_AlreadyDegradedDatabase_StillUpgrades(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	installed, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, installed.Migrate(t.Context()))

	var baseline ledgerSnapshot

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedLedger(t, handle)
		baseline = snapshotLedger(t, handle)

		// Degrade it behind the boot path's back. goose's bookkeeping is untouched, so as far as the
		// next boot is concerned this is simply the database it was handed.
		applyStatements(t, handle, gooseUpStatements(t, ledgerRebuildNoTriggersFixture))
	})

	withRawFK(t, dbPath, func(handle *sql.DB) {
		require.Equal(t,
			[]string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
			ledgerTriggerNames(t, handle),
			"the fixture did not degrade the database, so this test has nothing to prove")
	})

	upgraded, err := migrate.New(migrationDir(t, futureFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, upgraded.Migrate(t.Context()),
		"a database that ARRIVED without its append-only triggers was refused an unrelated migration. "+
			"That closes the upgrade path permanently for damage this binary did not cause and cannot "+
			"undo — the check must compare against the state before each migration, not against the "+
			"full catalogue.")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		// The migration ran, the surviving rows are still exactly what they were, and nothing
		// pretended to repair the missing triggers — the boot path does not silently re-create them,
		// because a database whose history was editable for an unknown period is a support
		// conversation and not something to paper over.
		requireLedgerUnchanged(t, baseline, snapshotLedger(t, handle), "an unrelated migration")
		require.Equal(t,
			[]string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
			ledgerTriggerNames(t, handle),
			"the boot path re-created triggers it never dropped; restoring the guarantee silently "+
				"hides that history was rewritable")
	})
}
