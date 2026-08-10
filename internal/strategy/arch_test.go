package strategy_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The purity proof for law 3. Phase 0 PR 10b.
//
// AGENTS.md's third law says internal/strategy is pure: no internal/store, no wall clock, no
// math/rand. PR 10's acceptance criterion says it is proved "by walking the import graph
// transitively", and this file is that walk. It parses the imports out of the tree with go/parser and
// follows every first-party edge, so a violation introduced three packages down — the shape a grep
// cannot see — fails here.
//
// TWO BANS, TWO DIFFERENT DEPTHS, and the asymmetry is the correction PR 10a's review asked for:
//
//   - internal/store is banned TRANSITIVELY. Nothing reachable from this package may hold a
//     *sql.DB. That is the law as written and it is achievable: the dependency this package needs
//     from the ledger side arrives as an interface (Rng) or through the Ctx façade (Balance,
//     Allocate), never as an import.
//
//   - `time` and `math/rand` are banned as DIRECT imports only, because a transitive ban on `time`
//     is not satisfiable and never was. core.Micros is an int64 with a Time() method, so
//     internal/core imports time; internal/clock is the injected clock and imports time by
//     definition; this package imports both, deliberately. A transitive ban would fail on the very
//     design it exists to protect. What matters is that no file HERE reaches for the wall clock or
//     for an unseeded generator itself, and that is exactly a direct-import property. The
//     complementary halves are enforced elsewhere and are not weakened by this: repo gate CLOCK001
//     bans `time.Now(` across all of internal/ (so a transitively-reachable clock cannot be CALLED
//     from here), and PURE002 bans the math/rand string in this tree.
//
// The direct ban covers _test.go files too. The transitive one does not, and that exemption is
// argued rather than convenient: the ban is on what this package COMPILES INTO A BINARY, a _test.go
// file cannot reach one, and the ledger-side implementations of the injected seams are the only real
// ones that exist — ledger.NewRng is the only strategy.Rng, ledger.Allocate the only allocator. A
// test forbidden from importing them could only test the strategy against a stand-in for the ledger,
// which is the one thing nobody needs to know. The exemption is exactly as wide as that: an
// EXTERNAL test package (`package strategy_test`), which the Go compiler already refuses to link
// into the package under test.
//
// THIS FILE MUST NOT TRIP THE GREP GATES IT DESCRIBES. scripts/repo-gates.sh's PURE001 and PURE002
// fire on any file under internal/strategy that merely NAMES those import paths, comments excepted —
// which is correct for a gate that has to be dumb enough that nobody can argue with it, and which
// makes a test asserting the ban indistinguishable from a violation of it. So every banned path here
// is ASSEMBLED FROM FRAGMENTS at runtime. Do not "tidy" them back into literals: `make lint-repo`
// goes red and the failure looks like a real law-3 violation in the file that proves law 3 holds.

// storeImportPath is the package no dependency of internal/strategy may reach. Assembled; see the
// header.
func storeImportPath(module string) string { return module + "/" + storeRelPath }

// storeRelPath, clockRelPath and randImportPath are the three assembled path fragments.
var (
	storeRelPath   = "internal" + "/" + "store"
	randImportPath = "math" + "/" + "rand"
)

// bannedDirect are the import paths no file under internal/strategy may name directly.
func bannedDirect() []string {
	return []string{"time", randImportPath, randImportPath + "/v2"}
}

// bannedClockType is the real clock's type name, which no file under internal/strategy may name.
//
// AN IMPORT BAN IS NOT ENOUGH, and this is the hole it left — found in review of this PR. A strategy
// legitimately imports internal/clock, because Ctx.Clock() returns a clock.Clock. Nothing stopped a
// file in the package then writing
//
//	core.FromTime(clock.System{}.Now())
//
// which reads the REAL wall clock. It compiles, and it walked past every guard: the direct-import
// test sees only `internal/clock`, which is allowed; repo gate CLOCK001 greps for `time.Now(`, which
// this is not; and forbidigo's `^time\.Now$` resolves to a method on clock.System, not to time.Now.
// Law 3 was enforced by convention at that point rather than mechanically, which is exactly the claim
// this file exists to make untrue.
//
// clock.System is the ONLY real-clock path out of internal/clock — Clock is an interface, Fake is a
// test double, and System.Now is the one function in the repository that calls time.Now. So banning
// the identifier here closes it completely rather than narrowing it.
const bannedClockType = "System"

