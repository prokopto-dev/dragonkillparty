package swap_test

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

// The purity proof for internal/swap, and — unlike internal/strategy's — it is the ONLY one.
//
// AGENTS.md's third law names internal/strategy, and every mechanism behind it is scoped to that
// tree: repo gates PURE001, PURE002 and CLOCK002 take `tree = "internal/strategy"`, and golangci's
// float ban is excluded everywhere outside internal/ledger and internal/strategy. None of them
// reaches this package. So this file is not a second opinion on a gate that already exists — it is
// the gate, and it is written to fail the same way the ones next door do.
//
// What it asserts, and at what depth:
//
//   - internal/store is banned TRANSITIVELY. Nothing reachable from here may hold a *sql.DB. A quote
//     is priced from the facts in the Request and from nothing else; a package that could reach the
//     store could price a swap on state the held quote never saw, and Phase 7's promise that a
//     pending swap is never re-priced would quietly stop being true.
//
//   - `time` and `math/rand` are banned as DIRECT imports of the SHIPPED files. A transitive ban is
//     not satisfiable and never was: core.Micros has a Time() method and clock.Clock is the injected
//     clock, so both import `time` by construction, and this package imports both deliberately. What
//     must not happen is a shipped file reaching for the wall clock itself.
//
//   - The TESTS may import `time`, and the exemption is argued rather than convenient. A test cannot
//     link into the shipped binary, time.Date is how a readable year boundary gets written into a
//     table, and the year-boundary arithmetic is precisely what needs testing at a leap year. The one
//     thing that would matter — a test reading the WALL clock and passing intermittently — is banned
//     by repo gate CLOCK001, which is an AST analyzer over all of internal/ and does read _test.go.
//
//   - clock.System is banned in the shipped files. An import ban cannot see it: this package
//     legitimately imports internal/clock, so a file could build the real clock without ever naming
//     `time`. CLOCK002 closes that hole for internal/strategy and stops there.
//
//   - float32 and float64 are banned in every file, tests included. This package's entire subject is
//     money, and a float in the arithmetic does not fail — it DRIFTS, and a price wrong by a fraction
//     of a point is discovered by a member disputing a quote rather than by CI.
//
// The negative fixture at the bottom is what makes those claims worth anything: it builds a tree
// that violates each of the four checks and requires every one of them to find it.

// The banned paths, written as plain literals. internal/strategy's twin has to assemble these from
// fragments because repo gates PURE001 and PURE002 fire on any file in THAT tree naming them; those
// gates do not read this one, so there is nothing here to defeat by spelling the path honestly.
const (
	storeRel  = "internal/store"
	clockRel  = "internal/clock"
	timePath  = "time"
	randPath  = "math/rand"
	swapRel   = "internal/swap"
	ledgerRel = "internal/ledger"
)

// realClockType is the only real-clock path out of internal/clock: Clock is an interface, Fake is a
// test double, and System.Now is the one function in the repository that calls time.Now. Banning the
// identifier closes the hole rather than narrowing it.
const realClockType = "System"

// bannedFloats are the two identifiers that must not appear in a package that prices things. A
// function rather than a package-level slice, which would be mutable state shared across a shuffled,
// parallel suite (.claude/rules/go-idioms.md).
func bannedFloats() []string { return []string{"float" + "32", "float" + "64"} }

// audit walks a module's first-party import graph.
//
// It resolves an import path to a directory by string prefix rather than through go/build or
// x/tools: x/tools is a dependency a human would have to approve (AGENTS.md), and go/build resolves
// against GOPATH and the module cache, which would make the fabricated tree in t.TempDir()
// unresolvable. Third-party and stdlib packages are not walked, and that is sound rather than a gap —
// `internal/` is a visibility boundary the compiler enforces, so nothing outside this module can
// reach internal/store however hard it tries.
type audit struct {
	root   string
	module string
}

// newAudit reads the module path out of root/go.mod.
func newAudit(tb testing.TB, root string) audit {
	tb.Helper()

	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(tb, err, "read %s/go.mod", root)

	for line := range strings.Lines(string(body)) {
		if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return audit{root: root, module: strings.TrimSpace(module)}
		}
	}

	tb.Fatalf("%s/go.mod has no module line", root)

	return audit{}
}

