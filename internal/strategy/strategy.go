package strategy

import (
	"errors"
	"fmt"

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The PointStrategy interface and everything a planner is allowed to see. Phase 0 PR 10b.
//
// proposal.go holds what a strategy PRODUCES; this file holds what it may CONSUME and the interface
// that joins the two. The split matters because the consumption side is where the purity law is
// actually won or lost: a strategy is pure not because it promises to be, but because the only thing
// it is handed is a Ctx, and a Ctx cannot open a database, read a wall clock or seed a generator.
//
// PURITY (law 3), enforced by PURE001/PURE002/CLOCK001 in scripts/repo-gates.sh, by the forbidigo
// float ban in .golangci.yml, and by TestArch_Strategy_ImportGraph_IsPure in arch_test.go:
//
//   - no internal/store, TRANSITIVELY — not through internal/ledger, not through anything;
//   - no `time` and no `math/rand` as DIRECT imports — the Clock and the Rng arrive through Ctx;
//   - no float32/float64 — point arithmetic is core.Centipoints (int64) only.
//
// ON ConfigSchema RETURNING []byte. It is a JSON Schema document as bytes, not a huma.Schema and not
// a decoded map. Returning the API framework's type here would make internal/strategy import
// internal/api's dependency graph — which is how a "pure" package acquires an HTTP server two
// refactors later — and returning a map would make the schema something a caller could mutate. Bytes
// are inert, hashable, and embeddable in the generated pool-settings form without a round trip.

// Errors this file returns. Sentinels live in the owning package (.claude/rules/go-idioms.md) and
// callers compare with errors.Is.
var (
	// ErrUnsupported reports that this strategy does not implement an optional part of the
	// interface — a fixed-price guild has no auction to settle, and a roll-based one has no bid to
	// validate.
	//
	// It is a SENTINEL rather than a nil-return-with-nil-error, because the two answers are
	// different and the difference is load-bearing at the API edge: "this strategy has no price
	// hint for that item" is a 200 with an absent field, and "this strategy has no concept of a
	// price hint" is a 501. A nil/nil return collapses them and the SPA cannot tell which screen to
	// draw.
	ErrUnsupported = errors.New("strategy does not support this operation")

	// ErrInvalidEvent reports that the caller handed the planner an event it cannot plan from — an
	// award with no buyer, an adjustment of zero centipoints, a decay run with no period key. It is
	// a planner-side rejection and it names the field, because the audience is whoever wrote the
	// calling code rather than the officer whose award was refused.
	ErrInvalidEvent = errors.New("event cannot be planned")

	// ErrInvalidConfig reports a pool config the strategy cannot parse or cannot act on. The config
	// is validated against ConfigSchema() at the API edge, so reaching this means either the schema
	// and the parser have drifted or a config was written past the edge — both of which are bugs
	// that must stop the plan rather than default themselves into a silently different DKP system.
	ErrInvalidConfig = errors.New("strategy config is invalid")
)

// RuleKind is which of a pool's three questions a strategy answers (ADR-0026).
//
// The three come from docs/guides/choosing-a-dkp-system.md, which has told guilds since before any
// of this shipped that "a pool answers three questions" and that "every shipped rule answers exactly
// one of those three". This type is that sentence made executable: a pool holds one strategy per
// question, and PoolConfig.Resolve refuses a strategy put in a slot it does not answer.
//
// IT IS DECLARED BY THE STRATEGY, as PointStrategy.RuleKind, rather than as a table in catalogue.go.
// A table would be a second list beside the file it describes, and the two would eventually disagree
// about a strategy somebody re-purposed — where a method is a compile error the day the interface
// grows and a one-line diff in the file whose behaviour actually changed.
//
// A STRING TYPE rather than an int, because it is written into a settings form, an error message and
// (in Phase 2) a JSON field, and an int would be a number nobody can read in any of the three. It is
// deliberately NOT a database CHECK: which slot a strategy may occupy is code-defined exactly as the
// set of strategies is, and db/schema.hcl says so at all four columns.
type RuleKind string

// The three kinds. Lowercase snake_case, identical in Go, in the column and (in Phase 2) on the wire
// — the same rule every other enum in this project follows.
const (
	// RuleEarn answers "how are points earned?" — tick, attendance_weighted.
	RuleEarn RuleKind = "earn"

	// RuleSpend answers "how are points spent?" — fixed_price, zero_sum, the auctions, loot_council,
	// roll.
	//
	// `zero_sum` is here rather than in the over-time family the guide's catalogue table listed it
	// under (#196), and the correction is the same rule that puts `start_points` below: THE SLOT
	// FOLLOWS THE PLANNER. Redistributing the winner's payment happens in PlanAward, at the moment an
	// item is won, not on the decay_run cadence — a pool that held it over time would never award an
	// item and never decay one either.
	RuleSpend RuleKind = "spend"

	// RuleOverTime answers "what happens to points over time?" — decay_percent, decay_window, cap,
	// start_points. Every member of this family posts on the decay_run cadence (ADR-0024) rather than
	// in response to a raid-night event.
	RuleOverTime RuleKind = "over_time"
)

// IsRuleKind reports whether a string is one of the three.
//
// It exists for the boundary rather than for the planners: a slot read out of a pool row, an
// importer's mapping or a settings form is a string until something says otherwise, and a silent
// "not any of them" is how a pool ends up with a rule nothing routes to.
func IsRuleKind(kind string) bool {
	switch RuleKind(kind) {
	case RuleEarn, RuleSpend, RuleOverTime:
		return true
	default:
		return false
	}
}

// BalanceKindDKP is the single balance kind every 1.0 strategy but epgp and suicide_kings moves.
//
// The vocabulary as a whole is a database value, an OpenAPI enum and a docs page in one. Unlike
// ledger_batch.kind — whose catalogue is internal/ledger/kinds and whose CHECK `make gen` writes
// from it (canonical §5) — balance_kind carries no CHECK constraint, because 'ep', 'gp' and
// 'sk_position' arrive with the strategies that need them and a fixed list would make shipping one a
// migration. It gets the same treatment as ledger_batch.kind the day a second kind ships.
const BalanceKindDKP = "dkp"

// The four system-account keys: the ledger-addressable non-human targets that make zero-sum splits,
// rot handling and write-offs expressible (docs/design/01-domain-model.md §6.1).
//
// THEY ARE THE CATALOGUE'S, re-exported here rather than declared here (#51). They used to be
// declared here and referenced by internal/ledger, which put the vocabulary on the pure side — a
// strategy has to name the guild bank without importing the package that knows what a guild bank's
// row id is — but left a second, unrelated copy in db/schema.hcl's CHECK that nothing generated and
// no test compared. internal/account/kinds is now the single definition `make gen` writes that CHECK
// from; these aliases keep a planner writing `SystemKeyGuildBank` without an import it does not need,
// and Ctx.SystemAccount is still the only thing that turns a key into an id.
//
// Importing the catalogue does not touch law 3: it is a stdlib-only leaf over internal/schemaenum, so
// it reaches internal/store no more than `strings` does, and arch_test.go's purity audit walks the
// real graph and would say so if that ever changed.
const (
	SystemKeyResidue       = accountkinds.SystemKeyResidue
	SystemKeyGuildBank     = accountkinds.SystemKeyGuildBank
	SystemKeyWriteOff      = accountkinds.SystemKeyWriteOff
	SystemKeyImportOpening = accountkinds.SystemKeyImportOpening
)

// Share is one account's claim on a split: an account and its non-negative weight.
//
// Weight is an int64 count, not a ratio and never a float. An attendance-weighted strategy passes
// ticks attended; an even split passes 1 for everybody. Expressing the weight as an integer is what
// lets the quota be computed exactly — a ratio would have to be a float, and there are no floats in
// this package or in internal/ledger.
type Share struct {
	AccountID core.ULID
	Weight    int64

	// Role is what this account was doing on the night — the raid_attendance.status vocabulary
	// ('present', 'standby', 'bench', 'pilot', 'excused', 'late', 'left_early'), or a guild's own
	// label. Empty means "unlabelled", which every planner treats as the ordinary case.
	//
	// IT IS AN INPUT AND NEVER AN OUTPUT. No proposal carries it: a strategy that credits standby at
	// half a share writes an entry for the halved AMOUNT, and the reason it was halved belongs to the
	// attendance record, not to the money. That is why it can be added to this type without moving a
	// single committed golden.
	//
	// ONLY `tick` READS IT, through its role_multipliers config. It sits on Share rather than in a
	// parallel map on AttendanceEvent because a role belongs to the attendee: a map is a second list
	// that can disagree with the first about who was there, and a planner cannot tell which one is
	// wrong. Allocators ignore it — ledger.Allocate splits on Weight alone.
	Role string
}

// Allocation is one account's resulting share of a split. Allocations with a zero amount are never
// produced: ledger_entry carries CHECK (amount_cp <> 0), and an entry that moves nothing is noise
// that breaks entry_count reasoning.
type Allocation struct {
	AccountID core.ULID
	AmountCp  core.Centipoints
}

// AccountRef is the strategy-visible projection of an account: enough to plan against and to name in
// an error, and nothing else.
//
// It carries no person, no email and no character list on purpose. A planner that could see a
// person's identity is a planner that could make a decision based on WHO an account belongs to,
// which is the one thing a DKP system must never do by accident.
type AccountRef struct {
	// ID is the balance holder — what a balance sums over and what an entry names.
	ID core.ULID

	// Kind is 'person' | 'system'. Planners route degenerate cases to system accounts and must not
	// treat one as a raider.
	Kind string

	// SystemKey is one of the SystemKey* constants for a system account, empty for a person's.
	SystemKey string

	// Label is the display name, for error messages and for the dry-run report. It is never an
	// input to arithmetic.
	Label string
}

// IsSystem reports whether this is one of the four ledger-addressable non-human accounts. Planners
// use it to keep the guild bank out of an attendee split — crediting the bank its own payout is the
// classic way a zero-sum ledger stays balanced while every raider is quietly short.
func (a AccountRef) IsSystem() bool { return a.Kind == accountkinds.KindSystem }

// ItemRef is the strategy-visible projection of an item: what a pricing decision may depend on.
//
// FixedPriceCp is a POINTER because zero is a meaningful price (a guild that hands out an item for
// free still records the award), so an unset price and a price of zero must not be the same value.
type ItemRef struct {
	ID           core.ULID
	Name         string
	FixedPriceCp *core.Centipoints
}

// Priority is where an account stands in a queue for an item, when the strategy has one.
//
// It is a RANK plus its explanation, not a bare number, because the number alone starts arguments
// that the explanation ends: "priority 1250" tells a raider nothing, and "priority 1250 — spendable
// balance" tells them what to change. Reason is rendered in the UI beside the rank.
//
// Computed weighting is permitted here and is FORBIDDEN in Spendable
// (.claude/rules/ledger-and-strategy.md). A priority is an opinion the strategy is entitled to have;
// a balance is a SUM over committed entries and nothing else.
type Priority struct {
	// Rank orders candidates, HIGHER FIRST. It is an int64 for the same reason every other number
	// here is: a float rank would sort differently on two machines at the fifteenth digit.
	Rank int64

	// Tiebreak is the deterministic secondary ordering applied at equal Rank — the account id,
	// ascending, for every strategy that ships in 1.0. A tiebreak that is not deterministic makes
	// two replays of the same loot decision differ, which is the same defect the allocator's
	// account_id tiebreak exists to prevent.
	Tiebreak string

	// Reason is the human-readable basis for the rank. Never a rendered sentence with numbers
	// formatted for a locale — the UI formats; this explains.
	Reason string
}

// Bid is one sealed or open bid on an item. Auctions are Phase 3; the shape lives here because the
// interface that mentions it lives here, and a strategy that cannot answer ValidateBid says so with
// ErrUnsupported rather than by not having the method.
type Bid struct {
	// AccountID is the bidder.
	AccountID core.ULID

	// AmountCp is what they bid. Integer centipoints, on the wire and in memory.
	AmountCp core.Centipoints

	// PlacedAt is game truth for the bid, used for first-in tiebreaks under an anti-snipe rule.
	PlacedAt core.Micros

	// Sealed reports that this bid's amount must not be logged or shown before the reveal
	// (.claude/rules/go-idioms.md: "no bid amounts before reveal").
	Sealed bool
}

// Session is one auction: the item, the window, and the seq every balance in it resolves against.
//
// SeqAtOpen is the whole reason this type exists rather than a pair of arguments. Percentages and
// spend limits resolve against a FROZEN balance snapshot at the seq the session opened at; resolving
// against live balances lets a concurrent decay run rewrite everyone's bid mid-auction, and the bug
// only appears on the one night a decay job overlaps a raid.
type Session struct {
	ID          core.ULID
	Item        ItemRef
	SeqAtOpen   int64
	OpenedAt    core.Micros
	ClosesAt    core.Micros
	MinAmountCp core.Centipoints
}

// Resolution is an auction's outcome: who won, at what price, and why.
//
// Winners is a slice because a session may award several copies of the same drop, and because a
// strategy that ties deliberately (a roll-off pending) must be able to say so by returning none.
type Resolution struct {
	// Winners are the accepted bids in award order.
	Winners []Allocation

	// Reason explains the settlement for the audit trail and the UI.
	Reason string

	// RngSeed is the seed consumed if the settlement broke a tie randomly, nil otherwise. It is
	// carried onto the batch so the roll-off is replayable — an unrecorded coin flip is the one
	// thing a loot dispute cannot be settled from.
	RngSeed *int64
}

// AttendanceEvent is "these characters were present for this tick": the raid-night event that earns
// points, as opposed to the award event that spends them.
type AttendanceEvent struct {
	// Attendees are the accounts credited, with the weights the caller derived from attendance.
	// Weight 1 for everybody is an even tick; a weighted guild passes ticks-in-window.
	Attendees []Share

	// AmountCp overrides the config's per-tick award for this tick only (a bonus tick for a first
	// kill). nil means "use the configured amount".
	AmountCp *core.Centipoints

	// TickID and RaidID are attribution: which tick, which raid. They never affect arithmetic.
	TickID *core.ULID
	RaidID *core.ULID

	// EffectiveAt is GAME truth and may be backdated — a tick credited the morning after the raid
	// is effective at the raid's time, not at the officer's.
	EffectiveAt core.Micros

	// Reason is the officer's note, carried onto the batch.
	Reason string
}

// AwardEvent is "this account won this item at this price": the event that spends points.
type AwardEvent struct {
	// Buyer is the account debited. Required.
	Buyer AccountRef

	// CharacterID is ATTRIBUTION ONLY — which character looted it. It never affects a balance, so
	// that re-parenting an alt to a different main cannot move history.
	CharacterID *core.ULID

	// Item is what was won. Its FixedPriceCp, when set, is the catalogue price this strategy uses
	// before falling back to the pool's configured default.
	Item ItemRef

	// PriceCp overrides both the catalogue and the config for this award — an officer's one-off
	// price. nil means "resolve the price from the item, then from the config".
	PriceCp *core.Centipoints

	// Beneficiaries are the accounts the price is redistributed to, with their weights. EMPTY is
	// not an error and not a silent drop: it is the solo-kill case, and the proceeds route to the
	// system account the config's solo policy names.
	Beneficiaries []Share

	// RaidID and ItemAwardID are attribution pointers carried onto every entry, so a member's
	// statement can say which raid and which award moved their points.
	RaidID      *core.ULID
	ItemAwardID *core.ULID

	// EffectiveAt is game truth for the award.
	EffectiveAt core.Micros

	// Reason is the officer's note.
	Reason string
}

// AdjustmentEvent is an officer moving points by hand: a correction, a bonus, a penalty.
//
// It names a COUNTERPARTY rather than minting or destroying points, because a ledger where officers
// can conjure a balance is a ledger whose totals mean nothing. The counterparty defaults to the
// guild bank, which is the account that exists to be the other side of exactly this.
type AdjustmentEvent struct {
	// Account is the adjusted account. Required.
	Account AccountRef

	// AmountCp is the signed delta applied to Account. It must be non-zero: ledger_entry carries
	// CHECK (amount_cp <> 0) and an adjustment of nothing is a batch nobody meant to write.
	AmountCp core.Centipoints

	// Counterparty is the other side of the movement. Empty means the guild bank.
	Counterparty core.ULID

	// EffectiveAt is game truth for the adjustment.
	EffectiveAt core.Micros

	// Reason is the officer's justification. The API layer, not this struct, enforces that it is
	// present for destructive and self-dealing actions (proposal.go's Reason field says the same).
	Reason string
}

// DecayRun is one cadence period's decay: the event that makes attendance recent rather than
// eternal.
//
// DECAY IS POSTED, NOT COMPUTED (.claude/rules/ledger-and-strategy.md). This type is the input to a
// planner that emits explicit batches; nothing anywhere computes a decayed balance on read. That is
// what keeps a balance literally a SUM and makes "why did my points change?" answerable.
type DecayRun struct {
	// PeriodKey identifies the cadence period ('2024-06'), and is the second half of the
	// idempotency key (pool_id, kind, cadence_period) that makes a re-run a no-op rather than a second
	// decay.
	PeriodKey string

	// AsOfSeq is the seq every balance in this run is read at. POSITIONAL, never temporal: a
	// backdated batch committed while the run is planning must not change what the run decayed.
	AsOfSeq int64

	// Accounts is the roster to decay. Empty means "every account in the pool", which the planner
	// resolves through Ctx.Roster.
	Accounts []AccountRef

	// EffectiveAt is when the decay takes effect.
	EffectiveAt core.Micros
}

// LedgerBatch is a COMMITTED batch, projected into the shape a reversal planner needs.
//
// It reuses EntryProposal for its entries rather than declaring a near-identical committed-entry
// type. That is deliberate: the fields a reversal reads — account, balance kind, amount and the four
// provenance pointers — are exactly EntryProposal's, and the fields it does not read (the entry id,
// the batch id, the pool, the seq) are the ones the ledger owns and a planner may not choose. One
// exported type per concept (.claude/rules/go-idioms.md), and "the shape of an entry a planner can
// see" is one concept.
type LedgerBatch struct {
	// ID is the batch being reversed; it lands in the reversal's ReversesBatchID.
	ID core.ULID

	// Kind is the original's ledger_batch.kind. A reversal of a reversal is legal, so this is not
	// constrained.
	Kind string

	// StrategyID and StrategyVersion identify the planner that produced the original. The reversal
	// carries them forward: a reversal is attributable to the rules it undoes, not to today's.
	StrategyID      string
	StrategyVersion string

	// ConfigSnapshotJSON is the original's captured config, carried onto the reversal for the same
	// reason.
	ConfigSnapshotJSON string

	// Reason is the original's note.
	Reason string

	// EffectiveAt is the ORIGINAL's effective time. It is deliberately NOT copied onto the
	// reversal: a reversal is a new economic event at the time it is decided, and backdating it
	// would silently rewrite every intermediate balance's meaning.
	EffectiveAt core.Micros

	// Entries are the committed deltas, in id order as the ledger stored them.
	Entries []EntryProposal
}

// Reversal returns the DEFAULT reversal of a committed batch: entry-wise negation, kind 'reversal',
// ReversesBatchID pointing at the original.
//
// It is the shared implementation every quantity-kind strategy's PlanReversal delegates to, and it is
// NOT always right. `.claude/rules/ledger-and-strategy.md`: "PlanReversal's default is entry-wise
// negation. The default is wrong for at least one balance kind and you must not assume it."
// Suicide Kings' sk_position is an ordering rather than a quantity — negating a position delta does
// not restore the list, because everyone below the winner shifted up in the meantime. A strategy
// whose balance kind is not a plain quantity MUST override PlanReversal and say so in its doc
// comment.
func (b LedgerBatch) Reversal() (BatchProposal, error) {
	original := BatchProposal{
		Kind:               b.Kind,
		StrategyID:         b.StrategyID,
		StrategyVersion:    b.StrategyVersion,
		ConfigSnapshotJSON: b.ConfigSnapshotJSON,
		Reason:             b.Reason,
		Entries:            b.Entries,
	}

	reversal, err := original.Negated(b.ID)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("reverse batch %s: %w", b.ID, err)
	}

	return reversal, nil
}

