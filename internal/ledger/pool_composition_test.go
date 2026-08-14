package ledger_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// A composed pool, end to end. Phase 1, ADR-0026 (#213).
//
// internal/strategy/pool_test.go proves the ROUTING against a fake façade: which rule answers which
// planner, which config it reads, and what an empty slot says. This file proves the thing that
// matters to a guild — that the three proposals a composed pool produces are batches the LEDGER
// ACCEPTS, in sequence, against a real SQLite database, leaving balances an officer would recognise.
//
// It is the acceptance criterion of #213 in one test: a pool that earns with `tick`, spends with
// `fixed_price` and trims with `cap` — the composition docs/guides/choosing-a-dkp-system.md has
// described since before any of it shipped, and the one a singular pool.strategy_id could not express.
//
// THE FAÇADE HERE IS NOT A FAKE, which is the whole reason the test is in this package. Balance reads
// go through ledger.BalanceAsOfSeq against committed rows, the allocator is ledger.Allocate, and the
// system accounts are the migration's seeded rows resolved by their real ids. The only injected
// values are the clock and the seed, which is what law 3 requires them to be.

// poolCtx is the strategy.Ctx façade backed by a real, migrated database.
//
// It holds a store rather than a map, and every read it answers is the read the shipped code will
// make. What it does NOT hold is a config: the pool's three rules each carry their own, and
// strategy.Rules binds the right one per delegation — so this returns the empty document and a
// planner that saw it would be a routing bug the assertions below would catch.
type poolCtx struct {
	tb      testing.TB
	store   *store.Store
	poolID  core.ULID
	headSeq int64
	roster  []strategy.AccountRef
}

func (c *poolCtx) PoolID() core.ULID  { return c.poolID }
func (c *poolCtx) HeadSeq() int64     { return c.headSeq }
func (c *poolCtx) Clock() clock.Clock { return clock.NewFake(fixedNow) }
func (c *poolCtx) Rng() strategy.Rng  { return ledger.NewRng(42) }
func (c *poolCtx) ConfigJSON() string { return "" }

func (c *poolCtx) Roster() ([]strategy.AccountRef, error) { return c.roster, nil }

func (c *poolCtx) Balance(
	account core.ULID, balanceKind string, asOfSeq int64,
) (core.Centipoints, error) {
	return ledger.BalanceAsOfSeq(
		context.Background(), c.store.Q(), c.poolID, account, balanceKind, asOfSeq)
}

// HasHistory is DELIBERATELY UNANSWERABLE here, and it fails loudly rather than guessing.
//
// A zero balance and an empty history are different facts (property P7, "the everyone-got-1000-points
// -again ticket"), and there is no query that answers the second one yet. No rule in the composed pool
// below asks it — only `start_points` does — so a call reaching this is a routing change that needs a
// real implementation, not a plausible one derived from a sum.
func (c *poolCtx) HasHistory(core.ULID, string, int64) (bool, error) {
	require.FailNow(c.tb, "HasHistory reached a façade that cannot answer it",
		"a zero balance is not an empty history; the composed pool under test has no rule that asks")

	return false, nil
}

func (c *poolCtx) SystemAccount(systemKey string) (core.ULID, error) {
	id, ok := ledger.SystemAccountIDs()[systemKey]
	require.True(c.tb, ok, "no seeded system account for key %q", systemKey)

	return id, nil
}

func (c *poolCtx) Allocate(
	total core.Centipoints, shares []strategy.Share, emptyAccount core.ULID,
) ([]strategy.Allocation, error) {
	return ledger.Allocate(total, shares, emptyAccount)
}

