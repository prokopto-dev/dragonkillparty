package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// fixedNow is the instant every commit test's fake clock is pinned to: 2024-06-01T12:00:00Z. Fixed
// rather than time.Now-derived because recorded_at, the audit row's `at`, effective_day and every
// minted ULID's timestamp prefix all come from it, and a test that could not name the instant could
// not assert on any of them. It is also mid-day UTC on purpose, so effective_day is unambiguous.
var fixedNow = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

// newService builds a commit service against a fresh database and returns both.
func newService(tb testing.TB) (*ledger.Service, *store.Store) {
	tb.Helper()

	s := store.NewDB(tb)

	return ledger.NewService(s, clock.NewFake(fixedNow)), s
}

// award builds a simple, legal proposal: one debit and N credits over the given accounts, summing to
// zero. It is the shape almost every test here needs, and building it in one place means a schema
// change that invalidates it fails one function rather than ten.
func award(payer core.ULID, credits []ledger.Allocation) strategy.BatchProposal {
	var total core.Centipoints
	for _, c := range credits {
		total += c.AmountCp
	}

	entries := []strategy.EntryProposal{
		{AccountID: payer, BalanceKind: "dkp", AmountCp: -total},
	}

	for _, c := range credits {
		entries = append(entries, strategy.EntryProposal{
			AccountID: c.AccountID, BalanceKind: "dkp", AmountCp: c.AmountCp,
		})
	}

	return strategy.BatchProposal{
		Kind:            "award",
		StrategyID:      "zero_sum",
		StrategyVersion: "0.0.0",
		EffectiveAt:     core.FromTime(fixedNow),
		Entries:         entries,
		Invariants: []strategy.Invariant{
			{Kind: strategy.InvariantSumZero, BalanceKind: "dkp"},
		},
	}
}

// request wraps a proposal in the minimum legal CommitRequest.
func request(p strategy.BatchProposal) ledger.CommitRequest {
	return ledger.CommitRequest{
		PoolID:   ledger.DefaultPoolID,
		Proposal: p,
		Source:   "system",
		Actor:    ledger.Actor{Kind: "system", Label: "test"},
	}
}

// countRow returns the single integer a counting query yields.
func countRow(tb testing.TB, s *store.Store, query string, args ...any) int64 {
	tb.Helper()

	var n int64
	require.NoError(tb, s.QueryRowForTest(tb, query, args...).Scan(&n), "query: %s", query)

	return n
}

// TestCommit_OneBatch_WritesAllFiveRowsAndBothHeads is the positive control for everything below it.
//
// The atomicity test asserts that a failed commit leaves NOTHING; without this one, an implementation
// that wrote nothing ever would satisfy it perfectly. So this pins the other direction: one Commit
// produces exactly one batch, N entries, the snapshot rows, one audit row, one outbox event, and both
// chain heads in dkp_meta.
func TestCommit_OneBatch_WritesAllFiveRowsAndBothHeads(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 3)

	receipt, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank, []ledger.Allocation{
		{AccountID: accounts[0], AmountCp: 100},
		{AccountID: accounts[1], AmountCp: 200},
		{AccountID: accounts[2], AmountCp: 300},
	})))
	require.NoError(t, err)

	require.False(t, receipt.Replayed)
	require.Equal(t, int64(1), receipt.Seq, "the first batch in a pool takes seq 1")
	require.Equal(t, int64(4), receipt.EntryCount, "one debit plus three credits")
	require.Equal(t, core.Centipoints(0), receipt.NetAmountCp, "a zero-sum award nets to 0")
	require.Nil(t, receipt.PrevHash, "prev_hash is NULL at seq 1 — there is no previous link")
	require.Len(t, receipt.Hash, 32, "the chain link is a raw 32-byte SHA-256, not hex text")
	require.Equal(t, int64(1), receipt.AuditSeq)
	require.Equal(t, int64(1), receipt.EventSeq)

	// All five tables, counted independently. Whole-table counts rather than counts filtered by the
	// batch id: a stray extra row written by a bug would be invisible to a filtered count.
	require.Equal(t, int64(1), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
	require.Equal(t, int64(4), countRow(t, s, `SELECT count(*) FROM ledger_entry`))
	require.Equal(t, int64(4), countRow(t, s, `SELECT count(*) FROM balance_snapshot`))
	require.Equal(t, int64(1), countRow(t, s, `SELECT count(*) FROM audit_log`))
	require.Equal(t, int64(1), countRow(t, s, `SELECT count(*) FROM event_outbox`))

	// Both heads, and both equal to the hash of the row they describe. A head that merely EXISTS
	// would satisfy a count; a head that is the wrong value forks the chain at the next commit.
	require.Equal(t,
		fmt.Sprintf("%x", receipt.Hash),
		metaValue(t, s, "ledger_head:"+ledger.DefaultPoolID.String()),
		"the per-pool ledger head must equal the hash of the batch just written")

	auditHash := blobValue(t, s, `SELECT hash FROM audit_log WHERE seq = 1`)
	require.Equal(t, fmt.Sprintf("%x", auditHash), metaValue(t, s, "audit_head"))

	// The audit row cross-links to the batch (domain model §17.1: "the batch is the what, the audit
	// row is the who"), and the outbox event points at the resource rather than carrying a document.
	require.Equal(t, receipt.BatchID.String(),
		textValue(t, s, `SELECT ledger_batch_id FROM audit_log WHERE seq = 1`))
	require.Equal(t, "/api/v1/ledger/batches/"+receipt.BatchID.String(),
		textValue(t, s, `SELECT resource_ref FROM event_outbox WHERE event_seq = 1`))
	require.Equal(t, "ledger.batch.committed",
		textValue(t, s, `SELECT event_type FROM event_outbox WHERE event_seq = 1`))

	// effective_day is UTC at this phase (see commit.go's materialise): 2024-06-01T12:00:00Z.
	require.Equal(t, "2024-06-01",
		textValue(t, s, `SELECT effective_day FROM ledger_batch WHERE seq = 1`))
	require.Equal(t, int64(0),
		countRow(t, s, `SELECT actor_is_beneficiary FROM ledger_batch WHERE seq = 1`),
		"actor_is_beneficiary is 0 until Phase 1 can compute it; a guess would be worse than a zero")
}

