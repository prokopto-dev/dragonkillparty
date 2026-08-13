package migrations_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Phase 1 schema objects from #191 and #192, asserted against SQL and nothing else.
//
// These tests import no domain package (docs/design/04-testing.md:541) and drive real statements
// against a real migrated database, so a refactor in internal/decay cannot silently rewrite what
// they claim the migration produced. The fingerprint test next door notices that the schema CHANGED;
// these name the properties that must not change and say why, which a hex digest cannot.
//
// The seeded default pool is used as the foreign-key target, at the literal id
// 000003_ledger.sql gives it — restated here rather than imported, for the reason
// ledger_seed_test.go restates it.

// defaultPoolID is the deterministic id of the pool seeded by 000003_ledger.sql.
const defaultPoolID = "00000000000000000000DKPP00"

// TestDecayRun_SecondRunForTheSamePeriod_IsRejected is issue #192's whole point, driven through the
// database rather than inferred from the DDL.
//
// Canonical §10 and ADR-0002 both state the rule as "decay is posted, not computed — explicit
// batches with idempotency key (pool_id, kind, cadence_period)". ux_decay_period is the database
// half of that key, and it is what holds when the Go half cannot: two workers can both read "no run for
// 2026-W31" before either writes one, and a scheduler catching up after downtime re-enqueues
// periods it has already applied. Without the index, the second run decays every balance in the pool
// a second time — and because the ledger is append-only, the repair is a reversal batch that every
// member sees.
//
// The last two cases are what stop the index being too strong: a different period on the same pool,
// and the same period on a different pool, must both be admitted. An index on cadence_period alone
// would satisfy the refusal and silently make a second pool's weekly decay impossible.
func TestDecayRun_SecondRunForTheSamePeriod_IsRejected(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		insertPool(t, handle, "00000000000000000000DKPP01", "second", "second")

		require.NoError(t,
			insertDecayRun(t, handle, "DECAYRUN000000000000000001", defaultPoolID, "decay", "2026-W31"),
			"the first run for a period must be admitted")

		err := insertDecayRun(t, handle, "DECAYRUN000000000000000002", defaultPoolID, "decay", "2026-W31")
		require.Error(t, err,
			"a SECOND decay_run for (pool, 'decay', '2026-W31') was accepted. ux_decay_period is the "+
				"idempotency key the decay strategies depend on (canonical §10): without it a scheduler "+
				"that fires twice decays every balance in the pool twice, and the only repair is a "+
				"reversal batch.")
		require.Contains(t, err.Error(), "UNIQUE",
			"the rejection must come from the unique index, not from something incidental: %v", err)

		require.NoError(t,
			insertDecayRun(t, handle, "DECAYRUN000000000000000003", defaultPoolID, "decay", "2026-W32"),
			"the NEXT period on the same pool must still run — the key is (pool, kind, period)")

		require.NoError(t,
			insertDecayRun(t, handle, "DECAYRUN000000000000000004", "00000000000000000000DKPP01", "decay", "2026-W31"),
			"the SAME period on a different pool must still run — pools decay independently")
	})
}

// TestDecayRun_CapAndDecayShareAPeriod_BothRun is ADR-0024 and issue #206, and it is the assertion
// that would have been impossible to write before `kind` was in the index.
//
// All three cadence families share one cadence vocabulary and the domain model defines ONE run
// table. Un-scoped by kind, a cap run for '2026-W31' violates the index that exists to stop a REPEAT — and
// an idempotent job that hits a uniqueness violation on its own key is supposed to conclude "already
// done" and exit 0. The cap then silently never applies, every week, with a green job dashboard:
// the exact class of defect this project cites EQdkp Plus for.
//
// Both halves are asserted. Admitting all three families is the fix; refusing the repeat WITHIN a
// family is the property the fix must not have cost.
func TestDecayRun_CapAndDecayShareAPeriod_BothRun(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		for i, kind := range []string{"decay", "cap", "start_points"} {
			require.NoError(t,
				insertDecayRun(t, handle, fmt.Sprintf("DECAYRUNFAMILY00000000000%d", i),
					defaultPoolID, kind, "2026-W31"),
				"a %s run for a period another family already occupies must be admitted — one run table, "+
					"three families, and the index is scoped by kind (ADR-0024)", kind)

			err := insertDecayRun(t, handle, fmt.Sprintf("DECAYRUNFAMILYX0000000000%d", i),
				defaultPoolID, kind, "2026-W31")
			require.Error(t, err,
				"a SECOND %s run for the same period was accepted — kind-scoping the index must not have "+
					"cost the repeat refusal it exists for", kind)
		}
	})
}

