package strategy_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// tick, tested at the strategy level. Phase 1, #193.
//
// The shape is fixed_price_test.go's, deliberately: one example per planner, a table of everything a
// planner refuses, the config strictness both directions, the whole-proposal goldens, and the
// properties in earn_property_test.go. What is different is what this strategy is FOR — the role
// multiplier and the weight-is-ticks arithmetic — so those get the arithmetic tests that fixed_price
// spends on price resolution.

// tickGoldenDir is where tick's canonical proposals live. Under test/golden/ rather than beside this
// file because that tree is CODEOWNERS-protected and is gated against shrinking.
const tickGoldenDir = "../../test/golden/strategy/tick"

// --- The planners, one example each -------------------------------------------------------------

// TestTick_PlanAttendance_CreditsWeightedTicksFromTheBank is the guide's worked example, in
// centipoints.
//
// A four-hour raid on twenty-minute ticks at 10.00 points a tick: Tankguy was there from form-up (12
// of 12), Healbot arrived after tick 3 (9 of 12), Druidgal left at tick 8 (8 of 12). The weights ARE
// the ticks, so one batch settles the night's attendance and the arithmetic is one multiplication per
// raider.
func TestTick_PlanAttendance_CreditsWeightedTicksFromTheBank(t *testing.T) {
	t.Parallel()

	tick, raid := acct(80), acct(81)

	p, err := strategy.Tick{}.PlanAttendance(
		newCtx(t, 3, 0, `{"tick_award_cp": 1000}`), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 12},
				{AccountID: acct(1), Weight: 9},
				{AccountID: acct(2), Weight: 8},
			},
			TickID:      &tick,
			RaidID:      &raid,
			EffectiveAt: fixedNow,
			Reason:      "Vox, 12 ticks",
		})
	require.NoError(t, err)

	require.Equal(t, "attendance", p.Kind)
	require.Equal(t, "tick", p.StrategyID)
	require.Len(t, p.Entries, 4, "the bank's debit plus three credits")

	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-29_000), p.Entries[0].AmountCp,
		"the bank funds the whole night: (12 + 9 + 8) x 1000")
	require.Equal(t, core.Centipoints(12_000), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(9_000), p.Entries[2].AmountCp)
	require.Equal(t, core.Centipoints(8_000), p.Entries[3].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	for _, e := range p.Entries {
		require.NotNil(t, e.TickID, "attribution reaches every entry, the bank's included")
		require.NotNil(t, e.RaidID)
	}

	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(p),
		"nobody's balance decreases and the bank is exempt from floors, so NonNegative would "+
			"constrain nothing and is deliberately not declared")
}

