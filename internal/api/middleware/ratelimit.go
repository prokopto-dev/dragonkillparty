package middleware

import (
	"strconv"
	"sync"
	"time"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
)

// The rate limiter: in-process token buckets, no Redis (docs/design/03-security.md §10).
//
// A guild instance is one process on one box, frequently a Raspberry Pi in somebody's spare room.
// There is nothing to coordinate with, so a shared store would buy consistency this deployment does
// not have a second replica to need, at the cost of a dependency an operator has to run.
//
// THE METER IS GCRA — a token bucket expressed as one timestamp per key rather than a counter plus a
// refill timer. Two properties are why:
//
//   - It is EXACT at the boundary. A fixed-window counter admits a full quota at 59.9s and another
//     at 60.1s, so the real ceiling is twice the advertised one for anybody who aims at the seam.
//   - It answers "how long until you may retry" precisely, which is what Retry-After has to carry.
//     A counter can only say "until the window ends", which sends every shed caller back at the same
//     instant — the thundering herd the header exists to prevent.
//
// It is INTEGER ARITHMETIC throughout: time.Duration is an int64 of nanoseconds, and no quantity here
// is ever a float. That matters less than in the ledger, where AGENTS.md bans floats outright, but
// the reason is the same — a rounding error in a limit is a limit nobody can reproduce.

const (
	// sweepInterval bounds how stale the key table may get on an idle instance.
	//
	// An entry whose bucket has fully refilled carries no information: deleting it and recreating it
	// on the next request produces exactly the same decision. So the sweep is lossless, and its only
	// cost is one pass over the map every few minutes.
	sweepInterval = 5 * time.Minute

	// pressureSweepInterval is the floor between sweeps when the table is at its cap.
	//
	// Without it, a flood of distinct keys would trigger an O(n) sweep on EVERY request once the cap
	// was reached, which turns a rate limiter into the amplifier for the attack it is meant to shed.
	pressureSweepInterval = time.Second

	// DefaultMaxKeys bounds the key table.
	//
	// The table is keyed by client address, and an attacker with a large address pool would otherwise
	// grow it without bound — memory exhaustion delivered through the component that exists to
	// prevent exhaustion. At roughly 64 bytes an entry this cap is a few megabytes, which is affordable
	// on the smallest machine this project targets and far beyond any guild's real key count.
	DefaultMaxKeys = 65536
)

// Policy is one named bucket: a quota, the window it refills over, and the name that appears in the
// RateLimit and RateLimit-Policy header fields.
//
// The name is WIRE SURFACE. A bot library keys its backoff on it, so it is chosen once and not
// renamed, exactly like an OperationID.
type Policy struct {
	// Name is the policy's identifier in the IETF header fields — the quoted string in
	// `RateLimit: "read";r=599;t=57`.
	Name string

	// Quota is how many requests the bucket holds.
	Quota int

	// Window is how long a full bucket takes to refill from empty.
	Window time.Duration
}

// quota and window are the policy's fields with a floor under each.
//
// A POLICY WITH A NON-POSITIVE QUOTA OR WINDOW IS CLAMPED, NOT IGNORED. The shipped policies are
// compile-time constants and the tests pin the headers they produce, so these floors are unreachable
// in the binary; they exist so that a future policy assembled from a bad configuration value fails
// CLOSED — at one request, or over one second — rather than silently turning the limiter off for that
// surface. Every other missing input in this codebase refuses rather than defaulting to allow, and a
// rate limit is not the place to invert that.
//
// EVERY READER GOES THROUGH THESE, the header writer included. A `q=` or `w=` taken from the raw
// fields while the meter ran on the clamped ones would be a response advertising a limit the server
// does not enforce, which is worse than either limit on its own.
func (p Policy) quota() int {
	if p.Quota < 1 {
		return 1
	}

	return p.Quota
}

func (p Policy) window() time.Duration {
	if p.Window <= 0 {
		return time.Second
	}

	return p.Window
}

// emission is the interval between two token refills: the cost, in time, of one request.
func (p Policy) emission() time.Duration {
	return p.window() / time.Duration(p.quota())
}

// capacity is the bucket depth expressed in time: quota requests' worth of emission intervals.
//
// Derived from emission rather than being Window, so that quota*emission == capacity exactly. Integer
// division truncates emission, and a capacity that did not match it by construction would make
// `remaining` disagree with the number of requests actually admitted — off by one, only for quotas
// that do not divide their window evenly, which is the hardest kind of limit bug to reproduce.
func (p Policy) capacity() time.Duration {
	return p.emission() * time.Duration(p.quota())
}