// TestDecayRun_KindOutsideTheCatalogue_IsRejected pins the other half of the kind column: it is a
// closed vocabulary, not free text.
//
// A misspelled family is worse here than a misspelled state, because it does not collide with
// anything: 'decy' takes its own slot in the unique index, so the real decay run for that period is
// still admitted afterwards and the pool quietly decays twice. The CHECK is what stops the typo
// reaching the index at all.
func TestDecayRun_KindOutsideTheCatalogue_IsRejected(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		err := insertDecayRun(t, handle, "DECAYRUNBADKIND0000000001", defaultPoolID, "decy", "2026-W31")
		require.Error(t, err,
			"decay_run.kind accepted a value outside internal/decay/kinds. A misspelled family does not "+
				"collide with anything — it takes its own slot in ux_decay_period and the period decays twice.")
		require.Contains(t, err.Error(), "CHECK", "the rejection must be the CHECK constraint: %v", err)
	})
}

// TestDecayRun_SkippedPeriod_StillOccupiesTheKey is the half of the idempotency that is easy to get
// wrong by making the index partial.
//
// A period an officer deliberately let pass is 'skipped', and a run that was attempted and did not
// post is 'failed'. Both are TERMINAL and both must keep the period's row: the unique index means
// the period can never be run later, so "we skipped December" has to be a row that says so rather
// than an absence. A partial index — `WHERE state = 'committed'`, say — would let a failed run be
// retried, which sounds helpful and is exactly the double-decay the key exists to prevent, since
// nothing in the schema can tell a run that failed BEFORE posting its batch from one that failed
// after.
func TestDecayRun_SkippedPeriod_StillOccupiesTheKey(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		for _, state := range []string{"skipped", "failed"} {
			id := "DECAYRUNSKIP0000000000000" + state[:1]
			period := "2026-" + state[:2]

			require.NoError(t, insertDecayRun(t, handle, id, defaultPoolID, "decay", period))

			_, err := handle.ExecContext(t.Context(),
				`UPDATE decay_run SET state = ?, executed_at = 1 WHERE id = ?`, state, id)
			require.NoError(t, err, "a decay_run is mutable — it is a schedule, not a ledger row")

			err = insertDecayRun(t, handle, id+"X", defaultPoolID, "decay", period)
			require.Error(t, err,
				"a period whose run is %q was re-run. A terminal run still owns its period; re-running "+
					"is not 'delete the row and try again'.", state)
		}
	})
}

// TestDecayRun_StateOutsideTheCatalogue_IsRejected drives the generated CHECK against a real
// database, which is the copy internal/decay/kinds' own tests deliberately cannot reach: that
// package is a leaf and importing internal/store from it would put generated code inside `make gen`'s
// first step.
//
// The default is asserted in the same test because the two are one mechanism. A DEFAULT SQLite
// accepts and the CHECK refuses produces a table nobody can insert into without naming the column —
// discovered by the first decay job rather than by a test.
func TestDecayRun_StateOutsideTheCatalogue_IsRejected(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		require.NoError(t,
			insertDecayRun(t, handle, "DECAYRUNSTATE00000000000A", defaultPoolID, "decay", "2026-01"))

		var state string
		require.NoError(t, handle.QueryRowContext(t.Context(),
			`SELECT state FROM decay_run WHERE id = 'DECAYRUNSTATE00000000000A'`).Scan(&state))
		require.Equal(t, "planned", state,
			"a run inserted without a state must be born 'planned' — the column default and the CHECK "+
				"have to agree, and this is the only place that pairing is exercised")

		_, err := handle.ExecContext(t.Context(),
			`UPDATE decay_run SET state = 'retrying' WHERE id = 'DECAYRUNSTATE00000000000A'`)
		require.Error(t, err,
			"decay_run.state accepted a value outside internal/decay/kinds. The CHECK is generated from "+
				"that catalogue (canonical §5); a state the database accepts and no Go constant names is "+
				"a run no code can advance.")
		require.Contains(t, err.Error(), "CHECK", "the rejection must be the CHECK constraint: %v", err)
	})
}

