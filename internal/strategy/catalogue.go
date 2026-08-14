package strategy

import (
	"fmt"
	"sort"
)

// The in-tree registry: every shipped strategy, in one list. Phase 1, #193.
//
// WHY THIS IS A GO LIST AND NOT A DATABASE CHECK. `db/schema.hcl` says it outright at the pool's rule
// columns: they are "not a CHECK enum: the set of strategies is code-defined and grows per PR, and
// the strategy package validates it — a CHECK here would make every new strategy a schema change".
// That is the opposite of `ledger_batch.kind`, where the vocabulary IS a CHECK generated from
// `internal/ledger/kinds` (canonical §5), and the difference is deliberate: a batch kind is a value
// the database has to reason about, and a strategy id is a pointer to code. So the registration a new
// strategy needs is an entry here, and ByID is the validation the schema comment promises exists.
//
// EACH ENTRY ALSO CARRIES WHICH OF A POOL'S THREE QUESTIONS IT ANSWERS, as PointStrategy.RuleKind
// (ADR-0026). A pool holds one rule per question, so a strategy whose kind is not one of the three is
// a strategy PoolConfig.Resolve can put in no slot — TestCatalogue_EveryStrategy_IsWellFormed rejects
// it, and TestCatalogue_EveryQuestion_HasAnAnswer rejects a release in which some question has no
// shipped answer at all.
//
// The add-strategy skill's step 8 still describes `make gen` writing an OpenAPI enum and a
// pool-settings form from this list. Neither generator exists yet — issue #212, deferred to Phase 2
// because a generated enum for a field no operation carries has no output to verify.
//
// A FUNCTION RETURNING A FRESH SLICE, never a package-level var. `.claude/rules/go-idioms.md` bans
// package-level mutable state, and a shared slice is one append in a test away from an intermittent
// failure under -shuffle=on. internal/authz.Catalogue() and kinds.BatchKinds() are the same shape for
// the same reason.
//
// THE ORDER IS THE CATALOGUE'S ORDER in docs/guides/choosing-a-dkp-system.md — earn, then spend, then
// over-time — rather than alphabetical, because that is the order a guild reads them in when choosing
// a system, and a settings form generated from this list should present them the way the guide
// explains them.

// Catalogue is every strategy this binary ships, in the order a guild meets them.
//
// A strategy that is not here is unreachable: nothing else in the product turns one of a pool's rule
// ids into a planner. Adding one is one line plus the file it names, and TestCatalogue_* is what
// refuses a duplicate id, an id that is not lowercase snake_case, a rule kind outside the three, a
// strategy whose schema does not parse, and one that declares no invariants.
func Catalogue() []PointStrategy {
	return []PointStrategy{
		Tick{},
		StartPoints{},
		FixedPrice{},
		ZeroSum{},
		Cap{},
		AttendanceWeighted{},
	}
}

// ErrUnknownStrategy reports a strategy id no shipped strategy answers to.
//
// A SENTINEL because the caller's response differs by context and both responses are wrong for the
// other: a pool row naming a strategy this binary does not have is an operator downgrading across a
// release that added one, which is a startup-time refusal with a name in it; the same error from a
// settings form is a 422 on a field. Neither is served by a nil planner and a nil error.
var ErrUnknownStrategy = fmt.Errorf("no such point strategy")

// ByID resolves one of a pool's rule ids to the planner that owns it.
//
// This is the validation `db/schema.hcl` points at when it explains why the columns carry no CHECK.
// It answers only "does this strategy exist?"; whether it belongs in the SLOT the pool put it in is
// PoolConfig.Resolve's question, checked against RuleKind. A linear scan over a handful of entries
// needs no map, and a map would be a package-level one — see the header.
func ByID(id string) (PointStrategy, error) {
	for _, s := range Catalogue() {
		if s.ID() == id {
			return s, nil
		}
	}

	return nil, fmt.Errorf("%q: %w; shipped strategies are %v", id, ErrUnknownStrategy, IDs())
}

// IDs is every shipped strategy id, sorted.
//
// SORTED rather than in Catalogue's order, because this is the list that answers "which values may
// this field take?" — an OpenAPI enum, an error message, a settings dropdown's option set. A stable
// alphabetical order is what keeps a generated artefact from churning when the catalogue is
// reordered for the reader's sake.
func IDs() []string {
	out := make([]string, 0, len(Catalogue()))
	for _, s := range Catalogue() {
		out = append(out, s.ID())
	}

	sort.Strings(out)

	return out
}
