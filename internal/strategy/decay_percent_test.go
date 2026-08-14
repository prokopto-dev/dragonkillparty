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

// decay_percent, tested at the strategy level. Phase 1, #194.
//
// The arithmetic tests carry the weight, because a percentage decay IS arithmetic: what a raider on
// 500.00 loses to a 10% run, what a member one centipoint above the floor loses, and what a debt
// does under each of the three policies. The idempotency claim is an example here and a property in
// decay_property_test.go — the example is what a reviewer reads, the property is what covers the
// balances nobody thought of.

// decayPercentGoldenDir is where decay_percent's canonical proposals live.
const decayPercentGoldenDir = "../../test/golden/strategy/decay_percent"

// --- The run --------------------------------------------------------------------------------------

// TestDecayPercent_PlanDecay_TakesTheRateAndFloorsIt is the guide's headline number: 10% of 500.00 is
// 50.00, posted as an explicit batch and credited to the bank.
//
// The third account is the rounding case and it matters more than it looks: 0.09 at 10% is 0.009,
// which FLOORS TO NOTHING and gets no entry at all. Rounding it up to a centipoint would take from
// the member with the least, every period, forever — and ledger_entry carries CHECK (amount_cp <> 0),
// so a zero entry is not even legal.
func TestDecayPercent_PlanDecay_TakesTheRateAndFloorsIt(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"decay_bp": 1000}`)
	ctx.balances[acct(0)] = 50_000
	ctx.balances[acct(1)] = 12_345
	ctx.balances[acct(2)] = 9

	p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     4,
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, "decay", p.Kind)
	require.Equal(t, "decay_percent", p.StrategyID)
	require.Equal(t, "decay 2026-W31", p.Reason)
	require.Len(t, p.Entries, 3, "two accounts decayed plus the bank; the third rounds to nothing")
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-5_000), p.Entries[0].AmountCp)
	require.Equal(t, acct(1), p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(-1_234), p.Entries[1].AmountCp,
		"1234.5 centipoints is FLOORED to 1234: rounding a decay up takes a centipoint the rate did "+
			"not ask for")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(6_234), p.Entries[2].AmountCp,
		"the points move to the bank rather than nowhere, so the batch sums to zero")
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestDecayPercent_PlanDecay_ReadsPositionallyAtTheRunSeq: every balance is read AT THE RUN'S SEQ.
//
// This is the whole of the idempotency argument. Reading the pool head instead would let a batch
// committed mid-run change what the run decayed, and would make a retry compound a second haircut
// onto the first one's result.
func TestDecayPercent_PlanDecay_ReadsPositionallyAtTheRunSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 50_000, `{"decay_bp": 1000}`)

	_, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.NotEmpty(t, ctx.readAtSeq)

	for _, seq := range ctx.readAtSeq {
		require.Equal(t, int64(4), seq, "a decay run reads at its own as-of seq, never at the head")
	}
}

// TestDecayPercent_PlanDecay_CompoundsAcrossPeriods reproduces the guide's four-week table exactly:
// 500.00 → 450.00 → 405.00 → 364.50 → 328.05 at 10% a week.
//
// Each period is planned against the balance the previous one LEFT, which is what
// `.claude/rules/decay-and-jobs.md` §6 requires of catch-up after downtime: oldest first, one batch
// per missed period, each carrying its own label.
func TestDecayPercent_PlanDecay_CompoundsAcrossPeriods(t *testing.T) {
	t.Parallel()

	ctx := newLedgerCtx(t, 1, `{"decay_bp": 1000}`)
	ctx.credit(1, acct(0), 50_000)

	seq := int64(1)

	for _, want := range []core.Centipoints{45_000, 40_500, 36_450, 32_805} {
		p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
			PeriodKey:   fmt.Sprintf("2026-W%02d", seq),
			AsOfSeq:     seq,
			EffectiveAt: fixedNow,
		})
		require.NoError(t, err)

		seq++
		ctx.commit(seq, p)

		got, err := ctx.Balance(acct(0), strategy.BalanceKindDKP, seq)
		require.NoError(t, err)
		require.Equal(t, want, got, "week %d of the guide's worked example", seq/2)
	}
}

// TestDecayPercent_PlanDecay_ASecondRunForThePeriodPlansTheSameBatch is the idempotency example: the
// same period, planned again after the first batch committed, is the SAME batch byte for byte.
//
// A percentage decay is not structurally idempotent the way a cap trim is — applying 10% twice takes
// 19% — so what makes the re-run a no-op is that both runs read the period's snapshot seq. The
// ledger's (pool_id, kind, cadence_period) key then collapses the two proposals into one committed
// batch. The randomised half is TestProperty_P9_DecayRuns_ASecondRunForThePeriodIsTheSameBatch.
func TestDecayPercent_PlanDecay_ASecondRunForThePeriodPlansTheSameBatch(t *testing.T) {
	t.Parallel()

	ctx := newLedgerCtx(t, 2, `{"decay_bp": 1000}`)
	ctx.credit(1, acct(0), 50_000)
	ctx.credit(1, acct(1), 12_345)

	run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 1, EffectiveAt: fixedNow}

	first, err := strategy.DecayPercent{}.PlanDecay(ctx, run)
	require.NoError(t, err)

	// The ledger commits it — and the job fires again, or an officer clicks "run decay now".
	ctx.commit(2, first)

	second, err := strategy.DecayPercent{}.PlanDecay(ctx, run)
	require.NoError(t, err)

	require.Equal(t, canonicalOf(t, first), canonicalOf(t, second),
		"a re-run of the same period must propose the same batch, so that the (pool_id, kind, "+
			"cadence_period) key makes it a no-op rather than a second haircut")

	// And the NEXT period, which reads the head the first batch left, decays what is actually there.
	next, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W32", AsOfSeq: 2, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-4_500), next.Entries[0].AmountCp,
		"the next period compounds on the balance the first run left: 10% of 450.00")
}

// TestDecayPercent_PlanDecay_StopsAtTheFloor covers the setting that stops a lapsed member decaying
// asymptotically forever — and the clamp that makes the run land ON the floor rather than crossing it.
//
// Crossing it would be worse than it sounds: NonNegative is declared, so the ledger would reject the
// whole batch and every other member's decay with it.
func TestDecayPercent_PlanDecay_StopsAtTheFloor(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 4, 0, `{"decay_bp": 1000, "floor_cp": 10000}`)
	ctx.balances[acct(0)] = 50_000 // well above the floor: the full rate
	ctx.balances[acct(1)] = 10_500 // 500 above it: the rate would take 1050, the floor allows 500
	ctx.balances[acct(2)] = 10_000 // exactly on it: nothing left to take
	ctx.balances[acct(3)] = 900    // below it already: untouched

	p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "two accounts above the floor plus the bank")
	require.Equal(t, core.Centipoints(-5_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(-500), p.Entries[1].AmountCp,
		"the amount is clamped so the balance lands exactly on the floor")
	require.Equal(t, core.Centipoints(10_000), requireNonNegativeFloor(t, p),
		"the pool's floor reaches the proposal, not the strategy catalogue's default")
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestDecayPercent_PlanDecay_NegativeBalances covers the three policies the guide says guilds argue
// about. Each is a different DKP system, which is why none of them is a default nobody chose.
func TestDecayPercent_PlanDecay_NegativeBalances(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		want   core.Centipoints
		why    string
	}{
		{
			name:   "skip leaves the debt alone",
			config: `{"decay_bp": 1000, "negative_balances": "skip"}`,
			want:   0,
			why:    "the default: nothing happens to a balance that is already below zero",
		},
		{
			name:   "toward_zero forgives it at the same rate",
			config: `{"decay_bp": 1000, "negative_balances": "toward_zero"}`,
			want:   500,
			why: "a CREDIT: -5000 at 10% becomes -4500, which is debt forgiveness and must be said " +
				"out loud in the guild's own rules",
		},
		{
			name:   "preserve_sign grows it",
			config: `{"decay_bp": 1000, "negative_balances": "preserve_sign", "floor_cp": -100000}`,
			want:   -500,
			why:    "the rule is about magnitude rather than direction: -5000 at 10% becomes -5500",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 1, 0, tc.config)
			ctx.balances[acct(0)] = -5_000

			p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
			})

			if tc.want == 0 {
				require.ErrorIs(t, err, strategy.ErrNothingToPlan, tc.why)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, p.Entries[0].AmountCp, tc.why)
			require.Equal(t, core.Centipoints(0), sumEntries(p))
		})
	}
}

// TestDecayPercent_PlanDecay_PreserveSign_StopsAtTheFloor: a growing debt stops where the guild said
// it stops, landing exactly on the floor rather than crossing it.
func TestDecayPercent_PlanDecay_PreserveSign_StopsAtTheFloor(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"decay_bp": 1000, "negative_balances": "preserve_sign", "floor_cp": -5200}`)
	ctx.balances[acct(0)] = -5_000 // the rate would take 500; the floor allows 200
	ctx.balances[acct(1)] = -5_200 // already on the floor

	p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 2, "one debt grew, one was already at the floor, plus the bank")
	require.Equal(t, core.Centipoints(-200), p.Entries[0].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
}

