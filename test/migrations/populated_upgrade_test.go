package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
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
	seedUserID      = "0000000000000000000USER001"
	seedTokenID     = "000000000000000000TOKEN001"
	seedCharacterID = "0000000000000000000CHAR001"
	seedItemID      = "0000000000000000000ITEM001"
	seedAwardID     = "000000000000000000AWARD001"
	seedRaidID      = "0000000000000000000RAID001"
	seedTickID      = "0000000000000000000TICK001"
	seedBalanceKind = "dkp"

	// guildBankAccountID is seeded by 000003_ledger.sql. Referencing it makes the seeded batch
	// straddle a migration-created row and a test-created row, so a rebuild that dropped either
	// side's referent shows up as a foreign-key violation rather than as a silent orphan.
	guildBankAccountID = "0000000000000000DKPACCTBNK"
)

// Micros (int64 Unix microseconds), fixed rather than derived from the clock: a migration test that
// took the current time would produce a different database on every run and could not compare rows.
//
// effective_at and recorded_at differ, and differ per batch. Seeding them equal would make a rebuild
// that transposed the two columns invisible — which is exactly the class of corruption a projection
// of "the interesting columns" fails to notice.
const (
	seedBatch1EffectiveAt = int64(1_754_784_000_000_000) // 2025-08-10T00:00:00Z
	seedBatch1RecordedAt  = int64(1_754_784_060_000_000)
	seedBatch1Day         = "2025-08-10"
	seedBatch2EffectiveAt = int64(1_754_870_400_000_000) // 2025-08-11T00:00:00Z
	seedBatch2RecordedAt  = int64(1_754_870_460_000_000)
	seedBatch2Day         = "2025-08-11"
)

// seedHash stands in for a real chain hash. sha256 of a label, so it is 32 bytes, deterministic and
// obviously not a value anybody computed from batch contents.
func seedHash(label string) []byte {
	sum := sha256.Sum256([]byte(label))

	return sum[:]
}

// tableSnapshot is every column of every seeded row in one table, keyed by id.
//
// A map of column name to value rather than a struct, and read with SELECT *, because a struct is a
// PROJECTION and a projection is exactly the wrong oracle here. An earlier version of this test
// compared 7 of ledger_entry's 13 columns and 8 of ledger_batch's 23: a rebuild could have reset
// every metadata_json, transposed effective_at and recorded_at, blanked reason and
// config_snapshot_json, or dropped an unlisted column outright, and the test would have called the
// ledger intact. Reading the columns the DATABASE reports also means a column added by a future
// migration is covered from the moment it exists, with nobody remembering to add a field here.
type tableSnapshot map[string]map[string]any

// ledgerSnapshot is the append-only pair, together.
type ledgerSnapshot struct {
	batches tableSnapshot
	entries tableSnapshot
}

// snapshotLedger captures every column of every seeded ledger row.
func snapshotLedger(tb testing.TB, handle *sql.DB) ledgerSnapshot {
	tb.Helper()

	snap := ledgerSnapshot{
		batches: snapshotTable(tb, handle,
			`SELECT * FROM ledger_batch WHERE pool_id = ? ORDER BY id`, seedPoolID),
		entries: snapshotTable(tb, handle,
			`SELECT * FROM ledger_entry WHERE pool_id = ? ORDER BY id`, seedPoolID),
	}

	// An empty snapshot compares equal to an empty snapshot. Pinning the counts means a migration
	// that removed every seeded row cannot pass the comparison by leaving nothing to compare.
	require.Len(tb, snap.batches, 2, "expected the two seeded ledger_batch rows")
	require.Len(tb, snap.entries, 4, "expected the four seeded ledger_entry rows")

	return snap
}

