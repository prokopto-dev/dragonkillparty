package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// auction_open — the ascending English auction every P99 guild has run in guild chat. Phase 1, #195.
//
// Bids are visible while the session is open, each one has to beat the leader by the increment, and
// the highest bid wins and pays itself. It is the simplest auction there is and the one a migrating
// guild already knows how to explain, which is why it ships beside the sealed pair rather than after
// them.
//
// WHAT LANDS HERE IS THE ARITHMETIC, NOT THE AUCTION. The bid state machine, the anti-snipe window
// and holds are Phase 6 (docs/guides/auctions.md), and every one of them needs a fact the Ctx façade
// does not carry. What a pure planner can answer today is exactly three questions — is this bid
// acceptable, who won, and what does the award batch look like — and those are the three this file
// answers.
//
// THE TIE-BREAK IS PARTIAL AND SAYS SO. docs/guides/auctions.md's chain is tier, amount, raid
// attendance, balance before the bid, items won in the window, bid sequence, then a seeded roll. What
// runs here is amount, then bid sequence, then the roll, and rankBids in spend.go carries the
// argument. TIER IS THE STEP ABOVE ALL OF THEM and it is its own Phase 1 deliverable (ROADMAP item
// 12, #224) rather than an omission: strategy.Bid carries no tier, and approximating "tier outranks
// amount" is how a 350-point alt bid beats a 10-point main, which is the single most consequential
// rule in the system and the one it inverts.
//
// IT IS A SPEND RULE AND IT DOES NOT EARN. PlanAttendance and PlanDecay return ErrUnsupported naming
// this strategy: a pool earns through its earn rule and expires points through its over-time rule, and
// it holds all three (ADR-0026). Inventing a tick award here would be a second copy of `tick`'s
// arithmetic that could then disagree with it.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = AuctionOpen{}

// AuctionOpen is the open ascending-auction spend strategy. STATELESS: everything it needs arrives
// through the Ctx façade, which is what lets one value serve every pool and every request
// concurrently.
type AuctionOpen struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	auctionOpenID      = "auction_open"
	auctionOpenVersion = "0.1.0"
)

// auctionOpenConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in one
// place, in the form that renders the pool-settings form and validates the config at the API edge.
//
// Draft 2020-12. `additionalProperties: false` is deliberate and load-bearing — a typo'd knob must be
// a validation error at the edge and not a silently ignored key that leaves the pool running the
// default. Every money field is an INTEGER named `_cp`: canonical §1 bans a decimal on the wire, and
// a schema that said `number` would invite one.
//
// THE SESSION'S OWN TIMINGS ARE NOT HERE. Duration, the anti-snipe window, the extension count and the
// hold policy are properties of a bid session rather than of the pool's spend rule
// (docs/guides/auctions.md's create-session body carries all four), and a knob here that nothing read
// would be worse than no knob: the officer sets it, the form shows it, and the auction ignores it.
const auctionOpenConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Open auction",
  "description": "Ascending English auction. Bids are visible, each must beat the leader by the increment, and the highest bid wins and pays it.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "min_bid_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Minimum bid (centipoints)",
      "description": "The smallest bid the pool accepts. A session may open with a higher minimum of its own; it may not open with a lower one."
    },
    "increment_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Bid increment (centipoints)",
      "description": "Every accepted bid is the minimum plus a whole number of increments. 100 centipoints is 1.00 point."
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

