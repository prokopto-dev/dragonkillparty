package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// The upgrade path of a POPULATED ledger, which is the one thing every other migration test in this
// package leaves uncovered.
//
// TestMigrate_FreshInstall_MatchesFingerprint catches a trigger that was never created. It cannot
// catch a trigger that was created by 000003 and then dropped by a later 12-step table rebuild,
// because the fingerprint is computed from an EMPTY database that applied every migration in one
// go — the end state is the same either way only if the rebuild happens to re-create it, and if it
// does not, the fingerprint changes and someone regenerates the golden. Meanwhile
// TestMigrate_AuditAndOutbox_DidNotRebuildTheLedgerTables greps one specific migration's text for
// DROP TABLE, which says nothing about the next one. And restore_test.go's seed is dkp_meta: a
// table with no foreign keys, no triggers and no relationship to the ledger.
//
// So nothing in the repository has ever put real ledger rows in a database, applied a later
// migration on top of them, and looked at what came out. That gap is exactly the shape of the
// failure .claude/rules/migrations.md warns about — "the append-only guarantee is gone with no test
// going red" — and Phase 1 is when the schema starts moving.
//
// These tests import no domain package (docs/design/04-testing.md:541). The ids, the column names
// and the trigger names are restated as literals so a refactor inside internal/ledger cannot
// silently change what they claim a migration did to somebody's data.

// Seed identifiers. 26-character ULID-shaped TEXT keys, in the zero-padded style the migrations'
// own deterministic constants use, so a failure message points at an obviously synthetic row.
const (
	seedPoolID      = "0000000000000000000POOLR01"
	seedAccountID   = "000000000000000000ACCTPER1"
	seedPersonID    = "0000000000000000000PERSON1"
	seedBatch1ID    = "0000000000000000000BATCH01"
	seedBatch2ID    = "0000000000000000000BATCH02"
	seedEntry1ID    = "0000000000000000000ENTRY01"
	seedEntry2ID    = "0000000000000000000ENTRY02"
	seedEntry3ID    = "0000000000000000000ENTRY03"
	seedEntry4ID    = "0000000000000000000ENTRY04"
	seedBalanceKind = "dkp"

	// guildBankAccountID is seeded by 000003_ledger.sql. Referencing it makes the seeded batch
	// straddle a migration-created row and a test-created row, so a rebuild that dropped either
	// side's referent shows up as a foreign-key violation rather than as a silent orphan.
	guildBankAccountID = "0000000000000000DKPACCTBNK"
)

// Micros (int64 Unix microseconds), fixed rather than derived from the clock: a migration test that
// took the current time would produce a different database on every run and could not compare
// whole rows.
const (
	seedEffectiveAt  = int64(1_754_784_000_000_000) // 2025-08-10T00:00:00Z
	seedRecordedAt   = int64(1_754_784_060_000_000)
	seedEffectiveDay = "2025-08-10"
)

// seedHash stands in for a real chain hash. sha256 of a label, so it is 32 bytes, deterministic and
// obviously not a value anybody computed from batch contents.
func seedHash(label string) []byte {
	sum := sha256.Sum256([]byte(label))

	return sum[:]
}

// ledgerEntryRow is the part of a ledger_entry a migration must not disturb.
//
// Compared whole (go-cmp/require.Equal on the struct, per .claude/rules/go-idioms.md) rather than
// field by field: asserting three columns survived a table rebuild hides the fourth that did not.
type ledgerEntryRow struct {
	ID          string
	BatchID     string
	PoolID      string
	Seq         int64
	AccountID   string
	BalanceKind string
	AmountCP    int64
}

// ledgerBatchRow is the same idea for the batch header, including the hash chain: prev_hash and
// hash are the ledger's tamper-evidence, and a rebuild that retyped a blob column or copied the
// rows through a text round-trip would corrupt them while every count(*) stayed correct.
type ledgerBatchRow struct {
	ID          string
	PoolID      string
	Seq         int64
	Kind        string
	EntryCount  int64
	NetAmountCP int64
	PrevHash    []byte
	Hash        []byte
}

// wantEntries and wantBatches are what the seed puts in and what must come back out, unchanged, on
// the far side of a migration.
func wantEntries() []ledgerEntryRow {
	return []ledgerEntryRow{
		{seedEntry1ID, seedBatch1ID, seedPoolID, 1, seedAccountID, seedBalanceKind, -25_000},
		{seedEntry2ID, seedBatch1ID, seedPoolID, 1, guildBankAccountID, seedBalanceKind, 25_000},
		{seedEntry3ID, seedBatch2ID, seedPoolID, 2, seedAccountID, seedBalanceKind, 10_000},
		{seedEntry4ID, seedBatch2ID, seedPoolID, 2, guildBankAccountID, seedBalanceKind, -10_000},
	}
}

