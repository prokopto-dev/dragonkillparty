package swap_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/swap"
)

// The evaluator's tests. Three things are being pinned here, in this order of importance:
//
//  1. THE NUMBERS. docs/design/10-ui-decisions.md §4 and the admin mockup's swapRequests give three
//     worked examples — 200.00, 450.00 and 500.00 against a base of 750.00 — and those are the first
//     three rows of the pricing table. A price that looks plausible and is wrong is this package's
//     whole risk, so the assertions are the design's own arithmetic rather than the code's.
//  2. THE ORDER. Rules apply in the policy's order, which is only observable when a multiplier is in
//     play; both orders are tested against the same rule set.
//  3. THE REFUSALS. A missing attendance sample and an overflowing multiplier are errors, not
//     defaults — an assumed zero would silently fail a discount the member was owed.
//
// `time` is imported here and by no other file in the package: arch_test.go bans it in the shipped
// code, where a wall clock would make a quote unreproducible, and allows it in the tests, where
// time.Date is how a readable year boundary gets written. time.Now stays banned everywhere by repo
// gate CLOCK001, which reads test files too.

// noon returns an instant at 12:00 UTC on the given day. Midday rather than midnight so that a
// year-boundary bug shows up as a wrong year rather than as an off-by-one on the boundary itself.
func noon(y int, m time.Month, d int) core.Micros {
	return core.FromTime(time.Date(y, m, d, 12, 0, 0, 0, time.UTC))
}

// The instant every test evaluates at unless it names another: mid-June 2026, outside the swap
// window declared below, with room on both sides of it inside the calendar year.
var evaluatedAt = noon(2026, time.June, 15)

// char builds the deterministic character id for index i. Fabricated rather than generated: the
// evaluator compares ids and never parses one, and a fixed id keeps a failure message readable.
func char(i int) core.ULID { return core.ULID(fmt.Sprintf("0000000000000000CHAR%06d", i)) }

var (
	currentMain = char(0)
	target      = char(1)
)

// guildPolicy is the shipped default as a real guild would have edited it: two needed classes and
// one scheduled swap window, both taken from the admin mockup.
func guildPolicy() swap.Policy {
	p := swap.DefaultPolicy()
	p.Rules[3].Classes = []string{"cleric", "enchanter"}
	p.Windows = []swap.Window{{
		Name:     "Autumn window",
		StartsAt: noon(2026, time.September, 26) - 12*60*60*1_000_000,
		EndsAt:   noon(2026, time.October, 1) - 12*60*60*1_000_000,
	}}

	return p
}

// spent returns n allowance uses earlier in the evaluation year.
func spent(n int) []core.Micros {
	out := make([]core.Micros, 0, n)
	for i := range n {
		out = append(out, noon(2026, time.February, 1+i))
	}

	return out
}

// request is a member who can afford anything, has attendance for both windows the shipped policy
// measures, and has never swapped. Each case changes exactly what it is about.
func request(class string, level int) swap.Request {
	return swap.Request{
		Target:        swap.Character{ID: target, Class: class, Level: level},
		CurrentMainID: currentMain,
		Attendance: []swap.Attendance{
			{WindowDays: 30, Bp: 9_000},
			{WindowDays: 90, Bp: 8_100},
		},
		SpendableCp: 1_000_000,
	}
}

// evaluator reads its clock at evaluatedAt. Every test that cares about a different instant sets
// Request.At instead of building a second clock, which is the seam Phase 7 will use to re-price a
// held quote at the instant it was first quoted.
func evaluator() swap.Evaluator { return swap.New(clock.NewFake(evaluatedAt.Time())) }

// step is the trail projection the pricing table compares: which rule ran, and the cost it left.
//
// Deliberately not the whole AppliedRule. The Detail sentences are compared in full by
// TestEvaluate_TheWorkedExample_ReturnsTheWholeDecision below; repeating one of them in every row
// here would make a reworded message read as a pricing regression in thirty places at once, which is
// how a table stops being read.
type step struct {
	kind    swap.RuleKind
	running core.Centipoints
}

func steps(applied []swap.AppliedRule) []step {
	out := make([]step, 0, len(applied))
	for _, a := range applied {
		out = append(out, step{kind: a.Kind, running: a.RunningCp})
	}

	return out
}

