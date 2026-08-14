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
//   - spend.go — what every SPEND rule does identically: the award batch, the proceeds routing, the
//     bid ordering and the seeded tie-break. What differs between spend rules is how the price is
//     decided; nothing after that differs at all.
//
// The strategies themselves are one file plus one test file each, which is the shape that lets them
// be written in parallel with near-zero conflict surface:
//
//   - fixed_price.go — spend at a published price, and the shape every other strategy varies.
//   - tick.go — earn per attendance snapshot, scaled by a per-role multiplier.
//   - start_points.go — grant a recruit an opening balance, once, on a cadence.
//   - cap.go — a ceiling: reduce or clamp what is earned, and trim what is already above it.
//   - auction_open.go — the ascending English auction: the highest bid wins and pays itself.
//   - auction_sealed.go — hidden bids, first price or second price by config.
//   - relative_bid.go — bids are a share of the balance frozen at the session's open.
//   - roll.go — a seeded server-side roll per entrant; a tie awards nobody and calls a new round.
//   - loot_council.go — spend by officer decision: the charge is the one the council named, recorded
//     rather than derived from a price, a bid or a balance.
//   - decay_percent.go — the weekly haircut: a rate, a floor it stops at, and a policy for debts.
//   - decay_window.go — earnings expire: the slice of the log that has aged out is removed.
//
// start_points, cap and the two decay rules are the CADENCE FAMILY. They post on the decay_run
// schedule keyed (pool_id, kind, cadence_period) rather than in response to a raid-night event, and
// every read they make is positional — which is what makes a re-run of a period propose the batch
// that already committed instead of a second one (.claude/rules/decay-and-jobs.md).
//
// The four bidding rules carry the loot ARITHMETIC only. The bid state machine, the anti-snipe
// window and holds are Phase 6 (docs/guides/auctions.md): each needs a fact the Ctx façade does not
// carry, and a planner that invented one would be guessing at the rules a guild argues over.
// Tier-aware resolution is arithmetic and is a Phase 1 deliverable of its own (ROADMAP item 12,
// #224), because it widens strategy.Bid rather than adding a rule to one strategy.
//
// catalogue.go is the registry that turns a pool's strategy_id into one of them. `pool.strategy_id`
// carries no CHECK constraint on purpose — the set is code-defined and grows per PR — so Catalogue
// and ByID are the validation db/schema.hcl's comment promises exists.
//
// Purity is proved rather than promised: arch_test.go walks the import graph transitively for
// internal/store and directly for the wall clock and math/rand, and scripts/repo-gates.sh carries
// grep twins of the same three rules.
package strategy
