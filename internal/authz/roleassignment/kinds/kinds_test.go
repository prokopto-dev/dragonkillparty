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

	"github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// role_assignment.subject_kind, .scope_type and .granted_via.
//
// THE COPIES ARE THREE PER VOCABULARY:
//
//	the Go catalogue          internal/authz/roleassignment/kinds, this package — the source
//	db/schema.hcl's CHECK     TestRoleAssignmentKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestRoleAssignmentKinds_MigrationCheckMatchesCatalogue, below (weaker;
//	                          it names the file that drifted)
//
// plus the two column DEFAULTS, which are catalogue values written where the generator does not reach:
// TestRoleAssignmentKinds_SchemaDefaults_MatchTheCatalogue holds those.
//
// WHAT DRIFT COSTS HERE. These three columns decide who holds a role, how far it reaches and where it
// came from. A scope_type in Go and not in the CHECK is an assignment the role editor offers and the
// database refuses; the reverse is a scope the resolver has no constant for, so `Can(perm, scope)`
// compares against a string nobody can spell twice. granted_via is provenance, and a value missing
// from the CHECK fails the sync or the import that was writing it — mid-transaction, after work.

// repoRoot returns the directory holding go.mod, so these tests find db/ regardless of where
// `go test` was invoked from.
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

// TestRoleAssignmentKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly
// what the catalogue renders, in both directions and for all three vocabularies at once.
func TestRoleAssignmentKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's role_assignment enum CHECKs have drifted from "+
			"internal/authz/roleassignment/kinds — run `make gen` (and `make migration "+
			"NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.SubjectKindCheckExpr())
	require.Contains(t, committed, kinds.ScopeTypeCheckExpr())
	require.Contains(t, committed, kinds.GrantedViaCheckExpr())
}

// TestRoleAssignmentKinds_SchemaDivergence_IsRestored is the negative control: a hand-edited CHECK must
// not survive a render.
//
// The mutations rewrite ONE WHOLE rendered expression each, rather than a bare value: 'user' and
// 'service_account' appear in role.applies_to's region too, and 'global' appears in the hand-authored
// scope-shape CHECK beside this one — a bare-value replacement would edit the file's first occurrence,
// which is outside this region and correctly left alone by a render, so the control would fail for the
// wrong reason and prove nothing.
func TestRoleAssignmentKinds_SchemaDivergence_IsRestored(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	editExpr := func(expr, old, replacement string) func(string) string {
		return func(s string) string {
			return strings.Replace(s, expr, strings.Replace(expr, old, replacement, 1), 1)
		}
	}

	subjects, scopes, granted := kinds.SubjectKindCheckExpr(), kinds.ScopeTypeCheckExpr(), kinds.GrantedViaCheckExpr()

	tests := []struct {
		name    string
		mutate  func(string) string
		explain string
	}{
		{
			name:    "subject kind dropped from the CHECK",
			mutate:  editExpr(subjects, ", 'service_account'", ""),
			explain: "the kind every bot assignment carries, deleted from the CHECK",
		},
		{
			name:    "scope type invented in the CHECK",
			mutate:  editExpr(scopes, "'raid_group'", "'raid_group', 'character'"),
			explain: "a per-character scope hand-added to the CHECK, which the resolver cannot evaluate",
		},
		{
			name:    "scope type misspelled in the CHECK",
			mutate:  editExpr(scopes, "'global'", "'globl'"),
			explain: "the default scope misspelled, which makes every ordinary assignment fail",
		},
		{
			name:    "granted_via dropped from the CHECK",
			mutate:  editExpr(granted, ", 'discord_sync'", ""),
			explain: "the provenance the Discord sync writes, deleted from the CHECK",
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

// TestRoleAssignmentKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently
// doing nothing when db/schema.hcl no longer carries this catalogue's markers.
func TestRoleAssignmentKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — role_assignment enum CHECKs, from internal/authz/roleassignment/kinds. Run `make gen`."
	end := "  // END GENERATED — role_assignment enum CHECKs."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"role_assignment\" {\n}\n"},
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

// TestRoleAssignmentKinds_MigrationCheckMatchesCatalogue reads the migration TEXT and names the file
// that drifted — weaker than the schema comparison, better as an error message.
//
// THE LAST OCCURRENCE, NOT EVERY ONE: a shipped migration is frozen, so the migration that created the
// original CHECK keeps the original list forever and a new one rebuilds the table with the new list.
func TestRoleAssignmentKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		constraint string
		column     string
		want       string
	}{
		{constraint: "role_assignment_subject_kind_enum", column: "subject_kind", want: kinds.SubjectKindCheckExpr()},
		{constraint: "role_assignment_scope_type_enum", column: "scope_type", want: kinds.ScopeTypeCheckExpr()},
		{constraint: "role_assignment_granted_via_enum", column: "granted_via", want: kinds.GrantedViaCheckExpr()},
	}

	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			t.Parallel()

			last, file := lastMigrationCheck(t, tt.constraint, tt.column)
			require.NotEmpty(t, file,
				"no migration declares CONSTRAINT %q — the enum reaches no database", tt.constraint)

			require.Equal(t, tt.want, last,
				"%s carries a %s CHECK that the Go catalogue no longer matches — the values in "+
					"internal/authz/roleassignment/kinds need a migration, written with "+
					"`make migration NAME=<snake_case>` after `make gen`", file, tt.column)
		})
	}
}

