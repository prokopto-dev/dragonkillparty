package strategy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// relative_bid's arithmetic. Phase 1, #195.
//
// The family contracts are spend_test.go's table. What is here is the share — how it is computed, the
// balance it is computed against, and what happens to a bid that is no longer a share of it.

// relativeBidGoldenDir is where the canonical proposals live.
const relativeBidGoldenDir = "../../test/golden/strategy/relative_bid"

// relativeBidGoldenConfig sets every knob to a non-default value.
const relativeBidGoldenConfig = `{"min_bid_bp":500,"max_bid_bp":7500,"proceeds":"attendees",` +
	`"solo_policy":"write_off","floor_cp":-500}`

// relativeBidGoldenCases is one case per planner this strategy answers.
func relativeBidGoldenCases() []goldenCase {
	s := strategy.RelativeBid{}
	raid, itemAward := acct(81), acct(82)

	return []goldenCase{
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				character := acct(50)
				price := core.Centipoints(1_250)

				p, err := s.PlanAward(spendCtx(tb, relativeBidGoldenConfig), strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					CharacterID:   &character,
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
					RaidID:        &raid,
					ItemAwardID:   &itemAward,
					EffectiveAt:   fixedNow,
					Reason:        "Nagafen, 12.5% of a frozen 100.00",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(spendCtx(tb, relativeBidGoldenConfig),
					strategy.AdjustmentEvent{
						Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
						AmountCp:    -750,
						EffectiveAt: fixedNow,
						Reason:      "charged against the wrong frozen balance",
					})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := spendCtx(tb, relativeBidGoldenConfig)
				price := core.Centipoints(1_250)

				original, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
					EffectiveAt:   fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:        "Nagafen, 12.5% of a frozen 100.00",
				})
				require.NoError(tb, err)

				p, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID:                 acct(70),
					Kind:               original.Kind,
					StrategyID:         original.StrategyID,
					StrategyVersion:    original.StrategyVersion,
					ConfigSnapshotJSON: original.ConfigSnapshotJSON,
					Reason:             original.Reason,
					EffectiveAt:        original.EffectiveAt,
					Entries:            original.Entries,
				})
				require.NoError(tb, err)

				return p
			},
		},
	}
}

// TestRelativeBid_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal.
func TestRelativeBid_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, relativeBidGoldenDir, relativeBidGoldenCases())
}

// TestRelativeBid_Goldens_CoverEveryPlanner is the anti-drift half.
func TestRelativeBid_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, relativeBidGoldenDir, relativeBidGoldenCases(),
		[]string{"adjustment", "award", "reversal"})
}

// TestRelativeBid_SettleAuction_TheLargerShareWinsWhilePayingLess is the guide's worked example, in
// centipoints (docs/guides/choosing-a-dkp-system.md): Tankguy holds 900 and commits 360, which is
// 40%; Healbot holds 500 and commits 275, which is 55%. Healbot wins while paying 85 points less.
//
// That inversion is the whole strategy. A test that only asserted the winner would pass with a
// planner that ranked by amount and got lucky, so the price is asserted too.
func TestRelativeBid_SettleAuction_TheLargerShareWinsWhilePayingLess(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"max_bid_bp":10000}`)
	ctx.balances[acct(0)] = 90_000
	ctx.balances[acct(1)] = 50_000

	res, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow},
		[]strategy.Bid{
			{AccountID: acct(0), AmountCp: 36_000, PlacedAt: fixedNow},
			{AccountID: acct(1), AmountCp: 27_500, PlacedAt: fixedNow},
		})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(1), AmountCp: 27_500}}, res.Winners,
		"55% of a smaller bank beats 40% of a larger one, and the winner pays what they committed")
	require.Contains(t, res.Reason, "5500")
}

// TestRelativeBid_SettleAuction_TheLadderOutranksTheShare (#224): a main who commits a tenth of their
// bank takes it from an alt who commits all of theirs.
//
// This strategy does not partition on the rung the way the two auctions do — a share is not a price
// and there is no second-price rule to keep inside one — but the ordering it settles by is rankBids',
// which compares the rung first. So "the largest share" is only ever the largest share ON THE WINNING
// RUNG, and where a lower rung holds a bid the resolution says which and how many, naming no share
// and no amount.
func TestRelativeBid_SettleAuction_TheLadderOutranksTheShare(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"max_bid_bp":10000}`)
	ctx.balances[acct(0)] = 90_000
	ctx.balances[acct(1)] = 50_000

	res, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow},
		[]strategy.Bid{
			{AccountID: acct(0), AmountCp: 90_000, PlacedAt: fixedNow, Tier: strategy.TierAlt},
			{AccountID: acct(1), AmountCp: 5_000, PlacedAt: fixedNow, Tier: strategy.TierMain},
		})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(1), AmountCp: 5_000}}, res.Winners,
		"1000 bp on the winning rung beats the alt's whole bank")
	require.Equal(t, strategy.TierMain, res.WinningTier)
	require.Contains(t, res.Reason, "tier main takes the item ahead of 1 bid(s) on lower rungs")
	require.NotContains(t, res.Reason, "10000",
		"the count above a bidder is disclosable and the shares below them are not")
}

