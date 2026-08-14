package strategy_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// loot_council, tested at the strategy level. Phase 1, #197.
//
// The arithmetic here is one subtraction, so the tests carry a different weight than `cap`'s or
// `fixed_price`'s: what has to be proved is that the planner writes down the number it was GIVEN and
// derives nothing. A council award that quietly clamped to a balance, or that fell back to the item's
// catalogue price, would look completely reasonable in review and would be a different DKP system.
//
// Three of those claims are asserted rather than argued — the planner reads no balance
// (TestLootCouncil_PlanAward_ReadsNoBalance), it ignores a catalogue price
// (TestLootCouncil_PlanAward_ChargeIsTheCouncilsOwnNumber) and it consumes no randomness
// (TestLootCouncil_Planners_ConsumeNoRandomness). The shared façade, the golden helpers and the
// property budget come from fixed_price_test.go and earn_test.go.

// lootCouncilGoldenDir is where loot_council's canonical proposals live.
const lootCouncilGoldenDir = "../../test/golden/strategy/loot_council"

// councilReason is the rationale every test that needs one passes. A council's reason is the audit
// trail, so the tests carry a realistic one rather than "x".
const councilReason = "council: main tank, no CoF yet, unanimous"

// --- The award: the one planner that writes a batch -----------------------------------------------

// TestLootCouncil_PlanAward_DebitsTheWinnerAndCreditsTheBank is the whole model in one test: the
// council decided, the winner pays what the council said, and the points land on the guild bank.
func TestLootCouncil_PlanAward_DebitsTheWinnerAndCreditsTheBank(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 10_000, `{"charge_cp": 2500}`)
	character, raid, itemAward := acct(50), acct(81), acct(82)

	p, err := strategy.LootCouncil{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
		CharacterID: &character,
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		RaidID:      &raid,
		ItemAwardID: &itemAward,
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.NoError(t, err)

	require.Equal(t, "award", p.Kind)
	require.Equal(t, "loot_council", p.StrategyID)
	require.Equal(t, councilReason, p.Reason,
		"the council's rationale travels onto the batch: it is the only record of why this happened")

	require.Len(t, p.Entries, 2, "one debit on the winner, one credit on the bank")
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-2500), p.Entries[0].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(2500), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	// Provenance reaches both entries; the character rides only on the winner's, because the bank
	// did not loot anything.
	for _, e := range p.Entries {
		require.NotNil(t, e.ItemID)
		require.Equal(t, acct(90), *e.ItemID)
		require.NotNil(t, e.ItemAwardID)
		require.NotNil(t, e.RaidID)
	}

	require.NotNil(t, p.Entries[0].CharacterID)
	require.Equal(t, character, *p.Entries[0].CharacterID)
	require.Nil(t, p.Entries[1].CharacterID)

	require.Equal(t,
		[]strategy.InvariantKind{strategy.InvariantSumZero, strategy.InvariantNonNegative},
		invariantKinds(p))
	require.NotContains(t, invariantKinds(p), strategy.InvariantLargestRemainderSumsToDebit,
		"there is no allocation to be exact about, so claiming the rule would be a story rather "+
			"than a constraint")
}

// TestLootCouncil_PlanAward_ChargeIsTheCouncilsOwnNumber pins the two-step resolution, including the
// step this strategy deliberately does NOT have.
//
// The catalogue row is the interesting case. `fixed_price` resolves officer → item → config; a
// council reads the item's published price and ignores it, because a council that used the price
// table would be `fixed_price` with extra steps. The price stays on the item for the pools whose
// spend rule is the one that reads it.
func TestLootCouncil_PlanAward_ChargeIsTheCouncilsOwnNumber(t *testing.T) {
	t.Parallel()

	catalogue := core.Centipoints(9_900)
	council := core.Centipoints(300)

	for _, tc := range []struct {
		name string
		item strategy.ItemRef
		ev   *core.Centipoints
		want core.Centipoints
	}{
		{
			name: "the pool's default charge",
			item: strategy.ItemRef{Name: "Plain"},
			want: 100,
		},
		{
			name: "the council's own amount beats the default",
			item: strategy.ItemRef{Name: "Plain"},
			ev:   &council,
			want: 300,
		},
		{
			name: "an item's catalogue price is ignored",
			item: strategy.ItemRef{Name: "Priced", FixedPriceCp: &catalogue},
			want: 100,
		},
		{
			name: "and is still ignored when the council names an amount",
			item: strategy.ItemRef{Name: "Priced", FixedPriceCp: &catalogue},
			ev:   &council,
			want: 300,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := strategy.LootCouncil{}.PlanAward(
				newCtx(t, 1, 10_000, `{"charge_cp": 100}`), strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:        tc.item,
					PriceCp:     tc.ev,
					EffectiveAt: fixedNow,
					Reason:      councilReason,
				})
			require.NoError(t, err)
			require.Equal(t, -tc.want, p.Entries[0].AmountCp)
			require.Equal(t, tc.want, p.Entries[1].AmountCp)
		})
	}
}