// TestDecayPercent_PlanDecay_PreserveSign_AtTheEndsOfInt64 pins the boundary the arithmetic relies on
// rather than assuming it.
//
// The room below a debt is `balance − floor` with both operands negative, so it is at most
// math.MaxInt64 — reached exactly here, by a balance of −1 against a floor at the bottom of the range.
// A debt at math.MinInt64 has no magnitude to apply a rate to at all, and `skip` is the policy that
// still has an answer for it: nothing.
func TestDecayPercent_PlanDecay_PreserveSign_AtTheEndsOfInt64(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0,
		`{"decay_bp": 10000, "negative_balances": "preserve_sign", "floor_cp": -9223372036854775808}`)
	ctx.balances[acct(0)] = -1

	p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(-1), p.Entries[0].AmountCp,
		"100%% of a debt of 1 centipoint, and the room below it is representable")

	skipped := newCtx(t, 1, 0, `{"decay_bp": 1000, "negative_balances": "skip"}`)
	skipped.balances[acct(0)] = math.MinInt64

	_, err = strategy.DecayPercent{}.PlanDecay(skipped, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrNothingToPlan,
		"a debt with no representable magnitude is still a debt `skip` leaves alone; only a policy "+
			"that has to apply a rate to it needs to refuse")
}

// TestDecayPercent_PlanDecay_UsesTheRosterAndSkipsSystemAccounts covers the façade read the run falls
// back to when it names no accounts.
//
// The guild bank is deliberately given a large balance. Decaying a system account would mean decaying
// the counterparty that makes the batch balance — and the bank is structurally negative by design,
// so under preserve_sign it would compound the debt that funds every tick.
func TestDecayPercent_PlanDecay_UsesTheRosterAndSkipsSystemAccounts(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 50_000, `{"decay_bp": 1000}`)
	ctx.balances[ledger.AccountIDGuildBank] = 90_000

	p, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "two raiders decayed, one credit to the bank")
	require.Equal(t, core.Centipoints(-5_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(-5_000), p.Entries[1].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(10_000), p.Entries[2].AmountCp,
		"the bank receives the decayed points; it is not itself decayed")
}

