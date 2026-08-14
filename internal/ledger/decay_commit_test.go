package ledger_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The decay family's batches, against a real database. Phase 1, #194.
//
// internal/strategy proves what these planners PROPOSE; this file proves the ledger ACCEPTS it. The
// gap between the two is where a strategy can be entirely correct and still unusable: a proposal
// whose declared invariants the commit-time engine refuses is a run that plans cleanly, passes every
// property, and then fails at 02:00 with a rejected batch and a period nobody can re-run.
//
// The `toward_zero` case below is exactly that failure, found in review of #194 and fixed in
// internal/ledger/invariant.go rather than in the planner — see checkNonNegative.

// TestCommit_NonNegative_MovementNotAbsolute pins all three sides of the floor rule in one place,
// because the middle one is what distinguishes the rule from having dropped it for credits.
//
// A floor constrains a DEDUCTION. So:
//
//   - a batch that takes an account below the floor is refused (the overdraft the rule exists for);
//   - a batch that pushes an account ALREADY below the floor further down is refused too — being in
//     debt is not a licence to go deeper;
//   - a batch that leaves an account below the floor but BETTER OFF is legal, because it took
//     nothing from anybody.
func TestCommit_NonNegative_MovementNotAbsolute(t *testing.T) {
	t.Parallel()

	floor := core.Centipoints(0)

	for _, tc := range []struct {
		name    string
		delta   core.Centipoints
		wantErr bool
		why     string
	}{
		{
			name:    "a credit that leaves the account below the floor",
			delta:   500,
			wantErr: false,
			why: "the account is at -5000 and ends at -4500: every affected member is better off, " +
				"and a batch that took nothing from anybody cannot have overdrawn anybody",
		},
		{
			name:    "a debit that pushes a debt further down",
			delta:   -500,
			wantErr: true,
			why:     "already below the floor is not a licence to go deeper",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			accounts := seedPersonAccounts(t, s, 1)

			// Put the account into debt the way a guild actually gets there: it was credited, it
			// spent, and the credit was then reversed. Here that is compressed into one movement out
			// of the account and into the bank.
			_, err := svc.Commit(t.Context(), request(award(accounts[0],
				[]ledger.Allocation{{AccountID: ledger.AccountIDGuildBank, AmountCp: 5_000}})))
			require.NoError(t, err)
			require.Equal(t, int64(-5_000), balanceOf(t, s, accounts[0]))

			p := award(ledger.AccountIDGuildBank,
				[]ledger.Allocation{{AccountID: accounts[0], AmountCp: tc.delta}})
			p.Invariants = append(p.Invariants, strategy.Invariant{
				Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor,
			})

			_, err = svc.Commit(t.Context(), request(p))

			if !tc.wantErr {
				require.NoError(t, err, tc.why)
				require.Equal(t, int64(-4_500), balanceOf(t, s, accounts[0]))

				return
			}

			var invErr *ledger.InvariantError
			require.ErrorAs(t, err, &invErr, tc.why)
			require.Equal(t, "NonNegative", invErr.Invariant)
			require.Equal(t, int64(-5_000), balanceOf(t, s, accounts[0]),
				"a rejected batch writes nothing")
		})
	}
}

// TestCommit_NonNegative_OverdraftIsStillRefused is the plain case the rule exists for, kept separate
// because it is the one a reader checks first: an account in credit cannot be spent below the floor.
func TestCommit_NonNegative_OverdraftIsStillRefused(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 1)

	_, err := svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[0], AmountCp: 1_000}})))
	require.NoError(t, err)

	floor := core.Centipoints(0)

	p := award(accounts[0], []ledger.Allocation{{AccountID: ledger.AccountIDGuildBank, AmountCp: 1_500}})
	p.Invariants = append(p.Invariants, strategy.Invariant{
		Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor,
	})

	_, err = svc.Commit(t.Context(), request(p))

	var invErr *ledger.InvariantError
	require.ErrorAs(t, err, &invErr)
	require.Equal(t, "NonNegative", invErr.Invariant)
	require.Contains(t, invErr.Detail, "below the floor of 0")
	require.Equal(t, int64(1_000), balanceOf(t, s, accounts[0]))
}