// TestCommit_DuplicateIdempotencyKey_ReturnsFirstBatch is the acceptance criterion, and the token
// rotation in the middle is the whole point.
//
// A bot retries. Between the attempt and the retry its token rolled over — which is exactly the
// window a rotation policy creates and exactly when a retry is most likely. If idempotency were
// scoped by token, the retry would present a different principal, miss the first batch, and commit
// a second one: the raid tick lands twice, or the bid is charged twice. Domain model §15 is explicit
// ("NEVER 'token:<ulid>': rotation mid-retry must replay") and this test is what makes it true here:
// the two calls carry DIFFERENT actor tokens and the same key, and the second must return the first
// batch having written nothing.
func TestCommit_DuplicateIdempotencyKey_ReturnsFirstBatch(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 2)

	key := "raid-tick-2024-06-01-19:00"

	proposal := award(ledger.AccountIDGuildBank, []ledger.Allocation{
		{AccountID: accounts[0], AmountCp: 250},
		{AccountID: accounts[1], AmountCp: 250},
	})

	firstToken := core.ULID(padID("TOKEN", 1))
	req := request(proposal)
	req.IdempotencyKey = &key
	req.Actor = ledger.Actor{Kind: "service_account", Label: "castle-steward", TokenID: &firstToken}

	first, err := svc.Commit(t.Context(), req)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	// The rotation. Same principal, same key, same proposal — a new token.
	rotatedToken := core.ULID(padID("TOKEN", 2))
	require.NotEqual(t, firstToken, rotatedToken, "the test is vacuous unless the token really changed")

	retry := request(proposal)
	retry.IdempotencyKey = &key
	retry.Actor = ledger.Actor{Kind: "service_account", Label: "castle-steward", TokenID: &rotatedToken}

	second, err := svc.Commit(t.Context(), retry)
	require.NoError(t, err)

	require.True(t, second.Replayed, "the retry must be recognised as a replay, not committed again")
	require.Equal(t, first.BatchID, second.BatchID,
		"the retry must return the FIRST batch id; a rotated token must not start a second batch")
	require.Equal(t, first.Seq, second.Seq)
	require.Equal(t, first.Hash, second.Hash)

	// Nothing was written the second time — in any of the five tables. Asserting only on the batch
	// count would miss a replay that skipped the batch but still emitted a duplicate event, which is
	// the same bug from a subscriber's point of view.
	//
	// balance_snapshot is counted with the other four, and it is the one that matters most here. It
	// is an ADDITIVE upsert: a replay that re-ran it would leave no duplicate row to find, just a
	// row carrying twice the delta — a doubled balance on /standings with a single correct batch
	// behind it, which is the hardest kind of discrepancy to explain to a member. A row count alone
	// cannot see that, so the balance is asserted below as well.
	require.Equal(t, int64(1), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
	require.Equal(t, int64(3), countRow(t, s, `SELECT count(*) FROM ledger_entry`))
	require.Equal(t, int64(3), countRow(t, s, `SELECT count(*) FROM balance_snapshot`),
		"one row per (account, balance kind): the payer plus the two credited raiders")
	require.Equal(t, int64(1), countRow(t, s, `SELECT count(*) FROM audit_log`))
	require.Equal(t, int64(1), countRow(t, s, `SELECT count(*) FROM event_outbox`))

	// And the balance moved exactly once. This is the assertion a guild would actually notice.
	require.Equal(t, int64(250), balanceOf(t, s, accounts[0]))
}

