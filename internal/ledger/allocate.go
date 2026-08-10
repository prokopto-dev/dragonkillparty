package ledger

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// Largest-remainder allocation. Phase 0 PR 10a.
//
// Zero-sum credits must sum to EXACTLY the debit. Rounding each credit independently mints or
// destroys points, and at forty attendees a night that is a visible drift within a month — which is
// the failure a member notices, cannot explain, and stops trusting the site over
// (.claude/rules/ledger-and-strategy.md).
//
// The algorithm, over a debit of P centipoints split across N accounts with weights w_i (Sigma w = W):
//
//	quota_i     = P * w_i / W          exact rational, computed in int64
//	base_i      = floor(quota_i)
//	remainder_i = quota_i - base_i     kept as the exact integer numerator, never a ratio
//	R           = P - Sigma base_i     0 <= R < N
//
//	Sort by remainder_i DESC, then account_id ASC. Award +1 cp to the first R accounts.
//
// The tiebreak on account_id (a ULID, compared lexicographically) is MANDATORY and it is what makes
// two replays of the same batch identical. Without it the +1 lands wherever Go's sort happened to
// leave equal elements, the determinism hash differs run to run, and the hash chain stops proving
// anything.

// Errors this file returns.
var (
	// ErrNegativeWeight is returned for a share with a weight below zero. A negative weight is not a
	// "smaller share" — it inverts the sign of that account's quota while still counting toward W,
	// so the remainders stop being remainders and the +1 pass allocates nonsense. It is a planner
	// bug and it fails loudly here rather than committing a plausible-looking wrong split.
	ErrNegativeWeight = errors.New("share weight is negative")

	// ErrZeroTotal is returned when there is nothing to allocate. Returning an empty allocation
	// instead would be a silent drop, and "degenerate cases route to a system account, never to a
	// silent drop" is the rule this file is built around. A caller with a genuinely valueless event
	// declines to write a batch; it does not write an empty one.
	ErrZeroTotal = errors.New("nothing to allocate")

	// ErrWeightOverflow is returned when the weights sum past int64. Guild-scale weights are
	// attendance counts and never approach this, so it fires only for a computed weight that has
	// itself overflowed — at which point every quota below it is meaningless.
	ErrWeightOverflow = errors.New("share weights overflow int64")

	// ErrAmountOutOfRange is returned for a total of math.MinInt64, the one value whose magnitude
	// (2^63) is larger than any positive Centipoints. A single-share split of it would produce an
	// allocation whose int64 value is negative and which only re-negates to the right answer by
	// two's-complement accident. Arithmetic that is correct by accident is not something the
	// highest-blast-radius function in the repository should return, so it is refused instead.
	ErrAmountOutOfRange = errors.New("amount is out of allocatable range")
)

// Share and Allocation are ALIASES for the types declared in internal/strategy, not copies.
//
// The allocator's input and output are named by both halves of the split this repository is built
// around: a strategy PROPOSES a division and the ledger PERFORMS it. internal/strategy may not import
// this package — law 3 bans internal/store transitively and this package holds it — so the types have
// to be declared on the pure side, and `strategy.Ctx.Allocate` is how a planner reaches the algorithm
// below without reaching the database behind it.
//
// Aliases rather than a second pair of structs, because "one exported type per concept" is a rule
// with teeth here: a ledger.Share that was merely field-identical to a strategy.Share would need a
// conversion loop at every planner call site, and a conversion loop is where a weight silently
// becomes an amount. `ledger.Share` stays the name this package's callers and tests already use.
type (
	Share      = strategy.Share
	Allocation = strategy.Allocation
)