// Ctx is the read-only façade a planner is handed: everything it may look at, and nothing else.
//
// It is the mechanism behind law 3 rather than a convenience. A strategy cannot import
// internal/store, so every fact it needs must arrive through this interface — which means widening
// it is a DESIGN DECISION taken in review, visible as a diff to one type, rather than an import
// somebody added at 2 a.m. If a strategy needs a fact this does not expose, the question is what to
// add here, never whether to reach past it (.claude/skills/add-strategy/SKILL.md, "Stop and ask if").
//
// ON THE ABSENCE OF context.Context. Every method here may perform I/O, and .claude/rules/go-idioms.md
// requires a ctx as the first parameter of every function that does. The planner signatures in
// `.claude/rules/ledger-and-strategy.md` and in the add-strategy skill are
// `PlanAward(ctx Ctx, ev AwardEvent)` with no second context, and that shape wins here for a reason
// beyond precedent: a Ctx is not a service, it is a VALUE MATERIALISED FOR ONE PLAN inside the
// commit transaction, and the transaction's context is bound into it at construction. Cancellation
// still propagates — it propagates through the bound context, one layer out, where the transaction
// that owns it can act on it. What a planner must not have is the ABILITY to start work that
// outlives its plan, and a ctx it could pass to something else is exactly that ability.
//
// DELIBERATELY NOT ON THE FAÇADE YET, and each omission is a deferral with a named reason rather
// than an oversight: attendance statistics (the tick and attendance tables are Phase 1, so no
// implementation could fill the method), the item catalogue (Phase 3), and active bid holds (Phase
// 3). A façade method nothing can implement is a method every implementer must fake, and a fake that
// returns zero is indistinguishable from a real answer of zero.
type Ctx interface {
	// PoolID is the pool being planned for. There is exactly one guild per instance and no
	// guild_id column; scope comes from the pool and from the request principal.
	PoolID() core.ULID

	// HeadSeq is the pool's head sequence number at plan time — the seq a "current" balance is read
	// at, and the seq the committed batch will be one past.
	HeadSeq() int64

	// Clock is the INJECTED clock. `time.Now` is banned outside internal/clock (canonical §2) and
	// a strategy that read a wall clock could not be replayed.
	Clock() clock.Clock

	// Rng is the SEEDED random source. Its Seed() is written onto ledger_batch.rng_seed, which is
	// what makes a replay byte-identical; without the seed, a tie-break coin flip makes the ledger
	// unreproducible and the determinism property is meaningless.
	Rng() Rng

	// ConfigJSON is the pool's strategy config, VERBATIM. The planner parses it against its own
	// ConfigSchema and copies these exact bytes onto the proposal's ConfigSnapshotJSON, so that
	// changing a pool's config later cannot change what a past batch meant.
	ConfigJSON() string

	// Balance is POSITIONAL, never temporal: the balance of an account for a balance kind as of a
	// seq. A backdated effective_at must not change what a past balance WAS.
	Balance(account core.ULID, balanceKind string, asOfSeq int64) (core.Centipoints, error)

	// HasHistory reports whether the account has any committed ledger entry for a balance kind as of
	// a seq. POSITIONAL, exactly like Balance, and for the same reason.
	//
	// A ZERO BALANCE AND NO HISTORY ARE DIFFERENT FACTS, and the difference is the whole reason this
	// method exists rather than a `Balance(...) == 0` test at the call site. A veteran who has earned
	// eight hundred points and spent every one of them has a balance of zero and four years of
	// statement; a recruit created this morning has a balance of zero and nothing. `start_points`
	// grants an opening balance to the second and must never grant it to the first — that is
	// property P7, "the everyone-got-1000-points-again ticket" (docs/design/04-testing.md), and it is
	// unanswerable from a sum.
	//
	// It is a BOOL rather than an entry count deliberately: the question a planner may ask is "has
	// this account any history?", and a number invites arithmetic on it — an eligibility rule of
	// "fewer than three entries" is a rule about the shape of the log rather than about the guild,
	// and it would silently change meaning the day the ledger writes an extra entry per event.
	HasHistory(account core.ULID, balanceKind string, asOfSeq int64) (bool, error)

	// Roster is every account in the pool, system accounts included and flagged as such. A decay
	// run with no explicit account list resolves it here.
	Roster() ([]AccountRef, error)

	// SystemAccount resolves one of the four SystemKey* constants to its account id, so a planner
	// can route a solo kill to the guild bank without knowing what a guild bank's row looks like.
	SystemAccount(systemKey string) (core.ULID, error)

	// Allocate is the SHARED largest-remainder allocator, implemented in internal/ledger and
	// reached through here.
	//
	// It is on the façade for the same reason Rng is an interface here and an implementation there:
	// the algorithm is the one piece of arithmetic every zero-sum strategy must get identically
	// right, `.claude/rules/ledger-and-strategy.md` requires it to be shared rather than
	// re-implemented per strategy, and the package that owns it also owns the system accounts its
	// degenerate cases route to — which makes it unreachable by import from a package that may not
	// import internal/store transitively. A planner that divided its own credits would mint or
	// destroy a centipoint on nearly every award, and the drift is invisible per-award and obvious
	// per-month.
	Allocate(total core.Centipoints, shares []Share, emptyAccount core.ULID) ([]Allocation, error)
}

