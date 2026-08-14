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

// attendance_weighted, tested at the strategy level. Phase 1, #196.
//
// The arithmetic under test is not this strategy's — every credit comes from ledger.Allocate, whose
// own properties are proved in internal/ledger — so what these tests prove is the PLANNER: that it
// distributes the whole pot and no more, that the bank's debit is the pot rather than the sum of what
// happened to be credited, and that an event nobody attended writes no batch at all.
//
// The shared helpers (fakeCtx, acct, shares, requireGoldens, requireInvariantsAgree,
// requireSchemaAgreesWithParser, the property seed) live in fixed_price_test.go and earn_test.go.

// attendanceWeightedGoldenDir is where the canonical proposals live. Under test/golden/ rather than
// beside this file because that tree is CODEOWNERS-protected and is gated against shrinking.
const attendanceWeightedGoldenDir = "../../test/golden/strategy/attendance_weighted"

// --- The planners, one example each -------------------------------------------------------------

// TestAttendanceWeighted_PlanAttendance_DividesThePotByAttendance is the worked example from
// docs/concepts/strategies.md: a 1000.00 pot over a raid whose three attendees were present for 12, 9
// and 8 ticks.
func TestAttendanceWeighted_PlanAttendance_DividesThePotByAttendance(t *testing.T) {
	t.Parallel()

	p, err := strategy.AttendanceWeighted{}.PlanAttendance(
		newCtx(t, 3, 0, `{"raid_pot_cp": 100000}`), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 12},
				{AccountID: acct(1), Weight: 9},
				{AccountID: acct(2), Weight: 8},
			},
			EffectiveAt: fixedNow,
			Reason:      "Vox, 12 ticks",
		})
	require.NoError(t, err)

	require.Equal(t, "attendance", p.Kind)
	require.Equal(t, "attendance_weighted", p.StrategyID)
	require.Len(t, p.Entries, 4, "the bank's debit plus one credit per attendee")

	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[0].AccountID, "the bank's debit leads")
	require.Equal(t, core.Centipoints(-100_000), p.Entries[0].AmountCp,
		"the bank is debited the WHOLE pot, not the sum of what happened to be credited")

	// 1200000/29, 900000/29 and 800000/29 floor to 41379, 31034 and 27586, which is one centipoint
	// short; the largest remainder (acct 1, at 14/29) takes it.
	require.Equal(t, core.Centipoints(41_379), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(31_035), p.Entries[2].AmountCp)
	require.Equal(t, core.Centipoints(27_586), p.Entries[3].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p),
		"the credits sum to exactly the pot: rounding each share independently would mint or destroy "+
			"a centipoint on every raid, forever")

	require.Contains(t, invariantKinds(p), strategy.InvariantLargestRemainderSumsToDebit)
}

// TestAttendanceWeighted_PlanAttendance_ThePotIsFixedRegardlessOfTurnout is the economic argument for
// this rule over `tick`, asserted rather than described: forty raiders cost the guild's economy
// exactly what twenty do.
func TestAttendanceWeighted_PlanAttendance_ThePotIsFixedRegardlessOfTurnout(t *testing.T) {
	t.Parallel()

	for _, n := range []int{1, 2, 20, 40} {
		t.Run(fmt.Sprintf("%d raiders", n), func(t *testing.T) {
			t.Parallel()

			p, err := strategy.AttendanceWeighted{}.PlanAttendance(
				newCtx(t, n, 0, `{"raid_pot_cp": 100000}`), strategy.AttendanceEvent{
					Attendees: shares(n), EffectiveAt: fixedNow,
				})
			require.NoError(t, err)

			require.Equal(t, core.Centipoints(-100_000), p.Entries[0].AmountCp)
			require.Equal(t, core.Centipoints(0), sumEntries(p))

			var credits core.Centipoints
			for _, e := range p.Entries[1:] {
				credits += e.AmountCp
			}

			require.Equal(t, core.Centipoints(100_000), credits)
		})
	}
}

