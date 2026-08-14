package strategy

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// loot_council — loot by officer decision. Phase 1, #197.
//
// A council of officers looks at who is standing there, decides who gets the drop, and writes down
// why. There is no bid, no price list and no queue: "loot_council has no arithmetic"
// (docs/guides/choosing-a-dkp-system.md), and this file is what that sentence looks like once it has
// to produce a batch an append-only ledger will accept.
//
// THE CHARGE IS RECORDED, NEVER COMPUTED. Every other spend rule in the catalogue DERIVES the number
// it debits — from a price table, from a bid, from a percentage of a balance. This one is handed a
// number by the people who made the decision, and its only job is to write that number down
// faithfully. So the resolution order is the council's own amount and then the pool's default, and
// the ITEM'S CATALOGUE PRICE IS DELIBERATELY NOT CONSULTED: a council that read a price table would
// be `fixed_price` with extra steps, and a guild that wants the table wants that strategy. The
// planner reads no balance at all, which is asserted rather than assumed
// (TestLootCouncil_PlanAward_ReadsNoBalance) — the ledger's NonNegative check is what refuses an
// overdraft, at commit time, where the balance is a fact rather than a plan input.
//
// A COUNCIL THAT CHARGES NOTHING IS A REAL COUNCIL, and it is the common one on P99: the pool still
// tracks attendance so the room can see who has turned up, and loot is decided by people rather than
// by points. `charge_cp` therefore defaults to 0, and an award at 0 returns ErrNothingToPlan rather
// than an empty batch — ledger_entry carries CHECK (amount_cp <> 0) and BatchNonEmpty is unwaivable,
// so there is no legal batch to write. That is not a failure: the item award is a fact recorded in
// the loot tables, and a decision that moved no points has nothing to say to the ledger. It is
// deliberately NOT an ErrInvalidConfig the way `cap` refuses an uncapped pool — an uncapped `cap`
// pool caps nothing and is a misconfiguration, while an uncharged council is the model working.
//
// THE RATIONALE IS PART OF THE DECISION, which is why `require_reason` defaults to true and refuses a
// decision that carries none. The guide is blunt about the failure mode — "councils without a
// published priority score reliably decay into loot-council fatigue" — and a council whose decisions
// arrive unexplained is the same disease one step earlier. Every other strategy leaves the reason to
// the API edge because the arithmetic speaks for itself; here the arithmetic says nothing at all, so
// the sentence an officer wrote IS the record. A guild that runs its council somewhere else — a
// Discord thread, a forum post — turns the knob off and says so.
//
// WHAT THIS FILE DOES NOT RECORD, and where it will go. Nominations, each councillor's vote, and the
// conflict-of-interest flag when a councillor is a candidate for the item under vote are all part of
// the guide's description and none of them is expressible here: they are facts about a deliberation,
// they belong in the loot tables beside `item_award.award_type = 'loot_council'`, and a planner may
// only propose entries. Putting them in metadata_json instead would be a fact worth filtering on
// hidden in a column nothing may query into (.claude/rules/store-and-sql.md). Tracked as #219; what
// ships here is the money half, which is the half the ledger owns.
//
// EVERY BATCH IT WRITES SUMS TO ZERO. The charge lands on the guild bank, so points leave circulation
// rather than being destroyed and conservation stays a single-column comparison. There is no
// redistribute-to-attendees knob: that is `fixed_price`'s `proceeds`, and a second copy of it here
// would be a rule that could disagree with the first.

// The compile-time proof that the implementation matches the interface. If PointStrategy grows a
// method, `go build` says so on the next save rather than a reviewer noticing.
var _ PointStrategy = LootCouncil{}

// LootCouncil is the officer-decision strategy. It is STATELESS: everything it needs arrives through
// the Ctx façade, which is what lets one value serve every pool and every request concurrently.
type LootCouncil struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same decision would now produce a different
// proposal, never for a comment.
const (
	lootCouncilID      = "loot_council"
	lootCouncilVersion = "0.1.0"
)

// lootCouncilConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in one
// place, in the form that renders the pool-settings form and validates the config at the API edge.
//
// Draft 2020-12, `additionalProperties: false`, integers only for money. There is NO DEFAULT CHARGE:
// what a council decides an item costs is the guild's business and there is no number a stranger
// could pick for them, so 0 means "the council awards without charging" and is a supported way to run
// this rule rather than a value nobody chose.
const lootCouncilConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Loot council",
  "description": "Councillors decide who receives an item. There is no bidding: the charge, if any, is the one the council names.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "charge_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "Default charge for a council award (centipoints)",
      "description": "What a council award costs its winner when the council names no amount. 0 means the council awards without charging, and such an award writes no ledger batch at all."
    },
    "require_reason": {
      "type": "boolean",
      "default": true,
      "title": "Require a written rationale",
      "description": "Refuse a council award that records no reason. A council's rationale is the whole audit trail, because there is no arithmetic to inspect afterwards."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "A council award is rejected if it would take the winner below this. Negative permits going into debt to a limit."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes.
//
// A COPY, not the backing array of the constant: a caller that could mutate the schema could change
// what every pool validates against. The constant is a string precisely so this conversion allocates
// a fresh slice each call.
func (LootCouncil) ConfigSchema() []byte { return []byte(lootCouncilConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (LootCouncil) ID() string { return lootCouncilID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (LootCouncil) Version() string { return lootCouncilVersion }

// RuleKind is spend: this strategy answers "how are points spent?", and it answers it by writing down
// what people decided rather than by computing anything (ADR-0026).
//
// A pool composes it with an earn rule that credits attendance and, usually, an over-time rule that
// keeps the standings honest — the council is the loot half and nothing else. That is why the three
// planners outside this slot refuse by name below instead of improvising an answer.
func (LootCouncil) RuleKind() RuleKind { return RuleSpend }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal (see PlanReversal).
func (LootCouncil) BalanceKinds() []string { return []string{BalanceKindDKP} }

// lootCouncilConfig is the parsed pool config. The JSON tags are the schema's property names and the
// two must agree; TestLootCouncil_ConfigSchema_EveryKnobAgreesWithTheParser asserts that they do,
// because a knob in the schema that the parser ignores is a knob the settings form offers and nothing
// reads.
type lootCouncilConfig struct {
	ChargeCp      core.Centipoints `json:"charge_cp"`
	RequireReason bool             `json:"require_reason"`
	FloorCp       core.Centipoints `json:"floor_cp"`
}

// defaultLootCouncilConfig is the config a pool that has set nothing runs under: a council that
// charges nothing and writes down why.
//
// It is the struct the pool's JSON is decoded OVER, which is what makes an absent key mean "the
// default" and a present `"require_reason": false` mean "off, chosen" — two things a zero value alone
// cannot distinguish.
func defaultLootCouncilConfig() lootCouncilConfig {
	return lootCouncilConfig{ChargeCp: 0, RequireReason: true, FloorCp: 0}
}

// config parses and validates the pool's config.
//
// It re-validates what the API edge already validated against ConfigSchema, and the duplication earns
// its keep: the edge validates what a human typed into the settings form, and this validates what
// actually reached the planner — which includes a config written by the importer, by a migration
// backfill, or by a test. The strict decode itself is decodeConfig in common.go, shared with every
// other strategy in this package.
func (LootCouncil) config(ctx Ctx) (lootCouncilConfig, error) {
	cfg := defaultLootCouncilConfig()

	if err := decodeConfig(lootCouncilID, ctx.ConfigJSON(), &cfg); err != nil {
		return lootCouncilConfig{}, err
	}

	return validateLootCouncilConfig(cfg)
}

// validateLootCouncilConfig applies the bounds the schema declares, to a config that has already
// parsed. Split from config so that the defaults are validated too — a default that violated its own
// schema would otherwise be the one config nothing ever checked.
func validateLootCouncilConfig(cfg lootCouncilConfig) (lootCouncilConfig, error) {
	// A negative default charge is a council that PAYS the winner for taking the item. That may even
	// be what a guild wants for a rotting drop nobody will carry, but it is an adjustment with a
	// reason attached, not an award, and writing it as an award would leave a member's statement
	// saying they were charged a negative amount for loot.
	if cfg.ChargeCp < 0 {
		return lootCouncilConfig{}, fmt.Errorf(
			"%s: charge_cp is %d; a council award that pays its winner is an adjustment, not an "+
				"award: %w", lootCouncilID, cfg.ChargeCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAward debits the winner the charge the council named and credits it to the guild bank.
//
// THE ORDER OF THE CHECKS IS THE ORDER A COUNCIL DECIDES IN, and it is deliberate: who won and why
// are settled before what it costs, so a decision with no rationale is refused even when it would
// have charged nothing. The alternative — checking the charge first — makes a missing rationale
// invisible on exactly the awards a no-charge council makes, which is all of them.
//
// THE WINNER IS NEVER A SYSTEM ACCOUNT. The four ledger-addressable non-human accounts are
// counterparties; a council cannot award an item to the guild bank, and an event that says so is a
// caller that assembled the wrong id rather than a guild with an unusual rule.
//
// BENEFICIARIES ARE IGNORED, and the silence is on purpose. The field exists for the zero-sum split
// `fixed_price` performs, a council has no split to perform, and a Phase 3 loot flow that fills the
// event generically for every strategy must not have its award refused by the one that has no use for
// it. Where the charge goes is not configurable here for the same reason: redistribution is
// `fixed_price`'s `proceeds` knob and a second copy of that rule could disagree with the first.
func (s LootCouncil) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if ev.Buyer.ID == "" {
		return BatchProposal{}, fmt.Errorf("%s: council award names no winner: %w",
			lootCouncilID, ErrInvalidEvent)
	}

	if ev.Buyer.IsSystem() {
		return BatchProposal{}, fmt.Errorf(
			"%s: winner %s is a system account; the four system accounts are counterparties, never "+
				"the recipients of a council's decision: %w", lootCouncilID, ev.Buyer.ID, ErrInvalidEvent)
	}

	if cfg.RequireReason && strings.TrimSpace(ev.Reason) == "" {
		return BatchProposal{}, fmt.Errorf(
			"%s: the council recorded no rationale for awarding %q to %s, and this pool sets "+
				"require_reason; a council decision with no reason has no audit trail at all, because "+
				"there is no arithmetic to inspect afterwards: %w",
			lootCouncilID, ev.Item.Name, ev.Buyer.ID, ErrInvalidEvent)
	}

	charge, err := resolveCouncilCharge(cfg, ev)
	if err != nil {
		return BatchProposal{}, err
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", lootCouncilID, err)
	}

	itemID := optionalULID(ev.Item.ID)

	// The winner's debit leads: they are the subject of the decision, and the statement view renders
	// the entries in the order they were planned. The character is attribution and rides only on the
	// winner's entry — the bank did not loot anything.
	entries := []EntryProposal{
		{
			AccountID:   ev.Buyer.ID,
			CharacterID: ev.CharacterID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    -charge,
			ItemID:      itemID,
			ItemAwardID: ev.ItemAwardID,
			RaidID:      ev.RaidID,
		},
		{
			AccountID:   bank,
			BalanceKind: BalanceKindDKP,
			AmountCp:    charge,
			ItemID:      itemID,
			ItemAwardID: ev.ItemAwardID,
			RaidID:      ev.RaidID,
		},
	}

	// NonNegative is declared HERE, on the spend, which is where a floor belongs: it refuses an
	// overdraft before anything is written, against a balance the commit-time engine reads for itself.
	// LargestRemainderSumsToDebit is deliberately absent — there is no allocation to be exact about,
	// and claiming a rule about a split that never ran would make the declared set a story rather than
	// a constraint.
	return proposeZeroSum(ctx, lootCouncilID, lootCouncilVersion, kinds.KindAward,
		ev.EffectiveAt, ev.Reason, entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
			{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
		})
}

// resolveCouncilCharge applies the two-step resolution and reports the no-charge decision as
// ErrNothingToPlan.
//
// TWO STEPS, NOT THREE. `fixed_price` resolves officer, then catalogue, then config; this one skips
// the catalogue entirely, because the catalogue price is the published table a council does not use.
// An item that carries one is awarded at the council's number anyway, and the price on the item stays
// meaningful for the pools whose spend rule is the one that reads it.
//
// A ZERO CHARGE IS NOT AN ERROR IN THE EVENT, it is a decision that moves no points: ErrNothingToPlan
// says the event was legal and produced no entries, and a caller that receives it records the item
// award and writes no batch (common.go). A negative charge is a different thing and is refused — see
// validateLootCouncilConfig for the argument, which holds identically for a per-award amount.
func resolveCouncilCharge(cfg lootCouncilConfig, ev AwardEvent) (core.Centipoints, error) {
	charge := cfg.ChargeCp
	if ev.PriceCp != nil {
		charge = *ev.PriceCp
	}

	if charge < 0 {
		return 0, fmt.Errorf(
			"%s: the council charged %d centipoints for %q; an award that pays its winner is an "+
				"adjustment, not an award: %w",
			lootCouncilID, charge, ev.Item.Name, ErrInvalidEvent)
	}

	if charge == 0 {
		return 0, fmt.Errorf(
			"%s: the council awarded %q to %s at no charge, so %w; record the item award and write no "+
				"batch — an entry of 0 is illegal (CHECK (amount_cp <> 0)) and an empty batch is "+
				"refused by BatchNonEmpty",
			lootCouncilID, ev.Item.Name, ev.Buyer.ID, ErrNothingToPlan)
	}

	return charge, nil
}

// PlanReversal negates every entry of the batch being reversed.
//
// ENTRY-WISE NEGATION IS CORRECT HERE. This strategy's only balance kind is `dkp`, a plain quantity:
// giving back exactly what was taken restores the balance, whatever happened in between. A strategy
// whose kind is positional (suicide_kings' sk_position) or paired (epgp's ep/gp) must override this
// and say so.
//
// REVERSING A COUNCIL CHARGE DOES NOT UN-DECIDE THE AWARD. The ledger holds the money and only the
// money: the reversal says the guild should not have charged for that drop, and the decision itself —
// who received it, and why — is a fact in the loot tables that stays exactly as it was. An officer
// taking the item back is a separate act with its own record.
//
// The body is reversePlan in common.go, which carries at length the two arguments a reversal must not
// get wrong: it declares no floor (a floor on a reversal does not prevent a debt, it prevents the
// CORRECTION), and it does not read the pool's current config (or every batch in a pool's history
// would become unreversible the moment a guild changed a rule).
func (LootCouncil) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, lootCouncilID, b)
}

// PlanAttendance is unsupported: a council decides loot, it does not credit a tick.
//
// ADR-0026 routes PlanAttendance to a pool's EARN rule, so no pool ever asks this — and answering
// anyway would mean inventing a per-tick value beside `tick`'s, which is the second copy of a rule
// that this composition exists to prevent.
func (LootCouncil) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(lootCouncilID,
		"credit an attendance tick: it decides loot, so pair it with an earn rule such as tick")
}

// PlanAdjustment moves points between an account and a counterparty.
//
// A COMPOSED POOL NEVER REACHES IT — ADR-0026 routes adjustments to the pool's EARN rule, and pool.go
// argues why at length: the floor a member's balance lives under between purchases is the one they
// earn against. An earlier cut of this file refused it for that reason, and the refusal was
// overruled: every strategy in this package implements the adjustment through one shared helper, and
// a strategy declining to do something it can obviously do is a worse answer than a method no
// composition happens to call. A pool that names `loot_council` as its only rule can still take a
// correction.
//
// The body is adjustmentProposal in common.go — two entries and never one, because an officer who
// could add points without naming where they came from could inflate a guild's economy invisibly.
// What this method owns is reading THIS strategy's config, because the floor it declares is a
// loot_council knob.
func (s LootCouncil) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, lootCouncilID, lootCouncilVersion, cfg.FloorCp, ev)
}

// PlanDecay is unsupported: a council has no cadence. A pool that wants balances to shrink over time
// puts a decay rule in its over-time slot beside this one.
func (LootCouncil) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(lootCouncilID,
		"post a cadence run: it decides loot, so pair it with an over-time rule such as decay_percent")
}