// TestTick_PlanAttendance_RoleMultiplier_ScalesTheShare is the knob this strategy exists for.
//
// The standby percentage is the most argued-about setting in P99 raiding. It is asserted here with a
// THIRD raider whose role is not in the config, because the interesting behaviour is not "standby
// earns half" — it is that everyone else keeps earning a full share while it does.
func TestTick_PlanAttendance_RoleMultiplier_ScalesTheShare(t *testing.T) {
	t.Parallel()

	const config = `{"tick_award_cp": 1000, "role_multipliers": [{"role": "standby", "multiplier_bp": 5000}]}`

	p, err := strategy.Tick{}.PlanAttendance(newCtx(t, 3, 0, config), strategy.AttendanceEvent{
		Attendees: []strategy.Share{
			{AccountID: acct(0), Weight: 12, Role: "present"},
			{AccountID: acct(1), Weight: 12, Role: "standby"},
			{AccountID: acct(2), Weight: 12},
		},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(12_000), p.Entries[1].AmountCp,
		"an unlisted role takes default_multiplier_bp, which is a full share")
	require.Equal(t, core.Centipoints(6_000), p.Entries[2].AmountCp, "standby earns half")
	require.Equal(t, core.Centipoints(12_000), p.Entries[3].AmountCp,
		"no role at all is the ordinary case and earns in full")
	require.Equal(t, core.Centipoints(-30_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestTick_PlanAttendance_RoleMultiplier_FloorsRatherThanRounds pins the rounding direction on the
// one arithmetic that can round at all.
//
// 75 centipoints at half a share is 37.5. Rounding to nearest would credit 38 — a centipoint the
// configured ratio did not ask for, on every entry, every raid, forever. It is the same argument
// decay makes for flooring, and it is asserted rather than assumed because the two rules are written
// in different files.
func TestTick_PlanAttendance_RoleMultiplier_FloorsRatherThanRounds(t *testing.T) {
	t.Parallel()

	const config = `{"tick_award_cp": 25, "role_multipliers": [{"role": "standby", "multiplier_bp": 5000}]}`

	p, err := strategy.Tick{}.PlanAttendance(newCtx(t, 1, 0, config), strategy.AttendanceEvent{
		Attendees:   []strategy.Share{{AccountID: acct(0), Weight: 3, Role: "standby"}},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(37), p.Entries[1].AmountCp,
		"75 centipoints at 5000 bp is 37.5, floored to 37")
	require.Equal(t, core.Centipoints(-37), p.Entries[0].AmountCp,
		"the bank debits exactly what was credited; a bank rounded separately would mint the "+
			"difference on every tick")
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestTick_PlanAttendance_EarnersOfNothingAreDropped covers the three ways a credit can come out zero.
//
// All three are legal inputs and none of them may become an entry: ledger_entry carries
// CHECK (amount_cp <> 0). The rule is applied to the PRODUCT rather than to each input, which is what
// keeps it one rule instead of three.
func TestTick_PlanAttendance_EarnersOfNothingAreDropped(t *testing.T) {
	t.Parallel()

	const config = `{"tick_award_cp": 100, "role_multipliers": [{"role": "bench", "multiplier_bp": 0}]}`

	p, err := strategy.Tick{}.PlanAttendance(newCtx(t, 4, 0, config), strategy.AttendanceEvent{
		Attendees: []strategy.Share{
			{AccountID: acct(0), Weight: 2},
			{AccountID: acct(1), Weight: 0},                // present, earned nothing
			{AccountID: acct(2), Weight: 4, Role: "bench"}, // a bench that does not pay
			{AccountID: acct(3), Weight: 1, Role: "tiny"},
		},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "the bank plus the two raiders who earned something")
	require.Equal(t, acct(0), p.Entries[1].AccountID)
	require.Equal(t, acct(3), p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(-300), p.Entries[0].AmountCp)

	for _, e := range p.Entries {
		require.NotZero(t, e.AmountCp, "ledger_entry carries CHECK (amount_cp <> 0)")
	}
}

// TestTick_PlanAttendance_AmountOverride_IsTheEventsValue covers the kill tick and the first-tick
// bonus, which are per-event values rather than config knobs.
//
// The design puts them on event_type.default_tick_value_cp and raid_tick_credit.value_cp
// (docs/design/01-domain-model.md §8.1), and the fan-out hands the planner the resulting number. A
// second copy in the strategy config would be two places for one value.
func TestTick_PlanAttendance_AmountOverride_IsTheEventsValue(t *testing.T) {
	t.Parallel()

	kill := core.Centipoints(2_500)

	p, err := strategy.Tick{}.PlanAttendance(
		newCtx(t, 2, 0, `{"tick_award_cp": 1000}`), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 2},
				{AccountID: acct(1), Weight: 1},
			},
			AmountCp:    &kill,
			EffectiveAt: fixedNow,
			Reason:      "Nagafen, 2 kills",
		})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(5_000), p.Entries[1].AmountCp, "two kills at 25.00 points")
	require.Equal(t, core.Centipoints(2_500), p.Entries[2].AmountCp)
	require.Equal(t, core.Centipoints(-7_500), p.Entries[0].AmountCp)
}

// TestTick_PlanAdjustment_MovesPointsAgainstACounterparty asserts an adjustment is two entries and
// never one, and that it carries the pool's floor.
func TestTick_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	p, err := strategy.Tick{}.PlanAdjustment(
		newCtx(t, 2, 1_000, `{"floor_cp": -500}`), strategy.AdjustmentEvent{
			Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
			AmountCp:    -250,
			EffectiveAt: fixedNow,
			Reason:      "double-credited tick on 2024-05-30",
		})
	require.NoError(t, err)

	require.Equal(t, "adjustment", p.Kind)
	require.Len(t, p.Entries, 2)
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-250), p.Entries[0].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID,
		"an adjustment with no named counterparty is funded by the guild bank, never minted")
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	floor := requireNonNegativeFloor(t, p)
	require.Equal(t, core.Centipoints(-500), floor,
		"the proposal carries the POOL's floor, not the strategy catalogue's default")
}

