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

	"github.com/prokopto-dev/dragonkillparty/internal/auth/serviceaccount/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// service_account.state.
//
// THE COPIES ARE THREE:
//
//	the Go catalogue          internal/auth/serviceaccount/kinds, this package — the source
//	db/schema.hcl's CHECK     TestServiceAccountKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestServiceAccountKinds_MigrationCheckMatchesCatalogue, below (weaker; it
//	                          names the file that drifted)
//
// WHAT DRIFT COSTS HERE. state is what the middleware reads before it believes a bot's token: disabling a service
// account is meant to turn off every token it owns at once, without revoking them one at a time. A
// value in Go and not in the CHECK fails at the write that was disabling the bot; the reverse is a
// state no Go branch handles, which fails OPEN.

// repoRoot returns the directory holding go.mod, so these tests find db/ regardless of where
// `go test` was invoked from. Walked rather than a relative path, which produces the wrong answer
// silently the day this file moves.
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

// TestServiceAccountKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly
// what the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the
// expression and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into the CHECK and not added to Go. It also proves the generated regions are
// INDEPENDENT: this render walks the whole file, and equality means it left every line outside its
// own markers alone.
func TestServiceAccountKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's service_account enum CHECK has drifted from internal/auth/serviceaccount/kinds — run `make gen` "+
			"(and `make migration NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.StateCheckExpr())
}

// TestServiceAccountKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
// hand-edited CHECK must not survive a render.
//
// Without it, the flagship is indistinguishable from a test that compares a file to itself — the
// classic tautology, and the one that matters most here because the gate's whole job is to notice a
// single edited word.
//
// The mutations target the RENDERED EXPRESSION rather than a bare value, because these values recur
// elsewhere in db/schema.hcl and a bare-value replacement edits the file's FIRST occurrence,
// whichever that turns out to be. An edit outside the region is one a render correctly does not
// restore, so the control would fail for the wrong reason and prove nothing about the region.
func TestServiceAccountKinds_SchemaDivergence_IsRestored(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	editExpr := func(expr, old, replacement string) func(string) string {
		return func(s string) string {
			return strings.Replace(s, expr, strings.Replace(expr, old, replacement, 1), 1)
		}
	}

	tests := []struct {
		name    string
		mutate  func(string) string
		explain string
	}{
		{
			name:    "value dropped from the CHECK",
			mutate:  editExpr(kinds.StateCheckExpr(), ", 'disabled'", ""),
			explain: "the state that turns a bot off, deleted from the CHECK",
		},
		{
			name:    "value misspelled in the CHECK",
			mutate:  editExpr(kinds.StateCheckExpr(), "'active'", "'activ'"),
			explain: "the drift that makes every service account unwritable",
		},
		{
			name:    "value invented in the CHECK",
			mutate:  editExpr(kinds.StateCheckExpr(), "'active'", "'active', 'orphaned'"),
			explain: "the derived orphaned flag hand-added as a state, with no constant behind it",
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

// TestServiceAccountKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries this catalogue's markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestServiceAccountKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — service_account enum CHECK, from internal/auth/serviceaccount/kinds. Run `make gen`."
	end := "  // END GENERATED — service_account enum CHECK."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"service_account\" {\n}\n"},
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

// TestServiceAccountKinds_MigrationCheckMatchesCatalogue reads the migration TEXT and names the file that
// drifted. It is a better error message than a schema comparison and a WEAKER assertion: it cannot
// see a later migration that rebuilt service_account without re-creating the constraint.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a value is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order.
func TestServiceAccountKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	last, file := lastMigrationCheck(t, "service_account_state_enum", kinds.StateCheckExpr())
	require.NotEmpty(t, file,
		"no migration declares CONSTRAINT %q — the enum reaches no database",
		"service_account_state_enum")
	require.Equal(t, kinds.StateCheckExpr(), last,
		"%s carries a state CHECK the Go catalogue no longer matches — run `make gen`, then "+
			"`make migration NAME=<snake_case>`", file)
}

// lastMigrationCheck returns the CHECK expression the final migration to declare constraint carries,
// and the file it came from. Files are read in lexical order, which is migration order — the numeric
// prefix is zero-padded precisely so those two orders are the same.
func lastMigrationCheck(t *testing.T, constraint, body string) (expr, file string) {
	t.Helper()

	dir := filepath.Join(repoRoot(t), "db", "migrations-sqlite")

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	require.NoError(t, err, "glob %s", dir)
	require.NotEmpty(t, files, "no migrations found in %s", dir)

	sort.Strings(files)

	pattern := regexp.MustCompile(
		fmt.Sprintf(`CONSTRAINT "%s" CHECK \((%s)\)`,
			regexp.QuoteMeta(constraint), regexp.QuoteMeta(body)))

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

// TestServiceAccountKinds_Values_AreCanonicalEnumValues checks the vocabulary against canonical §5's rule
// for enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestServiceAccountKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	for _, values := range [][]string{kinds.States()} {
		require.NotEmpty(t, values)

		seen := make(map[string]bool, len(values))

		for _, v := range values {
			require.Regexp(t, snakeCase, v, "%q is not lowercase snake_case", v)
			require.False(t, seen[v], "%q is declared twice", v)

			seen[v] = true
		}
	}
}

