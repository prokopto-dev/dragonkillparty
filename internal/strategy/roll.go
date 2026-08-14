package strategy

import (
	"fmt"
	"sort"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// roll — /random, with a seed nobody can argue with. Phase 1, #195.
//
// Every eligible raider is entered, the platform rolls once for each over the configured range, and
// the highest roll wins. It is the loot system a guild falls back to for an item nobody has points
// for, and on P99 it is what happens in guild chat forty times a night.
//
// THE SEED IS THE WHOLE PRODUCT HERE. A roll called in chat is a number three people remember
// differently by Wednesday; a roll this strategy makes is drawn from the injected seeded Rng, and the
// seed lands on the resolution and on the award batch — so "why did Tankguy get it?" is answered by
// replaying the roll rather than by asking who was watching. That is the same argument
// .claude/rules/ledger-and-strategy.md makes for the tie-break coin flip, and it is why a strategy
// may not reach for math/rand (gate PURE002): a roll nobody can reproduce is a roll nobody can
// defend.
//
// A TIE AWARDS NOBODY, DELIBERATELY. docs/guides/choosing-a-dkp-system.md: "Rolls are immutable; a
// re-roll on a tie is a new round, not an edit." Two raiders on 97 means a new session between those
// two, and the Resolution type provides for exactly this — "a strategy that ties deliberately (a
// roll-off pending) must be able to say so by returning none". Breaking the tie with a second roll
// inside the same settlement would be an edit of an immutable round, and the losing raider would
// never see the roll that beat them.
//
// WINNING MAY COST SOMETHING. `win_cost_cp` is 0 by default — a plain roll pool moves no points at
// all and posts no batch, which is the honest outcome rather than an award of nothing. Set it to one
// point and this is the `+1` system the guide describes: the counter is the balance, a win debits it,
// and the pool's earn rule credits back whatever the guild decided attendance is worth.
//
// THE ROLL IS ALWAYS SERVER-SIDE. A `/random` line parsed out of an officer's log is real provenance
// and it belongs on the award record that internal/parse and Phase 3 write — not here, because a
// planner handed a number it did not generate can neither replay it nor tell a typo from a roll.

// The compile-time proof that the implementation matches the interface.
var _ PointStrategy = Roll{}

// Roll is the random-winner spend strategy. STATELESS: everything it needs, including its randomness,
// arrives through the Ctx façade.
type Roll struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	rollID      = "roll"
	rollVersion = "0.1.0"
)

// maxRollFace is the largest roll a pool may configure.
//
// A BOUND RATHER THAN NO BOUND, for two reasons that point the same way. Rng.IntN takes an `int`, and
// `int` is 32 bits on linux/arm/v7 — the older Raspberry Pis a good half of this audience runs on —
// so an unbounded range is a range that does not compile the same on every platform this ships to.
// And a face of a million is already four orders of magnitude past `/random 1 1000`, so a config
// asking for more is an officer who added zeroes rather than a guild with an unusual rule.
const maxRollFace = 1_000_000

