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

	"github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
)

// The drift tests for canonical §5's third clause — "a test asserts the copies agree" — applied to
// audit_log.actor_kind.
//
// THE COPIES ARE FOUR, and each has a test somewhere:
//
//	the Go catalogue          internal/audit/kinds, this package — the source
//	db/schema.hcl's CHECK     TestAuditKinds_CheckMatchesCatalogue, below
//	the migration's CHECK     TestAuditKinds_MigrationCheckMatchesCatalogue, below (weaker; it names
//	                          the file that drifted)
//	the applied schema        TestAuditKinds_AppliedSchema_MatchesCatalogue, in internal/ledger —
//	                          that package has the migrated-database harness, and this one is
//	                          deliberately a leaf (see the package comment)
//	the runtime validator     TestAuditKinds_RuntimeValidation_AcceptsExactlyTheCatalogue, below
//
// The OpenAPI copy has no subject: there is no audit endpoint at Phase 0, so nothing in
// openapi/openapi.json carries this vocabulary. Deriving the enum AT the DTO rather than asserting
// about it is #42's work, which lands with the first endpoint that exposes one.
//
// WHAT DRIFT COSTS HERE. An actor kind in Go but not in the CHECK is a legal-looking commit that
// SQLite rejects at the audit INSERT — inside the transaction, after the batch, its entries and its
// snapshots have been written, so the officer's award rolls back with a constraint violation naming a
// table they have never heard of. The reverse — in the CHECK but not in Go — refuses a legal actor
// before the transaction opens. Neither is visible until someone adds the seventh value.

// repoRoot returns the directory holding go.mod, so these tests find db/ regardless of where
// `go test` was invoked from.
//
// Walked rather than filepath.Abs("../../.."), which produces the wrong answer silently the day this
// file moves — the same reasoning as internal/ledger/kinds' copy.
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

// TestAuditKinds_CheckMatchesCatalogue is the flagship: the committed db/schema.hcl is exactly what
// the catalogue renders.
//
// The assertion is "regenerating changes nothing", which is stronger than substring-matching the
// expression and fails in both directions — a value added to the Go catalogue and not regenerated,
// and a value hand-typed into the CHECK and not added to Go. It is the same question
// `make verify-generated` asks of every other generated tree, asked here as an ordinary test so a
// laptop finds the drift before CI does.
//
// It also proves the two generated regions are INDEPENDENT: this render walks the whole file,
// including the ledger's region, and equality means it left every line outside its own markers alone.
func TestAuditKinds_CheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	rendered, err := kinds.RenderSchemaHCL(committed)
	require.NoError(t, err, "render db/schema.hcl from the catalogue")

	require.Equal(t, committed, rendered,
		"db/schema.hcl's audit_log.actor_kind CHECK has drifted from internal/audit/kinds — run "+
			"`make gen` (and `make migration NAME=<snake_case>` if a value actually changed)")

	require.Contains(t, committed, kinds.CheckExpr())
}

