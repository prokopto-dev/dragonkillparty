package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// decay_percent — the classic weekly haircut. Phase 1, #194.
//
// Every balance shrinks by a percentage on a cadence, so attendance is recent rather than eternal and
// a raider who stopped logging in two expansions ago stops outbidding the people who turned up last
// night. It is the most configured EQdkp Plus feature after a cap, and the guide's worked example is
// the whole product: 10% a week on 500.00 posts −50.00, then −45.00, then −40.50, then −36.45
// (docs/guides/choosing-a-dkp-system.md).
//
// DECAY IS POSTED, NOT COMPUTED. The batch is explicit and permanent, so a balance stays literally a
// SUM and "why did my points change?" is answerable by pointing at a row
// (.claude/rules/decay-and-jobs.md §1). Nothing anywhere computes a decayed balance on read — not
// Spendable, not a standings query, not a cached total. That is the failure this whole family is
// shaped around, and `fixed_price` already carries the same rule for the haircut built into it.
//
// THE POINTS GO TO THE BANK RATHER THAN NOWHERE, so every batch sums to zero and conservation stays a
// column comparison (`net_amount_cp = 0`) instead of an aggregate over the whole ledger. A guild that
// wants decay to genuinely destroy points wants a strategy whose batches do not sum to zero, and that
// is a different strategy with a different invariant set.
//
// THE THREE SETTINGS GUILDS ARGUE ABOUT are knobs here rather than assumptions, because each of them
// is a different DKP system and picking one silently is how a guild discovers in month three that the
// site does not do what its own charter says (.claude/rules/decay-and-jobs.md §5):
//
//   - THE FLOOR is the balance decay stops at. Without one a lapsed member decays asymptotically
//     forever and their statement is an infinite column of −0.01. It is a CLAMP ON THE AMOUNT rather
//     than a check afterwards, so the run lands exactly on the floor rather than being rejected for
//     crossing it.
//   - THE NEGATIVE-BALANCE POLICY decides what a debt does. All three of the guide's answers ship,
//     and `skip` is the default because it is the only one that cannot surprise anybody.
//   - MISSED RUNS AFTER DOWNTIME are not a knob here and must not become one: catch-up posts ONE
//     BATCH PER MISSED PERIOD, each carrying its own cadence label and planned against the balance
//     the previous one left, which is what makes it idempotent with no extra machinery
//     (.claude/rules/decay-and-jobs.md §6). The planner sees one period at a time; the scheduler owns
//     the calendar.
//
// IDEMPOTENCY IS THE KEY, NOT THE ARITHMETIC, and this is the one thing about this strategy that is
// worth reading twice. A percentage haircut is not structurally idempotent the way a cap trim is —
// applying 10% twice takes 19%. What makes a re-run a no-op is that the run is a SNAPSHOT: every
// balance is read at run.AsOfSeq, so a second run for the same period reads exactly what the first
// one read and proposes exactly the batch the first one proposed, byte for byte. The ledger's
// (pool_id, kind, cadence_period) key then collapses the two into one committed batch
// (.claude/rules/decay-and-jobs.md §3). A planner that read the pool head instead would compound the
// haircut on every retry, and the retry is the normal case: a job firing twice after a restart, a
// retry after a partial failure, an officer clicking "run decay now" mid-flight.
//
// IT IS AN OVER-TIME RULE AND IT NEITHER EARNS NOR SPENDS. PlanAttendance and PlanAward return
// ErrUnsupported naming this strategy: a haircut is not a tick and it is not a price list. A pool
// composes it with an earn rule and a spend rule — three (strategy, config) pairs, one per question,
// settled by ADR-0026.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = DecayPercent{}

// DecayPercent is the percentage-decay strategy. STATELESS: everything it needs arrives through the
// Ctx façade, which is what lets one value serve every pool and every request concurrently.
type DecayPercent struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history.
const (
	decayPercentID      = "decay_percent"
	decayPercentVersion = "0.1.0"
)

