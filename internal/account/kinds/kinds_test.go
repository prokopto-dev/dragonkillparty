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

	"github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// account.kind and account.system_key.
//
// THE COPIES ARE FIVE for system_key, which is one more than any other catalogue has and is why #51
// called it the one with real blast radius:
//
//	the Go catalogue          internal/account/kinds, this package — the source
//	db/schema.hcl's CHECK     TestAccountKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestAccountKinds_MigrationCheckMatchesCatalogue, below (weaker; it
//	                          names the file that drifted)
//	the applied schema        TestAccountKinds_AppliedSchema_MatchesCatalogue, in internal/ledger —
//	                          that package has the migrated-database harness, and this one is
//	                          deliberately a leaf (see the package comment)
//	the SEEDED ROWS           TestSystemAccountIDs_CoverTheCatalogue and
//	                          TestAccountKinds_SeededSystemAccounts_MatchTheCatalogue, both in
//	                          internal/ledger. A system key with no seeded row is a CHECK the
//	                          database accepts and a lookup that returns not-found on every fresh
//	                          install, which no schema comparison can see.
//
// The re-exported constants in internal/strategy and internal/ledger are not a sixth copy: they are
// `const X = kinds.X`, so a divergence is a compile error rather than something to test.
//
// The OpenAPI copy has no subject: there is no account endpoint at Phase 0, so nothing in
// openapi/openapi.json carries this vocabulary — internal/ledger/kinds' own spec walker even uses an
// AccountDTO carrying these four values as its example of an enum it must NOT judge. Deriving the
// enum AT the DTO rather than asserting about it is #42's work.
//
// WHAT DRIFT COSTS HERE. A fifth system key in Go but not in the CHECK is a planner routing a split's
// remainder to an account SQLite refuses at the INSERT — inside the commit transaction, after the
// batch and its entries have been written, so the raid night's award rolls back naming a constraint.
// The reverse — in the CHECK but not in Go — refuses nothing early and surfaces as a NULL
// system-account lookup. The degenerate routes of the largest-remainder allocator (residue,
// write_off, guild_bank) are what keep the Conserved invariant verifiable, so a split that cannot
// reach its system account has nowhere to put the remainder.

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

// TestAccountKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly what
// the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the
// expressions and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into a CHECK and not added to Go. It is the same question
// `make verify-generated` asks of every other generated tree, asked here as an ordinary test so a
// laptop finds the drift before CI does.
//
// It also proves the three generated regions are INDEPENDENT: this render walks the whole file,
// including the ledger's and audit's regions, and equality means it left every line outside its own
// markers alone.
func TestAccountKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's account enum CHECKs have drifted from internal/account/kinds — run "+
			"`make gen` (and `make migration NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.KindCheckExpr())
	require.Contains(t, committed, kinds.SystemKeyCheckExpr())
}

// TestAccountKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
// hand-edited CHECK must not survive a render.
//
// Without it, TestAccountKinds_CheckMatchesCatalogue is indistinguishable from a test that compares a
// file to itself — the classic tautology, and the one that matters most here because the gate's whole
// job is to notice a single edited word.
//
// The mutations target the RENDERED EXPRESSIONS rather than a bare value, because these values recur
// across db/schema.hcl outside this region — `'person'` and `'system'` in the paired
// account_person_shape and account_system_shape CHECKs, `'write_off'` in ledger_batch's kind CHECK —
// and a bare-value replacement edits the file's FIRST occurrence, whichever that turns out to be. An
// edit outside the region is one a render correctly does not restore, so the control would fail for
// the wrong reason and prove nothing about the region.
func TestAccountKinds_SchemaDivergence_IsRestored(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	// Each mutation rewrites ONE WHOLE rendered expression: it finds the committed expression — which
	// occurs exactly once, inside this catalogue's region — and puts an edited copy of it back.
	// Mutating the value alone would edit the FIRST occurrence in the file, which for every value here
	// is a hand-authored comment above the column, outside the region and correctly left alone by a
	// render.
	editExpr := func(expr, old, replacement string) func(string) string {
		return func(s string) string {
			return strings.Replace(s, expr, strings.Replace(expr, old, replacement, 1), 1)
		}
	}

	systemKeys, accountKinds := kinds.SystemKeyCheckExpr(), kinds.KindCheckExpr()

	tests := []struct {
		name    string
		mutate  func(string) string
		explain string
	}{
		{
			name:    "system key dropped from the CHECK",
			mutate:  editExpr(systemKeys, "'write_off', ", ""),
			explain: "the rot route deleted from the CHECK but still reachable from a planner",
		},
		{
			name:    "system key misspelled in the CHECK",
			mutate:  editExpr(systemKeys, "'guild_bank'", "'guildbank'"),
			explain: "the drift that makes every solo-kill award fail at the INSERT",
		},
		{
			name:    "system key invented in the CHECK",
			mutate:  editExpr(systemKeys, "'residue'", "'residue', 'guild_tax'"),
			explain: "a fifth system key hand-added to the CHECK, with no constant and no seeded row",
		},
		{
			name:    "the IS NULL OR prefix removed",
			mutate:  editExpr(systemKeys, "system_key IS NULL OR ", ""),
			explain: "the nullable form reduced to a bare IN list, which says person accounts are illegal",
		},
		{
			name:    "account kind dropped from the CHECK",
			mutate:  editExpr(accountKinds, ", 'system'", ""),
			explain: "the kind every system account carries, deleted from the CHECK",
		},
		{
			name:    "account kind invented in the CHECK",
			mutate:  editExpr(accountKinds, "'system'", "'system', 'guild'"),
			explain: "a third account kind hand-added to the CHECK and never added to Go",
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

// TestAccountKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries this catalogue's markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestAccountKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — account enum CHECKs, from internal/account/kinds. Run `make gen`."
	end := "  // END GENERATED — account enum CHECKs."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"account\" {\n}\n"},
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

// TestAccountKinds_MigrationCheckMatchesCatalogue reads the migration TEXT, and names the file that
// drifted. It is a better error message than the applied-schema test in internal/ledger and a WEAKER
// assertion: it cannot see a later migration that rebuilt account without re-creating the constraint.
// Keep both; that one is the one that is sound.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a value is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order, and that is the only one that has to agree with Go.
func TestAccountKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		constraint string
		column     string
		want       string
	}{
		{constraint: "account_kind_enum", column: "kind", want: kinds.KindCheckExpr()},
		{constraint: "account_system_key_enum", column: "system_key", want: kinds.SystemKeyCheckExpr()},
	}

	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			t.Parallel()

			last, file := lastMigrationCheck(t, tt.constraint, tt.column)
			require.NotEmpty(t, file,
				"no migration declares CONSTRAINT %q — the enum reaches no database", tt.constraint)

			require.Equal(t, tt.want, last,
				"%s carries a %s CHECK that the Go catalogue no longer matches — the values in "+
					"internal/account/kinds need a migration, written with `make migration NAME=<snake_case>` "+
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

	// The expression is `<column> IN ('a', 'b')` or, for a nullable column, that with an
	// `<column> IS NULL OR ` prefix: one parenthesised list, no nesting, which is what lets the inner
	// group be [^()]* rather than a paren-balancing parser. The prefix is OPTIONAL in the pattern
	// rather than assumed, so a migration that dropped it is captured and reported as a difference
	// instead of vanishing into a no-match reported as "no migration declares this constraint".
	pattern := regexp.MustCompile(
		fmt.Sprintf(`CONSTRAINT "%s" CHECK \(((?:%s IS NULL OR )?%s IN \([^()]*\))\)`,
			regexp.QuoteMeta(constraint), regexp.QuoteMeta(column), regexp.QuoteMeta(column)))

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

// TestAccountKinds_Values_AreCanonicalEnumValues checks both vocabularies against canonical §5's rule
// for enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestAccountKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	vocabularies := map[string][]string{
		"kind":       kinds.Kinds(),
		"system_key": kinds.SystemKeys(),
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

// TestAccountKinds_ReturnFreshSlices is the guard on the shape .claude/rules/go-idioms.md asks for: a
// caller that mutates a returned slice must not be able to reach the catalogue every other caller
// sees.
//
// The suite runs -shuffle=on with t.Parallel() everywhere, so a shared backing array would surface as
// an intermittent failure in an unrelated package — the single hardest failure in this repository to
// attribute.
func TestAccountKinds_ReturnFreshSlices(t *testing.T) {
	t.Parallel()

	first := kinds.Kinds()
	first[0] = "clobbered"

	require.Equal(t, kinds.KindPerson, kinds.Kinds()[0], "Kinds handed out a slice backed by shared state")
	require.True(t, kinds.IsKind(kinds.KindPerson))
	require.False(t, kinds.IsKind("clobbered"))

	keys := kinds.SystemKeys()
	keys[0] = "clobbered"

	require.Equal(t, kinds.SystemKeyGuildBank, kinds.SystemKeys()[0],
		"SystemKeys handed out a slice backed by shared state")
	require.True(t, kinds.IsSystemKey(kinds.SystemKeyGuildBank))
	require.False(t, kinds.IsSystemKey("clobbered"))
}

// TestAccountKinds_RuntimeValidation_AcceptsExactlyTheCatalogue is the drift test for the RUNTIME
// half: what the membership checks accept must be what the generated CHECKs accept, value for value.
//
// There is no account writer at Phase 0 — the four system accounts arrive as seed rows — so these
// exist for the first one. That is the point of writing them now: the alternative is the first writer
// comparing against literals, which is how every defect #51 and #53 describe came to exist.
func TestAccountKinds_RuntimeValidation_AcceptsExactlyTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, v := range kinds.Kinds() {
		require.True(t, kinds.IsKind(v), "%q is in the catalogue and the generated CHECK", v)
	}

	for _, v := range kinds.SystemKeys() {
		require.True(t, kinds.IsSystemKey(v), "%q is in the catalogue and the generated CHECK", v)
	}

	// Near-misses, not nonsense: a casing slip, a plural, the other column's vocabulary, the name of
	// the column itself and the empty string are what a caller actually produces when it gets this
	// wrong.
	for _, v := range []string{"", "Person", "persons", "user", "kind", "guild_bank"} {
		require.False(t, kinds.IsKind(v), "%q is not an account kind", v)
	}

	// '' is listed deliberately: the empty string is how a Go zero value arrives, and it must not be
	// mistaken for "this account has no system key" — that is NULL, a property of the row, which
	// account_system_shape enforces.
	for _, v := range []string{"", "guildbank", "Guild_Bank", "bank", "system", "system_key", "guild_tax"} {
		require.False(t, kinds.IsSystemKey(v), "%q is not a system key", v)
	}
}

// TestAccountKinds_CheckExpr_RendersTheCommittedExpressions pins the rendering, because the generator
// and the drift tests are separate callers that have to agree with the committed schema byte for
// byte — including the ", " separator and the `IS NULL OR` prefix, which are what make the generated
// expressions identical to the ones that shipped and therefore migration-free.
func TestAccountKinds_CheckExpr_RendersTheCommittedExpressions(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"kind IN ('person', 'system')",
		kinds.KindCheckExpr(),
		"the rendered expression changed — every existing database carries the old text, so this is a "+
			"migration, not a formatting choice")

	require.Equal(t,
		"system_key IS NULL OR system_key IN ('guild_bank', 'residue', 'write_off', 'import_opening')",
		kinds.SystemKeyCheckExpr(),
		"the rendered expression changed — every existing database carries the old text, so this is a "+
			"migration, not a formatting choice")
}
