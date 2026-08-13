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

// start_points, tested at the strategy level. Phase 1, #193.
//
// Nearly every test here is about ONE distinction: an account with a zero balance and an account with
// no ledger history are different things, and only the second is a new member. Conflating them is the
// "everyone got 1000 points again" ticket (property P7), and it is the only way this strategy can be
// wrong in a way that matters.

// startPointsGoldenDir is where start_points' canonical proposals live.
const startPointsGoldenDir = "../../test/golden/strategy/start_points"

// --- The grant run ------------------------------------------------------------------------------

// TestStartPoints_PlanDecay_GrantsOnlyAccountsWithNoLedgerHistory is the strategy in one test.
func TestStartPoints_PlanDecay_GrantsOnlyAccountsWithNoLedgerHistory(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"grant_cp": 20000}`)
	ctx.history[acct(1)] = true // an existing raider

	p, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     4,
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, "start_points", p.Kind,
		"the batch names the rule that credited the member even though the run row and the planner "+
			"are shared with decay and cap")
	require.Equal(t, "start points 2026-W31", p.Reason)
	require.Len(t, p.Entries, 3, "the bank's debit plus the two recruits")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-40_000), p.Entries[0].AmountCp,
		"the points come from the bank rather than being minted, so the batch sums to zero")
	require.Equal(t, acct(0), p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(20_000), p.Entries[1].AmountCp)
	require.Equal(t, acct(2), p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(p),
		"nobody's balance decreases but the bank's, and the bank is exempt from floors, so "+
			"NonNegative would constrain nothing")
}

// TestStartPoints_PlanDecay_AZeroBalanceIsNotAnEmptyHistory is the regression test for the defect this
// strategy exists to avoid.
//
// The veteran earned eight hundred points and spent every one of them: balance zero, four years of
// statement. The recruit was created this morning: balance zero, nothing. A planner that tested the
// BALANCE would grant to both, and the guild would discover it from the standings page.
func TestStartPoints_PlanDecay_AZeroBalanceIsNotAnEmptyHistory(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"grant_cp": 20000}`)
	ctx.balances[acct(0)] = 0 // the veteran, spent out
	ctx.history[acct(0)] = true
	ctx.balances[acct(1)] = 0 // the recruit
	ctx.history[acct(1)] = false

	p, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 2, "the bank and the recruit, and nobody else")
	require.Equal(t, acct(1), p.Entries[1].AccountID,
		"the spent-out veteran has history and must never be granted again; a balance cannot tell "+
			"you that, which is why Ctx.HasHistory exists")
}

// TestStartPoints_PlanDecay_ASecondRunGrantsNothing is property P7 as an example: the grant itself is
// history, so the next run skips the account it just credited.
func TestStartPoints_PlanDecay_ASecondRunGrantsNothing(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"grant_cp": 20000}`)
	run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow}

	first, err := strategy.StartPoints{}.PlanDecay(ctx, run)
	require.NoError(t, err)
	require.Len(t, first.Entries, 4)

	// Commit it, exactly as the ledger would: every credited account now has an entry.
	for _, e := range first.Entries {
		ctx.balances[e.AccountID] += e.AmountCp
		ctx.history[e.AccountID] = true
	}

	_, err = strategy.StartPoints{}.PlanDecay(ctx, run)
	require.ErrorIs(t, err, strategy.ErrNothingToPlan,
		"every account has history now, so there is no batch to write and the job records the run "+
			"as skipped rather than granting a second opening balance")

	// And a period nobody has used yet reaches the same answer, which is the layer of idempotency
	// the unique index on (pool_id, kind, cadence_period) cannot provide.
	_, err = strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W32", AsOfSeq: 9, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan)
}

// TestStartPoints_PlanDecay_ReadsHistoryAtTheRunSeq: eligibility is positional, exactly like a
// balance. A grant committed while this run is planning must not change what this run decided.
func TestStartPoints_PlanDecay_ReadsHistoryAtTheRunSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"grant_cp": 20000}`)

	_, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 11, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.NotEmpty(t, ctx.readAtSeq)

	for _, seq := range ctx.readAtSeq {
		require.Equal(t, int64(11), seq,
			"eligibility must be read AT THE RUN'S SEQ; reading the head would let a batch "+
				"committed mid-run change who the run granted")
	}
}

