// Negative fixtures for scripts/typed-laws.sh — the type-aware second opinion on the architectural
// laws (issue #172).
//
// The ANALYZERS are unit-tested in internal/repogate/typedlaw, against fabricated modules, one law
// at a time. This file is about the WIRING, which is where an advisory quietly stops being one:
// whether the script builds the binary and points it at the tree it was given, whether MODE=advise
// and MODE=enforce differ in the verdict and only in the verdict, whether a tree that could not be
// analysed hard-fails in both modes, and whether the pass is still ADDITIVE to `make lint-repo`
// rather than a replacement for it.
//
// The same two rules as everything else here: fixtures in t.TempDir(), and assert on the diagnostic
// rather than on the exit code alone. One difference, and it is the whole reason the second opinion
// is a second opinion — a fixture here must BUILD. internal/repogate's fixtures deliberately do not,
// which is why that engine reads a repository mid-sequence and blocks the merge, and this one
// advises.
package repo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// dotImportedNowModule is a buildable module whose only sin is the one issue #172 names first.
//
// `import . "time"` makes `Now()` a bare call. There is no selector, no `time.` prefix and nothing
// for CLOCK001 in internal/repogate — a `go/parser` rule looking for `<local>.Now` — to match. The
// tree compiles, it reads the wall clock, and the merge-blocking gate says nothing about it.
var dotImportedNowModule = map[string]string{
	"go.mod": "module example.com/tainted\n\ngo 1.26\n",
	"internal/service/service.go": `package service

import . "time"

// At reads the wall clock with no selector anywhere in the file.
func At() Time { return Now() }
`,
}

// writeFixtureModule writes a buildable module into a fixture tree and returns its root.
func writeFixtureModule(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for path, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))
	}

	return root
}

// TestTypedLaws_EnforceMode_FailsOnAFinding is issue #172's acceptance criterion driven end to end:
// the analyzers run under a driver over a real, buildable tree, and the verdict survives the shim.
//
// MODE=enforce rather than the CI default, and that is the point of having both: an advisory can
// only be TESTED through the mode that has a verdict. A fixture asserted through MODE=advise would
// pass whether the law fired or not.
func TestTypedLaws_EnforceMode_FailsOnAFinding(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("builds and type-checks a fixture module; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, dotImportedNowModule)

	out, code := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree, "MODE=enforce")

	require.Equalf(t, 1, code, "a dot-imported time.Now must fail the pass in MODE=enforce. Exit 1 "+
		"is a law that fired; exit 2 would be the pass failing to run at all, which is a different "+
		"bug and must not be mistaken for this one\n%s", out)
	require.Containsf(t, out, "CLOCK001",
		"the failure must name the rule id rather than only exiting non-zero\n%s", out)
	require.Containsf(t, out, "internal/service/service.go",
		"the failure must name the file — a finding without a position is one nobody can act on\n%s", out)
}

// TestTypedLaws_SeesWhatTheSyntaxGateCannot is the argument for the whole change, asserted rather
// than claimed.
//
// The SAME tree, through both engines. internal/repogate is pointed at it and reports nothing —
// correctly, on its own terms: there is no `time.Now` selector in that file. The type-aware pass
// reports CLOCK001. If this test ever goes green in the other direction, the second opinion has
// stopped being a second opinion and the case for maintaining it is gone.
func TestTypedLaws_SeesWhatTheSyntaxGateCannot(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs both gate engines over a fixture module; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, dotImportedNowModule)

	syntax, syntaxCode := runRootedScript(t, scriptPath(t, "repo-gates.sh"), tree)
	require.Zerof(t, syntaxCode, "internal/repogate must PASS this tree. It is not a bug there — a "+
		"dot-imported `. \"time\"` produces no selector for a syntax rule to match, and that is "+
		"exactly the class issue #172 bought the typed pass for. If the syntax rule has since "+
		"learned to see it, delete this test and say so\n%s", syntax)
	require.NotContains(t, syntax, "CLOCK001", "the syntax pass reported CLOCK001 on this tree")

	typed, typedCode := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree, "MODE=enforce")
	require.NotZerof(t, typedCode, "the type-aware pass must report the violation the syntax pass "+
		"cannot see. With both silent, this change buys nothing\n%s", typed)
	require.Containsf(t, typed, "CLOCK001", "the finding must name the id\n%s", typed)
}

