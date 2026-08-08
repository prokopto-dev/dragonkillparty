package lintfixtures

import "time"

// TimeNowOutsideClock calls time.Now, which canonical §2 bans everywhere except internal/clock: the
// clock is injected so time-dependent behaviour is deterministic and testable (bid timers, decay
// windows, snapshot filenames). The .golangci forbidigo rule fires on the time.Now call here when
// the file is scanned as if it lived outside internal/clock. The import is real so the fixture
// compiles — a rule that only fires on code that builds is a rule you can trust.
func TimeNowOutsideClock() int64 {
	return time.Now().UnixMicro()
}