// TestStartPoints_PlanDecay_UsesTheRosterAndSkipsSystemAccounts covers the façade read the run falls
// back to. The guild bank funding its own opening balance is a batch that means nothing.
func TestStartPoints_PlanDecay_UsesTheRosterAndSkipsSystemAccounts(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"grant_cp": 20000}`)

	p, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "two recruits from the roster, plus the bank")

	for _, e := range p.Entries[1:] {
		require.NotEqual(t, ledger.AccountIDGuildBank, e.AccountID)
		require.NotEqual(t, ledger.AccountIDResidue, e.AccountID)
		require.NotEqual(t, ledger.AccountIDWriteOff, e.AccountID)
		require.NotEqual(t, ledger.AccountIDImportOpening, e.AccountID)
	}
}

// --- Adjustment, reversal, spendable ------------------------------------------------------------

// TestStartPoints_PlanAdjustment_MovesPointsAgainstACounterparty asserts an adjustment is two entries
// and never one, and that it carries the pool's floor.
func TestStartPoints_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	p, err := strategy.StartPoints{}.PlanAdjustment(
		newCtx(t, 2, 1_000, `{"grant_cp": 20000, "floor_cp": -500}`), strategy.AdjustmentEvent{
			Account:      strategy.AccountRef{ID: acct(0), Kind: "person"},
			AmountCp:     250,
			Counterparty: acct(1),
			EffectiveAt:  fixedNow,
			Reason:       "recruit joined mid-week",
		})
	require.NoError(t, err)

	require.Equal(t, "adjustment", p.Kind)
	require.Equal(t, core.Centipoints(250), p.Entries[0].AmountCp)
	require.Equal(t, acct(1), p.Entries[1].AccountID, "a named counterparty is used as given")
	require.Equal(t, core.Centipoints(-250), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
	require.Equal(t, core.Centipoints(-500), requireNonNegativeFloor(t, p))
}

// TestStartPoints_PlanReversal_DoesNotReArmTheGrant is the subtle half of the reversal contract.
//
// Reversing a grant leaves the account with entries, so it still has history and the next run still
// skips it. That is the correct behaviour: an officer who reverses a grant in order to re-issue it at
// a different amount posts the new amount as an adjustment, which carries a reason.
func TestStartPoints_PlanReversal_DoesNotReArmTheGrant(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"grant_cp": 20000}`)

	grant, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     4,
		EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
	})
	require.NoError(t, err)

	reversal, err := strategy.StartPoints{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            grant.Kind,
		StrategyID:      grant.StrategyID,
		StrategyVersion: grant.StrategyVersion,
		EffectiveAt:     grant.EffectiveAt,
		Entries:         grant.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.Equal(t, fixedNow, reversal.EffectiveAt,
		"a reversal is a new economic event at the time it is decided")
	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(reversal),
		"a floor on a reversal stops the correction, not the debt")

	for i, e := range reversal.Entries {
		require.Equal(t, -grant.Entries[i].AmountCp, e.AmountCp)
	}

	// Both batches are history, so the account stays ineligible.
	for _, e := range append(grant.Entries, reversal.Entries...) {
		ctx.history[e.AccountID] = true
	}

	_, err = strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W32", AsOfSeq: 9, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan,
		"a reversed grant is still history: re-running must not re-issue it")
}

// TestStartPoints_Spendable_ReadsTheHeadSeq: a plain SUM at the pool head, and the same rank shape
// every quantity strategy uses.
func TestStartPoints_Spendable_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 2_000, `{"grant_cp": 20000}`)

	got, err := strategy.StartPoints{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(2_000), got)
	require.Equal(t, []int64{7}, ctx.readAtSeq)

	rank, err := strategy.StartPoints{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(2_000), rank.Rank)
	require.Equal(t, "spendable balance", rank.Reason)
}

// TestStartPoints_UnsupportedOperations_RefuseAndNameTheStrategy covers the five methods this
// strategy declines. A grant is not a tick and it is not a price.
func TestStartPoints_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"grant_cp": 20000}`)
	s := strategy.StartPoints{}

	_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(1)})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "tick", "the refusal points at what to pair it with")

	_, err = s.PlanAward(ctx, strategy.AwardEvent{
		Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:  strategy.ItemRef{Name: "Cloak of Flames"},
	})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "start_points")

	hint, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Nil(t, hint)

	require.ErrorIs(t, s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)},
		strategy.Bid{AccountID: acct(0), AmountCp: 100}), strategy.ErrUnsupported)

	resolution, err := s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 7}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Empty(t, resolution.Winners)
}

// TestStartPoints_Identity_IsStableAndDeclared covers the three values written onto every batch.
func TestStartPoints_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.StartPoints{}

	require.Equal(t, "start_points", s.ID())
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants())

	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0])
}

// --- Rejections ---------------------------------------------------------------------------------

// TestStartPoints_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
func TestStartPoints_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.StartPoints{}
	live := `{"grant_cp": 20000}`

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "a run against a pool that grants nothing",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31"})

				return err
			},
			wantErr: strategy.ErrInvalidConfig,
		},
		{
			name:   "a run with no period key",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{})

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
			name:   "a run naming only system accounts",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey: "2026-W31",
					AsOfSeq:   7,
					Accounts: []strategy.AccountRef{
						{ID: ledger.AccountIDGuildBank, Kind: "system", SystemKey: "guild_bank"},
					},
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
			name:   "adjustment of zero",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(0)},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a reversal of an empty batch",
			config: live,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID: acct(70), StrategyID: "start_points",
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
					StrategyID: "tick",
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

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 0, tc.config)), tc.wantErr)
		})
	}
}

// TestStartPoints_PlanDecay_TotalOverflow_IsRefused covers the grant accumulator running out of
// int64: every individual grant fits and the total credited from the bank does not.
func TestStartPoints_PlanDecay_TotalOverflow_IsRefused(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, fmt.Sprintf(`{"grant_cp": %d}`, math.MaxInt64/2))

	_, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "2026-W31")
}

// TestStartPoints_Config_RejectsWhatTheSchemaWouldHaveRejected covers the strict decode.
func TestStartPoints_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`"start_points"`,
		`{"grant_cp": 100}{"grant_cp": 200}`,
		`{"grant_cp": null}`,
		`{"floor_cp": null}`,
		`{"grant_cp": 1.5}`,
		`{"grant_cp": "20000"}`,
		`{"grant_pc": 20000}`,
		`{"grant_cp": -1}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyStartPointsPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 1, 0, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}

	t.Run("a transposed knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.StartPoints{}.PlanDecay(
			newCtx(t, 1, 0, `{"grant_pc": 20000}`),
			strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 7})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "grant_pc")
	})

	t.Run("a negative grant names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.StartPoints{}.PlanDecay(
			newCtx(t, 1, 0, `{"grant_cp": -1}`),
			strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 7})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "adjustment, not a grant",
			"an opening balance that takes points away is an adjustment, and should carry a reason")
	})
}

