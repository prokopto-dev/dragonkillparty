package strategy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// auction_sealed's arithmetic. Phase 1, #195.
//
// The family contracts are spend_test.go's table. What is here is the pay rule — the one line of
// arithmetic that separates first price from second — and the two clamps that make second price
// honest: a winner never pays more than they bid, and a sole bidder pays the minimum.

// auctionSealedGoldenDir is where the canonical proposals live.
const auctionSealedGoldenDir = "../../test/golden/strategy/auction_sealed"

// auctionSealedGoldenConfig sets every knob to a non-default value, `pay_rule` included: the shipped
// default is second price, so the golden runs first price and a knob that stopped being read shows up
// as a changed config snapshot.
const auctionSealedGoldenConfig = `{"pay_rule":"first_price","min_bid_cp":500,"increment_cp":250,` +
	`"proceeds":"attendees","solo_policy":"write_off","floor_cp":-500}`

// auctionSealedGoldenCases is one case per planner this strategy answers.
func auctionSealedGoldenCases() []goldenCase {
	s := strategy.AuctionSealed{}
	raid, itemAward := acct(81), acct(82)

	return []goldenCase{
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				character := acct(50)
				price := core.Centipoints(1_250)

				p, err := s.PlanAward(spendCtx(tb, auctionSealedGoldenConfig), strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					CharacterID:   &character,
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
					RaidID:        &raid,
					ItemAwardID:   &itemAward,
					EffectiveAt:   fixedNow,
					Reason:        "Nagafen, sealed bids revealed at close",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(spendCtx(tb, auctionSealedGoldenConfig),
					strategy.AdjustmentEvent{
						Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
						AmountCp:    -750,
						EffectiveAt: fixedNow,
						Reason:      "settled at the wrong runner-up",
					})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := spendCtx(tb, auctionSealedGoldenConfig)
				price := core.Centipoints(1_250)

				original, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
					EffectiveAt:   fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:        "Nagafen, sealed bids revealed at close",
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

// TestAuctionSealed_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal.
func TestAuctionSealed_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, auctionSealedGoldenDir, auctionSealedGoldenCases())
}

// TestAuctionSealed_Goldens_CoverEveryPlanner is the anti-drift half.
func TestAuctionSealed_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, auctionSealedGoldenDir, auctionSealedGoldenCases(),
		[]string{"adjustment", "award", "reversal"})
}

// TestAuctionSealed_SettleAuction_PayRule is the guide's own table, in centipoints
// (docs/guides/choosing-a-dkp-system.md): bids of 350, 280 and 150 with a delta of 5 settle at 350
// under first price and at 285 under second.
func TestAuctionSealed_SettleAuction_PayRule(t *testing.T) {
	t.Parallel()

	bids := []strategy.Bid{
		{AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true},
		{AccountID: acct(1), AmountCp: 28_000, PlacedAt: fixedNow, Sealed: true},
		{AccountID: acct(2), AmountCp: 15_000, PlacedAt: fixedNow, Sealed: true},
	}

	for _, tc := range []struct {
		name   string
		config string
		want   core.Centipoints
	}{
		{"first price", `{"pay_rule":"first_price","min_bid_cp":1000,"increment_cp":500}`, 35_000},
		{"second price", `{"pay_rule":"second_price","min_bid_cp":1000,"increment_cp":500}`, 28_500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tc.config),
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, bids)

			require.NoError(t, err)
			require.Equal(t, []strategy.Allocation{{AccountID: acct(0), AmountCp: tc.want}},
				res.Winners, "the highest bid always wins; only what it pays changes")
			require.Nil(t, res.RngSeed)
		})
	}
}

