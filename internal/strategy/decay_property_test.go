package strategy_test

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The properties the decay family owes. Phase 1, #194.
//
// Example tests prove the case you thought of; properties prove the cases you did not. These are the
// universal two — P5 (reversal is an exact inverse) and P8 (determinism) — plus P9, which is the one
// this family exists to satisfy: "two runs for the same (pool_id, kind, cadence_period) produce one
// batch" (docs/design/04-testing.md), whose ticket title is "decay ran twice after the box rebooted".
//
// WHAT P9 CAN AND CANNOT BE PROVED AT THIS LAYER. The unique index on decay_run and the batch
// idempotency key are the ledger's and the scheduler's; a pure planner cannot see either. What it CAN
// prove — and what those two guarantees are worthless without — is that a re-run of a period PROPOSES
// THE SAME BATCH. A key deduplicates two identical writes; it does nothing about a planner that
// computes a second, different haircut and asks to write that. So the property below commits the
// first batch and plans the period again, against a façade that answers positionally, and requires
// the canonical bytes to be identical.
//
// ON THE GENERATOR. testing/quick's generator interface takes a *math/rand.Rand, and importing
// math/rand anywhere under internal/strategy trips repo gate PURE002, test files included. Cases are
// drawn from ledger.NewRng — the same PCG source a strategy would be handed — and the BASE SEED IS
// PRINTED, which makes a failure reproducible. Budget: 200 checks per PR, 20,000 nightly, both via
// DKP_PROPERTY_CHECKS, replayable with DKP_PROPERTY_SEED.

// decayCase is one generated scenario: a roster with balances and aged-out earnings, and the knobs to
// run it under.
//
// It carries the inputs for both strategies rather than one per strategy, so a single generated
// roster is exercised by every planner in the family — the interesting balances (exactly on the
// floor, one centipoint above it, a debt, a member who has spent what the window is about to expire)
// are the same for both.
type decayCase struct {
	Balances []core.Centipoints
	Earned   []core.Centipoints

	DecayBp    int64
	FloorCp    core.Centipoints
	Negative   string
	WindowDays int64
}

// decayRates are the rates a case is drawn from: the two ends of the range, the guide's 10%, and the
// two that expose rounding. A uniform draw over 0..10000 would produce none of them reliably.
var decayRates = []int64{1, 3, 250, 1_000, 2_500, 3_333, 9_999, 10_000}

// decayNegativePolicies is the closed set of answers to "what happens to a debt?".
var decayNegativePolicies = []string{
	strategy.NegativeBalancesSkip,
	strategy.NegativeBalancesTowardZero,
	strategy.NegativeBalancesPreserveSign,
}

// generateDecayCase draws one case from a seeded Rng.
//
// The distribution is CHOSEN rather than uniform, and every choice below is a case a uniform draw
// over int64 would never produce in 200 tries: a balance exactly ON the floor (the boundary the clamp
// turns on), one centipoint above it, a debt, a zero, and an earning LARGER than the balance that is
// left to take it from — which is the member who spent last year's points and must not be pushed into
// debt for them.
func generateDecayCase(rng strategy.Rng) decayCase {
	n := rng.IntN(20) + 1

	c := decayCase{
		Balances:   make([]core.Centipoints, n),
		Earned:     make([]core.Centipoints, n),
		DecayBp:    decayRates[rng.IntN(len(decayRates))],
		Negative:   decayNegativePolicies[rng.IntN(len(decayNegativePolicies))],
		WindowDays: int64(rng.IntN(365) + 1),
	}

	// The floor is drawn at or below zero for preserve_sign, which validateDecayPercentConfig
	// requires: a debt with a floor above it has nowhere to go, and the configuration is refused
	// rather than left inert.
	switch {
	case c.Negative == strategy.NegativeBalancesPreserveSign:
		c.FloorCp = -core.Centipoints(rng.IntN(20_000) + 1)
	case rng.IntN(3) == 0:
		c.FloorCp = core.Centipoints(rng.IntN(5_000))
	case rng.IntN(2) == 0:
		c.FloorCp = -core.Centipoints(rng.IntN(5_000))
	}

	for i := range n {
		switch rng.IntN(7) {
		case 0:
			c.Balances[i] = c.FloorCp // exactly on the floor
		case 1:
			c.Balances[i] = c.FloorCp + 1 // one centipoint above it
		case 2:
			c.Balances[i] = -core.Centipoints(rng.IntN(50_000)) // in debt
		case 3:
			c.Balances[i] = 0
		case 4:
			c.Balances[i] = core.Centipoints(rng.IntN(1_000_000))
		case 5:
			c.Balances[i] = core.Centipoints(rng.IntN(97)) // small enough that a rate rounds away
		default:
			c.Balances[i] = core.Centipoints(rng.IntN(20)) * 1_000_000
		}

		switch rng.IntN(4) {
		case 0:
			c.Earned[i] = 0 // nothing of this member's aged out this period
		case 1:
			c.Earned[i] = core.Centipoints(rng.IntN(1_000_000) + 1) // often more than they still hold
		case 2:
			c.Earned[i] = max(c.Balances[i], 0) // exactly what they hold
		default:
			c.Earned[i] = core.Centipoints(rng.IntN(500) + 1)
		}
	}

	return c
}

