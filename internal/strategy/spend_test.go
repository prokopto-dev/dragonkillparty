package strategy_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The assertions the spend family shares. Phase 1, #195.
//
// Four strategies land together — `auction_open`, `auction_sealed`, `relative_bid` and `roll` — and
// each owes the same contracts: it refuses the planners outside its slot, it adjusts and reverses
// like every other rule here, it validates a bid against the bidder rather than against whoever was
// passed alongside it, and its façade failures reach the caller instead of being swallowed. Written
// once per strategy, those are four chances for one of them to be written wrong; written here they
// are a table, and adding the fifth spend rule adds a row.
//
// The per-strategy files keep the ARITHMETIC — the price a sealed auction settles at, the share a
// relative bid ranks by, the roll a round produces — which is the part that is genuinely different
// and must not be abstracted into a shared table nobody reads. earn_test.go draws the same line for
// the earn family and its helpers (requireGoldens, requireInvariantsAgree, requireSchemaAgreesWithParser)
// are reused here rather than copied.

// spendCase is one spend strategy and enough context to exercise it generically: a config with every
// knob set, the floor that config declares, and a bid the strategy would accept.
type spendCase struct {
	id string
	s  strategy.PointStrategy

	// config is a fully-specified pool config — every knob at a non-default value, so a knob that
	// stopped being read shows up as a changed assertion rather than as nothing.
	config string

	// floorCp is the floor `config` declares, which every proposal that can overdraw must carry.
	floorCp core.Centipoints

	// bid builds a bid this strategy accepts from the given account, against the balance
	// spendCtx opens accounts at. The four differ — an open auction refuses a sealed bid, a sealed
	// one refuses an unsealed bid, and a roll refuses any amount at all — which is exactly why the
	// generic assertions need this rather than a literal.
	bid func(id core.ULID) strategy.Bid
}

// spendBalance is the balance spendCtx opens every account at. It is chosen so that `relative_bid`'s
// bids land inside its configured 500..7500 bp band: 1000 centipoints of 10000 is 1000 bp.
const spendBalance = core.Centipoints(10_000)

// errFacadeDown is the failure the façade is made to return: a database that will not answer. It is a
// package-level sentinel rather than one per test so that every "does this planner swallow an error?"
// assertion is comparing against the same thing with errors.Is.
var errFacadeDown = errors.New("the façade is down")

// spendCases is the family, in catalogue order.
func spendCases() []spendCase {
	return []spendCase{
		{
			id:      "auction_open",
			s:       strategy.AuctionOpen{},
			config:  auctionOpenGoldenConfig,
			floorCp: -500,
			bid: func(id core.ULID) strategy.Bid {
				// 1000 = the 500 minimum plus two 250 increments, so it is on the lattice.
				return strategy.Bid{AccountID: id, AmountCp: 1_000, PlacedAt: fixedNow}
			},
		},
		{
			id:      "auction_sealed",
			s:       strategy.AuctionSealed{},
			config:  auctionSealedGoldenConfig,
			floorCp: -500,
			bid: func(id core.ULID) strategy.Bid {
				return strategy.Bid{AccountID: id, AmountCp: 1_000, PlacedAt: fixedNow, Sealed: true}
			},
		},
		{
			id:      "relative_bid",
			s:       strategy.RelativeBid{},
			config:  relativeBidGoldenConfig,
			floorCp: -500,
			bid: func(id core.ULID) strategy.Bid {
				return strategy.Bid{AccountID: id, AmountCp: 1_000, PlacedAt: fixedNow}
			},
		},
		{
			id:      "roll",
			s:       strategy.Roll{},
			config:  rollGoldenConfig,
			floorCp: -500,
			bid: func(id core.ULID) strategy.Bid {
				return strategy.Bid{AccountID: id}
			},
		},
	}
}