// TestEvaluate_TheRuleStack_PricesTheSwap is the pricing table.
func TestEvaluate_TheRuleStack_PricesTheSwap(t *testing.T) {
	t.Parallel()

	// A stack with no free rules and no guards, for the cases that are about ordering alone.
	ordered := func(rules ...swap.Rule) swap.Policy {
		return swap.Policy{BaseCostCp: 75_000, Rules: rules}
	}

	level60 := swap.Rule{Kind: swap.RuleTargetLevel, Enabled: true, AmountCp: 25_000, MinLevel: 60}
	needed := swap.Rule{
		Kind: swap.RuleNeededClass, Enabled: true, AmountCp: 30_000,
		Classes: []string{"cleric", "enchanter"},
	}
	returning := swap.Rule{Kind: swap.RuleReturningMain, Enabled: true, FactorBp: 20_000}

	tests := []struct {
		name         string
		policy       swap.Policy
		req          swap.Request
		wantCost     core.Centipoints
		wantFree     bool
		wantConsumes bool
		wantTrail    []step
	}{
		{
			// The mockup's Borrik: "cost 200.00 · level 60 −250 · needed class −300".
			name:   "a level-60 cleric with no allowance left costs 200.00 of 750.00",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost: 20_000,
			wantTrail: []step{
				{swap.RuleTargetLevel, 50_000},
				{swap.RuleNeededClass, 20_000},
			},
		},
		{
			// The mockup's Tobble: an Enchanter at 57 — "needed class −300", cost 450.00.
			name:   "a needed class below 60 gets one discount",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("enchanter", 57)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost:  45_000,
			wantTrail: []step{{swap.RuleNeededClass, 45_000}},
		},
		{
			// The mockup's Ysolde: a Warrior at 60 — "level 60 −250", cost 500.00.
			name:   "a 60 nobody needs gets the level discount only",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("warrior", 60)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost:  50_000,
			wantTrail: []step{{swap.RuleTargetLevel, 50_000}},
		},
		{
			name:   "a low-level character nobody needs pays the base",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("warrior", 45)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost:  75_000,
			wantTrail: []step{},
		},
		{
			name:   "an open swap period is free and spends no allowance",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.At = noon(2026, time.September, 28)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost:  0,
			wantFree:  true,
			wantTrail: []step{{swap.RuleOpenWindow, 0}},
		},
		{
			name:         "an unspent allowance is free and spends one",
			policy:       guildPolicy(),
			req:          request("cleric", 60),
			wantCost:     0,
			wantFree:     true,
			wantConsumes: true,
			wantTrail:    []step{{swap.RuleAnnualAllowance, 0}},
		},
		{
			name:   "one of two allowances left is still free",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(1)

				return r
			}(),
			wantCost:     0,
			wantFree:     true,
			wantConsumes: true,
			wantTrail:    []step{{swap.RuleAnnualAllowance, 0}},
		},
		{
			name:   "the allowance resets on 1 January — last year's swaps do not count",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.At = noon(2026, time.January, 2)
				r.AllowanceUsedAt = []core.Micros{
					noon(2025, time.March, 4),
					noon(2025, time.December, 31),
				}

				return r
			}(),
			wantCost:     0,
			wantFree:     true,
			wantConsumes: true,
			wantTrail:    []step{{swap.RuleAnnualAllowance, 0}},
		},
		{
			name:   "a caller may name the period start, for a guild whose year is not UTC's",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceYearStart = noon(2026, time.April, 1)
				r.AllowanceUsedAt = spent(2) // both in February, before the named start
				return r
			}(),
			wantCost:     0,
			wantFree:     true,
			wantConsumes: true,
			wantTrail:    []step{{swap.RuleAnnualAllowance, 0}},
		},
		{
			name:   "an attendance discount stacks with the other two",
			policy: func() swap.Policy { p := guildPolicy(); p.Rules[4].Enabled = true; return p }(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost: 10_000,
			wantTrail: []step{
				{swap.RuleTargetLevel, 50_000},
				{swap.RuleNeededClass, 20_000},
				{swap.RuleAttendance, 10_000},
			},
		},
		{
			name:   "attendance below the rule's floor does not discount",
			policy: func() swap.Policy { p := guildPolicy(); p.Rules[4].Enabled = true; return p }(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(2)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 9_000}, {WindowDays: 90, Bp: 7_999}}

				return r
			}(),
			wantCost: 20_000,
			wantTrail: []step{
				{swap.RuleTargetLevel, 50_000},
				{swap.RuleNeededClass, 20_000},
			},
		},
		{
			name:   "the multiplier scales what the discounts left, because the policy lists it last",
			policy: ordered(level60, needed, returning),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{char(7), target}

				return r
			}(),
			wantCost: 40_000,
			wantTrail: []step{
				{swap.RuleTargetLevel, 50_000},
				{swap.RuleNeededClass, 20_000},
				{swap.RuleReturningMain, 40_000},
			},
		},
		{
			name:   "the same rules in a different order price differently, and that is the policy's call",
			policy: ordered(returning, level60, needed),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{target}

				return r
			}(),
			wantCost: 95_000,
			wantTrail: []step{
				{swap.RuleReturningMain, 150_000},
				{swap.RuleTargetLevel, 125_000},
				{swap.RuleNeededClass, 95_000},
			},
		},
		{
			name:      "a multiplier on a character who was never a main does not apply",
			policy:    ordered(returning),
			req:       request("cleric", 60),
			wantCost:  75_000,
			wantTrail: []step{},
		},
		{
			name: "discounts past the base floor the price at zero rather than inverting it",
			policy: ordered(
				swap.Rule{Kind: swap.RuleTargetLevel, Enabled: true, AmountCp: 60_000, MinLevel: 60},
				swap.Rule{
					Kind: swap.RuleNeededClass, Enabled: true, AmountCp: 60_000,
					Classes: []string{"cleric"},
				},
			),
			req:      request("cleric", 60),
			wantCost: 0,
			// Free stays FALSE: the cost reached zero through discounts, which is an ordinary priced
			// swap that happens to cost nothing, not the design's "free".
			wantFree: false,
			wantTrail: []step{
				{swap.RuleTargetLevel, 15_000},
				{swap.RuleNeededClass, 0},
			},
		},
		{
			name: "a multiplier on a floored price stays at zero",
			policy: ordered(
				swap.Rule{Kind: swap.RuleTargetLevel, Enabled: true, AmountCp: 75_000, MinLevel: 60},
				returning,
			),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{target}

				return r
			}(),
			wantCost: 0,
			wantTrail: []step{
				{swap.RuleTargetLevel, 0},
				{swap.RuleReturningMain, 0},
			},
		},
		{
			name: "a free rule after a discount discards it, and the trail shows both",
			policy: swap.Policy{
				BaseCostCp: 75_000,
				Rules:      []swap.Rule{level60, {Kind: swap.RuleOpenWindow, Enabled: true}},
				Windows:    guildPolicy().Windows,
			},
			req: func() swap.Request {
				r := request("cleric", 60)
				r.At = noon(2026, time.September, 28)

				return r
			}(),
			wantCost: 0,
			wantFree: true,
			wantTrail: []step{
				{swap.RuleTargetLevel, 50_000},
				{swap.RuleOpenWindow, 0},
			},
		},
		{
			name:   "nothing after a matching free rule is evaluated",
			policy: guildPolicy(),
			req:    request("cleric", 60),
			// The allowance matches at position 2 and the two discounts that follow would both have
			// matched. A price of zero cannot be discounted further, so the walk stops.
			wantCost:     0,
			wantFree:     true,
			wantConsumes: true,
			wantTrail:    []step{{swap.RuleAnnualAllowance, 0}},
		},
		{
			name:   "a disabled rule never matches and never appears in the trail",
			policy: guildPolicy(),
			req: func() swap.Request {
				// The attendance rule ships disabled, and this member is well above its floor.
				r := request("warrior", 45)
				r.AllowanceUsedAt = spent(2)

				return r
			}(),
			wantCost:  75_000,
			wantTrail: []step{},
		},
		{
			name:      "a policy with no rules charges the base",
			policy:    swap.Policy{BaseCostCp: 75_000},
			req:       request("cleric", 60),
			wantCost:  75_000,
			wantTrail: []step{},
		},
		{
			name:      "a base of zero is a guild that does not charge for swaps",
			policy:    swap.Policy{BaseCostCp: 0},
			req:       request("cleric", 60),
			wantCost:  0,
			wantTrail: []step{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := evaluator().Evaluate(tc.policy, tc.req)
			require.NoError(t, err)

			require.Equal(t, tc.wantCost, got.Quote.CostCp, "cost")
			require.Equal(t, tc.wantFree, got.Quote.Free, "free")
			require.Equal(t, tc.wantConsumes, got.Quote.ConsumesAllowance, "consumes an allowance")
			require.Equal(t, tc.wantTrail, steps(got.Quote.Applied), "the applied trail, in order")
			require.Equal(t, tc.policy.BaseCostCp, got.Quote.BaseCostCp, "the base the trail started from")
			require.GreaterOrEqual(t, got.Quote.CostCp, core.Centipoints(0), "a price is never negative")
		})
	}
}

