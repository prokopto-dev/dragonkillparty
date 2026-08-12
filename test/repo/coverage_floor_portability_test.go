package repo_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// `make test-coverage-floor` is the last target of `make check`, and until issue #111 it could not
// run on a Mac at all.
//
// The recipe passed the expected package count to awk as `-v want=$(… | wc -w)`, unquoted. BSD `wc`
// right-aligns its count in an eight-column field and GNU `wc` does not, so on a stock macOS the
// substitution expanded to `       6`, word-split into `-v want=` plus a separate argument `6`, and
// awk took `6` as its PROGRAM and the program text as a FILENAME:
//
//	awk: can't open file  	/^ok/ && /coverage:/ { …
//
// The target therefore died before evaluating a single coverage number, and with it `make check` —
// the one command AGENTS.md tells every contributor and agent to run before claiming a task is done.
// Linux CI was green throughout, which is exactly why it went unnoticed for so long: the gate was
// broken only where it is run by hand.
//
// The obvious objection to testing this is that asserting BSD/GNU portability needs the other
// userland present. It does not, if the userland is STAGED rather than required. Two shims:
//
//   - `wc` on PATH, delegating to the real `wc` and re-padding to BSD's shape — so the recipe is
//     exercised the way stock macOS runs it, on ANY host, including this repo's Linux runners;
//   - a stand-in for `$(GO)`, printing one canned coverage line per package argument. Nothing here
//     compiles Go. A real `go test -cover` would cost ~10s in a target budgeted at ~5s for the whole
//     unit suite, and would make the two negative cases below impossible to stage at all.
//
// Those negative cases are the point of the file as much as the positive one is. A quoting fix that
// left `want` empty, or that made the awk stop counting, would ALSO make the target exit 0 on
// macOS — passing this test's first subtest while silently enforcing nothing. So: a package below
// the floor must still fail, and a package that vanishes from the output must still fail.
const coverageFloorTarget = "test-coverage-floor"

var (
	// COVERAGE_FLOOR := 95
	coverageFloorRe = regexp.MustCompile(`(?m)^COVERAGE_FLOOR\s*:?=\s*(\d+)`)
	// COVERAGE_FLOOR_PACKAGES := ./a ./b \
	//                            ./c
	coverageFloorPackagesRe = regexp.MustCompile(`(?m)^COVERAGE_FLOOR_PACKAGES\s*:?=\s*((?:[^\n]*\\\n)*[^\n]*)`)
)

