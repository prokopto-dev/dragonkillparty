package strategy

import (
	"fmt"
	"strings"
)

// The bid tier ladder, and the phase of a settlement that runs before any amount is compared.
// Phase 1, #224.
//
// THE MOST CONSEQUENTIAL RULE IN THE PRODUCT, and it inverts what every other DKP tool assumes: a
// 10-point main bid beats a 350-point alt bid, and the alt's number is never compared against the
// main's (docs/guides/auctions.md). Resolution is TWO-PHASE — the highest rung holding any eligible
// bid takes the item, and only then are amounts compared, and only among the bids standing on it.
// Getting this backwards is not a rounding error: it hands a raid's best drop to whoever brought the
// biggest bank on their alt, which is the outcome the ladder exists to prevent.
//
// THE ORDER IS THE RULE, which makes `bid.tier` the one enum in the system whose declaration order is
// semantic (canonical §5). It travels as its TEXT value everywhere — on the wire, on strategy.Bid,
// and in the column the bid table will carry when the FSM lands in Phase 6 — with the ordering
// resolved in exactly ONE place, tierRank below. Storing the rung as an integer would put a copy of
// the ladder in every reader that ever sorts by it, and the copies would drift the first time a rung
// was inserted.
//
// THE TIER IS RECORDED, NEVER RE-DERIVED. It is derived server-side from the bidding character at bid
// time, is never accepted from a client, and is read from the BID rather than from the character's
// current main flag: mains change, and a bid was made under the ladder in force on the night. A
// settlement that re-derived it would silently re-decide a months-old loot argument the first time
// somebody swapped their main — and would do it to the ledger's history, not to a screen.
//
// WHAT IS NOT HERE. Deriving the tier is not a planner's job and cannot be: it needs the roster, the
// character's main flag and the item's spec, none of which the Ctx façade carries. This file is the
// LADDER and the resolution phase that reads it; whoever accepts a bid fills the field in.

// The four rungs, highest first. The values are canonical §5's `bid.tier` vocabulary verbatim —
// lowercase snake_case, identical in the database, the JSON and the OpenAPI enum — and they are
// PUBLIC API in the same way a strategy id is: a rung recorded on a bid is a rung a resolution months
// later has to be able to read.
const (
	// TierMain is the bidder's main, bidding for its primary spec. Nothing outranks it.
	TierMain = "main"

	// TierMainOffspec is the bidder's main, bidding for anything else.
	TierMainOffspec = "main_offspec"

	// TierAlt is a character the bidder owns that is not their main.
	TierAlt = "alt"

	// TierAnyone is everybody else — recruits, guests, second accounts — and it is also where a bid
	// recording NO tier stands. See tierRank for why that reading is the honest one.
	TierAnyone = "anyone"
)

// Tiers is the ladder in resolution order, highest first.
//
// A FUNCTION RETURNING A FRESH SLICE, never a package-level var, for the reason Catalogue is one:
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on.
func Tiers() []string {
	return []string{TierMain, TierMainOffspec, TierAlt, TierAnyone}
}

// TierCount is one rung of the ladder and how many eligible bids stood on it.
//
// IT IS A COUNT AND CAN NEVER BE AN AMOUNT, which is the whole point of the type. Blind mode
// discloses exactly one thing to a bidder who cannot win — that a HIGHER tier holds bids, and how
// many — and never a value, and never the count in the caller's own tier
// (docs/guides/auctions.md). A renderer sums the rungs above the caller's; a shape that carried
// amounts would make the leak a matter of remembering not to render one.
type TierCount struct {
	// Tier is the rung, as one of the Tier* values.
	Tier string

	// Bids is how many eligible bids stood on it. Never zero: a rung nobody bid on is absent rather
	// than present with a zero, so "is anybody above me?" is a length question.
	Bids int
}

// tierRank is where a recorded tier stands on the ladder: HIGHER WINS, and the second result reports
// whether the value is on the ladder at all.
//
// AN EMPTY TIER IS `anyone`, and that is a reading rather than a tolerance. `anyone` is the rung for a
// bidder with no claim to standing, and a bid recording no claim has precisely that. It is also what
// keeps an UNTIERED session — every session there is until the bid FSM fills the field in (Phase 6) —
// settling on the amount alone: every bid lands on one rung, the tier phase decides nothing, and the
// comparison below it runs exactly as it did before this file existed.
//
// A VALUE THAT IS NOT ON THE LADDER IS REFUSED BY THE CALLER, never ranked as the bottom. Ranking an
// unrecognised tier low is how "main_offpsec" — a typo one letter from the second rung — loses an item
// to an alt, with an arithmetic trail that looks correct at every step.
func tierRank(tier string) (int, bool) {
	ladder := Tiers()

	if tier == "" {
		tier = TierAnyone
	}

	for i, t := range ladder {
		if t == tier {
			return len(ladder) - i, true
		}
	}

	return 0, false
}

// tierOf is the rung a bid stands on: its recorded tier, or `anyone` when it records none.
func tierOf(b Bid) string {
	if b.Tier == "" {
		return TierAnyone
	}

	return b.Tier
}

