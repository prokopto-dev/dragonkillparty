package strategy_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// roll's arithmetic. Phase 1, #195.
//
// The family contracts are spend_test.go's table. What is here is the round: the rolls themselves,
// the seed that makes them replayable, the tie that awards nobody, and the free roll that posts no
// batch at all.

// rollGoldenDir is where the canonical proposals live.
const rollGoldenDir = "../../test/golden/strategy/roll"

// rollGoldenConfig sets every knob to a non-default value, `win_cost_cp` included — the shipped
// default is a free roll, which posts no batch and would leave the award golden with nothing to pin.
const rollGoldenConfig = `{"roll_min":10,"roll_max":1000,"win_cost_cp":250,"proceeds":"attendees",` +
	`"solo_policy":"write_off","floor_cp":-500}`

// rollGoldenCases is one case per planner this strategy answers.
func rollGoldenCases() []goldenCase {
	s := strategy.Roll{}
	raid, itemAward := acct(81), acct(82)

	return []goldenCase{
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				character := acct(50)

				p, err := s.PlanAward(spendCtx(tb, rollGoldenConfig), strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					CharacterID:   &character,
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					Beneficiaries: shares(3),
					RaidID:        &raid,
					ItemAwardID:   &itemAward,
					EffectiveAt:   fixedNow,
					Reason:        "Nagafen, rolled 782 of 10–1000",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(spendCtx(tb, rollGoldenConfig), strategy.AdjustmentEvent{
					Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
					AmountCp:    -750,
					EffectiveAt: fixedNow,
					Reason:      "charged for a round that was re-rolled",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := spendCtx(tb, rollGoldenConfig)

				original, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					Beneficiaries: shares(3),
					EffectiveAt:   fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:        "Nagafen, rolled 782 of 10–1000",
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

// TestRoll_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal — the persisted seed
// included, which is the field this strategy would be worthless without.
func TestRoll_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, rollGoldenDir, rollGoldenCases())
}

// TestRoll_Goldens_CoverEveryPlanner is the anti-drift half.
func TestRoll_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, rollGoldenDir, rollGoldenCases(),
		[]string{"adjustment", "award", "reversal"})
}

// TestRoll_PlanAward_AFreeRoll_PostsNoBatch. With win_cost_cp at its default of 0 nothing moved: the
// item was awarded, and awarding it is a fact for the item-award record rather than for the ledger.
// ErrNothingToPlan is what a caller declines to write a batch on.
func TestRoll_PlanAward_AFreeRoll_PostsNoBatch(t *testing.T) {
	t.Parallel()

	_, err := strategy.Roll{}.PlanAward(spendCtx(t, ""), strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
	})

	require.ErrorIs(t, err, strategy.ErrNothingToPlan)
	require.ErrorIs(t, err, strategy.ErrInvalidEvent,
		"ErrNothingToPlan wraps ErrInvalidEvent, so a caller matching on the family still catches it")
}

// TestRoll_PlanAward_ChargesTheWinCostAndCarriesTheSeed.
//
// The cost is the pool's unless an officer names one, which is the same override shape every other
// price resolution in this package uses. The seed is carried because an award decided by a roll is
// only defensible if the roll can be replayed — see the planner's doc comment for exactly what that
// claims and what it does not.
func TestRoll_PlanAward_ChargesTheWinCostAndCarriesTheSeed(t *testing.T) {
	t.Parallel()

	ctx := spendCtx(t, rollGoldenConfig)

	p, err := strategy.Roll{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(-250), p.Entries[0].AmountCp)
	require.NotNil(t, p.RngSeed)
	require.Equal(t, ctx.rng.Seed(), *p.RngSeed)

	override := core.Centipoints(900)
	p, err = strategy.Roll{}.PlanAward(spendCtx(t, rollGoldenConfig), strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		PriceCp:     &override,
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-900), p.Entries[0].AmountCp)
}