// The `negative_balances` knob's values: what a decay run does to an account already in debt. The
// three are the guide's three, and each is a real guild's policy rather than a shade of one.
const (
	// NegativeBalancesSkip — a debt is left alone. The default, and the only answer that cannot
	// surprise a member: nothing happens to a balance that is already below zero, so a raider who
	// overdrew stays exactly where the overdraft left them until they earn it back.
	NegativeBalancesSkip = "skip"

	// NegativeBalancesTowardZero — a debt shrinks by the same rate a credit would, so the decay is
	// DEBT FORGIVENESS: −500.00 at 10% is credited 50.00 and becomes −450.00. Say this one out loud
	// in the guild's own rules, because it means an overdraft eventually forgives itself.
	NegativeBalancesTowardZero = "toward_zero"

	// NegativeBalancesPreserveSign — a debt grows by the rate, because the rule is about magnitude
	// rather than about direction: −500.00 at 10% is debited 50.00 and becomes −550.00. It needs a
	// floor below zero to have anywhere to go, which validateDecayPercentConfig requires rather than
	// leaving the knob inert.
	NegativeBalancesPreserveSign = "preserve_sign"
)

// ID is the permanent identifier written onto every batch this strategy plans.
func (DecayPercent) ID() string { return decayPercentID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (DecayPercent) Version() string { return decayPercentVersion }

// RuleKind is over_time: this strategy's only ledger-writing planner is the cadence run, keyed
// (pool_id, kind, cadence_period) in the decay_run table beside cap and start_points (ADR-0024,
// ADR-0026).
func (DecayPercent) RuleKind() RuleKind { return RuleOverTime }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal (see PlanReversal).
func (DecayPercent) BalanceKinds() []string { return []string{BalanceKindDKP} }

// decayPercentConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in
// one place, in the form that renders the pool-settings form and validates the config at the API
// edge.
//
// Draft 2020-12, `additionalProperties: false`, integers only. `decay_bp` defaults to 0 and a pool
// that leaves it there is REFUSED rather than run: a pool that wants no decay leaves the over-time
// slot empty, which ADR-0026 makes a first-class answer (ErrNoRule), and a decay rule configured to
// decay nothing is a settings page that says "Decay" beside a standings page that never changes.
const decayPercentConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Percentage decay",
  "description": "Balances shrink by a percentage on a cadence, posted as an explicit batch per period.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "decay_bp": {
      "type": "integer",
      "minimum": 0,
      "maximum": 10000,
      "default": 0,
      "title": "Decay per period (basis points)",
      "description": "1000 is 10% of the balance per run. 0 is refused: a pool that does not decay leaves its over-time rule unset instead."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Balance decay stops at (centipoints)",
      "description": "Decay never takes an account past this. Without a floor a lapsed member decays toward it forever, one centipoint at a time."
    },
    "negative_balances": {
      "type": "string",
      "enum": ["skip", "toward_zero", "preserve_sign"],
      "default": "skip",
      "title": "What happens to an account in debt",
      "description": "skip leaves it alone; toward_zero forgives the debt at the same rate; preserve_sign grows it, and needs a floor below zero."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes.
//
// A COPY, not the backing array of the constant: a caller that could mutate the schema could change
// what every pool validates against.
func (DecayPercent) ConfigSchema() []byte { return []byte(decayPercentConfigSchema) }

// decayPercentConfig is the parsed pool config. The JSON tags are the schema's property names and the
// two must agree; TestDecayPercent_ConfigSchema_EveryKnobAgreesWithTheParser asserts that they do,
// because a knob in the schema the parser ignores is a knob the settings form offers and nothing
// reads.
type decayPercentConfig struct {
	DecayBp          int64            `json:"decay_bp"`
	FloorCp          core.Centipoints `json:"floor_cp"`
	NegativeBalances string           `json:"negative_balances"`
}

// defaultDecayPercentConfig is the config the pool's JSON is decoded OVER, which is what makes an
// absent key mean "the default" and a present `"floor_cp": 0` mean "zero, chosen".
//
// It is deliberately NOT a runnable configuration: `decay_bp` is 0 and validateDecayPercentConfig
// refuses that. See the schema comment — a default rate would be a haircut nobody chose.
func defaultDecayPercentConfig() decayPercentConfig {
	return decayPercentConfig{
		DecayBp:          0,
		FloorCp:          0,
		NegativeBalances: NegativeBalancesSkip,
	}
}

// config parses and validates the pool's config, re-validating what the API edge already checked
// against ConfigSchema because a config can also arrive from the importer, a backfill or a test.
func (DecayPercent) config(ctx Ctx) (decayPercentConfig, error) {
	cfg := defaultDecayPercentConfig()

	if err := decodeConfig(decayPercentID, ctx.ConfigJSON(), &cfg); err != nil {
		return decayPercentConfig{}, err
	}

	return validateDecayPercentConfig(cfg)
}

// validateDecayPercentConfig applies the bounds the schema declares, plus the one relationship
// between knobs that no per-field bound can express.
//
// PRESERVE_SIGN NEEDS SOMEWHERE TO GO. The floor is what decay stops at, so a debt can only grow if
// the floor is below it — and with the default floor of 0 every negative balance is already at or
// past it, which would make the policy silently identical to `skip`. An officer who chose
// "the debt grows" and got "nothing happens" has no way to tell from the settings page, so the
// configuration is refused by name instead.
func validateDecayPercentConfig(cfg decayPercentConfig) (decayPercentConfig, error) {
	// The bounds are the schema's, restated: a rate above 100% would take a balance past zero in one
	// run, and a negative rate would be growth wearing decay's name.
	if cfg.DecayBp < 0 || cfg.DecayBp > basisPointsWhole {
		return decayPercentConfig{}, fmt.Errorf("%s: decay_bp is %d, want 0..%d: %w",
			decayPercentID, cfg.DecayBp, basisPointsWhole, ErrInvalidConfig)
	}

	if cfg.DecayBp == 0 {
		return decayPercentConfig{}, fmt.Errorf(
			"%s: decay_bp is 0, so this pool decays nothing; a pool that does not decay leaves its "+
				"over-time rule unset rather than running a decay rule that takes 0%%: %w",
			decayPercentID, ErrInvalidConfig)
	}

	switch cfg.NegativeBalances {
	case NegativeBalancesSkip, NegativeBalancesTowardZero, NegativeBalancesPreserveSign:
	default:
		return decayPercentConfig{}, fmt.Errorf("%s: negative_balances is %q, want %q, %q or %q: %w",
			decayPercentID, cfg.NegativeBalances, NegativeBalancesSkip, NegativeBalancesTowardZero,
			NegativeBalancesPreserveSign, ErrInvalidConfig)
	}

	if cfg.NegativeBalances == NegativeBalancesPreserveSign && cfg.FloorCp >= 0 {
		return decayPercentConfig{}, fmt.Errorf(
			"%s: negative_balances is %q with floor_cp %d, so a debt is already at or past the floor "+
				"and could never grow — set a floor below zero or choose %q: %w",
			decayPercentID, NegativeBalancesPreserveSign, cfg.FloorCp, NegativeBalancesSkip,
			ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanDecay is the DECAY RUN: it debits each account its share of the period's haircut and credits
// the total to the guild bank.
//
// BALANCES ARE READ AT run.AsOfSeq — positionally, never temporally. That is what makes a re-run for
// the same period reproduce this exact batch instead of compounding a second haircut onto the first
// one's result, and it is what stops a batch committed while the run is planning from changing what
// the run decayed. A backdated effective_at must not change what a past balance WAS.
//
// SYSTEM ACCOUNTS ARE NEVER DECAYED. They are structurally negative by design — the guild bank funds
// every tick — and decaying the counterparty that makes the batch balance is meaningless in the best
// case and a compounding debt in the worst.
func (s DecayPercent) PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if run.PeriodKey == "" {
		return BatchProposal{}, fmt.Errorf(
			"%s: decay run has no period key, so nothing makes a re-run idempotent: %w",
			decayPercentID, ErrInvalidEvent)
	}

	targets, bank, err := cadenceTargets(ctx, decayPercentID, "decay", run)
	if err != nil {
		return BatchProposal{}, err
	}

	entries := make([]EntryProposal, 0, len(targets)+1)

	var total core.Centipoints

	for _, a := range targets {
		if a.IsSystem() {
			continue
		}

		balance, err := ctx.Balance(a.ID, BalanceKindDKP, run.AsOfSeq)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read balance for account %s at seq %d: %w",
				decayPercentID, a.ID, run.AsOfSeq, err)
		}

		amount, err := cfg.decayable(balance)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: account %s: %w", decayPercentID, a.ID, err)
		}

		// A balance small enough that the rate rounds to nothing, an account the policy skips, or one
		// already at the floor. Dropped rather than written as a zero entry — ledger_entry carries
		// CHECK (amount_cp <> 0) — and NOT rounded up: rounding a decay up takes a centipoint the rate
		// did not ask for, from the members with the least.
		if amount == 0 {
			continue
		}

		sum, ok := addCentipoints(total, amount)
		if !ok {
			// Checked rather than accumulated, even though proposeZeroSum's own balance assertion would
			// catch the consequence. A wrapped total lands on the BANK's entry, so the failure would
			// surface as "the batch sums to some enormous number" with no indication that a roster of
			// large balances was the cause — and the roster is what the officer would have to look at.
			return BatchProposal{}, fmt.Errorf(
				"%s: the decay for period %s sums past int64 at account %s: %w",
				decayPercentID, run.PeriodKey, a.ID, ErrInvalidEvent)
		}

		total = sum

		entries = append(entries, EntryProposal{
			AccountID:   a.ID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    amount,
		})
	}

	if len(entries) == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: no account in period %s has a balance that decays by %d bp, so %w",
			decayPercentID, run.PeriodKey, cfg.DecayBp, ErrNothingToPlan)
	}

	// The bank's entry closes the batch, so a reader sees who moved before where the points went. The
	// order is preserved by Canonical and is therefore part of the golden.
	entries = append(entries, EntryProposal{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -total,
	})

	// NonNegative is declared even though the clamp above already honours the floor, and the
	// duplication is the point: the planner reads balances at run.AsOfSeq while the engine checks them
	// at the commit head, so a spend that landed between the two is caught by the invariant and by
	// nothing else.
	//
	// IT IS DECLARED ON A toward_zero RUN TOO, and that is only defensible because the floor
	// constrains a DEDUCTION rather than a balance: forgiving a debt of 45.00 leaves the member at
	// −40.50, under a floor of zero and better off than they started, which the commit-time engine
	// permits and a debit to the same balance would not. That was found in review of this PR, as a
	// batch the ledger refused in its entirety; the fix is in internal/ledger/invariant.go's
	// checkNonNegative, with the argument, and TestRules_DecayPercentTowardZero_ForgivesADebtEndToEnd
	// is the run it refused.
	return proposeZeroSum(ctx, decayPercentID, decayPercentVersion, kinds.KindDecay, run.EffectiveAt,
		"decay "+run.PeriodKey, entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
			{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
		})
}

