package strategy_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// zero_sum, tested at the strategy level. Phase 1, #196.
//
// The arithmetic under test is not this strategy's — every credit comes from ledger.Allocate, whose
// own properties are proved in internal/ledger — so what these tests prove is the PLANNER: that it
// splits the whole price, debits the whole price, removes the winner when the pool says to, routes
// each degenerate case to the account the config names, and adds no entry of its own that breaks the
// equality. A planner can hold a correct allocator and still lose a centipoint by rounding its own
// debit.
//
// The shared helpers (fakeCtx, acct, shares, requireGoldens, requireInvariantsAgree,
// requireSchemaAgreesWithParser, the property seed) live in fixed_price_test.go and earn_test.go.

// zeroSumGoldenDir is where the canonical proposals live. Under test/golden/ rather than beside this
// file because that tree is CODEOWNERS-protected and is gated against shrinking.
const zeroSumGoldenDir = "../../test/golden/strategy/zero_sum"

// --- The planners, one example each -------------------------------------------------------------

// TestZeroSum_PlanAward_SplitsThePriceAcrossTheOtherRaiders is the guide's worked example: a 300.00
// item, seven other raiders, and credits that sum to exactly the debit rather than to 300.02.
func TestZeroSum_PlanAward_SplitsThePriceAcrossTheOtherRaiders(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(30_000)

	// The buyer is acct(0) and is one of the eight present; excluded from the split, seven remain.
	p, err := strategy.ZeroSum{}.PlanAward(newCtx(t, 8, 0, `{}`), strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		PriceCp:       &price,
		Beneficiaries: shares(8),
		EffectiveAt:   fixedNow,
		Reason:        "Nagafen, roll 97",
	})
	require.NoError(t, err)

	require.Equal(t, "award", p.Kind)
	require.Equal(t, "zero_sum", p.StrategyID)
	require.Len(t, p.Entries, 8, "the buyer's debit plus one credit for each of the seven others")

	require.Equal(t, acct(0), p.Entries[0].AccountID, "the buyer's debit leads")
	require.Equal(t, -price, p.Entries[0].AmountCp,
		"the debit is the whole price: a planner that rounded its own debit would balance against "+
			"rounded credits and still be wrong")

	var credits core.Centipoints

	for _, e := range p.Entries[1:] {
		require.NotEqual(t, acct(0), e.AccountID, "the winner is excluded by default")
		require.Positive(t, e.AmountCp)

		credits += e.AmountCp
	}

	require.Equal(t, price, credits,
		"30000 over 7 is 4285.71…; the credits must still sum to exactly the price. Rounding each "+
			"one independently mints two centipoints on every item, forever")
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	// Largest remainder, not equal shares: 4285 × 7 is 29995, so five accounts get 4286 and two get
	// 4285, and which five is decided by the remainder then by the account id, ascending.
	counts := map[core.Centipoints]int{}
	for _, e := range p.Entries[1:] {
		counts[e.AmountCp]++
	}

	require.Equal(t, map[core.Centipoints]int{4286: 5, 4285: 2}, counts)
	require.Equal(t, core.Centipoints(4286), p.Entries[1].AmountCp,
		"the +1 goes to the lowest account ids at equal remainders — the deterministic tiebreak, "+
			"without which two replays of the same batch differ")

	// The provenance pointer reaches every entry, including the credits, so a member's statement can
	// say which item moved their points.
	for i, e := range p.Entries {
		require.NotNil(t, e.ItemID, "entry %d carries no item", i)
		require.Equal(t, acct(90), *e.ItemID)
	}

	require.Contains(t, invariantKinds(p), strategy.InvariantLargestRemainderSumsToDebit)
}