// pkg turns a repo-relative directory into an import path.
func (a audit) pkg(rel string) string { return a.module + "/" + rel }

// packageDir maps a first-party import path to its directory. ok is false for stdlib and third-party
// paths, which the walk does not follow.
func (a audit) packageDir(importPath string) (string, bool) {
	rel, ok := strings.CutPrefix(importPath, a.module+"/")
	if !ok {
		return "", false
	}

	return filepath.Join(a.root, filepath.FromSlash(rel)), true
}

// goFiles lists the .go files of a directory, with or without the tests.
func goFiles(tb testing.TB, dir string, withTests bool) []string {
	tb.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(tb, err, "read package directory %s", dir)

	var out []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		if !withTests && strings.HasSuffix(name, "_test.go") {
			continue
		}

		out = append(out, filepath.Join(dir, name))
	}

	sort.Strings(out)

	return out
}

// imports parses the import paths out of a package, sorted and deduplicated.
func (a audit) imports(tb testing.TB, dir string, withTests bool) []string {
	tb.Helper()

	seen := map[string]bool{}
	fset := token.NewFileSet()

	for _, path := range goFiles(tb, dir, withTests) {
		// ImportsOnly: the declarations are irrelevant here and parsing them costs time on every
		// package in the graph.
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		require.NoError(tb, err, "parse %s", path)

		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			require.NoError(tb, err, "unquote import %s in %s", spec.Path.Value, path)

			seen[imported] = true
		}
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}

	sort.Strings(out)

	return out
}

// chainTo returns the shortest import chain from a package to banned, or nil when none exists.
//
// Breadth-first, so the chain reported is the shortest one — which is the one a human reading the
// failure has to break. Test files are never followed.
func (a audit) chainTo(tb testing.TB, start, banned string) []string {
	tb.Helper()

	type node struct {
		pkg   string
		chain []string
	}

	queue := []node{{pkg: start, chain: []string{start}}}
	seen := map[string]bool{start: true}

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
// It walks rather than naming internal/swap alone, so that adding a subpackage does not quietly take
// it out of the law's scope. That is the shape this defect always takes: the rule keeps applying to
// the directory somebody wrote it for.
func (a audit) packagesUnder(tb testing.TB, rel string) []string {
	tb.Helper()

	var out []string

	err := filepath.WalkDir(filepath.Join(a.root, filepath.FromSlash(rel)),
		func(path string, d os.DirEntry, err error) error {
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

			out = append(out, a.pkg(filepath.ToSlash(relPath)))

			return nil
		})
	require.NoError(tb, err, "walk %s", rel)

	return out
}

// identUses returns every use of one of the named identifiers in dir, as "file:line" strings.
func (a audit) identUses(tb testing.TB, dir string, withTests bool, names ...string) []string {
	tb.Helper()

	var hits []string

	fset := token.NewFileSet()

	for _, path := range goFiles(tb, dir, withTests) {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(tb, err, "parse %s", path)

		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if ok && slices.Contains(names, ident.Name) {
				hits = append(hits, fmt.Sprintf("%s:%d", path, fset.Position(ident.Pos()).Line))
			}

			return true
		})
	}

	sort.Strings(hits)

	return hits
}

