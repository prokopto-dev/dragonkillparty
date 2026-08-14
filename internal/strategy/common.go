package strategy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/bits"
	"sort"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// What every strategy in this package does identically, in one place. Phase 1, #193.
//
// It exists because the alternative is arithmetic. `fixed_price` shipped first and carried these
// helpers privately; `tick`, `cap` and `start_points` need the same config strictness, the same
// share validation, the same zero-sum assertion and the same reversal, and Go puts them all in one
// package — so a second copy is not merely duplication, it is a NAME COLLISION that forces one of
// the two copies to be renamed and then to drift.
//
// EVERY FUNCTION HERE THAT NAMES A STRATEGY IN AN ERROR TAKES THE ID AS ITS FIRST ARGUMENT. That is
// the whole reason these could not simply be shared as they were: a message reading
// "fixed_price: account X appears twice" for a batch `tick` planned sends an officer to read the
// wrong pool's settings. The format strings are otherwise unchanged from the originals, so the
// extraction moved code without moving behaviour — which is what makes `fixed_price`'s committed
// goldens and its rejection table the check on this file rather than a hope.
//
// PURITY (law 3) applies here as everywhere in this package: no internal/store, no wall clock, no
// math/rand, no float. arch_test.go walks the real import graph and would say so.

// ErrNothingToPlan reports that the event was legal but produced no entries — a decay run in which
// every balance rounded to nothing, an attendance tick whose every attendee had weight zero, a cap
// run in which nobody is above the ceiling, a start-points run in which everybody has already been
// granted.
//
// It is an ERROR rather than an empty proposal because ledger_batch carries CHECK (entry_count > 0)
// and the BatchNonEmpty invariant rejects an empty batch at commit time. A caller that receives this
// declines to write a batch; it does not write an empty one. For the cadence families that is the
// `skipped` run state (.claude/rules/decay-and-jobs.md §4) — a period that produced nothing stays
// distinguishable from a period nobody ran.
var ErrNothingToPlan = fmt.Errorf("%w: it produces no entries", ErrInvalidEvent)

// basisPointsWhole is 100% in basis points. Ratios are suffixed `_bp` and expressed as integers
// throughout, because the alternative is a float and there are none here.
const basisPointsWhole = 10_000

// decodeConfig parses a pool's config document over a struct preloaded with the strategy's defaults.
//
// STRICTLY, IN TWO PASSES, and the reason for the first one is the whole lesson of this function.
// Three rejections below were each found in review of the fixed_price PR, and all three are the same
// defect: a document the SCHEMA rejects that the PARSER accepted, leaving the pool running defaults
// nobody chose. They are fixed once here rather than once per strategy, because "every strategy
// remembered to do the two-pass decode" is exactly the property that stops being true at the fourth
// one.
//
// PASS ONE decodes into a map of RAW MEMBERS, because that is the only representation that preserves
// both PRESENCE and NULLNESS — the two facts a struct decode destroys:
//
//   - A NULL MEMBER. `{"tick_award_cp": null}` is the subtle one. encoding/json treats a JSON null
//     decoded into a non-pointer field as a documented NO-OP: no error, field untouched. The struct
//     was preloaded with the defaults, so the officer's null reads back as the default — the schema
//     says `"type": "integer"`, which does not admit null, and the pool silently runs a value nobody
//     typed. DisallowUnknownFields does not help: the key is perfectly known. Nothing about a struct
//     decode can distinguish "absent" from "present and null", so the check has to happen where the
//     raw member is still visible.
//   - A BARE `null` document. Unmarshalling it yields a NIL MAP rather than an error, for the same
//     reason — so the same pass catches it, as an absence of the object the schema's
//     `"type": "object"` requires.
//   - TRAILING CONTENT. `{...}{...}` fails this pass outright, as do an array and a scalar.
//
// PASS TWO decodes the struct with DisallowUnknownFields, which catches the third:
//
//   - An UNKNOWN KEY. `{"decay_pb": 1000}` — a transposition of decay_bp — unmarshals without error
//     and leaves decay at 0. The officer sets decay, the form shows decay, and no decay is ever
//     posted. The schema says additionalProperties: false; this is what makes the parser agree. It is
//     RECURSIVE, so an unknown key inside a nested object is refused too.
//
// A decimal or a quoted number needs no special handling: it cannot unmarshal into an int64, which
// is canonical §1 enforced by the type system rather than by a check.
//
// An ABSENT config is not a malformed one: a pool that has set nothing runs the defaults, and the
// column's own default is '{}'. Both return with cfg untouched.
func decodeConfig(strategyID, configJSON string, cfg any) error {
	raw := strings.TrimSpace(configJSON)
	if raw == "" || raw == "{}" {
		return nil
	}

	members, err := rawConfigMembers(strategyID, raw)
	if err != nil {
		return err
	}

	for _, name := range sortedMemberNames(members) {
		if isJSONNull(members[name]) {
			return fmt.Errorf(
				"%s: config knob %q is null; the schema gives it a type, and null is not a value of "+
					"any of them. Omit the knob to take its default, or give it one: %w",
				strategyID, name, ErrInvalidConfig)
		}
	}

	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse %s config: %w: %w", strategyID, ErrInvalidConfig, err)
	}

	return nil
}

