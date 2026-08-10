package migrations_test

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// The Phase 0 PR 10a schema objects, asserted against SQL and nothing else.
//
// These tests import no domain package (docs/design/04-testing.md:541) and read sqlite_schema
// directly, so a refactor in internal/ledger cannot silently rewrite what they claim the migration
// produced. The fingerprint test next door notices that the schema CHANGED; these name the two
// properties that must not change and say why, which a hex digest cannot.

// migratedDB applies the real migration set to a fresh database and returns a raw handle to it.
func migratedDB(tb testing.TB) (dbPath string) {
	tb.Helper()

	dataDir := tb.TempDir()
	dbPath = filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDir(tb), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(tb, err)
	require.NoError(tb, runner.Migrate(tb.Context()))

	return dbPath
}

// TestMigrate_AuditLog_HasAnUpdateTriggerAndNoDeleteTrigger pins the deliberate asymmetry between
// the audit log's guardrails and the ledger's.
//
// The ledger's two tables carry BOTH an update and a delete trigger, because a ledger row is never
// removed — deleting one corrupts every downstream balance. The audit log carries ONLY the update
// trigger, because audit rows are prunable by retention (domain model §17: `dkp audit prune
// --before`, which leaves an audit_gap_marker scar rather than a silence). A no-delete trigger here
// would have to be dropped in order to run the prune, and a guardrail that gets dropped during
// normal operation is not a guardrail.
//
// Both directions are asserted. A test that only checked for the update trigger would stay green
// against a future migration that "helpfully" added the delete trigger too — quietly making the
// retention command impossible, months before anybody tried to run it.
func TestMigrate_AuditLog_HasAnUpdateTriggerAndNoDeleteTrigger(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	handle := openRaw(t, migratedDB(t))

	rows, err := handle.QueryContext(t.Context(),
		`SELECT name, COALESCE(sql, '') FROM sqlite_schema WHERE type = 'trigger' AND tbl_name = 'audit_log'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	triggers := map[string]string{}

	for rows.Next() {
		var name, ddl string
		require.NoError(t, rows.Scan(&name, &ddl))

		triggers[name] = normaliseDDL(ddl)
	}

	require.NoError(t, rows.Err())

	require.Contains(t, triggers, "trg_audit_log_no_update",
		"audit_log must carry an append-only UPDATE trigger. Editing an audit row is how a forensic "+
			"record becomes fiction, and the trigger is the database half of the guarantee "+
			"(.claude/rules/migrations.md hand-edit case 1).")
	require.Contains(t, strings.ToUpper(triggers["trg_audit_log_no_update"]), "BEFORE UPDATE")
	require.Contains(t, triggers["trg_audit_log_no_update"], "audit_log is append-only",
		"the trigger must raise the exact message an operator reads")

	for name, ddl := range triggers {
		require.NotContains(t, strings.ToUpper(ddl), "BEFORE DELETE",
			"trigger %q blocks DELETE on audit_log. That is NOT wanted: retention pruning is a "+
				"supported operation and a delete trigger would have to be dropped to run it. If "+
				"this is a deliberate policy change, domain model §17 changes with it.", name)
	}
}

// TestMigrate_EventOutbox_NeverReusesASequence is the AUTOINCREMENT behaviour, proven rather than
// grepped for.
//
// Without AUTOINCREMENT, SQLite hands a new row the largest freed rowid — so pruning old events
// would let a NEW event take a number an old event already published, and a client resuming from
// Last-Event-ID would silently skip everything in between. That failure is invisible in testing and
// catastrophic in production: a bot that reconnects after a prune misses every event it had not yet
// seen, and nothing errors.
//
// The test inserts three events, deletes the highest, and inserts a fourth. Under AUTOINCREMENT the
// fourth must get 4; without it, it would get 3.
func TestMigrate_EventOutbox_NeverReusesASequence(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	handle := openRaw(t, migratedDB(t))

	insert := func(id string) int64 {
		var seq int64
		require.NoError(t, handle.QueryRowContext(t.Context(),
			`INSERT INTO event_outbox (id, topic, event_type, resource_ref, created_at)
			 VALUES (?, 'guild', 'ledger.batch.committed', '/api/v1/ledger/batches/x', 1704067200000000)
			 RETURNING event_seq`, id).Scan(&seq))

		return seq
	}

	require.Equal(t, int64(1), insert("0000000000000000000EVENT01"))
	require.Equal(t, int64(2), insert("0000000000000000000EVENT02"))
	require.Equal(t, int64(3), insert("0000000000000000000EVENT03"))

	_, err := handle.ExecContext(t.Context(), `DELETE FROM event_outbox WHERE event_seq = 3`)
	require.NoError(t, err, "the outbox is prunable — unlike the ledger, it carries no delete trigger")

	require.Equal(t, int64(4), insert("0000000000000000000EVENT04"),
		"event_seq must NEVER be reused. Without AUTOINCREMENT this would be 3, and a client "+
			"resuming from Last-Event-ID=2 after a prune would silently skip the real event 3.")

	// The mechanism, not just the outcome: AUTOINCREMENT is what creates sqlite_sequence, and the
	// high-water mark persists there rather than being derived from max(rowid).
	var tables int
	require.NoError(t, handle.QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_schema WHERE name = 'sqlite_sequence'`).Scan(&tables))
	require.Equal(t, 1, tables,
		"sqlite_sequence must exist — it is created by AUTOINCREMENT and is where the never-reused "+
			"high-water mark lives")
}

