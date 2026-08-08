package core

import (
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	"github.com/oklog/ulid/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
)

// ULIDLength is the fixed encoded length of a ULID: 26 characters of Crockford base32 (canonical
// §3). It mirrors ulid.EncodedSize and exists so callers and tests can name the constant without
// importing the third-party package.
const ULIDLength = ulid.EncodedSize

// ULID is a 26-character Crockford base32 identifier, lexicographically sortable (canonical §3).
//
// Sortability is the reason the whole system uses ULIDs rather than random UUIDs: the first 48 bits
// are a millisecond timestamp, so lexical order is time order, which gives the cursor codec a total
// order for free and avoids the uuidv7()/gen_random_uuid() dialect split between SQLite and
// Postgres. The value is stored in a TEXT column and travels the wire as a plain string.
type ULID string

// String returns the identifier text. ULID is already a string; the method exists so it satisfies
// fmt.Stringer and reads intentionally at call sites.
func (u ULID) String() string { return string(u) }

// Valid reports whether u is a syntactically valid 26-character Crockford base32 ULID.
//
// It parses strictly: length, alphabet and the timestamp-overflow rule are all checked, so a value
// that Valid accepts is one Generator could have produced. A `format:"ulid"` tag on an API input
// enforces the same shape at the edge; this is the check for values that arrive another way.
func (u ULID) Valid() bool {
	_, err := ulid.ParseStrict(string(u))

	return err == nil
}

// Time returns the millisecond timestamp embedded in the identifier.
//
// It returns a Micros so a ULID's creation instant reads in the same currency as every other time
// in the system. A malformed identifier returns 0 and false rather than panicking — a stored value
// can be corrupt, and the caller decides whether that is fatal.
func (u ULID) Time() (Micros, bool) {
	id, err := ulid.ParseStrict(string(u))
	if err != nil {
		return 0, false
	}

	// ulid.Time() is Unix milliseconds; scale to microseconds.
	return Micros(int64(id.Time()) * 1_000), true
}

// Generator mints ULIDs that are monotonic within a millisecond.
//
// Monotonicity matters because two identifiers created in the same millisecond would otherwise sort
// by their random tail, so the *creation order* of same-millisecond rows would be unrecoverable —
// and creation order is exactly what a cursor over "newest first" depends on. oklog's monotonic
// entropy increments the random component instead of redrawing it, so a later call in the same
// millisecond always compares greater.
//
// The clock is injected (never time.Now — CLOCK001), which is also what lets
// TestULID_SameMillisecond_IsMonotonic pin every generation to one instant and prove the property.
type Generator struct {
	clock clock.Clock

	// mu guards entropy. ulid.MonotonicEntropy is explicitly NOT safe for concurrent use (its
	// MonotonicRead mutates an internal counter without locking), and a Generator is shared by the
	// whole process, so the lock is mandatory. Two goroutines minting at once without it produce
	// duplicate or out-of-order ids — the exact failure the monotonic entropy was chosen to prevent.
	mu      sync.Mutex
	entropy io.Reader
}

// NewGenerator returns a Generator seeded from crypto/rand.
//
// crypto/rand rather than a seeded PRNG: an id is not a secret, but a predictable random tail lets
// an observer enumerate ids created near a known one, and there is no upside to predictability here.
// The monotonic wrapper's increment is bounded so an overflow within one millisecond returns an
// error rather than wrapping — see New.
func NewGenerator(c clock.Clock) *Generator {
	return &Generator{
		clock:   c,
		entropy: ulid.Monotonic(rand.Reader, 0),
	}
}

// New mints the next identifier.
//
// It takes the timestamp from the injected clock and the entropy under the lock, so concurrent
// callers are serialised through the monotonic source. An error is possible only when more than
// ~2⁸⁰ ids are requested in a single millisecond (monotonic overflow) — unreachable at guild scale,
// surfaced rather than swallowed because a swallowed overflow mints a duplicate id.
func (g *Generator) New() (ULID, error) {
	ms := ulid.Timestamp(g.clock.Now())

	g.mu.Lock()
	id, err := ulid.New(ms, g.entropy)
	g.mu.Unlock()

	if err != nil {
		return "", fmt.Errorf("generate ulid at ms %d: %w", ms, err)
	}

	return ULID(id.String()), nil
}

// MustNew mints an identifier and panics on the unreachable overflow error.
//
// It exists for the wiring paths — a service constructing a batch id — where an error return would
// be threaded through call sites that have no way to handle it and no reason to, since the only
// failure is a same-millisecond overflow that cannot happen. Handlers and anything on a request
// path use New and propagate the error.
func (g *Generator) MustNew() ULID {
	id, err := g.New()
	if err != nil {
		panic(err)
	}

	return id
}
