package ledger_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The invariant engine's arithmetic edges. Phase 0 PR 10b.
//
// commit_test.go covers the rules a planner gets wrong. This file covers the rules ARITHMETIC gets
// wrong: a sum that wraps int64, a per-account subtotal that wraps while the batch total does not, a
// balance that overflows only when this batch's delta is added, and the multi-kind filters that
// decide which entries a scoped rule even looks at.
//
// Every one of these is a way a broken batch can pass a check that looks like it ran. A wrapped sum
// satisfies a zero-sum check by arithmetic accident and no individual amount looks wrong, which is
// the only way conservation can be defeated without a visible mistake — so the branches that catch it
// are worth reaching deliberately rather than hoping a random batch stumbles into them.

// multiKindProposal builds a batch that moves two balance kinds, with the given entries.
func multiKindProposal(entries []strategy.EntryProposal, invariants []strategy.Invariant) strategy.BatchProposal {
	return strategy.BatchProposal{
		Kind:            "adjustment",
		StrategyID:      "zero_sum",
		StrategyVersion: "0.0.0",
		EffectiveAt:     core.FromTime(fixedNow),
		Entries:         entries,
		Invariants:      invariants,
	}
}

// TestCommit_ScopedInvariants_IgnoreTheOtherBalanceKinds is the positive control for the two filters.
//
// A batch that moves `dkp` and `ep` together, with both invariants scoped to `dkp`, must commit: the
// ep entries are not the scoped rule's business. Checking per kind rather than over the whole batch
// is what stops a multi-kind batch whose two errors cancel from passing a whole-batch sum while both
// balances are wrong — and this is the other half of that, which is that the filter must not reject a
// legal batch it was never meant to see.
//
// The same batch also names one account THREE times, which is the case the account lookups
// deduplicate: a forty-raider batch must cost forty primary-key reads, not one per entry.
func TestCommit_ScopedInvariants_IgnoreTheOtherBalanceKinds(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 2)

	floor := core.Centipoints(-1_000)

	receipt, err := svc.Commit(t.Context(), request(multiKindProposal(
		[]strategy.EntryProposal{
			{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: -100},
			{AccountID: accounts[0], BalanceKind: "ep", AmountCp: 50},
			{AccountID: accounts[0], BalanceKind: "ep", AmountCp: -50},
			{AccountID: accounts[1], BalanceKind: "dkp", AmountCp: 100},
		},
		[]strategy.Invariant{
			{Kind: strategy.InvariantSumZero, BalanceKind: "dkp"},
			// Scoped, so the per-account aggregate skips the ep entries entirely.
			{Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor},
			// Unscoped, so the same aggregate holds two keys for the SAME account — which is the tie
			// the deterministic ordering of the balance reads and the failure messages depends on.
			// A comparator that returned false for equal account ids would leave their order to the
			// sort's internals, and a statement order that changes run to run makes a statement-count
			// budget meaningless.
			{Kind: strategy.InvariantNonNegative, FloorCp: &floor},
		},
	)))
	require.NoError(t, err)
	require.Equal(t, int64(4), receipt.EntryCount)

	require.Equal(t, int64(-100), balanceOf(t, s, accounts[0]),
		"the dkp balance moved; the two ep entries cancelled and neither touched it")
	require.Equal(t, int64(3), countRow(t, s, `SELECT count(*) FROM balance_snapshot`),
		"three (account, balance kind) pairs: the two ep entries fold into one cache row")
}

