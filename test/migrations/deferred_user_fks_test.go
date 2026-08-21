package migrations_test

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// Issue #277, asserted where it has to hold: the UPGRADE path.
//
// role_assignment.granted_by, pool_config_change.changed_by and decay_run.triggered_by shipped as
// nullable TEXT with no constraint, each carrying a comment saying the foreign key would be wired
// when app_user landed. 000009 landed it. Adding a foreign key to an existing SQLite table is not an
// ALTER — there is no ADD CONSTRAINT — so the releasing migration is a 12-step rebuild of all three
// tables, and a rebuild is the shape .claude/rules/migrations.md warns about precisely because it
// passes every fresh-install check on its way to eating somebody's data.
//
// A fresh install therefore proves nothing here. These tests stand a database up at the release
// BEFORE the rebuild, put rows in all three tables — including rows whose attribution is a real
// app_user, which is the value the new constraint has an opinion about — and then upgrade.
//
// They import no domain package (docs/design/04-testing.md:541): the ids and the columns are
// literals, so a refactor inside internal/authz cannot change what they claim the migration did.

// userFKMigration is the migration that wires the three foreign keys.
//
// Restated as a literal because it is a fact about a FROZEN artefact rather than a fact about the
// current tree: 000010 either rebuilt those three tables or it did not, and it can never be edited to
// stop having done so. Deriving "the last migration" instead would silently re-aim these tests at
// whatever lands next.
const userFKMigration = 10

// Seed identifiers for the attribution rows. Distinct from populated_upgrade_test.go's so that a
// failure names the test it came from.
const (
	fkSeedUserID     = "000000000000000000FKUSER01"
	fkSeedRoleID     = "000000000000000000FKROLE01"
	fkSeedRole2ID    = "000000000000000000FKROLE02"
	fkSeedAssignID   = "00000000000000000FKASSIGN1"
	fkSeedConfigID   = "0000000000000000000FKPCC01"
	fkSeedDecayRunID = "000000000000000000FKDECAY1"
)

// TestMigrate_DeferredUserForeignKeys_PopulatedAttributionSurvivesTheUpgrade is the headline: the
// three rebuilds, run against tables that are not empty.
//
// Every column of every seeded row must be byte-for-byte what it was — a rebuild copies column by
// column, and transposing two or resetting one to its default is invisible to PRAGMA integrity_check
// and to a fresh-install fingerprint alike. The attribution is a REAL app_user id rather than NULL,
// because a rebuild that dropped the value would still satisfy the new constraint and a NULL-only
// fixture could not tell the difference.
func TestMigrate_DeferredUserForeignKeys_PopulatedAttributionSurvivesTheUpgrade(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	requireMigrated(t, dataDir, dbPath, migrationDirUpTo(t, userFKMigration-1), "v0.9.0")

	var before map[string]tableSnapshot

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedAttribution(t, handle, fkSeedUserID)
		before = attributionSnapshot(t, handle)
	})

	requireMigrated(t, dataDir, dbPath, migrationDir(t), "v1.0.0")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		after := attributionSnapshot(t, handle)

		for _, table := range []string{"role_assignment", "pool_config_change", "decay_run"} {
			requireTableUnchanged(t, table, before[table], after[table],
				"the migration that wires the app_user foreign keys")
		}

		require.Equal(t, 0, foreignKeyViolations(t, handle),
			"the rebuild left dangling references — PRAGMA integrity_check would call this healthy")
	})
}

