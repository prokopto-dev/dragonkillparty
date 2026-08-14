package strategy_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// decay_window, tested at the strategy level. Phase 1, #194.
//
// The claim that needs the most testing is not the arithmetic — it is that an earning expires ONCE.
// A window decay that re-expires its own batches drifts a whole roster's balances upward or downward
// depending on which way it gets the sum wrong, and every individual batch looks correct on its own.
// So the log-backed façade in decay_test.go is what most of this file runs against: committing a
// batch means rows at a higher seq, exactly as the ledger would, and the next run reads them.

// decayWindowGoldenDir is where decay_window's canonical proposals live.
const decayWindowGoldenDir = "../../test/golden/strategy/decay_window"

// aWindow is the slice a run expires, for the tests that do not care which one it is.
func aWindow(fromSeq, toSeq int64) *strategy.ExpiryWindow {
	return &strategy.ExpiryWindow{Days: 90, FromSeq: fromSeq, ToSeq: toSeq}
}

// --- The run --------------------------------------------------------------------------------------

// TestDecayWindow_PlanDecay_ExpiresTheSliceThatAgedOut is the guide's example as a batch: what a
// member earned before the window opened stops counting, and stops counting by being REMOVED.
func TestDecayWindow_PlanDecay_ExpiresTheSliceThatAgedOut(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"window_days": 90}`)
	ctx.balances[acct(0)] = 43_000
	ctx.earned[acct(0)] = 31_000 // the 2026-04-02 earning, 123 days old
	ctx.balances[acct(1)] = 19_000
	ctx.earned[acct(1)] = 0 // everything this member earned is still inside the window
	ctx.balances[acct(2)] = 5_000
	ctx.earned[acct(2)] = 1

	p, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     9,
		Window:      aWindow(2, 5),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, "decay", p.Kind,
		"a member's statement says that time removed these points; WHICH decay rule did it is "+
			"strategy_id, and a reversal routes on that")
	require.Equal(t, "decay_window", p.StrategyID)
	require.Equal(t, "decay 2026-W31", p.Reason)
	require.Len(t, p.Entries, 3, "two members had earnings age out, plus the bank")
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-31_000), p.Entries[0].AmountCp)
	require.Equal(t, acct(2), p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(-1), p.Entries[1].AmountCp,
		"one centipoint is still an earning that aged out")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(31_001), p.Entries[2].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	require.NotEmpty(t, ctx.earnedSlices)

	for _, slice := range ctx.earnedSlices {
		require.Equal(t, [2]int64{2, 5}, slice,
			"the run expires the slice the scheduler resolved and not the whole of history")
	}

	for _, seq := range ctx.readAtSeq {
		require.Equal(t, int64(9), seq, "balances are read at the run's as-of seq, never at the head")
	}
}

// TestDecayWindow_PlanDecay_ClampsAtWhatIsLeftAboveTheFloor is the case that separates an expiry from
// a spend.
//
// A member who earned 300.00 a year ago and has spent 280.00 of it has 20.00 left; expiring the whole
// 300.00 would push them 280.00 into debt for money they no longer hold. The clamp is what makes the
// window a rule about the AGE of an earning rather than a debt the pool collects.
func TestDecayWindow_PlanDecay_ClampsAtWhatIsLeftAboveTheFloor(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"window_days": 90, "floor_cp": -500}`)
	ctx.balances[acct(0)] = 2_000
	ctx.earned[acct(0)] = 30_000 // spent nearly all of it since
	ctx.balances[acct(1)] = -500
	ctx.earned[acct(1)] = 30_000 // already on the floor: nothing left to take
	ctx.balances[acct(2)] = -900
	ctx.earned[acct(2)] = 30_000 // below the floor already, from a reversal

	p, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     9,
		Window:      aWindow(2, 5),
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 2, "one member had something left above the floor, plus the bank")
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-2_500), p.Entries[0].AmountCp,
		"2000 of balance plus 500 of permitted debt is what is left to take")
	require.Equal(t, core.Centipoints(-500), requireNonNegativeFloor(t, p),
		"the pool's floor reaches the proposal, and the engine re-checks it against the balance at "+
			"the commit head")
}

