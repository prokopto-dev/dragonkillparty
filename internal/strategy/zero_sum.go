package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// zero_sum — a closed economy: what the winner pays, the other raiders receive. Phase 1, #196.
//
// It is the DKP model with the strongest fairness argument and the least inflation: no points enter
// circulation at loot time and none leave it, so a guild's total is decided entirely by its earn rule.
// A raider who wins nothing still gains on every item somebody else wins, which is why the guide calls
// it the system for a guild that wants nothing to inflate
// (docs/guides/choosing-a-dkp-system.md#zero_sum--a-closed-economy).
//
// IT IS A SPEND RULE (ADR-0026). Its planner is PlanAward — it answers "how are points spent?" and the
// redistribution is what spending them DOES, not a cadence run. The slot follows the planner rather
// than the effect on a balance, which is the same rule that puts `start_points` in the over-time slot
// for granting points. A zero-sum guild composes an earn rule beside it — `tick` is the P99 default and
// the preset the first-run guide offers — and, if it wants one, an over-time rule.
//
// THE ARITHMETIC IS NOT THIS FILE'S. Every credit comes from ledger.Allocate through Ctx.Allocate: the
// shared largest-remainder allocator, whose credits sum to EXACTLY the debit and whose tiebreak is the
// account id, ascending. `.claude/rules/ledger-and-strategy.md` requires it to be shared rather than
// re-implemented per strategy, and this is the strategy the requirement was written for — 300.00 split
// seven ways is 42.857…, and rounding each credit independently mints two centipoints on every item,
// forever. A planner that divided its own credits would be wrong in a way no individual number looks
// wrong in.
//
// THE THREE DECISIONS the guide tells a guild to take before switching this on are its three knobs,
// with one deliberate omission:
//
//   - DOES THE WINNER SHARE IN THE SPLIT? `winner_share`. Excluded is the default and the guide's
//     worked example: the winner pays 300.00 and receives nothing back. Included is a real variant —
//     the winner is an attendee like any other and nets 300.00 × (n−1)/n — and it is a knob rather than
//     a second strategy because the difference is one line of configuration and forcing it to be two
//     files would mean two copies of the price resolution that could then disagree.
//   - WHAT IF THE WINNER IS THE ONLY ATTENDEE? `solo_policy`. The price still leaves the buyer and
//     lands on a SYSTEM ACCOUNT — the guild bank or write_off — because a degenerate case routes to a
//     ledger-addressable account rather than to a silent drop, which is what keeps conservation
//     verifiable. `free` is the guild that says a solo kill costs nothing: no batch at all
//     (ErrNothingToPlan), rather than a batch that moves zero.
//   - WHO IS IN THE SPLIT — attendees at the moment of the award, everyone on the raid, or
//     tick-weighted? NOT A KNOB HERE, and the omission is argued rather than forgotten. A pure planner
//     is handed the beneficiaries; it cannot see a raid, a tick or a clock, so it could not resolve
//     `all_raid_attendees` if it were told to. That resolution belongs to the award ingest path (Phase
//     3), which fills AwardEvent.Beneficiaries — and tick-weighting is expressed by the WEIGHTS on
//     those shares, which this strategy already honours. A knob here that nothing read would be worse
//     than no knob: the officer sets it, the form shows it, and the arithmetic ignores it. `tick` makes
//     the identical argument about seconds-per-tick.
//
// EVERY BATCH IT WRITES SUMS TO ZERO, which is the name. The award moves the price from the buyer to
// the other raiders; the adjustment moves points between an account and a counterparty. Points are
// never minted and never destroyed, so conservation stays a column comparison (net_amount_cp = 0)
// rather than an aggregate over the whole ledger.
//
// THE BATCH KIND IS `award`, not `zero_sum_credit`. That kind exists for a split's credit half posted
// APART from its debit — the compensating batch a retroactive edit emits when the original award is
// six months old (.claude/rules/ledger-and-strategy.md, "retroactive zero-sum edits compensate, never
// replay"). This planner writes the debit and its credits in one atomic batch, which is what makes
// SumZero checkable at all, so `award` is what it is.

// The compile-time proof that the implementation matches the interface. If PointStrategy grows a
// method, `go build` says so on the next save rather than a reviewer noticing.
var _ PointStrategy = ZeroSum{}

