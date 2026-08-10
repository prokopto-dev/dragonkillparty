package ledger_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The drift tests for canonical §5's third clause: "a test asserts the three copies agree".
//
// The three copies are the Go catalogue in internal/ledger/kinds.go, the CHECK expression in
// db/schema.hcl (written by `make gen`), and the CHECK the migrations actually create in the
// database. The OpenAPI enum is the fourth copy and has no subject yet — no ledger endpoint exists
// at Phase 0 — so its test is written to be correct whether or not one exists, in the shape
// internal/core/openapi_contract_test.go established, with a negative control proving the walker
// fails on a violation rather than passing vacuously forever.
//
// WHAT DRIFT COSTS HERE. A kind that exists in Go and not in the CHECK is a legal write that the
// database rejects at COMMIT. The ledger is append-only, so there is no repair path that edits the
// row: the officer's award simply does not exist, and the first person to notice is a raider
// disputing a balance weeks later.

// repoRoot returns the directory holding go.mod, so these tests find db/ and openapi/ regardless of
// where `go test` was invoked from.
//
// Walked rather than filepath.Abs("../.."), which produces the wrong answer silently the day this
// file moves — the same reasoning as internal/authz/catalogue_test.go's canonicalConventionsPath.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")

		dir = parent
	}
}

func readSchemaHCL(t *testing.T) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), "db", "schema.hcl")

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	return string(raw)
}

// TestLedgerKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly what
// the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the two
// expressions and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into the CHECK and not added to Go. It is the same question
// `make verify-generated` asks of every other generated tree, asked here as an ordinary test so a
// laptop finds the drift before CI does.
func TestLedgerKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := ledger.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's ledger enum CHECKs have drifted from internal/ledger/kinds.go — run `make gen` "+
			"(and `make migration NAME=<snake_case>` if a value actually changed)")

	// Belt and braces: the expressions the CHECK must carry, named explicitly so a failure above
	// reads as "which enum" rather than as a whole-file diff.
	require.Contains(t, committed, ledger.CheckExpr("kind", ledger.BatchKinds()))
	require.Contains(t, committed, ledger.CheckExpr("source", ledger.BatchSources()))
}

// TestLedgerKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
// hand-edited CHECK must not survive a render.
//
// Without it, TestLedgerKinds_CheckMatchesCatalogue is indistinguishable from a test that compares a
// file to itself — the classic tautology, and the one that matters most here because the gate's whole
// job is to notice a single edited word.
func TestLedgerKinds_SchemaDivergence_IsRestored(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	tests := []struct {
		name    string
		mutate  func(string) string
		explain string
	}{
		{
			name:    "value dropped from the kind CHECK",
			mutate:  func(s string) string { return strings.Replace(s, "'re_attribution', ", "", 1) },
			explain: "a kind deleted from the CHECK but still legal in Go",
		},
		{
			name:    "value misspelled in the kind CHECK",
			mutate:  func(s string) string { return strings.Replace(s, "'attendance'", "'attendence'", 1) },
			explain: "the spelling drift that makes every tick fail at COMMIT",
		},
		{
			name:    "value invented in the source CHECK",
			mutate:  func(s string) string { return strings.Replace(s, "'discord'", "'discord', 'irc'", 1) },
			explain: "a source hand-added to the CHECK and never added to Go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mutated := tt.mutate(committed)
			require.NotEqual(t, committed, mutated, "the mutation did not apply — fixture is stale")

			restored, err := ledger.RenderSchemaHCL(mutated)
			require.NoError(t, err)

			require.Equal(t, committed, restored, "%s survived a render", tt.explain)
		})
	}
}

// TestLedgerKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries its markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestLedgerKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — ledger enum CHECKs, from internal/ledger/kinds.go. Run `make gen`."
	end := "  // END GENERATED — ledger enum CHECKs."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"ledger_batch\" {\n}\n"},
		{name: "begin only", src: begin + "\n"},
		{name: "end only", src: end + "\n"},
		{name: "begin twice", src: begin + "\n" + begin + "\n" + end + "\n"},
		{name: "end twice", src: begin + "\n" + end + "\n" + end + "\n"},
		{name: "end before begin", src: end + "\n" + begin + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := ledger.RenderSchemaHCL(tt.src)
			require.ErrorIs(t, err, ledger.ErrSchemaMarkersMissing)
			require.Empty(t, out, "a failed render must not return a half-written schema")
		})
	}
}

