// Package ledger is the append-only writer and the invariant engine, and the highest
// blast-radius code in the repository. A ledger_batch or ledger_entry row is never updated
// or deleted; a mistake is corrected by writing a reversal batch that references it.
//
// Lands in: Phase 0 PR 9.
package ledger