// TestMigrate_DeferredUserForeignKeys_AreEnforcedAfterTheUpgrade is the point of the change: the
// constraints are real now, in both directions.
//
// An unknown user id must be REFUSED — that is what a foreign key is — and deleting a user that rows
// point at must SET NULL rather than refuse or cascade. The second half is the decision #277 asked
// for: attribution on a history row is not a live capability, so erasing the officer keeps the row
// and drops the name, which is the opposite of what service_account.owner_user_id chose and for the
// stated opposite reason.
func TestMigrate_DeferredUserForeignKeys_AreEnforcedAfterTheUpgrade(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	requireMigrated(t, dataDir, dbPath, migrationDir(t), "v1.0.0")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedAttribution(t, handle, fkSeedUserID)

		unknown := []struct {
			column string
			stmt   string
			args   []any
		}{
			{
				column: "role_assignment.granted_by",
				stmt: `INSERT INTO role_assignment
				         (id, subject_kind, subject_id, role_id, scope_type, granted_by, created_at, updated_at)
				       VALUES ('00000000000000000FKASSIGN9', 'user', 'OTHER_SUBJECT_000000000001', ?,
				               'global', 'NO_SUCH_USER_00000000000', 1, 1)`,
				args: []any{fkSeedRoleID},
			},
			{
				column: "pool_config_change.changed_by",
				stmt: `INSERT INTO pool_config_change
				         (id, pool_id, changed_at, changed_by, from_strategy_id, from_strategy_version,
				          from_config_json, to_strategy_id, to_strategy_version, to_config_json)
				       VALUES ('0000000000000000000FKPCC09', ?, 1, 'NO_SUCH_USER_00000000000',
				               'a', '1.0.0', '{}', 'b', '1.0.0', '{}')`,
				args: []any{defaultPoolID},
			},
			{
				column: "decay_run.triggered_by",
				stmt: `INSERT INTO decay_run
				         (id, pool_id, kind, cadence_period, scheduled_for_at, triggered_by, created_at, updated_at)
				       VALUES ('000000000000000000FKDECAY9', ?, 'decay', '2026-W40',
				               1, 'NO_SUCH_USER_00000000000', 1, 1)`,
				args: []any{defaultPoolID},
			},
		}

		for _, tc := range unknown {
			_, err := handle.ExecContext(t.Context(), tc.stmt, tc.args...)
			require.Error(t, err,
				"%s accepted a user id that does not exist. #277 wired this foreign key precisely so "+
					"that attribution names a row rather than a string somebody typed.", tc.column)
			require.ErrorContains(t, err, "FOREIGN KEY",
				"the refusal of %s must be the constraint, not something incidental", tc.column)
		}

		// The erasure path. Deleting the user must succeed — an officer who has left cannot be held
		// in the database by the fact that they once granted a role — and must leave the history.
		_, err := handle.ExecContext(t.Context(), `DELETE FROM app_user WHERE id = ?`, fkSeedUserID)
		require.NoError(t, err,
			"deleting a user that only HISTORY rows point at was refused. That is the NO ACTION "+
				"behaviour service_account.owner_user_id chose deliberately and these three chose "+
				"against: it would make an erasure end in hand-editing role_assignment.")

		orphaned := []struct{ table, id, column string }{
			{"role_assignment", fkSeedAssignID, "granted_by"},
			{"pool_config_change", fkSeedConfigID, "changed_by"},
			{"decay_run", fkSeedDecayRunID, "triggered_by"},
		}

		for _, o := range orphaned {
			var (
				present int
				value   sql.NullString
			)

			require.NoError(t, handle.QueryRowContext(t.Context(),
				//nolint:gosec // table and column are literals from the slice above, not input.
				"SELECT count(*), max("+o.column+") FROM "+o.table+" WHERE id = ?", o.id).
				Scan(&present, &value))

			require.Equal(t, 1, present,
				"deleting the user took the %s row with it. SET NULL keeps the history and drops the "+
					"attribution; a CASCADE here would erase the record of who held power.", o.table)
			require.False(t, value.Valid,
				"%s.%s still points at a user that no longer exists", o.table, o.column)
		}

		require.Equal(t, 0, foreignKeyViolations(t, handle), "the SET NULL left something dangling")
	})
}

// TestMigrate_DeferredUserForeignKeys_DanglingAttribution_FailsTheUpgradeLoudly is the one way this
// migration can break somebody's upgrade, written down.
//
// Atlas wraps the rebuild in `PRAGMA foreign_keys = off` / `on`, and goose runs the migration inside
// a transaction, where SQLite ignores that pragma — silently, as .claude/rules/migrations.md records.
// So the row copy happens with the NEW constraint enforced, and a pre-existing `granted_by` naming a
// user that does not exist stops the migration dead.
//
// That is the correct outcome and this test exists to prove it is the outcome: the boot path restores
// the pre-migration snapshot and names the file, rather than dropping the offending rows or leaving
// the database half rebuilt. No writer fills these columns yet, so no real install can be in this
// state — which is exactly why the release that wires them is the cheap moment to do it.
func TestMigrate_DeferredUserForeignKeys_DanglingAttribution_FailsTheUpgradeLoudly(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	requireMigrated(t, dataDir, dbPath, migrationDirUpTo(t, userFKMigration-1), "v0.9.0")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedAttribution(t, handle, fkSeedUserID)

		// The state the constraint refuses, written while nothing refuses it.
		_, err := handle.ExecContext(t.Context(),
			`UPDATE role_assignment SET granted_by = 'NO_SUCH_USER_00000000000' WHERE id = ?`, fkSeedAssignID)
		require.NoError(t, err, "the column is unconstrained at this release, so this must be writable")
	})

	runner, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)

	err = runner.Migrate(t.Context())
	require.Error(t, err,
		"the rebuild copied a row whose granted_by names no user and the migration SUCCEEDED. Either "+
			"the foreign key is not enforced during the copy — in which case the constraint is a "+
			"comment — or the row was dropped, which is silent data loss on an upgrade.")

	// The database is the one the officer started with, not a half-rebuilt one.
	withRawFK(t, dbPath, func(handle *sql.DB) {
		require.Equal(t, 0, foreignKeyViolations(t, handle),
			"the restored database is not referentially intact")

		var granted sql.NullString
		require.NoError(t, handle.QueryRowContext(t.Context(),
			`SELECT granted_by FROM role_assignment WHERE id = ?`, fkSeedAssignID).Scan(&granted),
			"the seeded assignment is gone: the failed migration was not rolled back")
		require.Equal(t, "NO_SUCH_USER_00000000000", granted.String,
			"the restored row does not hold what it held before the migration ran")
	})
}

