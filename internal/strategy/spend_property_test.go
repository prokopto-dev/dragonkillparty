package strategy_test

import (
	"errors"
	"fmt"
	"slices"
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

	// Tiers is the rung each bid was placed from, and its distribution is drawn as a SHAPE per case
	// rather than per bid: a third of cases record no tier at all — which is every session in
	// production until the bid FSM fills the field in — a third put every bid on one named rung, and a
	// third mix them. Only the third shape lets the ladder decide anything, and it is also the only
	// one in which the largest bid in the session is regularly on a rung that cannot win, which is the
	// case the tier properties below exist to reach.
	Tiers []string

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

// untiedBids is the generated case's bids with every tie broken by construction: each amount nudged
// apart by its index, keeping the order and the rungs the case drew.
//
// IT IS WHAT KEEPS THE PRICE PROPERTIES AT FULL STRENGTH. A sealed auction REPORTS an equal-amount
// tie instead of resolving it, and the generator draws ties deliberately — a shared amount, so that
// two bids collide — so the properties about what a winner PAYS would quietly stop seeing those
// cases: they skip a resolution with no winner. Separating the amounts keeps every generated case
// producing a settlement to check the price arithmetic against, including the shapes that used to tie
// — a bid at the floor, a bid one increment above another, the whole bank. What the tie itself does
// is the two tie properties' subject, on the bids as drawn.
//
// The nudge is upward and by the INDEX, so it cannot collide with another nudged bid, and it is
// applied to a copy: a property that reordered the case's own slice would change what the properties
// after it are running on.
func (c spendPropCase) untiedBids(sealed bool) []strategy.Bid {
	out := c.bids(sealed)
	for i := range out {
		if out[i].AmountCp > 0 {
			out[i].AmountCp += core.Centipoints(i)
		}
	}

	return out
}

// tiedInWinningTier is who a sealed settlement must name as tied: the distinct accounts holding the
// HIGHEST eligible bid on the rung that takes the item, ascending, and fewer than two of them means
// there is no tie.
//
// RESTATED FROM THE ISSUE RATHER THAN TAKEN FROM THE CODE UNDER TEST, like every other expectation in
// this file. A tie set computed by calling tiedAccounts would agree with the settlement by
// construction, including when both reach below the winning rung.
func (c spendPropCase) tiedInWinningTier(bids []strategy.Bid) []core.ULID {
	inTier := c.inWinningTier(bids)

	var top core.Centipoints

	for _, b := range inTier {
		if b.AmountCp > top {
			top = b.AmountCp
		}
	}

	out := make([]core.ULID, 0, len(inTier))

	for _, b := range inTier {
		if b.AmountCp == top && !slices.Contains(out, b.AccountID) {
			out = append(out, b.AccountID)
		}
	}

	slices.Sort(out)

	if len(out) < 2 {
		return nil
	}

	return out
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
		Tiers:       make([]string, n),
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

	drawTiers(rng, &c)

	return c
}

// drawTiers fills in the rung each bid was placed from — see the Tiers field for the three shapes.
//
// DRAWN AFTER EVERY OTHER FIELD, and that is not tidiness. The draws are a single seeded sequence, so
// taking two of them in the middle of the loop above renumbered every balance, amount and placement
// time behind it: the pre-#224 properties would then be running different cases under the same seed,
// and `roll`'s tie — two entrants landing on the same face, which the whole awards-nobody branch
// depends on — stopped occurring in a 200-case run. Appending here leaves every existing case exactly
// as it was and adds the ladder on top of it.
func drawTiers(rng strategy.Rng, c *spendPropCase) {
	ladder := strategy.Tiers()
	shape := rng.IntN(3)
	oneRung := ladder[rng.IntN(len(ladder))]

	for i := range c.Tiers {
		switch shape {
		case 0: // every session in production today: nothing records a tier at all
		case 1:
			c.Tiers[i] = oneRung
		default:
			// The empty string is drawn alongside the four rungs rather than excluded: `anyone` and
			// "no tier recorded" must settle identically, and a session mixing the two is where a
			// normalisation that ran in only one of the two paths would show up.
			if pick := rng.IntN(len(ladder) + 1); pick < len(ladder) {
				c.Tiers[i] = ladder[pick]
			}
		}
	}
}

// ladderRank is where a rung stands, higher first, RESTATED from canonical §5 rather than taken from
// the code under test: the order is the rule, and a test that asked the package for it would be
// asserting the package against itself.
func ladderRank(tier string) int {
	if tier == "" {
		tier = strategy.TierAnyone
	}

	for i, rung := range strategy.Tiers() {
		if rung == tier {
			return len(strategy.Tiers()) - i
		}
	}

	return 0
}

// bids turns a generated case into the session's bids. `sealed` is what auction_sealed requires and
// auction_open refuses, so it is a parameter rather than a field.
func (c spendPropCase) bids(sealed bool) []strategy.Bid {
	out := make([]strategy.Bid, len(c.Amounts))
	for i := range c.Amounts {
		out[i] = strategy.Bid{
			AccountID: acct(i), AmountCp: c.Amounts[i], PlacedAt: c.PlacedAt[i], Sealed: sealed,
			Tier: c.Tiers[i],
		}
	}

	return out
}

// winningTier is the rung a settlement must award on: the highest one holding a bid at or above the
// floor that applied.
//
// RESTATED HERE RATHER THAN TAKEN FROM THE CODE UNDER TEST, like every other expectation in this
// file: a winning tier computed by calling resolveTier would agree with the settlement by
// construction, including when both are wrong. Empty when nothing is eligible, which is the rot case.
func (c spendPropCase) winningTier() string {
	best := ""
	bestRank := 0
	floor := c.effectiveMinimum()

	for i, tier := range c.Tiers {
		if c.Amounts[i] < floor || c.Amounts[i] <= 0 {
			continue
		}

		if tier == "" {
			tier = strategy.TierAnyone
		}

		if rank := ladderRank(tier); rank > bestRank {
			best, bestRank = tier, rank
		}
	}

	return best
}

// inWinningTier is the eligible bids standing on the rung that takes the item, with their index in
// the generated case.
func (c spendPropCase) inWinningTier(bids []strategy.Bid) []strategy.Bid {
	winning := c.winningTier()
	floor := c.effectiveMinimum()

	out := make([]strategy.Bid, 0, len(bids))

	for _, b := range bids {
		tier := b.Tier
		if tier == "" {
			tier = strategy.TierAnyone
		}

		if tier == winning && b.AmountCp >= floor && b.AmountCp > 0 {
			out = append(out, b)
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
// every other eligible one ON THE WINNING RUNG, pays exactly it, and never clears less than the floor
// that applied.
//
// THE FLOOR IS THE HIGHER OF THE POOL'S AND THE SESSION'S, which is where the generated session
// minimum earns its keep: a settlement that let a session LOWER the pool's floor would award a bid
// the guild's own settings refuse, and every other assertion in this file would still pass.
//
// "MAXIMAL" IS SCOPED TO THE TIER since #224, and the scoping is the property rather than a weakening
// of it: the bid it is maximal among is decided by the ladder first, and
// TestProperty_Auctions_AMaximalBidInALowerTierNeverWins is the other half — it proves the larger
// number below the winning rung was there to be wrongly awarded.
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
		for _, b := range c.inWinningTier(bids) {
			if b.AmountCp > top {
				top = b.AmountCp
			}
		}

		if res.Winners[0].AmountCp != top {
			return fmt.Errorf("the winner pays %d and the highest eligible bid in tier %s is %d; an "+
				"open auction charges the leader their own bid",
				res.Winners[0].AmountCp, c.winningTier(), top)
		}

		settled++

		return nil
	})

	require.Positive(t, settled, "no auction settled, so the property held vacuously")
}

// TestProperty_Auctions_AMaximalBidInALowerTierNeverWins is the ladder's own property, and the one
// this deliverable exists for: a bid larger than every bid on the winning rung, standing below it,
// loses (#224).
//
// TWO COUNTERS, and the second is what stops the property passing vacuously. `settled` counts
// resolutions; `outranked` counts the ones in which the largest eligible bid in the whole session was
// NOT on the winning rung — the shape a session has to have before "tier outranks amount" can be
// wrong. A generator that stopped producing that shape would leave this test green over a settlement
// that ranked by amount alone.
func TestProperty_Auctions_AMaximalBidInALowerTierNeverWins(t *testing.T) {
	t.Parallel()

	settled, outranked := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		for _, p := range []struct {
			name   string
			s      strategy.PointStrategy
			config string
			sealed bool
		}{
			{"auction_open", strategy.AuctionOpen{}, c.openConfigJSON(), false},
			{"auction_sealed", strategy.AuctionSealed{}, c.sealedConfigJSON("second_price"), true},
		} {
			// The sealed row separates the amounts so that every case still produces a winner to check
			// the ladder against: a sealed tie is reported rather than resolved, and a resolution with
			// no winner has no rung to compare (#248, see untiedBids). The ladder runs ABOVE the amount
			// either way — a rung is decided before a tie can exist — so this changes which cases reach
			// the assertion and never what it asserts.
			bids := c.bids(p.sealed)
			if p.sealed {
				bids = c.untiedBids(p.sealed)
			}

			res, err := p.s.SettleAuction(c.ctx(t, p.config), c.session(), bids)
			if err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}

			if len(res.Winners) == 0 {
				continue
			}

			winning := c.winningTier()
			if res.WinningTier != winning {
				return fmt.Errorf("%s: the item went to tier %q and the highest rung holding an "+
					"eligible bid is %q", p.name, res.WinningTier, winning)
			}

			var inTier, anywhere core.Centipoints

			for _, b := range c.inWinningTier(bids) {
				if b.AmountCp > inTier {
					inTier = b.AmountCp
				}
			}

			for i, b := range bids {
				if b.AmountCp >= c.effectiveMinimum() && b.AmountCp > anywhere && c.Balances[i] >= 0 {
					anywhere = b.AmountCp
				}
			}

			if anywhere > inTier {
				outranked++
			}

			// The winner has to be somebody who actually bid on the winning rung. Checked by account
			// rather than by amount, because a second-price winner does not pay their own bid.
			found := false

			for _, b := range c.inWinningTier(bids) {
				if b.AccountID == res.Winners[0].AccountID {
					found = true

					break
				}
			}

			if !found {
				return fmt.Errorf("%s: account %s won without holding an eligible bid in tier %s",
					p.name, res.Winners[0].AccountID, winning)
			}

			settled++
		}

		return nil
	})

	require.Positive(t, settled, "no auction settled, so the property held vacuously")
	require.Positive(t, outranked,
		"no generated session ever had its largest bid on a losing rung, so the ladder was never "+
			"actually tested")
}

