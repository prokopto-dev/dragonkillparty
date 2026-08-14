package strategy_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The properties the ALLOCATING strategies owe. Phase 1, #196.
//
// `zero_sum` and `attendance_weighted` are the two rules whose every batch is a division: one splits a
// price the buyer paid, the other splits a pot the bank funded. Both reach ledger.Allocate through the
// façade rather than dividing anything themselves, so what is under test here is the PLANNER around
// it — that it hands the allocator the whole amount, debits the whole amount, and adds no entry of its
// own that breaks the equality. A planner can hold a correct allocator and still lose a centipoint by
// rounding its own debit.
//
// P2 IS THE ONE THIS FILE EXISTS FOR. "Credits sum to exactly the debit" is the invariant the whole
// largest-remainder apparatus exists to hold up, and 300.00 over seven raiders is the case that breaks
// naive arithmetic: 42.857… rounds to 42.86, seven of those is 300.02, and two centipoints are minted
// from nothing on every item, forever (docs/guides/choosing-a-dkp-system.md).
//
// ON THE GENERATOR. testing/quick's generator interface takes a *math/rand.Rand, and importing
// math/rand ANYWHERE under internal/strategy trips repo gate PURE002, test files included. That is the
// gate working as designed rather than an obstacle to route around: the rule is that this package gets
// its randomness from the injected seeded Rng, and a test is not exempt from the rule it exists to
// prove. So the cases here are drawn from ledger.NewRng — the same PCG source a strategy would be
// handed — and the BASE SEED IS PRINTED, which makes a failure reproducible in a way a time-seeded
// quick.Check is not.
//
// The budget is the repository's: 200 checks per PR, 20,000 nightly, both controlled by
// DKP_PROPERTY_CHECKS and replayable with DKP_PROPERTY_SEED. propertySeed and propertyChecks are
// shared with fixed_price_test.go.

// allocCase is one generated scenario: a roster with attendance weights, the amounts to divide, and
// the knobs to divide them under.
//
// It carries the inputs for BOTH strategies rather than one per strategy, so that a single generated
// roster is exercised by every planner that divides — the interesting cases (a weight of zero, a
// buyer who is the only attendee, an amount that divides evenly and one that cannot) are the same
// cases for both, and generating them twice would mean two chances to draw a duller distribution.
type allocCase struct {
	Weights []int64

	PriceCp core.Centipoints
	PotCp   core.Centipoints

	Buyer         int
	BuyerAttended bool
	WinnerShare   string
	SoloPolicy    string
}

// allocAmounts are the amounts a case's price and pot are drawn from.
//
// CHOSEN rather than uniform, and every entry is a case a uniform draw over int64 would never produce
// in 200 tries: 1 is the amount that cannot be divided at all, 7 and 9973 are primes that leave a
// remainder against almost any roster, 30000 over 7 is the guide's worked example, and the two large
// ones exercise the allocator's 128-bit product where an int64 multiplication would have overflowed.
var allocAmounts = []core.Centipoints{
	1, 3, 7, 100, 250, 9_973, 30_000, 100_000, 1 << 40, 1 << 61,
}

// generateAllocCase draws one case from a seeded Rng.
func generateAllocCase(rng strategy.Rng) allocCase {
	n := rng.IntN(25) + 1

	c := allocCase{
		Weights:       make([]int64, n),
		PriceCp:       allocAmounts[rng.IntN(len(allocAmounts))],
		PotCp:         allocAmounts[rng.IntN(len(allocAmounts))],
		Buyer:         rng.IntN(n),
		BuyerAttended: rng.IntN(4) > 0,
		WinnerShare:   strategy.WinnerShareExcluded,
		SoloPolicy:    strategy.SystemKeyGuildBank,
	}

	if rng.IntN(3) == 0 {
		c.WinnerShare = strategy.WinnerShareIncluded
	}

	switch rng.IntN(6) {
	case 0:
		c.SoloPolicy = strategy.SystemKeyWriteOff
	case 1:
		// The one policy that can decline to write a batch at all. Drawn often enough that the
		// ErrNothingToPlan path is exercised rather than merely reachable.
		c.SoloPolicy = strategy.SoloPolicyFree
	}

	for i := range n {
		// Weight 0 is drawn deliberately and often: a raider who was on the roster and earned no share
		// is a real night, and an all-zero draw is the degenerate case the two strategies answer
		// DIFFERENTLY — residue for a price somebody paid, no batch at all for a pot nobody earned.
		switch rng.IntN(5) {
		case 0:
			c.Weights[i] = 0
		case 1:
			c.Weights[i] = 1
		default:
			c.Weights[i] = int64(rng.IntN(12) + 1)
		}
	}

	return c
}

