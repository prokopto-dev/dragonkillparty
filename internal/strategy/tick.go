package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// tick — the P99 default, and the only earn rule most guilds will ever run. Phase 1, #193.
//
// Every attendance snapshot credits a fixed value to everyone present, scaled by what that person
// was doing on the night. A raider who stood on the bench for half the raid earns a half share; the
// officer who was there from form-up earns a full one; a standby who never zoned in earns whatever
// the guild decided a standby is worth. That last number is the single most argued-about setting in
// P99 raiding, and `role_multipliers` is where a guild writes it down once instead of re-litigating
// it every Tuesday (docs/guides/choosing-a-dkp-system.md).
//
// WEIGHT IS TICKS, NOT A RATIO. The guide's worked example is a four-hour raid on twenty-minute
// ticks: Tankguy 12 of 12, Healbot 9 of 12 (arrived late), Druidgal 8 of 12 (left early). One
// AttendanceEvent with weights 12, 9 and 8 at a tick award of 10.00 points credits 120.00, 90.00 and
// 80.00 — integer arithmetic throughout, with no ratio anywhere. A guild that posts one batch per
// tick instead passes weight 1 every time and gets the same total; the domain model's
// raid_tick_credit is one row per (tick, pool) and one ledger batch each, so that is the shape the
// ingest path will actually use, and this planner is indifferent between them.
//
// WHAT IS DELIBERATELY NOT A CONFIG KNOB, because the guide lists them and this file does not:
//
//   - SECONDS PER TICK and the GRACE PERIOD for zoning in mid-tick decide WHEN a raid_tick row is
//     created and who is on it. They are ingest rules, and a pure planner cannot see them — it is
//     handed the attendees, not a clock and a log. They belong to the raid/tick ingest path (Phase
//     3). A knob here that nothing read would be worse than no knob: the officer sets it, the form
//     shows it, and the arithmetic ignores it.
//   - THE KILL-TICK VALUE and the FIRST-TICK BONUS are per-EVENT values, and the schema already has
//     the columns: event_type.default_tick_value_cp, pool_event_type.value_override_cp, and
//     raid_tick_credit.value_cp, which is the number the fan-out writes
//     (docs/design/01-domain-model.md §8.1). They arrive here as AttendanceEvent.AmountCp. Putting
//     them in the pool's strategy config too would be a second place for the same number, and two
//     places for one number is a disagreement waiting for a raid night.
//
// IT IS AN EARN RULE AND IT DOES NOT SPEND. PlanAward and PlanDecay return ErrUnsupported naming
// this strategy. A tick pool spends through a spend rule (fixed_price, an auction) and expires points
// through an over-time rule, and the pool holds all three: `earn_strategy_id`, `spend_strategy_id`
// and `over_time_strategy_id`, each with its own config, routed by strategy.Rules (ADR-0026, #213).
// Answering PlanAward by inventing a price would be a second copy of fixed_price's price resolution
// under another name, and the two copies would disagree.
//
// EVERY BATCH IT WRITES SUMS TO ZERO. The credits are debited from the guild bank rather than minted,
// so a guild's bank balance reads as "everything ever awarded, minus everything ever spent back into
// it" — a number an officer can sanity-check — and conservation stays a column comparison
// (net_amount_cp = 0) rather than an aggregate over the whole ledger.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = Tick{}

// Tick is the per-attendance-tick earn strategy. STATELESS: everything it needs arrives through the
// Ctx façade, which is what lets one value serve every pool and every request concurrently.
type Tick struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	tickID      = "tick"
	tickVersion = "0.1.0"
)

// maxRoleMultiplierBp is the ceiling on a role multiplier: 1000%, ten full shares.
//
// A BOUND RATHER THAN NO BOUND, because the failure it catches is a typo. 100000 is ten times a full
// share and is already absurd for a raid role; a config that asks for more is an officer who added a
// zero, and the difference between "the tanks earn double" and "the tanks earn twenty times" is one
// keystroke that nobody would notice until the standings page. It is deliberately ABOVE 10000 —
// a guild that genuinely pays its tanks a share and a half can say so — because a ceiling at exactly
// 100% would make the knob a discount rather than a multiplier.
const maxRoleMultiplierBp = 100_000

// tickConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in one
// place, in the form that renders the pool-settings form and validates the config at the API edge.
//
// Draft 2020-12. `additionalProperties: false` is deliberate and load-bearing at BOTH levels — a
// typo'd knob must be a validation error at the edge and not a silently ignored key that leaves the
// pool running the default. Every money field is an INTEGER named `_cp` and every ratio an INTEGER
// named `_bp`: canonical §1 bans a decimal on the wire, and a schema that said `number` would invite
// one.
const tickConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Tick",
  "description": "Every attendance snapshot credits a fixed value to everyone present, scaled by the role they raided as.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "tick_award_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Points per raid tick (centipoints)",
      "description": "What one attendance tick is worth at a full share. 100 centipoints is 1.00 point. A tick that is worth something else — a kill tick, a first-tick bonus — carries its own value on the event."
    },
    "default_multiplier_bp": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100000,
      "default": 10000,
      "title": "Multiplier for an unlisted role (basis points)",
      "description": "10000 is a full share. Applied to an attendee whose role is not named below, and to one with no role at all."
    },
    "role_multipliers": {
      "type": "array",
      "default": [],
      "title": "Per-role multipliers",
      "description": "What each role earns, as a share of the tick award. A standby at 5000 earns half. Roles are the raid attendance statuses: present, standby, bench, pilot, excused, late, left_early.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["role", "multiplier_bp"],
        "properties": {
          "role": {
            "type": "string",
            "minLength": 1,
            "title": "Role"
          },
          "multiplier_bp": {
            "type": "integer",
            "minimum": 0,
            "maximum": 100000,
            "title": "Multiplier (basis points)"
          }
        }
      }
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An adjustment is rejected if it would take an account below this. Negative permits going into debt to a limit."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes.
//
// A COPY, not the backing array of the constant: a caller that could mutate the schema could change
// what every pool validates against. The constant is a string precisely so this conversion allocates
// a fresh slice each call.
func (Tick) ConfigSchema() []byte { return []byte(tickConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (Tick) ID() string { return tickID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (Tick) Version() string { return tickVersion }

// RuleKind is earn: this strategy answers "how are points earned?" and nothing else. It is the
// slot's canonical occupant — the P99 default, and the rule the file header says pairs with a spend
// rule and a decay rule rather than answering for them (ADR-0026).
func (Tick) RuleKind() RuleKind { return RuleEarn }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal.
func (Tick) BalanceKinds() []string { return []string{BalanceKindDKP} }

// roleMultiplier is one role's share of a tick, in basis points.
//
// MultiplierBp is a POINTER, and that is the one thing about this struct worth arguing. A JSON null
// decoded into a non-pointer int64 is a documented no-op, so `{"role":"standby","multiplier_bp":null}`
// would read back as 0 — "a standby earns nothing", which is a real and very different rule from the
// one nobody typed. The pointer makes absent and null the same thing, and validation rejects both by
// name. decodeConfig catches the equivalent at the top level; this is the nested case.
type roleMultiplier struct {
	Role         string `json:"role"`
	MultiplierBp *int64 `json:"multiplier_bp"`
}

// tickConfig is the parsed pool config. The JSON tags are the schema's property names and the two
// must agree; TestTick_ConfigSchema_DeclaresExactlyTheParsedKnobs asserts that they do, because a
// knob in the schema that the parser ignores is a knob the settings form offers and nothing reads.
type tickConfig struct {
	TickAwardCp         core.Centipoints `json:"tick_award_cp"`
	DefaultMultiplierBp int64            `json:"default_multiplier_bp"`
	RoleMultipliers     []roleMultiplier `json:"role_multipliers"`
	FloorCp             core.Centipoints `json:"floor_cp"`
}

// defaultTickConfig is the config a pool that has set nothing runs under: a flat tick at a full share
// for everybody. It is the struct the pool's JSON is decoded OVER, which is what makes an absent key
// mean "the default" and a present `"floor_cp": 0` mean "zero, chosen".
//
// RoleMultipliers is an EMPTY SLICE rather than nil so that the shipped default marshals as `[]` and
// matches the schema's declared default. A nil slice marshals as `null`, which is the one value the
// schema does not admit — the defaults would then be a config this strategy would refuse to parse.
func defaultTickConfig() tickConfig {
	return tickConfig{
		TickAwardCp:         100,
		DefaultMultiplierBp: basisPointsWhole,
		RoleMultipliers:     []roleMultiplier{},
		FloorCp:             0,
	}
}

// config parses and validates the pool's config.
//
// It re-validates what the API edge already validated against ConfigSchema, and the duplication earns
// its keep: the edge validates what a human typed into the settings form, and this validates what
// actually reached the planner — which includes a config written by the importer, by a migration
// backfill, or by a test.
func (Tick) config(ctx Ctx) (tickConfig, error) {
	cfg := defaultTickConfig()

	if err := decodeConfig(tickID, ctx.ConfigJSON(), &cfg); err != nil {
		return tickConfig{}, err
	}

	return validateTickConfig(cfg)
}

// validateTickConfig applies the bounds the schema declares, to a config that has already parsed.
// Split from config so that the defaults are validated too — a default that violated its own schema
// would otherwise be the one config nothing ever checked.
func validateTickConfig(cfg tickConfig) (tickConfig, error) {
	if cfg.TickAwardCp <= 0 {
		return tickConfig{}, fmt.Errorf("%s: tick_award_cp is %d, which awards nothing: %w",
			tickID, cfg.TickAwardCp, ErrInvalidConfig)
	}

	if cfg.DefaultMultiplierBp < 0 || cfg.DefaultMultiplierBp > maxRoleMultiplierBp {
		return tickConfig{}, fmt.Errorf("%s: default_multiplier_bp is %d, want 0..%d: %w",
			tickID, cfg.DefaultMultiplierBp, maxRoleMultiplierBp, ErrInvalidConfig)
	}

	seen := make(map[string]bool, len(cfg.RoleMultipliers))

	for _, rm := range cfg.RoleMultipliers {
		if rm.Role == "" {
			return tickConfig{}, fmt.Errorf(
				"%s: a role_multipliers entry names no role; the multiplier for an unnamed role is "+
					"default_multiplier_bp: %w", tickID, ErrInvalidConfig)
		}

		// A REPEATED ROLE IS AMBIGUOUS AND IS NOT "THE LAST ONE WINS". Two rows for `standby` are an
		// officer who edited the list twice and now believes whichever they typed second, or first;
		// the lookup below would silently pick the first. Naming it costs a map and settles it.
		if seen[rm.Role] {
			return tickConfig{}, fmt.Errorf(
				"%s: role %q has two multipliers; one role earns one share: %w",
				tickID, rm.Role, ErrInvalidConfig)
		}

		seen[rm.Role] = true

		if rm.MultiplierBp == nil {
			return tickConfig{}, fmt.Errorf(
				"%s: role %q declares no multiplier_bp; omit the role to take default_multiplier_bp, "+
					"or give it one: %w", tickID, rm.Role, ErrInvalidConfig)
		}

		if *rm.MultiplierBp < 0 || *rm.MultiplierBp > maxRoleMultiplierBp {
			return tickConfig{}, fmt.Errorf("%s: role %q has multiplier_bp %d, want 0..%d: %w",
				tickID, rm.Role, *rm.MultiplierBp, maxRoleMultiplierBp, ErrInvalidConfig)
		}
	}

	return cfg, nil
}

// multiplierFor returns the basis-point share the given role earns.
//
// A LINEAR SCAN over the configured list rather than a map built at parse time, and the reason is
// determinism rather than performance: a role list is a handful of entries, and a map is a shape
// whose iteration order varies between runs. Nothing here iterates — the lookup is by key — but the
// package's rule is that a planner holds no structure whose order it could accidentally depend on,
// and a slice keeps that true by construction.
//
// AN UNLISTED ROLE TAKES THE DEFAULT, which is a full share unless the pool says otherwise. That is
// the right default for the ordinary case (most attendees have no special role at all) and it is why
// the role vocabulary is the raid_attendance.status CHECK rather than free text an officer types per
// raid: the values a planner can be handed are constrained by the database, so an unlisted role is a
// role the guild has not priced, not a typo.
func (cfg tickConfig) multiplierFor(role string) int64 {
	for _, rm := range cfg.RoleMultipliers {
		if rm.Role == role {
			return *rm.MultiplierBp
		}
	}

	return cfg.DefaultMultiplierBp
}

// PlanAttendance credits every attendee the tick award, scaled by their weight and their role's
// multiplier, debited from the guild bank.
//
// The bank is the counterparty rather than a mint, so the batch sums to zero like every other batch
// this strategy writes.
//
// THE ARITHMETIC, in order, in integers: `tick award × weight`, then `× multiplier_bp ÷ 10000`,
// FLOORED. Flooring is the same argument decay makes — rounding a ratio up credits a centipoint the
// configured rate did not ask for, on every entry, every raid — and the multiplication happens before
// the division so that a half share of an odd number is exact rather than a rounded number rounded
// again.
//
// An attendee who earns nothing is DROPPED rather than written as a zero: ledger_entry carries
// CHECK (amount_cp <> 0). Weight 0 (present, earned nothing) and a role multiplier of 0 (a bench
// that does not pay) both land there, and skipping on the PRODUCT rather than on either input keeps
// it to one rule instead of three.
func (s Tick) PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if len(ev.Attendees) == 0 {
		return BatchProposal{}, fmt.Errorf("%s: attendance tick has no attendees: %w",
			tickID, ErrInvalidEvent)
	}

	amount := cfg.TickAwardCp
	if ev.AmountCp != nil {
		amount = *ev.AmountCp
	}

	if amount <= 0 {
		return BatchProposal{}, fmt.Errorf("%s: tick award is %d centipoints, which awards nothing: %w",
			tickID, amount, ErrInvalidEvent)
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", tickID, err)
	}

	attendees := sortedShares(ev.Attendees)
	if err := checkDistinctShares(tickID, attendees); err != nil {
		return BatchProposal{}, err
	}

	credits := make([]EntryProposal, 0, len(attendees)+1)

	var total core.Centipoints

	for _, a := range attendees {
		if err := checkShare(tickID, a); err != nil {
			return BatchProposal{}, err
		}

		earned, err := tickEarned(amount, a, cfg.multiplierFor(a.Role))
		if err != nil {
			return BatchProposal{}, err
		}

		if earned == 0 {
			continue
		}

		sum, ok := addCentipoints(total, earned)
		if !ok {
			return BatchProposal{}, fmt.Errorf("%s: the tick's credits sum past int64: %w",
				tickID, ErrInvalidEvent)
		}

		total = sum

		credits = append(credits, EntryProposal{
			AccountID:   a.AccountID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    earned,
			RaidID:      ev.RaidID,
			TickID:      ev.TickID,
		})
	}

	if len(credits) == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: every attendee earns 0 at this tick's weights and role multipliers, so %w",
			tickID, ErrNothingToPlan)
	}

	// The bank's debit leads, so a reader of the batch sees where the points came from before where
	// they went. The order is preserved by Canonical and is therefore part of the golden.
	entries := append([]EntryProposal{{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -total,
		RaidID:      ev.RaidID,
		TickID:      ev.TickID,
	}}, credits...)

	// NonNegative is NOT declared here, and the omission is argued rather than forgotten: the only
	// account this batch debits is the guild bank, which the commit-time engine exempts from balance
	// floors by design (a bank that could not go negative could not fund the first tick of a fresh
	// guild). Declaring a rule that constrains nothing is what .claude/skills/add-strategy/SKILL.md
	// calls a red flag.
	return proposeZeroSum(ctx, tickID, tickVersion, kinds.KindAttendance, ev.EffectiveAt, ev.Reason,
		entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		})
}

