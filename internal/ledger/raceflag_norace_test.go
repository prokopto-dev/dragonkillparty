//go:build !race

package ledger_test

// raceEnabled reports that this test binary was built with the race detector. See the //go:build
// race twin, raceflag_race_test.go, for why the distinction exists.
const raceEnabled = false