// spendCtx is the façade the family assertions plan against: three raiders with a balance each can
// bid from.
func spendCtx(tb testing.TB, config string) *fakeCtx {
	tb.Helper()

	return newCtx(tb, 3, spendBalance, config)
}

// spendAwardEvent is the award every spend rule plans identically: a named buyer, a named price, and
// beneficiaries for the strategies configured to redistribute.
//
// THE PRICE IS ALWAYS EXPLICIT, which is the family's defining difference from `fixed_price`: an
// auction settles at the bid rather than at a published number, so PriceCp is where the settled price
// arrives and there is no catalogue to fall back to. `roll` takes it as the officer's override of
// win_cost_cp, which is the same shape.
func spendAwardEvent(price core.Centipoints) strategy.AwardEvent {
	return strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		PriceCp:       &price,
		Beneficiaries: shares(3),
		EffectiveAt:   fixedNow,
		Reason:        "Nagafen, settled at 10.00",
	}
}

// TestSpendStrategies_Identity_IsStableAndDeclared pins the four facts a catalogue entry is made of.
//
// The ID is written onto every batch and is PUBLIC API — renaming one orphans every batch it planned
// — so it is asserted as a literal rather than derived from the type.
func TestSpendStrategies_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.id, tc.s.ID())
			require.Equal(t, "0.1.0", tc.s.Version())
			require.Equal(t, strategy.RuleSpend, tc.s.RuleKind(),
				"every rule in this family answers 'how are points spent?' and is refused in another "+
					"slot at configuration time (ADR-0026)")
			require.Equal(t, []string{strategy.BalanceKindDKP}, tc.s.BalanceKinds())
			require.NotEmpty(t, tc.s.ConfigSchema())
		})
	}
}

// TestSpendStrategies_EarnAndCadencePlanners_AreUnsupported is the honest gap ADR-0026 describes.
//
// A spend rule is asked PlanAward and the five loot questions; a pool earns through its earn rule and
// expires points through its over-time rule. ErrUnsupported here is a 501 naming the strategy, and
// the alternative — inventing a tick award or a decay rate — would be a second copy of `tick`'s or
// `decay_percent`'s arithmetic that could then disagree with it.
func TestSpendStrategies_EarnAndCadencePlanners_AreUnsupported(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)

			_, err := tc.s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(2)})
			require.ErrorIs(t, err, strategy.ErrUnsupported)
			require.ErrorContains(t, err, tc.id, "the refusal must name the strategy that refused")

			_, err = tc.s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06", AsOfSeq: 7})
			require.ErrorIs(t, err, strategy.ErrUnsupported)
			require.ErrorContains(t, err, tc.id)
		})
	}
}

// TestSpendStrategies_PlanAward_DebitsTheBuyerAndBalances is the shape every spend batch has: the
// buyer's debit leads, the credits follow, and the entries sum to exactly zero.
func TestSpendStrategies_PlanAward_DebitsTheBuyerAndBalances(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			p, err := tc.s.PlanAward(spendCtx(t, tc.config), spendAwardEvent(1_000))
			require.NoError(t, err)

			require.Equal(t, "award", p.Kind)
			require.Equal(t, tc.id, p.StrategyID)
			require.Equal(t, tc.config, p.ConfigSnapshotJSON,
				"the config travels with the batch, verbatim, so changing a pool's settings later "+
					"cannot change what a past batch meant")
			require.Equal(t, acct(0), p.Entries[0].AccountID)
			require.Equal(t, core.Centipoints(-1_000), p.Entries[0].AmountCp)
			require.Zero(t, sumEntries(p), "an award moves points and mints none")
			require.Equal(t, tc.floorCp, requireNonNegativeFloor(t, p),
				"the POOL's floor reaches the proposal, not the strategy catalogue's default")
		})
	}
}

