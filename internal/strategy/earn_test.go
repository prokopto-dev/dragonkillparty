package strategy_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The assertions the earn-family suites share. Phase 1, #193.
//
// fixed_price shipped alone, so its test file could state each of these once inline. Three more
// strategies land together here, and an assertion written three times is an assertion that will be
// written twice the next time: `tick`, `cap` and `start_points` must each hold the golden contract,
// the invariant-declaration contract and the schema/parser contract, and a helper is what makes
// "each" true rather than "each of the ones somebody remembered".
//
// Each helper takes what varies — the strategy, its golden directory, its planners — and asserts what
// does not. The per-strategy files keep the arithmetic, which is the part that is genuinely different
// and must not be abstracted into a shared table nobody reads.

// requireGoldens compares each planner's WHOLE canonical proposal against its committed golden.
//
// Asserting on a handful of fields hides the fourth that changed, and the fourth is exactly the one
// nobody thought to assert. The canonical form is the byte string the determinism property hashes, so
// a golden over it pins entry order, provenance pointers, the config snapshot, the declared
// invariants and the effective time in one comparison.
//
// -update is refused under CI, for the reason it is refused for every golden in this repository: the
// run that would have caught a changed proposal must not be the run that overwrites the evidence.
func requireGoldens(t *testing.T, dir string, cases []goldenCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.plan(t).Canonical()
			require.NoError(t, err)

			path := filepath.Join(dir, tc.name+".json")

			if *updateGolden {
				if os.Getenv("CI") != "" {
					t.Fatal("refusing -update under CI: a golden CI can rewrite proves nothing")
				}

				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
				t.Logf("wrote %s", path)

				return
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "read the committed golden at %s", path)

			require.JSONEq(t, string(want), string(got),
				"the %s proposal changed shape. If you meant it, re-run with -update on a laptop "+
					"(never CI) and read the diff before committing it.", tc.name)
			require.Equal(t, string(want), string(got)+"\n",
				"the %s proposal's CANONICAL BYTES changed. Equivalent JSON is not enough: the "+
					"canonical form is what the determinism property hashes, so field order and "+
					"entry order are part of the contract.", tc.name)
		})
	}
}

// requireGoldensCoverPlanners is the anti-drift half of the golden contract: the batch kinds the
// cases produce, the case names, and the files actually committed must all be the same set.
//
// A planner added without a golden leaves the whole-proposal assertion covering fewer planners than
// the strategy has, silently; a golden deleted from the tree does the same from the other end.
func requireGoldensCoverPlanners(t *testing.T, dir string, cases []goldenCase, wantKinds []string) {
	t.Helper()

	kinds := map[string]bool{}
	for _, p := range plannedProposals(t, cases) {
		kinds[p.Kind] = true
	}

	got := make([]string, 0, len(kinds))
	for k := range kinds {
		got = append(got, k)
	}

	sort.Strings(got)
	require.Equal(t, wantKinds, got, "every planner must contribute a golden case")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "the golden directory must exist and be committed")

	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}

	want := make([]string, 0, len(cases))
	for _, c := range cases {
		want = append(want, c.name+".json")
	}

	sort.Strings(files)
	sort.Strings(want)

	require.Equal(t, want, files,
		"the committed goldens and the planners must be the same set — a deleted golden is how a "+
			"whole-proposal assertion quietly stops covering a planner")
}

// plannedProposals plans one proposal per golden case.
func plannedProposals(tb testing.TB, cases []goldenCase) []strategy.BatchProposal {
	tb.Helper()

	out := make([]strategy.BatchProposal, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.plan(tb))
	}

	return out
}

// requireInvariantsAgree keeps a strategy's catalogue and its per-proposal sets in step, in both
// directions.
//
// The catalogue is what a reviewer reads to see what constrains a strategy, and the proposal's set is
// what the ledger actually executes. A rule attached to a batch but missing from the catalogue makes
// the catalogue a lie; a rule in the catalogue that no planner ever attaches is a claim of protection
// nothing provides.
func requireInvariantsAgree(
	t *testing.T, s strategy.PointStrategy, proposals []strategy.BatchProposal,
) {
	t.Helper()

	require.NotEmpty(t, s.Invariants(), "a strategy that declares no invariants is a red flag")

	declared := map[strategy.InvariantKind]bool{}

	for _, inv := range s.Invariants() {
		require.Equal(t, strategy.BalanceKindDKP, inv.BalanceKind,
			"every declared invariant must be scoped to the one balance kind this strategy moves; "+
				"the commit-time engine rejects an invariant scoped to a kind the batch does not touch")
		declared[inv.Kind] = true
	}

	attached := map[strategy.InvariantKind]bool{}

	for _, p := range proposals {
		require.NotEmpty(t, p.Invariants, "the %s planner declares nothing", p.Kind)

		for _, inv := range p.Invariants {
			require.True(t, declared[inv.Kind],
				"the %s planner attaches %s, which Invariants() does not list", p.Kind, inv.Kind)

			if inv.Kind == strategy.InvariantNonNegative {
				require.NotNil(t, inv.FloorCp,
					"NonNegative with no floor is rejected at commit time: 'nobody may go below "+
						"zero' and 'somebody forgot' must not be the same declaration")
			}

			attached[inv.Kind] = true
		}
	}

	for kind := range declared {
		require.True(t, attached[kind],
			"Invariants() lists %s, which no planner attaches to a proposal — the ledger executes "+
				"the proposal's set, so this rule protects nothing", kind)
	}
}

