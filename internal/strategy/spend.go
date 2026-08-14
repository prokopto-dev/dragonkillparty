package strategy

import (
	"fmt"
	"sort"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// What every spend rule in this package does identically, in one place. Phase 1, #195.
//
// It is common.go's argument applied to the second family. `fixed_price` shipped alone and carried
// the award assembly privately; `auction_open`, `auction_sealed`, `relative_bid` and `roll` all debit
// a buyer and route the proceeds the same way, and Go puts them all in one package — so a second copy
// is not merely duplication, it is a name collision that forces one copy to be renamed and then to
// drift. `fixed_price.PlanAward` now returns through spendAward too, and its committed goldens are
// what prove the extraction moved code without moving behaviour.
//
// THE LINE THIS FILE DRAWS. A spend rule differs from every other spend rule in HOW THE PRICE IS
// DECIDED — a published price, the highest bid, the runner-up plus an increment, the largest share of
// a frozen balance, a die roll. It does not differ in what the batch then looks like: the buyer is
// debited the price, the proceeds land on the guild bank or are split across the beneficiaries by
// largest remainder, and the batch sums to zero. So the price resolution stays in each strategy,
// where it is the thing worth reading, and everything after it is here.
//
// WHAT IS DELIBERATELY NOT HERE, because it is Phase 6 and not arithmetic: the bid state machine
// (draft → open → extended → closing → resolved → settled), the anti-snipe window and holds.
//
// AND WHAT IS NOT HERE YET THOUGH IT IS ARITHMETIC: tier-aware resolution — "a 10-point main bid
// beats a 350-point alt bid" (docs/guides/auctions.md), the most consequential rule in the product.
// It is a Phase 1 deliverable of its own (ROADMAP item 12, #224) rather than part of this one: tier
// is derived server-side from the bidding character at bid time and recorded on the award, and
// strategy.Bid carries no such field, so implementing it means widening a shared type rather than
// adding arithmetic to these five. What lands here is the amount comparison the tier phase runs
// INSIDE the winning tier, which is the same comparison whether or not tiers exist.
//
// PURITY (law 3) applies as everywhere in this package: no internal/store, no wall clock, no
// math/rand — the settlement's coin flip is ctx.Rng(), whose seed is persisted onto the batch — and
// no float. arch_test.go walks the real import graph and would say so.

// spendTerms is what a spend rule's config says about the BATCH rather than about the price.
//
// The three knobs below are identical in every spend strategy and are validated identically, which is
// why they travel together rather than as three arguments: a planner that passed its floor where its
// solo policy belonged would still compile.
type spendTerms struct {
	// Proceeds is where the price goes: ProceedsGuildBank (a sink) or ProceedsAttendees (split
	// across the beneficiaries by largest remainder).
	Proceeds string

	// SoloPolicy is which system account receives the proceeds when there is nobody to split them
	// across — SoloPolicyGuildBank or SoloPolicyWriteOff. A degenerate case routes to a
	// ledger-addressable account, never to a silent drop, because that is what keeps conservation
	// verifiable when the arithmetic has nowhere to put the money.
	SoloPolicy string

	// FloorCp is the lowest balance an award may leave the buyer at. It is declared as
	// InvariantNonNegative on the proposal, so an overdraft is refused before anything is written.
	FloorCp core.Centipoints
}

// validateSpendTerms applies the bounds the schemas declare, to a config that has already parsed.
//
// It is shared for the reason the vocabulary is shared: `proceeds` and `solo_policy` mean the same
// thing in five strategies, and five copies of the same switch is five chances for one of them to
// accept a value the others refuse — which would be a pool whose settings page saved cleanly and
// whose awards then went somewhere nobody chose.
func validateSpendTerms(strategyID string, terms spendTerms) error {
	switch terms.Proceeds {
	case ProceedsGuildBank, ProceedsAttendees:
	default:
		return fmt.Errorf("%s: proceeds is %q, want %q or %q: %w",
			strategyID, terms.Proceeds, ProceedsGuildBank, ProceedsAttendees, ErrInvalidConfig)
	}

	switch terms.SoloPolicy {
	case SoloPolicyGuildBank, SoloPolicyWriteOff:
	default:
		return fmt.Errorf("%s: solo_policy is %q, want %q or %q: %w",
			strategyID, terms.SoloPolicy, SoloPolicyGuildBank, SoloPolicyWriteOff, ErrInvalidConfig)
	}

	return nil
}

// spendAward is the award batch every spend rule writes: the buyer debited the resolved price, the
// proceeds routed, the whole thing summing to zero.
//
// THE PRICE ARRIVES RESOLVED. Deciding it is the caller's — it is the one thing a spend rule is for —
// and this function refuses a non-positive one rather than writing an award of nothing. A strategy
// whose price resolution can legitimately produce zero (a free roll) must say so with ErrNothingToPlan
// before it gets here, because "the officer priced this at nothing" and "this pool does not charge for
// loot" are different facts and only the second is a batch nobody should write.
//
// THE BUYER IS NOT EXCLUDED FROM THE SPLIT. In a redistributing guild a raider who buys an item is
// still an attendee of the raid and receives their share of their own payment, which is what makes
// the model zero-sum rather than a tax. Excluding them would be a different DKP system, and one
// nobody asked for.
func spendAward(
	ctx Ctx, strategyID, strategyVersion string, terms spendTerms, ev AwardEvent,
	price core.Centipoints,
) (BatchProposal, error) {
	if ev.Buyer.ID == "" {
		return BatchProposal{}, fmt.Errorf("%s: award has no buyer: %w", strategyID, ErrInvalidEvent)
	}

	if ev.Buyer.IsSystem() {
		return BatchProposal{}, fmt.Errorf(
			"%s: buyer %s is a system account; the four system accounts are counterparties, never "+
				"purchasers: %w", strategyID, ev.Buyer.ID, ErrInvalidEvent)
	}

	if price <= 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: the award resolves to a price of %d centipoints, which charges the buyer nothing: %w",
			strategyID, price, ErrInvalidEvent)
	}

	itemID := optionalULID(ev.Item.ID)

	entries := []EntryProposal{{
		AccountID:   ev.Buyer.ID,
		CharacterID: ev.CharacterID,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -price,
		ItemID:      itemID,
		ItemAwardID: ev.ItemAwardID,
		RaidID:      ev.RaidID,
	}}

	invariants := []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &terms.FloorCp},
	}

	credits, split, err := routeProceeds(ctx, strategyID, terms, ev.Beneficiaries, price)
	if err != nil {
		return BatchProposal{}, err
	}

	if split {
		// Declared only when a split actually happened. LargestRemainderSumsToDebit and SumZero are
		// the same arithmetic and deliberately different rules — this one names the mistake of
		// rounding each credit independently — so claiming it for a batch with a single credit would
		// be asserting something about an allocation that never ran.
		invariants = append(invariants,
			Invariant{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP})
	}

	for _, c := range credits {
		entries = append(entries, EntryProposal{
			AccountID:   c.AccountID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    c.AmountCp,
			ItemID:      itemID,
			ItemAwardID: ev.ItemAwardID,
			RaidID:      ev.RaidID,
		})
	}

	return proposeZeroSum(ctx, strategyID, strategyVersion, kinds.KindAward, ev.EffectiveAt, ev.Reason,
		entries, invariants)
}