// TestRoll_SettleAuction_HighestRollWinsAndIsReplayable.
//
// The claim the persisted seed makes is that two settlements of the same round agree, entrant for
// entrant — which is why the second settlement below reverses the input: who gets which draw must
// depend on the account order rather than on the order the caller happened to collect the entries in.
func TestRoll_SettleAuction_HighestRollWinsAndIsReplayable(t *testing.T) {
	t.Parallel()

	s := strategy.Roll{}
	entries := []strategy.Bid{{AccountID: acct(0)}, {AccountID: acct(1)}, {AccountID: acct(2)}}

	first, err := s.SettleAuction(spendCtx(t, rollGoldenConfig), strategy.Session{ID: acct(60)}, entries)
	require.NoError(t, err)
	require.Len(t, first.Winners, 1)
	require.Equal(t, core.Centipoints(250), first.Winners[0].AmountCp, "the winner owes the win cost")
	require.NotNil(t, first.RngSeed)

	roll := winningRoll(t, first.Reason)
	require.GreaterOrEqual(t, roll, int64(10))
	require.LessOrEqual(t, roll, int64(1_000))

	second, err := s.SettleAuction(spendCtx(t, rollGoldenConfig), strategy.Session{ID: acct(60)},
		[]strategy.Bid{entries[2], entries[0], entries[1]})
	require.NoError(t, err)
	require.Equal(t, first.Winners, second.Winners)
	require.Equal(t, first.Reason, second.Reason)
}

// TestRoll_SettleAuction_ATie_AwardsNobody.
//
// "Rolls are immutable; a re-roll on a tie is a new round, not an edit"
// (docs/guides/choosing-a-dkp-system.md). Breaking the tie with a second roll inside the same
// settlement would be an edit of an immutable round, and the losing raider would never see the roll
// that beat them.
//
// The fixture forces the tie by pigeonhole: twelve entrants over a two-value range. It is
// deterministic rather than probabilistic — the seed is fixed — and the range is narrow enough that
// it stays a tie whatever the sequence does.
// TestRoll_SettleAuction_ALowerRungCannotWinWhateverItRolled (#224).
//
// Everybody rolls — the draws are per entrant, in account order, which is what makes the round
// replayable — and the ladder decides who those rolls are compared between. One main against three
// alts therefore wins on any face at all, and the reason says so: "highest of 1 roll" with a 97
// sitting in `alt` would otherwise read as a misread die rather than as the guild's own rule.
func TestRoll_SettleAuction_ALowerRungCannotWinWhateverItRolled(t *testing.T) {
	t.Parallel()

	res, err := strategy.Roll{}.SettleAuction(spendCtx(t, rollGoldenConfig),
		strategy.Session{ID: acct(60)}, []strategy.Bid{
			{AccountID: acct(0), Tier: strategy.TierAlt},
			{AccountID: acct(1), Tier: strategy.TierMain},
			{AccountID: acct(2), Tier: strategy.TierAlt},
		})

	require.NoError(t, err)
	require.Equal(t, acct(1), res.Winners[0].AccountID,
		"the only main takes it whatever the three alts rolled")
	require.Equal(t, strategy.TierMain, res.WinningTier)
	require.Equal(t,
		[]strategy.TierCount{{Tier: strategy.TierMain, Bids: 1}, {Tier: strategy.TierAlt, Bids: 2}},
		res.TierCounts)
	require.Contains(t, res.Reason, "highest of 1 rolls",
		"the roll it was highest of is the winning rung's, not the session's")
	require.Contains(t, res.Reason, "2 entrant(s) on lower rungs could not win it")
}