// TestRelativeBid_SettleAuction_TheTraceRecordsTheShareAndTheFrozenSeq (#224, AO review).
//
// This rule's step 2 is a SHARE and the trace names it as one, in basis points, against the seq the
// balance was frozen at. Rendering it as an `amount` would tell a raider they lost to a bigger bid
// when they lost to a bigger fraction of a smaller bank — which is the whole model, and the thing
// they will argue about.
func TestRelativeBid_SettleAuction_TheTraceRecordsTheShareAndTheFrozenSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"max_bid_bp":10000}`)
	ctx.balances[acct(0)] = 90_000
	ctx.balances[acct(1)] = 50_000

	res, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow},
		[]strategy.Bid{
			{AccountID: acct(0), AmountCp: 36_000, PlacedAt: fixedNow},
			{AccountID: acct(1), AmountCp: 27_500, PlacedAt: fixedNow},
		})
	require.NoError(t, err)

	require.Equal(t, []strategy.ResolutionStepKind{
		strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
		strategy.ResolutionStepShare, strategy.ResolutionStepPrice,
	}, traceKinds(res))

	require.Contains(t, res.Trace[0].Detail, "2 of the 2 bids placed")
	require.Contains(t, res.Trace[1].Detail, "settled nothing", "nothing here was tiered")
	require.Contains(t, res.Trace[2].Detail, "5500 bp")
	require.Contains(t, res.Trace[2].Detail, "seq 5",
		"a share means nothing without the seq the balance under it was frozen at")
	require.Contains(t, res.Trace[3].Detail, "27500")
}

// TestRelativeBid_SettleAuction_TheTraceNamesWhicheverStepDecidedIt (#224, second AO review; #244).
//
// This rule ranks by the SHARE, and rankBids then separates equal shares by WHEN they were placed and
// only then by a seeded roll. Both of those settle real items, so a trace that jumped from the share
// to the price would claim completeness while omitting the step that chose the winner, which is worse
// than no trace: it reads as an answer.
//
// THE FIRST CASE USED TO ASSERT THAT THE COMMITTED AMOUNT DECIDED IT, and that step is what #244
// removed: two bidders at the same share are equal under this model, so breaking the tie by the
// number of points behind the share is breaking it by the size of the bank. The case is kept — the
// same two bids, the same equal share — and now asserts the opposite outcome: the smaller bank that
// bid first takes it, and no `amount` step appears in the trace at all. Asserting the WINNER as well
// as the step is what makes that more than a rename: a trace could record `bid_sequence` while the
// comparator still ranked by the amount above it.
//
// The three cases are the three ways the chain can end below an equal share. Every case holds the
// share equal at 5000 bp — a bid of half of each balance — so the step under test is the only thing
// that differs.
func TestRelativeBid_SettleAuction_TheTraceNamesWhicheverStepDecidedIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		balance core.Centipoints
		amount  core.Centipoints
		placed  core.Micros
		winner  core.ULID
		want    strategy.ResolutionStepKind
		detail  string
	}{
		{
			// Half of 50000 against half of 90000: the same 5000 bp, 20000 fewer points committed, and
			// placed a second earlier. Before #244 the 45000 took it on the amount.
			name:    "the earlier bid decided it, and the larger bank lost",
			balance: 50_000, amount: 25_000, placed: fixedNow.Add(-1_000_000_000),
			winner: acct(1),
			want:   strategy.ResolutionStepBidSequence,
			detail: "earliest",
		},
		{
			// The same balance and the same commitment, placed a second later.
			name:    "the bid sequence decided it",
			balance: 90_000, amount: 45_000, placed: fixedNow.Add(1_000_000_000),
			winner: acct(0),
			want:   strategy.ResolutionStepBidSequence,
			detail: "earliest",
		},
		{
			// Identical in every key the comparator has.
			name:    "a seeded roll decided it",
			balance: 90_000, amount: 45_000, placed: fixedNow,
			want:   strategy.ResolutionStepSeededRoll,
			detail: "seed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 2, 0, `{"max_bid_bp":10000}`)
			ctx.balances[acct(0)] = 90_000
			ctx.balances[acct(1)] = tc.balance

			res, err := strategy.RelativeBid{}.SettleAuction(ctx,
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow},
				[]strategy.Bid{
					{AccountID: acct(0), AmountCp: 45_000, PlacedAt: fixedNow},
					{AccountID: acct(1), AmountCp: tc.amount, PlacedAt: tc.placed},
				})
			require.NoError(t, err)
			require.Len(t, res.Winners, 1)

			if tc.winner != "" {
				require.Equal(t, tc.winner, res.Winners[0].AccountID,
					"an equal share is settled by the chain, never by which bidder committed more")
			}

			kinds := traceKinds(res)
			require.Equal(t, strategy.ResolutionStepShare, kinds[2],
				"the share is still step 2 of the chain; what follows is what it failed to settle")
			require.NotContains(t, kinds, strategy.ResolutionStepAmount,
				"the amount is not a rung of the chain below the share (#244), so it is not a step "+
					"either — a trace naming one would be describing a comparison nothing makes")
			require.Equal(t, tc.want, kinds[len(kinds)-2],
				"the step before the price is the one that chose the winner")
			require.Equal(t, strategy.ResolutionStepPrice, kinds[len(kinds)-1])
			require.Contains(t, res.Trace[len(kinds)-2].Detail, tc.detail)
			require.Contains(t, res.Trace[2].Detail, "5000 bp")
		})
	}
}

// TestRelativeBid_SettleAuction_AnEqualShareDoesNotGoToTheLargerBank is #244's own table, run as the
// issue wrote it.
//
// | Bidder  | Balance at open | Commits | Share   |
// | Tankguy |          900.00 |  450.00 | 5000 bp |
// | Healbot |          500.00 |  250.00 | 5000 bp |
//
// Equal priority under the model, and before this fix Tankguy took it because 45000 > 25000 — "the
// exact inversion docs/concepts/strategies.md says this rule exists to prevent". Both halves are
// asserted here rather than one: whoever bid FIRST wins, whichever of them holds the larger bank, so
// the outcome flips with the placement times and not with the balances. A test that only showed
// Healbot winning would pass against a comparator that had merely been inverted to favour the
// smaller bank, which would be the same defect wearing the other sign.
func TestRelativeBid_SettleAuction_AnEqualShareDoesNotGoToTheLargerBank(t *testing.T) {
	t.Parallel()

	const (
		tankguy, healbot = 0, 1
		second           = core.Micros(1_000_000)
	)

	for _, tc := range []struct {
		name                             string
		tankguyPlacedAt, healbotPlacedAt core.Micros
		want                             core.ULID
	}{
		{
			name:            "the smaller bank bid first",
			tankguyPlacedAt: fixedNow + second, healbotPlacedAt: fixedNow,
			want: acct(healbot),
		},
		{
			name:            "the larger bank bid first",
			tankguyPlacedAt: fixedNow, healbotPlacedAt: fixedNow + second,
			want: acct(tankguy),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 2, 0, `{"max_bid_bp":10000}`)
			ctx.balances[acct(tankguy)] = 90_000
			ctx.balances[acct(healbot)] = 50_000

			res, err := strategy.RelativeBid{}.SettleAuction(ctx,
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow},
				[]strategy.Bid{
					{AccountID: acct(tankguy), AmountCp: 45_000, PlacedAt: tc.tankguyPlacedAt},
					{AccountID: acct(healbot), AmountCp: 25_000, PlacedAt: tc.healbotPlacedAt},
				})

			require.NoError(t, err)
			require.Len(t, res.Winners, 1)
			require.Equal(t, tc.want, res.Winners[0].AccountID,
				"5000 bp is 5000 bp; the earliest of the equal shares takes the item and the bank "+
					"behind it decides nothing")
			require.Nil(t, res.RngSeed,
				"the bid sequence separated them, so no roll was needed to")
			require.Contains(t, res.Reason, "5000 bp")
		})
	}
}

// TestRelativeBid_SettleAuction_NoBidableShare_StillCarriesATrace: the no-award path, which is the
// one an officer arrives at asking why a drop went nowhere. The chain stopped at eligibility, and
// that single step is the answer.
func TestRelativeBid_SettleAuction_NoBidableShare_StillCarriesATrace(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"min_bid_bp":5000,"max_bid_bp":10000}`)
	ctx.balances[acct(0)] = 100_000

	res, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 100, PlacedAt: fixedNow}})

	require.NoError(t, err)
	require.Empty(t, res.Winners)
	require.Equal(t, []strategy.ResolutionStepKind{strategy.ResolutionStepEligibility},
		traceKinds(res))
	require.Contains(t, res.Trace[0].Detail, "bp share")
}