// TestEvaluate_TheWorkedExample_ReturnsTheWholeDecision compares an entire Decision, details and all.
//
// The table above projects the trail to (kind, running cost) so that one reworded sentence does not
// read as thirty pricing regressions. This is the other half of that trade: one case where every
// field is asserted, including the sentences a member reads, so a change to any of them lands in a
// diff rather than passing unnoticed.
func TestEvaluate_TheWorkedExample_ReturnsTheWholeDecision(t *testing.T) {
	t.Parallel()

	r := request("cleric", 60)
	r.AllowanceUsedAt = spent(2)

	got, err := evaluator().Evaluate(guildPolicy(), r)
	require.NoError(t, err)

	require.Equal(t, swap.Decision{
		Quote: swap.Quote{
			BaseCostCp: 75_000,
			CostCp:     20_000,
			Applied: []swap.AppliedRule{
				{
					Kind:      swap.RuleTargetLevel,
					Effect:    swap.EffectDiscount,
					AmountCp:  25_000,
					Detail:    "the target is level 60, at or above 60",
					RunningCp: 50_000,
				},
				{
					Kind:      swap.RuleNeededClass,
					Effect:    swap.EffectDiscount,
					AmountCp:  30_000,
					Detail:    "cleric is on the needed list",
					RunningCp: 20_000,
				},
			},
			EvaluatedAt: evaluatedAt,
		},
		Blocks:            []swap.Block{},
		ApprovalsRequired: 2,
	}, got)

	require.True(t, got.Allowed())
}

