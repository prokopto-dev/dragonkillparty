package core

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRoundHalfEven_ExactTies_GoToEven exercises the tie branch that FromPoints cannot reach.
//
// An exact half-centipoint tie is (2k+1)/200 points, which has no terminating binary expansion, so
// no float64 argument ever lands on one — FromPoints therefore always takes a strict nearest branch.
// The rounder is still required to be correct on the tie, because it operates on exact rationals and
// a future caller (a decimal-input path, a big.Rat quota in the allocator) could feed it one. This
// test feeds exact rationals directly, in-package, and asserts ties go to the EVEN neighbour in both
// directions and for negatives — the property "round-half-up" would get wrong.
func TestRoundHalfEven_ExactTies_GoToEven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		num  int64
		den  int64
		want int64
	}{
		{"0.5 rounds down to even 0", 1, 2, 0},
		{"1.5 rounds up to even 2", 3, 2, 2},
		{"2.5 rounds down to even 2", 5, 2, 2},
		{"3.5 rounds up to even 4", 7, 2, 4},
		{"-0.5 rounds to even 0", -1, 2, 0},
		{"-1.5 rounds to even -2", -3, 2, -2},
		{"-2.5 rounds to even -2", -5, 2, -2},
		{"non-tie below rounds down", 4, 3, 1},         // 1.333…
		{"non-tie above rounds up", 5, 3, 2},           // 1.666…
		{"negative non-tie rounds nearest", -5, 3, -2}, // -1.666…
		{"exact integer unchanged", 6, 2, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := roundHalfEven(big.NewRat(tc.num, tc.den))
			require.True(t, got.IsInt64())
			require.Equal(t, tc.want, got.Int64(), "roundHalfEven(%d/%d)", tc.num, tc.den)
		})
	}
}