// TestTick_PlanReversal_NegatesRestampsAndDeclaresNoFloor is the reversal contract in one test.
//
// The floor assertion is the load-bearing half. A NonNegative floor on a reversal does not prevent a
// debt — it prevents the CORRECTION, and an append-only ledger has no other repair primitive. The
// scenario is an ordinary Tuesday: a tick credited to the wrong raider, spent, and then reversed.
func TestTick_PlanReversal_NegatesRestampsAndDeclaresNoFloor(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"tick_award_cp": 500, "floor_cp": 0}`)
	s := strategy.Tick{}

	erroneous, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   []strategy.Share{{AccountID: acct(0), Weight: 1}},
		EffectiveAt: fixedNow.Add(-72 * 60 * 60 * 1_000_000_000),
	})
	require.NoError(t, err)

	reversal, err := s.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            erroneous.Kind,
		StrategyID:      erroneous.StrategyID,
		StrategyVersion: erroneous.StrategyVersion,
		EffectiveAt:     erroneous.EffectiveAt,
		Entries:         erroneous.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.NotNil(t, reversal.ReversesBatchID)
	require.Equal(t, acct(70), *reversal.ReversesBatchID)
	require.Equal(t, fixedNow, reversal.EffectiveAt,
		"a reversal is a new economic event at the time it is decided; backdating it to the "+
			"original's effective time would rewrite what every intermediate balance meant")
	require.NotEqual(t, erroneous.EffectiveAt, reversal.EffectiveAt)

	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(reversal),
		"a reversal declares SumZero and NOTHING else: a floor here stops the correction, not the debt")

	for i, e := range reversal.Entries {
		require.Equal(t, -erroneous.Entries[i].AmountCp, e.AmountCp)
	}
}

// TestTick_PlanReversal_ForeignBatch_IsRefused: only the strategy that planned a batch knows whether
// negation is the right inverse for it.
func TestTick_PlanReversal_ForeignBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.Tick{}.PlanReversal(newCtx(t, 1, 0, ""), strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "suicide_kings",
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: "sk_position", AmountCp: 3},
		},
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "suicide_kings")
	require.ErrorContains(t, err, "tick")
}

// TestTick_PlanReversal_IgnoresTodaysPoolConfig: a batch must stay reversible whatever the pool looks
// like today. History is immutable and the only repair primitive there is must not be contingent on
// the present.
func TestTick_PlanReversal_IgnoresTodaysPoolConfig(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{"ep_per_tick": 100}`,
		`{"tick_award_cp": null}`,
		`{`,
		`null`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 2, 0, config)

			// The control: another planner refuses this config, so the reversal's success is a
			// property of the reversal and not of the config being fine after all.
			_, err := strategy.Tick{}.PlanAttendance(ctx, strategy.AttendanceEvent{
				Attendees: shares(2),
			})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)

			reversal, err := strategy.Tick{}.PlanReversal(ctx, strategy.LedgerBatch{
				ID:         acct(70),
				StrategyID: "tick",
				Entries: []strategy.EntryProposal{
					{AccountID: acct(0), BalanceKind: "dkp", AmountCp: 10},
					{AccountID: acct(1), BalanceKind: "dkp", AmountCp: -10},
				},
			})
			require.NoError(t, err)
			require.Equal(t, strategy.KindReversal, reversal.Kind)
			require.Empty(t, reversal.ConfigSnapshotJSON,
				"the reversal carries the ORIGINAL batch's snapshot and never today's config")
		})
	}
}

// --- Spendable, Priority and the refusals ---------------------------------------------------------

// TestTick_Spendable_ReadsTheHeadSeq asserts the balance is read positionally at the pool head and is
// not adjusted by anything computed.
func TestTick_Spendable_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 1_234, `{"tick_award_cp": 100}`)

	got, err := strategy.Tick{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(1_234), got)
	require.Equal(t, []int64{7}, ctx.readAtSeq)

	rank, err := strategy.Tick{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(1_234), rank.Rank)
	require.Equal(t, acct(0).String(), rank.Tiebreak)
	require.Equal(t, "spendable balance", rank.Reason)
}