// TestLootCouncil_PlanAward_NoCharge_PlansNothing covers the council that charges nothing, which is
// the common P99 configuration and the shipped default.
//
// It is ErrNothingToPlan rather than ErrInvalidConfig: the decision was legal and it moved no points,
// so the caller records the item award and writes no batch. An entry of 0 is illegal
// (CHECK (amount_cp <> 0)) and BatchNonEmpty refuses an empty batch, so there is nothing else this
// could honestly return.
func TestLootCouncil_PlanAward_NoCharge_PlansNothing(t *testing.T) {
	t.Parallel()

	zero := core.Centipoints(0)

	for _, tc := range []struct {
		name   string
		config string
		price  *core.Centipoints
	}{
		{"the pool charges nothing", `{}`, nil},
		{"the council waives a charging pool's default", `{"charge_cp": 2500}`, &zero},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.LootCouncil{}.PlanAward(
				newCtx(t, 1, 10_000, tc.config), strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:        strategy.ItemRef{Name: "Cloak of Flames"},
					PriceCp:     tc.price,
					EffectiveAt: fixedNow,
					Reason:      councilReason,
				})
			require.ErrorIs(t, err, strategy.ErrNothingToPlan)
			require.ErrorIs(t, err, strategy.ErrInvalidEvent,
				"ErrNothingToPlan wraps ErrInvalidEvent, so a caller that only knows the broader "+
					"sentinel still declines to write a batch")
			require.ErrorContains(t, err, "loot_council")
		})
	}
}

// TestLootCouncil_PlanAward_ReadsNoBalance is the issue's sentence made executable: the charge is
// RECORDED, not computed from a balance.
//
// The façade records the seq of every balance read, and a council award must make none — not to
// clamp, not to check affordability, not to derive a percentage. Whether the winner can afford it is
// the ledger's question, answered at commit time by the NonNegative invariant this proposal declares,
// against a balance that is a fact rather than a plan input.
func TestLootCouncil_PlanAward_ReadsNoBalance(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 10, `{"charge_cp": 500000}`)

	p, err := strategy.LootCouncil{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.NoError(t, err)

	require.Empty(t, ctx.readAtSeq,
		"the planner read a balance; a council's charge is the number the council named, and a "+
			"planner that consulted a balance would be deriving it")
	require.Equal(t, core.Centipoints(-500_000), p.Entries[0].AmountCp,
		"the debit is the whole charge even though it takes the winner far below zero — the "+
			"declared NonNegative floor is what refuses that, at commit time")
	require.Equal(t, core.Centipoints(0), requireNonNegativeFloor(t, p))
}

// TestLootCouncil_PlanAward_TheFloorReachesTheProposal: the catalogue's floor is the shipped default
// and the PROPOSAL must carry the pool's own, or a guild that permits debt to a limit would have its
// setting silently ignored.
func TestLootCouncil_PlanAward_TheFloorReachesTheProposal(t *testing.T) {
	t.Parallel()

	p, err := strategy.LootCouncil{}.PlanAward(
		newCtx(t, 1, 0, `{"charge_cp": 250, "floor_cp": -1000}`), strategy.AwardEvent{
			Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:        strategy.ItemRef{Name: "Cloak of Flames"},
			EffectiveAt: fixedNow,
			Reason:      councilReason,
		})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-1000), requireNonNegativeFloor(t, p))
}

// TestLootCouncil_PlanAward_IgnoresBeneficiaries: a council has no split, and a Phase 3 loot flow
// that fills the event generically for every strategy must not have its award refused by the one
// strategy with no use for the field.
func TestLootCouncil_PlanAward_IgnoresBeneficiaries(t *testing.T) {
	t.Parallel()

	p, err := strategy.LootCouncil{}.PlanAward(
		newCtx(t, 4, 10_000, `{"charge_cp": 1000}`), strategy.AwardEvent{
			Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:          strategy.ItemRef{Name: "Cloak of Flames"},
			Beneficiaries: shares(4),
			EffectiveAt:   fixedNow,
			Reason:        councilReason,
		})
	require.NoError(t, err)

	require.Len(t, p.Entries, 2,
		"the charge is a sink: redistribution is fixed_price's `proceeds` knob, and a second copy "+
			"of that rule here could disagree with the first")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
}