// TestSpendStrategies_PlanAward_APriceOfNothing_IsRefused: an award that charges the buyer nothing —
// or pays them — is not an award, and writing it would put an entry the ledger's
// CHECK (amount_cp <> 0) refuses in front of an officer as a commit failure rather than as a planner
// error naming the strategy.
//
// A NEGATIVE price rather than zero, because zero is a legitimate answer for exactly one rule here:
// `roll` reads it as "winning is free" and declines to write a batch at all (ErrNothingToPlan), which
// is its own file's case.
func TestSpendStrategies_PlanAward_APriceOfNothing_IsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			_, err := tc.s.PlanAward(spendCtx(t, tc.config), spendAwardEvent(-1))
			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, "charges the buyer nothing")
		})
	}
}

// TestSpendStrategies_ValidateBid_PropagatesABalanceFailure: a bid that could not be checked against a
// balance is not a bid that passed. Accepting one because the read failed is how a session takes a bid
// nobody can pay.
func TestSpendStrategies_ValidateBid_PropagatesABalanceFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)
			ctx.balanceErr = errFacadeDown

			err := tc.s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0), Kind: "person"},
				tc.bid(acct(0)))
			require.ErrorIs(t, err, errFacadeDown)
		})
	}
}

// TestSpendStrategies_PlanAdjustment_MovesPointsAgainstACounterparty asserts the one planner every
// strategy in the package shares: two entries, never one, so that every adjustment is answerable with
// "out of what?".
func TestSpendStrategies_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			p, err := tc.s.PlanAdjustment(spendCtx(t, tc.config), strategy.AdjustmentEvent{
				Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
				AmountCp:    -750,
				EffectiveAt: fixedNow,
				Reason:      "double-credited tick on 2024-05-30",
			})
			require.NoError(t, err)

			require.Len(t, p.Entries, 2)
			require.Equal(t, acct(1), p.Entries[0].AccountID)
			require.Equal(t, core.Centipoints(-750), p.Entries[0].AmountCp)
			require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID,
				"an unnamed counterparty is the guild bank, which exists to be the other side of this")
			require.Zero(t, sumEntries(p))
			require.Equal(t, tc.floorCp, requireNonNegativeFloor(t, p))
		})
	}
}

// TestSpendStrategies_PlanReversal_DeclaresNoFloor_SoACorrectionIsAlwaysPostable is the property that
// keeps a mistake fixable.
//
// A floor on a reversal does not prevent a debt, it prevents the CORRECTION: the ledger is
// append-only, a reversal is the only repair primitive there is, and refusing it leaves a mistake
// that is provably wrong and permanently unfixable. Every strategy here reverses through reversePlan,
// and this is what proves the shared path is the one they take.
func TestSpendStrategies_PlanReversal_DeclaresNoFloor_SoACorrectionIsAlwaysPostable(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)

			original, err := tc.s.PlanAward(ctx, spendAwardEvent(1_000))
			require.NoError(t, err)

			p, err := tc.s.PlanReversal(ctx, strategy.LedgerBatch{
				ID:                 acct(70),
				Kind:               original.Kind,
				StrategyID:         original.StrategyID,
				StrategyVersion:    original.StrategyVersion,
				ConfigSnapshotJSON: original.ConfigSnapshotJSON,
				Reason:             original.Reason,
				EffectiveAt:        fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
				Entries:            original.Entries,
			})
			require.NoError(t, err)

			require.Equal(t, strategy.KindReversal, p.Kind)
			require.Equal(t, acct(70), *p.ReversesBatchID)
			require.Equal(t, fixedNow, p.EffectiveAt,
				"a correction is a new economic event at the time it is decided, never backdated to "+
					"the original's")
			require.Len(t, p.Entries, len(original.Entries))

			for i, e := range p.Entries {
				require.Equal(t, -original.Entries[i].AmountCp, e.AmountCp)
			}

			for _, inv := range p.Invariants {
				require.NotEqual(t, strategy.InvariantNonNegative, inv.Kind,
					"a reversal that could be refused for overdrawing is a mistake nobody can fix")
			}

			require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(p),
				"a reversal still may not mint a centipoint")
		})
	}
}

