package strategy_test

import (
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// auction_sealed's arithmetic. Phase 1, #195 and #224.
//
// The family contracts are spend_test.go's table. What is here is the pay rule — the one line of
// arithmetic that separates first price from second — the two clamps that make second price honest (a
// winner never pays more than they bid, and a sole bidder pays the minimum), and the rung the whole
// rule is computed inside.
//
// WHY THE THREE-FIGURE FIXTURES BELOW ARE `alt` SESSIONS. The 350 / 280 / 150 table is the guide's own
// (docs/guides/choosing-a-dkp-system.md) and its arithmetic is untouched, but a three-figure MAIN-tier
// price models the scheme tiering replaced: "when mains only compete with mains, bids land in single
// or low double digits… any screen or fixture showing a three-figure main-tier price is modelling the
// old scheme" (docs/guides/auctions.md), and that page names the 350.00 as precisely the alt bid a
// main is never priced against. So the table is modelled as the rung it describes — which also proves
// second price is computed inside WHICHEVER rung wins, not only inside `main` — and the main-tier
// cases use the ladder's own numbers: a minimum of 5.00 and an increment of 1.00.

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
		{
			AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
		{
			AccountID: acct(1), AmountCp: 28_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
		{
			AccountID: acct(2), AmountCp: 15_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
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
			{
				AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
			{
				AccountID: acct(1), AmountCp: 34_900, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
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
		[]strategy.Bid{{
			AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		}})

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
		{
			AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
		{
			AccountID: acct(0), AmountCp: 30_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(1_000), res.Winners[0].AmountCp,
		"one bidder holding two bids is still a session with one bidder in it, so they pay the minimum")

	res, err = s.SettleAuction(spendCtx(t, config), session, []strategy.Bid{
		{
			AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
		{
			AccountID: acct(0), AmountCp: 30_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
		{
			AccountID: acct(1), AmountCp: 20_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(20_500), res.Winners[0].AmountCp,
		"the runner-up is the highest bid from a DIFFERENT account, not the row below the winner")
}

// tieredSealedConfig is the pool a sealed auction with a working ladder runs: second price, a minimum
// of 5.00 and an increment of 1.00 (docs/guides/auctions.md). See this file's header for why the
// main-tier numbers are small and the three-figure ones are alts.
const tieredSealedConfig = `{"pay_rule":"second_price","min_bid_cp":500,"increment_cp":100}`

// TestAuctionSealed_SettleAuction_SecondPrice_ALoneMainPaysTheMinimumOverALargerAlt is the one number
// a tiered second-price auction must never charge (#224, docs/guides/auctions.md).
//
// A main bids 10.00. Below them two alts bid 350.00 and 280.00. The runner-up is the highest bid from
// another account IN THE WINNING TIER, and there is nobody else in `main` — so this is the sole-bidder
// case and the main pays the minimum of 5.00. Pricing against the alt would charge them 351.00, an
// overcharge of seventy times the correct price with an arithmetic trail that looks right at every
// step, and it is exactly what a settlement that ranked by amount and then filtered by tier would do.
func TestAuctionSealed_SettleAuction_SecondPrice_ALoneMainPaysTheMinimumOverALargerAlt(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, []strategy.Bid{
			{
				AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
			{
				AccountID: acct(1), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
			{
				AccountID: acct(2), AmountCp: 28_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
		})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(1), AmountCp: 500}}, res.Winners,
		"the lone main pays the 5.00 minimum, not the 350.00 alt bid plus an increment")
	require.Equal(t, strategy.TierMain, res.WinningTier)
	require.Contains(t, res.Reason, "only bidder in tier main")
	require.NotContains(t, res.Reason, "35000", "a sealed resolution names no losing amount")
	require.Equal(t,
		[]strategy.TierCount{{Tier: strategy.TierMain, Bids: 1}, {Tier: strategy.TierAlt, Bids: 2}},
		res.TierCounts,
		"the board renders 'cannot win — 1 bid above you' from these counts and from nothing else")
}

// TestAuctionSealed_SettleAuction_SecondPrice_TheRunnerUpIsInTheWinningTier: with two mains and an
// alt, the price comes from the SECOND MAIN and the alt is not in the arithmetic at all.
//
// It is the other half of the lone-main case above, and the two together pin the rule: the runner-up
// is the highest bid from another account on the winning rung, or there is none. 10.00 plus the 1.00
// increment is 11.00 — a main-tier price, in the single or low double digits the ladder produces.
func TestAuctionSealed_SettleAuction_SecondPrice_TheRunnerUpIsInTheWinningTier(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, []strategy.Bid{
			{
				AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
			{
				AccountID: acct(1), AmountCp: 1_200, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
			{
				AccountID: acct(2), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
		})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(1), AmountCp: 1_100}}, res.Winners,
		"the runner-up in `main` is 10.00, so the winner pays 11.00")
	require.Equal(t, strategy.TierMain, res.WinningTier)
	require.Contains(t, res.Reason, "plus one increment")
}

// TestAuctionSealed_SettleAuction_FirstPrice_StillResolvesTheTierFirst.
//
// The pay rule decides what the winner pays and never who wins. First price makes that visible: the
// main pays their own 10.00, and the 350.00 alt neither wins nor prices anything, so a regression that
// resolved the tier only inside the second-price branch would show up here.
func TestAuctionSealed_SettleAuction_FirstPrice_StillResolvesTheTierFirst(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(
		spendCtx(t, `{"pay_rule":"first_price","min_bid_cp":500,"increment_cp":100}`),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, []strategy.Bid{
			{
				AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
			{
				AccountID: acct(1), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
		})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(1), AmountCp: 1_000}}, res.Winners)
	require.Equal(t, strategy.TierMain, res.WinningTier)
}

// TestAuctionSealed_SettleAuction_TheTraceIsWrittenOntoTheResolution: the officer's artefact, on the
// rule whose outcome is hardest to explain from the bids alone — a winner who paid 5.00 while a 350.00
// sat below them.
func TestAuctionSealed_SettleAuction_TheTraceIsWrittenOntoTheResolution(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, []strategy.Bid{
			{
				AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierAlt,
			},
			{
				AccountID: acct(1), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
			{
				AccountID: acct(2), AmountCp: 100, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
		})
	require.NoError(t, err)

	require.Equal(t, []strategy.ResolutionStepKind{
		strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
		strategy.ResolutionStepAmount, strategy.ResolutionStepPrice,
	}, traceKinds(res))

	require.Contains(t, res.Trace[0].Detail, "2 of the 3 bids placed",
		"the 1.00 bid is under the 5.00 minimum and is not a bid the board should count")
	require.Contains(t, res.Trace[1].Detail, "main 1, alt 1")
	require.Contains(t, res.Trace[2].Detail, "1000", "the winning amount is revealed at close")
	require.Contains(t, res.Trace[3].Detail, "second price")

	for _, step := range res.Trace {
		require.NotContains(t, step.Detail, "35000",
			"no step of a sealed resolution may name a losing bid's amount")
	}
}

// sealedTieBids is the case every tie test below is a variation of: two mains tied at 10.00, a third
// main under them at 5.00, and an alt at 350.00 on a rung that cannot win.
//
// THE THREE LOSERS ARE THE POINT OF THE FIXTURE. A tie set is wrong in exactly two directions — it
// can reach below the winning rung and it can swallow a bidder who is merely in the winning tier —
// and each of those has a bidder here to catch it: the 350.00 alt is the largest bid in the session,
// and the third main is on the winning rung and not tied.
func sealedTieBids() []strategy.Bid {
	return []strategy.Bid{
		{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true, Tier: strategy.TierMain},
		{
			AccountID: acct(1), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		},
		{AccountID: acct(2), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true, Tier: strategy.TierMain},
		{AccountID: acct(3), AmountCp: 500, PlacedAt: fixedNow, Sealed: true, Tier: strategy.TierMain},
	}
}

// TestAuctionSealed_SettleAuction_AWithinTierTie_NamesThePartiesAndAsksForARebid is #248: the
// settlement that used to pick one of two equal bidders with a coin flip nobody was told about now
// stops, names them, and asks for a rebid round.
//
// THE ASSERTIONS ARE THE ISSUE'S OWN THREE. The tie set is exactly the equal bidders on the winning
// rung — not the alt below it, not the main who bid less — the outcome is flagged as needing a rebid
// rather than a winner, and no randomness was consumed on the way there.
func TestAuctionSealed_SettleAuction_AWithinTierTie_NamesThePartiesAndAsksForARebid(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, sealedTieBids())
	require.NoError(t, err)

	require.Empty(t, res.Winners, "two bidders tied at the top means nobody has won it yet")
	require.NotNil(t, res.Tie, "a settlement that awards nobody has to say why")
	require.Equal(t, strategy.Tie{
		Tier:          strategy.TierMain,
		AmountCp:      1_000,
		Accounts:      []core.ULID{acct(0), acct(2)},
		RebidRequired: true,
	}, *res.Tie,
		"exactly the equal bidders in the winning tier: not the 350.00 alt, not the 5.00 main beside them")
	require.Equal(t, 1, res.Tie.MaxPasses(),
		"both tied bidders are offered the pass and they may not both take it, so one of the two is "+
			"the most that may")
	require.Zero(t, strategy.Tie{}.MaxPasses(),
		"and a tie of nobody permits no passes rather than minus one of them")
	require.Nil(t, res.RngSeed, "the roll is the fallback and nobody asked for it")
	require.Empty(t, res.WinningTier, "nothing took the item; the rung the rebid runs on is on the tie")
	require.Empty(t, res.TierCounts)

	require.Contains(t, res.Reason, "nothing settles it automatically",
		"the officer's sentence has to say that the platform is not going to decide this")
	require.Contains(t, res.Reason, "rebid round among exactly those bidders")
	require.Contains(t, res.Reason, "1001 centipoints or more",
		"a rebid beats the tie value; standing on it again is what everybody already did")
	require.Contains(t, res.Reason, "but the last may pass")

	require.Equal(t, []strategy.ResolutionStepKind{
		strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
		strategy.ResolutionStepAmount, strategy.ResolutionStepRebidRequired,
	}, traceKinds(res), "the chain stops where it tied, and says that it stopped")

	for _, step := range res.Trace {
		require.NotContains(t, step.Detail, "35000",
			"no step of a sealed resolution may name a losing bid's amount, tie or no tie")
	}
}

// TestAuctionSealed_SettleAuction_ATieIsBetweenBIDDERSRatherThanBids: one account holding both of the
// top bids is not tied with itself.
//
// Bids are append-only and a bidder may hold several — a raise, a retraction and its replacement — so
// two rows at the same amount are routinely one person. A tie declared there would open a rebid round
// against nobody, and the item would sit on the floor waiting for a second bidder who does not exist.
func TestAuctionSealed_SettleAuction_ATieIsBetweenBIDDERSRatherThanBids(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, []strategy.Bid{
			{
				AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
			{
				AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true,
				Tier: strategy.TierMain,
			},
		})

	require.NoError(t, err)
	require.Nil(t, res.Tie, "one bidder holding two equal bids is a session with one bidder in it")
	require.Equal(t, []strategy.Allocation{{AccountID: acct(0), AmountCp: 500}}, res.Winners,
		"and they pay the minimum, because there is no runner-up on their rung")
}

// TestAuctionSealed_SettleAuction_ADuplicateTopBid_IsStillOneBidder is the AO review's finding on
// #248, and it is about WHO IS COUNTED rather than about who wins.
//
// One account holding two identical top bids beside a second account holding one is a tie between
// TWO people, and every count the settlement makes has to say two: the named set, the pass budget,
// and — for the rules that still roll — the size of the draw. Nothing about a resolved outcome shows
// a miscount, which is why the draw is asserted from the fake Rng rather than inferred from a winner.
// Here the draw must be EMPTY, because a sealed tie is never resolved automatically at all; the
// family test in spend_test.go is where the two rules that do roll are held to the same count.
func TestAuctionSealed_SettleAuction_ADuplicateTopBid_IsStillOneBidder(t *testing.T) {
	t.Parallel()

	top := func(id core.ULID) strategy.Bid {
		return strategy.Bid{
			AccountID: id, AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true, Tier: strategy.TierMain,
		}
	}

	for _, tc := range []struct {
		name string
		bids []strategy.Bid
		tied []core.ULID
	}{
		{
			name: "two rows from one bidder and one from another",
			bids: []strategy.Bid{top(acct(0)), top(acct(0)), top(acct(1))},
			tied: []core.ULID{acct(0), acct(1)},
		},
		{
			name: "three rows and two rows",
			bids: []strategy.Bid{top(acct(0)), top(acct(1)), top(acct(0)), top(acct(1)), top(acct(0))},
			tied: []core.ULID{acct(0), acct(1)},
		},
		{
			name: "every tied row is the same bidder, so there is nobody to be tied with",
			bids: []strategy.Bid{top(acct(0)), top(acct(0)), top(acct(0))},
			tied: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tieredSealedConfig)

			res, err := strategy.AuctionSealed{}.SettleAuction(ctx,
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, tc.bids)
			require.NoError(t, err)

			require.Empty(t, ctx.rng.draws,
				"a sealed settlement never rolls: the case a roll would have decided is the case it "+
					"reports, and a duplicate row is not a second ticket in a round that never happens")
			require.Nil(t, res.RngSeed)

			if tc.tied == nil {
				require.Nil(t, res.Tie, "one bidder holding every top row has won, and alone")
				require.Len(t, res.Winners, 1)
				require.Equal(t, acct(0), res.Winners[0].AccountID)

				return
			}

			require.Empty(t, res.Winners)
			require.Equal(t, tc.tied, res.Tie.Accounts,
				"two people are tied however many rows one of them holds")
			require.Equal(t, 1, res.Tie.MaxPasses())
		})
	}
}

// TestAuctionSealed_SettleAuction_TheTieSetIsOrderedAndIndependentOfTheInput: the same session
// settles to the same named parties whatever order the bids arrive in.
//
// A resolution is written down and read back months later, so the list of who was tied cannot depend
// on the order a caller's query happened to return. It is the determinism argument the tie-break
// chain makes, applied to the artefact that records a tie rather than to the one that breaks one.
func TestAuctionSealed_SettleAuction_TheTieSetIsOrderedAndIndependentOfTheInput(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionSealed{}
	session := strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}

	forwards, err := s.SettleAuction(spendCtx(t, tieredSealedConfig), session, sealedTieBids())
	require.NoError(t, err)

	reversed := sealedTieBids()
	slices.Reverse(reversed)

	backwards, err := s.SettleAuction(spendCtx(t, tieredSealedConfig), session, reversed)
	require.NoError(t, err)

	require.Equal(t, forwards, backwards,
		"the whole resolution, not only the tie set: two callers holding the same bids in different "+
			"orders settle identically or the record of a tie is a record of a query plan")
	require.Equal(t, []core.ULID{acct(0), acct(2)}, backwards.Tie.Accounts)
}

// TestAuctionSealed_SettleAuction_AThreeWayTie_LeavesOnePartyUnableToPass, which is the rule that
// makes a rebid round terminate: every tied bidder may decline to raise, and the last one standing
// takes the item at the amount they all tied on.
func TestAuctionSealed_SettleAuction_AThreeWayTie_LeavesOnePartyUnableToPass(t *testing.T) {
	t.Parallel()

	bids := sealedTieBids()
	bids = append(bids, strategy.Bid{
		AccountID: acct(4), AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true, Tier: strategy.TierMain,
	})

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, bids)

	require.NoError(t, err)
	require.Equal(t, []core.ULID{acct(0), acct(2), acct(4)}, res.Tie.Accounts)
	require.Equal(t, 2, res.Tie.MaxPasses(), "all but one of the three")
	require.Equal(t, core.Centipoints(1_000), res.Tie.AmountCp,
		"and the last of the three to still be standing pays what they all tied on")
	require.Equal(t, core.Centipoints(1_001), res.Tie.MinRebidCp(),
		"while any of them takes it outright by beating that number")
}

// TestAuctionSealed_SettleAuction_ATie_IsNeverResolvedAutomatically is the rule this whole outcome
// exists to serve, stated where a future change would have to delete it to break it.
//
// A tie is settled BY HAND — a rebid above the tie value, or a pass — and there is no argument, no
// config knob and no session flag that makes the settlement pick a winner instead. The two shapes
// below are the two the automatic chain would have decided: bids placed in the same microsecond,
// which the seeded roll used to take, and bids a microsecond apart, which the bid sequence used to
// take. Both are now the same outcome, because in a blind auction neither step knows anything.
func TestAuctionSealed_SettleAuction_ATie_IsNeverResolvedAutomatically(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		placed [2]core.Micros
	}{
		{name: "placed in the same microsecond, which the roll used to take", placed: [2]core.Micros{fixedNow, fixedNow}},
		{name: "placed a microsecond apart, which the sequence used to take", placed: [2]core.Micros{fixedNow + 1, fixedNow}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tieredSealedConfig)

			res, err := strategy.AuctionSealed{}.SettleAuction(ctx,
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, []strategy.Bid{
					{
						AccountID: acct(0), AmountCp: 1_000, PlacedAt: tc.placed[0], Sealed: true,
						Tier: strategy.TierMain,
					},
					{
						AccountID: acct(1), AmountCp: 1_000, PlacedAt: tc.placed[1], Sealed: true,
						Tier: strategy.TierMain,
					},
				})

			require.NoError(t, err)
			require.Empty(t, res.Winners, "nobody won it; two people are still equal")
			require.Equal(t, []core.ULID{acct(0), acct(1)}, res.Tie.Accounts)
			require.Nil(t, res.RngSeed)
			require.Zero(t, ctx.rng.calls,
				"a sealed settlement touches the Rng on no path at all: the seed is not merely unused, "+
					"it is never taken, so the pool's randomness is exactly where the next round finds it")
			require.NotContains(t, res.Reason, "roll")
			require.NotContains(t, res.Reason, "earliest")
		})
	}
}