// TestCommit_ArithmeticOverflow_IsRejectedByName walks the four ways int64 runs out.
//
// Each row would otherwise be a batch whose numbers wrap into something plausible. They are grouped
// because the assertion is the same — a named invariant failure and nothing written — and because
// the value is in covering all four, not in the shape of any one.
func TestCommit_ArithmeticOverflow_IsRejectedByName(t *testing.T) {
	t.Parallel()

	floor := core.Centipoints(0)

	cases := []struct {
		name     string
		proposal func(accounts []core.ULID) strategy.BatchProposal
		wantName string
	}{
		{
			// The batch total itself wraps. NoAmountOverflow is the residue of the NoFloat ban that
			// survives into runtime: by the time a proposal reaches the engine its amounts are int64,
			// so the only float-shaped defect left is a sum that does not fit in one.
			name: "the batch's entries sum past int64",
			proposal: func(accounts []core.ULID) strategy.BatchProposal {
				return multiKindProposal([]strategy.EntryProposal{
					{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: math.MaxInt64},
					{AccountID: accounts[1], BalanceKind: "dkp", AmountCp: math.MaxInt64},
				}, nil)
			},
			wantName: "NoAmountOverflow",
		},
		{
			// The batch total does NOT wrap — the ep entry cancels it — but one kind's subtotal does.
			// A whole-batch check would pass this, which is exactly why the sums are per kind.
			name: "one balance kind's entries sum past int64",
			proposal: func(accounts []core.ULID) strategy.BatchProposal {
				return multiKindProposal([]strategy.EntryProposal{
					{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: math.MaxInt64},
					{AccountID: accounts[0], BalanceKind: "ep", AmountCp: -math.MaxInt64},
					{AccountID: accounts[1], BalanceKind: "dkp", AmountCp: math.MaxInt64},
				}, []strategy.Invariant{{Kind: strategy.InvariantSumZero}})
			},
			wantName: "SumZero",
		},
		{
			// The same shape one level finer: a single account's subtotal for one kind wraps.
			name: "one account's subtotal for one kind sums past int64",
			proposal: func(accounts []core.ULID) strategy.BatchProposal {
				return multiKindProposal([]strategy.EntryProposal{
					{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: math.MaxInt64},
					{AccountID: accounts[0], BalanceKind: "ep", AmountCp: -math.MaxInt64},
					{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: math.MaxInt64},
				}, []strategy.Invariant{{Kind: strategy.InvariantNonNegative, FloorCp: &floor}})
			},
			wantName: "NonNegative",
		},
		{
			// LargestRemainderSumsToDebit is the same arithmetic as SumZero and a deliberately
			// different rule: it names the mistake of rounding each credit independently, which is
			// what somebody reading the failure needs to be told.
			name: "credits that miss the debit under the largest-remainder rule",
			proposal: func(accounts []core.ULID) strategy.BatchProposal {
				return multiKindProposal([]strategy.EntryProposal{
					{AccountID: accounts[0], BalanceKind: "dkp", AmountCp: -100},
					{AccountID: accounts[1], BalanceKind: "dkp", AmountCp: 99},
				}, []strategy.Invariant{
					{Kind: strategy.InvariantLargestRemainderSumsToDebit, BalanceKind: "dkp"},
				})
			},
			wantName: "LargestRemainderSumsToDebit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 2)

			_, err := svc.Commit(t.Context(), request(tc.proposal(accounts)))
			require.ErrorIs(t, err, ledger.ErrInvariantViolated)

			var invErr *ledger.InvariantError
			require.ErrorAs(t, err, &invErr)
			require.Equal(t, tc.wantName, invErr.Invariant)

			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
		})
	}
}

// TestCommit_NonNegative_BalancePlusDeltaOverflows_IsRejected is the overflow that only exists once
// the ledger has history.
//
// Every batch below is individually legal and every amount fits. It is the SUM of a committed
// balance and this batch's delta that does not — and a NonNegative check that wrapped would compute a
// hugely negative "after" balance and reject a legal award, or wrap the other way and admit an
// illegal one. Reaching it needs two prior commits, which is why it is a test of its own rather than
// a row in the table above.
func TestCommit_NonNegative_BalancePlusDeltaOverflows_IsRejected(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	// Two commits of just under a quarter of int64 each, so the balance ends near the ceiling
	// without any single amount being remarkable.
	const huge = core.Centipoints(4_600_000_000_000_000_000)

	for range 2 {
		_, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
			[]ledger.Allocation{{AccountID: accounts[0], AmountCp: huge}})))
		require.NoError(t, err)
	}

	require.Equal(t, int64(huge)*2, balanceOf(t, s, accounts[0]))

	floor := core.Centipoints(0)

	proposal := award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: huge}})
	proposal.Invariants = append(proposal.Invariants,
		strategy.Invariant{Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor})

	_, err := svc.Commit(t.Context(), request(proposal))

	var invErr *ledger.InvariantError
	require.ErrorAs(t, err, &invErr)
	require.Equal(t, "NonNegative", invErr.Invariant)
	require.Contains(t, invErr.Detail, "overflows int64",
		"the failure must say the arithmetic ran out, not that the account is short: they are "+
			"different problems and only one of them is the officer's to fix")
}