// everyStartPointsPlanner returns one minimal, otherwise-legal call per planner that reads the pool's
// config.
func everyStartPointsPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.StartPoints{}

	return map[string]func(ctx strategy.Ctx) error{
		"grant run": func(ctx strategy.Ctx) error {
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

// TestStartPoints_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases from the schema.
func TestStartPoints_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.StartPoints{}.ConfigSchema(),
		map[string]string{
			// The floor is legal on its own, but a grant of nothing is refused by the RUN rather than
			// by the parser — so the document names both knobs and the planner has something to do.
			"floor_cp": `{"grant_cp":20000,"floor_cp":0}`,
		},
		func(t *testing.T, config string) error {
			t.Helper()

			_, err := strategy.StartPoints{}.PlanDecay(
				newCtx(t, 1, 0, config), strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 7})

			return err
		})
}

// TestStartPoints_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan.
//
// The HISTORY read is the one that matters here: a HasHistory that returned (false, err) and a
// planner that ignored the error would grant an opening balance to the entire roster, which is
// precisely the ticket this strategy is designed around.
func TestStartPoints_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.StartPoints{}
	boom := fmt.Errorf("the read pool is closed")
	live := `{"grant_cp": 20000}`

	t.Run("history", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 0, live)
		ctx.historyErr = boom

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 3})
		require.ErrorIs(t, err, boom)
	})

	t.Run("roster", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 0, live)
		ctx.rosterErr = boom

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31"})
		require.ErrorIs(t, err, boom)
	})

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 0, live)
		ctx.balanceErr = boom

		_, err := s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyStartPointsPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 0, live)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})
}

// --- Declaration and the goldens ------------------------------------------------------------------

// startPointsGoldenConfig sets both knobs to non-default values.
const startPointsGoldenConfig = `{"grant_cp":20000,"floor_cp":-500}`

// startPointsGoldenCtx is the façade every start_points golden is planned against: three accounts, of
// which the middle one already has history, so the goldens exercise the skip as well as the grant.
func startPointsGoldenCtx(tb testing.TB) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, 3, 0, startPointsGoldenConfig)
	ctx.history[acct(1)] = true

	return ctx
}

// startPointsGoldenCases is one case per planner start_points supports.
func startPointsGoldenCases() []goldenCase {
	s := strategy.StartPoints{}

	return []goldenCase{
		{
			name: "start_points",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanDecay(startPointsGoldenCtx(tb), strategy.DecayRun{
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
				p, err := s.PlanAdjustment(startPointsGoldenCtx(tb), strategy.AdjustmentEvent{
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
				ctx := startPointsGoldenCtx(tb)

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

// TestStartPoints_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal.
func TestStartPoints_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, startPointsGoldenDir, startPointsGoldenCases())
}

// TestStartPoints_Goldens_CoverEveryPlanner is the anti-drift half.
func TestStartPoints_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, startPointsGoldenDir, startPointsGoldenCases(),
		[]string{"adjustment", "reversal", "start_points"})
}

// TestStartPoints_EveryPlannerInvariant_IsDeclared keeps the catalogue and the proposals in step.
func TestStartPoints_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.StartPoints{},
		plannedProposals(t, startPointsGoldenCases()))
}

// TestStartPoints_Planners_ConsumeNoRandomness: no seed is carried because none is consumed.
func TestStartPoints_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := startPointsGoldenCtx(t)

	for _, p := range plannedProposals(t, startPointsGoldenCases()) {
		require.Nil(t, p.RngSeed, "%s carries a seed it never consumed", p.Kind)
	}

	_, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Zero(t, ctx.rng.calls, "start_points must consume no randomness")
}

// TestStartPoints_ConfigSchema_DeclaresNoNumber restates canonical §1.
func TestStartPoints_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.StartPoints{}.ConfigSchema())
}
