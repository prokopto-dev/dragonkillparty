package core

import (
	"encoding/json"
	"fmt"
	"time"
)

// Micros is an instant stored as Unix microseconds, UTC (canonical §2).
//
// Storage is an INTEGER column suffixed `_at`; the wire form is RFC 3339 with microsecond
// precision and an explicit `Z`. Nanoseconds are deliberately discarded at the boundary: SQLite
// integers and JavaScript numbers both carry microseconds comfortably, and a type that sometimes
// preserves nanoseconds and sometimes does not is a type whose round-trip depends on the value,
// which is the kind of intermittent that only shows up in production.
//
// time.Time is the primitive; Micros is derived from it. Do not add a second time type — one
// exported type per concept (.claude/rules/go-idioms.md).
type Micros int64

// rfc3339Micros is the ONE wire layout for time.
//
// Not time.RFC3339Nano: that trims trailing zeros, so 1_700_000_000_000_000 µs marshals as
// `...Z` while 1_700_000_000_500_000 marshals as `....5Z`, and a fixed-width field that changes
// width breaks column-aligned logs and lexical ordering of timestamp strings. The `.000000` verb
// pads to exactly six fractional digits, always. The `Z07:00` verb prints `Z` for a UTC instant,
// which is what .Time() always produces.
const rfc3339Micros = "2006-01-02T15:04:05.000000Z07:00"

// nanosPerMicro is spelled out so the arithmetic below reads as unit conversion rather than as a
// bare magic number.
const nanosPerMicro = 1_000

// FromTime converts a time.Time to Micros, truncating to microsecond precision.
//
// The result is timezone-independent: an instant is an instant, and UnixMicro erases the location.
// Formatting back out always uses UTC, so a Micros written on a machine in America/Chicago and read
// on one in UTC compares and prints identically — which is the whole reason the ledger stores an
// integer and not a formatted string.
func FromTime(t time.Time) Micros {
	return Micros(t.UnixMicro())
}

// Time converts back to a time.Time in UTC.
//
// UTC, not Local: the location is part of the contract (see clock.Clock). A caller that wants a
// guild-local wall-clock value derives the day bucket from this UTC instant with the guild's zone,
// per canonical §2 — it does not get a Local time here by surprise.
func (m Micros) Time() time.Time {
	return time.UnixMicro(int64(m)).UTC()
}

// String renders the RFC 3339 wire form. It is the same text MarshalJSON emits, minus the quotes.
func (m Micros) String() string {
	return m.Time().Format(rfc3339Micros)
}

// MarshalJSON emits the RFC 3339 microsecond wire form, always ending in `Z`.
func (m Micros) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.String())
}

// UnmarshalJSON parses an RFC 3339 timestamp, truncating to microseconds.
//
// It accepts any RFC 3339 offset on input — a client that sends `+00:00` or a non-UTC offset is not
// wrong, only unusual — and normalises to microseconds. The output side is what enforces `Z`; the
// input side is liberal in what it accepts, strict in what it emits.
func (m *Micros) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("decode micros as a JSON string: %w", err)
	}

	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("parse %q as RFC 3339: %w", s, err)
	}

	*m = FromTime(t)

	return nil
}

// Add returns m advanced by d, truncated to microsecond resolution.
//
// It exists so callers never reach for time.Time arithmetic on a value that is conceptually a
// Micros — round-tripping through time.Time to add a duration is where a stray nanosecond creeps
// back in and a round-trip test starts flaking.
func (m Micros) Add(d time.Duration) Micros {
	return m + Micros(d.Nanoseconds()/nanosPerMicro)
}

// Sub returns the duration from other to m.
func (m Micros) Sub(other Micros) time.Duration {
	return time.Duration(int64(m)-int64(other)) * time.Microsecond
}
