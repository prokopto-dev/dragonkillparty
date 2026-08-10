package strategy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The shapes a strategy proposes and the ledger commits. Phase 0 PR 10a.
//
// The division this file exists to hold up: `internal/strategy` PROPOSES, `internal/ledger`
// VALIDATES AND COMMITS (.claude/rules/ledger-and-strategy.md). A strategy is a pure function that
// returns a BatchProposal; it never touches the database and it never decides whether its own
// proposal is legal. That split is what lets a guild configure its own rules without being able to
// corrupt the ledger — a misconfigured strategy produces a rejected proposal, not a wrong balance.
//
// Everything here is DATA, and that is deliberate rather than minimal. The Invariant type below is a
// descriptor, not an interface with a Check method, because checking "does this batch push an
// account below its floor?" requires reading a balance, and a package that can read a balance is a
// package that imports internal/store — which law 3 forbids. So a strategy DECLARES which rules
// constrain it and the ledger's engine, which is inside the transaction and holds the Queries,
// executes them. Both halves are testable in isolation and neither can be skipped.
//
// PURITY (law 3), enforced by PURE001/PURE002/CLOCK001 in scripts/repo-gates.sh, by the forbidigo
// float ban in .golangci.yml, and by TestArch_Strategy_ImportsNoStore in this package:
//
//   - no internal/store, transitively;
//   - no wall clock — a Clock is injected and its Now() lands in EffectiveAt;
//   - no math/rand — a seeded Rng is injected and its Seed() lands in RngSeed;
//   - no float32/float64 — point arithmetic is core.Centipoints (int64) only.
//
// The PointStrategy interface itself (PlanAward, PlanReversal, Invariants, and the fixed_price
// implementation) is Phase 0 PR 10b. This file ships only what both halves need to agree on, so
// that the ledger's commit path can be written, tested and reviewed against real shapes before the
// first strategy exists to bend them.

// Errors this package returns. Sentinels live in the owning package (.claude/rules/go-idioms.md) and
// callers compare with errors.Is.
var (
	// ErrEmptyProposal is returned by Negated for a proposal with no entries. A batch with no
	// entries cannot be reversed because it should never have been committed: entry_count carries
	// CHECK (entry_count > 0) and the ledger's BatchNonEmpty invariant rejects it at commit time.
	ErrEmptyProposal = errors.New("proposal has no entries")

	// ErrNotNegatable is returned by Negated when an entry amount is math.MinInt64, the one int64
	// with no representable negation. It is a sentinel rather than a silent clamp because a clamp
	// would produce a "reversal" that does not reverse — the exact failure P5 exists to catch.
	ErrNotNegatable = errors.New("amount has no representable negation")
)

// KindReversal is the ledger_batch.kind a Negated proposal carries.
//
// The kind vocabulary as a whole is a CHECK constraint, an OpenAPI enum and a docs page in one
// (.claude/rules/ledger-and-strategy.md), so it is not enumerated here as a Go type — adding a kind
// is a schema change, and a Go enum would offer a second place to add one. This single constant
// exists because Negated must set it and a literal in that function would be invisible to a reader
// looking for where reversals are made.
const KindReversal = "reversal"

// Rng is the seeded random source a strategy is given.
//
// It is an INTERFACE HERE and an IMPLEMENTATION IN internal/ledger, which is the whole point:
// declaring it here lets a strategy consume randomness without importing math/rand (PURE002), and
// implementing it there keeps the seeding — and therefore the reproducibility — under the control of
// the package that persists the seed.
//
// Seed() is written onto ledger_batch.rng_seed at commit time. Persisting it is what makes a replay
// byte-identical; without it, a tie-break coin flip makes the ledger unreproducible and the
// determinism property is meaningless (.claude/rules/ledger-and-strategy.md). Two Rngs constructed
// from the same seed must produce the same sequence, on every platform and every Go version that
// builds this repository — internal/ledger/rng.go explains how that is guaranteed.
type Rng interface {
	// Seed returns the seed this Rng was constructed from, for persistence onto the batch.
	Seed() int64
	// IntN returns a uniform value in [0, n). It panics for n <= 0, matching math/rand/v2.
	IntN(n int) int
	// Shuffle permutes n elements using swap, for tie-breaking among equal candidates.
	Shuffle(n int, swap func(i, j int))
}