// Decision is the outcome of one Take, and everything the response headers need.
type Decision struct {
	// Allowed reports whether the request may proceed.
	Allowed bool

	// Remaining is how many further requests the bucket holds after this one. Zero on a refusal.
	Remaining int

	// Reset is how long until the bucket is full again. It is the `t` of the IETF RateLimit field and
	// the basis of X-RateLimit-Reset.
	//
	// IT IS NOT "TIME UNTIL THE WINDOW ROLLS OVER", because a continuous meter has no window to roll
	// over: quota returns one slot at a time, so after a single request against a 600/min policy the
	// bucket is full again in a tenth of a second and `t` says 1, not 59. That is the honest answer
	// to "when do I have my full quota back" and it is what the draft's field means; a client that
	// reads it as a fixed window simply waits longer than it needed to.
	Reset time.Duration

	// RetryAfter is how long until this request would be admitted. Zero when Allowed.
	RetryAfter time.Duration

	// At is the instant the meter was read.
	//
	// Carried on the decision rather than re-read by the caller so that the absolute
	// X-RateLimit-Reset and the relative RateLimit `t` describe the same moment. Two calls to a real
	// clock a few microseconds apart would usually agree after rounding and would occasionally
	// straddle a second boundary — an intermittent one-second disagreement between two headers
	// describing one decision, which is exactly the bug nobody reproduces.
	At time.Time
}

// entry is one key's meter: the theoretical arrival time of the next request that would exactly
// exhaust the bucket.
type entry struct {
	tat time.Time
}

// Limiter is the in-process meter shared by every request.
//
// One mutex over one map. A guild instance handles single-digit requests per second on a raid night,
// so lock contention is not a real quantity here; sharding by key hash is the change to make if that
// ever stops being true, and it does not alter the arithmetic below.
type Limiter struct {
	clock   clock.Clock
	maxKeys int

	mu        sync.Mutex
	entries   map[string]entry
	lastSweep time.Time

	// overflowed records that the table hit its cap and stopped tracking new keys. Read by
	// Overflowed so the condition is observable rather than silent; not logged from inside Take,
	// because the condition is attacker-triggerable and a log line per request is the flood it would
	// be reporting.
	overflowed bool
}

// NewLimiter builds a limiter. A maxKeys of zero or less uses DefaultMaxKeys.
func NewLimiter(clk clock.Clock, maxKeys int) *Limiter {
	if maxKeys <= 0 {
		maxKeys = DefaultMaxKeys
	}

	return &Limiter{
		clock:   clk,
		maxKeys: maxKeys,
		entries: make(map[string]entry),
	}
}

// Take charges one request against key under policy and reports what to do and what to say.
//
// THE ARITHMETIC, once, so it can be checked rather than trusted. Let `level` be how much of the
// bucket is currently spent, measured in time: `tat - now`, floored at zero. One request costs one
// `emission`. Then:
//
//	admitted   iff  level + emission <= capacity
//	on admit        tat = max(tat, now) + emission
//	remaining       (capacity - (tat - now)) / emission
//	reset           tat - now                      — until the bucket is full again
//	retry-after     level + emission - capacity    — until one slot frees, on a refusal
//
// A fresh key has level 0, so it is admitted and left with quota-1 remaining. After quota admissions
// at the same instant, level == capacity, so the next request is refused and told to come back in one
// emission — not at the end of the window, which is what makes the shed traffic arrive spread out
// rather than all at once.
//
// A REFUSAL COSTS THE CALLER NOTHING. The meter is not advanced on a refusal, so a client that
// hammers a closed bucket does not push its own retry time further out. The alternative (advancing
// on every arrival) is a lockout that a badly written bot cannot escape, and the bot authors are the
// people this API exists to serve.
func (l *Limiter) Take(policy Policy, key string) Decision {
	var (
		now      = l.clock.Now()
		emission = policy.emission()
		capacity = policy.capacity()
		tat      time.Time
		// tracked is the default: a key is metered unless the table is full and cannot be swept.
		tracked = true
	)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.maybeSweep(now, false)

	existing, known := l.entries[key]

	switch {
	case known:
		tat = existing.tat
	case len(l.entries) >= l.maxKeys:
		// Sweep once more — under pressure the table is mostly full buckets — and only then give up.
		l.maybeSweep(now, true)

		if len(l.entries) >= l.maxKeys {
			l.overflowed = true
			tracked = false
		}
	}

	if tat.Before(now) {
		tat = now
	}

	level := tat.Sub(now)

	if level+emission > capacity {
		return Decision{
			Allowed:    false,
			Remaining:  0,
			Reset:      level,
			RetryAfter: level + emission - capacity,
			At:         now,
		}
	}

	tat = tat.Add(emission)

	if tracked {
		l.entries[key] = entry{tat: tat}
	}

	return Decision{
		Allowed:   true,
		Remaining: int((capacity - tat.Sub(now)) / emission),
		Reset:     tat.Sub(now),
		At:        now,
	}
}

