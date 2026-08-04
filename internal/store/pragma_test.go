package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// TestPragmas_BothPools_MatchSpec asserts the settings the whole storage design rests on, by
// asking each pool what it actually has rather than by reading the DSN back.
//
// The expected values below are LITERALS, deliberately. Comparing against the constants in
// pragma.go would make this test agree with any value those constants happened to hold, including
// a typo, which is a test that cannot fail for the reason it exists.
func TestPragmas_BothPools_MatchSpec(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	pragmas := []struct {
		name string
		want string
	}{
		{"journal_mode", "wal"},   // readers never block the writer
		{"busy_timeout", "10000"}, // 10 s, enough to absorb the backup job's checkpoint pause
		{"synchronous", "1"},      // NORMAL, the correct pairing with WAL
		{"foreign_keys", "1"},     // ON; SQLite defaults it OFF and ON DELETE SET NULL must fire
	}

	pools := []struct {
		name string
		db   *sql.DB
	}{
		{"write", s.write},
		{"read", s.read},
	}

	for _, pool := range pools {
		for _, p := range pragmas {
			var got string

			err := pool.db.QueryRowContext(t.Context(), "PRAGMA "+p.name).Scan(&got)
			require.NoError(t, err, "read PRAGMA %s from the %s pool", p.name, pool.name)
			require.Equal(t, p.want, strings.ToLower(got),
				"PRAGMA %s on the %s pool", p.name, pool.name)
		}
	}

	// One writer, enforced in Go rather than discovered at runtime as SQLITE_BUSY. This is also
	// what makes `SELECT COALESCE(max(seq),0)+1` a safe sequence allocator on SQLite.
	require.Equal(t, 1, s.write.Stats().MaxOpenConnections,
		"the write pool must be capped at one connection")

	require.Equal(t, max(4, runtime.NumCPU()), s.read.Stats().MaxOpenConnections,
		"the read pool must be sized max(4, NumCPU)")
}

// TestPragmas_WritePool_TxlockImmediate_TakesTheLockAtBegin proves the one setting that cannot be
// read back with a PRAGMA: _txlock=immediate is a driver-level DSN parameter, not database state,
// so the only honest assertion is behavioural.
//
// The discriminator is a transaction that has executed NOTHING. Under the default `deferred`, an
// empty transaction holds no write lock at all and a second writer sails in. Under `immediate`,
// BEGIN itself took the lock. Both control subtests are load-bearing: without them the first
// subtest would also pass against a database that was locked for some entirely different reason.
func TestPragmas_WritePool_TxlockImmediate_TakesTheLockAtBegin(t *testing.T) {
	t.Parallel()

	t.Run("the write pool holds the lock from BEGIN", func(t *testing.T) {
		t.Parallel()

		s := NewDB(t)

		tx, err := s.write.BeginTx(t.Context(), nil)
		require.NoError(t, err, "begin a write transaction")

		rollbackOnCleanup(t, tx)

		_, err = beginImmediate(t, s.Path())
		require.Error(t, err,
			"an empty write-pool transaction did not hold the write lock — is _txlock=immediate "+
				"still on the write DSN?")
		require.ErrorContains(t, err, "locked",
			"the second BEGIN IMMEDIATE failed, but not with SQLITE_BUSY")
	})

	t.Run("a deferred transaction does not", func(t *testing.T) {
		t.Parallel()

		s := NewDB(t)

		deferred := rawPool(t, s.Path(), "deferred", 0)

		tx, err := deferred.BeginTx(t.Context(), nil)
		require.NoError(t, err, "begin a deferred transaction")

		rollbackOnCleanup(t, tx)

		probeTx, err := beginImmediate(t, s.Path())
		require.NoError(t, err,
			"an empty DEFERRED transaction must NOT hold the write lock — if this fails the "+
				"subtest above is passing for the wrong reason")
		rollbackOnCleanup(t, probeTx)
	})

	t.Run("the read pool does not", func(t *testing.T) {
		t.Parallel()

		s := NewDB(t)

		tx, err := s.read.BeginTx(t.Context(), nil)
		require.NoError(t, err, "begin a read-pool transaction")

		rollbackOnCleanup(t, tx)

		probeTx, err := beginImmediate(t, s.Path())
		require.NoError(t, err,
			"the read pool must not carry _txlock=immediate — a reader taking the write lock "+
				"serialises every read against the writer")
		rollbackOnCleanup(t, probeTx)
	})
}

// rollbackOnCleanup rolls tx back when the test ends, reporting anything other than the
// already-settled case. Discarding the error with `_ =` is banned by .claude/rules/go-idioms.md,
// and a rollback that genuinely fails means a connection is stuck — which is worth knowing about
// even in a test that has otherwise passed.
func rollbackOnCleanup(tb testing.TB, tx *sql.Tx) {
	tb.Helper()

	tb.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			tb.Errorf("rollback: %v", err)
		}
	})
}

// beginImmediate opens an independent connection to path and tries to take the write lock, waiting
// zero milliseconds. It either wins immediately or reports SQLITE_BUSY immediately, so the caller
// never has to sleep or poll.
func beginImmediate(tb testing.TB, path string) (*sql.Tx, error) {
	tb.Helper()

	tx, err := rawPool(tb, path, "immediate", 0).BeginTx(tb.Context(), nil)
	if err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}

	return tx, nil
}

// rawPool opens a one-connection pool with an explicit _txlock and busy_timeout.
//
// It bypasses Open on purpose. These tests are about what a DSN does, so they need to build DSNs
// the production code would never build — a `deferred` write pool, a zero busy_timeout — and
// routing them through Open would make that impossible.
func rawPool(tb testing.TB, path, txlock string, busyTimeoutMillis int) *sql.DB {
	tb.Helper()

	q := url.Values{
		"_txlock": []string{txlock},
		"_pragma": []string{
			"journal_mode(WAL)",
			fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis),
		},
	}

	connector, err := sqlite.NewConnector(dsn(path, q))
	require.NoError(tb, err, "build a connector for %s", path)

	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)

	tb.Cleanup(func() {
		require.NoError(tb, db.Close(), "close the probe pool")
	})

	return db
}