// ConfigSchema returns the JSON Schema document as bytes.
//
// A COPY, not the backing array of the constant: a caller that could mutate the schema could change
// what every pool validates against.
func (AuctionOpen) ConfigSchema() []byte { return []byte(auctionOpenConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (AuctionOpen) ID() string { return auctionOpenID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (AuctionOpen) Version() string { return auctionOpenVersion }

// RuleKind is spend: this strategy answers "how are points spent?" and nothing else (ADR-0026).
func (AuctionOpen) RuleKind() RuleKind { return RuleSpend }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal.
func (AuctionOpen) BalanceKinds() []string { return []string{BalanceKindDKP} }

// auctionOpenConfig is the parsed pool config. The JSON tags are the schema's property names and the
// two must agree; TestSpendStrategies_ConfigSchema_DeclareExactlyTheParsedKnobs asserts that they do,
// because a knob in the schema that the parser ignores is a knob the settings form offers and nothing
// reads.
type auctionOpenConfig struct {
	MinBidCp    core.Centipoints `json:"min_bid_cp"`
	IncrementCp core.Centipoints `json:"increment_cp"`
	Proceeds    string           `json:"proceeds"`
	SoloPolicy  string           `json:"solo_policy"`
	FloorCp     core.Centipoints `json:"floor_cp"`
}

// defaultAuctionOpenConfig is the config a pool that has set nothing runs under: a one-point minimum
// and a one-point increment, with the proceeds leaving circulation. It is the struct the pool's JSON
// is decoded OVER, which is what makes an absent key mean "the default" and a present `"floor_cp": 0`
// mean "zero, chosen".
func defaultAuctionOpenConfig() auctionOpenConfig {
	return auctionOpenConfig{
		MinBidCp:    100,
		IncrementCp: 100,
		Proceeds:    ProceedsGuildBank,
		SoloPolicy:  SoloPolicyGuildBank,
		FloorCp:     0,
	}
}

// terms is the part of this config every spend rule shares. See spend.go.
func (cfg auctionOpenConfig) terms() spendTerms {
	return spendTerms{Proceeds: cfg.Proceeds, SoloPolicy: cfg.SoloPolicy, FloorCp: cfg.FloorCp}
}

// config parses and validates the pool's config.
//
// It re-validates what the API edge already validated against ConfigSchema, and the duplication earns
// its keep: the edge validates what a human typed into the settings form, and this validates what
// actually reached the planner — which includes a config written by the importer, by a migration
// backfill, or by a test.
func (AuctionOpen) config(ctx Ctx) (auctionOpenConfig, error) {
	cfg := defaultAuctionOpenConfig()

	if err := decodeConfig(auctionOpenID, ctx.ConfigJSON(), &cfg); err != nil {
		return auctionOpenConfig{}, err
	}

	return validateAuctionOpenConfig(cfg)
}

// validateAuctionOpenConfig applies the bounds the schema declares, to a config that has already
// parsed. Split from config so that the defaults are validated too — a default that violated its own
// schema would otherwise be the one config nothing ever checked.
func validateAuctionOpenConfig(cfg auctionOpenConfig) (auctionOpenConfig, error) {
	if err := validateSpendTerms(auctionOpenID, cfg.terms()); err != nil {
		return auctionOpenConfig{}, err
	}

	if cfg.MinBidCp <= 0 {
		return auctionOpenConfig{}, fmt.Errorf(
			"%s: min_bid_cp is %d; an auction whose minimum is nothing accepts a bid of nothing: %w",
			auctionOpenID, cfg.MinBidCp, ErrInvalidConfig)
	}

	// A ZERO INCREMENT IS NOT "NO INCREMENT". Every accepted bid is min_bid + k × increment, so an
	// increment of zero makes that lattice the single point min_bid — every bid above the minimum
	// would be refused, and the auction could never be raised. A guild that wants free-form bidding
	// wants an increment of 1, which is one centipoint and is what "no increment" means here.
	if cfg.IncrementCp <= 0 {
		return auctionOpenConfig{}, fmt.Errorf(
			"%s: increment_cp is %d, so no bid could ever raise the leader; 1 is the smallest real "+
				"increment: %w", auctionOpenID, cfg.IncrementCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAward debits the winner their settled bid and routes the proceeds.
//
// THE PRICE IS THE SETTLED BID AND IT IS REQUIRED. An auction has no catalogue price and no pool
// default to fall back to: the number a winner pays is the one the session resolved, which arrives as
// AwardEvent.PriceCp. Falling back to the item's catalogue price would silently charge an auction
// winner a fixed-price guild's number, and it would do it on exactly the award nobody re-reads.
//
// THE MINIMUM IS NOT RE-CHECKED HERE, deliberately. It is a bid-time rule (ValidateBid) and a
// settlement rule (SettleAuction); by the time an award is planned, an officer may legitimately have
// overridden the resolution — docs/guides/auctions.md's `resolved` state exists for exactly that —
// and a planner that refused their number would refuse the override rather than the mistake.
func (s AuctionOpen) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if ev.PriceCp == nil {
		return BatchProposal{}, fmt.Errorf(
			"%s: the award for item %q carries no price; an auction award is settled at the winning "+
				"bid, so the caller must name it: %w", auctionOpenID, ev.Item.Name, ErrInvalidEvent)
	}

	return spendAward(ctx, auctionOpenID, auctionOpenVersion, cfg.terms(), ev, *ev.PriceCp)
}

// PlanAttendance is unsupported: an auction is how points are spent, not how they are earned.
func (AuctionOpen) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return spendPlanAttendance(auctionOpenID)
}

// PlanDecay is unsupported: a pool that wants points to expire runs an over-time rule beside this one.
func (AuctionOpen) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return spendPlanDecay(auctionOpenID)
}

// PlanAdjustment moves points between an account and a counterparty. See adjustmentProposal: it is two
// entries and never one, because an officer who could add points without naming where they came from
// could inflate a guild's economy invisibly.
func (s AuctionOpen) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, auctionOpenID, auctionOpenVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed. Entry-wise negation is correct here
// because this strategy's only balance kind is `dkp`, a plain quantity; see reversePlan for why the
// reversal declares no floor and reads no current config.
func (AuctionOpen) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, auctionOpenID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM, never a computed weighting.
//
// ACTIVE BID HOLDS ARE NOT SUBTRACTED YET, and that is the one thing about this method worth knowing.
// docs/guides/auctions.md defines spendable as `balance − Σ active holds`, and holds are Phase 6:
// there is no hold table, so there is nothing to subtract and a placeholder that read as though there
// were would be worse than the honest number. Until then two open sessions can each accept a bid for
// the whole balance, which is precisely what P4 and the `strict` hold policy exist to stop and why
// they are scheduled with the FSM rather than with the arithmetic.
func (AuctionOpen) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, auctionOpenID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
//
// An auction decides loot by the bid, so this is what the board shows BEFORE bidding opens: who can
// afford what. The tiebreak is deterministic for the same reason the allocator's is — a random one
// would make two replays of the same screen disagree.
func (AuctionOpen) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, auctionOpenID, acct)
}

// PriceHint answers "what should I bid?" with the smallest bid that would be accepted.
//
// The item's catalogue price wins when it has one: a guild that publishes an expected value for a
// drop is telling its bidders what it thinks the item is worth, and that is a better hint than the
// floor. With no catalogue price the hint is the pool's minimum bid, which is the only number this
// strategy can state without knowing who is asking.
func (s AuctionOpen) PriceHint(ctx Ctx, item ItemRef) (*core.Centipoints, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}

	if item.FixedPriceCp != nil {
		hint := *item.FixedPriceCp

		return &hint, nil
	}

	hint := cfg.MinBidCp

	return &hint, nil
}