// coverageFloorSettings reads the floor and the package list out of the real Makefile.
//
// Read rather than duplicated: a hard-coded 95 here would keep passing after someone raised the
// floor, and a hard-coded package list would stop proving anything the day the list grew. The
// count assertion below is only meaningful if it tracks the list the recipe actually measures.
func coverageFloorSettings(t *testing.T) (floor int, packages []string) {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), makefileRel))
	require.NoError(t, err, "read the Makefile")

	fm := coverageFloorRe.FindSubmatch(body)
	require.NotNil(t, fm, "Makefile must declare COVERAGE_FLOOR")
	_, err = fmt.Sscanf(string(fm[1]), "%d", &floor)
	require.NoError(t, err, "parse COVERAGE_FLOOR %q", fm[1])

	pm := coverageFloorPackagesRe.FindSubmatch(body)
	require.NotNil(t, pm, "Makefile must declare COVERAGE_FLOOR_PACKAGES")
	packages = strings.Fields(strings.ReplaceAll(string(pm[1]), `\`, " "))

	// An empty list would make every assertion here vacuous, and would make the recipe's own
	// `seen != want` check compare 0 against 0 and pass having measured nothing.
	require.GreaterOrEqualf(t, len(packages), 2,
		"parsed only %d package(s) from COVERAGE_FLOOR_PACKAGES — the regexp is broken, not the tree", len(packages))
	for _, p := range packages {
		require.Truef(t, strings.HasPrefix(p, "./"), "package %q is not a ./ path", p)
		require.NotContainsf(t, p, "...", "%q is a wildcard — it expands to several packages "+
			"and one word, so the recipe's expected count would be wrong in the safe-looking direction", p)
	}

	return floor, packages
}

// writeShim writes an executable shell shim.
func writeShim(t *testing.T, path, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(body), 0o755), "write shim %s", path)
}

// bsdWordCountDir returns a directory holding a `wc` that pads like BSD's.
//
// It delegates to the REAL wc rather than counting on its own — the padding is the only thing being
// simulated, and a hand-rolled counter would be a second implementation to get wrong. The real
// binary is resolved here and baked in absolutely, because the shim shadows `wc` on PATH and would
// otherwise recurse into itself.
func bsdWordCountDir(t *testing.T) string {
	t.Helper()

	realWC, err := exec.LookPath("wc")
	require.NoError(t, err, "locate the real wc")

	dir := t.TempDir()
	writeShim(t, filepath.Join(dir, "wc"), fmt.Sprintf(`#!/bin/sh
# Test shim: BSD wc right-aligns its count in an eight-column field; GNU wc does not. Reproduce the
# BSD shape on any host so the recipe under test is exercised as a stock macOS runs it.
n=$(%q "$@" | tr -d '[:space:]')
printf '%%8s\n' "$n"
`, realWC))

	return dir
}

// goCoverStub returns the path of a stand-in for `go test -count=1 -cover <pkgs>`.
//
// It prints one coverage line per package argument, so it stays correct when COVERAGE_FLOOR_PACKAGES
// grows — the recipe's expected count is derived from that same list, and a stub with a fixed number
// of lines would start failing for a reason that has nothing to do with what is being tested.
//
// DKP_STUB_COVERAGE is the percentage every package reports. DKP_STUB_DROP_FIRST makes the first
// package print "[no test files]" instead, which is exactly what `go test` emits for a package whose
// tests were deleted.
func goCoverStub(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "go-cover-stub")
	writeShim(t, path, `#!/bin/sh
# Test shim: stands in for the compiler, not for the coverage. See coverage_floor_portability_test.go.
first=1
for a in "$@"; do
	case "$a" in
	./*) ;;
	*) continue ;;
	esac
	if [ -n "$DKP_STUB_DROP_FIRST" ] && [ "$first" = 1 ]; then
		first=0
		printf '?   \t%s\t[no test files]\n' "$a"
		continue
	fi
	first=0
	printf 'ok  \t%s\t0.10s\tcoverage: %s%% of statements\n' "$a" "$DKP_STUB_COVERAGE"
done
exit 0
`)

	return path
}

// runCoverageFloor runs the REAL `make test-coverage-floor` against staged commands.
//
// The real recipe is the subject: reimplementing its awk here would test a second copy that nobody
// runs. MAKEFLAGS and MFLAGS are cleared because this process is itself usually started by make, and
// an inherited jobserver flag makes the child warn on stderr and muddy the assertions.
func runCoverageFloor(t *testing.T, env ...string) (output string, exitCode int) {
	t.Helper()

	_, err := exec.LookPath("make")
	require.NoError(t, err, "make must be on PATH — it is how this repo's gates are run")

	cmd := exec.Command("make", "-C", repoRoot(t), coverageFloorTarget, "GO="+goCoverStub(t))
	cmd.Env = append(os.Environ(),
		"PATH="+bsdWordCountDir(t)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MAKEFLAGS=", "MFLAGS=",
	)
	cmd.Env = append(cmd.Env, env...)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	require.NoError(t, err, "run make %s\n%s", coverageFloorTarget, out)

	return string(out), -1
}

// TestCoverageFloor_PaddedWordCount_RecipeRunsAndStillEnforces is issue #111 in both directions: the
// target must survive a BSD-shaped `wc`, and it must still fail everything it failed before.
func TestCoverageFloor_PaddedWordCount_RecipeRunsAndStillEnforces(t *testing.T) {
	t.Parallel()

	floor, packages := coverageFloorSettings(t)

	t.Run("the recipe survives a padded word count", func(t *testing.T) {
		t.Parallel()

		out, code := runCoverageFloor(t, fmt.Sprintf("DKP_STUB_COVERAGE=%d.0", floor+3))

		// Named explicitly, because this is the failure the issue reported and a future
		// regression would produce it again verbatim rather than a plain non-zero exit.
		require.NotContains(t, out, "can't open file",
			"awk was handed its program as a filename — the word count word-split again (issue #111)\n%s", out)
		require.Zerof(t, code, "make %s must pass under a BSD-shaped wc\n%s", coverageFloorTarget, out)

		// The floor is only a floor if it measured every package. Asserting the COUNT, not just
		// the exit code, is what separates "passed" from "passed having checked nothing".
		require.Containsf(t, out, fmt.Sprintf("in %d package(s)", len(packages)),
			"expected all %d packages to be measured\n%s", len(packages), out)
	})

	t.Run("a package below the floor still fails", func(t *testing.T) {
		t.Parallel()

		out, code := runCoverageFloor(t, fmt.Sprintf("DKP_STUB_COVERAGE=%d.0", floor-1))

		require.NotZerof(t, code,
			"a package below the %d%% floor must fail the target — the floor is not enforced\n%s", floor, out)
		require.Containsf(t, out, fmt.Sprintf("below the %d%% coverage floor", floor),
			"the failure must name the floor it broke\n%s", out)
	})

	t.Run("a package whose tests vanished still fails", func(t *testing.T) {
		t.Parallel()

		// `go test` prints "[no test files]" rather than a coverage line, so a deleted suite
		// would drop out of the output entirely. The `seen != want` assertion is the only thing
		// standing between that and a green gate — and `want` is precisely the value the
		// quoting bug corrupted.
		out, code := runCoverageFloor(t,
			fmt.Sprintf("DKP_STUB_COVERAGE=%d.0", floor+3), "DKP_STUB_DROP_FIRST=1")

		require.NotZerof(t, code,
			"a package with no coverage line must fail the target, not silently shrink it\n%s", out)
		require.Containsf(t, out, fmt.Sprintf("measured %d package(s), expected %d", len(packages)-1, len(packages)),
			"the failure must report the shortfall\n%s", out)
	})
}

// wcInvocation matches a `wc` run in shell, as opposed to the word "wc" in prose.
var wcInvocation = regexp.MustCompile(`\bwc\b`)

// whitespaceStripped matches the two spellings this repo uses to normalise a count. `tr -d ' '` is
// enough inside `$( )` — command substitution eats the trailing newline, leaving only the BSD
// padding to strip — while `tr -d '[:space:]'` also holds outside one.
var whitespaceStripped = regexp.MustCompile(`tr\s+-d\s+["']?(\[:space:\]| )`)

// shellSources returns every hand-written shell surface a contributor runs locally.
//
// Scoped to the Makefile, scripts/ and the tracked git hooks deliberately: those are what run on a
// developer's machine, where the userland is whatever the OS ships. The workflows are shell too, but
// they only ever run on this repo's runners, so a GNU assumption there is an assumption about a
// pinned image rather than a portability bug.
func shellSources(t *testing.T) []string {
	t.Helper()

	root := repoRoot(t)
	files := []string{filepath.Join(root, makefileRel)}

	for _, pattern := range []string{"scripts/*.sh", ".githooks/*"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoErrorf(t, err, "glob %s", pattern)
		require.NotEmptyf(t, matches, "%s matched nothing — the glob is broken, not the tree", pattern)
		files = append(files, matches...)
	}

	return files
}

// TestShellSources_WordCountOutput_IsWhitespaceNormalised is the durable half of issue #111: it
// catches the NEXT occurrence rather than this one.
//
// The rule is deliberately blunt — every `wc` in a Makefile recipe or a script pipes through
// `tr -d` — because the subtle version does not survive review. Whether a given padded count is
// harmless depends on where it lands: word-splitting into an argument list breaks loudly (this
// issue), `test -ne` skipping leading blanks works by tolerance rather than by promise, and awk's
// handling of a padded `-v` assignment is implementation detail. "Strip it every time" is one rule
// with no judgement call in it, which is the only kind that holds.
func TestShellSources_WordCountOutput_IsWhitespaceNormalised(t *testing.T) {
	t.Parallel()

	var checked int

	for _, file := range shellSources(t) {
		body, err := os.ReadFile(file)
		require.NoErrorf(t, err, "read %s", file)

		rel, err := filepath.Rel(repoRoot(t), file)
		require.NoError(t, err, "relativise %s", file)

		for i, line := range strings.Split(string(body), "\n") {
			// Comments are where this fix gets EXPLAINED, so they name `wc` constantly.
			if trimmed := strings.TrimLeft(line, " \t"); strings.HasPrefix(trimmed, "#") {
				continue
			}
			if !wcInvocation.MatchString(line) {
				continue
			}

			checked++
			require.Truef(t, whitespaceStripped.MatchString(line),
				"%s:%d runs wc without stripping its whitespace. BSD wc right-aligns its count in "+
					"an eight-column field and GNU wc does not, so this reads differently on macOS "+
					"than in CI (issue #111). Pipe it through `tr -d '[:space:]'`.\n\t%s",
				rel, i+1, strings.TrimSpace(line))
		}
	}

	// A sweep that matched nothing would be a green test asserting the absence of a pattern it can
	// no longer find. Eleven call sites exist today.
	require.GreaterOrEqualf(t, checked, 8,
		"found only %d wc invocation(s) — the sweep is broken, not the tree", checked)
}