// TestRoll_SettleAuction_ARejectedRound_SpendsNoRandomness is the defect an AO review of #224 found:
// a round that draws and THEN refuses the entry list spends randomness that a retry can never get
// back.
//
// The injected Rng is a sequence. `roll` draws one number per entrant, in account order, so a
// settlement that rejected a malformed entry after the loop would leave that sequence advanced by a
// round nobody ran — and the officer who fixes the entry and retries gets different numbers from the
// ones the same session would have produced had the bad entry never been there. Nothing explains the
// difference afterwards, because a rejected round persists no seed: it is exactly the unreproducible
// flip the whole seeded design exists to prevent, and it would be invisible to every test that gives
// each settlement a fresh façade.
//
// Two assertions, because either alone is weaker than it looks. THE COUNTER proves no draw was taken
// — a fix that rejected after drawing but re-seeded would pass the second. THE RETRY proves what the
// counter is a proxy for: the corrected round on the used façade settles identically to the same
// round on a clean one.
func TestRoll_SettleAuction_ARejectedRound_SpendsNoRandomness(t *testing.T) {
	t.Parallel()

	corrected := []strategy.Bid{
		{AccountID: acct(0), Tier: strategy.TierMain},
		{AccountID: acct(1), Tier: strategy.TierMain},
		{AccountID: acct(2), Tier: strategy.TierAlt},
	}

	malformed := append([]strategy.Bid{{AccountID: acct(3), Tier: "MAIN"}}, corrected...)

	used := spendCtx(t, rollGoldenConfig)

	_, err := strategy.Roll{}.SettleAuction(used, strategy.Session{ID: acct(60)}, malformed)
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "not on the ladder")
	require.Zero(t, used.rng.calls,
		"a round that refused its entry list must not have touched the generator, seed included")

	retried, err := strategy.Roll{}.SettleAuction(used, strategy.Session{ID: acct(60)}, corrected)
	require.NoError(t, err)

	clean, err := strategy.Roll{}.SettleAuction(spendCtx(t, rollGoldenConfig),
		strategy.Session{ID: acct(60)}, corrected)
	require.NoError(t, err)

	require.Equal(t, clean.Winners, retried.Winners,
		"the retry after a rejected round must settle exactly as a session that never saw one")
	require.Equal(t, clean.Reason, retried.Reason)
	require.Equal(t, *clean.RngSeed, *retried.RngSeed)
}

func TestRoll_SettleAuction_ATie_AwardsNobody(t *testing.T) {
	t.Parallel()

	entrants := make([]strategy.Bid, 12)
	for i := range entrants {
		entrants[i] = strategy.Bid{AccountID: acct(i)}
	}

	res, err := strategy.Roll{}.SettleAuction(
		newCtx(t, len(entrants), spendBalance, `{"roll_min":1,"roll_max":2}`),
		strategy.Session{ID: acct(60)}, entrants)

	require.NoError(t, err)
	require.Empty(t, res.Winners, "a tied round awards nobody and calls for a new one")
	require.Contains(t, res.Reason, "tied on")
	require.Contains(t, res.Reason, "new round")
	require.NotNil(t, res.RngSeed, "the tied round is still replayable")

	// The round that awarded nobody still says what it did, and stops where it stopped: no price step,
	// because nothing was priced. An officer asked to explain a drop that went nowhere has the same
	// question as one asked about a drop that went somewhere.
	require.Equal(t, []strategy.ResolutionStepKind{
		strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
		strategy.ResolutionStepSeededRoll,
	}, traceKinds(res))
	require.Contains(t, res.Trace[2].Detail, "awards nobody")
	require.Empty(t, res.WinningTier,
		"the chain evaluated the ladder and then awarded nothing, which is what makes the trace and "+
			"this field different questions")
}

// TestRoll_SettleAuction_TheTraceRecordsTheDieAndTheLadder (#224, AO review).
//
// `roll`'s step 2 is the die rather than an amount, so its trace says so — and the seed is in the
// sentence, because a roll an officer cannot re-run is the one thing a loot dispute cannot be settled
// from. The tier step is written whether or not the ladder decided anything.
func TestRoll_SettleAuction_TheTraceRecordsTheDieAndTheLadder(t *testing.T) {
	t.Parallel()

	res, err := strategy.Roll{}.SettleAuction(spendCtx(t, rollGoldenConfig),
		strategy.Session{ID: acct(60)}, []strategy.Bid{
			{AccountID: acct(0), Tier: strategy.TierAlt},
			{AccountID: acct(1), Tier: strategy.TierMain},
			{AccountID: acct(2), Tier: strategy.TierAlt},
		})
	require.NoError(t, err)

	require.Equal(t, []strategy.ResolutionStepKind{
		strategy.ResolutionStepEligibility, strategy.ResolutionStepTier,
		strategy.ResolutionStepSeededRoll, strategy.ResolutionStepPrice,
	}, traceKinds(res))

	require.Contains(t, res.Trace[0].Detail, "3 entrants")
	require.Contains(t, res.Trace[1].Detail, "entrant",
		"a roll takes entrants rather than bids, and the sentence an officer pastes into chat says so")
	require.Contains(t, res.Trace[2].Detail, "seed")
	require.Contains(t, res.Trace[2].Detail, "tier main")
	require.Contains(t, res.Trace[3].Detail, "250", "the win cost this pool configured")
}

