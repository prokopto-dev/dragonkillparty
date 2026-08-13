package swap_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/swap"
)

// Reproducibility, which is the property Phase 7 hangs a held quote on: "the cost is fixed at request
// time; changing a rule afterwards never re-prices a pending or approved swap"
// (docs/design/10-ui-decisions.md §4). That promise is only keepable if the same policy and the same
// request at the same instant produce the same quote, so it is asserted rather than assumed.

// TestEvaluate_TheSameRequestTwice_IsByteIdentical.
//
// The comparison is over the MARSHALLED form, not the struct: Phase 7 stores the quote as JSON beside
// the request, and two structs that compare equal while marshalling differently would still produce
// two different stored quotes. Two evaluators are used rather than one, because a value that cached
// anything between calls would pass with one.
func TestEvaluate_TheSameRequestTwice_IsByteIdentical(t *testing.T) {
	t.Parallel()

	r := request("cleric", 60)
	r.AllowanceUsedAt = spent(1)
	r.PreviousMainIDs = []core.ULID{char(9)}
	r.LastSwapAt = func() *core.Micros { at := noon(2025, time.May, 1); return &at }()

	p := guildPolicy()
	p.Rules[4].Enabled = true
	p.Rules[5].Enabled = true

	first, err := evaluator().Evaluate(p, r)
	require.NoError(t, err)

	second, err := swap.New(clock.NewFake(evaluatedAt.Time())).Evaluate(p, r)
	require.NoError(t, err)

	require.Equal(t, first, second)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)

	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)

	require.Equal(t, string(firstJSON), string(secondJSON))

	// Money is an unquoted integer and time is RFC 3339 with microseconds (canonical §1, §2). The
	// quote is a stored document, so its wire shape is asserted here rather than left to the first
	// endpoint that serialises one.
	require.Contains(t, string(firstJSON), `"cost_cp":0`)
	require.Contains(t, string(firstJSON), `"evaluated_at":"2026-06-15T12:00:00.000000Z"`)
	require.Contains(t, string(firstJSON), `"blocks":[]`)
}

// TestEvaluate_AnEmptyTrail_MarshalsAsAnArray. A client reading `null` has to special-case a value
// that never means anything different from the empty list.
func TestEvaluate_AnEmptyTrail_MarshalsAsAnArray(t *testing.T) {
	t.Parallel()

	got, err := evaluator().Evaluate(swap.Policy{BaseCostCp: 100}, request("warrior", 45))
	require.NoError(t, err)

	body, err := json.Marshal(got.Quote)
	require.NoError(t, err)
	require.Contains(t, string(body), `"applied":[]`)
}

// rng is a splitmix64, four lines of arithmetic with no dependency and no package-level state.
//
// math/rand is banned in this package (arch_test.go), and the ledger's seeded Rng lives behind an
// interface this package has no reason to import. What the sweep below needs is a reproducible
// stream of numbers, and a seeded splitmix64 is exactly that: same seed, same sweep, on every machine
// and in every shuffle order.
type rng struct{ state uint64 }

func (r *rng) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB

	return z ^ (z >> 31)
}

// intn returns a value in [0, n).
func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

// chance reports true one time in n.
func (r *rng) chance(n int) bool { return r.intn(n) == 0 }

// propertyChecks is the sweep length.
//
// Fixed at the repository's per-PR property budget rather than read from DKP_PROPERTY_CHECKS: that
// variable belongs to `make test-property`, which runs the ledger and strategy properties, and
// honouring it here would suggest the nightly 20 000-check run reaches this package when it does not.
// The sweep costs under a millisecond, so it runs on every `make test` instead.
const propertyChecks = 200

// TestEvaluate_OverASeededSweep_HoldsItsInvariants.
//
// Five properties, each one a shape a plausible-looking bug takes:
//
//   - A PRICE IS NEVER NEGATIVE. A guild paying a member to change main has no batch shape.
//   - FREE MEANS ZERO. The flag and the number cannot disagree, or a held quote says one thing and
//     charges another.
//   - CONSUMING AN ALLOWANCE IMPLIES FREE. An allowance spent on a priced swap is a member charged
//     twice.
//   - THE TRAIL ENDS AT THE PRICE. The last step's running cost is the quote's cost, which is what
//     makes the trail an audit of the arithmetic rather than a decoration beside it.
//   - RE-EVALUATION REPRODUCES. Same inputs, same answer, every time.
func TestEvaluate_OverASeededSweep_HoldsItsInvariants(t *testing.T) {
	t.Parallel()

	r := &rng{state: 0x0DDB1A5E5BAD5EED}

	for i := range propertyChecks {
		policy, req := randomCase(r)

		got, err := evaluator().Evaluate(policy, req)
		require.NoError(t, err, "check %d built an unpriceable case", i)

		require.GreaterOrEqual(t, got.Quote.CostCp, core.Centipoints(0),
			"check %d priced a swap at %d, and a negative price is the guild paying for the swap",
			i, got.Quote.CostCp)

		if got.Quote.Free {
			require.Zero(t, got.Quote.CostCp, "check %d is free and yet costs something", i)
		}

		if got.Quote.ConsumesAllowance {
			require.True(t, got.Quote.Free,
				"check %d spends an allowance on a priced swap, which charges the member twice", i)
		}

		if len(got.Quote.Applied) == 0 {
			require.Equal(t, policy.BaseCostCp, got.Quote.CostCp,
				"check %d applied no rule, so the price is the base", i)
		} else {
			require.Equal(t, got.Quote.Applied[len(got.Quote.Applied)-1].RunningCp, got.Quote.CostCp,
				"check %d ends its trail at a different number from the price it quotes", i)
		}

		again, err := evaluator().Evaluate(policy, req)
		require.NoError(t, err)
		require.Equal(t, got, again, "check %d does not reproduce", i)
	}
}

