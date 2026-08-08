// Package lintfixtures holds deliberately-illegal source used ONLY by the lint-ban tests in
// internal/core. Each file here trips exactly one golangci-lint or repo-gate rule; the tests copy
// it into a temp tree at the scoped path the rule watches and assert `make lint`'s mechanism exits
// non-zero. A disabled rule then shows up as a red test rather than as a silent hole.
//
// These files live under testdata/ on purpose: golangci-lint skips testdata/ by default, so they do
// NOT break the real `make lint` run — only the tests, which lint them at their real scoped path,
// ever see them. Do not "fix" the violation below; it is the point of the file.
package lintfixtures

// FloatInLedger returns a float64. Canonical §1 bans float32/float64 anywhere in internal/ledger and
// internal/strategy, because a single float in the point path silently converts the centipoint
// ledger to floating point and drifts a balance by a fraction of a point over a year. The .golangci
// forbidigo rule fires on the float64 in this signature when the file is scanned as if it lived in
// internal/ledger.
func FloatInLedger() float64 {
	return 1.5
}
