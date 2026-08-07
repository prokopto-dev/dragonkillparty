// Package clock provides the injected clock. time.Now is called here and nowhere else in
// the repository, which gate CLOCK001 in scripts/repo-gates.sh enforces.
//
// Phase 0 PR 3 landed the minimum this package needs to exist for: the Clock interface and the
// system implementation. Its first caller names a pre-migration snapshot with a timestamp, and
// there is no way to do that without calling time.Now somewhere — so it happens here, behind the
// interface, rather than in cmd/ where CLOCK001 does not look and the exemption would be silent.
//
// Phase 0 PR 8 completes it, beside internal/core: core.Micros as the time type on the wire and in
// the database, a fake clock for tests, and DKP_TEST_CLOCK.
package clock