// attendees is the whole roster of the generated raid: everyone who was on it, weighted by how much
// of it they were there for. It is what an attendance event carries.
func (c allocCase) attendees() []strategy.Share {
	out := make([]strategy.Share, len(c.Weights))
	for i := range c.Weights {
		out[i] = strategy.Share{AccountID: acct(i), Weight: c.Weights[i]}
	}

	return out
}

// beneficiaries is who an award's price is split across, which is NOT always the same list.
//
// BuyerAttended drops the buyer from it, which is how the generator reaches the solo case: a raider
// who wins an item on a night nobody else was credited for. `winner_share` then decides whether a
// buyer who IS on the list keeps their slice, and the two interact — a one-strong raid where the buyer
// is the only name and is excluded is the same empty split as a raid they were not on.
func (c allocCase) beneficiaries() []strategy.Share {
	out := make([]strategy.Share, 0, len(c.Weights))

	for i := range c.Weights {
		if i == c.Buyer && !c.BuyerAttended {
			continue
		}

		out = append(out, strategy.Share{AccountID: acct(i), Weight: c.Weights[i]})
	}

	return out
}

// zeroSumConfigJSON and attendanceWeightedConfigJSON render the case's knobs as pool config documents.
// They are built with fmt rather than a struct literal so that the property exercises the same PARSER
// a pool's stored JSON goes through — a config built in Go would skip the strict decode.
func (c allocCase) zeroSumConfigJSON() string {
	return fmt.Sprintf(`{"winner_share":%q,"solo_policy":%q,"floor_cp":-100000000}`,
		c.WinnerShare, c.SoloPolicy)
}

func (c allocCase) attendanceWeightedConfigJSON() string {
	return fmt.Sprintf(`{"raid_pot_cp":%d}`, c.PotCp)
}

// award is the event zero_sum plans from.
func (c allocCase) award() strategy.AwardEvent {
	price := c.PriceCp

	return strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(c.Buyer), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Generated"},
		PriceCp:       &price,
		Beneficiaries: c.beneficiaries(),
		EffectiveAt:   fixedNow,
	}
}

// attendance is the event attendance_weighted plans from.
func (c allocCase) attendance() strategy.AttendanceEvent {
	return strategy.AttendanceEvent{Attendees: c.attendees(), EffectiveAt: fixedNow}
}

// forEachAllocCase runs check over `propertyChecks` generated cases, failing with the seed that
// reproduces the first counterexample.
//
// One Rng per case, seeded base+i, rather than one for the whole run: a counterexample is then
// replayable on its own without replaying the i cases before it, which is what makes shrinking by hand
// practical.
func forEachAllocCase(t *testing.T, check func(t *testing.T, c allocCase) error) {
	t.Helper()

	base := propertySeed(t)
	checks := propertyChecks(t)

	t.Logf("%d cases from base seed %d", checks, base)

	for i := range checks {
		seed := base + int64(i)

		c := generateAllocCase(ledger.NewRng(seed))
		if err := check(t, c); err != nil {
			t.Fatalf("counterexample at seed %d (%d accounts, price %d, pot %d, buyer %d attended=%v, "+
				"winner %s, solo %s): %v\nreplay with: DKP_PROPERTY_SEED=%d DKP_PROPERTY_CHECKS=1 go "+
				"test ./internal/strategy",
				seed, len(c.Weights), c.PriceCp, c.PotCp, c.Buyer, c.BuyerAttended, c.WinnerShare,
				c.SoloPolicy, err, seed)
		}
	}
}

