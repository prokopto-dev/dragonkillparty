package strategy

import (
	"fmt"
	"sort"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The three rules a pool composes, and the routing between them. Phase 1, ADR-0026 (#213).
//
// THE PROBLEM THIS FILE ENDS. `pool.strategy_id` was singular while the catalogue lists earn rules,
// spend rules and over-time rules separately, so a pool whose strategy was `tick` could not award an
// item: Tick.PlanAward returns ErrUnsupported, correctly, because an earn rule has no price list and
// inventing one would be a second copy of fixed_price's price resolution that could then disagree
// with it. Each refusal was right on its own and the set of them was not a guild configuration.
//
// A POOL HOLDS THREE (strategy, config) PAIRS, one per question, and Rules routes every planner to
// the one rule that owns the question:
//
//	earn       PlanAttendance, PlanAdjustment
//	spend      PlanAward, Spendable, Priority, PriceHint, ValidateBid, SettleAuction
//	over time  PlanDecay
//
// NO FALLBACKS ANYWHERE, and that is the single most important property of this file. An empty slot
// refuses by name (ErrNoRule) rather than borrowing another rule's answer, because every borrow is a
// silently different DKP system: a pool with no decay rule that quietly asked its earn rule to decay
// would post batches nobody configured, against an append-only table.
//
// AND THE SLOT A STRATEGY MAY OCCUPY IS THE STRATEGY'S OWN DECLARATION (PointStrategy.RuleKind), so
// `tick` in the spend slot is refused at configuration time with ErrWrongRuleKind — on the settings
// form, not at 19:05 on a raid night.
//
// PURITY (law 3) is unchanged. Nothing here reads a database, a wall clock or a random source: the
// pool's three configs arrive as strings from the caller that read the row, and ruleCtx swaps one in
// per delegation. arch_test.go walks the real import graph and would say so.
//
// WHAT IS DELIBERATELY NOT HERE. Loading a pool row is internal/store's, and the HTTP surface that
// edits one is Phase 2 (#212's generators included) — a package that may not import internal/store
// cannot read a pool and must not pretend to. PoolConfig is the shape of what a caller read, not a
// reader.

// Rules-related errors. Sentinels live in the owning package (.claude/rules/go-idioms.md) and callers
// compare with errors.Is.
var (
	// ErrNoRule reports that the pool has no rule for the question being asked — no spend rule and
	// an award to plan, no over-time rule and a decay run to post.
	//
	// SEPARATE FROM ErrUnsupported, and the difference is the one an API edge has to draw:
	// ErrUnsupported means "this strategy has no concept of that" and is a 501 about the software,
	// while this means "this pool has not been configured to answer that" and is a 409 about the
	// guild's own settings with an officer-actionable fix. Collapsing them sends an officer to file a
	// bug report about a dropdown they could have filled in.
	ErrNoRule = fmt.Errorf("pool has no rule for this")

	// ErrWrongRuleKind reports a strategy configured into a slot it does not answer — `tick` as a
	// spend rule, `fixed_price` as an over-time rule.
	//
	// It fires in Resolve, at configuration time, which is the whole point of RuleKind existing: the
	// alternative is a pool that saves cleanly and then returns ErrUnsupported during loot, when the
	// officer who could fix it is holding a corpse timer.
	ErrWrongRuleKind = fmt.Errorf("strategy does not answer that question")
)

// PoolConfig is a pool row's composition columns, verbatim: the three (strategy_id, config_json)
// pairs and nothing else.
//
// IT IS THE ROW'S SHAPE RATHER THAN A DOMAIN TYPE, deliberately. The columns are TEXT and NULLABLE
// (empty here means SQL NULL means "no rule for that question"), the configs are opaque documents
// this package does not parse until a planner does, and the caller that filled this in is the one
// that may hold a *sql.DB. Resolve is the only thing that turns it into something that can plan.
type PoolConfig struct {
	// EarnStrategyID and EarnConfigJSON are pool.earn_strategy_id / earn_config_json.
	EarnStrategyID string
	EarnConfigJSON string

	// SpendStrategyID and SpendConfigJSON are pool.spend_strategy_id / spend_config_json.
	SpendStrategyID string
	SpendConfigJSON string

	// OverTimeStrategyID and OverTimeConfigJSON are pool.over_time_strategy_id /
	// over_time_config_json.
	OverTimeStrategyID string
	OverTimeConfigJSON string
}

// Rule is one of a pool's three configured rules: a resolved planner plus the config the pool holds
// for it.
//
// The config travels WITH the strategy rather than being looked up beside it, because the pairing is
// the thing that can go wrong: a pool that handed `cap`'s ceiling to `tick` would parse a config with
// an unknown key, refuse it, and report a strategy error for a wiring mistake.
type Rule struct {
	// Strategy is the resolved planner. Nil means the pool has no rule for this question.
	Strategy PointStrategy

	// ConfigJSON is this rule's own configuration document, verbatim, as the column holds it.
	ConfigJSON string
}

// IsSet reports whether the pool has a rule for this question.
func (r Rule) IsSet() bool { return r.Strategy != nil }

// Rules is a pool's composition: which strategy answers each of its three questions.
//
// It is NOT a PointStrategy and must not become one. A strategy has an ID that is written onto every
// batch it plans; a composition has three, and which one applies depends on what was asked — that is
// precisely what ledger_batch.strategy_id records, and a composition claiming a single id of its own
// would put a name on the column that resolves to no planner. TestCatalogue_ContainsEveryStrategyInThePackage
// is what would fail if somebody added the compile-time assertion anyway.
type Rules struct {
	// Earn answers "how are points earned?" — the raid-night credit.
	Earn Rule

	// Spend answers "how are points spent?" — the price, the queue and the auction.
	Spend Rule

	// OverTime answers "what happens to points over time?" — the cadence run that posts decay, a cap
	// trim or an opening grant.
	OverTime Rule
}

// Resolve turns a pool row's three (id, config) pairs into the rules that plan for it.
//
// Three refusals, in the order a misconfiguration is most likely to have happened:
//
//   - an EMPTY id is a slot the pool has not configured. Legal, and not an error: a pool part-way
//     through setup and a guild with no decay rule are both real, and refusing here would make an
//     unconfigured pool unreadable rather than partly usable. The refusal comes later, from the
//     planner whose slot is empty, where the message can name what to configure.
//   - an UNKNOWN id is an operator running a binary that does not have that strategy — a downgrade
//     across a release that added one. ErrUnknownStrategy, naming both the id and the slot, because
//     "no such strategy: epgp" without the slot leaves an officer reading three dropdowns.
//   - a WRONG-SLOT id is `tick` configured to spend. ErrWrongRuleKind, and this is the refusal that
//     RuleKind exists for: it is the difference between a settings form that says no and a raid
//     night that does.
//
// It resolves all three or none. A partially resolved Rules would be a value whose planners work
// until they reach the broken slot, which is the failure mode a startup-time refusal exists to
// prevent (catalogue.go says the same of ByID's sentinel).
func (c PoolConfig) Resolve() (Rules, error) {
	earn, err := resolveRule(RuleEarn, c.EarnStrategyID, c.EarnConfigJSON)
	if err != nil {
		return Rules{}, err
	}

	spend, err := resolveRule(RuleSpend, c.SpendStrategyID, c.SpendConfigJSON)
	if err != nil {
		return Rules{}, err
	}

	overTime, err := resolveRule(RuleOverTime, c.OverTimeStrategyID, c.OverTimeConfigJSON)
	if err != nil {
		return Rules{}, err
	}

	return Rules{Earn: earn, Spend: spend, OverTime: overTime}, nil
}

// resolveRule resolves one slot. See Resolve for the three cases and why each is what it is.
func resolveRule(kind RuleKind, strategyID, configJSON string) (Rule, error) {
	if strategyID == "" {
		return Rule{}, nil
	}

	s, err := ByID(strategyID)
	if err != nil {
		return Rule{}, fmt.Errorf("%s rule: %w", kind, err)
	}

	if s.RuleKind() != kind {
		return Rule{}, fmt.Errorf(
			"%s is the pool's %s rule but it answers %s: %w",
			strategyID, kind, s.RuleKind(), ErrWrongRuleKind)
	}

	return Rule{Strategy: s, ConfigJSON: configJSON}, nil
}

// BalanceKinds is the union of the balance kinds this pool's rules move, sorted.
//
// It is what `pool.balance_kinds` holds for a composed pool. THE UNION rather than any one rule's,
// because a balance kind is a column of the ledger a pool writes into: a pool whose spend rule moves
// `ep` and `gp` and whose earn rule moves `dkp` writes all three, and a declaration naming one of
// them would be a declaration that is wrong about the other two.
//
// SORTED so the column does not churn when a slot is reconfigured in a way that changes nothing about
// which kinds are moved.
func (r Rules) BalanceKinds() []string {
	seen := map[string]bool{}

	for _, rule := range []Rule{r.Earn, r.Spend, r.OverTime} {
		if !rule.IsSet() {
			continue
		}

		for _, kind := range rule.Strategy.BalanceKinds() {
			seen[kind] = true
		}
	}

	out := make([]string, 0, len(seen))
	for kind := range seen {
		out = append(out, kind)
	}

	sort.Strings(out)

	return out
}

// PlanAttendance routes the raid-night credit to the EARN rule.
func (r Rules) PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error) {
	rule, err := r.require(RuleEarn, "plan an attendance tick")
	if err != nil {
		return BatchProposal{}, err
	}

	return rule.Strategy.PlanAttendance(rule.bind(ctx), ev)
}

// PlanAdjustment routes an officer's manual movement to the EARN rule.
//
// EARN AND NOT SPEND, and the choice is argued rather than arbitrary. An adjustment is neither: it is
// a correction, a bonus or a penalty, and it is the one planner every strategy implements identically
// (adjustmentProposal) apart from the floor it declares. So the question is only whose floor applies,
// and the earn rule's is the right answer twice over — it is the rule a DKP pool cannot do without,
// and the floor a member's balance lives under between purchases is the one they earn against. A
// pool with no earn rule cannot take adjustments, which is a refusal an officer can act on.
func (r Rules) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	rule, err := r.require(RuleEarn, "plan an adjustment")
	if err != nil {
		return BatchProposal{}, err
	}

	return rule.Strategy.PlanAdjustment(rule.bind(ctx), ev)
}

