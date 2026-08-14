package strategy_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The three-rule pool, tested. Phase 1, ADR-0026 (#213).
//
// Two claims are worth separating, because only the second one is new. That `tick` credits correctly
// and `fixed_price` prices correctly is tick_test.go's and fixed_price_test.go's business. What is
// asserted here is the ROUTING: that a planner reaches the rule whose question it is, reads THAT
// rule's config and no other's, and refuses by name when the slot is empty — which is the whole of
// what a pool adds over a strategy.
//
// It uses fixed_price_test.go's fakeCtx, which is the package's one façade fake and reaches the real
// ledger for the three seams it cannot fake (Allocate, SystemAccount, Rng). The end-to-end half —
// planning through Rules and COMMITTING the result against real SQLite — is
// internal/ledger/pool_composition_test.go, because a strategy package may not import internal/store
// in a shipped file and the claim is about what the ledger accepts.

// The three configs the composed pool under test runs. Distinct numbers on purpose: a routing bug
// that handed the spend rule the earn rule's document would either fail to parse (the knobs differ)
// or price an item at the tick award, and both are visible in the assertions below.
const (
	tickRuleConfig       = `{"tick_award_cp": 1000}`
	fixedPriceRuleConfig = `{"default_price_cp": 5000, "proceeds": "guild_bank"}`
	capRuleConfig        = `{"hard_cap_cp": 100000}`
)

// composedPool is the worked example from docs/guides/choosing-a-dkp-system.md: earn with a tick,
// spend at a published price, keep the veterans from hoarding with a cap.
func composedPool(tb testing.TB) strategy.Rules {
	tb.Helper()

	rules, err := strategy.PoolConfig{
		EarnStrategyID:     "tick",
		EarnConfigJSON:     tickRuleConfig,
		SpendStrategyID:    "fixed_price",
		SpendConfigJSON:    fixedPriceRuleConfig,
		OverTimeStrategyID: "cap",
		OverTimeConfigJSON: capRuleConfig,
	}.Resolve()
	require.NoError(tb, err)

	return rules
}

// TestPoolConfig_Resolve_ComposesTheThreeRules is the happy path: three ids in, three planners out,
// each holding its own config document.
func TestPoolConfig_Resolve_ComposesTheThreeRules(t *testing.T) {
	t.Parallel()

	rules := composedPool(t)

	require.Equal(t, "tick", rules.Earn.Strategy.ID())
	require.Equal(t, tickRuleConfig, rules.Earn.ConfigJSON)
	require.Equal(t, "fixed_price", rules.Spend.Strategy.ID())
	require.Equal(t, fixedPriceRuleConfig, rules.Spend.ConfigJSON)
	require.Equal(t, "cap", rules.OverTime.Strategy.ID())
	require.Equal(t, capRuleConfig, rules.OverTime.ConfigJSON)
}

// TestPoolConfig_Resolve_EmptySlot_IsNotAnError: a pool part-way through setup, and a guild with no
// decay rule, are both legal states. The refusal belongs to the planner whose slot is empty, where
// the message can name what to configure.
func TestPoolConfig_Resolve_EmptySlot_IsNotAnError(t *testing.T) {
	t.Parallel()

	rules, err := strategy.PoolConfig{EarnStrategyID: "tick"}.Resolve()
	require.NoError(t, err)

	require.True(t, rules.Earn.IsSet())
	require.False(t, rules.Spend.IsSet())
	require.False(t, rules.OverTime.IsSet())

	empty, err := strategy.PoolConfig{}.Resolve()
	require.NoError(t, err, "a pool that has configured nothing is readable, just not yet usable")
	require.False(t, empty.Earn.IsSet())
}

// TestPoolConfig_Resolve_UnknownStrategy_IsRefused is the operator who downgraded across a release
// that added a strategy. The message must carry the SLOT as well as the id: "no such strategy" alone
// leaves an officer reading three dropdowns to find which one it meant.
func TestPoolConfig_Resolve_UnknownStrategy_IsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config strategy.PoolConfig
		slot   string
	}{
		// Each id is a strategy this binary does not have: `epgp` and `suicide_kings` are conditional
		// on a named pilot guild asking, and `auction_sealed` is Phase 3. `decay_window` stood here
		// until #194 shipped it — a placeholder that becomes real is the one way this test could go
		// quietly green, which is why the replacements are the two the rules mark as conditional
		// rather than as scheduled.
		{"earn", strategy.PoolConfig{EarnStrategyID: "epgp"}, "earn"},
		{"spend", strategy.PoolConfig{SpendStrategyID: "auction_sealed"}, "spend"},
		{"over_time", strategy.PoolConfig{OverTimeStrategyID: "suicide_kings"}, "over_time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.config.Resolve()
			require.ErrorIs(t, err, strategy.ErrUnknownStrategy)
			require.ErrorContains(t, err, tc.slot, "the refusal names the slot that holds the bad id")
		})
	}
}

