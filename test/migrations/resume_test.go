package migrations_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// TestMigrate_InterruptedRestore_CompletesOnNextBoot covers the crash window in the restore path.
//
// The window is real and it is not closable by any pragma. Between deleting the failed migration's
// WAL and renaming the verified snapshot into place there is an instant where a kill leaves the
// broken database on disk — and re-running does not fix it, because goose has already recorded the
// failing migration as applied, so the next boot finds nothing pending and never re-checks. The
// obvious mitigation does not work either: disabling WAL auto-checkpoint does keep the main file
// pristine while the connection is open, but closing the last connection checkpoints regardless,
// and the restore path is *required* to close the store before it can replace the file.
//
// So the fix is a marker file, and this test is what proves the marker is load-bearing rather than
// decorative. It simulates the crash exactly: run a failing migration, then put the database back
// into the mid-restore state by hand — broken database on disk, marker present — and assert the
// next Migrate finishes the job without being asked.
func TestMigrate_InterruptedRestore_CompletesOnNextBoot(t *testing.T) {
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
	seedRows(t, openRaw(t, dbPath), 11)

	broken, err := migrate.New(migrationDir(t, brokenFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	var failed *migrate.FailedError
	require.ErrorAs(t, broken.Migrate(t.Context()), &failed)
	require.True(t, failed.Restored)

	// Reconstruct the state a `kill -9` between the WAL deletion and the rename leaves behind: a
	// database that is not the pre-migration one, plus the marker naming the snapshot.
	//
	// The failing migration is deliberately NOT run a second time to get there. A second run inside
	// the same wall-clock second collides on the snapshot filename, because the snapshot is created
	// with O_EXCL and quietly overwriting a pre-migration snapshot is the one thing this whole
	// mechanism exists to prevent. Reconstructing the state directly tests the resume path instead
	// of that collision.
	markerPath := dbPath + ".restore-pending"
	require.NoError(t, os.WriteFile(markerPath, []byte(failed.Snapshot), 0o600))
	require.NoError(t, os.WriteFile(dbPath, []byte("this is not a database"), 0o600))

	resumed, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	require.NoError(t, resumed.Migrate(t.Context()),
		"the next boot did not finish the interrupted restore; the database is left in a state that "+
			"is neither the old one nor the new one, and re-running will never fix it")

	require.NoFileExists(t, markerPath,
		"the marker survived a completed restore, so every subsequent boot will redo it")

	require.Equal(t, 11, countSeedRows(t, openRaw(t, dbPath)),
		"the resumed restore did not bring the data back")

	var name string
	require.Error(t,
		openRaw(t, dbPath).QueryRow(`SELECT name FROM sqlite_schema WHERE name = 'broken_ledger'`).Scan(&name),
		"the poison migration's table survived the resumed restore")
}

// TestMigrate_NoMarker_DoesNotResume is the control.
//
// Without it, a resumeRestore that fired unconditionally — or one that mistook a missing marker for
// an empty one — would restore a snapshot over a perfectly healthy database on every single boot,
// silently discarding everything written since the last upgrade. That is a worse failure than the
// one the marker exists to fix.
func TestMigrate_NoMarker_DoesNotResume(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	seedRows(t, openRaw(t, dbPath), 5)
	require.NoFileExists(t, dbPath+".restore-pending", "a clean run must leave no marker behind")

	require.NoError(t, runner.Migrate(t.Context()))

	require.Equal(t, 5, countSeedRows(t, openRaw(t, dbPath)),
		"a boot with no marker restored a snapshot anyway and discarded live data")
}