// routeProceeds turns a price into the credits that balance it, and reports whether the allocator ran.
//
// The two config paths differ in more than their destination: the guild-bank path is one credit and
// cannot round, while the attendees path is a largest-remainder split whose credits must sum to
// exactly the debit. Keeping them in one function is what makes it impossible to add a third
// destination that forgets to balance.
func routeProceeds(
	ctx Ctx, strategyID string, terms spendTerms, beneficiaries []Share, price core.Centipoints,
) (credits []Allocation, split bool, err error) {
	if terms.Proceeds == ProceedsGuildBank {
		bank, err := ctx.SystemAccount(SystemKeyGuildBank)
		if err != nil {
			return nil, false, fmt.Errorf("%s: resolve the guild bank: %w", strategyID, err)
		}

		return []Allocation{{AccountID: bank, AmountCp: price}}, false, nil
	}

	shares := sortedShares(beneficiaries)
	if err := checkDistinctShares(strategyID, shares); err != nil {
		return nil, false, err
	}

	for _, b := range shares {
		if err := checkShare(strategyID, b); err != nil {
			return nil, false, err
		}
	}

	// The degenerate case, routed rather than dropped: nobody to split across means the solo policy
	// picks a system account and the whole price lands there. ledger.Allocate takes the same account
	// for its all-weights-zero case, which is why it is passed rather than handled here.
	solo, err := ctx.SystemAccount(terms.SoloPolicy)
	if err != nil {
		return nil, false, fmt.Errorf("%s: resolve the solo-policy account %q: %w",
			strategyID, terms.SoloPolicy, err)
	}

	credits, err = ctx.Allocate(price, shares, solo)
	if err != nil {
		return nil, false, fmt.Errorf("%s: split %d centipoints across %d beneficiaries: %w",
			strategyID, price, len(shares), err)
	}

	return credits, true, nil
}