// InvariantKind names a rule the ledger's commit-time engine knows how to check.
//
// The vocabulary is .claude/rules/ledger-and-strategy.md's table, reproduced here as constants so a
// strategy declares its constraints in a form the engine can execute rather than in a doc comment
// nobody runs. A strategy that declares no invariants is a red flag — the declared set must actually
// constrain its planner.
//
// NOT every kind below is implemented by the engine at this phase, and an unimplemented one is a
// COMMIT FAILURE rather than a silent pass. See internal/ledger/invariant.go: declaring a rule the
// engine cannot check must never look like declaring a rule that passed.
type InvariantKind string

const (
	// InvariantSumZero — the batch's entries sum to exactly zero for the given balance kind. The
	// zero-sum award's defining property: what one account pays, the others receive.
	InvariantSumZero InvariantKind = "sum_zero"

	// InvariantNonNegative — no account's balance may drop below FloorCp as a result of this batch.
	// The engine reads each affected account's current balance and adds this batch's delta.
	InvariantNonNegative InvariantKind = "non_negative"

	// InvariantLargestRemainderSumsToDebit — the credits in a split sum to exactly the debit, with
	// no minted or destroyed centipoint. Structurally the same arithmetic as SumZero and checked the
	// same way; it is a distinct name because it names a distinct MISTAKE (rounding each credit
	// independently), and an invariant failure should say which mistake was made.
	InvariantLargestRemainderSumsToDebit InvariantKind = "largest_remainder_sums_to_debit"

	// InvariantConserved — the total across all accounts for a balance kind is unchanged. NOT
	// IMPLEMENTED at this phase: it needs a pool-wide aggregate query that no shipped read provides.
	InvariantConserved InvariantKind = "conserved"

	// InvariantMonotoneNonDecreasing — a balance kind never decreases except via a reversal. EPGP's
	// GP. NOT IMPLEMENTED: epgp is conditional, not scheduled.
	InvariantMonotoneNonDecreasing InvariantKind = "monotone_non_decreasing"

	// InvariantPermutation — positions remain a bijection over the eligible list. Suicide Kings'
	// sk_position. NOT IMPLEMENTED: suicide_kings is conditional, not scheduled.
	InvariantPermutation InvariantKind = "permutation"

	// InvariantRatioPreserved — a decay batch scales two balance kinds identically, within
	// ToleranceBp. EPGP's EP/GP pair. NOT IMPLEMENTED: epgp is conditional, not scheduled.
	InvariantRatioPreserved InvariantKind = "ratio_preserved"
)

// Invariant is one declared constraint on a proposal: a descriptor the ledger's engine interprets.
//
// A single struct with per-kind fields rather than a sum type of seven structs, because the set is
// serialised into config_snapshot_json and compared field-by-field in golden tests, and a tagged
// union in Go costs an interface plus seven type assertions to express what a Kind field expresses
// directly. Fields not meaningful for a Kind are zero and the engine ignores them; the engine
// validates that the fields it DOES need are present, so a NonNegative with no floor is an error at
// commit time rather than a floor of zero somebody did not choose.
type Invariant struct {
	// Kind selects the rule. Required.
	Kind InvariantKind `json:"kind"`

	// BalanceKind scopes the rule to one balance kind ('dkp', 'ep', 'gp'). Empty means "every
	// balance kind this batch touches", which is what a single-kind strategy wants and what a
	// multi-kind one must be explicit about.
	BalanceKind string `json:"balance_kind,omitempty"`

	// FloorCp is the lower bound for InvariantNonNegative. Usually 0; a guild that permits going
	// into debt to a limit sets it negative. Ignored by every other kind.
	//
	// A POINTER, and that is the whole reason this field is not a plain Centipoints: zero is a
	// perfectly ordinary floor, so an unset value and a floor of zero are indistinguishable in a
	// value type. The engine rejects a NonNegative with no floor rather than defaulting it, because
	// "no account may go below zero" and "somebody forgot to say" must not be the same declaration.
	FloorCp *core.Centipoints `json:"floor_cp,omitempty"`

	// TotalCp is the expected conserved total for InvariantConserved. Ignored by every other kind,
	// and a pointer for the same reason FloorCp is — a conserved total of zero is meaningful.
	TotalCp *core.Centipoints `json:"total_cp,omitempty"`

	// ToleranceBp is the permitted divergence for InvariantRatioPreserved, in basis points
	// (10000 = 100%). Ignored by every other kind. A plain int64 rather than a pointer: zero means
	// "exact", which is a defensible default for a ratio check in a way that a zero floor is not for
	// a balance floor.
	ToleranceBp int64 `json:"tolerance_bp,omitempty"`

	// SecondBalanceKind is the other leg for InvariantRatioPreserved — the 'gp' to BalanceKind's
	// 'ep'. Ignored by every other kind.
	SecondBalanceKind string `json:"second_balance_kind,omitempty"`
}