// TestAttendanceWeighted_PlanAttendance_AmountOverride_IsTheEventsPot: a first kill worth double, or a
// short Tuesday worth half, carries its own value rather than needing a second pool.
func TestAttendanceWeighted_PlanAttendance_AmountOverride_IsTheEventsPot(t *testing.T) {
	t.Parallel()

	pot := core.Centipoints(50_000)

	p, err := strategy.AttendanceWeighted{}.PlanAttendance(
		newCtx(t, 2, 0, `{"raid_pot_cp": 100000}`), strategy.AttendanceEvent{
			Attendees:   shares(2),
			AmountCp:    &pot,
			EffectiveAt: fixedNow,
		})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(-50_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(25_000), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(25_000), p.Entries[2].AmountCp)
}

// TestAttendanceWeighted_PlanAttendance_AShareThatRoundsAwayGetsNoEntry: ledger_entry carries
// CHECK (amount_cp <> 0), so an attendee whose slice of the pot rounds to nothing is dropped — and the
// centipoint they did not get went to a higher remainder, so the pot is still distributed exactly.
func TestAttendanceWeighted_PlanAttendance_AShareThatRoundsAwayGetsNoEntry(t *testing.T) {
	t.Parallel()

	p, err := strategy.AttendanceWeighted{}.PlanAttendance(
		newCtx(t, 2, 0, `{"raid_pot_cp": 1}`), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 1},
				{AccountID: acct(1), Weight: 1},
			},
			EffectiveAt: fixedNow,
		})
	require.NoError(t, err)

	require.Len(t, p.Entries, 2,
		"one centipoint cannot be halved: the debit plus the single credit that took it")
	require.Equal(t, core.Centipoints(-1), p.Entries[0].AmountCp)
	require.Equal(t, acct(0), p.Entries[1].AccountID,
		"at equal remainders the tiebreak is the account id, ascending — deterministic, so two "+
			"replays of the same raid agree")
	require.Equal(t, core.Centipoints(1), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestAttendanceWeighted_PlanAttendance_NobodyAttendedAnything_WritesNoBatch is the degenerate case
// this strategy answers differently from zero_sum, and the difference is the whole reason it has its
// own check rather than the allocator's.
//
// A zero-sum split divides a price the buyer has already paid, so the points are in flight and must
// land somewhere — `residue` rather than a silent drop. A pot does not exist until the batch creates
// it, so there is nothing in flight, and posting it anyway would take points out of the bank for a
// raid nobody attended.
func TestAttendanceWeighted_PlanAttendance_NobodyAttendedAnything_WritesNoBatch(t *testing.T) {
	t.Parallel()

	_, err := strategy.AttendanceWeighted{}.PlanAttendance(
		newCtx(t, 3, 0, `{}`), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 0},
				{AccountID: acct(1), Weight: 0},
			},
			EffectiveAt: fixedNow,
		})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan)
	require.NotErrorIs(t, err, ledger.ErrZeroTotal,
		"the refusal is the planner's, taken before the allocator is asked: the allocator would "+
			"route the pot to residue, which is right for a price somebody paid and wrong for a pot "+
			"nobody earned")
}