// percentConfigJSON and windowConfigJSON render the case's knobs as pool config documents. They are
// built with fmt rather than a struct literal so the test exercises the same PARSER a pool's stored
// JSON goes through — a config built in Go would skip the strict decode this family's config
// strictness lives in.
func (c decayCase) percentConfigJSON() string {
	return fmt.Sprintf(`{"decay_bp":%d,"floor_cp":%d,"negative_balances":%q}`,
		c.DecayBp, c.FloorCp, c.Negative)
}

func (c decayCase) windowConfigJSON() string {
	return fmt.Sprintf(`{"window_days":%d,"floor_cp":%d}`, c.WindowDays, c.FloorCp)
}

// ctx builds the map-backed façade for this case: the balances as they stand, and what aged out.
func (c decayCase) ctx(tb testing.TB, config string) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, len(c.Balances), 0, config)

	for i := range c.Balances {
		ctx.balances[acct(i)] = c.Balances[i]
		ctx.earned[acct(i)] = c.Earned[i]
	}

	return ctx
}

// log builds the log-backed façade for this case: one earning per account, then whatever the balance
// says happened to it afterwards.
//
// Two entries rather than one, because the pair is what makes the interesting case expressible: what
// an account EARNED in the slice that aged out and what it still HOLDS are different numbers, and a
// façade that derived one from the other could not produce the member who has spent it.
func (c decayCase) log(tb testing.TB, config string) *ledgerCtx {
	tb.Helper()

	ctx := newLedgerCtx(tb, len(c.Balances), config)

	for i := range c.Balances {
		if c.Earned[i] > 0 {
			ctx.credit(1, acct(i), c.Earned[i])
		}

		if rest := c.Balances[i] - c.Earned[i]; rest != 0 {
			ctx.credit(2, acct(i), rest)
		}
	}

	ctx.headSeq = 2

	return ctx
}

// decayRun is the run every property plans: one period, read at the seq the log's opening entries
// left, expiring the slice that holds them.
func (c decayCase) decayRun(period string) strategy.DecayRun {
	return strategy.DecayRun{
		PeriodKey:   period,
		AsOfSeq:     2,
		Window:      &strategy.ExpiryWindow{Days: c.WindowDays, FromSeq: 0, ToSeq: 1},
		EffectiveAt: fixedNow,
	}
}