// checkTier reads a bid's rung, naming the strategy and the bidder when it cannot.
//
// The message lists the ladder because the failure it describes is almost always a caller writing a
// value from another vocabulary — a raid_attendance status, a character kind — into the field, and
// the four legal values are the shortest way to say so.
func checkTier(strategyID string, b Bid) (int, error) {
	rank, ok := tierRank(b.Tier)
	if !ok {
		return 0, fmt.Errorf(
			"%s: the bid from account %s records tier %q, which is not on the ladder (%s); a tier "+
				"nobody can rank is one this settlement must not guess at, because guessing it low is "+
				"how a main loses an item to an alt: %w",
			strategyID, b.AccountID, b.Tier, strings.Join(Tiers(), ", "), ErrInvalidEvent)
	}

	return rank, nil
}

// tierOutcome is the first phase's whole answer: which rung takes the item, the bids standing on it,
// and how many eligible bids stood on every rung.
type tierOutcome struct {
	// tier is the winning rung — the highest one holding an eligible bid.
	tier string

	// bids are the eligible bids standing on it, in the caller's order. The second phase ranks
	// these and nothing else.
	bids []Bid

	// counts is every rung holding at least one eligible bid, highest first.
	counts []TierCount

	// below is how many eligible bids stood on LOWER rungs. It is the number that decides whether
	// tier settled anything at all: zero means one rung held every bid, which is every session until
	// something fills the field in.
	below int
}

// resolveTier runs the first phase of a two-phase settlement over the eligible bids.
//
// IT REFUSES AN UNREADABLE TIER RATHER THAN DROPPING THE BID, which is the one place this differs
// from eligibleBids. A bid below the minimum should never have been accepted and could not win
// either way, so dropping it costs nobody an item; a bid whose rung nobody can read MIGHT BE THE
// WINNER, and dropping it awards the drop to somebody standing below a main whose bid was thrown
// away over a typo. The session machine has a `resolution_failed` state for a settlement that cannot
// be computed (docs/guides/auctions.md); it has no state for an award to the wrong raider.
//
// THE COUNTS ARE OF ELIGIBLE BIDS, not of placed ones. A bid under the floor is not a bid the board
// should tell anybody to worry about, and counting it would tell an alt that three mains are bidding
// when the three are all ineligible.
func resolveTier(strategyID string, eligible []Bid) (tierOutcome, error) {
	counts := make(map[string]int, len(Tiers()))
	best := 0

	for _, b := range eligible {
		rank, err := checkTier(strategyID, b)
		if err != nil {
			return tierOutcome{}, err
		}

		counts[tierOf(b)]++

		if rank > best {
			best = rank
		}
	}

	out := tierOutcome{}

	// Built by walking the LADDER rather than the map, which is what makes the order the ladder's and
	// the result byte-identical between two replays. Ranging a map here would be P8's classic failure:
	// a proposal that differs run to run in the one field nobody compares.
	for _, tier := range Tiers() {
		n := counts[tier]
		if n == 0 {
			continue
		}

		out.counts = append(out.counts, TierCount{Tier: tier, Bids: n})

		if rank, _ := tierRank(tier); rank == best {
			out.tier = tier
		} else {
			out.below += n
		}
	}

	for _, b := range eligible {
		if tierOf(b) == out.tier {
			out.bids = append(out.bids, b)
		}
	}

	return out, nil
}

// explain prefixes a settlement's sentence with the tier phase's decision, WHEN IT DECIDED ANYTHING.
//
// Silent when one rung held every eligible bid, because a session nobody tiered is settled by the
// amount and a sentence announcing that `anyone` won it would be noise an officer has to learn to
// ignore. Loud the moment a lower rung holds a bid, because that is the case the losing bidder
// arrives to argue about — "my 350 lost to a 10" is answered by this clause and by nothing else.
//
// IT NAMES NO AMOUNT. The count of bids below is disclosable to everybody (docs/guides/auctions.md);
// their values are not, and a resolution is pasted into guild chat.
func (o tierOutcome) explain(reason string) string {
	if o.below == 0 {
		return reason
	}

	return fmt.Sprintf("tier %s takes the item ahead of %d bid(s) on lower rungs; %s",
		o.tier, o.below, reason)
}

// step is the tier phase's line in the resolution's trace, and unlike explain it is always written:
// the trace is the artefact an officer re-reads months later, and "tier decided nothing here" is a
// fact worth having recorded rather than inferred from a silence.
func (o tierOutcome) step() ResolutionStep {
	parts := make([]string, 0, len(o.counts))
	for _, c := range o.counts {
		parts = append(parts, fmt.Sprintf("%s %d", c.Tier, c.Bids))
	}

	if o.below == 0 {
		return ResolutionStep{
			Kind: ResolutionStepTier,
			Detail: fmt.Sprintf(
				"every eligible bid stands in tier %s, so the ladder settled nothing (%s)",
				o.tier, strings.Join(parts, ", ")),
		}
	}

	return ResolutionStep{
		Kind: ResolutionStepTier,
		Detail: fmt.Sprintf(
			"tier %s is the highest rung holding an eligible bid and takes the item; the %d bid(s) "+
				"below it are never compared against it (%s)",
			o.tier, o.below, strings.Join(parts, ", ")),
	}
}