// PlanAward routes the loot charge to the SPEND rule.
func (r Rules) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	rule, err := r.require(RuleSpend, "plan an award")
	if err != nil {
		return BatchProposal{}, err
	}

	return rule.Strategy.PlanAward(rule.bind(ctx), ev)
}

// PlanDecay routes the cadence run to the OVER-TIME rule — a decay, a cap trim or an opening grant,
// whichever family the pool put there (ADR-0024's three decay_run kinds).
func (r Rules) PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error) {
	rule, err := r.require(RuleOverTime, "plan a cadence run")
	if err != nil {
		return BatchProposal{}, err
	}

	return rule.Strategy.PlanDecay(rule.bind(ctx), run)
}

// PlanReversal routes to the strategy that planned the ORIGINAL, resolved from its
// ledger_batch.strategy_id through the CATALOGUE — deliberately not through this pool's three slots.
//
// THIS IS WHAT ADR-0026 DECIDED THE COLUMN MEANS, made executable. A reversal must be planned by the
// rules that were in force: a reversal of a `fixed_price` award planned by `tick` would negate
// committed entries under a rule nobody applied to them, and `tick` has no way to know it should not.
//
// IT DOES NOT CONSULT r, AND THAT IS THE WHOLE POINT. An earlier cut of this method searched the
// pool's current three rules and refused a batch planned by a rule the pool no longer runs — which
// made every historical `fixed_price` award permanently unreversible the moment a guild switched to
// an auction. That is the same defect reversePlan's doc comment already argues against one layer
// down: "History is immutable and the repair primitive must not be contingent on the present." The
// ledger is append-only, so a reversal is the ONLY repair there is; refusing one does not prevent a
// bad batch, it prevents the CORRECTION, and leaves a mistake that is provably wrong and permanently
// unfixable. `.claude/rules/ledger-and-strategy.md` makes the identical argument about declaring
// NonNegative on a reversal, and it is the same mistake in a different place.
//
// THE CONFIG BOUND IS THE BATCH'S OWN SNAPSHOT, not any current slot's. It is what the original was
// planned under, it travels on the row, and reading a live slot's document instead would reintroduce
// the contingency by the back door — a knob added since, or a config for a different strategy
// entirely, and the reversal stops parsing. No strategy shipping today reads it during a reversal
// (reversePlan needs no config), so this is about the one that will.
//
// The strategy still has to EXIST. An id no shipped strategy answers to is an operator running a
// binary older than the batch — a downgrade across a release that added a strategy — and that is
// ErrUnknownStrategy with the pool's rules named for diagnosis, not a reversal planned by a guess.
func (r Rules) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	s, err := ByID(b.StrategyID)
	if err != nil {
		return BatchProposal{}, fmt.Errorf(
			"batch %s was planned by %q, which this binary does not have; the pool runs %s: %w",
			b.ID, b.StrategyID, r.describe(), err)
	}

	original := Rule{Strategy: s, ConfigJSON: b.ConfigSnapshotJSON}

	return s.PlanReversal(original.bind(ctx), b)
}

