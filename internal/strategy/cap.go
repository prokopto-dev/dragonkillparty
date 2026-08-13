package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// cap — a ceiling on hoarding. Phase 1, #193.
//
// The problem it solves is a recruitment problem, not a fairness one: the member who has raided for
// two years outbids everyone forever, nobody else can compete for a drop, and the guild stops being
// able to recruit. Caps are the single most common EQdkp Plus configuration after decay, and the one
// a migrating guild most often finds missing — a guild whose old site had `cap_current` or
// `hardcap_current` set wants this (docs/guides/choosing-a-dkp-system.md).
//
// TWO CAPS, AND THEY ACT AT DIFFERENT MOMENTS. That is the whole design and it is why one strategy
// carries both:
//
//   - THE HARD CAP is enforced when points are EARNED and again on a CADENCE. A balance may not
//     exceed the ceiling: an earn is clamped so it cannot cross it, and a `cap` run trims anything
//     already above it — an import, a correction, or a ceiling an officer lowered yesterday. The trim
//     is POSTED as an explicit batch and is visible on the member's statement, which is what makes
//     "why did my points drop?" answerable by pointing at a row.
//   - THE SOFT CAP is enforced only when points are EARNED, and it does not trim anything. Earnings
//     above it are credited at a reduced ratio — `over_cap_earn_ratio` in the guide — so a veteran
//     keeps earning, slower, instead of hitting a wall. Nothing is confiscated, so nothing needs to
//     be trimmed later.
//
// IDEMPOTENCE IS STRUCTURAL, NOT A FLAG (property P6). A trim run reads each balance at the run's
// as-of seq and emits an entry only for a balance strictly above the ceiling. After one run every
// balance is at or below it, so a second run for the same period finds nobody, plans nothing and
// returns ErrNothingToPlan — which the job records as a `skipped` run rather than a second batch
// (.claude/rules/decay-and-jobs.md §4). Applying the cap twice cannot move a balance past the cap,
// because the second application has nothing to apply.
//
// THE TRIMMED POINTS GO TO THE GUILD BANK rather than nowhere, so every batch this strategy writes
// sums to zero and conservation stays a column comparison. A guild that wants a cap to genuinely
// destroy points wants a strategy whose batches do not sum to zero, and that is a different strategy
// with a different invariant set.
//
// IT IS AN OVER-TIME RULE AND IT DOES NOT SPEND. PlanAward returns ErrUnsupported naming this
// strategy: a ceiling has no price list, and answering with one would be a second copy of
// fixed_price's price resolution. A pool composes it with an earn rule and a spend rule — three
// (strategy, config) pairs on the pool row, one per question, settled by ADR-0026 (#213).
//
// THE COST OF THAT COMPOSITION LANDS HERE, and it is worth naming where it is paid: the over-time
// slot is asked PlanDecay, so a composed pool reaches the CAP RUN and does not reach PlanAttendance's
// soft-cap reduction below. Both halves still ship and both are still tested; the earn-time half
// needs an earn slot this pool has given to its earn rule. #215 is where a pool that wants both is
// tracked.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = Cap{}

// Cap is the ceiling strategy. STATELESS: everything it needs arrives through the Ctx façade.
type Cap struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API.
const (
	capID      = "cap"
	capVersion = "0.1.0"
)

// capConfigSchema is the JSON Schema for the pool config.
//
// Draft 2020-12, `additionalProperties: false`, integers only. There is NO DEFAULT CEILING and there
// deliberately never will be: both caps default to 0, meaning "unset", and a pool running this
// strategy with neither set is refused rather than quietly behaving like a flat tick. A default
// ceiling would be a number nobody chose silently trimming a guild's balances.
const capConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Cap",
  "description": "Balances cannot exceed a ceiling. A hard cap trims the excess; a soft cap reduces what is earned above it.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "hard_cap_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "Hard cap (centipoints)",
      "description": "A balance may not exceed this. Earnings are clamped at it and a cap run trims anything already above it. 0 means no hard cap."
    },
    "soft_cap_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "Soft cap (centipoints)",
      "description": "Earnings above this are credited at the over-cap ratio instead of in full. Nothing is trimmed. 0 means no soft cap."
    },
    "over_cap_earn_bp": {
      "type": "integer",
      "minimum": 0,
      "maximum": 10000,
      "default": 0,
      "title": "Over-cap earn ratio (basis points)",
      "description": "What a point earned above the soft cap is worth. 2500 is a quarter. 0 means earnings stop at the soft cap."
    },
    "tick_award_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Points per raid tick (centipoints)",
      "description": "What one attendance tick is worth before the caps are applied. 100 centipoints is 1.00 point."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An adjustment or a trim is rejected if it would take an account below this."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes — a copy, so no caller can change what
