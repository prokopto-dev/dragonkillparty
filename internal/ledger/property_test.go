package ledger_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The four flagship properties: P1 conservation, P2 exact splits, P5 reversal is an exact inverse,
// P8 determinism. Phase 0 PR 10a.
//
// ON THE FRAMEWORK. `.claude/rules/ledger-and-strategy.md` and `docs/design/04-testing.md` both name
// `pgregory.net/rapid` for property tests, and these use `testing/quick` instead. That is a
// deviation and it is recorded rather than quietly taken: rapid is a NEW DEPENDENCY, and AGENTS.md
// is unambiguous that a dependency is proposed with its reason and its licence and a human decides
// — a rule that does not have an exception for "the design document already assumed it". Every
// property below is expressible with `quick.Check` plus a custom generator, which is exactly the
// shape internal/core's four PR 8 properties already use, so the cost of the deviation is shrinking
// (no automatic shrinking of counterexamples) rather than coverage. The dependency proposal for
// rapid belongs with the invariant work in Phase 1, where the state-machine mode it is actually
// better at — SK positions under random suicides, the bid FSM — is what gets built.
//
// ON THE COUNT. 200 per PR and 20,000 nightly is the acceptance criterion. It is one env var rather
// than a build tag so that the nightly lane is `DKP_PROPERTY_CHECKS=20000 make test` with no second
// code path — a nightly suite that compiles differently from the per-PR suite is a nightly suite
// that can go green while the real one is broken.
//
// ON THE SEED. Every property below draws from a FIXED, overridable seed, and prints it. That is a
// correction from review of PR 10b: these previously passed a quick.Config with no Rand, so the
// standard library seeded them from time.Now().UnixNano() — the cases differed on every run, the
// seed was never printed, and a nightly counterexample could not be reproduced by any command at
// all. `make test-property`, the nightly workflow and docs/design/04-testing.md all promise a
// replayable seed; this is what makes the promise true here as well as in internal/strategy.
//
// A fixed default also beats a random one on the morning something goes red: a suite whose inputs
// change every run is a suite where "it passed when I re-ran it" closes a real counterexample. The
// breadth comes from the nightly 20,000-case lane, not from irreproducibility.

// defaultPropertyChecks is the per-PR count from the acceptance criterion.
const defaultPropertyChecks = 200

// defaultPropertySeed is the base seed the cases are drawn from when DKP_PROPERTY_SEED is unset. The
// same constant as internal/strategy's, so the two halves of a nightly run replay together.
const defaultPropertySeed = 1_717_243_200

// propertySeed is the base seed for this run, overridable to replay a reported failure. A malformed
// value fails the test for the same reason a malformed check count does.
func propertySeed(tb testing.TB) int64 {
	tb.Helper()

	raw, ok := os.LookupEnv("DKP_PROPERTY_SEED")
	if !ok || raw == "" {
		return defaultPropertySeed
	}

	seed, err := strconv.ParseInt(raw, 10, 64)
	require.NoError(tb, err, "DKP_PROPERTY_SEED=%q is not a number", raw)

	return seed
}

// propertyConfig builds the quick.Config every property here uses: the run's check count, and a
// generator seeded from the run's base seed rather than from the wall clock.
//
// The seed is LOGGED, not just used. A counterexample that cannot be reproduced is a bug report
// nobody can act on, and `go test` only shows the log line on failure — which is exactly when it is
// wanted.
func propertyConfig(tb testing.TB) *quick.Config {
	tb.Helper()

	seed := propertySeed(tb)
	checks := propertyChecks(tb)

	tb.Logf("%d cases from base seed %d — replay with DKP_PROPERTY_SEED=%d", checks, seed, seed)

	return &quick.Config{
		MaxCount: checks,
		//nolint:gosec // G404: this is a test generator, not a security primitive. Reproducibility is
		// the whole requirement and crypto/rand cannot be seeded.
		Rand: rand.New(rand.NewSource(seed)),
	}
}