// TestProperty_AuctionSealed_SecondPrice_NeverLeavesTheWinningTier recomputes the price from the
// winning rung's bids alone and requires the settlement to agree, exactly.
//
// The bound properties above (never more than your own bid, never below the minimum) are both
// satisfied by a settlement that priced against a lower-tier bid — 351 is under a 1000-point bid and
// over a 500 minimum — so neither of them would catch the failure this deliverable is about. An
// independent recomputation does, and it is written from docs/guides/auctions.md's rule rather than
// from the code: the runner-up is the highest bid from another account ON THE WINNING RUNG, plus one
// increment, clamped to the winner's own bid, and the minimum when there is nobody else on it.
func TestProperty_AuctionSealed_SecondPrice_NeverLeavesTheWinningTier(t *testing.T) {
	t.Parallel()

	settled, alone := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		// Amounts separated so that every case still settles and reaches this arithmetic: a tie is
		// reported rather than resolved, and there is no price on a resolution that awarded nobody
		// (#248, see untiedBids). What a tie itself produces is
		// TestProperty_AuctionSealed_ATieNamesExactlyTheEqualBiddersInTheWinningTier's subject.
		bids := c.untiedBids(true)

		res, err := strategy.AuctionSealed{}.SettleAuction(
			c.ctx(t, c.sealedConfigJSON("second_price")), c.session(), bids)
		if err != nil {
			return err
		}

		if len(res.Winners) == 0 {
			return nil
		}

		winner := res.Winners[0].AccountID
		inTier := c.inWinningTier(bids)

		var own, runnerUp core.Centipoints

		for _, b := range inTier {
			if b.AccountID == winner && b.AmountCp > own {
				own = b.AmountCp
			}

			if b.AccountID != winner && b.AmountCp > runnerUp {
				runnerUp = b.AmountCp
			}
		}

		want := c.effectiveMinimum()

		switch {
		case runnerUp == 0:
			alone++
		case runnerUp+c.IncrementCp > own:
			want = own
		default:
			want = runnerUp + c.IncrementCp
		}

		if res.Winners[0].AmountCp != want {
			return fmt.Errorf(
				"the winner pays %d; priced inside tier %s it is %d (own %d, runner-up %d, increment "+
					"%d, minimum %d) — a price computed from a bid below the winning rung is the 350.00 "+
					"a main must never be charged against",
				res.Winners[0].AmountCp, c.winningTier(), want, own, runnerUp, c.IncrementCp,
				c.effectiveMinimum())
		}

		settled++

		return nil
	})

	require.Positive(t, settled, "no auction settled, so the property held vacuously")
	require.Positive(t, alone,
		"no winner was ever alone on their rung, which is the case that pays the minimum with larger "+
			"bids sitting below it")
}