// Spendable is the account's balance at the pool head.
//
// A COUNCIL POOL STILL HAS BALANCES, and this is still the honest answer even when the council never
// charges: the number is a SUM over committed entries, it is what the standings page shows, and a
// spend rule is the slot that answers it (ADR-0026). What it must never become is a rank — see
// Priority.
func (LootCouncil) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, lootCouncilID, acct)
}

// Priority is unsupported, and the refusal is the strategy's whole point rather than a gap.
//
// THE COUNCIL IS THE RANKING. Every other spend rule can say where an account stands before the
// decision is made, because the decision is a computation; here it is a conversation between
// officers, and a rank returned from this method would be a number the council did not use, rendered
// beside a decision it did not inform. A 501 tells the SPA there is no priority column for this pool,
// which is exactly what a council pool looks like.
//
// A COUNCIL SHOULD STILL PUBLISH A SCORE, and the guide is emphatic — "run one next to
// attendance_weighted and show the score", because councils without a visible score decay into
// loot-council fatigue. That score comes from the rule that computes it, beside this one; inventing a
// balance-shaped one here would be this strategy claiming an opinion it does not have.
func (LootCouncil) Priority(Ctx, AccountRef) (Priority, error) {
	return Priority{}, Unsupported(lootCouncilID,
		"rank an account for loot: the council decides, so publish a score with a ranking rule instead")
}

// PriceHint is unsupported: a council names a charge when it decides, and there is nothing to
// estimate beforehand. Returning a number would give a bidding UI something to render a bid box
// around for a pool that has no bidding.
func (LootCouncil) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(lootCouncilID, "hint at a price: the council names the charge, if any")
}

// ValidateBid is unsupported: there are no bids to validate.
func (LootCouncil) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(lootCouncilID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (LootCouncil) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(lootCouncilID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. TestLootCouncil_EveryPlannerInvariant_IsDeclared compares the two by kind and balance kind
// for exactly that reason.
func (LootCouncil) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