// TestRoll_SettleAuction_ARepeatedEntrant_IsRefused: two entries for one account is two draws and
// twice the chance of winning, and it is never a raider expressing more interest — it is a list
// assembled twice.
func TestRoll_SettleAuction_ARepeatedEntrant_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.Roll{}.SettleAuction(spendCtx(t, rollGoldenConfig),
		strategy.Session{ID: acct(60)},
		[]strategy.Bid{{AccountID: acct(0)}, {AccountID: acct(1)}, {AccountID: acct(0)}})

	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "entered twice")
}

// TestRoll_SettleAuction_AnEntryWithNoAccount_IsRefused: a roll has to be attributable to somebody,
// and an unattributed entry would consume a draw that no raider could be awarded.
func TestRoll_SettleAuction_AnEntryWithNoAccount_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.Roll{}.SettleAuction(spendCtx(t, rollGoldenConfig),
		strategy.Session{ID: acct(60)}, []strategy.Bid{{AccountID: acct(0)}, {}})

	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "names no account")
}

// TestRoll_SettleAuction_TheAmountOnAnEntryCannotChangeTheOutcome.
//
// ValidateBid refuses an entry that names an amount, but a settlement handed one anyway must not let
// it decide anything: the roll is the only ordering. Two rounds identical but for the amounts on the
// entries therefore produce the same winner.
func TestRoll_SettleAuction_TheAmountOnAnEntryCannotChangeTheOutcome(t *testing.T) {
	t.Parallel()

	s := strategy.Roll{}
	session := strategy.Session{ID: acct(60)}

	plain, err := s.SettleAuction(spendCtx(t, rollGoldenConfig), session,
		[]strategy.Bid{{AccountID: acct(0)}, {AccountID: acct(1)}})
	require.NoError(t, err)

	bribed, err := s.SettleAuction(spendCtx(t, rollGoldenConfig), session,
		[]strategy.Bid{{AccountID: acct(0), AmountCp: 9_999}, {AccountID: acct(1)}})
	require.NoError(t, err)

	require.Equal(t, plain.Winners, bribed.Winners)
}

// TestRoll_SettleAuction_NobodyEntered is the rot case: no entrants, no winner, no error.
func TestRoll_SettleAuction_NobodyEntered(t *testing.T) {
	t.Parallel()

	res, err := strategy.Roll{}.SettleAuction(spendCtx(t, rollGoldenConfig),
		strategy.Session{ID: acct(60)}, nil)

	require.NoError(t, err)
	require.Empty(t, res.Winners)
	require.Equal(t, "nobody entered the roll", res.Reason)
	require.Nil(t, res.RngSeed, "a round that never happened consumed no randomness")
}

