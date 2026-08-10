package strategy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/bits"
	"sort"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// fixed_price — the simplest DKP model that is still a real one. Phase 0 PR 10b.
//
// An item has a price. The buyer pays it. Points are earned by turning up, and a tick is worth what
// the pool says it is worth. There is no bidding, so there is nothing to validate and nothing to
// settle — those three methods return ErrUnsupported and say which strategy said so.
//
// It ships first because it is the shape every other strategy is a variation of, and because it
// exercises every part of the seam: it splits with the shared allocator, it reads balances
// positionally, it uses the injected clock, it declares invariants that actually constrain it, and
// it consumes no randomness at all — which the determinism property asserts rather than assumes.
//
// WHERE THE MONEY GOES, and why it is a config knob rather than a second strategy. `proceeds` picks
// between a SINK (the price lands on the guild bank and leaves circulation) and REDISTRIBUTION (the
// price is split across the night's attendees by largest remainder). Both are fixed-price guilds and
// both are common on P99; the difference is one line of config, and forcing it to be two strategies
// would mean two copies of the price-resolution rules that could then disagree.
//
// EVERY BATCH THIS STRATEGY WRITES SUMS TO ZERO. Attendance debits the guild bank and credits the
// raiders; an award debits the buyer and credits the bank or the attendees; an adjustment moves
// points between an account and a counterparty; decay debits the roster and credits the bank. Points
// are never minted and never destroyed, only moved — which is what makes conservation checkable
// against a single column (`net_amount_cp = 0`) instead of an aggregate over the whole ledger.

// The compile-time proof that the implementation matches the interface. If PointStrategy grows a
// method, `go build` says so on the next save rather than a reviewer noticing.
var _ PointStrategy = FixedPrice{}

// FixedPrice is the fixed-price point strategy. It is STATELESS: everything it needs arrives through
// the Ctx façade, which is what lets one value serve every pool and every request concurrently.
type FixedPrice struct{}

// The strategy's identity. ID is written onto every batch it plans and is therefore public API —
// renaming it orphans history. Version changes when the same event would now produce a different
// proposal, never for a comment.
const (
	fixedPriceID      = "fixed_price"
	fixedPriceVersion = "0.1.0"
)

// The `proceeds` knob's values: where an award's price goes.
const (
	// ProceedsGuildBank — the price leaves circulation into the guild bank. A sink economy: points
	// come from ticks and go to the bank, and the bank's balance is the count of everything ever
	// spent.
	ProceedsGuildBank = "guild_bank"

	// ProceedsAttendees — the price is split across the night's attendees by largest remainder. A
	// zero-sum economy: what one raider pays, the others receive.
	ProceedsAttendees = "attendees"
)

// The `solo_policy` knob's values: where an award's proceeds go when there is nobody to split them
// across. A solo kill, or an item nobody else was eligible for.
//
// Both are SYSTEM ACCOUNTS and that is the point — a degenerate case routes to a ledger-addressable
// account, never to a silent drop, because that is what keeps conservation verifiable when the
// arithmetic has nowhere to put the money.
const (
	// SoloPolicyGuildBank — the proceeds land on the guild bank, as if `proceeds` were guild_bank
	// for this one award. The usual choice.
	SoloPolicyGuildBank = SystemKeyGuildBank

	// SoloPolicyWriteOff — the proceeds land on write_off, which is the account that exists to
	// swallow value nobody received. A guild that wants a solo kill to cost the buyer without
	// enriching the bank picks this.
	SoloPolicyWriteOff = SystemKeyWriteOff
)

// ErrNothingToPlan reports that the event was legal but produced no entries — a decay run in which
// every balance rounded to nothing, an attendance tick whose every attendee had weight zero.
//
// It is an ERROR rather than an empty proposal because ledger_batch carries CHECK (entry_count > 0)
// and the BatchNonEmpty invariant rejects an empty batch at commit time. A caller that receives this
// declines to write a batch; it does not write an empty one. Returning it here names the strategy and
// the reason, where the commit-time rejection could only name the shape.
var ErrNothingToPlan = fmt.Errorf("%w: it produces no entries", ErrInvalidEvent)

// ID is the permanent identifier written onto every batch this strategy plans.
func (FixedPrice) ID() string { return fixedPriceID }

// Version is the semver of the planning rules, snapshotted onto every batch.
func (FixedPrice) Version() string { return fixedPriceVersion }

// BalanceKinds is the one balance kind this strategy moves. A single plain quantity, which is what
// makes entry-wise negation the correct reversal (see PlanReversal).
func (FixedPrice) BalanceKinds() []string { return []string{BalanceKindDKP} }