// ValidateBid rejects a bid before a session accepts it.
//
// WHAT IT CAN CHECK AND WHAT IT CANNOT. The signature carries no Session, so the leader's amount is
// invisible here and "beat the leader by one increment" cannot be enforced from a planner — that is a
// session-scoped rule and it lands with the FSM in Phase 6 (#219). What IS checkable without a
// session is the LATTICE the same rule implies: docs/guides/auctions.md words the increment as
// `min_bid + k × increment`, which is a property of the bid alone, and it is what catches the 160 in
// the guide's worked example (minimum 100, increment 25) before anything else looks at it.
//
// A SEALED BID IS REFUSED. This strategy is the open auction: bids are visible live, and a caller
// marking one sealed has either mixed up the session's mode or is expecting a confidentiality this
// strategy does not provide. Accepting it would silently downgrade that expectation, which for a bid
// amount is a leak rather than a mismatch.
//
// The balance check is at the POOL HEAD, which is where a bid is being placed. It is the honest half
// of `spendable`: holds are Phase 6, so two sessions can still each accept a bid for the whole
// balance — see Spendable.
func (s AuctionOpen) ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}

	if err := checkBidIdentity(auctionOpenID, acct, bid); err != nil {
		return err
	}

	if bid.Sealed {
		return fmt.Errorf(
			"%s: the bid from account %s is marked sealed, and this is the open auction — every bid "+
				"in it is visible live: %w", auctionOpenID, bid.AccountID, ErrInvalidEvent)
	}

	if bid.AmountCp < cfg.MinBidCp {
		return fmt.Errorf("%s: the bid from account %s is %d centipoints, below the minimum of %d: %w",
			auctionOpenID, bid.AccountID, bid.AmountCp, cfg.MinBidCp, ErrInvalidEvent)
	}

	if (bid.AmountCp-cfg.MinBidCp)%cfg.IncrementCp != 0 {
		return fmt.Errorf(
			"%s: the bid from account %s is %d centipoints, which is not the minimum %d plus a whole "+
				"number of %d increments: %w",
			auctionOpenID, bid.AccountID, bid.AmountCp, cfg.MinBidCp, cfg.IncrementCp, ErrInvalidEvent)
	}

	return checkBidAffordable(ctx, auctionOpenID, acct, bid)
}

