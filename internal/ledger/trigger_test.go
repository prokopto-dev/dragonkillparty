package ledger_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestTriggers_MutatingLedger_Raises asserts the four append-only triggers fire: a raw UPDATE or
// DELETE on ledger_batch or ledger_entry each raises 'is append-only' (canonical §10, PR 9 acceptance
// criterion 1). The trigger is the database half of the append-only invariant and this test is the
// other half — together they mean the guardrail cannot be silently regressed by a future migration
// that rebuilds a table and forgets to recreate the trigger.
//
// It seeds one batch and one entry (there is no commit service until PR 10), then attempts each of the
// four forbidden mutations and requires the abort with the exact message the migration installs. The
// mutation runs through store.ExecErrForTest, which returns the error so it can be asserted.
func TestTriggers_MutatingLedger_Raises(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	poolID := ledger.DefaultPoolID.String()
	accountID := ledger.AccountIDGuildBank.String()
	batchID, entryID := seedBatchWithEntry(t, s, poolID, accountID)

	cases := []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{
			name: "update ledger_entry",
			sql:  `UPDATE ledger_entry SET amount_cp = 1 WHERE id = ?`,
			args: []any{entryID},
			want: "ledger_entry is append-only",
		},
		{
			name: "delete ledger_entry",
			sql:  `DELETE FROM ledger_entry WHERE id = ?`,
			args: []any{entryID},
			want: "ledger_entry is append-only",
		},
		{
			name: "update ledger_batch",
			sql:  `UPDATE ledger_batch SET reason = 'tampered' WHERE id = ?`,
			args: []any{batchID},
			want: "ledger_batch is append-only",
		},
		{
			name: "delete ledger_batch",
			sql:  `DELETE FROM ledger_batch WHERE id = ?`,
			args: []any{batchID},
			want: "ledger_batch is append-only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := s.ExecErrForTest(t, tc.sql, tc.args...)
			require.Error(t, err, "the append-only trigger must abort %s", tc.name)
			require.ErrorContains(t, err, tc.want,
				"the trigger must raise the exact append-only message so an operator can read it")
		})
	}
}

// TestTriggers_Insert_IsAllowed is the positive control: an INSERT must NOT be blocked, or the test
// above would pass against a table nobody can write to at all. The seed helper inserts a batch and an
// entry; reaching this assertion at all means both inserts succeeded, and the count confirms it.
func TestTriggers_Insert_IsAllowed(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	batchID, entryID := seedBatchWithEntry(t, s,
		ledger.DefaultPoolID.String(), ledger.AccountIDGuildBank.String())

	require.NotEmpty(t, batchID)
	require.NotEmpty(t, entryID)

	var got int
	require.NoError(t,
		s.QueryRowForTest(t, `SELECT count(*) FROM ledger_entry WHERE id = ?`, entryID).Scan(&got))
	require.Equal(t, 1, got, "the entry insert must have landed")
}
