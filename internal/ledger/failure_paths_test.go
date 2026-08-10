package ledger_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// What the ledger does when a read fails. Phase 0 PR 10b.
//
// commit_test.go injects faults into the WRITES and asserts nothing survives. This file does the
// other half: every read on the commit path and every read helper has an error branch, and an error
// branch nothing has ever executed is a line of code, not a behaviour. The failure modes here are the
// ones that actually happen to a running instance — a query that errors mid-transaction, a corrupted
// dkp_meta row — and each one must stop the commit rather than continue with a zero.
//
// TWO TECHNIQUES, both against real SQLite and neither a mock (.claude/rules/go-idioms.md: no mocks
// of the database, because a mocked store does not fire the trigger, which is the thing under test):
//
//   - DROPPING A TABLE inside the test's own cloned database, which makes exactly one query fail with
//     a real driver error while every other one keeps working. It is how you fail the third read of a
//     transaction without decorating anything.
//   - A CANCELLED CONTEXT for the read helpers, which is what a client disconnect looks like from
//     inside a query and is the failure these helpers will actually see in production.

// cancelledContext returns a context that is already done, so the next query on it fails the way a
// disconnected client's would.
func cancelledContext(tb testing.TB) context.Context {
	tb.Helper()

	ctx, cancel := context.WithCancel(tb.Context())
	cancel()

	return ctx
}

// TestReadHelpers_QueryFailure_IsWrappedNotSwallowed covers the error branch of every read helper the
// ledger exports.
//
// Each of these has a not-found branch that is already tested and an I/O branch that was not. The
// distinction matters at the call site: store.ErrNotFound is a 404 and a query failure is a 500, and
// a helper that reported the second as the first would turn a broken database into "that account
// does not exist" on every screen in the product.
func TestReadHelpers_QueryFailure_IsWrappedNotSwallowed(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	q := s.Q()
	ctx := cancelledContext(t)
	acct := core.ULID(padID("ACCT", 1))

	t.Run("GetAccount", func(t *testing.T) {
		t.Parallel()

		_, err := ledger.GetAccount(ctx, q, acct)
		require.ErrorIs(t, err, context.Canceled)
		require.NotErrorIs(t, err, store.ErrNotFound,
			"a failed query is not a missing row: one is a 500 and the other is a 404")
	})

	t.Run("GetSystemAccount", func(t *testing.T) {
		t.Parallel()

		_, err := ledger.GetSystemAccount(ctx, q, ledger.SystemKeyGuildBank)
		require.ErrorIs(t, err, context.Canceled)
		require.NotErrorIs(t, err, store.ErrNotFound)

		// The same helper on a live context, so the failure above is a failure and not the only
		// thing the helper can do.
		bank, err := ledger.GetSystemAccount(t.Context(), q, ledger.SystemKeyGuildBank)
		require.NoError(t, err)
		require.Equal(t, ledger.AccountIDGuildBank, bank.ID)
		require.Equal(t, "system", bank.Kind)
		require.Nil(t, bank.PersonID, "a system account belongs to nobody")
	})

	t.Run("BalanceAsOfSeq", func(t *testing.T) {
		t.Parallel()

		_, err := ledger.BalanceAsOfSeq(ctx, q, ledger.DefaultPoolID, acct, "dkp", 1)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("CurrentBalance", func(t *testing.T) {
		t.Parallel()

		_, err := ledger.CurrentBalance(ctx, q, ledger.DefaultPoolID, acct, "dkp")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("MaxPoolSeq", func(t *testing.T) {
		t.Parallel()

		_, err := ledger.MaxPoolSeq(ctx, q, ledger.DefaultPoolID)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("NextPoolSeq", func(t *testing.T) {
		t.Parallel()

		_, err := ledger.NextPoolSeq(ctx, q, ledger.DefaultPoolID)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// TestNextPoolSeq_InsideATransaction_AllocatesHeadPlusOne covers the allocator's happy path through
// the exported helper.
//
// The commit service calls the generated query directly, so this helper — the one a later multi-pool
// writer will reach for — had no caller and therefore no coverage. It is exercised INSIDE store.Tx
// because that is the only place it is safe: the write pool is _txlock=immediate with one connection,
// which is what makes max+1 unraceable, and calling it on the read pool would let two allocations
// return the same number.
func TestNextPoolSeq_InsideATransaction_AllocatesHeadPlusOne(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	_, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})))
	require.NoError(t, err)

	require.NoError(t, s.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		head, err := ledger.MaxPoolSeq(ctx, q, ledger.DefaultPoolID)
		require.NoError(t, err)
		require.Equal(t, int64(1), head)

		next, err := ledger.NextPoolSeq(ctx, q, ledger.DefaultPoolID)
		require.NoError(t, err)
		require.Equal(t, head+1, next, "the next seq is exactly one past the head")

		return nil
	}))
}

// TestCommit_ReadFailureMidTransaction_RollsBackAndReturnsTheError walks the reads the commit path
// makes, failing each one in turn by removing the table it needs.
//
// A dropped table is a real driver error from a real query, arriving at exactly one call site while
// every other read in the transaction still works — which is what makes it a scalpel rather than a
// mock. What each row proves is that the commit STOPS: not that it logs, not that it defaults, and
// certainly not that it writes a batch whose balance check never ran.
func TestCommit_ReadFailureMidTransaction_RollsBackAndReturnsTheError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		drop string
		// idempotent makes the request carry a key, so the replay lookup runs before anything else.
		idempotent bool
		// nonNegative declares a floor, so the balance read runs.
		nonNegative bool
	}{
		{
			// The replay lookup, which is the FIRST thing a commit does and the one a retrying bot
			// hits on every attempt.
			name: "the idempotency lookup", drop: "ledger_batch", idempotent: true,
		},
		{
			// Seq allocation. Without a seq there is no ordering, and a batch with an invented one
			// would collide on ux_batch_seq at insert time — after the invariants had passed.
			name: "seq allocation", drop: "ledger_batch",
		},
		{
			// The account existence check, which the EntriesReferenceLiveAccounts rule runs per
			// distinct account.
			name: "the account lookup", drop: "account",
		},
		{
			// The balance read inside NonNegative. A failure here that returned zero would compute
			// every account's floor against a balance of nothing and admit every overdraft.
			name: "the balance read", drop: "ledger_entry", nonNegative: true,
		},
		{
			// The chain head. Reading it wrong forks the chain at the next commit, which is the one
			// thing the chain exists to make visible.
			name: "the chain head", drop: "dkp_meta",
		},
		{
			// The audit sequence, allocated after the ledger rows are already written — so this is
			// also the case where the rollback has the most to undo.
			name: "the audit sequence", drop: "audit_log",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 2)

			proposal := award(ledger.AccountIDGuildBank, []ledger.Allocation{
				{AccountID: accounts[0], AmountCp: 100},
				{AccountID: accounts[1], AmountCp: 100},
			})

			if tc.nonNegative {
				floor := core.Centipoints(-1_000)
				proposal.Invariants = append(proposal.Invariants, strategy.Invariant{
					Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor,
				})
			}

			req := request(proposal)

			if tc.idempotent {
				key := "retry-me"
				req.IdempotencyKey = &key
			}

			// Dropped AFTER the accounts are seeded and BEFORE the commit, so the schema is whole for
			// everything except the one read under test.
			s.ExecForTest(t, `DROP TABLE `+tc.drop)

			_, err := svc.Commit(t.Context(), req)
			require.Error(t, err, "a failed read must stop the commit, not be defaulted past")
			require.NotErrorIs(t, err, ledger.ErrInvariantViolated,
				"an I/O failure is not an invariant violation: telling an officer their award broke "+
					"a rule when the database is broken sends them to fix the wrong thing")

			// Nothing survives. The dropped table cannot be counted, so the others stand in — and
			// they are the ones that would hold a partial write.
			if tc.drop != "ledger_batch" {
				require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
			}

			if tc.drop != "audit_log" {
				require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM audit_log`))
			}

			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM event_outbox`))
		})
	}
}