// TestDecayWindow_PlanDecay_AnExpiryNeverExpiresAgain is the defect this strategy is shaped around,
// as an example. The randomised half is TestProperty_DecayWindow_AnEarningExpiresAtMostOnce.
//
// Run 1 expires week 1's earnings and posts a DEBIT. That debit ages into a later slice — and must
// count for nothing there, because it is not an earning. A planner that took the balance delta over
// the slice instead would net the debit against week 3's earnings, expire nothing, and leave a
// balance that ratchets upward with no row to point at.
//
// The bank's credit is in that slice too, and it is skipped for a different reason: a system account
// holds no earnings that age, and expiring the counterparty would expire the other side of the batch.
func TestDecayWindow_PlanDecay_AnExpiryNeverExpiresAgain(t *testing.T) {
	t.Parallel()

	ctx := newLedgerCtx(t, 1, `{"window_days": 90}`)
	ctx.credit(1, acct(0), 10_000) // week 1
	ctx.credit(2, acct(0), 20_000) // week 2
	ctx.credit(3, acct(0), 30_000) // week 3

	first, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 3, Window: aWindow(0, 1), EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-10_000), first.Entries[0].AmountCp)

	ctx.commit(4, first)

	// The next period expires week 2 and nothing else — the balance is 50_000 and the slice is 20_000.
	second, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W32", AsOfSeq: 4, Window: aWindow(1, 2), EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-20_000), second.Entries[0].AmountCp)

	ctx.commit(5, second)

	// And the period whose slice CONTAINS the first expiry batch expires nothing at all: the batch is
	// a debit for the member and a credit for the bank, and neither is an earning this run may take.
	_, err = strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W33", AsOfSeq: 5, Window: aWindow(3, 4), EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan,
		"an expiry is a debit, so it can never appear in a later slice — and the bank's half of it is "+
			"a system account's, which no run touches")

	balance, err := ctx.Balance(acct(0), strategy.BalanceKindDKP, 5)
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(30_000), balance,
		"weeks 1 and 2 expired exactly once each, leaving week 3")
}

// TestDecayWindow_PlanDecay_ASecondRunForThePeriodPlansTheSameBatch is the idempotency example: the
// same run, planned again after its batch committed, is the same batch byte for byte, so the
// (pool_id, kind, cadence_period) key collapses the two into one.
func TestDecayWindow_PlanDecay_ASecondRunForThePeriodPlansTheSameBatch(t *testing.T) {
	t.Parallel()

	ctx := newLedgerCtx(t, 2, `{"window_days": 90}`)
	ctx.credit(1, acct(0), 10_000)
	ctx.credit(1, acct(1), 4_500)
	ctx.credit(2, acct(0), 20_000)

	run := strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 2, Window: aWindow(0, 1), EffectiveAt: fixedNow,
	}

	first, err := strategy.DecayWindow{}.PlanDecay(ctx, run)
	require.NoError(t, err)

	ctx.commit(3, first)

	second, err := strategy.DecayWindow{}.PlanDecay(ctx, run)
	require.NoError(t, err)

	require.Equal(t, canonicalOf(t, first), canonicalOf(t, second),
		"every read a window run makes is positional, so a retry proposes the batch that already "+
			"committed rather than expiring the slice a second time")
}

