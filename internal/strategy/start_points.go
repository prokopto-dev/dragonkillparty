package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// start_points — an opening balance for recruits. Phase 1, #193.
//
// A new member joins a mature guild where everyone has 800 points. Without a starting grant they
// cannot bid on anything for two months, which is a recruitment problem rather than a fairness one.
// The grant posts once per account per pool as a `start_points` batch and appears on the statement
// like everything else, so a member can see exactly where their opening 200 points came from
// (docs/guides/choosing-a-dkp-system.md).
//
// ONCE PER ACCOUNT, AND "ONCE" IS DEFINED BY THE LEDGER RATHER THAN BY A FLAG. An account is eligible
// if it has NO ledger history in this pool; the grant itself is history, so the next run skips it.
// There is no `granted` column to forget to set, no flag to get out of step with the entries, and no
// way for a restore-from-backup to re-arm it. This is property P7 — "start_points applies exactly
// once per account, and never to an account that already has ledger history"
// (docs/design/04-testing.md), whose ticket title is "everyone got 1000 points again".
//
// A ZERO BALANCE IS NOT AN EMPTY HISTORY, and conflating them is the whole bug. A veteran who earned
// eight hundred points and spent every one of them has a balance of zero and four years of statement;
// granting them an opening balance would be indistinguishable, in the ledger, from paying them twice
// for turning up. Ctx.HasHistory is on the façade precisely so this planner can tell the two apart —
// a sum cannot.
//
// IT IS A CADENCE FAMILY, sharing the `decay_run` table and the `(pool_id, kind, cadence_period)`
// idempotency key with `decay` and `cap` (ADR-0024, .claude/rules/decay-and-jobs.md). Two layers of
// idempotency, deliberately: the unique index stops a second RUN row for a period, and the
// eligibility rule above stops a second GRANT to an account whatever the period is called. The second
// is what survives an officer clicking "run now" in a period nobody has used yet.
//
// THE POINTS COME FROM THE GUILD BANK rather than being minted, so the batch sums to zero and
// conservation stays a column comparison. The bank is the account that funds every tick and is
// structurally negative by design; import_opening is deliberately NOT used here, because that account
// is the other side of a MIGRATED balance and a recruit's opening grant is not a migration — routing
// it there would make the importer's reconciliation report disagree with itself.
//
// IT EARNS EXACTLY ONCE, AND IT OCCUPIES THE OVER-TIME SLOT. PlanAttendance and PlanAward return
// ErrUnsupported naming this strategy: a grant is not a tick and it is not a price. Its only planner
// is PlanDecay, which is what a composed pool asks its OVER-TIME rule (ADR-0026) — so RuleKind is
// RuleOverTime even though the guide's catalogue describes the EFFECT as earning. The two are not in
// conflict and the distinction is worth holding on to: what a rule does for a member's balance is not
// the same question as which planner a pool routes to it, and this family is defined by the second —
// `start_points` is a decay_run.kind beside `decay` and `cap`, not a raid-night event.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = StartPoints{}

// StartPoints is the opening-balance strategy. STATELESS: everything it needs arrives through Ctx.
type StartPoints struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API.
const (
	startPointsID      = "start_points"
	startPointsVersion = "0.1.0"
)

// startPointsConfigSchema is the JSON Schema for the pool config.
//
// `grant_cp` defaults to 0 — no grant — because there is no defensible default opening balance: it
// depends entirely on what the rest of the guild already holds. A run against a pool that has not set
// it is refused rather than posting a batch of nothing.
const startPointsConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Start points",
  "description": "Grants a new member an opening balance, once, the first time a run sees an account with no ledger history.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "grant_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "Opening grant (centipoints)",
      "description": "What a new member starts with. 20000 centipoints is 200.00 points. 0 disables the grant, and a run against a pool with 0 is refused rather than posting an empty batch."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An adjustment is rejected if it would take an account below this. Negative permits going into debt to a limit."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes — a copy, so no caller can change what