// propertyChecks is the number of random cases each property runs, overridable with
// DKP_PROPERTY_CHECKS for the nightly 20,000-case lane.
//
// A malformed value FAILS the test rather than falling back to the default. A typo'd
// `DKP_PROPERTY_CHECKS=2O000` (letter O) that silently ran 200 cases would report a nightly deep run
// that never happened, which is worse than no nightly run at all.
func propertyChecks(tb testing.TB) int {
	tb.Helper()

	raw, ok := os.LookupEnv("DKP_PROPERTY_CHECKS")
	if !ok || raw == "" {
		return defaultPropertyChecks
	}

	n, err := strconv.Atoi(raw)
	require.NoError(tb, err, "DKP_PROPERTY_CHECKS=%q is not a number", raw)
	require.Positive(tb, n, "DKP_PROPERTY_CHECKS must be positive, got %d", n)

	return n
}

// TestPropertyConfig_SameSeed_DrawsTheSameCases is the test of the replay promise itself.
//
// `make test-property`, nightly-verify.yml and docs/design/04-testing.md all tell a maintainer that a
// counterexample replays with DKP_PROPERTY_SEED. That promise was FALSE for these four properties
// until PR 10b — they passed a quick.Config with no Rand, so the standard library seeded them from
// the wall clock — and it went unnoticed precisely because nothing asserted it. A documented
// reproduce command that does not reproduce is worse than none: it is a maintainer's whole Saturday.
//
// Both halves are the test. The same seed must draw the same cases, and a different seed must not:
// a propertyConfig that returned a constant generator would satisfy the first alone, and 20,000
// nightly checks of the same case is not a deep run.
// It does NOT call t.Parallel, and cannot: t.Setenv panics in a parallel test, because the
// environment is process-wide. That is a forced exception to the house rule, not an oversight.
func TestPropertyConfig_SameSeed_DrawsTheSameCases(t *testing.T) {
	draw := func(cfg *quick.Config) []splitCase {
		out := make([]splitCase, 0, 8)
		for range 8 {
			out = append(out, splitCase{}.Generate(cfg.Rand, 0).Interface().(splitCase))
		}

		return out
	}

	t.Setenv("DKP_PROPERTY_SEED", "424242")
	first := draw(propertyConfig(t))

	t.Setenv("DKP_PROPERTY_SEED", "424242")
	require.Equal(t, first, draw(propertyConfig(t)),
		"the same seed must draw the same cases, or DKP_PROPERTY_SEED replays nothing")

	t.Setenv("DKP_PROPERTY_SEED", "424243")
	require.NotEqual(t, first, draw(propertyConfig(t)),
		"a different seed must draw different cases; a generator that ignored its seed would make "+
			"the nightly 20 000-case lane 20 000 runs of one case")
}

// TestPropertySeed_MalformedValue_FailsRatherThanFallingBack: a typo'd seed must not silently run the
// default, for the same reason a typo'd check count must not — a run that reports a replay it did
// not perform sends somebody looking for a bug in the wrong place.
func TestPropertySeed_MalformedValue_FailsRatherThanFallingBack(t *testing.T) {
	t.Setenv("DKP_PROPERTY_SEED", "4242forty-two")

	fake := &recordingTB{TB: t}

	// On its own goroutine: require.NoError calls FailNow, which the recorder implements with
	// runtime.Goexit, and running that on the test's goroutine would end this test rather than
	// record the failure. The deferred close still runs on Goexit, which is what makes the wait
	// terminate.
	done := make(chan struct{})

	go func() {
		defer close(done)

		propertySeed(fake)
	}()

	<-done

	require.True(t, fake.failed, "a malformed DKP_PROPERTY_SEED must fail the test")
}

// recordingTB captures a require failure instead of ending the real test. require.NoError calls
// FailNow, which must run on the test's own goroutine, so the helper under test is called directly
// and the failure is recorded rather than propagated.
type recordingTB struct {
	testing.TB

	failed bool
}

func (r *recordingTB) Errorf(string, ...any) { r.failed = true }
func (r *recordingTB) FailNow()              { r.failed = true; runtime.Goexit() }

// splitCase is one generated (total, weights) pair — the input to a zero-sum split.
type splitCase struct {
	Total   core.Centipoints
	Weights []int64
}