// rollConfigSchema is the JSON Schema for the pool config.
//
// Draft 2020-12, `additionalProperties: false`, money as INTEGER `_cp`. The two face values are plain
// integers rather than centipoints and are named accordingly: a die face is not money, and suffixing
// it `_cp` would invite somebody to compare it against a balance.
const rollConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Roll",
  "description": "Every entrant gets one seeded server-side roll. The highest wins; a tie awards nobody and calls for a new round.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "roll_min": {
      "type": "integer",
      "minimum": 0,
      "maximum": 1000000,
      "default": 1,
      "title": "Lowest roll",
      "description": "The bottom of the range, matching the /random your guild calls in chat."
    },
    "roll_max": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1000000,
      "default": 100,
      "title": "Highest roll",
      "description": "The top of the range. A wider range ties less often, and a tie is a new round rather than a re-roll."
    },
    "win_cost_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "What winning costs (centipoints)",
      "description": "0 is a free roll and posts no ledger batch at all. 100 centipoints makes this a +1 system, where the balance is the counter of items won."
    },
    "proceeds": {
      "type": "string",
      "enum": ["guild_bank", "attendees"],
      "default": "guild_bank",
      "title": "Where the winner's cost goes",
      "description": "guild_bank drains the points out of circulation; attendees splits them across the night's raiders by largest remainder."
    },
    "solo_policy": {
      "type": "string",
      "enum": ["guild_bank", "write_off"],
      "default": "guild_bank",
      "title": "Where proceeds go with nobody to split them across",
      "description": "A solo kill has no attendees. The cost still leaves the winner; this says which system account receives it."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An award is rejected if it would take the winner below this. Negative permits going into debt to a limit."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes. A COPY, not the backing array of the
// constant.
func (Roll) ConfigSchema() []byte { return []byte(rollConfigSchema) }

// ID is the permanent identifier written onto every batch this strategy plans.
func (Roll) ID() string { return rollID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (Roll) Version() string { return rollVersion }

// RuleKind is spend: this strategy answers "how are points spent?" — by not spending them, which is
// still an answer to the question and is why a roll pool composes with a `tick` and a decay rule like
// any other (ADR-0026).
func (Roll) RuleKind() RuleKind { return RuleSpend }

// BalanceKinds is the one balance kind this strategy moves.
func (Roll) BalanceKinds() []string { return []string{BalanceKindDKP} }

// rollConfig is the parsed pool config. The JSON tags are the schema's property names and the two
// must agree.
type rollConfig struct {
	RollMin    int64            `json:"roll_min"`
	RollMax    int64            `json:"roll_max"`
	WinCostCp  core.Centipoints `json:"win_cost_cp"`
	Proceeds   string           `json:"proceeds"`
	SoloPolicy string           `json:"solo_policy"`
	FloorCp    core.Centipoints `json:"floor_cp"`
}

// defaultRollConfig is the config a pool that has set nothing runs under: `/random 1 100`, free.
func defaultRollConfig() rollConfig {
	return rollConfig{
		RollMin:    1,
		RollMax:    100,
		WinCostCp:  0,
		Proceeds:   ProceedsGuildBank,
		SoloPolicy: SoloPolicyGuildBank,
		FloorCp:    0,
	}
}

// terms is the part of this config every spend rule shares. See spend.go.
func (cfg rollConfig) terms() spendTerms {
	return spendTerms{Proceeds: cfg.Proceeds, SoloPolicy: cfg.SoloPolicy, FloorCp: cfg.FloorCp}
}

// faces is how many distinct values the configured range holds — the argument to Rng.IntN.
//
// An int rather than an int64 because that is what IntN takes, and the conversion is safe because
// validateRollConfig bounds both ends at maxRollFace: the widest legal span is a million and one,
// which fits in an int on every platform this repository builds for, 32-bit ARM included.
func (cfg rollConfig) faces() int { return int(cfg.RollMax-cfg.RollMin) + 1 }

// config parses and validates the pool's config, re-validating what the API edge already validated.
func (Roll) config(ctx Ctx) (rollConfig, error) {
	cfg := defaultRollConfig()

	if err := decodeConfig(rollID, ctx.ConfigJSON(), &cfg); err != nil {
		return rollConfig{}, err
	}

	return validateRollConfig(cfg)
}

// validateRollConfig applies the bounds the schema declares, to a config that has already parsed.
// Split from config so that the defaults are validated too.
func validateRollConfig(cfg rollConfig) (rollConfig, error) {
	if err := validateSpendTerms(rollID, cfg.terms()); err != nil {
		return rollConfig{}, err
	}

	if cfg.RollMin < 0 || cfg.RollMin > maxRollFace {
		return rollConfig{}, fmt.Errorf("%s: roll_min is %d, want 0..%d: %w",
			rollID, cfg.RollMin, maxRollFace, ErrInvalidConfig)
	}

	if cfg.RollMax > maxRollFace {
		return rollConfig{}, fmt.Errorf("%s: roll_max is %d, want at most %d; Rng.IntN takes an int "+
			"and an int is 32 bits on the ARM builds this ships to: %w",
			rollID, cfg.RollMax, maxRollFace, ErrInvalidConfig)
	}

	// A RANGE OF ONE IS NOT A ROLL. Every entrant would roll the same number, every settlement would
	// be a tie, and the pool would award nothing for ever while looking configured. Naming it costs a
	// comparison; discovering it costs a raid night.
	if cfg.RollMax <= cfg.RollMin {
		return rollConfig{}, fmt.Errorf(
			"%s: roll_max %d is not above roll_min %d, so every entrant rolls the same number and "+
				"every round ties: %w", rollID, cfg.RollMax, cfg.RollMin, ErrInvalidConfig)
	}

	if cfg.WinCostCp < 0 {
		return rollConfig{}, fmt.Errorf(
			"%s: win_cost_cp is %d; winning cannot pay the winner: %w",
			rollID, cfg.WinCostCp, ErrInvalidConfig)
	}

	return cfg, nil
}

// PlanAward debits the winner what the pool charges for a win, and routes it.
//
// A FREE ROLL POSTS NO BATCH. With `win_cost_cp` at its default of 0 and no officer override, nothing
// moved: the item was awarded, and awarding it is a fact for the item-award record rather than for
// the ledger. ErrNothingToPlan is the package's word for "legal, but it produces no entries", and a
// caller that receives it declines to write a batch instead of writing an empty one — which
// ledger_batch's own CHECK (entry_count > 0) would refuse anyway.
//
// THE SEED IS CARRIED ONTO THE BATCH, and it is worth being exact about what that claims. This
// planner consumes no randomness — the winner arrives decided — so the seed is not what produced these
// entries; it is the seed of the Rng this pool's plan was materialised with, which is the same source
// the settlement rolled from. Recording it is what lets an award batch be traced back to a
// reproducible roll months later, and it is the reason ledger_batch carries rng_seed at all. The
// authoritative record of the roll itself is Resolution.RngSeed, returned by SettleAuction.
func (s Roll) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	cost := cfg.WinCostCp
	if ev.PriceCp != nil {
		cost = *ev.PriceCp
	}

	if cost == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: winning costs nothing in this pool, so the award for item %q moves no points and %w",
			rollID, ev.Item.Name, ErrNothingToPlan)
	}

	p, err := spendAward(ctx, rollID, rollVersion, cfg.terms(), ev, cost)
	if err != nil {
		return BatchProposal{}, err
	}

	seed := ctx.Rng().Seed()
	p.RngSeed = &seed

	return p, nil
}

