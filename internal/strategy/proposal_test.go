package strategy_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// sample returns a proposal with two entries, used as the thing to perturb below.
func sample() strategy.BatchProposal {
	seed := int64(11)

	return strategy.BatchProposal{
		Kind:            "award",
		StrategyID:      "zero_sum",
		StrategyVersion: "1.0.0",
		RngSeed:         &seed,
		EffectiveAt:     core.Micros(1_717_243_200_000_000),
		Entries: []strategy.EntryProposal{
			{AccountID: "0000000000000000000000ACC1", BalanceKind: "dkp", AmountCp: -100},
			{AccountID: "0000000000000000000000ACC2", BalanceKind: "dkp", AmountCp: 100},
		},
	}
}

// TestProposal_Canonical_PreservesEntryOrder is the property that makes P8 able to catch an
// ordering bug, asserted directly rather than only through the property test.
//
// Sorting here would be the tempting choice — a batch is conceptually a set of deltas — and it would
// be wrong. A planner that iterated a map and emitted its entries in a different order on every run
// would produce identical canonical bytes under a sorting implementation, pass the determinism
// property, and write a differently-ordered batch every time.
func TestProposal_Canonical_PreservesEntryOrder(t *testing.T) {
	t.Parallel()

	forward := sample()

	reversed := sample()
	reversed.Entries = []strategy.EntryProposal{forward.Entries[1], forward.Entries[0]}

	a, err := forward.Canonical()
	require.NoError(t, err)

	b, err := reversed.Canonical()
	require.NoError(t, err)

	require.NotEqual(t, a, b,
		"Canonical must NOT sort entries: preserving the planner's order is what lets the "+
			"determinism property see a planner that ranges a map")
}

// TestProposal_Canonical_IsStableAndUnescaped covers the two encoder settings the digest depends on.
//
// HTML escaping is off, so a reason containing `&` or `<` — an officer typing "Naggy & Vox" —
// canonicalises to those bytes rather than to `&`. The value of asserting it is that the
// default is the other way: a future refactor to json.Marshal would silently change every hash the
// product has ever computed, and every stored chain would stop verifying at once.
func TestProposal_Canonical_IsStableAndUnescaped(t *testing.T) {
	t.Parallel()

	p := sample()
	p.Reason = `Naggy & Vox <split>`

	first, err := p.Canonical()
	require.NoError(t, err)

	second, err := p.Canonical()
	require.NoError(t, err)

	require.Equal(t, first, second, "Canonical must be a function of its input and nothing else")
	require.Contains(t, string(first), `Naggy & Vox <split>`,
		"HTML escaping must be off; a digest whose input depends on a marshalling default is a "+
			"digest that changes when somebody upgrades a library")
	require.NotContains(t, string(first), `\u0026`,
		"`&` must appear literally, not as the \\u0026 escape encoding/json emits by default")
	require.NotContains(t, string(first), `\u003c`)
	require.NotContains(t, string(first), "\n", "the trailing newline json.Encoder adds must be stripped")

	// It is real JSON, not merely stable bytes — `dkp verify-ledger` and any future auditor have to
	// be able to read it.
	var back map[string]any
	require.NoError(t, json.Unmarshal(first, &back))
	require.Equal(t, "award", back["kind"])
}

// TestProposal_NetAmountCp_ReportsOverflowRatherThanWrapping is the arithmetic guard.
//
// A wrapped sum satisfies a zero-sum check by accident, which is the one way conservation can be
// defeated without any individual amount looking wrong — so the function reports rather than wraps,
// and the ledger's NoAmountOverflow invariant turns that report into a rejected batch.
func TestProposal_NetAmountCp_ReportsOverflowRatherThanWrapping(t *testing.T) {
	t.Parallel()

	p := sample()
	p.Entries = []strategy.EntryProposal{
		{AccountID: "0000000000000000000000ACC1", BalanceKind: "dkp", AmountCp: math.MaxInt64},
		{AccountID: "0000000000000000000000ACC2", BalanceKind: "dkp", AmountCp: 1},
	}

	_, ok := p.NetAmountCp()
	require.False(t, ok, "a sum past int64 must be reported, not wrapped into a plausible number")

	net, ok := sample().NetAmountCp()
	require.True(t, ok)
	require.Equal(t, core.Centipoints(0), net, "a zero-sum award nets to exactly 0")
}

// TestProposal_Negated_IsTheDefaultReversal covers the shape of the default inverse.
//
// It is the DEFAULT and it is not always right: `.claude/rules/ledger-and-strategy.md` is explicit
// that entry-wise negation is wrong for at least one balance kind — Suicide Kings' sk_position is an
// ordering rather than a quantity, and negating a position delta does not restore a list everyone
// else has shifted up in. A strategy whose balance kind is not a plain quantity overrides this in
// PR 10b; this pins what the default does so that the override is a visible departure from it.
func TestProposal_Negated_IsTheDefaultReversal(t *testing.T) {
	t.Parallel()

	original := sample()
	target := core.ULID("0000000000000000000BATCH01")

	got, err := original.Negated(target)
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, got.Kind)
	require.Equal(t, &target, got.ReversesBatchID)
	require.Nil(t, got.RngSeed,
		"a negation consumes no randomness; carrying the original's seed forward would assert that "+
			"replaying from it reproduces this batch, which is false")
	require.Zero(t, got.EffectiveAt,
		"a reversal is a new economic event at the time it is decided; backdating it to the "+
			"original's effective time would rewrite what every intermediate balance meant")

	require.Len(t, got.Entries, len(original.Entries))
	for i, e := range got.Entries {
		require.Equal(t, -original.Entries[i].AmountCp, e.AmountCp)
		require.Equal(t, original.Entries[i].AccountID, e.AccountID,
			"the provenance must carry through: a reversal of an award for an item is still about "+
				"that item, and dropping the link makes it unattributable in the statement view")
	}

	// The original is untouched — Negated returns a new proposal rather than mutating in place.
	require.Equal(t, sample(), original)
}

// TestProposal_Negated_RefusesWhatItCannotInvert covers the two inputs with no correct answer.
//
// math.MinInt64 is the one int64 with no representable negation, and clamping it would produce a
// "reversal" that does not reverse — the exact failure P5 exists to catch, and one that would look
// entirely normal in the batch it wrote.
func TestProposal_Negated_RefusesWhatItCannotInvert(t *testing.T) {
	t.Parallel()

	empty := sample()
	empty.Entries = nil

	_, err := empty.Negated("0000000000000000000BATCH01")
	require.ErrorIs(t, err, strategy.ErrEmptyProposal)

	unrepresentable := sample()
	unrepresentable.Entries[0].AmountCp = math.MinInt64

	_, err = unrepresentable.Negated("0000000000000000000BATCH01")
	require.ErrorIs(t, err, strategy.ErrNotNegatable)
}