// fixedPriceConfigSchema is the JSON Schema for the pool config: every knob a guild can turn, in one
// place, in the form that renders the pool-settings form and validates the config at the API edge.
//
// Draft 2020-12. `additionalProperties: false` is deliberate and load-bearing — a typo'd knob
// (`decay_pb`) must be a validation error at the edge and not a silently ignored key that leaves the
// pool running the default, which is how a guild discovers three months later that decay was never
// on. Every money field is an INTEGER named `_cp`: canonical §1 bans a decimal on the wire, and a
// schema that said `number` would invite one.
const fixedPriceConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Fixed price",
  "description": "Items have a set price. Points are earned per raid tick and spent at that price.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "default_price_cp": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "title": "Default item price (centipoints)",
      "description": "Used when the item carries no catalogue price and the officer names none. 0 means every item must be priced explicitly."
    },
    "tick_award_cp": {
      "type": "integer",
      "minimum": 1,
      "default": 100,
      "title": "Points per raid tick (centipoints)",
      "description": "What one attendance tick is worth. 100 centipoints is 1.00 point."
    },
    "proceeds": {
      "type": "string",
      "enum": ["guild_bank", "attendees"],
      "default": "guild_bank",
      "title": "Where an item's price goes",
      "description": "guild_bank drains the points out of circulation; attendees splits them across the night's raiders by largest remainder."
    },
    "solo_policy": {
      "type": "string",
      "enum": ["guild_bank", "write_off"],
      "default": "guild_bank",
      "title": "Where proceeds go with nobody to split them across",
      "description": "A solo kill has no attendees. The price still leaves the buyer; this says which system account receives it."
    },
    "floor_cp": {
      "type": "integer",
      "default": 0,
      "title": "Lowest permitted balance (centipoints)",
      "description": "An award is rejected if it would take the buyer below this. Negative permits going into debt to a limit."
    },
    "decay_bp": {
      "type": "integer",
      "minimum": 0,
      "maximum": 10000,
      "default": 0,
      "title": "Decay per period (basis points)",
      "description": "1000 is 10% of the balance per run. 0 disables decay, and a decay run against a pool with 0 is refused rather than posting an empty batch."
    }
  }
}`

// ConfigSchema returns the JSON Schema document as bytes.
//
// A COPY, not the backing array of the constant: a caller that could mutate the schema could change
// what every pool validates against. The constant is a string precisely so this conversion allocates
// a fresh slice each call.
func (FixedPrice) ConfigSchema() []byte { return []byte(fixedPriceConfigSchema) }

// fixedPriceConfig is the parsed pool config. The JSON tags are the schema's property names and the
// two must agree; TestFixedPrice_ConfigSchema_MatchesTheParsedShape asserts that they do, because a
// knob in the schema that the parser ignores is a knob the settings form offers and nothing reads.
type fixedPriceConfig struct {
	DefaultPriceCp core.Centipoints `json:"default_price_cp"`
	TickAwardCp    core.Centipoints `json:"tick_award_cp"`
	Proceeds       string           `json:"proceeds"`
	SoloPolicy     string           `json:"solo_policy"`
	FloorCp        core.Centipoints `json:"floor_cp"`
	DecayBp        int64            `json:"decay_bp"`
}

// defaultFixedPriceConfig is the config a pool that has set nothing runs under. It is the struct the
// pool's JSON is decoded OVER, which is what makes an absent key mean "the default" and a present
// `"floor_cp": 0` mean "zero, chosen" — two things a zero value alone cannot distinguish.
func defaultFixedPriceConfig() fixedPriceConfig {
	return fixedPriceConfig{
		DefaultPriceCp: 0,
		TickAwardCp:    100,
		Proceeds:       ProceedsGuildBank,
		SoloPolicy:     SoloPolicyGuildBank,
		FloorCp:        0,
		DecayBp:        0,
	}
}

// config parses and validates the pool's config.
//
// It re-validates what the API edge already validated against ConfigSchema, and the duplication earns
// its keep: the edge validates what a human typed into the settings form, and this validates what
// actually reached the planner — which includes a config written by the importer, by a migration
// backfill, or by a test. A planner that defaulted a bad value would run a DKP system nobody chose.
func (FixedPrice) config(ctx Ctx) (fixedPriceConfig, error) {
	cfg := defaultFixedPriceConfig()

	// An ABSENT config is not a malformed one: a pool that has set nothing runs the defaults, and
	// the column's own default is '{}'. Everything past this point is a document somebody wrote.
	raw := strings.TrimSpace(ctx.ConfigJSON())
	if raw == "" || raw == "{}" {
		return validateFixedPriceConfig(cfg)
	}

	// STRICTLY, IN TWO PASSES, and the reason for the first one is the whole lesson of this function.
	// Three rejections below were each found in review of this PR, and all three are the same defect:
	// a document the SCHEMA rejects that the PARSER accepted, leaving the pool running defaults
	// nobody chose.
	//
	// PASS ONE decodes into a map of RAW MEMBERS, because that is the only representation that
	// preserves both PRESENCE and NULLNESS — the two facts a struct decode destroys:
	//
	//   - A NULL MEMBER. `{"tick_award_cp": null}` is the subtle one. encoding/json treats a JSON
	//     null decoded into a non-pointer field as a documented NO-OP: no error, field untouched. The
	//     struct was preloaded with the defaults, so the officer's null reads back as 100 — the
	//     schema says `"type": "integer"`, which does not admit null, and the pool silently runs a
	//     tick award nobody typed. DisallowUnknownFields does not help: the key is perfectly known.
	//     Nothing about a struct decode can distinguish "absent" from "present and null", so the
	//     check has to happen where the raw member is still visible.
	//   - A BARE `null` document. Unmarshalling it yields a NIL MAP rather than an error, for the
	//     same reason — so the same pass catches it, as an absence of the object the schema's
	//     `"type": "object"` requires.
	//   - TRAILING CONTENT. `{...}{...}` fails this pass outright, as do an array and a scalar.
	//
	// PASS TWO decodes the struct with DisallowUnknownFields, which catches the third:
	//
	//   - An UNKNOWN KEY. `{"decay_pb": 1000}` — a transposition of decay_bp — unmarshals without
	//     error and leaves decay at 0. The officer sets decay, the form shows decay, and no decay is
	//     ever posted. The schema says additionalProperties: false; this is what makes the parser
	//     agree.
	//
	// A decimal or a quoted number needs no special handling: it cannot unmarshal into an int64,
	// which is canonical §1 enforced by the type system rather than by a check.
	members, err := rawConfigMembers(raw)
	if err != nil {
		return fixedPriceConfig{}, err
	}

	for _, name := range sortedMemberNames(members) {
		if isJSONNull(members[name]) {
			return fixedPriceConfig{}, fmt.Errorf(
				"%s: config knob %q is null; the schema gives it a type, and null is not a value of "+
					"any of them. Omit the knob to take its default, or give it one: %w",
				fixedPriceID, name, ErrInvalidConfig)
		}
	}

	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&cfg); err != nil {
		return fixedPriceConfig{}, fmt.Errorf("parse %s config: %w: %w", fixedPriceID, ErrInvalidConfig, err)
	}

	return validateFixedPriceConfig(cfg)
}

// rawConfigMembers decodes the config into its top-level members, undecoded.
//
// The nil-map check is not defensive: `json.Unmarshal("null", &m)` leaves m nil and returns no
// error, so a nil map here means the document was the JSON literal null rather than an object, and
// an empty map means `{}`. Those are different documents and only one of them is legal.
func rawConfigMembers(raw string) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage

	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return nil, fmt.Errorf("parse %s config: %w: %w", fixedPriceID, ErrInvalidConfig, err)
	}

	if members == nil {
		return nil, fmt.Errorf(
			"%s: config is the JSON literal null, not an object; an unset config is an empty column, "+
				"not a null document: %w", fixedPriceID, ErrInvalidConfig)
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

// validateFixedPriceConfig applies the bounds the schema declares, to a config that has already
// parsed. Split from config so that the defaults are validated too — a default that violated its own
// schema would otherwise be the one config nothing ever checked.
func validateFixedPriceConfig(cfg fixedPriceConfig) (fixedPriceConfig, error) {
	switch cfg.Proceeds {
	case ProceedsGuildBank, ProceedsAttendees:
	default:
		return fixedPriceConfig{}, fmt.Errorf("%s: proceeds is %q, want %q or %q: %w",
			fixedPriceID, cfg.Proceeds, ProceedsGuildBank, ProceedsAttendees, ErrInvalidConfig)
	}

	switch cfg.SoloPolicy {
	case SoloPolicyGuildBank, SoloPolicyWriteOff:
	default:
		return fixedPriceConfig{}, fmt.Errorf("%s: solo_policy is %q, want %q or %q: %w",
			fixedPriceID, cfg.SoloPolicy, SoloPolicyGuildBank, SoloPolicyWriteOff, ErrInvalidConfig)
	}

	if cfg.DefaultPriceCp < 0 {
		return fixedPriceConfig{}, fmt.Errorf("%s: default_price_cp is %d, which is negative: %w",
			fixedPriceID, cfg.DefaultPriceCp, ErrInvalidConfig)
	}

	if cfg.TickAwardCp <= 0 {
		return fixedPriceConfig{}, fmt.Errorf("%s: tick_award_cp is %d, which awards nothing: %w",
			fixedPriceID, cfg.TickAwardCp, ErrInvalidConfig)
	}

	// The bounds are the schema's, restated: a rate above 100% would decay a balance past zero in one
	// run, and a negative rate would be growth wearing decay's name.
	if cfg.DecayBp < 0 || cfg.DecayBp > basisPointsWhole {
		return fixedPriceConfig{}, fmt.Errorf("%s: decay_bp is %d, want 0..%d: %w",
			fixedPriceID, cfg.DecayBp, basisPointsWhole, ErrInvalidConfig)
	}

	return cfg, nil
}

// basisPointsWhole is 100% in basis points. Ratios are suffixed `_bp` and expressed as integers
// throughout, because the alternative is a float and there are none here.
const basisPointsWhole = 10_000

// PlanAttendance credits every attendee the configured tick award, debited from the guild bank.
//
// The bank is the counterparty rather than a mint, so the batch sums to zero like every other batch
// this strategy writes. A guild's bank balance therefore reads as "everything ever awarded, minus
// everything ever spent back into it", which is a number an officer can sanity-check.
//
// WEIGHT IS A MULTIPLIER. A flat tick passes weight 1 for everybody. A guild that half-credits a late
// arrival passes 2 for a full share and 1 for a half on a halved base — integer arithmetic
// throughout, with no ratio anywhere. Weight 0 is legal and means "present, earned nothing": the
// entry is dropped rather than written as a zero, because ledger_entry carries
// CHECK (amount_cp <> 0).
func (s FixedPrice) PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if len(ev.Attendees) == 0 {
		return BatchProposal{}, fmt.Errorf("%s: attendance tick has no attendees: %w",
			fixedPriceID, ErrInvalidEvent)
	}

	amount := cfg.TickAwardCp
	if ev.AmountCp != nil {
		amount = *ev.AmountCp
	}

	if amount <= 0 {
		return BatchProposal{}, fmt.Errorf("%s: tick award is %d centipoints, which awards nothing: %w",
			fixedPriceID, amount, ErrInvalidEvent)
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", fixedPriceID, err)
	}

	credits := make([]EntryProposal, 0, len(ev.Attendees)+1)

	attendees := sortedShares(ev.Attendees)
	if err := checkDistinctShares(attendees); err != nil {
		return BatchProposal{}, err
	}

	var total core.Centipoints

	for _, a := range attendees {
		if err := checkShare(a); err != nil {
			return BatchProposal{}, err
		}

		earned, ok := mulCentipoints(amount, a.Weight)
		if !ok {
			return BatchProposal{}, fmt.Errorf(
				"%s: %d centipoints at weight %d for account %s overflows int64: %w",
				fixedPriceID, amount, a.Weight, a.AccountID, ErrInvalidEvent)
		}

		// Weight 0 earns nothing and is DROPPED rather than written as a zero: ledger_entry carries
		// CHECK (amount_cp <> 0). Skipping on the product rather than on the weight keeps it to one
		// rule — "an attendee who earned nothing gets no entry" — instead of one rule before the
		// multiplication and another after it.
		if earned == 0 {
			continue
		}

		sum, ok := addCentipoints(total, earned)
		if !ok {
			return BatchProposal{}, fmt.Errorf("%s: the tick's credits sum past int64: %w",
				fixedPriceID, ErrInvalidEvent)
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
		return BatchProposal{}, fmt.Errorf("%s: every attendee has weight 0, so %w",
			fixedPriceID, ErrNothingToPlan)
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
	// guild). Declaring a rule that constrains nothing is what
	// .claude/skills/add-strategy/SKILL.md calls a red flag.
	return s.propose(ctx, "attendance", ev.EffectiveAt, ev.Reason, entries, []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
	})
}

// PlanAward debits the buyer the item's price and routes the proceeds.
//
// THE BUYER IS NOT EXCLUDED FROM THE SPLIT. In a redistributing guild a raider who buys an item is
// still an attendee of the raid and receives their share of their own payment, which is what makes
// the model zero-sum rather than a tax. Excluding them would be a different DKP system, and one
// nobody asked for.
//
// The price resolves in one order and only one: the officer's explicit price, then the item's
// catalogue price, then the pool's default. Each step is a deliberate override of the one below it,
// and a resolved price of zero or less is refused rather than written as an award of nothing.
func (s FixedPrice) PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if ev.Buyer.ID == "" {
		return BatchProposal{}, fmt.Errorf("%s: award has no buyer: %w", fixedPriceID, ErrInvalidEvent)
	}

	if ev.Buyer.IsSystem() {
		return BatchProposal{}, fmt.Errorf(
			"%s: buyer %s is a system account; the four system accounts are counterparties, never "+
				"purchasers: %w", fixedPriceID, ev.Buyer.ID, ErrInvalidEvent)
	}

	price, err := resolvePrice(cfg, ev)
	if err != nil {
		return BatchProposal{}, err
	}

	itemID := optionalULID(ev.Item.ID)

	entries := []EntryProposal{{
		AccountID:   ev.Buyer.ID,
		CharacterID: ev.CharacterID,
		BalanceKind: BalanceKindDKP,
		AmountCp:    -price,
		ItemID:      itemID,
		ItemAwardID: ev.ItemAwardID,
		RaidID:      ev.RaidID,
	}}

	invariants := []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
	}

	credits, split, err := s.proceeds(ctx, cfg, ev, price)
	if err != nil {
		return BatchProposal{}, err
	}

	if split {
		// Declared only when a split actually happened. LargestRemainderSumsToDebit and SumZero are
		// the same arithmetic and deliberately different rules — this one names the mistake of
		// rounding each credit independently — so claiming it for a batch with a single credit would
		// be asserting something about an allocation that never ran.
		invariants = append(invariants,
			Invariant{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP})
	}

	for _, c := range credits {
		entries = append(entries, EntryProposal{
			AccountID:   c.AccountID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    c.AmountCp,
			ItemID:      itemID,
			ItemAwardID: ev.ItemAwardID,
			RaidID:      ev.RaidID,
		})
	}

	return s.propose(ctx, "award", ev.EffectiveAt, ev.Reason, entries, invariants)
}

// proceeds turns a price into the credits that balance it, and reports whether the allocator ran.
//
// The two config paths differ in more than their destination: the guild-bank path is one credit and
// cannot round, while the attendees path is a largest-remainder split whose credits must sum to
// exactly the debit. Keeping them in one function is what makes it impossible to add a third
// destination that forgets to balance.
func (s FixedPrice) proceeds(
	ctx Ctx, cfg fixedPriceConfig, ev AwardEvent, price core.Centipoints,
) (credits []Allocation, split bool, err error) {
	if cfg.Proceeds == ProceedsGuildBank {
		bank, err := ctx.SystemAccount(SystemKeyGuildBank)
		if err != nil {
			return nil, false, fmt.Errorf("%s: resolve the guild bank: %w", fixedPriceID, err)
		}

		return []Allocation{{AccountID: bank, AmountCp: price}}, false, nil
	}

	shares := sortedShares(ev.Beneficiaries)
	if err := checkDistinctShares(shares); err != nil {
		return nil, false, err
	}

	for _, b := range shares {
		if err := checkShare(b); err != nil {
			return nil, false, err
		}
	}

	// The degenerate case, routed rather than dropped: nobody to split across means the solo policy
	// picks a system account and the whole price lands there. ledger.Allocate takes the same account
	// for its all-weights-zero case, which is why it is passed rather than handled here.
	solo, err := ctx.SystemAccount(cfg.SoloPolicy)
	if err != nil {
		return nil, false, fmt.Errorf("%s: resolve the solo-policy account %q: %w",
			fixedPriceID, cfg.SoloPolicy, err)
	}

	credits, err = ctx.Allocate(price, shares, solo)
	if err != nil {
		return nil, false, fmt.Errorf("%s: split %d centipoints across %d beneficiaries: %w",
			fixedPriceID, price, len(shares), err)
	}

	return credits, true, nil
}

// resolvePrice applies the three-step price resolution and refuses a price that awards nothing.
func resolvePrice(cfg fixedPriceConfig, ev AwardEvent) (core.Centipoints, error) {
	price := cfg.DefaultPriceCp

	switch {
	case ev.PriceCp != nil:
		price = *ev.PriceCp
	case ev.Item.FixedPriceCp != nil:
		price = *ev.Item.FixedPriceCp
	}

	if price <= 0 {
		return 0, fmt.Errorf(
			"%s: item %q resolves to a price of %d centipoints; price it in the catalogue, name a "+
				"price on the award, or set default_price_cp: %w",
			fixedPriceID, ev.Item.Name, price, ErrInvalidEvent)
	}

	return price, nil
}

// PlanAdjustment moves points between an account and a counterparty.
//
// It is two entries, never one. An officer who could add points without naming where they came from
// could inflate a guild's economy invisibly, and the counterparty — the guild bank unless the caller
// names another — is what makes every adjustment answerable with "out of what?".
func (s FixedPrice) PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if ev.Account.ID == "" {
		return BatchProposal{}, fmt.Errorf("%s: adjustment has no account: %w",
			fixedPriceID, ErrInvalidEvent)
	}

	if ev.AmountCp == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: adjustment moves 0 centipoints; drop the adjustment instead of writing a zero: %w",
			fixedPriceID, ErrInvalidEvent)
	}

	counterparty := ev.Counterparty
	if counterparty == "" {
		counterparty, err = ctx.SystemAccount(SystemKeyGuildBank)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", fixedPriceID, err)
		}
	}

	if counterparty == ev.Account.ID {
		return BatchProposal{}, fmt.Errorf(
			"%s: account %s is its own counterparty, so the adjustment moves nothing: %w",
			fixedPriceID, ev.Account.ID, ErrInvalidEvent)
	}

	// The adjusted account leads: it is the subject of the batch, and the statement view renders the
	// entries in the order they were planned.
	entries := []EntryProposal{
		{AccountID: ev.Account.ID, BalanceKind: BalanceKindDKP, AmountCp: ev.AmountCp},
		{AccountID: counterparty, BalanceKind: BalanceKindDKP, AmountCp: -ev.AmountCp},
	}

	return s.propose(ctx, "adjustment", ev.EffectiveAt, ev.Reason, entries, []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
	})
}

// PlanDecay debits each account a percentage of its balance and credits the total to the guild bank.
//
// DECAY IS POSTED, NOT COMPUTED. The batch is explicit and permanent, so a balance stays literally a
// SUM and "why did my points change?" is answerable by pointing at a row
// (.claude/rules/ledger-and-strategy.md). Nothing anywhere computes a decayed balance on read.
//
// THE POINTS GO TO THE BANK RATHER THAN NOWHERE. Decay in this strategy moves value back into
// circulation instead of destroying it, which keeps every batch zero-sum and keeps conservation a
// column comparison. A guild that wants decay to genuinely destroy points wants a strategy whose
// batches do not sum to zero, and that is a different strategy with a different invariant set.
//
// Balances are read AT run.AsOfSeq — positionally, never temporally. A batch committed while this
// run is planning must not change what it decayed, and a backdated effective_at must not change what
// a past balance was.
func (s FixedPrice) PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return BatchProposal{}, err
	}

	if cfg.DecayBp == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: decay_bp is 0, so this pool does not decay; a run against it would post an empty "+
				"batch: %w", fixedPriceID, ErrInvalidConfig)
	}

	if run.PeriodKey == "" {
		return BatchProposal{}, fmt.Errorf(
			"%s: decay run has no period key, so nothing makes a re-run idempotent: %w",
			fixedPriceID, ErrInvalidEvent)
	}

	accounts := run.Accounts
	if len(accounts) == 0 {
		accounts, err = ctx.Roster()
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read the roster to decay: %w", fixedPriceID, err)
		}
	}

	bank, err := ctx.SystemAccount(SystemKeyGuildBank)
	if err != nil {
		return BatchProposal{}, fmt.Errorf("%s: resolve the guild bank: %w", fixedPriceID, err)
	}

	debits := make([]EntryProposal, 0, len(accounts)+1)

	targets := sortedAccounts(accounts)
	if err := checkDistinctAccounts(targets); err != nil {
		return BatchProposal{}, err
	}

	var total core.Centipoints

	for _, a := range targets {
		// System accounts are not decayed. They are structurally negative by design — the bank funds
		// every tick — and decaying a negative balance by a positive rate would GROW the debt.
		if a.IsSystem() {
			continue
		}

		balance, err := ctx.Balance(a.ID, BalanceKindDKP, run.AsOfSeq)
		if err != nil {
			return BatchProposal{}, fmt.Errorf("%s: read balance for account %s at seq %d: %w",
				fixedPriceID, a.ID, run.AsOfSeq, err)
		}

		if balance <= 0 {
			continue
		}

		amount := decayAmount(balance, cfg.DecayBp)
		if amount == 0 {
			// A balance small enough that the rate rounds to nothing. Skipped rather than written as
			// a zero entry, and NOT rounded up: rounding a decay up takes a centipoint the rate did
			// not ask for, from the members with the least.
			continue
		}

		sum, ok := addCentipoints(total, amount)
		if !ok {
			// Checked rather than accumulated, even though propose's own balance assertion would
			// catch the consequence. A wrapped total lands on the BANK's entry, so the failure would
			// surface as "the batch sums to some enormous number" with no indication that a roster
			// of large balances was the cause — and the roster is what the officer would have to
			// look at.
			return BatchProposal{}, fmt.Errorf(
				"%s: the decay for period %s sums past int64 at account %s: %w",
				fixedPriceID, run.PeriodKey, a.ID, ErrInvalidEvent)
		}

		total = sum

		debits = append(debits, EntryProposal{
			AccountID:   a.ID,
			BalanceKind: BalanceKindDKP,
			AmountCp:    -amount,
		})
	}

	if len(debits) == 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: no account in period %s has a balance that decays by %d bp, so %w",
			fixedPriceID, run.PeriodKey, cfg.DecayBp, ErrNothingToPlan)
	}

	entries := append(debits, EntryProposal{
		AccountID:   bank,
		BalanceKind: BalanceKindDKP,
		AmountCp:    total,
	})

	return s.propose(ctx, "decay", run.EffectiveAt, "decay "+run.PeriodKey, entries, []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &cfg.FloorCp},
	})
}

// decayAmount is balance * bp / 10000, floored, computed exactly in integers.
//
// The 128-bit product is the same technique ledger.Allocate uses and for the same reason: `balance *
// bp` overflows int64 for a large balance, a float would be a lint failure and would lose precision
// exactly where the invariant lives, and math/big would allocate per account on a run that touches
// the whole roster. bits.Mul64/Div64 are exact and allocation-free.
//
// Div64 panics when the quotient would not fit in 64 bits. It cannot here: bp <= 10000 = the divisor,
// so the quotient is at most `balance`.
//
// FLOORED, never rounded. Rounding a decay to nearest takes a centipoint the configured rate did not
// ask for, and it takes it from every member every period.
func decayAmount(balance core.Centipoints, bp int64) core.Centipoints {
	hi, lo := bits.Mul64(uint64(balance), uint64(bp))
	q, _ := bits.Div64(hi, lo, basisPointsWhole)

	return core.Centipoints(q)
}