// PointStrategy is a guild's DKP model: a pure function from an event to a proposed batch.
//
// EVERY METHOD IS TOTAL. A strategy that has no auction returns ErrUnsupported from SettleAuction; it
// does not omit the method, because an interface a caller must type-assert against is an interface
// where a missing case is a runtime nil rather than a compile error. The set of things a DKP system
// can be asked is the same across strategies; only the answers differ.
//
// The interface is deliberately WIDE and the alternative is worse. Splitting it into Planner,
// Pricer and Auctioneer would let a strategy silently not implement a half — and "this pool's
// strategy cannot answer that" is a fact the API must be able to state precisely, per operation,
// which ErrUnsupported does and a missing interface does not.
type PointStrategy interface {
	// ID is the permanent, lowercase snake_case identifier ('fixed_price'). It is written onto
	// every batch this strategy plans and is therefore PUBLIC API: renaming it orphans history.
	ID() string

	// Version is the semver of the PLANNING RULES, snapshotted onto every batch. It changes when
	// the same event would now produce a different proposal — not when a comment is fixed.
	Version() string

	// RuleKind is which of a pool's three questions this strategy answers, and therefore which of
	// the pool's three slots it may occupy (ADR-0026).
	//
	// It is on the interface rather than in the catalogue so that adding a strategy without deciding
	// is a COMPILE ERROR. The decision is not incidental: it is what makes a `tick` pool's inability
	// to award an item a refusal on the settings form instead of a 501 during loot.
	RuleKind() RuleKind

	// BalanceKinds are the ledger balance kinds this strategy moves ('dkp'; 'ep' and 'gp' for
	// epgp; 'sk_position' for suicide_kings).
	BalanceKinds() []string

	// ConfigSchema is the JSON Schema for this strategy's pool config, as bytes. It renders the
	// pool-settings form and validates the config at the API edge; every knob a guild can turn
	// lives here and there is no second place.
	ConfigSchema() []byte

	// The five planners. Each returns a BatchProposal the ledger validates and commits, or an
	// error that names the strategy.
	PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error)
	PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error)
	PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error)
	PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error)
	PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error)

	// Spendable is what this account may commit to a purchase right now. It is a SUM over committed
	// entries, minus active holds — never a computed decay, never a weighting
	// (.claude/rules/ledger-and-strategy.md).
	Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error)

	// Priority is where this account stands in a queue for loot, when the strategy has a queue.
	Priority(ctx Ctx, acct AccountRef) (Priority, error)

	// PriceHint is what an item is expected to cost, for a bidding UI. nil with a nil error means
	// "no hint for this item"; ErrUnsupported means "this strategy has no concept of one".
	PriceHint(ctx Ctx, item ItemRef) (*core.Centipoints, error)

	// ValidateBid rejects a bid before it is accepted into a session.
	ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error

	// SettleAuction decides a session's winners.
	SettleAuction(ctx Ctx, s Session, bids []Bid) (Resolution, error)

	// Invariants is the CATALOGUE of rules this strategy constrains itself with — every rule any
	// of its planners may attach to a proposal.
	//
	// The set the ledger actually executes is the one on the PROPOSAL (BatchProposal.Invariants),
	// because which rules apply depends on what the batch did: a fixed-price award that redistributes
	// to attendees declares LargestRemainderSumsToDebit and one that routes to the guild bank has no
	// split to make that claim about. The commit-time engine rejects an invariant scoped to a balance
	// kind the batch does not touch, so a single fixed set per strategy is not expressible.
	//
	// A strategy that declares NOTHING here is a red flag and is rejected in review: the declared
	// set is what the ledger checks before committing, and an empty set means the ledger trusts you.
	// TestFixedPrice_EveryPlannerInvariant_IsDeclared asserts the two stay in step.
	Invariants() []Invariant
}

// Unsupported builds the error an optional method returns, so that every strategy words it
// identically and a caller can match on both the sentinel and the operation name.
//
// The operation is in the message rather than only in the caller's context because this error
// crosses a package boundary on its way to an HTTP 501, and "strategy does not support this
// operation" with no subject is precisely the support ticket nobody can act on.
func Unsupported(strategyID, operation string) error {
	return fmt.Errorf("strategy %s cannot %s: %w", strategyID, operation, ErrUnsupported)
}
