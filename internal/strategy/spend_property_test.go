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

// The properties the spend family owes. Phase 1, #195.
//
// Example tests prove the case you thought of; properties prove the cases you did not. These are the
// repository's own numbered ones where they touch a spend rule — P2 (credits sum to the debit
// exactly), P5 (reversal is an exact inverse), P8 (determinism) and the planner-side half of P4 (no
// bid exceeds the balance) — plus one per strategy for the arithmetic that is this family's alone:
// the second-price bound, the maximal winner, the frozen share, and a roll that stays inside its
// range.
//
// ON THE GENERATOR. testing/quick's generator interface takes a *math/rand.Rand, and importing
// math/rand ANYWHERE under internal/strategy trips repo gate PURE002, test files included. So the
// cases here are drawn from ledger.NewRng — the same PCG source a strategy would be handed — and the
// BASE SEED IS PRINTED, which makes a failure reproducible in a way a time-seeded quick.Check is not.
// The budget is the repository's: 200 checks per PR, 20,000 nightly, both DKP_PROPERTY_CHECKS, and
// DKP_PROPERTY_SEED replays a counterexample. propertySeed and propertyChecks are shared with
// fixed_price_test.go.

// spendPropCase is one generated session: a roster with balances, the bids placed into it, the price
// an award settles at, and the knobs to run all of it under.
type spendPropCase struct {
	Balances []core.Centipoints
	Amounts  []core.Centipoints
	PlacedAt []core.Micros
	Weights  []int64

	PriceCp     core.Centipoints
	MinBidCp    core.Centipoints
	IncrementCp core.Centipoints
	RollMin     int64
	RollMax     int64

	// SessionMinCp is the minimum the SESSION names, which is drawn unset, above and BELOW the
	// pool's. The last of those is the one worth generating: a session may raise the floor and may
	// not lower it, and a settlement that honoured a lowered one would award below a guild rule while
	// every other assertion — eligible bid, exact split, zero-sum batch — still passed.
	SessionMinCp core.Centipoints

	// AllBelowFloor puts EVERY bid under the pool's minimum, and it is drawn as its own shape rather
	// than left to chance. An open auction charges the winner's own bid, so a lowered session minimum
	// changes its outcome only when no bid clears the pool's floor at all — otherwise the highest bid
	// of a larger eligible set is the same bid, and the property passes while the bug is present.
	// Drawing that shape independently is what makes the regression reachable in every run rather
	// than in the runs where a dozen independent draws happened to agree.
	AllBelowFloor bool
}

// session is the bid session a generated case settles in.
func (c spendPropCase) session() strategy.Session {
	return strategy.Session{ID: acct(60), SeqAtOpen: 3, MinAmountCp: c.SessionMinCp}
}

// effectiveMinimum is what the floor must actually be: the higher of the two, restated here rather
// than taken from the code under test — a floor computed by calling sessionMinimum would agree with
// it by construction, including when both are wrong.
func (c spendPropCase) effectiveMinimum() core.Centipoints {
	if c.SessionMinCp > c.MinBidCp {
		return c.SessionMinCp
	}

	return c.MinBidCp
}