// TestAuctionSealed_SettleAuction_ARebidMustBeatTheTie: the floor of the round that settles a tie is
// strictly ABOVE the tie value.
//
// "A tie must be hand resolved either by someone choosing to rebid more than the tie value or
// passing" — so standing on the same number again is not a rebid, it is what everybody already did,
// and a round that accepted it would reproduce the same tie for ever. One centipoint above, not one
// increment: a sealed auction has no bid lattice on purpose, and `increment_cp` is a rule about what
// a second-price winner PAYS.
func TestAuctionSealed_SettleAuction_ARebidMustBeatTheTie(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, tieredSealedConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, sealedTieBids())
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(1_000), res.Tie.AmountCp)
	require.Equal(t, core.Centipoints(1_001), res.Tie.MinRebidCp(),
		"the smallest bid that WINS the rebid is one centipoint above the tie")
	require.Equal(t, core.Centipoints(1_000), strategy.Tie{AmountCp: 1_000}.AmountCp,
		"and the tie value itself is what the last bidder standing pays if the others pass")

	require.Contains(t, res.Reason, "nothing settles it automatically")
	require.Contains(t, res.Reason, "1001 centipoints or more")

	// Saturating rather than wrapping: an unrepresentable raise leaves passing as the only move, and
	// a floor that wrapped below the tie would let a rebid win by bidding LESS than the tie.
	require.Equal(t, core.Centipoints(math.MaxInt64),
		strategy.Tie{AmountCp: math.MaxInt64}.MinRebidCp())
}