// rawConfigMembers decodes the config into its top-level members, undecoded.
//
// The nil-map check is not defensive: `json.Unmarshal("null", &m)` leaves m nil and returns no
// error, so a nil map here means the document was the JSON literal null rather than an object, and
// an empty map means `{}`. Those are different documents and only one of them is legal.
func rawConfigMembers(strategyID, raw string) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage

	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return nil, fmt.Errorf("parse %s config: %w: %w", strategyID, ErrInvalidConfig, err)
	}

	if members == nil {
		return nil, fmt.Errorf(
			"%s: config is the JSON literal null, not an object; an unset config is an empty column, "+
				"not a null document: %w", strategyID, ErrInvalidConfig)
	}

	return members, nil
}

// isJSONNull reports whether a raw member is the literal null, ignoring the whitespace a formatter
// may have left around it.
func isJSONNull(member json.RawMessage) bool {
	return string(bytes.TrimSpace(member)) == "null"
}

// sortedMemberNames returns the member names in a stable order, so that a config with two bad knobs
// names the same one on every run. A map range would report whichever key Go happened to visit
// first, and a test asserting the message would be intermittently red.
func sortedMemberNames(members map[string]json.RawMessage) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// proposeZeroSum assembles a finished BatchProposal and performs the planner-side balance assertion.
//
// EVERY PLANNER IN THIS PACKAGE RETURNS THROUGH HERE, which is what makes the three things below
// true of all of them rather than of the ones somebody remembered:
//
//   - the config snapshot travels with the batch, verbatim, so changing a pool's config later cannot
//     change what a past batch meant;
//   - the effective time is game truth, defaulting to the injected clock rather than to a zero
//     timestamp that would land the batch in 1970;
//   - the entries sum to zero, checked HERE. LargestRemainderSumsToDebit and SumZero would both
//     reject a broken batch at commit time, but a failure here names the planner and the event while
//     a failure there names a row.
//
// The seed is nil because no strategy in the earn family consumes randomness: their only ordering is
// the account id, deliberately, so that two replays agree. A strategy that DID consume randomness
// would have to carry ctx.Rng().Seed() here — a seed on a batch asserts that replaying from it
// reproduces the plan, and carrying one that was never used makes that assertion true only by
// irrelevance.
func proposeZeroSum(
	ctx Ctx, strategyID, strategyVersion, kind string, effectiveAt core.Micros, reason string,
	entries []EntryProposal, invariants []Invariant,
) (BatchProposal, error) {
	p := BatchProposal{
		Kind:               kind,
		StrategyID:         strategyID,
		StrategyVersion:    strategyVersion,
		ConfigSnapshotJSON: ctx.ConfigJSON(),
		RngSeed:            nil,
		Reason:             reason,
		EffectiveAt:        effectiveAtOr(ctx, effectiveAt),
		Entries:            entries,
		Invariants:         invariants,
	}

	net, ok := p.NetAmountCp()
	if !ok {
		return BatchProposal{}, fmt.Errorf("%s: the %s batch's %d entries sum past int64: %w",
			strategyID, kind, len(entries), ErrInvalidEvent)
	}

	if net != 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: the %s batch's %d entries sum to %d rather than 0; every batch this strategy writes "+
				"moves points between accounts and mints none: %w",
			strategyID, kind, len(entries), net, ErrInvalidEvent)
	}

	return p, nil
}