// Spendable routes to the SPEND rule: what an account may commit to a purchase is a question about
// purchasing, and a pool with no spend rule has nothing to commit it to. The account's BALANCE is
// still readable — it is a SUM over committed entries and belongs to internal/ledger, not here.
func (r Rules) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	rule, err := r.require(RuleSpend, "report a spendable balance")
	if err != nil {
		return 0, err
	}

	return rule.Strategy.Spendable(rule.bind(ctx), acct)
}

// Priority routes to the SPEND rule: a queue for loot is the spend rule's queue.
func (r Rules) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	rule, err := r.require(RuleSpend, "rank an account for loot")
	if err != nil {
		return Priority{}, err
	}

	return rule.Strategy.Priority(rule.bind(ctx), acct)
}

// PriceHint routes to the SPEND rule.
func (r Rules) PriceHint(ctx Ctx, item ItemRef) (*core.Centipoints, error) {
	rule, err := r.require(RuleSpend, "hint at a price")
	if err != nil {
		return nil, err
	}

	return rule.Strategy.PriceHint(rule.bind(ctx), item)
}

// ValidateBid routes to the SPEND rule.
func (r Rules) ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error {
	rule, err := r.require(RuleSpend, "validate a bid")
	if err != nil {
		return err
	}

	return rule.Strategy.ValidateBid(rule.bind(ctx), acct, bid)
}