// --- Adjustment, reversal, spendable ---------------------------------------------------------------

// TestDecayPercent_PlanAdjustment_IsNotDecayed: an officer's correction reaches the ledger as they
// typed it, and the next run applies the rate to the corrected balance in a batch that says so.
func TestDecayPercent_PlanAdjustment_IsNotDecayed(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"decay_bp": 1000, "floor_cp": -500}`)

	p, err := strategy.DecayPercent{}.PlanAdjustment(ctx, strategy.AdjustmentEvent{
		Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
		AmountCp:    5_000,
		EffectiveAt: fixedNow,
		Reason:      "missed three raids' worth of ticks in April",
	})
	require.NoError(t, err)

	require.Equal(t, core.Centipoints(5_000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(-500), requireNonNegativeFloor(t, p))
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestDecayPercent_PlanReversal_GivesThePeriodBack: a reversal of a haircut restores the balance and
// declares no floor, because a floor on a reversal prevents the correction rather than the debt.
func TestDecayPercent_PlanReversal_GivesThePeriodBack(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 50_000, `{"decay_bp": 1000}`)

	decay, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     4,
		EffectiveAt: fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
	})
	require.NoError(t, err)

	reversal, err := strategy.DecayPercent{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            decay.Kind,
		StrategyID:      decay.StrategyID,
		StrategyVersion: decay.StrategyVersion,
		EffectiveAt:     decay.EffectiveAt,
		Entries:         decay.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.Equal(t, core.Centipoints(5_000), reversal.Entries[0].AmountCp)
	require.Equal(t, fixedNow, reversal.EffectiveAt,
		"a correction is a new economic event at the time it is decided, never backdated")
	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(reversal))
}

// TestDecayPercent_Spendable_IsThePlainSum is the rule the whole family exists to keep: decay is
// posted, so a decayed balance is already the sum. Applying the rate here would apply it twice, and
// the second application would be invisible in every statement.
func TestDecayPercent_Spendable_IsThePlainSum(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"decay_bp": 1000}`)
	ctx.balances[acct(0)] = 12_345

	got, err := strategy.DecayPercent{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(12_345), got)

	rank, err := strategy.DecayPercent{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(12_345), rank.Rank)
	require.Equal(t, acct(0).String(), rank.Tiebreak)
}