// effectiveAtOr returns the caller's game-truth time, or the injected clock's when they supplied
// none.
//
// The clock is the ONLY source of "now" a strategy has (`time.Now` is banned outside internal/clock,
// canonical §2) and this is where it is consumed. A zero EffectiveAt is a caller that did not
// specify, not a caller that meant 1970 — and a batch stamped 1970 sorts before every real one in
// the statement view and lands in the wrong effective_day bucket forever.
func effectiveAtOr(ctx Ctx, supplied core.Micros) core.Micros {
	if supplied != 0 {
		return supplied
	}

	return core.FromTime(ctx.Clock().Now())
}

// reversePlan is the reversal every quantity-kind strategy in this package plans: entry-wise
// negation, restamped to the present, declaring SumZero and nothing else.
//
// ENTRY-WISE NEGATION IS CORRECT FOR THESE STRATEGIES, and it is not correct everywhere. Their only
// balance kind is `dkp`, a plain quantity: subtracting what was added restores the balance exactly,
// whatever happened in between. A strategy whose kind is positional (suicide_kings' sk_position) or
// paired (epgp's ep/gp) must NOT call this — it must override PlanReversal and say so in its doc
// comment, because negating a position delta does not restore a list; everyone below the winner
// shifted up in the meantime.
//
// THE REVERSAL'S EFFECTIVE TIME IS NOT THE ORIGINAL'S. A correction is a new economic event at the
// time it is decided; backdating it would silently rewrite what every intermediate balance meant.
//
// A REVERSAL DOES NOT DECLARE NonNegative, AND THAT IS THE POINT:
//
//	an officer credits a tick to the wrong raider  ->  +500 to Alice
//	Alice spends it                                ->  -500, balance 0
//	the officer reverses the erroneous tick        ->  -500, balance -500  <- below the floor
//
// With the floor declared, the ledger REJECTS that third batch. The ledger is append-only: there is
// no UPDATE, no DELETE, and a reversal is the only repair primitive there is
// (.claude/rules/ledger-and-strategy.md). A floor on it therefore does not prevent a debt — it
// prevents the CORRECTION, and the guild is left with a mistake that is provably wrong and
// permanently unfixable, which is a worse outcome than a visible negative balance by every measure
// that matters. The debt is the correct outcome and it is meant to be seen. What a floor
// legitimately guards is a SPEND or a scheduled deduction, where the planner declares it and an
// overdraft is refused before anything is written.
//
// IT ALSO DOES NOT READ THE POOL'S CURRENT CONFIG. Reversing a batch must not depend on a document
// that has nothing to do with it: a guild that switched strategies — or that added a knob this
// version does not know — has a pool config the planner cannot parse, and every batch in the pool's
// history would become unreversible the moment the config changed. History is immutable and the
// repair primitive must not be contingent on the present. Nothing here needs the config anyway: the
// floor is gone for the reason above, and the batch carries its own ConfigSnapshotJSON, which
// LedgerBatch.Reversal copies forward.
func reversePlan(ctx Ctx, strategyID string, b LedgerBatch) (BatchProposal, error) {
	if b.StrategyID != "" && b.StrategyID != strategyID {
		return BatchProposal{}, fmt.Errorf(
			"%s cannot reverse batch %s, which was planned by %s; a reversal must be planned by the "+
				"strategy that planned the original: %w",
			strategyID, b.ID, b.StrategyID, ErrInvalidEvent)
	}

	reversal, err := b.Reversal()
	if err != nil {
		return BatchProposal{}, err
	}

	// The invariant set is REPLACED rather than inherited: the original's set constrained an earn or
	// a spend, and this is neither. BatchProposal.Negated already drops NonNegative by default, so
	// this is no longer what keeps the floor off a reversal — the default is. It stays because a
	// strategy declares what constrains it rather than inheriting whatever an earlier version of
	// itself declared, and because a strategy's Invariants are read in review as its rules.
	reversal.Invariants = []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
	}
	reversal.EffectiveAt = effectiveAtOr(ctx, 0)

	return reversal, nil
}