// plannedAllocBatches plans every batch a case can produce, from both strategies.
//
// A planner that legitimately has nothing to do — a solo kill on a pool where a solo kill is free, a
// raid nobody attended for any part of — returns ErrNothingToPlan and is SKIPPED rather than failed:
// that outcome is the point of those branches, and a property that treated it as a failure would be
// asserting the opposite of what the config knob means. Every other error fails the case.
func plannedAllocBatches(t *testing.T, c allocCase) ([]planned, error) {
	t.Helper()

	sources := []struct {
		name string
		s    strategy.PointStrategy
		plan func() (strategy.BatchProposal, error)
	}{
		{"zero_sum award", strategy.ZeroSum{}, func() (strategy.BatchProposal, error) {
			return strategy.ZeroSum{}.PlanAward(newCtx(t, 0, 0, c.zeroSumConfigJSON()), c.award())
		}},
		{
			"attendance_weighted attendance",
			strategy.AttendanceWeighted{},
			func() (strategy.BatchProposal, error) {
				return strategy.AttendanceWeighted{}.PlanAttendance(
					newCtx(t, 0, 0, c.attendanceWeightedConfigJSON()), c.attendance())
			},
		},
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

// TestProperty_P2_AllocStrategies_CreditsSumToTheDebitExactly is P2 at the strategy level, and it is
// the acceptance criterion #196 names.
//
// internal/ledger's P2 proves the ALLOCATOR. This proves the PLANNERS: that the debit is the whole
// amount, that the credits sum to exactly it, and that no entry moves zero. A planner that rounded its
// own debit would balance against rounded credits and pass the allocator's property while being wrong.
func TestProperty_P2_AllocStrategies_CreditsSumToTheDebitExactly(t *testing.T) {
	t.Parallel()

	checked := 0

	forEachAllocCase(t, func(t *testing.T, c allocCase) error {
		batches, err := plannedAllocBatches(t, c)
		if err != nil {
			return err
		}

		for _, b := range batches {
			want := c.PriceCp
			if b.strategy.ID() == "attendance_weighted" {
				want = c.PotCp
			}

			debit := b.proposal.Entries[0].AmountCp
			if debit != -want {
				return fmt.Errorf("%s: the debit is %d, want the whole amount %d — a planner that "+
					"rounded its own debit would balance against rounded credits and still be wrong",
					b.name, debit, -want)
			}

			var credits core.Centipoints

			for i, e := range b.proposal.Entries[1:] {
				if e.AmountCp == 0 {
					return fmt.Errorf("%s: credit %d moves 0 centipoints; CHECK (amount_cp <> 0) "+
						"means a zero share is dropped, never written", b.name, i)
				}

				credits += e.AmountCp
			}

			if credits != want {
				return fmt.Errorf("%s: the credits sum to %d, want exactly %d", b.name, credits, want)
			}

			if net, ok := b.proposal.NetAmountCp(); !ok || net != 0 {
				return fmt.Errorf("%s: the batch nets to %d (ok=%v), want exactly 0", b.name, net, ok)
			}

			checked++
		}

		return nil
	})

	require.Positive(t, checked,
		"no generated case produced a batch at all, so the property held vacuously — the generator, "+
			"not the arithmetic, is what is broken")
}

// TestProperty_P5_AllocStrategies_ReversalIsAnExactInverse is P5: applying a batch and then its
// reversal restores every affected balance, exactly.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint on one account leaves a
// permanent, unexplainable discrepancy in a member's statement — and nobody finds it, because the
// original and its reversal both look right individually. It matters most here: a zero-sum award's
// reversal has to undo the debit AND every credit the split produced, together.
func TestProperty_P5_AllocStrategies_ReversalIsAnExactInverse(t *testing.T) {
	t.Parallel()

	nonTrivial := 0

	forEachAllocCase(t, func(t *testing.T, c allocCase) error {
		batches, err := plannedAllocBatches(t, c)
		if err != nil {
			return err
		}

		for _, b := range batches {
			delta := map[core.ULID]core.Centipoints{}
			for _, e := range b.proposal.Entries {
				delta[e.AccountID] += e.AmountCp
			}

			// A one-attendee award whose sole beneficiary IS the buyer nets every account to zero — a
			// legal batch that records the loot with its provenance and moves no points — and for that
			// case "restored to where it started" is vacuously true. The vacuous ones are counted so
			// the run can be required to contain real ones.
			for _, v := range delta {
				if v != 0 {
					nonTrivial++

					break
				}
			}

			reversal, err := b.strategy.PlanReversal(newCtx(t, 0, 0, ""), strategy.LedgerBatch{
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

			if len(reversal.Entries) != len(b.proposal.Entries) {
				return fmt.Errorf("%s: the original has %d entries and the reversal %d; a zero-sum "+
					"award is undone with every credit it produced, together",
					b.name, len(b.proposal.Entries), len(reversal.Entries))
			}

			for _, inv := range reversal.Invariants {
				if inv.Kind == strategy.InvariantNonNegative {
					return fmt.Errorf("%s: the reversal declares a floor, which does not prevent a "+
						"debt — it prevents the correction, and an append-only ledger has no other "+
						"repair primitive", b.name)
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
		}

		return nil
	})

	require.Positive(t, nonTrivial,
		"every generated batch netted every account to zero, so the property held vacuously — the "+
			"generator, not the reversal, is what is broken")
}

// TestProperty_P8_AllocStrategies_PlanByteIdentically is P8: the same (event, config, clock, seed)
// produces a byte-identical proposal.
//
// Two claims, and the second is the one that catches real bugs. Planning the same event twice must
// produce identical canonical bytes — which a planner that ranged over a map would fail
// intermittently — and planning it with the shares SHUFFLED must produce the same bytes too, because a
// set of raiders is a set and the officer's upload order is not part of it. The second is what makes
// the first non-trivial: a planner that preserved input order would pass the first perfectly and
// produce a different hash for the same raid depending on how the roster was sorted when parsed.
func TestProperty_P8_AllocStrategies_PlanByteIdentically(t *testing.T) {
	t.Parallel()

	compared := 0

	forEachAllocCase(t, func(t *testing.T, c allocCase) error {
		first, err := plannedAllocBatches(t, c)
		if err != nil {
			return err
		}

		second, err := plannedAllocBatches(t, c)
		if err != nil {
			return err
		}

		if len(first) != len(second) {
			return fmt.Errorf("planning the same case twice produced %d batches and then %d",
				len(first), len(second))
		}

		for i := range first {
			a, err := first[i].proposal.Canonical()
			if err != nil {
				return err
			}

			b, err := second[i].proposal.Canonical()
			if err != nil {
				return err
			}

			if string(a) != string(b) {
				return fmt.Errorf("%s: two plans of the same event differ:\n\t%s\n\t%s",
					first[i].name, a, b)
			}

			compared++
		}

		// The share order is the input a caller controls and must not influence the batch. It is
		// shuffled with a NEGATIVE seed, so the permutation is not the identity and the seeded Rng's
		// whole int64 range is exercised: a generator that collapsed negative seeds onto positive ones
		// would still shuffle, but would silently halve the space a replay can address.
		rng := ledger.NewRng(-int64(len(c.Weights)) - 1)
		shuffle := func(s []strategy.Share) []strategy.Share {
			rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })

			return s
		}

		award := c.award()
		award.Beneficiaries = shuffle(c.beneficiaries())

		attendance := c.attendance()
		attendance.Attendees = shuffle(c.attendees())

		for _, tc := range []struct {
			name   string
			config string
			plan   func(ctx strategy.Ctx) (strategy.BatchProposal, error)
			mixed  func(ctx strategy.Ctx) (strategy.BatchProposal, error)
		}{
			{
				name:   "zero_sum",
				config: c.zeroSumConfigJSON(),
				plan: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
					return strategy.ZeroSum{}.PlanAward(ctx, c.award())
				},
				mixed: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
					return strategy.ZeroSum{}.PlanAward(ctx, award)
				},
			},
			{
				name:   "attendance_weighted",
				config: c.attendanceWeightedConfigJSON(),
				plan: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
					return strategy.AttendanceWeighted{}.PlanAttendance(ctx, c.attendance())
				},
				mixed: func(ctx strategy.Ctx) (strategy.BatchProposal, error) {
					return strategy.AttendanceWeighted{}.PlanAttendance(ctx, attendance)
				},
			},
		} {
			ordered, orderedErr := tc.plan(newCtx(t, 0, 0, tc.config))
			mixed, mixedErr := tc.mixed(newCtx(t, 0, 0, tc.config))

			if (orderedErr == nil) != (mixedErr == nil) {
				return fmt.Errorf("%s: the same raiders in a different order planned %v and %v",
					tc.name, orderedErr, mixedErr)
			}

			if orderedErr != nil {
				continue
			}

			a, err := ordered.Canonical()
			if err != nil {
				return err
			}

			b, err := mixed.Canonical()
			if err != nil {
				return err
			}

			if string(a) != string(b) {
				return fmt.Errorf("%s: the same raiders in a different order planned differently:"+
					"\n\t%s\n\t%s", tc.name, a, b)
			}

			compared++
		}

		return nil
	})

	require.Positive(t, compared, "no generated case produced a batch to compare")
}

// TestProperty_AllocStrategies_ConsumeNoRandomness is the honest form of P8's anti-tautology half.
//
// A planner that consumed randomness would need its seed persisted onto the batch for a replay to be
// byte-identical. These two consume none — their only tie-break is the allocator's account_id
// ordering, which is deliberately NOT random — so their proposals carry no seed, and the way to state
// that as a fact rather than an assumption is to count the calls across the whole generated run.
func TestProperty_AllocStrategies_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	forEachAllocCase(t, func(t *testing.T, c allocCase) error {
		ctx := newCtx(t, 0, 0, c.zeroSumConfigJSON())

		award, err := strategy.ZeroSum{}.PlanAward(ctx, c.award())
		if err != nil && !errors.Is(err, strategy.ErrNothingToPlan) {
			return err
		}

		if err == nil && award.RngSeed != nil {
			return fmt.Errorf("zero_sum carries a seed it never consumed")
		}

		if ctx.rng.calls != 0 {
			return fmt.Errorf("zero_sum reached for the Rng %d times", ctx.rng.calls)
		}

		ctx = newCtx(t, 0, 0, c.attendanceWeightedConfigJSON())

		attendance, err := strategy.AttendanceWeighted{}.PlanAttendance(ctx, c.attendance())
		if err != nil && !errors.Is(err, strategy.ErrNothingToPlan) {
			return err
		}

		if err == nil && attendance.RngSeed != nil {
			return fmt.Errorf("attendance_weighted carries a seed it never consumed")
		}

		if ctx.rng.calls != 0 {
			return fmt.Errorf("attendance_weighted reached for the Rng %d times", ctx.rng.calls)
		}

		return nil
	})
}
