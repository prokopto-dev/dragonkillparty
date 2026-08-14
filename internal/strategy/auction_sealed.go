package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// auction_sealed — hidden bids, first price or second price. Phase 1, #195.
//
// Bids are invisible to everyone, officers included, until the session enters `closing`. The highest
// bid wins; what it pays is the pool's `pay_rule`, and the recommended default is SECOND PRICE.
//
// WHY SECOND PRICE IS THE DEFAULT, in one paragraph, because it is the only DKP setting whose
// justification is a theorem. Under first price the rational move is to shade your bid down toward
// what you think the runner-up will bid, and since nobody knows that, the equilibrium is everybody
// bidding their whole bank — so the auction measures bank size instead of desire. Under second price
// the winner pays the runner-up plus one increment, so bidding your true valuation is optimal: you
// cannot lower what you pay by bidding less, you can only lose an item you wanted
// (docs/guides/choosing-a-dkp-system.md). Both rules ship because a guild that has always run first
// price should be able to migrate without changing its rules on the same night.
//
// ONE STRATEGY WITH A KNOB, NOT TWO STRATEGIES. docs/guides/auctions.md lists `auction_sealed_first`
// and `auction_sealed_second` as session MODES, and the catalogue lists them as two rows of one rule.
// They are the same auction — same sealing, same eligibility, same tie-break, same batch — differing
// in one line of price arithmetic, and two strategies would mean two copies of everything else that
// could then disagree about what a sealed bid is.
//
// WHAT LANDS HERE IS THE ARITHMETIC, NOT THE AUCTION. The state machine, the reveal at `closing`, the
// anti-snipe window and holds are Phase 6, and so is the enforcement that keeps a sealed amount out of
// every read path — that is a property of the API and the logger, not of a pure planner, and this
// file's one contribution to it is refusing a bid that is not marked sealed.
//
// SECOND PRICE IS COMPUTED WITHIN THE WINNING TIER (#224), which is the half of the pay rule the
// ladder changes. The runner-up is the highest bid from another account STANDING ON THE SAME RUNG,
// and a bidder alone on their rung pays the minimum however large the bids below them are: the 350.00
// an alt put in is not a price a main is ever charged against (docs/guides/auctions.md). settlePrice
// below is handed the winning rung's bids and nothing else, which is what makes that structural
// rather than remembered.
//
// A BLIND TIE IS AN OUTCOME, NOT A COIN FLIP (#248). Two sealed bids equal in amount on the winning
// rung are the one case this rule cannot decide from what it collected, and the steps below the
// amount cannot decide it either: in an auction where nobody saw anybody else's number, submitting
// first is not evidence of wanting the item more. So the settlement names the tied accounts and asks
// for a rebid among exactly them — the tied amount becomes that round's floor, and all but one of
// them may pass, so it ends with a single winner (#247 carries the round; this file carries the
// arithmetic it starts from). The seeded roll remains as the final fallback and is reached only when
// a session asks for it, which is Session.BreakTies.
//
// IT IS A SPEND RULE AND IT DOES NOT EARN. PlanAttendance and PlanDecay return ErrUnsupported naming
// this strategy; a pool holds an earn rule and an over-time rule beside it (ADR-0026).

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = AuctionSealed{}

// AuctionSealed is the sealed-bid auction spend strategy, first or second price. STATELESS:
// everything it needs arrives through the Ctx façade.
type AuctionSealed struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	auctionSealedID      = "auction_sealed"
	auctionSealedVersion = "0.1.0"
)

// The `pay_rule` knob's values: what the winner of a sealed auction pays.
const (
	// PayRuleFirstPrice — the winner pays their own bid.
	PayRuleFirstPrice = "first_price"

	// PayRuleSecondPrice — the winner pays the runner-up's bid plus one increment, never more than
	// their own bid. A lone bidder pays the session's minimum.
	PayRuleSecondPrice = "second_price"
)

// auctionSealedConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in
// one place, in the form that renders the pool-settings form and validates the config at the API edge.
//
// Draft 2020-12, `additionalProperties: false`, every money field an INTEGER named `_cp` — the same
// three rules every schema in this package follows, for the reasons fixed_price's carries.
const auctionSealedConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Sealed auction",
  "description": "Hidden bids. The highest wins and pays either its own bid or the runner-up plus one increment.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "pay_rule": {
      "type": "string",
      "enum": ["second_price", "first_price"],
      "default": "second_price",
      "title": "What the winner pays",
      "description": "second_price charges the runner-up's bid plus one increment, so bidding your true valuation is optimal. first_price charges the winner's own bid."
    },
    "min_bid_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Minimum bid (centipoints)",
      "description": "The smallest bid the pool accepts, and what a sole bidder pays under second price. A session may open with a higher minimum of its own."
    },
    "increment_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Increment (centipoints)",
      "description": "How far above the runner-up a second-price winner pays. 100 centipoints is 1.00 point."
    },
    "proceeds": {
      "type": "string",
      "enum": ["guild_bank", "attendees"],
      "default": "guild_bank",
      "title": "Where the winning bid goes",
      "description": "guild_bank drains the points out of circulation; attendees splits them across the night's raiders by largest remainder."
    },
    "solo_policy": {
      "type": "string",
      "enum": ["guild_bank", "write_off"],
      "default": "guild_bank",
      "title": "Where proceeds go with nobody to split them across",
      "description": "A solo kill has no attendees. The bid still leaves the winner; this says which system account receives it."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An award is rejected if it would take the winner below this. Negative permits going into debt to a limit."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes. A COPY, not the backing array of the
// constant: a caller that could mutate the schema could change what every pool validates against.
func (AuctionSealed) ConfigSchema() []byte { return []byte(auctionSealedConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (AuctionSealed) ID() string { return auctionSealedID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (AuctionSealed) Version() string { return auctionSealedVersion }

// RuleKind is spend: this strategy answers "how are points spent?" and nothing else (ADR-0026).
func (AuctionSealed) RuleKind() RuleKind { return RuleSpend }

// BalanceKinds is the one balance kind this strategy moves.
func (AuctionSealed) BalanceKinds() []string { return []string{BalanceKindDKP} }

// auctionSealedConfig is the parsed pool config. The JSON tags are the schema's property names and
// the two must agree; TestSpendStrategies_ConfigSchema_DeclareExactlyTheParsedKnobs asserts that they
// do.
type auctionSealedConfig struct {
	PayRule     string           `json:"pay_rule"`
	MinBidCp    core.Centipoints `json:"min_bid_cp"`
	IncrementCp core.Centipoints `json:"increment_cp"`
	Proceeds    string           `json:"proceeds"`
	SoloPolicy  string           `json:"solo_policy"`
	FloorCp     core.Centipoints `json:"floor_cp"`
}

// defaultAuctionSealedConfig is the config a pool that has set nothing runs under: second price, a
// one-point minimum and a one-point increment, with the proceeds leaving circulation.
func defaultAuctionSealedConfig() auctionSealedConfig {
	return auctionSealedConfig{
		PayRule:     PayRuleSecondPrice,
		MinBidCp:    100,
		IncrementCp: 100,
		Proceeds:    ProceedsGuildBank,
		SoloPolicy:  SoloPolicyGuildBank,
		FloorCp:     0,
	}
}

// terms is the part of this config every spend rule shares. See spend.go.
func (cfg auctionSealedConfig) terms() spendTerms {
	return spendTerms{Proceeds: cfg.Proceeds, SoloPolicy: cfg.SoloPolicy, FloorCp: cfg.FloorCp}
}

// config parses and validates the pool's config, re-validating what the API edge already validated
// — because what reaches a planner also comes from the importer, from a backfill and from a test.
func (AuctionSealed) config(ctx Ctx) (auctionSealedConfig, error) {
	cfg := defaultAuctionSealedConfig()

	if err := decodeConfig(auctionSealedID, ctx.ConfigJSON(), &cfg); err != nil {
		return auctionSealedConfig{}, err
	}

	return validateAuctionSealedConfig(cfg)
}

// validateAuctionSealedConfig applies the bounds the schema declares, to a config that has already
// parsed. Split from config so that the defaults are validated too.
func validateAuctionSealedConfig(cfg auctionSealedConfig) (auctionSealedConfig, error) {
	if err := validateSpendTerms(auctionSealedID, cfg.terms()); err != nil {
		return auctionSealedConfig{}, err
	}

	switch cfg.PayRule {
	case PayRuleFirstPrice, PayRuleSecondPrice:
	default:
		return auctionSealedConfig{}, fmt.Errorf("%s: pay_rule is %q, want %q or %q: %w",
			auctionSealedID, cfg.PayRule, PayRuleSecondPrice, PayRuleFirstPrice, ErrInvalidConfig)
	}

	if cfg.MinBidCp <= 0 {
		return auctionSealedConfig{}, fmt.Errorf(
			"%s: min_bid_cp is %d; it is both the smallest bid the pool accepts and what a sole "+
				"second-price bidder pays, and neither may be nothing: %w",
			auctionSealedID, cfg.MinBidCp, ErrInvalidConfig)
	}

	// A ZERO INCREMENT MAKES SECOND PRICE INTO A TIE. The winner would pay exactly the runner-up's
	// bid, which is the number the runner-up was willing to pay and lost at — so the two bids become
	// indistinguishable in the only place the difference matters. One centipoint is the smallest
	// increment that keeps "just above the runner-up" true.
	if cfg.IncrementCp <= 0 {
		return auctionSealedConfig{}, fmt.Errorf(
			"%s: increment_cp is %d, so a second-price winner would pay exactly the runner-up's bid "+
				"rather than just above it; 1 is the smallest real increment: %w",
			auctionSealedID, cfg.IncrementCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAward debits the winner the settled price and routes the proceeds.
//
// THE PRICE IS THE SETTLED ONE AND IT IS REQUIRED — and under second price it is emphatically NOT the
// winner's bid, which is the whole point of the rule and the one number a caller must not re-derive
// on its own. SettleAuction computed it; the award carries it.
func (s AuctionSealed) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if ev.PriceCp == nil {
		return BatchProposal{}, fmt.Errorf(
			"%s: the award for item %q carries no price; a sealed auction settles at a price the "+
				"session resolved — under second price it is not even the winner's own bid — so the "+
				"caller must name it: %w", auctionSealedID, ev.Item.Name, ErrInvalidEvent)
	}

	return spendAward(ctx, auctionSealedID, auctionSealedVersion, cfg.terms(), ev, *ev.PriceCp)
}

// PlanAttendance is unsupported: an auction is how points are spent, not how they are earned.
func (AuctionSealed) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return spendPlanAttendance(auctionSealedID)
}

// PlanDecay is unsupported: a pool that wants points to expire runs an over-time rule beside this one.
func (AuctionSealed) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return spendPlanDecay(auctionSealedID)
}

// PlanAdjustment moves points between an account and a counterparty — two entries, never one.
func (s AuctionSealed) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, auctionSealedID, auctionSealedVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed. Entry-wise negation is correct here
// because this strategy's only balance kind is `dkp`, a plain quantity; see reversePlan.
func (AuctionSealed) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, auctionSealedID, b)
}

// Spendable is the account's balance at the pool head. Holds are Phase 6 — see AuctionOpen.Spendable
// for why the honest number is better than a placeholder that reads as though they were handled.
func (AuctionSealed) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, auctionSealedID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
func (AuctionSealed) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, auctionSealedID, acct)
}

