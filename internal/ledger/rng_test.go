package ledger_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// draws pulls n values from an Rng, which is what "the same sequence" means concretely.
func draws(r *ledger.Rng, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = r.IntN(1 << 30)
	}

	return out
}

// TestRng_SameSeed_SameSequence is the reproducibility contract, in both directions.
//
// The forward half — same seed, same sequence — is what makes `rng_seed` on a batch worth persisting
// at all: a replay from the seed reproduces the plan, and without it a tie-break coin flip makes the
// ledger unreproducible and the determinism property meaningless.
//
// The reverse half — different seeds, different sequences — is the control that stops a degenerate
// implementation passing. An Rng that returned 0 forever would satisfy the forward half perfectly.
// It is stated over 64 draws of a 30-bit range, so two different seeds colliding on all of them has
// probability about 2^-1920; this is not a flaky assertion dressed up as a deterministic one.
func TestRng_SameSeed_SameSequence(t *testing.T) {
	t.Parallel()

	const seed = int64(20_240_601)

	require.Equal(t, draws(ledger.NewRng(seed), 64), draws(ledger.NewRng(seed), 64),
		"two Rngs built from the same seed must produce identical sequences")

	require.NotEqual(t, draws(ledger.NewRng(seed), 64), draws(ledger.NewRng(seed+1), 64),
		"two Rngs built from different seeds must not produce identical sequences; an Rng that "+
			"ignored its seed would satisfy the assertion above perfectly")
}

// TestRng_Seed_RoundTrips asserts the seed a caller supplied is the seed that comes back out — that
// value is written onto ledger_batch.rng_seed, and a seed that had been transformed on the way in
// would not reproduce anything when replayed.
//
// The negative and extreme values are the point. A constructor that clamped or absolute-valued the
// seed would collapse pairs of seeds onto one sequence, which is a silent loss of entropy in the one
// place the product promises reproducibility.
func TestRng_Seed_RoundTrips(t *testing.T) {
	t.Parallel()

	for _, seed := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64, 20_240_601} {
		require.Equal(t, seed, ledger.NewRng(seed).Seed())
	}

	require.NotEqual(t,
		draws(ledger.NewRng(7), 64), draws(ledger.NewRng(-7), 64),
		"a seed and its negation must not collapse onto the same sequence")
}

// TestRng_Shuffle_IsDeterministicAndPermutes covers the third method: the same seed permutes the
// same way, and the result is a permutation rather than a corruption.
//
// Both halves matter. A Shuffle that dropped or duplicated an element would still be deterministic,
// and the tie-break it stands in for — which of two equal bidders wins a roll-off — would silently
// lose a bidder.
func TestRng_Shuffle_IsDeterministicAndPermutes(t *testing.T) {
	t.Parallel()

	const seed = int64(99)

	shuffled := func() []int {
		xs := make([]int, 32)
		for i := range xs {
			xs[i] = i
		}

		ledger.NewRng(seed).Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })

		return xs
	}

	first := shuffled()
	require.Equal(t, first, shuffled(), "the same seed must produce the same permutation")

	seen := make(map[int]bool, len(first))
	for _, v := range first {
		require.False(t, seen[v], "%d appears twice: Shuffle must permute, not duplicate", v)
		seen[v] = true
	}

	require.Len(t, seen, 32, "every element must survive the shuffle")

	identity := make([]int, 32)
	for i := range identity {
		identity[i] = i
	}

	require.NotEqual(t, identity, first,
		"a Shuffle that never moved anything would pass every assertion above")
}

// TestRng_SatisfiesStrategyRng is the seam assertion, at run time as well as at compile time.
//
// rng.go carries `var _ strategy.Rng = (*Rng)(nil)`, which fails the build if the interface grows a
// method. This asserts the other direction — that a strategy really can be handed this value through
// the interface — which is the arrangement law 3 requires: internal/strategy never imports
// math/rand, and the package that persists the seed is the package that implements the generator.
func TestRng_SatisfiesStrategyRng(t *testing.T) {
	t.Parallel()

	var r strategy.Rng = ledger.NewRng(5)

	require.Equal(t, int64(5), r.Seed())
	require.GreaterOrEqual(t, r.IntN(10), 0)
	require.Less(t, r.IntN(10), 10)
}
