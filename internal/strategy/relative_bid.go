package strategy

import (
	"fmt"
	"math/bits"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// relative_bid — bid a share of your bank rather than a number. Phase 1, #195.
//
// The guide's worked example is the whole design in two rows
// (docs/guides/choosing-a-dkp-system.md): Tankguy holds 900 points and commits 360, which is 40% of
// his bank; Healbot holds 500 and commits 275, which is 55%. HEALBOT WINS while paying 85 fewer
// points. A hoarder therefore pays more absolute points for the same priority, which is the entire
// argument for the model — it is a cap that nobody has to configure.
//
// THE SHARE IS RESOLVED AGAINST A FROZEN BALANCE, and that is the rule this file exists to get right.
// Session.SeqAtOpen is the seq every balance in a session is read at, positionally, and
// .claude/rules/ledger-and-strategy.md is explicit about why: "resolving against live balances lets a
// concurrent decay run rewrite everyone's bid mid-auction, and the bug only appears on the one night
// a decay job overlaps a raid". Every Balance call in SettleAuction passes run-of-the-session's
// seq_at_open and never HeadSeq; a test asserts the seqs read, not just the winner.
//
// A BID IS STILL CENTIPOINTS, NOT BASIS POINTS, and that is deliberate rather than incidental.
// Bid.AmountCp is the amount a Phase 6 hold is placed for, the amount the ledger debits, and the
// amount every other strategy in this package compares against a balance. A strategy that redefined
// that one field to mean "percent" would produce a hold of 40 centipoints for a 40% bid — silently,
// in the one subsystem whose job is to stop a raider spending the same points twice. So a bidder
// types a percentage, the API resolves it against the balance it froze and stores the resulting
// AMOUNT, and this strategy ranks by the share that amount represents. The percentage is a rendering
// of the number; the number is the money.
//
// WHAT LANDS HERE IS THE ARITHMETIC, NOT THE AUCTION: the state machine, the reveal, anti-snipe and
// holds are Phase 6 (docs/guides/auctions.md).
//
// THE LADDER OUTRANKS THE SHARE, as it outranks every other ranking in this family (#224). This
// strategy does not partition on the tier the way the two auctions do — a share is not a price and
// there is no second-price rule here to keep inside a rung — but the ordering it settles by comes
// from rankBids, which compares the rung first. So a main who committed 10% of their bank takes the
// item from an alt who committed all of theirs, and a recorded tier nobody can rank stops the
// settlement rather than being ranked at the bottom.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = RelativeBid{}

// RelativeBid is the percentage-of-balance spend strategy. STATELESS: everything it needs arrives
// through the Ctx façade.
type RelativeBid struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	relativeBidID      = "relative_bid"
	relativeBidVersion = "0.1.0"
)