// rankedBid is one bid with the number its strategy ranks it by: the amount for an auction, the share
// of a frozen balance for `relative_bid`, the die roll for `roll`.
//
// A SEPARATE RANK RATHER THAN SORTING BIDS DIRECTLY, because the three families rank by three
// different numbers and only one of them is on the Bid. Computing the rank once, where the strategy's
// own arithmetic lives, and ordering here is what lets the tie-break chain be written once — and the
// tie-break is the part that has to be identical, because it is what makes two replays of the same
// loot decision agree.
type rankedBid struct {
	bid  Bid
	rank int64
}

// rankBids copies the bids into settlement order: highest rank first, then highest amount, then
// earliest, then the account id.
//
// A COPY, so a settlement never reorders its caller's slice, and TOTAL on the bid's own content, so
// two callers that pass the same bids in different orders settle identically. That is the determinism
// defect that is invisible in every test that happens to build its input in the order it expects.
//
// THE CHAIN IS docs/guides/auctions.md's, MINUS THE STEPS A PLANNER CANNOT SEE. That page's order is
// tier, amount, raid attendance, balance before the bid, items won in the window, bid sequence, then a
// seeded roll. Tier is not on the Bid and attendance and items-won are not on the Ctx façade — the
// façade's own comment says a method nothing can implement is a method every implementer must fake.
// So what runs here is amount, then bid sequence (PlacedAt), then the seeded roll that settleHighest
// performs, and the account id is the last resort that makes the ORDER total even when the roll is not
// reached. A step this cannot evaluate is a step that lands ABOVE it later — tier as #224, the rest
// with the facts Phase 3 and Phase 6 record — not one it approximates.
//
// SliceStable rather than Slice, because one account may legitimately hold several bids — an open
// auction is a bidder raising themselves — so the key is not unique. Two bids identical in every key
// are indistinguishable to every rule below, and stability makes the choice between them depend on
// the input rather than on the sort's internals.
func rankBids(in []rankedBid) []rankedBid {
	out := make([]rankedBid, len(in))
	copy(out, in)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank > out[j].rank
		}

		if out[i].bid.AmountCp != out[j].bid.AmountCp {
			return out[i].bid.AmountCp > out[j].bid.AmountCp
		}

		if out[i].bid.PlacedAt != out[j].bid.PlacedAt {
			return out[i].bid.PlacedAt < out[j].bid.PlacedAt
		}

		return out[i].bid.AccountID < out[j].bid.AccountID
	})

	return out
}

// tiedAtTop is how many leading bids share the whole deterministic key — rank, amount and placement
// time. It is the count settleHighest rolls between, and it is 1 for every ordinary auction.
func tiedAtTop(ranked []rankedBid) int {
	n := 1

	for ; n < len(ranked); n++ {
		if ranked[n].rank != ranked[0].rank ||
			ranked[n].bid.AmountCp != ranked[0].bid.AmountCp ||
			ranked[n].bid.PlacedAt != ranked[0].bid.PlacedAt {
			break
		}
	}

	return n
}

// tiedOnRank is how many leading bids share the RANK alone, whatever else differs.
//
// It is a different question from tiedAtTop and `roll` is why: two raiders who both rolled 97 are
// tied, and which of them entered the session first has nothing to do with it. An auction settles a
// tie; a roll-off does not — "a re-roll on a tie is a new round, not an edit"
// (docs/guides/choosing-a-dkp-system.md).
func tiedOnRank(ranked []rankedBid) int {
	n := 1

	for ; n < len(ranked); n++ {
		if ranked[n].rank != ranked[0].rank {
			break
		}
	}

	return n
}

// settleHighest returns the winning bid and the seed it consumed, if it consumed one.
//
// THE ROLL IS THE LAST STEP OF THE TIE-BREAK CHAIN AND IT IS SEEDED. Two bids that agree on the rank,
// the amount and the microsecond are genuinely equal, and the alternative to a roll is the account id
// — which is deterministic and is also a permanent bias: the raider whose ULID sorts first wins every
// coin flip this guild ever holds. The seed is returned so it can be persisted onto the batch, which
// is what makes the flip replayable three months later when somebody asks
// (.claude/rules/ledger-and-strategy.md).
//
// The caller guarantees a non-empty list: an auction with no eligible bid is the rot case, and it is
// answered with a resolution that names no winner rather than by rolling between nobody.
func settleHighest(ctx Ctx, ranked []rankedBid) (rankedBid, *int64) {
	tied := tiedAtTop(ranked)
	if tied == 1 {
		return ranked[0], nil
	}

	seed := ctx.Rng().Seed()

	return ranked[ctx.Rng().IntN(tied)], &seed
}