// ZeroSum is the redistributing spend strategy. It is STATELESS: everything it needs arrives through
// the Ctx façade, which is what lets one value serve every pool and every request concurrently.
type ZeroSum struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	zeroSumID      = "zero_sum"
	zeroSumVersion = "0.1.0"
)

// The `winner_share` knob's values: whether the buyer is one of the accounts their own payment is
// split across.
const (
	// WinnerShareExcluded — the buyer is removed from the split. They pay the whole price and receive
	// none of it back. The guide's worked example, and the default.
	WinnerShareExcluded = "excluded"

	// WinnerShareIncluded — the buyer is an attendee like any other and receives their share of their
	// own payment, netting price × (n−1)/n.
	WinnerShareIncluded = "included"
)

// SoloPolicyFree is the third `solo_policy` value, and it is this strategy's alone.
//
// It means "a kill with nobody to redistribute to costs the winner nothing": there is no batch, not a
// batch that moves zero, because ledger_entry carries CHECK (amount_cp <> 0) and ledger_batch carries
// CHECK (entry_count > 0). The caller receives ErrNothingToPlan and records the loot without a ledger
// batch.
//
// `fixed_price` does not admit it and must not: there, the price is what the ITEM costs and the buyer
// pays it whether or not anybody receives it — the solo policy only picks which system account the
// proceeds land on. Here the price exists in order to be redistributed, so "nobody to redistribute to"
// is a coherent reason for there to be no charge. Each strategy's ConfigSchema is the normative list
// of the values it accepts, and the two enumerate different sets on purpose.
const SoloPolicyFree = "free"

// zeroSumConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in one
// place, in the form that renders the pool-settings form and validates the config at the API edge.
//
// Draft 2020-12. `additionalProperties: false` is deliberate and load-bearing — a typo'd knob
// (`winner_shares`) must be a validation error at the edge and not a silently ignored key that leaves
// the pool running the default, which is how a guild discovers three months later that the winner has
// been in every split. Every money field is an INTEGER named `_cp`: canonical §1 bans a decimal on the
// wire, and a schema that said `number` would invite one.
const zeroSumConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Zero sum",
  "description": "What the winner pays is redistributed to the other raiders by largest remainder, so the pool's total never changes.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "default_price_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "Default item price (centipoints)",
      "description": "Used when the item carries no catalogue price and the officer names none. 0 means every item must be priced explicitly."
    },
    "winner_share": {
      "type": "string",
      "enum": ["excluded", "included"],
      "default": "excluded",
      "title": "Does the winner share in the split?",
      "description": "excluded means the winner pays the whole price and receives none of it back. included means they are an attendee like any other and receive their share of their own payment."
    },
    "solo_policy": {
      "type": "string",
      "enum": ["guild_bank", "write_off", "free"],
      "default": "guild_bank",
      "title": "What if there is nobody to split across?",
      "description": "A solo kill has no other attendees. guild_bank and write_off charge the winner and land the price on that system account; free means the item costs nothing and no batch is written."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An award is rejected if it would take the buyer below this. Negative permits going into debt to a limit."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes.
//
// A COPY, not the backing array of the constant: a caller that could mutate the schema could change
// what every pool validates against. The constant is a string precisely so this conversion allocates
// a fresh slice each call.
func (ZeroSum) ConfigSchema() []byte { return []byte(zeroSumConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (ZeroSum) ID() string { return zeroSumID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (ZeroSum) Version() string { return zeroSumVersion }

// RuleKind is spend: this strategy answers "how are points spent?", and redistributing the price is
// what spending them does here (ADR-0026).
//
// NOT over time, which is where the guide's catalogue table listed it and where a reader of the third
// question ("nothing, decay, a cap, or redistribution") would expect it. The slot follows the PLANNER:
// this rule is reached by PlanAward at the moment an item is won, not by a cadence run keyed
// (pool_id, kind, cadence_period) like every member of the decay family. A pool that put it in the
// over-time slot would be a pool that never awarded an item and never decayed one either.
func (ZeroSum) RuleKind() RuleKind { return RuleSpend }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal (see PlanReversal).
func (ZeroSum) BalanceKinds() []string { return []string{BalanceKindDKP} }

// zeroSumConfig is the parsed pool config. The JSON tags are the schema's property names and the two
// must agree; TestZeroSum_ConfigSchema_EveryKnobAgreesWithTheParser asserts that they do, because a
// knob in the schema that the parser ignores is a knob the settings form offers and nothing reads.
type zeroSumConfig struct {
	DefaultPriceCp core.Centipoints `json:"default_price_cp"`
	WinnerShare    string           `json:"winner_share"`
	SoloPolicy     string           `json:"solo_policy"`
	FloorCp        core.Centipoints `json:"floor_cp"`
}

// defaultZeroSumConfig is the config a pool that has set nothing runs under: the guide's worked
// example, where the winner is excluded from the split and a solo kill's price lands on the guild
// bank. It is the struct the pool's JSON is decoded OVER, which is what makes an absent key mean "the
// default" and a present `"floor_cp": 0` mean "zero, chosen" — two things a zero value alone cannot
// distinguish.
func defaultZeroSumConfig() zeroSumConfig {
	return zeroSumConfig{
		DefaultPriceCp: 0,
		WinnerShare:    WinnerShareExcluded,
		SoloPolicy:     SoloPolicyGuildBank,
		FloorCp:        0,
	}
}

// config parses and validates the pool's config.
//
// It re-validates what the API edge already validated against ConfigSchema, and the duplication earns
// its keep: the edge validates what a human typed into the settings form, and this validates what
// actually reached the planner — which includes a config written by the importer, by a migration
// backfill, or by a test. The strict decode itself is decodeConfig in common.go, shared with every
// other strategy in this package.
func (ZeroSum) config(ctx Ctx) (zeroSumConfig, error) {
	cfg := defaultZeroSumConfig()

	if err := decodeConfig(zeroSumID, ctx.ConfigJSON(), &cfg); err != nil {
		return zeroSumConfig{}, err
	}

	return validateZeroSumConfig(cfg)
}

// validateZeroSumConfig applies the bounds the schema declares, to a config that has already parsed.
// Split from config so that the defaults are validated too — a default that violated its own schema
// would otherwise be the one config nothing ever checked.
func validateZeroSumConfig(cfg zeroSumConfig) (zeroSumConfig, error) {
	switch cfg.WinnerShare {
	case WinnerShareExcluded, WinnerShareIncluded:
	default:
		return zeroSumConfig{}, fmt.Errorf("%s: winner_share is %q, want %q or %q: %w",
			zeroSumID, cfg.WinnerShare, WinnerShareExcluded, WinnerShareIncluded, ErrInvalidConfig)
	}

	switch cfg.SoloPolicy {
	case SoloPolicyGuildBank, SoloPolicyWriteOff, SoloPolicyFree:
	default:
		return zeroSumConfig{}, fmt.Errorf("%s: solo_policy is %q, want %q, %q or %q: %w",
			zeroSumID, cfg.SoloPolicy, SoloPolicyGuildBank, SoloPolicyWriteOff, SoloPolicyFree,
			ErrInvalidConfig)
	}

	if cfg.DefaultPriceCp < 0 {
		return zeroSumConfig{}, fmt.Errorf("%s: default_price_cp is %d, which is negative: %w",
			zeroSumID, cfg.DefaultPriceCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAward debits the buyer the item's price and splits it across the other raiders.
//
// THE WHOLE PRICE IS SPLIT, ALWAYS. There is no path through this planner that keeps part of the price
// back: the credits come from the shared allocator, which returns a set summing to exactly the debit,
// and the degenerate cases route to a system account rather than dropping the difference. That is what
// makes SumZero and LargestRemainderSumsToDebit both true of every batch it writes.
//
// The price resolves in one order and only one: the officer's explicit price, then the item's
// catalogue price, then the pool's default. Each step is a deliberate override of the one below it,
// and a resolved price of zero or less is refused rather than written as an award of nothing. It is
// resolvePrice in common.go, shared with `fixed_price` — a second copy of the resolution rules is
// exactly the drift tick.go's header argues against, and the two spend rules would eventually disagree
// about what an unpriced item costs.
//
// THE BATCH ITSELF IS spendAward's, shared with `fixed_price` and the four bidding rules (spend.go,
// #195): the buyer's debit, the largest-remainder split, the routing of a degenerate case to a system
// account, and the invariant set are identical in every spend rule, and what differs between them is
// how the price is decided. WHAT THIS STRATEGY ADDS is the one thing spendAward deliberately does not
// do — remove the winner from the list their own payment is split across — which it does by handing
// spendAward an event whose beneficiaries are already filtered. spendAward's comment ("the buyer is
// not excluded from the split") states fixed_price's rule; excluding them is a pool CONFIGURATION
// here, and `winner_share: included` is the same behaviour spendAward describes.
func (s ZeroSum) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	price, err := resolvePrice(zeroSumID, cfg.DefaultPriceCp, ev)
	if err != nil {
		return BatchProposal{}, err
	}

	beneficiaries, err := s.beneficiaries(cfg, ev)
	if err != nil {
		return BatchProposal{}, err
	}

	// The solo case, taken before anything is routed, because `free` has no account to route to and no
	// batch to write. Every other policy names a system account and falls through to the split.
	if len(beneficiaries) == 0 && cfg.SoloPolicy == SoloPolicyFree {
		return BatchProposal{}, fmt.Errorf(
			"%s: account %s is the only attendee and solo_policy is %q, so the item costs nothing and "+
				"there is no batch to write: %w", zeroSumID, ev.Buyer.ID, SoloPolicyFree, ErrNothingToPlan)
	}

	// The account a solo kill's proceeds land on. With `free` it is unreachable — the branch above has
	// already returned for the only case that consults it — so the guild bank stands in rather than a
	// value routeProceeds would then fail to resolve.
	soloKey := cfg.SoloPolicy
	if soloKey == SoloPolicyFree {
		soloKey = SoloPolicyGuildBank
	}

	// Proceeds are ALWAYS the attendees here, which is what the strategy is: there is no guild-bank
	// path to configure, so `zero_sum` carries no `proceeds` knob and the split always runs. That is
	// also why LargestRemainderSumsToDebit is declared on every award it writes, where `fixed_price`
	// declares it only when its proceeds go to the attendees.
	split := ev
	split.Beneficiaries = beneficiaries

	return spendAward(ctx, zeroSumID, zeroSumVersion, spendTerms{
		Proceeds:   ProceedsAttendees,
		SoloPolicy: soloKey,
		FloorCp:    cfg.FloorCp,
	}, split, price)
}

// beneficiaries is the validated, sorted set of accounts the price is split across, with the buyer
// removed when the pool excludes them.
//
// THE EXCLUSION HAPPENS AFTER THE DUPLICATE CHECK, deliberately. A beneficiary list naming the buyer
// twice is a list that was built twice — from two sources, or from a join that fanned out — and
// dropping one of the two before noticing would hide the defect on exactly the pool configuration
// where it is least visible. checkDistinctShares names the account; this then removes it.
func (ZeroSum) beneficiaries(cfg zeroSumConfig, ev AwardEvent) ([]Share, error) {
	shares := sortedShares(ev.Beneficiaries)

	if err := checkDistinctShares(zeroSumID, shares); err != nil {
		return nil, err
	}

	for _, b := range shares {
		if err := checkShare(zeroSumID, b); err != nil {
			return nil, err
		}
	}

	if cfg.WinnerShare == WinnerShareIncluded {
		return shares, nil
	}

	out := make([]Share, 0, len(shares))

	for _, b := range shares {
		if b.AccountID == ev.Buyer.ID {
			continue
		}

		out = append(out, b)
	}

	return out, nil
}

// PlanAdjustment moves points between an account and a counterparty.
//
// It is two entries, never one. An officer who could add points without naming where they came from
// could inflate a guild's economy invisibly — which in a pool whose whole premise is that nothing
// inflates would be the one hole in the argument — and the counterparty, the guild bank unless the
// caller names another, is what makes every adjustment answerable with "out of what?".
//
// The body is adjustmentProposal in common.go: an adjustment is the one planner that is identical in
// every strategy, and the only thing that varies is the pool's floor. What this method still owns is
// reading THIS strategy's config, because that floor is a zero_sum knob.
//
// A COMPOSED POOL NEVER REACHES IT — Rules.PlanAdjustment routes to the EARN rule, and this is a spend
// rule (ADR-0026). It is implemented rather than refused because every strategy in this package
// implements it identically and a refusal here would be a refusal to do something this strategy can
// obviously do; a pool that names zero_sum as its only rule can still take a correction.
func (s ZeroSum) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, zeroSumID, zeroSumVersion, cfg.FloorCp, ev)
}

// PlanAttendance is unsupported, and the refusal is a statement rather than a gap.
//
// This strategy answers "how are points spent?" and nothing else. It has no tick value and no view of
// what a night of raiding is worth, so there is no defensible number to credit an attendee. A zero-sum
// pool earns through an earn rule — `tick` is the P99 default and the preset the first-run guide
// offers — and the pool holds both (ADR-0026).
func (ZeroSum) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(zeroSumID,
		"credit an attendance tick: it is a spend rule, so pair it with an earn rule such as tick")
}

// PlanDecay is unsupported: a zero-sum pool that also wants points to expire runs a decay rule beside
// this one. Inventing a rate here would put a second decay in the tree with no cadence, no run row and
// no idempotency key — and decay is posted, not improvised (.claude/rules/decay-and-jobs.md).
func (ZeroSum) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(zeroSumID,
		"decay balances: it is a spend rule, so pair it with decay_percent or decay_window")
}

// PlanReversal negates every entry of the batch being reversed — the debit and all of its credits,
// together, in one batch.
//
// ENTRY-WISE NEGATION IS CORRECT HERE, and it is not correct everywhere. This strategy's only balance
// kind is `dkp`, a plain quantity: subtracting what was added restores every balance exactly, whatever
// happened in between. A strategy whose kind is positional (suicide_kings' sk_position) or paired
// (epgp's ep/gp) must override this and say so.
//
// WHAT IT DOES NOT DO IS REPLAY. Reversing a six-month-old zero-sum award means every intermediate
// balance was arithmetically "wrong" under the new history, and no reversal can fix that without
// rewriting an append-only table. The rule is one corrective batch at today's seq
// (.claude/rules/ledger-and-strategy.md, "retroactive zero-sum edits compensate, never replay"), which
// is exactly what this returns: the negation, restamped to the present.
//
// The body is reversePlan in common.go, which carries at length why a reversal declares no floor and
// reads no current config. Both failures are unfixable once made — a floor on a reversal prevents the
// CORRECTION rather than the debt, and reading today's config makes every batch in a pool's history
// unreversible the moment a guild changes a rule.
func (ZeroSum) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, zeroSumID, b)
}