// relativeBidConfigSchema is the JSON Schema for the pool config.
//
// Draft 2020-12, `additionalProperties: false`, and every ratio an INTEGER named `_bp`: canonical §1
// bans a decimal in the point path, and a schema that said `number` would invite one. A share is
// basis points for the same reason a rate is — 4000 is 40%, exactly, on every platform.
const relativeBidConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Relative bid",
  "description": "Bids are a share of the bidder's balance, frozen at the moment the session opened. The largest share wins and pays what it committed.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "min_bid_bp": {
      "type": "integer",
      "minimum": 0,
      "maximum": 10000,
      "default": 0,
      "title": "Smallest share a bid may commit (basis points)",
      "description": "10000 is 100% of the frozen balance. 0 means the pool sets no minimum share."
    },
    "max_bid_bp": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10000,
      "default": 10000,
      "title": "Largest share a bid may commit (basis points)",
      "description": "10000 permits an all-in bid. A guild that wants nobody emptying their bank on one drop sets 5000."
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
// constant.
func (RelativeBid) ConfigSchema() []byte { return []byte(relativeBidConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (RelativeBid) ID() string { return relativeBidID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (RelativeBid) Version() string { return relativeBidVersion }

// RuleKind is spend: this strategy answers "how are points spent?" and nothing else (ADR-0026).
func (RelativeBid) RuleKind() RuleKind { return RuleSpend }

// BalanceKinds is the one balance kind this strategy moves.
func (RelativeBid) BalanceKinds() []string { return []string{BalanceKindDKP} }

// relativeBidConfig is the parsed pool config. The JSON tags are the schema's property names and the
// two must agree.
type relativeBidConfig struct {
	MinBidBp   int64            `json:"min_bid_bp"`
	MaxBidBp   int64            `json:"max_bid_bp"`
	Proceeds   string           `json:"proceeds"`
	SoloPolicy string           `json:"solo_policy"`
	FloorCp    core.Centipoints `json:"floor_cp"`
}

// defaultRelativeBidConfig is the config a pool that has set nothing runs under: any share up to the
// whole bank, with the proceeds leaving circulation.
func defaultRelativeBidConfig() relativeBidConfig {
	return relativeBidConfig{
		MinBidBp:   0,
		MaxBidBp:   basisPointsWhole,
		Proceeds:   ProceedsGuildBank,
		SoloPolicy: SoloPolicyGuildBank,
		FloorCp:    0,
	}
}

// terms is the part of this config every spend rule shares. See spend.go.
func (cfg relativeBidConfig) terms() spendTerms {
	return spendTerms{Proceeds: cfg.Proceeds, SoloPolicy: cfg.SoloPolicy, FloorCp: cfg.FloorCp}
}

// config parses and validates the pool's config, re-validating what the API edge already validated.
func (RelativeBid) config(ctx Ctx) (relativeBidConfig, error) {
	cfg := defaultRelativeBidConfig()

	if err := decodeConfig(relativeBidID, ctx.ConfigJSON(), &cfg); err != nil {
		return relativeBidConfig{}, err
	}

	return validateRelativeBidConfig(cfg)
}

// validateRelativeBidConfig applies the bounds the schema declares, to a config that has already
// parsed. Split from config so that the defaults are validated too.
func validateRelativeBidConfig(cfg relativeBidConfig) (relativeBidConfig, error) {
	if err := validateSpendTerms(relativeBidID, cfg.terms()); err != nil {
		return relativeBidConfig{}, err
	}

	if cfg.MinBidBp < 0 || cfg.MinBidBp > basisPointsWhole {
		return relativeBidConfig{}, fmt.Errorf("%s: min_bid_bp is %d, want 0..%d: %w",
			relativeBidID, cfg.MinBidBp, basisPointsWhole, ErrInvalidConfig)
	}

	// A SHARE ABOVE 100% IS NOT A BID, it is an overdraft with a percentage sign. The frozen balance
	// is the whole of what a bidder has to commit, and a pool that wants debt bidding wants a negative
	// floor_cp — which is a decision about the ledger rather than about the auction.
	if cfg.MaxBidBp <= 0 || cfg.MaxBidBp > basisPointsWhole {
		return relativeBidConfig{}, fmt.Errorf(
			"%s: max_bid_bp is %d, want 1..%d; a share above the whole balance is an overdraft "+
				"wearing a percentage: %w",
			relativeBidID, cfg.MaxBidBp, basisPointsWhole, ErrInvalidConfig)
	}

	if cfg.MinBidBp > cfg.MaxBidBp {
		return relativeBidConfig{}, fmt.Errorf(
			"%s: min_bid_bp %d is above max_bid_bp %d, so no share is bidable: %w",
			relativeBidID, cfg.MinBidBp, cfg.MaxBidBp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAward debits the winner what they committed and routes the proceeds.
//
// THE PRICE IS THE COMMITTED AMOUNT AND IT IS REQUIRED. It is not re-derived from a share here, and
// that is the point of the frozen snapshot: the share was resolved against the balance at
// seq_at_open, and re-deriving it at award time against a balance that has since moved would charge a
// different number than the one the auction resolved.
func (s RelativeBid) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if ev.PriceCp == nil {
		return BatchProposal{}, fmt.Errorf(
			"%s: the award for item %q carries no price; a relative bid settles at the amount the "+
				"winner committed, resolved against the balance frozen at the session's open: %w",
			relativeBidID, ev.Item.Name, ErrInvalidEvent)
	}

	return spendAward(ctx, relativeBidID, relativeBidVersion, cfg.terms(), ev, *ev.PriceCp)
}

// PlanAttendance is unsupported: bidding is how points are spent, not how they are earned.
func (RelativeBid) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return spendPlanAttendance(relativeBidID)
}

// PlanDecay is unsupported: a pool that wants points to expire runs an over-time rule beside this one.
func (RelativeBid) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return spendPlanDecay(relativeBidID)
}

// PlanAdjustment moves points between an account and a counterparty — two entries, never one.
func (s RelativeBid) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, relativeBidID, relativeBidVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed. Entry-wise negation is correct here
// because this strategy's only balance kind is `dkp`, a plain quantity; see reversePlan.
func (RelativeBid) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, relativeBidID, b)
}

// Spendable is the account's balance at the pool head. Holds are Phase 6 — see AuctionOpen.Spendable.
func (RelativeBid) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, relativeBidID, acct)
}

