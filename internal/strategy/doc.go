// Package strategy holds the pure point strategies: no database, no wall clock and no
// random number generator of its own (law 3). The clock and a seeded RNG are injected, and
// the seed is persisted into the batch so that any run can be replayed exactly.
//
// Phase 0 PR 10a ships the shared shapes only — BatchProposal, EntryProposal, the declarative
// Invariant vocabulary, the Rng interface and the deterministic Canonical form, all in proposal.go.
// The PointStrategy interface and the first implementation (fixed_price) land in PR 10b, written
// against these shapes rather than alongside them.
package strategy