// PriceHint is the minimum bid, and under second price that is a real answer rather than a floor.
//
// A SEALED AUCTION MUST NOT HINT AT THE BIDS. Under second price the winner frequently pays close to
// the minimum — the guide's own example has a sole bidder paying exactly it — so the minimum is the
// most useful number that discloses nothing. The item's catalogue price is deliberately NOT preferred
// here as it is in the open auction: a published expected value is a signal about what others will
// bid, and a sealed auction's whole design is that no such signal exists while the session is open.
func (s AuctionSealed) PriceHint(ctx Ctx, _ ItemRef) (*core.Centipoints, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}

	hint := cfg.MinBidCp

	return &hint, nil
}

// ValidateBid rejects a bid before a session accepts it.
//
// AN UNSEALED BID IS REFUSED, and this is the one check in the file that is about confidentiality
// rather than arithmetic. Bid.Sealed is what tells every layer downstream that this amount must not be
// logged, rendered or returned before the reveal (.claude/rules/go-idioms.md: "no bid amounts before
// reveal"). A bid arriving at a sealed auction without it is a caller that will leak it, and the
// difference between refusing and accepting is whether the leak happens.
//
// THERE IS NO INCREMENT LATTICE HERE, unlike the open auction. Under sealed bidding you name what the
// item is worth to you — 285, not 300 — and the increment is a settlement rule rather than a bid rule.
// Forcing bids onto a lattice would quantise exactly the valuations second price exists to elicit.
func (s AuctionSealed) ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}

	if err := checkBidIdentity(auctionSealedID, acct, bid); err != nil {
		return err
	}

	if !bid.Sealed {
		return fmt.Errorf(
			"%s: the bid from account %s is not marked sealed; this auction hides every amount until "+
				"the session closes, and an unmarked bid is one the layers below are free to log and "+
				"render: %w", auctionSealedID, bid.AccountID, ErrInvalidEvent)
	}

	if bid.AmountCp < cfg.MinBidCp {
		return fmt.Errorf("%s: the bid from account %s is below the minimum of %d centipoints: %w",
			auctionSealedID, bid.AccountID, cfg.MinBidCp, ErrInvalidEvent)
	}

	return checkBidAffordable(ctx, auctionSealedID, acct, bid)
}

