package migrations_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// TestMigrate_NewerSchemaThanBinary_RefusesToStart is the downgrade refusal.
//
// The scenario is mundane and therefore likely: an officer upgrades, something unrelated looks
// wrong, and they roll the image back the way they would roll back any other container. The old
// binary now faces a schema it does not understand. It must refuse.
//
// Refusing is not conservatism. An old binary against a new schema does not crash — it writes rows
// that ignore the new columns, drops values it has no field for, and degrades the data over days
// while everything appears to work. By the time anyone notices, every backup in the retention
// window contains the damage. A process that will not start is a support ticket; a process that
// starts is a data-loss incident.
//
// The setup is the real thing rather than a stub: migrate forward with a two-migration set, then
// point a "previous release" migrator carrying only the first migration at the same file.
func TestMigrate_NewerSchemaThanBinary_RefusesToStart(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	// The "new" release: the real migration set plus one more migration.
	newer, err := migrate.New(migrationDir(t, futureFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v2.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, newer.Migrate(t.Context()), "the forward migration must succeed before a downgrade can be tested")

	// The "old" release: only the migrations that shipped before.
	older, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	runErr := older.Migrate(t.Context())
	require.Error(t, runErr,
		"an older binary started against a newer schema. It will now write rows that ignore columns "+
			"it does not know about, and nothing will report an error until the data is unrecoverable.")
	require.ErrorIs(t, runErr, migrate.ErrSchemaAhead)

	var ahead *migrate.SchemaAheadError
	require.ErrorAs(t, runErr, &ahead)

	message := runErr.Error()

	// The message must name the image tag that CAN read this database. That is only possible
	// because a successful migration records binary_version into dkp_meta; without it the best
	// available sentence is "use a newer version", which the operator already knows.
	require.Equal(t, "v2.0.0", ahead.WroteBy,
		"the binary version that wrote this database was not recorded, so the refusal cannot name "+
			"the image tag to run — check that recordVersion writes %s", migrate.MetaKeyBinaryVersion)
	require.Contains(t, message, "v2.0.0", "the refusal must name the image tag that can read this database")
	require.Contains(t, message, ahead.BackupDir, "the refusal must name the snapshot path")

	// It must NOT have migrated downward, and must not have touched anything.
	status, err := newer.Status(t.Context())
	require.NoError(t, err)
	require.Equal(t, migrate.StateAhead, mustAheadFor(t, older))
	require.Equal(t, migrate.StateUpToDate, status.State,
		"the database moved. A refusal that modifies anything is not a refusal.")
}

// mustAheadFor reads the older binary's view of the same database.
func mustAheadFor(t *testing.T, r *migrate.Runner) migrate.State {
	t.Helper()

	status, err := r.Status(t.Context())
	require.NoError(t, err)

	return status.State
}

// TestMigrate_NewerSchemaThanBinary_TakesNoSnapshot asserts the refusal happens BEFORE any work.
//
// Snapshotting on the downgrade path would be actively harmful: it writes a fresh file into the
// backup directory at exactly the moment the operator is going to go looking for the last good
// one, and under a retention policy it is one more thing pushing the snapshot they actually need
// out of the window.
func TestMigrate_NewerSchemaThanBinary_TakesNoSnapshot(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	newer, err := migrate.New(migrationDir(t, futureFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v2.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, newer.Migrate(t.Context()))

	before := snapshotNames(t, filepath.Join(dataDir, "backups"))

	older, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.Error(t, older.Migrate(t.Context()))

	require.Equal(t, before, snapshotNames(t, filepath.Join(dataDir, "backups")),
		"the downgrade refusal wrote a snapshot. It must refuse before doing any work at all.")
}

// TestMigrate_AutoMigrateDisabled_LeavesDatabaseAlone covers DKP_AUTO_MIGRATE=false.
//
// The contract is specific and easy to get wrong in the direction that loses data: with
// auto-migrate off the process must still START (so the officer can reach the UI and read the
// banner) and must not apply anything. Returning an error here would make the container
// crash-loop, which is the outcome the whole migrate-on-boot default exists to avoid.
func TestMigrate_AutoMigrateDisabled_LeavesDatabaseAlone(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: false,
	})
	require.NoError(t, err)

	require.NoError(t, runner.Migrate(t.Context()),
		"pending migrations with auto-migrate off must not be an error: the process serves and "+
			"/readyz reports 503 with the command to run")

	status, err := runner.Status(t.Context())
	require.NoError(t, err)
	require.Equal(t, migrate.StatePending, status.State)
	require.NotEmpty(t, status.Pending, "the pending set must be reported so /readyz can name it")
	require.Equal(t, int64(0), status.Applied, "auto-migrate was off and a migration was applied anyway")

	require.Empty(t, snapshotNames(t, filepath.Join(dataDir, "backups")),
		"a snapshot was taken for migrations that were never applied")

	require.Empty(t, status.WroteBy,
		"binary_version was recorded even though nothing was migrated")
}