// PlanReversal negates every entry of the batch being reversed.
//
// ENTRY-WISE NEGATION IS CORRECT HERE, and it is not correct everywhere. This strategy's only balance
// kind is `dkp`, a plain quantity: subtracting what was added restores the balance exactly, whatever
// happened in between. A strategy whose kind is positional (suicide_kings' sk_position) or paired
// (epgp's ep/gp) must override this and say so, because negating a position delta does not restore a
// list — everyone below the winner shifted up in the meantime.
//
// The reversal's effective time is NOT the original's. A correction is a new economic event at the
// time it is decided; backdating it would silently rewrite what every intermediate balance meant.
//
// A REVERSAL DOES NOT DECLARE NonNegative, AND THAT IS THE POINT. Found in review of this PR, where
// it was declared and would have made real mistakes uncorrectable:
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
// that matters.
//
// The debt is the correct outcome and it is meant to be seen. Alice is at -500 because she spent
// points she was never owed; the balance says so, the reversal batch says why, and she works it off.
// What the floor legitimately guards is a SPEND — PlanAward declares it, and an overdraft there is
// refused before anything is written.
//
// SumZero still holds and is still declared: a reversal is the exact negation of a committed batch,
// so it can no more mint a point than the original could.
//
// IT ALSO DOES NOT READ THE POOL'S CURRENT CONFIG, and that is the same argument one step further
// out. Found in review of this PR: this used to call s.config(ctx) to validate, which made reversing
// a batch depend on a document that has nothing to do with it. A guild that switched strategies —
// or that added a knob this version does not know — has a pool config fixed_price cannot parse, and
// every fixed_price batch in the pool's history would become unreversible the moment the config
// changed. History is immutable and the repair primitive must not be contingent on the present.
//
// Nothing here needs the config anyway: the floor is gone for the reason above, and the batch
// carries its own ConfigSnapshotJSON, which LedgerBatch.Reversal copies forward so the reversal
// records the rules in force for the thing being undone.
func (s FixedPrice) PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error) {
	if b.StrategyID != "" && b.StrategyID != fixedPriceID {
		return BatchProposal{}, fmt.Errorf(
			"%s cannot reverse batch %s, which was planned by %s; a reversal must be planned by the "+
				"strategy that planned the original: %w",
			fixedPriceID, b.ID, b.StrategyID, ErrInvalidEvent)
	}

	reversal, err := b.Reversal()
	if err != nil {
		return BatchProposal{}, err
	}

	// The reversal carries the ORIGINAL's config snapshot (LedgerBatch.Reversal copies it), so the
	// batch still records the rules that were in force for the thing being undone rather than the
	// rules in force today. Its invariant set is replaced rather than inherited: the original's set
	// constrained a spend, and this is not one. See the doc comment above for why NonNegative in
	// particular must not be here.
	//
	// BatchProposal.Negated now drops NonNegative from the inherited set by default, so this
	// assignment is no longer what keeps the floor off a fixed_price reversal — the default is. It
	// stays because this strategy declares what constrains it rather than inheriting whatever an
	// earlier version of itself declared, and because a strategy's Invariants are read in review as
	// the statement of its rules.
	reversal.Invariants = []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
	}
	reversal.EffectiveAt = s.effectiveAt(ctx, 0)

	return reversal, nil
}