// Overflowed reports whether the key table has ever hit its cap and stopped tracking new keys.
//
// It is a fact about the process, not about a request, which is why it is a method rather than a
// field of Decision: an operator wants to know that enforcement is degraded, and the request that
// happened to be the first one through the cap is not interesting on its own. What it means
// concretely is that a key not already in the table is admitted unmetered; keys already there are
// still enforced.
func (l *Limiter) Overflowed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.overflowed
}

// maybeSweep drops entries whose buckets have refilled, at most once per interval.
//
// Called with l.mu held. `pressure` selects the shorter floor, for the case where the table is at its
// cap and a sweep is the only thing that can make room.
func (l *Limiter) maybeSweep(now time.Time, pressure bool) {
	interval := sweepInterval
	if pressure {
		interval = pressureSweepInterval
	}

	// !After rather than Before, so a fake clock that never advances still sweeps exactly once for a
	// given instant rather than on every call.
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < interval {
		return
	}

	l.lastSweep = now

	for key, e := range l.entries {
		// A tat at or before now means the bucket is full: the entry and its absence produce the same
		// decision, so deleting it loses nothing.
		if !e.tat.After(now) {
			delete(l.entries, key)
		}
	}
}

// The two header families, spelled as they go on the wire.
//
// BOTH, ON EVERY RESPONSE — docs/design/02-api-design.md §5 is explicit that this is not a 429-only
// courtesy: "Both header families are emitted on every response, not just 429s". A client that only
// ever sees them when it has already been shed cannot avoid being shed.
//
//   - The IETF fields (draft-ietf-httpapi-ratelimit-headers) are structured: one `RateLimit` giving
//     the live state and one `RateLimit-Policy` giving the fixed policy. The design's own examples
//     use this spelling — `RateLimit: "ingest";r=19;t=52` at 02-api-design.md:698 — rather than the
//     older RateLimit-Limit/Remaining/Reset triplet the same draft used before it was restructured.
//   - The X- triplet is de-facto rather than standard, and it is emitted because every bot library
//     already parses it. Dropping it would mean every existing EQdkp-era script silently loses its
//     backoff on the day it migrates.
const (
	HeaderRateLimit       = "RateLimit"
	HeaderRateLimitPolicy = "RateLimit-Policy"

	HeaderXRateLimitLimit     = "X-RateLimit-Limit"
	HeaderXRateLimitRemaining = "X-RateLimit-Remaining"
	HeaderXRateLimitReset     = "X-RateLimit-Reset"

	HeaderRetryAfter = "Retry-After"
)

// SetHeaders writes both families for one decision.
//
// It takes a setter rather than an http.Header because the two callers hold different things: a
// transport-level wrapper has an http.Header, and a Huma middleware has ctx.SetHeader. Both are
// `func(name, value string)`, so one function serves both and there is exactly one place where the
// wire spelling of these six headers is decided.
//
// SECONDS, ROUNDED UP, EVERYWHERE. Both drafts and RFC 9110 §10.2.3 specify integer seconds, and
// rounding down would hand a client a deadline that is still inside the window — a retry that is
// refused again, which reads to a bot author as the server lying about its own headers.
func (d Decision) SetHeaders(policy Policy, set func(name, value string)) {
	quoted := strconv.Quote(policy.Name)
	reset := ceilSeconds(d.Reset)

	set(HeaderRateLimitPolicy, quoted+";q="+strconv.Itoa(policy.quota())+
		";w="+strconv.FormatInt(ceilSeconds(policy.window()), 10))
	set(HeaderRateLimit, quoted+";r="+strconv.Itoa(d.Remaining)+";t="+strconv.FormatInt(reset, 10))

	set(HeaderXRateLimitLimit, strconv.Itoa(policy.quota()))
	set(HeaderXRateLimitRemaining, strconv.Itoa(d.Remaining))

	// UNIX EPOCH SECONDS, not a delta, because that is what the de-facto triplet means to the
	// libraries that read it — GitHub's and Twitter's X-RateLimit-Reset are both absolute, and a bot
	// that treats an absolute value as a delta sleeps until 2055. The IETF field beside it is the
	// delta; the two disagree by design, each in the spelling its own readers expect.
	set(HeaderXRateLimitReset, strconv.FormatInt(d.At.Add(time.Duration(reset)*time.Second).Unix(), 10))
}

// RetryAfterSeconds is the Retry-After value for a refusal: at least one second, because RFC 9110
// makes the field a non-negative integer and a `Retry-After: 0` invites an immediate retry that is
// refused again.
func (d Decision) RetryAfterSeconds() int64 {
	if seconds := ceilSeconds(d.RetryAfter); seconds > 0 {
		return seconds
	}

	return 1
}

// ceilSeconds rounds a duration up to whole seconds, floored at zero.
func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}

	return int64((d + time.Second - 1) / time.Second)
}