// SettleAuction awards the item to the highest bid, at the price the pay rule names — or names the
// bidders it could not separate and awards nobody.
//
// NO ELIGIBLE BID IS THE ROT CASE, not an error — see AuctionOpen.SettleAuction.
//
// AN EQUAL-AMOUNT TIE ON THE WINNING RUNG IS REPORTED, NOT BROKEN (#248). Two bidders who named the
// same number in a BLIND auction are equal in the only fact the auction collected, and every step
// left in the chain is noise about them: nobody could see anybody else's bid, so who submitted first
// is not evidence about who wanted the item more, and a coin flip is not evidence about anything. So
// the resolution stops, names exactly the tied accounts, and asks for a rebid round among them
// (#247) — which is the answer a guild reaches for anyway when it finds out, except that now it is
// the platform's answer rather than an officer improvising one at 01:00 while eleven people watch.
//
// THE SEEDED ROLL IS STILL THERE AND IT IS STILL LAST. Session.BreakTies runs the rest of the chain —
// bid sequence, then the roll — and it exists because a chain has to terminate: a rebid that ties
// again, and again, ends with somebody asking for the deterministic answer. What changed is that
// reaching it requires asking, so it is the fallback rather than the default.
//
// THE MESSAGE NAMES NO LOSING AMOUNT. A resolution is read back by an officer and pasted into chat,
// and a second-price reason that said "285, the runner-up's 280 plus 5" would publish a losing sealed
// bid to settle an argument about the winning one. What it says instead is the rule that produced the
// number, which is what an officer actually has to defend.
func (s AuctionSealed) SettleAuction(ctx Ctx, session Session, bids []Bid) (Resolution, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return Resolution{}, err
	}

	minimum := sessionMinimum(session, cfg.MinBidCp)

	eligible := eligibleBids(bids, minimum)
	if len(eligible) == 0 {
		return rotResolution(len(bids), minimum), nil
	}

	phase, err := resolveTier(auctionSealedID, eligible)
	if err != nil {
		return Resolution{}, err
	}

	ranked := make([]rankedBid, 0, len(phase.bids))
	for _, b := range phase.bids {
		ranked = append(ranked, rankedBid{bid: b, rank: int64(b.AmountCp)})
	}

	ordered, err := rankBids(auctionSealedID, ranked)
	if err != nil {
		return Resolution{}, err
	}

	// THE TIE IS DETECTED BEFORE ANYTHING IS DECIDED, and before any randomness is consumed: a
	// settlement that reported a tie having already drawn from the Rng would leave that sequence
	// advanced by a roll nobody used, and the rebid round it asks for would then settle from
	// different numbers than the ones this session would have produced (sortedEntrants makes the same
	// argument for `roll`).
	tied := tiedAccounts(ordered)

	if len(tied) > 1 && !session.BreakTies {
		return rebidResolution(len(bids), len(eligible), minimum, phase, ordered, tied), nil
	}

	winner, seed := settleHighest(ctx, ordered)

	price, reason := cfg.settlePrice(winner, ordered, phase, minimum)

	switch {
	case len(tied) > 1 && seed != nil:
		reason += fmt.Sprintf(", after this session asked for the %d-way tie in tier %s to be broken "+
			"rather than rebid and a seeded roll settled it", len(tied), phase.tier)
	case len(tied) > 1:
		reason += fmt.Sprintf(", after this session asked for the %d-way tie in tier %s to be broken "+
			"rather than rebid and the earliest of the tied bids took it", len(tied), phase.tier)
	case seed != nil:
		reason += ", after a seeded roll between the bids tied at the top"
	}

	trace := append(auctionTrace(len(bids), len(eligible), minimum, phase, ordered, seed),
		ResolutionStep{Kind: ResolutionStepPrice, Detail: reason})

	return Resolution{
		Winners:     []Allocation{{AccountID: winner.bid.AccountID, AmountCp: price}},
		Reason:      phase.explain(reason),
		RngSeed:     seed,
		WinningTier: phase.tier,
		TierCounts:  phase.counts,
		Trace:       trace,
	}, nil
}