// adjustmentProposal is the officer's manual movement of points, identical in every strategy here.
//
// It is two entries, never one. An officer who could add points without naming where they came from
// could inflate a guild's economy invisibly, and the counterparty — the guild bank unless the caller
// names another — is what makes every adjustment answerable with "out of what?".
//
// The floor is the POOL's, passed in: NonNegative is what refuses an adjustment that would overdraw
// an account, and it is declared here rather than left to the caller because an adjustment is the
// one planner every strategy shares and the floor is the one thing that differs between them.
func adjustmentProposal(
	ctx Ctx, strategyID, strategyVersion string, floorCp core.Centipoints, ev AdjustmentEvent,
) (BatchProposal, error) {
	if ev.Account.ID == "" {
		return BatchProposal{}, fmt.Errorf("%s: adjustment has no account: %w",
			strategyID, ErrInvalidEvent)
	}

	if ev.AmountCp == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: adjustment moves 0 centipoints; drop the adjustment instead of writing a zero: %w",
			strategyID, ErrInvalidEvent)
	}

	counterparty := ev.Counterparty

	if counterparty == "" {
		bank, err := ctx.SystemAccount(SystemKeyGuildBank)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", strategyID, err)
		}

		counterparty = bank
	}

	if counterparty == ev.Account.ID {
		return BatchProposal{}, fmt.Errorf(
			"%s: account %s is its own counterparty, so the adjustment moves nothing: %w",
			strategyID, ev.Account.ID, ErrInvalidEvent)
	}

	// The adjusted account leads: it is the subject of the batch, and the statement view renders the
	// entries in the order they were planned.
	entries := []EntryProposal{
		{AccountID: ev.Account.ID, BalanceKind: BalanceKindDKP, AmountCp: ev.AmountCp},
		{AccountID: counterparty, BalanceKind: BalanceKindDKP, AmountCp: -ev.AmountCp},
	}

	return proposeZeroSum(ctx, strategyID, strategyVersion, kinds.KindAdjustment, ev.EffectiveAt, ev.Reason,
		entries, []Invariant{
			{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
			{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floorCp},
		})
}

// cadenceTargets resolves the accounts a cadence run touches and the guild bank that balances it.
//
// The four members of the cadence family — decay_percent, decay_window, cap and start_points — open
// their run identically, and the four steps are each load-bearing rather than ceremony: an empty
// account list means the whole roster, the accounts are SORTED because the entry order is hashed into
// the batch, a repeat is REFUSED because a run that named an account twice would read the same as-of
// balance twice and post the period's entry twice, and the bank is resolved before any arithmetic so
// a run that has nowhere to put the points fails before it computes any.
//
// `verb` is what the roster is being read FOR — "decay", "trim", "grant" — so the error names the run
// an officer is looking at rather than the function they cannot see.
func cadenceTargets(
	ctx Ctx, strategyID, verb string, run DecayRun,
) (targets []AccountRef, bank core.ULID, err error) {
	accounts := run.Accounts

	if len(accounts) == 0 {
		accounts, err = ctx.Roster()
		if err != nil {
			return nil, "", fmt.Errorf("%s: read the roster to %s: %w", strategyID, verb, err)
		}
	}

	bank, err = ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return nil, "", fmt.Errorf("%s: resolve the guild bank: %w", strategyID, err)
	}

	targets = sortedAccounts(accounts)
	if err := checkDistinctAccounts(strategyID, targets); err != nil {
		return nil, "", err
	}

	return targets, bank, nil
}

// spendableBalance is the account's balance at the pool head.
//
// NO COMPUTED DECAY, NO CAP AND NO WEIGHTING. Decay and cap trims are POSTED as explicit batches, so
// they are already in the sum; a strategy that also applied a rate here would apply it twice, and
// the second application would be invisible in every statement.
// `.claude/rules/ledger-and-strategy.md` permits computed weighting in Priority and forbids it here,
// and this is the method that would be tempting.
//
// Active bid holds are subtracted in Phase 3, when holds exist. Until then a spendable balance is a
// balance, and saying so is better than a placeholder that reads as though holds were handled.
func spendableBalance(ctx Ctx, strategyID string, acct AccountRef) (core.Centipoints, error) {
	balance, err := ctx.Balance(acct.ID, BalanceKindDKP, ctx.HeadSeq())
	if err != nil {
		return 0, fmt.Errorf("%s: spendable balance for account %s: %w", strategyID, acct.ID, err)
	}

	return balance, nil
}