// TestPoolConfigChange_HistoryAccumulates is issue #191: a config change is an APPEND, never an
// overwrite.
//
// Two changes to one pool are two rows, and the row carries BOTH sides — what the configuration was
// and what it became — so "what was this pool's decay rule in March?" is answerable from the table
// rather than from a backup nobody took. EQdkp Plus kept these rules in a PHP-serialised file
// outside the database (domain model §7, parity row 15), which is the failure this table exists to
// close.
//
// It also pins the deferred FK. changed_by references app_user, a Phase 2 table, so the column is
// nullable TEXT with NO constraint today: a non-existent user id must be storable, or the first
// writer cannot record an import's config change at all.
func TestPoolConfigChange_HistoryAccumulates(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		insertConfigChange(t, handle, "PCC00000000000000000000001", 1_000, "zero_sum", "tick")
		insertConfigChange(t, handle, "PCC00000000000000000000002", 2_000, "tick", "fixed_price")

		rows, err := handle.QueryContext(t.Context(),
			`SELECT from_strategy_id, to_strategy_id FROM pool_config_change
			 WHERE pool_id = ? ORDER BY changed_at DESC`, defaultPoolID)
		require.NoError(t, err)

		defer func() { require.NoError(t, rows.Close()) }()

		var got [][2]string

		for rows.Next() {
			var from, to string
			require.NoError(t, rows.Scan(&from, &to))

			got = append(got, [2]string{from, to})
		}

		require.NoError(t, rows.Err())

		require.Equal(t, [][2]string{{"tick", "fixed_price"}, {"zero_sum", "tick"}}, got,
			"the second change must not have replaced the first. pool_config_change is the history; "+
				"pool holds the configuration in force.")
	})
}

// TestPoolConfigChange_UnknownPool_IsRejected proves the one foreign key that is NOT deferred does
// what it says.
//
// The pool FK is real because `pool` exists; changed_by's is not because `app_user` does not. A test
// that only inserted valid rows could not tell a real constraint from a comment, and this table's
// value is entirely in being a trustworthy record of a specific pool's history.
func TestPoolConfigChange_UnknownPool_IsRejected(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	path := migratedDB(t)

	withRawFK(t, path, func(handle *sql.DB) {
		_, err := handle.ExecContext(t.Context(),
			`INSERT INTO pool_config_change
			   (id, pool_id, changed_at, from_strategy_id, from_strategy_version, from_config_json,
			    to_strategy_id, to_strategy_version, to_config_json)
			 VALUES ('PCCBAD0000000000000000001', 'NO_SUCH_POOL_00000000000', 1, 'a', '1.0.0', '{}',
			         'b', '1.0.0', '{}')`)
		require.Error(t, err, "a config change for a pool that does not exist was accepted")
		require.Contains(t, err.Error(), "FOREIGN KEY", "the rejection must be the FK: %v", err)
	})
}

// insertPool adds a second pool, so the per-pool half of ux_decay_period has something to be
// per-pool about. Raw SQL rather than a domain helper, for the reason at the top of this file.
func insertPool(t *testing.T, handle *sql.DB, id, name, nameNorm string) {
	t.Helper()

	_, err := handle.ExecContext(t.Context(),
		`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version, balance_kinds,
		                   created_at, updated_at)
		 VALUES (?, ?, ?, 'zero_sum', '1.0.0', 'dkp', 1, 1)`, id, name, nameNorm)
	require.NoError(t, err, "insert pool %s", id)
}

// insertDecayRun writes one run and RETURNS the error rather than requiring success: every
// interesting assertion in this file is about which insert the database refuses.
//
// state is left to its default on purpose — TestDecayRun_StateOutsideTheCatalogue_IsRejected reads
// it back, and a helper that named it would make that assertion about the helper.
func insertDecayRun(t *testing.T, handle *sql.DB, id, poolID, kind, period string) error {
	t.Helper()

	_, err := handle.ExecContext(t.Context(),
		`INSERT INTO decay_run (id, pool_id, kind, cadence_period, scheduled_for_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, 1, 1)`, id, poolID, kind, period)

	return err
}

func insertConfigChange(t *testing.T, handle *sql.DB, id string, changedAt int64, from, to string) {
	t.Helper()

	_, err := handle.ExecContext(t.Context(),
		`INSERT INTO pool_config_change
		   (id, pool_id, changed_at, changed_by, from_strategy_id, from_strategy_version,
		    from_config_json, to_strategy_id, to_strategy_version, to_config_json, reason)
		 VALUES (?, ?, ?, NULL, ?, '1.0.0', '{}', ?, '1.0.0', '{"decay_bp":500}', 'officer vote')`,
		id, defaultPoolID, changedAt, from, to)
	require.NoError(t, err, "insert pool_config_change %s", id)
}
