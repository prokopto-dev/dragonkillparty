package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// Rebuilding ledger_batch — the PARENT of the ledger's two tables — on a database that has data in
// it.
//
// TestMigrate_FullStack_LedgerDataSurvivesUpgrade covers the child. The child is the easy one:
// SQLite's 12-step rebuild drops it, nothing references it, and the drop succeeds whether or not
// foreign keys are enforced. The parent is where Atlas's generated form stops working, and it stops
// working on exactly the databases nobody tests against — the populated ones.
//
// The mechanism, because a reader will otherwise assume the pragma does what it says:
//
//	Atlas wraps the rebuild in `PRAGMA foreign_keys = off` … `on`. goose runs each migration inside
//	a transaction. SQLite IGNORES that pragma inside a transaction — silently, no error, no warning
//	— so `DROP TABLE ledger_batch` runs with foreign keys enforced, ledger_entry's rows still point
//	at it, and SQLite raises. `PRAGMA defer_foreign_keys` does not rescue it either: deferred
//	enforcement counts violating operations rather than re-scanning at COMMIT, so dropping the
//	parent and re-creating it with identical rows never decrements the counter.
//
// The two fixtures in test/fixtures/migrations/rebuild/ are the same rebuild with one variable
// between them, and these tests are the two halves of that experiment. Neither is worth much alone:
// "the safe form works" says nothing without a form that does not, and "Atlas's form fails" is a
// bug report rather than a gate without the pattern that fixes it sitting next to it.
//
// Both are here rather than in populated_upgrade_test.go because they are a different question. That
// file asks "does a REAL migration hurt a populated ledger"; this one asks "which shape may a future
// migration take at all", and the answer is what .claude/rules/migrations.md documents.

// TestMigrate_FullStack_LedgerBatchRebuildSurvivesPopulatedUpgrade is the pattern the rule
// prescribes, executed against real data.
//
// It is a canary in the strict sense: it fails when the environment changes underneath the rule
// rather than when this repository's code changes. If goose's transaction handling, the driver's
// pragma handling, or SQLite's foreign-key semantics move, the documented safe pattern stops being
// safe — and the first migration to find that out would otherwise be one an officer is running.
func TestMigrate_FullStack_LedgerBatchRebuildSurvivesPopulatedUpgrade(t *testing.T) {
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

	upgraded, err := migrate.New(migrationDir(t, batchRebuildFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, upgraded.Migrate(t.Context()),
		"the safe ledger_batch rebuild failed on a populated database. That fixture IS the pattern "+
			".claude/rules/migrations.md tells the next author to copy, so this failing means the "+
			"rule is now wrong — fix the rule and the fixture together, and do not relax this test.")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		after := requireLedgerIntact(t, handle, baseline, "the ledger_batch table rebuild")

		// The rebuild's reason for existing. Without this, a fixture that had quietly become a no-op
		// would pass every assertion above by never touching the table at all.
		require.Contains(t, after.batches[seedBatch1ID], "memo",
			"the rebuild fixture did not add its new column, so this proved nothing about a migration "+
				"that changes the parent table's shape")

		// The child's rows still resolve to the rebuilt parent. requireLedgerIntact already ran
		// foreign_key_check, but that check is only as strong as the constraint still existing — a
		// rebuild that dropped the FK clause would leave a clean report over an unconstrained table.
		var fks int
		require.NoError(t, handle.QueryRowContext(t.Context(),
			`SELECT count(*) FROM pragma_foreign_key_list('ledger_entry') WHERE "table" = 'ledger_batch'`).
			Scan(&fks))
		require.Equal(t, 1, fks,
			"ledger_entry no longer declares a foreign key to ledger_batch, so foreign_key_check has "+
				"nothing left to enforce and its clean report above means nothing")
	})
}