// TestLootCouncil_PlanAward_RequiresARationale covers the knob that is this strategy's own: a council
// decision with no reason has no audit trail at all, because there is no arithmetic to inspect
// afterwards.
func TestLootCouncil_PlanAward_RequiresARationale(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		config  string
		reason  string
		wantErr bool
	}{
		{"an empty reason is refused by default", `{"charge_cp": 250}`, "", true},
		{"and so is whitespace", `{"charge_cp": 250}`, "   \t\n", true},
		{"a reason satisfies it", `{"charge_cp": 250}`, councilReason, false},
		{
			name:   "a guild that records its council elsewhere turns it off",
			config: `{"charge_cp": 250, "require_reason": false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.LootCouncil{}.PlanAward(
				newCtx(t, 1, 10_000, tc.config), strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:        strategy.ItemRef{Name: "Cloak of Flames"},
					EffectiveAt: fixedNow,
					Reason:      tc.reason,
				})

			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, "require_reason", "the refusal names the knob to turn off")
		})
	}
}

// TestLootCouncil_PlanAward_RejectsUnplannableDecisions is the rejection table: every shape of
// decision the planner has no defensible answer for, and the message each one produces.
func TestLootCouncil_PlanAward_RejectsUnplannableDecisions(t *testing.T) {
	t.Parallel()

	negative := core.Centipoints(-500)

	for _, tc := range []struct {
		name   string
		config string
		ev     strategy.AwardEvent
		want   error
		says   string
	}{
		{
			name:   "no winner",
			config: `{"charge_cp": 250}`,
			ev:     strategy.AwardEvent{Item: strategy.ItemRef{Name: "Cloak of Flames"}},
			want:   strategy.ErrInvalidEvent,
			says:   "no winner",
		},
		{
			name:   "the guild bank cannot win a council vote",
			config: `{"charge_cp": 250}`,
			ev: strategy.AwardEvent{
				Buyer: strategy.AccountRef{ID: ledger.AccountIDGuildBank, Kind: "system"},
				Item:  strategy.ItemRef{Name: "Cloak of Flames"},
			},
			want: strategy.ErrInvalidEvent,
			says: "system account",
		},
		{
			name:   "a charge that pays the winner",
			config: `{"charge_cp": 250}`,
			ev: strategy.AwardEvent{
				Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:    strategy.ItemRef{Name: "Cloak of Flames"},
				PriceCp: &negative,
			},
			want: strategy.ErrInvalidEvent,
			says: "adjustment, not an award",
		},
		{
			name:   "a pool whose default charge pays the winner",
			config: `{"charge_cp": -1}`,
			ev: strategy.AwardEvent{
				Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:  strategy.ItemRef{Name: "Cloak of Flames"},
			},
			want: strategy.ErrInvalidConfig,
			says: "charge_cp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := tc.ev
			ev.EffectiveAt = fixedNow
			ev.Reason = councilReason

			_, err := strategy.LootCouncil{}.PlanAward(newCtx(t, 1, 10_000, tc.config), ev)
			require.ErrorIs(t, err, tc.want)
			require.ErrorContains(t, err, tc.says)
			require.ErrorContains(t, err, "loot_council",
				"a planner's refusal names the strategy: the officer reading it has three rules "+
					"configured and needs to know which one said no")
		})
	}
}

// TestLootCouncil_PlanAward_PropagatesAFacadeFailure: the guild bank is the counterparty on every
// batch this strategy writes, so a façade that cannot resolve it must stop the plan rather than
// produce a one-sided batch.
func TestLootCouncil_PlanAward_PropagatesAFacadeFailure(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 10_000, `{"charge_cp": 250}`)
	ctx.systemErr = errors.New("boom")

	_, err := strategy.LootCouncil{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.ErrorContains(t, err, "boom")
	require.ErrorContains(t, err, "guild bank")
}

// TestLootCouncil_PlanAward_UsesTheClockWhenTheEventNamesNoTime: a zero EffectiveAt is a caller that
// did not specify, not a caller that meant 1970 — a batch stamped 1970 sorts before every real one in
// the statement view and lands in the wrong effective_day bucket forever.
func TestLootCouncil_PlanAward_UsesTheClockWhenTheEventNamesNoTime(t *testing.T) {
	t.Parallel()

	p, err := strategy.LootCouncil{}.PlanAward(
		newCtx(t, 1, 10_000, `{"charge_cp": 250}`), strategy.AwardEvent{
			Buyer:  strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:   strategy.ItemRef{Name: "Cloak of Flames"},
			Reason: councilReason,
		})
	require.NoError(t, err)
	require.Equal(t, fixedNow, p.EffectiveAt, "the INJECTED clock, never time.Now")
}

// --- The reversal --------------------------------------------------------------------------------

// TestLootCouncil_PlanReversal_NegatesAndRestampsTheEffectiveTime covers both halves of the default
// reversal: the entries are the exact negation, and the reversal is a NEW economic event at the time
// it is decided rather than at the original's.
func TestLootCouncil_PlanReversal_NegatesAndRestampsTheEffectiveTime(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 10_000, `{"charge_cp": 2500}`)
	yesterday := fixedNow.Add(-24 * 60 * 60 * 1_000_000_000)

	original, err := strategy.LootCouncil{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		EffectiveAt: yesterday,
		Reason:      councilReason,
	})
	require.NoError(t, err)

	p, err := strategy.LootCouncil{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:                 acct(70),
		Kind:               original.Kind,
		StrategyID:         original.StrategyID,
		StrategyVersion:    original.StrategyVersion,
		ConfigSnapshotJSON: original.ConfigSnapshotJSON,
		Reason:             original.Reason,
		EffectiveAt:        original.EffectiveAt,
		Entries:            original.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, p.Kind)
	require.NotNil(t, p.ReversesBatchID)
	require.Equal(t, acct(70), *p.ReversesBatchID)
	require.Equal(t, fixedNow, p.EffectiveAt,
		"a correction is a new economic event at the time it is decided; backdating it would "+
			"silently rewrite what every intermediate balance meant")

	require.Len(t, p.Entries, len(original.Entries))

	for i, e := range p.Entries {
		require.Equal(t, -original.Entries[i].AmountCp, e.AmountCp)
		require.Equal(t, original.Entries[i].AccountID, e.AccountID)
		require.Equal(t, original.Entries[i].ItemID, e.ItemID,
			"provenance is carried through: a reversal of an award for an item is still about "+
				"that item, and dropping the link makes it unattributable in the statement view")
	}
}

// TestLootCouncil_PlanReversal_DeclaresNoFloor_SoACorrectionIsAlwaysPostable is the rule whose
// failure mode is unfixable, asserted rather than trusted.
//
// A floor on a reversal does not prevent a debt, it prevents the CORRECTION — and the ledger is
// append-only, so a refused reversal leaves a mistake that is provably wrong and permanently
// unrepairable. The conservation rule survives, because a reversal can no more mint a centipoint than
// the original could.
func TestLootCouncil_PlanReversal_DeclaresNoFloor_SoACorrectionIsAlwaysPostable(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 10_000, `{"charge_cp": 2500, "floor_cp": 0}`)

	original, err := strategy.LootCouncil{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.NoError(t, err)
	require.Contains(t, invariantKinds(original), strategy.InvariantNonNegative,
		"the SPEND is where the floor belongs, and it must be there for this test to mean anything")

	p, err := strategy.LootCouncil{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            original.Kind,
		StrategyID:      original.StrategyID,
		StrategyVersion: original.StrategyVersion,
		Entries:         original.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(p),
		"a reversal keeps the conservation rule and drops the floor")
}

// TestLootCouncil_PlanReversal_ForeignBatch_IsRefused: a reversal must be planned by the strategy
// that planned the original, and ledger_batch.strategy_id is what routes it there.
func TestLootCouncil_PlanReversal_ForeignBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.LootCouncil{}.PlanReversal(newCtx(t, 1, 0, ""), strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "fixed_price",
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
		},
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "fixed_price")
	require.ErrorContains(t, err, "loot_council")
}

// TestLootCouncil_PlanReversal_EmptyBatch_IsRefused: a batch with no entries should never have been
// committed (CHECK (entry_count > 0)), so there is nothing to negate.
func TestLootCouncil_PlanReversal_EmptyBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.LootCouncil{}.PlanReversal(newCtx(t, 1, 0, ""), strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "loot_council",
	})
	require.ErrorIs(t, err, strategy.ErrEmptyProposal)
}

// --- What it refuses, and the two read-side questions it answers ---------------------------------

// TestLootCouncil_UnsupportedOperations_NameTheStrategy covers every method that returns
// ErrUnsupported, and asserts each says which strategy refused and why.
//
// Two of these are planners OUTSIDE this strategy's slot (ADR-0026 routes attendance to the earn rule
// and cadence runs to the over-time rule), and Priority is inside the slot and refused on purpose: the
// council IS the ranking, so a rank computed here would be a number the council did not use.
//
// PlanAdjustment is NOT here, and used to be: the adjustment is the one planner every strategy in this
// package implements identically, through one shared helper, and refusing it here made the tree carry
// two conventions for the same method. See LootCouncil.PlanAdjustment.
func TestLootCouncil_UnsupportedOperations_NameTheStrategy(t *testing.T) {
	t.Parallel()

	s := strategy.LootCouncil{}
	ctx := newCtx(t, 1, 0, "")

	for _, tc := range []struct {
		name string
		call func() error
		says string
	}{
		{"attendance", func() error {
			_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(1)})

			return err
		}, "tick"},
		{"decay", func() error {
			_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06"})

			return err
		}, "over-time rule"},
		{"priority", func() error {
			_, err := s.Priority(ctx, strategy.AccountRef{ID: acct(0)})

			return err
		}, "the council decides"},
		{"price hint", func() error {
			_, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})

			return err
		}, "names the charge"},
		{"validate bid", func() error {
			return s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)}, strategy.Bid{AmountCp: 10})
		}, "no bidding"},
		{"settle auction", func() error {
			_, err := s.SettleAuction(ctx, strategy.Session{}, nil)

			return err
		}, "no auctions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.call()
			require.ErrorIs(t, err, strategy.ErrUnsupported)
			require.ErrorContains(t, err, "loot_council",
				"ErrUnsupported crosses a package boundary on its way to a 501, and a refusal with "+
					"no subject is the support ticket nobody can act on")
			require.ErrorContains(t, err, tc.says)
		})
	}
}

// TestLootCouncil_Spendable_ReadsTheHeadSeq: a spendable balance is a SUM over committed entries as
// of the pool head — no computed decay, no weighting, and no clamping to what the council might
// charge.
func TestLootCouncil_Spendable_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 4_200, "")

	got, err := strategy.LootCouncil{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(4_200), got)
	require.Equal(t, []int64{ctx.headSeq}, ctx.readAtSeq,
		"balances are POSITIONAL: the head seq is the only meaningful 'now' for a spendable balance")

	ctx.balanceErr = errors.New("boom")
	_, err = strategy.LootCouncil{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.ErrorContains(t, err, "boom")
	require.ErrorContains(t, err, "loot_council")
}

// TestLootCouncil_Identity_IsStableAndDeclared pins the values that are public API the moment the
// first batch is written: the id lands on every ledger row, and the version is snapshotted beside it.
func TestLootCouncil_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.LootCouncil{}

	require.Equal(t, "loot_council", s.ID(),
		"the id is written onto every batch this strategy plans; renaming it orphans history")
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, strategy.RuleSpend, s.RuleKind(),
		"a council answers 'how are points spent?' — it is the pool's spend rule (ADR-0026)")
	require.Equal(t, []string{strategy.BalanceKindDKP}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants())

	registered, err := strategy.ByID("loot_council")
	require.NoError(t, err, "an unregistered strategy is one no pool can run")
	require.Equal(t, s.ID(), registered.ID())
}

// --- The config ----------------------------------------------------------------------------------

// TestLootCouncil_Config_AbsentIsTheDefaults pins all three defaults at once, through behaviour
// rather than through a struct nobody outside the package can see.
func TestLootCouncil_Config_AbsentIsTheDefaults(t *testing.T) {
	t.Parallel()

	charge := core.Centipoints(250)

	// charge_cp defaults to 0: a council that charges nothing is the shipped default and a real
	// configuration, so an award under it plans no batch at all.
	_, err := strategy.LootCouncil{}.PlanAward(newCtx(t, 1, 10_000, ""), strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan)

	// require_reason defaults to TRUE, so the same decision without a rationale is refused before the
	// charge is even resolved.
	_, err = strategy.LootCouncil{}.PlanAward(newCtx(t, 1, 10_000, "{}"), strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		PriceCp:     &charge,
		EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "require_reason")

	// floor_cp defaults to 0, and the proposal carries it.
	p, err := strategy.LootCouncil{}.PlanAward(newCtx(t, 1, 10_000, ""), strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{Name: "Cloak of Flames"},
		PriceCp:     &charge,
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(0), requireNonNegativeFloor(t, p))
}

// TestLootCouncil_Config_RejectsWhatTheSchemaWouldHaveRejected is the parser half of
// additionalProperties:false and of the declared types.
//
// Each row is a document the SCHEMA rejects, and the planner must reject it too: a config can also
// arrive from the importer, a backfill or a test, and a planner that defaulted a bad value would run
// a DKP system nobody chose.
func TestLootCouncil_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		says   string
	}{
		{"a typo'd knob", `{"charge_pc": 250}`, "charge_pc"},
		{"the JSON literal null", `null`, "null"},
		{"an array", `[{"charge_cp": 250}]`, "loot_council"},
		{"trailing content", `{"charge_cp": 250}{"charge_cp": 500}`, "loot_council"},
		{"a decimal charge", `{"charge_cp": 2.5}`, "loot_council"},
		{"a quoted charge", `{"charge_cp": "250"}`, "loot_council"},
		{"a null knob", `{"require_reason": null}`, "require_reason"},
		{"a negative default charge", `{"charge_cp": -1}`, "charge_cp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.LootCouncil{}.PlanAward(
				newCtx(t, 1, 10_000, tc.config), strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:        strategy.ItemRef{Name: "Cloak of Flames"},
					EffectiveAt: fixedNow,
					Reason:      councilReason,
				})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.says)
		})
	}

	// The adjustment planner reads the same config for its floor, so it owes the same strictness. A
	// planner that defaulted a bad document would declare a floor nobody configured.
	t.Run("the adjustment planner re-validates too", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.LootCouncil{}.PlanAdjustment(
			newCtx(t, 1, 10_000, `{"floor_pc": -500}`), strategy.AdjustmentEvent{
				Account: strategy.AccountRef{ID: acct(0), Kind: "person"}, AmountCp: -750,
			})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "floor_pc")
	})
}

// TestLootCouncil_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases FROM THE SCHEMA, so a
// knob added later is covered without anybody remembering to add a row.
func TestLootCouncil_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	schema := strategy.LootCouncil{}.ConfigSchema()

	requireNoNumberType(t, schema)

	// require_reason is a boolean, which the derived single-knob document cannot express, and every
	// knob's document must still plan an award — so the charge arrives on the EVENT rather than from
	// whichever knob is under test.
	legal := map[string]string{"require_reason": `{"require_reason": false}`}

	requireSchemaAgreesWithParser(t, schema, legal, func(t *testing.T, config string) error {
		t.Helper()

		charge := core.Centipoints(250)

		_, err := strategy.LootCouncil{}.PlanAward(
			newCtx(t, 1, 10_000, config), strategy.AwardEvent{
				Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:        strategy.ItemRef{Name: "Cloak of Flames"},
				PriceCp:     &charge,
				EffectiveAt: fixedNow,
				Reason:      councilReason,
			})

		return err
	})
}

// --- Declaration, randomness and the goldens ------------------------------------------------------

// TestLootCouncil_Planners_ConsumeNoRandomness: this strategy has no tie to break and no roll to make,
// so it must never reach for the injected Rng — and it must not carry a seed it did not consume, since
// a seed asserts that replaying the batch from it reproduces the plan.
func TestLootCouncil_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	s := strategy.LootCouncil{}
	ctx := goldenCouncilCtx(t)

	award, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	})
	require.NoError(t, err)

	reversal, err := s.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            award.Kind,
		StrategyID:      award.StrategyID,
		StrategyVersion: award.StrategyVersion,
		EffectiveAt:     award.EffectiveAt,
		Entries:         award.Entries,
	})
	require.NoError(t, err)

	for _, p := range []strategy.BatchProposal{award, reversal} {
		require.Nil(t, p.RngSeed,
			"%s carries a seed it never consumed; a seed asserts that replaying from it reproduces "+
				"the plan, which would be true here only by irrelevance", p.Kind)
	}

	require.Zero(t, ctx.rng.calls,
		"loot_council must consume no randomness: a council decision is made by people and recorded, "+
			"so there is no tie for a coin flip to break")
}

// TestLootCouncil_EveryPlannerInvariant_IsDeclared keeps the strategy-level catalogue and the
// per-proposal sets in step, in both directions.
func TestLootCouncil_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.LootCouncil{},
		plannedProposals(t, lootCouncilGoldenCases()))
}

// lootCouncilGoldenConfig is the config every golden is planned under: every knob set to a
// non-default value, so that a knob that stopped being read shows up as a changed golden rather than
// as nothing.
const lootCouncilGoldenConfig = `{"charge_cp":2500,"require_reason":false,"floor_cp":-500}`

// lootCouncilGoldenCases is one case per planner that writes a batch. There are two, because the
// other five planners refuse by name — a golden for a refusal would be a file recording that a
// strategy still says no.
func lootCouncilGoldenCases() []goldenCase {
	s := strategy.LootCouncil{}
	character, raid, itemAward := acct(50), acct(81), acct(82)

	award := func(tb testing.TB, ctx strategy.Ctx, at core.Micros) strategy.BatchProposal {
		tb.Helper()

		p, err := s.PlanAward(ctx, strategy.AwardEvent{
			Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
			CharacterID: &character,
			Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
			RaidID:      &raid,
			ItemAwardID: &itemAward,
			EffectiveAt: at,
			Reason:      councilReason,
		})
		require.NoError(tb, err)

		return p
	}

	return []goldenCase{
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				return award(tb, goldenCouncilCtx(tb), fixedNow)
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(goldenCouncilCtx(tb), strategy.AdjustmentEvent{
					Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
					AmountCp:    -750,
					EffectiveAt: fixedNow,
					Reason:      "double-charged for the Cloak on 2024-05-30",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := goldenCouncilCtx(tb)
				original := award(tb, ctx, fixedNow.Add(-24*60*60*1_000_000_000))

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

// goldenCouncilCtx is the façade every golden is planned against.
func goldenCouncilCtx(tb testing.TB) *fakeCtx {
	tb.Helper()

	return newCtx(tb, 3, 12_345, lootCouncilGoldenConfig)
}

// TestLootCouncil_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestLootCouncil_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, lootCouncilGoldenDir, lootCouncilGoldenCases())
}

// TestLootCouncil_Goldens_CoverEveryPlanner is the anti-drift half: a planner added without a golden
// would leave the whole-proposal assertion covering less than the strategy does, silently.
func TestLootCouncil_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, lootCouncilGoldenDir, lootCouncilGoldenCases(),
		[]string{"adjustment", "award", "reversal"})
}

// --- The properties -------------------------------------------------------------------------------
//
// Named TestProperty_* because `make test-property` selects on that prefix. The generator is the
// seeded Rng the strategy itself would be handed — importing math/rand anywhere under
// internal/strategy trips repo gate PURE002, test files included — and the base seed is printed, so a
// counterexample replays with DKP_PROPERTY_SEED.

// councilCase is one generated council decision: what it charged, and the balances and floor it was
// decided against.
type councilCase struct {
	ChargeCp  core.Centipoints
	BalanceCp core.Centipoints
	FloorCp   core.Centipoints
	HeadSeq   int64

	// FromEvent decides which of the two resolution steps carried the charge: the council's own
	// amount on the event, or the pool's default. Both are drawn, because a planner that read only
	// one of them would pass a property that always used the other.
	FromEvent bool
}

// generateCouncilCase draws one case from a seeded Rng.
//
// The distribution is CHOSEN rather than uniform, and each branch is a case a uniform int64 draw
// would never produce: a charge of exactly one centipoint, a charge far beyond any balance (the case
// a planner that clamped would silently pass), a balance already in debt, and a charge large enough
// that a planner doing arithmetic on it would overflow.
func generateCouncilCase(rng strategy.Rng) councilCase {
	c := councilCase{HeadSeq: int64(rng.IntN(1_000) + 1)}

	switch rng.IntN(4) {
	case 0:
		c.ChargeCp = 1
	case 1:
		c.ChargeCp = core.Centipoints(rng.IntN(10_000) + 1)
	case 2:
		c.ChargeCp = core.Centipoints(rngInt64(rng, 1_000_000_000) + 1)
	default:
		c.ChargeCp = core.Centipoints(rngInt64(rng, 1<<60) + 1)
	}

	switch rng.IntN(4) {
	case 0:
		c.BalanceCp = 0
	case 1:
		c.BalanceCp = -core.Centipoints(rng.IntN(50_000))
	case 2:
		c.BalanceCp = c.ChargeCp // exactly affordable
	default:
		c.BalanceCp = core.Centipoints(rngInt64(rng, 1_000_000))
	}

	c.FloorCp = core.Centipoints(-int64(rng.IntN(3)) * 1_000)
	c.FromEvent = rng.IntN(2) == 0

	return c
}

// config renders the generated case as a pool config document. A case whose charge came from the
// council carries a pool default of 0 — the shipped one — so that the event is genuinely the only
// source of the number.
func (c councilCase) config() string {
	charge := c.ChargeCp
	if c.FromEvent {
		charge = 0
	}

	return fmt.Sprintf(`{"charge_cp": %d, "floor_cp": %d}`, charge, c.FloorCp)
}

// event renders the generated case as a council decision.
func (c councilCase) event() strategy.AwardEvent {
	ev := strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Generated"},
		EffectiveAt: fixedNow,
		Reason:      councilReason,
	}

	if c.FromEvent {
		charge := c.ChargeCp
		ev.PriceCp = &charge
	}

	return ev
}

// ctx builds the façade the generated case is planned against.
func (c councilCase) ctx(tb testing.TB) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, 1, c.BalanceCp, c.config())
	ctx.headSeq = c.HeadSeq

	return ctx
}

// forEachCouncilCase runs check over `propertyChecks` generated decisions, failing with the seed that
// reproduces the first counterexample.
func forEachCouncilCase(t *testing.T, check func(c councilCase) error) {
	t.Helper()

	base := propertySeed(t)
	checks := propertyChecks(t)

	t.Logf("%d cases from base seed %d", checks, base)

	for i := range checks {
		seed := base + int64(i)

		c := generateCouncilCase(ledger.NewRng(seed))
		if err := check(c); err != nil {
			t.Fatalf("counterexample at seed %d (charge %d, balance %d, floor %d): %v\n"+
				"replay with: DKP_PROPERTY_SEED=%d DKP_PROPERTY_CHECKS=1 go test ./internal/strategy",
				seed, c.ChargeCp, c.BalanceCp, c.FloorCp, err, seed)
		}
	}
}

// TestProperty_LootCouncil_ChargeIsRecordedNotComputed is this strategy's own property, and it is the
// one that would catch the plausible wrong change.
//
// For every generated decision the debit is EXACTLY what the council named — not clamped to the
// winner's balance, not reduced to the floor, not scaled by anything. A planner that "helpfully"
// limited the charge to what the account could afford would pass every zero-sum check ever written
// and would quietly make the council's decision advisory.
func TestProperty_LootCouncil_ChargeIsRecordedNotComputed(t *testing.T) {
	t.Parallel()

	overdrafts := 0

	forEachCouncilCase(t, func(c councilCase) error {
		ctx := c.ctx(t)

		p, err := strategy.LootCouncil{}.PlanAward(ctx, c.event())
		if err != nil {
			return err
		}

		if len(ctx.readAtSeq) != 0 {
			return fmt.Errorf("the planner read %d balance(s); the charge is recorded, not computed",
				len(ctx.readAtSeq))
		}

		if p.Entries[0].AmountCp != -c.ChargeCp {
			return fmt.Errorf("the winner is debited %d, want exactly the %d the council named",
				p.Entries[0].AmountCp, c.ChargeCp)
		}

		if p.Entries[1].AmountCp != c.ChargeCp || p.Entries[1].AccountID != ledger.AccountIDGuildBank {
			return fmt.Errorf("the counterparty is %s at %d, want the guild bank at %d",
				p.Entries[1].AccountID, p.Entries[1].AmountCp, c.ChargeCp)
		}

		if net, ok := p.NetAmountCp(); !ok || net != 0 {
			return fmt.Errorf("the batch nets to %d (ok=%v), want exactly 0", net, ok)
		}

		if floor := councilFloor(p); floor == nil || *floor != c.FloorCp {
			return fmt.Errorf("the proposal declares floor %v, want the pool's %d", floor, c.FloorCp)
		}

		if c.BalanceCp-c.ChargeCp < c.FloorCp {
			overdrafts++
		}

		return nil
	})

	require.Positive(t, overdrafts,
		"no generated decision would take its winner below the floor, so the property never "+
			"exercised the case a clamping planner would have silently passed")
}

// councilFloor returns the floor a proposal's NonNegative invariant declares, or nil if it declares
// none. It is the property-side twin of requireNonNegativeFloor, which fails the test rather than
// returning a value a property needs to report on.
func councilFloor(p strategy.BatchProposal) *core.Centipoints {
	for _, inv := range p.Invariants {
		if inv.Kind == strategy.InvariantNonNegative {
			return inv.FloorCp
		}
	}

	return nil
}

// TestProperty_P5_LootCouncilReversal_IsAnExactInverse is P5 at the strategy level: planning a council
// award and then its reversal leaves every account exactly where it started.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint leaves a permanent,
// unexplainable discrepancy in a member's statement, and nobody finds it because the award and its
// reversal both look right individually.
func TestProperty_P5_LootCouncilReversal_IsAnExactInverse(t *testing.T) {
	t.Parallel()

	forEachCouncilCase(t, func(c councilCase) error {
		ctx := c.ctx(t)

		original, err := strategy.LootCouncil{}.PlanAward(ctx, c.event())
		if err != nil {
			return err
		}

		balances := map[core.ULID]core.Centipoints{}
		for _, e := range original.Entries {
			balances[e.AccountID] += e.AmountCp
		}

		reversal, err := strategy.LootCouncil{}.PlanReversal(ctx, strategy.LedgerBatch{
			ID:              acct(70),
			Kind:            original.Kind,
			StrategyID:      original.StrategyID,
			StrategyVersion: original.StrategyVersion,
			EffectiveAt:     original.EffectiveAt,
			Entries:         original.Entries,
		})
		if err != nil {
			return err
		}

		if reversal.Kind != strategy.KindReversal || reversal.ReversesBatchID == nil {
			return fmt.Errorf("the reversal is kind %q with target %v; a reversal that points at "+
				"nothing is an ordinary batch wearing the word", reversal.Kind, reversal.ReversesBatchID)
		}

		for _, e := range reversal.Entries {
			balances[e.AccountID] += e.AmountCp
		}

		for id, v := range balances {
			if v != 0 {
				return fmt.Errorf("account %s is %d centipoints from where it started", id, v)
			}
		}

		return nil
	})
}

// TestProperty_P8_LootCouncilPlan_IsByteIdentical is P8 at the strategy level, with the extra claim
// this strategy owes.
//
// Two plans of the same decision must produce byte-identical canonical bytes — the ordinary
// determinism claim — AND a plan against a different pool state must produce the same bytes too. The
// second is what "recorded, not computed" means at the level of the whole proposal: nothing about the
// winner's balance or the pool's head seq may reach the batch, so a planner that let either leak in
// would produce a different hash for the same decision on a different night.
func TestProperty_P8_LootCouncilPlan_IsByteIdentical(t *testing.T) {
	t.Parallel()

	forEachCouncilCase(t, func(c councilCase) error {
		first, err := councilCanonical(t, c.ctx(t), c.event())
		if err != nil {
			return err
		}

		second, err := councilCanonical(t, c.ctx(t), c.event())
		if err != nil {
			return err
		}

		if string(first) != string(second) {
			return fmt.Errorf("two plans of the same decision differ:\n\t%s\n\t%s", first, second)
		}

		// The same decision, a richer pool: more accounts, different balances, a later head seq.
		other := newCtx(t, 12, c.BalanceCp+7_777, c.config())
		other.headSeq = c.HeadSeq + 41

		third, err := councilCanonical(t, other, c.event())
		if err != nil {
			return err
		}

		if string(first) != string(third) {
			return fmt.Errorf("the same decision planned differently against a different pool "+
				"state:\n\t%s\n\t%s", first, third)
		}

		return nil
	})
}

// councilCanonical plans one council award and returns its canonical bytes.
func councilCanonical(tb testing.TB, ctx strategy.Ctx, ev strategy.AwardEvent) ([]byte, error) {
	tb.Helper()

	p, err := strategy.LootCouncil{}.PlanAward(ctx, ev)
	if err != nil {
		return nil, err
	}

	return p.Canonical()
}
