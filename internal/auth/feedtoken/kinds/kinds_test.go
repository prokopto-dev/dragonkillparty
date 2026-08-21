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

	"github.com/prokopto-dev/dragonkillparty/internal/auth/feedtoken/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// feed_token.kind.
//
// THE COPIES ARE THREE:
//
//	the Go catalogue          internal/auth/feedtoken/kinds, this package — the source
//	db/schema.hcl's CHECK     TestFeedTokenKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestFeedTokenKinds_MigrationCheckMatchesCatalogue, below (weaker; it
//	                          names the file that drifted)
//
// WHAT DRIFT COSTS HERE. kind is what makes a feed token SINGLE-PURPOSE. A feed credential travels in a URL, so the
// only thing bounding a leaked one is that it answers for exactly one feed; a value in the CHECK
// that no Go branch handles is a token whose purpose nothing enforces.

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

// TestFeedTokenKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly
// what the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the
// expression and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into the CHECK and not added to Go. It also proves the generated regions are
// INDEPENDENT: this render walks the whole file, and equality means it left every line outside its
// own markers alone.
func TestFeedTokenKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's feed_token enum CHECK has drifted from internal/auth/feedtoken/kinds — run `make gen` "+
			"(and `make migration NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.KindCheckExpr())
}

// TestFeedTokenKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
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
func TestFeedTokenKinds_SchemaDivergence_IsRestored(t *testing.T) {
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
			mutate:  editExpr(kinds.KindCheckExpr(), ", 'articles_rss'", ""),
			explain: "a feed kind the product can mint and the database would refuse",
		},
		{
			name:    "value misspelled in the CHECK",
			mutate:  editExpr(kinds.KindCheckExpr(), "'raids_ical'", "'raids_ics'"),
			explain: "the drift that makes the calendar feed unmintable",
		},
		{
			name:   "value invented in the CHECK",
			mutate: editExpr(kinds.KindCheckExpr(), "'raids_ical'", "'raids_ical', 'everything'"),
			explain: "a general-purpose feed kind hand-added to the CHECK — the exact thing this " +
				"table exists to prevent",
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

// TestFeedTokenKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries this catalogue's markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestFeedTokenKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — feed_token enum CHECK, from internal/auth/feedtoken/kinds. Run `make gen`."
	end := "  // END GENERATED — feed_token enum CHECK."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"feed_token\" {\n}\n"},
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

// TestFeedTokenKinds_MigrationCheckMatchesCatalogue reads the migration TEXT and names the file that
// drifted. It is a better error message than a schema comparison and a WEAKER assertion: it cannot
// see a later migration that rebuilt feed_token without re-creating the constraint.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a value is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order.
func TestFeedTokenKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	last, file := lastMigrationCheck(t, "feed_token_kind_enum", kinds.KindCheckExpr())
	require.NotEmpty(t, file,
		"no migration declares CONSTRAINT %q — the enum reaches no database", "feed_token_kind_enum")
	require.Equal(t, kinds.KindCheckExpr(), last,
		"%s carries a kind CHECK the Go catalogue no longer matches — run `make gen`, then "+
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

// TestFeedTokenKinds_Values_AreCanonicalEnumValues checks the vocabulary against canonical §5's rule
// for enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestFeedTokenKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	for _, values := range [][]string{kinds.Kinds()} {
		require.NotEmpty(t, values)

		seen := make(map[string]bool, len(values))

		for _, v := range values {
			require.Regexp(t, snakeCase, v, "%q is not lowercase snake_case", v)
			require.False(t, seen[v], "%q is declared twice", v)

			seen[v] = true
		}
	}
}

// TestFeedTokenKinds_Guards_AcceptOnlyTheCatalogue is the runtime half: the guard a writer calls
// instead of a literal, so a bad value is a Go error naming the legal ones rather than a constraint
// failure from inside a write transaction that has already done work.
func TestFeedTokenKinds_Guards_AcceptOnlyTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, v := range kinds.Kinds() {
		require.True(t, kinds.IsKind(v), "%q is in the catalogue and was rejected", v)
	}

	for _, v := range []string{"", "raids", "RAIDS_ICAL", "everything", "raids-ical"} {
		require.False(t, kinds.IsKind(v), "%q is not in the catalogue and was accepted", v)
	}
}

// TestFeedTokenKinds_SchemaEnumBlock_IsIndentedForTheTableBody guards the two mechanical properties
// the region rewrite depends on: the block carries its own markers as its first and last lines, and
// it is indented to sit inside a `table` body.
//
// TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues in test/repo/ takes lines[0] and
// lines[len-1] of exactly this string as the marker pair, so a block that gained a trailing blank
// line would make that test compare an empty string against a schema line and fail somewhere else
// entirely.
func TestFeedTokenKinds_SchemaEnumBlock_IsIndentedForTheTableBody(t *testing.T) {
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