// TestSpendStrategies_PlanReversal_ForeignBatch_IsRefused: a reversal is planned by the strategy that
// planned the original, and a batch naming another one is a routing bug rather than something to
// improvise over.
func TestSpendStrategies_PlanReversal_ForeignBatch_IsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			_, err := tc.s.PlanReversal(spendCtx(t, tc.config), strategy.LedgerBatch{
				ID:         acct(70),
				Kind:       "award",
				StrategyID: "some_other_strategy",
				Entries: []strategy.EntryProposal{
					{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
					{AccountID: acct(1), BalanceKind: strategy.BalanceKindDKP, AmountCp: -100},
				},
			})

			require.ErrorIs(t, err, strategy.ErrInvalidEvent)
			require.ErrorContains(t, err, "some_other_strategy")
		})
	}
}

// TestSpendStrategies_EveryPlannerInvariant_IsDeclared keeps each strategy's catalogue and its
// per-proposal sets in step, in both directions. See requireInvariantsAgree.
func TestSpendStrategies_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)

			award, err := tc.s.PlanAward(ctx, spendAwardEvent(1_000))
			require.NoError(t, err)

			adjustment, err := tc.s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
				Account: strategy.AccountRef{ID: acct(1), Kind: "person"}, AmountCp: -750,
			})
			require.NoError(t, err)

			reversal, err := tc.s.PlanReversal(ctx, strategy.LedgerBatch{
				ID: acct(70), Kind: award.Kind, StrategyID: award.StrategyID,
				StrategyVersion: award.StrategyVersion, Entries: award.Entries,
			})
			require.NoError(t, err)

			requireInvariantsAgree(t, tc.s, []strategy.BatchProposal{award, adjustment, reversal})
		})
	}
}

// TestSpendStrategies_Spendable_ReadsTheHeadSeq: a spendable balance is a SUM at the pool head, never
// a computed decay and never a weighting. The seq it is read at is the assertion, because a planner
// that read the wrong one passes every value check while being wrong the moment a batch commits.
func TestSpendStrategies_Spendable_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)

			got, err := tc.s.Spendable(ctx, strategy.AccountRef{ID: acct(0), Kind: "person"})
			require.NoError(t, err)
			require.Equal(t, spendBalance, got)
			require.Equal(t, []int64{ctx.headSeq}, ctx.readAtSeq)
		})
	}
}

// TestSpendStrategies_Priority_IsDeterministic asserts the one property every ranking in this
// repository owes, whatever it ranks by: the tiebreak is the account id, so two replays of the same
// loot screen agree.
func TestSpendStrategies_Priority_IsDeterministic(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)

			first, err := tc.s.Priority(ctx, strategy.AccountRef{ID: acct(1), Kind: "person"})
			require.NoError(t, err)

			second, err := tc.s.Priority(ctx, strategy.AccountRef{ID: acct(1), Kind: "person"})
			require.NoError(t, err)

			require.Equal(t, first, second)
			require.Equal(t, acct(1).String(), first.Tiebreak)
			require.NotEmpty(t, first.Reason, "a rank without its explanation starts the argument it "+
				"exists to end")
		})
	}
}