// purityAudit walks a module's first-party import graph.
//
// It resolves an import path to a directory by string prefix rather than through go/build or
// golang.org/x/tools/go/packages, and both alternatives were considered. x/tools would be a new
// dependency for a test (AGENTS.md: a human decides); go/build resolves against GOPATH and the
// module cache, which makes the negative fixture below — a fabricated module in t.TempDir() —
// unresolvable. A prefix match over the module path is what this graph actually is.
//
// THIRD-PARTY AND STDLIB PACKAGES ARE NOT WALKED, and that is sound rather than a gap: `internal/` is
// a visibility boundary the COMPILER enforces, so no package outside this module can import
// internal/store however hard it tries. The only paths that can reach it are the ones this walk
// follows.
type purityAudit struct {
	root   string
	module string
}

// newAudit reads the module path out of root/go.mod.
func newAudit(tb testing.TB, root string) purityAudit {
	tb.Helper()

	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(tb, err, "read %s/go.mod", root)

	for line := range strings.Lines(string(body)) {
		if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return purityAudit{root: root, module: strings.TrimSpace(module)}
		}
	}

	tb.Fatalf("%s/go.mod has no module line", root)

	return purityAudit{}
}

// packageDir maps a first-party import path to its directory. ok is false for stdlib and third-party
// paths, which the walk does not follow.
func (a purityAudit) packageDir(importPath string) (string, bool) {
	rel, ok := strings.CutPrefix(importPath, a.module+"/")
	if !ok {
		return "", false
	}

	return filepath.Join(a.root, filepath.FromSlash(rel)), true
}

// imports parses the import paths out of every .go file in dir, returning them sorted and
// deduplicated. Test files are included only when withTests is set.
func (a purityAudit) imports(tb testing.TB, dir string, withTests bool) []string {
	tb.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(tb, err, "read package directory %s", dir)

	seen := map[string]bool{}
	fset := token.NewFileSet()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		if !withTests && strings.HasSuffix(name, "_test.go") {
			continue
		}

		// ImportsOnly: the declarations are irrelevant and parsing them costs time on every package
		// in the graph.
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		require.NoError(tb, err, "parse %s", filepath.Join(dir, name))

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			require.NoError(tb, err, "unquote import %s in %s", spec.Path.Value, name)

			seen[path] = true
		}
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}

	sort.Strings(out)

	return out
}

// chainTo returns the shortest import chain from the package at startImportPath to banned, or nil
// when none exists.
//
// Breadth-first, so the chain reported is the shortest one — which is the one a human reading the
// failure has to break. Test files are never followed: see the header.
func (a purityAudit) chainTo(tb testing.TB, startImportPath, banned string) []string {
	tb.Helper()

	type node struct {
		pkg   string
		chain []string
	}

	queue := []node{{pkg: startImportPath, chain: []string{startImportPath}}}
	seen := map[string]bool{startImportPath: true}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		dir, ok := a.packageDir(current.pkg)
		if !ok {
			continue
		}

		for _, imported := range a.imports(tb, dir, false) {
			if imported == banned {
				return append(slices.Clone(current.chain), imported)
			}

			if seen[imported] {
				continue
			}

			seen[imported] = true

			queue = append(queue, node{pkg: imported, chain: append(slices.Clone(current.chain), imported)})
		}
	}

	return nil
}

// packagesUnder returns the import paths of every package in the tree rooted at rel.
//
// It walks rather than naming internal/strategy alone, so that adding a subpackage does not quietly
// take it out of the law's scope. That is the shape this defect always takes: the rule keeps
// applying to the directory somebody wrote it for.
func (a purityAudit) packagesUnder(tb testing.TB, rel string) []string {
	tb.Helper()

	var out []string

	err := filepath.WalkDir(filepath.Join(a.root, rel), func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}

		// testdata/ is not a package: the Go tool ignores it, and a tree of deliberately-tainted
		// fixtures is exactly what it would hold.
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}

		matches, err := filepath.Glob(filepath.Join(path, "*.go"))
		if err != nil || len(matches) == 0 {
			return err
		}

		relPath, err := filepath.Rel(a.root, path)
		require.NoError(tb, err)

		out = append(out, a.module+"/"+filepath.ToSlash(relPath))

		return nil
	})
	require.NoError(tb, err, "walk %s", rel)

	return out
}

// repoRoot is the checkout this test runs against: two levels up from internal/strategy.
func repoRoot(tb testing.TB) string {
	tb.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(tb, err)

	return root
}

