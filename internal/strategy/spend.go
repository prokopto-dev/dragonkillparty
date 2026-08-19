package strategy

import (
	"fmt"
	"slices"
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
// TIER IS NOW THE FIRST COMPARISON EVERY ORDERING HERE MAKES (#224). The ladder and the phase that
// partitions on it live in tier.go; what this file owns is that no ranking in the spend family can
// skip it — rankBids compares the rung before the rank, and both tie counters carry the rung in their
// key, so a settlement cannot roll a main against an alt however it assembled its input.
//
// AND THE COMMITTED AMOUNT IS NOT A COMPARISON HERE AT ALL (#244). It used to sit below the rank,
// where for the two auctions it was the rank restated — they rank BY the amount — and for `roll` it
// was a number an entry may not even carry. `relative_bid` was the one rule it decided, and there it
// decided in favour of the larger bank: two raiders committing the same share of what they hold are
// equal under that model by construction, and handing the item to whichever of them holds more is the
// hoarder's advantage the rule exists to remove. So an equal rank now falls through to the chain
// docs/guides/auctions.md actually describes — bid sequence, then the seeded roll — and no rung of
// that chain is a bank balance.
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
	// checkBuyer rather than two inline conditions, because a spend rule may legitimately return
	// before reaching this function and must refuse the same buyers when it does — see its comment.
	if err := checkBuyer(strategyID, ev.Buyer); err != nil {
		return BatchProposal{}, err
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

	// A system account may RECEIVE the proceeds as the solo policy's destination below, and may never
	// be one of the beneficiaries they are split across: the split divides a fixed price, so a share
	// for the guild bank is a share taken from every raider who was actually there — and the batch
	// still sums to zero and still passes the invariant engine (review of #228).
	if err := checkNoSystemAccounts(ctx, strategyID, shares); err != nil {
		return nil, false, err
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

	// tier is the bid's rung on the ladder, HIGHER FIRST, filled in by rankBids rather than by the
	// strategy that built the rankedBid. Computed once where the ladder is read, so that a comparator
	// running O(n log n) times cannot be the place a tier is parsed — or, worse, the place one
	// strategy parses it differently from another.
	tier int
}

// rankBids copies the bids into settlement order: highest TIER first, then highest rank, then
// earliest, then the account id.
//
// A COPY, so a settlement never reorders its caller's slice, and TOTAL on the bid's own content, so
// two callers that pass the same bids in different orders settle identically. That is the determinism
// defect that is invisible in every test that happens to build its input in the order it expects.
//
// THE CHAIN IS docs/guides/auctions.md's, MINUS THE STEPS A PLANNER CANNOT SEE, AND NOTHING ELSE.
// That page's order is tier, amount, raid attendance, balance before the bid, items won in the
// window, bid sequence, then a seeded roll. TIER IS STEP 1 AND IT IS HERE (#224): it is recorded on
// the bid, so a pure planner can read it, and reading it is not optional — "a 10-point main bid beats
// a 350-point alt bid" is the most consequential rule in the product and every ordering in this
// family runs through this function. Attendance, the balance before the bid and items-won are still
// not on the Ctx façade — the façade's own comment says a method nothing can implement is a method
// every implementer must fake — so what runs below the rung is the rank, then bid sequence
// (PlacedAt), then the seeded roll that settleHighest performs, with the account id as the last
// resort that makes the ORDER total even when the roll is not reached. A step this cannot evaluate
// lands ABOVE those later, with the facts Phase 3 and Phase 6 record, rather than being approximated
// now.
//
// THE COMMITTED AMOUNT IS NOT ONE OF THOSE STEPS (#244), and it was until this comparator stopped
// comparing it. The guide's step 4 is the bidder's BALANCE before the bid, four rungs down and below
// attendance; "the larger number of points committed" appears nowhere in the chain, and an extra rung
// that only one rule can reach is a rule nobody documented and nobody chose. For the two auctions the
// rank IS the amount, so removing it changes no ordering they can produce; for `roll` an entry
// carries no amount at all. `relative_bid` is the rule it decided, and see spend.go's header for why
// deciding a tied share by the size of the bank inverts that rule's whole argument.
//
// IT REFUSES A TIER IT CANNOT RANK, which is why it returns an error at all. The two auctions have
// already partitioned on the ladder by the time they call this, so for them the tier key is a
// tautology — deliberately, because `relative_bid` and `roll` rank bids WITHOUT partitioning, and
// leaving them tier-blind would mean a main's roll of 3 losing to an alt's 97 in a session the guild
// believed was tiered. One funnel, one rule, one refusal.
//
// SliceStable rather than Slice, because one account may legitimately hold several bids — an open
// auction is a bidder raising themselves — so the key is not unique. Two bids identical in every key
// are indistinguishable to every rule below, and stability makes the choice between them depend on
// the input rather than on the sort's internals.
func rankBids(strategyID string, in []rankedBid) ([]rankedBid, error) {
	out := make([]rankedBid, len(in))
	copy(out, in)

	for i := range out {
		tier, err := checkTier(strategyID, out[i].bid)
		if err != nil {
			return nil, err
		}

		out[i].tier = tier
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tier != out[j].tier {
			return out[i].tier > out[j].tier
		}

		if out[i].rank != out[j].rank {
			return out[i].rank > out[j].rank
		}

		if out[i].bid.PlacedAt != out[j].bid.PlacedAt {
			return out[i].bid.PlacedAt < out[j].bid.PlacedAt
		}

		return out[i].bid.AccountID < out[j].bid.AccountID
	})

	return out, nil
}

// tiedAtTop is how many leading bids share the whole deterministic key — rung, rank and placement
// time. It is the count settleHighest rolls between, and it is 1 for every ordinary auction.
//
// THE KEY IS THE COMPARATOR'S, EXACTLY. rankBids stops ordering below the microsecond, so the bids
// this counts are the ones nothing above the roll can separate; a counter reading a field the
// comparator does not read would leave a settlement decided by a step that is neither in the chain
// nor in the trace. That is what the committed amount was doing here before #244, and dropping it
// from one of the two without the other would have moved the defect rather than fixed it.
//
// THE RUNG IS PART OF THE KEY, and it is the part that decides whether a coin is flipped at all. Two
// bids equal in rank and in the microsecond are genuinely equal only if they are on the same rung; a
// main and an alt who bid the same number in the same instant are not tied, the main has won, and a
// counter that omitted the rung would roll for it.
func tiedAtTop(ranked []rankedBid) int {
	n := 1

	for ; n < len(ranked); n++ {
		if ranked[n].tier != ranked[0].tier ||
			ranked[n].rank != ranked[0].rank ||
			ranked[n].bid.PlacedAt != ranked[0].bid.PlacedAt {
			break
		}
	}

	return n
}

// tiedOnTier is how many leading bids stand on the WINNING RUNG, and lowerRungs is how many stand
// below it.
//
// Both are prefix counts for the reason the tie counters are: rankBids compares the rung first, so
// the winning rung's bids are a contiguous run at the head of the slice. They exist for the two rules
// that rank ACROSS the ladder rather than partitioning on it — `relative_bid` and `roll` — which
// still have to say what the ladder decided and must not describe a winner as the largest share or
// the highest roll of a set it was only the largest of on its own rung.
func tiedOnTier(ranked []rankedBid) int {
	n := 1

	for ; n < len(ranked); n++ {
		if ranked[n].tier != ranked[0].tier {
			break
		}
	}

	return n
}

func lowerRungs(ranked []rankedBid) int {
	if len(ranked) == 0 {
		return 0
	}

	return len(ranked) - tiedOnTier(ranked)
}

// tierCountsOf tallies ranked bids by rung, highest first — the disclosure shape, for the two rules
// that do not go through resolveTier.
//
// IT REQUIRES A RANKED SLICE, which is what lets it group by comparing against the previous entry
// rather than by building a map: rankBids has already put each rung's bids in one contiguous run, and
// a map would put the ladder's order at the mercy of Go's map iteration — P8's classic failure.
func tierCountsOf(ranked []rankedBid) []TierCount {
	out := make([]TierCount, 0, len(Tiers()))

	for _, r := range ranked {
		tier := tierOf(r.bid)

		if n := len(out); n > 0 && out[n-1].Tier == tier {
			out[n-1].Bids++

			continue
		}

		out = append(out, TierCount{Tier: tier, Bids: 1})
	}

	return out
}

// tiedOnRank is how many leading bids share the rung and the RANK alone, whatever else differs. It is
// what the trace reads to say WHICH step of the chain decided the item: one means the rank did — the
// amount in an auction, the share in `relative_bid`, the die in `roll` — and more than one means the
// chain ran on past it, to the bid sequence and then to the roll.
//
// It is a different question from tiedAtTop and `roll` is why: two raiders who both rolled 97 are
// tied, and which of them entered the session first has nothing to do with it. An auction settles a
// tie; a roll-off does not — "a re-roll on a tie is a new round, not an edit"
// (docs/guides/choosing-a-dkp-system.md). The rung is in the key for tiedAtTop's reason: a main who
// rolled 97 and an alt who rolled 97 are not tied, and a round that declared them tied would send a
// guild into a re-roll the ladder had already settled.
func tiedOnRank(ranked []rankedBid) int {
	n := 1

	for ; n < len(ranked); n++ {
		if ranked[n].tier != ranked[0].tier || ranked[n].rank != ranked[0].rank {
			break
		}
	}

	return n
}

// tiedAccounts is WHO is tied on the rank: the distinct accounts holding the leading bids that share
// the rung and the rank, ascending by id (#248).
//
// IT IS tiedOnRank COUNTED IN BIDDERS RATHER THAN IN BIDS, and the two differ in exactly the case a
// tie must not be declared in. Bids are append-only and one account may hold several — a raise, a
// retraction and its replacement — so two rows at the top can be one person who bid the same number
// twice. That is a settlement with one bidder in it, and calling it a tie would send a guild into a
// rebid round against nobody. Two entries here is what a tie is; one is a winner.
//
// SORTED, so two replays of the same settlement name the parties in the same order. The tie-break
// chain's determinism argument applies to the artefact that RECORDS a tie exactly as it does to the
// steps that break one: a set that came back in bid order would be a resolution whose bytes depended
// on the order a caller happened to collect its bids in.
//
// The caller decides what a tie MEANS — a rebid round for a sealed auction, a submission race for an
// open one, another round of the same kind for a roll. This function only answers who is in it.
func tiedAccounts(ranked []rankedBid) []core.ULID {
	if len(ranked) == 0 {
		return nil
	}

	tied := bidPerAccount(ranked[:tiedOnRank(ranked)])

	out := make([]core.ULID, 0, len(tied))
	for _, r := range tied {
		out = append(out, r.bid.AccountID)
	}

	slices.Sort(out)

	return out
}

// bidPerAccount collapses a run of ranked bids to ONE row per account, keeping each account's first
// row in the comparator's order.
//
// IT IS THE ONE PLACE THIS PACKAGE TURNS BIDS INTO BIDDERS, which is why both callers go through it.
// A tie is between people: bids are append-only and one account may hold several — a raise, a
// retraction and its replacement — so a run of equal rows is not a run of equal bidders. Naming the
// tied parties and rolling between them are the same question asked twice, and two implementations
// of it are two chances to answer it differently in the one place a settlement is deciding who owns
// a raid's best drop.
//
// THE ORDER IS THE COMPARATOR'S, not the input's: rankBids has already put the run in a total order
// whose last key is the account id, so each account's kept row and the order they are kept in are
// both deterministic. A roll drawn over a list that depended on the input order would be a roll that
// depended on a query plan.
func bidPerAccount(ranked []rankedBid) []rankedBid {
	out := make([]rankedBid, 0, len(ranked))

	for _, r := range ranked {
		if !slices.ContainsFunc(out, func(kept rankedBid) bool {
			return kept.bid.AccountID == r.bid.AccountID
		}) {
			out = append(out, r)
		}
	}

	return out
}

// contested reports whether the top of the order is held by MORE THAN ONE BIDDER — the question
// every step below the rank is an answer to (AO review of #248).
//
// IT IS THE GATE ON THE WHOLE BOTTOM OF THE CHAIN. A run of equal top rows can be one person who bid
// the same number twice, and there is no bid sequence to compare and no roll to hold between a
// bidder and themselves: they simply won. The trace records the steps that were EVALUATED
// (Resolution.Trace), so a `bid_sequence` line saying "the earliest of them takes the item" written
// for a comparison that never happened is a resolution asserting a decision nobody made — which is
// exactly the sort of plausible line an officer would later have to defend.
//
// A ROW COUNT CANNOT ANSWER IT, which is why this exists rather than a comparison against
// tiedOnRank: that counts rows, and rows are not people.
func contested(ranked []rankedBid) bool {
	return len(tiedAccounts(ranked)) > 1
}

// tiedBiddersAtTop is how many BIDDERS the roll would be drawn between — tiedAtTop counted in people
// rather than in rows. It is what the trace reports, so that the sentence describes the draw that
// actually happened.
func tiedBiddersAtTop(ranked []rankedBid) int {
	if len(ranked) == 0 {
		return 0
	}

	return len(bidPerAccount(ranked[:tiedAtTop(ranked)]))
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
// THE ROLL IS BETWEEN BIDDERS AND NOT BETWEEN BID ROWS (AO review of #248), and the difference is a
// raider's odds. Bids are append-only and one account may hold several, so a bidder whose duplicate
// bid sits in the tied run would draw a second ticket: with A holding two identical top bids and B
// one, a roll over the rows gives A two chances in three where the rule — and the tie the settlement
// just reported, which names two PEOPLE — says one in two. It is silent, it is plausible, and it is
// wrong in the direction of whoever submitted twice.
//
// A RUN THAT IS ALL ONE ACCOUNT THEREFORE CONSUMES NO RANDOMNESS. There is one bidder, they have won,
// and a seed recorded there would claim a coin was flipped to decide something nobody was contesting.
//
// The caller guarantees a non-empty list: an auction with no eligible bid is the rot case, and it is
// answered with a resolution that names no winner rather than by rolling between nobody.
func settleHighest(ctx Ctx, ranked []rankedBid) (rankedBid, *int64) {
	candidates := bidPerAccount(ranked[:tiedAtTop(ranked)])
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	seed := ctx.Rng().Seed()

	return candidates[ctx.Rng().IntN(len(candidates))], &seed
}

// auctionTrace is the tie-break chain an auction actually walked, in order, ready to be written onto
// the resolution.
//
// IT RECORDS THE STEPS THAT WERE REACHED, NOT THE STEPS THAT EXIST. A trace listing every step of
// docs/guides/auctions.md's chain with "not reached" against five of them is a form; a trace that
// stops where the item was decided is an answer. The caller appends the price step, because what the
// winner pays is the pay rule's sentence and this function does not know the pay rule.
//
// THE ONLY AMOUNT IT NAMES IS THE WINNING ONE. That number is revealed at `closing` and is what the
// winner pays; every other bid's value stays out of a resolution officers paste into chat
// (docs/guides/auctions.md). The counts are safe and are the point: "3 of the 4 bids in main stood at
// 1000" is what makes a seeded roll explicable months later without republishing anybody's number.
func auctionTrace(
	placed, eligible int, minimum core.Centipoints, phase tierOutcome, ordered []rankedBid,
	seed *int64,
) []ResolutionStep {
	trace := auctionTraceThroughAmount(placed, eligible, minimum, phase, ordered)
	if !contested(ordered) {
		return trace
	}

	return append(trace, sequenceOrRoll(ordered, seed))
}

// auctionTraceThroughAmount is the chain as far as step 2 — eligibility, the ladder, the amount — and
// it stops there because that is where the two ways an auction can end diverge.
//
// SPLIT OUT RATHER THAN DUPLICATED (#248). A sealed auction that ties on the amount does not fall to
// the bid sequence and the roll; it stops and names the tied parties, and the three steps it walked
// to get there are the same three steps every other settlement walked. Two copies of them would be
// two wordings of the eligibility count, drifting apart the first time one was improved.
func auctionTraceThroughAmount(
	placed, eligible int, minimum core.Centipoints, phase tierOutcome, ordered []rankedBid,
) []ResolutionStep {
	trace := []ResolutionStep{
		{
			Kind: ResolutionStepEligibility,
			Detail: fmt.Sprintf("%d of the %d bids placed cleared the minimum of %d centipoints",
				eligible, placed, minimum),
		},
		phase.step(),
	}

	// tiedOnRank rather than a count over the amounts: an auction ranks BY the amount, so the two are
	// the same number here, and the one that is still the same number for a rule that does not is the
	// one worth calling (#244).
	atAmount := tiedOnRank(ordered)
	if atAmount == 1 {
		return append(trace, ResolutionStep{
			Kind: ResolutionStepAmount,
			Detail: fmt.Sprintf(
				"the highest bid in tier %s is %d centipoints and no other bid there matches it",
				phase.tier, ordered[0].bid.AmountCp),
		})
	}

	return append(trace, ResolutionStep{
		Kind: ResolutionStepAmount,
		Detail: fmt.Sprintf("%d of the %d bids in tier %s stand at %d centipoints, the highest there",
			atAmount, len(ordered), phase.tier, ordered[0].bid.AmountCp),
	})
}

// sequenceOrRoll is the bottom of the tie-break chain, and it is shared because by the time a rule
// reaches it every rule is asking the same question: with everything above tied, the EARLIEST bid
// takes the item, and when the microsecond ties too a seeded roll does.
//
// THE SEED IS IN THE SENTENCE. A roll an officer cannot re-run is the one thing a loot dispute cannot
// be settled from (.claude/rules/ledger-and-strategy.md), and the trace is what they read three
// months later — so the number that reproduces it goes in the line rather than only in the field
// beside it.
//
// The caller guarantees it was reached, and guarantees more than that: `contested` has established
// that MORE THAN ONE BIDDER holds the top of the order, so "every step above" is a claim the trace
// has already made and "the earliest of them takes the item" is a comparison that genuinely ran
// between two people rather than between one person's two bids.
func sequenceOrRoll(ordered []rankedBid, seed *int64) ResolutionStep {
	if seed == nil {
		return ResolutionStep{
			Kind: ResolutionStepBidSequence,
			Detail: fmt.Sprintf(
				"%d bids are tied on every step above; the earliest of them takes the item",
				tiedOnRank(ordered)),
		}
	}

	// BIDDERS RATHER THAN BIDS, because that is the draw settleHighest actually made: one ticket per
	// account, however many rows an account holds in the tied run. A sentence counting rows would
	// describe odds nobody was given.
	return ResolutionStep{
		Kind: ResolutionStepSeededRoll,
		Detail: fmt.Sprintf(
			"%d bidders are equal on every step above and in the microsecond they bid; a roll "+
				"from seed %d settled it, and re-running that seed settles it the same way",
			tiedBiddersAtTop(ordered), *seed),
	}
}

// rotResolution is the settlement of an item nobody legally bid on: no winner, no tier, and the one
// trace step there was anything to record.
//
// A ROT IS AN OUTCOME AND NOT A FAILURE (docs/guides/auctions.md), so it carries the same artefacts
// every other outcome does — an officer looking at a session that awarded nothing has the same
// question about it, and "12 bids placed, none of them legal" is the answer. Shared between the two
// auctions because their wording was already identical, and two copies of a sentence are two
// sentences that can stop agreeing.
func rotResolution(placed int, minimum core.Centipoints) Resolution {
	reason := fmt.Sprintf("no bid of the %d placed reached the minimum of %d centipoints",
		placed, minimum)

	return Resolution{
		Reason: reason,
		Trace:  []ResolutionStep{{Kind: ResolutionStepEligibility, Detail: reason}},
	}
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

// sessionMinimum is the floor a bid must clear: the higher of the session's own minimum and the
// pool's configured one.
//
// A SESSION MAY RAISE THE FLOOR AND MAY NOT LOWER IT, which is what both auction schemas promise
// ("a session may open with a higher minimum of its own; it may not open with a lower one") and is
// not the same rule as an officer's explicit price overriding a catalogue. The pool's minimum is a
// GUILD RULE, written on the settings page by whoever is allowed to write it; a session is one
// instance of that rule. A session that could lower it would let whoever opens a session waive the
// guild's floor for one item — silently, per drop, without the settings changing — and the resulting
// award would be perfectly consistent with itself: eligible bid, correct arithmetic, batch sums to
// zero, and a price the guild had voted not to allow.
//
// A SESSION MINIMUM OF ZERO IS "UNSET" RATHER THAN "FREE". That is the one ambiguity in the Session
// shape, and taking the maximum resolves it for free: an unset session takes the pool's floor because
// zero can never be the larger value, so there is no separate case to remember.
func sessionMinimum(s Session, configuredCp core.Centipoints) core.Centipoints {
	if s.MinAmountCp > configuredCp {
		return s.MinAmountCp
	}

	return configuredCp
}

// checkBidIdentity rejects the bid shapes no strategy has a defensible answer for: one that names no
// bidder, one whose bidder is not the account being validated, one from a system account, and one
// whose recorded tier is not on the ladder.
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

	// THE RUNG IS CHECKED WHERE THE BID IS ACCEPTED, not only where it is settled. resolveTier and
	// rankBids both refuse a tier nobody can rank — guessing it low is how a main loses an item to an
	// alt — and a session that discovered that at `closing`, with the item on the floor and the
	// bidders waiting, would be a session that should have refused the bid two hours earlier. What
	// this cannot check is whether the tier is the RIGHT one: that is derived from the bidding
	// character by whoever accepts the bid, and a planner cannot see the roster (tier.go).
	if _, err := checkTier(strategyID, bid); err != nil {
		return err
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