// TestProperty_AuctionSealed_ATieNamesExactlyTheEqualBiddersInTheWinningTier is #248's property, and
// it is stated in both directions because only one of them is about the tie set.
//
// A tie that is REPORTED must name exactly the accounts holding the highest eligible bid on the
// winning rung — nobody from a lower rung, and nobody on the rung who bid less. And a tie that
// EXISTS must be reported: the failure this deliverable is about is a settlement that picked one of
// two equal bidders and said nothing, which every assertion about the tie set passes vacuously.
//
// It also pins what a reported tie must NOT do. No winner, because nobody has won; no seed, because
// the roll is the fallback and nobody asked for it; and no winning tier, because a rung a rebid will
// be decided on has not taken anything yet.
func TestProperty_AuctionSealed_ATieNamesExactlyTheEqualBiddersInTheWinningTier(t *testing.T) {
	t.Parallel()

	ties, decided := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		bids := c.bids(true)

		res, err := strategy.AuctionSealed{}.SettleAuction(
			c.ctx(t, c.sealedConfigJSON("second_price")), c.session(), bids)
		if err != nil {
			return err
		}

		want := c.tiedInWinningTier(bids)

		if len(want) == 0 {
			if res.Tie != nil {
				return fmt.Errorf("tier %s has one highest bidder and the settlement named %d as tied",
					c.winningTier(), len(res.Tie.Accounts))
			}

			decided++

			return nil
		}

		if res.Tie == nil {
			return fmt.Errorf(
				"%d bidders hold the highest eligible bid in tier %s and the settlement awarded %d "+
					"winner(s) without naming a tie; a blind tie decided in silence is the whole of #248",
				len(want), c.winningTier(), len(res.Winners))
		}

		if !slices.Equal(want, res.Tie.Accounts) {
			return fmt.Errorf("the tie names %v and the equal bidders in tier %s are %v",
				res.Tie.Accounts, c.winningTier(), want)
		}

		if res.Tie.Tier != c.winningTier() {
			return fmt.Errorf("the tie stands on rung %q and the winning rung is %q",
				res.Tie.Tier, c.winningTier())
		}

		if !res.Tie.RebidRequired || res.Tie.MaxPasses() != len(want)-1 {
			return fmt.Errorf("a tie of %d bidders reports rebid_required=%v and %d permitted passes; "+
				"every tied bidder may pass and they may not all pass",
				len(want), res.Tie.RebidRequired, res.Tie.MaxPasses())
		}

		// The floor the rebid round opens at, which is the amount they tied on: a bidder asked to bid
		// again may raise or stand, and may never retreat below what they already committed.
		for _, b := range bids {
			if slices.Contains(want, b.AccountID) && b.AmountCp > res.Tie.AmountCp {
				return fmt.Errorf("the tie is recorded at %d and a tied bidder committed %d",
					res.Tie.AmountCp, b.AmountCp)
			}
		}

		if len(res.Winners) > 0 || res.RngSeed != nil || res.WinningTier != "" {
			return fmt.Errorf(
				"a reported tie awarded %d winner(s), consumed seed %v and named tier %q; it decides "+
					"nothing and rolls for nothing", len(res.Winners), res.RngSeed, res.WinningTier)
		}

		ties++

		return nil
	})

	require.Positive(t, ties, "no generated session ever tied, so the tie set is unexercised")
	require.Positive(t, decided,
		"every generated session tied, so 'a settlement with one top bidder reports no tie' held "+
			"vacuously")
}

