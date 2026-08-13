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

// cap, tested at the strategy level. Phase 1, #193.
//
// The arithmetic tests carry the weight here, because a cap is arithmetic: what a raider at 95 of a
// 100-point ceiling earns from a 10-point tick is the whole product. The idempotence claim (P6) is a
// property in earn_property_test.go and an example below, in both places deliberately — the example
// is what a reviewer reads, the property is what covers the balances nobody thought of.

// capGoldenDir is where cap's canonical proposals live.
const capGoldenDir = "../../test/golden/strategy/cap"

// --- The earning half: soft cap, hard cap, and the order they apply in --------------------------

// TestCap_PlanAttendance_HardCap_ClampsTheCreditToTheHeadroom is the ceiling doing its job.
//
// A raider 5.00 points below a 100.00 ceiling earns 5.00 of a 10.00 tick, not 10.00 and not nothing.
// Clamping to the headroom rather than refusing the whole credit is what makes the cap a ceiling
// rather than a cliff.
func TestCap_PlanAttendance_HardCap_ClampsTheCreditToTheHeadroom(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 9_500
	ctx.balances[acct(1)] = 1_000

	p, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   shares(2),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(500), p.Entries[1].AmountCp,
		"9500 of a 10000 ceiling leaves 500 of headroom, so a 1000 tick credits 500")
	require.Equal(t, core.Centipoints(1_000), p.Entries[2].AmountCp,
		"a raider nowhere near the ceiling earns the whole tick")
	require.Equal(t, core.Centipoints(-1_500), p.Entries[0].AmountCp,
		"the bank funds exactly what was credited: nothing is routed anywhere when an earn is "+
			"reduced, because the points were never credited in the first place")
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestCap_PlanAttendance_AtTheHardCap_EarnsNothingAndGetsNoEntry covers the boundary and the
// CHECK (amount_cp <> 0) consequence.
func TestCap_PlanAttendance_AtTheHardCap_EarnsNothingAndGetsNoEntry(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 10_000 // exactly at it
	ctx.balances[acct(1)] = 12_000 // above it, from an import or a lowered ceiling

	_, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   shares(2),
		EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan,
		"every attendee is at or above the ceiling, so there is no legal batch to write — the "+
			"BatchNonEmpty invariant is unwaivable and an entry of zero is illegal")
}

// TestCap_PlanAttendance_SoftCap_ReducesOnlyThePartAboveIt is the over-cap earn ratio, and the split
// across the threshold is the part that is easy to get wrong.
//
// A raider 2.00 points below an 80.00 soft cap earning a 10.00 tick at a quarter ratio receives
// 2.00 + 8.00 × 0.25 = 4.00, not 10.00 × 0.25 = 2.50. Reducing the WHOLE credit would silently
// penalise the part that was still under the cap.
func TestCap_PlanAttendance_SoftCap_ReducesOnlyThePartAboveIt(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"soft_cap_cp": 8000, "over_cap_earn_bp": 2500, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 7_800  // 200 of room, then 800 at a quarter
	ctx.balances[acct(1)] = 12_000 // wholly above the soft cap
	ctx.balances[acct(2)] = -500   // in debt: the cap is a ceiling on a balance, not a budget

	p, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   shares(3),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(400), p.Entries[1].AmountCp,
		"200 under the cap in full plus 800 above it at 2500 bp")
	require.Equal(t, core.Centipoints(250), p.Entries[2].AmountCp,
		"already above the soft cap, so the whole tick earns the reduced ratio")
	require.Equal(t, core.Centipoints(1_000), p.Entries[3].AmountCp,
		"a negative balance earns in full: it has more room below the cap, not less")
	require.Equal(t, core.Centipoints(-1_650), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestCap_PlanAttendance_SoftCapThenHardCap covers both knobs on one account, in the order they
// apply.
//
// The soft cap reduces and the hard cap clamps the result. Applying them the other way round would
// let a reduced credit cross a ceiling the officer set, which is the one thing a hard cap promises
// cannot happen.
func TestCap_PlanAttendance_SoftCapThenHardCap(t *testing.T) {
	t.Parallel()

	const config = `{"soft_cap_cp": 1000, "hard_cap_cp": 1200, "over_cap_earn_bp": 5000, ` +
		`"tick_award_cp": 1000}`

	ctx := newCtx(t, 1, 0, config)
	ctx.balances[acct(0)] = 900

	p, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   shares(1),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(300), p.Entries[1].AmountCp,
		"the soft cap reduces 1000 to 100 + 450 = 550, and the hard cap then clamps it to the "+
			"300 of headroom that is actually left")
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestCap_PlanAttendance_ReadsTheHeadSeq: the balance a cap is applied against is the one this batch
// will be written after, and nothing else.
func TestCap_PlanAttendance_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 500, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)

	_, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   shares(1),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.NotEmpty(t, ctx.readAtSeq)

	for _, seq := range ctx.readAtSeq {
		require.Equal(t, int64(7), seq, "an earn is capped against the balance at the pool head")
	}
}