// PlanAttendance is unsupported: a roll decides who gets loot, not what turning up earns.
func (Roll) PlanAttendance(Ctx, AttendanceEvent) (BatchProposal, error) {
	return spendPlanAttendance(rollID)
}

// PlanDecay is unsupported: a pool that wants points to expire runs an over-time rule beside this one.
func (Roll) PlanDecay(Ctx, DecayRun) (BatchProposal, error) {
	return spendPlanDecay(rollID)
}

// PlanAdjustment moves points between an account and a counterparty — two entries, never one.
func (s Roll) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	return adjustmentProposal(ctx, rollID, rollVersion, cfg.FloorCp, ev)
}

// PlanReversal negates every entry of the batch being reversed. Entry-wise negation is correct here
// because this strategy's only balance kind is `dkp`, a plain quantity; see reversePlan.
//
// REVERSING AN AWARD DOES NOT UN-ROLL THE ROLL, and nothing about the reversal pretends otherwise: it
// returns the winner's points, and the round itself stays in the record, struck through. A roll is
// immutable in the same sense a ledger batch is.
func (Roll) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	return reversePlan(ctx, rollID, b)
}

// Spendable is the account's balance at the pool head. Holds are Phase 6 — see AuctionOpen.Spendable.
func (Roll) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	return spendableBalance(ctx, rollID, acct)
}

// Priority ranks every candidate EQUALLY, which is the point of a roll pool rather than a gap in it.
//
// The whole reason a guild rolls for an item is that it has decided standing should not decide this
// one — so a board that ranked candidates by balance would be showing the very ordering the roll
// exists to ignore, and an officer would be asked to justify it. The account id is still the tiebreak
// so that a candidate list renders in a stable order.
func (Roll) Priority(_ Ctx, acct AccountRef) (Priority, error) {
	return Priority{
		Rank:     0,
		Tiebreak: acct.ID.String(),
		Reason:   "every entrant has the same claim; the roll decides",
	}, nil
}

// PriceHint is what winning costs, including when that is nothing.
//
// ZERO IS A REAL ANSWER HERE and is returned as one. "This item is free" and "this strategy cannot
// say what it costs" are different facts, which is exactly why the interface returns a POINTER — the
// same argument ItemRef.FixedPriceCp carries — and a free roll pool that answered nil would have a
// bidding UI drawing an empty space where "free" belongs.
func (s Roll) PriceHint(ctx Ctx, _ ItemRef) (*core.Centipoints, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, err
	}

	hint := cfg.WinCostCp

	return &hint, nil
}