// TestProperty_AuctionSealed_NeverConsumesRandomness is "a tie is never auto-resolved" stated as the
// one thing that cannot be true by accident (#248).
//
// The rule is a negative — no roll, ever, on any input — and a negative is exactly what an example
// test cannot establish: it passes on the cases somebody thought of. The Rng is the witness. A
// settlement that resolved a tie any automatic way would have had to draw from it, and this asserts
// across every generated session that it never so much as READ the seed, ties included. It is also
// the load-bearing half of the rebid round's replayability: the pool's randomness is exactly where
// the next round finds it, because settling a sealed auction never advances it.
func TestProperty_AuctionSealed_NeverConsumesRandomness(t *testing.T) {
	t.Parallel()

	tied, decided := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		ctx := c.ctx(t, c.sealedConfigJSON("second_price"))

		res, err := strategy.AuctionSealed{}.SettleAuction(ctx, c.session(), c.bids(true))
		if err != nil {
			return err
		}

		if ctx.rng.calls != 0 || len(ctx.rng.draws) != 0 || res.RngSeed != nil {
			return fmt.Errorf(
				"the settlement made %d call(s) to the Rng and drew %v; a sealed auction decides a tie "+
					"by hand or not at all, so there is nothing here for a roll to do",
				ctx.rng.calls, ctx.rng.draws)
		}

		if res.Tie != nil {
			tied++
		} else if len(res.Winners) == 1 {
			decided++
		}

		return nil
	})

	require.Positive(t, tied, "no session ever tied, so the case a roll would have decided is untested")
	require.Positive(t, decided, "no session ever settled, so the property held vacuously")
}