// randomCase builds a policy and a request that are always valid — the sweep is about the arithmetic,
// and the refusals have their own table in evaluate_test.go.
//
// The distribution is chosen rather than uniform, in the shape internal/ledger/property_test.go uses:
// the interesting cases are the ones where discounts nearly exhaust the base and where a multiplier
// lands on an already-small number, so the amounts are drawn to make both common.
func randomCase(r *rng) (swap.Policy, swap.Request) {
	base := core.Centipoints(r.intn(120_000))

	rules := []swap.Rule{}

	if r.chance(3) {
		rules = append(rules, swap.Rule{Kind: swap.RuleOpenWindow, Enabled: true})
	}

	if r.chance(2) {
		rules = append(rules, swap.Rule{
			Kind: swap.RuleAnnualAllowance, Enabled: true, PerYear: 1 + r.intn(3),
		})
	}

	if r.chance(2) {
		rules = append(rules, swap.Rule{
			Kind: swap.RuleTargetLevel, Enabled: true,
			AmountCp: core.Centipoints(1 + r.intn(60_000)), MinLevel: 1 + r.intn(60),
		})
	}

	if r.chance(2) {
		rules = append(rules, swap.Rule{
			Kind: swap.RuleNeededClass, Enabled: true,
			AmountCp: core.Centipoints(1 + r.intn(60_000)),
			Classes:  []string{"cleric", "enchanter"},
		})
	}

	if r.chance(2) {
		rules = append(rules, swap.Rule{
			Kind: swap.RuleAttendance, Enabled: true,
			AmountCp: core.Centipoints(1 + r.intn(60_000)),
			// 1..10000 bp, biased low so the rule fires more often than not.
			MinAttendanceBp: int64(1 + r.intn(9_000)), WindowDays: 90,
		})
	}

	if r.chance(2) {
		rules = append(rules, swap.Rule{
			Kind: swap.RuleReturningMain, Enabled: true, FactorBp: int64(1 + r.intn(40_000)),
		})
	}

	window := swap.Window{
		Name:     "swept window",
		StartsAt: noon(2026, time.September, 26),
		EndsAt:   noon(2026, time.October, 1),
	}

	at := noon(2026, time.June, 15)
	if r.chance(3) {
		at = noon(2026, time.September, 28)
	}

	classes := []string{"cleric", "enchanter", "warrior", "shadow_knight"}

	req := swap.Request{
		At: at,
		Target: swap.Character{
			ID:    char(1 + r.intn(4)),
			Class: classes[r.intn(len(classes))],
			Level: 1 + r.intn(60),
		},
		CurrentMainID: currentMain,
		Attendance: []swap.Attendance{
			{WindowDays: 30, Bp: int64(r.intn(10_001))},
			{WindowDays: 90, Bp: int64(r.intn(10_001))},
		},
		SpendableCp: core.Centipoints(r.intn(200_000)) - 50_000,
	}

	if r.chance(2) {
		req.PreviousMainIDs = []core.ULID{char(1 + r.intn(4))}
	}

	for range r.intn(4) {
		used := noon(2026, time.February, 1+len(req.AllowanceUsedAt))
		req.AllowanceUsedAt = append(req.AllowanceUsedAt, used)
	}

	return swap.Policy{
		BaseCostCp: base,
		Rules:      rules,
		Windows:    []swap.Window{window},
		Guards: swap.Guards{
			CooldownDays:         r.intn(120),
			MinAttendanceBp:      int64(r.intn(10_001)),
			AttendanceWindowDays: 30,
			ApprovalsRequired:    r.intn(3),
		},
	}, req
}

// TestRandomCase_BuildsPoliciesTheEvaluatorAccepts is the sweep's own control.
//
// Without it, a generator that produced one degenerate policy for all 200 checks — no rules, base
// zero — would satisfy every invariant above and prove nothing. It asserts the sweep actually reaches
// the shapes it claims to: a priced swap, a free one, a multiplied one, and a blocked one.
func TestRandomCase_BuildsPoliciesTheEvaluatorAccepts(t *testing.T) {
	t.Parallel()

	r := &rng{state: 0x0DDB1A5E5BAD5EED}

	var priced, free, multiplied, blocked int

	for range propertyChecks {
		policy, req := randomCase(r)
		require.NoError(t, policy.Validate())

		got, err := evaluator().Evaluate(policy, req)
		require.NoError(t, err)

		if got.Quote.CostCp > 0 {
			priced++
		}

		if got.Quote.Free {
			free++
		}

		for _, a := range got.Quote.Applied {
			if a.Effect == swap.EffectMultiply {
				multiplied++
			}
		}

		if !got.Allowed() {
			blocked++
		}
	}

	require.Positive(t, priced, "the sweep never priced a swap")
	require.Positive(t, free, "the sweep never made one free")
	require.Positive(t, multiplied, "the sweep never applied a multiplier")
	require.Positive(t, blocked, "the sweep never blocked a request")
}