// snapshotTable reads whatever columns the table currently has, for the rows the query selects.
func snapshotTable(tb testing.TB, handle *sql.DB, query string, args ...any) tableSnapshot {
	tb.Helper()

	rows, err := handle.QueryContext(context.Background(), query, args...)
	require.NoError(tb, err, "snapshot query: %s", query)

	defer func() { require.NoError(tb, rows.Close()) }()

	columns, err := rows.Columns()
	require.NoError(tb, err, "read column names for: %s", query)
	require.NotEmpty(tb, columns, "the table has no columns")

	out := tableSnapshot{}

	for rows.Next() {
		// Scanned into `any`, so the driver's own type for each column survives into the
		// comparison: a rebuild that retyped an INTEGER column to TEXT changes int64 to string and
		// fails, which is the correct outcome for a change that needs a human.
		values := make([]any, len(columns))
		targets := make([]any, len(columns))

		for i := range values {
			targets[i] = &values[i]
		}

		require.NoError(tb, rows.Scan(targets...), "scan row for: %s", query)

		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}

		id, ok := row["id"].(string)
		require.True(tb, ok, "every snapshotted table needs a TEXT id column; got %T", row["id"])

		out[id] = row
	}

	require.NoError(tb, rows.Err())

	return out
}

// requireLedgerUnchanged is the row-survival oracle: every column that existed before a migration
// still exists after it, on the same rows, holding the same value.
//
// Columns the migration ADDED are deliberately not compared — adding a column is what migrations
// are for, and the fixture rebuild in this file adds one. Columns it REMOVED fail, because dropping
// a column a user's database already holds is a destructive change that needs a human.
func requireLedgerUnchanged(tb testing.TB, before, after ledgerSnapshot, stage string) {
	tb.Helper()

	requireTableUnchanged(tb, "ledger_batch", before.batches, after.batches, stage)
	requireTableUnchanged(tb, "ledger_entry", before.entries, after.entries, stage)
}

func requireTableUnchanged(tb testing.TB, table string, before, after tableSnapshot, stage string) {
	tb.Helper()

	require.Equal(tb, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)),
		"the set of %s rows changed across %s. The ledger is append-only: a migration may not add, "+
			"remove or re-key a committed row.", table, stage)

	for _, id := range slices.Sorted(maps.Keys(before)) {
		was, is := before[id], after[id]

		for _, column := range slices.Sorted(maps.Keys(was)) {
			require.Contains(tb, is, column,
				"%s.%s was DROPPED by %s, taking row %s's stored value with it. Removing a column a "+
					"user's database already holds is a destructive migration: it needs the "+
					"!destructive-migration label and a human (.claude/rules/migrations.md).",
				table, column, stage, id)
			require.Equal(tb, was[column], is[column],
				"%s.%s changed on row %s across %s. Nothing may rewrite a committed ledger row — not "+
					"a backfill, not a table rebuild. A correction is a reversal batch, never an edit "+
					"to history.", table, column, id, stage)
		}
	}
}