// TestProperty_AuctionSealed_ATie_IsAlwaysResolvableByHand is the argument that the rule terminates,
// which is what "never auto-resolve" has to answer for (#248).
//
// A hand-resolved tie ends because each round strictly shrinks the problem: somebody bids above the
// tie and wins, or somebody passes and the contenders reduce, and all-but-one may pass so the last
// one takes it. This asserts the two arithmetic facts that argument stands on, over every tie the
// generator produces: there is a bid that BEATS the tie value (the floor is strictly above it, and it
// is representable), and the pass budget always leaves exactly one bidder who cannot pass.
func TestProperty_AuctionSealed_ATie_IsAlwaysResolvableByHand(t *testing.T) {
	t.Parallel()

	ties := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		res, err := strategy.AuctionSealed{}.SettleAuction(
			c.ctx(t, c.sealedConfigJSON("second_price")), c.session(), c.bids(true))
		if err != nil {
			return err
		}

		if res.Tie == nil {
			return nil
		}

		// A rebid floor that is not strictly above the tie is a round that accepts the tied amount
		// again — Session.MinAmountCp is a `>=` test — so the same tie comes back for ever. Where no
		// raise is representable there is no floor at all, and the round is passes-only (AO review);
		// the pass budget below is what terminates it either way.
		if floor, canRebid := res.Tie.MinRebidCp(); canRebid && floor <= res.Tie.AmountCp {
			return fmt.Errorf("a rebid clears %d and the tie stands at %d; standing on the tie value "+
				"again is what everybody already did and would tie for ever", floor, res.Tie.AmountCp)
		}

		if res.Tie.MaxPasses() != len(res.Tie.Accounts)-1 || res.Tie.MaxPasses() < 1 {
			return fmt.Errorf("%d tied bidders may take %d passes; every one of them is offered the "+
				"pass and one of them has to be left holding the item",
				len(res.Tie.Accounts), res.Tie.MaxPasses())
		}

		ties++

		return nil
	})

	require.Positive(t, ties, "no generated session ever tied, so the round's terminating conditions "+
		"are unexercised")
}

// TestProperty_Auctions_TheWholeTraceIsWrittenOntoTheResolution.
//
// "The whole trace is written onto the resolution, so an officer can explain an outcome months later
// without re-deriving it" (docs/guides/auctions.md). Three claims: the chain is recorded in order and
// starts where a chain starts; the tier counts add up to the eligible bids and are ordered by the
// ladder; and no step and no reason ever names a losing bid's amount — checked on the SEALED auction,
// where publishing one is a leak rather than a redundancy.
func TestProperty_Auctions_TheWholeTraceIsWrittenOntoTheResolution(t *testing.T) {
	t.Parallel()

	traced, rotted, tied := 0, 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		bids := c.bids(true)

		res, err := strategy.AuctionSealed{}.SettleAuction(
			c.ctx(t, c.sealedConfigJSON("second_price")), c.session(), bids)
		if err != nil {
			return err
		}

		if len(res.Trace) == 0 {
			return errors.New("a resolution with no trace is one nobody can explain in three months")
		}

		if res.Trace[0].Kind != strategy.ResolutionStepEligibility {
			return fmt.Errorf("the trace starts at %q; a bid under the floor was never in the chain",
				res.Trace[0].Kind)
		}

		if len(res.Winners) == 0 {
			if res.WinningTier != "" || len(res.TierCounts) > 0 {
				return fmt.Errorf("a settlement that awarded nobody named tier %q with %d rung(s) "+
					"counted", res.WinningTier, len(res.TierCounts))
			}

			// The two ways a sealed auction awards nobody, and their traces differ: a rot never
			// reached the ladder and stops at `eligibility`, while a reported tie walked the ladder and
			// the amount and stops at `rebid_required` — the step that says the chain had more to run
			// and deliberately did not run it (#248).
			if res.Tie != nil {
				if last := res.Trace[len(res.Trace)-1]; last.Kind != strategy.ResolutionStepRebidRequired {
					return fmt.Errorf("a reported tie's trace ends at %q rather than at the step that "+
						"says why it stopped", last.Kind)
				}

				tied++

				return nil
			}

			if len(res.Trace) != 1 || res.Trace[0].Kind != strategy.ResolutionStepEligibility {
				return fmt.Errorf("a rot recorded %d step(s) ending at %q; nothing below the floor was "+
					"ever ranked", len(res.Trace), res.Trace[len(res.Trace)-1].Kind)
			}

			rotted++

			return nil
		}

		if res.Trace[1].Kind != strategy.ResolutionStepTier {
			return fmt.Errorf("step 2 of the chain is %q and the ladder runs before the amount",
				res.Trace[1].Kind)
		}

		if last := res.Trace[len(res.Trace)-1]; last.Kind != strategy.ResolutionStepPrice {
			return fmt.Errorf("the trace ends at %q rather than at what the winner pays", last.Kind)
		}

		eligible, seen := 0, 0
		below := len(strategy.Tiers()) + 1

		for _, b := range bids {
			if b.AmountCp >= c.effectiveMinimum() && b.AmountCp > 0 {
				eligible++
			}
		}

		for _, tc := range res.TierCounts {
			seen += tc.Bids

			rank := ladderRank(tc.Tier)
			if rank >= below {
				return fmt.Errorf("the tier counts are ordered %v; the disclosure reads the rungs "+
					"ABOVE the caller's, so they are ordered highest first", res.TierCounts)
			}

			below = rank

			if tc.Bids <= 0 {
				return fmt.Errorf("tier %s is counted with %d bids; a rung nobody bid on is absent",
					tc.Tier, tc.Bids)
			}
		}

		if seen != eligible {
			return fmt.Errorf("the tier counts total %d and %d bids were eligible", seen, eligible)
		}

		if res.TierCounts[0].Tier != res.WinningTier {
			return fmt.Errorf("the counts lead with tier %s and the item went to %s",
				res.TierCounts[0].Tier, res.WinningTier)
		}

		traced++

		return nil
	})

	require.Positive(t, traced, "no resolution carried a trace, so the property held vacuously")
	require.Positive(t, rotted, "no session ever rotted, so the trace on a rot is unexercised")
	require.Positive(t, tied, "no session ever reported a tie, so the trace on one is unexercised")
}