// TestAuctionSealed_SettleAuction_SecondPrice_NeverExceedsTheWinningBid is the clamp, and it is the
// failure that looks correct in every test whose increment happens to be smaller than the gap between
// the top two bids.
//
// 350 and 349 with an increment of 5: the runner-up plus one increment is 354, which is more than the
// winner offered. Charging it would break the one promise second price makes.
func TestAuctionSealed_SettleAuction_SecondPrice_NeverExceedsTheWinningBid(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(
		spendCtx(t, `{"pay_rule":"second_price","min_bid_cp":1000,"increment_cp":500}`),
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{
			{AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true},
			{AccountID: acct(1), AmountCp: 34_900, PlacedAt: fixedNow, Sealed: true},
		})

	require.NoError(t, err)
	require.Equal(t, core.Centipoints(35_000), res.Winners[0].AmountCp)
	require.Contains(t, res.Reason, "exceeds the winning bid")
}

// TestAuctionSealed_SettleAuction_SecondPrice_ALoneBidderPaysTheMinimum
// (docs/guides/choosing-a-dkp-system.md). Charging their own bid would silently make an uncontested
// item a first-price auction, which is exactly the incentive second price exists to remove.
func TestAuctionSealed_SettleAuction_SecondPrice_ALoneBidderPaysTheMinimum(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(
		spendCtx(t, `{"pay_rule":"second_price","min_bid_cp":1000,"increment_cp":500}`),
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true}})

	require.NoError(t, err)
	require.Equal(t, core.Centipoints(1_000), res.Winners[0].AmountCp)
	require.Contains(t, res.Reason, "only bidder")
}

// TestAuctionSealed_SettleAuction_SecondPrice_TheRunnerUpIsAnotherAccount is the overcharge with a
// plausible-looking arithmetic trail.
//
// Bids are append-only and a bidder may hold several — a raise, a retraction and its replacement — so
// the row below the winner is frequently the winner's OWN earlier bid. Pricing against it would
// charge a bidder their own number plus an increment while every other assertion still passed.
func TestAuctionSealed_SettleAuction_SecondPrice_TheRunnerUpIsAnotherAccount(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionSealed{}
	config := `{"pay_rule":"second_price","min_bid_cp":1000,"increment_cp":500}`
	session := strategy.Session{ID: acct(60), SeqAtOpen: 5}

	res, err := s.SettleAuction(spendCtx(t, config), session, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true},
		{AccountID: acct(0), AmountCp: 30_000, PlacedAt: fixedNow, Sealed: true},
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(1_000), res.Winners[0].AmountCp,
		"one bidder holding two bids is still a session with one bidder in it, so they pay the minimum")

	res, err = s.SettleAuction(spendCtx(t, config), session, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true},
		{AccountID: acct(0), AmountCp: 30_000, PlacedAt: fixedNow, Sealed: true},
		{AccountID: acct(1), AmountCp: 20_000, PlacedAt: fixedNow, Sealed: true},
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(20_500), res.Winners[0].AmountCp,
		"the runner-up is the highest bid from a DIFFERENT account, not the row below the winner")
}

// TestAuctionSealed_SettleAuction_BelowTheMinimum_Rots: sealed or not, a bid under the floor cannot
// win, and a session of nothing but those has no winner.
func TestAuctionSealed_SettleAuction_BelowTheMinimum_Rots(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, auctionSealedGoldenConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 100, PlacedAt: fixedNow, Sealed: true}})

	require.NoError(t, err)
	require.Empty(t, res.Winners)
	require.Contains(t, res.Reason, "minimum")
}