// TestRelativeBid_SettleAuction_ResolvesAgainstTheFrozenBalance is the rule the strategy exists for.
//
// Session.SeqAtOpen is the seq every balance in a settlement is read at, POSITIONALLY. Resolving
// against live balances would let a decay run committed mid-auction rewrite everybody's percentage,
// and the bug would only appear on the one night a decay job overlaps a raid — so the assertion is on
// the seqs that were read, not on the winner, because a planner reading HeadSeq would pick the same
// winner in every test whose balances did not move.
func TestRelativeBid_SettleAuction_ResolvesAgainstTheFrozenBalance(t *testing.T) {
	t.Parallel()

	ctx := spendCtx(t, relativeBidGoldenConfig)

	_, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 3, OpenedAt: fixedNow},
		[]strategy.Bid{
			{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow},
			{AccountID: acct(1), AmountCp: 2_000, PlacedAt: fixedNow},
		})

	require.NoError(t, err)
	require.Equal(t, []int64{3, 3}, ctx.readAtSeq,
		"every balance in the settlement is read at seq_at_open, never at the pool head")
	require.NotEqual(t, ctx.headSeq, int64(3), "the fixture must make the two seqs distinguishable")
}

// TestRelativeBid_SettleAuction_ABidNoLongerAShareOfItsFrozenBalance_IsIgnoredAndSaidToBe.
//
// It happens honestly: a bidder who earned a tick after the session opened may have committed more
// than they held at open. Awarding it would honour a share above the pool's ceiling and failing the
// whole settlement would leave an officer unable to close a session at 01:00 — so the bid is ignored
// and the count is in the reason an officer reads. A silent drop is the one outcome that is not
// acceptable.
func TestRelativeBid_SettleAuction_ABidNoLongerAShareOfItsFrozenBalance_IsIgnoredAndSaidToBe(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"min_bid_bp":1000,"max_bid_bp":7500}`)
	ctx.balances[acct(0)] = 10_000 // bids more than it held at open
	ctx.balances[acct(1)] = 10_000 // bids 90%, above the pool's 75% ceiling
	ctx.balances[acct(2)] = 10_000 // bids 50%, and wins by default

	res, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{
			{AccountID: acct(0), AmountCp: 11_000, PlacedAt: fixedNow},
			{AccountID: acct(1), AmountCp: 9_000, PlacedAt: fixedNow},
			{AccountID: acct(2), AmountCp: 5_000, PlacedAt: fixedNow},
		})

	require.NoError(t, err)
	require.Equal(t, acct(2), res.Winners[0].AccountID)
	require.Contains(t, res.Reason, "2 bid(s) ignored")
}

// TestRelativeBid_SettleAuction_NoBidableShare_Rots: a session in which nothing is a legal share has
// no winner, which is the rot case rather than an error.
func TestRelativeBid_SettleAuction_NoBidableShare_Rots(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, relativeBidGoldenConfig)

	res, err := strategy.RelativeBid{}.SettleAuction(ctx,
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow}})

	require.NoError(t, err)
	require.Empty(t, res.Winners, "a bidder with no balance has no share to commit")
	require.Contains(t, res.Reason, "frozen at seq 5")
}

// TestRelativeBid_SettleAuction_TheSessionMinimumStillApplies: a pool that bids in shares may still
// refuse a bid of two centipoints.
func TestRelativeBid_SettleAuction_TheSessionMinimumStillApplies(t *testing.T) {
	t.Parallel()

	res, err := strategy.RelativeBid{}.SettleAuction(spendCtx(t, `{"min_bid_bp":0}`),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, MinAmountCp: 5_000},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 2, PlacedAt: fixedNow}})

	require.NoError(t, err)
	require.Empty(t, res.Winners)
}

// TestRelativeBid_ValidateBid_ChecksTheShareAgainstThePoolHead.
//
// The signature carries no Session (#219), so the frozen balance is invisible here: this is the
// pre-acceptance guard, and the authoritative share is the settlement's. What it can catch is a bid
// that is unaffordable outright or outside the pool's band at the moment it is placed.
func TestRelativeBid_ValidateBid_ChecksTheShareAgainstThePoolHead(t *testing.T) {
	t.Parallel()

	bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

	for _, tc := range []struct {
		name    string
		balance core.Centipoints
		bid     strategy.Bid
		want    string
	}{
		{
			name:    "a bid of nothing",
			balance: 10_000,
			bid:     strategy.Bid{AccountID: acct(0)},
			want:    "no share of anything",
		},
		{
			name:    "a bidder with no balance",
			balance: 0,
			bid:     strategy.Bid{AccountID: acct(0), AmountCp: 100},
			want:    "no share of nothing to bid",
		},
		{
			// A DIFFERENT MESSAGE from the case above, deliberately: this bidder's balance is fine and
			// their share is not, and one sentence for both sends them to check the wrong number.
			name:    "more than the whole balance",
			balance: 10_000,
			bid:     strategy.Bid{AccountID: acct(0), AmountCp: 10_001},
			want:    "more than the whole of it",
		},
		{
			name:    "under the pool's smallest share",
			balance: 10_000,
			bid:     strategy.Bid{AccountID: acct(0), AmountCp: 100},
			want:    "outside the pool's 500..7500 bp",
		},
		{
			name:    "over the pool's largest share",
			balance: 10_000,
			bid:     strategy.Bid{AccountID: acct(0), AmountCp: 9_000},
			want:    "outside the pool's 500..7500 bp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 1, tc.balance, relativeBidGoldenConfig)

			err := strategy.RelativeBid{}.ValidateBid(ctx, bidder, tc.bid)
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestRelativeBid_Share_IsFlooredRatherThanRounded. Rounding a share up would let a 39.996% bid rank
// as 40%, which in a strategy whose whole ordering is this number is the difference between winning
// and losing an item.
func TestRelativeBid_Share_IsFlooredRatherThanRounded(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"min_bid_bp":3333,"max_bid_bp":3333}`)
	ctx.balances[acct(0)] = 3

	// 1 of 3 is 3333.33… bp. Floored it is 3333, which the band accepts; rounded it would be 3334,
	// which it would not.
	err := strategy.RelativeBid{}.ValidateBid(ctx,
		strategy.AccountRef{ID: acct(0), Kind: "person"},
		strategy.Bid{AccountID: acct(0), AmountCp: 1})

	require.NoError(t, err)
}