// TestServiceAccountKinds_Guards_AcceptOnlyTheCatalogue is the runtime half: the guard a writer calls
// instead of a literal, so a bad value is a Go error naming the legal ones rather than a constraint
// failure from inside a write transaction that has already done work.
func TestServiceAccountKinds_Guards_AcceptOnlyTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, v := range kinds.States() {
		require.True(t, kinds.IsState(v), "%q is in the catalogue and was rejected", v)
	}

	for _, v := range []string{"", "Active", "orphaned", "suspended", "pending"} {
		require.False(t, kinds.IsState(v), "%q is not in the catalogue and was accepted", v)
	}
}

// TestServiceAccountKinds_SchemaEnumBlock_IsIndentedForTheTableBody guards the two mechanical properties
// the region rewrite depends on: the block carries its own markers as its first and last lines, and
// it is indented to sit inside a `table` body.
//
// TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues in test/repo/ takes lines[0] and
// lines[len-1] of exactly this string as the marker pair, so a block that gained a trailing blank
// line would make that test compare an empty string against a schema line and fail somewhere else
// entirely.
func TestServiceAccountKinds_SchemaEnumBlock_IsIndentedForTheTableBody(t *testing.T) {
	t.Parallel()

	lines := strings.Split(kinds.SchemaEnumBlock(), "\n")
	require.GreaterOrEqual(t, len(lines), 2)

	require.Contains(t, lines[0], "BEGIN GENERATED")
	require.Contains(t, lines[len(lines)-1], "END GENERATED")

	for _, line := range lines {
		require.True(t, strings.HasPrefix(line, "  "),
			"every line sits inside a table body and is indented two spaces: %q", line)
	}
}

// TestServiceAccountKinds_SchemaDefault_MatchesTheCatalogue ties the column default to a catalogue value.
//
// The default is a catalogue value written in a place NOTHING regenerates: it is a column attribute,
// not a check block, so `make gen` does not rewrite it and ENUM001 does not read it. A default
// outside the CHECK is a table whose every insert fails; a default that is a legal value but the
// wrong one is worse, because it silently changes what a row created without an explicit state
// means.
func TestServiceAccountKinds_SchemaDefault_MatchesTheCatalogue(t *testing.T) {
	t.Parallel()

	require.Contains(t, kinds.States(), kinds.DefaultState(),
		"the default is not one of the legal values")

	require.Contains(t, readSchemaHCL(t), fmt.Sprintf("default = %q", kinds.DefaultState()),
		"db/schema.hcl does not give service_account.state the default this catalogue names (%q)",
		kinds.DefaultState())
}