// Spendable is the account's balance at the pool head.
//
// NO COMPUTED WEIGHTING AND NO PENDING SPLIT. A member whose share of tonight's awards has not been
// committed yet cannot spend it: a balance is a SUM over committed entries and nothing else
// (.claude/rules/ledger-and-strategy.md). Active bid holds are subtracted in Phase 3, when holds exist.
func (ZeroSum) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, zeroSumID, acct)
}

// Priority ranks candidates for an item by spendable balance, highest first, tie-broken on the account
// id, ascending.
//
// A zero-sum pool has no bidding of its own, so when two raiders want the same drop at the same price
// something has to decide. Balance is the fairest available answer — it is the accumulated cost of
// turning up, minus what has already been spent — and the tiebreak is deterministic and therefore
// replayable. A random tiebreak here would make two replays of the same loot decision differ, which is
// the defect the allocator's account_id tiebreak exists to prevent.
func (ZeroSum) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, zeroSumID, acct)
}

// PriceHint is unsupported, and the refusal is a statement rather than a gap.
//
// A price hint answers "what should I bid?". This strategy has no bidding: an item's price is a number
// the officer or the catalogue already fixed, and PlanAward resolves it. Returning one here would give
// a bidding UI a value to draw a bid box around for a pool that has no bid box, which is worse than a
// 501 saying the concept does not apply.
func (ZeroSum) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(zeroSumID, "hint at a price: items are priced, not bid on")
}

// ValidateBid is unsupported: there are no bids to validate.
func (ZeroSum) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(zeroSumID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (ZeroSum) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(zeroSumID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. TestZeroSum_EveryPlannerInvariant_IsDeclared compares the two by kind and balance kind for
// exactly that reason.
func (ZeroSum) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