// TestRelativeBid_Priority_RanksEveryCandidateEqually.
//
// Each bidder may commit the same share of whatever they hold, so a bank of 900 buys no more priority
// than a bank of 500 — that is the model's premise. Ranking by spendable balance would put the
// hoarder at the top of the board this strategy exists to flatten.
func TestRelativeBid_Priority_RanksEveryCandidateEqually(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, relativeBidGoldenConfig)
	ctx.balances[acct(0)] = 90_000
	ctx.balances[acct(1)] = 50_000

	rich, err := strategy.RelativeBid{}.Priority(ctx, strategy.AccountRef{ID: acct(0), Kind: "person"})
	require.NoError(t, err)

	poor, err := strategy.RelativeBid{}.Priority(ctx, strategy.AccountRef{ID: acct(1), Kind: "person"})
	require.NoError(t, err)

	require.Equal(t, rich.Rank, poor.Rank)
	require.Zero(t, rich.Rank)
	require.Empty(t, ctx.readAtSeq, "an equal rank needs no balance read at all")
}

// TestRelativeBid_PriceHint_HasNoAnswerForAnItem: what a drop costs depends on who is asking, and
// PriceHint is handed an item and no account. nil with a nil error is the façade's documented "no
// hint for this item"; ErrUnsupported would tell a bidding UI not to draw a bid box for a pool whose
// whole point is bidding.
func TestRelativeBid_PriceHint_HasNoAnswerForAnItem(t *testing.T) {
	t.Parallel()

	hint, err := strategy.RelativeBid{}.PriceHint(spendCtx(t, relativeBidGoldenConfig),
		strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"})

	require.NoError(t, err)
	require.Nil(t, hint)
}

// TestRelativeBid_PlanAward_RequiresTheCommittedAmount: the price is not re-derived from a share at
// award time, because the balance it would be derived against has moved since the session opened.
func TestRelativeBid_PlanAward_RequiresTheCommittedAmount(t *testing.T) {
	t.Parallel()

	_, err := strategy.RelativeBid{}.PlanAward(spendCtx(t, relativeBidGoldenConfig),
		strategy.AwardEvent{
			Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:  strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		})

	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "carries no price")
}