func wantBatches() []ledgerBatchRow {
	return []ledgerBatchRow{
		{seedBatch1ID, seedPoolID, 1, "adjustment", 2, 0, nil, seedHash("batch-1")},
		{seedBatch2ID, seedPoolID, 2, "award", 2, 0, seedHash("batch-1"), seedHash("batch-2")},
	}
}

// seedLedger writes a real, referentially complete ledger: a pool, a person account, two chained
// batches and their four entries.
//
// It goes through a handle with foreign keys ON (openRawFK) on purpose. Seeding with them off would
// let this write rows production could never have produced, and the whole point of the test is what
// a later migration does to rows that are genuinely constrained.
//
// Raw SQL rather than internal/ledger for the reason at the top of harness_test.go: a migration
// test that asked the domain package to write the rows would be asserting that today's writer and
// today's schema agree, which is not the question.
func seedLedger(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	ctx := context.Background()

	// Parent before child, every time. Under foreign_keys=ON any other order fails, and the failure
	// would be reported against the seed rather than against the migration under test.
	_, err := handle.ExecContext(ctx,
		`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version, balance_kinds, created_at, updated_at)
		 VALUES (?, 'Raid Night', 'raidnight', 'zero_sum', '1.0.0', 'dkp', ?, ?)`,
		seedPoolID, seedEffectiveAt, seedEffectiveAt)
	require.NoError(tb, err, "seed pool")

	// kind='person' requires person_id NOT NULL and system_key NULL (account_person_shape and
	// account_system_shape). There is no person table yet, so person_id is an unreferenced ULID.
	_, err = handle.ExecContext(ctx,
		`INSERT INTO account (id, kind, person_id, system_key, label, created_at, updated_at)
		 VALUES (?, 'person', ?, NULL, 'Fippy Darkpaw', ?, ?)`,
		seedAccountID, seedPersonID, seedEffectiveAt, seedEffectiveAt)
	require.NoError(tb, err, "seed person account")

	insertBatch := func(b ledgerBatchRow, reason string) {
		_, execErr := handle.ExecContext(ctx,
			`INSERT INTO ledger_batch (
				id, pool_id, seq, kind, strategy_id, strategy_version, config_snapshot_json,
				source, actor_is_beneficiary, reason, effective_at, recorded_at, effective_day,
				entry_count, net_amount_cp, prev_hash, hash)
			 VALUES (?, ?, ?, ?, 'zero_sum', '1.0.0', '{}', 'web', 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.PoolID, b.Seq, b.Kind, reason,
			seedEffectiveAt, seedRecordedAt, seedEffectiveDay,
			b.EntryCount, b.NetAmountCP, b.PrevHash, b.Hash)
		require.NoError(tb, execErr, "seed ledger_batch %s", b.ID)
	}

	batches := wantBatches()
	insertBatch(batches[0], "loot charge, seeded by the migration suite")
	insertBatch(batches[1], "award funded from the guild bank, seeded by the migration suite")

	for _, e := range wantEntries() {
		_, execErr := handle.ExecContext(ctx,
			`INSERT INTO ledger_entry (id, batch_id, pool_id, seq, account_id, balance_kind, amount_cp, metadata_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '{}')`,
			e.ID, e.BatchID, e.PoolID, e.Seq, e.AccountID, e.BalanceKind, e.AmountCP)
		require.NoError(tb, execErr, "seed ledger_entry %s", e.ID)
	}

	// The seed is only worth anything if it landed. A silently empty ledger satisfies every
	// "the data survived" assertion below.
	require.Equal(tb, wantBatches(), readBatches(tb, handle), "the seed did not take")
	require.Equal(tb, wantEntries(), readEntries(tb, handle), "the seed did not take")
}

// readBatches reads the batch headers in seq order.
func readBatches(tb testing.TB, handle *sql.DB) []ledgerBatchRow {
	tb.Helper()

	rows, err := handle.QueryContext(context.Background(),
		`SELECT id, pool_id, seq, kind, entry_count, net_amount_cp, prev_hash, hash
		 FROM ledger_batch WHERE pool_id = ? ORDER BY seq`, seedPoolID)
	require.NoError(tb, err, "read ledger_batch")

	defer func() { require.NoError(tb, rows.Close()) }()

	var out []ledgerBatchRow

	for rows.Next() {
		var b ledgerBatchRow
		require.NoError(tb, rows.Scan(&b.ID, &b.PoolID, &b.Seq, &b.Kind, &b.EntryCount, &b.NetAmountCP, &b.PrevHash, &b.Hash))
		out = append(out, b)
	}

	require.NoError(tb, rows.Err())

	return out
}

// readEntries reads the seeded entries in id order.
func readEntries(tb testing.TB, handle *sql.DB) []ledgerEntryRow {
	tb.Helper()

	rows, err := handle.QueryContext(context.Background(),
		`SELECT id, batch_id, pool_id, seq, account_id, balance_kind, amount_cp
		 FROM ledger_entry WHERE pool_id = ? ORDER BY id`, seedPoolID)
	require.NoError(tb, err, "read ledger_entry")

	defer func() { require.NoError(tb, rows.Close()) }()

	var out []ledgerEntryRow

	for rows.Next() {
		var e ledgerEntryRow
		require.NoError(tb, rows.Scan(&e.ID, &e.BatchID, &e.PoolID, &e.Seq, &e.AccountID, &e.BalanceKind, &e.AmountCP))
		out = append(out, e)
	}

	require.NoError(tb, rows.Err())

	return out
}

// ledgerTriggerNames lists the triggers currently attached to the ledger's two tables.
func ledgerTriggerNames(tb testing.TB, handle *sql.DB) []string {
	tb.Helper()

	rows, err := handle.QueryContext(context.Background(),
		`SELECT name FROM sqlite_schema
		 WHERE type = 'trigger' AND tbl_name IN ('ledger_batch', 'ledger_entry') ORDER BY name`)
	require.NoError(tb, err, "read ledger triggers from sqlite_schema")

	defer func() { require.NoError(tb, rows.Close()) }()

	var out []string

	for rows.Next() {
		var n string
		require.NoError(tb, rows.Scan(&n))
		out = append(out, n)
	}

	require.NoError(tb, rows.Err())

	return out
}

// foreignKeyViolations counts what PRAGMA foreign_key_check finds.
//
// A 12-step rebuild that copied rows in the wrong order, or dropped a parent, leaves dangling
// references that PRAGMA integrity_check calls perfectly healthy — so this is a separate question
// from "is the file corrupt", and it is the one that matters to a ledger.
func foreignKeyViolations(tb testing.TB, handle *sql.DB) int {
	tb.Helper()

	rows, err := handle.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	require.NoError(tb, err, "run PRAGMA foreign_key_check")

	defer func() { require.NoError(tb, rows.Close()) }()

	n := 0
	for rows.Next() {
		n++
	}

	require.NoError(tb, rows.Err())

	return n
}

// requireAppendOnlyTriggersFire is the behavioural half: not "a trigger exists" but "a mutation is
// refused".
//
// A trigger row in sqlite_schema whose body no longer aborts is indistinguishable from a working one
// to a name query, and both halves are cheap. Each statement runs in autocommit, so an abort leaves
// nothing open behind it.
func requireAppendOnlyTriggersFire(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	ctx := context.Background()

	mutations := []struct {
		name    string
		stmt    string
		args    []any
		message string
	}{
		{
			name:    "trg_ledger_batch_no_update",
			stmt:    `UPDATE ledger_batch SET reason = 'edited' WHERE id = ?`,
			args:    []any{seedBatch1ID},
			message: "ledger_batch is append-only",
		},
		{
			name:    "trg_ledger_batch_no_delete",
			stmt:    `DELETE FROM ledger_batch WHERE id = ?`,
			args:    []any{seedBatch1ID},
			message: "ledger_batch is append-only",
		},
		{
			name:    "trg_ledger_entry_no_update",
			stmt:    `UPDATE ledger_entry SET amount_cp = 1 WHERE id = ?`,
			args:    []any{seedEntry1ID},
			message: "ledger_entry is append-only",
		},
		{
			name:    "trg_ledger_entry_no_delete",
			stmt:    `DELETE FROM ledger_entry WHERE id = ?`,
			args:    []any{seedEntry1ID},
			message: "ledger_entry is append-only",
		},
	}

	for _, m := range mutations {
		_, err := handle.ExecContext(ctx, m.stmt, m.args...)
		require.Error(tb, err,
			"%s did not fire: `%s` SUCCEEDED against a populated ledger. The append-only guarantee "+
				"is the product's entire trust argument and it is now gone.", m.name, m.stmt)
		require.ErrorContains(tb, err, m.message,
			"%s aborted with the wrong message; the RAISE text is what an operator reads", m.name)
	}
}

// TestMigrate_FullStack_LedgerDataSurvivesUpgrade migrates a POPULATED ledger across a version
// boundary and proves the append-only triggers survive a table rebuild.
//
// The shape:
//
//	(a) apply the real migration set to an empty database;
//	(b) seed a real pool, account, two chained ledger_batch rows and their four ledger_entry rows,
//	    through a connection with foreign keys enforced;
//	(c) apply a later migration that REBUILDS ledger_entry — SQLite's 12-step create/copy/drop/
//	    rename, which is what any change ALTER TABLE cannot express becomes;
//	(d) assert the rows are still there and unchanged, the hash chain still links, no foreign key
//	    dangles, and all four append-only triggers still RAISE(ABORT).
//
// (c) is the load-bearing step and the reason a fixture is used rather than the real migration set:
// no shipped migration rebuilds a ledger table yet, so there is nothing in db/migrations-sqlite/ to
// point this at. The fixture is a faithful copy of what Atlas emits for that change, plus the
// hand-appended trigger re-creation that .claude/rules/migrations.md case 1 requires — which is
// precisely the line a real migration will omit, because Atlas cannot see a trigger and so never
// mentions one.
//
// Its negative control lives next door: TestMigrate_FullStack_ForgetfulRebuildLosesTheTriggers
// applies the same rebuild with that hand-edit removed and watches this test's assertions become
// false. Without that pair, "the triggers still fire" is an assertion nobody has ever seen fail.
func TestMigrate_FullStack_LedgerDataSurvivesUpgrade(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	// (a) The release the officer is upgrading FROM.
	installed, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, installed.Migrate(t.Context()), "the real migration set must apply to an empty database")

	// (b) Ten years of guild history, in miniature.
	seedLedger(t, openRawFK(t, dbPath))

	// (c) The release they are upgrading TO. migrationDir renumbers the fixture to one past the
	// highest real migration, so this stays correct as Phase 1 adds migrations ahead of it.
	upgraded, err := migrate.New(migrationDir(t, ledgerRebuildFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, upgraded.Migrate(t.Context()),
		"the upgrade failed on a populated database. A migration that applies to an empty database "+
			"and not to a real one is the most damaging bug class this product has.")

	handle := openRawFK(t, dbPath)

	// (d) The data. Whole rows, not counts: a rebuild that copied the columns in the wrong order
	// produces exactly the right number of exactly wrong rows.
	require.Equal(t, wantEntries(), readEntries(t, handle),
		"ledger entries did not survive the rebuild unchanged")
	require.Equal(t, wantBatches(), readBatches(t, handle),
		"ledger batches did not survive the rebuild unchanged — note prev_hash and hash, which are "+
			"the tamper-evidence and would be silently mangled by a text round-trip")

	// The chain still links. Asserted separately from the row comparison because it is the property
	// a reader cares about, and a message naming it beats a struct diff.
	batches := readBatches(t, handle)
	require.Len(t, batches, 2)
	require.Nil(t, batches[0].PrevHash, "the genesis batch has no predecessor")
	require.Equal(t, batches[0].Hash, batches[1].PrevHash,
		"the hash chain is broken: batch 2 no longer points at batch 1")

	require.Equal(t, 0, foreignKeyViolations(t, handle),
		"the rebuild left dangling references — PRAGMA integrity_check would still call this "+
			"database healthy")

	// The parents the entries hang off are still there. A rebuild that took a referenced row with
	// it would show up above as a violation, but naming the rows makes the failure readable.
	var pools, accounts int
	require.NoError(t, handle.QueryRowContext(t.Context(),
		`SELECT count(*) FROM pool WHERE id = ?`, seedPoolID).Scan(&pools))
	require.Equal(t, 1, pools, "the seeded pool is gone")
	require.NoError(t, handle.QueryRowContext(t.Context(),
		`SELECT count(*) FROM account WHERE id IN (?, ?)`, seedAccountID, guildBankAccountID).Scan(&accounts))
	require.Equal(t, 2, accounts, "a seeded account is gone")

	// The guarantee itself, both halves. The names first, so a missing trigger reports as a missing
	// trigger rather than as a mutation that unexpectedly succeeded.
	require.Equal(t,
		[]string{
			"trg_ledger_batch_no_delete",
			"trg_ledger_batch_no_update",
			"trg_ledger_entry_no_delete",
			"trg_ledger_entry_no_update",
		},
		ledgerTriggerNames(t, handle),
		"a ledger trigger did not survive the table rebuild. SQLite's DROP TABLE takes every trigger "+
			"attached to the table with it and re-creates NOTHING; the migration must re-create them "+
			"after the rename (.claude/rules/migrations.md case 1).")

	requireAppendOnlyTriggersFire(t, handle)

	// And the refused mutations refused: an abort that rolled back cleanly leaves the ledger exactly
	// as it was. A trigger that fires but lets a partial write through is worse than no trigger.
	require.Equal(t, wantEntries(), readEntries(t, handle),
		"the ledger changed while the append-only triggers were rejecting mutations")
	require.Equal(t, wantBatches(), readBatches(t, handle),
		"the ledger changed while the append-only triggers were rejecting mutations")
}

// TestMigrate_FullStack_ForgetfulRebuildLosesTheTriggers is the negative control for the test above,
// and it is the finding itself, reproduced.
//
// It applies the identical rebuild with the two CREATE TRIGGER statements removed — the one line a
// real migration omits, because Atlas cannot express a trigger and therefore never emits one — and
// asserts what actually happens:
//
//   - the migration SUCCEEDS. No error, no warning;
//   - PRAGMA integrity_check and PRAGMA foreign_key_check both pass (the runner runs them after
//     every migration and this run did not fail);
//   - every ledger row is still present and correct;
//   - and the ledger is now editable. UPDATE and DELETE on ledger_entry succeed.
//
// That combination is why this is a must-fix rather than a nice-to-have: every check the product
// currently runs on an upgrade reports success, and the append-only guarantee is gone.
//
// This test failing means one of two things. Either the rebuild fixture was "helpfully" repaired,
// in which case the control controls for nothing and the test above proves nothing — restore the
// fixture rather than this assertion. Or SQLite changed such that DROP TABLE now preserves or
// re-raises triggers, which would be excellent news and a deliberate rewrite of both tests.
func TestMigrate_FullStack_ForgetfulRebuildLosesTheTriggers(t *testing.T) {
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

	seedLedger(t, openRawFK(t, dbPath))

	upgraded, err := migrate.New(migrationDir(t, ledgerRebuildNoTriggersFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, upgraded.Migrate(t.Context()),
		"the forgetful rebuild must APPLY CLEANLY — that is the finding. If it started failing, the "+
			"boot path grew a check that catches this, and the test above should be re-read in that "+
			"light rather than this one being deleted.")

	handle := openRawFK(t, dbPath)

	// The data is fine. That is what makes this invisible: nobody loses a row.
	require.Equal(t, wantEntries(), readEntries(t, handle),
		"the forgetful rebuild also lost data, which would make it a LOUD failure and a poor control")
	require.Equal(t, 0, foreignKeyViolations(t, handle))

	// The two triggers on the rebuilt table are gone. The two on ledger_batch, which this migration
	// never touched, are untouched — so the loss is scoped to the rebuilt table, which is what makes
	// the positive test's exact four-name assertion the right assertion.
	require.Equal(t,
		[]string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
		ledgerTriggerNames(t, handle),
		"expected DROP TABLE to have taken ledger_entry's two triggers and left ledger_batch's alone")

	// And the consequence, stated as behaviour rather than as schema. This is the only place in the
	// repository where a ledger row is mutated, and it is a throwaway database in t.TempDir()
	// demonstrating the disaster the trigger prevents.
	res, err := handle.ExecContext(t.Context(),
		`UPDATE ledger_entry SET amount_cp = 999999 WHERE id = ?`, seedEntry1ID)
	require.NoError(t, err,
		"the UPDATE was refused, so ledger_entry's trigger survived a rebuild that never re-created "+
			"it — re-read the fixture: it must not contain a CREATE TRIGGER")

	affected, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected, "history was rewritten and the database reported success")

	res, err = handle.ExecContext(t.Context(), `DELETE FROM ledger_entry WHERE id = ?`, seedEntry2ID)
	require.NoError(t, err, "the DELETE was refused; see above")

	affected, err = res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected, "a ledger entry was deleted and the database reported success")

	// ledger_batch, whose triggers this migration left alone, still refuses. Proof that the loss is
	// the rebuild's doing and not something about this test's connection.
	_, err = handle.ExecContext(t.Context(),
		`UPDATE ledger_batch SET reason = 'edited' WHERE id = ?`, seedBatch1ID)
	require.ErrorContains(t, err, "ledger_batch is append-only",
		"ledger_batch's triggers were never dropped and must still fire")
}
