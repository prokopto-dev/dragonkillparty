package core

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Centipoints is a point amount stored as an int64 of points × 100 (canonical §1).
//
// It is the ONLY money type in the repository. There is no float money, no decimal string, no
// NUMERIC column — those are banned in internal/ledger and internal/strategy by a golangci-lint
// rule and by the runtime NoFloat invariant. Realistic maxima are ~10¹¹ centipoints, four orders of
// magnitude below MAX_SAFE_INTEGER, so the wire form is an unquoted JSON integer and every
// JavaScript consumer reads it losslessly.
type Centipoints int64

// centiPerPoint is the scale factor. A "point" is the unit a guild talks in; a centipoint is the
// unit the ledger stores in, so one point is a hundred centipoints.
const centiPerPoint = 100

// Points returns the amount as a whole-and-fraction float64, for DISPLAY ONLY.
//
// This is a boundary conversion and it is lossy above 2⁵³ centipoints — far beyond any real
// balance, but the type does not pretend otherwise. Never feed the result back into arithmetic that
// produces a ledger entry: the float ban exists precisely because "just multiply by 100 again" is
// where a balance drifts by a fraction of a point over a year.
func (c Centipoints) Points() float64 {
	return float64(c) / centiPerPoint
}

// FromPoints converts a display value in points to Centipoints under round-half-even (banker's
// rounding), and reports whether the conversion lost precision.
//
// Round-half-even is canonical §1's boundary rule. It is applied to the EXACT value of the float64
// input, captured via math/big.Rat, rather than to a re-parsed decimal literal. That distinction is
// the whole subtlety: 2.675 is not representable and arrives as 2.67499999…, so rounding its true
// value gives 267 — the correct answer for the number that was actually passed. Rounding a decimal
// re-derived from the printed form would give 268 for a value the caller never held. The tie branch
// (exactly k + 0.5 centipoints) is therefore essentially unreachable from a float64 input, because
// (2k+1)/200 has no terminating binary expansion; it is implemented correctly regardless, so a value
// that ever does land on a tie rounds to even rather than by accident.
//
// The bool is the "log every lossy row" hook from canonical §1: internal/core stays free of slog so
// it can be imported by the pure strategy package, and the caller at the ledger boundary logs the
// row when lossy is true. A value that is already an exact multiple of 0.01 is never lossy.
func FromPoints(points float64) (cp Centipoints, lossy bool) {
	if math.IsNaN(points) || math.IsInf(points, 0) {
		return 0, true
	}

	// Exact rational for `points`, then multiply by 100. new(big.Rat).SetFloat64 is exact: it
	// captures the actual float64 value, including the 2.67499999… that a decimal literal became.
	r := new(big.Rat).SetFloat64(points)
	if r == nil { // NaN/Inf already handled; belt and braces.
		return 0, true
	}

	scaled := new(big.Rat).Mul(r, big.NewRat(centiPerPoint, 1))

	rounded := roundHalfEven(scaled)

	if !rounded.IsInt64() {
		// Overflow: saturate and flag it. A balance this large cannot occur, but returning a wrapped
		// int64 silently would be worse than saturating loudly.
		return 0, true
	}

	cp = Centipoints(rounded.Int64())

	// Lossy is defined against the FLOAT, not against the exact decimal — and the distinction is the
	// whole subtlety of this type. 12.34 is not exactly representable, so its exact scaled value is
	// 1233.9999…, which "differs from the rounded integer" and would flag every ordinary amount as
	// lossy. What actually matters is whether the input carried sub-centipoint precision: round the
	// chosen centipoint back to a float and compare. An input that is the nearest float to an exact
	// centipoint reproduces itself and is NOT lossy; an input like 1.005 (a genuine half-cent) does
	// not, and IS. This is what makes cp -> Points() -> FromPoints() a clean round trip.
	lossy = float64(cp)/centiPerPoint != points

	return cp, lossy
}

// roundHalfEven rounds an exact rational to the nearest integer, ties to even.
//
// big.Rat has no round-half-even, so this is spelled out. floor is the rational's integer part
// toward negative infinity; the remainder decides up, down, or (at exactly one half) toward the
// even neighbour.
func roundHalfEven(r *big.Rat) *big.Int {
	// num / den, floored.
	num := r.Num()
	den := r.Denom()

	q := new(big.Int)
	rem := new(big.Int)
	q.DivMod(num, den, rem) // Euclidean: 0 <= rem < den, q floored toward -inf.

	// twiceRem = 2*rem, compared against den to find which side of the half we are on.
	twiceRem := new(big.Int).Lsh(rem, 1)
	cmp := twiceRem.Cmp(den)

	switch {
	case cmp < 0:
		// Below the midpoint: floor.
		return q
	case cmp > 0:
		// Above the midpoint: ceil.
		return q.Add(q, big.NewInt(1))
	default:
		// Exactly the midpoint: round to even. q is the floor; if q is odd, step up to the even one.
		if q.Bit(0) == 1 {
			return q.Add(q, big.NewInt(1))
		}

		return q
	}
}

// String renders the integer centipoint value, so a log line shows the stored value, not a float.
func (c Centipoints) String() string {
	return strconv.FormatInt(int64(c), 10)
}

// MarshalJSON emits an UNQUOTED JSON integer (canonical §1).
//
// This is the whole point of the type carrying its own marshaller: a bare int64 already marshals as
// an unquoted integer, but making it explicit here means TestCentipoints_MarshalJSON_IsUnquotedInteger
// pins the behaviour, so a future change that wrapped money in a string (as an earlier security
// design did — `"value_centipoints": "35000"`) fails a test rather than shipping.
func (c Centipoints) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(c), 10)), nil
}

// UnmarshalJSON accepts ONLY an unquoted JSON integer.
//
// A quoted number is rejected, not leniently parsed: accepting `"35000"` here would let a
// string-money client through and defeat the invariant on the read path, so the strictness is
// deliberate. json.Number cannot be used for this — it silently accepts BOTH `"35000"` and `350.5`,
// stripping the quotes and keeping the fraction — so the raw token is inspected directly. The error
// names the offending token so a bot author sees why their body was refused.
func (c *Centipoints) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))

	// A leading quote is a JSON string. Reject it before ParseInt, which would happily accept the
	// digits inside — this is the exact hole json.Number leaves open.
	if strings.HasPrefix(trimmed, `"`) {
		return fmt.Errorf("centipoints must be an unquoted integer, got a JSON string %s", trimmed)
	}

	i, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		// A fractional value (`350.5`) or anything non-integer lands here — money that never
		// existed at centipoint resolution, refused rather than truncated.
		return fmt.Errorf("centipoints must be an unquoted integer, got %s: %w", trimmed, err)
	}

	*c = Centipoints(i)

	return nil
}