// TestProperty_TieredSettlement_AwardsAndReversesExactly extends the invariant suite over the whole
// path the ladder changed: settle, award at the resolved price, reverse.
//
// P2 and P5 above plan an award at a GENERATED price, which is the right way to exercise the
// allocator and is silent about the number a tiered auction actually resolves to. This walks the real
// sequence — the settlement decides the winner and the price, the award debits that winner exactly
// that price, and the reversal returns every balance to where it started — so a price that left the
// winning tier, or a winner the ladder did not choose, is caught as a conservation failure on a real
// batch rather than as an assertion about a struct.
func TestProperty_TieredSettlement_AwardsAndReversesExactly(t *testing.T) {
	t.Parallel()

	awarded := 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		s := strategy.AuctionSealed{}
		config := c.sealedConfigJSON("second_price")

		// Amounts separated so that the settle-award-reverse path is walked for every case: a reported
		// tie has no price to award and nothing to conserve, and skipping those cases would quietly
		// shrink what this property runs over (#248, see untiedBids).
		res, err := s.SettleAuction(c.ctx(t, config), c.session(), c.untiedBids(true))
		if err != nil {
			return err
		}

		if len(res.Winners) == 0 {
			return nil
		}

		price := res.Winners[0].AmountCp
		ev := c.awardEvent()
		ev.Buyer = strategy.AccountRef{ID: res.Winners[0].AccountID, Kind: "person"}
		ev.PriceCp = &price
		ev.Reason = res.Reason

		award, err := s.PlanAward(c.ctx(t, config), ev)
		if err != nil {
			return fmt.Errorf("award the %s winner at %d: %w", res.WinningTier, price, err)
		}

		if award.Entries[0].AccountID != res.Winners[0].AccountID ||
			award.Entries[0].AmountCp != -price {
			return fmt.Errorf("the batch debits %s %d and the settlement awarded %s at %d",
				award.Entries[0].AccountID, -award.Entries[0].AmountCp, res.Winners[0].AccountID, price)
		}

		delta := map[core.ULID]core.Centipoints{}
		for _, e := range award.Entries {
			delta[e.AccountID] += e.AmountCp
		}

		reversal, err := s.PlanReversal(c.ctx(t, ""), strategy.LedgerBatch{
			ID: acct(70), Kind: award.Kind, StrategyID: award.StrategyID,
			StrategyVersion: award.StrategyVersion, EffectiveAt: award.EffectiveAt,
			Entries: award.Entries,
		})
		if err != nil {
			return fmt.Errorf("reverse: %w", err)
		}

		for _, e := range reversal.Entries {
			delta[e.AccountID] += e.AmountCp
		}

		for id, v := range delta {
			if v != 0 {
				return fmt.Errorf("account %s is %d centipoints from where it started after a tiered "+
					"award and its reversal", id, v)
			}
		}

		awarded++

		return nil
	})

	require.Positive(t, awarded, "no tiered settlement was ever awarded, so the property held vacuously")
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
		// Amounts separated here too, and the near-tie they leave is the shape this property most
		// wants: two bids a few centipoints apart under an increment of at least 25 is exactly the
		// "runner-up plus one increment exceeds the winning bid" clamp (#248, see untiedBids).
		bids := c.untiedBids(true)
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

		// THE LADDER RUNS FIRST HERE TOO (#224). `relative_bid` does not partition on the rung the way
		// the two auctions do, but the ordering it settles by comes from the same rankBids — so the
		// largest share it may award is the largest share ON THE HIGHEST RUNG that holds a bidable
		// bid, and a hoarder's whole bank on an alt loses to a main's tenth of one.
		top := 0

		for i, b := range c.bids(false) {
			if b.AmountCp <= 0 || c.Balances[i] <= 0 || b.AmountCp > c.Balances[i] {
				continue
			}

			if rank := ladderRank(b.Tier); rank > top {
				top = rank
			}
		}

		best := int64(-1)

		var winnerShare int64

		for i, b := range c.bids(false) {
			if b.AmountCp <= 0 || c.Balances[i] <= 0 || b.AmountCp > c.Balances[i] {
				continue
			}

			if ladderRank(b.Tier) != top {
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
			return fmt.Errorf("the winner committed %d bp and the largest bidable share on the "+
				"winning rung was %d bp", winnerShare, best)
		}

		settled++

		return nil
	})

	require.Positive(t, settled, "no session settled, so the property held vacuously")
}

