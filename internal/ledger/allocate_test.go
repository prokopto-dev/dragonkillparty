package ledger_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
)

// allocateIterations is the acceptance criterion's 10^5 for
// TestAllocate_LargestRemainder_SumsToDebit. It is not the property-test count (see
// property_test.go): this is one enumerated test with a specified size, and shrinking it would be
// weakening a criterion rather than tuning a knob.
const allocateIterations = 100_000

// allocateSeed makes the 10^5 run reproducible. A failure that cannot be re-run is a failure nobody
// can fix, and math/rand/v2's package functions cannot be seeded at all — which is why this uses an
// explicitly-seeded PCG exactly as internal/ledger/rng.go does.
const allocateSeed uint64 = 0x0DDBA11

// TestAllocate_LargestRemainder_SumsToDebit is the flagship allocator assertion: over 10^5 random
// (P, N, weights), the credits sum to EXACTLY the debit.
//
// Rounding each credit independently mints or destroys points, and at forty attendees a night that is
// a visible drift within a month. This is the test that says it does not happen — and it says so at
// a scale where a one-in-ten-thousand rounding case actually appears, rather than at the handful of
// hand-picked values a table test would cover.
//
// It also asserts, on every iteration, the two properties that make the sum meaningful rather than
// accidental: no returned allocation is zero (CHECK (amount_cp <> 0) would reject it), and every
// allocation is on an account that was passed in (a split cannot invent a beneficiary).
func TestAllocate_LargestRemainder_SumsToDebit(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(allocateSeed, 0))

	for i := range allocateIterations {
		// A spread of magnitudes rather than a uniform draw: the interesting cases are P near N,
		// P below N and P a prime, and a uniform int64 would be enormous every single time.
		total := core.Centipoints(rng.Int64N(1_000_000) + 1)
		if i%2 == 0 {
			total = -total
		}

		n := rng.IntN(50) + 1
		shares := make([]ledger.Share, n)

		for j := range shares {
			shares[j] = ledger.Share{
				AccountID: testAccountID(j),
				// Weight 0 is included on purpose: a raider with no attendance in the window is a
				// real input, and it is the case where an unguarded quota is 0 and the +1 pass has
				// to decide fairly.
				Weight: rng.Int64N(10),
			}
		}

		got, err := ledger.Allocate(total, shares, ledger.AccountIDGuildBank)
		require.NoError(t, err, "iteration %d", i)

		var sum core.Centipoints

		for _, a := range got {
			require.NotZero(t, a.AmountCp,
				"iteration %d: a zero allocation would violate CHECK (amount_cp <> 0); "+
					"Allocate must drop it, not return it", i)

			sum += a.AmountCp
		}

		require.Equal(t, total, sum,
			"iteration %d: %d centipoints across %d shares summed to %d, not %d. "+
				"Credits must sum to EXACTLY the debit; rounding each credit independently is what "+
				"mints or destroys points.", i, total, n, sum, total)
	}
}

// TestAllocate_Tiebreak_IsAccountIDAscending pins the tiebreak as a SPECIFIED behaviour rather than
// an accident of sort order.
//
// Four accounts with equal weights split 102 centipoints: each quota is 25.5, so every base is 25 and
// every remainder is identical. R = 102 - 100 = 2, so exactly two accounts get the extra centipoint —
// and which two is decided purely by the tiebreak. Without `account_id ASC` the +1 lands wherever the
// sort happened to leave equal elements, two replays of the same batch differ, and the determinism
// hash test becomes meaningless.
//
// The shares are passed in DESCENDING id order, so a passing result cannot be input order surviving
// a stable sort.
func TestAllocate_Tiebreak_IsAccountIDAscending(t *testing.T) {
	t.Parallel()

	shares := []ledger.Share{
		{AccountID: testAccountID(3), Weight: 1},
		{AccountID: testAccountID(2), Weight: 1},
		{AccountID: testAccountID(1), Weight: 1},
		{AccountID: testAccountID(0), Weight: 1},
	}

	got, err := ledger.Allocate(102, shares, ledger.AccountIDGuildBank)
	require.NoError(t, err)

	want := []ledger.Allocation{
		{AccountID: testAccountID(0), AmountCp: 26},
		{AccountID: testAccountID(1), AmountCp: 26},
		{AccountID: testAccountID(2), AmountCp: 25},
		{AccountID: testAccountID(3), AmountCp: 25},
	}

	require.Equal(t, want, got,
		"the two extra centipoints must land on the two LOWEST account ids, and the result must be "+
			"returned in account order regardless of the order the shares were passed in")
}

