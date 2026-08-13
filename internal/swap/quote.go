package swap

import "github.com/prokopto-dev/dragonkillparty/internal/core"

// AppliedRule is one step of the trail: a rule that matched, what it did, and what the cost was
// afterwards.
//
// THE RUNNING COST IS THE POINT. docs/design/11-ui-backend-contract.md §6 asks for a quote that is
// "honest about which discounts applied and in what order", and a list of names is not that — the
// order only becomes checkable when each step carries the number it produced. With it, a member
// disputing a price and an officer reading the same trail see the arithmetic, not the total.
type AppliedRule struct {
	// Kind is the rule that matched.
	Kind RuleKind `json:"kind"`

	// Effect is what it did, copied from the kind so a reader of the trail alone can follow it.
	Effect Effect `json:"effect"`

	// AmountCp is the discount subtracted. Discount steps only.
	AmountCp core.Centipoints `json:"amount_cp,omitempty"`

	// FactorBp is the multiplier applied. Multiply steps only.
	FactorBp int64 `json:"factor_bp,omitempty"`

	// Detail says WHY this rule matched, in the terms the member would ask in: which window, which
	// class, how many allowances were left. Integers only — a percentage rendered as a float here
	// would be the one float in the money path.
	Detail string `json:"detail"`

	// RunningCp is the cost after this step.
	RunningCp core.Centipoints `json:"running_cp"`
}

// Quote is the price and the reasoning behind it.
//
// Phase 7 HOLDS one of these at request time and never re-prices it: changing a rule afterwards must
// not change what a pending swap costs (docs/design/10-ui-decisions.md §4), which is the same
// argument ledger_batch.config_snapshot_json makes about a strategy's config.
type Quote struct {
	// BaseCostCp is where the arithmetic started.
	BaseCostCp core.Centipoints `json:"base_cost_cp"`

	// CostCp is what the swap costs. Never negative.
	CostCp core.Centipoints `json:"cost_cp"`

	// Free reports that a FIRST-MATCH FREE rule matched — not merely that the cost reached zero.
	//
	// The distinction is load-bearing: a swap made free by an open window costs nothing and spends
	// nothing, while a stack of discounts that happens to reach zero is still an ordinary priced swap
	// that reached a price of zero. Only the first kind is what the design calls free.
	Free bool `json:"free"`

	// ConsumesAllowance reports that this swap, if approved, spends one of the member's annual free
	// swaps. True only for RuleAnnualAllowance: an open swap period is free AND does not consume one.
	ConsumesAllowance bool `json:"consumes_allowance"`

	// Applied is the ordered trail. Never nil — an empty trail marshals as [], because a client
	// reading `null` has to special-case a case that never means anything different.
	Applied []AppliedRule `json:"applied"`

	// EvaluatedAt is the instant the quote was priced at, which is also the instant that decided
	// which window was open and which allowance period was current.
	EvaluatedAt core.Micros `json:"evaluated_at"`
}

// BlockCode is why a request may not proceed. Lowercase snake_case, one code per reason, closed —
// the same shape every other enum in the system has (canonical §5), so that Phase 7 can put these on
// the wire without renaming them.
type BlockCode string

const (
	// BlockTargetIsCurrentMain — the target is already the main. Not a swap.
	BlockTargetIsCurrentMain BlockCode = "target_is_current_main"

	// BlockTargetIsShared — the target is a guild character. A character several people play cannot
	// be one person's main (docs/design/10-ui-decisions.md §5).
	BlockTargetIsShared BlockCode = "target_is_shared"

	// BlockCooldownActive — the member swapped too recently.
	BlockCooldownActive BlockCode = "cooldown_active"

	// BlockBelowAttendanceFloor — the member is below the attendance floor to request at all.
	BlockBelowAttendanceFloor BlockCode = "below_attendance_floor"

	// BlockInsufficientPoints — the quote costs more than the member can spend.
	BlockInsufficientPoints BlockCode = "insufficient_points"
)

// Block is one reason a request is refused, with the number the screen needs to render it.
//
// The two numeric fields are per-code and zero otherwise, which is why they are documented on the
// code rather than split into separate types: "short 640.00" and "clears on the 14th" are the two
// blocks a member can do something about, and a screen that had to join a code to a lookup table to
// find its number would be a screen that eventually renders the wrong one.
type Block struct {
	// Code is the machine-readable reason.
	Code BlockCode `json:"code"`

	// Reason is the human sentence, in the officer's terms.
	Reason string `json:"reason"`

	// ShortfallCp is how far short the member is. BlockInsufficientPoints only; zero otherwise.
	ShortfallCp core.Centipoints `json:"shortfall_cp,omitempty"`

	// ClearsAt is when the cooldown expires. BlockCooldownActive only; zero otherwise.
	ClearsAt core.Micros `json:"clears_at,omitempty"`
}

// Decision is the whole answer: what it costs, whether they may ask, and how many officers must
// agree if they do.
//
// One value rather than two calls, because the affordability block needs the price — computing them
// separately would mean pricing twice and hoping both runs saw the same policy.
type Decision struct {
	// Quote is the price and its trail. It is computed even when the request is blocked: the member's
	// character page shows the cost of a swap they cannot yet make, which is how they know what to
	// save for.
	Quote Quote `json:"quote"`

	// Blocks are the reasons this request may not proceed, in a fixed order (see evaluate.go). Never
	// nil; empty means the member may ask.
	Blocks []Block `json:"blocks"`

	// ApprovalsRequired is the policy's officer count, carried through so the screen can say "1 of
	// 2". Counting actual approvals is Phase 7's state machine, not a pricing question.
	ApprovalsRequired int `json:"approvals_required"`
}

// Allowed reports whether the member may submit this request.
func (d Decision) Allowed() bool { return len(d.Blocks) == 0 }