// TestAuditKinds_SchemaDivergence_IsRestored is the negative control for the test above: a
// hand-edited CHECK must not survive a render.
//
// Without it, TestAuditKinds_CheckMatchesCatalogue is indistinguishable from a test that compares a
// file to itself — the classic tautology, and the one that matters most here because the gate's whole
// job is to notice a single edited word.
func TestAuditKinds_SchemaDivergence_IsRestored(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)

	tests := []struct {
		name    string
		mutate  func(string) string
		explain string
	}{
		{
			name:    "value dropped from the CHECK",
			mutate:  func(s string) string { return strings.Replace(s, "'anonymous'", "'user'", 1) },
			explain: "an actor kind deleted from the CHECK but still legal in Go",
		},
		{
			name:    "value misspelled in the CHECK",
			mutate:  func(s string) string { return strings.Replace(s, "'service_account'", "'service-account'", 1) },
			explain: "the separator drift that makes every bot's commit fail at the audit INSERT",
		},
		{
			name:    "value invented in the CHECK",
			mutate:  func(s string) string { return strings.Replace(s, "'boot'", "'boot', 'cron'", 1) },
			explain: "an actor kind hand-added to the CHECK and never added to Go",
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

// TestAuditKinds_MissingMarkers_IsAnError proves the generator refuses rather than silently doing
// nothing when db/schema.hcl no longer carries this catalogue's markers.
//
// A generator that cannot find its target and exits 0 is the worst of the available failures: every
// gate downstream reports success while the CHECK stays frozen at whatever the file last said.
func TestAuditKinds_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	committed := readSchemaHCL(t)
	begin := "  // BEGIN GENERATED — audit_log.actor_kind CHECK, from internal/audit/kinds. Run `make gen`."
	end := "  // END GENERATED — audit_log.actor_kind CHECK."

	require.Contains(t, committed, begin, "marker text changed — update this test with it")
	require.Contains(t, committed, end, "marker text changed — update this test with it")

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"audit_log\" {\n}\n"},
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

// TestAuditKinds_MigrationCheckMatchesCatalogue reads the migration TEXT, and names the file that
// drifted. It is a better error message than the applied-schema test in internal/ledger and a WEAKER
// assertion: it cannot see a later migration that rebuilt audit_log without re-creating the
// constraint. Keep both; that one is the one that is sound.
//
// THE LAST OCCURRENCE, NOT EVERY ONE. A shipped migration is frozen (.claude/rules/migrations.md), so
// when a value is added the migration that created the original CHECK keeps the original list forever
// and a NEW migration rebuilds the table with the new one. What a fresh install ends up with is the
// last CHECK in migration order, and that is the only one that has to agree with Go.
func TestAuditKinds_MigrationCheckMatchesCatalogue(t *testing.T) {
	t.Parallel()

	const constraint = "audit_log_actor_kind_enum"

	last, file := lastMigrationCheck(t, constraint, "actor_kind")
	require.NotEmpty(t, file,
		"no migration declares CONSTRAINT %q — the enum reaches no database", constraint)

	require.Equal(t, kinds.CheckExpr(), last,
		"%s carries an actor_kind CHECK that the Go catalogue no longer matches — the values in "+
			"internal/audit/kinds need a migration, written with `make migration NAME=<snake_case>` "+
			"after `make gen`", file)
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

// TestActorKinds_Values_AreCanonicalEnumValues checks the catalogue against canonical §5's rule for
// enum values: lowercase snake_case, unique, non-empty.
//
// The wire value IS the database value, so a value with a capital letter or a hyphen is not a style
// question — it is a JSON field and a CHECK literal that disagree the first time someone assumes one
// spelling from the other.
func TestActorKinds_Values_AreCanonicalEnumValues(t *testing.T) {
	t.Parallel()

	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

	values := kinds.ActorKinds()
	require.NotEmpty(t, values)

	seen := make(map[string]bool, len(values))

	for _, v := range values {
		require.Regexp(t, snakeCase, v, "%q is not lowercase snake_case (canonical §5)", v)
		require.False(t, seen[v], "%q appears twice in the catalogue", v)

		seen[v] = true
	}
}

// TestActorKinds_ReturnsAFreshSlice is the guard on the shape .claude/rules/go-idioms.md asks for: a
// caller that mutates the returned slice must not be able to reach the catalogue every other caller
// sees.
//
// The suite runs -shuffle=on with t.Parallel() everywhere, so a shared backing array would surface as
// an intermittent failure in an unrelated package — the single hardest failure in this repository to
// attribute, and the reason the old validActorKinds map had to go rather than merely be moved.
func TestActorKinds_ReturnsAFreshSlice(t *testing.T) {
	t.Parallel()

	first := kinds.ActorKinds()
	first[0] = "clobbered"

	require.Equal(t, kinds.ActorUser, kinds.ActorKinds()[0],
		"ActorKinds handed out a slice backed by shared state")
	require.True(t, kinds.IsActorKind(kinds.ActorUser))
	require.False(t, kinds.IsActorKind("clobbered"))
}

// TestAuditKinds_RuntimeValidation_AcceptsExactlyTheCatalogue is the drift test for the RUNTIME copy:
// what internal/ledger/commit.go's validate accepts must be what the generated CHECK accepts, value
// for value.
//
// Before this, commit.go carried its own `validActorKinds` map. Two lists meant two failure modes:
// add a kind to the CHECK and the validator refuses it as ErrInvalidRequest; add it only to the map
// and the validator waves through a value the database rejects mid-transaction. Deriving both from
// the catalogue removes the second list, and this asserts the derivation actually holds rather than
// trusting that it does.
func TestAuditKinds_RuntimeValidation_AcceptsExactlyTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, v := range kinds.ActorKinds() {
		require.True(t, kinds.IsActorKind(v),
			"%q is in the catalogue and the generated CHECK, so the validator must accept it", v)
	}

	// Near-misses, not nonsense: a casing slip, a hyphen for an underscore, a plural, the name of the
	// column itself and the empty string are what a caller actually produces when it gets this wrong.
	rejected := []string{"", "System", "service-account", "users", "actor_kind", "bot", "carrier_pigeon"}

	for _, v := range rejected {
		require.False(t, kinds.IsActorKind(v),
			"%q is not in the catalogue, so the validator must refuse it before the transaction", v)
	}
}

// TestAuditKinds_CheckExpr_RendersASQLInList pins the rendering, because the generator and the drift
// tests are separate callers that have to agree with the committed schema byte for byte — including
// the ", " separator, which is what makes the generated expression identical to the one that shipped
// and therefore migration-free.
func TestAuditKinds_CheckExpr_RendersASQLInList(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"actor_kind IN ('user', 'service_account', 'system', 'boot', 'import', 'anonymous')",
		kinds.CheckExpr(),
		"the rendered expression changed — every existing database carries the old text, so this is a "+
			"migration, not a formatting choice")
}