// ValidateBid checks an ENTRY rather than a bid: a roll has no amounts to compare.
//
// AN ENTRY THAT NAMES AN AMOUNT IS REFUSED. There is nothing to bid — the roll decides — so a
// non-zero amount is either a caller wiring a bidding UI to a roll pool or a raider trying to hand
// the platform a number it did not generate. Either way, accepting it and ignoring it silently is how
// a guild ends up believing amounts matter here.
//
// IT MUST STILL BE AFFORDABLE when the pool charges for a win: entering a roll you cannot pay for
// wastes the round for everyone else, and the check costs one balance read.
func (s Roll) ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}

	if err := checkBidIdentity(rollID, acct, bid); err != nil {
		return err
	}

	if bid.AmountCp != 0 {
		return fmt.Errorf(
			"%s: the entry from account %s names %d centipoints; a roll is not bid on, and what a win "+
				"costs is the pool's win_cost_cp: %w",
			rollID, bid.AccountID, bid.AmountCp, ErrInvalidEvent)
	}

	if bid.Sealed {
		return fmt.Errorf(
			"%s: the entry from account %s is marked sealed; an entry carries no amount, so there is "+
				"nothing to keep sealed: %w", rollID, bid.AccountID, ErrInvalidEvent)
	}

	if cfg.WinCostCp == 0 {
		return nil
	}

	return checkBidAffordable(ctx, rollID, acct, Bid{AccountID: acct.ID, AmountCp: cfg.WinCostCp})
}

// SettleAuction rolls once for every entrant and awards the item to the highest roll.
//
// THE ROLLS ARE DRAWN IN ACCOUNT ORDER, which is what makes the round reproducible: the sequence a
// seeded Rng produces is fixed, so who gets which draw must not depend on the order the caller
// happened to collect the entries in. Two settlements of the same round from the same seed therefore
// agree, entrant for entrant — which is the claim the persisted seed makes and the determinism
// property checks.
//
// A REPEATED ENTRANT IS REFUSED rather than folded. Two entries for one account is two draws and
// double the chance of winning, and it is never a raider expressing more interest — it is a list
// assembled twice, from two sources or from a join that fanned out. checkDistinctShares makes the
// same argument for a split; this is the same defect where the stake is the item rather than the
// arithmetic.
//
// A TIE AWARDS NOBODY. See the file header: a re-roll is a new round, not an edit, so the resolution
// names the tie and the officer opens another session between the raiders in it.
func (s Roll) SettleAuction(ctx Ctx, _ Session, bids []Bid) (Resolution, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return Resolution{}, err
	}

	if len(bids) == 0 {
		const nobody = "nobody entered the roll"

		return Resolution{
			Reason: nobody,
			Trace:  []ResolutionStep{{Kind: ResolutionStepEligibility, Detail: nobody}},
		}, nil
	}

	// EVERY REFUSAL HAPPENS ABOVE THIS LINE. Below it the round is committed to a sequence of draws,
	// and a round that drew and then failed would spend randomness a retry can never get back — see
	// sortedEntrants. Nothing between here and the seed may reject an entry.
	entrants, err := sortedEntrants(bids)
	if err != nil {
		return Resolution{}, err
	}

	seed := ctx.Rng().Seed()

	rolled := make([]rankedBid, 0, len(entrants))
	for _, b := range entrants {
		rolled = append(rolled, rankedBid{bid: b, rank: cfg.RollMin + int64(ctx.Rng().IntN(cfg.faces()))})
	}

	ordered, err := rankBids(rollID, rolled)
	if err != nil {
		return Resolution{}, err
	}

	// tiedOnRank rather than tiedAtTop: two entrants who rolled the same number are tied, whatever
	// else differs between their entries. An entry carries no amount and its placement time has
	// nothing to do with a die. THE RUNG IS STILL PART OF THAT KEY (#224): a main and an alt who both
	// rolled 97 are not tied, the ladder settled it, and a round that called for a re-roll there would
	// re-open a question the guild's own rules had already answered.
	// The ladder ran whatever the round then did with it, so the trace records it on every path out of
	// here — including the two that award nobody. See Resolution.Trace: what the chain evaluated and
	// what took the item are different questions, and only the second is empty on a tie.
	phase := tierOutcomeOf(ordered, "entrant")
	entered := ResolutionStep{
		Kind:   ResolutionStepEligibility,
		Detail: fmt.Sprintf("%d entrants, each rolled once over %d–%d", len(entrants), cfg.RollMin, cfg.RollMax),
	}

	if tied := tiedOnRank(ordered); tied > 1 {
		return Resolution{
			Reason: fmt.Sprintf(
				"%d entrants tied on %d of %d–%d; a re-roll is a new round rather than an edit, so "+
					"this one awards nobody",
				tied, ordered[0].rank, cfg.RollMin, cfg.RollMax),
			RngSeed: &seed,
			Trace: []ResolutionStep{entered, phase.step(), {
				Kind: ResolutionStepSeededRoll,
				Detail: fmt.Sprintf(
					"%d entrants in tier %s rolled %d from seed %d, and a tie is a new round rather "+
						"than an edit, so this one awards nobody",
					tied, phase.tier, ordered[0].rank, seed),
			}},
		}, nil
	}

	// THE ROLL THAT WON IS THE HIGHEST ON THE WINNING RUNG, and the sentence has to say so. Everybody
	// entered rolls — the draws are per entrant, in account order, which is what makes the round
	// replayable — but a lower rung cannot take the item whatever it rolled. "Highest of 6 rolls: 5"
	// with a 97 sitting in `alt` would read as a misread die rather than as the ladder.
	reason := fmt.Sprintf("highest of %d rolls of %d–%d: %d",
		tiedOnTier(ordered), cfg.RollMin, cfg.RollMax, ordered[0].rank)
	if phase.below > 0 {
		reason = fmt.Sprintf("%s; %d entrant(s) on lower rungs could not win it whatever they rolled",
			reason, phase.below)
	}

	return Resolution{
		Winners:     []Allocation{{AccountID: ordered[0].bid.AccountID, AmountCp: cfg.WinCostCp}},
		Reason:      reason,
		RngSeed:     &seed,
		WinningTier: phase.tier,
		TierCounts:  phase.counts,
		Trace: []ResolutionStep{entered, phase.step(), {
			Kind: ResolutionStepSeededRoll,
			Detail: fmt.Sprintf(
				"the highest of the %d rolls in tier %s is %d, drawn from seed %d; re-running that "+
					"seed over the same entrants in account order draws it again",
				tiedOnTier(ordered), phase.tier, ordered[0].rank, seed),
		}, {
			Kind:   ResolutionStepPrice,
			Detail: fmt.Sprintf("winning costs the configured %d centipoints", cfg.WinCostCp),
		}},
	}, nil
}