// generateSpendPropCase draws one case from a seeded Rng.
//
// The distribution is CHOSEN rather than uniform, and each choice below is a case a uniform draw over
// int64 would never produce in 200 tries: bids EQUAL to each other (the tie that reaches the seeded
// roll), bids one increment apart (the second-price clamp), a bid exactly at the minimum, a balance of
// zero (no share to bid), a bid exactly equal to its balance (a 100% relative bid), and prices that
// are prime or below the number of beneficiaries (the largest-remainder edges P2 names).
func generateSpendPropCase(rng strategy.Rng) spendPropCase {
	n := rng.IntN(12) + 1

	c := spendPropCase{
		Balances:    make([]core.Centipoints, n),
		Amounts:     make([]core.Centipoints, n),
		PlacedAt:    make([]core.Micros, n),
		Weights:     make([]int64, n),
		MinBidCp:    core.Centipoints((rng.IntN(20) + 1) * 100),
		IncrementCp: core.Centipoints((rng.IntN(8) + 1) * 25),
		RollMin:     int64(rng.IntN(5)),
	}

	c.RollMax = c.RollMin + int64(rng.IntN(1_000)) + 1

	// Unset, raised, and lowered — a third of the cases each. The lowered draw is the regression the
	// AO reviewer found by reading sessionMinimum: it must change nothing about the outcome.
	switch rng.IntN(3) {
	case 0:
		c.SessionMinCp = 0
	case 1:
		c.SessionMinCp = c.MinBidCp * 2
	default:
		c.SessionMinCp = c.MinBidCp / 2
	}

	c.AllBelowFloor = rng.IntN(6) == 0

	switch rng.IntN(4) {
	case 0:
		c.PriceCp = core.Centipoints(rng.IntN(30) + 1) // frequently below the beneficiary count
	case 1:
		c.PriceCp = core.Centipoints(propertyPrimes[rng.IntN(len(propertyPrimes))])
	case 2:
		c.PriceCp = core.Centipoints(rngInt64(rng, 1_000_000) + 1)
	default:
		// Large enough that price * weight overflows int64 if the allocator multiplies before
		// dividing, which is the bug the 128-bit product in ledger.Allocate exists to prevent.
		c.PriceCp = core.Centipoints(rngInt64(rng, 1<<60) + 1)
	}

	// A shared amount, drawn once, so that ties actually occur: two independently drawn int64s never
	// collide, and the tie is the branch that reaches the seeded roll.
	shared := c.MinBidCp + core.Centipoints(rng.IntN(40))*c.IncrementCp

	for i := range n {
		c.Weights[i] = int64(rng.IntN(8))
		// Only four distinct placement times, so that bids equal in amount are frequently equal in
		// time too — which is the pair that reaches the seeded roll rather than the timestamp step.
		// Added as MICROSECONDS rather than through Micros.Add, whose argument is a time.Duration:
		// importing `time` anywhere under internal/strategy trips repo gate PURE002, test files
		// included, and a test is not exempt from the rule it exists to prove.
		c.PlacedAt[i] = fixedNow + core.Micros(rng.IntN(4))*1_000_000

		switch rng.IntN(6) {
		case 0:
			c.Balances[i] = 0
		case 1:
			c.Balances[i] = c.MinBidCp
		default:
			c.Balances[i] = core.Centipoints(rng.IntN(1_000_000) + 1)
		}

		if c.AllBelowFloor {
			c.Amounts[i] = c.MinBidCp / 2

			continue
		}

		switch rng.IntN(6) {
		case 0:
			c.Amounts[i] = shared // the tie
		case 1:
			c.Amounts[i] = shared + c.IncrementCp // one increment above it
		case 2:
			c.Amounts[i] = c.MinBidCp // exactly at the floor
		case 3:
			c.Amounts[i] = c.Balances[i] // the whole bank: a 100% relative bid
		case 4:
			// BELOW the pool's floor and at or above a halved session minimum — the one bid a session
			// that could lower the floor would wrongly make eligible. Without it the lowered draw
			// changes no outcome, because every other amount clears the pool's floor anyway and the
			// highest of a larger eligible set is the same bid.
			c.Amounts[i] = c.MinBidCp / 2
		default:
			c.Amounts[i] = c.MinBidCp + core.Centipoints(rng.IntN(200))*c.IncrementCp
		}
	}

	return c
}

// bids turns a generated case into the session's bids. `sealed` is what auction_sealed requires and
// auction_open refuses, so it is a parameter rather than a field.
func (c spendPropCase) bids(sealed bool) []strategy.Bid {
	out := make([]strategy.Bid, len(c.Amounts))
	for i := range c.Amounts {
		out[i] = strategy.Bid{
			AccountID: acct(i), AmountCp: c.Amounts[i], PlacedAt: c.PlacedAt[i], Sealed: sealed,
		}
	}

	return out
}

// entrants turns a generated case into a roll's entries: one per account, no amounts.
func (c spendPropCase) entrants() []strategy.Bid {
	out := make([]strategy.Bid, len(c.Amounts))
	for i := range out {
		out[i] = strategy.Bid{AccountID: acct(i)}
	}

	return out
}