// TestDecayWindow_PlanDecay_UsesTheRosterAndSkipsSystemAccounts covers the façade read the run falls
// back to when it names no accounts.
func TestDecayWindow_PlanDecay_UsesTheRosterAndSkipsSystemAccounts(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 50_000, `{"window_days": 90}`)
	ctx.balances[ledger.AccountIDGuildBank] = 90_000
	ctx.earned[acct(0)] = 5_000
	ctx.earned[acct(1)] = 5_000
	ctx.earned[ledger.AccountIDGuildBank] = 90_000

	p, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 9, Window: aWindow(2, 5), EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "two raiders expired, one credit to the bank")
	require.Equal(t, core.Centipoints(-5_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(-5_000), p.Entries[1].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(10_000), p.Entries[2].AmountCp,
		"the bank receives the expired points and is never itself expired")
}

// TestDecayWindow_PlanDecay_RejectsAWindowThisPoolCannotHaveProduced is the scheduler's half of the
// contract, and every row is a run that would otherwise be written into an append-only table.
func TestDecayWindow_PlanDecay_RejectsAWindowThisPoolCannotHaveProduced(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		window *strategy.ExpiryWindow
		want   string
	}{
		{
			name:   "no window at all",
			window: nil,
			want:   "carries no expiry window",
		},
		{
			name:   "resolved from a config no longer in force",
			window: &strategy.ExpiryWindow{Days: 30, FromSeq: 2, ToSeq: 5},
			want:   "resolved from a 30-day window",
		},
		{
			name:   "a negative bound",
			window: &strategy.ExpiryWindow{Days: 90, FromSeq: -1, ToSeq: 5},
			want:   "negative bound",
		},
		{
			name:   "a slice that runs backwards",
			window: &strategy.ExpiryWindow{Days: 90, FromSeq: 6, ToSeq: 5},
			want:   "runs backwards",
		},
		{
			name:   "a slice reaching past the run's snapshot",
			window: &strategy.ExpiryWindow{Days: 90, FromSeq: 2, ToSeq: 10},
			want:   "past the run's as-of seq",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.DecayWindow{}.PlanDecay(
				newCtx(t, 2, 10_000, `{"window_days": 90}`),
				strategy.DecayRun{
					PeriodKey:   "2026-W31",
					AsOfSeq:     9,
					Window:      tc.window,
					EffectiveAt: fixedNow,
				})
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestDecayWindow_PlanDecay_AnEmptySliceIsLegalAndExpiresNothing: a pool that raided nothing in the
// week that aged out has a run to record and no batch to write, which is the `skipped` run state
// rather than an error (.claude/rules/decay-and-jobs.md §4).
func TestDecayWindow_PlanDecay_AnEmptySliceIsLegalAndExpiresNothing(t *testing.T) {
	t.Parallel()

	_, err := strategy.DecayWindow{}.PlanDecay(
		newCtx(t, 2, 10_000, `{"window_days": 90}`),
		strategy.DecayRun{
			PeriodKey:   "2026-W31",
			AsOfSeq:     9,
			Window:      &strategy.ExpiryWindow{Days: 90, FromSeq: 5, ToSeq: 5},
			EffectiveAt: fixedNow,
		})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan)
	require.ErrorContains(t, err, "2026-W31")
}

// --- Adjustment, reversal, spendable ---------------------------------------------------------------

// TestDecayWindow_PlanAdjustment_MovesPointsAgainstACounterparty covers the one planner every strategy
// shares, and the pool's floor reaching the proposal.
func TestDecayWindow_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"window_days": 90, "floor_cp": -500}`)

	p, err := strategy.DecayWindow{}.PlanAdjustment(ctx, strategy.AdjustmentEvent{
		Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
		AmountCp:    5_000,
		EffectiveAt: fixedNow,
		Reason:      "missed three raids' worth of ticks in April",
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(5_000), p.Entries[0].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(-500), requireNonNegativeFloor(t, p))
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestDecayWindow_PlanReversal_RestoresThePointsAndTheyStayBack: the restored points are themselves a
// credit in the slice the reversal lands in, so they age out one window later rather than being
// re-expired by the next run.
func TestDecayWindow_PlanReversal_RestoresThePointsAndTheyStayBack(t *testing.T) {
	t.Parallel()

	ctx := newLedgerCtx(t, 1, `{"window_days": 90}`)
	ctx.credit(1, acct(0), 10_000)

	expiry, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     1,
		Window:      aWindow(0, 1),
		EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
	})
	require.NoError(t, err)

	ctx.commit(2, expiry)

	reversal, err := strategy.DecayWindow{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            expiry.Kind,
		StrategyID:      expiry.StrategyID,
		StrategyVersion: expiry.StrategyVersion,
		EffectiveAt:     expiry.EffectiveAt,
		Entries:         expiry.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.Equal(t, core.Centipoints(10_000), reversal.Entries[0].AmountCp)
	require.Equal(t, fixedNow, reversal.EffectiveAt)
	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(reversal),
		"a reversal declares no floor: a floor on one does not prevent a debt, it prevents the "+
			"correction")

	ctx.commit(3, reversal)

	// The slice that already expired is spent: re-running it takes nothing, because the earning it
	// held is gone and the restored points are an earning of seq 3.
	_, err = strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 3, Window: aWindow(0, 1), EffectiveAt: fixedNow,
	})
	require.NoError(t, err, "the slice's earning is still in the log, so the run still plans it")

	restored, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W40", AsOfSeq: 3, Window: aWindow(2, 3), EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-10_000), restored.Entries[0].AmountCp,
		"the restored points age from the day they were restored, which is what stops the next run "+
			"from silently undoing the officer's correction")
}

// TestDecayWindow_Spendable_IsThePlainSum is the rule this whole design exists to keep: expiry is
// posted, so a member's balance is the total of their own statement and nothing filters it by age.
func TestDecayWindow_Spendable_IsThePlainSum(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"window_days": 90}`)
	ctx.balances[acct(0)] = 12_345
	ctx.earned[acct(0)] = 99_999

	got, err := strategy.DecayWindow{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(12_345), got)

	rank, err := strategy.DecayWindow{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(12_345), rank.Rank)
	require.Equal(t, acct(0).String(), rank.Tiebreak)
}

