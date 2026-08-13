package swap

import (
	"errors"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// ErrInvalidRequest reports facts the evaluator cannot price or judge.
//
// Every one of them is a MISSING OR CONTRADICTORY FACT rather than a rule failure: a rule failure is
// a block, and a member reads that on the screen. This error means the caller did not supply what
// the policy asked about — most often an attendance sample for a window a rule measures.
var ErrInvalidRequest = errors.New("main-swap request is invalid")

// Character is the target of the swap, as the roster sees it.
//
// Only the four facts the rules read. The member's name, title and race belong to the screen, and a
// field this package carries but never evaluates would be a field nobody keeps correct.
type Character struct {
	// ID is the character being swapped TO.
	ID core.ULID `json:"id"`

	// Class is lowercase snake_case, the same value the database and the wire carry.
	Class string `json:"class"`

	// Level is 1..60.
	Level int `json:"level"`

	// Shared marks a guild character — one several people play. A shared character can never be
	// somebody's main (docs/design/10-ui-decisions.md §5: its own log is ambiguous by definition, and
	// attribution runs off custody windows), so this is a block rather than a price.
	Shared bool `json:"shared"`
}

// Attendance is one measured window: "81% over the last 90 days" is {WindowDays: 90, Bp: 8100}.
//
// The caller MEASURES; this package only compares. Attendance is raids attended over raids held
// (docs/design/10-ui-decisions.md §12) and computing it needs the ledger, which a pure evaluator
// cannot read — so the samples arrive as facts, one per window any rule or guard names.
type Attendance struct {
	WindowDays int   `json:"window_days"`
	Bp         int64 `json:"bp"`
}

// Request is everything the evaluator is allowed to know.
//
// It is a SNAPSHOT of facts, which is what makes a quote reproducible: re-evaluating the same
// request at the same instant under the same policy produces the same quote, byte for byte, however
// much the ledger has moved on. Phase 7 holds the quote it got; it does not re-derive one.
type Request struct {
	// At is the instant to evaluate at — game truth. ZERO means "now", read from the injected clock,
	// exactly as a strategy's EffectiveAt does. A caller re-pricing a historical request passes the
	// original instant and gets the original answer.
	At core.Micros `json:"at,omitempty"`

	// Target is the character being swapped to.
	Target Character `json:"target"`

	// CurrentMainID is the main today. It may be EMPTY: a person with no main yet is claiming one
	// rather than swapping, and that is not an error here — it only means the "already your main"
	// block cannot fire.
	CurrentMainID core.ULID `json:"current_main_id,omitempty"`

	// PreviousMainIDs are characters that have been this person's main before, for the returning-main
	// multiplier. Order is irrelevant; the rule asks only whether the target is among them.
	PreviousMainIDs []core.ULID `json:"previous_main_ids,omitempty"`

	// AllowanceUsedAt are the instants of previous swaps that CONSUMED an annual allowance.
	//
	// Instants rather than a count, because the count is the answer to "how many fall inside the
	// current period" and that is the rule this package owns. A caller passing a precomputed count
	// would be re-implementing the calendar-year reset, and two implementations of one rule disagree
	// eventually — usually in the last week of December.
	AllowanceUsedAt []core.Micros `json:"allowance_used_at,omitempty"`

	// AllowanceYearStart is the instant the current allowance period began. ZERO means 1 January
	// 00:00 UTC of the evaluation instant's year, which is the design's "resets 1 Jan".
	//
	// It is overridable because a guild's new year starts in the guild's timezone, and resolving an
	// IANA zone means linking tzdata into a package whose entire claim is that it computes from its
	// inputs. Phase 7 holds the guild's zone; it can resolve the boundary and pass it here.
	AllowanceYearStart core.Micros `json:"allowance_year_start,omitempty"`

	// Attendance holds one sample per window any enabled rule or guard measures. A window that is
	// asked for and not supplied is an ErrInvalidRequest, never an assumed zero: an assumed zero
	// silently fails a discount the member was owed and silently passes nobody through a floor.
	Attendance []Attendance `json:"attendance,omitempty"`

	// SpendableCp is the balance the swap would be paid from, already net of holds. It may be
	// NEGATIVE — a reversal can legitimately leave a member in debt
	// (.claude/rules/ledger-and-strategy.md) — which simply means they cannot afford anything.
	SpendableCp core.Centipoints `json:"spendable_cp"`

	// LastSwapAt is when this person last swapped main, for the cooldown. NIL means never, which is
	// a different fact from "swapped at the epoch" and is why this is a pointer.
	LastSwapAt *core.Micros `json:"last_swap_at,omitempty"`
}

// validate checks the request against the instant it will be evaluated at.
//
// It takes `at` rather than reading r.At because the zero value means "the injected clock", and the
// three not-in-the-future checks below have to be made against the instant that was actually
// resolved.
func (r Request) validate(at core.Micros) error {
	if r.Target.ID == "" {
		return fmt.Errorf("the request names no target character: %w", ErrInvalidRequest)
	}

	if !isSnakeCase(r.Target.Class) {
		return fmt.Errorf(
			"target %s has class %q; classes are lowercase snake_case (\"shadow_knight\"), and a "+
				"differently-spelled class silently misses the needed-class discount: %w",
			r.Target.ID, r.Target.Class, ErrInvalidRequest)
	}

	if r.Target.Level < 1 || r.Target.Level > maxLevel {
		return fmt.Errorf("target %s is level %d, want 1..%d: %w",
			r.Target.ID, r.Target.Level, maxLevel, ErrInvalidRequest)
	}

	if err := r.validateMains(); err != nil {
		return err
	}

	if err := r.validateAllowance(at); err != nil {
		return err
	}

	if err := r.validateAttendance(); err != nil {
		return err
	}

	if r.LastSwapAt != nil {
		if *r.LastSwapAt <= 0 {
			return fmt.Errorf("last_swap_at is %d, which is not an instant; omit it for a member who "+
				"has never swapped: %w", *r.LastSwapAt, ErrInvalidRequest)
		}

		if *r.LastSwapAt > at {
			return fmt.Errorf("last_swap_at %s is after the evaluation instant %s, so the cooldown "+
				"would be measured backwards: %w", *r.LastSwapAt, at, ErrInvalidRequest)
		}
	}

	return nil
}

// validateMains checks the previous-main list.
func (r Request) validateMains() error {
	seen := make(map[core.ULID]bool, len(r.PreviousMainIDs))

	for i, id := range r.PreviousMainIDs {
		if id == "" {
			return fmt.Errorf("previous_main_ids[%d] is empty: %w", i, ErrInvalidRequest)
		}

		if seen[id] {
			return fmt.Errorf(
				"previous_main_ids names %s twice; the multiplier asks whether the target is in the "+
					"list at all, so a repeat is a list assembled from two sources rather than a "+
					"stronger claim: %w", id, ErrInvalidRequest)
		}

		seen[id] = true
	}

	return nil
}

// validateAllowance checks the recorded allowance uses.
func (r Request) validateAllowance(at core.Micros) error {
	if r.AllowanceYearStart < 0 || r.AllowanceYearStart > at {
		return fmt.Errorf(
			"allowance_year_start %s is not at or before the evaluation instant %s; a period that has "+
				"not begun contains none of the swaps it is supposed to count: %w",
			r.AllowanceYearStart, at, ErrInvalidRequest)
	}

	seen := make(map[core.Micros]bool, len(r.AllowanceUsedAt))

	for i, used := range r.AllowanceUsedAt {
		if used <= 0 {
			return fmt.Errorf("allowance_used_at[%d] is %d, which is not an instant: %w",
				i, used, ErrInvalidRequest)
		}

		if used > at {
			return fmt.Errorf(
				"allowance_used_at[%d] is %s, after the evaluation instant %s; a swap that has not "+
					"happened cannot have spent an allowance: %w", i, used, at, ErrInvalidRequest)
		}

		if seen[used] {
			return fmt.Errorf("allowance_used_at names %s twice, which would spend one allowance "+
				"twice: %w", used, ErrInvalidRequest)
		}

		seen[used] = true
	}

	return nil
}

// validateAttendance checks the samples: one window each, in range.
func (r Request) validateAttendance() error {
	seen := make(map[int]bool, len(r.Attendance))

	for i, a := range r.Attendance {
		if a.WindowDays < 1 || a.WindowDays > maxDays {
			return fmt.Errorf("attendance[%d] measures %d days, want 1..%d: %w",
				i, a.WindowDays, maxDays, ErrInvalidRequest)
		}

		if a.Bp < 0 || a.Bp > basisPointsWhole {
			return fmt.Errorf("attendance[%d] is %d bp, want 0..%d (10000 is 100%%): %w",
				i, a.Bp, basisPointsWhole, ErrInvalidRequest)
		}

		if seen[a.WindowDays] {
			return fmt.Errorf(
				"attendance holds two samples for the %d-day window; one window has one measurement, "+
					"and which of two a rule read would depend on list order: %w",
				a.WindowDays, ErrInvalidRequest)
		}

		seen[a.WindowDays] = true
	}

	return nil
}

// attendanceOver returns the sample for a window, or an error naming the window that is missing.
//
// The error is deliberate and is the whole reason attendance is not a plain int on the request: a
// rule that measures 90 days and a guard that measures 30 are two different questions, and a caller
// that supplied only one of them must be told which one it forgot rather than have the other
// silently substituted.
func (r Request) attendanceOver(days int, asked string) (int64, error) {
	for _, a := range r.Attendance {
		if a.WindowDays == days {
			return a.Bp, nil
		}
	}

	return 0, fmt.Errorf(
		"%s measures attendance over %d days and the request carries no sample for that window; an "+
			"absent measurement is not zero attendance: %w", asked, days, ErrInvalidRequest)
}

// isPreviousMain reports whether the target has been this person's main before.
func (r Request) isPreviousMain() bool {
	for _, id := range r.PreviousMainIDs {
		if id == r.Target.ID {
			return true
		}
	}

	return false
}
