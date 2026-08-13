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

// The properties the earn family owes. Phase 1, #193.
//
// Example tests prove the case you thought of; properties prove the cases you did not. These are the
// four from docs/design/04-testing.md that name these strategies — P5 (reversal is an exact inverse),
// P6 (cap clamps and is idempotent), P7 (start_points applies once per account) and P8 (determinism)
// — driven by generated input rather than hand-picked balances.
//
// ON THE GENERATOR. testing/quick's generator interface takes a *math/rand.Rand, and importing
// math/rand ANYWHERE under internal/strategy trips repo gate PURE002, test files included. That is
// the gate working as designed rather than an obstacle to route around: the rule is that this package
// gets its randomness from the injected seeded Rng, and a test is not exempt from the rule it exists
// to prove. So the cases here are drawn from ledger.NewRng — the same PCG source a strategy would be
// handed — and the BASE SEED IS PRINTED, which makes a failure reproducible in a way a time-seeded
// quick.Check is not.
//
// The budget is the repository's: 200 checks per PR, 20,000 nightly, both controlled by
// DKP_PROPERTY_CHECKS, replayable with DKP_PROPERTY_SEED. propertySeed and propertyChecks are shared
// with fixed_price_test.go.

// earnCase is one generated scenario: a roster with balances and history, and the knobs to run it
// under.
//
// It carries the inputs for all three strategies rather than one per strategy, so that a single
// generated roster is exercised by every planner in the family — the interesting cases (a balance
// exactly at a cap, an account with no history and a negative balance) are the same cases for all of
// them, and generating them three times would mean three chances to draw a duller distribution.
type earnCase struct {
	Weights  []int64
	Roles    []string
	Balances []core.Centipoints
	History  []bool

	TickAwardCp core.Centipoints
	HardCapCp   core.Centipoints
	SoftCapCp   core.Centipoints
	OverCapBp   int64
	GrantCp     core.Centipoints
}

// earnRoles is the role vocabulary cases are drawn from: the raid_attendance.status values a real
// event carries, plus the empty role, plus one the config never names.
var earnRoles = []string{"", "present", "standby", "bench", "late", "unpriced"}

// generateEarnCase draws one case from a seeded Rng.
//
// The distribution is CHOSEN rather than uniform, and every choice below is a case that a uniform
// draw over int64 would never produce in 200 tries: a balance exactly AT the cap (the boundary the
// trim's strictly-greater test turns on), a balance one centipoint above it, a negative balance, and
// a cap small enough that a single tick crosses it.
func generateEarnCase(rng strategy.Rng) earnCase {
	n := rng.IntN(25) + 1

	c := earnCase{
		Weights:     make([]int64, n),
		Roles:       make([]string, n),
		Balances:    make([]core.Centipoints, n),
		History:     make([]bool, n),
		TickAwardCp: core.Centipoints(rng.IntN(2_000) + 1),
		HardCapCp:   core.Centipoints((rng.IntN(20) + 1) * 500),
		OverCapBp:   int64(rng.IntN(5) * 2_500),
		GrantCp:     core.Centipoints(rng.IntN(50_000) + 1),
	}

	// The soft cap is drawn AT OR BELOW the hard cap, because validateCapConfig refuses the other
	// order — a soft cap above a hard one could never apply. Zero means "no soft cap" and is drawn
	// often, since a hard-cap-only pool is the common configuration.
	if rng.IntN(2) == 0 {
		c.SoftCapCp = c.HardCapCp - core.Centipoints(rng.IntN(int(c.HardCapCp)+1))
	}

	if c.SoftCapCp == 0 {
		c.OverCapBp = 0
	}

	for i := range n {
		c.Weights[i] = int64(rng.IntN(8))
		c.Roles[i] = earnRoles[rng.IntN(len(earnRoles))]
		c.History[i] = rng.IntN(3) == 0

		switch rng.IntN(6) {
		case 0:
			c.Balances[i] = c.HardCapCp // exactly at the ceiling
		case 1:
			c.Balances[i] = c.HardCapCp + 1 // one centipoint above it
		case 2:
			c.Balances[i] = -core.Centipoints(rng.IntN(5_000)) // in debt
		case 3:
			c.Balances[i] = 0
		case 4:
			c.Balances[i] = core.Centipoints(rng.IntN(100_000))
		default:
			c.Balances[i] = c.HardCapCp * core.Centipoints(rng.IntN(4))
		}
	}

	return c
}