// TestRoll_ValidateBid_RejectsAnEntryThatNamesAnAmount. There is nothing to bid — the roll decides —
// so an amount is either a caller wiring a bidding UI to a roll pool or a raider trying to hand the
// platform a number it did not generate.
func TestRoll_ValidateBid_RejectsAnEntryThatNamesAnAmount(t *testing.T) {
	t.Parallel()

	bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

	for _, tc := range []struct {
		name string
		bid  strategy.Bid
		want string
	}{
		{"an amount", strategy.Bid{AccountID: acct(0), AmountCp: 100}, "not bid on"},
		{"a sealed entry", strategy.Bid{AccountID: acct(0), Sealed: true}, "nothing to keep sealed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := strategy.Roll{}.ValidateBid(spendCtx(t, rollGoldenConfig), bidder, tc.bid)
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestRoll_ValidateBid_ChecksTheWinCostIsAffordable: entering a roll you cannot pay for wastes the
// round for everyone else. A free roll reads no balance at all, which is the second half of this.
func TestRoll_ValidateBid_ChecksTheWinCostIsAffordable(t *testing.T) {
	t.Parallel()

	bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

	broke := newCtx(t, 1, 100, `{"win_cost_cp":5000}`)
	err := strategy.Roll{}.ValidateBid(broke, bidder, strategy.Bid{AccountID: acct(0)})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "spendable balance")

	free := newCtx(t, 1, 0, `{"win_cost_cp":0}`)
	require.NoError(t, strategy.Roll{}.ValidateBid(free, bidder, strategy.Bid{AccountID: acct(0)}))
	require.Empty(t, free.readAtSeq, "a free roll has no cost to check a balance against")
}

// TestRoll_PriceHint_IsWhatWinningCosts_IncludingNothing.
//
// "This item is free" and "this strategy cannot say what it costs" are different facts, which is why
// the interface returns a pointer. A free roll pool answering nil would have a UI drawing an empty
// space where "free" belongs.
func TestRoll_PriceHint_IsWhatWinningCosts_IncludingNothing(t *testing.T) {
	t.Parallel()

	item := strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"}

	hint, err := strategy.Roll{}.PriceHint(spendCtx(t, rollGoldenConfig), item)
	require.NoError(t, err)
	require.NotNil(t, hint)
	require.Equal(t, core.Centipoints(250), *hint)

	hint, err = strategy.Roll{}.PriceHint(spendCtx(t, ""), item)
	require.NoError(t, err)
	require.NotNil(t, hint, "free is a price, and nil would mean the strategy could not say")
	require.Zero(t, *hint)
}

// TestRoll_Priority_RanksEveryEntrantEqually: the whole reason a guild rolls for an item is that it
// has decided standing should not decide this one.
func TestRoll_Priority_RanksEveryEntrantEqually(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, rollGoldenConfig)
	ctx.balances[acct(0)] = 90_000

	rich, err := strategy.Roll{}.Priority(ctx, strategy.AccountRef{ID: acct(0), Kind: "person"})
	require.NoError(t, err)

	poor, err := strategy.Roll{}.Priority(ctx, strategy.AccountRef{ID: acct(1), Kind: "person"})
	require.NoError(t, err)

	require.Equal(t, rich.Rank, poor.Rank)
	require.Zero(t, rich.Rank)
	require.Empty(t, ctx.readAtSeq)
}

// TestRoll_Config_RefusesARangeThatIsNotARoll.
func TestRoll_Config_RefusesARangeThatIsNotARoll(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"a range of one", `{"roll_min":100,"roll_max":100}`, "every round ties"},
		{"an inverted range", `{"roll_min":100,"roll_max":50}`, "every round ties"},
		{"a negative floor", `{"roll_min":-1}`, "roll_min"},
		{"a face nobody could draw", `{"roll_max":1000001}`, "roll_max"},
		{"a win that pays the winner", `{"win_cost_cp":-100}`, "win_cost_cp"},
		{"proceeds nobody ships", `{"proceeds":"the_officer"}`, "proceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.Roll{}.SettleAuction(spendCtx(t, tc.config),
				strategy.Session{ID: acct(60)}, []strategy.Bid{{AccountID: acct(0)}})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// winningRoll pulls the roll out of a resolution's reason.
//
// The reason is where a roll is recorded — Resolution carries the winner, the reason and the seed,
// and the value belongs in the sentence an officer pastes into chat rather than in a field nothing
// else would read. Parsing it back is what lets a test assert the value is inside the configured
// range, which is the one property of a die that matters.
func winningRoll(tb testing.TB, reason string) int64 {
	tb.Helper()

	idx := strings.LastIndex(reason, ": ")
	require.NotEqual(tb, -1, idx, "a winning resolution states the roll it was won on: %q", reason)

	roll, err := strconv.ParseInt(reason[idx+2:], 10, 64)
	require.NoError(tb, err, "the roll must be the last thing in the reason: %q", reason)

	return roll
}