// TestRules_ComposedPool_EarnsAwardsAndDecaysEndToEnd is #213's acceptance criterion.
//
// One pool, three rules, four committed batches, in the order a raid night produces them:
//
//	tick         a raid tick credits three raiders 10.00 each, debited from the guild bank
//	fixed_price  one of them buys an item at 5.00, which lands in the guild bank
//	cap          the cadence run trims the two balances still above the 8.00 ceiling back to it
//	reversal     the award is undone, routed by ledger_batch.strategy_id to the rule that planned it
//
// Every batch is a real commit: five rows across five tables, the invariants checked, the hash chain
// extended. What a singular pool.strategy_id could produce is the FIRST of the four and then a 501.
func TestRules_ComposedPool_EarnsAwardsAndDecaysEndToEnd(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	ctx := t.Context()

	accounts := seedPersonAccounts(t, s, 3)

	rules, err := strategy.PoolConfig{
		EarnStrategyID: "tick",
		EarnConfigJSON: `{"tick_award_cp": 1000}`,

		// The price is affordable at the tick's award, so the buyer stays above fixed_price's floor.
		// An overdraft is the NonNegative invariant's business and has its own tests; what is under
		// test here is that a composed pool can spend at all.
		SpendStrategyID: "fixed_price",
		SpendConfigJSON: `{"default_price_cp": 500, "proceeds": "guild_bank"}`,

		// A ceiling low enough that a raider is over it after one tick, so the cadence run has
		// something to trim. The soft cap is left unset: this pool's earn rule is `tick`, and the
		// over-time slot only ever reaches the CAP RUN.
		OverTimeStrategyID: "cap",
		OverTimeConfigJSON: `{"hard_cap_cp": 800}`,
	}.Resolve()
	require.NoError(t, err)

	require.Equal(t, []string{strategy.BalanceKindDKP}, rules.BalanceKinds(),
		"the pool declares the union of what its three rules move")

	facade := &poolCtx{tb: t, store: s, poolID: ledger.DefaultPoolID}
	for _, id := range accounts {
		facade.roster = append(facade.roster, strategy.AccountRef{ID: id, Kind: "person"})
	}

	// 1. EARN. Weight 1 for everybody is a flat tick; at 10.00 each that is 30.00 out of the bank.
	tick, err := rules.PlanAttendance(facade, strategy.AttendanceEvent{
		Attendees: []strategy.Share{
			{AccountID: accounts[0], Weight: 1},
			{AccountID: accounts[1], Weight: 1},
			{AccountID: accounts[2], Weight: 1},
		},
		EffectiveAt: core.FromTime(fixedNow),
		Reason:      "Plane of Sky, tick 1",
	})
	require.NoError(t, err)
	require.Equal(t, "tick", tick.StrategyID,
		"the batch records WHICH rule planned it, which is what a reversal later routes on")
	require.Equal(t, kinds.KindAttendance, tick.Kind)

	commitComposed(t, svc, ctx, tick, "tick-1")

	requireBalance(t, s, accounts[0], 1000)
	requireBalance(t, s, ledger.AccountIDGuildBank, -3000)

	// 2. SPEND. A pool whose only rule was `tick` returns ErrUnsupported here, which is #213.
	facade.headSeq = headSeq(t, s)

	award, err := rules.PlanAward(facade, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: accounts[1], Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		EffectiveAt: core.FromTime(fixedNow),
		Reason:      "Cloak of Flames to Healbot",
	})
	require.NoError(t, err)
	require.Equal(t, "fixed_price", award.StrategyID)
	require.Equal(t, kinds.KindAward, award.Kind)

	awardBatchID := commitComposed(t, svc, ctx, award, "award-1")

	requireBalance(t, s, accounts[1], 500)
	requireBalance(t, s, ledger.AccountIDGuildBank, -2500)

	// 3. OVER TIME. The cap run trims every balance above 8.00 — raiders 0 and 2, still at 10.00 —
	// and credits the excess to the bank. Raider 1 is at 5.00 and is left alone, which is what makes
	// this a trim rather than a flat deduction.
	facade.headSeq = headSeq(t, s)

	trim, err := rules.PlanDecay(facade, strategy.DecayRun{
		PeriodKey:   "2024-W22",
		AsOfSeq:     facade.headSeq,
		EffectiveAt: core.FromTime(fixedNow),
	})
	require.NoError(t, err)
	require.Equal(t, "cap", trim.StrategyID)
	require.Equal(t, kinds.KindCap, trim.Kind)

	commitComposed(t, svc, ctx, trim, "cap-2024-W22")

	requireBalance(t, s, accounts[0], 800)
	requireBalance(t, s, accounts[2], 800)
	requireBalance(t, s, accounts[1], 500)
	requireBalance(t, s, ledger.AccountIDGuildBank, -2100)

	// Conservation, over the whole composed run: three rules, three batches, and not one centipoint
	// minted or destroyed between them.
	var total core.Centipoints
	for _, id := range append(append([]core.ULID{}, accounts...), ledger.AccountIDGuildBank) {
		total += currentBalance(t, s, id)
	}

	require.Zero(t, total,
		"every batch a composed pool writes still sums to zero; composition moves the question of "+
			"WHICH rule plans, never whether points may be minted")

	// And the reversal routes on the column: the award was planned by the spend rule, so the spend
	// rule is what reverses it. The other two are refused one layer down by reversePlan.
	facade.headSeq = headSeq(t, s)

	reversal, err := rules.PlanReversal(facade, strategy.LedgerBatch{
		ID:   awardBatchID,
		Kind: award.Kind,

		// The ORIGINAL's attribution, carried forward whole. A reversal is attributable to the rules
		// it undoes rather than to today's, so the version and the config snapshot come off the
		// committed batch and not off the pool's current settings.
		StrategyID:         award.StrategyID,
		StrategyVersion:    award.StrategyVersion,
		ConfigSnapshotJSON: award.ConfigSnapshotJSON,
		Entries:            award.Entries,
	})
	require.NoError(t, err)
	require.Equal(t, "fixed_price", reversal.StrategyID)
	require.Equal(t, kinds.KindReversal, reversal.Kind)

	commitComposed(t, svc, ctx, reversal, "reverse-award-1")

	requireBalance(t, s, accounts[1], 1000,
		"reversing the award restores the buyer's balance exactly; the cap run in between did not "+
			"touch them, so there is nothing else to unwind — and the restored 10.00 is now above "+
			"the ceiling, which the NEXT period's run trims, because a reversal is a new economic "+
			"event and not a rewrite of the one that already ran")
}