// forEachDecayCase runs check over `propertyChecks` generated cases, failing with the seed that
// reproduces the first counterexample.
//
// One Rng per case, seeded base+i, rather than one for the whole run: a counterexample is then
// replayable on its own without replaying the i cases before it.
func forEachDecayCase(t *testing.T, check func(t *testing.T, c decayCase) error) {
	t.Helper()

	base := propertySeed(t)
	checks := propertyChecks(t)

	t.Logf("%d cases from base seed %d", checks, base)

	for i := range checks {
		seed := base + int64(i)

		c := generateDecayCase(ledger.NewRng(seed))
		if err := check(t, c); err != nil {
			t.Fatalf("counterexample at seed %d (%d accounts, %d bp, floor %d, %s, %d-day window): "+
				"%v\nreplay with: DKP_PROPERTY_SEED=%d DKP_PROPERTY_CHECKS=1 go test ./internal/strategy",
				seed, len(c.Balances), c.DecayBp, c.FloorCp, c.Negative, c.WindowDays, err, seed)
		}
	}
}

// plannedDecayBatches plans every decay-family batch a case can produce, against the log-backed
// façade.
//
// A run that legitimately has nothing to do — every balance at the floor, nothing aged out — returns
// ErrNothingToPlan and is SKIPPED rather than failed: that outcome is the point of these planners,
// and a property that treated it as a failure would be asserting the opposite of §4's "a run that
// moves nothing must not commit". Every other error fails the case.
func plannedDecayBatches(t *testing.T, c decayCase) ([]planned, error) {
	t.Helper()

	sources := []struct {
		name string
		s    strategy.PointStrategy
		plan func() (strategy.BatchProposal, error)
	}{
		{"decay_percent run", strategy.DecayPercent{}, func() (strategy.BatchProposal, error) {
			return strategy.DecayPercent{}.PlanDecay(
				c.log(t, c.percentConfigJSON()), c.decayRun("2026-W31"))
		}},
		{"decay_window run", strategy.DecayWindow{}, func() (strategy.BatchProposal, error) {
			return strategy.DecayWindow{}.PlanDecay(
				c.log(t, c.windowConfigJSON()), c.decayRun("2026-W31"))
		}},
		{"decay_percent adjustment", strategy.DecayPercent{}, func() (strategy.BatchProposal, error) {
			return strategy.DecayPercent{}.PlanAdjustment(
				c.ctx(t, c.percentConfigJSON()), c.adjustment())
		}},
		{"decay_window adjustment", strategy.DecayWindow{}, func() (strategy.BatchProposal, error) {
			return strategy.DecayWindow{}.PlanAdjustment(
				c.ctx(t, c.windowConfigJSON()), c.adjustment())
		}},
	}

	out := make([]planned, 0, len(sources))

	for _, src := range sources {
		p, err := src.plan()
		if errors.Is(err, strategy.ErrNothingToPlan) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.name, err)
		}

		out = append(out, planned{name: src.name, strategy: src.s, proposal: p})
	}

	return out, nil
}

// adjustment is the officer's correction this case's adjustment planners are given. Its amount is
// derived from the case so that a generated roster varies it rather than testing one number 200
// times.
func (c decayCase) adjustment() strategy.AdjustmentEvent {
	return strategy.AdjustmentEvent{
		Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
		AmountCp:    core.Centipoints(c.DecayBp),
		EffectiveAt: fixedNow,
		Reason:      "a correction",
	}
}