// TestDecayWindow_UnsupportedOperations_RefuseAndNameTheStrategy covers the five methods this strategy
// declines.
func TestDecayWindow_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"window_days": 90}`)
	s := strategy.DecayWindow{}

	_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(1)})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "decay_window")

	_, err = s.PlanAward(ctx, strategy.AwardEvent{
		Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:  strategy.ItemRef{Name: "Cloak of Flames"},
	})
	require.ErrorIs(t, err, strategy.ErrUnsupported)

	hint, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Nil(t, hint)

	require.ErrorIs(t, s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)},
		strategy.Bid{AccountID: acct(0), AmountCp: 100}), strategy.ErrUnsupported)

	resolution, err := s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 7}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Empty(t, resolution.Winners)
}

// TestDecayWindow_Identity_IsStableAndDeclared covers the values written onto every batch.
func TestDecayWindow_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.DecayWindow{}

	require.Equal(t, "decay_window", s.ID())
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, strategy.RuleOverTime, s.RuleKind())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants())

	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0], "ConfigSchema hands out a copy")
}

// --- Rejections ------------------------------------------------------------------------------------

// TestDecayWindow_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
func TestDecayWindow_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.DecayWindow{}
	live := `{"window_days": 90}`

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "a run with no period key",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{AsOfSeq: 9, Window: aWindow(2, 5)})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a run naming an account twice",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey: "2026-W31",
					AsOfSeq:   9,
					Window:    aWindow(2, 5),
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
			name:   "a run in which nobody earned anything that aged out",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey: "2026-W31", AsOfSeq: 9, Window: aWindow(2, 5),
				})

				return err
			},
			wantErr: strategy.ErrNothingToPlan,
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
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID: acct(70), StrategyID: "decay_window",
				})

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
					StrategyID: "decay_percent",
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

// TestDecayWindow_PlanDecay_TotalOverflow_IsRefused covers the accumulator running out of int64: every
// individual expiry fits and the total credited back to the bank does not.
func TestDecayWindow_PlanDecay_TotalOverflow_IsRefused(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"window_days": 90}`)

	const nearlyHalf = core.Centipoints(4_600_000_000_000_000_000)

	for i := range 3 {
		ctx.balances[acct(i)] = nearlyHalf
		ctx.earned[acct(i)] = nearlyHalf
	}

	_, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 9, Window: aWindow(2, 5), EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "2026-W31")
}