// TestMigrate_FullStack_AtlasShapedBatchRebuildFailsOnPopulatedData is the negative half, and it is
// the finding from issue #35 reproduced as a test rather than as a paragraph.
//
// Its two subtests are the same migration applied to two databases, and the pair is the point: the
// form Atlas generates PASSES on an empty ledger and FAILS on a populated one. That is the exact
// shape of "works on a fresh install, breaks on upgrade" — every fresh-install gate in this
// repository is green while every real guild's upgrade dies.
//
// This test failing means the trap closed: either goose stopped wrapping migrations in a
// transaction, or SQLite started honouring the pragma inside one. Both would be good news, both make
// the NO TRANSACTION requirement in .claude/rules/migrations.md unnecessary, and both are a
// deliberate rewrite of the rule and its two fixtures — not a reason to delete this test.
func TestMigrate_FullStack_AtlasShapedBatchRebuildFailsOnPopulatedData(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	t.Run("on a populated ledger it fails and the snapshot is restored", func(t *testing.T) {
		t.Parallel()

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

		upgraded, err := migrate.New(migrationDir(t, batchRebuildInTransactionFixture), migrate.Config{
			DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
		})
		require.NoError(t, err)

		runErr := upgraded.Migrate(t.Context())
		require.Error(t, runErr,
			"Atlas's generated rebuild of ledger_batch SUCCEEDED against a populated ledger. If the "+
				"pragma now works inside goose's transaction, the NO TRANSACTION requirement in "+
				".claude/rules/migrations.md is obsolete and both fixtures need rewriting.")

		var failed *migrate.FailedError
		require.ErrorAs(t, runErr, &failed)
		require.Equal(t, fixtureName(t, batchRebuildInTransactionFixture, 0), failed.Migration.Source,
			"the failure must name the migration that caused it")
		require.ErrorContains(t, runErr, "FOREIGN KEY constraint failed",
			"it failed, but not on the foreign key — the DROP is supposed to be refused because "+
				"ledger_entry still references ledger_batch. A different failure means this fixture is "+
				"no longer reproducing the finding.")
		require.True(t, failed.Restored, "the migrator reported that it did not restore")

		// The officer loses nothing, which is what makes this a loud failure rather than a disaster.
		restored := filepath.Join(t.TempDir(), "snapshot.db")
		require.NoError(t, migrate.Decompress(failed.Snapshot, restored))
		require.Equal(t, fileSHA256(t, restored), fileSHA256(t, dbPath),
			"the database is not byte-identical to the pre-migration snapshot after a failed parent "+
				"rebuild")

		withRawFK(t, dbPath, func(handle *sql.DB) {
			after := requireLedgerIntact(t, handle, baseline, "the failed ledger_batch rebuild")
			require.NotContains(t, after.batches[seedBatch1ID], "memo",
				"the rebuild's new column survived a migration that was rolled back and restored")
		})
	})

	t.Run("on an empty ledger the identical migration passes", func(t *testing.T) {
		t.Parallel()

		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "dkp.db")

		installed, err := migrate.New(migrationDir(t), migrate.Config{
			DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
		})
		require.NoError(t, err)
		require.NoError(t, installed.Migrate(t.Context()))

		// No seedLedger. ledger_batch has no rows, so nothing in ledger_entry points at it and the
		// DROP the subtest above died on is uncontroversial.
		upgraded, err := migrate.New(migrationDir(t, batchRebuildInTransactionFixture), migrate.Config{
			DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
		})
		require.NoError(t, err)
		require.NoError(t, upgraded.Migrate(t.Context()),
			"the broken migration must APPLY CLEANLY to an empty ledger — that is what makes it "+
				"dangerous. A fresh-install gate cannot see this class of bug at all.")

		withRawFK(t, dbPath, func(handle *sql.DB) {
			var memo int
			require.NoError(t, handle.QueryRowContext(t.Context(),
				`SELECT count(*) FROM pragma_table_info('ledger_batch') WHERE name = 'memo'`).Scan(&memo))
			require.Equal(t, 1, memo, "the rebuild did not run, so this subtest proved nothing")
		})
	})
}
