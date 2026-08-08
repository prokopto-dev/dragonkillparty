package core_test

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// propertyChecks is the number of random cases every property test runs. The acceptance criterion
// (docs/development/first-ten-prs.md §PR 8) names 200; it lives in one constant so all four
// property tests agree and raising it is one edit.
const propertyChecks = 200

// TestMicros_RFC3339RoundTrip_AtMicrosecondPrecision is the property from the acceptance criteria:
// a Micros marshals to RFC 3339 at microsecond precision, always Z, and reading it back yields the
// same Micros — for 200 random instants.
func TestMicros_RFC3339RoundTrip_AtMicrosecondPrecision(t *testing.T) {
	t.Parallel()

	roundTrips := func(us int64) bool {
		m := core.Micros(us)

		b, err := json.Marshal(m)
		if err != nil {
			return false
		}

		// Assert the wire SHAPE inside the property, not only round-trip identity. A round trip is
		// satisfied by any self-consistent format — a `+00:00` offset or a variable-width fraction
		// would still unmarshal back to the same Micros — so the "always Z, µs precision" half of the
		// criterion has to be checked on the emitted string over all 200 cases, not spot-checked.
		s := strings.Trim(string(b), `"`)
		if !strings.HasSuffix(s, "Z") {
			return false // must end in Z, never a numeric offset
		}

		dot := strings.IndexByte(s, '.')
		if dot == -1 || len(s)-dot-2 != 6 {
			return false // must carry exactly six fractional digits before the trailing Z
		}

		var got core.Micros
		if err := json.Unmarshal(b, &got); err != nil {
			return false
		}

		return got == m
	}

	// Restrict the generated int64 to a range that time.UnixMicro can format without year overflow:
	// roughly ±292 million years is the full int64 microsecond range, but time.Format misbehaves at
	// the extremes and no real timestamp is out there. Years 1000–9999 covers every value the system
	// will ever see and keeps the generator honest.
	cfg := &quick.Config{
		MaxCount: propertyChecks,
		Values: func(vs []reflect.Value, rng *rand.Rand) {
			vs[0] = microValue(randomMicroInRange(rng))
		},
	}

	require.NoError(t, quick.Check(roundTrips, cfg))
}

// TestMicros_MarshalJSON_AlwaysZAndSixFractionalDigits pins the wire shape the round-trip property
// cannot see on its own: a round-trip is satisfied by any self-consistent format, so this asserts
// the format is specifically RFC 3339, always `Z`, always six fractional digits.
func TestMicros_MarshalJSON_AlwaysZAndSixFractionalDigits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		us   int64
		want string
	}{
		{"whole second", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC).UnixMicro(), `"2024-01-02T03:04:05.000000Z"`},
		{"half second", time.Date(2024, 1, 2, 3, 4, 5, 500_000_000, time.UTC).UnixMicro(), `"2024-01-02T03:04:05.500000Z"`},
		{"one micro", time.Date(2024, 1, 2, 3, 4, 5, 1_000, time.UTC).UnixMicro(), `"2024-01-02T03:04:05.000001Z"`},
		{"unix epoch", 0, `"1970-01-01T00:00:00.000000Z"`},
		{"before epoch", -1_000_000, `"1969-12-31T23:59:59.000000Z"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b, err := json.Marshal(core.Micros(tc.us))
			require.NoError(t, err)
			require.Equal(t, tc.want, string(b))

			// Every emitted timestamp ends in Z and carries exactly six fractional digits.
			s := strings.Trim(string(b), `"`)
			require.True(t, strings.HasSuffix(s, "Z"), "must end in Z, got %q", s)
			dot := strings.IndexByte(s, '.')
			require.NotEqual(t, -1, dot, "must carry a fractional part, got %q", s)
			require.Equal(t, 6, len(s)-dot-2, "must carry exactly six fractional digits, got %q", s)
		})
	}
}

// TestMicros_FromTime_TruncatesNanoseconds proves the boundary rule: sub-microsecond precision is
// dropped, deterministically, so two times that differ only in nanoseconds become one Micros.
func TestMicros_FromTime_TruncatesNanoseconds(t *testing.T) {
	t.Parallel()

	base := time.Date(2024, 6, 1, 12, 0, 0, 123_456_000, time.UTC)
	withNanos := time.Date(2024, 6, 1, 12, 0, 0, 123_456_789, time.UTC)

	require.Equal(t, core.FromTime(base), core.FromTime(withNanos),
		"nanoseconds below the microsecond must not change the Micros")
	require.Equal(t, "2024-06-01T12:00:00.123456Z", core.FromTime(withNanos).String())
}

// TestMicros_UnmarshalJSON_AcceptsAnyOffsetNormalisesToZ proves the input side is liberal (any RFC
// 3339 offset is accepted) while the output side stays strict (re-emitted as Z).
func TestMicros_UnmarshalJSON_AcceptsAnyOffsetNormalisesToZ(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`"2024-01-02T03:04:05.000000Z"`,
		`"2024-01-02T03:04:05.000000+00:00"`,
		`"2024-01-02T04:04:05.000000+01:00"`, // same instant, different offset
	} {
		var m core.Micros
		require.NoError(t, json.Unmarshal([]byte(in), &m), "input %s", in)
		require.Equal(t, "2024-01-02T03:04:05.000000Z", m.String(),
			"every offset must normalise to the same Z instant")
	}
}

// TestMicros_UnmarshalJSON_RejectsGarbage proves a non-timestamp string is an error, not a zero
// value silently accepted.
func TestMicros_UnmarshalJSON_RejectsGarbage(t *testing.T) {
	t.Parallel()

	var m core.Micros
	require.Error(t, json.Unmarshal([]byte(`"not a time"`), &m))
	require.Error(t, json.Unmarshal([]byte(`12345`), &m), "a bare number is not the wire form")
}

// TestMicros_AddSub_RoundTripThroughDuration proves the arithmetic helpers stay in microsecond
// resolution and are inverses.
func TestMicros_AddSub_RoundTripThroughDuration(t *testing.T) {
	t.Parallel()

	m := core.FromTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	later := m.Add(90 * time.Minute)

	require.Equal(t, 90*time.Minute, later.Sub(m))
	require.Equal(t, m, later.Add(-90*time.Minute))
}
