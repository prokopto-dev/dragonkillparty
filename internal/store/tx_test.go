package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// errBoom is the caller-side failure the rollback tests inject.
var errBoom = errors.New("boom")

// The machinery tests below drive txRaw, the unexported transaction primitive, because they need to
// run statements no Queries method covers — a scratch table. The public Tx is a thin wrapper that
// constructs a Queries and delegates to txRaw, so the commit/rollback/panic guarantees proven
// against txRaw hold for it too; TestTx_PublicTx_HandsAQueriesAndCommits is Tx's own positive
// control for the one thing txRaw cannot show, that the callback receives a Queries.

// TestTx_Commits is the positive control for txRaw. Without it, a txRaw that rolled everything back
// unconditionally would pass every other test in this file.
func TestTx_Commits(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	err := s.txRaw(t.Context(), func(ctx context.Context, tx DBTX) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (1)")

		return err
	})
	require.NoError(t, err)

	require.Equal(t, 1, scratchRows(t, s), "a committed transaction must leave its row behind")
}

// TestTx_PublicTx_HandsAQueriesAndCommits is the positive control for the public Tx door.
//
// It proves the two things txRaw cannot: that Tx hands the callback a store.Queries rather than a
// raw handle, and that a write issued through that Queries commits. Without it, Tx could pass a nil
// Queries or roll everything back, and every other test in this file — which drives txRaw directly
// — would still pass.
func TestTx_PublicTx_HandsAQueriesAndCommits(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	err := s.Tx(t.Context(), func(ctx context.Context, q Queries) error {
		require.NotNil(t, q, "Tx must hand the callback a Queries")

		return q.UpsertMetaValue(ctx, sqlitegen.UpsertMetaValueParams{
			Key: "tx_probe", Value: "committed", UpdatedAt: 1,
		})
	})
	require.NoError(t, err)

	got, err := s.MetaValue(t.Context(), "tx_probe")
	require.NoError(t, err, "the value written through the transaction's Queries must have committed")
	require.Equal(t, "committed", got)
}

// TestTx_FnReturnsError_RollsBack asserts the ordinary failure path: the work is undone and the
// caller's error survives the round trip intact, so errors.Is still matches a sentinel from four
// layers down.
func TestTx_FnReturnsError_RollsBack(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	err := s.txRaw(t.Context(), func(ctx context.Context, tx DBTX) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (1)"); err != nil {
			return err
		}

		return errBoom
	})

	require.ErrorIs(t, err, errBoom, "the caller's error must survive wrapping")
	require.Equal(t, 0, scratchRows(t, s), "the insert must have been rolled back")
}

// TestTx_FnPanics_RollsBackAndRepanics covers the case a `defer tx.Rollback()` written by hand
// gets wrong.
//
// Three assertions, and the third is the one that matters. A panic that escapes without releasing
// the connection leaves the write pool — capped at ONE connection — permanently empty, and the
// next write blocks forever rather than failing. That is a hang on raid night, not a test failure,
// so the test proves the pool still works afterwards rather than merely inspecting a counter.
func TestTx_FnPanics_RollsBackAndRepanics(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	require.PanicsWithValue(t, "kaboom", func() {
		// Nothing to check the result of: the callback panics, so txRaw never returns.
		_ = s.txRaw(t.Context(), func(ctx context.Context, tx DBTX) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (1)"); err != nil {
				return err
			}

			panic("kaboom")
		})
	}, "the panic must reach the caller unchanged, not be converted into an error")

	require.Equal(t, 0, scratchRows(t, s), "the insert must have been rolled back")

	require.Zero(t, s.write.Stats().InUse, "the write connection must have been returned to the pool")

	// The real proof: the single writer still works.
	err := s.txRaw(t.Context(), func(ctx context.Context, tx DBTX) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (2)")

		return err
	})
	require.NoError(t, err, "the write pool is deadlocked — the panicking transaction leaked its connection")
	require.Equal(t, 1, scratchRows(t, s))
}

// createScratch gives a test its own table to write into, through the same Tx it is testing.
//
// STRICT because every table in this product is; a test fixture that is laxer than production
// exercises type checking production does not have.
func createScratch(tb testing.TB, s *Store) {
	tb.Helper()

	err := s.txRaw(tb.Context(), func(ctx context.Context, tx DBTX) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE scratch (id INTEGER PRIMARY KEY) STRICT")

		return err
	})
	require.NoError(tb, err, "create the scratch table")
}