// TestPoolConfig_Resolve_StrategyInTheWrongSlot_IsRefused is the mechanism RuleKind exists for.
//
// Every case here is a pool that would have SAVED CLEANLY under a singular strategy_id and then
// returned ErrUnsupported during loot or during a cadence run — at the moment the officer who could
// fix it is least able to. ErrWrongRuleKind moves that refusal to the settings form.
func TestPoolConfig_Resolve_StrategyInTheWrongSlot_IsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config strategy.PoolConfig
		wants  string
	}{
		{"an earn rule cannot spend", strategy.PoolConfig{SpendStrategyID: "tick"}, "earn"},
		{"an earn rule is not a cadence", strategy.PoolConfig{OverTimeStrategyID: "tick"}, "earn"},
		{"a spend rule does not earn", strategy.PoolConfig{EarnStrategyID: "fixed_price"}, "spend"},
		{"a cadence rule does not spend", strategy.PoolConfig{SpendStrategyID: "cap"}, "over_time"},
		{"a cadence grant is not a tick", strategy.PoolConfig{EarnStrategyID: "start_points"}, "over_time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.config.Resolve()
			require.ErrorIs(t, err, strategy.ErrWrongRuleKind)
			require.ErrorContains(t, err, tc.wants,
				"the refusal says which question the strategy DOES answer, so the fix is the message")
		})
	}
}

// TestPoolConfig_Resolve_ABadSlot_ResolvesNothing: Resolve is all-or-nothing.
//
// A partially resolved Rules would plan correctly until it reached the broken slot, which is the
// failure a configuration-time refusal exists to prevent — and the broken slot is by definition the
// one nobody exercised before the raid.
func TestPoolConfig_Resolve_ABadSlot_ResolvesNothing(t *testing.T) {
	t.Parallel()

	rules, err := strategy.PoolConfig{
		EarnStrategyID:  "tick",
		SpendStrategyID: "epgp",
	}.Resolve()

	require.Error(t, err)
	require.False(t, rules.Earn.IsSet(),
		"the good slot must not be resolved either: a half-built pool is the state this refuses")
}

// TestRules_BalanceKinds_AreTheUnionSorted. A pool writes every kind any of its rules moves, so a
// declaration naming one rule's kinds would be wrong about the other two.
func TestRules_BalanceKinds_AreTheUnionSorted(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{strategy.BalanceKindDKP}, composedPool(t).BalanceKinds(),
		"every shipped strategy moves `dkp`, so the union of three of them is one kind and not three")

	require.Empty(t, strategy.Rules{}.BalanceKinds(),
		"a pool with no rules moves nothing, and saying `dkp` anyway would be a declaration about a "+
			"pool that cannot write an entry")
}