// Generate makes splitCase satisfy quick.Generator, so quick.Check drives it directly.
//
// The distribution is chosen, not uniform, and the choices are the acceptance criterion's: N = 1,
// P < N and P prime all have to appear, and a uniform int64 total over a uniform slice length would
// produce none of them in 200 draws. So the total is drawn from a mixture that includes small
// values and a list of primes, and the share count is drawn small enough that P < N is common.
func (splitCase) Generate(rng *rand.Rand, _ int) reflect.Value {
	// N in [1, 60]: a raid is forty-ish people, and 1 must appear often because a solo award is the
	// degenerate case where the remainder pass has nowhere to go.
	n := rng.Intn(60) + 1

	weights := make([]int64, n)
	for i := range weights {
		// Zero weights are included: a raider with no attendance in the window is a real input.
		weights[i] = int64(rng.Intn(12))
	}

	var total core.Centipoints

	switch rng.Intn(4) {
	case 0:
		// Small: frequently below N, which is the case where every base is 0 and the whole result
		// comes from the remainder pass.
		total = core.Centipoints(rng.Intn(80) + 1)
	case 1:
		// Prime: the case where no weight divides the total evenly, so the remainders are maximally
		// spread and the tiebreak decides the most awards.
		total = core.Centipoints(smallPrimes[rng.Intn(len(smallPrimes))])
	case 2:
		total = core.Centipoints(rng.Int63n(1_000_000) + 1)
	default:
		// Large enough that P * w_i overflows int64 if the implementation multiplies before dividing.
		total = core.Centipoints(rng.Int63n(1 << 61))
	}

	if rng.Intn(2) == 0 {
		total = -total
	}

	return reflect.ValueOf(splitCase{Total: total, Weights: weights})
}

// smallPrimes are the prime totals P2 requires. Primes matter because a prime P shares no factor
// with any plausible weight sum, so every quota has a non-zero remainder and the +1 pass is
// maximally exercised.
var smallPrimes = []int{
	2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47,
	97, 101, 997, 1009, 7919, 65_537, 1_000_003,
}

// shares turns a generated case into the allocator's input.
func (c splitCase) shares() []ledger.Share {
	out := make([]ledger.Share, len(c.Weights))
	for i, w := range c.Weights {
		out[i] = ledger.Share{AccountID: testAccountID(i), Weight: w}
	}

	return out
}

// TestProperty_P2_CreditsSumToDebitExactly is P2: for every (P, N) — including N = 1, P < N and P
// prime — the credits sum to exactly P.
//
// This is the arithmetic heart of the zero-sum model. Rounding each credit independently mints or
// destroys points on nearly every award, and the drift is invisible per-award and obvious per-month.
func TestProperty_P2_CreditsSumToDebitExactly(t *testing.T) {
	t.Parallel()

	sumsExactly := func(c splitCase) bool {
		got, err := ledger.Allocate(c.Total, c.shares(), ledger.AccountIDGuildBank)
		if err != nil {
			return false
		}

		var sum core.Centipoints
		for _, a := range got {
			if a.AmountCp == 0 {
				return false // CHECK (amount_cp <> 0): a zero must be dropped, never returned
			}

			sum += a.AmountCp
		}

		return sum == c.Total
	}

	require.NoError(t, quick.Check(sumsExactly, propertyConfig(t)))
}