// beneficiaries turns a generated case into an award's split.
func (c spendPropCase) beneficiaries() []strategy.Share {
	out := make([]strategy.Share, len(c.Weights))
	for i, w := range c.Weights {
		out[i] = strategy.Share{AccountID: acct(i), Weight: w}
	}

	return out
}

// ctx builds the façade for this case under the given config.
func (c spendPropCase) ctx(tb testing.TB, config string) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, len(c.Balances), 0, config)
	for i := range c.Balances {
		ctx.balances[acct(i)] = c.Balances[i]
	}

	return ctx
}

// The generated configs. They are built with fmt rather than as struct literals so that the test
// exercises the same PARSER a pool's stored JSON goes through — a config built in Go would skip the
// strict decode this family's config handling lives in.
func (c spendPropCase) openConfigJSON() string {
	return fmt.Sprintf(`{"min_bid_cp":%d,"increment_cp":%d,"proceeds":"attendees"}`,
		c.MinBidCp, c.IncrementCp)
}

func (c spendPropCase) sealedConfigJSON(payRule string) string {
	return fmt.Sprintf(`{"pay_rule":%q,"min_bid_cp":%d,"increment_cp":%d,"proceeds":"attendees"}`,
		payRule, c.MinBidCp, c.IncrementCp)
}

func (c spendPropCase) relativeConfigJSON() string {
	return `{"min_bid_bp":0,"max_bid_bp":10000,"proceeds":"attendees"}`
}

func (c spendPropCase) rollConfigJSON() string {
	return fmt.Sprintf(`{"roll_min":%d,"roll_max":%d,"win_cost_cp":100,"proceeds":"attendees"}`,
		c.RollMin, c.RollMax)
}

// spendAwardEvents is the same award put to all four strategies, with the generated price.
func (c spendPropCase) awardEvent() strategy.AwardEvent {
	price := c.PriceCp

	return strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Generated"},
		PriceCp:       &price,
		Beneficiaries: c.beneficiaries(),
		EffectiveAt:   fixedNow,
	}
}

// spendPlanners is every (strategy, config) pair a generated case is planned against.
func (c spendPropCase) planners() []struct {
	name   string
	s      strategy.PointStrategy
	config string
} {
	return []struct {
		name   string
		s      strategy.PointStrategy
		config string
	}{
		{"auction_open", strategy.AuctionOpen{}, c.openConfigJSON()},
		{"auction_sealed", strategy.AuctionSealed{}, c.sealedConfigJSON("second_price")},
		{"relative_bid", strategy.RelativeBid{}, c.relativeConfigJSON()},
		{"roll", strategy.Roll{}, c.rollConfigJSON()},
	}
}

// forEachSpendCase runs check over `propertyChecks` generated cases, failing with the seed that
// reproduces the first counterexample.
//
// One Rng per case, seeded base+i, rather than one for the whole run: a counterexample is then
// replayable on its own without replaying the i cases before it, which is what makes shrinking by
// hand practical.
func forEachSpendCase(t *testing.T, check func(t *testing.T, c spendPropCase) error) {
	t.Helper()

	base := propertySeed(t)
	checks := propertyChecks(t)

	t.Logf("%d cases from base seed %d", checks, base)

	for i := range checks {
		seed := base + int64(i)

		c := generateSpendPropCase(ledger.NewRng(seed))
		if err := check(t, c); err != nil {
			t.Fatalf("counterexample at seed %d (%d accounts, price %d, minimum %d, increment %d, "+
				"roll %d–%d): %v\nreplay with: DKP_PROPERTY_SEED=%d DKP_PROPERTY_CHECKS=1 go test "+
				"./internal/strategy",
				seed, len(c.Amounts), c.PriceCp, c.MinBidCp, c.IncrementCp, c.RollMin, c.RollMax,
				err, seed)
		}
	}
}

