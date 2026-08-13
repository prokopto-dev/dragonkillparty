//go:build race

package ledger_test

// raceEnabled reports that this test binary was built with the race detector.
//
// The two-file build-tagged constant is the standard library's own shape (internal/race), and it is
// here for one reason: a WALL-CLOCK budget cannot be asserted under `-race`. The detector
// instruments every memory access, and modernc.org/sqlite is the SQLite engine itself compiled to
// Go — so every b-tree page touch is instrumented and a query that takes 0.5 ms takes tens of
// milliseconds. `make test` runs `-race`; `make test-perf` does not.
//
// What this constant does NOT do is skip the budget. The standings read's I/O cost is counted from
// dbstat, and a page count does not change under instrumentation, so that half of the budget is
// asserted in both builds. Only the wall-clock half — the half the detector makes meaningless — is
// deferred to the un-instrumented run, and the test says so in its output rather than passing
// quietly on less than it claims.
const raceEnabled = true