// TestProperty_P1_ZeroSumConservationOverBatchSequences is P1: over a random SEQUENCE of zero-sum
// batches, the total across all accounts stays exactly zero.
//
// P2 checks one split. This checks that splits compose — that no sequence of awards, however the
// remainders fall, leaks a centipoint. A batch here is a debit on the payer plus the allocated
// credits, which is the shape a zero-sum strategy produces; the property is that summing every entry
// of every batch in the sequence gives zero, per balance kind and overall.
//
// It runs in memory rather than against SQLite, deliberately. The database-backed half is
// TestCommit_ZeroSumSequence_LedgerSumsToZero below, which commits a sequence for real; running the
// PROPERTY through SQLite at 20,000 nightly cases would take hours to prove the same arithmetic the
// in-memory version proves in a second, and the arithmetic is what can be wrong.
func TestProperty_P1_ZeroSumConservationOverBatchSequences(t *testing.T) {
	t.Parallel()

	conserves := func(cases []splitCase) bool {
		if len(cases) == 0 {
			return true
		}

		balances := map[core.ULID]core.Centipoints{}

		for _, c := range cases {
			credits, err := ledger.Allocate(c.Total, c.shares(), ledger.AccountIDGuildBank)
			if err != nil {
				return false
			}

			// The payer's debit is the exact negation of the total that was split, which is what
			// makes the batch zero-sum before the credits are even considered.
			balances[ledger.AccountIDGuildBank] -= c.Total

			for _, a := range credits {
				balances[a.AccountID] += a.AmountCp
			}
		}

		var total core.Centipoints
		for _, v := range balances {
			total += v
		}

		return total == 0
	}

	cfg := propertyConfig(t)
	cfg.Values = func(vs []reflect.Value, rng *rand.Rand) {
		// One to eight batches per sequence: enough for remainders to interact, small enough
		// that a failure is readable.
		n := rng.Intn(8) + 1
		seq := make([]splitCase, n)

		for i := range seq {
			seq[i] = splitCase{}.Generate(rng, 0).Interface().(splitCase)
		}

		vs[0] = reflect.ValueOf(seq)
	}

	require.NoError(t, quick.Check(conserves, cfg))
}

// TestProperty_P5_ReversalIsAnExactInverse is P5: negating a proposal and applying both leaves every
// account exactly where it started.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint on one account is a reversal
// that leaves a permanent, unexplainable discrepancy in a member's statement — and one that nobody
// finds, because the batch and its reversal both look right individually.
//
// The property also asserts the reversal's DECLARED SET over the same random cases: SumZero survives
// and NonNegative does not, whatever the original declared. An exact inverse the engine would reject
// is not a repair primitive, and the ledger has no other one — so "the arithmetic is right" and "the
// batch is committable" are two halves of the same property rather than two tests.
func TestProperty_P5_ReversalIsAnExactInverse(t *testing.T) {
	t.Parallel()

	floor := core.Centipoints(0)

	inverts := func(c splitCase) bool {
		credits, err := ledger.Allocate(c.Total, c.shares(), ledger.AccountIDGuildBank)
		if err != nil {
			return false
		}

		entries := []strategy.EntryProposal{
			{AccountID: ledger.AccountIDGuildBank, BalanceKind: "dkp", AmountCp: -c.Total},
		}
		for _, a := range credits {
			entries = append(entries, strategy.EntryProposal{
				AccountID: a.AccountID, BalanceKind: "dkp", AmountCp: a.AmountCp,
			})
		}

		// Declared the way a well-written zero-sum award declares: conserving AND floored, because the
		// floor is what refuses an overdraft on the spend side.
		original := strategy.BatchProposal{
			Kind: "award", StrategyID: "zero_sum", StrategyVersion: "0.0.0", Entries: entries,
			Invariants: []strategy.Invariant{
				{Kind: strategy.InvariantSumZero, BalanceKind: "dkp"},
				{Kind: strategy.InvariantNonNegative, BalanceKind: "dkp", FloorCp: &floor},
			},
		}

		reversal, err := original.Negated(core.ULID(padID("BATCH", 1)))
		if err != nil {
			return false
		}

		if reversal.Kind != strategy.KindReversal || reversal.ReversesBatchID == nil {
			return false
		}

		// Committable as well as exact. A floor inherited onto a reversal does not prevent a debt, it
		// prevents the correction — and an append-only ledger has no other way to correct anything.
		if !slices.ContainsFunc(reversal.Invariants, func(inv strategy.Invariant) bool {
			return inv.Kind == strategy.InvariantSumZero
		}) {
			return false
		}

		if slices.ContainsFunc(reversal.Invariants, func(inv strategy.Invariant) bool {
			return inv.Kind == strategy.InvariantNonNegative
		}) {
			return false
		}

		balances := map[core.ULID]core.Centipoints{}
		for _, e := range original.Entries {
			balances[e.AccountID] += e.AmountCp
		}

		// Non-trivially changed: if the original moved nothing, "restored to zero" is vacuous.
		moved := false

		for _, v := range balances {
			if v != 0 {
				moved = true

				break
			}
		}

		if !moved {
			return false
		}

		for _, e := range reversal.Entries {
			balances[e.AccountID] += e.AmountCp
		}

		for _, v := range balances {
			if v != 0 {
				return false
			}
		}

		return true
	}

	require.NoError(t, quick.Check(inverts, propertyConfig(t)))
}