// TestProperty_P5_DecayStrategies_ReversalIsAnExactInverse is P5 over the family: applying a batch and
// then its reversal restores every affected balance, exactly.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint on one account leaves a
// permanent, unexplainable discrepancy in a member's statement, and nobody finds it because the
// original and its reversal both look right individually.
func TestProperty_P5_DecayStrategies_ReversalIsAnExactInverse(t *testing.T) {
	t.Parallel()

	reversed := 0

	forEachDecayCase(t, func(t *testing.T, c decayCase) error {
		batches, err := plannedDecayBatches(t, c)
		if err != nil {
			return err
		}

		for _, b := range batches {
			delta := map[core.ULID]core.Centipoints{}
			for _, e := range b.proposal.Entries {
				delta[e.AccountID] += e.AmountCp
			}

			reversal, err := b.strategy.PlanReversal(c.ctx(t, ""), strategy.LedgerBatch{
				ID:              acct(70),
				Kind:            b.proposal.Kind,
				StrategyID:      b.proposal.StrategyID,
				StrategyVersion: b.proposal.StrategyVersion,
				EffectiveAt:     b.proposal.EffectiveAt,
				Entries:         b.proposal.Entries,
			})
			if err != nil {
				return fmt.Errorf("%s: reverse: %w", b.name, err)
			}

			if reversal.Kind != strategy.KindReversal || reversal.ReversesBatchID == nil {
				return fmt.Errorf("%s: the reversal is kind %q with target %v; a reversal that points "+
					"at nothing is an ordinary batch wearing the word",
					b.name, reversal.Kind, reversal.ReversesBatchID)
			}

			for _, inv := range reversal.Invariants {
				if inv.Kind == strategy.InvariantNonNegative {
					return fmt.Errorf("%s: the reversal declares a floor, which does not prevent a debt "+
						"— it prevents the correction, and an append-only ledger has no other repair "+
						"primitive", b.name)
				}
			}

			for _, e := range reversal.Entries {
				delta[e.AccountID] += e.AmountCp
			}

			for id, v := range delta {
				if v != 0 {
					return fmt.Errorf("%s: account %s is %d centipoints from where it started",
						b.name, id, v)
				}
			}

			reversed++
		}

		return nil
	})

	require.Positive(t, reversed,
		"no generated case produced a batch at all, so the property held vacuously — the generator, "+
			"not the reversal, is what is broken")
}

// TestProperty_P8_DecayStrategies_PlanByteIdentically is P8: the same (event, config, clock, seed)
// produces a byte-identical proposal, whatever order the roster arrived in.
//
// The second half is the one that catches real bugs. A roster is a query result and a query without
// an ORDER BY has whichever order the storage engine felt like — so a planner that emitted entries in
// the order it was handed would produce a different batch, and a different hash, for the same period.
func TestProperty_P8_DecayStrategies_PlanByteIdentically(t *testing.T) {
	t.Parallel()

	compared := 0

	forEachDecayCase(t, func(t *testing.T, c decayCase) error {
		first, err := plannedDecayBatches(t, c)
		if err != nil {
			return err
		}

		second, err := plannedDecayBatches(t, c)
		if err != nil {
			return err
		}

		if len(first) != len(second) {
			return fmt.Errorf("planning the same case twice produced %d batches and then %d",
				len(first), len(second))
		}

		for i := range first {
			if a, b := canonicalOf(t, first[i].proposal), canonicalOf(t, second[i].proposal); a != b {
				return fmt.Errorf("%s: two plans of the same run differ:\n\t%s\n\t%s",
					first[i].name, a, b)
			}

			compared++
		}

		// The same roster, shuffled. It is shuffled with a NEGATIVE seed, so the permutation is not
		// the identity and the seeded Rng's whole int64 range is exercised.
		rng := ledger.NewRng(-int64(len(c.Balances)) - 1)

		accounts := make([]strategy.AccountRef, len(c.Balances))
		for i := range accounts {
			accounts[i] = strategy.AccountRef{ID: acct(i), Kind: "person"}
		}

		rng.Shuffle(len(accounts), func(i, j int) {
			accounts[i], accounts[j] = accounts[j], accounts[i]
		})

		for _, tc := range []struct {
			name   string
			plan   func(ctx strategy.Ctx, run strategy.DecayRun) (strategy.BatchProposal, error)
			config string
		}{
			{"decay_percent", strategy.DecayPercent{}.PlanDecay, c.percentConfigJSON()},
			{"decay_window", strategy.DecayWindow{}.PlanDecay, c.windowConfigJSON()},
		} {
			ordered, orderedErr := tc.plan(c.log(t, tc.config), c.decayRun("2026-W31"))

			mixed := c.decayRun("2026-W31")
			mixed.Accounts = accounts

			shuffled, shuffledErr := tc.plan(c.log(t, tc.config), mixed)

			if (orderedErr == nil) != (shuffledErr == nil) {
				return fmt.Errorf("%s: the same roster in a different order planned %v and %v",
					tc.name, orderedErr, shuffledErr)
			}

			if orderedErr != nil {
				continue
			}

			if a, b := canonicalOf(t, ordered), canonicalOf(t, shuffled); a != b {
				return fmt.Errorf("%s: the same roster in a different order planned differently:"+
					"\n\t%s\n\t%s", tc.name, a, b)
			}
		}

		return nil
	})

	require.Positive(t, compared, "no batch was compared, so the property held vacuously")
}

