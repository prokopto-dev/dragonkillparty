package strategy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// auction_open's arithmetic. Phase 1, #195.
//
// The family contracts — the unsupported planners, the adjustment, the reversal, the bid identity
// checks, the façade failures — are spend_test.go's table. What is here is what makes this strategy
// different from the other three: the increment lattice, the winner paying their own bid, and the
// tie-break that ends in a seeded roll.

// auctionOpenGoldenDir is where the canonical proposals live. Under test/golden/ rather than beside
// this file because that tree is CODEOWNERS-protected and is gated against shrinking.
const auctionOpenGoldenDir = "../../test/golden/strategy/auction_open"

// auctionOpenGoldenConfig sets every knob to a non-default value, so that a knob which stopped being
// read shows up as a changed golden rather than as nothing.
const auctionOpenGoldenConfig = `{"min_bid_cp":500,"increment_cp":250,"proceeds":"attendees",` +
	`"solo_policy":"write_off","floor_cp":-500}`

// auctionOpenGoldenCases is one case per planner this strategy answers. The three it refuses
// (attendance, decay) contribute no proposal to compare — an ErrUnsupported has no canonical form.
func auctionOpenGoldenCases() []goldenCase {
	s := strategy.AuctionOpen{}
	raid, itemAward := acct(81), acct(82)

	return []goldenCase{
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				character := acct(50)
				price := core.Centipoints(1_250)

				p, err := s.PlanAward(spendCtx(tb, auctionOpenGoldenConfig), strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					CharacterID:   &character,
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
					RaidID:        &raid,
					ItemAwardID:   &itemAward,
					EffectiveAt:   fixedNow,
					Reason:        "Nagafen, open auction at 12.50",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(spendCtx(tb, auctionOpenGoldenConfig),
					strategy.AdjustmentEvent{
						Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
						AmountCp:    -750,
						EffectiveAt: fixedNow,
						Reason:      "outbid by a retracted bid",
					})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := spendCtx(tb, auctionOpenGoldenConfig)
				price := core.Centipoints(1_250)

				original, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
					EffectiveAt:   fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:        "Nagafen, open auction at 12.50",
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

// TestAuctionOpen_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestAuctionOpen_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, auctionOpenGoldenDir, auctionOpenGoldenCases())
}

// TestAuctionOpen_Goldens_CoverEveryPlanner is the anti-drift half: a planner added without a golden
// would leave the whole-proposal assertion covering fewer planners than the strategy has, silently.
func TestAuctionOpen_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, auctionOpenGoldenDir, auctionOpenGoldenCases(),
		[]string{"adjustment", "award", "reversal"})
}

// TestAuctionOpen_PlanAward_RequiresTheSettledPrice: an auction has no catalogue price and no pool
// default to fall back to, so an award that names no price is refused rather than priced from
// somewhere else.
func TestAuctionOpen_PlanAward_RequiresTheSettledPrice(t *testing.T) {
	t.Parallel()

	catalogue := core.Centipoints(9_999)

	_, err := strategy.AuctionOpen{}.PlanAward(spendCtx(t, auctionOpenGoldenConfig),
		strategy.AwardEvent{
			Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:  strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames", FixedPriceCp: &catalogue},
		})

	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "carries no price",
		"the item's catalogue price must NOT be used: it would charge an auction winner a "+
			"fixed-price guild's number")
}

// TestAuctionOpen_SettleAuction_HighestBidWinsAndPaysIt is the guide's worked example, in
// centipoints: minimum 100, increment 25, and the 200 that closes it (choosing-a-dkp-system.md).
func TestAuctionOpen_SettleAuction_HighestBidWinsAndPaysIt(t *testing.T) {
	t.Parallel()

	const config = `{"min_bid_cp":10000,"increment_cp":2500}`

	res, err := strategy.AuctionOpen{}.SettleAuction(spendCtx(t, config), strategy.Session{
		ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow,
	}, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 10_000, PlacedAt: fixedNow},
		{AccountID: acct(1), AmountCp: 12_500, PlacedAt: fixedNow.Add(1_000_000_000)},
		{AccountID: acct(0), AmountCp: 15_000, PlacedAt: fixedNow.Add(2_000_000_000)},
		{AccountID: acct(1), AmountCp: 17_500, PlacedAt: fixedNow.Add(3_000_000_000)},
		{AccountID: acct(0), AmountCp: 20_000, PlacedAt: fixedNow.Add(4_000_000_000)},
	})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(0), AmountCp: 20_000}}, res.Winners,
		"the leader wins and pays their own bid — an ascending auction has already revealed what the "+
			"item is worth to everyone else")
	require.Nil(t, res.RngSeed, "an outright winner consumes no randomness")
	require.Contains(t, res.Reason, "20000")
}

