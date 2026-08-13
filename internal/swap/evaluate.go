package swap

import (
	"fmt"
	"math"
	"math/bits"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The unit conversions, spelled out so the arithmetic below reads as conversion rather than as magic
// numbers. A day here is exactly 24 hours: Micros are UTC (canonical §2), which has no DST to make a
// day 23 or 25 hours long, and a cooldown measured in calendar days would need a guild timezone this
// package deliberately does not link.
const (
	microsPerSecond = 1_000_000
	nanosPerMicro   = 1_000
	secondsPerDay   = 24 * 60 * 60
	microsPerDay    = secondsPerDay * microsPerSecond
)

// Evaluator prices main swaps against a policy.
//
// It holds ONE dependency and that dependency is the clock, because "is a swap period open" and
// "how many allowances are left this year" are both questions about when the request is being made.
// Injecting it is what makes a quote reproducible: Phase 7 re-evaluates a held quote by passing the
// original instant, and gets the original answer whatever today is.
type Evaluator struct {
	clock clock.Clock
}

// New returns an Evaluator reading "now" from c.
//
// A nil clock is legal and is not a panic: an evaluator built without one can still price any
// request that names its own instant, and it fails with a named error on the one that does not.
// Panicking here would put a crash in a pure package, where the caller can always be told instead.
func New(c clock.Clock) Evaluator { return Evaluator{clock: c} }

// Evaluate prices the swap and judges whether it may be requested.
//
// The order is deliberate: the policy is validated before anything is priced, the request is
// validated against the RESOLVED instant, the price is computed, and the guards run last because
// affordability needs the price. A caller gets either a complete Decision or an error — never a
// half-priced quote.
func (e Evaluator) Evaluate(p Policy, r Request) (Decision, error) {
	if err := p.Validate(); err != nil {
		return Decision{}, fmt.Errorf("price a main swap: %w", err)
	}

	at, err := e.instant(r)
	if err != nil {
		return Decision{}, err
	}

	if err := r.validate(at); err != nil {
		return Decision{}, fmt.Errorf("price a main swap: %w", err)
	}

	quote, err := p.price(r, at)
	if err != nil {
		return Decision{}, fmt.Errorf("price a main swap to %s: %w", r.Target.ID, err)
	}

	blocks, err := p.guard(r, at, quote)
	if err != nil {
		return Decision{}, fmt.Errorf("guard a main swap to %s: %w", r.Target.ID, err)
	}

	return Decision{
		Quote:             quote,
		Blocks:            blocks,
		ApprovalsRequired: p.Guards.ApprovalsRequired,
	}, nil
}

// instant resolves the evaluation time: the caller's, or the injected clock's when they named none.
//
// A zero At is a caller who did not specify, not a caller who meant 1970 — the same convention a
// strategy's EffectiveAt uses, and for the same reason: an instant of zero would put every swap
// before every swap period and make the whole calendar half of this package meaningless.
func (e Evaluator) instant(r Request) (core.Micros, error) {
	at := r.At

	if at == 0 {
		if e.clock == nil {
			return 0, fmt.Errorf(
				"price a main swap: the request names no instant and the evaluator has no clock; "+
					"build it with swap.New(clock) or set Request.At: %w", ErrInvalidRequest)
		}

		at = core.FromTime(e.clock.Now())
	}

	if at <= 0 {
		return 0, fmt.Errorf(
			"price a main swap: the evaluation instant is %d, at or before the epoch; no swap period "+
				"and no allowance period can be resolved against it: %w", at, ErrInvalidRequest)
	}

	return at, nil
}

// price walks the rule stack top to bottom and returns the quote with its trail.
//
// THE FIRST MATCHING FREE RULE ENDS THE WALK. Nothing after it could change a price of zero — a
// discount would subtract from nothing and a multiplier would scale nothing — so continuing would
// only add steps to the trail that changed no number, and a trail that lists rules which did nothing
// is a trail nobody can read the price out of.
func (p Policy) price(r Request, at core.Micros) (Quote, error) {
	q := Quote{
		BaseCostCp: p.BaseCostCp,
		CostCp:     p.BaseCostCp,
		// Non-nil, so an unmatched stack marshals as [] and not as null.
		Applied:     []AppliedRule{},
		EvaluatedAt: at,
	}

	for i, rule := range p.Rules {
		if !rule.Enabled {
			continue
		}

		detail, matched, err := p.match(rule, r, at)
		if err != nil {
			return Quote{}, err
		}

		if !matched {
			continue
		}

		// Validate has already rejected a kind outside the catalogue, so the effect resolves.
		effect, _ := rule.Kind.effect()
		step := AppliedRule{Kind: rule.Kind, Effect: effect, Detail: detail}

		switch effect {
		case EffectFree:
			q.CostCp = 0
			q.Free = true
			q.ConsumesAllowance = rule.Kind == RuleAnnualAllowance
			step.RunningCp = 0
			q.Applied = append(q.Applied, step)

			return q, nil

		case EffectDiscount:
			q.CostCp = discount(q.CostCp, rule.AmountCp)
			step.AmountCp = rule.AmountCp

		case EffectMultiply:
			scaled, ok := multiply(q.CostCp, rule.FactorBp)
			if !ok {
				return Quote{}, fmt.Errorf(
					"rule %d (%s) scales %d centipoints by %d bp, which does not fit in an int64: %w",
					i, rule.Kind, q.CostCp, rule.FactorBp, ErrInvalidPolicy)
			}

			q.CostCp = scaled
			step.FactorBp = rule.FactorBp
		}

		step.RunningCp = q.CostCp
		q.Applied = append(q.Applied, step)
	}

	return q, nil
}

// match reports whether one rule's condition holds, and says why in the terms the member would ask
// in. It is the only place a rule kind is turned into a question about the request.
//
// The detail is read ONLY when matched is true — the caller discards it otherwise — which is why a
// case may build the sentence before it knows the answer.
func (p Policy) match(rule Rule, r Request, at core.Micros) (detail string, matched bool, err error) {
	switch rule.Kind {
	case RuleOpenWindow:
		for _, w := range p.Windows {
			if w.covers(at) {
				return fmt.Sprintf("the %q swap period is open", w.Name), true, nil
			}
		}

		return "", false, nil

	case RuleAnnualAllowance:
		start := r.AllowanceYearStart
		if start == 0 {
			start = yearStart(at)
		}

		used := 0

		for _, u := range r.AllowanceUsedAt {
			if u >= start {
				used++
			}
		}

		return fmt.Sprintf("%d of %d free swaps used since %s", used, rule.PerYear, start),
			used < rule.PerYear, nil

	case RuleTargetLevel:
		return fmt.Sprintf("the target is level %d, at or above %d", r.Target.Level, rule.MinLevel),
			r.Target.Level >= rule.MinLevel, nil

	case RuleNeededClass:
		for _, c := range rule.Classes {
			if c == r.Target.Class {
				return fmt.Sprintf("%s is on the needed list", c), true, nil
			}
		}

		return "", false, nil

	case RuleAttendance:
		bp, err := r.attendanceOver(rule.WindowDays, fmt.Sprintf("the %s rule", rule.Kind))
		if err != nil {
			return "", false, err
		}

		return fmt.Sprintf("%d bp over %d days, at or above %d bp", bp, rule.WindowDays, rule.MinAttendanceBp),
			bp >= rule.MinAttendanceBp, nil

	case RuleReturningMain:
		return "the target has been this member's main before", r.isPreviousMain(), nil

	default:
		// Unreachable: Validate rejects a kind outside the catalogue before anything is priced. It is
		// an error rather than a false so that a kind added to the catalogue and forgotten here fails
		// loudly instead of silently never matching.
		return "", false, fmt.Errorf("rule kind %q has no condition: %w", rule.Kind, ErrInvalidPolicy)
	}
}

// guard runs the checks that decide whether the member may ask at all.
//
// THE ORDER IS FIXED and it is the order an officer would read: the two shapes that are not swaps,
// then the two the member has to wait out, then the one they can save for. A screen renders the
// first block as the headline, so a stable order is the difference between "you are on cooldown" and
// "you are 140.00 short" being the sentence a member sees for the same request on two loads.
func (p Policy) guard(r Request, at core.Micros, q Quote) ([]Block, error) {
	// Non-nil, so "no blocks" marshals as [] rather than null.
	blocks := []Block{}

	if r.CurrentMainID != "" && r.Target.ID == r.CurrentMainID {
		blocks = append(blocks, Block{
			Code:   BlockTargetIsCurrentMain,
			Reason: fmt.Sprintf("%s is already the main, so there is nothing to swap", r.Target.ID),
		})
	}

	if r.Target.Shared {
		blocks = append(blocks, Block{
			Code: BlockTargetIsShared,
			Reason: fmt.Sprintf(
				"%s is a guild character; a character several people play cannot be one person's main",
				r.Target.ID),
		})
	}

	if p.Guards.CooldownDays > 0 && r.LastSwapAt != nil {
		// Bounded by maxDays in Validate, so the conversion cannot overflow. The comparison subtracts
		// rather than adding because the request has already been validated as at or after the last
		// swap, which makes the difference safe where a sum near the int64 ceiling would not be.
		cooldown := core.Micros(p.Guards.CooldownDays) * microsPerDay
		if at-*r.LastSwapAt < cooldown {
			blocks = append(blocks, Block{
				Code: BlockCooldownActive,
				Reason: fmt.Sprintf("the last swap was at %s and the cooldown is %d days",
					*r.LastSwapAt, p.Guards.CooldownDays),
				ClearsAt: saturatingAdd(*r.LastSwapAt, cooldown),
			})
		}
	}

	if p.Guards.MinAttendanceBp > 0 {
		bp, err := r.attendanceOver(p.Guards.AttendanceWindowDays, "the attendance floor")
		if err != nil {
			return nil, err
		}

		if bp < p.Guards.MinAttendanceBp {
			blocks = append(blocks, Block{
				Code: BlockBelowAttendanceFloor,
				Reason: fmt.Sprintf("attendance is %d bp over %d days, below the %d bp floor to request",
					bp, p.Guards.AttendanceWindowDays, p.Guards.MinAttendanceBp),
			})
		}
	}

	// A COST OF ZERO IS NEVER UNAFFORDABLE, and the guard says so explicitly rather than relying on
	// the comparison. A member in debt whose swap is free — an open window, an unspent allowance, or
	// discounts that reached the base — owes nothing for it, and `0 > -50000` would otherwise report
	// them as short by their whole debt for a swap that costs nothing.
	if q.CostCp > 0 && q.CostCp > r.SpendableCp {
		short, ok := subCentipoints(q.CostCp, r.SpendableCp)
		if !ok {
			return nil, fmt.Errorf(
				"the shortfall between a cost of %d centipoints and a spendable balance of %d does not "+
					"fit in an int64: %w", q.CostCp, r.SpendableCp, ErrInvalidRequest)
		}

		blocks = append(blocks, Block{
			Code: BlockInsufficientPoints,
			Reason: fmt.Sprintf("the swap costs %d centipoints and %d are spendable",
				q.CostCp, r.SpendableCp),
			ShortfallCp: short,
		})
	}

	return blocks, nil
}

// discount subtracts an amount, FLOORED AT ZERO.
//
// The floor is the rule that keeps a stack of discounts from inverting the transaction. Discounts
// stack by owner decision (docs/design/10-ui-decisions.md §12), so a guild that adds a fifth
// generous discount can reach the base cost; the swap is then free, which is what they configured.
// What must not happen is a negative price, because a negative price is the guild paying a member to
// change main and there is no batch shape that means that.
func discount(running, amount core.Centipoints) core.Centipoints {
	if amount >= running {
		return 0
	}

	return running - amount
}

// multiply scales an amount by a basis-point factor, exactly, in integers.
//
// The 128-bit product is the technique ledger.Allocate and fixed_price's decay use, for the same
// reason: `amount * factorBp` overflows an int64 well inside the range of a plausible surcharge on a
// large base, a float would be a lint failure and would lose precision exactly where the money is,
// and math/big would allocate on a path that runs per quote.
//
// FLOORED, never rounded up. The only multiplier the design ships is a x2 surcharge, where flooring
// is exact; on a factor that does divide unevenly, rounding up would charge a centipoint the policy
// did not ask for, and it would charge it to the member.
//
// Preconditions, both held by Validate and by the walk: amount is not negative (the cost is floored
// at zero) and factorBp is positive.
func multiply(amount core.Centipoints, factorBp int64) (scaled core.Centipoints, ok bool) {
	if amount == 0 || factorBp == 0 {
		return 0, true
	}

	hi, lo := bits.Mul64(uint64(amount), uint64(factorBp))

	// Div64 PANICS when the quotient would not fit in 64 bits, which is exactly hi >= the divisor.
	// Checking it here turns an overflow into the refusal the caller can report.
	if hi >= basisPointsWhole {
		return 0, false
	}

	q, _ := bits.Div64(hi, lo, basisPointsWhole)
	if q > math.MaxInt64 {
		return 0, false
	}

	return core.Centipoints(q), true
}

// subCentipoints subtracts, reporting overflow rather than wrapping. A member in deep debt and a
// large cost is the shape that would wrap, and a wrapped shortfall would render as a member who is
// somehow owed points by the price.
func subCentipoints(a, b core.Centipoints) (diff core.Centipoints, ok bool) {
	diff = a - b
	if (b < 0 && diff < a) || (b > 0 && diff > a) {
		return 0, false
	}

	return diff, true
}

// saturatingAdd is a + b for a non-negative b, pinned to the largest representable instant instead of
// wrapping. Reaching it needs a request dated around the year 294 000; reporting "the far future" is
// a better answer for a cooldown that cannot be represented than a clears-at back in 1970.
func saturatingAdd(a, b core.Micros) core.Micros {
	if sum := a + b; sum >= a {
		return sum
	}

	return math.MaxInt64
}

// yearStart returns 1 January 00:00:00.000000 UTC of the instant's year — the default allowance
// period boundary, the design's "resets 1 Jan".
//
// It SUBTRACTS THE ELAPSED PART OF THE YEAR rather than constructing a date, which is what keeps
// this package free of a direct `time` import (see arch_test.go) while staying exact: Micros are UTC
// by contract, UTC has no DST, and Go's clock has no leap seconds, so the elapsed micros since the
// year began are a plain sum of the day, hour, minute and second fields. Leap years need no special
// case because YearDay already counts them.
func yearStart(at core.Micros) core.Micros {
	t := at.Time()

	elapsedSeconds := int64(t.YearDay()-1)*secondsPerDay +
		int64(t.Hour())*60*60 +
		int64(t.Minute())*60 +
		int64(t.Second())

	return at - core.Micros(elapsedSeconds*microsPerSecond+int64(t.Nanosecond())/nanosPerMicro)
}