// SettleAuction awards the item to the highest bid, at that bid.
//
// PAYING YOUR OWN BID IS WHAT MAKES IT AN OPEN AUCTION. The ascending format already reveals what the
// item is worth to everyone else — the leader knows what they had to beat — so there is nothing for a
// second-price rule to discover, and charging the runner-up's number would let the leader raise
// themselves for free. That difference is the whole reason `auction_sealed` is a separate strategy
// rather than a knob here.
//
// NO ELIGIBLE BID IS THE ROT CASE, not an error: an item nobody bid on has no winner, the session goes
// to `rot`, and the guild's rot policy decides what happens to it (docs/guides/auctions.md). Returning
// an error would make an unwanted drop look like a broken auction.
func (s AuctionOpen) SettleAuction(ctx Ctx, session Session, bids []Bid) (Resolution, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return Resolution{}, err
	}

	minimum := sessionMinimum(session, cfg.MinBidCp)

	eligible := eligibleBids(bids, minimum)
	if len(eligible) == 0 {
		return Resolution{
			Reason: fmt.Sprintf("no bid of the %d placed reached the minimum of %d centipoints",
				len(bids), minimum),
		}, nil
	}

	ranked := make([]rankedBid, 0, len(eligible))
	for _, b := range eligible {
		ranked = append(ranked, rankedBid{bid: b, rank: int64(b.AmountCp)})
	}

	winner, seed := settleHighest(ctx, rankBids(ranked))

	reason := fmt.Sprintf("highest of %d eligible bids at %d centipoints; the winner pays their bid",
		len(eligible), winner.bid.AmountCp)
	if seed != nil {
		reason = fmt.Sprintf("%s, after a seeded roll between the bids tied at that amount and time",
			reason)
	}

	return Resolution{
		Winners: []Allocation{{AccountID: winner.bid.AccountID, AmountCp: winner.bid.AmountCp}},
		Reason:  reason,
		RngSeed: seed,
	}, nil
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. requireInvariantsAgree compares the two by kind and balance kind for exactly that reason.
func (AuctionOpen) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