// tieredOpenConfig is the pool an auction with a working ladder actually runs: a minimum of 5.00 and
// an increment of 1.00 (docs/guides/auctions.md).
//
// THE NUMBERS ARE SMALL BY DESIGN AND THAT IS THE POINT. When mains only compete with mains, bids land
// in single or low double digits — the whole three-figure economy of the old scheme was mains bidding
// against alts with a bank behind them. A fixture showing a three-figure main-tier price is modelling
// the system this deliverable replaced.
const tieredOpenConfig = `{"min_bid_cp":500,"increment_cp":100}`

// TestAuctionOpen_SettleAuction_ATenPointMainBeatsAThreeHundredAndFiftyPointAlt is the single most
// consequential rule in the product, in one assertion (#224, docs/guides/auctions.md).
//
// The alt's 350.00 is the larger number by a factor of thirty-five and it is NEVER COMPARED against
// the main's 10.00: the first phase finds the highest rung holding an eligible bid, that rung takes
// the item, and the amount comparison happens only among the bids standing on it. Inverting this is
// worse than not implementing it, which is why #195 shipped the auctions without a tier rather than
// with an approximated one.
func TestAuctionOpen_SettleAuction_ATenPointMainBeatsAThreeHundredAndFiftyPointAlt(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionOpen{}.SettleAuction(spendCtx(t, tieredOpenConfig), strategy.Session{
		ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow,
	}, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 35_000, PlacedAt: fixedNow, Tier: strategy.TierAlt},
		{
			AccountID: acct(1), AmountCp: 1_000, PlacedAt: fixedNow.Add(1_000_000_000),
			Tier: strategy.TierMain,
		},
		{
			AccountID: acct(2), AmountCp: 28_000, PlacedAt: fixedNow.Add(2_000_000_000),
			Tier: strategy.TierAlt,
		},
	})

	require.NoError(t, err)
	require.Equal(t, []strategy.Allocation{{AccountID: acct(1), AmountCp: 1_000}}, res.Winners,
		"the main pays their own 10.00 and the two alts are not in the comparison at all")
	require.Equal(t, strategy.TierMain, res.WinningTier)
	require.Nil(t, res.RngSeed, "the ladder decided it outright")
	require.Contains(t, res.Reason, "tier main takes the item ahead of 2 bid(s) on lower rungs",
		"the losing bidder arrives to argue about exactly this sentence")
	require.NotContains(t, res.Reason, "35000",
		"a resolution is pasted into guild chat and never republishes a losing bid's amount")
}

// TestAuctionOpen_SettleAuction_TheWholeLadderIsOrdered walks it rung by rung, with the amounts
// INVERTED against the order: whoever is highest on the ladder has bid the least.
//
// One assertion per rung rather than one for `main`, because the ordering is what canonical §5 calls
// the one enum whose declaration order is semantic — and an implementation that special-cased `main`
// and compared the rest by amount would pass a test that only ever checks the top of the ladder.
func TestAuctionOpen_SettleAuction_TheWholeLadderIsOrdered(t *testing.T) {
	t.Parallel()

	ladder := strategy.Tiers()
	require.Equal(t, []string{"main", "main_offspec", "alt", "anyone"}, ladder,
		"the order IS the rule (canonical §5); a reordering here is a change to who wins items")

	for cut := range ladder {
		present := ladder[cut:]

		t.Run(present[0], func(t *testing.T) {
			t.Parallel()

			bids := make([]strategy.Bid, 0, len(present))
			for i, tier := range present {
				bids = append(bids, strategy.Bid{
					// The lower the rung, the larger the bid: 5.00 for the top one present, then
					// 105.00, 205.00, 305.00.
					AccountID: acct(i), AmountCp: core.Centipoints(500 + i*10_000),
					PlacedAt: fixedNow, Tier: tier,
				})
			}

			res, err := strategy.AuctionOpen{}.SettleAuction(spendCtx(t, tieredOpenConfig),
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, bids)

			require.NoError(t, err)
			require.Equal(t, present[0], res.WinningTier)
			require.Equal(t, acct(0), res.Winners[0].AccountID,
				"the highest rung present wins on the smallest bid in the session")
			require.Equal(t, core.Centipoints(500), res.Winners[0].AmountCp)
			require.Len(t, res.TierCounts, len(present),
				"every rung holding an eligible bid is counted, and no rung that holds none")
			require.Equal(t, strategy.TierCount{Tier: present[0], Bids: 1}, res.TierCounts[0],
				"the counts are ordered highest rung first, so the winning tier leads them")
		})
	}
}

