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

// TestTriggers_MutatingAuditLog_Raises asserts trg_audit_log_no_update fires — and, in the same
// test, that DELETE is deliberately NOT blocked.
//
// The asymmetry with the ledger is the point, and it is why both halves are asserted here rather
// than only the one that raises. A ledger row is never removed, so ledger_batch and ledger_entry
// carry both an update and a delete trigger. An audit row IS prunable by retention — domain model
// §17's `dkp audit prune --before`, which leaves an audit_gap_marker scar rather than a silence — so
// a no-delete trigger would have to be dropped in order to run the prune, and a guardrail that gets
// dropped during normal operation is not a guardrail.
//
// What must never happen is an audit row being EDITED. That is how a forensic record becomes
// fiction, and a test that only checked the UPDATE half would go green against a future migration
// that "helpfully" added the delete trigger too — quietly making the retention command impossible.
func TestTriggers_MutatingAuditLog_Raises(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	const auditID = "0000000000000000000AUDIT01"

	s.ExecForTest(t,
		`INSERT INTO audit_log
		   (id, seq, at, actor_kind, actor_label, action, resource_kind, resource_id, outcome,
		    ledger_batch_id, prev_hash, hash)
		 VALUES (?, 1, 1704067200000000, 'system', 'boot', 'ledger.batch.commit', 'ledger_batch',
		         NULL, 'success', NULL, NULL, X'00')`,
		auditID)

	require.ErrorContains(t,
		s.ExecErrForTest(t, `UPDATE audit_log SET actor_label = 'somebody else' WHERE id = ?`, auditID),
		"audit_log is append-only",
		"editing an audit row must abort: the row's whole value is that it was not edited")

	require.NoError(t,
		s.ExecErrForTest(t, `DELETE FROM audit_log WHERE id = ?`, auditID),
		"DELETE must NOT be blocked — retention pruning is a supported operation (domain model §17) "+
			"and adding a no-delete trigger here would make `dkp audit prune` impossible without "+
			"first dropping the guardrail")
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
