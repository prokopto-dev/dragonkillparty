package kinds_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/decay/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// decay_run.kind and decay_run.state.
//
// THE COPIES ARE FOUR, and the fourth is the one this table has that no other catalogue does:
//
//	the Go catalogue          internal/decay/kinds, this package — the source
//	db/schema.hcl's CHECK     TestDecayKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestDecayKinds_MigrationCheckMatchesCatalogue, below (weaker; it
//	                          names the file that drifted)
//	the COLUMN DEFAULT        TestDecayKinds_SchemaDefault_MatchesTheCatalogue, below. `default =
//	                          "planned"` is a column attribute rather than a check block, so
//	                          `make gen` does not write it and ENUM001 does not read it — it is the
//	                          one place a catalogue value is spelled by hand, and therefore the one
//	                          place that needs a test of its own.
//
// The applied-schema copy has no test here deliberately: this package is a LEAF (see the package
// comment) and cannot reach a migrated database without importing internal/store, which would put
// generated code inside `make gen`'s first step. TestMigrate_FreshInstall_MatchesFingerprint in
// test/migrations covers the applied form, and TestDecayRun_StateOutsideTheCatalogue_IsRejected
// drives the real CHECK against a real database.
//
// The OpenAPI copy has no subject yet: there is no decay endpoint (docs/design/02-api-design.md:348
// is Phase 3), so nothing in openapi/openapi.json carries this vocabulary.
//
// WHAT DRIFT COSTS HERE. A state in Go but not in the CHECK is a decay job that writes a legal state
// and has SQLite refuse it from inside the write transaction — after the run has been computed. The
// reverse — in the CHECK but not in Go — is worse and quieter: the job's own membership check
// refuses a state the database would have accepted, and a run that should have advanced to
// 'committed' sits at 'preview' with the period's unique row already taken, so nothing will ever
// retry it.

// repoRoot returns the directory holding go.mod, so these tests find db/ regardless of where
// `go test` was invoked from.
//
// Walked rather than filepath.Abs("../../.."), which produces the wrong answer silently the day this
// file moves — the same reasoning as the other catalogues' copies.
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

// TestDecayKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly what
// the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the
// expression and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into the CHECK and not added to Go. It is the same question
// `make verify-generated` asks of every other generated tree, asked here as an ordinary test so a
// laptop finds the drift before CI does.
//
// It also proves the four generated regions are INDEPENDENT: this render walks the whole file,
// including the ledger's, audit's and account's regions, and equality means it left every line
// outside its own markers alone.
func TestDecayKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's decay_run enum CHECKs have drifted from internal/decay/kinds — run "+
			"`make gen` (and `make migration NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.KindCheckExpr())
	require.Contains(t, committed, kinds.StateCheckExpr())
}

// TestDecayKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
// hand-edited CHECK must not survive a render.
//
// Without it, TestDecayKinds_CheckMatchesCatalogue is indistinguishable from a test that compares a
// file to itself — the classic tautology, and the one that matters most here because the gate's whole
// job is to notice a single edited word.
//
// The mutations target the RENDERED EXPRESSION rather than a bare value, for the reason
// internal/account/kinds' equivalent does: `'planned'` also appears in this table's column default,
// outside the generated region, and a bare-value replacement would edit whichever occurrence comes
// first — an edit a render correctly does not restore, so the control would fail for the wrong
// reason and prove nothing about the region.
func TestDecayKinds_SchemaDivergence_IsRestored(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	editExpr := func(expr, old, replacement string) func(string) string {
		return func(s string) string {
			return strings.Replace(s, expr, strings.Replace(expr, old, replacement, 1), 1)
		}
	}

	states, runKinds := kinds.StateCheckExpr(), kinds.KindCheckExpr()

	tests := []struct {
		name    string
		mutate  func(string) string
		explain string
	}{
		{
			name:    "cadence family dropped from the CHECK",
			mutate:  editExpr(runKinds, ", 'start_points'", ""),
			explain: "a family removed from the CHECK while its strategy still writes runs",
		},
		{
			name:    "cadence family invented in the CHECK",
			mutate:  editExpr(runKinds, "'cap'", "'cap', 'bonus'"),
			explain: "a fourth family hand-added to the CHECK, with no constant and no strategy",
		},
		{
			name:    "state dropped from the CHECK",
			mutate:  editExpr(states, "'skipped', ", ""),
			explain: "the state that records a deliberately skipped period, deleted from the CHECK",
		},
		{
			name:    "state misspelled in the CHECK",
			mutate:  editExpr(states, "'committed'", "'commited'"),
			explain: "the drift that makes every decay commit fail at the UPDATE, after the batch is posted",
		},
		{
			name:    "state invented in the CHECK",
			mutate:  editExpr(states, "'failed'", "'failed', 'retrying'"),
			explain: "a sixth state hand-added to the CHECK, with no constant and no job that writes it",
		},
		{
			name:    "the lifecycle reordered",
			mutate:  editExpr(states, "'planned', 'preview'", "'preview', 'planned'"),
			explain: "a reordering, which Atlas sees as a schema change and a migration nobody wanted",
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

// TestDecayKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries this catalogue's markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestDecayKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — decay_run enum CHECKs, from internal/decay/kinds. Run `make gen`."
	end := "  // END GENERATED — decay_run enum CHECKs."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"decay_run\" {\n}\n"},
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

// TestDecayKinds_MigrationCheckMatchesCatalogue reads the migration TEXT, and names the file that
// drifted. It is a WEAKER assertion than the applied-schema comparison: it cannot see a later
// migration that rebuilt decay_run without re-creating the constraint. What it buys is the error
// message — a file name — and it is what fails first when a value is added to Go and `make migration`
// is not run afterwards.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a value is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order, and that is the only one that has to agree with Go.
func TestDecayKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		constraint string
		column     string
		want       string
	}{
		{constraint: "decay_run_kind_enum", column: "kind", want: kinds.KindCheckExpr()},
		{constraint: "decay_run_state_enum", column: "state", want: kinds.StateCheckExpr()},
	}

	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			t.Parallel()

			last, file := lastMigrationCheck(t, tt.constraint, tt.column)
			require.NotEmpty(t, file,
				"no migration declares CONSTRAINT %q — the enum reaches no database", tt.constraint)

			require.Equal(t, tt.want, last,
				"%s carries a %s CHECK that the Go catalogue no longer matches — the values in "+
					"internal/decay/kinds need a migration, written with `make migration NAME=<snake_case>` "+
					"after `make gen`", file, tt.column)
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
	// lets the inner group be [^()]* rather than a paren-balancing parser. No `IS NULL OR` arm is
	// admitted — decay_run.state is NOT NULL with a default, and a migration that made it nullable is
	// a difference this test should report rather than match.
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

// TestDecayKinds_SchemaDefault_MatchesTheCatalogue ties the one hand-written copy of a catalogue
// value back to the catalogue.
//
// `default = "planned"` on decay_run.state is a column attribute, not a check block: `make gen` does
// not write it, ENUM001 does not read it, and TestDecayKinds_CheckMatchesCatalogue would still pass
// with a default of "pland" — which SQLite accepts as a DEFAULT and then rejects at the first INSERT
// that omits the column, because the CHECK refuses it. That is a table nobody can write a row into,
// discovered by the first decay job rather than by a test.
func TestDecayKinds_SchemaDefault_MatchesTheCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	require.Contains(t, committed, fmt.Sprintf("default = %q", kinds.DefaultState()),
		"db/schema.hcl no longer gives decay_run.state a default of %q", kinds.DefaultState())

	require.True(t, kinds.IsState(kinds.DefaultState()),
		"the default state is not in the catalogue, so the CHECK refuses every row that omits the column")

	require.Equal(t, kinds.States()[0], kinds.DefaultState(),
		"the default must be the first state of the lifecycle — a run is born planned")
}

// TestDecayKinds_Values_AreCanonicalEnumValues checks the vocabulary against canonical §5's rule for
// enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestDecayKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	vocabularies := map[string][]string{
		"kind":  kinds.Kinds(),
		"state": kinds.States(),
	}

	for column, values := range vocabularies {
		require.NotEmpty(t, values, "%s has no values", column)

		seen := make(map[string]bool, len(values))

		for _, v := range values {
			require.Regexp(t, snakeCase, v, "%s: %q is not lowercase snake_case (canonical §5)", column, v)
			require.False(t, seen[v], "%s: %q appears twice in the catalogue", column, v)

			seen[v] = true
		}
	}
}