// TestAuctionOpen_SettleAuction_TheTraceIsWrittenOntoTheResolution.
//
// "The whole trace is written onto the resolution, so an officer can explain an outcome months later
// without re-deriving it" (docs/guides/auctions.md). The three cases below are the three ways the
// chain can end once the ladder has run — the amount settled it, the bid sequence settled it, or a
// seeded roll did — and the trace names which, rather than leaving an officer to infer it from a
// sentence.
func TestAuctionOpen_SettleAuction_TheTraceIsWrittenOntoTheResolution(t *testing.T) {
	t.Parallel()

	main := func(id core.ULID, amount core.Centipoints, at core.Micros) strategy.Bid {
		return strategy.Bid{AccountID: id, AmountCp: amount, PlacedAt: at, Tier: strategy.TierMain}
	}

	for _, tc := range []struct {
		name  string
		bids  []strategy.Bid
		want  []strategy.ResolutionStepKind
		rolls bool
	}{
		{
			name: "the amount settled it",
			bids: []strategy.Bid{main(acct(0), 1_000, fixedNow), main(acct(1), 500, fixedNow)},
			want: []strategy.ResolutionStepKind{
				strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
				strategy.ResolutionStepAmount, strategy.ResolutionStepPrice,
			},
		},
		{
			name: "the bid sequence settled it",
			bids: []strategy.Bid{
				main(acct(0), 1_000, fixedNow.Add(1_000_000_000)), main(acct(1), 1_000, fixedNow),
			},
			want: []strategy.ResolutionStepKind{
				strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
				strategy.ResolutionStepAmount, strategy.ResolutionStepBidSequence,
				strategy.ResolutionStepPrice,
			},
		},
		{
			name: "a seeded roll settled it",
			bids: []strategy.Bid{main(acct(0), 1_000, fixedNow), main(acct(1), 1_000, fixedNow)},
			want: []strategy.ResolutionStepKind{
				strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
				strategy.ResolutionStepAmount, strategy.ResolutionStepSeededRoll,
				strategy.ResolutionStepPrice,
			},
			rolls: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// One alt below them, so the tier step has something to record and the eligibility count
			// is not the same number as the in-tier count.
			bids := append(tc.bids, strategy.Bid{
				AccountID: acct(2), AmountCp: 35_000, PlacedAt: fixedNow, Tier: strategy.TierAlt,
			})

			res, err := strategy.AuctionOpen{}.SettleAuction(spendCtx(t, tieredOpenConfig),
				strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}, bids)
			require.NoError(t, err)

			require.Equal(t, tc.want, traceKinds(res),
				"the trace records the steps that were REACHED, in the order the chain runs them")

			if tc.rolls {
				require.NotNil(t, res.RngSeed)
				require.Contains(t, res.Trace[3].Detail, "seed",
					"a roll nobody can replay is the one thing a dispute cannot be settled from")
			}

			require.Contains(t, res.Trace[0].Detail, "3 bids placed")
			require.Contains(t, res.Trace[1].Detail, "main")
			require.NotContains(t, res.Trace[1].Detail, "35000",
				"the tier step discloses how many bids sit below, never what they were")
		})
	}
}

// traceKinds is the trace's shape without its prose, so a test can assert which steps ran without
// pinning every sentence in them.
func traceKinds(res strategy.Resolution) []strategy.ResolutionStepKind {
	out := make([]strategy.ResolutionStepKind, 0, len(res.Trace))
	for _, s := range res.Trace {
		out = append(out, s.Kind)
	}

	return out
}

// TestAuctionOpen_SettleAuction_AnUntieredSession_SaysNothingAboutTiers is the other half of the
// ladder shipping: every session that exists today records no tier at all, because the field is
// filled in by the bid FSM in Phase 6.
//
// Such a session settles on the amount exactly as it did before #224 — every bid stands on `anyone`,
// so the rung decides nothing — and the resolution does not announce a rung nobody chose. The TRACE
// still records that the ladder ran and settled nothing, because "tier was not the reason" is a fact
// worth having written down rather than inferred from a silence.
func TestAuctionOpen_SettleAuction_AnUntieredSession_SaysNothingAboutTiers(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionOpen{}.SettleAuction(spendCtx(t, tieredOpenConfig), strategy.Session{
		ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow,
	}, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow},
		{AccountID: acct(1), AmountCp: 500, PlacedAt: fixedNow},
	})

	require.NoError(t, err)
	require.Equal(t, acct(0), res.Winners[0].AccountID)
	require.Equal(t, strategy.TierAnyone, res.WinningTier,
		"a bid recording no claim to standing stands on the bottom rung, which is what `anyone` means")
	require.Equal(t, []strategy.TierCount{{Tier: strategy.TierAnyone, Bids: 2}}, res.TierCounts)
	require.NotContains(t, res.Reason, "tier",
		"an officer running an untiered session must not be told which rung won it")
	require.Contains(t, res.Trace[1].Detail, "settled nothing")
}