// TestZeroSum_PlanAward_WinnerIncluded_SharesInTheirOwnPayment is the other configuration: the buyer
// is an attendee like any other and nets price × (n−1)/n.
func TestZeroSum_PlanAward_WinnerIncluded_SharesInTheirOwnPayment(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(30_000)

	p, err := strategy.ZeroSum{}.PlanAward(
		newCtx(t, 4, 0, `{"winner_share": "included"}`), strategy.AwardEvent{
			Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
			PriceCp:       &price,
			Beneficiaries: shares(4),
			EffectiveAt:   fixedNow,
		})
	require.NoError(t, err)

	require.Len(t, p.Entries, 5, "the debit plus a credit for all four attendees, the buyer included")

	net := map[core.ULID]core.Centipoints{}
	for _, e := range p.Entries {
		net[e.AccountID] += e.AmountCp
	}

	require.Equal(t, core.Centipoints(-22_500), net[acct(0)],
		"the winner pays 300.00 and receives a quarter of it back, netting 225.00")
	require.Equal(t, core.Centipoints(7_500), net[acct(1)])
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestZeroSum_PlanAward_SoloKill_RoutesByTheSoloPolicy covers all three answers to "what if the winner
// is the only attendee?".
//
// The first two route to a SYSTEM ACCOUNT rather than dropping the points, which is what keeps
// conservation verifiable when there is nobody to divide across. The third writes no batch at all.
func TestZeroSum_PlanAward_SoloKill_RoutesByTheSoloPolicy(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(30_000)

	for _, tc := range []struct {
		name    string
		config  string
		wantAcc core.ULID
	}{
		{"the default is the guild bank", `{}`, ledger.AccountIDGuildBank},
		{"write_off", `{"solo_policy": "write_off"}`, ledger.AccountIDWriteOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The buyer is the only attendee, so excluding the winner empties the split.
			p, err := strategy.ZeroSum{}.PlanAward(newCtx(t, 1, 0, tc.config), strategy.AwardEvent{
				Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
				PriceCp:       &price,
				Beneficiaries: shares(1),
				EffectiveAt:   fixedNow,
			})
			require.NoError(t, err)

			require.Len(t, p.Entries, 2)
			require.Equal(t, -price, p.Entries[0].AmountCp)
			require.Equal(t, tc.wantAcc, p.Entries[1].AccountID)
			require.Equal(t, price, p.Entries[1].AmountCp)
			require.Equal(t, core.Centipoints(0), sumEntries(p))
		})
	}

	t.Run("free writes no batch at all", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.ZeroSum{}.PlanAward(
			newCtx(t, 1, 0, `{"solo_policy": "free"}`), strategy.AwardEvent{
				Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
				PriceCp:       &price,
				Beneficiaries: shares(1),
				EffectiveAt:   fixedNow,
			})
		require.ErrorIs(t, err, strategy.ErrNothingToPlan,
			"an item that costs nothing is no batch, not a batch that moves zero: ledger_entry "+
				"carries CHECK (amount_cp <> 0) and ledger_batch CHECK (entry_count > 0)")
		require.ErrorIs(t, err, strategy.ErrInvalidEvent,
			"ErrNothingToPlan wraps ErrInvalidEvent, so a caller that handles neither still sees one")
	})

	// The free-solo path returns before the shared award assembly runs, so it is the one branch where
	// a malformed award could be reported as a legitimate one. ErrNothingToPlan means "this event was
	// legal and produced no entries", and a caller acts on it by recording the loot with no batch — so
	// an award naming no buyer must never reach it. Found in review of #228, after the refactor onto
	// spendAward moved the buyer checks downstream of this branch.
	t.Run("a malformed buyer is refused rather than reported as a free award", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name  string
			buyer strategy.AccountRef
		}{
			{"no buyer at all", strategy.AccountRef{}},
			{
				"a system account as the buyer",
				strategy.AccountRef{ID: ledger.AccountIDGuildBank, Kind: "system"},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				_, err := strategy.ZeroSum{}.PlanAward(
					newCtx(t, 1, 0, `{"solo_policy": "free"}`), strategy.AwardEvent{
						Buyer:       tc.buyer,
						Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
						PriceCp:     &price,
						EffectiveAt: fixedNow,
					})
				require.ErrorIs(t, err, strategy.ErrInvalidEvent)
				require.NotErrorIs(t, err, strategy.ErrNothingToPlan,
					"a caller treats ErrNothingToPlan as 'legal event, no batch' and records the loot "+
						"anyway; a malformed award must be an error it can act on")
			})
		}
	})

	t.Run("free with other raiders present still splits", func(t *testing.T) {
		t.Parallel()

		p, err := strategy.ZeroSum{}.PlanAward(
			newCtx(t, 3, 0, `{"solo_policy": "free"}`), strategy.AwardEvent{
				Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
				PriceCp:       &price,
				Beneficiaries: shares(3),
				EffectiveAt:   fixedNow,
			})
		require.NoError(t, err, "the solo policy applies to a solo kill, not to every award")
		require.Len(t, p.Entries, 3)
		require.Equal(t, core.Centipoints(0), sumEntries(p))
	})
}