// attendees turns a generated case into an attendance event's shares.
func (c earnCase) attendees() []strategy.Share {
	out := make([]strategy.Share, len(c.Weights))
	for i := range c.Weights {
		out[i] = strategy.Share{AccountID: acct(i), Weight: c.Weights[i], Role: c.Roles[i]}
	}

	return out
}

// ctx builds the façade for this case under the given config.
func (c earnCase) ctx(tb testing.TB, config string) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, len(c.Weights), 0, config)

	for i := range c.Weights {
		ctx.balances[acct(i)] = c.Balances[i]
		ctx.history[acct(i)] = c.History[i]
	}

	return ctx
}

// tickConfig, capConfig and startPointsConfig render the case's knobs as pool config documents. They
// are built with fmt rather than a struct literal so that the test exercises the same PARSER a pool's
// stored JSON goes through — a config built in Go would skip the decode this family's strictness
// lives in.
func (c earnCase) tickConfigJSON() string {
	return fmt.Sprintf(
		`{"tick_award_cp":%d,"default_multiplier_bp":7500,`+
			`"role_multipliers":[{"role":"standby","multiplier_bp":5000},`+
			`{"role":"bench","multiplier_bp":0},{"role":"present","multiplier_bp":10000}]}`,
		c.TickAwardCp)
}

func (c earnCase) capConfigJSON() string {
	return fmt.Sprintf(`{"hard_cap_cp":%d,"soft_cap_cp":%d,"over_cap_earn_bp":%d,"tick_award_cp":%d}`,
		c.HardCapCp, c.SoftCapCp, c.OverCapBp, c.TickAwardCp)
}

func (c earnCase) startPointsConfigJSON() string {
	return fmt.Sprintf(`{"grant_cp":%d}`, c.GrantCp)
}

// forEachEarnCase runs check over `propertyChecks` generated cases, failing with the seed that
// reproduces the first counterexample.
//
// One Rng per case, seeded base+i, rather than one for the whole run: a counterexample is then
// replayable on its own without replaying the i cases before it, which is what makes shrinking by
// hand practical.
func forEachEarnCase(t *testing.T, check func(t *testing.T, c earnCase) error) {
	t.Helper()

	base := propertySeed(t)
	checks := propertyChecks(t)

	t.Logf("%d cases from base seed %d", checks, base)

	for i := range checks {
		seed := base + int64(i)

		c := generateEarnCase(ledger.NewRng(seed))
		if err := check(t, c); err != nil {
			t.Fatalf("counterexample at seed %d (%d accounts, tick %d, hard cap %d, soft cap %d at "+
				"%d bp, grant %d): %v\nreplay with: DKP_PROPERTY_SEED=%d DKP_PROPERTY_CHECKS=1 go "+
				"test ./internal/strategy",
				seed, len(c.Weights), c.TickAwardCp, c.HardCapCp, c.SoftCapCp, c.OverCapBp,
				c.GrantCp, err, seed)
		}
	}
}

// planned is one generated plan and the strategy that produced it, so a property can walk every
// planner in the family without three copies of the loop.
type planned struct {
	name     string
	strategy strategy.PointStrategy
	proposal strategy.BatchProposal
}