// EntryProposal is one account's delta within a proposed batch: the pre-commit shape of a
// ledger_entry row, minus everything the ledger owns (the entry id, the batch id, the pool id and
// the seq, all of which are assigned at commit time and none of which a strategy may choose).
type EntryProposal struct {
	// AccountID is the balance holder. This is what a balance sums over.
	AccountID core.ULID `json:"account_id"`

	// CharacterID is ATTRIBUTION ONLY and never affects a balance. It records which character was
	// present, so that re-parenting an alt to a different main cannot move history. nil when the
	// entry is not character-attributable (a decay run, a system-account movement).
	CharacterID *core.ULID `json:"character_id,omitempty"`

	// BalanceKind is 'dkp' | 'ep' | 'gp' | ... — the ledger balance this entry moves.
	BalanceKind string `json:"balance_kind"`

	// AmountCp is the signed delta. It must be non-zero: the column carries
	// CHECK (amount_cp <> 0), because a zero entry is noise that breaks entry_count reasoning.
	AmountCp core.Centipoints `json:"amount_cp"`

	// The four provenance pointers, all optional and all attribution rather than arithmetic. Their
	// tables are Phase 3/4, so the columns are nullable TEXT with no foreign key yet.
	ItemID      *core.ULID `json:"item_id,omitempty"`
	ItemAwardID *core.ULID `json:"item_award_id,omitempty"`
	RaidID      *core.ULID `json:"raid_id,omitempty"`
	TickID      *core.ULID `json:"tick_id,omitempty"`

	// MetadataJSON is read whole and never queried into. Empty means '{}' at commit time; a fact
	// worth filtering on is a real column, not a key in here (.claude/rules/store-and-sql.md).
	MetadataJSON string `json:"metadata_json,omitempty"`
}

// BatchProposal is what a strategy returns: one atomic point-changing event, planned but not yet
// legal and not yet ordered.
//
// It carries no id, no seq, no hash and no recorded_at. Each of those is assigned by the ledger
// inside the write transaction, and a strategy that could choose any of them could choose to
// overwrite history. What it DOES carry is the reproducibility set — ConfigSnapshotJSON and RngSeed
// — because those are inputs to the plan and are meaningless if the ledger invents them afterwards.
type BatchProposal struct {
	// Kind is the ledger_batch.kind CHECK enum value: 'award', 'adjustment', 'decay', 'reversal', …
	Kind string `json:"kind"`

	// StrategyID and StrategyVersion identify the planner, so a batch planned six months ago is
	// still attributable to the exact code that planned it.
	StrategyID      string `json:"strategy_id"`
	StrategyVersion string `json:"strategy_version"`

	// ConfigSnapshotJSON is the EXACT rules in force when this was planned, captured verbatim.
	// Changing a pool's config must not change what a past batch MEANT, which is only true if the
	// config travelled with the batch. Empty means '{}' at commit time.
	ConfigSnapshotJSON string `json:"config_snapshot_json,omitempty"`

	// RngSeed is the seed of the Rng the planner consumed, or nil when it used no randomness. It is
	// persisted so a replay is byte-identical.
	RngSeed *int64 `json:"rng_seed,omitempty"`

	// Reason is the officer-supplied free text shown on the batch. Mandatory for destructive and
	// self-dealing actions; the API layer, not this struct, enforces that.
	Reason string `json:"reason,omitempty"`

	// ReversesBatchID points at the batch this one reverses, and is set only on a reversal.
	// ux_batch_reverses enforces that a batch is reversed at most once.
	ReversesBatchID *core.ULID `json:"reverses_batch_id,omitempty"`

	// EffectiveAt is GAME truth and may be backdated. The ledger stamps recorded_at (SYSTEM truth,
	// never backdated) itself; a dispute needs both and a strategy may only influence one.
	EffectiveAt core.Micros `json:"effective_at"`

	// Entries are the per-account deltas, in the planner's own order. That order is preserved by
	// Canonical, which is what makes the determinism property able to see an ordering bug.
	Entries []EntryProposal `json:"entries"`

	// Invariants is the set of rules the planner declares must hold. The ledger checks these PLUS a
	// universal set no strategy may waive.
	Invariants []Invariant `json:"invariants,omitempty"`
}