// TestZeroSum_PlanAward_AllWeightsZero_RoutesToResidue is the second degenerate case, and it routes
// differently from the first on purpose.
//
// There ARE beneficiaries, so this is not a solo kill; there is no basis on which to divide, because
// every quota would be 0/0. The price has left the buyer either way, so it lands on `residue` — the
// account that exists precisely so that conservation stays verifiable when the arithmetic cannot
// decide.
func TestZeroSum_PlanAward_AllWeightsZero_RoutesToResidue(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(25_00)

	p, err := strategy.ZeroSum{}.PlanAward(newCtx(t, 3, 0, `{}`), strategy.AwardEvent{
		Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:  strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		Beneficiaries: []strategy.Share{
			{AccountID: acct(1), Weight: 0},
			{AccountID: acct(2), Weight: 0},
		},
		PriceCp:     &price,
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 2)
	require.Equal(t, ledger.AccountIDResidue, p.Entries[1].AccountID,
		"an unallocatable amount lands on residue, never on a silent drop")
	require.Equal(t, price, p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestZeroSum_PlanAward_ASystemAccountBeneficiary_IsRefused is the same dilution defect from the
// spend side: the price is fixed, so a share for the guild bank is a share taken from every raider who
// was actually there, and the batch still sums to zero either way.
//
// The refusal comes from routeProceeds, so it covers every spend rule that splits — `fixed_price` with
// `proceeds: attendees` and the four bidding rules included, not only this one. A system account is
// still a legal DESTINATION for the whole price under the solo policy; what it may not be is one of
// the accounts the price is divided across.
func TestZeroSum_PlanAward_ASystemAccountBeneficiary_IsRefused(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(30_000)

	_, err := strategy.ZeroSum{}.PlanAward(newCtx(t, 3, 0, `{}`), strategy.AwardEvent{
		Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:    strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		PriceCp: &price,
		Beneficiaries: []strategy.Share{
			{AccountID: acct(1), Weight: 1},
			{AccountID: ledger.AccountIDGuildBank, Weight: 1},
		},
		EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "guild_bank")

	// The solo policy still routes the WHOLE price to a system account, which is a different thing and
	// must keep working: receiving the proceeds is not receiving a share of them.
	solo, err := strategy.ZeroSum{}.PlanAward(
		newCtx(t, 1, 0, `{"solo_policy": "write_off"}`), strategy.AwardEvent{
			Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
			PriceCp:     &price,
			EffectiveAt: fixedNow,
		})
	require.NoError(t, err)
	require.Equal(t, ledger.AccountIDWriteOff, solo.Entries[1].AccountID)
	require.Equal(t, price, solo.Entries[1].AmountCp)
}

// TestZeroSum_PlanAward_PriceResolution_PrefersTheOfficerThenTheItemThenTheConfig asserts the one
// resolution order, shared with fixed_price.
func TestZeroSum_PlanAward_PriceResolution_PrefersTheOfficerThenTheItemThenTheConfig(t *testing.T) {
	t.Parallel()

	officer := core.Centipoints(4_000)
	catalogue := core.Centipoints(2_000)

	for _, tc := range []struct {
		name   string
		config string
		ev     strategy.AwardEvent
		want   core.Centipoints
	}{
		{
			name:   "the officer's price wins",
			config: `{"default_price_cp": 100}`,
			ev: strategy.AwardEvent{
				PriceCp: &officer,
				Item:    strategy.ItemRef{Name: "Cloak", FixedPriceCp: &catalogue},
			},
			want: officer,
		},
		{
			name:   "then the catalogue's",
			config: `{"default_price_cp": 100}`,
			ev:     strategy.AwardEvent{Item: strategy.ItemRef{Name: "Cloak", FixedPriceCp: &catalogue}},
			want:   catalogue,
		},
		{
			name:   "then the pool's default",
			config: `{"default_price_cp": 100}`,
			ev:     strategy.AwardEvent{Item: strategy.ItemRef{Name: "Cloak"}},
			want:   100,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := tc.ev
			ev.Buyer = strategy.AccountRef{ID: acct(0), Kind: "person"}
			ev.Beneficiaries = shares(3)
			ev.EffectiveAt = fixedNow

			p, err := strategy.ZeroSum{}.PlanAward(newCtx(t, 3, 0, tc.config), ev)
			require.NoError(t, err)
			require.Equal(t, -tc.want, p.Entries[0].AmountCp)
		})
	}
}

// TestZeroSum_PlanAdjustment_MovesPointsAgainstACounterparty: an adjustment is two entries, never one.
func TestZeroSum_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	p, err := strategy.ZeroSum{}.PlanAdjustment(
		newCtx(t, 2, 5_000, `{"floor_cp": -1000}`), strategy.AdjustmentEvent{
			Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
			AmountCp:    -750,
			EffectiveAt: fixedNow,
			Reason:      "double-credited tick",
		})
	require.NoError(t, err)

	require.Equal(t, "adjustment", p.Kind)
	require.Len(t, p.Entries, 2)
	require.Equal(t, acct(0), p.Entries[0].AccountID, "the adjusted account leads")
	require.Equal(t, core.Centipoints(-750), p.Entries[0].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID,
		"the counterparty defaults to the guild bank: an adjustment must be answerable with 'out of "+
			"what?'")
	require.Equal(t, core.Centipoints(750), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(-1000), requireNonNegativeFloor(t, p),
		"the POOL's floor reaches the proposal, not the strategy catalogue's default")
}

// TestZeroSum_PlanReversal_NegatesTheDebitAndEveryCredit is the guarantee
// docs/guides/loot-and-reconciliation.md makes to an officer: reversing a zero-sum award reverses the
// whole split, together, in one batch.
func TestZeroSum_PlanReversal_NegatesTheDebitAndEveryCredit(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 4, 10_000, `{}`)
	s := strategy.ZeroSum{}
	price := core.Centipoints(1_000)

	original, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		PriceCp:       &price,
		Beneficiaries: shares(4),
		EffectiveAt:   fixedNow.Add(-48 * 60 * 60 * 1_000_000_000),
		Reason:        "Nagafen, roll 97",
	})
	require.NoError(t, err)

	reversal, err := s.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            original.Kind,
		StrategyID:      original.StrategyID,
		StrategyVersion: original.StrategyVersion,
		Reason:          original.Reason,
		EffectiveAt:     original.EffectiveAt,
		Entries:         original.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.NotNil(t, reversal.ReversesBatchID)
	require.Equal(t, acct(70), *reversal.ReversesBatchID)
	require.Len(t, reversal.Entries, len(original.Entries),
		"every credit the split produced is reversed with the debit, in one batch")

	net := map[core.ULID]core.Centipoints{}
	for _, e := range append(append([]strategy.EntryProposal{}, original.Entries...),
		reversal.Entries...) {
		net[e.AccountID] += e.AmountCp
	}

	for id, v := range net {
		require.Equal(t, core.Centipoints(0), v, "account %s is %d centipoints out", id, v)
	}

	require.Equal(t, fixedNow, reversal.EffectiveAt,
		"a correction is a new economic event at the time it is decided; backdating it would rewrite "+
			"what every intermediate balance meant")
	require.NotContains(t, invariantKinds(reversal), strategy.InvariantNonNegative,
		"a floor on a reversal does not prevent a debt — it prevents the correction, and an "+
			"append-only ledger has no other repair primitive")
}

