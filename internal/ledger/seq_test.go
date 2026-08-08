package ledger_test

import (
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestSeq_ConcurrentCommits_NoGapsNoDuplicates fires 100 goroutines that each, inside one write
// transaction on the single writer, allocate the next per-pool seq and insert a batch at it. The
// result must be exactly the set 1..100: no gaps, no duplicates (PR 9 acceptance criterion 4). seq is
// per pool and allocated inside the write transaction; the single-writer cap (SetMaxOpenConns(1),
// _txlock=immediate) is what makes COALESCE(max(seq),0)+1 a correct allocator on SQLite.
//
// The allocation runs the exact NextPoolSeq SQL (COALESCE(max(seq),0)+1) that db/queries/ledger.sql
// ships, through store.TxForTest so the allocate and the insert are one atomic unit on the serialised
// writer — which is precisely the property PR 10's commit service will rely on.
func TestSeq_ConcurrentCommits_NoGapsNoDuplicates(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	poolID := ledger.DefaultPoolID.String()

	const n = 100

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		got  = make([]int64, 0, n)
		errs = make([]error, 0)
	)

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			seq, err := allocateAndInsert(t, s, poolID)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)

				return
			}

			got = append(got, seq)
		}()
	}

	wg.Wait()

	require.Empty(t, errs, "no commit should fail: %v", errs)
	require.Len(t, got, n)

	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i := int64(0); i < n; i++ {
		require.Equal(t, i+1, got[i], "seq %d is missing or duplicated (want a gapless 1..%d)", i+1, n)
	}
}

// allocateAndInsert runs one write transaction: NextPoolSeq's SQL, then an INSERT of a batch at that
// seq so the next allocation sees it. Returns the seq it claimed. All SQL runs through the
// TxHandleForTest, so the raw calls stay inside internal/store (law 2).
func allocateAndInsert(t *testing.T, s *store.Store, poolID string) (int64, error) {
	t.Helper()

	var seq int64

	err := s.TxForTest(t, func(h *store.TxHandleForTest) error {
		next, err := h.QueryRowInt(
			`SELECT CAST(COALESCE(max(seq), 0) + 1 AS INTEGER) FROM ledger_batch WHERE pool_id = ?`,
			poolID)
		if err != nil {
			return err
		}

		seq = next

		batchID := core.ULID(padID("SEQBATCH", seq))

		return h.Do(
			`INSERT INTO ledger_batch
			   (id, pool_id, seq, kind, strategy_id, strategy_version, source, actor_is_beneficiary,
			    effective_at, recorded_at, effective_day, entry_count, net_amount_cp, hash)
			 VALUES (?, ?, ?, 'award', 'zero_sum', '0.0.0', 'system', 0,
			         1704067200000000, 1704067200000000, '2024-01-01', 1, 0, X'00')`,
			batchID.String(), poolID, seq)
	})

	return seq, err
}

// TestSeq_DuplicateInserts_ViolateUniqueIndexes proves each of the four unique batch indexes rejects a
// second conflicting row: ux_batch_seq, ux_batch_srcref, ux_batch_idem, ux_batch_reverses (PR 9
// acceptance criterion: "the unique indexes exist and are tested by inserting a duplicate"). Each case
// inserts a first, legal batch and then a second that collides on exactly one index, and requires the
// second insert to fail. The legal insert uses store.ExecForTest; the colliding one uses
// store.ExecErrForTest so the rejection is the assertion.
func TestSeq_DuplicateInserts_ViolateUniqueIndexes(t *testing.T) {
	t.Parallel()

	poolID := ledger.DefaultPoolID.String()

	const insertSQL = `INSERT INTO ledger_batch
	   (id, pool_id, seq, kind, strategy_id, strategy_version, source, actor_is_beneficiary,
	    source_ref, idempotency_key, reverses_batch_id,
	    effective_at, recorded_at, effective_day, entry_count, net_amount_cp, hash)
	 VALUES (?, ?, ?, 'award', 'zero_sum', '0.0.0', 'system', 0,
	         ?, ?, ?, 1704067200000000, 1704067200000000, '2024-01-01', 1, 0, X'00')`

	strptr := func(s string) *string { return &s }

	cases := []struct {
		name string
		// first inserts the legal batch (or batches); dup inserts the colliding one.
		first func(t *testing.T, s *store.Store)
		dup   func(t *testing.T, s *store.Store) error
	}{
		{
			name: "ux_batch_seq",
			first: func(t *testing.T, s *store.Store) {
				s.ExecForTest(t, insertSQL, padID("A", 1), poolID, 10, nil, nil, nil)
			},
			// Same (pool_id, seq), different id.
			dup: func(t *testing.T, s *store.Store) error {
				return s.ExecErrForTest(t, insertSQL, padID("B", 1), poolID, 10, nil, nil, nil)
			},
		},
		{
			name: "ux_batch_srcref",
			first: func(t *testing.T, s *store.Store) {
				s.ExecForTest(t, insertSQL, padID("A", 2), poolID, 20, strptr("tick_credit:x"), nil, nil)
			},
			// Same (pool_id, source_ref), different id and seq.
			dup: func(t *testing.T, s *store.Store) error {
				return s.ExecErrForTest(t, insertSQL, padID("B", 2), poolID, 21, strptr("tick_credit:x"), nil, nil)
			},
		},
		{
			name: "ux_batch_idem",
			first: func(t *testing.T, s *store.Store) {
				s.ExecForTest(t, insertSQL, padID("A", 3), poolID, 30, nil, strptr("idem-1"), nil)
			},
			// Same (pool_id, idempotency_key), different id and seq.
			dup: func(t *testing.T, s *store.Store) error {
				return s.ExecErrForTest(t, insertSQL, padID("B", 3), poolID, 31, nil, strptr("idem-1"), nil)
			},
		},
		{
			name: "ux_batch_reverses",
			first: func(t *testing.T, s *store.Store) {
				// The reversal points at a real batch: insert the target (seq 40), then a reversal of
				// it (seq 41).
				s.ExecForTest(t, insertSQL, padID("T", 4), poolID, 40, nil, nil, nil)
				s.ExecForTest(t, insertSQL, padID("A", 4), poolID, 41, nil, nil, strptr(padID("T", 4)))
			},
			// A second reversal of the same batch collides on ux_batch_reverses.
			dup: func(t *testing.T, s *store.Store) error {
				return s.ExecErrForTest(t, insertSQL, padID("B", 4), poolID, 42, nil, nil, strptr(padID("T", 4)))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := store.NewDB(t)

			tc.first(t, s)
			require.Error(t, tc.dup(t, s),
				"a second row colliding on %s must be rejected by the unique index", tc.name)
		})
	}
}