// TestProperty_P2_SpendStrategies_CreditsSumToTheDebitExactly is P2 at the strategy level, over all
// four: for every (price, N) the credits sum to exactly the price, and the batch therefore sums to
// exactly zero.
//
// internal/ledger's P2 proves the allocator; this proves the PLANNERS. One that rounded its own debit,
// or emitted a zero credit, would pass the allocator's property and fail this one.
func TestProperty_P2_SpendStrategies_CreditsSumToTheDebitExactly(t *testing.T) {
	t.Parallel()

	checked := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		for _, p := range c.planners() {
			proposal, err := p.s.PlanAward(c.ctx(t, p.config), c.awardEvent())
			if err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}

			if proposal.Entries[0].AmountCp != -c.PriceCp {
				return fmt.Errorf("%s: the debit is %d, want the whole price %d — a planner that "+
					"rounded its own debit would balance against rounded credits and still be wrong",
					p.name, proposal.Entries[0].AmountCp, -c.PriceCp)
			}

			var credits core.Centipoints

			for i, e := range proposal.Entries[1:] {
				if e.AmountCp == 0 {
					return fmt.Errorf("%s: credit %d moves 0 centipoints; CHECK (amount_cp <> 0) means "+
						"a zero share must be dropped, never written", p.name, i)
				}

				credits += e.AmountCp
			}

			if credits != c.PriceCp {
				return fmt.Errorf("%s: the credits sum to %d, want exactly the price %d",
					p.name, credits, c.PriceCp)
			}

			if net, ok := proposal.NetAmountCp(); !ok || net != 0 {
				return fmt.Errorf("%s: the batch nets to %d (ok=%v), want exactly 0", p.name, net, ok)
			}

			checked++
		}

		return nil
	})

	require.Positive(t, checked, "no award was planned, so the property held vacuously")
}

// TestProperty_P5_SpendStrategies_ReversalIsAnExactInverse is P5 over the family: applying a batch and
// then its reversal restores every affected balance, exactly.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint on one account leaves a
// permanent, unexplainable discrepancy in a member's statement — and nobody finds it, because the
// original and its reversal both look right individually.
func TestProperty_P5_SpendStrategies_ReversalIsAnExactInverse(t *testing.T) {
	t.Parallel()

	reversed := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		for _, p := range c.planners() {
			award, err := p.s.PlanAward(c.ctx(t, p.config), c.awardEvent())
			if err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}

			delta := map[core.ULID]core.Centipoints{}
			for _, e := range award.Entries {
				delta[e.AccountID] += e.AmountCp
			}

			reversal, err := p.s.PlanReversal(c.ctx(t, ""), strategy.LedgerBatch{
				ID:              acct(70),
				Kind:            award.Kind,
				StrategyID:      award.StrategyID,
				StrategyVersion: award.StrategyVersion,
				EffectiveAt:     award.EffectiveAt,
				Entries:         award.Entries,
			})
			if err != nil {
				return fmt.Errorf("%s: reverse: %w", p.name, err)
			}

			if reversal.Kind != strategy.KindReversal || reversal.ReversesBatchID == nil {
				return fmt.Errorf("%s: the reversal is kind %q with target %v; a reversal that points "+
					"at nothing is an ordinary batch wearing the word",
					p.name, reversal.Kind, reversal.ReversesBatchID)
			}

			for _, inv := range reversal.Invariants {
				if inv.Kind == strategy.InvariantNonNegative {
					return fmt.Errorf("%s: the reversal declares a floor, which does not prevent a "+
						"debt — it prevents the correction, and an append-only ledger has no other "+
						"repair primitive", p.name)
				}
			}

			for _, e := range reversal.Entries {
				delta[e.AccountID] += e.AmountCp
			}

			for id, v := range delta {
				if v != 0 {
					return fmt.Errorf("%s: account %s is %d centipoints from where it started",
						p.name, id, v)
				}
			}

			reversed++
		}

		return nil
	})

	require.Positive(t, reversed, "no batch was reversed, so the property held vacuously")
}