// Priority ranks every candidate EQUALLY, and the equality is the answer rather than a placeholder.
//
// Under this strategy each bidder may commit the same share of whatever they hold, so a bank of 900
// buys no more priority than a bank of 500 — that is the model's entire premise, and the guide's
// worked example is Healbot beating Tankguy while paying less. Ranking by spendable balance here
// would put the hoarder at the top of the very board this strategy exists to flatten, which is worse
// than saying "the bid decides" out loud.
//
// The account id is still the tiebreak, so a UI listing candidates renders them in a stable order
// rather than in whatever order the roster came back in.
func (RelativeBid) Priority(_ Ctx, acct AccountRef) (Priority, error) {
	return Priority{
		Rank:     0,
		Tiebreak: acct.ID.String(),
		Reason:   "every bidder may commit the same share of their own balance; the bid decides",
	}, nil
}

// PriceHint has no answer for an ITEM, and says so with nil rather than with a number.
//
// What an item costs here depends on who is asking — a 40% bid is 360 points for one raider and 200
// for another — and PriceHint is handed an item and no account. nil with a nil error is the façade's
// documented "no hint for this item", which renders as an absent field; ErrUnsupported would be the
// wrong answer because this strategy plainly does have a concept of a price, and the API's 501 would
// tell a bidding UI not to draw a bid box for a pool whose whole point is bidding.
func (RelativeBid) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, nil
}

// ValidateBid rejects a bid before a session accepts it.
//
// IT CHECKS AGAINST THE POOL HEAD, AND THE SETTLEMENT CHECKS AGAINST seq_at_open. The signature
// carries no Session (#219), so the frozen balance is not visible here — and the two answers can
// legitimately differ, because a bidder who earned a tick after the session opened has a larger head
// balance than frozen one. This is therefore the PRE-ACCEPTANCE guard: it catches the bid that is
// unaffordable outright or outside the pool's share bounds at the moment it is placed. The
// authoritative share is SettleAuction's, resolved against the frozen snapshot, where a bid that no
// longer fits its frozen balance is ignored and said to be ignored.
func (s RelativeBid) ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}

	if err := checkBidIdentity(relativeBidID, acct, bid); err != nil {
		return err
	}

	if bid.AmountCp <= 0 {
		return fmt.Errorf("%s: the bid from account %s commits %d centipoints, which is no share of "+
			"anything: %w", relativeBidID, bid.AccountID, bid.AmountCp, ErrInvalidEvent)
	}

	balance, err := spendableBalance(ctx, relativeBidID, acct)
	if err != nil {
		return err
	}

	// The two ways a bid is not a share of a balance are different facts and get different messages:
	// a bidder with nothing to commit has no share to take, and a bidder committing more than they
	// hold has named a share above 100%. Collapsing them into one sentence sends the second one's
	// bidder to check a balance that is fine.
	if balance <= 0 {
		return fmt.Errorf(
			"%s: account %s has a balance of %d, and there is no share of nothing to bid: %w",
			relativeBidID, acct.ID, balance, ErrInvalidEvent)
	}

	share, ok := shareBasisPoints(bid.AmountCp, balance)
	if !ok {
		return fmt.Errorf(
			"%s: account %s bid %d centipoints of a balance of %d, which is more than the whole of "+
				"it; a share above 100%% is an overdraft wearing a percentage: %w",
			relativeBidID, acct.ID, bid.AmountCp, balance, ErrInvalidEvent)
	}

	if share < cfg.MinBidBp || share > cfg.MaxBidBp {
		return fmt.Errorf(
			"%s: the bid from account %s is %d bp of its balance, outside the pool's %d..%d bp: %w",
			relativeBidID, acct.ID, share, cfg.MinBidBp, cfg.MaxBidBp, ErrInvalidEvent)
	}

	return nil
}

