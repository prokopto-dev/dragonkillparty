// Package strategy holds the pure point strategies: no database, no wall clock and no
// random number generator of its own (law 3). The clock and a seeded RNG are injected, and
// the seed is persisted into the batch so that any run can be replayed exactly.
//
// Lands in: Phase 0 PR 10.
package strategy