// NetAmountCp is the sum of the proposal's entry amounts: zero for a zero-sum batch, and the batch's
// net effect otherwise. It is precomputed onto the batch row so that conservation is a column
// comparison rather than an aggregate (domain model §9.7).
//
// It reports ok=false on int64 overflow rather than wrapping. A wrapped sum would satisfy a
// zero-sum check by arithmetic accident, which is the one way a conservation invariant can be
// defeated without any individual amount looking wrong.
func (p BatchProposal) NetAmountCp() (net core.Centipoints, ok bool) {
	for _, e := range p.Entries {
		sum := net + e.AmountCp

		// The standard signed-overflow test: adding two values of the same sign must not produce a
		// result of the other sign. Written out rather than via a helper because there is exactly
		// one place in the product that adds centipoints without a database behind it, and this is
		// it.
		if (e.AmountCp > 0 && sum < net) || (e.AmountCp < 0 && sum > net) {
			return 0, false
		}

		net = sum
	}

	return net, true
}

// Canonical renders the proposal as deterministic JSON — the byte string the determinism property
// hashes and a golden test compares.
//
// Determinism comes from three properties, all of which are load-bearing:
//
//   - Struct fields marshal in DECLARATION order, and Go's encoding/json sorts map keys. There are
//     no maps in these structs, so the output depends on nothing that varies between runs.
//   - Entry order is PRESERVED, not sorted. Sorting here would be the tempting choice — a batch is
//     conceptually a set — and it would be wrong, because it would hide exactly the bug this
//     function exists to catch: a planner that iterates a map and emits its entries in a different
//     order on every run would produce identical canonical bytes and pass the determinism property
//     while writing a differently-ordered batch every time. The ledger's per-pool hash chain sorts
//     the COMMITTED entries by id (domain model §9.6); that is a different hash for a different job.
//   - HTML escaping is off. encoding/json escapes <, > and & by default, which is a defence for
//     documents embedded in HTML and pure noise for a value that is hashed. Leaving it on would make
//     the canonical form of a reason containing "&" depend on a marshalling default rather than on
//     the reason.
//
// It does NOT include a trailing newline: json.Encoder adds one and it is stripped, so that
// Canonical(p) is exactly the document and not the document plus a separator.
func (p BatchProposal) Canonical() ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(p); err != nil {
		return nil, fmt.Errorf("canonicalise proposal %s/%s: %w", p.StrategyID, p.Kind, err)
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Negated returns the DEFAULT reversal of a proposal: every entry's amount negated, kind set to
// 'reversal', and ReversesBatchID pointing at the original.
//
// It is the default and it is not always right. `.claude/rules/ledger-and-strategy.md` is explicit:
// "PlanReversal's default is entry-wise negation. The default is wrong for at least one balance kind
// and you must not assume it." Suicide Kings' sk_position is an ordering rather than a quantity, so
// negating a position delta does not restore the list — everyone below the winner shifted up in the
// meantime. A strategy whose balance kind is not a plain quantity MUST override this and say so in
// its doc comment; the PointStrategy interface that lets it do so lands in PR 10b, and this function
// is what that interface's default implementation will call.
//
// The provenance pointers are carried through unchanged. A reversal of an award for an item is still
// about that item, and dropping the link would make the reversal unattributable in the statement
// view — which is where a member goes to ask why their points moved.
//
// EffectiveAt is NOT copied. A reversal is a new economic event at the time it is decided, and
// backdating it to the original's effective time would silently rewrite every intermediate balance's
// meaning. The caller supplies the reversal's own effective time; this returns it zero and the
// ledger stamps it.
//
// THE INVARIANT SET IS INHERITED MINUS InvariantNonNegative — see reversalInvariants below for the
// argument. The floor is dropped BY DEFAULT rather than by each strategy remembering to drop it,
// because the failure mode of forgetting is a mistake that is provably wrong and permanently
// unfixable, and a default whose safety depends on every future author having read one other
// package's doc comment is not a default, it is a trap with documentation.
func (p BatchProposal) Negated(reverses core.ULID) (BatchProposal, error) {
	if len(p.Entries) == 0 {
		return BatchProposal{}, fmt.Errorf("negate batch %s: %w", p.StrategyID, ErrEmptyProposal)
	}

	entries := make([]EntryProposal, len(p.Entries))

	for i, e := range p.Entries {
		if e.AmountCp == math.MinInt64 {
			return BatchProposal{}, fmt.Errorf(
				"negate entry %d for account %s: %w", i, e.AccountID, ErrNotNegatable)
		}

		entries[i] = e
		entries[i].AmountCp = -e.AmountCp
	}

	return BatchProposal{
		Kind:            KindReversal,
		StrategyID:      p.StrategyID,
		StrategyVersion: p.StrategyVersion,
		// The config snapshot travels with the reversal so that the reversal records the rules that
		// were in force for the thing being reversed, not the rules in force today.
		ConfigSnapshotJSON: p.ConfigSnapshotJSON,
		// No seed: a negation consumes no randomness. Carrying the original's seed forward would
		// assert that replaying this batch from that seed reproduces it, which is false.
		RngSeed:         nil,
		Reason:          p.Reason,
		ReversesBatchID: &reverses,
		Entries:         entries,
		Invariants:      reversalInvariants(p.Invariants),
	}, nil
}

// reversalInvariants is the set a default reversal declares: everything the original declared, minus
// every InvariantNonNegative.
//
// A FLOOR ON A REVERSAL DOES NOT PREVENT A DEBT, IT PREVENTS THE CORRECTION. The scenario is an
// ordinary Tuesday for a volunteer officer:
//
//	an officer credits a tick to the wrong raider  ->  Alice +500
//	Alice spends it on an item                     ->  Alice 0
//	the officer reverses the erroneous tick        ->  Alice -500  <- below a floor of 0
//
// With the floor inherited, the ledger REJECTS that third batch. The ledger is append-only: there is
// no UPDATE, no DELETE, and a batch carrying reverses_batch_id is the ONLY repair primitive there is
// (.claude/rules/ledger-and-strategy.md). So the guild is left with a mistake everybody can see and
// nobody can fix — a strictly worse outcome than a visible negative balance, by every measure that
// matters. The debt is the correct outcome and it is meant to be seen: Alice is at -500 because she
// spent points she was never owed, the reversal batch says why, and she works it off. What a floor
// legitimately guards is a SPEND, where the planner declares it and an overdraft is refused before
// anything is written.
//
// IT IS THE DEFAULT, and that is the whole point of it living here rather than in each planner.
// FixedPrice.PlanReversal reaches the same set by replacing rather than inheriting, and its doc
// comment carries the same argument at length — but "correct because every future author read that
// comment" is not a property the ledger can rest on. Inheriting the floor is the one mistake in this
// function whose consequence is unfixable, so it is the one the default must not make. A strategy
// that genuinely wants a floor on a reversal can still declare one; it now has to say so.
//
// EVERYTHING ELSE IS INHERITED, INCLUDING THE CONSERVATION RULES. SumZero and
// LargestRemainderSumsToDebit constrain the batch's ARITHMETIC, not its direction: a reversal is the
// exact negation of a committed batch, so it can no more mint or destroy a centipoint than the
// original could. Dropping them alongside the floor would be the easy over-correction and it would
// leave nothing checking that a reversal actually reverses — a batch returning one centipoint more
// than the original took is minting points, whichever direction it moves them in.
//
// InvariantMonotoneNonDecreasing IS DELIBERATELY NOT FILTERED, though it has the same shape:
// .claude/rules/ledger-and-strategy.md defines it as "never decreases EXCEPT VIA A REVERSAL", so
// declaring it on one is definitionally wrong. It is not filtered because the engine does not
// implement it and fails closed on a declared rule it cannot check (internal/ledger/invariant.go) —
// so the epgp author who inherits it gets a loud commit failure naming the invariant, not a silent
// trap. Filtering it here would also be designing for a strategy the rules mark as conditional
// rather than scheduled. If epgp lands, its checker and this decision land together.
//
// The result is a FRESH SLICE even when nothing is dropped. The original set is a strategy's own
// declaration and may be a package-level value it hands out for every plan; a reversal that shared
// its backing array could have a later append write through into it.
func reversalInvariants(declared []Invariant) []Invariant {
	var kept []Invariant

	for _, inv := range declared {
		if inv.Kind == InvariantNonNegative {
			continue
		}

		kept = append(kept, inv)
	}

	return kept
}