// seedLedger writes a real, referentially complete ledger: a pool, a person account, a batch and its
// reversal, and their four entries.
//
// Every column is given a DISTINCT, NON-DEFAULT value wherever the schema allows one, and every
// nullable column appears both NULL and non-NULL across the four entries. That is what makes
// requireLedgerUnchanged able to fail: a rebuild that reset config_snapshot_json to its '{}' default
// would be invisible against a row that was already '{}', and a rebuild that turned NULL into ”
// would be invisible against a row that had no NULLs.
//
// It goes through a handle with foreign keys ON (openRawFK/withRawFK) on purpose. Seeding with them
// off would let this write rows production could never have produced, and the whole point of the
// test is what a later migration does to rows that are genuinely constrained.
//
// Raw SQL rather than internal/ledger for the reason at the top of harness_test.go: a migration test
// that asked the domain package to write the rows would be asserting that today's writer and today's
// schema agree, which is not the question.
func seedLedger(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	ctx := context.Background()

	// Parent before child, every time. Under foreign_keys=ON any other order fails, and the failure
	// would be reported against the seed rather than against the migration under test.
	_, err := handle.ExecContext(ctx,
		`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version, balance_kinds, created_at, updated_at)
		 VALUES (?, 'Raid Night', 'raidnight', 'zero_sum', '1.4.2', 'dkp', ?, ?)`,
		seedPoolID, seedBatch1EffectiveAt, seedBatch1EffectiveAt)
	require.NoError(tb, err, "seed pool")

	// kind='person' requires person_id NOT NULL and system_key NULL (account_person_shape and
	// account_system_shape). There is no person table yet, so person_id is an unreferenced ULID.
	_, err = handle.ExecContext(ctx,
		`INSERT INTO account (id, kind, person_id, system_key, label, created_at, updated_at)
		 VALUES (?, 'person', ?, NULL, 'Fippy Darkpaw', ?, ?)`,
		seedAccountID, seedPersonID, seedBatch1EffectiveAt, seedBatch1EffectiveAt)
	require.NoError(tb, err, "seed person account")

	const insertBatch = `
		INSERT INTO ledger_batch (
			id, pool_id, seq, kind, strategy_id, strategy_version, config_snapshot_json, rng_seed,
			source, source_ref, actor_user_id, actor_token_id, actor_is_beneficiary, reason,
			reverses_batch_id, effective_at, recorded_at, effective_day, idempotency_key,
			entry_count, net_amount_cp, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// A loot charge. rng_seed set, actor is a user, no reversal, genesis of the hash chain.
	_, err = handle.ExecContext(ctx, insertBatch,
		seedBatch1ID, seedPoolID, 1, "adjustment", "zero_sum", "1.4.2", `{"decay_bp":250}`, 7_420_147,
		"web", "eqdkp:seedguild:adjustment:11", seedUserID, nil, 0,
		"loot charge for Cloak of Flames, seeded by the migration suite",
		nil, seedBatch1EffectiveAt, seedBatch1RecordedAt, seedBatch1Day, "seed-idem-batch-1",
		2, 0, nil, seedHash("batch-1"))
	require.NoError(tb, err, "seed ledger_batch %s", seedBatch1ID)

	// Its reversal. Every nullable column that batch 1 filled is NULL here and vice versa, and
	// actor_is_beneficiary=1 exercises the ix_batch_selfdeal partial index.
	_, err = handle.ExecContext(ctx, insertBatch,
		seedBatch2ID, seedPoolID, 2, "reversal", "zero_sum", "1.4.2", `{"decay_bp":250}`, nil,
		"api", "eqdkp:seedguild:reversal:12", nil, seedTokenID, 1,
		"reversal: wrong buyer, seeded by the migration suite",
		seedBatch1ID, seedBatch2EffectiveAt, seedBatch2RecordedAt, seedBatch2Day, "seed-idem-batch-2",
		2, 0, seedHash("batch-1"), seedHash("batch-2"))
	require.NoError(tb, err, "seed ledger_batch %s", seedBatch2ID)

	const insertEntry = `
		INSERT INTO ledger_entry (
			id, batch_id, pool_id, seq, account_id, character_id, balance_kind, amount_cp,
			item_id, item_award_id, raid_id, tick_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	entries := []struct {
		id, batchID, accountID string
		seq                    int64
		characterID            any
		amountCP               int64
		itemID, awardID        any
		raidID, tickID         any
		metadata               string
	}{
		{
			seedEntry1ID, seedBatch1ID, seedAccountID, 1, seedCharacterID, -25_000,
			nil, nil, seedRaidID, nil, `{"seeded":"entry-1"}`,
		},
		{
			seedEntry2ID, seedBatch1ID, guildBankAccountID, 1, nil, 25_000,
			seedItemID, seedAwardID, nil, seedTickID, `{"seeded":"entry-2"}`,
		},
		{
			seedEntry3ID, seedBatch2ID, seedAccountID, 2, seedCharacterID, 25_000,
			seedItemID, seedAwardID, seedRaidID, seedTickID, `{"seeded":"entry-3"}`,
		},
		{
			seedEntry4ID, seedBatch2ID, guildBankAccountID, 2, nil, -25_000,
			nil, nil, nil, nil, `{"seeded":"entry-4"}`,
		},
	}

	for _, e := range entries {
		_, execErr := handle.ExecContext(ctx, insertEntry,
			e.id, e.batchID, seedPoolID, e.seq, e.accountID, e.characterID, seedBalanceKind,
			e.amountCP, e.itemID, e.awardID, e.raidID, e.tickID, e.metadata)
		require.NoError(tb, execErr, "seed ledger_entry %s", e.id)
	}
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

// ledgerTablesExist reports whether the migrations applied so far have created the ledger.
//
// Used to find the seed point by asking the database rather than by hard-coding "after 000003": the
// number is a fact about the current tree, and a test that restates it acquires a second source of
// truth that nothing keeps in step.
func ledgerTablesExist(tb testing.TB, handle *sql.DB) bool {
	tb.Helper()

	var n int
	require.NoError(tb, handle.QueryRowContext(context.Background(),
		`SELECT count(*) FROM sqlite_schema
		 WHERE type = 'table' AND name IN ('pool', 'account', 'ledger_batch', 'ledger_entry')`).Scan(&n))

	return n == 4
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

// requireLedgerIntact is every guarantee a migration must not break, in one call so that it can be
// made after EVERY migration rather than only at the end. It returns the post-migration snapshot,
// which becomes the baseline for the NEXT migration — so a column introduced by migration N is
// compared across migration N+1.
//
// stage names what was just applied, because "the ledger is broken" is a much less useful failure
// than "the ledger is broken after 000007_add_bid_hold.sql".
func requireLedgerIntact(tb testing.TB, handle *sql.DB, before ledgerSnapshot, stage string) ledgerSnapshot {
	tb.Helper()

	after := snapshotLedger(tb, handle)

	// Every column of every row, not a projection. This is the row-survival oracle.
	requireLedgerUnchanged(tb, before, after, stage)

	// The chain still links. Implied by the comparison above, but asserted by name because it is
	// the property a reader cares about and a message naming it beats a map diff.
	require.Nil(tb, after.batches[seedBatch1ID]["prev_hash"], "the genesis batch has no predecessor")
	require.Equal(tb, after.batches[seedBatch1ID]["hash"], after.batches[seedBatch2ID]["prev_hash"],
		"the hash chain is broken after %s: batch 2 no longer points at batch 1", stage)

	require.Equal(tb, 0, foreignKeyViolations(tb, handle),
		"%s left dangling references — PRAGMA integrity_check would still call this database healthy",
		stage)

	// The parents the entries hang off are still there. A migration that took a referenced row with
	// it would show up above as a violation, but naming the rows makes the failure readable.
	//
	// Presence only, deliberately: pool and account are NOT append-only. A backfill that populates
	// name_norm for existing rows is a legitimate migration (.claude/rules/migrations.md case 4), so
	// asserting their columns never change would fire on correct work. The ledger's two tables get
	// the strict treatment precisely because nothing may ever rewrite them.
	var pools, accounts int
	require.NoError(tb, handle.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pool WHERE id = ?`, seedPoolID).Scan(&pools))
	require.Equal(tb, 1, pools, "the seeded pool is gone after %s", stage)
	require.NoError(tb, handle.QueryRowContext(context.Background(),
		`SELECT count(*) FROM account WHERE id IN (?, ?)`, seedAccountID, guildBankAccountID).Scan(&accounts))
	require.Equal(tb, 2, accounts, "a seeded account is gone after %s", stage)

	// The guarantee itself, both halves. The names first, so a missing trigger reports as a missing
	// trigger rather than as a mutation that unexpectedly succeeded.
	require.Equal(tb,
		[]string{
			"trg_ledger_batch_no_delete",
			"trg_ledger_batch_no_update",
			"trg_ledger_entry_no_delete",
			"trg_ledger_entry_no_update",
		},
		ledgerTriggerNames(tb, handle),
		"a ledger trigger did not survive %s. SQLite's DROP TABLE takes every trigger attached to "+
			"the table with it and re-creates NOTHING; a migration that rebuilds a ledger table must "+
			"re-create its triggers after the rename, in the same file "+
			"(.claude/rules/migrations.md case 1).", stage)

	requireAppendOnlyTriggersFire(tb, handle)

	// And the refused mutations refused: an abort that rolled back cleanly leaves the ledger exactly
	// as it was. A trigger that fires but lets a partial write through is worse than no trigger.
	requireLedgerUnchanged(tb, after, snapshotLedger(tb, handle),
		"the append-only triggers rejecting mutations")

	return after
}

// TestMigrate_FullStack_LedgerDataSurvivesUpgrade walks a POPULATED ledger forward through every
// real migration that lands after the ledger exists, and then through a table rebuild.
//
// The shape, and the order is the whole point:
//
//	(a) install the earliest release whose migrations create the ledger — NOT the whole set;
//	(b) seed a real pool, account, a batch and its reversal and their four entries, through a
//	    connection with foreign keys enforced, giving every column a distinct non-default value;
//	(c) apply each REMAINING REAL migration, one at a time, requiring after every single one that
//	    EVERY COLUMN of every ledger row is byte-for-byte what it was before that migration, that
//	    the hash chain links, that no foreign key dangles, and that all four append-only triggers
//	    are present and still RAISE(ABORT);
//	(d) then apply a fixture migration that REBUILDS ledger_entry — SQLite's create/copy/drop/
//	    rename, which is what any change ALTER TABLE cannot express becomes — and require the same
//	    things again.
//
// (a) and (c) are what make this a gate on FUTURE migrations rather than a story about a fixture.
// Seeding after the whole set, which is what an earlier draft did, means every real migration runs
// against an empty database and the fixture in (d) — which re-creates the triggers unconditionally —
// would REPAIR whatever a future real rebuild had just broken. The test would stay green while the
// regression it exists to catch shipped.
//
// The baseline for each comparison is the state after the PREVIOUS migration, not the seed, so a
// column introduced by migration N is covered across migration N+1 without anybody adding it here.
//
// (d) is still worth doing separately, because no shipped migration rebuilds a ledger table yet:
// there is nothing in db/migrations-sqlite/ to point (c) at that exercises the dangerous shape. The
// fixture is a faithful copy of what Atlas emits for such a change, plus the hand-appended trigger
// re-creation .claude/rules/migrations.md case 1 requires — which is precisely the line a real
// migration will omit, because Atlas cannot see a trigger and so never mentions one.
//
// This is NOT the N-1 upgrade ladder. That one (`make test-upgrade`) starts from the previous
// release's published reference database and is Phase 8, blocked on `release-refdb`. This walks the
// in-repo migration set, which is the part that can be gated today and the part a new migration
// actually changes.
//
// Its negative control lives next door: TestMigrate_FullStack_ForgetfulRebuildLosesTheTriggers
// applies the same rebuild with that hand-edit removed and watches these assertions become false.
// Without that pair, "the triggers still fire" is an assertion nobody has ever seen fail.
func TestMigrate_FullStack_LedgerDataSurvivesUpgrade(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	// One release per migration. Distinct versions rather than a constant so the snapshot the
	// migrator takes on each step gets its own name, and so a failure message says which release
	// the officer was on.
	release := func(version int) string { return fmt.Sprintf("v0.%d.0", version) }

	var (
		baseline ledgerSnapshot
		seededAt int
		checked  []int
	)

	for _, version := range realMigrationVersions(t) {
		// (a)/(c) The set truncated to this release. Each pass has exactly one migration pending,
		// so the checks below attribute a failure to a single file.
		runner, err := migrate.New(migrationDirUpTo(t, version), migrate.Config{
			DBPath: dbPath, DataDir: dataDir, BinaryVersion: release(version), AutoMigrate: true,
		})
		require.NoError(t, err)
		require.NoError(t, runner.Migrate(t.Context()),
			"migration %06d failed to apply. If the ledger was already seeded at this point, this is "+
				"the headline failure: a migration that applies to an empty database and not to a "+
				"real one is the most damaging bug class this product has.", version)

		withRawFK(t, dbPath, func(handle *sql.DB) {
			if seededAt == 0 {
				if !ledgerTablesExist(t, handle) {
					return // Too early to hold an opinion: there is no ledger yet.
				}

				// (b) Ten years of guild history, in miniature, at the earliest release that can
				// hold it.
				seedLedger(t, handle)

				seededAt = version
				baseline = snapshotLedger(t, handle)

				return
			}

			baseline = requireLedgerIntact(t, handle, baseline,
				fmt.Sprintf("real migration %06d", version))

			checked = append(checked, version)
		})
	}

	require.Positive(t, seededAt,
		"no migration created pool, account, ledger_batch and ledger_entry, so nothing was seeded "+
			"and every assertion below is vacuous")

	// Not a style check. If the ledger's own migration is the LAST one, this test seeds and then
	// checks nothing, and it would have quietly stopped being the upgrade gate it claims to be.
	// 000003_ledger.sql is shipped and frozen and 000004 already exists, so this can only ever grow.
	require.NotEmpty(t, checked,
		"the ledger tables are created by the last real migration, so no real migration was "+
			"exercised against populated data")

	t.Logf("seeded at migration %06d; checked a populated ledger across real migrations %v", seededAt, checked)

	// (d) The dangerous shape, which no real migration has taken yet. migrationDir renumbers the
	// fixture to one past the highest real migration, so this stays correct as Phase 1 adds
	// migrations ahead of it.
	upgraded, err := migrate.New(migrationDir(t, ledgerRebuildFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, upgraded.Migrate(t.Context()),
		"the ledger_entry rebuild failed on a populated database")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		after := requireLedgerIntact(t, handle, baseline, "the ledger_entry table rebuild")

		// The rebuild's whole reason for existing: it ADDS a column. Asserting that it arrived
		// proves requireLedgerUnchanged tolerated a legitimate addition rather than passing because
		// the migration did nothing.
		require.Contains(t, after.entries[seedEntry1ID], "note",
			"the rebuild fixture did not add its new column, so this step proved nothing about a "+
				"migration that changes the ledger's shape")
	})
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

	var baseline ledgerSnapshot

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedLedger(t, handle)
		baseline = snapshotLedger(t, handle)
	})

	upgraded, err := migrate.New(migrationDir(t, ledgerRebuildNoTriggersFixture), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.1.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, upgraded.Migrate(t.Context()),
		"the forgetful rebuild must APPLY CLEANLY — that is the finding. If it started failing, the "+
			"boot path grew a check that catches this, and the test above should be re-read in that "+
			"light rather than this one being deleted.")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		// The data is fine, every column of it. That is what makes this invisible: nobody loses a row.
		requireLedgerUnchanged(t, baseline, snapshotLedger(t, handle), "the forgetful rebuild")
		require.Equal(t, 0, foreignKeyViolations(t, handle))

		// The two triggers on the rebuilt table are gone. The two on ledger_batch, which this
		// migration never touched, are untouched — so the loss is scoped to the rebuilt table, which
		// is what makes the positive test's exact four-name assertion the right assertion.
		require.Equal(t,
			[]string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
			ledgerTriggerNames(t, handle),
			"expected DROP TABLE to have taken ledger_entry's two triggers and left ledger_batch's alone")

		// And the consequence, stated as behaviour rather than as schema. This is the only place in
		// the repository where a ledger row is mutated, and it is a throwaway database in
		// t.TempDir() demonstrating the disaster the trigger prevents.
		res, execErr := handle.ExecContext(t.Context(),
			`UPDATE ledger_entry SET amount_cp = 999999 WHERE id = ?`, seedEntry1ID)
		require.NoError(t, execErr,
			"the UPDATE was refused, so ledger_entry's trigger survived a rebuild that never "+
				"re-created it — re-read the fixture: it must not contain a CREATE TRIGGER")

		affected, rowsErr := res.RowsAffected()
		require.NoError(t, rowsErr)
		require.Equal(t, int64(1), affected, "history was rewritten and the database reported success")

		res, execErr = handle.ExecContext(t.Context(), `DELETE FROM ledger_entry WHERE id = ?`, seedEntry2ID)
		require.NoError(t, execErr, "the DELETE was refused; see above")

		affected, rowsErr = res.RowsAffected()
		require.NoError(t, rowsErr)
		require.Equal(t, int64(1), affected, "a ledger entry was deleted and the database reported success")

		// ledger_batch, whose triggers this migration left alone, still refuses. Proof that the loss
		// is the rebuild's doing and not something about this test's connection.
		_, execErr = handle.ExecContext(t.Context(),
			`UPDATE ledger_batch SET reason = 'edited' WHERE id = ?`, seedBatch1ID)
		require.ErrorContains(t, execErr, "ledger_batch is append-only",
			"ledger_batch's triggers were never dropped and must still fire")
	})
}