// Allocate splits total across shares by largest remainder, returning credits that sum to exactly
// total.
//
// SIGN. total may be negative (splitting a debit) and the property holds either way: the magnitude
// is allocated and the sign is applied at the end, so Sigma allocations == total exactly for both
// signs. Doing it the other way — flooring a negative quota — rounds toward negative infinity and
// makes the remainder pass allocate in the wrong direction.
//
// THE TWO DEGENERATE CASES, both of which route to a system account rather than dropping points:
//
//   - NO SHARES AT ALL. A solo kill with no raid, or an item that rotted with nobody bidding. The
//     whole amount lands on emptyAccount, which the caller chooses from its solo_policy:
//     AccountIDGuildBank for a solo kill, AccountIDWriteOff for a rot.
//   - SHARES PRESENT BUT ALL WEIGHTS ZERO. There is no basis on which to divide — every quota would
//     be 0/0 — so the amount is unallocatable and lands on AccountIDResidue, the account that exists
//     so that conservation stays verifiable when the arithmetic cannot decide.
//
// Neither case is an error, and that is deliberate: both are legal guild nights, and an error would
// push the caller into inventing its own fallback. What IS an error is a negative weight, a zero
// total, or weights that overflow — each of those is a bug in the planner and none of them has a
// defensible fallback.
func Allocate(total core.Centipoints, shares []Share, emptyAccount core.ULID) ([]Allocation, error) {
	if total == 0 {
		return nil, fmt.Errorf("allocate across %d shares: %w", len(shares), ErrZeroTotal)
	}

	if total == math.MinInt64 {
		return nil, fmt.Errorf("allocate %d centipoints: %w", total, ErrAmountOutOfRange)
	}

	if len(shares) == 0 {
		return []Allocation{{AccountID: emptyAccount, AmountCp: total}}, nil
	}

	weightSum, err := sumWeights(shares)
	if err != nil {
		return nil, err
	}

	if weightSum == 0 {
		return []Allocation{{AccountID: AccountIDResidue, AmountCp: total}}, nil
	}

	// Work in magnitude, reapply the sign at the end. magnitude() rather than a bare negation
	// because -math.MinInt64 is not representable; see that function.
	magnitude, negative := magnitudeOf(total)

	type candidate struct {
		accountID core.ULID
		base      uint64
		remainder uint64
	}

	candidates := make([]candidate, len(shares))
	var allocated uint64

	for i, s := range shares {
		// P * w_i as a full 128-bit product, then divided by W. This is the reason the file imports
		// math/bits rather than math/big: `P * w_i` overflows int64 for large values of either, and
		// the two alternatives are both worse. A float would be a lint failure and would lose
		// precision exactly where the invariant lives; math/big would allocate per share on the hot
		// path of every raid-night award. bits.Mul64/Div64 are exact, allocation-free, and int64
		// throughout, which is what "compute in int64" asks for.
		//
		// Div64 panics when the quotient would not fit in 64 bits. It cannot here: w_i <= W, so the
		// quotient is at most `magnitude`, which is itself at most 2^63.
		hi, lo := bits.Mul64(magnitude, uint64(s.Weight))
		base, remainder := bits.Div64(hi, lo, uint64(weightSum))

		candidates[i] = candidate{accountID: s.AccountID, base: base, remainder: remainder}
		allocated += base
	}

	// R = P - Sigma base_i, which is in [0, N) by construction: each base_i is a floor, so at most
	// one whole unit per share is unaccounted for.
	shortfall := magnitude - allocated

	// Sort by remainder DESC, then account_id ASC. sort.SliceStable is not needed — the comparator
	// is a total order because account ids are unique within a split — but the tiebreak IS needed,
	// and a comparator that returned false for two equal remainders would leave their order to the
	// sort's internals. That is the determinism bug this line exists to prevent.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].remainder != candidates[j].remainder {
			return candidates[i].remainder > candidates[j].remainder
		}

		return candidates[i].accountID < candidates[j].accountID
	})

	for i := range candidates {
		if uint64(i) >= shortfall {
			break
		}

		candidates[i].base++
	}

	// Re-sort into account order before returning. The remainder order is an implementation detail
	// of the +1 pass, and a caller that received entries in it would be handed an ordering that
	// depends on the arithmetic — which would then be hashed into the batch. Account order is stable
	// under any input permutation, so two callers that pass the same shares in different orders get
	// byte-identical output.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].accountID < candidates[j].accountID })

	out := make([]Allocation, 0, len(candidates))

	for _, c := range candidates {
		if c.base == 0 {
			// Dropped, not written as zero: CHECK (amount_cp <> 0). This is not a silent drop of
			// points — a zero base means this account's exact share rounded to nothing and the
			// centipoint it did not get went to a higher remainder, so the sum is still exact.
			continue
		}

		amount := core.Centipoints(c.base)
		if negative {
			amount = -amount
		}

		out = append(out, Allocation{AccountID: c.accountID, AmountCp: amount})
	}

	return out, nil
}

// sumWeights adds the weights, rejecting a negative one and an overflow.
func sumWeights(shares []Share) (int64, error) {
	var total int64

	for _, s := range shares {
		if s.Weight < 0 {
			return 0, fmt.Errorf("share for account %s has weight %d: %w",
				s.AccountID, s.Weight, ErrNegativeWeight)
		}

		sum := total + s.Weight
		if sum < total {
			return 0, fmt.Errorf("weights across %d shares: %w", len(shares), ErrWeightOverflow)
		}

		total = sum
	}

	return total, nil
}

// magnitudeOf returns |v| as a uint64 and whether v was negative. Allocate has already rejected
// math.MinInt64, so the magnitude is at most 2^63 - 1 and every base derived from it converts back
// to a positive Centipoints without wrapping.
//
// The uint64 return type is what lets the 128-bit product above be computed at all: bits.Mul64 and
// bits.Div64 are unsigned, and doing this arithmetic on signed values would mean either a float
// (banned in this package) or math/big (an allocation per share on the raid-night hot path).
func magnitudeOf(v core.Centipoints) (magnitude uint64, negative bool) {
	if v < 0 {
		return uint64(-v), true
	}

	return uint64(v), false
}