// sortedEntrants copies the entries into account order and refuses the ones no round has an answer
// for: an entry naming nobody, an account entered twice, and a rung this package cannot rank.
//
// A COPY, so a settlement never reorders its caller's slice, and SORTED, because the roll each
// entrant receives is the draw at their position in this list — see SettleAuction. The duplicate
// check rides along because the list is already ordered, which is the same shape checkDistinctShares
// uses for the same reason.
//
// EVERY CHECK IS HERE BECAUSE EVERY CHECK MUST RUN BEFORE THE FIRST DRAW. This function is the last
// thing SettleAuction does before it takes the seed, and that ordering is load-bearing rather than
// tidy: the injected Rng is a sequence, so a settlement that consumed draws and then refused the
// round would leave that sequence advanced by a round nobody ran. The officer fixes the malformed
// entry, retries, and gets different numbers from the ones the same session would have produced had
// the bad entry never been there — with nothing to explain the difference, because a rejected round
// persists no seed. The rung check in particular has to be repeated here rather than left to
// rankBids (found in AO review of #224): rankBids runs after the draws, which for every other spend
// rule is still before any randomness and for this one is not.
func sortedEntrants(bids []Bid) ([]Bid, error) {
	out := make([]Bid, len(bids))
	copy(out, bids)

	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })

	for i, b := range out {
		if b.AccountID == "" {
			return nil, fmt.Errorf("%s: an entry names no account: %w", rollID, ErrInvalidEvent)
		}

		if i > 0 && b.AccountID == out[i-1].AccountID {
			return nil, fmt.Errorf(
				"%s: account %s is entered twice, which is two rolls and twice the chance; a repeated "+
					"entrant is a list that was built twice: %w", rollID, b.AccountID, ErrInvalidEvent)
		}

		if _, err := checkTier(rollID, b); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
func (Roll) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}
