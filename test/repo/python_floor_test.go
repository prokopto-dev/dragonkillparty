package repo_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The repository's Python floor, asserted rather than assumed.
//
// `make docs-links` is Python, and until issue #83 nothing declared what interpreter it needed. The
// script that actually broke was scripts/verify-spec.py: it annotated a return as `dict | None`,
// which PEP 604 only made legal in 3.10 and which is evaluated when the function is DEFINED, so on
// macOS's stock 3.9.6 the module raised TypeError at import.
//
// The cost was not the version bump. It was WHERE the failure surfaced: `make check` reported the
// SPEC GATE failing, on a tree whose spec was fine, pointing at openapi/openapi.json — the one file
// nobody is allowed to hand-edit. The natural next move is to go hunting for drift that does not
// exist. An environment fault wearing a content fault's clothes is the most expensive shape a gate
// failure has.
//
// THAT PARTICULAR GATE IS GO NOW — issue #127 moved it to internal/specgate, so the spec can no
// longer be blamed for an interpreter. What is asserted here is unchanged and still load-bearing: the
// failure shape was never specific to the spec, and every remaining scripts/*.py is a gate or a
// publisher that can fail the same way. The floor leaves this file when the last of them does, not
// before.
//
// Four assertions, each a different way for it to come back:
//
//  1. every scripts/*.py carries `from __future__ import annotations`, so no annotation is ever
//     evaluated at runtime and the whole PEP 604 / PEP 585 class of import-time failure is closed;
//  2. every scripts/*.py declares the floor and refuses to run below it, with a message that says
//     what is wrong — a named refusal instead of a traceback;
//  3. every scripts/*.py still PARSES at the floor. This is the one that catches the next
//     occurrence rather than this one: CI runs a newer interpreter than the floor, so `match`, the
//     walrus in a comprehension, or any other 3.10-only syntax would sail through CI and fail only
//     on a contributor's laptop;
//  4. the floor is the SAME number in the .py files, in the Makefile and in scripts/subset-fonts.sh,
//     and CI declares a floor at or above it. Two floors in one repository is the same defect again.
const (
	pythonScriptsDir  = "scripts"
	makefileRel       = "Makefile"
	subsetFontsRel    = "scripts/subset-fonts.sh"
	toolchainStepsRel = ".github/actions/setup-toolchain/action.yml"
)

var (
	// MINIMUM_PYTHON = (3, 9)
	pythonFloorRe = regexp.MustCompile(`(?m)^MINIMUM_PYTHON\s*=\s*\(\s*(\d+)\s*,\s*(\d+)\s*\)`)
	// PYTHON_REQUIRED := 3.9
	makefileFloorRe = regexp.MustCompile(`(?m)^PYTHON_REQUIRED\s*:?=\s*(\d+)\.(\d+)`)
	// sys.version_info >= (3, 9), as scripts/subset-fonts.sh spells its own check.
	shellFloorRe = regexp.MustCompile(`sys\.version_info\s*>=\s*\(\s*(\d+)\s*,\s*(\d+)\s*\)`)
	// DKP_PYTHON_CI_FLOOR: "3.10"
	ciFloorRe = regexp.MustCompile(`DKP_PYTHON_CI_FLOOR:\s*"(\d+)\.(\d+)"`)
)

// pythonScripts returns every scripts/*.py, repo-relative. A floor is not a floor if a script can
// be added without one, so the list is discovered rather than enumerated here.
func pythonScripts(t *testing.T) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(repoRoot(t), pythonScriptsDir, "*.py"))
	require.NoError(t, err, "glob scripts/*.py")

	// A floor asserted over an empty set is a green test that checks nothing.
	//
	// ONE exists today — check-links.py — where this said three a fortnight ago. Epic #125 took the
	// other two in quick succession: issue #127 moved verify-spec.py to internal/specgate, and issue
	// #126 replaced dc-publish.py with internal/mockup. The number tracks the tree rather than
	// guarding it, so lowering it is not a loosening: what it exists to catch is a broken glob
	// returning nothing, and a count of one catches that exactly as well as a count of three did —
	// every assertion below still runs over every script the glob returns.
	//
	// When check-links.py goes too, this whole file goes with it rather than the floor being asserted
	// over nothing. Deleting it is then the correct move and not a weakening, because there is no
	// Python left to declare a floor.
	require.GreaterOrEqualf(t, len(matches), 1,
		"found only %d scripts/*.py — the glob is broken, not the tree", len(matches))

	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, filepath.ToSlash(filepath.Join(pythonScriptsDir, filepath.Base(m))))
	}

	return out
}