// SettleAuction routes to the SPEND rule.
func (r Rules) SettleAuction(ctx Ctx, s Session, bids []Bid) (Resolution, error) {
	rule, err := r.require(RuleSpend, "settle an auction")
	if err != nil {
		return Resolution{}, err
	}

	return rule.Strategy.SettleAuction(rule.bind(ctx), s, bids)
}

// require returns the rule for a slot, or the refusal naming what the pool would have to configure.
//
// The message says which slot and what the caller was trying to do, because the audience is an
// officer reading an error on a screen rather than the code that raised it: "this pool has no spend
// rule, so it cannot plan an award" is actionable, and "unsupported" is a support ticket.
func (r Rules) require(kind RuleKind, operation string) (Rule, error) {
	var rule Rule

	switch kind {
	case RuleEarn:
		rule = r.Earn
	case RuleSpend:
		rule = r.Spend
	case RuleOverTime:
		rule = r.OverTime
	}

	if !rule.IsSet() {
		return Rule{}, fmt.Errorf("this pool has no %s rule, so it cannot %s: %w",
			kind, operation, ErrNoRule)
	}

	return rule, nil
}

// describe renders the pool's three rules for an error message, in slot order.
func (r Rules) describe() string {
	out := ""

	for _, pair := range []struct {
		kind RuleKind
		rule Rule
	}{{RuleEarn, r.Earn}, {RuleSpend, r.Spend}, {RuleOverTime, r.OverTime}} {
		id := "none"
		if pair.rule.IsSet() {
			id = pair.rule.Strategy.ID()
		}

		if out != "" {
			out += ", "
		}

		out += string(pair.kind) + "=" + id
	}

	return out
}

// bind hands a planner the façade with THIS RULE's config in it.
//
// The whole mechanism, and it is four lines because Ctx is an interface: ruleCtx embeds the caller's
// façade and overrides the one method whose answer differs per rule. Everything else — the balances,
// the roster, the clock, the seeded Rng, the shared allocator — is the pool's and is passed straight
// through, which is what keeps three rules planning against one consistent view of one pool.
//
// A rule reads ONLY its own config, and that is load-bearing rather than tidy: every strategy in this
// package decodes strictly, with DisallowUnknownFields, so handing `cap` the earn rule's document
// would be a refusal blaming the strategy for a wiring mistake. It is also what makes
// ConfigSnapshotJSON on the resulting batch the config that actually planned it — proposeZeroSum
// copies ctx.ConfigJSON() verbatim, so the snapshot is this rule's document and changing another
// slot's config later cannot change what this batch meant.
func (r Rule) bind(ctx Ctx) Ctx {
	return ruleCtx{Ctx: ctx, configJSON: r.ConfigJSON}
}

// ruleCtx is the pool's façade with one rule's config swapped in.
//
// EMBEDDING THE INTERFACE rather than reimplementing eleven methods: a decorator that listed them
// would have to be edited every time Ctx grows, and the edit that gets forgotten is the one that
// silently returns a zero value. Embedding means a new façade method reaches every rule the day it is
// added, and only ConfigJSON is the pool's business to override.
type ruleCtx struct {
	Ctx

	configJSON string
}

// ConfigJSON is this rule's document, not the pool's singular one.
func (c ruleCtx) ConfigJSON() string { return c.configJSON }