// TestRules_ComposedPool_AttendancePotAndZeroSumSplit_CommitEndToEnd is the same claim for the two
// ALLOCATING strategies (#196): that a batch whose every credit came out of the largest-remainder
// allocator is a batch the ledger accepts.
//
// It is not a duplicate of the test above. Those three rules produce credits the planner computed
// directly — a tick award times a weight, a trim to a ceiling — and each one is exact by construction.
// These two produce credits that CANNOT be exact individually: 100.00 over weights 12/9/8 and 20.00
// over 9/8 both leave a remainder, so the batch balances only because the allocator gave the odd
// centipoints to the largest remainders. That is precisely the arithmetic
// LargestRemainderSumsToDebit exists to check, and until this test it was checked nowhere against a
// real commit — the strategy package's own tests stop at the proposal, and the invariant engine had
// never been handed one of these batches.
//
//	attendance_weighted  a raid worth a 100.00 pot, split 12/9/8 across three raiders
//	zero_sum             one of them buys an item at 20.00, split across the other two
//	reversal             the award is undone, routed by ledger_batch.strategy_id to zero_sum
func TestRules_ComposedPool_AttendancePotAndZeroSumSplit_CommitEndToEnd(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	ctx := t.Context()

	accounts := seedPersonAccounts(t, s, 3)

	rules, err := strategy.PoolConfig{
		EarnStrategyID: "attendance_weighted",
		EarnConfigJSON: `{"raid_pot_cp": 10000}`,

		// The winner is excluded from the split by default, which is the guide's worked example: they
		// pay the whole price and the other raiders share it.
		SpendStrategyID: "zero_sum",
		SpendConfigJSON: `{"default_price_cp": 2000}`,
	}.Resolve()
	require.NoError(t, err)

	facade := &poolCtx{tb: t, store: s, poolID: ledger.DefaultPoolID}
	for _, id := range accounts {
		facade.roster = append(facade.roster, strategy.AccountRef{ID: id, Kind: "person"})
	}

	attendees := []strategy.Share{
		{AccountID: accounts[0], Weight: 12},
		{AccountID: accounts[1], Weight: 9},
		{AccountID: accounts[2], Weight: 8},
	}

	// 1. EARN. The pot is 100.00 whatever the turnout, and 12/9/8 of 29 does not divide: the exact
	// quotas are 4137.93, 3103.44 and 2758.62, so two centipoints are left over and go to the two
	// largest remainders (raiders 0 and 2). The bank is debited the whole pot, never the sum of what
	// was credited — those are the same number here only because the allocator makes them so.
	pot, err := rules.PlanAttendance(facade, strategy.AttendanceEvent{
		Attendees:   attendees,
		EffectiveAt: core.FromTime(fixedNow),
		Reason:      "Vox, 12 ticks",
	})
	require.NoError(t, err)
	require.Equal(t, "attendance_weighted", pot.StrategyID)
	require.Equal(t, kinds.KindAttendance, pot.Kind)

	commitComposed(t, svc, ctx, pot, "raid-1")

	requireBalance(t, s, accounts[0], 4138)
	requireBalance(t, s, accounts[1], 3103)
	requireBalance(t, s, accounts[2], 2759)
	requireBalance(t, s, ledger.AccountIDGuildBank, -10000,
		"the bank funded exactly the pot: 4138 + 3103 + 2759 = 10000, which is the property that "+
			"would fail by one centipoint if each share had been rounded on its own")

	// 2. SPEND. 20.00 across the two raiders who are not the buyer, weights 9 and 8: 1058.82 and
	// 941.18 exactly, so one centipoint is left over and goes to raider 1's larger remainder.
	facade.headSeq = headSeq(t, s)

	award, err := rules.PlanAward(facade, strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: accounts[0], Kind: "person"},
		Item:          strategy.ItemRef{Name: "Cloak of Flames"},
		Beneficiaries: attendees,
		EffectiveAt:   core.FromTime(fixedNow),
		Reason:        "Nagafen, roll 97",
	})
	require.NoError(t, err)
	require.Equal(t, "zero_sum", award.StrategyID)
	require.Equal(t, kinds.KindAward, award.Kind)

	awardBatchID := commitComposed(t, svc, ctx, award, "award-1")

	requireBalance(t, s, accounts[0], 2138, "the buyer pays the whole price and gets none of it back")
	requireBalance(t, s, accounts[1], 4162)
	requireBalance(t, s, accounts[2], 3700)
	requireBalance(t, s, ledger.AccountIDGuildBank, -10000,
		"a zero-sum award does not touch the bank at all: the price went to the other raiders, which "+
			"is the whole difference between this and a fixed_price pool")

	require.Greater(t, currentBalance(t, s, accounts[1]), currentBalance(t, s, accounts[0]),
		"raider 1 attended LESS of the raid and now leads, because raider 0 spent — which is the "+
			"closed economy working, not a defect")

	// Conservation over the whole composed run: two rules, two batches, and not one centipoint minted
	// or destroyed between them despite neither split dividing evenly.
	var total core.Centipoints
	for _, id := range append(append([]core.ULID{}, accounts...), ledger.AccountIDGuildBank) {
		total += currentBalance(t, s, id)
	}

	require.Zero(t, total)

	// 3. The reversal routes on the column to the rule that planned the original, and undoes the debit
	// AND both credits together — the guarantee docs/guides/loot-and-reconciliation.md makes to an
	// officer about a zero-sum award.
	facade.headSeq = headSeq(t, s)

	reversal, err := rules.PlanReversal(facade, strategy.LedgerBatch{
		ID:                 awardBatchID,
		Kind:               award.Kind,
		StrategyID:         award.StrategyID,
		StrategyVersion:    award.StrategyVersion,
		ConfigSnapshotJSON: award.ConfigSnapshotJSON,
		Entries:            award.Entries,
	})
	require.NoError(t, err)
	require.Equal(t, "zero_sum", reversal.StrategyID)
	require.Equal(t, kinds.KindReversal, reversal.Kind)

	commitComposed(t, svc, ctx, reversal, "reverse-award-1")

	requireBalance(t, s, accounts[0], 4138, "every account is exactly where the raid left it")
	requireBalance(t, s, accounts[1], 3103)
	requireBalance(t, s, accounts[2], 2759)
}