// relativeContender is one bid a `relative_bid` settlement actually ranks, with the share it commits.
//
// RESTATED FROM THE RULE RATHER THAN TAKEN FROM IT, like every expectation in this file: a contender
// list built by calling the strategy would agree with the settlement by construction, including when
// both are wrong. A bid contends when it commits something, its frozen balance is positive and the
// commitment is no larger than that balance — the three facts shareBasisPoints refuses on — and when
// it stands on the highest rung holding such a bid, because the ladder outranks the share (#224).
type relativeContender struct {
	account  core.ULID
	share    int64
	amountCp core.Centipoints
	placedAt core.Micros
}

// relativeContenders is the case's contenders, in the order the bids were generated.
//
// The session it describes is one with NO absolute minimum, which is what the two properties below
// settle under. A session minimum is an amount rather than a share, so it is the one knob under which
// two sessions holding the same shares are not the same session.
func (c spendPropCase) relativeContenders() []relativeContender {
	bidable := func(i int) bool {
		return c.Amounts[i] > 0 && c.Balances[i] > 0 && c.Amounts[i] <= c.Balances[i]
	}

	top := 0

	for i, b := range c.bids(false) {
		if !bidable(i) {
			continue
		}

		if rank := ladderRank(b.Tier); rank > top {
			top = rank
		}
	}

	out := make([]relativeContender, 0, len(c.Amounts))

	for i, b := range c.bids(false) {
		if !bidable(i) || ladderRank(b.Tier) != top {
			continue
		}

		out = append(out, relativeContender{
			account: b.AccountID,
			// The same integer arithmetic the strategy uses, restated for the reason above.
			share:    int64(b.AmountCp) * 10_000 / int64(c.Balances[i]),
			amountCp: b.AmountCp,
			placedAt: b.PlacedAt,
		})
	}

	return out
}

// topShare is the largest share any contender committed, and -1 when nobody contended.
func topShare(contenders []relativeContender) int64 {
	best := int64(-1)

	for _, ct := range contenders {
		if ct.share > best {
			best = ct.share
		}
	}

	return best
}

// largestCommitmentAtTopShare is the account that committed the most POINTS among the contenders tied
// at the largest share — which is to say the account the comparator handed the item to before #244.
//
// It exists to make the properties below non-vacuous rather than to assert anything: a run in which
// this account never changes as the banks are scaled is a run in which the defect was never reachable,
// and a property that passes only because its interesting case never occurred is not a property.
func largestCommitmentAtTopShare(contenders []relativeContender) core.ULID {
	best := topShare(contenders)

	var (
		account core.ULID
		most    core.Centipoints
	)

	for _, ct := range contenders {
		if ct.share == best && ct.amountCp > most {
			account, most = ct.account, ct.amountCp
		}
	}

	return account
}

// scaledBanks is the same session at a different scale: every bidder's frozen balance and the amount
// they committed multiplied by the same small factor, so that every share is EXACTLY what it was.
//
// floor(k*a * 10000 / k*b) is floor(a * 10000 / b) for any positive k — the ratio is unchanged, so
// the flooring is too — which is what makes the settlement of the scaled session the same question as
// the settlement of the original, asked of banks of a different size. The factors are positional
// rather than drawn, because the generator's draws are one seeded sequence and taking two more here
// would renumber every case behind it (see drawTiers).
func (c spendPropCase) scaledBanks() spendPropCase {
	out := c
	out.Balances = make([]core.Centipoints, len(c.Balances))
	out.Amounts = make([]core.Centipoints, len(c.Amounts))

	for i := range c.Balances {
		factor := core.Centipoints(i%4) + 1
		out.Balances[i] = c.Balances[i] * factor
		out.Amounts[i] = c.Amounts[i] * factor
	}

	return out
}