// TestProperty_P8_SpendStrategies_PlanByteIdentically is P8: the same (event, config, clock, seed)
// produces a byte-identical proposal.
//
// Two claims, and the second is the one that catches real bugs. Planning the same award twice must
// produce identical canonical bytes — which a planner that ranged over a map would fail
// intermittently — and planning it with the beneficiaries SHUFFLED must produce the same bytes too,
// because a set of beneficiaries is a set and the caller's assembly order is not part of it.
func TestProperty_P8_SpendStrategies_PlanByteIdentically(t *testing.T) {
	t.Parallel()

	compared := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		// A NEGATIVE seed, so the permutation is not the identity and the seeded Rng's whole int64
		// range is exercised.
		rng := ledger.NewRng(-int64(len(c.Weights)) - 1)
		shuffled := c.beneficiaries()

		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		for _, p := range c.planners() {
			first, err := p.s.PlanAward(c.ctx(t, p.config), c.awardEvent())
			if err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}

			second, err := p.s.PlanAward(c.ctx(t, p.config), c.awardEvent())
			if err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}

			mixedEvent := c.awardEvent()
			mixedEvent.Beneficiaries = shuffled

			mixed, err := p.s.PlanAward(c.ctx(t, p.config), mixedEvent)
			if err != nil {
				return fmt.Errorf("%s (shuffled): %w", p.name, err)
			}

			a, err := first.Canonical()
			if err != nil {
				return err
			}

			b, err := second.Canonical()
			if err != nil {
				return err
			}

			m, err := mixed.Canonical()
			if err != nil {
				return err
			}

			if string(a) != string(b) {
				return fmt.Errorf("%s: two plans of the same award differ:\n\t%s\n\t%s", p.name, a, b)
			}

			if string(a) != string(m) {
				return fmt.Errorf("%s: the same beneficiaries in a different order planned "+
					"differently:\n\t%s\n\t%s", p.name, a, m)
			}

			compared++
		}

		return nil
	})

	require.Positive(t, compared, "no proposal was compared, so the property held vacuously")
}

// TestProperty_P4_SpendStrategies_ValidateBid_NeverAcceptsMoreThanTheBalance is the planner-side half
// of P4: no accepted bid exceeds what the bidder holds.
//
// The whole property is a state machine over holds, sessions and settlements and belongs with the FSM
// in Phase 6. What is provable HERE is the half that lives in a pure function — and it is the half a
// bidding UI leans on, because it is what turns "insufficient balance" into a 409 at bid time rather
// than a failed settlement at 01:00.
func TestProperty_P4_SpendStrategies_ValidateBid_NeverAcceptsMoreThanTheBalance(t *testing.T) {
	t.Parallel()

	accepted, rejected := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		for _, p := range []struct {
			name   string
			s      strategy.PointStrategy
			config string
			sealed bool
		}{
			{"auction_open", strategy.AuctionOpen{}, c.openConfigJSON(), false},
			{"auction_sealed", strategy.AuctionSealed{}, c.sealedConfigJSON("second_price"), true},
			{"relative_bid", strategy.RelativeBid{}, c.relativeConfigJSON(), false},
		} {
			ctx := c.ctx(t, p.config)

			for i, bid := range c.bids(p.sealed) {
				err := p.s.ValidateBid(ctx, strategy.AccountRef{ID: acct(i), Kind: "person"}, bid)
				if err == nil {
					if bid.AmountCp > c.Balances[i] {
						return fmt.Errorf("%s: a bid of %d was accepted from an account holding %d",
							p.name, bid.AmountCp, c.Balances[i])
					}

					accepted++

					continue
				}

				if !errors.Is(err, strategy.ErrInvalidEvent) {
					return fmt.Errorf("%s: a rejected bid must say so with ErrInvalidEvent: %w",
						p.name, err)
				}

				rejected++
			}
		}

		return nil
	})

	require.Positive(t, accepted, "no bid was ever accepted, so the property held vacuously")
	require.Positive(t, rejected, "no bid was ever rejected, so the generator draws nothing hard")
}