// TestEvaluate_TheFreeRules_ExplainThemselves. A member who is told a swap is free asks why, and the
// two free rules answer differently: one names the window, the other counts what is left.
func TestEvaluate_TheFreeRules_ExplainThemselves(t *testing.T) {
	t.Parallel()

	inWindow := request("cleric", 60)
	inWindow.At = noon(2026, time.September, 28)
	inWindow.AllowanceUsedAt = spent(2)

	got, err := evaluator().Evaluate(guildPolicy(), inWindow)
	require.NoError(t, err)
	require.Equal(t, []swap.AppliedRule{{
		Kind:   swap.RuleOpenWindow,
		Effect: swap.EffectFree,
		Detail: `the "Autumn window" swap period is open`,
	}}, got.Quote.Applied)
	require.False(t, got.Quote.ConsumesAllowance, "an open window does not spend an allowance")

	onAllowance := request("cleric", 60)
	onAllowance.AllowanceUsedAt = spent(1)

	got, err = evaluator().Evaluate(guildPolicy(), onAllowance)
	require.NoError(t, err)
	require.Equal(t, []swap.AppliedRule{{
		Kind:   swap.RuleAnnualAllowance,
		Effect: swap.EffectFree,
		Detail: "1 of 2 free swaps used since 2026-01-01T00:00:00.000000Z",
	}}, got.Quote.Applied)
	require.True(t, got.Quote.ConsumesAllowance)
}