// every pool validates against.
func (StartPoints) ConfigSchema() []byte { return []byte(startPointsConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (StartPoints) ID() string { return startPointsID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (StartPoints) Version() string { return startPointsVersion }

// RuleKind is over_time: the grant is POSTED on a cadence, keyed (pool_id, kind, cadence_period) in
// the decay_run table beside decay and cap (ADR-0024), and PlanDecay is the only planner it answers.
// See the file header on why that is not a contradiction with the guide calling the effect earning.
func (StartPoints) RuleKind() RuleKind { return RuleOverTime }

// BalanceKinds is the one balance kind this strategy moves.
func (StartPoints) BalanceKinds() []string { return []string{BalanceKindDKP} }

// startPointsConfig is the parsed pool config; the JSON tags are the schema's property names.
type startPointsConfig struct {
	GrantCp core.Centipoints `json:"grant_cp"`
	FloorCp core.Centipoints `json:"floor_cp"`
}

// defaultStartPointsConfig is the config the pool's JSON is decoded OVER: no grant until a guild
// names one.
func defaultStartPointsConfig() startPointsConfig {
	return startPointsConfig{GrantCp: 0, FloorCp: 0}
}

// config parses and validates the pool's config.
func (StartPoints) config(ctx Ctx) (startPointsConfig, error) {
	cfg := defaultStartPointsConfig()

	if err := decodeConfig(startPointsID, ctx.ConfigJSON(), &cfg); err != nil {
		return startPointsConfig{}, err
	}

	return validateStartPointsConfig(cfg)
}

// validateStartPointsConfig applies the bounds the schema declares. A negative grant is rejected
// rather than treated as a charge: an opening balance that takes points away is not a start-points
// rule, it is an adjustment, and it should be written as one so that it carries a reason.
func validateStartPointsConfig(cfg startPointsConfig) (startPointsConfig, error) {
	if cfg.GrantCp < 0 {
		return startPointsConfig{}, fmt.Errorf(
			"%s: grant_cp is %d; an opening balance that takes points away is an adjustment, not a "+
				"grant: %w", startPointsID, cfg.GrantCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanDecay is the GRANT RUN: it credits the opening balance to every account with no ledger history,
// debited from the guild bank.
//
// It is on PlanDecay because the cadence is shared, not because a grant is a decay — `decay`, `cap`
// and `start_points` are one table, one lifecycle and one key (ADR-0024). The batch carries kind
// `start_points`, so the member's statement names the rule that credited them even though the run row
// and the planner are shared with the other two families.
//
// ELIGIBILITY IS READ AT run.AsOfSeq, positionally, exactly like a balance. A grant committed while
// this run is planning must not change what this run decided, and re-running the same period must
// reach the same answer.
//
// SYSTEM ACCOUNTS ARE NEVER GRANTED. They are counterparties rather than raiders, and the guild bank
// funding its own opening balance is a batch that nets to zero on one account and means nothing.
func (s StartPoints) PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if cfg.GrantCp == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: grant_cp is 0, so this pool grants no opening balance; a run against it would post "+
				"an empty batch: %w", startPointsID, ErrInvalidConfig)
	}

	if run.PeriodKey == "" {
		return BatchProposal{}, fmt.Errorf(
			"%s: start-points run has no period key, so nothing makes a re-run idempotent: %w",
			startPointsID, ErrInvalidEvent)
	}

	accounts := run.Accounts
	if len(accounts) == 0 {
		accounts, err = ctx.Roster()
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read the roster to grant: %w", startPointsID, err)
		}
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", startPointsID, err)
	}

	targets := sortedAccounts(accounts)
	if err := checkDistinctAccounts(startPointsID, targets); err != nil {
		return BatchProposal{}, err
	}

	credits := make([]EntryProposal, 0, len(targets)+1)

	var total core.Centipoints

	for _, a := range targets {
		if a.IsSystem() {
			continue
		}

		granted, err := ctx.HasHistory(a.ID, BalanceKindDKP, run.AsOfSeq)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read ledger history for account %s at seq %d: %w",
				startPointsID, a.ID, run.AsOfSeq, err)
		}

		// THE ENTIRE IDEMPOTENCY OF THIS STRATEGY IS THIS BRANCH. An account with any history has
		// already been granted, or has earned and spent, or was migrated with an opening balance —
		// all three are "not a new member", and none of them is distinguishable from a balance.
		if granted {
			continue
		}

		sum, ok := addCentipoints(total, cfg.GrantCp)
		if !ok {
			return BatchProposal{}, fmt.Errorf(
				"%s: the grant for period %s sums past int64 at account %s: %w",
				startPointsID, run.PeriodKey, a.ID, ErrInvalidEvent)
		}

		total = sum

		credits = append(credits, EntryProposal{
			AccountID:   a.ID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    cfg.GrantCp,
		})
	}

	if len(credits) == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: every account in period %s already has ledger history, so %w",
			startPointsID, run.PeriodKey, ErrNothingToPlan)
	}

	// The bank's debit leads, so a reader of the batch sees where the points came from before where
	// they went — the same order the attendance batches use.
	entries := append([]EntryProposal{{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -total,
	}}, credits...)

	// NonNegative is not declared: nobody's balance decreases except the guild bank's, and the
	// commit-time engine exempts system accounts from balance floors by design. Declaring a rule that
	// constrains nothing is what .claude/skills/add-strategy/SKILL.md calls a red flag.
	return proposeZeroSum(ctx, startPointsID, startPointsVersion, kinds.KindStartPoints,
		run.EffectiveAt, "start points "+run.PeriodKey, entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		})
}

// PlanAttendance is unsupported: an opening balance is granted once, not per tick. A pool wants this
// rule beside an earn rule, not instead of one.
func (StartPoints) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(startPointsID,
		"credit an attendance tick: it grants an opening balance once, so pair it with tick")
}

// PlanAward is unsupported: a grant prices nothing.
func (StartPoints) PlanAward(Ctx, AwardEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(startPointsID,
		"award an item: it grants an opening balance, so pair it with a spend rule such as fixed_price")
}

// PlanAdjustment moves points between an account and a counterparty; see adjustmentProposal.
func (s StartPoints) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, startPointsID, startPointsVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed; see reversePlan.
//
// REVERSING A GRANT RE-ARMS IT, and that is the correct behaviour rather than a hazard: the reversal
// leaves the account with entries, so HasHistory still reports true and the next run still skips it.
// An officer who reverses a grant in order to re-issue it at a different amount posts the new amount
// as an adjustment, which carries a reason — the grant run is for new members, not for corrections.
func (StartPoints) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, startPointsID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM.
func (StartPoints) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, startPointsID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
func (StartPoints) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, startPointsID, acct)
}

// PriceHint is unsupported: this strategy prices nothing.
func (StartPoints) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(startPointsID, "hint at a price: it grants points and prices nothing")
}

// ValidateBid is unsupported: there are no bids to validate.
func (StartPoints) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(startPointsID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (StartPoints) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(startPointsID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal. The floor
// is the shipped default; each proposal carries the pool's own.
func (StartPoints) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