// TestTick_UnsupportedOperations_RefuseAndNameTheStrategy covers the five methods this strategy
// declines.
//
// PlanAward and PlanDecay are the two that matter: `tick` answers "how are points earned?" and
// nothing else, and a refusal that names the strategy is what turns into a 501 an operator can act
// on. A zero value with a nil error would be indistinguishable from a real answer of zero.
func TestTick_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"tick_award_cp": 100}`)
	s := strategy.Tick{}

	award, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:  strategy.ItemRef{Name: "Cloak of Flames"},
	})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "fixed_price", "the refusal points at what to pair it with")
	require.Empty(t, award.Entries)

	decay, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "decay_percent")
	require.Empty(t, decay.Entries)

	hint, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Nil(t, hint)

	require.ErrorIs(t, s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)},
		strategy.Bid{AccountID: acct(0), AmountCp: 100}), strategy.ErrUnsupported)

	resolution, err := s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 7}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Empty(t, resolution.Winners)
	require.ErrorContains(t, err, "tick",
		"every refusal names the strategy that made it: the error crosses a package boundary on "+
			"its way to a 501, and \"strategy does not support this operation\" with no subject is "+
			"precisely the support ticket nobody can act on")
}

// TestTick_Identity_IsStableAndDeclared covers the three values written onto every batch.
func TestTick_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.Tick{}

	require.Equal(t, "tick", s.ID(),
		"the id is written onto every batch and is public API: renaming it orphans history")
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants(), "a strategy that declares no invariants is a red flag")

	// The schema is a copy: a caller that could mutate it could change what every pool validates
	// against.
	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0])
}

// --- Rejections ---------------------------------------------------------------------------------

// TestTick_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
func TestTick_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.Tick{}

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "attendance with no attendees",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an override that awards nothing",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				zero := core.Centipoints(0)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: shares(2), AmountCp: &zero,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "every attendee earns nothing",
			config: `{"default_multiplier_bp": 0}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(3)})

				return err
			},
			wantErr: strategy.ErrNothingToPlan,
		},
		{
			name:   "a negative weight",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: -3}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a share with no account",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{Weight: 1}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a repeated account",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(0), Weight: 1},
					},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a tick whose value times its weight overflows int64",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				huge := core.Centipoints(math.MaxInt64 / 2)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: 4}},
					AmountCp:  &huge,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a role multiplier that overflows int64",
			config: `{"role_multipliers": [{"role": "tank", "multiplier_bp": 100000}]}`,
			plan: func(ctx strategy.Ctx) error {
				huge := core.Centipoints(math.MaxInt64 / 2)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: 1, Role: "tank"}},
					AmountCp:  &huge,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "credits that sum past int64",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				huge := core.Centipoints(math.MaxInt64 / 2)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(1), Weight: 1},
						{AccountID: acct(2), Weight: 1},
					},
					AmountCp: &huge,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment with no account",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{AmountCp: 100})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment of zero",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(0)},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment against itself",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account:      strategy.AccountRef{ID: acct(0)},
					AmountCp:     100,
					Counterparty: acct(0),
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an adjustment with no representable negation",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account:      strategy.AccountRef{ID: acct(0)},
					AmountCp:     math.MinInt64,
					Counterparty: acct(1),
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a reversal of an empty batch",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{ID: acct(70), StrategyID: "tick"})

				return err
			},
			wantErr: strategy.ErrEmptyProposal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 1, tc.config)), tc.wantErr)
		})
	}
}