// --- The trim half: the cap run -----------------------------------------------------------------

// TestCap_PlanDecay_TrimsWhatIsAboveTheCapIntoTheBank is the cadence run.
//
// It reads POSITIONALLY at the run's as-of seq: a batch committed while the run is planning must not
// change what it trimmed, and a backdated effective_at must not change what a past balance was.
func TestCap_PlanDecay_TrimsWhatIsAboveTheCapIntoTheBank(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 4, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 12_345 // 2345 above
	ctx.balances[acct(1)] = 10_000 // exactly at it: compliant, no entry
	ctx.balances[acct(2)] = 9_999  // below
	ctx.balances[acct(3)] = 10_001 // one centipoint above

	p, err := strategy.Cap{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     4,
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, "cap", p.Kind,
		"the batch names the rule that moved the points even though the run row and the planner are "+
			"shared with decay and start_points")
	require.Equal(t, "cap 2026-W31", p.Reason)
	require.Len(t, p.Entries, 3, "two accounts above the ceiling plus the bank")
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-2_345), p.Entries[0].AmountCp)
	require.Equal(t, acct(3), p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(-1), p.Entries[1].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(2_346), p.Entries[2].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	require.NotEmpty(t, ctx.readAtSeq)

	for _, seq := range ctx.readAtSeq {
		require.Equal(t, int64(4), seq,
			"every balance must be read AT THE RUN'S SEQ. Reading the head would let a batch "+
				"committed mid-run change what the run trimmed.")
	}
}

// TestCap_PlanDecay_ASecondRunHasNothingToTrim is property P6 as an example: applying the cap twice
// produces one batch, and the second application cannot move a balance past the cap because there is
// nothing left above it.
//
// IDEMPOTENCE IS STRUCTURAL rather than a flag: the run reads the balances the first run left. The
// randomised half is TestProperty_P6_Cap_ClampsAndIsIdempotent.
func TestCap_PlanDecay_ASecondRunHasNothingToTrim(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 12_345
	ctx.balances[acct(1)] = 40_000
	ctx.balances[acct(2)] = 500

	run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow}

	first, err := strategy.Cap{}.PlanDecay(ctx, run)
	require.NoError(t, err)

	// Apply it, exactly as the ledger would.
	for _, e := range first.Entries {
		ctx.balances[e.AccountID] += e.AmountCp
	}

	require.Equal(t, core.Centipoints(10_000), ctx.balances[acct(0)])
	require.Equal(t, core.Centipoints(10_000), ctx.balances[acct(1)])

	_, err = strategy.Cap{}.PlanDecay(ctx, run)
	require.ErrorIs(t, err, strategy.ErrNothingToPlan,
		"the second run finds nobody above the ceiling, so there is no batch to write and the job "+
			"records the run as skipped rather than posting a second trim")
}