// Spendable is the account's balance at the pool head.
//
// NO COMPUTED DECAY AND NO WEIGHTING. Decay is posted as explicit batches, so it is already in the
// sum; a strategy that also applied a rate here would apply it twice, and the second application
// would be invisible in every statement. `.claude/rules/ledger-and-strategy.md` permits computed
// weighting in Priority and forbids it here, and this is the method that would be tempting.
//
// Active bid holds are subtracted in Phase 3, when holds exist. Until then a spendable balance is a
// balance, and saying so is better than a placeholder that reads as though holds were handled.
func (FixedPrice) Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error) {
	balance, err := ctx.Balance(acct.ID, BalanceKindDKP, ctx.HeadSeq())
	if err != nil {
		return 0, fmt.Errorf("%s: spendable balance for account %s: %w", fixedPriceID, acct.ID, err)
	}

	return balance, nil
}

// Priority ranks candidates for an item by spendable balance, highest first.
//
// A fixed-price guild has no bidding, so when two raiders want the same drop at the same price
// something has to decide. Balance is the fairest available answer — it is the accumulated cost of
// turning up — and the tiebreak at equal balance is the account id, ascending, which is deterministic
// and therefore replayable. A random tiebreak here would make two replays of the same loot decision
// differ, which is the defect the allocator's account_id tiebreak exists to prevent.
func (s FixedPrice) Priority(ctx Ctx, acct AccountRef) (Priority, error) {
	spendable, err := s.Spendable(ctx, acct)
	if err != nil {
		return Priority{}, err
	}

	return Priority{
		Rank:     int64(spendable),
		Tiebreak: acct.ID.String(),
		Reason:   "spendable balance",
	}, nil
}