// TestArch_Strategy_ImportGraph_HasNoStore is law 3's transitive half: nothing reachable from
// internal/strategy may hold a *sql.DB.
//
// The positive control is the point. A walk that found nothing would pass whether it worked or not,
// so the same auditor is pointed at internal/ledger — which imports internal/store openly and must —
// and is required to FIND the chain there. Without that, a bug that made packageDir return ok=false
// for everything would report a perfectly pure strategy package forever.
func TestArch_Strategy_ImportGraph_HasNoStore(t *testing.T) {
	t.Parallel()

	audit := newAudit(t, repoRoot(t))
	store := storeImportPath(audit.module)

	packages := audit.packagesUnder(t, filepath.Join("internal", "strategy"))
	require.NotEmpty(t, packages, "no packages found under internal/strategy — did the walk work?")

	for _, pkg := range packages {
		chain := audit.chainTo(t, pkg, store)
		require.Nil(t, chain,
			"law 3: %s must not reach %s, transitively.\n\t%s\n"+
				"A strategy proposes and the ledger commits. What this package needs from the ledger "+
				"side arrives as an interface (strategy.Rng) or through the Ctx façade "+
				"(Balance, Allocate, SystemAccount) — never as an import.",
			pkg, store, strings.Join(chain, "\n\t\t-> "))
	}

	// The positive control, on real code.
	ledger := audit.module + "/internal/ledger"
	require.NotNil(t, audit.chainTo(t, ledger, store),
		"the auditor found no path from %s to %s, which certainly exists. The walk is broken, and "+
			"every assertion above passed for that reason rather than because the strategy package "+
			"is pure.", ledger, store)
}

// TestArch_Strategy_Files_DoNotImportTimeOrMathRand is law 3's direct half, over every file in the
// tree including the tests.
//
// Direct rather than transitive, for the reason argued in the file header: core.Micros and
// clock.Clock both import `time`, so a transitive ban would reject the injected-clock design it
// exists to protect. What must not happen is a file HERE reaching for the wall clock or for an
// unseeded generator, and that is a direct-import property.
func TestArch_Strategy_Files_DoNotImportTimeOrMathRand(t *testing.T) {
	t.Parallel()

	audit := newAudit(t, repoRoot(t))

	for _, pkg := range audit.packagesUnder(t, filepath.Join("internal", "strategy")) {
		dir, ok := audit.packageDir(pkg)
		require.True(t, ok, "package %s did not resolve to a directory", pkg)

		found := audit.imports(t, dir, true)
		require.NotEmpty(t, found, "no imports parsed from %s — did the parse work?", dir)

		for _, banned := range bannedDirect() {
			require.NotContains(t, found, banned,
				"law 3: %s imports %q directly.\n"+
					"The clock and the seeded Rng are INJECTED, through strategy.Ctx. A wall clock "+
					"makes a plan unreplayable; an unseeded generator makes the rng_seed persisted "+
					"onto every batch a decoration, and a replay proves nothing.", pkg, banned)
		}
	}
}

// realClockUses returns every `<clockpkg>.System` reference in the .go files of dir, as
// "file:line" strings.
//
// It parses the whole file rather than the imports, and it resolves the clock package's LOCAL NAME
// from the import spec rather than assuming it is `clock` — an aliased import (`import c
// ".../internal/clock"`) is the first thing anybody reaches for when a grep gate starts complaining,
// and a check that could be defeated by an alias is a check that teaches people to alias.
func (a purityAudit) realClockUses(tb testing.TB, dir string) []string {
	tb.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(tb, err, "read package directory %s", dir)

	clockPath := a.module + "/internal/clock"

	var hits []string

	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}

		path := filepath.Join(dir, e.Name())

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(tb, err, "parse %s", path)

		local := ""

		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			require.NoError(tb, err)

			if imported != clockPath {
				continue
			}

			local = "clock"
			if spec.Name != nil {
				local = spec.Name.Name
			}
		}

		if local == "" || local == "_" {
			continue // the file does not import the clock package under a usable name
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != bannedClockType {
				return true
			}

			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == local {
				hits = append(hits, fmt.Sprintf("%s:%d", path, fset.Position(sel.Pos()).Line))
			}

			return true
		})
	}

	sort.Strings(hits)

	return hits
}

// TestArch_Strategy_DoesNotConstructTheRealClock closes the gap an import ban cannot: a strategy may
// import internal/clock, so it could construct clock.System and read the wall clock without ever
// naming `time`.
//
// See bannedClockType for why this is not covered by the import test, by CLOCK001 or by forbidigo.
// The injected clock arrives through Ctx.Clock() as an INTERFACE VALUE, and a strategy that made its
// own could not be replayed — the batch's effective time would depend on when the replay ran rather
// than on when the event happened, which is the whole reason the clock is injected.
func TestArch_Strategy_DoesNotConstructTheRealClock(t *testing.T) {
	t.Parallel()

	audit := newAudit(t, repoRoot(t))

	for _, pkg := range audit.packagesUnder(t, filepath.Join("internal", "strategy")) {
		dir, ok := audit.packageDir(pkg)
		require.True(t, ok, "package %s did not resolve to a directory", pkg)

		require.Empty(t, audit.realClockUses(t, dir),
			"law 3: %s constructs the real clock.\n"+
				"Ctx.Clock() hands the planner an injected clock.Clock; building the system "+
				"implementation reads the wall clock, and a plan that depends on when it ran cannot "+
				"be replayed. This is not caught by the import ban (internal/clock is allowed) nor by "+
				"CLOCK001 (it greps for time.Now).", pkg)
	}
}

