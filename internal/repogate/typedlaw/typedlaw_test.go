// Negative fixtures for the type-aware pass.
//
// Same two rules internal/repogate's tests are held to: the fixture lives in t.TempDir(), and the
// assertion names the rule id rather than only the colour of the run. One difference, and it is the
// reason this package exists at all — a fixture here must BUILD. That is what a type checker needs,
// it is what internal/repogate's fixtures deliberately are not, and it is why that engine is the
// merge gate and this one is the second opinion.
//
// And every fixture is OFFLINE. A t.TempDir() module has no module cache to draw on, so ROUTE001's
// — the one law about a third-party symbol — stubs huma in a second local module and reaches it
// with a `replace`. Requiring the real one would make this file's coverage depend on whether the
// machine running it had downloaded a dependency, which is a fixture that skips on the day it
// matters.
//
// TestLaws_EveryLawHasAFixture is what keeps the set complete: a law added to laws() with no
// requireFired naming it fails there.
package typedlaw

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fixtureModule writes a buildable module into t.TempDir() and returns its root.
//
// files is keyed by module-relative path. go.mod is defaulted here rather than by each caller so
// that the Go directive is in one place: a fixture that pins a different one from the toolchain
// fails to load with an error about versions rather than about the taint under test. A caller that
// needs a `replace` — ROUTE001's does — supplies its own and this one steps aside.
func fixtureModule(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	// example.com/tainted, never this repository's module path: a rule that hardcoded the real one
	// would pass vacuously in exactly the tree these tests point it at.
	all := map[string]string{"go.mod": "module example.com/tainted\n\ngo 1.26\n"}
	for path, body := range files {
		all[path] = body
	}

	for path, body := range all {
		abs := filepath.Join(root, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))
	}

	return root
}

// findings runs the whole pass over a fixture module and returns its results.
func findings(t *testing.T, root string) map[string][]string {
	t.Helper()

	pkgs, err := load(root)
	require.NoErrorf(t, err, "the fixture module must build and type-check — a fixture that does "+
		"not is a test about the fixture rather than about the law")

	out := map[string][]string{}
	for _, r := range evaluate(pkgs) {
		out[r.ID] = r.Hits
	}

	return out
}