// TestCommit_DeclaredButUnimplementedInvariant_IsRefusedEvenWhenItsKindIsTouched closes the gap
// between the two ways an unimplemented rule can be declared.
//
// A rule scoped to a balance kind the batch does not touch is already rejected by the scope guard —
// and that guard fires FIRST, which means the existing coverage of "declared but not implemented"
// was really coverage of "scoped to nothing". This declares Conserved against the kind the batch
// actually moves, so the engine reaches the fail-closed branch itself.
//
// Fail closed is the whole point: a strategy that declares Conserved and gets no conservation
// checking, silently, is worse than a vocabulary that never offered the word, because every review of
// that strategy reads the declaration as protection.
func TestCommit_DeclaredButUnimplementedInvariant_IsRefusedEvenWhenItsKindIsTouched(t *testing.T) {
	t.Parallel()

	total := core.Centipoints(0)

	for _, inv := range []strategy.Invariant{
		{Kind: strategy.InvariantConserved, BalanceKind: "dkp", TotalCp: &total},
		{Kind: strategy.InvariantMonotoneNonDecreasing, BalanceKind: "dkp"},
		{Kind: strategy.InvariantPermutation, BalanceKind: "dkp"},
		{Kind: strategy.InvariantRatioPreserved, BalanceKind: "dkp", SecondBalanceKind: "ep"},
	} {
		t.Run(string(inv.Kind), func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 1)

			proposal := award(ledger.AccountIDGuildBank,
				[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})
			proposal.Invariants = append(proposal.Invariants, inv)

			_, err := svc.Commit(t.Context(), request(proposal))

			var invErr *ledger.InvariantError
			require.ErrorAs(t, err, &invErr)
			require.Equal(t, string(inv.Kind), invErr.Invariant)
			require.Contains(t, invErr.Detail, "not implemented at this phase",
				"the refusal must say the engine cannot check it, not that the batch failed it")

			require.Equal(t, int64(0), countRow(t, s, `SELECT count(*) FROM ledger_batch`))
		})
	}
}

// TestCommit_ReversalOfABatchThatDoesNotExist_IsRejected covers the reversal-linkage lookup missing
// its target.
//
// The self-FK would also reject it, as a constraint violation naming a column. This names the batch
// and says there is nothing to reverse — and it does so BEFORE the write, so a mistyped batch id
// costs a read rather than a rolled-back transaction.
func TestCommit_ReversalOfABatchThatDoesNotExist_IsRejected(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	missing := core.ULID(padID("BATCH", 404))

	proposal := award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})
	proposal.Kind = strategy.KindReversal
	proposal.ReversesBatchID = &missing

	_, err := svc.Commit(t.Context(), request(proposal))

	var invErr *ledger.InvariantError
	require.ErrorAs(t, err, &invErr)
	require.Equal(t, "ReversalLinkage", invErr.Invariant)
	require.Contains(t, invErr.Detail, "nothing to reverse")
}

// TestCommit_OptionalColumns_AreCarriedRatherThanDefaulted covers the substitutions the commit path
// makes for unset values, in the direction where it must NOT substitute.
//
// The config snapshot and the entry metadata are defaulted to '{}' when empty, and the audit action,
// the event type and the topic to their package defaults. Every one of those has a caller-supplied
// branch that the defaults hide — and the config snapshot in particular is the field that makes a
// past batch still mean what it meant, so a path that quietly replaced it with '{}' would erase the
// rules a six-month-old award was planned under.
func TestCommit_OptionalColumns_AreCarriedRatherThanDefaulted(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	proposal := award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})
	proposal.ConfigSnapshotJSON = `{"default_price_cp":250}`
	proposal.Entries[1].MetadataJSON = `{"roll":97}`

	req := request(proposal)
	req.Action = "ledger.batch.import"
	req.EventType = "ledger.batch.imported"
	req.Topic = "import:2024-06"

	_, err := svc.Commit(t.Context(), req)
	require.NoError(t, err)

	require.Equal(t, `{"default_price_cp":250}`,
		textValue(t, s, `SELECT config_snapshot_json FROM ledger_batch WHERE seq = 1`))
	require.Equal(t, `{"roll":97}`,
		textValue(t, s, `SELECT metadata_json FROM ledger_entry WHERE amount_cp = 100`))
	require.Equal(t, "{}",
		textValue(t, s, `SELECT metadata_json FROM ledger_entry WHERE amount_cp = -100`),
		"an unset metadata column takes the schema default, which the insert has to apply itself "+
			"because it names every column explicitly")

	require.Equal(t, "ledger.batch.import",
		textValue(t, s, `SELECT action FROM audit_log WHERE seq = 1`))
	require.Equal(t, "ledger.batch.imported",
		textValue(t, s, `SELECT event_type FROM event_outbox WHERE event_seq = 1`))
	require.Equal(t, "import:2024-06",
		textValue(t, s, `SELECT topic FROM event_outbox WHERE event_seq = 1`))
}
