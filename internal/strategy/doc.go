// Package strategy holds the pure point strategies: no database, no wall clock and no
// random number generator of its own (law 3). The clock and a seeded RNG are injected, and
// the seed is persisted into the batch so that any run can be replayed exactly.
//
// The division the package exists to hold up: internal/strategy PROPOSES and internal/ledger
// VALIDATES AND COMMITS. A strategy is a pure function from an event to a BatchProposal; it never
// touches the database and it never decides whether its own proposal is legal. That is what lets a
// guild configure its own rules without being able to corrupt the ledger — a misconfigured strategy
// produces a rejected proposal, not a wrong balance.
//
// The seam, in three files:
//
//   - proposal.go — what a strategy PRODUCES: BatchProposal, EntryProposal, the declarative
//     Invariant vocabulary, the Rng interface and the deterministic Canonical form (PR 10a).
//   - strategy.go — what a strategy may CONSUME: the events, the read-only Ctx façade, and the
//     PointStrategy interface that joins the two halves (PR 10b).
//   - common.go — what every strategy here does identically: the strict config decode, the
//     zero-sum assertion, the shared reversal, the share and account checks, and the integer
//     arithmetic helpers. Each takes the strategy id, so an error names the pool's own rules.
//
// The strategies themselves are one file plus one test file each, which is the shape that lets them
// be written in parallel with near-zero conflict surface:
//
//   - fixed_price.go — spend at a published price, and the shape every other strategy varies.
//   - tick.go — earn per attendance snapshot, scaled by a per-role multiplier.
//   - start_points.go — grant a recruit an opening balance, once, on a cadence.
//   - cap.go — a ceiling: reduce or clamp what is earned, and trim what is already above it.
//
// catalogue.go is the registry that turns a pool's strategy_id into one of them. `pool.strategy_id`
// carries no CHECK constraint on purpose — the set is code-defined and grows per PR — so Catalogue
// and ByID are the validation db/schema.hcl's comment promises exists.
//
// Purity is proved rather than promised: arch_test.go walks the import graph transitively for
// internal/store and directly for the wall clock and math/rand, and scripts/repo-gates.sh carries
// grep twins of the same three rules.
package strategy