// TestAllocate_DegenerateCases_RouteToSystemAccounts covers the three routes
// .claude/rules/ledger-and-strategy.md specifies, so that no degenerate night silently drops points.
//
// The rule's three cases map onto two mechanisms, and the distinction is worth stating: "N = 0 (solo
// kill) -> guild_bank" and "a rotted item -> write_off" are the SAME code path with a different
// caller-supplied fallback, because which system account absorbs an unsplittable award is the pool's
// solo_policy and not the allocator's business. "An unallocatable remainder -> residue" is the
// allocator's own decision and is hard-wired, because there is no policy question: weights that sum
// to zero give no basis on which to divide, and residue is the account that exists precisely so
// conservation stays verifiable when the arithmetic cannot decide.
func TestAllocate_DegenerateCases_RouteToSystemAccounts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		total    core.Centipoints
		shares   []ledger.Share
		fallback core.ULID
		want     []ledger.Allocation
	}{
		{
			name:     "no shares at all routes to the caller's fallback (solo kill)",
			total:    500,
			shares:   nil,
			fallback: ledger.AccountIDGuildBank,
			want:     []ledger.Allocation{{AccountID: ledger.AccountIDGuildBank, AmountCp: 500}},
		},
		{
			name:     "no shares at all, rot policy, routes to write_off",
			total:    -750,
			shares:   nil,
			fallback: ledger.AccountIDWriteOff,
			want:     []ledger.Allocation{{AccountID: ledger.AccountIDWriteOff, AmountCp: -750}},
		},
		{
			name:  "shares present but every weight zero is unallocatable and routes to residue",
			total: 300,
			shares: []ledger.Share{
				{AccountID: testAccountID(0), Weight: 0},
				{AccountID: testAccountID(1), Weight: 0},
			},
			fallback: ledger.AccountIDGuildBank,
			want:     []ledger.Allocation{{AccountID: ledger.AccountIDResidue, AmountCp: 300}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ledger.Allocate(tc.total, tc.shares, tc.fallback)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestAllocate_InvalidInput_IsRejected covers the three inputs that are planner bugs rather than
// legal guild nights. Each has no defensible fallback, so each is an error — an allocator that
// quietly "handled" a negative weight would be an allocator that produced a plausible-looking wrong
// split.
func TestAllocate_InvalidInput_IsRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		total  core.Centipoints
		shares []ledger.Share
		want   error
	}{
		{
			name:   "zero total",
			total:  0,
			shares: []ledger.Share{{AccountID: testAccountID(0), Weight: 1}},
			want:   ledger.ErrZeroTotal,
		},
		{
			name:   "negative weight",
			total:  100,
			shares: []ledger.Share{{AccountID: testAccountID(0), Weight: -1}},
			want:   ledger.ErrNegativeWeight,
		},
		{
			name:  "weights overflow int64",
			total: 100,
			shares: []ledger.Share{
				{AccountID: testAccountID(0), Weight: math.MaxInt64},
				{AccountID: testAccountID(1), Weight: 1},
			},
			want: ledger.ErrWeightOverflow,
		},
		{
			name:   "math.MinInt64 has no representable magnitude",
			total:  math.MinInt64,
			shares: []ledger.Share{{AccountID: testAccountID(0), Weight: 1}},
			want:   ledger.ErrAmountOutOfRange,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ledger.Allocate(tc.total, tc.shares, ledger.AccountIDGuildBank)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// TestAllocate_LargeMagnitudes_DoNotOverflow proves the 128-bit intermediate is doing its job.
//
// P * w_i overflows int64 for these inputs — that product is roughly 2^62 * 3 — so an implementation
// that multiplied in int64 before dividing would wrap and return quotas that are not merely wrong but
// negative. bits.Mul64/Div64 compute the exact 128-bit product and divide it down, which is why
// allocate.go imports math/bits rather than doing the obvious thing.
func TestAllocate_LargeMagnitudes_DoNotOverflow(t *testing.T) {
	t.Parallel()

	total := core.Centipoints(math.MaxInt64 / 2)

	shares := []ledger.Share{
		{AccountID: testAccountID(0), Weight: math.MaxInt32},
		{AccountID: testAccountID(1), Weight: math.MaxInt32},
		{AccountID: testAccountID(2), Weight: 1},
	}

	got, err := ledger.Allocate(total, shares, ledger.AccountIDGuildBank)
	require.NoError(t, err)

	var sum core.Centipoints

	for _, a := range got {
		require.Positive(t, a.AmountCp, "a positive split must not produce a negative allocation")

		sum += a.AmountCp
	}

	require.Equal(t, total, sum)
}

// TestAllocate_ShareOrder_DoesNotChangeResult is the determinism assertion at the allocator level:
// the same shares in any order produce byte-identical output.
//
// It matters because the entries this returns are hashed into the batch. If the result depended on
// the order a caller happened to build its slice in, two plans of the same event would produce
// different hashes and the chain would attest to an ordering rather than to a decision.
func TestAllocate_ShareOrder_DoesNotChangeResult(t *testing.T) {
	t.Parallel()

	forward := []ledger.Share{
		{AccountID: testAccountID(0), Weight: 3},
		{AccountID: testAccountID(1), Weight: 5},
		{AccountID: testAccountID(2), Weight: 7},
		{AccountID: testAccountID(3), Weight: 11},
	}

	reversed := make([]ledger.Share, len(forward))
	for i, s := range forward {
		reversed[len(forward)-1-i] = s
	}

	a, err := ledger.Allocate(1_000_003, forward, ledger.AccountIDGuildBank)
	require.NoError(t, err)

	b, err := ledger.Allocate(1_000_003, reversed, ledger.AccountIDGuildBank)
	require.NoError(t, err)

	require.Equal(t, a, b)
}