// realClockUses returns every `<clockpkg>.System` reference in dir's shipped files.
//
// It resolves the clock package's LOCAL NAME from the import spec rather than assuming it is `clock`:
// an aliased import is the first thing anybody reaches for when a grep starts complaining, and a
// check an alias defeats is a check that teaches people to alias.
func (a audit) realClockUses(tb testing.TB, dir string) []string {
	tb.Helper()

	clockPath := a.pkg(clockRel)

	var hits []string

	fset := token.NewFileSet()

	for _, path := range goFiles(tb, dir, false) {
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
			if !ok || sel.Sel.Name != realClockType {
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

// swapRepoRoot is the checkout this test runs against: two levels up from internal/swap.
func swapRepoRoot(tb testing.TB) string {
	tb.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(tb, err)

	return root
}

// TestArch_Swap_ImportGraph_HasNoStore: nothing reachable from internal/swap may hold a *sql.DB.
//
// The positive control is the point. A walk that found nothing would pass whether it worked or not,
// so the same auditor is pointed at internal/ledger — which imports internal/store openly and must —
// and is required to FIND the chain there.
func TestArch_Swap_ImportGraph_HasNoStore(t *testing.T) {
	t.Parallel()

	a := newAudit(t, swapRepoRoot(t))
	store := a.pkg(storeRel)

	packages := a.packagesUnder(t, swapRel)
	require.NotEmpty(t, packages, "no packages found under %s — did the walk work?", swapRel)

	for _, pkg := range packages {
		chain := a.chainTo(t, pkg, store)
		require.Nil(t, chain,
			"%s must not reach %s, transitively.\n\t%s\n"+
				"A quote is priced from the facts in the Request and from nothing else. Reading the "+
				"store here would price a swap on state the held quote never saw, and Phase 7's promise "+
				"that a pending swap is never re-priced would stop being true.",
			pkg, store, strings.Join(chain, "\n\t\t-> "))
	}

	require.NotNil(t, a.chainTo(t, a.pkg(ledgerRel), store),
		"the auditor found no path from %s to %s, which certainly exists. The walk is broken, and "+
			"every assertion above passed for that reason rather than because this package is pure.",
		a.pkg(ledgerRel), store)
}

// TestArch_Swap_ShippedFiles_DoNotImportTimeOrMathRand is the direct half, over the files that link
// into the binary. See the file header for why the tests are exempt and what still covers them.
func TestArch_Swap_ShippedFiles_DoNotImportTimeOrMathRand(t *testing.T) {
	t.Parallel()

	a := newAudit(t, swapRepoRoot(t))

	for _, pkg := range a.packagesUnder(t, swapRel) {
		dir, ok := a.packageDir(pkg)
		require.True(t, ok, "package %s did not resolve to a directory", pkg)

		found := a.imports(t, dir, false)
		require.NotEmpty(t, found, "no imports parsed from %s — did the parse work?", dir)

		for _, banned := range []string{timePath, randPath, randPath + "/v2"} {
			require.NotContains(t, found, banned,
				"%s imports %q directly.\n"+
					"The clock is INJECTED, through swap.New. A wall clock read here would make a quote "+
					"depend on when it was priced rather than on the instant it was priced FOR, and a "+
					"held quote could not be reproduced. Nothing in this package needs randomness at "+
					"all.", pkg, banned)
		}
	}
}

// TestArch_Swap_DoesNotConstructTheRealClock closes the hole an import ban cannot see: this package
// may import internal/clock, so it could build clock.System and read the wall clock without ever
// naming `time`.
func TestArch_Swap_DoesNotConstructTheRealClock(t *testing.T) {
	t.Parallel()

	a := newAudit(t, swapRepoRoot(t))

	for _, pkg := range a.packagesUnder(t, swapRel) {
		dir, ok := a.packageDir(pkg)
		require.True(t, ok, "package %s did not resolve to a directory", pkg)

		require.Empty(t, a.realClockUses(t, dir),
			"%s constructs the real clock.\n"+
				"swap.New takes an injected clock.Clock. Building the system implementation reads the "+
				"wall clock, and a quote that depends on when it ran cannot be re-priced to the same "+
				"number. CLOCK002 does not cover this tree.", pkg)
	}
}

// TestArch_Swap_DeclaresNoFloat, over the tests as well as the shipped files.
//
// golangci-lint's float ban is excluded everywhere outside internal/ledger and internal/strategy, so
// nothing else stops one here. A float in a package whose subject is money does not fail — it drifts,
// and a price wrong by a fraction of a point is found by a member disputing a quote.
func TestArch_Swap_DeclaresNoFloat(t *testing.T) {
	t.Parallel()

	a := newAudit(t, swapRepoRoot(t))

	for _, pkg := range a.packagesUnder(t, swapRel) {
		dir, ok := a.packageDir(pkg)
		require.True(t, ok, "package %s did not resolve to a directory", pkg)

		require.Empty(t, a.identUses(t, dir, true, bannedFloats()...),
			"%s names a float type.\n"+
				"Money is core.Centipoints and ratios are integer basis points. A percentage held as a "+
				"float is how a price drifts, and this package is the one that quotes prices.", pkg)
	}
}

// TestArch_SwapAudit_FiresOnATaintedTree is the negative fixture: proof that all four assertions
// above would actually go red.
//
// None of them can be exercised against this repository without breaking it, which is why the tainted
// tree is fabricated in t.TempDir() rather than committed under testdata/ — a committed fixture that
// imported internal/store would be found by the real `make lint-repo` and fail the project's own CI.
//
// The tainted tree is TWO HOPS deep — swap -> ledger -> store — so it proves the walk is transitive
// rather than merely reading the package's own import block. A one-hop fixture would pass against an
// auditor that never recursed.
func TestArch_SwapAudit_FiresOnATaintedTree(t *testing.T) {
	t.Parallel()

	const module = "example.com/tainted"

	tree := t.TempDir()

	writeGoFile(t, tree, "go.mod", "module "+module+"\n\ngo 1.25\n")
	writeGoFile(t, tree, filepath.Join("internal", "swap", "tainted.go"),
		"package swap\n\nimport _ \""+module+"/"+ledgerRel+"\"\n")
	writeGoFile(t, tree, filepath.Join("internal", "ledger", "ledger.go"),
		"package ledger\n\nimport _ \""+module+"/"+storeRel+"\"\n")
	writeGoFile(t, tree, filepath.Join("internal", "store", "store.go"), "package store\n")
	writeGoFile(t, tree, filepath.Join("internal", "swap", "wallclock.go"),
		"package swap\n\nimport (\n\t_ \""+timePath+"\"\n\t_ \""+randPath+"\"\n)\n")

	// The real-clock path, under an ALIASED import — the shape that would defeat a grep and that a
	// resolved-local-name check must still catch.
	writeGoFile(t, tree, filepath.Join("internal", "clock", "clock.go"),
		"package clock\n\ntype "+realClockType+" struct{}\n")
	writeGoFile(t, tree, filepath.Join("internal", "swap", "realclock.go"),
		"package swap\n\nimport c \""+module+"/"+clockRel+"\"\n\nvar _ = c."+realClockType+"{}\n")

	// The float, in a TEST file, because that is the half of the float ban the other checks do not
	// cover and the half a reviewer is least likely to notice.
	writeGoFile(t, tree, filepath.Join("internal", "swap", "drift_test.go"),
		"package swap\n\nvar rate "+bannedFloats()[1]+" = 0.8\n")

	a := newAudit(t, tree)
	require.Equal(t, module, a.module)

	swapPkg := a.pkg(swapRel)
	storePkg := a.pkg(storeRel)

	require.Equal(t, []string{swapPkg, a.pkg(ledgerRel), storePkg}, a.chainTo(t, swapPkg, storePkg),
		"the auditor must report the whole chain, not just that one exists: the chain is what tells "+
			"somebody which edge to break")

	dir, ok := a.packageDir(swapPkg)
	require.True(t, ok)

	found := a.imports(t, dir, false)
	for _, banned := range []string{timePath, randPath} {
		require.Contains(t, found, banned,
			"the direct-import check must see %q in the tainted tree, or "+
				"TestArch_Swap_ShippedFiles_DoNotImportTimeOrMathRand is checking nothing", banned)
	}

	require.Len(t, a.realClockUses(t, dir), 1,
		"the real-clock check must see the aliased construction, or an `import c \".../clock\"` walks "+
			"straight past TestArch_Swap_DoesNotConstructTheRealClock")

	require.Len(t, a.identUses(t, dir, true, bannedFloats()...), 1,
		"the float check must see the declaration in the tainted test file")

	// The clean half of the fixture. Without it the test would pass against an auditor that reported
	// a violation for everything.
	require.Nil(t, a.chainTo(t, storePkg, a.pkg(ledgerRel)),
		"%s imports nothing in the fixture, so there is no chain to find", storePkg)

	ledgerDir, ok := a.packageDir(a.pkg(ledgerRel))
	require.True(t, ok)
	require.Empty(t, a.realClockUses(t, ledgerDir),
		"the fixture's ledger package does not import the clock, so there is nothing to report")
	require.Empty(t, a.identUses(t, ledgerDir, true, bannedFloats()...),
		"the fixture's ledger package names no float")
}

// writeGoFile writes one file of the fabricated tree, creating its parent directories.
func writeGoFile(tb testing.TB, root, rel, body string) {
	tb.Helper()

	path := filepath.Join(root, rel)
	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(tb, os.WriteFile(path, []byte(body), 0o644))
}
