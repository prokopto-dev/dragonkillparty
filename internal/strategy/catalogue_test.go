package strategy_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The registry, tested. Phase 1, #193.
//
// `pool.strategy_id` carries no CHECK constraint — db/schema.hcl says so explicitly, because the set
// of strategies is code-defined and a CHECK would make every new strategy a schema change. What it
// promises instead is that "the strategy package validates it", and Catalogue plus ByID are that
// validation. These tests are what make the promise true rather than stated.

// TestCatalogue_EveryStrategy_IsWellFormed is the contract every entry owes before it can be reached
// by an id.
func TestCatalogue_EveryStrategy_IsWellFormed(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for _, s := range strategy.Catalogue() {
		t.Run(s.ID(), func(t *testing.T) {
			require.NotEmpty(t, s.ID())
			require.Equal(t, strings.ToLower(s.ID()), s.ID(),
				"a strategy id is lowercase snake_case: it is written onto every batch and is public API")
			require.NotContains(t, s.ID(), " ")
			require.NotContains(t, s.ID(), "-", "snake_case, not kebab-case")
			require.NotEmpty(t, s.Version(), "the version is snapshotted onto every batch")
			require.True(t, strategy.IsRuleKind(string(s.RuleKind())),
				"%q declares rule kind %q, which is not one of the three questions a pool answers; a "+
					"strategy outside the closed set is one PoolConfig.Resolve can put in no slot, so "+
					"no pool could ever run it (ADR-0026)", s.ID(), s.RuleKind())
			require.NotEmpty(t, s.BalanceKinds())
			require.NotEmpty(t, s.Invariants(),
				"a strategy that declares no invariants is a red flag: the declared set is what the "+
					"ledger checks before committing, and an empty set means the ledger trusts you")

			var doc struct {
				Type                 string          `json:"type"`
				Title                string          `json:"title"`
				AdditionalProperties *bool           `json:"additionalProperties"`
				Properties           map[string]any  `json:"properties"`
				Schema               json.RawMessage `json:"$schema"`
			}

			require.NoError(t, json.Unmarshal(s.ConfigSchema(), &doc),
				"the config schema is served to the SPA and to every SDK, so it must be valid JSON")
			require.Equal(t, "object", doc.Type)
			require.NotEmpty(t, doc.Title, "the title names the strategy in the settings form")
			require.NotEmpty(t, doc.Schema, "the schema declares its own dialect")
			require.NotNil(t, doc.AdditionalProperties)
			require.False(t, *doc.AdditionalProperties,
				"additionalProperties must be false: a typo'd knob has to be a validation error at "+
					"the edge, not a silently ignored key that leaves the pool running the default")
			require.NotEmpty(t, doc.Properties)
		})

		require.False(t, seen[s.ID()],
			"strategy id %q is registered twice; the id is what a pool row names, so a duplicate "+
				"makes ByID's answer depend on the order of a slice", s.ID())
		seen[s.ID()] = true
	}
}

// TestCatalogue_EveryQuestion_HasAnAnswer is the composition's habitability check.
//
// A pool composes one rule per question (ADR-0026), so a release in which no shipped strategy
// declares one of the three kinds is a release in which no guild can configure a working pool — and
// every per-strategy test would still be green, because each strategy is individually correct. It is
// the same class of gap TestCatalogue_ContainsEveryStrategyInThePackage closes for registration.
func TestCatalogue_EveryQuestion_HasAnAnswer(t *testing.T) {
	t.Parallel()

	answered := map[strategy.RuleKind][]string{}

	for _, s := range strategy.Catalogue() {
		answered[s.RuleKind()] = append(answered[s.RuleKind()], s.ID())
	}

	for _, kind := range []strategy.RuleKind{strategy.RuleEarn, strategy.RuleSpend, strategy.RuleOverTime} {
		require.NotEmpty(t, answered[kind],
			"no shipped strategy answers %q, so no pool can be configured to answer that question at "+
				"all — a guild composing a pool would have an empty dropdown", kind)
	}
}

