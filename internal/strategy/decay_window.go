package strategy

import (
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// decay_window — earnings expire. Phase 1, #194.
//
// The friendlier of the two decays and the one most P99 guilds actually want: nothing compounds, and
// points simply stop counting once they are older than the window. A member who raided hard for six
// months and then took a break is not punished twice, which matters on a server where the content is
// finite (docs/guides/choosing-a-dkp-system.md).
//
// IT IS THE EXCEPTION THAT PROVES THE RULE, and it is the one most likely to be got wrong.
// ".claude/rules/decay-and-jobs.md" §1 says it outright: "earnings older than the window stop
// counting" still means POSTING A BATCH THAT REMOVES THEM, not filtering the sum. The window is an
// input to the planner and never a predicate in a balance query. A `WHERE effective_at > ?` in the
// balance read would be cheaper, and it would make a member's balance stop being the sum of their own
// statement — which is the one property the whole product is built on.
//
// WHAT AGES OUT IS A SLICE OF THE LOG, AND THE RUN CARRIES IT. Each run expires the earnings in
// `(Window.FromSeq, Window.ToSeq]` — the piece of the pool's history that crossed the window boundary
// since the previous run. Consecutive runs tile the log, so every earning falls in exactly one slice
// and expires exactly once. The scheduler resolves "90 days" into those two positions when it creates
// the run, because mapping wall-clock time onto a position in the log is a query and this package may
// not run one (law 3).
//
// AND IT ASKS FOR EARNINGS RATHER THAN A BALANCE DELTA, which is the single decision in this file
// that a reviewer should check hardest. `Balance(to) − Balance(from)` looks like the same number and
// is not: it nets in every DEBIT in the slice, including this strategy's own expiry batches. Run k
// posts a debit; some weeks later that debit ages into run k+n's slice; the slice nets to nothing;
// the earnings that should have expired never do — and the balance ratchets upward, silently, by an
// amount nobody can point at a row for. `Ctx.EarnedBetween` sums POSITIVE entries only, so an expiry
// can never appear in a later slice and no earning can expire twice. That is a structural property,
// asserted by TestProperty_DecayWindow_AnEarningExpiresAtMostOnce rather than trusted.
//
// A SPEND IS NOT AN EXPIRY AND DOES NOT REDUCE ONE. The rule is about how old an EARNING is; what a
// member did with the points afterwards is a different fact. What stops the two from compounding into
// a debt is the clamp: a run never takes an account below the floor, and never takes more than the
// account actually holds. A member who earned 500 a year ago and spent every point of it has nothing
// left for the window to expire, and gets no entry.
//
// THE POINTS GO TO THE BANK, so every batch sums to zero and conservation stays a column comparison —
// the same argument decay_percent, cap and start_points make.
//
// IDEMPOTENCY IS THE KEY PLUS THE SNAPSHOT. Every read is positional — the slice at its two
// boundaries, the balance at run.AsOfSeq — so a second run for the same period reads exactly what the
// first read and proposes exactly the batch the first proposed, byte for byte. The ledger's
// (pool_id, kind, cadence_period) key then collapses the two into one committed batch
// (.claude/rules/decay-and-jobs.md §3).
//
// WHAT IS NOT HERE: THE LINEAR TAPER. The guide offers one as an alternative to the hard cutoff, and
// it needs to know how old each earning is rather than merely which side of the boundary it fell on —
// a per-entry ageing the run's two boundaries cannot express. It is issue #221 rather than a guess:
// inventing a taper would silently redistribute points across a whole roster, and the ledger is
// append-only, so the guess would be permanent.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = DecayWindow{}

// DecayWindow is the expiring-earnings strategy. STATELESS: everything it needs arrives through the
// Ctx façade and the run.
type DecayWindow struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API.
const (
	decayWindowID      = "decay_window"
	decayWindowVersion = "0.1.0"
)

// maxWindowDays is ten years, the longest window that is a window rather than a way of saying
// "never". It is the same bound internal/swap puts on its attendance window, for the same reason: an
// unbounded day count is a number a typo can turn into a rule nobody notices is inert.
const maxWindowDays = 3_650

// ID is the permanent identifier written onto every batch this strategy plans.
func (DecayWindow) ID() string { return decayWindowID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (DecayWindow) Version() string { return decayWindowVersion }

// RuleKind is over_time: its only ledger-writing planner is the cadence run, keyed
// (pool_id, kind, cadence_period) in the decay_run table beside cap and start_points (ADR-0024,
// ADR-0026).
func (DecayWindow) RuleKind() RuleKind { return RuleOverTime }

// BalanceKinds is the one balance kind this strategy moves.
func (DecayWindow) BalanceKinds() []string { return []string{BalanceKindDKP} }

// decayWindowConfigSchema is the JSON Schema for the pool config.
//
// `window_days` HAS NO DEFAULT WORTH SHIPPING, so it defaults to 0 and a pool that leaves it there is
// refused: the right window depends entirely on how often a guild raids and how long its content
// takes, and a number nobody chose would silently expire a roster's earnings on a schedule nobody
// agreed to.
//
// IT IS READ BY THE SCHEDULER RATHER THAN BY THE ARITHMETIC BELOW, and it still belongs here. A knob
// a guild can turn lives in the strategy's schema and nowhere else (.claude/skills/add-strategy),
// because the settings form, the validation at the API edge and the config snapshot on every batch
// are all generated from this document. What the planner does with it is REFUSE A RUN RESOLVED FROM A
// DIFFERENT WINDOW — the run carries the days it was resolved from, and a mismatch means a run
// planned under a config that is no longer in force.
const decayWindowConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Window decay",
  "description": "Earnings older than the window stop counting, posted as an explicit batch that removes them.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "window_days": {
      "type": "integer",
      "minimum": 0,
      "maximum": 3650,
      "default": 0,
      "title": "How long an earning counts (days)",
      "description": "90 is the common choice. Earnings older than this stop counting and are removed by the run that sees them cross the boundary. 0 is refused: a window of no days would expire everything."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Balance expiry stops at (centipoints)",
      "description": "A run never takes an account past this. Expiring earnings a member has already spent would otherwise push them into debt."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes — a copy, so no caller can change what every
// pool validates against.
func (DecayWindow) ConfigSchema() []byte { return []byte(decayWindowConfigSchema) }

// decayWindowConfig is the parsed pool config; the JSON tags are the schema's property names.
type decayWindowConfig struct {
	WindowDays int64            `json:"window_days"`
	FloorCp    core.Centipoints `json:"floor_cp"`
}

// defaultDecayWindowConfig is the config the pool's JSON is decoded OVER. It is deliberately NOT a
// runnable configuration: `window_days` is 0 and validateDecayWindowConfig refuses that.
func defaultDecayWindowConfig() decayWindowConfig {
	return decayWindowConfig{WindowDays: 0, FloorCp: 0}
}

// config parses and validates the pool's config.
func (DecayWindow) config(ctx Ctx) (decayWindowConfig, error) {
	cfg := defaultDecayWindowConfig()

	if err := decodeConfig(decayWindowID, ctx.ConfigJSON(), &cfg); err != nil {
		return decayWindowConfig{}, err
	}

	return validateDecayWindowConfig(cfg)
}

// validateDecayWindowConfig applies the bounds the schema declares. A window of zero days is refused
// rather than treated as "no decay": a pool that does not expire earnings leaves its over-time rule
// unset, which ADR-0026 makes a first-class answer.
func validateDecayWindowConfig(cfg decayWindowConfig) (decayWindowConfig, error) {
	if cfg.WindowDays <= 0 {
		return decayWindowConfig{}, fmt.Errorf(
			"%s: window_days is %d, so every earning would be older than the window; a pool that does "+
				"not expire earnings leaves its over-time rule unset: %w",
			decayWindowID, cfg.WindowDays, ErrInvalidConfig)
	}

	if cfg.WindowDays > maxWindowDays {
		return decayWindowConfig{}, fmt.Errorf("%s: window_days is %d, want 1..%d: %w",
			decayWindowID, cfg.WindowDays, maxWindowDays, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanDecay is the WINDOW RUN: it expires the earnings in the slice that has just aged out and credits
// them to the guild bank.
//
// It is on PlanDecay because the cadence is the decay family's: `decay`, `cap` and `start_points`
// share one cadence vocabulary, one `decay_run` table keyed (pool_id, kind, cadence_period) and one
// preview/commit lifecycle (ADR-0024). The batch carries kind `decay`, like decay_percent's, because
// what a member's statement needs to say is that time removed these points; WHICH decay rule did it is
// `ledger_batch.strategy_id`, and a reversal routes on that.
//
// EVERY READ IS POSITIONAL — the slice at its two boundaries, the balance at run.AsOfSeq. See the file
// header for why that, and not a flag, is what makes a re-run a no-op.
func (s DecayWindow) PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if run.PeriodKey == "" {
		return BatchProposal{}, fmt.Errorf(
			"%s: window run has no period key, so nothing makes a re-run idempotent: %w",
			decayWindowID, ErrInvalidEvent)
	}

	if err := cfg.checkWindow(run); err != nil {
		return BatchProposal{}, err
	}

	targets, bank, err := cadenceTargets(ctx, decayWindowID, "expire", run)
	if err != nil {
		return BatchProposal{}, err
	}

	entries := make([]EntryProposal, 0, len(targets)+1)

	var total core.Centipoints

	for _, a := range targets {
		// System accounts hold no earnings that age: the guild bank is the counterparty that funds
		// every tick and receives every expiry, and expiring its balance would expire the other side
		// of this very batch.
		if a.IsSystem() {
			continue
		}

		amount, err := s.expired(ctx, cfg, a, run)
		if err != nil {
			return BatchProposal{}, err
		}

		// Nothing earned in the slice, nothing left to take it from, or an account already at the
		// floor. Dropped rather than written as a zero — ledger_entry carries CHECK (amount_cp <> 0).
		if amount == 0 {
			continue
		}

		sum, ok := addCentipoints(total, amount)
		if !ok {
			return BatchProposal{}, fmt.Errorf(
				"%s: the expiry for period %s sums past int64 at account %s: %w",
				decayWindowID, run.PeriodKey, a.ID, ErrInvalidEvent)
		}

		total = sum

		entries = append(entries, EntryProposal{
			AccountID:   a.ID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    -amount,
		})
	}

	if len(entries) == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: nothing earned between seq %d and seq %d is still on the books in period %s, so %w",
			decayWindowID, run.Window.FromSeq, run.Window.ToSeq, run.PeriodKey, ErrNothingToPlan)
	}

	entries = append(entries, EntryProposal{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    total,
	})

	// NonNegative is declared even though `expired` already clamps at the floor: the planner reads
	// balances at run.AsOfSeq and the engine checks them at the commit head, so a spend that landed
	// between the two is caught by the invariant and by nothing else.
	return proposeZeroSum(ctx, decayWindowID, decayWindowVersion, kinds.KindDecay, run.EffectiveAt,
		"decay "+run.PeriodKey, entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
			{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
		})
}

// checkWindow rejects a run whose aged-out slice this pool's config cannot have produced.
//
// Every rejection here is a scheduler bug that would otherwise be written into an append-only table:
//
//   - NO WINDOW AT ALL. The zero value is a slice bounded at seq 0, which would expire the whole of
//     history the first time anybody ran a window pool's decay by hand.
//   - A WINDOW RESOLVED FROM A DIFFERENT CONFIG. A run planned yesterday under 90 days and committed
//     today under 30 expires the wrong earnings; the config a run used must be the config it
//     snapshotted (.claude/rules/decay-and-jobs.md §7).
//   - A SLICE THAT RUNS BACKWARDS, or one that reaches past the snapshot the run reads. The second is
//     the subtle one: expiring earnings from beyond AsOfSeq would take points the run cannot see and
//     therefore cannot have checked against a balance.
func (cfg decayWindowConfig) checkWindow(run DecayRun) error {
	if run.Window == nil {
		return fmt.Errorf(
			"%s: window run for period %s carries no expiry window; the scheduler resolves "+
				"window_days into the slice of the log that has aged out, and a planner cannot: %w",
			decayWindowID, run.PeriodKey, ErrInvalidEvent)
	}

	w := run.Window

	if w.Days != cfg.WindowDays {
		return fmt.Errorf(
			"%s: the run was resolved from a %d-day window and this pool is configured for %d; a run "+
				"planned under a config that is no longer in force expires the wrong earnings: %w",
			decayWindowID, w.Days, cfg.WindowDays, ErrInvalidEvent)
	}

	if w.FromSeq < 0 || w.ToSeq < 0 {
		return fmt.Errorf("%s: the expiry window (%d, %d] has a negative bound: %w",
			decayWindowID, w.FromSeq, w.ToSeq, ErrInvalidEvent)
	}

	if w.FromSeq > w.ToSeq {
		return fmt.Errorf(
			"%s: the expiry window (%d, %d] runs backwards, so it names no slice of the log: %w",
			decayWindowID, w.FromSeq, w.ToSeq, ErrInvalidEvent)
	}

	if w.ToSeq > run.AsOfSeq {
		return fmt.Errorf(
			"%s: the expiry window ends at seq %d, past the run's as-of seq %d; a run cannot expire "+
				"earnings it does not read: %w",
			decayWindowID, w.ToSeq, run.AsOfSeq, ErrInvalidEvent)
	}

	return nil
}

// expired is what one account loses to this run: the earnings in the aged-out slice, clamped by what
// the account actually still holds above the floor.
//
//	earned    = credits in (Window.FromSeq, Window.ToSeq]
//	available = balance at AsOfSeq − floor
//	expired   = min(earned, available), and 0 when either is not positive
//
// THE CLAMP IS NOT A ROUNDING DETAIL. Earnings and spends are different facts and this strategy
// expires only the first, so an account that spent last year's points has less on the books than the
// slice says it earned — without the clamp the run would push it into debt for money it no longer
// has, and with a floor above zero it would push it below a bound the invariant then rejects, losing
// the whole period's expiry for every other member too.
//
// WHAT THE CLAMP GIVES UP is a carry-forward: the part of a slice that could not be taken is not
// remembered and does not reappear next period. That is deliberate. The window is a rule about the
// AGE of an earning, not a debt an account owes the pool, and a carried-forward remainder would need
// state that the ledger — where the only durable facts are entries — has nowhere to put.
func (DecayWindow) expired(
	ctx Ctx, cfg decayWindowConfig, a AccountRef, run DecayRun,
) (core.Centipoints, error) {
	earned, err := ctx.EarnedBetween(a.ID, BalanceKindDKP, run.Window.FromSeq, run.Window.ToSeq)
	if err != nil {
		return 0, fmt.Errorf("%s: read what account %s earned in (%d, %d]: %w",
			decayWindowID, a.ID, run.Window.FromSeq, run.Window.ToSeq, err)
	}

	if earned <= 0 {
		return 0, nil
	}

	balance, err := ctx.Balance(a.ID, BalanceKindDKP, run.AsOfSeq)
	if err != nil {
		return 0, fmt.Errorf("%s: read balance for account %s at seq %d: %w",
			decayWindowID, a.ID, run.AsOfSeq, err)
	}

	available, ok := subCentipoints(balance, cfg.FloorCp)
	if !ok {
		return 0, fmt.Errorf("%s: account %s: balance %d against floor %d overflows int64: %w",
			decayWindowID, a.ID, balance, cfg.FloorCp, ErrInvalidEvent)
	}

	if available <= 0 {
		return 0, nil
	}

	return min(earned, available), nil
}

// PlanAttendance is unsupported: an expiry is not a tick. A pool wants this rule beside an earn rule,
// not instead of one.
func (DecayWindow) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(decayWindowID,
		"credit an attendance tick: it expires old earnings, so pair it with an earn rule such as tick")
}

// PlanAward is unsupported: a window prices nothing.
func (DecayWindow) PlanAward(Ctx, AwardEvent) (BatchProposal, error) {
	return BatchProposal{}, Unsupported(decayWindowID,
		"award an item: it expires old earnings, so pair it with a spend rule such as fixed_price")
}

// PlanAdjustment moves points between an account and a counterparty; see adjustmentProposal.
//
// AN ADJUSTMENT IS AN EARNING, in the only sense this strategy cares about: a positive one lands in
// whichever slice its seq falls into and expires with everything else earned that period. An officer
// who means to grant points that never expire is describing a different rule, and should say so with
// a pool whose over-time slot is empty.
func (s DecayWindow) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, decayWindowID, decayWindowVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed; see reversePlan.
//
// REVERSING AN EXPIRY GIVES THE POINTS BACK AND THEY STAY BACK. The reversal is a credit, so the
// restored points are themselves an earning in the slice the reversal lands in — and that slice ages
// out one window later, which is the right answer rather than a loophole: an officer who reverses a
// wrong expiry today has restored points that are now as old as the day they were restored, and the
// alternative is a reversal that the next run silently undoes.
func (DecayWindow) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, decayWindowID, b)
}

// Spendable is the account's balance at the pool head — a plain SUM.
//
// NO WINDOW FILTER, and this is the method the whole design exists to keep honest. Expiry is POSTED,
// so the earnings that stopped counting are already gone from the sum; a Spendable that also filtered
// by age would apply the window twice and make a member's balance stop being the total of their own
// statement (.claude/rules/decay-and-jobs.md §1).
func (DecayWindow) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, decayWindowID, acct)
}

// Priority ranks candidates by spendable balance, tie-broken on the account id, ascending.
func (DecayWindow) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	return priorityBySpendable(ctx, decayWindowID, acct)
}

// PriceHint is unsupported: this strategy prices nothing.
func (DecayWindow) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(decayWindowID,
		"hint at a price: it expires old earnings and prices nothing")
}

// ValidateBid is unsupported: there are no bids to validate.
func (DecayWindow) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(decayWindowID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (DecayWindow) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(decayWindowID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal. The floor
// is the shipped default; each proposal carries the pool's own.
func (DecayWindow) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