// TestAuctionOpen_SettleAuction_ARot_CarriesItsOwnTrace: an item nobody legally bid on has no winner
// and no rung, and the one question an officer has about it — how many bids there were and what they
// missed — is answered by the trace rather than by nothing.
func TestAuctionOpen_SettleAuction_ARot_CarriesItsOwnTrace(t *testing.T) {
	t.Parallel()

	res, err := strategy.AuctionOpen{}.SettleAuction(spendCtx(t, tieredOpenConfig), strategy.Session{
		ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow,
	}, []strategy.Bid{{
		AccountID: acct(0), AmountCp: 100, PlacedAt: fixedNow,
		Tier: strategy.TierMain,
	}})

	require.NoError(t, err)
	require.Empty(t, res.Winners)
	require.Empty(t, res.WinningTier, "no bid was eligible, so no rung took anything")
	require.Empty(t, res.TierCounts)
	require.Equal(t, []strategy.ResolutionStepKind{strategy.ResolutionStepEligibility},
		traceKinds(res))
}

// TestAuctionOpen_SettleAuction_BidsBelowTheMinimum_AreIgnored, and an auction whose every bid is
// below it rots rather than failing: an item nobody legally bid on is a rot policy's problem, not a
// broken settlement.
func TestAuctionOpen_SettleAuction_BidsBelowTheMinimum_AreIgnored(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionOpen{}
	session := strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}

	res, err := s.SettleAuction(spendCtx(t, auctionOpenGoldenConfig), session, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 100, PlacedAt: fixedNow},
		{AccountID: acct(1), AmountCp: 750, PlacedAt: fixedNow},
	})
	require.NoError(t, err)
	require.Equal(t, acct(1), res.Winners[0].AccountID,
		"the 100 is below the pool's 500 minimum and cannot win")

	res, err = s.SettleAuction(spendCtx(t, auctionOpenGoldenConfig), session, []strategy.Bid{
		{AccountID: acct(0), AmountCp: 100, PlacedAt: fixedNow},
	})
	require.NoError(t, err)
	require.Empty(t, res.Winners)
	require.Contains(t, res.Reason, "minimum")
}

// TestAuctionOpen_SettleAuction_ASessionMayRaiseTheFloorAndMayNotLowerIt.
//
// The pool's minimum is a GUILD RULE and a session is one instance of it. Raising it is a session's
// business — an officer opening a session for a raid-wide bonus item may want a higher floor — and
// LOWERING it is not: that would let whoever opens the session waive the guild's floor for one drop,
// silently, without the settings changing, and the award would be consistent with itself at a price
// the guild had voted not to allow.
func TestAuctionOpen_SettleAuction_ASessionMayRaiseTheFloorAndMayNotLowerIt(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionOpen{}
	bid := []strategy.Bid{{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow}}

	raised, err := s.SettleAuction(spendCtx(t, auctionOpenGoldenConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, MinAmountCp: 5_000}, bid)
	require.NoError(t, err)
	require.Empty(t, raised.Winners, "1000 clears the pool's 500 and not the session's 5000")
	require.Contains(t, raised.Reason, "5000")

	lowered, err := s.SettleAuction(spendCtx(t, auctionOpenGoldenConfig),
		strategy.Session{ID: acct(60), SeqAtOpen: 5, MinAmountCp: 1},
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 250, PlacedAt: fixedNow}})
	require.NoError(t, err)
	require.Empty(t, lowered.Winners,
		"a session naming a minimum of 1 must not make a 250 bid eligible in a pool whose floor is 500")
	require.Contains(t, lowered.Reason, "500", "the refusal names the floor that actually applied")
}