// TestProperty_AuctionOpen_TheWinnerHoldsAMaximalBid: whoever wins holds a bid at least as large as
// every other eligible one, pays exactly it, and never clears less than the floor that applied.
//
// THE FLOOR IS THE HIGHER OF THE POOL'S AND THE SESSION'S, which is where the generated session
// minimum earns its keep: a settlement that let a session LOWER the pool's floor would award a bid
// the guild's own settings refuse, and every other assertion in this file would still pass.
func TestProperty_AuctionOpen_TheWinnerHoldsAMaximalBid(t *testing.T) {
	t.Parallel()

	settled := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		bids := c.bids(false)
		floor := c.effectiveMinimum()

		res, err := strategy.AuctionOpen{}.SettleAuction(c.ctx(t, c.openConfigJSON()), c.session(), bids)
		if err != nil {
			return err
		}

		if len(res.Winners) == 0 {
			return nil // every bid was below the minimum: the rot case
		}

		if res.Winners[0].AmountCp < floor {
			return fmt.Errorf("the winner pays %d, below the floor of %d (pool %d, session %d); a "+
				"session may raise the pool's minimum and may not lower it",
				res.Winners[0].AmountCp, floor, c.MinBidCp, c.SessionMinCp)
		}

		var top core.Centipoints
		for _, b := range bids {
			if b.AmountCp >= floor && b.AmountCp > top {
				top = b.AmountCp
			}
		}

		if res.Winners[0].AmountCp != top {
			return fmt.Errorf("the winner pays %d and the highest eligible bid is %d; an open auction "+
				"charges the leader their own bid", res.Winners[0].AmountCp, top)
		}

		settled++

		return nil
	})

	require.Positive(t, settled, "no auction settled, so the property held vacuously")
}

// TestProperty_AuctionSealed_SecondPrice_IsBoundedByTheWinningBidAndTheMinimum.
//
// Two bounds, both of which a guild would notice being broken and neither of which a single example
// test covers across the whole input space: nobody ever pays more than they bid — the promise second
// price exists to make — and nobody ever pays less than the minimum, which is what a sole bidder pays
// and therefore the floor of the whole rule.
func TestProperty_AuctionSealed_SecondPrice_IsBoundedByTheWinningBidAndTheMinimum(t *testing.T) {
	t.Parallel()

	settled := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		bids := c.bids(true)
		floor := c.effectiveMinimum()

		res, err := strategy.AuctionSealed{}.SettleAuction(
			c.ctx(t, c.sealedConfigJSON("second_price")), c.session(), bids)
		if err != nil {
			return err
		}

		if len(res.Winners) == 0 {
			return nil
		}

		var winning core.Centipoints

		for _, b := range bids {
			if b.AccountID == res.Winners[0].AccountID && b.AmountCp > winning {
				winning = b.AmountCp
			}
		}

		price := res.Winners[0].AmountCp

		if price > winning {
			return fmt.Errorf("the winner pays %d having bid %d; second price may never charge more "+
				"than the winning bid", price, winning)
		}

		// The lower bound is the FLOOR THAT APPLIED, not the pool's alone: a sole bidder pays the
		// minimum, so a session that could lower it would hand them a discount nobody configured.
		if price < floor {
			return fmt.Errorf("the winner pays %d, below the floor of %d (pool %d, session %d)",
				price, floor, c.MinBidCp, c.SessionMinCp)
		}

		settled++

		return nil
	})

	require.Positive(t, settled, "no auction settled, so the property held vacuously")
}