// TestCommit_FaultInjectedMidWrite_LeavesNothing is the atomicity acceptance criterion.
//
// It injects a failure at EVERY write position in turn — the batch, each entry, each snapshot
// upsert, the audit row, the outbox event, and each of the two chain heads — and requires that after
// each one, all five tables are empty and neither head exists. The acceptance criterion names the
// fourth write specifically; running every position instead is strictly stronger and costs nothing,
// and it means a future reordering of the writes cannot move the untested one into the gap.
//
// The fault is injected by DECORATING the real store.Queries, not by faking it. Every call still
// reaches real SQLite in a real transaction — .claude/rules/go-idioms.md's "no mocks of the database"
// stands — and the decorator's only behaviour is to count writes and return an error on the nth. A
// rollback that is proven against a fake proves nothing about a rollback.
func TestCommit_FaultInjectedMidWrite_LeavesNothing(t *testing.T) {
	t.Parallel()

	// One debit plus two credits, so the write sequence is:
	//   1 batch, 2-4 entries, 5-7 snapshots, 8 audit, 9 outbox, 10-11 the two heads.
	const writes = 11

	for failAt := 1; failAt <= writes; failAt++ {
		t.Run(fmt.Sprintf("fail_at_write_%02d", failAt), func(t *testing.T) {
			t.Parallel()

			s := store.NewDB(t)
			accounts := seedPersonAccounts(t, s, 2)

			svc := ledger.NewService(&faultyRunner{inner: s, failAt: failAt}, clock.NewFake(fixedNow))

			_, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank, []ledger.Allocation{
				{AccountID: accounts[0], AmountCp: 100},
				{AccountID: accounts[1], AmountCp: 100},
			})))
			require.ErrorIs(t, err, errInjected,
				"the injected failure at write %d must reach the caller, not be swallowed", failAt)

			for _, table := range []string{
				"ledger_batch", "ledger_entry", "balance_snapshot", "audit_log", "event_outbox",
			} {
				require.Equal(t, int64(0),
					countRow(t, s, `SELECT count(*) FROM `+table),
					"%s must be empty after a failure at write %d: the five rows are one economic "+
						"event and must not be separable", table, failAt)
			}

			require.Equal(t, int64(0),
				countRow(t, s, `SELECT count(*) FROM dkp_meta WHERE key LIKE 'ledger_head:%' OR key = 'audit_head'`),
				"a chain head must not survive a rolled-back batch; a head describing a batch that "+
					"does not exist forks the chain at the next commit")
		})
	}
}

// TestCommit_FaultInjection_CountsEveryWrite is the guard on the test above.
//
// If the number of writes ever changed — an extra insert, a merged upsert — the loop above would
// silently stop covering the tail, and every position it did cover would still pass. This asserts the
// exact count, so a change to the write sequence fails HERE, with a message saying to update the
// bound, rather than quietly shrinking the atomicity test's coverage.
func TestCommit_FaultInjection_CountsEveryWrite(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	accounts := seedPersonAccounts(t, s, 2)

	runner := &faultyRunner{inner: s, failAt: 0} // 0 = never fail; just count
	svc := ledger.NewService(runner, clock.NewFake(fixedNow))

	_, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank, []ledger.Allocation{
		{AccountID: accounts[0], AmountCp: 100},
		{AccountID: accounts[1], AmountCp: 100},
	})))
	require.NoError(t, err)

	require.Equal(t, 11, runner.writes,
		"a 3-entry commit performs 11 writes (1 batch + 3 entries + 3 snapshots + 1 audit + "+
			"1 outbox + 2 heads). If this changed on purpose, update the `writes` bound in "+
			"TestCommit_FaultInjectedMidWrite_LeavesNothing in the same edit — otherwise that test "+
			"silently stops covering the writes past its old bound.")
}

// TestCommit_SecondBatch_ChainsToTheFirst asserts the per-pool hash chain actually links.
//
// A chain whose prev_hash is always NULL passes every "the hash is 32 bytes" assertion and proves
// nothing, so this checks the link itself: batch 2's prev_hash must equal batch 1's hash, and the
// head must have advanced to batch 2's.
func TestCommit_SecondBatch_ChainsToTheFirst(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	first, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})))
	require.NoError(t, err)

	second, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 50}})))
	require.NoError(t, err)

	require.Equal(t, int64(2), second.Seq)
	require.Equal(t, first.Hash, second.PrevHash,
		"batch 2's prev_hash must be batch 1's hash — that link IS the chain")
	require.NotEqual(t, first.Hash, second.Hash)
	require.Equal(t, fmt.Sprintf("%x", second.Hash),
		metaValue(t, s, "ledger_head:"+ledger.DefaultPoolID.String()))

	// The audit chain advances independently and in step.
	require.Equal(t, int64(2), second.AuditSeq)
	require.Equal(t,
		blobValue(t, s, `SELECT hash FROM audit_log WHERE seq = 1`),
		blobValue(t, s, `SELECT prev_hash FROM audit_log WHERE seq = 2`))
}