// TestAttendanceWeighted_PlanAttendance_ASystemAccountAttendee_IsRefused is the dilution defect, and
// it is the one a zero-sum check cannot see.
//
// The pot is FIXED. Putting the guild bank on the attendee list gives it a share of the very pot it
// funds, so every real raider is credited less — and the batch still sums to exactly zero, still
// allocates by largest remainder, and passes the invariant engine untouched. Nobody auditing a number
// that adds up would find it. Found in review of #228.
func TestAttendanceWeighted_PlanAttendance_ASystemAccountAttendee_IsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		id   core.ULID
		says string
	}{
		{"the guild bank, which funds the pot", ledger.AccountIDGuildBank, "guild_bank"},
		{"residue", ledger.AccountIDResidue, "residue"},
		{"write_off", ledger.AccountIDWriteOff, "write_off"},
		{"import_opening", ledger.AccountIDImportOpening, "import_opening"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.AttendanceWeighted{}.PlanAttendance(
				newCtx(t, 3, 0, `{"raid_pot_cp": 100000}`), strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 12},
						{AccountID: acct(1), Weight: 9},
						{AccountID: tc.id, Weight: 8},
					},
					EffectiveAt: fixedNow,
				})
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, tc.says, "the refusal names which system account it was")
		})
	}

	// The half that makes the test worth having: without the check the batch is perfectly well-formed,
	// so a test that only asserted "an error happened" would not show what was at stake.
	t.Run("the two real raiders would otherwise be short", func(t *testing.T) {
		t.Parallel()

		clean, err := strategy.AttendanceWeighted{}.PlanAttendance(
			newCtx(t, 2, 0, `{"raid_pot_cp": 100000}`), strategy.AttendanceEvent{
				Attendees: []strategy.Share{
					{AccountID: acct(0), Weight: 12},
					{AccountID: acct(1), Weight: 9},
				},
				EffectiveAt: fixedNow,
			})
		require.NoError(t, err)

		// 12/21 and 9/21 of the pot, which is what those two are owed for that raid. With the bank
		// added at weight 8 the same two would have taken 12/29 and 9/29 — 41379 and 31035 — and the
		// batch would still have summed to zero.
		require.Equal(t, core.Centipoints(57_143), clean.Entries[1].AmountCp)
		require.Equal(t, core.Centipoints(42_857), clean.Entries[2].AmountCp)
		require.Equal(t, core.Centipoints(0), sumEntries(clean))
	})
}

// TestAttendanceWeighted_PlanAdjustment_MovesPointsAgainstACounterparty: an adjustment is two entries,
// never one.
func TestAttendanceWeighted_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	p, err := strategy.AttendanceWeighted{}.PlanAdjustment(
		newCtx(t, 2, 5_000, `{"floor_cp": -1000}`), strategy.AdjustmentEvent{
			Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
			AmountCp:    -750,
			EffectiveAt: fixedNow,
			Reason:      "double-credited raid",
		})
	require.NoError(t, err)

	require.Equal(t, "adjustment", p.Kind)
	require.Len(t, p.Entries, 2)
	require.Equal(t, acct(0), p.Entries[0].AccountID, "the adjusted account leads")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(750), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(-1000), requireNonNegativeFloor(t, p),
		"the POOL's floor reaches the proposal, not the strategy catalogue's default")
}