// TestZeroSum_PlanReversal_ForeignBatch_IsRefused: a reversal must be planned by the strategy that
// planned the original.
func TestZeroSum_PlanReversal_ForeignBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.ZeroSum{}.PlanReversal(newCtx(t, 1, 0, `{}`), strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "suicide_kings",
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: 100},
		},
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "suicide_kings")
}

// TestZeroSum_PlanReversal_IgnoresTodaysPoolConfig is the property that keeps history reversible after
// a guild changes its rules: a config this version cannot even parse must not stop a correction.
func TestZeroSum_PlanReversal_IgnoresTodaysPoolConfig(t *testing.T) {
	t.Parallel()

	original := []strategy.EntryProposal{
		{AccountID: acct(0), BalanceKind: strategy.BalanceKindDKP, AmountCp: -300},
		{AccountID: acct(1), BalanceKind: strategy.BalanceKindDKP, AmountCp: 300},
	}

	for _, config := range []string{`{"a_knob_from_the_future": 1}`, `null`, `{`, `{"winner_share": 3}`} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			p, err := strategy.ZeroSum{}.PlanReversal(
				newCtx(t, 2, 0, config), strategy.LedgerBatch{
					ID:                 acct(70),
					Kind:               "award",
					StrategyID:         "zero_sum",
					StrategyVersion:    "0.1.0",
					ConfigSnapshotJSON: `{"winner_share":"excluded"}`,
					Entries:            original,
				})
			require.NoError(t, err,
				"reversing must not depend on the pool's CURRENT config: a guild that changed a rule "+
					"would otherwise find every batch in its history permanently unreversible")
			require.Equal(t, `{"winner_share":"excluded"}`, p.ConfigSnapshotJSON,
				"the reversal carries the ORIGINAL's snapshot forward, not today's document")
		})
	}
}