// TestEvaluate_TheSwapWindow_IsHalfOpen. Two adjacent windows must not both cover the instant they
// share, and the member on the last microsecond of a window is inside it.
func TestEvaluate_TheSwapWindow_IsHalfOpen(t *testing.T) {
	t.Parallel()

	p := guildPolicy()
	p.Rules[1].Enabled = false // the allowance would otherwise make every case free
	window := p.Windows[0]

	for _, tc := range []struct {
		name     string
		at       core.Micros
		wantFree bool
	}{
		{"one microsecond before it opens", window.StartsAt - 1, false},
		{"the microsecond it opens", window.StartsAt, true},
		{"the last microsecond it covers", window.EndsAt - 1, true},
		{"the microsecond it closes", window.EndsAt, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := request("warrior", 45)
			r.At = tc.at

			got, err := evaluator().Evaluate(p, r)
			require.NoError(t, err)
			require.Equal(t, tc.wantFree, got.Quote.Free)
			require.Equal(t, tc.at, got.Quote.EvaluatedAt)
		})
	}
}

// TestEvaluate_TheClock_SuppliesNowAndOnlyNow. The injected clock is what makes a quote reproducible:
// a request naming its own instant must not consult it at all.
func TestEvaluate_TheClock_SuppliesNowAndOnlyNow(t *testing.T) {
	t.Parallel()

	p := guildPolicy()
	p.Rules[1].Enabled = false

	insideTheWindow := noon(2026, time.September, 28)

	// The clock reads mid-June, so the window is shut and the swap is priced.
	priced, err := evaluator().Evaluate(p, request("warrior", 45))
	require.NoError(t, err)
	require.False(t, priced.Quote.Free)
	require.Equal(t, evaluatedAt, priced.Quote.EvaluatedAt)

	// The same evaluator, same policy, a request that names an instant inside the window.
	named := request("warrior", 45)
	named.At = insideTheWindow

	free, err := evaluator().Evaluate(p, named)
	require.NoError(t, err)
	require.True(t, free.Quote.Free)
	require.Equal(t, insideTheWindow, free.Quote.EvaluatedAt)

	// And an evaluator with no clock at all still prices that request, because it never needs a now.
	sansClock, err := swap.New(nil).Evaluate(p, named)
	require.NoError(t, err)
	require.Equal(t, free, sansClock)
}