// scratchRows counts the scratch table through the READ pool, so a row that is only visible to the
// writer's own connection cannot be mistaken for a committed one.
func scratchRows(tb testing.TB, s *Store) int {
	tb.Helper()

	var n int

	err := s.read.QueryRowContext(tb.Context(), "SELECT count(*) FROM scratch").Scan(&n)
	require.NoError(tb, err, "count the scratch table")

	return n
}

// TestTx_UsesTheWritePool asserts law-2 plumbing that is otherwise invisible: txRaw must never reach
// for the read pool, whose connections carry no _txlock and are not serialised.
func TestTx_UsesTheWritePool(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	err := s.txRaw(t.Context(), func(_ context.Context, _ DBTX) error {
		// Inside the callback exactly one write connection is checked out. If txRaw had used the read
		// pool this would be zero and the read pool's would be one.
		require.Equal(t, 1, s.write.Stats().InUse, "the transaction must hold a write-pool connection")
		require.Zero(t, s.read.Stats().InUse, "the transaction must not touch the read pool")

		return nil
	})
	require.NoError(t, err)
}

// TestReadTx_ConcurrentCommit_IsNotVisibleInTheSnapshot is the property ReadTx exists for, and the
// one Q() cannot provide.
//
// Inside a read transaction, every statement must observe the database as it was when the first one
// ran. A job that reads several things and then compares them against each other depends on that
// absolutely: through Q() each statement gets its own connection and its own view, so the reads are
// individually right and the comparison between them is wrong. `dkp verify-ledger` is the caller
// that found this — a batch committing between its batch walk and its chain-head read made it report
// corruption on a healthy ledger (PR #211 review).
//
// The reads go through the callback's Queries, which is the ONLY handle bound to the transaction.
// A query issued against s.read instead would take a different connection out of the pool and see
// the committed row, which is a true fact about the pool and no evidence at all about the snapshot.
//
// The write lands through Tx on the write pool while the read transaction is open, which is the
// second half of the claim: in WAL a reader does not block the writer, so this test would hang
// rather than fail if that were ever untrue.
func TestReadTx_ConcurrentCommit_IsNotVisibleInTheSnapshot(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	const key = "snapshot_probe"

	require.NoError(t, s.SetMetaValue(t.Context(), key, "before", 1))

	err := s.ReadTx(t.Context(), func(ctx context.Context, q Queries) error {
		// The first statement is what establishes the snapshot — BEGIN is deferred on the read pool,
		// so a transaction that had not read yet would have nothing pinned and this test would pass
		// for the wrong reason.
		first, err := q.GetMetaValue(ctx, key)
		require.NoError(t, err)
		require.Equal(t, "before", first, "the snapshot opens on the committed state")

		require.NoError(t, s.SetMetaValue(t.Context(), key, "after", 2),
			"a writer must not be blocked by an open read transaction in WAL")

		second, err := q.GetMetaValue(ctx, key)
		require.NoError(t, err)
		require.Equal(t, "before", second,
			"a value committed after the snapshot opened must not appear inside it — this is exactly "+
				"the read-your-neighbour's-write that makes a multi-read job compare two different "+
				"databases")

		return nil
	})
	require.NoError(t, err)

	// And the snapshot is released with the transaction: a fresh read now sees the new value, so the
	// isolation above was a snapshot and not a stuck connection.
	after, err := s.MetaValue(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, "after", after, "the write was real and is visible once the snapshot ends")
}

// TestReadTx_UsesTheReadPool is the law-2 plumbing assertion its write-side twin above makes: a
// replay that took the single write connection for its duration would queue every raid-night write
// behind a report (.claude/rules/store-and-sql.md).
func TestReadTx_UsesTheReadPool(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	err := s.ReadTx(t.Context(), func(_ context.Context, _ Queries) error {
		require.Equal(t, 1, s.read.Stats().InUse, "the transaction must hold a read-pool connection")
		require.Zero(t, s.write.Stats().InUse, "the transaction must not touch the write pool")

		return nil
	})
	require.NoError(t, err)
}

// TestReadTx_CallbackError_IsWrappedAndTheSnapshotIsReleased covers the error path: a read
// transaction has nothing to commit, so the only thing that can leak is the connection and the WAL
// snapshot it pins.
func TestReadTx_CallbackError_IsWrappedAndTheSnapshotIsReleased(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	err := s.ReadTx(t.Context(), func(_ context.Context, _ Queries) error { return errBoom })
	require.ErrorIs(t, err, errBoom)

	require.Zero(t, s.read.Stats().InUse, "the failed transaction released its connection")

	// And the pool still works, which is what a leaked snapshot would eventually stop being true.
	require.NoError(t, s.ReadTx(t.Context(), func(_ context.Context, _ Queries) error { return nil }))
}