// eligibleBids drops the bids a settlement may not award to: anything below the session's minimum.
//
// DROPPED RATHER THAN REFUSED, because a bid below the minimum should never have been accepted and a
// settlement that failed outright on one would hand the officer a session that cannot be closed at
// 01:00. Dropping is also what makes the rot case reachable: an item whose only bids are ineligible
// has no winner, which is exactly what `rot` means (docs/guides/auctions.md).
//
// A COPY for the reason rankBids returns one.
func eligibleBids(bids []Bid, minAmountCp core.Centipoints) []Bid {
	out := make([]Bid, 0, len(bids))

	for _, b := range bids {
		if b.AmountCp >= minAmountCp && b.AmountCp > 0 {
			out = append(out, b)
		}
	}

	return out
}

// sessionMinimum is the floor a bid must clear: the session's own minimum when it names one, and the
// pool's configured minimum otherwise.
//
// THE SESSION OVERRIDES THE POOL, in the same direction and for the same reason an officer's explicit
// price overrides the catalogue: the pool's minimum is the default a session is opened with, and a
// session opened for a raid-wide bonus item may deliberately set another. A session minimum of zero
// is "unset" rather than "free", which is the one ambiguity in the Session shape and is why this
// function exists rather than a `max` at each call site.
func sessionMinimum(s Session, configuredCp core.Centipoints) core.Centipoints {
	if s.MinAmountCp > 0 {
		return s.MinAmountCp
	}

	return configuredCp
}

// checkBidIdentity rejects the bid shapes no strategy has a defensible answer for: one that names no
// bidder, one whose bidder is not the account being validated, and one from a system account.
//
// THE BID AND THE ACCOUNT MUST AGREE. ValidateBid is handed both — the account whose eligibility is
// being checked and the bid being placed — and nothing in the types makes them the same account. A
// caller that passed one bidder's balance and another's amount would get a bid validated against
// somebody else's points, which is a defect that only ever shows up as a raider bidding more than
// they hold.
//
// A SYSTEM ACCOUNT CANNOT BID for the same reason it cannot buy: the four are counterparties, and a
// guild bank that could win an item would be a balance sheet bidding against the guild.
func checkBidIdentity(strategyID string, acct AccountRef, bid Bid) error {
	if acct.ID == "" {
		return fmt.Errorf("%s: the bid names no account: %w", strategyID, ErrInvalidEvent)
	}

	if bid.AccountID != acct.ID {
		return fmt.Errorf(
			"%s: the bid is from account %q and is being validated against account %s; a bid is "+
				"checked against its own bidder's balance or against nobody's: %w",
			strategyID, bid.AccountID, acct.ID, ErrInvalidEvent)
	}

	if acct.IsSystem() {
		return fmt.Errorf(
			"%s: account %s is a system account; the four system accounts are counterparties, never "+
				"bidders: %w", strategyID, acct.ID, ErrInvalidEvent)
	}

	return nil
}

// checkBidAffordable rejects a bid larger than the bidder's balance at the pool head.
//
// IT IS THE HONEST HALF OF `spendable`. docs/guides/auctions.md defines the quantity as
// `balance − Σ active holds`, and holds are Phase 6: there is no hold table to read, so what this
// compares against is the balance. That leaves the cross-session double-spend — two sessions each
// accepting a bid for the whole balance — which is exactly what property P4 and the `strict` hold
// policy exist to close, and both are scheduled with the FSM rather than with the arithmetic.
func checkBidAffordable(ctx Ctx, strategyID string, acct AccountRef, bid Bid) error {
	spendable, err := spendableBalance(ctx, strategyID, acct)
	if err != nil {
		return err
	}

	if bid.AmountCp > spendable {
		return fmt.Errorf(
			"%s: the bid from account %s is %d centipoints and its spendable balance is %d: %w",
			strategyID, acct.ID, bid.AmountCp, spendable, ErrInvalidEvent)
	}

	return nil
}

// spendPlanAttendance is the refusal a spend rule returns when it is asked to credit a raid tick.
//
// It is a STATEMENT rather than a gap. A spend rule answers "how are points spent?" and nothing else
// (ADR-0026); a pool earns through its earn rule. Inventing a tick award here would be a second copy
// of `tick`'s arithmetic under another name, and the two copies would disagree about what a standby
// earns on the night a guild changed one of them.
func spendPlanAttendance(strategyID string) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(strategyID,
		"credit an attendance tick: it is a spend rule, so pair it with an earn rule such as tick")
}

// spendPlanDecay is the refusal a spend rule returns when it is asked to post a cadence run. Decay is
// posted, not improvised: a rate invented here would have no cadence, no decay_run row and no
// idempotency key (.claude/rules/decay-and-jobs.md).
func spendPlanDecay(strategyID string) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(strategyID,
		"decay balances: it is a spend rule, so pair it with decay_percent, decay_window or cap")
}