// TestZeroSum_Spendable_ReadsTheHeadSeq: spendable is a sum over committed entries at the pool head,
// never a computed weighting.
func TestZeroSum_Spendable_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 4_200, `{}`)

	got, err := strategy.ZeroSum{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(4_200), got)
	require.Equal(t, []int64{7}, ctx.readAtSeq, "read at the head seq, positionally")

	rank, err := strategy.ZeroSum{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, int64(4_200), rank.Rank)
	require.Equal(t, acct(0).String(), rank.Tiebreak,
		"the tiebreak is deterministic: a random one would make two replays of the same loot "+
			"decision differ")
	require.NotEmpty(t, rank.Reason)
}

// TestZeroSum_UnsupportedOperations_RefuseAndNameTheStrategy covers every question this strategy
// declines.
//
// PlanAttendance and PlanDecay are the two that matter: zero_sum answers "how are points spent?" and
// nothing else, and a refusal that names what to pair it with is what turns into a 501 an operator can
// act on.
func TestZeroSum_UnsupportedOperations_RefuseAndNameTheStrategy(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, `{}`)
	s := strategy.ZeroSum{}

	attendance, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(1)})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "tick", "the refusal points at what to pair it with")
	require.Empty(t, attendance.Entries)

	decay, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.ErrorContains(t, err, "decay_percent")
	require.Empty(t, decay.Entries)

	hint, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Nil(t, hint)

	require.ErrorIs(t, s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)},
		strategy.Bid{AccountID: acct(0), AmountCp: 100}), strategy.ErrUnsupported)

	resolution, err := s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 7}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Empty(t, resolution.Winners)
	require.ErrorContains(t, err, "zero_sum",
		"every refusal names the strategy that made it: the error crosses a package boundary on its "+
			"way to a 501, and a refusal with no subject is the support ticket nobody can act on")
}

// TestZeroSum_Identity_IsStableAndDeclared covers the values written onto every batch, and the slot
// the whole strategy is placed in.
func TestZeroSum_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.ZeroSum{}

	require.Equal(t, "zero_sum", s.ID(),
		"the id is written onto every batch and is public API: renaming it orphans history")
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, strategy.RuleSpend, s.RuleKind(),
		"redistribution happens in PlanAward, at the moment an item is won, not on the decay_run "+
			"cadence — the slot follows the planner (ADR-0026)")
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants(), "a strategy that declares no invariants is a red flag")

	// The schema is a copy: a caller that could mutate it could change what every pool validates
	// against.
	first := s.ConfigSchema()
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0])
}

// TestZeroSum_ResolvesThroughTheCatalogueIntoTheSpendSlot is the registration, end to end: an id in a
// pool row becomes a planner, and only in the slot it answers.
func TestZeroSum_ResolvesThroughTheCatalogueIntoTheSpendSlot(t *testing.T) {
	t.Parallel()

	rules, err := strategy.PoolConfig{
		EarnStrategyID:  "tick",
		SpendStrategyID: "zero_sum",
		SpendConfigJSON: `{"winner_share": "included"}`,
	}.Resolve()
	require.NoError(t, err)
	require.Equal(t, "zero_sum", rules.Spend.Strategy.ID())

	_, err = strategy.PoolConfig{OverTimeStrategyID: "zero_sum"}.Resolve()
	require.ErrorIs(t, err, strategy.ErrWrongRuleKind,
		"the guide's catalogue table called it an over-time rule; the shipped rule kind is what "+
			"PoolConfig.Resolve enforces, and it refuses on the settings form rather than at 19:05")
}

// --- Rejections ---------------------------------------------------------------------------------