// TestProperty_P8_SameSeedProducesAByteIdenticalProposal is P8: planning the same event twice from
// the same seed produces byte-identical canonical bytes, and different seeds do not.
//
// Both halves are the test. Without the second, a planner that ignored its Rng entirely would pass
// the first perfectly — and a planner that ignores its seed is precisely the bug P8 exists to catch,
// because it means rng_seed on the batch is decoration and a replay proves nothing.
//
// The planner here is a miniature one declared in this file rather than a real strategy: PR 10a
// ships no strategies (fixed_price is PR 10b), and the property under test is the CONTRACT between
// the seeded Rng and the canonical form, which a two-line planner exercises as well as a real one.
func TestProperty_P8_SameSeedProducesAByteIdenticalProposal(t *testing.T) {
	t.Parallel()

	deterministic := func(seed int64, c splitCase) bool {
		first, order, err := planWithRng(ledger.NewRng(seed), c)
		if err != nil {
			return false
		}

		second, sameOrder, err := planWithRng(ledger.NewRng(seed), c)
		if err != nil {
			return false
		}

		if !bytes.Equal(first, second) || order != sameOrder {
			return false
		}

		// The anti-tautology half. A planner that ignored its Rng entirely would satisfy the check
		// above perfectly, and a planner that ignores its seed is exactly the bug P8 exists to
		// catch — it means rng_seed on the batch is decoration and a replay proves nothing. So the
		// plan must also DEPEND on the seed.
		//
		// It compares the ENTRY ORDER, not the canonical bytes, and that distinction is the whole
		// point. The seed is itself a field of the proposal, so two different seeds ALWAYS produce
		// different canonical bytes whether or not the planner ever called the Rng — comparing the
		// bytes here would be a tautology wearing the costume of a control. The order is what the
		// Rng actually influences.
		//
		// Stated over EIGHT seeds rather than two, which is what makes it an assertion rather than a
		// coin flip. Two different seeds can legitimately shuffle k credits into the same order with
		// probability 1/k!, which at k = 2 is one run in two — a two-seed check would flake half the
		// time. Requiring that not all eight agree makes a false failure (1/k!)^7, about 1 in 10^32
		// at the k >= 8 guard below.
		//
		// The guard is not a fudge either: with one credit (every weight zero, so the whole amount
		// routes to residue) two seeds genuinely MUST agree, because there is only one possible
		// plan. Skipping those cases is correct, not convenient.
		const seedsTried = 8

		if creditsIn(order) < seedsTried {
			return true
		}

		for i := 1; i < seedsTried; i++ {
			_, other, err := planWithRng(ledger.NewRng(seed+int64(i)), c)
			if err != nil {
				return false
			}

			if other != order {
				return true
			}
		}

		return false
	}

	cfg := propertyConfig(t)
	cfg.Values = func(vs []reflect.Value, rng *rand.Rand) {
		vs[0] = reflect.ValueOf(rng.Int63() - (1 << 62)) // negative seeds must work too
		vs[1] = splitCase{}.Generate(rng, 0)
	}

	require.NoError(t, quick.Check(deterministic, cfg))
}