// TestProperty_P9_DecayRuns_ASecondRunForThePeriodIsTheSameBatch is P9 at the layer a pure planner
// owns: a re-run of a cadence period proposes the batch that already committed, byte for byte.
//
// THE FIRST BATCH IS COMMITTED BETWEEN THE TWO PLANS, which is the whole test. The job fires twice
// after a restart, a retry follows a partial failure, an officer clicks "run decay now" while the
// nightly run is mid-flight — in each case the second plan happens against a ledger the first one has
// already changed. What keeps the answer identical is that every read is positional: the balances at
// the period's as-of seq, the earnings in the slice that aged out. A planner that read the pool head
// would compound a second haircut onto the first, and the (pool_id, kind, cadence_period) key would
// not save it, because a key deduplicates identical writes and this would be a different one.
func TestProperty_P9_DecayRuns_ASecondRunForThePeriodIsTheSameBatch(t *testing.T) {
	t.Parallel()

	rerun := 0

	forEachDecayCase(t, func(t *testing.T, c decayCase) error {
		for _, tc := range []struct {
			name   string
			plan   func(ctx strategy.Ctx, run strategy.DecayRun) (strategy.BatchProposal, error)
			config string
		}{
			{"decay_percent", strategy.DecayPercent{}.PlanDecay, c.percentConfigJSON()},
			{"decay_window", strategy.DecayWindow{}.PlanDecay, c.windowConfigJSON()},
		} {
			ctx := c.log(t, tc.config)
			run := c.decayRun("2026-W31")

			first, err := tc.plan(ctx, run)
			if errors.Is(err, strategy.ErrNothingToPlan) {
				continue
			}

			if err != nil {
				return fmt.Errorf("%s: %w", tc.name, err)
			}

			// The ledger commits it, at the next seq, exactly as it would.
			ctx.commit(3, first)

			second, err := tc.plan(ctx, run)
			if err != nil {
				return fmt.Errorf("%s: the period could not be planned a second time: %w", tc.name, err)
			}

			if a, b := canonicalOf(t, first), canonicalOf(t, second); a != b {
				return fmt.Errorf("%s: re-running period %s proposed a DIFFERENT batch, so the "+
					"idempotency key cannot collapse the two:\n\t%s\n\t%s",
					tc.name, run.PeriodKey, a, b)
			}

			rerun++
		}

		return nil
	})

	require.Positive(t, rerun, "no generated case produced a run at all, so the property held vacuously")
}