// TestCommit_Reversal_RestoresBalances is P5 against a real database: committing a proposal and then
// its negation returns every balance to where it started.
//
// The property test covers the arithmetic over random proposals; this covers the round trip through
// SQLite, the snapshot cache and the reversal's own row — including that reverses_batch_id points at
// the original, which is what makes "is this batch reversed?" an index-only EXISTS rather than a
// column somebody would have to UPDATE.
func TestCommit_Reversal_RestoresBalances(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 3)

	proposal := award(ledger.AccountIDGuildBank, []ledger.Allocation{
		{AccountID: accounts[0], AmountCp: 333},
		{AccountID: accounts[1], AmountCp: 333},
		{AccountID: accounts[2], AmountCp: 334},
	})

	original, err := svc.Commit(t.Context(), request(proposal))
	require.NoError(t, err)

	require.Equal(t, int64(333), balanceOf(t, s, accounts[0]))

	reversal, err := proposal.Negated(original.BatchID)
	require.NoError(t, err)
	reversal.EffectiveAt = core.FromTime(fixedNow)

	receipt, err := svc.Commit(t.Context(), request(reversal))
	require.NoError(t, err)
	require.Equal(t, int64(2), receipt.Seq)

	for i, id := range accounts {
		require.Equal(t, int64(0), balanceOf(t, s, id),
			"account %d must be back to zero after the reversal", i)
	}

	require.Equal(t, int64(0), balanceOf(t, s, ledger.AccountIDGuildBank))

	require.Equal(t, "reversal", textValue(t, s, `SELECT kind FROM ledger_batch WHERE seq = 2`))
	require.Equal(t, original.BatchID.String(),
		textValue(t, s, `SELECT reverses_batch_id FROM ledger_batch WHERE seq = 2`))

	// The snapshot cache tracks the ledger rather than drifting from it: it is what /standings reads,
	// so a cache that said 333 after the reversal would be wrong on the only screen a member looks at.
	require.Equal(t, int64(0),
		countRow(t, s, `SELECT amount_cp FROM balance_snapshot
		                WHERE pool_id = ? AND account_id = ? AND balance_kind = 'dkp'`,
			ledger.DefaultPoolID.String(), accounts[0].String()))
}