// TestEvaluate_TheGuards_BlockTheRequest is the guard table. Guards are separate from cost
// (docs/design/10-ui-decisions.md §4): a member can afford a swap and still be inside their cooldown.
func TestEvaluate_TheGuards_BlockTheRequest(t *testing.T) {
	t.Parallel()

	swappedAt := func(days int) *core.Micros {
		at := evaluatedAt - core.Micros(days)*24*60*60*1_000_000

		return &at
	}

	tests := []struct {
		name   string
		policy swap.Policy
		req    swap.Request
		want   []swap.Block
	}{
		{
			name:   "a member who may ask is blocked by nothing",
			policy: guildPolicy(),
			req:    request("cleric", 60),
			want:   []swap.Block{},
		},
		{
			name:   "swapping to the character that is already the main is not a swap",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Target.ID = currentMain

				return r
			}(),
			want: []swap.Block{{
				Code: swap.BlockTargetIsCurrentMain,
				Reason: fmt.Sprintf("%s is already the main, so there is nothing to swap",
					currentMain),
			}},
		},
		{
			name:   "a person with no main yet is claiming one, and that is not blocked",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.CurrentMainID = ""

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name:   "a guild character cannot be anybody's main",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Target.Shared = true

				return r
			}(),
			want: []swap.Block{{
				Code: swap.BlockTargetIsShared,
				Reason: fmt.Sprintf(
					"%s is a guild character; a character several people play cannot be one person's main",
					target),
			}},
		},
		{
			name:   "a swap 89 days ago is inside the 90-day cooldown",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.LastSwapAt = swappedAt(89)

				return r
			}(),
			want: []swap.Block{{
				Code: swap.BlockCooldownActive,
				Reason: fmt.Sprintf("the last swap was at %s and the cooldown is 90 days",
					*swappedAt(89)),
				ClearsAt: *swappedAt(89) + 90*24*60*60*1_000_000,
			}},
		},
		{
			name:   "a swap exactly 90 days ago has served the cooldown",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.LastSwapAt = swappedAt(90)

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name:   "a member who has never swapped is not on cooldown",
			policy: guildPolicy(),
			req:    request("cleric", 60),
			want:   []swap.Block{},
		},
		{
			name: "a guild with no cooldown never blocks on one",
			policy: func() swap.Policy {
				p := guildPolicy()
				p.Guards.CooldownDays = 0

				return p
			}(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.LastSwapAt = swappedAt(1)

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name:   "below the attendance floor to request at all",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 4_100}, {WindowDays: 90, Bp: 8_100}}

				return r
			}(),
			want: []swap.Block{{
				Code:   swap.BlockBelowAttendanceFloor,
				Reason: "attendance is 4100 bp over 30 days, below the 5000 bp floor to request",
			}},
		},
		{
			name:   "exactly at the floor is above it",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 5_000}, {WindowDays: 90, Bp: 8_100}}

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name: "a guild with no floor asks for no sample",
			policy: func() swap.Policy {
				p := guildPolicy()
				p.Guards.MinAttendanceBp = 0

				return p
			}(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = nil

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name:   "short of the cost, by exactly the shortfall",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(2) // costs 20 000
				r.SpendableCp = 5_500

				return r
			}(),
			want: []swap.Block{{
				Code:        swap.BlockInsufficientPoints,
				Reason:      "the swap costs 20000 centipoints and 5500 are spendable",
				ShortfallCp: 14_500,
			}},
		},
		{
			name:   "exactly affordable is affordable",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(2)
				r.SpendableCp = 20_000

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name:   "a member in debt can still take a free swap",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.SpendableCp = -50_000

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name: "a price discounted to zero is affordable to a member in debt too",
			policy: swap.Policy{
				BaseCostCp: 75_000,
				Rules: []swap.Rule{{
					Kind: swap.RuleTargetLevel, Enabled: true, AmountCp: 75_000, MinLevel: 60,
				}},
			},
			req: func() swap.Request {
				r := request("cleric", 60)
				r.SpendableCp = -50_000

				return r
			}(),
			want: []swap.Block{},
		},
		{
			name:   "every block that applies is reported, in a fixed order",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Target.ID = currentMain
				r.Target.Shared = true
				r.LastSwapAt = swappedAt(1)
				r.AllowanceUsedAt = spent(2)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 100}, {WindowDays: 90, Bp: 100}}
				r.SpendableCp = 0

				return r
			}(),
			want: []swap.Block{
				{
					Code: swap.BlockTargetIsCurrentMain,
					Reason: fmt.Sprintf("%s is already the main, so there is nothing to swap",
						currentMain),
				},
				{
					Code: swap.BlockTargetIsShared,
					Reason: fmt.Sprintf(
						"%s is a guild character; a character several people play cannot be one "+
							"person's main", currentMain),
				},
				{
					Code: swap.BlockCooldownActive,
					Reason: fmt.Sprintf("the last swap was at %s and the cooldown is 90 days",
						*swappedAt(1)),
					ClearsAt: *swappedAt(1) + 90*24*60*60*1_000_000,
				},
				{
					Code:   swap.BlockBelowAttendanceFloor,
					Reason: "attendance is 100 bp over 30 days, below the 5000 bp floor to request",
				},
				{
					Code:        swap.BlockInsufficientPoints,
					Reason:      "the swap costs 20000 centipoints and 0 are spendable",
					ShortfallCp: 20_000,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := evaluator().Evaluate(tc.policy, tc.req)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Blocks)
			require.Equal(t, len(tc.want) == 0, got.Allowed())
		})
	}
}

// TestEvaluate_ACooldownPastTheEndOfTime_SaturatesRatherThanWrapping. Reaching this needs a request
// dated around the year 294 000; reporting the far future beats reporting a clears-at in 1970.
func TestEvaluate_ACooldownPastTheEndOfTime_SaturatesRatherThanWrapping(t *testing.T) {
	t.Parallel()

	last := core.Micros(math.MaxInt64 - 1)

	p := guildPolicy()
	p.Rules = nil
	p.Guards.MinAttendanceBp = 0

	r := swap.Request{
		At:          math.MaxInt64,
		Target:      swap.Character{ID: target, Class: "cleric", Level: 60},
		LastSwapAt:  &last,
		SpendableCp: 1_000_000,
	}

	got, err := evaluator().Evaluate(p, r)
	require.NoError(t, err)
	require.Len(t, got.Blocks, 1)
	require.Equal(t, swap.BlockCooldownActive, got.Blocks[0].Code)
	require.Equal(t, core.Micros(math.MaxInt64), got.Blocks[0].ClearsAt)
}