// planWithRng is the miniature planner P8 exercises: it shuffles the shares with the injected Rng,
// splits the total across them, and returns the canonical bytes of the resulting proposal plus the
// number of credits it produced.
//
// The shuffle is what makes the seed matter. A real strategy's randomness is a tie-break — which of
// two equal bidders wins a roll-off — and this stands in for it: the ORDER the planner considers
// candidates in comes from the Rng, and if the canonical form depended on nothing else the shuffle
// would be invisible. Here it survives into the entry order, which strategy.Canonical preserves
// precisely so that an ordering non-determinism is visible.
//
// The second return is the credit ORDER, as the account ids joined in the order the plan emitted
// them. P8's anti-tautology half compares that rather than the canonical bytes, because the seed is
// itself a field of the proposal and would make any two seeds differ regardless of the planner.
func planWithRng(rng *ledger.Rng, c splitCase) (canonical []byte, order string, err error) {
	shares := c.shares()
	rng.Shuffle(len(shares), func(i, j int) { shares[i], shares[j] = shares[j], shares[i] })

	credits, err := ledger.Allocate(c.Total, shares, ledger.AccountIDGuildBank)
	if err != nil {
		return nil, "", fmt.Errorf("plan: %w", err)
	}

	// Re-order the credits by the shuffled share order, so that the Rng's output reaches the entry
	// list. Allocate returns account order by design (see its comment); a planner that wanted a
	// different order has to impose it, which is what a roll-off tie-break does.
	rank := make(map[core.ULID]int, len(shares))
	for i, s := range shares {
		rank[s.AccountID] = i
	}

	sort.Slice(credits, func(i, j int) bool {
		return rank[credits[i].AccountID] < rank[credits[j].AccountID]
	})

	seed := rng.Seed()

	entries := []strategy.EntryProposal{
		{AccountID: ledger.AccountIDGuildBank, BalanceKind: "dkp", AmountCp: -c.Total},
	}
	for _, a := range credits {
		entries = append(entries, strategy.EntryProposal{
			AccountID: a.AccountID, BalanceKind: "dkp", AmountCp: a.AmountCp,
		})
	}

	encoded, err := strategy.BatchProposal{
		Kind:            "award",
		StrategyID:      "property_planner",
		StrategyVersion: "0.0.0",
		RngSeed:         &seed,
		Entries:         entries,
	}.Canonical()
	if err != nil {
		return nil, "", fmt.Errorf("canonicalise plan: %w", err)
	}

	ids := make([]string, len(credits))
	for i, a := range credits {
		ids[i] = a.AccountID.String()
	}

	return encoded, strings.Join(ids, ","), nil
}

// creditsIn reports how many credits an order string names, so P8 can decide whether there were
// enough permutations for two seeds agreeing to mean anything.
func creditsIn(order string) int {
	if order == "" {
		return 0
	}

	return strings.Count(order, ",") + 1
}

// TestCommit_ZeroSumSequence_LedgerSumsToZero is P1's database-backed half: a sequence of real
// commits leaves the ledger summing to exactly zero across every account.
//
// It is an ordinary test rather than a property because it is slow — twelve commits, each a
// transaction — and because what it proves is different from what the property proves. The property
// proves the arithmetic composes; this proves the arithmetic survives the round trip through
// allocation, invariants, insertion, the snapshot cache and SQLite's integer columns.
func TestCommit_ZeroSumSequence_LedgerSumsToZero(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	accounts := seedPersonAccounts(t, s, 7)

	// Totals chosen so that most divide unevenly across seven accounts, which is where the
	// remainder pass does its work.
	for i, total := range []core.Centipoints{997, 1_000_003, 5, 4, 65_537, 1, 100, 33, 7919, 2, 11, 123_457} {
		shares := make([]ledger.Share, len(accounts))
		for j, id := range accounts {
			shares[j] = ledger.Share{AccountID: id, Weight: int64(j%5) + 1}
		}

		credits, err := ledger.Allocate(total, shares, ledger.AccountIDGuildBank)
		require.NoError(t, err, "batch %d", i)

		_, err = svc.Commit(t.Context(), request(award(ledger.AccountIDGuildBank, credits)))
		require.NoError(t, err, "batch %d", i)
	}

	require.Equal(t, int64(0),
		countRow(t, s, `SELECT CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) FROM ledger_entry`),
		"every entry ever written must sum to zero: a zero-sum ledger that does not is a ledger "+
			"that has minted or destroyed points")

	// And the cache agrees with the log. balance_snapshot is derived, but a cache that has drifted is
	// what /standings shows a member — and since ADR-0023 there is no fallback that would show them
	// the log instead — so the two must be checked against each other rather than the cache being
	// trusted.
	for _, id := range append(accounts, ledger.AccountIDGuildBank) {
		require.Equal(t, balanceOf(t, s, id),
			countRow(t, s, `SELECT CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) FROM balance_snapshot
			                WHERE pool_id = ? AND account_id = ? AND balance_kind = 'dkp'`,
				ledger.DefaultPoolID.String(), id.String()),
			"the snapshot cache for %s has drifted from the ledger", id)
	}
}