// TestDecayPercent_UnsupportedOperations_RefuseAndNameTheStrategy covers the five methods this
// strategy declines: it neither earns nor spends, and a refusal that names the rule is what tells an
// officer which slot to fill.
func TestDecayPercent_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{"decay_bp": 1000}`)
	s := strategy.DecayPercent{}

	_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(1)})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "decay_percent")
	require.ErrorContains(t, err, "tick", "and names the kind of rule to pair it with")

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

// TestDecayPercent_Identity_IsStableAndDeclared covers the values written onto every batch.
func TestDecayPercent_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.DecayPercent{}

	require.Equal(t, "decay_percent", s.ID())
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, strategy.RuleOverTime, s.RuleKind())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants())

	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0], "ConfigSchema hands out a copy")
}

// --- Rejections ------------------------------------------------------------------------------------

// TestDecayPercent_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
func TestDecayPercent_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.DecayPercent{}
	live := `{"decay_bp": 1000}`

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
				_, err := s.PlanDecay(ctx, strategy.DecayRun{AsOfSeq: 4})

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
					AsOfSeq:   4,
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
			name:   "a run in which every balance rounds to nothing",
			config: `{"decay_bp": 1}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4})

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
					ID: acct(70), StrategyID: "decay_percent",
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
					StrategyID: "decay_window",
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

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 100, tc.config)), tc.wantErr)
		})
	}
}

// TestDecayPercent_PlanDecay_TotalOverflow_IsRefused covers the accumulator running out of int64.
//
// Every individual balance fits and every individual haircut fits; it is the total credited back to
// the bank that does not. A wrapped total would land on the bank's entry, where the failure reads as
// "the batch sums to some enormous number" with no indication that the roster was the cause.
func TestDecayPercent_PlanDecay_TotalOverflow_IsRefused(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"decay_bp": 10000}`)

	const nearlyHalf = core.Centipoints(4_600_000_000_000_000_000)

	for i := range 3 {
		ctx.balances[acct(i)] = nearlyHalf
	}

	_, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "2026-W31")
}

// TestDecayPercent_PlanDecay_UnrepresentableBalances_AreRefused covers the two balances at the ends of
// int64, where the arithmetic has no answer rather than a wrong one.
//
// math.MinInt64 is the one value with no positive magnitude, so a debt at the bottom of the range
// cannot have a rate applied to it at all; and a balance at the top with a floor at the bottom is a
// room that does not fit. Both name the account, because the roster is what an officer would have to
// look at.
func TestDecayPercent_PlanDecay_UnrepresentableBalances_AreRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		config  string
		balance core.Centipoints
	}{
		{
			name:    "a debt with no representable magnitude",
			config:  `{"decay_bp": 1000, "negative_balances": "toward_zero"}`,
			balance: math.MinInt64,
		},
		{
			name:    "a balance whose room above the floor does not fit",
			config:  `{"decay_bp": 1000, "floor_cp": -9223372036854775808}`,
			balance: math.MaxInt64,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 1, 0, tc.config)
			ctx.balances[acct(0)] = tc.balance

			_, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow,
			})
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, acct(0).String())
		})
	}
}

// TestDecayPercent_Config_RejectsWhatTheSchemaWouldHaveRejected covers the strict decode and the two
// configurations that parse and then mean nothing.
func TestDecayPercent_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`{"decay_bp": 1000}{"decay_bp": 2000}`,
		`{"decay_bp": null}`,
		`{"decay_bp": 10.5}`,
		`{"decay_bp": "1000"}`,
		`{"decay_pb": 1000}`,
		`{"decay_bp": -1}`,
		`{"decay_bp": 10001}`,
		`{"decay_bp": 1000, "negative_balances": "forgive"}`,

		// The two that parse. A pool that decays nothing is a settings page saying "Decay" beside a
		// standings page that never changes; preserve_sign with a floor at or above zero is a policy
		// that can never apply.
		`{}`,
		`{"floor_cp": 100}`,
		`{"decay_bp": 1000, "negative_balances": "preserve_sign"}`,
		`{"decay_bp": 1000, "negative_balances": "preserve_sign", "floor_cp": 0}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyDecayPercentPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 1, 1_000, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// TestDecayPercent_Config_NamesTheKnobThatIsWrong: "invalid config" sends an officer to read the whole