// declaredFloor extracts (major, minor) with re, which must have exactly two capture groups.
func declaredFloor(t *testing.T, re *regexp.Regexp, body, what string) (int, int) {
	t.Helper()

	m := re.FindStringSubmatch(body)
	require.Lenf(t, m, 3, "%s does not declare a Python floor matching %s", what, re)

	var major, minor int
	_, err := fmt.Sscanf(m[1]+"."+m[2], "%d.%d", &major, &minor)
	require.NoErrorf(t, err, "parse the Python floor %q.%q declared in %s", m[1], m[2], what)

	return major, minor
}

// TestPythonFloor_EveryScriptDeclaresAndEnforcesIt covers assertions 1, 2 and 4.
func TestPythonFloor_EveryScriptDeclaresAndEnforcesIt(t *testing.T) {
	t.Parallel()

	wantMajor, wantMinor := declaredFloor(t, makefileFloorRe, readRepoFile(t, makefileRel), makefileRel)

	for _, rel := range pythonScripts(t) {
		body := readRepoFile(t, rel)

		// The future import must be present AND must precede every other statement, which is a
		// language rule rather than a style one: it is only legal directly after the docstring, and
		// an annotation evaluated before it takes effect is exactly the bug (#83).
		require.Containsf(t, body, "from __future__ import annotations",
			"%s must carry `from __future__ import annotations`. Without it an annotation like "+
				"`-> dict | None` is EVALUATED when the function is defined and raises TypeError on "+
				"an interpreter below 3.10 — at import, before the script reads anything, so the "+
				"failure looks like the gate's subject is broken rather than the environment.", rel)

		major, minor := declaredFloor(t, pythonFloorRe, body, rel)
		require.Equalf(t, [2]int{wantMajor, wantMinor}, [2]int{major, minor},
			"%s declares MINIMUM_PYTHON = (%d, %d) but %s declares PYTHON_REQUIRED := %d.%d. "+
				"One floor per repository: two numbers means one of them is the one nobody checks.",
			rel, major, minor, makefileRel, wantMajor, wantMinor)

		// Declaring is not checking. The guard is what turns a traceback into a sentence.
		require.Containsf(t, body, "if sys.version_info < MINIMUM_PYTHON:",
			"%s declares MINIMUM_PYTHON but does not act on it. A floor nothing enforces is a "+
				"comment: the interpreter that is first on PATH still decides, and it still fails "+
				"in the confusing direction.", rel)
		require.Containsf(t, body, "sys.exit(2)",
			"%s must exit 2 when the interpreter is too old, not 1. Every gate in this repo uses a "+
				"non-zero exit for `your tree is wrong`; 2 is reserved here for `this cannot run at "+
				"all`, which is the distinction issue #83 was about.", rel)
	}

	// scripts/subset-fonts.sh predates all of this and carries its own inline guard. It is the same
	// floor and must stay the same floor.
	sMajor, sMinor := declaredFloor(t, shellFloorRe, readRepoFile(t, subsetFontsRel), subsetFontsRel)
	require.Equalf(t, [2]int{wantMajor, wantMinor}, [2]int{sMajor, sMinor},
		"%s checks for Python %d.%d but the repository floor is %d.%d",
		subsetFontsRel, sMajor, sMinor, wantMajor, wantMinor)
}