// TestMigrate_AuditAndOutbox_DidNotRebuildTheLedgerTables is the guard on how 000004 was generated.
//
// Adding two tables must not touch the four that already exist. SQLite's 12-step rebuild is the one
// migration shape that silently drops every trigger attached to the old table — including the
// ledger's four append-only triggers, which is precisely the failure where the product's trust
// argument evaporates without a single test going red (.claude/rules/migrations.md, item V6).
//
// So this asserts the migration's TEXT, which is the only place the distinction is visible: a
// migration that adds tables contains no DROP and no rename of an existing one.
func TestMigrate_AuditAndOutbox_DidNotRebuildTheLedgerTables(t *testing.T) {
	t.Parallel()

	// The text half below needs no database, but the trigger-count half calls migratedDB, so the
	// whole test carries the same guard every other test in this package does. Without it a
	// `make test-unit` run — which is budgeted under five seconds — silently pays for a full
	// migration set, and the budget is what keeps the inner loop worth using.
	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	// Read from the EMBEDDED set rather than from disk: that is the artefact the binary ships and
	// applies, and a test that read db/migrations-sqlite/ directly would keep passing if go:embed
	// ever stopped picking the file up.
	body, err := fs.ReadFile(realMigrations(t), "000004_audit_and_outbox.sql")
	require.NoError(t, err, "the embedded migration set must contain 000004")

	up, _, found := strings.Cut(string(body), "-- +goose Down")
	require.True(t, found, "the migration must have a Down marker")

	upper := strings.ToUpper(up)

	require.NotContains(t, upper, "DROP TABLE",
		"000004 adds two tables and must not rebuild any existing one. A 12-step rebuild drops the "+
			"table's triggers and re-creates NOTHING, silently — which for ledger_batch or "+
			"ledger_entry means the append-only guarantee is gone with no test going red.")
	require.NotContains(t, upper, "ALTER TABLE",
		"000004 must not alter an existing table either; if that is genuinely needed, it is its own "+
			"migration with its own review")

	// And the four ledger triggers are still there afterwards, which is the property the assertions
	// above exist to protect. Checking the outcome as well as the mechanism means a future migration
	// that finds some other way to lose them still fails here.
	handle := openRaw(t, migratedDB(t))

	var triggers int
	require.NoError(t, handle.QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_schema
		 WHERE type = 'trigger' AND tbl_name IN ('ledger_batch', 'ledger_entry')`).Scan(&triggers))
	require.Equal(t, 4, triggers,
		"the ledger's four append-only triggers must survive every later migration")
}