// TestLedgerKinds_MigrationCheckMatchesCatalogue asserts the copy that actually reaches the
// database — the CHECK the migration set creates — matches the catalogue.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a kind is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order, and that is the only one that has to agree with Go.
func TestLedgerKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		constraint string
		column     string
		values     []string
	}{
		{constraint: "ledger_batch_kind_enum", column: "kind", values: ledger.BatchKinds()},
		{constraint: "ledger_batch_source_enum", column: "source", values: ledger.BatchSources()},
	}

	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			t.Parallel()

			last, file := lastMigrationCheck(t, tt.constraint, tt.column)
			require.NotEmpty(t, file, "no migration declares CONSTRAINT %q — the enum reaches no database", tt.constraint)

			require.Equal(t, ledger.CheckExpr(tt.column, tt.values), last,
				"%s carries a %s CHECK that the Go catalogue no longer matches — the values in "+
					"internal/ledger/kinds.go need a migration, written with "+
					"`make migration NAME=<snake_case>` after `make gen`", file, tt.constraint)
		})
	}
}

// lastMigrationCheck returns the CHECK expression the final migration to declare constraint carries,
// and the file it came from. Files are read in lexical order, which is migration order — the numeric
// prefix is zero-padded precisely so those two orders are the same.
func lastMigrationCheck(t *testing.T, constraint, column string) (expr, file string) {
	t.Helper()

	dir := filepath.Join(repoRoot(t), "db", "migrations-sqlite")

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	require.NoError(t, err, "glob %s", dir)
	require.NotEmpty(t, files, "no migrations found in %s", dir)

	sort.Strings(files)

	// The expression is `<column> IN ('a', 'b')`: one parenthesised list, no nesting, which is what
	// lets the inner group be [^()]* rather than a paren-balancing parser.
	pattern := regexp.MustCompile(
		fmt.Sprintf(`CONSTRAINT "%s" CHECK \((%s IN \([^()]*\))\)`,
			regexp.QuoteMeta(constraint), regexp.QuoteMeta(column)))

	for _, f := range files {
		raw, readErr := os.ReadFile(f)
		require.NoError(t, readErr, "read %s", f)

		matches := pattern.FindAllStringSubmatch(string(raw), -1)
		if len(matches) == 0 {
			continue
		}

		expr = matches[len(matches)-1][1]
		file = filepath.Base(f)
	}

	return expr, file
}

// TestLedgerKinds_StrategyReversalKind_IsInCatalogue closes the one place a kind is still written as
// a literal outside the catalogue.
//
// internal/strategy cannot import internal/ledger — the purity law forbids reaching internal/store,
// transitively (law 3) — so strategy.KindReversal, which Negated stamps onto every reversal
// proposal, has to be its own constant. This asserts the two agree, which is the whole guarantee a
// shared constant would have given.
func TestLedgerKinds_StrategyReversalKind_IsInCatalogue(t *testing.T) {
	t.Parallel()

	require.Contains(t, ledger.BatchKinds(), strategy.KindReversal,
		"strategy.KindReversal is stamped on every reversal batch and must be a legal ledger_batch.kind")
}

// TestBatchKinds_Values_AreCanonicalEnumValues checks both catalogues against canonical §5's rule for
// enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestBatchKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	tests := []struct {
		name   string
		values []string
	}{
		{name: "ledger_batch.kind", values: ledger.BatchKinds()},
		{name: "ledger_batch.source", values: ledger.BatchSources()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotEmpty(t, tt.values)

			seen := make(map[string]bool, len(tt.values))

			for _, v := range tt.values {
				require.Regexp(t, snakeCase, v, "%q is not lowercase snake_case (canonical §5)", v)
				require.False(t, seen[v], "%q appears twice in the catalogue", v)

				seen[v] = true
			}
		})
	}
}

// TestCheckExpr_RendersASQLInList pins the rendering, because the generator and this test file are
// two callers that have to agree with the committed schema byte for byte — including the ", "
// separator, which is what makes the generated expression identical to the one that shipped and
// therefore migration-free.
func TestCheckExpr_RendersASQLInList(t *testing.T) {
	t.Parallel()

	require.Equal(t, "kind IN ('a', 'b_c')", ledger.CheckExpr("kind", []string{"a", "b_c"}))
	require.Equal(t, "source IN ('web')", ledger.CheckExpr("source", []string{"web"}))
}

