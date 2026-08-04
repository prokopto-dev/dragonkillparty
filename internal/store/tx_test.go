package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// errBoom is the caller-side failure the rollback tests inject.
var errBoom = errors.New("boom")

// TestTx_Commits is the positive control. Without it, a Tx that rolled everything back
// unconditionally would pass every other test in this file.
func TestTx_Commits(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	err := s.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (1)")

		return err
	})
	require.NoError(t, err)

	require.Equal(t, 1, scratchRows(t, s), "a committed transaction must leave its row behind")
}

// TestTx_FnReturnsError_RollsBack asserts the ordinary failure path: the work is undone and the
// caller's error survives the round trip intact, so errors.Is still matches a sentinel from four
// layers down.
func TestTx_FnReturnsError_RollsBack(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	err := s.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
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
		// Nothing to check the result of: the callback panics, so Tx never returns.
		_ = s.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (1)"); err != nil {
				return err
			}

			panic("kaboom")
		})
	}, "the panic must reach the caller unchanged, not be converted into an error")

	require.Equal(t, 0, scratchRows(t, s), "the insert must have been rolled back")

	require.Zero(t, s.write.Stats().InUse, "the write connection must have been returned to the pool")

	// The real proof: the single writer still works.
	err := s.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
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

	err := s.Tx(tb.Context(), func(ctx context.Context, tx DBTX) error {
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

// TestTx_UsesTheWritePool asserts law-2 plumbing that is otherwise invisible: Tx must never reach
// for the read pool, whose connections carry no _txlock and are not serialised.
func TestTx_UsesTheWritePool(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	err := s.Tx(t.Context(), func(_ context.Context, _ DBTX) error {
		// Inside the callback exactly one write connection is checked out. If Tx had used the read
		// pool this would be zero and the read pool's would be one.
		require.Equal(t, 1, s.write.Stats().InUse, "Tx must hold a write-pool connection")
		require.Zero(t, s.read.Stats().InUse, "Tx must not touch the read pool")

		return nil
	})
	require.NoError(t, err)
}