// requireFired asserts a law reported, and that its hit names the file the taint is in.
func requireFired(t *testing.T, got map[string][]string, id, file string) {
	t.Helper()

	hits, ok := got[id]
	require.Truef(t, ok, "%s did not fire on a tree that violates it. Fired: %v", id, keys(got))
	require.NotEmpty(t, hits, "%s fired with no hits, so the report names nothing to fix", id)

	joined := strings.Join(hits, "\n")
	require.Containsf(t, joined, file,
		"%s must name the file the violation is in — a finding without a position is one nobody "+
			"can act on\n%s", id, joined)
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// TestCLOCK001_DotImportedTimeNow_Fires is issue #172's first named example, and the single
// clearest argument for the whole pass.
//
// `import . "time"` makes `Now()` a bare call: there is no selector, no `time.` prefix and nothing
// for a syntax rule to match. CLOCK001 in internal/repogate looks for a `<local>.Now` selector and
// walks straight past this file. Resolving the identifier to its object asks the question the law
// is actually asking.
func TestCLOCK001_DotImportedTimeNow_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/service/service.go": `package service

import . "time"

// At reads the wall clock with no selector anywhere in the file.
func At() Time { return Now() }
`,
	})

	requireFired(t, findings(t, root), "CLOCK001", "internal/service/service.go")
}

// TestROUTE001_RegisterThroughAFunctionValue_Fires — law 1, reached through a variable.
//
// `register := huma.Register` and then `register(...)` produces no `huma.Register` selector at the
// call site, so the syntax rule sees a route declared outside internal/api and reports nothing.
// Resolving the identifier to its object sees the same function either way.
//
// huma is STUBBED and replaced locally rather than required from the module cache: a fixture that
// needs the network is a fixture that skips on the day it matters, and the law asks only whether an
// object named Register comes from a package path carrying danielgtaylor/huma.
func TestROUTE001_RegisterThroughAFunctionValue_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"go.mod": `module example.com/tainted

go 1.26

require github.com/danielgtaylor/huma/v2 v2.0.0

replace github.com/danielgtaylor/huma/v2 => ./huma
`,
		"huma/go.mod": "module github.com/danielgtaylor/huma/v2\n\ngo 1.26\n",
		"huma/huma.go": `package huma

// API stands in for the real registry.
type API struct{}

// Operation stands in for the real operation descriptor.
type Operation struct{ Path string }

// Register is the one symbol law 1 is about.
func Register(api API, op Operation) {}
`,
		"internal/ui/routes.go": `package ui

import huma "github.com/danielgtaylor/huma/v2"

// Mount declares a route outside internal/api, through a function value.
func Mount(api huma.API) {
	register := huma.Register
	register(api, huma.Operation{Path: "/ui/secret"})
}
`,
	})

	requireFired(t, findings(t, root), "ROUTE001", "internal/ui/routes.go")
}

// TestSQL002_MethodOnAnEmbeddedHandle_Fires is the second: a query run through a wrapper type that
// embeds *sql.DB. The calling line never names database/sql, and the promoted method still belongs
// to it.
func TestSQL002_MethodOnAnEmbeddedHandle_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/service/service.go": `package service

import "database/sql"

// Pool hides the handle behind a name of its own.
type Pool struct{ *sql.DB }

// Count runs SQL through the promoted method. No ` + "`sql.`" + ` on this line.
func Count(p Pool) error {
	_, err := p.ExecContext(nil, "SELECT 1")

	return err
}
`,
	})

	got := findings(t, root)
	requireFired(t, got, "SQL002", "internal/service/service.go")
	requireFired(t, got, "SQL004", "internal/service/service.go")
}

// TestSQL002_URLQuery_DoesNotFire is the OTHER direction, and it is the half a gate loses first.
//
// `r.URL.Query()` is net/url's accessor and it appears throughout internal/api. The syntax rule has
// to exclude it by shape — a zero-argument call — because a line-scoped allowlist can only drop the
// whole line. Here the exclusion is not needed: net/url is not database/sql. A pass that fired on
// this would be turned off within a week.
func TestSQL002_URLQuery_DoesNotFire(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/handler/handler.go": `package handler

import "net/http"

// Search reads a query parameter, which is not a database call.
func Search(r *http.Request) string { return r.URL.Query().Get("q") }
`,
	})

	got := findings(t, root)
	require.NotContainsf(t, keys(got), "SQL002",
		"SQL002 fired on net/url's Query accessor. The whole point of deciding by declaring "+
			"package is that this is not a database call: %v", got["SQL002"])
}

// TestSQL004_AliasedHandle_Fires is the law as AGENTS.md states it — "*sql.DB is held only by
// internal/store" — through an alias that never spells the type.
//
// This is the finding class internal/repogate cannot have, and the package doc records the reason.
func TestSQL004_AliasedHandle_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/service/service.go": `package service

import "database/sql"

// handle is the alias. Nothing below names sql.DB.
type handle = sql.DB

// Service holds the database outside internal/store.
type Service struct{ conn *handle }
`,
	})

	requireFired(t, findings(t, root), "SQL004", "internal/service/service.go")
}

// TestSQL001_AliasedImport_Fires — an aliased database/sql defeats a name-based rule and not an
// object-based one.
func TestSQL001_AliasedImport_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/service/service.go": `package service

import d "database/sql"

// Connect opens a handle outside internal/store.
func Connect() (*d.DB, error) { return d.Open("sqlite", ":memory:") }
`,
	})

	requireFired(t, findings(t, root), "SQL001", "internal/service/service.go")
}

// TestPURE001_TransitiveStoreImport_Fires — a strategy that reaches the store through an
// intermediate package satisfies the syntax rule, which reads one file's import block.
func TestPURE001_TransitiveStoreImport_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/strategy/plan.go": `package strategy

import "example.com/tainted/internal/helper"

// Plan looks pure. Its import block names nothing banned.
func Plan() int { return helper.Rows() }
`,
		"internal/helper/helper.go": `package helper

import "example.com/tainted/internal/store"

// Rows reaches the database on the strategy's behalf.
func Rows() int { return store.Rows() }
`,
		"internal/store/store.go": `package store

// Rows is the store.
func Rows() int { return 0 }
`,
	})

	got := findings(t, root)
	requireFired(t, got, "PURE001", "internal/strategy/plan.go")
	require.Containsf(t, strings.Join(got["PURE001"], "\n"), "internal/helper",
		"the finding must print the CHAIN. A transitive violation whose path the reader has to "+
			"rediscover with `go list -deps` is one they will not act on:\n%s",
		strings.Join(got["PURE001"], "\n"))
}

// TestPURE002_TransitiveRandInsideTheModule_Fires — the same shape for the seeded-RNG half.
func TestPURE002_TransitiveRandInsideTheModule_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/strategy/plan.go": `package strategy

import "example.com/tainted/internal/dice"

// Plan looks pure.
func Plan() int { return dice.Roll() }
`,
		"internal/dice/dice.go": `package dice

import "math/rand/v2"

// Roll rolls an unseeded die on the strategy's behalf. math/rand/v2 counts.
func Roll() int { return rand.IntN(6) }
`,
	})

	requireFired(t, findings(t, root), "PURE002", "internal/strategy/plan.go")
}

// TestPURE002_ThirdPartyRand_DoesNotFire is the boundary that keeps PURE002 usable.
//
// The walk is transitive through THIS MODULE's packages and terminal at everything else. Without
// that line the rule fires on this repository today, through ulid's own math/rand three hops from
// any strategy — a dependency's implementation detail, reported on every run, which is how a pass
// becomes noise. The fixture stands in for that with a package outside the module's import graph.
func TestPURE002_ThirdPartyRand_DoesNotFire(t *testing.T) {
	t.Parallel()

	// internal/strategy imports only the standard library's crypto/rand, whose own dependencies are
	// not this module's packages and are therefore terminal.
	root := fixtureModule(t, map[string]string{
		"internal/strategy/plan.go": `package strategy

import "crypto/rand"

// Plan draws entropy the way internal/core does, from crypto/rand rather than math/rand.
func Plan() ([]byte, error) {
	b := make([]byte, 8)
	_, err := rand.Read(b)

	return b, err
}
`,
	})

	got := findings(t, root)
	require.NotContainsf(t, keys(got), "PURE002",
		"PURE002 fired on a package that imports crypto/rand. The law is about unseeded dice in a "+
			"plan, not about every package with `rand` in its path: %v", got["PURE002"])
}

// TestMONEY001_UntypedFloatConstant_Fires is the hole the syntax rule cannot close.
//
// `rate := 0.15` in internal/strategy is a float64 with the word float64 nowhere in the file, so
// MONEY001 in internal/repogate — which looks for the two identifiers — reports nothing. A float in
// the point path does not fail, it DRIFTS.
func TestMONEY001_UntypedFloatConstant_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/strategy/decay.go": `package strategy

// Decay applies a rate. The word float64 does not appear in this file.
func Decay(points int64) int64 {
	rate := 0.15

	return int64(float64(points) * (1 - rate))
}
`,
	})

	requireFired(t, findings(t, root), "MONEY001", "internal/strategy/decay.go")
}

// TestMONEY001_IntegerArithmetic_DoesNotFire is its control: the shape the ledger actually uses.
func TestMONEY001_IntegerArithmetic_DoesNotFire(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/ledger/split.go": `package ledger

// Split is largest-remainder allocation in integers, which is the only way it may be written.
func Split(total int64, n int64) (int64, int64) { return total / n, total % n }
`,
	})

	got := findings(t, root)
	require.NotContainsf(t, keys(got), "MONEY001",
		"MONEY001 fired on integer arithmetic: %v", got["MONEY001"])
}

// TestSQL003_ForTestHelperInProductionCode_Fires — the typed form needs no `_test.go:` allowlist,
// because this pass reads only the non-test build.
func TestSQL003_ForTestHelperInProductionCode_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/store/testing.go": `package store

// ExecForTest is the raw-SQL affordance tests reach for.
func ExecForTest(query string) error { return nil }
`,
		"internal/service/service.go": `package service

import "example.com/tainted/internal/store"

// Repair calls a test affordance from production code.
func Repair() error { return store.ExecForTest("DELETE FROM ledger_entry") }
`,
	})

	requireFired(t, findings(t, root), "SQL003", "internal/service/service.go")
}

// TestCLOCK002_ClockSystemInAStrategy_Fires — the real clock, constructed inside a plan.
func TestCLOCK002_ClockSystemInAStrategy_Fires(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/clock/clock.go": `package clock

import "time"

// System is the one real-clock path out of this package.
type System struct{}

// Now reads the wall clock.
func (System) Now() time.Time { return time.Now() }
`,
		"internal/strategy/plan.go": `package strategy

import (
	"time"

	c "example.com/tainted/internal/clock"
)

// Plan reads the wall clock through an aliased import of the clock package.
func Plan() time.Time { return c.System{}.Now() }
`,
	})

	requireFired(t, findings(t, root), "CLOCK002", "internal/strategy/plan.go")
}

// TestCleanTree_ReportsNothing is the control for the whole file. A pass that fires on everything
// gets turned off rather than obeyed.
func TestCleanTree_ReportsNothing(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/strategy/plan.go": `package strategy

// Plan is pure integer arithmetic over its arguments.
func Plan(points, share int64) int64 { return points * share }
`,
		"internal/store/store.go": `package store

import "database/sql"

// Open is allowed here and nowhere else.
func Open(dsn string) (*sql.DB, error) { return sql.Open("sqlite", dsn) }
`,
	})

	got := findings(t, root)
	require.Emptyf(t, keys(got), "a clean tree must report nothing: %v", got)
}

// TestLoad_TreeThatDoesNotBuild_IsAHardFailure is the advisory's own limit.
//
// A pass that exits 0 because it never ran is worse than no pass. This is the rule
// scripts/migrate-lint.sh states for atlas and `make govulncheck` states for its binary, asserted
// here rather than trusted: a tree that does not compile must produce an ERROR, never an empty
// result that the report would print as "no findings".
func TestLoad_TreeThatDoesNotBuild_IsAHardFailure(t *testing.T) {
	t.Parallel()

	root := fixtureModule(t, map[string]string{
		"internal/service/service.go": "package service\n\nfunc Broken() int { return \"not an int\" }\n",
	})

	_, err := load(root)
	require.Error(t, err, "a tree that does not build must be a hard failure, not an empty result — "+
		"an advisory that reports 'no findings' about code it never read is worse than no advisory")
}

// TestLoad_NoModule_IsAHardFailure is the same limit from the other end, and it is the property
// that decides which of the two engines is the merge gate.
//
// internal/repogate's negative fixtures are trees with no go.mod at all, and it reads them happily.
// This pass cannot, says so, and that is why `make lint-repo` blocks the merge and this one advises.
func TestLoad_NoModule_IsAHardFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal", "service"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "service", "service.go"),
		[]byte("package service\n"), 0o644))

	_, err := load(root)
	require.Error(t, err, "a tree with no module must be a hard failure")
}

// TestLaws_EveryLawHasAFixture keeps this file honest as the catalogue grows.
//
// A law with no negative fixture is a law nobody has seen go red, which AGENTS.md says is a law
// nobody knows works. This is the assertion that makes adding one to laws() cost a fixture.
func TestLaws_EveryLawHasAFixture(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("typedlaw_test.go")
	require.NoError(t, err)

	for _, l := range laws() {
		t.Run(l.id, func(t *testing.T) {
			t.Parallel()

			// A requireFired call naming the id — the assertion that the law FIRED on a tree that
			// violates it. Matching the call rather than the bare string is what stops a law whose
			// only mention is a `DoesNotFire` control from counting as covered.
			fired := regexp.MustCompile(`requireFired\((?s).{0,60}"` + l.id + `"`)

			require.Truef(t, fired.Match(body),
				"%s has no negative fixture in this file — no requireFired asserts it goes red on a "+
					"tree that violates it. A law nobody has seen fire is a law nobody knows works "+
					"(AGENTS.md).", l.id)
		})
	}
}