// TestAttendanceWeighted_PlanReversal_NegatesRestampsAndDeclaresNoFloor.
func TestAttendanceWeighted_PlanReversal_NegatesRestampsAndDeclaresNoFloor(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"raid_pot_cp": 100000}`)
	s := strategy.AttendanceWeighted{}

	original, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees: []strategy.Share{
			{AccountID: acct(0), Weight: 12},
			{AccountID: acct(1), Weight: 9},
			{AccountID: acct(2), Weight: 8},
		},
		EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
		Reason:      "Vox, 12 ticks",
	})
	require.NoError(t, err)

	reversal, err := s.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            original.Kind,
		StrategyID:      original.StrategyID,
		StrategyVersion: original.StrategyVersion,
		Reason:          original.Reason,
		EffectiveAt:     original.EffectiveAt,
		Entries:         original.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.NotNil(t, reversal.ReversesBatchID)

	net := map[core.ULID]core.Centipoints{}
	for _, e := range append(append([]strategy.EntryProposal{}, original.Entries...),
		reversal.Entries...) {
		net[e.AccountID] += e.AmountCp
	}

	for id, v := range net {
		require.Equal(t, core.Centipoints(0), v, "account %s is %d centipoints out", id, v)
	}

	require.Equal(t, fixedNow, reversal.EffectiveAt,
		"a correction is a new economic event at the time it is decided")
	require.NotContains(t, invariantKinds(reversal), strategy.InvariantNonNegative,
		"a floor on a reversal does not prevent a debt — it prevents the correction")
}

// TestAttendanceWeighted_PlanReversal_ForeignBatch_IsRefused: a reversal must be planned by the
// strategy that planned the original.
func TestAttendanceWeighted_PlanReversal_ForeignBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.AttendanceWeighted{}.PlanReversal(
		newCtx(t, 1, 0, `{}`), strategy.LedgerBatch{
			ID:         acct(70),
			StrategyID: "suicide_kings",
			Entries: []strategy.EntryProposal{
				{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
			},
		})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "suicide_kings")
}

// TestAttendanceWeighted_PlanReversal_IgnoresTodaysPoolConfig: a config this version cannot parse must
// not stop a correction, because a reversal is the only repair an append-only ledger has.
func TestAttendanceWeighted_PlanReversal_IgnoresTodaysPoolConfig(t *testing.T) {
	t.Parallel()

	for _, config := range []string{`{"a_knob_from_the_future": 1}`, `null`, `{`, `{"raid_pot_cp": 0}`} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			p, err := strategy.AttendanceWeighted{}.PlanReversal(
				newCtx(t, 2, 0, config), strategy.LedgerBatch{
					ID:                 acct(70),
					Kind:               "attendance",
					StrategyID:         "attendance_weighted",
					StrategyVersion:    "0.1.0",
					ConfigSnapshotJSON: `{"raid_pot_cp":100000}`,
					Entries: []strategy.EntryProposal{
						{AccountID: ledger.AccountIDGuildBank, BalanceKind: strategy.BalanceKindDKP, AmountCp: -300},
						{AccountID: acct(1), BalanceKind: strategy.BalanceKindDKP, AmountCp: 300},
					},
				})
			require.NoError(t, err)
			require.Equal(t, `{"raid_pot_cp":100000}`, p.ConfigSnapshotJSON,
				"the reversal carries the ORIGINAL's snapshot forward, not today's document")
		})
	}
}

// TestAttendanceWeighted_Spendable_IsAPlainBalance is the defect the guide names outright: letting
// attendance scale the spendable balance rather than the ranking means a member's bank shrinks when
// they miss a raid, so their past purchases were retroactively mispriced.
func TestAttendanceWeighted_Spendable_IsAPlainBalance(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 4_200, `{"raid_pot_cp": 100000}`)

	got, err := strategy.AttendanceWeighted{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(4_200), got,
		"the balance is a SUM over committed entries, unscaled by anything this strategy computes")
	require.Equal(t, []int64{7}, ctx.readAtSeq, "read at the head seq, positionally")

	rank, err := strategy.AttendanceWeighted{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(4_200), rank.Rank,
		"the documented balance x attendance %% score needs attendance statistics that land in "+
			"Phase 4 and that the Ctx facade deliberately does not expose; ranking by balance is the "+
			"honest placeholder, and a composed pool routes Priority to its SPEND rule anyway")
	require.Equal(t, acct(0).String(), rank.Tiebreak)
}

// TestAttendanceWeighted_UnsupportedOperations_RefuseAndNameTheStrategy covers every question this
// strategy declines.
func TestAttendanceWeighted_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{}`)
	s := strategy.AttendanceWeighted{}

	award, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:  strategy.ItemRef{Name: "Cloak of Flames"},
	})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "zero_sum", "the refusal points at what to pair it with")
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
	require.ErrorContains(t, err, "attendance_weighted",
		"every refusal names the strategy that made it: the error crosses a package boundary on its "+
			"way to a 501, and a refusal with no subject is the support ticket nobody can act on")
}

// TestAttendanceWeighted_Identity_IsStableAndDeclared covers the values written onto every batch, and
// the slot the whole strategy is placed in.
func TestAttendanceWeighted_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.AttendanceWeighted{}

	require.Equal(t, "attendance_weighted", s.ID(),
		"the id is written onto every batch and is public API: renaming it orphans history")
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, strategy.RuleEarn, s.RuleKind())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants(), "a strategy that declares no invariants is a red flag")

	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0])
}

// TestAttendanceWeighted_IsAnAlternativeToTick_NotACompanion is the composition fact a reader of the
// first-run guide's "Attendance-first" preset needs: a pool holds ONE earn rule, so this replaces
// `tick` rather than running beside it.
func TestAttendanceWeighted_IsAnAlternativeToTick_NotACompanion(t *testing.T) {
	t.Parallel()

	rules, err := strategy.PoolConfig{
		EarnStrategyID:  "attendance_weighted",
		EarnConfigJSON:  `{"raid_pot_cp": 100000}`,
		SpendStrategyID: "zero_sum",
	}.Resolve()
	require.NoError(t, err)
	require.Equal(t, "attendance_weighted", rules.Earn.Strategy.ID())

	_, err = strategy.PoolConfig{SpendStrategyID: "attendance_weighted"}.Resolve()
	require.ErrorIs(t, err, strategy.ErrWrongRuleKind,
		"it earns; a pool that put it in the spend slot would have no way to award an item")
}