// TestRules_EachPlanner_RoutesToItsOwnRule is the routing table, asserted end to end through real
// planners.
//
// It checks TWO things per planner and both matter. The proposal's StrategyID says which rule ran —
// that is what ledger_batch.strategy_id will carry. Its ConfigSnapshotJSON says which document that
// rule read — proposeZeroSum copies ctx.ConfigJSON() verbatim, so a routing bug that bound the wrong
// config is visible here even when the arithmetic happens to survive it.
func TestRules_EachPlanner_RoutesToItsOwnRule(t *testing.T) {
	t.Parallel()

	rules := composedPool(t)

	for _, tc := range []struct {
		name       string
		plan       func(strategy.Ctx) (strategy.BatchProposal, error)
		strategyID string
		config     string
	}{
		{
			name: "attendance goes to the earn rule",
			plan: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
				return rules.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees:   []strategy.Share{{AccountID: acct(0), Weight: 1}},
					EffectiveAt: fixedNow,
				})
			},
			strategyID: "tick",
			config:     tickRuleConfig,
		},
		{
			name: "an award goes to the spend rule",
			plan: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
				return rules.PlanAward(ctx, strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:        strategy.ItemRef{Name: "Cloak of Flames"},
					EffectiveAt: fixedNow,
				})
			},
			strategyID: "fixed_price",
			config:     fixedPriceRuleConfig,
		},
		{
			name: "a cadence run goes to the over-time rule",
			plan: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
				return rules.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey:   "2026-W31",
					AsOfSeq:     7,
					Accounts:    []strategy.AccountRef{{ID: acct(0), Kind: "person"}},
					EffectiveAt: fixedNow,
				})
			},
			strategyID: "cap",
			config:     capRuleConfig,
		},
		{
			name: "an adjustment goes to the earn rule",
			plan: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
				return rules.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
					AmountCp:    -500,
					EffectiveAt: fixedNow,
				})
			},
			strategyID: "tick",
			config:     tickRuleConfig,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A balance well above the cap's ceiling, so the cadence run has something to trim.
			ctx := newCtx(t, 3, 150_000, "THE POOL'S SINGULAR CONFIG, WHICH NO RULE MAY READ")

			p, err := tc.plan(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.strategyID, p.StrategyID,
				"ledger_batch.strategy_id records WHICH of the three rules planned the batch")
			require.Equal(t, tc.config, p.ConfigSnapshotJSON,
				"a rule reads its own config document and no other slot's")
		})
	}
}

// TestRules_EachPlanner_WithNoRule_RefusesByName is the empty-slot half, and it is why there are no
// fallbacks in this file.
//
// A pool that quietly asked its earn rule to decay would post batches nobody configured, against an
// append-only table. ErrNoRule is a different sentinel from ErrUnsupported deliberately: this one is
// the guild's settings, which an officer can change, and that one is the software's, which they
// cannot.
func TestRules_EachPlanner_WithNoRule_RefusesByName(t *testing.T) {
	t.Parallel()

	// Every slot empty, so each planner meets its own missing rule.
	var rules strategy.Rules

	ctx := newCtx(t, 2, 1000, "")

	for _, tc := range []struct {
		name string
		call func() error
		slot string
	}{
		{"attendance", func() error {
			_, err := rules.PlanAttendance(ctx, strategy.AttendanceEvent{})

			return err
		}, "earn"},
		{"adjustment", func() error {
			_, err := rules.PlanAdjustment(ctx, strategy.AdjustmentEvent{})

			return err
		}, "earn"},
		{"award", func() error {
			_, err := rules.PlanAward(ctx, strategy.AwardEvent{})

			return err
		}, "spend"},
		{"decay", func() error {
			_, err := rules.PlanDecay(ctx, strategy.DecayRun{})

			return err
		}, "over_time"},
		{"spendable", func() error {
			_, err := rules.Spendable(ctx, strategy.AccountRef{ID: acct(0)})

			return err
		}, "spend"},
		{"priority", func() error {
			_, err := rules.Priority(ctx, strategy.AccountRef{ID: acct(0)})

			return err
		}, "spend"},
		{"price hint", func() error {
			_, err := rules.PriceHint(ctx, strategy.ItemRef{})

			return err
		}, "spend"},
		{"validate bid", func() error {
			return rules.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)}, strategy.Bid{})
		}, "spend"},
		{"settle auction", func() error {
			_, err := rules.SettleAuction(ctx, strategy.Session{}, nil)

			return err
		}, "spend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.call()
			require.ErrorIs(t, err, strategy.ErrNoRule)
			require.NotErrorIs(t, err, strategy.ErrUnsupported,
				"an unconfigured pool is the guild's settings and a 409; an unsupported operation is "+
					"the software's and a 501. Collapsing them sends an officer to file a bug report "+
					"about a dropdown they could have filled in")
			require.ErrorContains(t, err, tc.slot)
		})
	}
}

