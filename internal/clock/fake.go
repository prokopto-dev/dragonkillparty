package clock

import (
	"sync"
	"time"
)

// Fake is a Clock whose time is set by the test, not by the wall clock.
//
// It exists so time-dependent code can be exercised deterministically: a ULID generator that must
// mint several ids "in the same millisecond", a snapshot filename that must be reproducible, a decay
// window that must land on a chosen day. The real System clock is unusable for any of those, and
// time.Sleep to reach a wall-clock instant is the single largest flake source in a Go test suite
// (.claude/rules/go-idioms.md) — which is why time.Sleep is grep-banned in tests and this is the
// mechanism that replaces it.
//
// Every method is safe for concurrent use: a fake shared by a handful of goroutines minting ULIDs
// is exactly the case that must not race. Each test constructs its own Fake, so there is no shared
// mutable state across tests and t.Parallel() stays honest.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake reading the given instant, normalised to UTC.
//
// UTC normalisation matches System.Now: the location is part of the Clock contract, and a Fake that
// returned a Local time would let a test pass against a bug that System would trigger in production.
func NewFake(at time.Time) *Fake {
	return &Fake{now: at.UTC()}
}

// Now returns the fake's current instant, in UTC.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.now
}

// Set moves the fake to a specific instant, normalised to UTC. It can move time backward, which the
// real clock cannot — a test of "what if two events share a timestamp" needs exactly that.
func (f *Fake) Set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now = at.UTC()
}

// Advance moves the fake forward by d and returns the new instant. A negative d moves it backward,
// for the same reason Set allows it.
func (f *Fake) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now = f.now.Add(d)

	return f.now
}

// compile-time proof that *Fake satisfies Clock, so a signature change to Clock is caught here
// rather than at the first test that happens to pass a *Fake.
var _ Clock = (*Fake)(nil)