// TestProperty_RelativeBid_TheWinnerHoldsTheLargestFrozenShare, and every balance the settlement read
// was read at seq_at_open.
//
// The second half is the one that cannot be seen from the outcome: a planner reading HeadSeq would
// pick the same winner in every case whose balances did not move, and would silently re-price
// everybody's bid on the one night a decay run overlaps a raid.
func TestProperty_RelativeBid_TheWinnerHoldsTheLargestFrozenShare(t *testing.T) {
	t.Parallel()

	settled := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		const seqAtOpen = int64(3)

		ctx := c.ctx(t, c.relativeConfigJSON())

		res, err := strategy.RelativeBid{}.SettleAuction(ctx,
			strategy.Session{ID: acct(60), SeqAtOpen: seqAtOpen}, c.bids(false))
		if err != nil {
			return err
		}

		for _, seq := range ctx.readAtSeq {
			if seq != seqAtOpen {
				return fmt.Errorf("a balance was read at seq %d rather than at the session's %d; a "+
					"share resolved against a live balance is one a concurrent decay run can rewrite",
					seq, seqAtOpen)
			}
		}

		if len(res.Winners) == 0 {
			return nil
		}

		best := int64(-1)

		var winnerShare int64

		for i, b := range c.bids(false) {
			if b.AmountCp <= 0 || c.Balances[i] <= 0 || b.AmountCp > c.Balances[i] {
				continue
			}

			// The same integer arithmetic the strategy uses, restated rather than reused: a share
			// computed by calling the code under test would agree with it by construction.
			share := int64(b.AmountCp) * 10_000 / int64(c.Balances[i])
			if share > best {
				best = share
			}

			if b.AccountID == res.Winners[0].AccountID && share > winnerShare {
				winnerShare = share
			}
		}

		if winnerShare != best {
			return fmt.Errorf("the winner committed %d bp and the largest bidable share was %d bp",
				winnerShare, best)
		}

		settled++

		return nil
	})

	require.Positive(t, settled, "no session settled, so the property held vacuously")
}

// TestProperty_Roll_EveryRoundIsInRangeAndReplayable.
//
// Three claims. The winning roll is inside the configured range — a die that can come up outside its
// faces is not a die. The round is replayable: the same seed settles identically, whatever order the
// entries arrived in. And a tied round awards nobody, which is what makes the first two matter — a
// settlement that quietly picked one of two 97s would be an edit of an immutable round.
func TestProperty_Roll_EveryRoundIsInRangeAndReplayable(t *testing.T) {
	t.Parallel()

	won, tied := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		entrants := c.entrants()

		res, err := strategy.Roll{}.SettleAuction(c.ctx(t, c.rollConfigJSON()),
			strategy.Session{ID: acct(60)}, entrants)
		if err != nil {
			return err
		}

		if res.RngSeed == nil {
			return errors.New("a round that rolled must report the seed it rolled from")
		}

		reversedEntrants := make([]strategy.Bid, len(entrants))
		for i := range entrants {
			reversedEntrants[i] = entrants[len(entrants)-1-i]
		}

		replay, err := strategy.Roll{}.SettleAuction(c.ctx(t, c.rollConfigJSON()),
			strategy.Session{ID: acct(60)}, reversedEntrants)
		if err != nil {
			return err
		}

		if replay.Reason != res.Reason || len(replay.Winners) != len(res.Winners) {
			return fmt.Errorf("the same round settled two ways:\n\t%s\n\t%s", res.Reason, replay.Reason)
		}

		if len(res.Winners) == 0 {
			tied++

			return nil
		}

		if replay.Winners[0] != res.Winners[0] {
			return fmt.Errorf("the same round awarded %s and then %s",
				res.Winners[0].AccountID, replay.Winners[0].AccountID)
		}

		roll := winningRoll(t, res.Reason)
		if roll < c.RollMin || roll > c.RollMax {
			return fmt.Errorf("the winning roll is %d, outside the configured %d–%d",
				roll, c.RollMin, c.RollMax)
		}

		won++

		return nil
	})

	require.Positive(t, won, "no roll was ever won, so the property held vacuously")
	require.Positive(t, tied, "no round ever tied, so the awards-nobody branch is unexercised")
}

// TestProperty_NoFloat_SpendConfigSchemas_DeclareNoNumber walks every nested property of every shipped
// spend schema. `number` permits 12.5, and a decimal in the point path is a float.
func TestProperty_NoFloat_SpendConfigSchemas_DeclareNoNumber(t *testing.T) {
	t.Parallel()

	for _, s := range []strategy.PointStrategy{
		strategy.AuctionOpen{}, strategy.AuctionSealed{}, strategy.RelativeBid{}, strategy.Roll{},
	} {
		t.Run(s.ID(), func(t *testing.T) {
			t.Parallel()

			requireNoNumberType(t, s.ConfigSchema())
		})
	}
}