// rebidResolution is the settlement of a sealed auction whose top bids tied: no winner, no price,
// and the parties a rebid round has to be opened to (#248).
//
// IT AWARDS NOBODY AND IS NOT A FAILURE, which is the same shape rotResolution has and for a
// stronger reason: a rot is an item nobody legally wanted, and this is an item two people wanted
// equally. Both are outcomes an officer has to be able to read and act on, so both carry a reason and
// the trace that produced them.
//
// IT NAMES NO RUNG AS THE WINNER and no tier counts, exactly as a rot does not: WinningTier is what
// TOOK the item, and nothing has. The rung is on the Tie, where it belongs — it is the rung the rebid
// will be decided on, and the round is opened from the tie rather than from the counts.
//
// THE ONLY AMOUNT IT NAMES IS THE TIED ONE, which is the winning amount: revealed at `closing`,
// paid by whoever ends up taking the item, and the floor the rebid opens at. Every losing bid below
// it stays sealed, as it does on every other path out of this file.
func rebidResolution(
	placed, eligible int, minimum core.Centipoints, phase tierOutcome, ordered []rankedBid,
	tied []core.ULID,
) Resolution {
	tie := Tie{
		Tier:          phase.tier,
		AmountCp:      ordered[0].bid.AmountCp,
		Accounts:      tied,
		RebidRequired: true,
	}

	reason := fmt.Sprintf(
		"%d bidders in tier %s are tied at %d centipoints; a rebid round among exactly those bidders "+
			"settles it, opening at that amount as its floor, and every one of them but the last may "+
			"pass", len(tied), phase.tier, tie.AmountCp)

	trace := append(auctionTraceThroughAmount(placed, eligible, minimum, phase, ordered),
		ResolutionStep{
			Kind: ResolutionStepRebidRequired,
			Detail: fmt.Sprintf(
				"the bids at the top of tier %s are held by %d different bidders, and in a sealed "+
					"auction nothing below the amount separates them; the item is settled by a rebid "+
					"among exactly those %d, opening at %d centipoints, so the bid sequence and the "+
					"seeded roll were not run",
				phase.tier, len(tied), len(tied), tie.AmountCp),
		})

	return Resolution{
		Reason: phase.explain(reason),
		Tie:    &tie,
		Trace:  trace,
	}
}