// TestSpendStrategies_ValidateBid_ChecksTheBidderRatherThanTheArgument is the defect checkBidIdentity
// exists for: ValidateBid is handed an account AND a bid, and nothing in the types makes them the
// same account. A caller that crossed them would have a bid checked against somebody else's balance.
func TestSpendStrategies_ValidateBid_ChecksTheBidderRatherThanTheArgument(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			ctx := spendCtx(t, tc.config)
			bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

			require.NoError(t, tc.s.ValidateBid(ctx, bidder, tc.bid(acct(0))),
				"the shipped example bid must be one this strategy accepts, or every rejection below "+
					"proves nothing")

			err := tc.s.ValidateBid(ctx, bidder, tc.bid(acct(1)))
			require.ErrorIs(t, err, strategy.ErrInvalidEvent,
				"a bid from one account validated against another is a crossed caller")

			err = tc.s.ValidateBid(ctx, strategy.AccountRef{Kind: "person"}, tc.bid(""))
			require.ErrorIs(t, err, strategy.ErrInvalidEvent, "a bid from nobody is not a bid")

			system := strategy.AccountRef{
				ID: ledger.AccountIDGuildBank, Kind: "system", SystemKey: strategy.SystemKeyGuildBank,
			}
			err = tc.s.ValidateBid(ctx, system, tc.bid(ledger.AccountIDGuildBank))
			require.ErrorIs(t, err, strategy.ErrInvalidEvent,
				"the four system accounts are counterparties, never bidders")
		})
	}
}

// TestSpendStrategies_SettleAuction_NoBids_IsRotRatherThanAnError: an item nobody bid on has no
// winner, the session goes to `rot`, and the guild's rot policy decides what happens to it. An error
// would make an unwanted drop look like a broken auction.
func TestSpendStrategies_SettleAuction_NoBids_IsRotRatherThanAnError(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			res, err := tc.s.SettleAuction(spendCtx(t, tc.config), strategy.Session{
				ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow,
			}, nil)

			require.NoError(t, err)
			require.Empty(t, res.Winners)
			require.NotEmpty(t, res.Reason, "the resolution has to say why nobody won")
		})
	}
}

// TestSpendStrategies_SettleAuction_AwardsOneWinner is the settlement's shape, over the bid every
// strategy accepts: exactly one winner, drawn from the bidders, at a non-negative price.
func TestSpendStrategies_SettleAuction_AwardsOneWinner(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			bids := []strategy.Bid{tc.bid(acct(0)), tc.bid(acct(1))}

			// The two bids are identical but for the account, so the auctions tie at the top and roll:
			// the seed must then be reported, because an unrecorded coin flip is the one thing a loot
			// dispute cannot be settled from. `roll` instead rolls per entrant and can legitimately
			// tie, which awards nobody — that case is its own file's.
			res, err := tc.s.SettleAuction(spendCtx(t, tc.config), strategy.Session{
				ID: acct(60), SeqAtOpen: 5, OpenedAt: fixedNow,
			}, bids)
			require.NoError(t, err)
			require.NotEmpty(t, res.Reason)

			if len(res.Winners) == 0 {
				require.Equal(t, "roll", tc.id,
					"only a roll-off may award nobody, and only because a re-roll is a new round")
				require.NotNil(t, res.RngSeed, "the round is replayable or it is not a round")

				return
			}

			require.Len(t, res.Winners, 1)
			require.Contains(t, []core.ULID{acct(0), acct(1)}, res.Winners[0].AccountID)
			require.GreaterOrEqual(t, res.Winners[0].AmountCp, core.Centipoints(0))
		})
	}
}

// TestSpendStrategies_Planners_PropagateFacadeFailures walks each planner with a façade that fails
// the way a database does.
//
// NEVER SWALLOW AN ERROR TO MAKE A PLAN SUCCEED (AGENTS.md). A planner that treated an unreadable
// balance as zero would post an award nobody could explain, and the failure would be invisible in
// every test whose fake always answers.
func TestSpendStrategies_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			t.Run("system account", func(t *testing.T) {
				t.Parallel()

				ctx := spendCtx(t, tc.config)
				ctx.systemErr = errFacadeDown

				_, err := tc.s.PlanAward(ctx, spendAwardEvent(1_000))
				require.ErrorIs(t, err, errFacadeDown)

				_, err = tc.s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(1), Kind: "person"}, AmountCp: 100,
				})
				require.ErrorIs(t, err, errFacadeDown)
			})

			t.Run("balance", func(t *testing.T) {
				t.Parallel()

				ctx := spendCtx(t, tc.config)
				ctx.balanceErr = errFacadeDown

				_, err := tc.s.Spendable(ctx, strategy.AccountRef{ID: acct(0), Kind: "person"})
				require.ErrorIs(t, err, errFacadeDown)
			})
		})
	}
}