// TestProperty_RelativeBid_ATiedShareGoesToTheEarliestBid, and never to the largest commitment (#244).
//
// The rule's own model is that an equal share is an equal claim: a bidder who commits half of 500.00
// has done exactly what a bidder who commits half of 900.00 did, and the 400.00 between them is the
// bank that `relative_bid` exists to stop deciding items. So the step below the share is
// docs/guides/auctions.md's step 6 — the earliest bid — and the property is the exact one that
// implies: the winner's placement time is the smallest among the bids tied with it at the winning
// share. It holds where a seeded roll settles the item too, because the roll only ever runs between
// bids that already share that smallest time.
//
// The second counter is what makes the property about #244 rather than about the trace: it counts the
// sessions in which the winner was OUTBID in points by somebody tied with it at the same share, which
// is precisely the outcome the old comparator could not produce.
func TestProperty_RelativeBid_ATiedShareGoesToTheEarliestBid(t *testing.T) {
	t.Parallel()

	tied, outbid := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		res, err := strategy.RelativeBid{}.SettleAuction(c.ctx(t, c.relativeConfigJSON()),
			strategy.Session{ID: acct(60), SeqAtOpen: 3}, c.bids(false))
		if err != nil {
			return err
		}

		if len(res.Winners) == 0 {
			return nil
		}

		contenders := c.relativeContenders()
		best := topShare(contenders)

		var (
			winner   relativeContender
			earliest core.Micros
			at       int
		)

		for _, ct := range contenders {
			if ct.share != best {
				continue
			}

			at++

			if at == 1 || ct.placedAt < earliest {
				earliest = ct.placedAt
			}

			if ct.account == res.Winners[0].AccountID {
				winner = ct
			}
		}

		if winner.account == "" {
			return fmt.Errorf("account %s took the item and is not one of the %d bids at the winning "+
				"share of %d bp", res.Winners[0].AccountID, at, best)
		}

		if winner.placedAt != earliest {
			return fmt.Errorf("the winning bid at %d bp was placed at %d and a bid tied with it at %d; "+
				"below an equal share the chain is the bid sequence, and the points behind the share "+
				"are not a rung of it", best, winner.placedAt, earliest)
		}

		if at > 1 {
			tied++
		}

		for _, ct := range contenders {
			if ct.share == best && ct.amountCp > winner.amountCp {
				outbid++

				break
			}
		}

		return nil
	})

	require.Positive(t, tied, "no session ever tied on the share, so the step below it is unexercised")
	require.Positive(t, outbid,
		"no winner ever committed fewer points than a bid tied with it at the same share, so the "+
			"inversion #244 names never occurred and the property held vacuously")
}

// TestProperty_RelativeBid_ScalingEveryBankLeavesTheOutcomeAlone is the same claim from the other
// side, and it is the one that cannot be satisfied by a comparator that merely swapped its sign
// (#244).
//
// Multiply one bidder's balance and their commitment by the same factor and they have committed the
// same share of a bigger bank — the same bid under this model, in more points. If the settlement's
// answer moves, the size of the bank decided it. The whole outcome is compared, not just the winner:
// the winning rung, whether a roll was needed, and the account it went to, because a rule that quietly
// started rolling between bids it used to separate would be deciding items by a different means while
// naming the same account most of the time.
func TestProperty_RelativeBid_ScalingEveryBankLeavesTheOutcomeAlone(t *testing.T) {
	t.Parallel()

	settled, reordered := 0, 0

	forEachSpendCase(t, func(t *testing.T, c spendPropCase) error {
		session := strategy.Session{ID: acct(60), SeqAtOpen: 3}

		res, err := strategy.RelativeBid{}.SettleAuction(
			c.ctx(t, c.relativeConfigJSON()), session, c.bids(false))
		if err != nil {
			return err
		}

		grown := c.scaledBanks()

		grownRes, err := strategy.RelativeBid{}.SettleAuction(
			grown.ctx(t, grown.relativeConfigJSON()), session, grown.bids(false))
		if err != nil {
			return err
		}

		if len(res.Winners) != len(grownRes.Winners) {
			return fmt.Errorf("the session awarded %d winner(s) and the same shares over larger banks "+
				"awarded %d", len(res.Winners), len(grownRes.Winners))
		}

		if len(res.Winners) == 0 {
			return nil
		}

		if res.Winners[0].AccountID != grownRes.Winners[0].AccountID {
			return fmt.Errorf("the item went to %s and, at the same shares over banks of a different "+
				"size, to %s; a share is a ratio and the points behind it decide nothing",
				res.Winners[0].AccountID, grownRes.Winners[0].AccountID)
		}

		if res.WinningTier != grownRes.WinningTier {
			return fmt.Errorf("the winning rung was %q and became %q as the banks grew",
				res.WinningTier, grownRes.WinningTier)
		}

		if (res.RngSeed == nil) != (grownRes.RngSeed == nil) {
			return fmt.Errorf("one of the two settlements rolled and the other did not: %v against %v",
				res.RngSeed, grownRes.RngSeed)
		}

		settled++

		if largestCommitmentAtTopShare(c.relativeContenders()) !=
			largestCommitmentAtTopShare(grown.relativeContenders()) {
			reordered++
		}

		return nil
	})

	require.Positive(t, settled, "no session settled, so the property held vacuously")
	require.Positive(t, reordered,
		"the scaling never changed which of the tied shares committed the most points, so it never "+
			"asked the question #244 is about")
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