// TestRules_SpendQuestions_RouteToTheSpendRule covers the five read-side methods on a pool that HAS
// a spend rule, so the routing is asserted rather than only its absence.
func TestRules_SpendQuestions_RouteToTheSpendRule(t *testing.T) {
	t.Parallel()

	rules := composedPool(t)
	ctx := newCtx(t, 2, 4200, "")
	me := strategy.AccountRef{ID: acct(0), Kind: "person"}

	spendable, err := rules.Spendable(ctx, me)
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(4200), spendable,
		"a spendable balance is a SUM over committed entries, read through the spend rule")

	priority, err := rules.Priority(ctx, me)
	require.NoError(t, err)
	require.Equal(t, int64(4200), priority.Rank)
	require.Equal(t, acct(0).String(), priority.Tiebreak)

	// fixed_price has no bidding, so its three auction methods are ErrUnsupported — which is the
	// SPEND rule speaking, not the pool. A pool with no spend rule returns ErrNoRule for the same
	// three calls (the test above), and the two answers must stay distinguishable.
	_, err = rules.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "fixed_price")

	require.ErrorIs(t, rules.ValidateBid(ctx, me, strategy.Bid{AmountCp: 10}), strategy.ErrUnsupported)

	_, err = rules.SettleAuction(ctx, strategy.Session{}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
}

// TestRules_PlanReversal_RoutesByTheBatchStrategyID is ADR-0026's central claim, executed.
//
// `ledger_batch.strategy_id` records which of the three rules planned a batch, and that is what a
// reversal routes on: the repair primitive of an append-only ledger must be planned by the rule that
// planned the original. Reversing a `fixed_price` award through `tick` would negate committed entries
// under a rule nobody applied to them.
func TestRules_PlanReversal_RoutesByTheBatchStrategyID(t *testing.T) {
	t.Parallel()

	rules := composedPool(t)
	ctx := newCtx(t, 2, 10_000, "")

	for _, strategyID := range []string{"tick", "fixed_price", "cap"} {
		t.Run(strategyID, func(t *testing.T) {
			t.Parallel()

			original := strategy.LedgerBatch{
				ID:         core.ULID("0000000000000000000BATCH01"),
				Kind:       "award",
				StrategyID: strategyID,
				Entries: []strategy.EntryProposal{
					{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: -100},
					{AccountID: acct(1), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
				},
			}

			p, err := rules.PlanReversal(ctx, original)
			require.NoError(t, err)
			require.Equal(t, strategyID, p.StrategyID,
				"a reversal is attributable to the rule that planned the original, not to today's")
			require.Equal(t, strategy.KindReversal, p.Kind)
			require.Equal(t, original.ID, *p.ReversesBatchID)
		})
	}
}

// TestRules_PlanReversal_ForARuleThePoolNoLongerRuns_StillPlans is the finding this file exists to
// have caught, and it asserts the OPPOSITE of what an earlier cut of Rules.PlanReversal did.
//
// That cut searched the pool's current three rules and refused anything else, on the reasoning that
// "the rules that could reverse it are no longer configured" is an officer's decision. It is not: the
// ledger is APPEND-ONLY, so a reversal is the only repair primitive there is, and refusing one does
// not prevent a bad batch — it prevents the CORRECTION. A guild that switched from `fixed_price` to
// an auction would have found every historical award permanently unreversible, with no way back,
// because the repair had been made contingent on the present.
//
// `.claude/rules/ledger-and-strategy.md` makes exactly this argument about NOT declaring NonNegative
// on a reversal, and reversePlan's own doc comment makes it about not reading the pool's current
// config. This is the third place the same mistake was available, and the assertion below is what
// keeps it made only once.
func TestRules_PlanReversal_ForARuleThePoolNoLongerRuns_StillPlans(t *testing.T) {
	t.Parallel()

	// A pool that now earns and spends, and runs NO over-time rule — the guild dropped `cap` after
	// the run below was committed.
	rules, err := strategy.PoolConfig{
		EarnStrategyID:  "tick",
		EarnConfigJSON:  tickRuleConfig,
		SpendStrategyID: "fixed_price",
		SpendConfigJSON: fixedPriceRuleConfig,
	}.Resolve()
	require.NoError(t, err)

	ctx := newCtx(t, 2, 10_000, "")

	original := strategy.LedgerBatch{
		ID:                 core.ULID("0000000000000000000BATCH02"),
		Kind:               "cap",
		StrategyID:         "cap",
		StrategyVersion:    "0.1.0",
		ConfigSnapshotJSON: capRuleConfig,
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: -100},
			{AccountID: acct(1), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
		},
	}

	p, err := rules.PlanReversal(ctx, original)
	require.NoError(t, err,
		"a batch planned by a rule the pool has since dropped must still be reversible; the ledger is "+
			"append-only and this is the only repair there is")
	require.Equal(t, "cap", p.StrategyID,
		"and it is reversed by the rule that planned it, not by whatever occupies that slot today")
	require.Equal(t, strategy.KindReversal, p.Kind)
	require.Equal(t, original.ID, *p.ReversesBatchID)

	// The ORIGINAL's config snapshot travels onto the reversal, and it is the batch's rather than any
	// current slot's — reading a live document would reintroduce the contingency the fix removed.
	require.Equal(t, capRuleConfig, p.ConfigSnapshotJSON)
}