// requireNonNegativeFloor returns the floor a proposal's NonNegative invariant declares, failing if
// it declares none. It exists so a test can assert that the POOL's floor reached the proposal rather
// than the strategy catalogue's default.
func requireNonNegativeFloor(t *testing.T, p strategy.BatchProposal) core.Centipoints {
	t.Helper()

	for _, inv := range p.Invariants {
		if inv.Kind == strategy.InvariantNonNegative {
			require.NotNil(t, inv.FloorCp, "NonNegative with no floor is rejected at commit time")

			return *inv.FloorCp
		}
	}

	t.Fatalf("the %s proposal declares no NonNegative invariant", p.Kind)

	return 0
}

// requireSchemaAgreesWithParser derives its cases FROM THE SCHEMA, so a knob added later is covered
// without anybody remembering to add a row.
//
// Both directions of schema/parser drift are asserted, because it has two:
//
//   - every declared knob REJECTS null — the schema gives each one a type, and null is a value of
//     none of them. encoding/json decodes a null into a non-pointer field as a no-op, so this is the
//     failure that looks exactly like "the officer left it alone";
//   - every declared knob ACCEPTS a legal value of its declared type — a knob the settings form
//     offers and the planner refuses is the same drift seen from the other side.
//
// `legal` supplies the whole document for a knob whose legal value cannot be written alone: a `cap`
// pool's over-cap ratio needs a soft cap to apply to, and a role multiplier is an object rather than a
// scalar. Anything absent from it is tested as a single-knob document built from the declared type,
// which is the case that must keep working without a per-knob entry.
func requireSchemaAgreesWithParser(
	t *testing.T, schemaJSON []byte, legal map[string]string, plan func(*testing.T, string) error,
) {
	t.Helper()

	var schema struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(schemaJSON, &schema))
	require.NotEmpty(t, schema.Properties)

	for name, prop := range schema.Properties {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := plan(t, fmt.Sprintf(`{%q: null}`, name))
			require.ErrorIs(t, err, strategy.ErrInvalidConfig,
				"knob %q accepts null. The schema types it, so null is not one of its values — and a "+
					"null decoded into a non-pointer field is a no-op, which means the pool silently "+
					"runs the default instead.", name)
			require.ErrorContains(t, err, name, "the rejection must name the knob")

			doc, ok := legal[name]
			if !ok {
				var value string

				switch {
				case len(prop.Enum) > 0:
					value = fmt.Sprintf("%q", prop.Enum[0])
				case prop.Type == "integer":
					value = "1"
				default:
					t.Fatalf("knob %q is declared as %q, which this test has no legal value for — add "+
						"one to the `legal` map in the same change that adds the type", name, prop.Type)
				}

				doc = fmt.Sprintf(`{%q: %s}`, name, value)
			}

			require.NoError(t, plan(t, doc),
				"knob %q is declared in the schema but the parser refuses %s, so the settings form "+
					"offers a control that cannot be set", name, doc)
		})
	}
}

// requireNoNumberType restates canonical §1 where a schema could break it: `number` in a JSON Schema
// permits 12.5, and a decimal in the point path is a float. It recurses, because a nested property —
// a role multiplier inside an array — is as much part of the config as a top-level one.
func requireNoNumberType(t *testing.T, schemaJSON []byte) {
	t.Helper()

	var node map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schemaJSON, &node))

	requireNoNumberTypeIn(t, node, "$")
}

// requireNoNumberTypeIn walks one schema node's properties and items.
func requireNoNumberTypeIn(t *testing.T, node map[string]json.RawMessage, path string) {
	t.Helper()

	if raw, ok := node["type"]; ok {
		var typ string
		if err := json.Unmarshal(raw, &typ); err == nil {
			require.NotEqual(t, "number", typ,
				"%s is declared as `number`, which permits a decimal. Money is integer centipoints "+
					"and ratios are integer basis points.", path)
		}
	}

	if raw, ok := node["properties"]; ok {
		var props map[string]map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &props))

		for name, prop := range props {
			requireNoNumberTypeIn(t, prop, path+"."+name)
		}
	}

	if raw, ok := node["items"]; ok {
		var items map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &items))

		requireNoNumberTypeIn(t, items, path+"[]")
	}
}