// TestRules_DecayPercentTowardZero_ForgivesADebtEndToEnd is the review finding from #194, as the run
// an officer would actually schedule.
//
// A pool that earns with `tick` and forgives debts with `decay_percent` at 10% a week. One member is
// in debt — the ordinary way, by spending points a later reversal took back — and the cadence run
// credits them a tenth of it. Every affected member ends up better off and the member is still below
// zero, which is what debt forgiveness IS: it is a rate, not a write-off.
//
// The batch was refused before the fix, because the proposal declares NonNegative at the pool's floor
// and the engine read that as an absolute bound on the resulting balance rather than as a bound on
// what the batch may TAKE. The policy is documented in the guide and in
// `.claude/rules/decay-and-jobs.md` §5, so "cannot commit" was not a defensible answer.
func TestRules_DecayPercentTowardZero_ForgivesADebtEndToEnd(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	ctx := t.Context()

	accounts := seedPersonAccounts(t, s, 2)

	rules, err := strategy.PoolConfig{
		EarnStrategyID: "tick",
		EarnConfigJSON: `{"tick_award_cp": 1000}`,

		OverTimeStrategyID: "decay_percent",
		OverTimeConfigJSON: `{"decay_bp": 1000, "negative_balances": "toward_zero"}`,
	}.Resolve()
	require.NoError(t, err)

	facade := &poolCtx{tb: t, store: s, poolID: ledger.DefaultPoolID}
	for _, id := range accounts {
		facade.roster = append(facade.roster, strategy.AccountRef{ID: id, Kind: "person"})
	}

	// One member is 50.00 in debt; the other holds 100.00. A run that only forgave would prove half
	// the fix, so the roster carries both directions at once — the same batch takes from one member
	// and gives to the other, which is the case a floor has to keep telling apart.
	_, err = svc.Commit(ctx, request(award(accounts[0],
		[]ledger.Allocation{{AccountID: ledger.AccountIDGuildBank, AmountCp: 5_000}})))
	require.NoError(t, err)

	_, err = svc.Commit(ctx, request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[1], AmountCp: 10_000}})))
	require.NoError(t, err)

	requireBalance(t, s, accounts[0], -5_000)
	requireBalance(t, s, accounts[1], 10_000)

	facade.headSeq = headSeq(t, s)

	run, err := rules.PlanDecay(facade, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     facade.headSeq,
		EffectiveAt: core.FromTime(fixedNow),
	})
	require.NoError(t, err)
	require.Equal(t, "decay_percent", run.StrategyID,
		"the batch records which of the pool's three rules planned it")

	commitComposed(t, svc, ctx, run, "decay:2026-W31")

	requireBalance(t, s, accounts[0], -4_500,
		"the debt shrank by a tenth and is still a debt: forgiveness is a rate, not a write-off")
	requireBalance(t, s, accounts[1], 9_000, "and the member in credit was decayed by the same rate")

	// The whole point of the cadence key: the same period, planned and committed again, is the batch
	// that already landed rather than a second haircut.
	rerun, err := rules.PlanDecay(facade, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     facade.headSeq,
		EffectiveAt: core.FromTime(fixedNow),
	})
	require.NoError(t, err)

	req := request(rerun)
	key := "decay:2026-W31"
	req.IdempotencyKey = &key

	receipt, err := svc.Commit(ctx, req)
	require.NoError(t, err)
	require.True(t, receipt.Replayed,
		"the second commit for the period returns the first batch rather than writing a second")

	requireBalance(t, s, accounts[0], -4_500, "and no balance moved twice")
	requireBalance(t, s, accounts[1], 9_000)
}

// TestRules_DecayPercentTowardZero_APeriodNettingToZero_CommitsWithNoBankEntry is #245, as the run an
// officer would actually schedule.
//
// `toward_zero` is the only policy in the strategy package that plans MIXED-SIGN amounts — a debt is
// credited while a positive balance is debited — so it is the only one whose counterparty can land on
// zero. Here 10% of a debt of 50.00 is forgiven and 10% of a credit of 50.00 is taken: the two
// members balance the batch between themselves and the bank funds nothing.
//
// THIS FILE IS WHERE THAT MATTERED, which is the same gap the test above was written for. The planner
// wrote the bank in regardless, at −0; AmountsNonZero is universal and unwaivable and ledger_entry
// carries CHECK (amount_cp <> 0), so the zero entry did not spoil one row — it got the WHOLE batch
// refused. Every member's decay for the period was lost, over an arithmetic coincidence, from a
// planner that had returned no error and passed every example test. The nightly property found it;
// this is the commit it was standing in for.
func TestRules_DecayPercentTowardZero_APeriodNettingToZero_CommitsWithNoBankEntry(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	ctx := t.Context()

	accounts := seedPersonAccounts(t, s, 2)

	rules, err := strategy.PoolConfig{
		OverTimeStrategyID: "decay_percent",
		OverTimeConfigJSON: `{"decay_bp": 1000, "negative_balances": "toward_zero"}`,
	}.Resolve()
	require.NoError(t, err)

	facade := &poolCtx{tb: t, store: s, poolID: ledger.DefaultPoolID}
	for _, id := range accounts {
		facade.roster = append(facade.roster, strategy.AccountRef{ID: id, Kind: "person"})
	}

	// Equal and opposite, which is what makes the period net to zero: one member 50.00 in debt, one
	// holding 50.00. The bank funded the second out of what the first overdrew, so it is back at 0 and
	// stays there.
	_, err = svc.Commit(ctx, request(award(accounts[0],
		[]ledger.Allocation{{AccountID: ledger.AccountIDGuildBank, AmountCp: 5_000}})))
	require.NoError(t, err)

	_, err = svc.Commit(ctx, request(award(ledger.AccountIDGuildBank,
		[]ledger.Allocation{{AccountID: accounts[1], AmountCp: 5_000}})))
	require.NoError(t, err)

	requireBalance(t, s, ledger.AccountIDGuildBank, 0)

	facade.headSeq = headSeq(t, s)

	run, err := rules.PlanDecay(facade, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     facade.headSeq,
		EffectiveAt: core.FromTime(fixedNow),
	})
	require.NoError(t, err)

	require.Len(t, run.Entries, 2, "the two members moved and the bank did not, so the bank gets no row")

	for i, e := range run.Entries {
		require.NotEqual(t, core.Centipoints(0), e.AmountCp,
			"entry %d moves 0 centipoints, which CHECK (amount_cp <> 0) refuses", i)
		require.NotEqual(t, ledger.AccountIDGuildBank, e.AccountID,
			"entry %d is the bank, which funded nothing this period", i)
	}

	commitComposed(t, svc, ctx, run, "decay:2026-W31")

	requireBalance(t, s, accounts[0], -4_500,
		"the debt shrank by a tenth, out of what the other member's haircut released")
	requireBalance(t, s, accounts[1], 4_500)
	requireBalance(t, s, ledger.AccountIDGuildBank, 0,
		"the bank is where it started, because it genuinely did not move")
}