// TestAuctionSealed_ValidateBid_RejectsWhatASessionMustNotAccept.
//
// The unsealed case is the one that is about confidentiality rather than arithmetic: Bid.Sealed is
// what tells every layer downstream that this amount must not be logged, rendered or returned before
// the reveal, and a bid arriving without it is a caller that will leak it.
func TestAuctionSealed_ValidateBid_RejectsWhatASessionMustNotAccept(t *testing.T) {
	t.Parallel()

	bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

	for _, tc := range []struct {
		name string
		bid  strategy.Bid
		want string
	}{
		{
			name: "not marked sealed",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 1_000},
			want: "not marked sealed",
		},
		{
			name: "below the minimum",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 250, Sealed: true},
			want: "below the minimum",
		},
		{
			name: "more than the bidder holds",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 10_500, Sealed: true},
			want: "spendable balance",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := strategy.AuctionSealed{}.ValidateBid(spendCtx(t, auctionSealedGoldenConfig),
				bidder, tc.bid)
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestAuctionSealed_ValidateBid_TakesAnyAmountAboveTheMinimum: there is no increment lattice under
// sealed bidding, because quantising a bid would quantise exactly the valuations second price exists
// to elicit. 285 is a legal sealed bid where an open auction would refuse it.
func TestAuctionSealed_ValidateBid_TakesAnyAmountAboveTheMinimum(t *testing.T) {
	t.Parallel()

	err := strategy.AuctionSealed{}.ValidateBid(spendCtx(t, auctionSealedGoldenConfig),
		strategy.AccountRef{ID: acct(0), Kind: "person"},
		strategy.Bid{AccountID: acct(0), AmountCp: 762, Sealed: true})

	require.NoError(t, err)
}

// TestAuctionSealed_PriceHint_IsTheMinimumAndNotTheCatalogue.
//
// A published expected value is a signal about what others will bid, and a sealed auction's whole
// design is that no such signal exists while the session is open. The minimum discloses nothing and,
// under second price, is frequently what the winner actually pays.
func TestAuctionSealed_PriceHint_IsTheMinimumAndNotTheCatalogue(t *testing.T) {
	t.Parallel()

	catalogue := core.Centipoints(30_000)

	hint, err := strategy.AuctionSealed{}.PriceHint(spendCtx(t, auctionSealedGoldenConfig),
		strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames", FixedPriceCp: &catalogue})

	require.NoError(t, err)
	require.NotNil(t, hint)
	require.Equal(t, core.Centipoints(500), *hint)
}

// TestAuctionSealed_PlanAward_RequiresTheSettledPrice: under second price the settled number is not
// even the winner's own bid, so it is the one value a caller must not re-derive.
func TestAuctionSealed_PlanAward_RequiresTheSettledPrice(t *testing.T) {
	t.Parallel()

	_, err := strategy.AuctionSealed{}.PlanAward(spendCtx(t, auctionSealedGoldenConfig),
		strategy.AwardEvent{
			Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:  strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		})

	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "carries no price")
}

// TestAuctionSealed_Config_RefusesWhatWouldMakeTheAuctionUnrunnable.
func TestAuctionSealed_Config_RefusesWhatWouldMakeTheAuctionUnrunnable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"a pay rule nobody ships", `{"pay_rule":"third_price"}`, "pay_rule"},
		{"a minimum of nothing", `{"min_bid_cp":0}`, "min_bid_cp"},
		{"an increment of nothing", `{"increment_cp":0}`, "increment_cp"},
		{"proceeds nobody ships", `{"proceeds":"the_officer"}`, "proceeds"},
		{"a solo policy nobody ships", `{"solo_policy":"residue"}`, "solo_policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.AuctionSealed{}.PlanAward(spendCtx(t, tc.config), spendAwardEvent(1_000))
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestAuctionSealed_DefaultConfig_IsSecondPrice pins the recommended default: a pool that has set
// nothing runs the rule that makes bidding your true valuation optimal.
func TestAuctionSealed_DefaultConfig_IsSecondPrice(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, ""),
		strategy.Session{ID: acct(60), SeqAtOpen: 5}, []strategy.Bid{
			{AccountID: acct(0), AmountCp: 5_000, PlacedAt: fixedNow, Sealed: true},
			{AccountID: acct(1), AmountCp: 3_000, PlacedAt: fixedNow, Sealed: true},
		})

	require.NoError(t, err)
	require.Equal(t, core.Centipoints(3_100), res.Winners[0].AmountCp,
		"the runner-up's 3000 plus the default 100 increment")
}