// lastMigrationCheck returns the CHECK expression the final migration to declare constraint carries,
// and the file it came from. Lexical order is migration order; the numeric prefix is zero-padded
// precisely so those two orders are the same.
func lastMigrationCheck(t *testing.T, constraint, column string) (expr, file string) {
	t.Helper()

	dir := filepath.Join(repoRoot(t), "db", "migrations-sqlite")

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	require.NoError(t, err, "glob %s", dir)
	require.NotEmpty(t, files, "no migrations found in %s", dir)

	sort.Strings(files)

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

// TestRoleAssignmentKinds_SchemaDefaults_MatchTheCatalogue ties the two column defaults to catalogue
// values.
//
// A default is a catalogue value written in a place NOTHING regenerates — it is a column attribute, not
// a check block, so `make gen` does not rewrite it and ENUM001 does not read it. A default outside its
// CHECK makes every insert that omits the column fail; a default that is legal but wrong silently
// changes what an assignment written without one means, and for scope_type that is the difference
// between an instance-wide officer and a pool-scoped one.
func TestRoleAssignmentKinds_SchemaDefaults_MatchTheCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	require.Contains(t, kinds.ScopeTypes(), kinds.DefaultScopeType())
	require.Contains(t, kinds.GrantedVia(), kinds.DefaultGrantedVia())

	require.Contains(t, committed, fmt.Sprintf("default = %q", kinds.DefaultScopeType()),
		"db/schema.hcl does not give role_assignment.scope_type the default this catalogue names")
	require.Contains(t, committed, fmt.Sprintf("default = %q", kinds.DefaultGrantedVia()),
		"db/schema.hcl does not give role_assignment.granted_via the default this catalogue names")
}

// TestRoleAssignmentKinds_Values_AreCanonicalEnumValues checks all three vocabularies against canonical
// §5's rule for enum values: lowercase snake_case, unique, non-empty.
func TestRoleAssignmentKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	vocabularies := map[string][]string{
		"subject_kind": kinds.SubjectKinds(),
		"scope_type":   kinds.ScopeTypes(),
		"granted_via":  kinds.GrantedVia(),
	}

	for column, values := range vocabularies {
		require.NotEmpty(t, values, "%s has no values", column)

		seen := make(map[string]bool, len(values))

		for _, v := range values {
			require.Regexp(t, snakeCase, v, "%s: %q is not lowercase snake_case", column, v)
			require.False(t, seen[v], "%s: %q is declared twice", column, v)

			seen[v] = true
		}
	}
}

// TestRoleAssignmentKinds_Guards_AcceptOnlyTheirOwnVocabulary is the runtime half, and it checks the
// three guards against EACH OTHER as well as against nonsense.
//
// That cross-check is the point: this package governs three columns, so the mistake available here and
// nowhere else is validating one column's value against another's list. 'user' is a legal subject_kind
// and not a legal scope_type, and a guard that accepted it either way would compile, pass a naive test,
// and let an assignment through that the database then refuses.
func TestRoleAssignmentKinds_Guards_AcceptOnlyTheirOwnVocabulary(t *testing.T) {
	t.Parallel()

	guards := map[string]struct {
		values []string
		is     func(string) bool
	}{
		"subject_kind": {values: kinds.SubjectKinds(), is: kinds.IsSubjectKind},
		"scope_type":   {values: kinds.ScopeTypes(), is: kinds.IsScopeType},
		"granted_via":  {values: kinds.GrantedVia(), is: kinds.IsGrantedVia},
	}

	own := map[string]map[string]bool{}
	for column, g := range guards {
		own[column] = make(map[string]bool, len(g.values))
		for _, v := range g.values {
			own[column][v] = true
		}
	}

	for column, g := range guards {
		for _, v := range g.values {
			require.True(t, g.is(v), "%s: %q is in the catalogue and was rejected", column, v)
		}

		// Every OTHER column's values, plus outright nonsense.
		for otherColumn, other := range guards {
			if otherColumn == column {
				continue
			}

			for _, v := range other.values {
				if own[column][v] {
					continue // 'user' is legitimately both a subject_kind and a role.applies_to value.
				}

				require.False(t, g.is(v),
					"%s accepted %q, which belongs to %s", column, v, otherColumn)
			}
		}

		for _, v := range []string{"", "Global", "raid group", "sync"} {
			require.False(t, g.is(v), "%s accepted %q", column, v)
		}
	}
}

// TestRoleAssignmentKinds_SchemaEnumBlock_IsIndentedForTheTableBody guards the two mechanical
// properties the region rewrite depends on: the block carries its own markers as its first and last
// lines, and every line is indented to sit inside a `table` body.
//
// TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues in test/repo/ takes lines[0] and
// lines[len-1] of exactly this string as the marker pair, so a block that gained a trailing blank line
// would make that test compare an empty string against a schema line and fail somewhere else entirely.
func TestRoleAssignmentKinds_SchemaEnumBlock_IsIndentedForTheTableBody(t *testing.T) {
	t.Parallel()

	lines := strings.Split(kinds.SchemaEnumBlock(), "\n")
	require.GreaterOrEqual(t, len(lines), 2)

	require.Contains(t, lines[0], "BEGIN GENERATED")
	require.Contains(t, lines[len(lines)-1], "END GENERATED")

	for _, line := range lines {
		if line == "" {
			continue // the blank separators between the three check blocks
		}

		require.True(t, strings.HasPrefix(line, "  "),
			"every line sits inside a table body and is indented two spaces: %q", line)
	}
}