// TestRules_PlanReversal_ForAStrategyThisBinaryLacks_IsRefused is the one refusal that survives.
//
// An id no shipped strategy answers to is an operator running a binary OLDER than the batch — a
// downgrade across a release that added a strategy. There is no planner to reverse it with, and
// guessing would negate committed entries under rules this binary has never seen. That is a
// startup-shaped refusal naming the id, which is what catalogue.go says ErrUnknownStrategy is for.
func TestRules_PlanReversal_ForAStrategyThisBinaryLacks_IsRefused(t *testing.T) {
	t.Parallel()

	rules := composedPool(t)
	ctx := newCtx(t, 2, 10_000, "")

	_, err := rules.PlanReversal(ctx, strategy.LedgerBatch{
		ID:         core.ULID("0000000000000000000BATCH03"),
		Kind:       "award",
		StrategyID: "auction_sealed",
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
			{AccountID: acct(1), BalanceKind: strategy.BalanceKindDKP, AmountCp: -100},
		},
	})

	require.ErrorIs(t, err, strategy.ErrUnknownStrategy)
	require.ErrorContains(t, err, "auction_sealed", "the refusal names the rule that planned it")
	require.ErrorContains(t, err, "earn=tick", "and the three this pool does run, for diagnosis")
}

// TestRules_Bind_DoesNotLeakTheConfigBetweenRules is the failure the whole ruleCtx mechanism exists
// to prevent, asserted directly rather than through a planner.
//
// A CROSSED CONFIG DOES NOT RELIABLY FAIL TO PARSE, and that is why this test is here rather than
// left to strict decoding. Every strategy in this package decodes with DisallowUnknownFields, so an
// unknown knob is refused — but `tick` and `cap` genuinely SHARE `tick_award_cp` and `floor_cp`
// (cap's soft cap reduces what a tick earns), so `{"tick_award_cp": 1000}` bound into the cap slot
// parses cleanly and is refused only later, for having no ceiling. Two rules that shared enough knobs
// would not be refused at all: they would run a DKP system assembled from the wrong halves of two
// documents. The binding has to be right; the decoder is not a backstop for it.
func TestRules_Bind_DoesNotLeakTheConfigBetweenRules(t *testing.T) {
	t.Parallel()

	rules := composedPool(t)
	ctx := newCtx(t, 1, 150_000, "the pool's own column, which is superseded and read by nobody")

	// A plan that succeeds here is a plan that was handed the cap rule's ceiling, because
	// defaultCapConfig has none and validateCapConfig refuses a pool that caps nothing.
	_, err := rules.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2026-W31",
		AsOfSeq:     7,
		Accounts:    []strategy.AccountRef{{ID: acct(0), Kind: "person"}},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	// And the reverse, with a knob only `tick` has: bind the earn rule's document into the over-time
	// slot and `cap` names the key it does not know. This is what a routing bug looks like from an
	// officer's side, in the case the decoder does catch.
	crossed := strategy.Rules{OverTime: strategy.Rule{
		Strategy:   rules.OverTime.Strategy,
		ConfigJSON: `{"tick_award_cp": 1000, "default_multiplier_bp": 5000}`,
	}}

	_, err = crossed.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 7})
	require.ErrorIs(t, err, strategy.ErrInvalidConfig)
	require.ErrorContains(t, err, "default_multiplier_bp",
		"the unknown knob is the evidence of the crossing")
}

// TestRuleKind_IsRuleKind_IsTheClosedSet. The boundary needs to be able to ask, because a slot read
// out of a pool row or an importer mapping is a string until something says otherwise.
func TestRuleKind_IsRuleKind_IsTheClosedSet(t *testing.T) {
	t.Parallel()

	for _, kind := range []strategy.RuleKind{strategy.RuleEarn, strategy.RuleSpend, strategy.RuleOverTime} {
		require.True(t, strategy.IsRuleKind(string(kind)))
	}

	for _, bad := range []string{"", "Earn", "over time", "overtime", "decay", "spend "} {
		require.False(t, strategy.IsRuleKind(bad), "%q is not one of the three", bad)
	}
}