// plannedEarnBatches plans every earn-family batch a case can produce.
//
// A planner that legitimately has nothing to do — every attendee at the cap, nobody above the
// ceiling, everybody already granted — returns ErrNothingToPlan and is SKIPPED rather than failed:
// that outcome is the point of those planners, and a property that treated it as a failure would be
// asserting the opposite of P6 and P7. Every other error fails the case.
func plannedEarnBatches(t *testing.T, c earnCase) ([]planned, error) {
	t.Helper()

	ev := strategy.AttendanceEvent{Attendees: c.attendees(), EffectiveAt: fixedNow}
	run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow}

	sources := []struct {
		name string
		s    strategy.PointStrategy
		plan func() (strategy.BatchProposal, error)
	}{
		{"tick attendance", strategy.Tick{}, func() (strategy.BatchProposal, error) {
			return strategy.Tick{}.PlanAttendance(c.ctx(t, c.tickConfigJSON()), ev)
		}},
		{"cap attendance", strategy.Cap{}, func() (strategy.BatchProposal, error) {
			return strategy.Cap{}.PlanAttendance(c.ctx(t, c.capConfigJSON()), ev)
		}},
		{"cap run", strategy.Cap{}, func() (strategy.BatchProposal, error) {
			return strategy.Cap{}.PlanDecay(c.ctx(t, c.capConfigJSON()), run)
		}},
		{"start_points run", strategy.StartPoints{}, func() (strategy.BatchProposal, error) {
			return strategy.StartPoints{}.PlanDecay(c.ctx(t, c.startPointsConfigJSON()), run)
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

// TestProperty_P5_EarnStrategies_ReversalIsAnExactInverse is P5 over the whole family: applying a
// batch and then its reversal restores every affected balance, exactly.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint on one account leaves a
// permanent, unexplainable discrepancy in a member's statement — and nobody finds it, because the
// original and its reversal both look right individually.
func TestProperty_P5_EarnStrategies_ReversalIsAnExactInverse(t *testing.T) {
	t.Parallel()

	reversed := 0

	forEachEarnCase(t, func(t *testing.T, c earnCase) error {
		batches, err := plannedEarnBatches(t, c)
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
				return fmt.Errorf("%s: the reversal is kind %q with target %v; a reversal that "+
					"points at nothing is an ordinary batch wearing the word",
					b.name, reversal.Kind, reversal.ReversesBatchID)
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

			reversed++
		}

		return nil
	})

	require.Positive(t, reversed,
		"no generated case produced a batch at all, so the property held vacuously — the generator, "+
			"not the reversal, is what is broken")
}

// TestProperty_P8_EarnStrategies_PlanByteIdentically is P8: the same (event, config, clock, seed)
// produces a byte-identical proposal.
//
// Two claims, and the second is the one that catches real bugs. Planning the same event twice must
// produce identical canonical bytes — which a planner that ranged over a map would fail
// intermittently — and planning it with the attendees SHUFFLED must produce the same bytes too,
// because a set of attendees is a set and the officer's upload order is not part of it.
func TestProperty_P8_EarnStrategies_PlanByteIdentically(t *testing.T) {
	t.Parallel()

	compared := 0

	forEachEarnCase(t, func(t *testing.T, c earnCase) error {
		first, err := plannedEarnBatches(t, c)
		if err != nil {
			return err
		}

		second, err := plannedEarnBatches(t, c)
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

		// The attendee order is the input a caller controls and must not influence the batch. It is
		// shuffled with a NEGATIVE seed, so the permutation is not the identity and the seeded Rng's
		// whole int64 range is exercised.
		shuffled := c
		shuffled.Weights = append([]int64(nil), c.Weights...)
		shuffled.Roles = append([]string(nil), c.Roles...)

		rng := ledger.NewRng(-int64(len(c.Weights)) - 1)
		attendees := shuffled.attendees()

		rng.Shuffle(len(attendees), func(i, j int) {
			attendees[i], attendees[j] = attendees[j], attendees[i]
		})

		for _, tc := range []struct {
			name   string
			plan   func(ctx strategy.Ctx, ev strategy.AttendanceEvent) (strategy.BatchProposal, error)
			config string
		}{
			{"tick", strategy.Tick{}.PlanAttendance, c.tickConfigJSON()},
			{"cap", strategy.Cap{}.PlanAttendance, c.capConfigJSON()},
		} {
			ordered, orderedErr := tc.plan(c.ctx(t, tc.config), strategy.AttendanceEvent{
				Attendees: c.attendees(), EffectiveAt: fixedNow,
			})
			mixed, mixedErr := tc.plan(c.ctx(t, tc.config), strategy.AttendanceEvent{
				Attendees: attendees, EffectiveAt: fixedNow,
			})

			if (orderedErr == nil) != (mixedErr == nil) {
				return fmt.Errorf("%s: the same attendees in a different order planned %v and %v",
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
				return fmt.Errorf("%s: the same attendees in a different order planned "+
					"differently:\n\t%s\n\t%s", tc.name, a, b)
			}
		}

		return nil
	})

	require.Positive(t, compared, "no batch was compared, so the property held vacuously")
}

// TestProperty_P6_Cap_ClampsAndIsIdempotent is P6: applying the cap twice produces one batch and
// never moves a balance past the cap.
//
// Three claims in one loop, because they are the same run seen at three moments: the trim moves every
// balance to EXACTLY the ceiling (never past it, never short of it), the second run finds nothing to
// do, and an earn planned after the trim cannot cross the ceiling either. The third is what makes the
// first two worth having — a cap that only trimmed would be re-trimming every raid night.
func TestProperty_P6_Cap_ClampsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	trimmed := 0

	forEachEarnCase(t, func(t *testing.T, c earnCase) error {
		ctx := c.ctx(t, c.capConfigJSON())
		run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow}

		p, err := strategy.Cap{}.PlanDecay(ctx, run)
		if err != nil && !errors.Is(err, strategy.ErrNothingToPlan) {
			return fmt.Errorf("cap run: %w", err)
		}

		if err == nil {
			trimmed++

			if net, ok := p.NetAmountCp(); !ok || net != 0 {
				return fmt.Errorf("the trim nets to %d (ok=%v), want exactly 0", net, ok)
			}

			for _, e := range p.Entries {
				if e.AmountCp == 0 {
					return fmt.Errorf("account %s is trimmed by 0; CHECK (amount_cp <> 0) means an "+
						"account at the ceiling gets no entry at all", e.AccountID)
				}

				ctx.balances[e.AccountID] += e.AmountCp
			}

			for i := range c.Weights {
				if got := ctx.balances[acct(i)]; got > c.HardCapCp {
					return fmt.Errorf("account %s is at %d after the trim, above the cap of %d",
						acct(i), got, c.HardCapCp)
				}

				// A balance that was above the ceiling must land ON it, not below: a trim that took
				// more than the excess would be confiscating points the ceiling did not ask for.
				if c.Balances[i] > c.HardCapCp && ctx.balances[acct(i)] != c.HardCapCp {
					return fmt.Errorf("account %s was at %d and is now at %d, want exactly the cap "+
						"of %d", acct(i), c.Balances[i], ctx.balances[acct(i)], c.HardCapCp)
				}
			}
		}

		// The second run, against the balances the first left.
		_, rerunErr := strategy.Cap{}.PlanDecay(ctx, run)
		if !errors.Is(rerunErr, strategy.ErrNothingToPlan) {
			return fmt.Errorf("a second cap run for the same period planned a batch (err=%v); "+
				"applying the cap twice must produce one batch", rerunErr)
		}

		// And an earn after the trim cannot cross the ceiling.
		earn, err := strategy.Cap{}.PlanAttendance(ctx, strategy.AttendanceEvent{
			Attendees: c.attendees(), EffectiveAt: fixedNow,
		})
		if err != nil {
			if errors.Is(err, strategy.ErrNothingToPlan) {
				return nil
			}

			return fmt.Errorf("cap attendance: %w", err)
		}

		for _, e := range earn.Entries {
			if e.AccountID == ledger.AccountIDGuildBank {
				continue
			}

			if after := ctx.balances[e.AccountID] + e.AmountCp; after > c.HardCapCp {
				return fmt.Errorf("account %s earns %d and lands at %d, above the cap of %d",
					e.AccountID, e.AmountCp, after, c.HardCapCp)
			}
		}

		return nil
	})

	require.Positive(t, trimmed,
		"no generated case had anybody above the ceiling, so the property held vacuously")
}

// TestProperty_P7_StartPoints_AppliesOncePerAccount is P7: the grant reaches each account exactly
// once, and never reaches an account that already has ledger history.
//
// The generator gives roughly a third of each roster prior history, which is the case that matters —
// a mature guild running the grant for the first time is exactly a roster where most accounts must be
// skipped and a few must not.
func TestProperty_P7_StartPoints_AppliesOncePerAccount(t *testing.T) {
	t.Parallel()

	granted := 0

	forEachEarnCase(t, func(t *testing.T, c earnCase) error {
		ctx := c.ctx(t, c.startPointsConfigJSON())
		run := strategy.DecayRun{PeriodKey: "2026-W31", AsOfSeq: 4, EffectiveAt: fixedNow}

		p, err := strategy.StartPoints{}.PlanDecay(ctx, run)
		if errors.Is(err, strategy.ErrNothingToPlan) {
			// Legal: every account on this roster already has history.
			for i := range c.Weights {
				if !c.History[i] {
					return fmt.Errorf("account %s has no history and was not granted", acct(i))
				}
			}

			return nil
		}

		if err != nil {
			return fmt.Errorf("start_points run: %w", err)
		}

		granted++

		seen := map[core.ULID]int{}

		for _, e := range p.Entries {
			if e.AccountID == ledger.AccountIDGuildBank {
				continue
			}

			seen[e.AccountID]++

			if e.AmountCp != c.GrantCp {
				return fmt.Errorf("account %s is granted %d, want the configured %d",
					e.AccountID, e.AmountCp, c.GrantCp)
			}
		}

		for i := range c.Weights {
			switch {
			case c.History[i] && seen[acct(i)] > 0:
				return fmt.Errorf("account %s has ledger history and was granted anyway — this is "+
					"the everyone-got-1000-points-again ticket", acct(i))
			case !c.History[i] && seen[acct(i)] != 1:
				return fmt.Errorf("account %s has no history and appears %d times, want exactly 1",
					acct(i), seen[acct(i)])
			}
		}

		// Commit it, exactly as the ledger would: the grant is itself history.
		for _, e := range p.Entries {
			ctx.balances[e.AccountID] += e.AmountCp
			ctx.history[e.AccountID] = true
		}

		// A re-run in the same period AND in a fresh one must both grant nothing. The second is the
		// layer the unique index on (pool_id, kind, cadence_period) cannot provide.
		for _, period := range []string{"2026-W31", "2026-W32"} {
			_, err := strategy.StartPoints{}.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: period, AsOfSeq: 9, EffectiveAt: fixedNow,
			})
			if !errors.Is(err, strategy.ErrNothingToPlan) {
				return fmt.Errorf("a second run in period %s planned a batch (err=%v); the grant "+
					"applies exactly once per account", period, err)
			}
		}

		return nil
	})

	require.Positive(t, granted,
		"no generated case granted anybody, so the property held vacuously")
}

// TestProperty_NoFloat_EarnConfigSchemas_DeclareNoNumber walks each shipped schema for a `number`
// type, which permits a decimal. The proposal TYPES are covered by
// TestProperty_NoFloat_AppearsAnywhereInAProposal; this is the config half, and it recurses into
// nested properties because a role multiplier inside an array is as much part of the config as a
// top-level knob.
func TestProperty_NoFloat_EarnConfigSchemas_DeclareNoNumber(t *testing.T) {
	t.Parallel()

	for _, s := range strategy.Catalogue() {
		t.Run(s.ID(), func(t *testing.T) {
			t.Parallel()

			requireNoNumberType(t, s.ConfigSchema())
		})
	}
}