// TestProperty_DecayPercent_TakesTheExactFlooredRateAndNeverCrossesTheFloor is the arithmetic, checked
// against an independent implementation in math/big rather than against the strategy's own.
//
// Three claims, and each is a different way to redistribute a guild's points by accident:
//
//   - the amount is EXACTLY floor(balance × bp ÷ 10000), computed without an int64 in sight, so a
//     lost bit at the top of the range or a rounding up at the bottom is a counterexample;
//   - the amount never takes a balance past the floor, in either direction;
//   - no entry is zero, because ledger_entry carries CHECK (amount_cp <> 0).
func TestProperty_DecayPercent_TakesTheExactFlooredRateAndNeverCrossesTheFloor(t *testing.T) {
	t.Parallel()

	checked := 0

	forEachDecayCase(t, func(t *testing.T, c decayCase) error {
		ctx := c.ctx(t, c.percentConfigJSON())

		p, err := strategy.DecayPercent{}.PlanDecay(ctx, c.decayRun("2026-W31"))
		if errors.Is(err, strategy.ErrNothingToPlan) {
			return nil
		}

		if err != nil {
			return err
		}

		if net, ok := p.NetAmountCp(); !ok || net != 0 {
			return fmt.Errorf("the decay nets to %d (ok=%v), want exactly 0", net, ok)
		}

		for i, e := range p.Entries {
			if e.AmountCp == 0 {
				return fmt.Errorf("entry %d moves 0 centipoints, which the column forbids", i)
			}

			if e.AccountID == ledger.AccountIDGuildBank {
				continue
			}

			before := ctx.balances[e.AccountID]
			after := before + e.AmountCp

			exact := exactRate(before, c.DecayBp)

			var want core.Centipoints

			switch {
			case before > 0 && before > c.FloorCp:
				want = -min(exact, before-c.FloorCp)
			case c.Negative == strategy.NegativeBalancesTowardZero:
				want = exactRate(-before, c.DecayBp)
			default:
				want = -min(exactRate(-before, c.DecayBp), before-c.FloorCp)
			}

			if e.AmountCp != want {
				return fmt.Errorf("account %s at %d decays by %d, want %d (%d bp, floor %d, %s)",
					e.AccountID, before, e.AmountCp, want, c.DecayBp, c.FloorCp, c.Negative)
			}

			if e.AmountCp < 0 && after < c.FloorCp {
				return fmt.Errorf("account %s was at %d and is now at %d, past the floor of %d",
					e.AccountID, before, after, c.FloorCp)
			}

			if e.AmountCp > 0 && after > 0 {
				return fmt.Errorf("account %s was at %d and is now at %d; forgiving a debt must not "+
					"overshoot zero", e.AccountID, before, after)
			}

			checked++
		}

		return nil
	})

	require.Positive(t, checked, "no account was decayed, so the property held vacuously")
}

// exactRate is floor(amount × bp ÷ 10000) computed in arbitrary precision, for a non-negative amount.
//
// It exists so the property checks the strategy against ARITHMETIC rather than against a second copy
// of the strategy's own 128-bit trick: big.Int cannot overflow, so a disagreement is the
// implementation's and not the test's. Its cost is an allocation per call, which is why it is here
// and not in the shipped path.
func exactRate(amount core.Centipoints, bp int64) core.Centipoints {
	if amount < 0 {
		return 0
	}

	product := new(big.Int).Mul(big.NewInt(int64(amount)), big.NewInt(bp))

	return core.Centipoints(product.Div(product, big.NewInt(10_000)).Int64())
}