// TestSpendStrategies_Config_RejectsWhatTheSchemaWouldHaveRejected walks every knob of every schema
// and asserts the parser and the schema agree in both directions. See requireSchemaAgreesWithParser:
// the cases are derived FROM the schema, so a knob added later is covered without anybody remembering
// to add a row.
func TestSpendStrategies_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	// A knob whose legal value cannot be written alone names its whole document here: `roll`'s default
	// floor is 1, so the single-knob `{"roll_max": 1}` the helper would otherwise build is a range of
	// one — which validateRollConfig refuses, correctly, and which would then look like schema drift.
	legal := map[string]map[string]string{
		"roll": {"roll_max": `{"roll_min": 0, "roll_max": 1}`},
	}

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			requireSchemaAgreesWithParser(t, tc.s.ConfigSchema(), legal[tc.id],
				func(t *testing.T, config string) error {
					t.Helper()

					// PlanAdjustment is the cheapest planner that parses the config on every strategy
					// here — Spendable deliberately does not read one, because a balance is a SUM and
					// no knob changes it — so a rejection below is the config's rather than the event's.
					_, err := tc.s.PlanAdjustment(spendCtx(t, config), strategy.AdjustmentEvent{
						Account: strategy.AccountRef{ID: acct(0), Kind: "person"}, AmountCp: 100,
					})

					return err
				})
		})
	}
}

// TestSpendStrategies_ConfigSchemas_DeclareNoNumberType restates canonical §1 where a schema could
// break it: `number` in a JSON Schema permits 12.5, and a decimal in the point path is a float.
func TestSpendStrategies_ConfigSchemas_DeclareNoNumberType(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			requireNoNumberType(t, tc.s.ConfigSchema())
		})
	}
}

// TestSpendStrategies_BadConfig_StopsEveryPlanner is the config half of "never default a bad value".
//
// A planner that ran on an unparseable config would run a DKP system nobody chose, and the officer
// would find out from a balance. Every entry point re-reads the config, so every entry point refuses.
func TestSpendStrategies_BadConfig_StopsEveryPlanner(t *testing.T) {
	t.Parallel()

	for _, tc := range spendCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			// An unknown knob: the typo that would otherwise leave the pool silently on the defaults.
			ctx := spendCtx(t, `{"not_a_knob": 1}`)
			bidder := strategy.AccountRef{ID: acct(0), Kind: "person"}

			_, err := tc.s.PlanAward(ctx, spendAwardEvent(1_000))
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)

			_, err = tc.s.PlanAdjustment(ctx, strategy.AdjustmentEvent{Account: bidder, AmountCp: 100})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)

			_, err = tc.s.PriceHint(ctx, strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"})
			requireConfigRefusedOrNoHint(t, tc.id, err)

			require.ErrorIs(t, tc.s.ValidateBid(ctx, bidder, tc.bid(acct(0))),
				strategy.ErrInvalidConfig)

			_, err = tc.s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 5}, nil)
			require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		})
	}
}

// requireConfigRefusedOrNoHint holds PriceHint to the one exception in the table above.
//
// `relative_bid` answers nil/nil for every item — what a drop costs there depends on who is asking,
// and PriceHint is handed an item and no account — so it never reads the config and cannot refuse it.
// Writing that as a bare skip would hide it; writing it as a named case is what makes the asymmetry
// reviewable.
func requireConfigRefusedOrNoHint(t *testing.T, id string, err error) {
	t.Helper()

	if id == "relative_bid" {
		require.NoError(t, err,
			"relative_bid hints at no item price at all, so there is no config for it to refuse")

		return
	}

	require.ErrorIs(t, err, strategy.ErrInvalidConfig)
}