// TestAuctionSealed_SettleAuction_BelowTheMinimum_Rots: sealed or not, a bid under the floor cannot
// win, and a session of nothing but those has no winner.
func TestAuctionSealed_SettleAuction_BelowTheMinimum_Rots(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionSealed{}.SettleAuction(spendCtx(t, auctionSealedGoldenConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5},
		[]strategy.Bid{{
			AccountID: acct(0), AmountCp: 100, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		}})

	require.NoError(t, err)
	require.Empty(t, res.Winners)
	require.Contains(t, res.Reason, "minimum")
}

// TestAuctionSealed_SettleAuction_ASessionMayNotLowerThePoolFloor, and under second price it must not
// lower what a sole bidder pays either.
//
// The second half is the one with a price attached: a lone bidder pays the minimum, so a session that
// could name a lower one would charge them that instead — a discount granted by whoever opened the
// session rather than by the settings page. See sessionMinimum for why raising is a session's business
// and lowering is the guild's.
func TestAuctionSealed_SettleAuction_ASessionMayNotLowerThePoolFloor(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionSealed{}
	session := strategy.Session{ID: acct(60), SeqAtOpen: 5, MinAmountCp: 1}

	rots, err := s.SettleAuction(spendCtx(t, auctionSealedGoldenConfig), session,
		[]strategy.Bid{{
			AccountID: acct(0), AmountCp: 250, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		}})
	require.NoError(t, err)
	require.Empty(t, rots.Winners,
		"a session naming a minimum of 1 must not make a 250 bid eligible in a pool whose floor is 500")

	sole, err := s.SettleAuction(
		spendCtx(t, `{"pay_rule":"second_price","min_bid_cp":1000,"increment_cp":500}`), session,
		[]strategy.Bid{{
			AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Sealed: true,
			Tier: strategy.TierAlt,
		}})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(1_000), sole.Winners[0].AmountCp,
		"the sole bidder pays the POOL's 1000, not the 1 the session asked for")
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
//
// UNTIERED ON PURPOSE, unlike the rest of this file: it is also the case every session in production
// is today, because the field is filled in by the bid FSM in Phase 6. Second price over one implicit
// rung must settle exactly as it did before the ladder existed.
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
	require.Equal(t, strategy.TierAnyone, res.WinningTier)
	require.NotContains(t, res.Reason, "tier",
		"nothing was tiered, so the ladder settled nothing and says nothing")
}