// TestAuctionOpen_SettleAuction_AGenuineTie_IsBrokenBySeededRoll.
//
// Two bids equal in amount and placed in the same microsecond are genuinely equal, and the
// alternative to a roll is the account id — which is deterministic and is also a permanent bias: the
// raider whose ULID sorts first would win every coin flip this guild ever holds. The seed is reported
// so the flip is replayable, and the same seed picks the same winner.
func TestAuctionOpen_SettleAuction_AGenuineTie_IsBrokenBySeededRoll(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionOpen{}
	session := strategy.Session{ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow}
	bids := []strategy.Bid{
		{AccountID: acct(0), AmountCp: 1_000, PlacedAt: fixedNow},
		{AccountID: acct(1), AmountCp: 1_000, PlacedAt: fixedNow},
	}

	first, err := s.SettleAuction(spendCtx(t, auctionOpenGoldenConfig), session, bids)
	require.NoError(t, err)
	require.Len(t, first.Winners, 1)
	require.NotNil(t, first.RngSeed, "an unrecorded coin flip is the one thing a dispute cannot be "+
		"settled from")
	require.Contains(t, first.Reason, "seeded roll")

	// Reversed input, fresh façade: the same seed must reach the same conclusion, whatever order the
	// caller collected the bids in.
	second, err := s.SettleAuction(spendCtx(t, auctionOpenGoldenConfig), session,
		[]strategy.Bid{bids[1], bids[0]})
	require.NoError(t, err)
	require.Equal(t, first.Winners, second.Winners)
	require.Equal(t, *first.RngSeed, *second.RngSeed)
}

// TestAuctionOpen_ValidateBid_RejectsWhatASessionMustNotAccept.
func TestAuctionOpen_ValidateBid_RejectsWhatASessionMustNotAccept(t *testing.T) {
	t.Parallel()

	bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

	for _, tc := range []struct {
		name string
		bid  strategy.Bid
		want string
	}{
		{
			name: "sealed in an open auction",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 1_000, Sealed: true},
			want: "sealed",
		},
		{
			name: "below the minimum",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 250},
			want: "below the minimum",
		},
		{
			// The guide's rejected 160 against a minimum of 100 and an increment of 25, scaled: 760 is
			// 500 + 260, and 260 is not a whole number of 250s.
			name: "off the increment lattice",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 760},
			want: "whole number",
		},
		{
			name: "more than the bidder holds",
			bid:  strategy.Bid{AccountID: acct(0), AmountCp: 10_500},
			want: "spendable balance",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := strategy.AuctionOpen{}.ValidateBid(spendCtx(t, auctionOpenGoldenConfig), bidder, tc.bid)
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestAuctionOpen_PriceHint_PrefersTheCatalogueThenTheMinimum: a guild that publishes an expected
// value is telling its bidders what it thinks a drop is worth, and that is a better hint than the
// floor. With no catalogue price the hint is the smallest bid that would be accepted.
func TestAuctionOpen_PriceHint_PrefersTheCatalogueThenTheMinimum(t *testing.T) {
	t.Parallel()

	s := strategy.AuctionOpen{}
	ctx := spendCtx(t, auctionOpenGoldenConfig)

	hint, err := s.PriceHint(ctx, strategy.ItemRef{ID: acct(90), Name: "Unlisted"})
	require.NoError(t, err)
	require.NotNil(t, hint)
	require.Equal(t, core.Centipoints(500), *hint)

	catalogue := core.Centipoints(30_000)
	hint, err = s.PriceHint(ctx, strategy.ItemRef{
		ID: acct(90), Name: "Cloak of Flames", FixedPriceCp: &catalogue,
	})
	require.NoError(t, err)
	require.Equal(t, catalogue, *hint)
}

// TestAuctionOpen_Config_RefusesWhatWouldMakeTheAuctionUnrunnable.
func TestAuctionOpen_Config_RefusesWhatWouldMakeTheAuctionUnrunnable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"a minimum of nothing", `{"min_bid_cp":0}`, "min_bid_cp"},
		{"a negative minimum", `{"min_bid_cp":-100}`, "min_bid_cp"},
		{"an increment of nothing", `{"increment_cp":0}`, "increment_cp"},
		{"proceeds nobody ships", `{"proceeds":"the_officer"}`, "proceeds"},
		{"a solo policy nobody ships", `{"solo_policy":"residue"}`, "solo_policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.AuctionOpen{}.PlanAward(spendCtx(t, tc.config), spendAwardEvent(1_000))
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestAuctionOpen_DefaultConfig_IsRunnable pins what a pool that has set nothing does: a one-point
// minimum, a one-point increment, and the proceeds leaving circulation.
func TestAuctionOpen_DefaultConfig_IsRunnable(t *testing.T) {
	t.Parallel()

	p, err := strategy.AuctionOpen{}.PlanAward(spendCtx(t, ""), spendAwardEvent(1_000))
	require.NoError(t, err)
	require.Len(t, p.Entries, 2, "the shipped default is a sink: one debit, one credit to the bank")
	require.Zero(t, sumEntries(p))
}