// form.
func TestDecayPercent_Config_NamesTheKnobThatIsWrong(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ config, want string }{
		{`{}`, "decay_bp is 0"},
		{`{"decay_bp": 10001}`, "decay_bp is 10001, want 0..10000"},
		{`{"decay_bp": 1000, "negative_balances": "forgive"}`, `negative_balances is "forgive"`},
		{`{"decay_bp": 1000, "negative_balances": "preserve_sign"}`, "floor_cp 0"},
		{`{"decay_pb": 1000}`, "decay_pb"},
		{`{"decay_bp": null}`, "decay_bp"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			t.Parallel()

			_, err := strategy.DecayPercent{}.PlanDecay(
				newCtx(t, 1, 1_000, tc.config),
				strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// everyDecayPercentPlanner returns one minimal, otherwise-legal call per planner that reads the pool's
// config.
func everyDecayPercentPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.DecayPercent{}

	return map[string]func(ctx strategy.Ctx) error{
		"decay run": func(ctx strategy.Ctx) error {
			_, err := s.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: "2026-W31",
				AsOfSeq:   4,
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

// TestDecayPercent_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases from the schema, so a
// knob added later is covered without anybody remembering to add a row.
func TestDecayPercent_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.DecayPercent{}.ConfigSchema(),
		map[string]string{
			// Each needs a rate to be a legal document at all: a pool that decays nothing is refused,
			// which is the relationship validateDecayPercentConfig exists to enforce.
			"floor_cp":          `{"decay_bp":1000,"floor_cp":0}`,
			"negative_balances": `{"decay_bp":1000,"negative_balances":"toward_zero"}`,
		},
		func(t *testing.T, config string) error {
			t.Helper()

			_, err := strategy.DecayPercent{}.PlanAdjustment(
				newCtx(t, 1, 0, config), strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(0)}, AmountCp: 10,
				})

			return err
		})
}

// TestDecayPercent_ConfigSchema_DeclaresNoNumber restates canonical §1 where a schema could break it.
func TestDecayPercent_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.DecayPercent{}.ConfigSchema())
}

// TestDecayPercent_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan
// rather than producing a batch built on a zero — a Balance that returns (0, err) and a planner that
// ignored it would decay nobody, which looks exactly like a successful run.
func TestDecayPercent_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.DecayPercent{}
	boom := fmt.Errorf("the read pool is closed")
	live := `{"decay_bp": 1000}`

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, live)
		ctx.balanceErr = boom

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 3})
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

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 3})
		require.ErrorIs(t, err, boom)
	})

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyDecayPercentPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 50_000, live)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})
}

// --- Declaration and the goldens --------------------------------------------------------------------

// decayPercentGoldenConfig sets every knob to a non-default value, so a knob that stopped being read
// shows up as a changed golden rather than as nothing.
const decayPercentGoldenConfig = `{"decay_bp":1000,"floor_cp":-500,` +
	`"negative_balances":"preserve_sign"}`

// decayPercentGoldenCtx is the façade every decay_percent golden is planned against: one raider well
// above the floor, one in debt with room to fall, one whose balance rounds to nothing — so a single
// batch exercises the rate, the debt policy and the drop.
func decayPercentGoldenCtx(tb testing.TB) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, 3, 0, decayPercentGoldenConfig)
	ctx.balances[acct(0)] = 12_345
	ctx.balances[acct(1)] = -300
	ctx.balances[acct(2)] = 9

	return ctx
}

// decayPercentGoldenCases is one case per planner decay_percent supports.
func decayPercentGoldenCases() []goldenCase {
	s := strategy.DecayPercent{}

	return []goldenCase{
		{
			name: "decay",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanDecay(decayPercentGoldenCtx(tb), strategy.DecayRun{
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
				p, err := s.PlanAdjustment(decayPercentGoldenCtx(tb), strategy.AdjustmentEvent{
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
				ctx := decayPercentGoldenCtx(tb)

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

// TestDecayPercent_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestDecayPercent_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, decayPercentGoldenDir, decayPercentGoldenCases())
}

// TestDecayPercent_Goldens_CoverEveryPlanner is the anti-drift half.
func TestDecayPercent_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, decayPercentGoldenDir, decayPercentGoldenCases(),
		[]string{"adjustment", "decay", "reversal"})
}

// TestDecayPercent_EveryPlannerInvariant_IsDeclared keeps the catalogue and the per-proposal sets in
// step.
func TestDecayPercent_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.DecayPercent{},
		plannedProposals(t, decayPercentGoldenCases()))
}

// TestDecayPercent_Planners_ConsumeNoRandomness: a seed on a batch asserts that replaying from it
// reproduces the plan, and this strategy's only ordering is the account id.
func TestDecayPercent_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := decayPercentGoldenCtx(t)

	for _, p := range plannedProposals(t, decayPercentGoldenCases()) {
		require.Nil(t, p.RngSeed, "%s carries a seed it never consumed", p.Kind)
	}

	_, err := strategy.DecayPercent{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2026-W31", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)
	require.Zero(t, ctx.rng.calls, "decay_percent must consume no randomness")
}