// TestTick_Config_RejectsWhatTheSchemaWouldHaveRejected asserts the planner re-validates rather than
// defaulting — including the NESTED cases, which the top-level strict decode does not reach.
//
// A role multiplier is an object inside an array, and every failure the top-level knobs have it has
// too: a null value that reads back as "earns nothing", an unknown key that leaves the multiplier at
// its zero value, a role named twice with two different answers.
func TestTick_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`"tick"`,
		`{"tick_award_cp": 1000}{"tick_award_cp": 2000}`,
		`{"tick_award_cp": 0}`,
		`{"tick_award_cp": -1}`,
		`{"tick_award_cp": 1.5}`,
		`{"tick_award_cp": "1000"}`,
		`{"tick_award_cp": null}`,
		`{"tick_avard_cp": 1000}`,
		`{"default_multiplier_bp": -1}`,
		`{"default_multiplier_bp": 100001}`,
		`{"role_multipliers": null}`,

		// The nested family. Each of these parses into a plausible config and means something the
		// officer did not type.
		`{"role_multipliers": [{"role": "standby", "multiplier_bp": null}]}`,
		`{"role_multipliers": [{"role": "standby"}]}`,
		`{"role_multipliers": [{"role": "", "multiplier_bp": 5000}]}`,
		`{"role_multipliers": [{"multiplier_bp": 5000}]}`,
		`{"role_multipliers": [{"role": "standby", "multiplier_bp": -1}]}`,
		`{"role_multipliers": [{"role": "standby", "multiplier_bp": 100001}]}`,
		`{"role_multipliers": [{"role": "standby", "mutliplier_bp": 5000}]}`,
		`{"role_multipliers": [{"role": "standby", "multiplier_bp": 5000}, ` +
			`{"role": "standby", "multiplier_bp": 2500}]}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyTickPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 1, 0, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// everyTickPlanner returns one minimal, otherwise-legal call per planner that reads the pool's config.
//
// PlanReversal is deliberately absent: it reads neither the current config nor any façade value it
// could fail on, which is a property rather than an oversight — see
// TestTick_PlanReversal_IgnoresTodaysPoolConfig.
func everyTickPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.Tick{}

	return map[string]func(ctx strategy.Ctx) error{
		"attendance": func(ctx strategy.Ctx) error {
			_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(2)})

			return err
		},
		"adjustment": func(ctx strategy.Ctx) error {
			_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
				Account: strategy.AccountRef{ID: acct(0)}, AmountCp: 10,
			})

			return err
		},
	}
}

// TestTick_Config_AbsentIsTheDefaults_AndTypoedIsNot is the other direction of the strict decoding,
// and it is what stops the strictness from being a regression: a pool that has set nothing must still
// plan.
func TestTick_Config_AbsentIsTheDefaults_AndTypoedIsNot(t *testing.T) {
	t.Parallel()

	for _, config := range []string{"", "{}", "  ", "\n{}\n"} {
		t.Run(fmt.Sprintf("%q", config), func(t *testing.T) {
			t.Parallel()

			p, err := strategy.Tick{}.PlanAttendance(
				newCtx(t, 1, 0, config), strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: 1, Role: "standby"}},
				})
			require.NoError(t, err)

			require.Equal(t, core.Centipoints(100), p.Entries[1].AmountCp,
				"an unset config runs the shipped default: 100 centipoints at a full share, for "+
					"every role, because a guild that has priced no role has priced none of them")
		})
	}

	t.Run("a transposed knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.Tick{}.PlanAttendance(
			newCtx(t, 1, 0, `{"tick_avard_cp": 1000}`),
			strategy.AttendanceEvent{Attendees: shares(1)})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "tick_avard_cp")
	})

	t.Run("a null knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.Tick{}.PlanAttendance(
			newCtx(t, 1, 0, `{"tick_award_cp": null, "floor_cp": null}`),
			strategy.AttendanceEvent{Attendees: shares(1)})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "floor_cp",
			"with several null knobs the first in sorted order is named, on every run")
	})

	t.Run("a duplicate role names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.Tick{}.PlanAttendance(
			newCtx(t, 1, 0, `{"role_multipliers": [{"role": "standby", "multiplier_bp": 5000}, `+
				`{"role": "standby", "multiplier_bp": 1}]}`),
			strategy.AttendanceEvent{Attendees: shares(1)})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "standby")
	})
}

// TestTick_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases FROM THE SCHEMA, so a knob
// added later is covered without anybody remembering to add a row. Both directions of schema/parser
// drift are asserted: every declared knob rejects null, and every declared knob accepts a legal value
// of its declared type.
func TestTick_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.Tick{}.ConfigSchema(),
		map[string]string{
			"role_multipliers": `{"role_multipliers":[{"role":"standby","multiplier_bp":5000}]}`,
			// A full share rather than the helper's generic 1. One basis point is a perfectly legal
			// multiplier and the parser accepts it — it just rounds the whole tick away to nothing,
			// so the PLANNER then refuses the event, and this test is about the config rather than
			// about the arithmetic.
			"default_multiplier_bp": `{"default_multiplier_bp":10000}`,
		},
		func(t *testing.T, config string) error {
			t.Helper()

			_, err := strategy.Tick{}.PlanAttendance(
				newCtx(t, 1, 0, config), strategy.AttendanceEvent{Attendees: shares(1)})

			return err
		})
}

// TestTick_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan rather than
// producing a batch built on a zero.
func TestTick_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.Tick{}
	boom := fmt.Errorf("the read pool is closed")

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyTickPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 1_000, `{"tick_award_cp": 100}`)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, `{"tick_award_cp": 100}`)
		ctx.balanceErr = boom

		_, err := s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})
}

// --- Declaration and the goldens ------------------------------------------------------------------

// tickGoldenConfig is the config every tick golden is planned under: every knob set to a non-default
// value, so that a knob that stopped being read shows up as a changed golden rather than as nothing.
const tickGoldenConfig = `{"tick_award_cp":150,"default_multiplier_bp":7500,` +
	`"role_multipliers":[{"role":"standby","multiplier_bp":5000},{"role":"present","multiplier_bp":10000}],` +
	`"floor_cp":-500}`

// tickGoldenCases is one case per planner tick supports.
func tickGoldenCases() []goldenCase {
	s := strategy.Tick{}
	tick, raid := acct(80), acct(81)

	return []goldenCase{
		{
			name: "attendance",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAttendance(newCtx(tb, 3, 0, tickGoldenConfig), strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 12, Role: "present"},
						{AccountID: acct(1), Weight: 9, Role: "standby"},
						{AccountID: acct(2), Weight: 7},
					},
					TickID:      &tick,
					RaidID:      &raid,
					EffectiveAt: fixedNow,
					Reason:      "Vox, 12 ticks",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(newCtx(tb, 3, 0, tickGoldenConfig), strategy.AdjustmentEvent{
					Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
					AmountCp:    -750,
					EffectiveAt: fixedNow,
					Reason:      "double-credited tick on 2024-05-30",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := newCtx(tb, 3, 0, tickGoldenConfig)

				original, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 3, Role: "standby"},
						{AccountID: acct(1), Weight: 3},
					},
					TickID:      &tick,
					EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:      "Vox, tick 3",
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

// TestTick_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestTick_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, tickGoldenDir, tickGoldenCases())
}

// TestTick_Goldens_CoverEveryPlanner is the anti-drift half: a planner added without a golden would
// leave the whole-proposal assertion covering fewer planners than the strategy has, silently.
func TestTick_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, tickGoldenDir, tickGoldenCases(),
		[]string{"adjustment", "attendance", "reversal"})
}

// TestTick_EveryPlannerInvariant_IsDeclared keeps the strategy-level catalogue and the per-proposal
// sets in step, in both directions.
func TestTick_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.Tick{}, plannedProposals(t, tickGoldenCases()))
}

// TestTick_Planners_ConsumeNoRandomness asserts the injected Rng is offered and refused.
//
// A planner that consumed randomness would need its seed persisted onto the batch for a replay to be
// byte-identical; this one consumes none, so its proposals carry no seed — and the way to state that
// as a fact rather than an assumption is to count the calls and require the seed to be absent.
func TestTick_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 10_000, tickGoldenConfig)
	s := strategy.Tick{}

	attendance, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   shares(3),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	adjustment, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
		Account: strategy.AccountRef{ID: acct(1)}, AmountCp: 42, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	for _, p := range []strategy.BatchProposal{attendance, adjustment} {
		require.Nil(t, p.RngSeed,
			"%s carries a seed it never consumed; a seed asserts that replaying from it reproduces "+
				"the plan, which would be true here only by irrelevance", p.Kind)
	}

	require.Zero(t, ctx.rng.calls,
		"tick must consume no randomness: its only ordering is the account id, which is deliberately "+
			"NOT random so that two replays agree")
}

// TestTick_ConfigSchema_DeclaresNoNumber restates canonical §1 where a schema could break it:
// `number` in a JSON Schema permits 12.5, and a decimal in the point path is a float.
func TestTick_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.Tick{}.ConfigSchema())
}