// PriceHint is unsupported, and the refusal is a statement rather than a gap.
//
// A price hint answers "what should I bid?". This strategy has no bidding: an item's price is not an
// estimate to be hinted at, it is a number the officer or the catalogue already fixed, and PlanAward
// resolves it. Returning a number here would give a bidding UI a value to render a bid box around for
// a pool that has no bid box, which is worse than a 501 saying the concept does not apply.
func (FixedPrice) PriceHint(Ctx, ItemRef) (*core.Centipoints, error) {
	return nil, Unsupported(fixedPriceID, "hint at a price: items are priced, not bid on")
}

// ValidateBid is unsupported: there are no bids to validate.
func (FixedPrice) ValidateBid(Ctx, AccountRef, Bid) error {
	return Unsupported(fixedPriceID, "validate a bid: it has no bidding")
}

// SettleAuction is unsupported: there are no auctions to settle.
func (FixedPrice) SettleAuction(Ctx, Session, []Bid) (Resolution, error) {
	return Resolution{}, Unsupported(fixedPriceID, "settle an auction: it has no auctions")
}

// Invariants is the catalogue of every rule this strategy's planners attach to a proposal.
//
// The floor here is ZERO — the shipped default — while each proposal carries the POOL's configured
// floor, because the catalogue is a static property of the strategy and the floor is a per-pool
// setting. TestFixedPrice_EveryPlannerInvariant_IsDeclared compares the two by kind and balance kind
// for exactly that reason.
func (FixedPrice) Invariants() []Invariant {
	floor := core.Centipoints(0)

	return []Invariant{
		{Kind: InvariantSumZero, BalanceKind: BalanceKindDKP},
		{Kind: InvariantLargestRemainderSumsToDebit, BalanceKind: BalanceKindDKP},
		{Kind: InvariantNonNegative, BalanceKind: BalanceKindDKP, FloorCp: &floor},
	}
}