// TestTypedLaws_AdviseMode_ReportsAndExitsZero is the advisory half, and the half most likely to rot
// into `continue-on-error` or into a gate with teeth nobody meant to give it.
//
// The mode changes the VERDICT and nothing else: the same finding is printed, with its id and its
// position, plus the `::warning::` annotation that puts it on the PR's Files-changed tab.
func TestTypedLaws_AdviseMode_ReportsAndExitsZero(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("builds and type-checks a fixture module; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, dotImportedNowModule)

	out, code := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree, "MODE=advise")

	require.Zerof(t, code, "MODE=advise must exit 0 on a finding — advisory BY CONSTRUCTION is what "+
		"lets ci.yml keep `continue-on-error` banned\n%s", out)
	require.Containsf(t, out, "CLOCK001",
		"advisory must still REPORT. A step that exits 0 without printing the finding is a step "+
			"nobody reads, which is the same as no step\n%s", out)
	require.Containsf(t, out, "::warning",
		"the advisory must emit a GitHub Actions annotation, or the findings live only in a log "+
			"nobody opens\n%s", out)
}

// TestTypedLaws_UnbuildableTree_HardFailsInBothModes is the limit of "advisory".
//
// A pass that exits 0 because it never ran is worse than no pass — the rule scripts/migrate-lint.sh
// states for atlas and `make govulncheck` states for its binary. Advisory covers a VERDICT about
// code; it never covers code that was never read. Exit 2, in both modes, distinct from the 1 that
// means a law fired.
func TestTypedLaws_UnbuildableTree_HardFailsInBothModes(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("builds a fixture module; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, map[string]string{
		"go.mod":                      "module example.com/tainted\n\ngo 1.26\n",
		"internal/service/service.go": "package service\n\nfunc Broken() int { return \"not an int\" }\n",
	})

	for _, mode := range []string{"advise", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			out, code := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree, "MODE="+mode)

			require.Equalf(t, 2, code, "a tree that does not build must exit 2 in MODE=%s. Exit 0 "+
				"would report 'no findings' about code the pass never read, and exit 1 would be "+
				"indistinguishable from a law firing\n%s", mode, out)
			require.Containsf(t, out, "could not analyse",
				"the failure must say the pass could not run, not merely fail\n%s", out)
		})
	}
}

// TestTypedLaws_NoModule_HardFails is the same limit from the other end, and it is the property that
// decides which of the two engines blocks a merge.
//
// internal/repogate's negative fixtures are trees with no go.mod at all — that is what
// TestLintShell_* and gates_test.go write — and it reads them happily. This pass cannot, and says
// so. Nothing about the split is a preference: it is why `make lint-repo` is the gate.
func TestTypedLaws_NoModule_HardFails(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the pass over a fixture tree; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, map[string]string{
		"internal/service/service.go": "package service\n",
	})

	out, code := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree)
	require.Equalf(t, 2, code, "a tree with no Go module must exit 2 rather than report clean\n%s", out)
}

// TestTypedLaws_CleanTree_Passes is the control. A pass that fires on everything gets turned off
// rather than obeyed, and this repository's own tree is the one it has to be quiet about.
func TestTypedLaws_CleanTree_Passes(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("builds and type-checks a fixture module; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, map[string]string{
		"go.mod": "module example.com/tainted\n\ngo 1.26\n",
		"internal/strategy/plan.go": `package strategy

// Plan is pure integer arithmetic over its arguments.
func Plan(points, share int64) int64 { return points * share }
`,
	})

	out, code := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree, "MODE=enforce")

	require.Zerof(t, code, "a clean module must pass even in MODE=enforce\n%s", out)
	require.Containsf(t, out, "no findings", "the pass must say what it checked\n%s", out)
}

// TestTypedLaws_BadMode_FailsRatherThanDefaulting — a typo in MODE must not silently become the
// permissive mode.
//
// The direction matters: defaulting an unrecognised value to `advise` means a CI step written as
// MODE=enforce with a typo runs advisory forever, green, and nobody finds out.
func TestTypedLaws_BadMode_FailsRatherThanDefaulting(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	tree := writeFixtureModule(t, dotImportedNowModule)

	out, code := runRootedScript(t, scriptPath(t, "typed-laws.sh"), tree, "MODE=warn")

	require.NotZerof(t, code, "an unrecognised MODE must fail rather than fall back to the "+
		"permissive one\n%s", out)
	require.Containsf(t, out, "not a mode", "the failure must say what was wrong with the value\n%s", out)
}