// decayable is the whole arithmetic of this strategy: the signed entry one account contributes to the
// run, or 0 for an account it does not touch.
//
// In integers, floored, with one clamp that every branch shares — DECAY NEVER MOVES A BALANCE PAST
// THE FLOOR:
//
//	balance > floor, balance > 0   debit  min(balance × bp, balance − floor)
//	balance < 0, skip              nothing
//	balance < 0, toward_zero       credit balance × bp, forgiving the debt but never past zero
//	balance < 0, preserve_sign     debit  min(|balance| × bp, balance − floor), growing the debt
//	otherwise                      nothing — already at or below the floor
//
// A POSITIVE BALANCE CAN NEVER BE DECAYED PAST ZERO even with no floor, because the rate is capped at
// 10000 bp and the amount is floored: at 100% the account lands exactly on 0. The floor is what makes
// the run stop earlier than that, and the clamp is what makes it land ON the floor rather than
// crossing it and being rejected at commit — a rejected batch is a whole period's decay lost because
// one account was near the bottom.
func (cfg decayPercentConfig) decayable(balance core.Centipoints) (core.Centipoints, error) {
	if balance > 0 && balance > cfg.FloorCp {
		room, ok := subCentipoints(balance, cfg.FloorCp)
		if !ok {
			return 0, fmt.Errorf("balance %d against floor %d overflows int64: %w",
				balance, cfg.FloorCp, ErrInvalidEvent)
		}

		return -min(decayAmount(balance, cfg.DecayBp), room), nil
	}

	// Zero, or a positive balance already at or below the floor. Nothing to take either way.
	if balance >= 0 {
		return 0, nil
	}

	// A debt, and the default policy is to leave one alone. Answered BEFORE the magnitude below,
	// because a debt at math.MinInt64 has no magnitude and `skip` still has an answer for it.
	if cfg.NegativeBalances == NegativeBalancesSkip {
		return 0, nil
	}

	// Its magnitude is what the rate applies to, whichever direction the policy then moves it, and
	// negating a balance is the one subtraction in this file that can fail: math.MinInt64 has no
	// positive counterpart.
	magnitude, ok := subCentipoints(0, balance)
	if !ok {
		return 0, fmt.Errorf("balance %d has no representable magnitude to decay: %w",
			balance, ErrInvalidEvent)
	}

	if cfg.NegativeBalances == NegativeBalancesTowardZero {
		// A CREDIT: the debt shrinks. It cannot overshoot zero — the rate is at most 10000 bp and the
		// amount is floored, so the credit is at most the magnitude itself.
		return decayAmount(magnitude, cfg.DecayBp), nil
	}

	// preserve_sign, and it is the only value left — validateDecayPercentConfig refuses every other
	// one, and requires a floor below zero for this one so that the debt has somewhere to go.
	//
	// The room below a debt cannot overflow, unlike the room above a balance: both operands are
	// negative here, so `balance - floor` is at most math.MaxInt64, reached only by a balance of −1
	// against a floor of math.MinInt64.
	room := balance - cfg.FloorCp
	if room <= 0 {
		return 0, nil
	}

	return -min(decayAmount(magnitude, cfg.DecayBp), room), nil
}