// TestCommit_InvariantViolations_AreRejectedByName covers the engine's failure paths.
//
// Each case is a proposal that is legal SQL and illegal arithmetic — the class of mistake a plausible
// strategy actually makes. The assertion is on the RULE NAME as well as the failure, because
// "rejected" is not an answer an officer can act on and the name is what a support conversation
// quotes.
func TestCommit_InvariantViolations_AreRejectedByName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(p *strategy.BatchProposal, accounts []core.ULID)
		wantName string
	}{
		{
			name:     "an empty batch",
			mutate:   func(p *strategy.BatchProposal, _ []core.ULID) { p.Entries = nil },
			wantName: "BatchNonEmpty",
		},
		{
			name: "a zero-amount entry",
			mutate: func(p *strategy.BatchProposal, accounts []core.ULID) {
				p.Entries = append(p.Entries, strategy.EntryProposal{
					AccountID: accounts[0], BalanceKind: "dkp", AmountCp: 0,
				})
			},
			wantName: "AmountsNonZero",
		},
		{
			name: "credits that do not sum to the debit",
			mutate: func(p *strategy.BatchProposal, _ []core.ULID) {
				p.Entries[1].AmountCp++
			},
			wantName: "SumZero",
		},
		{
			name: "an entry on an account that does not exist",
			mutate: func(p *strategy.BatchProposal, _ []core.ULID) {
				p.Entries[1].AccountID = testAccountID(9_999)
			},
			wantName: "EntriesReferenceLiveAccounts",
		},
		{
			name: "a NonNegative invariant with no floor",
			mutate: func(p *strategy.BatchProposal, _ []core.ULID) {
				p.Invariants = append(p.Invariants, strategy.Invariant{
					Kind: strategy.InvariantNonNegative, BalanceKind: "dkp",
				})
			},
			wantName: "NonNegative",
		},
		{
			name: "an invariant the engine cannot check",
			mutate: func(p *strategy.BatchProposal, _ []core.ULID) {
				p.Invariants = append(p.Invariants, strategy.Invariant{
					Kind: strategy.InvariantPermutation, BalanceKind: "sk_position",
				})
			},
			wantName: string(strategy.InvariantPermutation),
		},
		{
			name: "an invariant kind nobody has ever defined",
			mutate: func(p *strategy.BatchProposal, _ []core.ULID) {
				p.Invariants = append(p.Invariants, strategy.Invariant{Kind: "vibes"})
			},
			wantName: "vibes",
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
			tc.mutate(&proposal, accounts)

			_, err := svc.Commit(t.Context(), request(proposal))
			require.ErrorIs(t, err, ledger.ErrInvariantViolated)

			var invErr *ledger.InvariantError
			require.ErrorAs(t, err, &invErr,
				"the failure must be a named InvariantError, not a bare error: the rule's name is "+
					"what an officer is told and what a support conversation quotes")
			require.Equal(t, tc.wantName, invErr.Invariant)

			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`),
				"a rejected proposal must write nothing at all")
		})
	}
}

// TestCommit_InvariantScopedToAnUntouchedKind_IsRejected is the regression test for a silent
// no-op, found in review of this PR.
//
// A scoped invariant filters the batch's entries down to one balance kind. When that filter matched
// NOTHING, the aggregate was empty, the loop over it ran zero times, and the invariant returned
// success — so a planner that declared `dkp` while emitting `dpk` got a batch that satisfied
// SumZero without any entry ever being summed. The batch then committed non-zero-sum, which is the
// single failure the whole zero-sum model exists to prevent, and every review of that strategy would
// read the declaration and believe it was constrained.
//
// The batches below are deliberately ILLEGAL under the invariant they declare — they do not sum to
// zero, and the account would go below its floor — so if the scope check ever regresses to a silent
// pass, these commit and the test goes red on the count rather than on the error.
func TestCommit_InvariantScopedToAnUntouchedKind_IsRejected(t *testing.T) {
	t.Parallel()

	floor := core.Centipoints(0)

	cases := []struct {
		name string
		inv  strategy.Invariant
	}{
		{"sum zero", strategy.Invariant{Kind: strategy.InvariantSumZero, BalanceKind: "dpk"}},
		{
			"largest remainder",
			strategy.Invariant{Kind: strategy.InvariantLargestRemainderSumsToDebit, BalanceKind: "dpk"},
		},
		{
			"non negative",
			strategy.Invariant{Kind: strategy.InvariantNonNegative, BalanceKind: "dpk", FloorCp: &floor},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 1)

			// Every entry is 'dkp'; the invariant is scoped to the typo'd 'dpk'. The batch does not
			// sum to zero and drives the account negative, so nothing about it is legal.
			proposal := strategy.BatchProposal{
				Kind:            "award",
				StrategyID:      "zero_sum",
				StrategyVersion: "0.0.0",
				EffectiveAt:     core.FromTime(fixedNow),
				Entries: []strategy.EntryProposal{
					{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: -500},
				},
				Invariants: []strategy.Invariant{tc.inv},
			}

			_, err := svc.Commit(t.Context(), request(proposal))
			require.ErrorIs(t, err, ledger.ErrInvariantViolated,
				"an invariant scoped to a balance kind the batch never touches must FAIL CLOSED; "+
					"treating the empty selection as success lets a typo'd scope commit anything")
			require.ErrorContains(t, err, "dpk")

			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
		})
	}
}

// TestCommit_ReversalLinkage_IsValidated is the regression test for unvalidated reversal metadata,
// found in review of this PR.
//
// `reverses_batch_id` was copied out of the proposal verbatim. The self-FK proves the target EXISTS
// and `ux_batch_reverses` proves it is reversed at most once, but neither says anything about the
// two properties that actually matter, and the damage from each is permanent:
//
//   - AN ORDINARY BATCH POINTING AT ANOTHER. "Is this batch reversed?" is a query, not a column
//     (`EXISTS (SELECT 1 FROM ledger_batch WHERE reverses_batch_id = ?)`), so an award carrying the
//     pointer makes its target render struck through — AND consumes the unique-index slot, so the
//     real reversal can never be written. The history is now wrong and uncorrectable, in a table
//     where nothing can be updated to fix it.
//   - A CROSS-POOL REVERSAL. Balances are per-pool, so a reversal in pool A undoes nothing in pool
//     B while still marking B's batch reversed.
//
// The fourth case is the positive control: a well-formed reversal must still commit, or all of the
// above would be satisfied by a check that rejected every reversal.
func TestCommit_ReversalLinkage_IsValidated(t *testing.T) {
	t.Parallel()

	// A second pool, so the cross-pool case has somewhere to point. The ledger ships one pool; a
	// test needs two and inserts the second directly, which is the shape a later multi-pool PR will
	// replace with a real creation path.
	const otherPoolID = "0000000000000000000POOLB00"

	seedOtherPool := func(tb testing.TB, s *store.Store) {
		tb.Helper()

		s.ExecForTest(tb,
			`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version, balance_kinds,
			                   created_at, updated_at)
			 VALUES (?, 'Second', 'second', 'zero_sum', '0.0.0', 'dkp', 1704067200000000, 1704067200000000)`,
			otherPoolID)
	}

	cases := []struct {
		name      string
		wantErr   bool
		wantMatch string
		// build returns the request to commit second, given the first batch's id.
		build func(t *testing.T, s *store.Store, first core.ULID, accounts []core.ULID) ledger.CommitRequest
	}{
		{
			name:      "an award may not name a batch as reversed",
			wantErr:   true,
			wantMatch: "only a reversal",
			build: func(_ *testing.T, _ *store.Store, first core.ULID, accounts []core.ULID) ledger.CommitRequest {
				p := award(ledger.AccountIDGuildBank,
					[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 10}})
				p.ReversesBatchID = &first

				return request(p)
			},
		},
		{
			name:      "a reversal must name the batch it reverses",
			wantErr:   true,
			wantMatch: "names no batch",
			build: func(_ *testing.T, _ *store.Store, _ core.ULID, accounts []core.ULID) ledger.CommitRequest {
				p := award(ledger.AccountIDGuildBank,
					[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 10}})
				p.Kind = strategy.KindReversal

				return request(p)
			},
		},
		{
			name:      "a reversal may not cross pools",
			wantErr:   true,
			wantMatch: "belongs to pool",
			build: func(t *testing.T, s *store.Store, _ core.ULID, accounts []core.ULID) ledger.CommitRequest {
				seedOtherPool(t, s)

				// A batch in the OTHER pool, seeded raw so it exists to be pointed at.
				const strayID = "0000000000000000000STRAY01"

				s.ExecForTest(t,
					`INSERT INTO ledger_batch
					   (id, pool_id, seq, kind, strategy_id, strategy_version, source,
					    actor_is_beneficiary, effective_at, recorded_at, effective_day,
					    entry_count, net_amount_cp, hash)
					 VALUES (?, ?, 1, 'award', 'zero_sum', '0.0.0', 'system', 0,
					         1704067200000000, 1704067200000000, '2024-01-01', 1, 0, X'00')`,
					strayID, otherPoolID)

				stray := core.ULID(strayID)

				p := award(ledger.AccountIDGuildBank,
					[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 10}})
				p.Kind = strategy.KindReversal
				p.ReversesBatchID = &stray

				return request(p)
			},
		},
		{
			name:    "a well-formed reversal still commits",
			wantErr: false,
			build: func(_ *testing.T, _ *store.Store, first core.ULID, accounts []core.ULID) ledger.CommitRequest {
				p := award(ledger.AccountIDGuildBank,
					[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 10}})
				p.Kind = strategy.KindReversal
				p.ReversesBatchID = &first

				return request(p)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 1)

			first, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
				[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})))
			require.NoError(t, err)

			_, err = svc.Commit(t.Context(), tc.build(t, s, first.BatchID, accounts))

			// Scoped to the pool under test: the cross-pool case seeds a batch in the OTHER pool on
			// purpose, and an unscoped count would include it and mean something different per case.
			const countInPool = `SELECT count(*) FROM ledger_batch WHERE pool_id = ?`

			if !tc.wantErr {
				require.NoError(t, err, "a well-formed reversal must still commit")
				require.Equal(t, int64(2),
					countRow(t, s, countInPool, ledger.DefaultPoolID.String()))

				return
			}

			require.ErrorIs(t, err, ledger.ErrInvariantViolated)
			require.ErrorContains(t, err, tc.wantMatch)

			require.Equal(t, int64(1),
				countRow(t, s, countInPool, ledger.DefaultPoolID.String()),
				"the malformed batch must not have been written; reverses_batch_id is unique, so a "+
					"bad pointer permanently consumes the slot the real correction needs")
		})
	}
}

// TestCommit_NonNegativeFloor_BlocksAnOverdraft is the invariant's positive and negative control in
// one: the same floor accepts a spend that stays above it and rejects one that does not.
//
// Only the pair is meaningful. A floor that rejected everything would satisfy the rejection half
// alone, and that is the failure mode of a guard nobody has tested in both directions.
func TestCommit_NonNegativeFloor_BlocksAnOverdraft(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	floor := core.Centipoints(0)

	// Open with 1000 points, no floor declared — the guild bank goes negative, which is what a bank
	// is for.
	opening := award(ledger.AccountIDGuildBank, []ledger.Allocation{{AccountID: accounts[0], AmountCp: 1000}})
	_, err := svc.Commit(t.Context(), request(opening))
	require.NoError(t, err)

	withFloor := func(cost core.Centipoints) strategy.BatchProposal {
		p := award(accounts[0], []ledger.Allocation{{AccountID: ledger.AccountIDGuildBank, AmountCp: cost}})
		p.Invariants = append(p.Invariants, strategy.Invariant{
			Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor,
		})

		return p
	}

	// Spending 600 of 1000 leaves 400: allowed.
	_, err = svc.Commit(t.Context(), request(withFloor(600)))
	require.NoError(t, err, "a spend that stays above the floor must be allowed")
	require.Equal(t, int64(400), balanceOf(t, s, accounts[0]))

	// Spending another 600 would leave -200: refused.
	_, err = svc.Commit(t.Context(), request(withFloor(600)))
	require.ErrorIs(t, err, ledger.ErrInvariantViolated)
	require.ErrorContains(t, err, "below the floor")

	require.Equal(t, int64(400), balanceOf(t, s, accounts[0]),
		"the refused spend must not have moved the balance")
	require.Equal(t, int64(2), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
}

// TestCommit_MalformedRequest_IsRejectedBeforeTheTransaction covers the request-shape validation.
//
// It is a distinct error from an invariant violation, and deliberately so: the two have different
// audiences. An invariant failure is shown to the officer whose award was rejected; this one is a
// message to whoever wrote the calling code, and it fires before the single write connection has
// been taken.
func TestCommit_MalformedRequest_IsRejectedBeforeTheTransaction(t *testing.T) {
	t.Parallel()

	emptyKey := ""

	cases := []struct {
		name   string
		mutate func(r *ledger.CommitRequest)
	}{
		{"no pool", func(r *ledger.CommitRequest) { r.PoolID = "" }},
		{"unknown source", func(r *ledger.CommitRequest) { r.Source = "carrier_pigeon" }},
		{"no actor kind", func(r *ledger.CommitRequest) { r.Actor.Kind = "" }},
		{"unknown actor kind", func(r *ledger.CommitRequest) { r.Actor.Kind = "wizard" }},
		{"no batch kind", func(r *ledger.CommitRequest) { r.Proposal.Kind = "" }},
		{"no strategy id", func(r *ledger.CommitRequest) { r.Proposal.StrategyID = "" }},
		// A present-but-empty key is worse than none: ux_batch_idem is partial on
		// `idempotency_key IS NOT NULL`, so "" is a real value and the second batch to use it would
		// replay as a duplicate of an unrelated first one.
		{"empty idempotency key", func(r *ledger.CommitRequest) { r.IdempotencyKey = &emptyKey }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 1)

			req := request(award(ledger.AccountIDGuildBank,
				[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}}))
			tc.mutate(&req)

			_, err := svc.Commit(t.Context(), req)
			require.ErrorIs(t, err, ledger.ErrInvalidRequest)
			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
		})
	}
}

// TestCommit_CommittedBatch_StillCannotBeMutated closes the loop between the write path and the
// append-only triggers.
//
// TestTriggers_MutatingLedger_Raises proves the triggers fire against rows a test seeded by hand.
// This proves they fire against a row the SERVICE wrote — which is the row that will actually exist
// on a guild's disk, and the case where a future "just fix the reason field" helper would land.
func TestCommit_CommittedBatch_StillCannotBeMutated(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	receipt, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})))
	require.NoError(t, err)

	require.ErrorContains(t,
		s.ExecErrForTest(t, `UPDATE ledger_batch SET reason = 'tampered' WHERE id = ?`, receipt.BatchID.String()),
		"ledger_batch is append-only")

	require.ErrorContains(t,
		s.ExecErrForTest(t, `UPDATE audit_log SET actor_label = 'somebody else' WHERE seq = 1`),
		"audit_log is append-only")
}

// --- the fault-injection seam ------------------------------------------------------------------

// errInjected is the failure the decorator returns. A sentinel so the test asserts it arrived rather
// than merely that something went wrong — a rollback triggered by an unrelated bug would otherwise
// look like a pass.
var errInjected = errors.New("injected write failure")

// faultyRunner is a ledger.TxRunner that wraps the real store's transaction and hands the callback a
// Queries which fails on the failAt-th write.
//
// It is NOT a fake database. Every call it does not fail is delegated to the real generated Queries
// inside a real SQLite transaction, which is what makes the rollback assertion mean something:
// .claude/rules/go-idioms.md bans mocking the database precisely so that a test like this exercises
// the transaction rather than a model of one.
type faultyRunner struct {
	inner  *store.Store
	failAt int // 1-based; 0 means never fail
	writes int
}

func (r *faultyRunner) Tx(ctx context.Context, fn func(context.Context, store.Queries) error) error {
	return r.inner.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		return fn(ctx, &faultyQueries{Queries: q, runner: r})
	})
}

// faultyQueries embeds store.Queries, so every method it does not name is delegated unchanged. Only
// the seven write methods are overridden, and only to count.
type faultyQueries struct {
	store.Queries

	runner *faultyRunner
}

// next records a write and reports whether this one should fail.
func (f *faultyQueries) next() error {
	f.runner.writes++

	if f.runner.failAt != 0 && f.runner.writes == f.runner.failAt {
		return fmt.Errorf("write %d: %w", f.runner.writes, errInjected)
	}

	return nil
}

// The six write methods, each counted and then delegated. Read methods are not overridden at all —
// the embedded interface handles them — so a lookup the service makes during a fault-injected run
// still returns real data, and the failure is a write failure rather than a starved read.
//
// Six methods rather than one interception point because Go has no method interception, and the
// alternative — reflection, or a hand-written full implementation of the ~15-method interface —
// would be either magic or a fake. This is neither: adding a write to the commit path and forgetting
// to add it here shows up immediately in TestCommit_FaultInjection_CountsEveryWrite, which pins the
// count.

func (f *faultyQueries) InsertLedgerBatch(ctx context.Context, arg sqlitegen.InsertLedgerBatchParams) error {
	if err := f.next(); err != nil {
		return err
	}

	return f.Queries.InsertLedgerBatch(ctx, arg)
}

func (f *faultyQueries) InsertLedgerEntry(ctx context.Context, arg sqlitegen.InsertLedgerEntryParams) error {
	if err := f.next(); err != nil {
		return err
	}

	return f.Queries.InsertLedgerEntry(ctx, arg)
}

func (f *faultyQueries) UpsertBalanceSnapshot(ctx context.Context, arg sqlitegen.UpsertBalanceSnapshotParams) error {
	if err := f.next(); err != nil {
		return err
	}

	return f.Queries.UpsertBalanceSnapshot(ctx, arg)
}

func (f *faultyQueries) InsertAuditLog(ctx context.Context, arg sqlitegen.InsertAuditLogParams) error {
	if err := f.next(); err != nil {
		return err
	}

	return f.Queries.InsertAuditLog(ctx, arg)
}

func (f *faultyQueries) InsertOutboxEvent(ctx context.Context, arg sqlitegen.InsertOutboxEventParams) (int64, error) {
	if err := f.next(); err != nil {
		return 0, err
	}

	return f.Queries.InsertOutboxEvent(ctx, arg)
}

func (f *faultyQueries) UpsertMetaValue(ctx context.Context, arg sqlitegen.UpsertMetaValueParams) error {
	if err := f.next(); err != nil {
		return err
	}

	return f.Queries.UpsertMetaValue(ctx, arg)
}

// --- read helpers ------------------------------------------------------------------------------

// metaValue reads one dkp_meta value, failing the test when the key is absent. A chain head that is
// MISSING and one that is WRONG are different bugs, and a helper returning "" would collapse them.
func metaValue(tb testing.TB, s *store.Store, key string) string {
	tb.Helper()

	var v string
	require.NoError(tb, s.QueryRowForTest(tb, `SELECT value FROM dkp_meta WHERE key = ?`, key).Scan(&v),
		"dkp_meta has no row for %q", key)

	return v
}

// textValue scans a single TEXT column.
func textValue(tb testing.TB, s *store.Store, query string, args ...any) string {
	tb.Helper()

	var v string
	require.NoError(tb, s.QueryRowForTest(tb, query, args...).Scan(&v), "query: %s", query)

	return v
}

// blobValue scans a single BLOB column.
func blobValue(tb testing.TB, s *store.Store, query string, args ...any) []byte {
	tb.Helper()

	var v []byte
	require.NoError(tb, s.QueryRowForTest(tb, query, args...).Scan(&v), "query: %s", query)

	return v
}

// balanceOf reads an account's dkp balance THROUGH THE LEDGER, not through the snapshot cache.
//
// Deliberate: the sum over ledger_entry is the definition, and the snapshot is a droppable cache. A
// test that checked the cache would be checking the thing that is allowed to be wrong, and would go
// green on a commit that updated the cache and wrote no entries.
func balanceOf(tb testing.TB, s *store.Store, account core.ULID) int64 {
	tb.Helper()

	var v int64
	require.NoError(tb, s.QueryRowForTest(tb,
		`SELECT CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) FROM ledger_entry
		 WHERE pool_id = ? AND account_id = ? AND balance_kind = 'dkp'`,
		ledger.DefaultPoolID.String(), account.String()).Scan(&v))

	return v
}