// TestLedgerKinds_OpenAPIEnums_MatchCatalogue is the fourth copy: canonical §5 requires the OpenAPI
// enum to come from the same catalogue as the CHECK.
//
// TODAY IT HAS NO SUBJECT — no ledger endpoint exists at Phase 0, so openapi.json contains no batch
// kind or source enum and this passes having checked nothing. It is written now, in
// internal/core/openapi_contract_test.go's shape, so that it becomes load-bearing the instant the
// first ledger DTO lands rather than being remembered then.
//
// THE TRIGGER IS A MARKER VALUE, not a subset match. An enum sharing 'import' or 'write_off' with
// this catalogue may legitimately be a different vocabulary — account.system_key carries 'write_off'
// too — so an enum is only judged against the catalogue when it carries a value that belongs to
// nothing else in the schema.
func TestLedgerKinds_OpenAPIEnums_MatchCatalogue(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	path := filepath.Join(root, "openapi", "openapi.json")

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	var doc any
	require.NoError(t, json.Unmarshal(raw, &doc), "parse openapi.json")

	assertEnumsMatchCatalogue(t, doc)
}

// TestLedgerKinds_OpenAPIWalker_DetectsAPartialEnum is the negative control: a synthetic spec whose
// batch-kind enum is missing a value must be caught by the same walker. Without it, the test above is
// indistinguishable from one that never checks anything.
func TestLedgerKinds_OpenAPIWalker_DetectsAPartialEnum(t *testing.T) {
	t.Parallel()

	partial := ledger.BatchKinds()[:len(ledger.BatchKinds())-1]

	var doc any
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{
	  "components": { "schemas": { "LedgerBatchDTO": { "properties": {
	    "kind": { "type": "string", "enum": %s }
	  } } } }
	}`, mustJSON(t, partial))), &doc))

	found := collectEnums(doc, "$")
	require.Len(t, found, 1, "the walker must find the one enum in the fixture")

	for _, e := range found {
		require.True(t, isBatchKindEnum(e.values), "the marker value must classify this as a kind enum")
		require.NotEqual(t, ledger.BatchKinds(), e.values, "the fixture is meant to be short one value")
	}
}

// enumSite is one `enum` array in the spec, with the JSON path that reaches it — the path is what
// makes a failure actionable rather than "some enum somewhere is wrong".
type enumSite struct {
	path   string
	values []string
}

func assertEnumsMatchCatalogue(t *testing.T, doc any) {
	t.Helper()

	for _, e := range collectEnums(doc, "$") {
		switch {
		case isBatchKindEnum(e.values):
			require.Equal(t, ledger.BatchKinds(), e.values,
				"%s is a ledger_batch.kind enum and must be generated from internal/ledger/kinds.go", e.path)
		case isBatchSourceEnum(e.values):
			require.Equal(t, ledger.BatchSources(), e.values,
				"%s is a ledger_batch.source enum and must be generated from internal/ledger/kinds.go", e.path)
		}
	}
}

// isBatchKindEnum reports whether values carry a marker that belongs to no other vocabulary in the
// schema. 'zero_sum_credit' and 're_attribution' are unique to ledger_batch.kind; 'write_off' and
// 'import' are not, which is exactly why they are not markers.
func isBatchKindEnum(values []string) bool {
	for _, v := range values {
		if v == "zero_sum_credit" || v == "re_attribution" || v == "start_points" {
			return true
		}
	}

	return false
}

// isBatchSourceEnum reports whether values look like ledger_batch.source. No single value there is
// distinctive — 'web' and 'api' name half the enums anyone might write — so the marker is the PAIR
// 'discord' and 'parser', which nothing else in the schema puts together.
func isBatchSourceEnum(values []string) bool {
	var discord, parser bool

	for _, v := range values {
		switch v {
		case "discord":
			discord = true
		case "parser":
			parser = true
		}
	}

	return discord && parser
}

// collectEnums walks an arbitrary decoded JSON document and returns every string-valued `enum` array
// in it, keyed by path. Walking the whole document rather than the schema locations we expect is
// deliberate: an enum inlined on a parameter or in a response body is exactly as capable of drifting
// as one in components/schemas.
func collectEnums(node any, path string) []enumSite {
	var found []enumSite

	switch n := node.(type) {
	case map[string]any:
		if raw, ok := n["enum"].([]any); ok {
			if values, allStrings := stringSlice(raw); allStrings {
				found = append(found, enumSite{path: path + ".enum", values: values})
			}
		}

		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for _, k := range keys {
			found = append(found, collectEnums(n[k], path+"."+k)...)
		}
	case []any:
		for i, v := range n {
			found = append(found, collectEnums(v, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}

	return found
}

func stringSlice(raw []any) ([]string, bool) {
	out := make([]string, 0, len(raw))

	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}

		out = append(out, s)
	}

	return out, true
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err, "marshal fixture")

	return string(raw)
}