// TestCap_PlanDecay_UsesTheRosterAndSkipsSystemAccounts covers the façade read the run falls back to.
//
// The guild bank is deliberately given a balance above the ceiling. Trimming a system account would
// mean capping the counterparty that makes every batch balance — and the bank is structurally
// negative by design in any case.
func TestCap_PlanDecay_UsesTheRosterAndSkipsSystemAccounts(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 50_000, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[ledger.AccountIDGuildBank] = 90_000

	p, err := strategy.Cap{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "two raiders trimmed, one credit to the bank")
	require.Equal(t, core.Centipoints(-40_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(-40_000), p.Entries[1].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(80_000), p.Entries[2].AmountCp,
		"the bank receives the trimmed points; it is not itself trimmed")
}

// --- Adjustment, reversal, spendable ------------------------------------------------------------

// TestCap_PlanAdjustment_IsNotCapped is a rule stated as a test because it looks like an omission.
//
// An adjustment is an officer correcting a number they know to be wrong — often a correction of this
// strategy's own arithmetic. Clamping it would mean the ledger silently refusing to record what the
// officer decided, and the cap run is what brings the balance back under the ceiling in a batch that
// says so.
func TestCap_PlanAdjustment_IsNotCapped(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000, "floor_cp": -500}`)
	ctx.balances[acct(0)] = 9_000

	p, err := strategy.Cap{}.PlanAdjustment(ctx, strategy.AdjustmentEvent{
		Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
		AmountCp:    5_000,
		EffectiveAt: fixedNow,
		Reason:      "missed three raids' worth of ticks in April",
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(5_000), p.Entries[0].AmountCp,
		"the officer's number reaches the ledger unclamped, taking the account above the ceiling; "+
			"the next cap run trims it and says so")
	require.Equal(t, core.Centipoints(0), sumEntries(p))
	require.Equal(t, core.Centipoints(-500), requireNonNegativeFloor(t, p))
}

// TestCap_PlanReversal_OfATrim_GivesThePointsBack: a reversal that re-clamped would not be a
// reversal. The next run trims again if the ceiling still applies.
func TestCap_PlanReversal_OfATrim_GivesThePointsBack(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 12_000

	trim, err := strategy.Cap{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     4,
		EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
	})
	require.NoError(t, err)

	reversal, err := strategy.Cap{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            trim.Kind,
		StrategyID:      trim.StrategyID,
		StrategyVersion: trim.StrategyVersion,
		EffectiveAt:     trim.EffectiveAt,
		Entries:         trim.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.Equal(t, core.Centipoints(2_000), reversal.Entries[0].AmountCp,
		"the trimmed points go back, above the cap — the reversal says the trim should not have "+
			"happened, and re-clamping would make it a no-op")
	require.Equal(t, fixedNow, reversal.EffectiveAt)
	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(reversal))
}

// TestCap_Spendable_IsNotClampedToTheCap: the cap is posted as batches, so a capped balance is
// already the sum. A Spendable that also clamped would hide the exact case the trim run exists to
// correct — a balance above a ceiling that was lowered an hour ago.
func TestCap_Spendable_IsNotClampedToTheCap(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	ctx.balances[acct(0)] = 12_345

	got, err := strategy.Cap{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(12_345), got,
		"the member and the ledger must see the same number; the trim run is what reconciles it "+
			"with the ceiling")

	rank, err := strategy.Cap{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(12_345), rank.Rank)
}

// TestCap_UnsupportedOperations_RefuseAndNameTheStrategy covers the four methods this strategy
// declines.
func TestCap_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`)
	s := strategy.Cap{}

	_, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:  strategy.ItemRef{Name: "Cloak of Flames"},
	})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "cap")

	hint, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Nil(t, hint)

	require.ErrorIs(t, s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)},
		strategy.Bid{AccountID: acct(0), AmountCp: 100}), strategy.ErrUnsupported)

	resolution, err := s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 7}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Empty(t, resolution.Winners)
}

// TestCap_Identity_IsStableAndDeclared covers the three values written onto every batch.
func TestCap_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.Cap{}

	require.Equal(t, "cap", s.ID())
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants())

	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0])
}

// --- Rejections ---------------------------------------------------------------------------------

// TestCap_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
func TestCap_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.Cap{}
	live := `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "attendance with no attendees",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an override that awards nothing",
			config: live,
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
			name:   "a negative weight",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: -1}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a repeated attendee",
			config: live,
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
			config: live,
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
			name:   "a cap run against a pool with only a soft cap",
			config: `{"soft_cap_cp": 5000, "over_cap_earn_bp": 2500, "tick_award_cp": 1000}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31"})

				return err
			},
			wantErr: strategy.ErrInvalidConfig,
		},
		{
			name:   "a cap run with no period key",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a cap run in which nobody is above the ceiling",
			config: `{"hard_cap_cp": 100000, "tick_award_cp": 1000}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 7})

				return err
			},
			wantErr: strategy.ErrNothingToPlan,
		},
		{
			name:   "a cap run naming an account twice",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey: "2026-W31",
					AsOfSeq:   7,
					Accounts: []strategy.AccountRef{
						{ID: acct(0), Kind: "person"},
						{ID: acct(0), Kind: "person"},
					},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment with no account",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{AmountCp: 100})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a reversal of an empty batch",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{ID: acct(70), StrategyID: "cap"})

				return err
			},
			wantErr: strategy.ErrEmptyProposal,
		},
		{
			name:   "a reversal of another strategy's batch",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID:         acct(70),
					StrategyID: "fixed_price",
					Entries: []strategy.EntryProposal{
						{AccountID: acct(0), BalanceKind: "dkp", AmountCp: 1},
					},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 1_000, tc.config)), tc.wantErr)
		})
	}
}

// TestCap_PlanDecay_TotalOverflow_IsRefused covers the trim accumulator running out of int64.
//
// Every individual balance fits and every individual excess fits; it is the total credited back to
// the bank that does not. A wrapped total would land on the bank's entry, where the batch would
// either be rejected with an arithmetic message naming no cause or — with a different sign pattern —
// balance.
func TestCap_PlanDecay_TotalOverflow_IsRefused(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"hard_cap_cp": 1, "tick_award_cp": 1000}`)

	const nearlyHalf = core.Centipoints(4_600_000_000_000_000_000)

	for i := range 3 {
		ctx.balances[acct(i)] = nearlyHalf
	}

	_, err := strategy.Cap{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "2026-W31")
}