// SettleAuction awards the item to the largest SHARE of a frozen balance, at what that bidder
// committed.
//
// EVERY BALANCE IS READ AT session.SeqAtOpen. Positionally, never temporally, and never at HeadSeq:
// a decay run or another settlement committed while this session was open must not change what
// anybody's percentage meant when they bid it.
//
// A BID THAT NO LONGER FITS ITS FROZEN BALANCE IS IGNORED, AND THE RESOLUTION SAYS SO. It can happen
// honestly — a bidder who earned points after the session opened may have committed more than they
// held at open — and the alternatives are both worse: awarding it would honour a share above the
// pool's ceiling, and failing the whole settlement would leave an officer unable to close a session
// at 01:00 because one bid became stale. What must not happen is a silent drop, so the count is in
// the reason an officer reads.
//
// NO ELIGIBLE BID IS THE ROT CASE, not an error.
func (s RelativeBid) SettleAuction(ctx Ctx, session Session, bids []Bid) (Resolution, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return Resolution{}, err
	}

	// The session's own absolute minimum still applies: a pool that bids in shares may still refuse a
	// bid of two centipoints. Zero — the ordinary case — keeps every positive bid.
	eligible := eligibleBids(bids, session.MinAmountCp)

	ranked := make([]rankedBid, 0, len(eligible))

	var ignored int

	for _, b := range eligible {
		frozen, err := ctx.Balance(b.AccountID, BalanceKindDKP, session.SeqAtOpen)
		if err != nil {
			return Resolution{}, fmt.Errorf("%s: read the frozen balance for account %s at seq %d: %w",
				relativeBidID, b.AccountID, session.SeqAtOpen, err)
		}

		share, ok := shareBasisPoints(b.AmountCp, frozen)
		if !ok || share < cfg.MinBidBp || share > cfg.MaxBidBp {
			ignored++

			continue
		}

		ranked = append(ranked, rankedBid{bid: b, rank: share})
	}

	if len(ranked) == 0 {
		return Resolution{
			Reason: fmt.Sprintf(
				"none of the %d bids placed is a %d..%d bp share of its balance frozen at seq %d",
				len(bids), cfg.MinBidBp, cfg.MaxBidBp, session.SeqAtOpen),
		}, nil
	}

	ordered, err := rankBids(relativeBidID, ranked)
	if err != nil {
		return Resolution{}, err
	}

	winner, seed := settleHighest(ctx, ordered)

	reason := fmt.Sprintf(
		"largest share of a balance frozen at seq %d: %d bp, committing %d centipoints",
		session.SeqAtOpen, winner.rank, winner.bid.AmountCp)
	if ignored > 0 {
		reason = fmt.Sprintf("%s (%d bid(s) ignored as no longer a bidable share of that balance)",
			reason, ignored)
	}

	// "The largest share" is only true within the rung that took the item, so where a lower one holds
	// a bid the sentence says which and how many. It names no share and no amount: the count above a
	// bidder is disclosable and the values below them are not (docs/guides/auctions.md).
	if below := lowerRungs(ordered); below > 0 {
		reason = fmt.Sprintf("tier %s takes the item ahead of %d bid(s) on lower rungs; %s",
			tierOf(winner.bid), below, reason)
	}

	if seed != nil {
		reason += ", after a seeded roll between the bids tied at that share"
	}

	return Resolution{
		Winners:     []Allocation{{AccountID: winner.bid.AccountID, AmountCp: winner.bid.AmountCp}},
		Reason:      reason,
		RngSeed:     seed,
		WinningTier: tierOf(winner.bid),
		TierCounts:  tierCountsOf(ordered),
	}, nil
}

// shareBasisPoints is amount / balance in basis points, FLOORED, computed exactly in integers.
//
// The 128-bit product is the same technique fixed_price's decayAmount and ledger.Allocate use, and
// for the same reason: `amount * 10000` overflows int64 for a large bid, a float would be a lint
// failure and would lose precision exactly where the ranking lives, and math/big would allocate per
// bid on every settlement.
//
// IT REFUSES rather than clamping when the bid exceeds the balance, and the refusal is load-bearing
// twice over. It is the answer the callers need — a bid above 100% is not a share this pool accepts —
// and it is also what keeps bits.Div64 from panicking: the quotient fits in 64 bits precisely because
// amount <= balance bounds it at 10000. A balance of zero or less has no shares at all, which is a
// different fact from a share of zero and is why this returns ok=false rather than 0.
//
// FLOORED, never rounded. Rounding a share up would let a 39.996% bid rank as 40%, which is the
// difference between winning and losing an item in a strategy whose whole ordering is this number.
func shareBasisPoints(amount, balance core.Centipoints) (int64, bool) {
	if balance <= 0 || amount < 0 || amount > balance {
		return 0, false
	}

	hi, lo := bits.Mul64(uint64(amount), basisPointsWhole)
	q, _ := bits.Div64(hi, lo, uint64(balance))

	return int64(q), true
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
func (RelativeBid) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