// TestZeroSum_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
func TestZeroSum_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.ZeroSum{}
	price := core.Centipoints(1_000)

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "an award with no buyer",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Item: strategy.ItemRef{Name: "Cloak"}, PriceCp: &price,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a system account as the buyer",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:   strategy.AccountRef{ID: ledger.AccountIDGuildBank, Kind: "system"},
					Item:    strategy.ItemRef{Name: "Cloak"},
					PriceCp: &price,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an item with no price anywhere",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:  strategy.ItemRef{Name: "Cloak"},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a negative price",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				negative := core.Centipoints(-1)
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:    strategy.ItemRef{Name: "Cloak"},
					PriceCp: &negative,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a beneficiary named twice",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:    strategy.ItemRef{Name: "Cloak"},
					PriceCp: &price,
					Beneficiaries: []strategy.Share{
						{AccountID: acct(1), Weight: 1},
						{AccountID: acct(1), Weight: 1},
					},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "the winner named twice, on a pool that excludes them",
			config: `{"winner_share": "excluded"}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:    strategy.ItemRef{Name: "Cloak"},
					PriceCp: &price,
					Beneficiaries: []strategy.Share{
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(1), Weight: 1},
					},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a beneficiary with a negative weight",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{Name: "Cloak"},
					PriceCp:       &price,
					Beneficiaries: []strategy.Share{{AccountID: acct(1), Weight: -1}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a beneficiary with no account",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{Name: "Cloak"},
					PriceCp:       &price,
					Beneficiaries: []strategy.Share{{Weight: 1}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "weights that overflow int64",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:    strategy.ItemRef{Name: "Cloak"},
					PriceCp: &price,
					Beneficiaries: []strategy.Share{
						{AccountID: acct(1), Weight: math.MaxInt64},
						{AccountID: acct(2), Weight: math.MaxInt64},
					},
				})

				return err
			},
			wantErr: ledger.ErrWeightOverflow,
		},
		{
			name:   "a solo kill on a pool where a solo kill is free",
			config: `{"solo_policy": "free"}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:    strategy.ItemRef{Name: "Cloak"},
					PriceCp: &price,
				})

				return err
			},
			wantErr: strategy.ErrNothingToPlan,
		},
		{
			name:   "adjustment with no account",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{AmountCp: 100})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment of zero",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(0)},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment against itself",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account:      strategy.AccountRef{ID: acct(0)},
					AmountCp:     100,
					Counterparty: acct(0),
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an adjustment with no representable negation",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account:      strategy.AccountRef{ID: acct(0)},
					AmountCp:     math.MinInt64,
					Counterparty: acct(1),
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a reversal of an empty batch",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanReversal(ctx, strategy.LedgerBatch{ID: acct(70), StrategyID: "zero_sum"})

				return err
			},
			wantErr: strategy.ErrEmptyProposal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 100_000, tc.config)), tc.wantErr)
		})
	}
}