// TestCap_PlanAttendance_BalanceOverflow_IsRefused covers the one earn input that cannot be reasoned
// about: a balance at the bottom of int64, where the room below the cap is not representable.
func TestCap_PlanAttendance_BalanceOverflow_IsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
	}{
		{"soft cap", `{"soft_cap_cp": 8000, "over_cap_earn_bp": 2500, "tick_award_cp": 1000}`},
		{"hard cap", `{"hard_cap_cp": 8000, "tick_award_cp": 1000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 1, 0, tc.config)
			ctx.balances[acct(0)] = math.MinInt64

			_, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
				Attendees:   shares(1),
				EffectiveAt: fixedNow,
			})
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, acct(0).String(),
				"the refusal names the account, because the roster is what an officer would have to "+
					"look at")
		})
	}
}

// TestCap_Config_RejectsWhatTheSchemaWouldHaveRejected covers the strict decode and the three
// relationships between knobs that no per-field bound can express.
func TestCap_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`{"hard_cap_cp": 1000}{"hard_cap_cp": 2000}`,
		`{"hard_cap_cp": null}`,
		`{"hard_cap_cp": 1.5}`,
		`{"hard_cap_cp": "1000"}`,
		`{"hard_cpa_cp": 1000}`,
		`{"hard_cap_cp": -1}`,
		`{"soft_cap_cp": -1}`,
		`{"hard_cap_cp": 1000, "tick_award_cp": 0}`,
		`{"soft_cap_cp": 1000, "over_cap_earn_bp": 10001}`,
		`{"soft_cap_cp": 1000, "over_cap_earn_bp": -1}`,

		// The three relationships. Each parses, and each means something the officer did not intend.
		`{}`,
		`{"tick_award_cp": 1000}`,
		`{"hard_cap_cp": 1000, "soft_cap_cp": 5000}`,
		`{"over_cap_earn_bp": 2500, "hard_cap_cp": 1000}`,
		`{"hard_cap_cp": 1000, "floor_cp": 2000}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyCapPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 1, 0, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// TestCap_Config_NamesTheKnobThatIsWrong: "invalid config" sends an officer to read the whole form.
func TestCap_Config_NamesTheKnobThatIsWrong(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ config, want string }{
		{`{}`, "hard_cap_cp and soft_cap_cp are both 0"},
		{`{"hard_cap_cp": 1000, "soft_cap_cp": 5000}`, "soft_cap_cp 5000 is above hard_cap_cp 1000"},
		{`{"over_cap_earn_bp": 2500, "hard_cap_cp": 1000}`, "over_cap_earn_bp is 2500 with no soft_cap_cp"},
		{`{"hard_cap_cp": 1000, "floor_cp": 2000}`, "floor_cp 2000 is above hard_cap_cp 1000"},
		{`{"hard_cpa_cp": 1000}`, "hard_cpa_cp"},
		{`{"hard_cap_cp": null}`, "hard_cap_cp"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.Cap{}.PlanAttendance(
				newCtx(t, 1, 0, tc.config), strategy.AttendanceEvent{Attendees: shares(1)})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// everyCapPlanner returns one minimal, otherwise-legal call per planner that reads the pool's config.
func everyCapPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.Cap{}

	return map[string]func(ctx strategy.Ctx) error{
		"attendance": func(ctx strategy.Ctx) error {
			_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(2)})

			return err
		},
		"cap run": func(ctx strategy.Ctx) error {
			_, err := s.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: "2026-W31",
				Accounts:  []strategy.AccountRef{{ID: acct(0), Kind: "person"}},
			})

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

// TestCap_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases from the schema.
func TestCap_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.Cap{}.ConfigSchema(),
		map[string]string{
			// Each of these needs a companion knob to be a legal document at all — which is the
			// relationship validateCapConfig exists to enforce, seen from the accepting side.
			"over_cap_earn_bp": `{"soft_cap_cp":1000,"over_cap_earn_bp":2500}`,
			"tick_award_cp":    `{"hard_cap_cp":5000,"tick_award_cp":1}`,
			"floor_cp":         `{"hard_cap_cp":5000,"floor_cp":0}`,
		},
		func(t *testing.T, config string) error {
			t.Helper()

			_, err := strategy.Cap{}.PlanAttendance(
				newCtx(t, 1, 0, config), strategy.AttendanceEvent{Attendees: shares(1)})

			return err
		})
}