// requireMigrated applies a migration set to dbPath, failing with the release it was applying.
func requireMigrated(tb testing.TB, dataDir, dbPath string, set fs.FS, version string) {
	tb.Helper()

	runner, err := migrate.New(set, migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: version, AutoMigrate: true,
	})
	require.NoError(tb, err)
	require.NoError(tb, runner.Migrate(tb.Context()), "apply the migration set at %s", version)
}

// seedAttribution writes one app_user and one row in each of the three tables that reference it.
//
// A real user id in every attribution column, and a second role_assignment row with NULL, so the
// upgrade is exercised against both the value the new constraint checks and the value it must keep
// admitting. Raw SQL rather than a domain helper, for the reason at the top of harness_test.go.
func seedAttribution(tb testing.TB, handle *sql.DB, userID string) {
	tb.Helper()

	_, err := handle.ExecContext(tb.Context(),
		`INSERT INTO app_user (id, username, username_norm, display_name, state, created_at, updated_at)
		 VALUES (?, 'Aradune', 'aradune', 'Aradune Mithara', 'active', 1, 1)`, userID)
	require.NoError(tb, err, "seed app_user")

	_, err = handle.ExecContext(tb.Context(),
		`INSERT INTO role (id, key, name, name_norm, is_builtin, applies_to, created_at, updated_at)
		 VALUES (?, 'officer', 'Officer', 'officer', 1, 'both', 1, 1)`, fkSeedRoleID)
	require.NoError(tb, err, "seed role")

	// A second role, because ux_role_assign is (subject, role, scope) and the two assignments below
	// differ only in their granter — which is not part of the key, and must not be.
	_, err = handle.ExecContext(tb.Context(),
		`INSERT INTO role (id, key, name, name_norm, is_builtin, applies_to, created_at, updated_at)
		 VALUES (?, 'raid_leader', 'Raid Leader', 'raidleader', 1, 'both', 1, 1)`, fkSeedRole2ID)
	require.NoError(tb, err, "seed second role")

	_, err = handle.ExecContext(tb.Context(),
		`INSERT INTO role_assignment
		   (id, subject_kind, subject_id, role_id, scope_type, granted_by, granted_via, created_at, updated_at)
		 VALUES (?, 'user', ?, ?, 'global', ?, 'manual', 1, 1)`,
		fkSeedAssignID, userID, fkSeedRoleID, userID)
	require.NoError(tb, err, "seed role_assignment with a real granter")

	_, err = handle.ExecContext(tb.Context(),
		`INSERT INTO role_assignment
		   (id, subject_kind, subject_id, role_id, scope_type, granted_by, granted_via, created_at, updated_at)
		 VALUES ('00000000000000000FKASSIGN2', 'user', ?, ?, 'global', NULL, 'bootstrap', 1, 1)`,
		userID, fkSeedRole2ID)
	require.NoError(tb, err, "seed role_assignment with no granter")

	_, err = handle.ExecContext(tb.Context(),
		`INSERT INTO pool_config_change
		   (id, pool_id, changed_at, changed_by, from_strategy_id, from_strategy_version,
		    from_config_json, to_strategy_id, to_strategy_version, to_config_json, reason)
		 VALUES (?, ?, 1000, ?, 'zero_sum', '1.0.0', '{}', 'tick', '1.0.0', '{"decay_bp":500}', 'officer vote')`,
		fkSeedConfigID, defaultPoolID, userID)
	require.NoError(tb, err, "seed pool_config_change")

	_, err = handle.ExecContext(tb.Context(),
		`INSERT INTO decay_run
		   (id, pool_id, kind, cadence_period, scheduled_for_at, state, triggered_by, error, created_at, updated_at)
		 VALUES (?, ?, 'decay', '2026-W31', 5000, 'planned', ?, '', 1, 1)`,
		fkSeedDecayRunID, defaultPoolID, userID)
	require.NoError(tb, err, "seed decay_run")
}

// attributionSnapshot reads every column of every seeded row in the three tables, the way
// snapshotLedger does for the ledger: SELECT *, so a column this test never heard of is compared too.
func attributionSnapshot(tb testing.TB, handle *sql.DB) map[string]tableSnapshot {
	tb.Helper()

	snap := map[string]tableSnapshot{
		"role_assignment":    snapshotTable(tb, handle, `SELECT * FROM role_assignment ORDER BY id`),
		"pool_config_change": snapshotTable(tb, handle, `SELECT * FROM pool_config_change ORDER BY id`),
		"decay_run":          snapshotTable(tb, handle, `SELECT * FROM decay_run ORDER BY id`),
	}

	require.Len(tb, snap["role_assignment"], 2, "expected the two seeded role_assignment rows")
	require.Len(tb, snap["pool_config_change"], 1, "expected the seeded pool_config_change row")
	require.Len(tb, snap["decay_run"], 1, "expected the seeded decay_run row")

	return snap
}
