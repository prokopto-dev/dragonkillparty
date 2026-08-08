package core_test

import (
	"math/rand"
	"reflect"
	"time"

	"github.com/oklog/ulid/v2"
)

// This file holds the random-value generators the property tests share. testing/quick drives them
// through Config.Values, whose signature is func([]reflect.Value, *rand.Rand); the helpers below
// produce domain-shaped values (an in-range microsecond, a valid ULID string) rather than the
// arbitrary int64s and strings quick would otherwise invent, because a round-trip property is only
// meaningful over values the type actually accepts.

// microRangeLow and microRangeHigh bound the generated instants to years ~1000–9999, the range
// time.UnixMicro formats without overflow and the only range any real timestamp lives in.
var (
	microRangeLow  = time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()
	microRangeHigh = time.Date(9999, 12, 31, 23, 59, 59, 999_999_000, time.UTC).UnixMicro()
)

// randomMicroInRange returns a microsecond value inside the formattable range.
func randomMicroInRange(rng *rand.Rand) int64 {
	span := microRangeHigh - microRangeLow

	return microRangeLow + rng.Int63n(span)
}

// microValue wraps an int64 microsecond as a reflect.Value of the int64 kind quick.Check's function
// parameter expects.
func microValue(us int64) reflect.Value {
	return reflect.ValueOf(us)
}

// randomULIDString mints a valid 26-character Crockford base32 ULID from the quick RNG.
//
// It uses the RNG quick supplies as the entropy source so a failing case is reproducible from the
// seed quick prints, and it draws a random millisecond timestamp so the generated ULIDs span the
// key space rather than clustering at "now" — which is what the order-preservation property needs
// to exercise.
func randomULIDString(rng *rand.Rand) string {
	ms := uint64(rng.Int63n(int64(ulid.MaxTime())))
	id, err := ulid.New(ms, rng)
	if err != nil {
		// Unreachable: ms is bounded below MaxTime and rng never errors.
		panic(err)
	}

	return id.String()
}
