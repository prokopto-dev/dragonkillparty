package swap

import (
	"errors"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// ErrInvalidPolicy reports a policy document that cannot be evaluated.
//
// It is a REFUSAL rather than a fallback to the defaults, for the reason fixed_price's config parser
// records: a policy that silently ran the shipped defaults would price swaps under rules nobody
// chose, and the guild would discover that months later from a member's statement.
var ErrInvalidPolicy = errors.New("main-swap policy is invalid")

// RuleKind is the closed catalogue of conditions a policy may price on.
//
// Lowercase snake_case, because these values reach the wire and the settings form when Phase 7 gives
// the policy an API (canonical §5). The catalogue is CLOSED: an unrecognised kind fails validation
// instead of being skipped, since a rule the evaluator does not understand is a discount the member
// was promised and never got.
type RuleKind string

// The six rules of docs/design/10-ui-decisions.md §4, in the order that document lists them.
const (
	// RuleOpenWindow — an open swap period covers the moment of the request. Free, and it does NOT
	// consume an annual allowance: a guild that opens a window is inviting swaps, and charging an
	// allowance for one would make the window cost something after all.
	RuleOpenWindow RuleKind = "open_window"

	// RuleAnnualAllowance — the member has a free swap left in the current allowance period. Free,
	// and it CONSUMES one. The period is a calendar year by default; see Request.AllowanceYearStart.
	RuleAnnualAllowance RuleKind = "annual_allowance"

	// RuleTargetLevel — the target character is at or above a level. A 60 is raid-ready on the night
	// it swaps, so the guild pays less for the swap it wanted anyway.
	RuleTargetLevel RuleKind = "target_level"

	// RuleNeededClass — the target class is on the needed list. The list is the guild's lever over
	// which swaps are cheap, and it lives on the rule rather than on the policy so that a second
	// class-priced rule stays possible without two lists to keep in step.
	RuleNeededClass RuleKind = "needed_class"

	// RuleAttendance — attendance over a window is at or above a floor. Off by default in the
	// design, which argues that it rewards the same people twice.
	RuleAttendance RuleKind = "attendance"

	// RuleReturningMain — the target character was a previous main. A MULTIPLIER, not a discount:
	// the design's x2, to discourage swapping back and forth for a tier. Off by default.
	RuleReturningMain RuleKind = "returning_main"
)

// Effect is what a matching rule does to the running cost. It is a property of the RULE KIND, not a
// per-policy choice: a guild may set how big the level-60 discount is, and may turn it off, but it
// may not configure "level 60" to multiply. Binding the shape to the kind is what keeps the six rows
// of the policy screen legible as the six rules the design named.
type Effect string

const (
	// EffectFree sets the cost to zero and ENDS the evaluation. First match wins.
	EffectFree Effect = "free"

	// EffectDiscount subtracts a centipoint amount, floored at zero.
	EffectDiscount Effect = "discount"

	// EffectMultiply scales the running cost by a basis-point factor.
	EffectMultiply Effect = "multiply"
)

// effect returns the kind's effect, and false for a kind outside the catalogue.
func (k RuleKind) effect() (Effect, bool) {
	switch k {
	case RuleOpenWindow, RuleAnnualAllowance:
		return EffectFree, true
	case RuleTargetLevel, RuleNeededClass, RuleAttendance:
		return EffectDiscount, true
	case RuleReturningMain:
		return EffectMultiply, true
	default:
		return "", false
	}
}

// ruleField names one optional parameter of a Rule.
//
// The names are the JSON names, so a validation failure quotes the key the officer would edit rather
// than a Go field name they have never seen.
type ruleField string

const (
	fieldAmountCp        ruleField = "amount_cp"
	fieldFactorBp        ruleField = "factor_bp"
	fieldPerYear         ruleField = "per_year"
	fieldMinLevel        ruleField = "min_level"
	fieldMinAttendanceBp ruleField = "min_attendance_bp"
	fieldWindowDays      ruleField = "window_days"
	fieldClasses         ruleField = "classes"
)

// Rule is one row of the policy's ordered stack.
//
// It is a UNION over the six kinds: each kind uses a named subset of the parameters below and
// validation rejects any other one being set. That is fixed_price's `additionalProperties: false`
// lesson at the struct level — a min_level quietly ignored on a needed_class rule is an officer who
// thinks they restricted a discount and did not.
type Rule struct {
	// Kind selects the condition and, with it, the effect.
	Kind RuleKind `json:"kind"`

	// Enabled is the policy screen's toggle. A disabled rule never matches and never appears in a
	// quote's trail — but it is still VALIDATED, because a parameter that only breaks on the day
	// somebody flips the toggle is a parameter that breaks during a raid.
	Enabled bool `json:"enabled"`

	// AmountCp is the discount, as a positive number of centipoints to subtract. Discount kinds only.
	AmountCp core.Centipoints `json:"amount_cp,omitempty"`

	// FactorBp is the multiplier in basis points: 20000 is x2. Multiply kinds only.
	FactorBp int64 `json:"factor_bp,omitempty"`

	// PerYear is how many free swaps an allowance period grants. RuleAnnualAllowance only.
	PerYear int `json:"per_year,omitempty"`

	// MinLevel is the level at or above which the rule matches. RuleTargetLevel only.
	MinLevel int `json:"min_level,omitempty"`

	// MinAttendanceBp is the attendance floor in basis points: 8000 is 80%. RuleAttendance only.
	MinAttendanceBp int64 `json:"min_attendance_bp,omitempty"`

	// WindowDays is the attendance window the floor is measured over. RuleAttendance only.
	WindowDays int `json:"window_days,omitempty"`

	// Classes is the needed list, lowercase snake_case. RuleNeededClass only. An EMPTY list is legal
	// and means the discount never matches: a fresh install has named no needed class, and that is a
	// guild that has not decided yet rather than a broken policy.
	Classes []string `json:"classes,omitempty"`
}

// Window is a period during which a swap is free. Half-open: [StartsAt, EndsAt).
//
// Half-open so that two adjacent windows cannot both cover the instant they share. A closed interval
// would make the boundary microsecond ambiguous, and "which window paid for this swap" is a question
// the quote's trail has to answer with one name.
type Window struct {
	Name     string      `json:"name"`
	StartsAt core.Micros `json:"starts_at"`
	EndsAt   core.Micros `json:"ends_at"`
}

// covers reports whether the window contains the instant.
func (w Window) covers(at core.Micros) bool {
	return at >= w.StartsAt && at < w.EndsAt
}

// Guards are the checks that decide whether a member may ASK, and they are deliberately separate
// from cost (docs/design/10-ui-decisions.md §4). A member can be able to afford a swap and still be
// inside their cooldown; a member below the attendance floor is not blocked by a price.
type Guards struct {
	// CooldownDays is the minimum gap between swaps. 0 disables the guard. Measured as exact 24-hour
	// days from the previous swap, not as calendar days — see Request.LastSwapAt.
	CooldownDays int `json:"cooldown_days"`

	// MinAttendanceBp is the floor to request at all: the design's 50%, which is 5000. 0 disables the
	// guard, and disabling it also means no attendance sample is required for it.
	MinAttendanceBp int64 `json:"min_attendance_bp"`

	// AttendanceWindowDays is the window the floor above is measured over. It is a DIFFERENT window
	// from the attendance discount's: the mockup's guard is 50% over 30 days while the discount is
	// 80% over 90 days, and collapsing them into one number would silently re-scope one of them.
	AttendanceWindowDays int `json:"attendance_window_days"`

	// ApprovalsRequired is how many officers must sign off. It travels through Decision so the screen
	// can render "1 of 2"; this package never counts approvals, because an approval is not a price.
	ApprovalsRequired int `json:"approvals_required"`
}

// Policy is the whole configurable document: what a swap costs, when it is free, and who may ask.
//
// It carries JSON tags because Phase 7 snapshots it beside the quote it produced, for the same
// reason ledger_batch carries config_snapshot_json: changing a rule must never re-price a swap that
// was already quoted (docs/design/10-ui-decisions.md §4).
type Policy struct {
	// BaseCostCp is what a swap costs before any rule applies.
	BaseCostCp core.Centipoints `json:"base_cost_cp"`

	// Rules is the ordered stack. Order is semantic: see the package doc.
	Rules []Rule `json:"rules"`

	// Windows are the open swap periods RuleOpenWindow matches against. The FIRST one covering the
	// instant names the trail entry.
	Windows []Window `json:"windows"`

	// Guards are the request-time checks.
	Guards Guards `json:"guards"`
}

// The bounds every magnitude in a policy is checked against.
const (
	// basisPointsWhole is 100% in basis points. Ratios are integers here as everywhere else: a
	// percentage held as a float is how a price drifts.
	basisPointsWhole = 10_000

	// maxLevel is Project 1999's level cap. A rule requiring 61 could never match, so it is a typo
	// rather than a policy.
	maxLevel = 60

	// maxDays bounds every day-count in the document at ten years. It is not a judgement about what a
	// guild might want; it is what keeps a cooldown's arithmetic (days x 86 400 000 000 µs) far below
	// the int64 ceiling without a checked multiply at every use.
	maxDays = 3_650
)

// DefaultPolicy returns the shipped default: docs/design/10-ui-decisions.md §4's table, verbatim.
//
// A fresh guild runs this until an officer edits it. Rules 5 and 6 ship DISABLED and configured —
// the design turns them off, and shipping them with their magnitudes already set is what makes the
// policy screen's toggle a one-click decision rather than a form to fill in.
//
// The needed-class list ships EMPTY. The mockup shows Cleric and Enchanter, but that is one guild's
// roster gap and not a default anybody else should inherit; an empty list means the rule matches
// nothing, which is the honest state of a guild that has not said which classes it needs.
func DefaultPolicy() Policy {
	return Policy{
		BaseCostCp: 75_000, // 750.00
		Rules: []Rule{
			{Kind: RuleOpenWindow, Enabled: true},
			{Kind: RuleAnnualAllowance, Enabled: true, PerYear: 2},
			{Kind: RuleTargetLevel, Enabled: true, AmountCp: 25_000, MinLevel: 60},
			{Kind: RuleNeededClass, Enabled: true, AmountCp: 30_000},
			{Kind: RuleAttendance, Enabled: false, AmountCp: 10_000, MinAttendanceBp: 8_000, WindowDays: 90},
			{Kind: RuleReturningMain, Enabled: false, FactorBp: 2 * basisPointsWhole},
		},
		Guards: Guards{
			CooldownDays:         90,
			MinAttendanceBp:      5_000,
			AttendanceWindowDays: 30,
			ApprovalsRequired:    2,
		},
	}
}

// Validate reports the first thing wrong with the policy, or nil.
//
// FIRST rather than all of them: the caller is a settings form that highlights one field, and a
// joined list of six failures reads worse than the one an officer has to fix to get to the next one.
// The order below is the document's own — base, rules, windows, guards — so the failure a reader
// gets is the earliest one in the thing they are looking at.
func (p Policy) Validate() error {
	if p.BaseCostCp < 0 {
		return fmt.Errorf("base_cost_cp is %d, which would pay a member to swap: %w",
			p.BaseCostCp, ErrInvalidPolicy)
	}

	seen := make(map[RuleKind]bool, len(p.Rules))

	for i, r := range p.Rules {
		if seen[r.Kind] {
			return fmt.Errorf(
				"rule %d repeats kind %q; a kind appears at most once, because two copies of one rule "+
					"would apply their effect twice and the second would be invisible in the trail: %w",
				i, r.Kind, ErrInvalidPolicy)
		}

		seen[r.Kind] = true

		if err := r.validate(i); err != nil {
			return err
		}
	}

	for i, w := range p.Windows {
		if err := w.validate(i); err != nil {
			return err
		}
	}

	return p.Guards.validate()
}

// validate checks one rule: that its kind is in the catalogue, that it sets only the parameters its
// kind uses, and that each of those is in range.
func (r Rule) validate(index int) error {
	effect, ok := r.Kind.effect()
	if !ok {
		return fmt.Errorf(
			"rule %d has kind %q, which is not one of the six main-swap rules; an unknown kind is a "+
				"discount the member was quoted and never received, so it fails rather than being "+
				"skipped: %w", index, r.Kind, ErrInvalidPolicy)
	}

	if err := r.checkFields(index); err != nil {
		return err
	}

	switch r.Kind {
	case RuleOpenWindow:
		return nil
	case RuleAnnualAllowance:
		if r.PerYear < 1 {
			return fmt.Errorf("rule %d (%s) grants %d free swaps per period, so it can never match: %w",
				index, r.Kind, r.PerYear, ErrInvalidPolicy)
		}
	case RuleTargetLevel:
		if r.MinLevel < 1 || r.MinLevel > maxLevel {
			return fmt.Errorf("rule %d (%s) has min_level %d, want 1..%d: %w",
				index, r.Kind, r.MinLevel, maxLevel, ErrInvalidPolicy)
		}
	case RuleNeededClass:
		if err := checkClasses(index, r.Classes); err != nil {
			return err
		}
	case RuleAttendance:
		if r.MinAttendanceBp < 1 || r.MinAttendanceBp > basisPointsWhole {
			return fmt.Errorf("rule %d (%s) has min_attendance_bp %d, want 1..%d: %w",
				index, r.Kind, r.MinAttendanceBp, basisPointsWhole, ErrInvalidPolicy)
		}

		if r.WindowDays < 1 || r.WindowDays > maxDays {
			return fmt.Errorf("rule %d (%s) measures attendance over %d days, want 1..%d: %w",
				index, r.Kind, r.WindowDays, maxDays, ErrInvalidPolicy)
		}
	case RuleReturningMain:
	}

	return r.checkMagnitude(index, effect)
}

// checkMagnitude applies the bound that belongs to the EFFECT rather than to the kind: a discount of
// nothing and a factor of nothing are both rules that do nothing, which is a policy somebody meant
// to configure and did not finish.
func (r Rule) checkMagnitude(index int, effect Effect) error {
	switch effect {
	case EffectDiscount:
		if r.AmountCp <= 0 {
			return fmt.Errorf(
				"rule %d (%s) discounts %d centipoints; a discount is a positive amount to subtract, "+
					"and a rule that subtracts nothing is one nobody finished configuring: %w",
				index, r.Kind, r.AmountCp, ErrInvalidPolicy)
		}
	case EffectMultiply:
		if r.FactorBp <= 0 {
			return fmt.Errorf(
				"rule %d (%s) has factor_bp %d; the factor is basis points and must be positive "+
					"(20000 is x2, 10000 leaves the price alone): %w",
				index, r.Kind, r.FactorBp, ErrInvalidPolicy)
		}
	case EffectFree:
	}

	return nil
}

// checkFields rejects a parameter set on a kind that does not read it.
func (r Rule) checkFields(index int) error {
	used := r.Kind.fields()

	for _, set := range r.setFields() {
		if !containsField(used, set) {
			return fmt.Errorf(
				"rule %d (%s) sets %q, which that kind does not read; the parameter would be silently "+
					"ignored, and a discount an officer believes they restricted is worse than a "+
					"rejected policy: %w", index, r.Kind, set, ErrInvalidPolicy)
		}
	}

	return nil
}

// fields is the closed set of parameters each kind reads.
func (k RuleKind) fields() []ruleField {
	switch k {
	case RuleOpenWindow:
		return nil
	case RuleAnnualAllowance:
		return []ruleField{fieldPerYear}
	case RuleTargetLevel:
		return []ruleField{fieldAmountCp, fieldMinLevel}
	case RuleNeededClass:
		return []ruleField{fieldAmountCp, fieldClasses}
	case RuleAttendance:
		return []ruleField{fieldAmountCp, fieldMinAttendanceBp, fieldWindowDays}
	case RuleReturningMain:
		return []ruleField{fieldFactorBp}
	default:
		return nil
	}
}

// setFields lists the parameters this rule actually sets, in declaration order so that a policy with
// two stray parameters names the same one on every run.
func (r Rule) setFields() []ruleField {
	var set []ruleField

	for _, f := range []struct {
		name ruleField
		on   bool
	}{
		{fieldAmountCp, r.AmountCp != 0},
		{fieldFactorBp, r.FactorBp != 0},
		{fieldPerYear, r.PerYear != 0},
		{fieldMinLevel, r.MinLevel != 0},
		{fieldMinAttendanceBp, r.MinAttendanceBp != 0},
		{fieldWindowDays, r.WindowDays != 0},
		{fieldClasses, len(r.Classes) > 0},
	} {
		if f.on {
			set = append(set, f.name)
		}
	}

	return set
}

// containsField is slices.Contains, spelled out to keep the field vocabulary local.
func containsField(in []ruleField, want ruleField) bool {
	for _, f := range in {
		if f == want {
			return true
		}
	}

	return false
}

// checkClasses validates the needed list: lowercase snake_case, no duplicates, no blanks.
//
// The format is checked rather than normalised, and that is the point. Normalising "Cleric" to
// "cleric" would make the policy agree with a request that spelled it either way — and would just as
// happily accept "Clerik", which then matches nothing and looks like a rule that is simply never
// hit. Enum values are lowercase snake_case everywhere (canonical §5); a value that is not is a typo
// worth naming at the edit rather than a mystery worth debugging at the quote.
func checkClasses(index int, classes []string) error {
	seen := make(map[string]bool, len(classes))

	for i, c := range classes {
		if !isSnakeCase(c) {
			return fmt.Errorf(
				"rule %d (%s) lists class %q at position %d; classes are lowercase snake_case "+
					"(\"shadow_knight\"), the same value the database and the wire carry: %w",
				index, RuleNeededClass, c, i, ErrInvalidPolicy)
		}

		if seen[c] {
			return fmt.Errorf("rule %d (%s) lists class %q twice: %w",
				index, RuleNeededClass, c, ErrInvalidPolicy)
		}

		seen[c] = true
	}

	return nil
}

// isSnakeCase reports whether s is a non-empty lowercase snake_case token: it starts with a letter
// and holds only lowercase letters, digits and single interior underscores.
//
// Hand-rolled rather than a regexp because it runs inside validation on every evaluation and a
// compiled pattern would either be package-level mutable state or a recompile per call.
func isSnakeCase(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_':
			// A leading underscore is already excluded by the first-byte check above; a trailing or
			// doubled one is what this catches.
			if i == len(s)-1 || s[i+1] == '_' {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// validate checks one open swap period.
func (w Window) validate(index int) error {
	if w.Name == "" {
		return fmt.Errorf(
			"window %d has no name; the name is what the quote's trail reports as the reason a swap "+
				"was free, and \"an unnamed window\" is not an answer to a member asking why: %w",
			index, ErrInvalidPolicy)
	}

	if w.StartsAt <= 0 {
		return fmt.Errorf("window %q starts at %d, which is not an instant: %w",
			w.Name, w.StartsAt, ErrInvalidPolicy)
	}

	if w.EndsAt <= w.StartsAt {
		return fmt.Errorf(
			"window %q ends at %d, at or before its start %d; it is half-open [start, end), so a "+
				"window that ends where it starts covers no instant at all: %w",
			w.Name, w.EndsAt, w.StartsAt, ErrInvalidPolicy)
	}

	return nil
}

// validate checks the guards.
func (g Guards) validate() error {
	if g.CooldownDays < 0 || g.CooldownDays > maxDays {
		return fmt.Errorf("guards.cooldown_days is %d, want 0..%d (0 disables the cooldown): %w",
			g.CooldownDays, maxDays, ErrInvalidPolicy)
	}

	if g.MinAttendanceBp < 0 || g.MinAttendanceBp > basisPointsWhole {
		return fmt.Errorf("guards.min_attendance_bp is %d, want 0..%d (0 disables the floor): %w",
			g.MinAttendanceBp, basisPointsWhole, ErrInvalidPolicy)
	}

	if g.AttendanceWindowDays < 0 || g.AttendanceWindowDays > maxDays {
		return fmt.Errorf("guards.attendance_window_days is %d, want 0..%d: %w",
			g.AttendanceWindowDays, maxDays, ErrInvalidPolicy)
	}

	// A floor with no window is the configuration that would quietly measure attendance over nothing.
	// The inverse — a window with no floor — is harmless and stays legal: it is a guild that turned
	// the floor off and kept the number it had.
	if g.MinAttendanceBp > 0 && g.AttendanceWindowDays < 1 {
		return fmt.Errorf(
			"guards.min_attendance_bp is %d but attendance_window_days is %d; a floor needs a window "+
				"to be measured over: %w", g.MinAttendanceBp, g.AttendanceWindowDays, ErrInvalidPolicy)
	}

	if g.ApprovalsRequired < 0 {
		return fmt.Errorf("guards.approvals_required is %d: %w", g.ApprovalsRequired, ErrInvalidPolicy)
	}

	return nil
}
