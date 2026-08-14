package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// attendance_weighted — the raid is worth a fixed pot, divided by who turned up. Phase 1, #196.
//
// It is the earn rule for a guild that wants standing driven by attendance rather than by hoarding
// (docs/getting-started/first-run.md's "Attendance-first" preset). A night is worth what the pool says
// it is worth, and each raider's credit is their SHARE of that night: present for twelve ticks of a
// twelve-tick raid earns twelve twenty-ninths of the pot when the other two raiders were there for
// nine and eight.
//
// HOW IT DIFFERS FROM `tick`, which is the strategy it will be compared against. `tick` pays a fixed
// amount PER UNIT of attendance, so a raid's cost to the guild's economy grows with the number of
// people on it: forty raiders at 10.00 a tick is twice the inflation of twenty. This one pays a fixed
// amount PER RAID, so the pot is constant and turning up buys a bigger slice of it. Neither is more
// correct; they are different guilds, and a guild that argues about attendance percentages usually
// wants this one.
//
// THE ARITHMETIC IS NOT THIS FILE'S. Every credit comes from ledger.Allocate through Ctx.Allocate: the
// shared largest-remainder allocator, whose credits sum to EXACTLY the pot and whose tiebreak is the
// account id, ascending. A pot of 1000.00 across weights 12/9/8 is 413.79 + 310.34 + 275.87 — three
// numbers that do not divide evenly and must still total the pot to the centipoint. Rounding each
// share independently is the classic way a ledger drifts, and it is why
// `.claude/rules/ledger-and-strategy.md` requires the allocator to be shared rather than
// re-implemented per strategy.
//
// WEIGHT IS ATTENDANCE, and what it counts is the caller's to decide: ticks present, minutes, or 1 for
// everybody on a raid that is scored as a whole. The planner is indifferent, because a share of a pot
// is a RATIO and any consistent unit produces the same split. A guild that credits a standby at half a
// share passes half the weight — there is no role_multipliers knob here, unlike `tick`, because
// scaling a weight and scaling a share of a pot are the same operation and two ways to say it would be
// two places for one number.
//
// THE RANKING SCORE IS NOT HERE YET, and that is the one thing a reader of the guide will look for.
// docs/guides/choosing-a-dkp-system.md describes this rule's headline as a standing of
// `balance × attendance %` — a ranking score, not a balance — and that number needs attendance
// STATISTICS: a percentage over a 30/60/90-day window, computed from the tick and raid tables against
// a denominator every member shares (docs/guides/attendance-and-windows.md, which lands in Phase 4).
// The Ctx façade deliberately exposes none of it, and `Ctx.Balance` is positional by seq rather than
// temporal, so a window cannot be derived from the ledger either. Inventing a substitute would be a
// ranking nobody configured. What ships here is the earn rule; the score is issue #223.
//
// EVERY BATCH IT WRITES SUMS TO ZERO. The pot is debited from the guild bank rather than minted, so a
// guild's bank balance reads as "everything ever awarded, minus everything ever spent back into it" —
// a number an officer can sanity-check — and conservation stays a column comparison
// (net_amount_cp = 0) rather than an aggregate over the whole ledger.

// The compile-time proof that the implementation matches the interface. If PointStrategy grows a
// method, `go build` says so on the next save rather than a reviewer noticing.
var _ PointStrategy = AttendanceWeighted{}

// AttendanceWeighted is the pot-per-raid earn strategy. It is STATELESS: everything it needs arrives
// through the Ctx façade, which is what lets one value serve every pool and every request
// concurrently.
type AttendanceWeighted struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	attendanceWeightedID      = "attendance_weighted"
	attendanceWeightedVersion = "0.1.0"
)