// TestTypedLaws_AreAdditive is issue #172's acceptance criterion "the rules in internal/repogate are
// unchanged and still merge-blocking", asserted in code rather than promised in a comment.
//
// Two halves, and both can rot silently:
//
//  1. Every id the typed pass carries is STILL a rule in internal/repogate. The failure mode is a
//     later cleanup that reads the two catalogues, calls one a duplicate, and deletes the rule from
//     the engine that blocks the merge — leaving the law enforced only by a step that exits 0.
//  2. `make lint-repo` is still a `lint` prerequisite, and so still in `make check`.
//
// SQL004 is the one id with no twin, and that is the finding class issue #172's acceptance asks to
// be accounted for: "*sql.DB is held only by internal/store" is a statement about a TYPE, and a
// syntax pass cannot make a rule out of it — matching the literal `*sql.DB` is exactly what an alias
// walks past. The reason lives in internal/repogate/typedlaw's package doc; the row below is what
// makes deleting it a decision.
func TestTypedLaws_AreAdditive(t *testing.T) {
	t.Parallel()

	// The ids with no twin in internal/repogate, and why. A new one needs a row and an argument.
	typedOnly := map[string]string{
		"SQL004": "law 2 is a statement about a TYPE; a syntax rule can only match the literal " +
			"`*sql.DB`, which an alias, a named type or an embedded field defeats",
	}

	laws := readRepoFile(t, "internal/repogate/typedlaw/laws.go")
	engine := readRepoFile(t, "internal/repogate/ast.go")

	for _, id := range []string{
		"ROUTE001", "SQL001", "SQL002", "SQL003", "SQL004",
		"PURE001", "PURE002", "CLOCK001", "CLOCK002", "MONEY001",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			require.Containsf(t, laws, `"`+id+`"`,
				"%s is no longer a law in internal/repogate/typedlaw, so this table is stale", id)

			if why, ok := typedOnly[id]; ok {
				require.NotEmpty(t, why)

				return
			}

			require.Containsf(t, engine, `id: "`+id+`"`,
				"%s is a typed law with no rule of the same id in internal/repogate. The typed pass "+
					"is ADDITIVE and advisory: it exits 0 on a finding. A law enforced only here is "+
					"a law nothing blocks a merge on. Either restore the rule or add a row to "+
					"typedOnly with the reason a syntax rule cannot express it (issue #172).", id)
		})
	}

	makefile := readRepoFile(t, "Makefile")
	require.Regexpf(t, `(?m)^lint:.*\blint-repo\b`, makefile,
		"`make lint` no longer runs lint-repo, so the merge-blocking half of the pair is gone and "+
			"the advisory half is all that is left")
	require.Regexpf(t, `(?m)^lint:.*\blint-laws\b`, makefile,
		"`make lint` no longer runs lint-laws, so the type-aware pass runs nowhere a contributor "+
			"would see it (issue #172)")
}

// TestTypedLaws_NotLinkedIntoTheBinary holds the same line internal/licence and internal/repogate
// hold: repo tooling never reaches the shipped binary.
//
// cmd/dkp is the only shipped binary, and a gate engine inside it would ship a type checker, a
// package loader and a set of rules to every operator who runs `dkp serve`.
func TestTypedLaws_NotLinkedIntoTheBinary(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to `go list -deps`; runs under `make test`")
	}

	const typed = "internal/repogate/typedlaw"

	for _, dep := range goListDeps(t, []string{"./cmd/dkp"}) {
		require.NotContainsf(t, dep, typed,
			"cmd/dkp's package graph reaches %s. Repo tooling must never ship: the binary would "+
				"carry a package loader, a type checker and the architectural rules to every "+
				"operator who runs it.", dep)
	}
}

// TestTypedLaws_ScriptIsWiredIntoCI keeps the pass from becoming a target nothing invokes.
//
// Issue #172's own comment names this failure mode — "a dkpvet landing without them would be a
// binary nothing invokes: the shape of dead code that reads as coverage". The step lives in
// `lint / go` because that job already gates on the `code` filter and already has the module built.
func TestTypedLaws_ScriptIsWiredIntoCI(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	require.Containsf(t, workflow, "make lint-laws",
		"no CI step runs `make lint-laws`. An analyzer nothing invokes is dead code that reads as "+
			"coverage (issue #172)")

	// In the job gated on `code`: the pass reads Go sources and nothing else can change its verdict.
	patterns := pathFilterPatterns(t, workflow, "code")
	require.True(t, selects(patterns, "internal/strategy/plan.go"),
		"the `code` filter must select Go sources, or the job carrying the typed pass skips on the "+
			"only change that can affect it")

	require.Containsf(t, readRepoFile(t, "scripts/typed-laws.sh"), "MODE=enforce",
		"scripts/typed-laws.sh must keep the enforce path. It is the only mode with a verdict, so "+
			"it is the only mode the fixtures above can assert through — an advisory whose enforce "+
			"path was deleted could not be tested at all")

	// The step must be a plain `run:`, not a `continue-on-error` one. That is asserted repo-wide by
	// TestCIWorkflow_NoContinueOnError, which reads the workflow properly rather than grepping a
	// file whose comments discuss the string — including the ones this step carries.
}
