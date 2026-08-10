package kinds_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The drift tests for canonical §5's third clause: "a test asserts the three copies agree".
//
// The three copies are the Go catalogue in internal/ledger/kinds, the CHECK expression in
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

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's ledger enum CHECKs have drifted from internal/ledger/kinds — run `make gen` "+
			"(and `make migration NAME=<snake_case>` if a value actually changed)")

	// Belt and braces: the expressions the CHECK must carry, named explicitly so a failure above
	// reads as "which enum" rather than as a whole-file diff.
	require.Contains(t, committed, kinds.CheckExpr("kind", kinds.BatchKinds()))
	require.Contains(t, committed, kinds.CheckExpr("source", kinds.BatchSources()))
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

			restored, err := kinds.RenderSchemaHCL(mutated)
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
	begin := "  // BEGIN GENERATED — ledger enum CHECKs, from internal/ledger/kinds. Run `make gen`."
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

			out, err := kinds.RenderSchemaHCL(tt.src)
			require.ErrorIs(t, err, kinds.ErrSchemaMarkersMissing)
			require.Empty(t, out, "a failed render must not return a half-written schema")
		})
	}
}

// TestLedgerKinds_MigrationCheckMatchesCatalogue reads the migration TEXT, and names the file that
// drifted. It is a better error message than the applied-schema test above and a WEAKER assertion:
// it cannot see a later migration that rebuilt the table without re-creating the constraint. Keep
// both; the one above is the one that is sound.
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
		{constraint: "ledger_batch_kind_enum", column: "kind", values: kinds.BatchKinds()},
		{constraint: "ledger_batch_source_enum", column: "source", values: kinds.BatchSources()},
	}

	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			t.Parallel()

			last, file := lastMigrationCheck(t, tt.constraint, tt.column)
			require.NotEmpty(t, file, "no migration declares CONSTRAINT %q — the enum reaches no database", tt.constraint)

			require.Equal(t, kinds.CheckExpr(tt.column, tt.values), last,
				"%s carries a %s CHECK that the Go catalogue no longer matches — the values in "+
					"internal/ledger/kinds need a migration, written with "+
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

// TestLedgerKinds_StrategyReversalKind_IsInCatalogue guards the one kind the strategy package names
// on its own.
//
// `strategy.KindReversal = kinds.KindReversal` is an ALIAS now, so the two agreeing is a compile-time
// fact and asserting it would be a tautology. What is still worth a test is the edit that undoes
// that: a future change replacing the alias with a literal — the state this package spent three
// review rounds getting out of — compiles cleanly and fails here instead.
//
// The membership check is the honest form of it: whatever strategy stamps onto a reversal batch has
// to be a value the generated CHECK will accept.
func TestLedgerKinds_StrategyReversalKind_IsInCatalogue(t *testing.T) {
	t.Parallel()

	require.Contains(t, kinds.BatchKinds(), strategy.KindReversal,
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
		{name: "ledger_batch.kind", values: kinds.BatchKinds()},
		{name: "ledger_batch.source", values: kinds.BatchSources()},
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

// TestLedgerKinds_RuntimeValidation_AcceptsExactlyTheCatalogue is the drift test for the RUNTIME
// copy: what Commit.validate accepts must be what the generated CHECK accepts, value for value.
//
// Before this, internal/ledger/commit.go carried its own `validSources` map. Two lists meant two
// failure modes: add a source to the catalogue and the database accepts a value the validator
// refuses as ErrInvalidRequest; add it only to the map and the validator waves through a value the
// database rejects mid-transaction. Deriving both from the catalogue removes the second list, and
// this asserts the derivation actually holds rather than trusting that it does.
func TestLedgerKinds_RuntimeValidation_AcceptsExactlyTheCatalogue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		values   []string
		accepts  func(string) bool
		rejected []string
	}{
		{
			name:    "ledger_batch.kind",
			values:  kinds.BatchKinds(),
			accepts: kinds.IsBatchKind,
			// Near-misses, not nonsense: a typo, a plural, a casing slip and the empty string are
			// what a planner actually produces when it gets this wrong.
			rejected: []string{"", "awrad", "awards", "Award", "zero_sum", "carrier_pigeon"},
		},
		{
			name:     "ledger_batch.source",
			values:   kinds.BatchSources(),
			accepts:  kinds.IsBatchSource,
			rejected: []string{"", "webs", "API", "slack", "carrier_pigeon"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, v := range tt.values {
				require.True(t, tt.accepts(v),
					"%q is in the catalogue and the generated CHECK, so the validator must accept it", v)
			}

			for _, v := range tt.rejected {
				require.False(t, tt.accepts(v),
					"%q is not in the catalogue, so the validator must refuse it before the transaction", v)
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

	require.Equal(t, "kind IN ('a', 'b_c')", kinds.CheckExpr("kind", []string{"a", "b_c"}))
	require.Equal(t, "source IN ('web')", kinds.CheckExpr("source", []string{"web"}))
}

// TestLedgerKinds_OpenAPIEnums_MatchCatalogue is the fourth copy: canonical §5 requires the OpenAPI
// enum to come from the same catalogue as the CHECK.
//
// TODAY IT HAS NO SUBJECT — no ledger endpoint exists at Phase 0, so openapi.json contains no batch
// kind or source enum and this passes having checked nothing. It is written now, in
// internal/core/openapi_contract_test.go's shape, so that it becomes load-bearing the instant the
// first ledger DTO lands rather than being remembered then.
//
// TWO INDEPENDENT TRIGGERS, because either alone has a hole:
//
//   - BY LOCATION: an enum on a `kind` or `source` property of a schema named for a ledger batch is
//     judged whatever its values are. This is what catches a DTO exposing only the kinds a strategy
//     implements today ('attendance', 'award', 'adjustment', 'decay', 'reversal') or a source list
//     shortened to 'web' and 'api' — subsets that carry no marker value at all.
//   - BY MARKER VALUE: an enum carrying a value unique to this catalogue is judged wherever it
//     appears, however the schema is named. This is what catches the same list on a property called
//     something else entirely.
//
// A subset match is deliberately NOT a trigger: 'import' and 'write_off' belong to other
// vocabularies — account.system_key carries 'write_off' — so judging on shared values alone would
// fail unrelated enums.
//
// The residual gap, stated rather than papered over: a DTO named for neither a batch nor carrying a
// marker (say `PointsHistoryEntry.kind`) with a marker-free subset is still invisible here. Closing
// that needs the enum GENERATED at the DTO instead of asserted about, which is the first ledger
// endpoint's work — .claude/rules/api-endpoints.md now says so.
func TestLedgerKinds_OpenAPIEnums_MatchCatalogue(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRoot(t), "openapi", "openapi.json")

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	var doc any
	require.NoError(t, json.Unmarshal(raw, &doc), "parse openapi.json")

	for _, v := range specViolations(doc) {
		require.Fail(t, "ledger enum drift in the committed spec", "%s: %s", v.path, v.detail)
	}
}

// TestLedgerKinds_OpenAPIWalker_DetectsDrift is the negative control, and it runs the SAME
// specViolations the test above runs — a control that reimplements the check proves nothing about
// the check.
//
// The first two cases are the false negatives a marker-only trigger had: a partial kind list and a
// shortened source list, neither carrying a marker. The last two are the cases that must stay quiet,
// because a test that fires on everything is as useless as one that fires on nothing.
func TestLedgerKinds_OpenAPIWalker_DetectsDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		spec       string
		wantDetail string
	}{
		{
			name: "kind enum missing a value",
			spec: `{"components":{"schemas":{"LedgerBatchDTO":{"properties":{
			  "kind":{"type":"string","enum":["attendance","award","adjustment","decay","reversal"]}}}}}}`,
			wantDetail: "ledger_batch.kind",
		},
		{
			name: "source enum shortened to the two obvious values",
			spec: `{"components":{"schemas":{"LedgerBatchDTO":{"properties":{
			  "source":{"type":"string","enum":["web","api"]}}}}}}`,
			wantDetail: "ledger_batch.source",
		},
		{
			name: "kind list under a schema named for something else, caught by its marker",
			spec: `{"components":{"schemas":{"Whatever":{"properties":{
			  "flavour":{"type":"string","enum":["zero_sum_credit","award"]}}}}}}`,
			wantDetail: "ledger_batch.kind",
		},
		{
			name: "batch schema exposes kind as a free string with no enum",
			spec: `{"components":{"schemas":{"LedgerBatchDTO":{"properties":{
			  "kind":{"type":"string"}}}}}}`,
			wantDetail: "no enum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var doc any
			require.NoError(t, json.Unmarshal([]byte(tt.spec), &doc))

			found := specViolations(doc)
			require.NotEmpty(t, found, "the walker missed drift it must catch")
			require.Contains(t, found[0].detail, tt.wantDetail)
		})
	}
}

// TestLedgerKinds_OpenAPIWalker_IgnoresUnrelatedEnums is the other half of the control: the walker
// must not fire on a vocabulary that merely shares a value with the catalogue, or the first person to
// add account.system_key to the spec gets a red test about the kinds.
func TestLedgerKinds_OpenAPIWalker_IgnoresUnrelatedEnums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
	}{
		{
			name: "account system keys, which share write_off",
			spec: `{"components":{"schemas":{"AccountDTO":{"properties":{
			  "system_key":{"type":"string","enum":["guild_bank","residue","write_off","import_opening"]}}}}}}`,
		},
		{
			name: "a bid state machine, which shares nothing",
			spec: `{"components":{"schemas":{"BidSessionDTO":{"properties":{
			  "state":{"type":"string","enum":["draft","open","extended","closing","resolved"]}}}}}}`,
		},
		{
			name: "a correct batch DTO",
			spec: `{"components":{"schemas":{"LedgerBatchDTO":{"properties":{
			  "kind":{"type":"string","enum":KINDS},
			  "source":{"type":"string","enum":SOURCES}}}}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := strings.ReplaceAll(tt.spec, "KINDS", mustJSON(t, kinds.BatchKinds()))
			spec = strings.ReplaceAll(spec, "SOURCES", mustJSON(t, kinds.BatchSources()))

			var doc any
			require.NoError(t, json.Unmarshal([]byte(spec), &doc))

			require.Empty(t, specViolations(doc), "the walker fired on an unrelated vocabulary")
		})
	}
}

// enumSite is one `enum` array in the spec, with the JSON path that reaches it — the path is what
// makes a failure actionable rather than "some enum somewhere is wrong".
type enumSite struct {
	path   string
	values []string
}

// specViolation is one disagreement between the spec and the catalogue.
type specViolation struct {
	path   string
	detail string
}

// specViolations returns every place the decoded spec disagrees with the catalogue.
//
// A function returning findings rather than one that calls t.Fatal, so the negative controls can
// assert it FIRES on drift using the same code the real test uses to assert it does not.
func specViolations(doc any) []specViolation {
	var found []specViolation

	// By location: a ledger-batch schema's kind/source property must carry the catalogue, and must
	// carry an enum at all — a free-form string is drift that no value comparison would ever see.
	for _, p := range batchProperties(doc, "$") {
		if p.values == nil {
			found = append(found, specViolation{
				path:   p.path,
				detail: "a ledger_batch." + p.column + " property with no enum — the vocabulary must be declared",
			})

			continue
		}

		if v, ok := mismatch(p.path, p.column, p.values); ok {
			found = append(found, v)
		}
	}

	// By marker value: an enum carrying a value unique to this catalogue, wherever it lives.
	for _, e := range collectEnums(doc, "$") {
		column := ""

		switch {
		case isBatchKindEnum(e.values):
			column = "kind"
		case isBatchSourceEnum(e.values):
			column = "source"
		default:
			continue
		}

		if v, ok := mismatch(e.path, column, e.values); ok && !alreadyReported(found, v.path) {
			found = append(found, v)
		}
	}

	return found
}

func mismatch(path, column string, values []string) (specViolation, bool) {
	want := kinds.BatchKinds()
	if column == "source" {
		want = kinds.BatchSources()
	}

	if slices.Equal(want, values) {
		return specViolation{}, false
	}

	return specViolation{
		path: path,
		detail: fmt.Sprintf("ledger_batch.%s must be generated from internal/ledger/kinds: want %v, got %v",
			column, want, values),
	}, true
}

func alreadyReported(found []specViolation, path string) bool {
	for _, v := range found {
		if v.path == path {
			return true
		}
	}

	return false
}

// batchProperty is a `kind` or `source` property on a schema named for a ledger batch. values is nil
// when the property declares no enum, which is itself a finding.
type batchProperty struct {
	path   string
	column string
	values []string
}

// batchProperties finds the kind/source properties of every ledger-batch-ish schema in the document.
//
// The schema-name match is normalised (lowercased, underscores dropped) so LedgerBatch, ledger_batch
// and LedgerBatchDTO all count. Matching on the name is what makes this independent of the VALUES —
// which is the entire point, since a subset carries no marker to match on.
func batchProperties(node any, path string) []batchProperty {
	var found []batchProperty

	switch n := node.(type) {
	case map[string]any:
		for _, k := range sortedKeys(n) {
			child := n[k]

			if isLedgerBatchName(k) {
				found = append(found, batchPropertiesOfSchema(child, path+"."+k)...)
			}

			found = append(found, batchProperties(child, path+"."+k)...)
		}
	case []any:
		for i, v := range n {
			found = append(found, batchProperties(v, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}

	return found
}

// batchPropertiesOfSchema reads the kind/source properties directly off one schema object.
func batchPropertiesOfSchema(schema any, path string) []batchProperty {
	obj, ok := schema.(map[string]any)
	if !ok {
		return nil
	}

	props, ok := obj["properties"].(map[string]any)
	if !ok {
		return nil
	}

	var found []batchProperty

	for _, column := range []string{"kind", "source"} {
		prop, present := props[column].(map[string]any)
		if !present {
			continue
		}

		p := batchProperty{path: path + ".properties." + column, column: column}

		if raw, hasEnum := prop["enum"].([]any); hasEnum {
			if values, allStrings := stringSlice(raw); allStrings {
				p.values = values
			}
		}

		found = append(found, p)
	}

	return found
}

// isLedgerBatchName reports whether a schema name denotes a ledger batch. Normalised so the casing
// and separator choices a future DTO makes cannot slip past.
func isLedgerBatchName(name string) bool {
	normalised := strings.ToLower(strings.ReplaceAll(name, "_", ""))

	return strings.Contains(normalised, "ledgerbatch")
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

		for _, k := range sortedKeys(n) {
			found = append(found, collectEnums(n[k], path+"."+k)...)
		}
	case []any:
		for i, v := range n {
			found = append(found, collectEnums(v, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}

	return found
}

// sortedKeys returns a map's keys in a stable order, so a walk reports the same first violation on
// every run rather than whichever one Go's map iteration happened to reach.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
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