// commitComposed writes one planned batch, requires it to have landed, and returns its id.
//
// Each carries an idempotency key, because that is how the shipped callers commit — the cadence
// families key on (pool_id, kind, cadence_period) and a bot retries — and a test that omitted them
// would exercise a path no production caller takes.
func commitComposed(
	tb testing.TB, svc *ledger.Service, ctx context.Context,
	p strategy.BatchProposal, key string,
) core.ULID {
	tb.Helper()

	req := request(p)
	req.IdempotencyKey = &key

	receipt, err := svc.Commit(ctx, req)
	require.NoError(tb, err, "commit the %s batch planned by %s", p.Kind, p.StrategyID)
	require.False(tb, receipt.Replayed)
	require.NotEmpty(tb, receipt.Hash)

	return receipt.BatchID
}

// requireBalance asserts an account's balance at the pool head, read from the log rather than the
// cache — the sum over committed entries is the definition, and a dispute is settled by it.
func requireBalance(
	tb testing.TB, s *store.Store, account core.ULID, want core.Centipoints, msgAndArgs ...any,
) {
	tb.Helper()

	require.Equal(tb, want, currentBalance(tb, s, account), msgAndArgs...)
}

func currentBalance(tb testing.TB, s *store.Store, account core.ULID) core.Centipoints {
	tb.Helper()

	balance, err := ledger.CurrentBalance(
		context.Background(), s.Q(), ledger.DefaultPoolID, account, strategy.BalanceKindDKP)
	require.NoError(tb, err)

	return balance
}

// headSeq is the pool's current head — the seq a planner reads "current" balances at.
func headSeq(tb testing.TB, s *store.Store) int64 {
	tb.Helper()

	head, err := ledger.MaxPoolSeq(context.Background(), s.Q(), ledger.DefaultPoolID)
	require.NoError(tb, err)

	return head
}
