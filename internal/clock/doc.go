// Package clock provides the injected clock. time.Now is called here and nowhere else in
// the repository, which gate CLOCK001 in scripts/repo-gates.sh enforces.
//
// Lands in: Phase 0 PR 8, beside internal/core; first needed by PR 3, whose migration
// snapshots are named with a timestamp.
package clock