// propose assembles the finished BatchProposal and performs the planner-side balance assertion.
//
// EVERY PLANNER RETURNS THROUGH HERE, which is what makes the three things below true of all of them
// rather than of the ones somebody remembered:
//
//   - the config snapshot travels with the batch, verbatim, so changing a pool's config later cannot
//     change what a past batch meant;
//   - the effective time is game truth, defaulting to the injected clock rather than to a zero
//     timestamp that would land the batch in 1970;
//   - the entries sum to zero, checked HERE. LargestRemainderSumsToDebit and SumZero would both
//     reject a broken batch at commit time, but a failure here names the planner and the event while
//     a failure there names a row.
func (s FixedPrice) propose(
	ctx Ctx, kind string, effectiveAt core.Micros, reason string,
	entries []EntryProposal, invariants []Invariant,
) (BatchProposal, error) {
	p := BatchProposal{
		Kind:               kind,
		StrategyID:         fixedPriceID,
		StrategyVersion:    fixedPriceVersion,
		ConfigSnapshotJSON: ctx.ConfigJSON(),
		// No seed: this strategy consumes no randomness, and carrying a seed would assert that
		// replaying the batch from it reproduces the plan — which is true only because the seed is
		// irrelevant. TestFixedPrice_Planners_ConsumeNoRandomness asserts the Rng is never called.
		RngSeed:     nil,
		Reason:      reason,
		EffectiveAt: s.effectiveAt(ctx, effectiveAt),
		Entries:     entries,
		Invariants:  invariants,
	}

	net, ok := p.NetAmountCp()
	if !ok {
		return BatchProposal{}, fmt.Errorf("%s: the %s batch's %d entries sum past int64: %w",
			fixedPriceID, kind, len(entries), ErrInvalidEvent)
	}

	if net != 0 {
		return BatchProposal{}, fmt.Errorf(
			"%s: the %s batch's %d entries sum to %d rather than 0; every batch this strategy writes "+
				"moves points between accounts and mints none: %w",
			fixedPriceID, kind, len(entries), net, ErrInvalidEvent)
	}

	return p, nil
}