// priorityBySpendable ranks candidates for an item by spendable balance, highest first.
//
// Balance is the accumulated cost of turning up, and the tiebreak at equal balance is the account
// id, ascending, which is deterministic and therefore replayable. A random tiebreak here would make
// two replays of the same loot decision differ, which is the defect the allocator's account_id
// tiebreak exists to prevent.
func priorityBySpendable(ctx Ctx, strategyID string, acct AccountRef) (Priority, error) {
	spendable, err := spendableBalance(ctx, strategyID, acct)
	if err != nil {
		return Priority{}, err
	}

	return Priority{
		Rank:     int64(spendable),
		Tiebreak: acct.ID.String(),
		Reason:   "spendable balance",
	}, nil
}

// checkDistinctShares rejects a list that names the same account twice.
//
// A REPEAT IS INDISTINGUISHABLE FROM A WEIGHT, and that is the whole reason this is an error rather
// than a silent fold. `[{A,1},{A,1}]` and `[{A,2}]` mean the same thing to the arithmetic, so a
// duplicate is never a caller expressing a bigger share — it is a caller who assembled the list
// twice, from two sources, or from a join that fanned out. Folding them silently doubles somebody's
// credit; rejecting names the account and costs one comparison.
//
// It relies on the list being SORTED, so only adjacent pairs need comparing. Every caller sorts
// first because the entry order is hashed into the batch.
func checkDistinctShares(strategyID string, sorted []Share) error {
	for i := 1; i < len(sorted); i++ {
		if sorted[i].AccountID == sorted[i-1].AccountID {
			return fmt.Errorf(
				"%s: account %s appears twice in the same event; a repeated account is a list that "+
					"was built twice, not a bigger share — say so with the weight: %w",
				strategyID, sorted[i].AccountID, ErrInvalidEvent)
		}
	}

	return nil
}

// checkDistinctAccounts is checkDistinctShares for a plain account list. A cadence run that names an
// account twice would read the same as-of balance twice and post two full debits — charging the
// period's deduction twice while SumZero and NonNegative both still pass, because the arithmetic is
// self-consistent and simply wrong.
func checkDistinctAccounts(strategyID string, sorted []AccountRef) error {
	for i := 1; i < len(sorted); i++ {
		if sorted[i].ID == sorted[i-1].ID {
			return fmt.Errorf(
				"%s: account %s appears twice in the run; each balance is read once per period, "+
					"and a repeat would post the period's entry twice: %w",
				strategyID, sorted[i].ID, ErrInvalidEvent)
		}
	}

	return nil
}

// checkShare rejects the two share shapes no planner has a defensible answer for.
func checkShare(strategyID string, s Share) error {
	if s.AccountID == "" {
		return fmt.Errorf("%s: a share names no account: %w", strategyID, ErrInvalidEvent)
	}

	if s.Weight < 0 {
		return fmt.Errorf("%s: account %s has weight %d; a negative weight inverts that account's "+
			"quota while still counting toward the total: %w",
			strategyID, s.AccountID, s.Weight, ErrInvalidEvent)
	}

	return nil
}

// sortedShares copies the shares into account order.
//
// A COPY, so a planner never reorders its caller's slice, and SORTED, because the entry order is
// preserved by Canonical and hashed into the batch: two callers that pass the same attendees in
// different orders must produce byte-identical proposals. This is the determinism defect that is
// invisible in every test that happens to build its input in sorted order.
func sortedShares(in []Share) []Share {
	out := make([]Share, len(in))
	copy(out, in)

	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })

	return out
}

// sortedAccounts copies the accounts into id order, for the same reason sortedShares does. A roster
// read is a query result, and a query without an ORDER BY is a query whose order is a coincidence.
func sortedAccounts(in []AccountRef) []AccountRef {
	out := make([]AccountRef, len(in))
	copy(out, in)

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

// optionalULID turns an empty id into the nil pointer a nullable column wants. An empty string in a
// TEXT column is a value; a missing pointer is an absence, and provenance that was never recorded
// must read as absent.
func optionalULID(id core.ULID) *core.ULID {
	if id == "" {
		return nil
	}

	return &id
}

// addCentipoints adds two centipoint values, reporting overflow rather than wrapping. The ledger's
// invariant engine holds an identical function for the identical reason: a wrapped sum satisfies a
// zero-sum check by arithmetic accident, which is the one way conservation can be defeated without
// any individual amount looking wrong. The two cannot be shared — this package may not import that
// one — and both are four lines with a property test behind them.
func addCentipoints(a, b core.Centipoints) (sum core.Centipoints, ok bool) {
	sum = a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}

	return sum, true
}