// every pool validates against.
func (Cap) ConfigSchema() []byte { return []byte(capConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (Cap) ID() string { return capID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (Cap) Version() string { return capVersion }

// RuleKind is over_time: this strategy's ledger-writing run is the CAP RUN, a cadence family that
// shares the decay_run table and the (pool_id, kind, cadence_period) key with decay and start_points
// (ADR-0024). The guide's catalogue lists it under "over time" for the same reason.
func (Cap) RuleKind() RuleKind { return RuleOverTime }

// BalanceKinds is the one balance kind this strategy moves.
func (Cap) BalanceKinds() []string { return []string{BalanceKindDKP} }

// capConfig is the parsed pool config; the JSON tags are the schema's property names.
type capConfig struct {
	HardCapCp     core.Centipoints `json:"hard_cap_cp"`
	SoftCapCp     core.Centipoints `json:"soft_cap_cp"`
	OverCapEarnBp int64            `json:"over_cap_earn_bp"`
	TickAwardCp   core.Centipoints `json:"tick_award_cp"`
	FloorCp       core.Centipoints `json:"floor_cp"`
}

// defaultCapConfig is the config the pool's JSON is decoded OVER. Both ceilings are 0 — unset — and
// validateCapConfig refuses that, so this default is deliberately NOT a runnable configuration. See
// the schema comment: a default ceiling would be a number nobody chose, trimming a guild's balances.
func defaultCapConfig() capConfig {
	return capConfig{
		HardCapCp:     0,
		SoftCapCp:     0,
		OverCapEarnBp: 0,
		TickAwardCp:   100,
		FloorCp:       0,
	}
}

// config parses and validates the pool's config, re-validating what the API edge already checked
// because a config can also arrive from the importer, a backfill or a test.
func (Cap) config(ctx Ctx) (capConfig, error) {
	cfg := defaultCapConfig()

	if err := decodeConfig(capID, ctx.ConfigJSON(), &cfg); err != nil {
		return capConfig{}, err
	}

	return validateCapConfig(cfg)
}

// validateCapConfig applies the bounds the schema declares plus the three relationships between
// knobs that no per-field bound can express.
//
// Each of the three is a configuration that PARSES and then means nothing, or means something the
// officer did not intend:
//
//   - NEITHER CAP SET. The pool runs the cap strategy and caps nothing — the settings page says
//     "Cap", the standings page shows an uncapped veteran, and no error is ever raised. A guild that
//     wants a flat tick wants `tick`.
//   - A SOFT CAP ABOVE THE HARD CAP. The hard cap clamps first, so the soft cap's reduced ratio can
//     never apply: the knob is inert and the officer believes it is working.
//   - AN OVER-CAP RATIO WITH NO SOFT CAP. The ratio applies to earnings above a threshold that does
//     not exist. It is the same defect seen from the other side.
func validateCapConfig(cfg capConfig) (capConfig, error) {
	if cfg.TickAwardCp <= 0 {
		return capConfig{}, fmt.Errorf("%s: tick_award_cp is %d, which awards nothing: %w",
			capID, cfg.TickAwardCp, ErrInvalidConfig)
	}

	if cfg.HardCapCp < 0 {
		return capConfig{}, fmt.Errorf("%s: hard_cap_cp is %d, which is negative: %w",
			capID, cfg.HardCapCp, ErrInvalidConfig)
	}

	if cfg.SoftCapCp < 0 {
		return capConfig{}, fmt.Errorf("%s: soft_cap_cp is %d, which is negative: %w",
			capID, cfg.SoftCapCp, ErrInvalidConfig)
	}

	if cfg.OverCapEarnBp < 0 || cfg.OverCapEarnBp > basisPointsWhole {
		return capConfig{}, fmt.Errorf("%s: over_cap_earn_bp is %d, want 0..%d: %w",
			capID, cfg.OverCapEarnBp, basisPointsWhole, ErrInvalidConfig)
	}

	if cfg.HardCapCp == 0 && cfg.SoftCapCp == 0 {
		return capConfig{}, fmt.Errorf(
			"%s: hard_cap_cp and soft_cap_cp are both 0, so this pool caps nothing; set a ceiling or "+
				"run the tick strategy: %w", capID, ErrInvalidConfig)
	}

	if cfg.HardCapCp > 0 && cfg.SoftCapCp > cfg.HardCapCp {
		return capConfig{}, fmt.Errorf(
			"%s: soft_cap_cp %d is above hard_cap_cp %d, so the reduced ratio could never apply — the "+
				"hard cap clamps first: %w", capID, cfg.SoftCapCp, cfg.HardCapCp, ErrInvalidConfig)
	}

	if cfg.OverCapEarnBp > 0 && cfg.SoftCapCp == 0 {
		return capConfig{}, fmt.Errorf(
			"%s: over_cap_earn_bp is %d with no soft_cap_cp, so the ratio applies to nothing: %w",
			capID, cfg.OverCapEarnBp, ErrInvalidConfig)
	}

	if cfg.HardCapCp > 0 && cfg.FloorCp > cfg.HardCapCp {
		return capConfig{}, fmt.Errorf(
			"%s: floor_cp %d is above hard_cap_cp %d, so a trim would land every balance below the "+
				"floor and every run would be rejected: %w",
			capID, cfg.FloorCp, cfg.HardCapCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAttendance credits every attendee the tick award, reduced above the soft cap and clamped at the
// hard cap, debited from the guild bank.
//
// Balances are read at the POOL HEAD, which is the seq this batch will be one past — the only
// meaningful "before" for a batch about to be written. A trim run reads at its own as-of seq instead,
// because a run is a snapshot of a period rather than of now.
//
// AN ATTENDEE AT OR ABOVE THE HARD CAP EARNS NOTHING AND GETS NO ENTRY, rather than an entry of zero:
// ledger_entry carries CHECK (amount_cp <> 0). Their attendance is still recorded — the raid_tick and
// raid_attendance rows are facts about the night, and this batch is only about the money.
//
// NOTHING IS ROUTED ANYWHERE WHEN AN EARN IS REDUCED. The points were never credited, so nothing was
// taken: the batch debits the bank exactly what it credits the raiders, and the guild simply awards
// less. That is the difference between a cap on EARNING and a trim, and it is why the two cannot be
// the same code path.
func (s Cap) PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if len(ev.Attendees) == 0 {
		return BatchProposal{}, fmt.Errorf("%s: attendance tick has no attendees: %w",
			capID, ErrInvalidEvent)
	}

	amount := cfg.TickAwardCp
	if ev.AmountCp != nil {
		amount = *ev.AmountCp
	}

	if amount <= 0 {
		return BatchProposal{}, fmt.Errorf("%s: tick award is %d centipoints, which awards nothing: %w",
			capID, amount, ErrInvalidEvent)
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", capID, err)
	}

	attendees := sortedShares(ev.Attendees)
	if err := checkDistinctShares(capID, attendees); err != nil {
		return BatchProposal{}, err
	}

	credits := make([]EntryProposal, 0, len(attendees)+1)

	var total core.Centipoints

	for _, a := range attendees {
		if err := checkShare(capID, a); err != nil {
			return BatchProposal{}, err
		}

		gross, ok := mulCentipoints(amount, a.Weight)
		if !ok {
			return BatchProposal{}, fmt.Errorf(
				"%s: %d centipoints at weight %d for account %s overflows int64: %w",
				capID, amount, a.Weight, a.AccountID, ErrInvalidEvent)
		}

		balance, err := ctx.Balance(a.AccountID, BalanceKindDKP, ctx.HeadSeq())
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read balance for account %s at seq %d: %w",
				capID, a.AccountID, ctx.HeadSeq(), err)
		}

		earned, err := cfg.earnable(balance, gross)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: account %s: %w", capID, a.AccountID, err)
		}

		if earned == 0 {
			continue
		}

		sum, ok := addCentipoints(total, earned)
		if !ok {
			return BatchProposal{}, fmt.Errorf("%s: the tick's credits sum past int64: %w",
				capID, ErrInvalidEvent)
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
			"%s: every attendee is already at the cap or earns 0, so %w", capID, ErrNothingToPlan)
	}

	entries := append([]EntryProposal{{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -total,
		RaidID:      ev.RaidID,
		TickID:      ev.TickID,
	}}, credits...)

	// NonNegative is not declared: the only debited account is the guild bank, which the commit-time
	// engine exempts from balance floors by design.
	return proposeZeroSum(ctx, capID, capVersion, kinds.KindAttendance, ev.EffectiveAt, ev.Reason,
		entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		})
}

// earnable is the cap arithmetic: what an account on `balance` may actually receive of a `gross`
// earning.
//
// The soft cap applies FIRST and the hard cap clamps the result, which is the only order that makes
// both knobs mean what the settings page says. In integers, floored:
//
//	room  = soft_cap - balance                       (0 when already at or above the soft cap)
//	net   = min(gross, room) + (gross - min(gross, room)) * over_cap_earn_bp / 10000
//	net   = min(net, hard_cap - balance)             (0 when already at or above the hard cap)
//
// A NEGATIVE BALANCE EARNS IN FULL up to the soft cap, which falls out of the subtraction rather than
// being a special case: an account at -200 with a soft cap of 1000 has 1200 of room, and that is the
// correct answer — the cap is a ceiling on a balance, not a budget for a period.
//
// Every step is overflow-checked. A wrapped credit is the one arithmetic failure that satisfies every
// declared invariant, because the bank's debit wraps with it.
func (cfg capConfig) earnable(balance, gross core.Centipoints) (core.Centipoints, error) {
	net := gross

	if cfg.SoftCapCp > 0 {
		room, ok := subCentipoints(cfg.SoftCapCp, balance)
		if !ok {
			return 0, fmt.Errorf("balance %d against soft cap %d overflows int64: %w",
				balance, cfg.SoftCapCp, ErrInvalidEvent)
		}

		if room < 0 {
			room = 0
		}

		if gross > room {
			reduced, ok := scaleByBasisPoints(gross-room, cfg.OverCapEarnBp)
			if !ok {
				return 0, fmt.Errorf("%d centipoints above the soft cap at %d bp overflows int64: %w",
					gross-room, cfg.OverCapEarnBp, ErrInvalidEvent)
			}

			net, ok = addCentipoints(room, reduced)
			if !ok {
				return 0, fmt.Errorf("%d plus %d centipoints overflows int64: %w",
					room, reduced, ErrInvalidEvent)
			}
		}
	}

	if cfg.HardCapCp > 0 {
		headroom, ok := subCentipoints(cfg.HardCapCp, balance)
		if !ok {
			return 0, fmt.Errorf("balance %d against hard cap %d overflows int64: %w",
				balance, cfg.HardCapCp, ErrInvalidEvent)
		}

		if headroom < 0 {
			headroom = 0
		}

		if net > headroom {
			net = headroom
		}
	}

	return net, nil
}

// PlanDecay is the CAP RUN: it trims every balance above the hard cap and credits the excess to the
// guild bank.
//
// It is on PlanDecay because the cadence is the decay family's, not because a cap is a decay:
// `decay`, `cap` and `start_points` share one cadence vocabulary, one `decay_run` table keyed
// `(pool_id, kind, cadence_period)` and one preview/commit lifecycle (ADR-0024,
// .claude/rules/decay-and-jobs.md). The batch it emits carries kind `cap`, so a member's statement
// says which rule moved their points even though the run row and the planner are shared.
//
// BALANCES ARE READ AT run.AsOfSeq — positionally, never temporally. A batch committed while this run
// is planning must not change what it trimmed, and a backdated effective_at must not change what a
// past balance was.
//
// SYSTEM ACCOUNTS ARE NEVER TRIMMED. The guild bank is structurally negative by design — it funds
// every tick — and trimming a system account would mean capping the counterparty that makes the
// batch balance.
//
// A SOFT CAP DOES NOT TRIM, so a pool with only a soft cap has no run to make: the refusal names the
// knob rather than posting an empty batch.
func (s Cap) PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if cfg.HardCapCp == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: hard_cap_cp is 0, so this pool trims nothing; a soft cap reduces what is earned and "+
				"a run against it would post an empty batch: %w", capID, ErrInvalidConfig)
	}

	if run.PeriodKey == "" {
		return BatchProposal{}, fmt.Errorf(
			"%s: cap run has no period key, so nothing makes a re-run idempotent: %w",
			capID, ErrInvalidEvent)
	}

	accounts := run.Accounts
	if len(accounts) == 0 {
		accounts, err = ctx.Roster()
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read the roster to trim: %w", capID, err)
		}
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", capID, err)
	}

	targets := sortedAccounts(accounts)
	if err := checkDistinctAccounts(capID, targets); err != nil {
		return BatchProposal{}, err
	}

	debits := make([]EntryProposal, 0, len(targets)+1)

	var total core.Centipoints

	for _, a := range targets {
		if a.IsSystem() {
			continue
		}

		balance, err := ctx.Balance(a.ID, BalanceKindDKP, run.AsOfSeq)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read balance for account %s at seq %d: %w",
				capID, a.ID, run.AsOfSeq, err)
		}

		// STRICTLY above: a balance exactly at the ceiling is already compliant, and an entry of zero
		// is illegal anyway. This is also what makes a second run for the same period a no-op — after
		// one run every balance is at or below the cap, so the next finds nobody. Property P6.
		if balance <= cfg.HardCapCp {
			continue
		}

		excess := balance - cfg.HardCapCp

		sum, ok := addCentipoints(total, excess)
		if !ok {
			return BatchProposal{}, fmt.Errorf(
				"%s: the trim for period %s sums past int64 at account %s: %w",
				capID, run.PeriodKey, a.ID, ErrInvalidEvent)
		}

		total = sum

		debits = append(debits, EntryProposal{
			AccountID:   a.ID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    -excess,
		})
	}

	if len(debits) == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: no account in period %s is above the cap of %d, so %w",
			capID, run.PeriodKey, cfg.HardCapCp, ErrNothingToPlan)
	}

	entries := append(debits, EntryProposal{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    total,
	})

	return proposeZeroSum(ctx, capID, capVersion, kinds.KindCap, run.EffectiveAt,
		"cap "+run.PeriodKey, entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
			{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
		})
}

