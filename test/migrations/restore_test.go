package migrations_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// TestMigrate_BrokenMigration_RestoresByteIdentical is the reason this PR is third in the sequence.
//
// docs/design/04-testing.md:535 calls the auto-restore path "the highest-value 40 lines in the
// product; untested it is decoration". This is that test. It seeds a database, points the migrator
// at a migration that leaves it failing PRAGMA integrity_check, and requires four things:
//
//  1. the run fails rather than reporting success;
//  2. the on-disk database is byte-identical to the pre-migration snapshot;
//  3. the seeded rows are still there — a valid but empty database satisfies (2) and is a total
//     loss;
//  4. the message names the failing migration FILE and how to restore, because at 1 a.m. after a
//     raid the operator has that one line and nothing else.
//
// On (2), read the acceptance criterion carefully. "Byte-identical to the pre-migration snapshot"
// cannot mean byte-identical to the pre-migration FILE: the snapshot is taken with VACUUM INTO,
// which defragments, so the two differ by design. The restored file must equal the snapshot. Any
// other reading makes this test either impossible or vacuous.
func TestMigrate_BrokenMigration_RestoresByteIdentical(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	// A first, clean run gets the database to a good state with data in it. Restoring an empty
	// database is not a test of anything.
	good, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err, "build the migrator over the real migration set")
	require.NoError(t, good.Migrate(t.Context()), "the real migration set must apply to an empty database")

	seedRows(t, openRaw(t, dbPath), 25)
	require.Equal(t, 25, countSeedRows(t, openRaw(t, dbPath)), "seeding did not take")

	// Now the same database, one migration further, where that migration is poison.
	broken, err := migrate.New(migrationDir(t, brokenFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err, "build the migrator over the broken migration set")

	runErr := broken.Migrate(t.Context())
	require.Error(t, runErr,
		"a migration that leaves the database failing PRAGMA integrity_check reported success — "+
			"either the check is not running after each migration, or its result is being discarded")

	var failed *migrate.FailedError
	require.ErrorAs(t, runErr, &failed,
		"the failure must be a *migrate.FailedError so cmd/dkp can exit 1 and print the snapshot path")

	// (4) The operator-facing message.
	require.Equal(t, "000002_broken_integrity.sql", failed.Migration.Source,
		"the error names the wrong migration — per-migration integrity checking is what makes this "+
			"identify the culprit instead of whichever migration happened to run last")
	require.True(t, failed.Restored, "the migrator reported that it did not restore")
	require.Contains(t, runErr.Error(), failed.Snapshot, "the message must contain the snapshot path")
	require.Contains(t, runErr.Error(), "zstd -d", "the message must contain a runnable restore command")

	// (2) The headline assertion.
	restoredPlain := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, migrate.Decompress(failed.Snapshot, restoredPlain),
		"the snapshot could not be decompressed — a snapshot that cannot be read is not a backup")

	require.Equal(t, fileSHA256(t, restoredPlain), fileSHA256(t, dbPath),
		"the database on disk is NOT byte-identical to the pre-migration snapshot. A failed upgrade "+
			"left the officer's database in some third state that is neither the old one nor the new one.")

	// (3) The data.
	require.Equal(t, 25, countSeedRows(t, openRaw(t, dbPath)),
		"the restored database is structurally fine and has lost its rows")

	// The poison migration's table must be gone: its presence would mean the restore replaced the
	// file but the migration's effects survived, which is the failure mode a naive `cp` produces.
	var name string
	queryErr := openRaw(t, dbPath).QueryRow(
		`SELECT name FROM sqlite_schema WHERE name = 'broken_ledger'`).Scan(&name)
	require.Error(t, queryErr, "broken_ledger survived the restore — the rollback was not complete")

	// The WAL and shm siblings must not have been left behind pointing at the old file. SQLite
	// would replay a stale -wal over the restored database on the next open, which reintroduces
	// exactly the state that was just rolled back.
	for _, sibling := range []string{dbPath + "-wal", dbPath + "-shm"} {
		_, statErr := os.Stat(sibling)
		require.True(t, os.IsNotExist(statErr),
			"%s still exists after restore; SQLite will replay it over the restored database", sibling)
	}
}

// TestMigrate_BrokenMigration_SnapshotIsAReadableDatabase covers the seventh acceptance criterion:
// the snapshot is a VALID READABLE DATABASE, not merely a file that exists.
//
// Separated from the test above because they fail for different reasons and a reviewer seeing this
// one red should not have to work out which half broke. A snapshot that is present, correctly
// named, correctly sized and unreadable passes every existence check anyone writes by reflex.
func TestMigrate_BrokenMigration_SnapshotIsAReadableDatabase(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	good, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, good.Migrate(t.Context()))
	seedRows(t, openRaw(t, dbPath), 7)

	broken, err := migrate.New(migrationDir(t, brokenFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	var failed *migrate.FailedError
	require.ErrorAs(t, broken.Migrate(t.Context()), &failed)

	plain := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, migrate.Decompress(failed.Snapshot, plain))

	handle := openRaw(t, plain)

	var quick string
	require.NoError(t, handle.QueryRow("PRAGMA quick_check").Scan(&quick), "the snapshot could not be opened")
	require.Equal(t, "ok", quick, "the snapshot is a file, but not a healthy database")

	require.Equal(t, 7, countSeedRows(t, handle),
		"the snapshot opened cleanly and does not contain the data it was taken to preserve")

	// The snapshot holds every credential and every email address the guild has. 03-security.md
	// requires 0600 on anything under /data/backups/.
	info, err := os.Stat(failed.Snapshot)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the snapshot is readable by other users on the box")

	require.True(t, strings.HasPrefix(filepath.Base(failed.Snapshot), "pre-v1.1.0-"),
		"snapshot name %q does not follow pre-<version>-<timestamp>.db.zst", filepath.Base(failed.Snapshot))
	require.True(t, strings.HasSuffix(failed.Snapshot, ".db.zst"),
		"snapshot name %q does not end .db.zst, which is the glob the Down blocks tell operators to look for",
		failed.Snapshot)
}

// TestMigrate_BrokenMigration_LeavesVersionUnchanged asserts the recorded schema version goes back
// with the file.
//
// Without this, a restore could put the bytes back while goose's bookkeeping still claimed the
// broken migration had been applied — so the next boot would skip it, and the database would be
// permanently one migration behind what the binary believes it is.
func TestMigrate_BrokenMigration_LeavesVersionUnchanged(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	good, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, good.Migrate(t.Context()))

	before, err := good.Status(t.Context())
	require.NoError(t, err)

	broken, err := migrate.New(migrationDir(t, brokenFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.Error(t, broken.Migrate(t.Context()))

	after, err := good.Status(t.Context())
	require.NoError(t, err)

	require.Equal(t, before.Applied, after.Applied,
		"the applied version moved even though the migration was rolled back; the next boot will "+
			"skip a migration that never actually ran")
	require.Equal(t, migrate.StateUpToDate, after.State,
		"against the real migration set the restored database should be up to date again")

	require.False(t, errors.Is(err, migrate.ErrSchemaAhead), "sanity: this is not the downgrade path")
}