// subCentipoints subtracts one centipoint value from another, reporting overflow rather than
// wrapping.
//
// It is not addCentipoints with a negated operand, and that is the whole reason it exists: negating
// math.MinInt64 wraps back to itself, so `add(a, -b)` is silently wrong for exactly the input a
// planner is least likely to have tested. Written as a subtraction, the standard signed test — the
// result must not cross to the other side of `a` from `b`'s sign — covers every input.
func subCentipoints(a, b core.Centipoints) (diff core.Centipoints, ok bool) {
	diff = a - b
	if (b < 0 && diff < a) || (b > 0 && diff > a) {
		return 0, false
	}

	return diff, true
}

// mulCentipoints multiplies a centipoint amount by a non-negative integer factor, reporting overflow
// rather than wrapping.
//
// The check is a division rather than a 128-bit product because the answer must fit in an int64 to be
// usable at all: if `amount * factor` does not, there is no entry to write and the planner must say
// so. bits.Mul64 would compute a correct 128-bit value that then cannot be stored.
func mulCentipoints(amount core.Centipoints, factor int64) (product core.Centipoints, ok bool) {
	if factor == 0 || amount == 0 {
		return 0, true
	}

	product = amount * core.Centipoints(factor)
	if product/core.Centipoints(factor) != amount {
		return 0, false
	}

	return product, true
}

// decayAmount is balance * bp / 10000, floored, computed exactly in integers. `balance` and `bp` are
// both non-negative and `bp` is at or below 10000 — every caller either validates them or negates a
// debt into a magnitude first.
//
// It lives here rather than beside the first strategy that needed it because three now do —
// fixed_price's built-in haircut, decay_percent's rate and decay_percent's debt forgiveness — and a
// second copy in one package is a name collision that forces a rename and then drifts (see this
// file's header).
//
// The 128-bit product is the same technique ledger.Allocate uses and for the same reason: `balance *
// bp` overflows int64 for a large balance, a float would be a lint failure and would lose precision
// exactly where the invariant lives, and math/big would allocate per account on a run that touches
// the whole roster. bits.Mul64/Div64 are exact and allocation-free.
//
// Div64 panics when the quotient would not fit in 64 bits. It cannot here: bp <= 10000 = the divisor,
// so the quotient is at most `balance`. scaleByBasisPoints is the sibling for the amounts that must
// REFUSE rather than saturate — it reports overflow instead of widening, because a reduced earning
// that does not fit in an int64 has no entry to write.
//
// FLOORED, never rounded. Rounding a decay to nearest takes a centipoint the configured rate did not
// ask for, and it takes it from every member every period.
func decayAmount(balance core.Centipoints, bp int64) core.Centipoints {
	hi, lo := bits.Mul64(uint64(balance), uint64(bp))
	q, _ := bits.Div64(hi, lo, basisPointsWhole)

	return core.Centipoints(q)
}

// scaleByBasisPoints multiplies an amount by a ratio in basis points, FLOORED, in integers only.
//
// FLOORED, never rounded to nearest, and the direction is the same argument decay makes: rounding a
// ratio up credits or takes a centipoint the configured rate did not ask for, on every entry, for
// every member, forever. A half-share of an odd tick is the smaller half.
//
// It reports overflow rather than wrapping, and refuses a negative amount or ratio rather than
// producing a plausible number: `amount * bp` is checked with mulCentipoints, so a base large enough
// to overflow at the configured ratio is a named planner error and not a wrapped credit. Both inputs
// are non-negative by construction in every caller; the guard is what makes that a fact rather than
// an assumption.
func scaleByBasisPoints(amount core.Centipoints, bp int64) (scaled core.Centipoints, ok bool) {
	if amount < 0 || bp < 0 {
		return 0, false
	}

	product, ok := mulCentipoints(amount, bp)
	if !ok {
		return 0, false
	}

	return product / basisPointsWhole, true
}