// PlanAward is unsupported: a ceiling prices nothing. See the file header on composition.
func (Cap) PlanAward(Ctx, AwardEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(capID,
		"award an item: it is a ceiling on balances, so pair it with a spend rule such as fixed_price")
}

// PlanAdjustment moves points between an account and a counterparty.
//
// IT IS NOT CAPPED, and that is deliberate. An adjustment is an officer correcting a number they know
// to be wrong — often a correction of this very strategy's own arithmetic — and clamping it would
// mean the ledger silently refusing to record what the officer decided. The cap run is what brings a
// balance back under the ceiling, in a batch that says so.
func (s Cap) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, capID, capVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed; see reversePlan.
//
// REVERSING A TRIM GIVES THE POINTS BACK, above the cap. That is correct: the reversal says the trim
// should not have happened, and the next cap run will trim again if the ceiling still applies. A
// reversal that re-clamped would not be a reversal.
func (Cap) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, capID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM.
//
// NOT CLAMPED TO THE CAP. The cap is posted as batches, so a capped balance is already the sum; a
// Spendable that also clamped would hide the exact case the trim run exists to correct — a balance
// that is above the ceiling right now, because the ceiling was lowered an hour ago and the run has
// not fired. The member would see one number and the ledger would hold another.
func (Cap) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, capID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
func (Cap) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, capID, acct)
}

// PriceHint is unsupported: this strategy prices nothing.
func (Cap) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(capID, "hint at a price: it is a ceiling on balances and prices nothing")
}

// ValidateBid is unsupported: there are no bids to validate.
func (Cap) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(capID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (Cap) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(capID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal. The floor
// is the shipped default; each proposal carries the pool's own.
func (Cap) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
