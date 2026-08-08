package core_test

import (
	"encoding/json"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// TestCentipoints_FloatRoundTrip_UnderRoundHalfEven is the property from the acceptance criteria:
// a Centipoints converted to its points float and back is unchanged, and the reverse direction
// rounds half-to-even at the 0.01 boundary — over 200 random amounts.
//
// The forward direction (cp -> Points -> FromPoints -> cp) is exact for every centipoint value in a
// realistic range, because cp/100 is exactly representable for those magnitudes. That is the
// invariant a ledger relies on: a balance displayed and re-entered must not drift.
func TestCentipoints_FloatRoundTrip_UnderRoundHalfEven(t *testing.T) {
	t.Parallel()

	roundTrips := func(cp int64) bool {
		c := core.Centipoints(cp)

		back, lossy := core.FromPoints(c.Points())

		// An exact centipoint value round-trips without loss.
		return back == c && !lossy
	}

	cfg := &quick.Config{
		MaxCount: propertyChecks,
		Values: func(vs []reflect.Value, rng *rand.Rand) {
			// ±10¹¹ centipoints is canonical §1's realistic maximum; stay an order of magnitude
			// inside it so the float division is always exact and the property is about rounding,
			// not about float64's own limits.
			vs[0] = reflect.ValueOf(rng.Int63n(2*10_000_000_000) - 10_000_000_000)
		},
	}

	require.NoError(t, quick.Check(roundTrips, cfg))
}

// TestCentipoints_FromPoints_RoundsToNearestOnExactValue pins the rounding with hand-picked values.
//
// Round-half-even is applied to the EXACT value of the float64 argument (via big.Rat), so the
// results below are what rounding the true stored float produces — which is subtly not what rounding
// the printed decimal would. 2.675 is the textbook case: the literal is stored as 2.67499999…, so
// its correct rounding is 267, not the 268 a decimal round-half-up of "2.675" would give. Exact
// ties (k + 0.5 centipoints) are not float64-representable, so every case here is a strict
// nearest-rounding; the tie-to-even branch is exercised by TestRoundHalfEven_ExactTies_GoToEven,
// which feeds the rounder exact rationals it cannot reach through a float.
func TestCentipoints_FromPoints_RoundsToNearestOnExactValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		points float64
		want   core.Centipoints
	}{
		{"just below a half-cent rounds down", 1.004, 100},
		{"just above a half-cent rounds up", 1.006, 101},
		{"2.675 is stored below the midpoint", 2.675, 267},
		{"exact centipoint", 12.34, 1234},
		{"negative exact centipoint", -12.34, -1234},
		{"negative below a half-cent", -1.006, -101},
		{"zero", 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, _ := core.FromPoints(tc.points)
			require.Equal(t, tc.want, got, "FromPoints(%v)", tc.points)
		})
	}
}

// TestCentipoints_FromPoints_FlagsLossyRows proves the "log every lossy row" hook (canonical §1):
// a value with sub-centipoint precision reports lossy=true, and an exact multiple of 0.01 reports
// false. The bool is what the ledger boundary logs on.
func TestCentipoints_FromPoints_FlagsLossyRows(t *testing.T) {
	t.Parallel()

	_, lossy := core.FromPoints(1.005)
	require.True(t, lossy, "a sub-centipoint value is lossy")

	_, exact := core.FromPoints(12.34)
	require.False(t, exact, "an exact centipoint value is not lossy")

	_, nan := core.FromPoints(math.NaN())
	require.True(t, nan, "NaN is lossy, never a silent zero")

	_, inf := core.FromPoints(math.Inf(1))
	require.True(t, inf, "Inf is lossy, never a silent zero")
}

// TestCentipoints_MarshalJSON_IsUnquotedInteger is named verbatim in the acceptance criteria. Money
// on the wire is an unquoted JSON integer (canonical §1): never a string, never a float. A struct
// field carrying a Centipoints must serialise as `35000`, not `"35000"` and not `350.0`.
func TestCentipoints_MarshalJSON_IsUnquotedInteger(t *testing.T) {
	t.Parallel()

	// Bare value.
	b, err := json.Marshal(core.Centipoints(35000))
	require.NoError(t, err)
	require.Equal(t, "35000", string(b), "must be an unquoted integer")
	require.NotContains(t, string(b), `"`, "must not be quoted")
	require.NotContains(t, string(b), ".", "must not be a float")

	// Inside a struct with a _centipoints field, which is the shape 5a/9 will ship.
	type body struct {
		ValueCentipoints core.Centipoints `json:"value_centipoints"`
	}

	out, err := json.Marshal(body{ValueCentipoints: 35000})
	require.NoError(t, err)
	require.JSONEq(t, `{"value_centipoints":35000}`, string(out))
	require.Contains(t, string(out), `"value_centipoints":35000`,
		"the field value must be an unquoted integer in the object")

	// Negative amounts (a debit) marshal as a signed integer.
	nb, err := json.Marshal(core.Centipoints(-1))
	require.NoError(t, err)
	require.Equal(t, "-1", string(nb))
}

// TestCentipoints_UnmarshalJSON_RejectsStringAndFloat proves the read side is strict: a quoted or
// fractional value is an error, so a string-money client cannot slip through the door canonical §1
// closed.
func TestCentipoints_UnmarshalJSON_RejectsStringAndFloat(t *testing.T) {
	t.Parallel()

	var c core.Centipoints

	require.NoError(t, json.Unmarshal([]byte("35000"), &c))
	require.Equal(t, core.Centipoints(35000), c)

	require.NoError(t, json.Unmarshal([]byte("-5"), &c), "a signed integer is a valid debit")
	require.Equal(t, core.Centipoints(-5), c)

	// Every non-integer or non-numeric form is refused rather than coerced. A float-with-zero-fraction
	// (`35000.0`) and scientific notation (`1e3`) are the sneaky ones: they name an integer value but
	// are not integer TOKENS, and accepting them would open the door canonical §1 closed.
	for _, bad := range []string{
		`"35000"`,               // quoted
		"350.5",                 // fractional
		"35000.0",               // float spelling of an integer
		"1e3",                   // scientific notation
		"null",                  // not a number
		`""`,                    // empty string
		"9223372036854775808",   // int64 overflow (max+1)
		"350000000000000000000", // far past int64
	} {
		require.Error(t, json.Unmarshal([]byte(bad), &c), "%s must be rejected", bad)
	}
}