// settlePrice is what the winner pays, and the sentence that explains it.
//
// FIRST PRICE is the winner's own bid and needs no argument.
//
// SECOND PRICE is the runner-up plus one increment, with two clamps that are the whole of the
// arithmetic risk in this file:
//
//   - NEVER MORE THAN THE WINNER'S OWN BID. With bids of 350 and 349 and an increment of 5, the
//     runner-up plus one increment is 354 — more than the winner offered. Charging it would break the
//     one promise second price makes (you can never pay more than you bid), and it is the failure that
//     looks correct in every test whose increment happens to be smaller than the gap between the top
//     two bids.
//   - A SOLE BIDDER PAYS THE MINIMUM (docs/guides/choosing-a-dkp-system.md). There is no runner-up to
//     price against, and charging their own bid would silently make an uncontested item a first-price
//     auction — which is exactly the incentive second price exists to remove.
//
// THE RUNNER-UP IS THE HIGHEST BID FROM A DIFFERENT ACCOUNT, not simply the second row. Bids are
// append-only and a bidder may hold several — a raise, a retraction and its replacement — so the row
// below the winner is frequently the winner's own earlier bid, and pricing against it would charge a
// bidder their own number plus an increment. That is a real overcharge with a plausible-looking
// arithmetic trail, and it is why this loop skips on the account rather than on the index.
//
// AND IT IS THE HIGHEST SUCH BID ON THE WINNING RUNG (#224), which is structural rather than
// remembered: `ordered` is the winning tier's bids and nothing else, so a lower-tier number cannot be
// reached by a loop that ran one row too far. A main alone in their tier therefore falls through to
// the minimum with a 350.00 alt bid sitting below them — which is the rule, and is the one number a
// tiered second-price auction must never charge.
func (cfg auctionSealedConfig) settlePrice(
	winner rankedBid, ordered []rankedBid, phase tierOutcome, minimum core.Centipoints,
) (core.Centipoints, string) {
	if cfg.PayRule == PayRuleFirstPrice {
		return winner.bid.AmountCp, fmt.Sprintf(
			"first price: the highest of %d sealed bids pays its own %d centipoints",
			len(ordered), winner.bid.AmountCp)
	}

	for _, r := range ordered {
		if r.bid.AccountID == winner.bid.AccountID {
			continue
		}

		// On overflow the sum would exceed every representable bid, so the winner's own bid is the
		// smaller of the two by construction and the clamp below is the answer anyway. Reporting it
		// rather than wrapping is what keeps that true rather than accidental.
		raised, ok := addCentipoints(r.bid.AmountCp, cfg.IncrementCp)
		if !ok || raised > winner.bid.AmountCp {
			return winner.bid.AmountCp, fmt.Sprintf(
				"second price: the runner-up plus one increment of %d exceeds the winning bid, so the "+
					"winner pays their own %d centipoints", cfg.IncrementCp, winner.bid.AmountCp)
		}

		return raised, fmt.Sprintf(
			"second price: the runner-up's bid plus one increment of %d centipoints", cfg.IncrementCp)
	}

	if phase.below > 0 {
		return minimum, fmt.Sprintf(
			"second price: the only bidder in tier %s pays the minimum of %d centipoints; the %d bid(s) "+
				"on lower rungs are never priced against", phase.tier, minimum, phase.below)
	}

	return minimum, fmt.Sprintf(
		"second price: the only bidder in the session pays the minimum of %d centipoints", minimum)
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal. The floor
// here is the shipped default while each proposal carries the POOL's; requireInvariantsAgree compares
// the two by kind and balance kind.
func (AuctionSealed) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