// attendanceWeightedConfigSchema is the JSON Schema for the pool config: every knob a guild can turn,
// in one place, in the form that renders the pool-settings form and validates the config at the API
// edge.
//
// Draft 2020-12. `additionalProperties: false` is deliberate and load-bearing — a typo'd knob
// (`raid_pot_cd`) must be a validation error at the edge and not a silently ignored key that leaves the
// pool running the default, which is how a guild discovers three months later that every raid has been
// worth 100.00. Every money field is an INTEGER named `_cp`: canonical §1 bans a decimal on the wire,
// and a schema that said `number` would invite one.
const attendanceWeightedConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Attendance weighted",
  "description": "Each raid is worth a fixed pot, divided across the people who attended it in proportion to how much of it they attended.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "raid_pot_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 10000,
      "title": "Points per raid (centipoints)",
      "description": "What one attendance event distributes in total, however many people were on it. 10000 centipoints is 100.00 points. An event worth something else — a first kill, a short night — carries its own value."
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
func (AttendanceWeighted) ConfigSchema() []byte { return []byte(attendanceWeightedConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (AttendanceWeighted) ID() string { return attendanceWeightedID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (AttendanceWeighted) Version() string { return attendanceWeightedVersion }

// RuleKind is earn: this strategy answers "how are points earned?" and nothing else. It occupies the
// same slot as `tick` and is an alternative to it, not a companion — a pool holds ONE earn rule
// (ADR-0026), so "tick plus attendance_weighted" is not a configuration.
func (AttendanceWeighted) RuleKind() RuleKind { return RuleEarn }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal.
func (AttendanceWeighted) BalanceKinds() []string { return []string{BalanceKindDKP} }

// attendanceWeightedConfig is the parsed pool config. The JSON tags are the schema's property names
// and the two must agree; TestAttendanceWeighted_ConfigSchema_EveryKnobAgreesWithTheParser asserts
// that they do, because a knob in the schema that the parser ignores is a knob the settings form
// offers and nothing reads.
type attendanceWeightedConfig struct {
	RaidPotCp core.Centipoints `json:"raid_pot_cp"`
	FloorCp   core.Centipoints `json:"floor_cp"`
}

// defaultAttendanceWeightedConfig is the config a pool that has set nothing runs under: 100.00 points
// a raid. It is the struct the pool's JSON is decoded OVER, which is what makes an absent key mean
// "the default" and a present `"floor_cp": 0` mean "zero, chosen".
func defaultAttendanceWeightedConfig() attendanceWeightedConfig {
	return attendanceWeightedConfig{
		RaidPotCp: 10_000,
		FloorCp:   0,
	}
}

// config parses and validates the pool's config.
//
// It re-validates what the API edge already validated against ConfigSchema, and the duplication earns
// its keep: the edge validates what a human typed into the settings form, and this validates what
// actually reached the planner — which includes a config written by the importer, by a migration
// backfill, or by a test.
func (AttendanceWeighted) config(ctx Ctx) (attendanceWeightedConfig, error) {
	cfg := defaultAttendanceWeightedConfig()

	if err := decodeConfig(attendanceWeightedID, ctx.ConfigJSON(), &cfg); err != nil {
		return attendanceWeightedConfig{}, err
	}

	return validateAttendanceWeightedConfig(cfg)
}

// validateAttendanceWeightedConfig applies the bounds the schema declares, to a config that has
// already parsed. Split from config so that the defaults are validated too — a default that violated
// its own schema would otherwise be the one config nothing ever checked.
func validateAttendanceWeightedConfig(
	cfg attendanceWeightedConfig,
) (attendanceWeightedConfig, error) {
	if cfg.RaidPotCp <= 0 {
		return attendanceWeightedConfig{}, fmt.Errorf(
			"%s: raid_pot_cp is %d, so a raid distributes nothing: %w",
			attendanceWeightedID, cfg.RaidPotCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAttendance splits the pot across the attendees in proportion to their weights, debited from the
// guild bank.
//
// The bank is the counterparty rather than a mint, so the batch sums to zero like every other batch
// this strategy writes — and unlike `tick`, the bank's debit is the SAME every raid regardless of how
// many people were on it, which is the whole economic argument for this rule.
//
// AN EVENT'S OWN VALUE OVERRIDES THE POOL'S. A first kill worth double, or a two-hour Tuesday worth
// half, arrives as AttendanceEvent.AmountCp — the same per-event value `tick` reads, sourced from
// event_type.default_tick_value_cp through raid_tick_credit.value_cp (domain model §8.1). Putting it
// in the pool config too would be a second place for one number.
//
// AN ATTENDEE WHOSE SHARE ROUNDS TO NOTHING GETS NO ENTRY, and that is the allocator's doing rather
// than this planner's: a zero base means their exact share rounded away and the centipoint it did not
// get went to a higher remainder, so the credits still sum to exactly the pot. ledger_entry carries
// CHECK (amount_cp <> 0), so writing the zero is not an option.
func (s AttendanceWeighted) PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if len(ev.Attendees) == 0 {
		return BatchProposal{}, fmt.Errorf("%s: attendance event has no attendees: %w",
			attendanceWeightedID, ErrInvalidEvent)
	}

	pot := cfg.RaidPotCp
	if ev.AmountCp != nil {
		pot = *ev.AmountCp
	}

	if pot <= 0 {
		return BatchProposal{}, fmt.Errorf("%s: the pot is %d centipoints, which awards nothing: %w",
			attendanceWeightedID, pot, ErrInvalidEvent)
	}

	attendees := sortedShares(ev.Attendees)
	if err := checkDistinctShares(attendanceWeightedID, attendees); err != nil {
		return BatchProposal{}, err
	}

	// BEFORE the weights are totalled, so that a system account on the attendee list is named as
	// itself rather than reported as whatever its weight happened to make of the run. The pot is
	// FIXED, so a share for the account that funds it is a share taken from every real attendee — and
	// the batch would still sum to zero and still pass the invariant engine (review of #228).
	if err := checkNoSystemAccounts(ctx, attendanceWeightedID, attendees); err != nil {
		return BatchProposal{}, err
	}

	if err := checkAttendanceWeights(attendees); err != nil {
		return BatchProposal{}, err
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", attendanceWeightedID, err)
	}

	// The empty account is UNREACHABLE: the attendee list is non-empty by the check above, and a list
	// whose weights all round to nothing was refused by checkAttendanceWeights. The bank stands in
	// because it is the account this batch already debits, so even if the allocator's contract changed
	// the pot would return to where it came from rather than landing somewhere nobody chose.
	credits, err := ctx.Allocate(pot, attendees, bank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: split %d centipoints across %d attendees: %w",
			attendanceWeightedID, pot, len(attendees), err)
	}

	// The bank's debit leads, so a reader of the batch sees where the points came from before where
	// they went. The order is preserved by Canonical and is therefore part of the golden.
	entries := make([]EntryProposal, 0, len(credits)+1)
	entries = append(entries, EntryProposal{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -pot,
		RaidID:      ev.RaidID,
		TickID:      ev.TickID,
	})

	for _, c := range credits {
		entries = append(entries, EntryProposal{
			AccountID:   c.AccountID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    c.AmountCp,
			RaidID:      ev.RaidID,
			TickID:      ev.TickID,
		})
	}

	// NonNegative is NOT declared here, and the omission is argued rather than forgotten: the only
	// account this batch debits is the guild bank, which the commit-time engine exempts from balance
	// floors by design (a bank that could not go negative could not fund the first raid of a fresh
	// guild). Declaring a rule that constrains nothing is what .claude/skills/add-strategy/SKILL.md
	// calls a red flag.
	return proposeZeroSum(ctx, attendanceWeightedID, attendanceWeightedVersion, kinds.KindAttendance,
		ev.EffectiveAt, ev.Reason, entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
			{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		})
}

// checkAttendanceWeights rejects a share this planner cannot divide by, and refuses an event where
// nobody attended anything.
//
// THE ALL-ZERO CASE DECLINES RATHER THAN ROUTING TO A SYSTEM ACCOUNT, which is the opposite of what
// `zero_sum` does with the same input, and the difference is which direction the money is already
// moving. A zero-sum split divides a price the buyer HAS PAID: the points are in flight and must land
// somewhere, so an unallocatable remainder goes to `residue` rather than being dropped. Here the pot
// does not exist until this batch creates it, so there is nothing in flight — posting it to a system
// account would take points out of the bank for a raid nobody attended, and the honest answer is that
// there is no batch. ErrNothingToPlan is what the caller records as a skipped event.
//
// The weight sum is accumulated with an overflow check rather than left to the allocator's, because
// the answer this needs is "is it zero?" and a wrapped total can be zero.
func checkAttendanceWeights(sorted []Share) error {
	var total int64

	for _, a := range sorted {
		if err := checkShare(attendanceWeightedID, a); err != nil {
			return err
		}

		sum := total + a.Weight
		if sum < total {
			return fmt.Errorf("%s: the attendance weights sum past int64 at account %s: %w",
				attendanceWeightedID, a.AccountID, ErrInvalidEvent)
		}

		total = sum
	}

	if total == 0 {
		return fmt.Errorf(
			"%s: every attendee has weight 0, so there is no attendance to divide the pot by and %w",
			attendanceWeightedID, ErrNothingToPlan)
	}

	return nil
}

// PlanAdjustment moves points between an account and a counterparty. See adjustmentProposal: it is two
// entries and never one, because an officer who could add points without naming where they came from
// could inflate a guild's economy invisibly.
func (s AttendanceWeighted) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, attendanceWeightedID, attendanceWeightedVersion, cfg.FloorCp, ev)
}

// PlanAward is unsupported, and the refusal is a statement rather than a gap.
//
// This strategy answers "how are points earned?" and nothing else. It has no price list, no auction
// and no council, so there is no defensible number to debit a buyer. Returning one would mean copying
// a spend rule's price resolution into a third file, where the copies would eventually disagree about
// what an unpriced item costs.
func (AttendanceWeighted) PlanAward(Ctx, AwardEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(attendanceWeightedID,
		"award an item: it is an earn rule, so pair it with a spend rule such as fixed_price or zero_sum")
}

// PlanDecay is unsupported: a pool that wants points to expire runs a decay rule beside this one.
// Inventing a rate here would put a second decay in the tree with no cadence, no run row and no
// idempotency key — and decay is posted, not improvised (.claude/rules/decay-and-jobs.md).
func (AttendanceWeighted) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(attendanceWeightedID,
		"decay balances: it is an earn rule, so pair it with decay_percent or decay_window")
}

// PlanReversal negates every entry of the batch being reversed. Entry-wise negation is correct here
// because this strategy's only balance kind is `dkp`, a plain quantity; see reversePlan for why the
// reversal declares no floor and reads no current config.
func (AttendanceWeighted) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, attendanceWeightedID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM, never a computed weighting.
//
// THIS IS THE METHOD THE STRATEGY'S NAME MAKES TEMPTING, and scaling it by attendance would be the
// defect the guide names outright: "Conflating the two — letting attendance scale the spendable
// balance rather than the ranking — produces nonsense: your bank shrinks when you miss a raid, so your
// past purchases were retroactively mispriced." `.claude/rules/ledger-and-strategy.md` permits
// computed weighting in Priority and forbids it here.
func (AttendanceWeighted) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, attendanceWeightedID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
//
// IT IS NOT THE ATTENDANCE-WEIGHTED SCORE YET, and this is the honest placeholder rather than a
// pretend one: the documented standing is `balance × attendance %`, the percentage comes from
// attendance statistics that land in Phase 4, and no fact on the Ctx façade substitutes for it (see
// the file header, and issue #223). A rank computed from a number this method invented would be a
// ranking nobody configured, shown beside a percentage that disagreed with it.
//
// A composed pool does not reach this method at all — Rules.Priority routes to the SPEND rule and this
// is an earn rule (ADR-0026) — so what ships here is a total implementation of the interface rather
// than a queue any pool consults.
func (AttendanceWeighted) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, attendanceWeightedID, acct)
}

// PriceHint is unsupported: this strategy does not price items at all, so it has no estimate to offer
// and a bidding UI must not be given a number to draw a bid box around.
func (AttendanceWeighted) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(attendanceWeightedID, "hint at a price: it is an earn rule and prices nothing")
}

// ValidateBid is unsupported: there are no bids to validate.
func (AttendanceWeighted) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(attendanceWeightedID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (AttendanceWeighted) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(attendanceWeightedID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. TestAttendanceWeighted_EveryPlannerInvariant_IsDeclared compares the two by kind and
// balance kind.
func (AttendanceWeighted) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