// TestZeroSum_Config_RejectsWhatTheSchemaWouldHaveRejected asserts the planner re-validates rather than
// defaulting. A config that reached the planner past the API edge — from the importer, a backfill or a
// test — must not silently run a DKP system nobody chose.
func TestZeroSum_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`null`,
		`[]`,
		`"zero_sum"`,
		`{"default_price_cp": 100}{"default_price_cp": 200}`,
		`{"default_price_cp": -1}`,
		`{"default_price_cp": 1.5}`,
		`{"default_price_cp": "100"}`,
		`{"default_price_cp": null}`,
		`{"default_pryce_cp": 100}`,
		`{"winner_share": "sometimes"}`,
		`{"winner_share": null}`,
		`{"winner_shares": "included"}`,
		`{"solo_policy": "residue"}`,
		`{"solo_policy": null}`,
		`{"floor_cp": null}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyZeroSumPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 3, 10_000, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// everyZeroSumPlanner returns one minimal, otherwise-legal call per planner that reads the pool's
// config.
//
// PlanReversal is deliberately absent: it reads neither the current config nor any façade value it
// could fail on, which is a property rather than an oversight — see
// TestZeroSum_PlanReversal_IgnoresTodaysPoolConfig.
func everyZeroSumPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.ZeroSum{}

	return map[string]func(ctx strategy.Ctx) error{
		"award": func(ctx strategy.Ctx) error {
			price := core.Centipoints(1_000)
			_, err := s.PlanAward(ctx, strategy.AwardEvent{
				Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:          strategy.ItemRef{Name: "Cloak"},
				PriceCp:       &price,
				Beneficiaries: shares(3),
			})

			return err
		},
		"adjustment": func(ctx strategy.Ctx) error {
			_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
				Account: strategy.AccountRef{ID: acct(0)}, AmountCp: 10,
			})

			return err
		},
	}
}

// TestZeroSum_Config_AbsentIsTheDefaults_AndTypoedIsNot is the other direction of the strict decoding:
// a pool that has set nothing must still plan, under the guide's documented defaults.
func TestZeroSum_Config_AbsentIsTheDefaults_AndTypoedIsNot(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(30_000)

	for _, config := range []string{"", "{}", "  ", "\n{}\n"} {
		t.Run(fmt.Sprintf("%q", config), func(t *testing.T) {
			t.Parallel()

			p, err := strategy.ZeroSum{}.PlanAward(newCtx(t, 4, 0, config), strategy.AwardEvent{
				Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:          strategy.ItemRef{Name: "Cloak"},
				PriceCp:       &price,
				Beneficiaries: shares(4),
			})
			require.NoError(t, err)

			require.Len(t, p.Entries, 4,
				"an unset config runs the shipped default: the winner is excluded, so a four-strong "+
					"raid produces the debit and three credits")
			require.Equal(t, core.Centipoints(10_000), p.Entries[1].AmountCp)
		})
	}

	t.Run("a transposed knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.ZeroSum{}.PlanAward(
			newCtx(t, 3, 0, `{"winner_shares": "included"}`), strategy.AwardEvent{
				Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:    strategy.ItemRef{Name: "Cloak"},
				PriceCp: &price,
			})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "winner_shares")
	})

	t.Run("a null knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.ZeroSum{}.PlanAward(
			newCtx(t, 3, 0, `{"solo_policy": null, "winner_share": null}`), strategy.AwardEvent{
				Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:    strategy.ItemRef{Name: "Cloak"},
				PriceCp: &price,
			})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "solo_policy",
			"with several null knobs the first in sorted order is named, on every run")
	})
}

// TestZeroSum_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases FROM THE SCHEMA, so a knob
// added later is covered without anybody remembering to add a row.
func TestZeroSum_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	requireSchemaAgreesWithParser(t, strategy.ZeroSum{}.ConfigSchema(),
		map[string]string{
			// The generic integer value the helper builds is 1, and a default price of 1 centipoint is
			// perfectly legal — but this plan names its own price, so the knob has to be exercised at a
			// value the planner reads without the event overriding it.
			"default_price_cp": `{"default_price_cp": 2500}`,
		},
		func(t *testing.T, config string) error {
			t.Helper()

			price := core.Centipoints(1_000)
			_, err := strategy.ZeroSum{}.PlanAward(
				newCtx(t, 3, 0, config), strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{Name: "Cloak"},
					PriceCp:       &price,
					Beneficiaries: shares(3),
				})

			return err
		})
}

// TestZeroSum_ConfigSchema_DeclaresNoNumber restates canonical §1 where a schema could break it:
// `number` in a JSON Schema permits 12.5, and a decimal in the point path is a float.
func TestZeroSum_ConfigSchema_DeclaresNoNumber(t *testing.T) {
	t.Parallel()

	requireNoNumberType(t, strategy.ZeroSum{}.ConfigSchema())
}

// TestZeroSum_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan rather
// than producing a batch built on a zero.
func TestZeroSum_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.ZeroSum{}
	boom := fmt.Errorf("the read pool is closed")

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		for name, plan := range everyZeroSumPlanner() {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := newCtx(t, 3, 10_000, `{}`)
				ctx.systemErr = boom

				require.ErrorIs(t, plan(ctx), boom)
			})
		}
	})

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, `{}`)
		ctx.balanceErr = boom

		_, err := s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})
}

// --- Declaration and the goldens ------------------------------------------------------------------

// zeroSumGoldenConfig is the config every golden is planned under: every knob set to a non-default
// value, so that a knob that stopped being read shows up as a changed golden rather than as nothing.
const zeroSumGoldenConfig = `{"default_price_cp":250,"winner_share":"included",` +
	`"solo_policy":"write_off","floor_cp":-500}`

// zeroSumGoldenCases is one case per planner, plus the solo kill — the degenerate path whose entries
// come from a different branch of the allocator and are therefore not covered by the ordinary award.
func zeroSumGoldenCases() []goldenCase {
	s := strategy.ZeroSum{}
	raid, itemAward := acct(81), acct(82)

	return []goldenCase{
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				character := acct(50)
				price := core.Centipoints(30_000)

				p, err := s.PlanAward(newCtx(tb, 7, 0, zeroSumGoldenConfig), strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					CharacterID: &character,
					Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:     &price,
					Beneficiaries: []strategy.Share{
						{AccountID: acct(0), Weight: 12},
						{AccountID: acct(1), Weight: 9},
						{AccountID: acct(2), Weight: 8},
						{AccountID: acct(3), Weight: 3},
						{AccountID: acct(4), Weight: 1},
						{AccountID: acct(5), Weight: 1},
						{AccountID: acct(6), Weight: 1},
					},
					RaidID:      &raid,
					ItemAwardID: &itemAward,
					EffectiveAt: fixedNow,
					Reason:      "Nagafen, roll 97",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "award_solo",
			plan: func(tb testing.TB) strategy.BatchProposal {
				price := core.Centipoints(30_000)

				p, err := s.PlanAward(newCtx(tb, 1, 0, zeroSumGoldenConfig), strategy.AwardEvent{
					Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:     &price,
					RaidID:      &raid,
					EffectiveAt: fixedNow,
					Reason:      "solo Phinigel",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(
					newCtx(tb, 3, 0, zeroSumGoldenConfig), strategy.AdjustmentEvent{
						Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
						AmountCp:    -750,
						EffectiveAt: fixedNow,
						Reason:      "double-credited tick on 2024-05-30",
					})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := newCtx(tb, 4, 0, zeroSumGoldenConfig)
				price := core.Centipoints(1_000)

				original, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					PriceCp:       &price,
					Beneficiaries: shares(4),
					EffectiveAt:   fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:        "Nagafen, roll 97",
				})
				require.NoError(tb, err)

				p, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID:                 acct(70),
					Kind:               original.Kind,
					StrategyID:         original.StrategyID,
					StrategyVersion:    original.StrategyVersion,
					ConfigSnapshotJSON: original.ConfigSnapshotJSON,
					Reason:             original.Reason,
					EffectiveAt:        original.EffectiveAt,
					Entries:            original.Entries,
				})
				require.NoError(tb, err)

				return p
			},
		},
	}
}

// TestZeroSum_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
func TestZeroSum_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	requireGoldens(t, zeroSumGoldenDir, zeroSumGoldenCases())
}

// TestZeroSum_Goldens_CoverEveryPlanner is the anti-drift half: a planner added without a golden would
// leave the whole-proposal assertion covering fewer planners than the strategy has, silently.
func TestZeroSum_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	requireGoldensCoverPlanners(t, zeroSumGoldenDir, zeroSumGoldenCases(),
		[]string{"adjustment", "award", "reversal"})
}

// TestZeroSum_EveryPlannerInvariant_IsDeclared keeps the strategy-level catalogue and the per-proposal
// sets in step, in both directions.
func TestZeroSum_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	requireInvariantsAgree(t, strategy.ZeroSum{}, plannedProposals(t, zeroSumGoldenCases()))
}

// TestZeroSum_Planners_ConsumeNoRandomness asserts the injected Rng is offered and refused.
//
// A planner that consumed randomness would need its seed persisted onto the batch for a replay to be
// byte-identical; this one consumes none, so its proposals carry no seed — and the way to state that
// as a fact rather than an assumption is to count the calls and require the seed to be absent.
func TestZeroSum_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 4, 100_000, zeroSumGoldenConfig)
	s := strategy.ZeroSum{}
	price := core.Centipoints(999)

	award, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		PriceCp:       &price,
		Beneficiaries: shares(4),
		EffectiveAt:   fixedNow,
	})
	require.NoError(t, err)

	adjustment, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
		Account: strategy.AccountRef{ID: acct(1)}, AmountCp: 42, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	for _, p := range []strategy.BatchProposal{award, adjustment} {
		require.Nil(t, p.RngSeed,
			"%s carries a seed it never consumed; a seed asserts that replaying from it reproduces "+
				"the plan, which would be true here only by irrelevance", p.Kind)
	}

	require.Zero(t, ctx.rng.calls,
		"zero_sum must consume no randomness: its only tie-break is the allocator's account_id "+
			"ordering, which is deliberately NOT random so that two replays agree")
}
