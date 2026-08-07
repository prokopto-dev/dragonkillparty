package clock

import "time"

// Clock is the source of wall-clock time. Inject it; never call time.Now.
//
// Consumers declare their own narrower interface where they need less (.claude/rules/go-idioms.md),
// but there is nothing narrower than this yet, so it lives here and callers depend on it directly.
type Clock interface {
	// Now returns the current instant in UTC.
	//
	// UTC is part of the contract, not a courtesy. The first caller formats the result into a
	// snapshot filename, and a filename whose timestamp depends on the operator's TZ sorts
	// differently on two machines and compares differently across a DST boundary — for a file
	// whose entire job is to be found again, in order, during an incident.
	//
	// Phase 0 PR 8 adds core.Micros (int64 Unix microseconds) as the type this converts into for
	// storage and for the wire. time.Time is the primitive; Micros is derived from it.
	Now() time.Time
}

// System is the real clock. It is the only place in the repository that calls time.Now.
//
// A zero-sized struct rather than a package-level function value: a var could be reassigned from a
// test and would then be package-level mutable state, which -shuffle=on and t.Parallel() turn into
// an intermittent failure in some unrelated package.
type System struct{}

// Now returns the current UTC instant.
func (System) Now() time.Time { return time.Now().UTC() }

// Compile-time proof that System satisfies Clock. Without it, a signature change to Clock would be
// caught only at the first call site that happened to pass a System.
var _ Clock = System{}