// TestCommit_CorruptChainHead_IsRefusedRatherThanForked is the failure a silent recovery would make
// permanent.
//
// A head that is present but unparseable must be an ERROR, never "this chain starts here". Treating a
// corrupted head as absent would fork the per-pool hash chain at the very next commit — and a forked
// chain still verifies internally from the fork onwards, so the damage is invisible to everything
// except a full replay against a backup nobody has.
//
// Both corruptions are tested because they fail in different functions: text that is not hex at all,
// and text that is valid hex of the wrong length. The second is the one a truncated write produces.
func TestCommit_CorruptChainHead_IsRefusedRatherThanForked(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"not hex", "ledger_head:" + ledger.DefaultPoolID.String(), "this is not a hash"},
		{"hex of the wrong length", "ledger_head:" + ledger.DefaultPoolID.String(), "deadbeef"},
		// The instance-wide audit chain is a SECOND head, read later in the same transaction. It
		// corrupts independently and it must refuse independently: an audit chain that forked is an
		// audit trail whose "nothing was tampered with" claim quietly stops covering the fork.
		{"the audit chain's head", "audit_head", "not a hash either"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 1)

			s.ExecForTest(t,
				`INSERT INTO dkp_meta (key, value, updated_at) VALUES (?, ?, 1704067200000000)`,
				tc.key, tc.value)

			_, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
				[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})))
			require.Error(t, err)
			require.Contains(t, err.Error(), "chain head",
				"the error must name the chain head: it is the one failure whose fix is a restore "+
					"rather than a retry")

			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`),
				"a batch written onto a corrupt head is a forked chain, permanently")
		})
	}
}