// effectiveAt returns the caller's game-truth time, or the injected clock's when they supplied none.
//
// The clock is the ONLY source of "now" a strategy has (`time.Now` is banned outside internal/clock,
// canonical §2) and this is where it is consumed. A zero EffectiveAt is a caller that did not
// specify, not a caller that meant 1970 — and a batch stamped 1970 sorts before every real one in
// the statement view and lands in the wrong effective_day bucket forever.
func (FixedPrice) effectiveAt(ctx Ctx, supplied core.Micros) core.Micros {
	if supplied != 0 {
		return supplied
	}

	return core.FromTime(ctx.Clock().Now())
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
func checkDistinctShares(sorted []Share) error {
	for i := 1; i < len(sorted); i++ {
		if sorted[i].AccountID == sorted[i-1].AccountID {
			return fmt.Errorf(
				"%s: account %s appears twice in the same event; a repeated account is a list that "+
					"was built twice, not a bigger share — say so with the weight: %w",
				fixedPriceID, sorted[i].AccountID, ErrInvalidEvent)
		}
	}

	return nil
}

// checkDistinctAccounts is checkDistinctShares for a plain account list. A decay run that names an
// account twice would read the same as-of balance twice and post two full debits — charging the
// period's decay twice while SumZero and NonNegative both still pass, because the arithmetic is
// self-consistent and simply wrong.
func checkDistinctAccounts(sorted []AccountRef) error {
	for i := 1; i < len(sorted); i++ {
		if sorted[i].ID == sorted[i-1].ID {
			return fmt.Errorf(
				"%s: account %s appears twice in the decay run; each balance decays once per period, "+
					"and a repeat would post the period's debit twice: %w",
				fixedPriceID, sorted[i].ID, ErrInvalidEvent)
		}
	}

	return nil
}

// checkShare rejects the two share shapes no planner has a defensible answer for.
func checkShare(s Share) error {
	if s.AccountID == "" {
		return fmt.Errorf("%s: a share names no account: %w", fixedPriceID, ErrInvalidEvent)
	}

	if s.Weight < 0 {
		return fmt.Errorf("%s: account %s has weight %d; a negative weight inverts that account's "+
			"quota while still counting toward the total: %w",
			fixedPriceID, s.AccountID, s.Weight, ErrInvalidEvent)
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

// mulCentipoints multiplies a centipoint amount by a non-negative integer weight, reporting overflow
// rather than wrapping.
//
// The check is a division rather than a 128-bit product because the answer must fit in an int64 to be
// usable at all: if `amount * weight` does not, there is no entry to write and the planner must say
// so. bits.Mul64 would compute a correct 128-bit value that then cannot be stored.
func mulCentipoints(amount core.Centipoints, weight int64) (product core.Centipoints, ok bool) {
	if weight == 0 || amount == 0 {
		return 0, true
	}

	product = amount * core.Centipoints(weight)
	if product/core.Centipoints(weight) != amount {
		return 0, false
	}

	return product, true
}