// TestPythonFloor_EveryScriptParsesAtTheFloor covers assertion 3 — the one that catches the NEXT
// occurrence.
//
// CI runs a newer interpreter than the floor, deliberately (see the setup-toolchain action). That
// asymmetry is only safe if something holds the scripts to the floor's SYNTAX, because otherwise a
// `match` statement passes every CI job and fails on the machine of whoever runs `make check` on a
// stock macOS interpreter. ast.parse's feature_version is exactly that check: it is the CPython
// parser told to reject anything newer than the target.
//
// It does NOT catch a runtime-evaluated `dict | None`, which is a type error rather than a syntax
// error — the future-import assertion above is what closes that half. Between them the two cover the
// class.
func TestPythonFloor_EveryScriptParsesAtTheFloor(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to python3; run `make test` or `make check`")
	}

	major, minor := declaredFloor(t, makefileFloorRe, readRepoFile(t, makefileRel), makefileRel)

	// NO VACUOUS PASS. If feature_version stopped rejecting anything — an interpreter that ignores
	// it, a typo in the harness — every assertion below would pass on any input at all. So the
	// harness is first proved able to fail, on syntax that is 3.10-only by construction.
	//
	// A `match` statement, because it is the other 3.10 feature issue #83 named and because it is
	// unambiguously newer than the floor. If the floor is ever raised past 3.10 this control needs a
	// newer sample, and it will say so by failing rather than by quietly passing.
	require.Less(t, minor, 10,
		"the floor reached 3.10, so a `match` statement is no longer a valid negative control for "+
			"the parse check — replace it with syntax newer than the floor")

	control := filepath.Join(t.TempDir(), "control.py")
	require.NoError(t, os.WriteFile(control,
		[]byte("def f(x):\n    match x:\n        case 1:\n            return 'one'\n    return None\n"), 0o600))

	out, err := parseAtFloor(t, control, major, minor)
	require.Errorf(t, err,
		"a `match` statement parsed cleanly at %d.%d, so this test's harness cannot fail and the "+
			"assertions below prove nothing. Output: %s", major, minor, out)

	for _, rel := range pythonScripts(t) {
		out, err := parseAtFloor(t, filepath.Join(repoRoot(t), filepath.FromSlash(rel)), major, minor)
		require.NoErrorf(t, err,
			"%s does not parse as Python %d.%d, which is the floor this repository declares "+
				"(Makefile PYTHON_REQUIRED, and the guard in the script itself).\n%s\n"+
				"CI runs a newer interpreter, so this would have been green in CI and red on a "+
				"contributor's laptop. Either rewrite the construct, or raise the floor everywhere "+
				"at once — the Makefile, every scripts/*.py, scripts/subset-fonts.sh and "+
				"docs/development/inner-loop.md.",
			rel, major, minor, out)
	}
}

// parseAtFloor asks CPython to parse path with the parser restricted to major.minor.
func parseAtFloor(t *testing.T, path string, major, minor int) (string, error) {
	t.Helper()

	// The source is read by the child rather than interpolated into it: a repo path can contain a
	// quote, and a harness that mangles its own input reports syntax errors that are its own.
	prog := fmt.Sprintf(`
import ast, sys
src = open(sys.argv[1], encoding="utf-8").read()
ast.parse(src, filename=sys.argv[1], feature_version=(%d, %d))
`, major, minor)

	cmd := exec.Command("python3", "-c", prog, path)

	out, err := cmd.CombinedOutput()

	return strings.TrimSpace(string(out)), err
}

// TestPythonFloor_CIDeclaresItsOwnInterpreter covers the CI half of issue #83.
//
// ci.yml's spec-drift job used to carry the comment "needs only python3, which the runner has". That
// was true and useless: it recorded an ASSUMPTION about the runner image, so the day it stopped
// holding, the symptom would have been the spec gate failing rather than a missing dependency.
//
// CI's floor is deliberately HIGHER than the scripts' floor. The scripts must run on a stock macOS
// interpreter; CI should run a current one. What makes the gap safe is the parse test above, not
// good intentions.
func TestPythonFloor_CIDeclaresItsOwnInterpreter(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainStepsRel)

	require.Contains(t, action, "python:",
		"the setup-toolchain action must expose a `python` input — a job that runs a Python gate "+
			"needs a way to say so")

	ciMajor, ciMinor := declaredFloor(t, ciFloorRe, action, toolchainStepsRel)
	major, minor := declaredFloor(t, makefileFloorRe, readRepoFile(t, makefileRel), makefileRel)

	require.Truef(t, ciMajor > major || (ciMajor == major && ciMinor >= minor),
		"CI's Python floor (%d.%d, in %s) is BELOW the floor the scripts require (%d.%d). CI would "+
			"then be the environment that cannot run the gates.", ciMajor, ciMinor, toolchainStepsRel, major, minor)

	// Every job that runs a Python target must ask for the check. Named individually rather than
	// scanned for, because the failure mode is a job that quietly stops asking.
	//
	// `spec-drift:` was in this list until issue #127. It is out because `make verify-spec` is Go now
	// and a job that no longer runs Python must not be required to declare an interpreter — an
	// assertion that is true of nothing is how a list like this stops meaning anything. The gate it
	// covers did not move: spec-drift is still required, still runs on every PR, and is now gated on
	// the `go` path filter through the package it runs.
	workflow := readCIWorkflow(t)

	for job, why := range map[string]string{
		"docs-build:":       "runs `make docs-links`, which is scripts/check-links.py",
		"test-integration:": "runs `make test`, whose test/repo suite execs python3 directly",
	} {
		require.Containsf(t, jobBlock(t, workflow, job), `python: "true"`,
			"ci.yml's %s job %s, so it must ask setup-toolchain for the python check", job, why)
	}
}