// --- Rejections ---------------------------------------------------------------------------------

// TestAttendanceWeighted_Planners_RejectUnplannableEvents is the table of everything a planner
// refuses.
func TestAttendanceWeighted_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.AttendanceWeighted{}

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "an event with no attendees",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an override that distributes nothing",
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
			name:   "a negative override",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				negative := core.Centipoints(-1)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: shares(2), AmountCp: &negative,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "nobody attended anything",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: 0}},
				})

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
			name:   "weights that sum past int64",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: math.MaxInt64},
						{AccountID: acct(1), Weight: math.MaxInt64},
					},
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
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID: acct(70), StrategyID: "attendance_weighted",
				})

				return err
			},
			wantErr: strategy.ErrEmptyProposal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 1_000, tc.config)), tc.wantErr)
		})
	}
}

// TestAttendanceWeighted_Config_RejectsWhatTheSchemaWouldHaveRejected asserts the planner re-validates
// rather than defaulting: a config that reached it past the API edge must not silently run a pot
// nobody chose.
func TestAttendanceWeighted_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`"attendance_weighted"`,
		`{"raid_pot_cp": 100}{"raid_pot_cp": 200}`,
		`{"raid_pot_cp": 0}`,
		`{"raid_pot_cp": -1}`,
		`{"raid_pot_cp": 1.5}`,
		`{"raid_pot_cp": "100"}`,
		`{"raid_pot_cp": null}`,
		`{"raid_pot_cd": 100}`,
		`{"floor_cp": null}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyAttendanceWeightedPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 3, 1_000, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// everyAttendanceWeightedPlanner returns one minimal, otherwise-legal call per planner that reads the
// pool's config.
//
// PlanReversal is deliberately absent: it reads neither the current config nor any façade value it
// could fail on — see TestAttendanceWeighted_PlanReversal_IgnoresTodaysPoolConfig.
func everyAttendanceWeightedPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.AttendanceWeighted{}

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

// TestAttendanceWeighted_Config_AbsentIsTheDefaults_AndTypoedIsNot is the other direction of the
// strict decoding: a pool that has set nothing must still plan.
func TestAttendanceWeighted_Config_AbsentIsTheDefaults_AndTypoedIsNot(t *testing.T) {
	t.Parallel()

	for _, config := range []string{"", "{}", "  ", "\n{}\n"} {
		t.Run(fmt.Sprintf("%q", config), func(t *testing.T) {
			t.Parallel()

			p, err := strategy.AttendanceWeighted{}.PlanAttendance(
				newCtx(t, 2, 0, config), strategy.AttendanceEvent{Attendees: shares(2)})
			require.NoError(t, err)

			require.Equal(t, core.Centipoints(-10_000), p.Entries[0].AmountCp,
				"an unset config runs the shipped default: 100.00 points a raid")
			require.Equal(t, core.Centipoints(5_000), p.Entries[1].AmountCp)
		})
	}

	t.Run("a transposed knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.AttendanceWeighted{}.PlanAttendance(
			newCtx(t, 1, 0, `{"raid_pot_cd": 1000}`),
			strategy.AttendanceEvent{Attendees: shares(1)})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "raid_pot_cd")
	})

	t.Run("a null knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.AttendanceWeighted{}.PlanAttendance(
			newCtx(t, 1, 0, `{"raid_pot_cp": null, "floor_cp": null}`),
			strategy.AttendanceEvent{Attendees: shares(1)})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "floor_cp",
			"with several null knobs the first in sorted order is named, on every run")
	})
}

// TestAttendanceWeighted_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases FROM THE SCHEMA,
// so a knob added later is covered without anybody remembering to add a row.
func TestAttendanceWeighted_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.AttendanceWeighted{}.ConfigSchema(),
		map[string]string{},
		func(t *testing.T, config string) error {
			t.Helper()

			_, err := strategy.AttendanceWeighted{}.PlanAttendance(
				newCtx(t, 1, 0, config), strategy.AttendanceEvent{Attendees: shares(1)})

			return err
		})
}

// TestAttendanceWeighted_ConfigSchema_DeclaresNoNumber restates canonical §1 where a schema could
// break it: `number` in a JSON Schema permits 12.5, and a decimal in the point path is a float.
func TestAttendanceWeighted_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.AttendanceWeighted{}.ConfigSchema())
}

// TestAttendanceWeighted_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan
// rather than producing a batch built on a zero.
func TestAttendanceWeighted_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.AttendanceWeighted{}
	boom := fmt.Errorf("the read pool is closed")

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyAttendanceWeightedPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 1_000, `{}`)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, `{}`)
		ctx.balanceErr = boom

		_, err := s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})

	// The allocator is the one façade call whose arguments this planner has already validated, so no
	// event can make it refuse — see fakeCtx.allocateErr for why the branch is still watched rather
	// than assumed. A planner that ignored the error would build a batch out of a nil credit list, and
	// the bank's debit would be the only entry in it.
	t.Run("allocator", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 3, 0, `{}`)
		ctx.allocateErr = boom

		_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(3)})
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "attendance_weighted",
			"the wrapping names the strategy and the amount it was dividing")
	})
}

// --- Declaration and the goldens ------------------------------------------------------------------

// attendanceWeightedGoldenConfig is the config every golden is planned under: every knob set to a
// non-default value, so that a knob that stopped being read shows up as a changed golden rather than
// as nothing.
const attendanceWeightedGoldenConfig = `{"raid_pot_cp":100000,"floor_cp":-500}`

// attendanceWeightedGoldenCases is one case per planner this strategy supports.
func attendanceWeightedGoldenCases() []goldenCase {
	s := strategy.AttendanceWeighted{}
	tick, raid := acct(80), acct(81)

	return []goldenCase{
		{
			name: "attendance",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAttendance(
					newCtx(tb, 3, 0, attendanceWeightedGoldenConfig), strategy.AttendanceEvent{
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
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(
					newCtx(tb, 3, 0, attendanceWeightedGoldenConfig), strategy.AdjustmentEvent{
						Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
						AmountCp:    -750,
						EffectiveAt: fixedNow,
						Reason:      "double-credited raid on 2024-05-30",
					})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := newCtx(tb, 3, 0, attendanceWeightedGoldenConfig)

				original, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 3},
						{AccountID: acct(1), Weight: 1},
					},
					TickID:      &tick,
					EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:      "Vox, 12 ticks",
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

// TestAttendanceWeighted_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three
// fields.
func TestAttendanceWeighted_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, attendanceWeightedGoldenDir, attendanceWeightedGoldenCases())
}

// TestAttendanceWeighted_Goldens_CoverEveryPlanner is the anti-drift half: a planner added without a
// golden would leave the whole-proposal assertion covering fewer planners than the strategy has.
func TestAttendanceWeighted_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, attendanceWeightedGoldenDir, attendanceWeightedGoldenCases(),
		[]string{"adjustment", "attendance", "reversal"})
}

// TestAttendanceWeighted_EveryPlannerInvariant_IsDeclared keeps the strategy-level catalogue and the
// per-proposal sets in step, in both directions.
func TestAttendanceWeighted_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.AttendanceWeighted{},
		plannedProposals(t, attendanceWeightedGoldenCases()))
}

// TestAttendanceWeighted_Planners_ConsumeNoRandomness asserts the injected Rng is offered and refused.
func TestAttendanceWeighted_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 10_000, attendanceWeightedGoldenConfig)
	s := strategy.AttendanceWeighted{}

	attendance, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees: shares(3), EffectiveAt: fixedNow,
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
		"attendance_weighted must consume no randomness: its only tie-break is the allocator's "+
			"account_id ordering, which is deliberately NOT random so that two replays agree")
}