// TestDecayWindow_PlanDecay_UnrepresentableRoom_IsRefused: a balance at the top of int64 against a
// floor at the bottom has a room that does not fit, and the refusal names the account because the
// roster is what an officer would have to look at.
func TestDecayWindow_PlanDecay_UnrepresentableRoom_IsRefused(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"window_days": 90, "floor_cp": -9223372036854775808}`)
	ctx.balances[acct(0)] = 9_223_372_036_854_775_807
	ctx.earned[acct(0)] = 1_000

	_, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 9, Window: aWindow(2, 5), EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, acct(0).String())
}

// TestDecayWindow_Config_RejectsWhatTheSchemaWouldHaveRejected covers the strict decode and the window
// that parses and then means "expire everything".
func TestDecayWindow_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`{"window_days": 90}{"window_days": 30}`,
		`{"window_days": null}`,
		`{"window_days": 90.5}`,
		`{"window_days": "90"}`,
		`{"window_dyas": 90}`,
		`{"window_days": -1}`,
		`{"window_days": 3651}`,

		// A window of no days would make every earning older than the window.
		`{}`,
		`{"window_days": 0}`,
		`{"floor_cp": -500}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyDecayWindowPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 1, 1_000, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// TestDecayWindow_Config_NamesTheKnobThatIsWrong: "invalid config" sends an officer to read the whole
// form.
func TestDecayWindow_Config_NamesTheKnobThatIsWrong(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ config, want string }{
		{`{}`, "window_days is 0"},
		{`{"window_days": -1}`, "window_days is -1"},
		{`{"window_days": 3651}`, "window_days is 3651, want 1..3650"},
		{`{"window_dyas": 90}`, "window_dyas"},
		{`{"window_days": null}`, "window_days"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.DecayWindow{}.PlanDecay(
				newCtx(t, 1, 1_000, tc.config),
				strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 9, Window: aWindow(2, 5)})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// everyDecayWindowPlanner returns one minimal, otherwise-legal call per planner that reads the pool's
// config.
func everyDecayWindowPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.DecayWindow{}

	return map[string]func(ctx strategy.Ctx) error{
		"window run": func(ctx strategy.Ctx) error {
			_, err := s.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: "2026-W31",
				AsOfSeq:   9,
				Window:    aWindow(2, 5),
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

// TestDecayWindow_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases from the schema.
func TestDecayWindow_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.DecayWindow{}.ConfigSchema(),
		map[string]string{
			// The floor needs a window to belong to: a pool that expires nothing is refused.
			"floor_cp": `{"window_days":90,"floor_cp":0}`,
		},
		func(t *testing.T, config string) error {
			t.Helper()

			_, err := strategy.DecayWindow{}.PlanAdjustment(
				newCtx(t, 1, 0, config), strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(0)}, AmountCp: 10,
				})

			return err
		})
}

// TestDecayWindow_ConfigSchema_DeclaresNoNumber restates canonical §1 where a schema could break it.
func TestDecayWindow_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.DecayWindow{}.ConfigSchema())
}