// TestProperty_DecayWindow_AnEarningExpiresAtMostOnce is the defect decay_window is shaped around,
// over a roster and a calendar rather than one worked example.
//
// It walks several periods, crediting earnings, running the window over the slice that has just aged
// out, and COMMITTING each batch — so every later run reads a log that already contains the earlier
// runs' own debits. Four claims, and the pair of bounds is the point:
//
//   - NO ACCOUNT LOSES MORE THAN IT EARNED. Taking a slice twice is the over-expiry failure;
//   - NO ACCOUNT KEEPS MORE THAN IT EARNED INSIDE THE WINDOW. This is the under-expiry failure, and
//     it is the one a naive implementation actually has: a planner that took the BALANCE DELTA over
//     the slice instead of the earnings in it nets its own expiry batch — which ages into a later
//     slice — against that period's earnings, expires less than it should, and leaves a roster whose
//     balances ratchet upward with no row to point at. Only the upper bound catches it, because
//     under-expiry never breaks the lower one;
//   - no run takes an account past the floor;
//   - every batch is zero-sum with no empty entries, so each one is a batch the ledger would accept.
func TestProperty_DecayWindow_AnEarningExpiresAtMostOnce(t *testing.T) {
	t.Parallel()

	const periods = 6

	expired := 0

	forEachDecayCase(t, func(t *testing.T, c decayCase) error {
		ctx := newLedgerCtx(t, len(c.Balances), c.windowConfigJSON())

		earnedTotal := map[core.ULID]core.Centipoints{}
		expiredTotal := map[core.ULID]core.Centipoints{}
		boundaries := []int64{0}

		// The position everything at or below has now aged out. It advances whether or not the period
		// produced a batch: a slice nobody earned in is still a slice that has expired.
		var expiredThrough int64

		seq := int64(0)

		for period := range periods {
			seq++

			for i := range c.Balances {
				// A member earns in most periods and sits out some, so a slice with nothing in it is a
				// case the walk actually reaches.
				if (i+period)%3 == 0 || c.Earned[i] <= 0 {
					continue
				}

				ctx.credit(seq, acct(i), c.Earned[i])
				earnedTotal[acct(i)] += c.Earned[i]
			}

			// Half the roster spends in odd periods, which is what makes the clamp matter: the member
			// who has spent what the window is about to expire.
			if period%2 == 1 {
				for i := range c.Balances {
					if c.Balances[i] <= 0 || i%2 == 1 {
						continue
					}

					ctx.credit(seq, acct(i), -c.Balances[i])
				}
			}

			boundaries = append(boundaries, seq)

			// The window is two periods wide, so nothing ages out until the third one.
			if period < 2 {
				continue
			}

			run := strategy.DecayRun{
				PeriodKey: fmt.Sprintf("2026-W%02d", period),
				AsOfSeq:   seq,
				Window: &strategy.ExpiryWindow{
					Days:    c.WindowDays,
					FromSeq: boundaries[period-2],
					ToSeq:   boundaries[period-1],
				},
				EffectiveAt: fixedNow,
			}

			expiredThrough = run.Window.ToSeq

			p, err := strategy.DecayWindow{}.PlanDecay(ctx, run)
			if errors.Is(err, strategy.ErrNothingToPlan) {
				continue
			}

			if err != nil {
				return fmt.Errorf("period %d: %w", period, err)
			}

			if net, ok := p.NetAmountCp(); !ok || net != 0 {
				return fmt.Errorf("period %d nets to %d (ok=%v), want exactly 0", period, net, ok)
			}

			for _, e := range p.Entries {
				if e.AmountCp == 0 {
					return fmt.Errorf("period %d writes a zero entry for %s, which the column forbids",
						period, e.AccountID)
				}

				if e.AccountID == ledger.AccountIDGuildBank {
					continue
				}

				before, err := ctx.Balance(e.AccountID, strategy.BalanceKindDKP, seq)
				if err != nil {
					return err
				}

				if after := before + e.AmountCp; after < c.FloorCp {
					return fmt.Errorf("period %d takes account %s from %d to %d, past the floor of %d",
						period, e.AccountID, before, after, c.FloorCp)
				}

				expiredTotal[e.AccountID] += -e.AmountCp
			}

			seq++
			ctx.commit(seq, p)
			boundaries[len(boundaries)-1] = seq
			expired++
		}

		for id, taken := range expiredTotal {
			if taken > earnedTotal[id] {
				return fmt.Errorf("account %s earned %d over the whole walk and lost %d to expiry; an "+
					"earning that expires twice is a roster drifting with no row to point at",
					id, earnedTotal[id], taken)
			}
		}

		// The upper bound: what is left is what is still INSIDE the window, plus whatever a positive
		// floor stopped an expiry from taking. A balance above that is an earning that aged out and
		// stayed — the under-expiry half, which no amount of checking the other direction can see.
		//
		// The floor term is the guild's own setting, not slack: a pool whose floor is 17.67 keeps every
		// member at or above 17.67 by construction, so an account that was clamped there and has earned
		// since holds the floor plus those earnings. A floor at or below zero adds nothing to the
		// bound, which is why it is max(floor, 0) rather than the floor.
		for i := range c.Balances {
			id := acct(i)

			balance, err := ctx.Balance(id, strategy.BalanceKindDKP, seq)
			if err != nil {
				return err
			}

			inWindow, err := ctx.EarnedBetween(id, strategy.BalanceKindDKP, expiredThrough, seq)
			if err != nil {
				return err
			}

			if limit := inWindow + max(c.FloorCp, 0); balance > limit {
				return fmt.Errorf("account %s holds %d after the walk but earned only %d inside the "+
					"window and the floor is %d; earnings older than the window have stopped counting "+
					"for nobody", id, balance, inWindow, c.FloorCp)
			}
		}

		return nil
	})

	require.Positive(t, expired, "no generated case expired anything, so the property held vacuously")
}