// TestRelativeBid_Config_RefusesABandNobodyCouldBidIn.
func TestRelativeBid_Config_RefusesABandNobodyCouldBidIn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"a negative smallest share", `{"min_bid_bp":-1}`, "min_bid_bp"},
		{"a smallest share above the whole balance", `{"min_bid_bp":10001}`, "min_bid_bp"},
		{"a largest share of nothing", `{"max_bid_bp":0}`, "max_bid_bp"},
		{"a largest share above the whole balance", `{"max_bid_bp":10001}`, "max_bid_bp"},
		{"a floor above the ceiling", `{"min_bid_bp":6000,"max_bid_bp":5000}`, "no share is bidable"},
		{"proceeds nobody ships", `{"proceeds":"the_officer"}`, "proceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.RelativeBid{}.PlanAward(spendCtx(t, tc.config), spendAwardEvent(1_000))
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestRelativeBid_SettleAuction_PropagatesAFrozenBalanceFailure: a settlement that could not read a
// balance must not settle on the balances it did read.
func TestRelativeBid_SettleAuction_PropagatesAFrozenBalanceFailure(t *testing.T) {
	t.Parallel()

	ctx := spendCtx(t, relativeBidGoldenConfig)
	ctx.balanceErr = errFacadeDown

	_, err := strategy.RelativeBid{}.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow}})

	require.ErrorIs(t, err, errFacadeDown)
	require.ErrorContains(t, err, "frozen balance")
}