// PlanAttendance is unsupported: a haircut is not a tick. A pool wants this rule beside an earn rule,
// not instead of one.
func (DecayPercent) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(decayPercentID,
		"credit an attendance tick: it decays balances, so pair it with an earn rule such as tick")
}

// PlanAward is unsupported: a decay rate prices nothing.
func (DecayPercent) PlanAward(Ctx, AwardEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(decayPercentID,
		"award an item: it decays balances, so pair it with a spend rule such as fixed_price")
}

// PlanAdjustment moves points between an account and a counterparty; see adjustmentProposal.
//
// IT IS NOT DECAYED. An adjustment is an officer correcting a number they know to be wrong, and the
// next run is what applies the rate to the corrected balance — in a batch that says so.
func (s DecayPercent) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, decayPercentID, decayPercentVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed; see reversePlan.
//
// REVERSING A DECAY GIVES THE POINTS BACK, and the next run decays the restored balance again if the
// rate still applies. That is correct rather than circular: the reversal says this period's haircut
// should not have happened — a rate an officer typed wrong, a period the guild had agreed to skip —
// and the period's label stays taken, so the reversal cannot be undone by simply re-running it.
func (DecayPercent) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, decayPercentID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM.
//
// NO COMPUTED DECAY. The rate is posted as explicit batches, so it is already in the sum; a strategy
// that also applied it here would apply it twice, and the second application would be invisible in
// every statement. `.claude/rules/ledger-and-strategy.md` permits computed weighting in Priority and
// forbids it here, and this is the method where it would be most tempting.
func (DecayPercent) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, decayPercentID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
func (DecayPercent) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, decayPercentID, acct)
}

// PriceHint is unsupported: this strategy prices nothing.
func (DecayPercent) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(decayPercentID,
		"hint at a price: it decays balances and prices nothing")
}

// ValidateBid is unsupported: there are no bids to validate.
func (DecayPercent) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(decayPercentID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (DecayPercent) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(decayPercentID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. TestDecayPercent_EveryPlannerInvariant_IsDeclared compares the two by kind and balance
// kind for exactly that reason.
func (DecayPercent) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
