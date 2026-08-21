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

	"github.com/prokopto-dev/dragonkillparty/internal/auth/useridentity/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// user_identity.provider and user_identity.password_algo.
//
// THE COPIES ARE THREE:
//
//	the Go catalogue          internal/auth/useridentity/kinds, this package — the source
//	db/schema.hcl's CHECK     TestUserIdentityKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestUserIdentityKinds_MigrationCheckMatchesCatalogue, below (weaker; it
//	                          names the file that drifted)
//
// WHAT DRIFT COSTS HERE. provider is half of the unique index that makes account takeover by handle reuse impossible
// (§3.5: identity is the provider id, never the username), so a value that disagrees between Go and
// the CHECK is a link the product writes and the database refuses — mid-OAuth-callback.
// password_algo has exactly ONE legal value and that IS the point: a second one appearing here is
// the first step of importing a legacy EQdkp hash, which AGENTS.md forbids outright.

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

// TestUserIdentityKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly
// what the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the
// expression and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into the CHECK and not added to Go. It also proves the generated regions are
// INDEPENDENT: this render walks the whole file, and equality means it left every line outside its
// own markers alone.
func TestUserIdentityKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's user_identity enum CHECK has drifted from internal/auth/useridentity/kinds — run `make gen` "+
			"(and `make migration NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.ProviderCheckExpr())
	require.Contains(t, committed, kinds.PasswordAlgoCheckExpr())
}

// TestUserIdentityKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
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
func TestUserIdentityKinds_SchemaDivergence_IsRestored(t *testing.T) {
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
			name:    "provider dropped from the CHECK",
			mutate:  editExpr(kinds.ProviderCheckExpr(), ", 'oidc'", ""),
			explain: "a provider the OAuth code can name and the database would refuse",
		},
		{
			name:    "provider misspelled in the CHECK",
			mutate:  editExpr(kinds.ProviderCheckExpr(), "'local'", "'password'"),
			explain: "the drift that makes the first-run owner's identity unwritable",
		},
		{
			name:   "a second password algorithm",
			mutate: editExpr(kinds.PasswordAlgoCheckExpr(), "'argon2id'", "'argon2id', 'bcrypt'"),
			explain: "a legacy verifier hand-added to the CHECK — the first step of importing an " +
				"EQdkp hash, which AGENTS.md forbids outright",
		},
		{
			name:   "the nullable prefix removed",
			mutate: editExpr(kinds.PasswordAlgoCheckExpr(), "password_algo IS NULL OR ", ""),
			explain: "the IS NULL arm deleted, which makes every OAuth identity — every row with no " +
				"password at all — depend on SQLite admitting a NULL CHECK",
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

// TestUserIdentityKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries this catalogue's markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestUserIdentityKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — user_identity enum CHECKs, from internal/auth/useridentity/kinds. Run `make gen`."
	end := "  // END GENERATED — user_identity enum CHECKs."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"user_identity\" {\n}\n"},
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

// TestUserIdentityKinds_MigrationCheckMatchesCatalogue reads the migration TEXT and names the file that
// drifted. It is a better error message than a schema comparison and a WEAKER assertion: it cannot
// see a later migration that rebuilt user_identity without re-creating the constraint.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a value is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order.
func TestUserIdentityKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		constraint string
		want       string
	}{
		{constraint: "user_identity_provider_enum", want: kinds.ProviderCheckExpr()},
		{constraint: "user_identity_password_algo_enum", want: kinds.PasswordAlgoCheckExpr()},
	} {
		last, file := lastMigrationCheck(t, tt.constraint, tt.want)
		require.NotEmptyf(t, file,
			"no migration declares CONSTRAINT %q — the enum reaches no database", tt.constraint)
		require.Equalf(t, tt.want, last,
			"%s carries a %s CHECK the Go catalogue no longer matches — run `make gen`, then "+
				"`make migration NAME=<snake_case>`", file, tt.constraint)
	}
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

// TestUserIdentityKinds_Values_AreCanonicalEnumValues checks the vocabulary against canonical §5's rule
// for enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestUserIdentityKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	for _, values := range [][]string{kinds.Providers(), kinds.PasswordAlgos()} {
		require.NotEmpty(t, values)

		seen := make(map[string]bool, len(values))

		for _, v := range values {
			require.Regexp(t, snakeCase, v, "%q is not lowercase snake_case", v)
			require.False(t, seen[v], "%q is declared twice", v)

			seen[v] = true
		}
	}
}

// TestUserIdentityKinds_Guards_AcceptOnlyTheCatalogue is the runtime half: the guard a writer calls
// instead of a literal, so a bad value is a Go error naming the legal ones rather than a constraint
// failure from inside a write transaction that has already done work.
func TestUserIdentityKinds_Guards_AcceptOnlyTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, v := range kinds.Providers() {
		require.True(t, kinds.IsProvider(v), "%q is in the catalogue and was rejected", v)
	}

	for _, v := range []string{"", "Local", "saml", "google", "discord2"} {
		require.False(t, kinds.IsProvider(v), "%q is not in the catalogue and was accepted", v)
	}

	require.True(t, kinds.IsPasswordAlgo(kinds.AlgoArgon2id))

	// NULL is not expressible here and must not be: the column's absence is a property of the row,
	// and a "" sentinel meaning NULL would be a second spelling of "no password" that some caller
	// eventually compares against the wrong one.
	for _, v := range []string{"", "bcrypt", "argon2i", "ARGON2ID", "phpass"} {
		require.False(t, kinds.IsPasswordAlgo(v), "%q is not in the catalogue and was accepted", v)
	}
}

// TestUserIdentityKinds_SchemaEnumBlock_IsIndentedForTheTableBody guards the two mechanical properties
// the region rewrite depends on: the block carries its own markers as its first and last lines, and
// it is indented to sit inside a `table` body.
//
// TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues in test/repo/ takes lines[0] and
// lines[len-1] of exactly this string as the marker pair, so a block that gained a trailing blank
// line would make that test compare an empty string against a schema line and fail somewhere else
// entirely.
func TestUserIdentityKinds_SchemaEnumBlock_IsIndentedForTheTableBody(t *testing.T) {
	t.Parallel()

	lines := strings.Split(kinds.SchemaEnumBlock(), "\n")
	require.GreaterOrEqual(t, len(lines), 2)

	require.Contains(t, lines[0], "BEGIN GENERATED")
	require.Contains(t, lines[len(lines)-1], "END GENERATED")

	for _, line := range lines {
		if line == "" {
			continue // the blank separator between the two check blocks
		}

		require.True(t, strings.HasPrefix(line, "  "),
			"every line sits inside a table body and is indented two spaces: %q", line)
	}
}