// TestDecayWindow_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan rather
// than producing a batch built on a zero.
//
// The EarnedBetween case is the one that matters most: a read that returned (0, err) and a planner
// that ignored it would expire nobody, which is indistinguishable from a period in which nothing aged
// out.
func TestDecayWindow_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.DecayWindow{}
	boom := fmt.Errorf("the read pool is closed")
	live := `{"window_days": 90}`
	run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 9, Window: aWindow(2, 5)}

	t.Run("earned", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, live)
		ctx.earnedErr = boom

		_, err := s.PlanDecay(ctx, run)
		require.ErrorIs(t, err, boom)
	})

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, live)
		ctx.balanceErr = boom
		ctx.earned[acct(0)] = 500
		ctx.earned[acct(1)] = 500

		_, err := s.PlanDecay(ctx, run)
		require.ErrorIs(t, err, boom)

		_, err = s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})

	t.Run("roster", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, live)
		ctx.rosterErr = boom

		_, err := s.PlanDecay(ctx, run)
		require.ErrorIs(t, err, boom)
	})

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyDecayWindowPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 1_000, live)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})
}

// --- Declaration and the goldens --------------------------------------------------------------------

// decayWindowGoldenConfig sets every knob to a non-default value, so a knob that stopped being read
// shows up as a changed golden rather than as nothing.
const decayWindowGoldenConfig = `{"window_days":90,"floor_cp":-500}`

// decayWindowGoldenCtx is the façade every decay_window golden is planned against: one member whose
// aged-out earnings are all still on the books, one who has spent most of theirs and is clamped, and
// one who earned nothing in the slice — the three outcomes in one batch.
func decayWindowGoldenCtx(tb testing.TB) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, 3, 0, decayWindowGoldenConfig)
	ctx.balances[acct(0)] = 43_000
	ctx.earned[acct(0)] = 31_000
	ctx.balances[acct(1)] = 2_000
	ctx.earned[acct(1)] = 30_000
	ctx.balances[acct(2)] = 5_000
	ctx.earned[acct(2)] = 0

	return ctx
}

// decayWindowGoldenCases is one case per planner decay_window supports.
func decayWindowGoldenCases() []goldenCase {
	s := strategy.DecayWindow{}

	return []goldenCase{
		{
			name: "decay",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanDecay(decayWindowGoldenCtx(tb), strategy.DecayRun{
					PeriodKey:   "2026-W31",
					AsOfSeq:     9,
					Window:      &strategy.ExpiryWindow{Days: 90, FromSeq: 2, ToSeq: 5},
					EffectiveAt: fixedNow,
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(decayWindowGoldenCtx(tb), strategy.AdjustmentEvent{
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
				ctx := decayWindowGoldenCtx(tb)

				original, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey:   "2026-W31",
					AsOfSeq:     9,
					Window:      &strategy.ExpiryWindow{Days: 90, FromSeq: 2, ToSeq: 5},
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

// TestDecayWindow_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestDecayWindow_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, decayWindowGoldenDir, decayWindowGoldenCases())
}

// TestDecayWindow_Goldens_CoverEveryPlanner is the anti-drift half.
func TestDecayWindow_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, decayWindowGoldenDir, decayWindowGoldenCases(),
		[]string{"adjustment", "decay", "reversal"})
}

// TestDecayWindow_EveryPlannerInvariant_IsDeclared keeps the catalogue and the per-proposal sets in
// step.
func TestDecayWindow_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.DecayWindow{}, plannedProposals(t, decayWindowGoldenCases()))
}

// TestDecayWindow_Planners_ConsumeNoRandomness: a seed on a batch asserts that replaying from it
// reproduces the plan, and this strategy's only ordering is the account id.
func TestDecayWindow_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := decayWindowGoldenCtx(t)

	for _, p := range plannedProposals(t, decayWindowGoldenCases()) {
		require.Nil(t, p.RngSeed, "%s carries a seed it never consumed", p.Kind)
	}

	_, err := strategy.DecayWindow{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     9,
		Window:      &strategy.ExpiryWindow{Days: 90, FromSeq: 2, ToSeq: 5},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Zero(t, ctx.rng.calls, "decay_window must consume no randomness")
}