// TestDecayKinds_ReturnFreshSlices is the guard on the shape .claude/rules/go-idioms.md asks for: a
// caller that mutates a returned slice must not be able to reach the catalogue every other caller
// sees.
//
// The suite runs -shuffle=on with t.Parallel() everywhere, so a shared backing array would surface as
// an intermittent failure in an unrelated package — the single hardest failure in this repository to
// attribute.
func TestDecayKinds_ReturnFreshSlices(t *testing.T) {
	t.Parallel()

	first := kinds.States()
	first[0] = "clobbered"

	require.Equal(t, kinds.StatePlanned, kinds.States()[0], "States handed out a slice backed by shared state")
	require.True(t, kinds.IsState(kinds.StatePlanned))
	require.False(t, kinds.IsState("clobbered"))

	families := kinds.Kinds()
	families[0] = "clobbered"

	require.Equal(t, kinds.KindDecay, kinds.Kinds()[0], "Kinds handed out a slice backed by shared state")
	require.True(t, kinds.IsKind(kinds.KindDecay))
	require.False(t, kinds.IsKind("clobbered"))
}

// TestDecayKinds_RuntimeValidation_AcceptsExactlyTheCatalogue is the drift test for the RUNTIME half:
// what the membership check accepts must be what the generated CHECK accepts, value for value.
//
// There is no decay_run writer yet — the table lands before the job that fills it (#192) — so this
// exists for the first one. That is the point of writing it now: the alternative is the first writer
// comparing against literals, which is how every defect the package comment lists came to exist.
func TestDecayKinds_RuntimeValidation_AcceptsExactlyTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, v := range kinds.States() {
		require.True(t, kinds.IsState(v), "%q is in the catalogue and the generated CHECK", v)
	}

	for _, v := range kinds.Kinds() {
		require.True(t, kinds.IsKind(v), "%q is in the catalogue and the generated CHECK", v)
	}

	// The near-misses that matter for kind are the OTHER catalogue's values and the plurals a caller
	// reaches for: a run whose kind is 'decayed' or 'caps' is refused by the CHECK from inside the
	// same transaction that computed the run.
	for _, v := range []string{"", "Decay", "decayed", "caps", "startpoints", "planned", "kind"} {
		require.False(t, kinds.IsKind(v), "%q is not a decay_run kind", v)
	}

	// Near-misses, not nonsense: a casing slip, a tense, a plural, the vocabulary of the neighbouring
	// column, the name of the column itself and the empty string are what a caller actually produces
	// when it gets this wrong. '' is listed deliberately — it is how a Go zero value arrives, and the
	// column is NOT NULL, so an unset state must not be mistaken for a valid one.
	for _, v := range []string{"", "Planned", "plan", "previewed", "commit", "state", "decay", "pending"} {
		require.False(t, kinds.IsState(v), "%q is not a decay_run state", v)
	}
}

// TestDecayKinds_CheckExpr_RendersTheCommittedExpression pins the rendering, because the generator
// and the drift tests are separate callers that have to agree with the committed schema byte for
// byte — including the ", " separator, which is what makes the generated expression identical to the
// one that shipped and therefore migration-free.
func TestDecayKinds_CheckExpr_RendersTheCommittedExpression(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"kind IN ('decay', 'cap', 'start_points')",
		kinds.KindCheckExpr(),
		"the rendered expression changed — every existing database carries the old text, so this is a "+
			"migration, not a formatting choice")

	require.Equal(t,
		"state IN ('planned', 'preview', 'committed', 'skipped', 'failed')",
		kinds.StateCheckExpr(),
		"the rendered expression changed — every existing database carries the old text, so this is a "+
			"migration, not a formatting choice")
}

// TestDecayKinds_SchemaEnumBlock_CarriesItsOwnMarkers pins the block's shape: the region a render
// writes has to begin and end with the marker lines schemaenum.Region matches WHOLE, or `make gen`
// produces a file whose regions nothing can find again.
func TestDecayKinds_SchemaEnumBlock_CarriesItsOwnMarkers(t *testing.T) {
	t.Parallel()

	lines := strings.Split(kinds.SchemaEnumBlock(), "\n")
	require.GreaterOrEqual(t, len(lines), 2, "a generated block is at least its two markers")

	require.Equal(t,
		"  // BEGIN GENERATED — decay_run enum CHECKs, from internal/decay/kinds. Run `make gen`.",
		lines[0])
	require.Equal(t, "  // END GENERATED — decay_run enum CHECKs.", lines[len(lines)-1])
	require.NotContains(t, kinds.SchemaEnumBlock(), "\n\n\n", "a blank run would grow the region per render")
}
