package ledger

import (
	"math/rand/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The seeded random source a strategy is handed. Phase 0 PR 10a.
//
// The interface is declared in internal/strategy (strategy.Rng) and implemented HERE, which is the
// arrangement law 3 requires: a strategy consumes randomness without importing math/rand (gate
// PURE002), and the package that persists the seed onto ledger_batch.rng_seed is the package that
// decides how the seed becomes a sequence. Persisting the seed is what makes a replay byte-identical;
// without it a tie-break coin flip makes the ledger unreproducible and the determinism property is
// meaningless (.claude/rules/ledger-and-strategy.md).
//
// WHY PCG AND NOT THE DEFAULT SOURCE. math/rand/v2's package-level functions are seeded randomly at
// process start and cannot be seeded at all — that is the v2 design, and it is the right one for
// everything except this. rand.NewPCG(seed, 0) is a NAMED algorithm with a published state
// transition, so the sequence it produces from a given seed is a property of the algorithm rather
// than of the Go release, and a batch planned today replays identically on a binary built next year.
// A ChaCha8 source would be equally reproducible and roughly as fast; PCG is chosen because its
// 128-bit state is two int64s, which is what a future column would have to hold if the seed ever
// needs to widen.
//
// The second PCG word is a fixed constant rather than a second seed. One int64 in the schema is what
// domain model §9.1 specifies (`rng_seed INTEGER NULL`), so a second word would either have to be
// derived — which adds nothing, since a derived word carries no information the first does not — or
// be unpersistable, which would defeat the whole exercise. The constant is arbitrary and its only
// requirement is that it never change; changing it would silently alter every replay.
const rngStreamConstant uint64 = 0x9E3779B97F4A7C15

// Rng is the ledger's seeded implementation of strategy.Rng.
//
// Constructed from an int64 seed, it produces the same sequence every time. Two Rngs built from the
// same seed are indistinguishable; two built from different seeds are, with overwhelming probability,
// not — and TestRng_SameSeed_SameSequence asserts both halves, because a generator that ignored its
// seed would satisfy the first alone.
type Rng struct {
	seed int64
	src  *rand.Rand
}

// NewRng returns a deterministic Rng for the given seed.
//
// The seed is converted to uint64 by two's-complement reinterpretation, so the whole int64 range —
// including negative seeds — maps onto distinct PCG states. Clamping or absolute-valuing the seed
// would collapse pairs of seeds onto one sequence, which is a silent loss of entropy in the one
// place the product promises reproducibility rather than unpredictability.
func NewRng(seed int64) *Rng {
	return &Rng{
		seed: seed,
		src:  rand.New(rand.NewPCG(uint64(seed), rngStreamConstant)),
	}
}

// Seed returns the seed this Rng was constructed from. It is written onto ledger_batch.rng_seed at
// commit time, and it is the only thing a replay needs.
func (r *Rng) Seed() int64 { return r.seed }

// IntN returns a uniform value in [0, n). It panics for n <= 0, matching math/rand/v2's own
// contract — a caller asking for a value below zero has a bug, and returning 0 would hide it inside
// a tie-break where nobody would ever look.
func (r *Rng) IntN(n int) int { return r.src.IntN(n) }

// Shuffle permutes n elements using swap.
//
// It exists on the interface for tie-breaking among genuinely equal candidates — a roll-off between
// two bidders at the same amount, say. Note what it must NOT be used for: the largest-remainder
// allocator's tiebreak is account_id ASC and is deliberately NOT random, because a random tiebreak
// there would make two replays of the same batch differ (see allocate.go).
func (r *Rng) Shuffle(n int, swap func(i, j int)) { r.src.Shuffle(n, swap) }

// The compile-time proof that the seam holds. If strategy.Rng ever grew a method this type does not
// implement — or an unexported one, which would make it unimplementable from here at all — `go build`
// says so on the next save rather than a reviewer noticing.
var _ strategy.Rng = (*Rng)(nil)