// TestCatalogue_ByID_ResolvesEveryShippedStrategy covers both directions of the lookup, including the
// refusal.
func TestCatalogue_ByID_ResolvesEveryShippedStrategy(t *testing.T) {
	t.Parallel()

	for _, want := range strategy.Catalogue() {
		got, err := strategy.ByID(want.ID())
		require.NoError(t, err)
		require.Equal(t, want.ID(), got.ID())
	}

	_, err := strategy.ByID("epgp")
	require.ErrorIs(t, err, strategy.ErrUnknownStrategy)
	require.ErrorContains(t, err, "epgp",
		"the refusal names the id it was given: a pool row naming a strategy this binary does not "+
			"have is an operator downgrading across a release, and the message is the whole diagnosis")
	require.ErrorContains(t, err, "fixed_price", "and lists what it does have")

	_, err = strategy.ByID("")
	require.ErrorIs(t, err, strategy.ErrUnknownStrategy)
}

// TestCatalogue_IDs_AreSortedAndMatchTheCatalogue: IDs answers "which values may this field take?",
// so its order must be stable rather than the reader's order.
func TestCatalogue_IDs_AreSortedAndMatchTheCatalogue(t *testing.T) {
	t.Parallel()

	ids := strategy.IDs()
	require.Len(t, ids, len(strategy.Catalogue()))
	require.True(t, sort.StringsAreSorted(ids),
		"a generated artefact built from this list must not churn when the catalogue is reordered "+
			"for the reader's sake")

	for _, s := range strategy.Catalogue() {
		require.Contains(t, ids, s.ID())
	}
}

// TestCatalogue_ReturnsAFreshSlice is the package-level-mutable-state rule, asserted.
//
// A shared backing array is one append in a test away from an intermittent failure under
// -shuffle=on, and this catalogue is exactly the kind of value somebody would later "optimise" into
// a package-level var.
func TestCatalogue_ReturnsAFreshSlice(t *testing.T) {
	t.Parallel()

	first := strategy.Catalogue()
	require.NotEmpty(t, first)

	first[0] = nil

	second := strategy.Catalogue()
	require.NotNil(t, second[0], "Catalogue must return a fresh slice, not a package-level one")

	ids := strategy.IDs()
	ids[0] = "clobbered"
	require.NotEqual(t, "clobbered", strategy.IDs()[0])
}

// TestCatalogue_ContainsEveryStrategyInThePackage is the anti-drift half, and it is the one that
// earns this file.
//
// A strategy is UNREACHABLE unless it is in the catalogue: nothing else turns a pool's strategy_id
// into a planner. A new file that implements PointStrategy, ships tests, passes review and is never
// registered is a strategy the product cannot run, and every test it has would still be green.
//
// It finds them the way the package declares them: every strategy carries a compile-time assertion
// `var _ PointStrategy = X{}`, which is the idiom fixed_price established and this test now makes
// load-bearing. Parsing the package's own source is the same technique arch_test.go uses to prove
// law 3 — the alternative, a hand-maintained list here, is a third place to forget.
func TestCatalogue_ContainsEveryStrategyInThePackage(t *testing.T) {
	t.Parallel()

	declared := declaredStrategyTypes(t)
	require.NotEmpty(t, declared,
		"no `var _ PointStrategy = X{}` assertion was found at all, so this test is watching nothing")

	registered := map[string]bool{}

	for _, s := range strategy.Catalogue() {
		registered[reflect.TypeOf(s).Name()] = true
	}

	for _, name := range declared {
		require.True(t, registered[name],
			"type %s implements PointStrategy but is not in Catalogue(), so no pool can ever run "+
				"it: nothing else turns a strategy_id into a planner", name)
	}

	require.Len(t, registered, len(declared),
		"the catalogue holds a strategy the package does not declare with a compile-time "+
			"PointStrategy assertion; add `var _ PointStrategy = X{}` beside its type")
}

// declaredStrategyTypes parses the package's sources for `var _ PointStrategy = X{}`.
func declaredStrategyTypes(t *testing.T) []string {
	t.Helper()

	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var out []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "parse %s", name)

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}

			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || value.Names[0].Name != "_" {
					continue
				}

				ident, ok := value.Type.(*ast.Ident)
				if !ok || ident.Name != "PointStrategy" || len(value.Values) != 1 {
					continue
				}

				lit, ok := value.Values[0].(*ast.CompositeLit)
				if !ok {
					continue
				}

				if typeName, ok := lit.Type.(*ast.Ident); ok {
					out = append(out, typeName.Name)
				}
			}
		}
	}

	sort.Strings(out)

	return out
}