// TestArch_PurityAudit_FiresOnATaintedTree is the negative fixture: proof that the two assertions
// above would actually go red.
//
// A gate is only worth what its failure mode is worth, and neither assertion can be exercised against
// this repository without breaking it — which is precisely why the tainted tree is fabricated in
// t.TempDir() rather than committed under testdata/. A committed fixture that imported
// internal/store would be found by the real `make lint-repo` and fail the project's own CI, which is
// the same reasoning scripts/repo-gates.sh's own negative fixtures are built on.
//
// The tainted tree is TWO HOPS deep — strategy -> ledger -> store — so it proves the walk is
// transitive rather than merely checking the package's own import block. A one-hop fixture would pass
// against an auditor that never recursed.
func TestArch_PurityAudit_FiresOnATaintedTree(t *testing.T) {
	t.Parallel()

	const module = "example.com/tainted"

	tree := t.TempDir()

	// Assembled at runtime for the reason in the file header: a source literal here would put the
	// banned path into a file under internal/strategy and trip PURE001 on this very test.
	storePkg := module + "/" + storeRelPath

	writeGo(t, tree, "go.mod", "module "+module+"\n\ngo 1.25\n")
	writeGo(t, tree, filepath.Join("internal", "strategy", "tainted.go"),
		"package strategy\n\nimport _ \""+module+"/internal/ledger\"\n")
	writeGo(t, tree, filepath.Join("internal", "ledger", "ledger.go"),
		"package ledger\n\nimport _ \""+storePkg+"\"\n")
	writeGo(t, tree, filepath.Join("internal", "store", "store.go"), "package store\n")
	writeGo(t, tree, filepath.Join("internal", "strategy", "wallclock.go"),
		"package strategy\n\nimport (\n\t_ \"time\"\n\t_ \""+randImportPath+"\"\n)\n")

	// The real-clock path, under an ALIASED import — the shape that would defeat a grep and that a
	// resolved-local-name check must still catch.
	writeGo(t, tree, filepath.Join("internal", "clock", "clock.go"), "package clock\n\ntype System struct{}\n")
	writeGo(t, tree, filepath.Join("internal", "strategy", "realclock.go"),
		"package strategy\n\nimport c \""+module+"/internal/clock\"\n\nvar _ = c."+bannedClockType+"{}\n")

	audit := newAudit(t, tree)
	require.Equal(t, module, audit.module)

	strategyPkg := module + "/internal/strategy"

	chain := audit.chainTo(t, strategyPkg, storePkg)
	require.Equal(t, []string{strategyPkg, module + "/internal/ledger", storePkg}, chain,
		"the auditor must report the whole chain, not just that one exists: the chain is what tells "+
			"somebody which edge to break")

	// And the direct ban fires on the same tree, naming both paths.
	dir, ok := audit.packageDir(strategyPkg)
	require.True(t, ok)

	found := audit.imports(t, dir, true)
	for _, banned := range bannedDirect()[:2] {
		require.Contains(t, found, banned,
			"the direct-import check must see %q in the tainted tree, or the assertion in "+
				"TestArch_Strategy_Files_DoNotImportTimeOrMathRand is checking nothing", banned)
	}

	// And the real-clock check fires, on the aliased import.
	require.Len(t, audit.realClockUses(t, dir), 1,
		"the real-clock check must see the aliased construction, or an `import c \".../clock\"` "+
			"walks straight past TestArch_Strategy_DoesNotConstructTheRealClock")

	// The clean half of the fixture: a package that does NOT reach the banned one must report no
	// chain, and a package that does not import the clock at all must report no clock use. Without
	// these the test would pass against an auditor that reported a violation for everything.
	require.Nil(t, audit.chainTo(t, storePkg, module+"/internal/ledger"),
		"%s imports nothing in the fixture, so there is no chain to find", storePkg)

	ledgerDir, ok := audit.packageDir(module + "/internal/ledger")
	require.True(t, ok)
	require.Empty(t, audit.realClockUses(t, ledgerDir),
		"the fixture's ledger package does not import the clock, so there is nothing to report")
}

// writeGo writes one file of the fabricated tree, creating its parent directories.
func writeGo(tb testing.TB, root, rel, body string) {
	tb.Helper()

	path := filepath.Join(root, rel)
	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(tb, os.WriteFile(path, []byte(body), 0o644))
}