// tickEarned is one attendee's credit: the tick award times their weight, scaled by their role.
//
// Both steps report overflow rather than wrapping, and both name the ACCOUNT and the numbers. A
// wrapped credit is the one arithmetic failure that satisfies every declared invariant — the batch
// still sums to zero, because the bank's debit wrapped with it — so it has to be refused here or not
// at all.
func tickEarned(amount core.Centipoints, a Share, multiplierBp int64) (core.Centipoints, error) {
	base, ok := mulCentipoints(amount, a.Weight)
	if !ok {
		return 0, fmt.Errorf("%s: %d centipoints at weight %d for account %s overflows int64: %w",
			tickID, amount, a.Weight, a.AccountID, ErrInvalidEvent)
	}

	earned, ok := scaleByBasisPoints(base, multiplierBp)
	if !ok {
		return 0, fmt.Errorf(
			"%s: %d centipoints at role %q (%d bp) for account %s overflows int64: %w",
			tickID, base, a.Role, multiplierBp, a.AccountID, ErrInvalidEvent)
	}

	return earned, nil
}

// PlanAward is unsupported, and the refusal is a statement rather than a gap.
//
// `tick` answers "how are points earned?" and nothing else. It has no price list, no auction and no
// council, so there is no defensible number to debit a buyer. Returning one would mean copying
// fixed_price's price resolution into a second file, where the two copies would eventually disagree
// about what an unpriced item costs — and a pool that wants both rules wants the composition
// mechanism the design documents have not settled, not a silently doubled implementation of one of
// them.
func (Tick) PlanAward(Ctx, AwardEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(tickID,
		"award an item: it is an earn rule, so pair it with a spend rule such as fixed_price")
}