// TestCap_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan rather than
// producing a batch built on a zero — the failure a fake makes easy to get wrong, because a Balance
// that returns (0, err) and a planner that ignores the error caps nobody, which looks exactly like a
// successful run.
func TestCap_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.Cap{}
	boom := fmt.Errorf("the read pool is closed")
	live := `{"hard_cap_cp": 10000, "tick_award_cp": 1000}`

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, live)
		ctx.balanceErr = boom

		_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(2)})
		require.ErrorIs(t, err, boom)

		_, err = s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 3})
		require.ErrorIs(t, err, boom)

		_, err = s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})

	t.Run("roster", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 50_000, live)
		ctx.rosterErr = boom

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31"})
		require.ErrorIs(t, err, boom)
	})

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyCapPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 50_000, live)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})
}

// --- Declaration and the goldens ------------------------------------------------------------------

// capGoldenConfig sets every knob to a non-default value, so a knob that stopped being read shows up
// as a changed golden rather than as nothing.
const capGoldenConfig = `{"hard_cap_cp":12000,"soft_cap_cp":8000,"over_cap_earn_bp":2500,` +
	`"tick_award_cp":150,"floor_cp":-500}`

// capGoldenCtx is the façade every cap golden is planned against: one raider above the hard cap, one
// under both, one nearly empty — so the goldens exercise the clamp, the reduction and the ordinary
// case in the same batch.
func capGoldenCtx(tb testing.TB) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, 3, 0, capGoldenConfig)
	ctx.balances[acct(0)] = 12_345
	ctx.balances[acct(1)] = 6_789
	ctx.balances[acct(2)] = 9

	return ctx
}

// capGoldenCases is one case per planner cap supports.
func capGoldenCases() []goldenCase {
	s := strategy.Cap{}
	tick, raid := acct(80), acct(81)

	return []goldenCase{
		{
			name: "attendance",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAttendance(capGoldenCtx(tb), strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(1), Weight: 2},
						{AccountID: acct(2), Weight: 1},
					},
					TickID:      &tick,
					RaidID:      &raid,
					EffectiveAt: fixedNow,
					Reason:      "Vox, tick 3",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "cap",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanDecay(capGoldenCtx(tb), strategy.DecayRun{
					PeriodKey:   "2026-W31",
					AsOfSeq:     7,
					EffectiveAt: fixedNow,
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(capGoldenCtx(tb), strategy.AdjustmentEvent{
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
				ctx := capGoldenCtx(tb)

				original, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey:   "2026-W31",
					AsOfSeq:     7,
					EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
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

// TestCap_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestCap_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, capGoldenDir, capGoldenCases())
}

// TestCap_Goldens_CoverEveryPlanner is the anti-drift half.
func TestCap_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, capGoldenDir, capGoldenCases(),
		[]string{"adjustment", "attendance", "cap", "reversal"})
}

// TestCap_EveryPlannerInvariant_IsDeclared keeps the catalogue and the per-proposal sets in step.
func TestCap_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.Cap{}, plannedProposals(t, capGoldenCases()))
}

// TestCap_Planners_ConsumeNoRandomness: a seed on a batch asserts that replaying from it reproduces
// the plan, and this strategy's only ordering is the account id.
func TestCap_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := capGoldenCtx(t)

	for _, p := range plannedProposals(t, capGoldenCases()) {
		require.Nil(t, p.RngSeed, "%s carries a seed it never consumed", p.Kind)
	}

	_, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees: shares(3), EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Zero(t, ctx.rng.calls, "cap must consume no randomness")
}

// TestCap_ConfigSchema_DeclaresNoNumber restates canonical §1 where a schema could break it.
func TestCap_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.Cap{}.ConfigSchema())
}