// TestEvaluate_RejectsWhatItCannotPrice is the refusal table.
//
// Every case here is a MISSING OR CONTRADICTORY FACT rather than a rule failure. The distinction is
// the one that matters to a member: a rule failure is a block they can read on the screen, and this
// is a caller that did not supply what the policy asked about.
func TestEvaluate_RejectsWhatItCannotPrice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  swap.Policy
		req     swap.Request
		wantErr error
		want    string
	}{
		{
			name:   "no target character",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Target.ID = ""

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "names no target character",
		},
		{
			name:    "a class spelled for a human would silently miss the needed-class discount",
			policy:  guildPolicy(),
			req:     request("Cleric", 60),
			wantErr: swap.ErrInvalidRequest,
			want:    `has class "Cleric"`,
		},
		{
			name:    "level zero",
			policy:  guildPolicy(),
			req:     request("cleric", 0),
			wantErr: swap.ErrInvalidRequest,
			want:    "is level 0, want 1..60",
		},
		{
			name:    "level past the cap",
			policy:  guildPolicy(),
			req:     request("cleric", 61),
			wantErr: swap.ErrInvalidRequest,
			want:    "is level 61, want 1..60",
		},
		{
			name:   "an empty previous main",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{""}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "previous_main_ids[0] is empty",
		},
		{
			name:   "the same previous main twice",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{target, target}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "twice",
		},
		{
			name:   "an allowance spent in the future",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = []core.Micros{noon(2026, time.December, 1)}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "after the evaluation instant",
		},
		{
			name:   "an allowance spent at the epoch",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = []core.Micros{0}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "allowance_used_at[0] is 0",
		},
		{
			name:   "the same allowance counted twice",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				used := noon(2026, time.March, 1)
				r.AllowanceUsedAt = []core.Micros{used, used}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "would spend one allowance twice",
		},
		{
			name:   "an allowance period that has not begun",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceYearStart = noon(2026, time.December, 1)

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "is not at or before the evaluation instant",
		},
		{
			name:   "an attendance sample the rule asks for and the caller did not supply",
			policy: func() swap.Policy { p := guildPolicy(); p.Rules[4].Enabled = true; return p }(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.AllowanceUsedAt = spent(2)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 9_000}}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "the attendance rule measures attendance over 90 days",
		},
		{
			name:   "an attendance sample the guard asks for and the caller did not supply",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = nil

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "the attendance floor measures attendance over 30 days",
		},
		{
			name:   "an attendance sample over no days",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = []swap.Attendance{{WindowDays: 0, Bp: 5_000}}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "attendance[0] measures 0 days",
		},
		{
			name:   "an attendance sample above 100%",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 10_001}}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "attendance[0] is 10001 bp",
		},
		{
			name:   "two samples for one window",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.Attendance = []swap.Attendance{{WindowDays: 30, Bp: 9_000}, {WindowDays: 30, Bp: 1_000}}

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "two samples for the 30-day window",
		},
		{
			name:   "a last swap in the future would measure the cooldown backwards",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				future := noon(2026, time.July, 1)
				r.LastSwapAt = &future

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "is after the evaluation instant",
		},
		{
			name:   "a last swap at the epoch is not a last swap",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				epoch := core.Micros(0)
				r.LastSwapAt = &epoch

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "last_swap_at is 0",
		},
		{
			name:   "an instant before the epoch resolves no window and no period",
			policy: guildPolicy(),
			req: func() swap.Request {
				r := request("cleric", 60)
				r.At = -1

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "the evaluation instant is -1",
		},
		{
			name:    "a policy that does not validate is refused before anything is priced",
			policy:  swap.Policy{BaseCostCp: -1},
			req:     request("cleric", 60),
			wantErr: swap.ErrInvalidPolicy,
			want:    "base_cost_cp is -1",
		},
		{
			name: "a multiplier whose product does not fit in an int64",
			policy: swap.Policy{
				BaseCostCp: math.MaxInt64,
				Rules: []swap.Rule{
					{Kind: swap.RuleReturningMain, Enabled: true, FactorBp: 20_000},
				},
			},
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{target}

				return r
			}(),
			wantErr: swap.ErrInvalidPolicy,
			want:    "does not fit in an int64",
		},
		{
			name: "a multiplier whose product does not fit in 128 bits either",
			policy: swap.Policy{
				BaseCostCp: math.MaxInt64,
				Rules: []swap.Rule{
					{Kind: swap.RuleReturningMain, Enabled: true, FactorBp: 100_000},
				},
			},
			req: func() swap.Request {
				r := request("cleric", 60)
				r.PreviousMainIDs = []core.ULID{target}

				return r
			}(),
			wantErr: swap.ErrInvalidPolicy,
			want:    "does not fit in an int64",
		},
		{
			name:   "a shortfall against a balance at the int64 floor",
			policy: swap.Policy{BaseCostCp: 1},
			req: func() swap.Request {
				r := request("cleric", 60)
				r.SpendableCp = math.MinInt64

				return r
			}(),
			wantErr: swap.ErrInvalidRequest,
			want:    "does not fit in an int64",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := evaluator().Evaluate(tc.policy, tc.req)
			require.ErrorIs(t, err, tc.wantErr)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestEvaluate_WithNoClockAndNoInstant_SaysSo. A pure package tells the caller rather than panicking.
func TestEvaluate_WithNoClockAndNoInstant_SaysSo(t *testing.T) {
	t.Parallel()

	_, err := swap.New(nil).Evaluate(guildPolicy(), request("cleric", 60))
	require.ErrorIs(t, err, swap.ErrInvalidRequest)
	require.Contains(t, err.Error(), "the evaluator has no clock")
}

// TestEvaluate_TheAllowanceYear_IsTheUTCCalendarYear covers the boundary the design words as "resets
// 1 Jan", including the leap year the arithmetic would get wrong if it counted days rather than
// asking the calendar.
func TestEvaluate_TheAllowanceYear_IsTheUTCCalendarYear(t *testing.T) {
	t.Parallel()

	p := guildPolicy()
	p.Rules[0].Enabled = false // no window may interfere
	p.Rules[1].PerYear = 1

	tests := []struct {
		name     string
		at       core.Micros
		used     core.Micros
		wantFree bool
	}{
		{
			name:     "a swap earlier in the same year has spent the allowance",
			at:       noon(2026, time.June, 15),
			used:     noon(2026, time.January, 1),
			wantFree: false,
		},
		{
			name:     "the last microsecond of the previous year has not",
			at:       noon(2026, time.June, 15),
			used:     core.FromTime(time.Date(2025, time.December, 31, 23, 59, 59, 999_999_000, time.UTC)),
			wantFree: true,
		},
		{
			name:     "the first microsecond of this year has",
			at:       noon(2026, time.June, 15),
			used:     core.FromTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
			wantFree: false,
		},
		{
			name:     "a leap year counts its extra day",
			at:       noon(2028, time.March, 1),
			used:     noon(2028, time.February, 29),
			wantFree: false,
		},
		{
			name:     "and does not reach back past its own 1 January",
			at:       noon(2028, time.March, 1),
			used:     noon(2027, time.December, 31),
			wantFree: true,
		},
		{
			name:     "an instant exactly at midnight on 1 January",
			at:       core.FromTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
			used:     core.FromTime(time.Date(2025, time.December, 31, 12, 0, 0, 0, time.UTC)),
			wantFree: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := request("warrior", 45)
			r.At = tc.at
			r.AllowanceUsedAt = []core.Micros{tc.used}

			got, err := evaluator().Evaluate(p, r)
			require.NoError(t, err)
			require.Equal(t, tc.wantFree, got.Quote.Free)
			require.Equal(t, tc.wantFree, got.Quote.ConsumesAllowance)
		})
	}
}