// PlanDecay is unsupported: a tick pool that wants points to expire runs a decay rule beside this
// one. Inventing a rate here would put a second decay in the tree with no cadence, no run row and no
// idempotency key — and decay is posted, not improvised (.claude/rules/decay-and-jobs.md).
func (Tick) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(tickID,
		"decay balances: it is an earn rule, so pair it with decay_percent or decay_window")
}

// PlanAdjustment moves points between an account and a counterparty. See adjustmentProposal: it is
// two entries and never one, because an officer who could add points without naming where they came
// from could inflate a guild's economy invisibly.
func (s Tick) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, tickID, tickVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed. Entry-wise negation is correct here
// because this strategy's only balance kind is `dkp`, a plain quantity; see reversePlan for why the
// reversal declares no floor and reads no current config.
func (Tick) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, tickID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM, never a computed weighting.
func (Tick) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, tickID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
func (Tick) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, tickID, acct)
}

// PriceHint is unsupported: this strategy does not price items at all, so it has no estimate to
// offer and a bidding UI must not be given a number to draw a bid box around.
func (Tick) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(tickID, "hint at a price: it is an earn rule and prices nothing")
}

// ValidateBid is unsupported: there are no bids to validate.
func (Tick) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(tickID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (Tick) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(tickID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. TestTick_EveryPlannerInvariant_IsDeclared compares the two by kind and balance kind.
func (Tick) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
