// Negative fixtures for scripts/lint-shell.sh — the shell gate (issue #122).
//
// gates_test.go's two rules again: fixtures in t.TempDir() only, and assert on the diagnostic rather
// than on the exit code alone.
//
// The fixtures are the two shapes issue #122's acceptance criterion names:
//
//	SC2086  an unquoted command substitution feeding awk. This is #111 verbatim — it killed
//	        `make test-coverage-floor` on macOS, where BSD `wc` right-aligns its count in an
//	        eight-column field, so the expansion word-split and awk took a stray `6` as its PROGRAM.
//	SC2005  `echo $(cmd)` — the "useless-echo no-op" of the acceptance criterion.
//
// #122 also cites #84, whose readyz probe printed "reachable" on both branches of an `&& ... ||`.
// There is deliberately no fixture for it: shellcheck does not report that shape at any severity,
// and asserting it here would be a test written to a claim rather than to the tool. What caught #84
// was a test for the probe, and it is still what would.
//
// Plus the formatting half, because `make fmt` and `make lint-shell` share one flag set and a drift
// between them would make the fix unavailable exactly when the gate sends you to it.
package repo_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFixtureScript writes an executable shell script into a fixture tree's scripts/ directory.
func writeFixtureScript(t *testing.T, tree, name, body string) {
	t.Helper()

	dir := filepath.Join(tree, "scripts")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755))
}

// The #111 shape: an unquoted `$(...)` passed to awk. SC2086.
const wordSplittingScript = `#!/usr/bin/env bash
set -euo pipefail

count=$(printf '%s\n' one two three | wc -w)
printf 'a b c\n' | awk -v want=$count '{ print NF, want }'
`

// The useless echo: `echo $(cmd)` runs the command, splits its output on whitespace and prints the
// pieces — a no-op wearing a probe's clothes, and SC2005 (plus SC2046 for the splitting).
const uselessEchoScript = `#!/usr/bin/env bash
set -euo pipefail

echo $(curl -fsS http://127.0.0.1:8080/readyz)
`

// Correct, and formatted the way the gate's flags produce. The control.
const cleanScript = `#!/usr/bin/env bash
set -euo pipefail

count="$(printf '%s\n' one two three | wc -w | tr -d '[:space:]')"
if printf 'a b c\n' | awk -v want="$count" '{ exit NF == want ? 0 : 1 }'; then
    printf 'fields match\n'
else
    printf 'fields differ\n'
fi
`

// requireLintShellToolchain skips on a laptop without the tools and FAILS in CI (issue #177).
func requireLintShellToolchain(t *testing.T) {
	t.Helper()

	const why = "ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` must pass " +
		"shellcheck and shfmt in setup-toolchain's tools: input"

	requireTool(t, "shellcheck", why)
	requireTool(t, "shfmt", why)
}

// TestLintShell_WordSplitting_FailsGate is issue #122's acceptance criterion, in the shape issue
// #111 actually took: an unquoted expansion must fail the gate, naming SC2086.
func TestLintShell_WordSplitting_FailsGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	writeFixtureScript(t, tree, "coverage-floor.sh", wordSplittingScript)

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)

	require.NotZerof(t, code, "an unquoted expansion feeding awk must fail the gate — this is "+
		"issue #111, which took `make test-coverage-floor` out on macOS\n%s", out)
	require.Containsf(t, out, "SC2086",
		"the failure must name SC2086 rather than only exiting non-zero: a wrong DKP_REPO_ROOT "+
			"also exits non-zero\n%s", out)
}

// TestLintShell_UselessEcho_FailsGate is the second half of #122's acceptance criterion.
func TestLintShell_UselessEcho_FailsGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	writeFixtureScript(t, tree, "smoke.sh", uselessEchoScript)

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)

	require.NotZerof(t, code, "a useless echo — `echo $(cmd)`, which splits the command's output "+
		"and prints the pieces — must fail the gate\n%s", out)
	require.Containsf(t, out, "SC2005",
		"the failure must name SC2005, the rule that caught it, rather than only exiting "+
			"non-zero\n%s", out)
}

// TestLintShell_UnformattedScript_FailsGate covers the shfmt half. Formatting drift is not a
// correctness bug, which is exactly why nothing catches it without a gate: a reviewer reads past it
// and the tree ends up with three indentation styles, as this one had before issue #122.
func TestLintShell_UnformattedScript_FailsGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	// Correct shell, wrong shape: a two-space indent where the policy is four.
	writeFixtureScript(t, tree, "indent.sh", "#!/usr/bin/env bash\nset -euo pipefail\n\nif true; then\n  printf 'yes\\n'\nfi\n")

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)

	require.NotZerof(t, code, "a script that is not shfmt-clean must fail the gate\n%s", out)
	require.Containsf(t, out, "make fmt",
		"the failure must name the command that fixes it — a formatting gate whose message does "+
			"not is a gate people work around\n%s", out)
}

// TestLintShell_WriteMode_MakesTheGatePass proves `make fmt` and `make lint-shell` share one flag
// set. If they ever drift, --write produces a tree the check rejects, and the person following the
// instruction the failure printed ends up in a loop.
func TestLintShell_WriteMode_MakesTheGatePass(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	writeFixtureScript(t, tree, "indent.sh", "#!/usr/bin/env bash\nset -euo pipefail\n\nif true; then\n  printf 'yes\\n'\nfi\n")

	_, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)
	require.NotZero(t, code, "the fixture must start out failing, or this test proves nothing")

	out, code := runRootedScriptArgs(t, scriptPath(t, "lint-shell.sh"), tree, []string{"--write"})
	require.Zerof(t, code, "--write must succeed\n%s", out)

	out, code = runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)
	require.Zerof(t, code, "after --write the same tree must pass the check — `make fmt` and "+
		"`make lint-shell` must not be able to disagree\n%s", out)
}

// TestLintShell_NoScripts_FailsRatherThanPassesVacuously — an empty tree means the invocation is
// broken, not that every script is clean.
func TestLintShell_NoScripts_FailsRatherThanPassesVacuously(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), t.TempDir())

	require.NotZerof(t, code, "a tree with no shell scripts must FAIL the gate\n%s", out)
	require.Containsf(t, out, "no shell scripts found",
		"the failure must say the gate found nothing to lint\n%s", out)
}

// TestLintShell_CoversTheGitHooks asserts the gate reads .githooks/ as well as scripts/.
//
// The hooks have no .sh suffix, so an extension-based enumeration would miss them — and they are the
// two shell files least likely to be noticed when they rot, because a hook that fails is a hook
// people bypass with --no-verify rather than fix. scripts/lint-shell.sh selects by SHEBANG for that
// reason, and this fixture is what stops a "tidy-up" turning it back into a glob.
func TestLintShell_CoversTheGitHooks(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	// A clean script under scripts/, so the run is not vacuous, and the violation in .githooks only.
	writeFixtureScript(t, tree, "clean.sh", cleanScript)

	hooks := filepath.Join(tree, ".githooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-push"), []byte(wordSplittingScript), 0o755))

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)

	require.NotZerof(t, code, "a violation in .githooks/pre-push must fail the gate: the hooks are "+
		"shell too, and they carry no .sh suffix\n%s", out)
	require.Containsf(t, out, "pre-push", "the failure must name the hook file\n%s", out)
}

// failOpenCdScript is the SC2164 shape, in the file where it costs most.
//
// `cd ... ` with no `|| exit` is the fail-open defect of a guard: when the cd fails the script keeps
// going in whatever directory it was invoked from, every relative path below it resolves to nothing,
// and the guard reports on a tree it never read. This is the exact finding the gate reported the
// first time it was pointed at .claude/hooks/ (issue #187) — in test-guard-bash.sh, the self-test
// for the command guard.
const failOpenCdScript = `#!/usr/bin/env bash
set -uo pipefail

cd "$(dirname "$0")/../.."
GUARD=.claude/hooks/guard-bash.sh
printf '%s\n' "$GUARD"
`

// TestLintShell_CoversTheClaudeHooks closes issue #187, and it is TestLintShell_CoversTheGitHooks's
// sibling for the tree that matters more than its file count suggests.
//
// .claude/hooks/ is five shell files and two of them decide whether a tool call runs at all:
// guard-bash.sh blocks the unrecoverable, publishing and destructive commands, and
// guard-protected-paths.sh blocks edits to protected paths. guard-bash.sh is FAIL-OPEN BY DESIGN —
// its own header says a missing JSON parser or an unparseable payload allows the command — so a
// shell defect there is a guard that stops guarding with nothing going red. That is the defect class
// the gate was bought for (#111), in the one tree nothing was linting.
//
// The fixture is deliberately the fail-open shape rather than any old finding: it fails the gate,
// and it fails it for the reason the issue is about.
func TestLintShell_CoversTheClaudeHooks(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	// A clean script under scripts/, so the run is not vacuous and the violation is in the hooks.
	writeFixtureScript(t, tree, "clean.sh", cleanScript)

	hooks := filepath.Join(tree, ".claude", "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "guard-bash.sh"), []byte(failOpenCdScript), 0o755))

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)

	require.NotZerof(t, code, "a fail-open `cd` in .claude/hooks/guard-bash.sh must fail the gate. "+
		"That tree was in no filter and no enumeration until issue #187, and it holds the two guards "+
		"that decide whether a tool call runs at all\n%s", out)
	require.Containsf(t, out, "SC2164",
		"the failure must name SC2164 — the rule that reports a `cd` whose failure is ignored — "+
			"rather than only exiting non-zero: a wrong DKP_REPO_ROOT also exits non-zero\n%s", out)
	require.Containsf(t, out, "guard-bash.sh", "the failure must name the hook file\n%s", out)
}

// TestLintShell_EnumeratesTheRealCommandGuard is the other half, and the half a fixture cannot give.
//
// Everything above proves the gate reads a `.claude/hooks/` directory somebody fabricated. This
// proves it reads THE one, in this checkout, with the real guard in it — the failure mode being a
// path that still parses, still lints five files, and lints five files somewhere else. The count is
// re-derived here rather than read out of the script, so a change to the enumeration has to be a
// change in two places that agree.
func TestLintShell_EnumeratesTheRealCommandGuard(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script over this checkout; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	root := repoRoot(t)

	// The guard itself must exist, or the row below protects a file nobody has.
	guard := filepath.Join(root, ".claude", "hooks", "guard-bash.sh")
	_, err := os.Stat(guard)
	require.NoErrorf(t, err, "%s no longer exists — issue #187's whole argument was that this file "+
		"is fail-open and unlinted, so a rename needs this assertion moved with it", guard)

	want := countShellScripts(t, root, "scripts", ".githooks", filepath.Join(".claude", "hooks"))
	require.GreaterOrEqual(t, want, 30, "the enumeration found suspiciously few scripts")

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), root)
	require.Zerof(t, code, "the real tree must pass its own shell gate\n%s", out)

	require.Containsf(t, out, fmt.Sprintf("%d script(s)", want),
		"the gate linted a different number of scripts than the three trees hold (%d). It reports "+
			"the count precisely so this can be checked: a glob that quietly stopped selecting "+
			"`.claude/hooks/` would leave every fixture above passing while the fail-open command "+
			"guard went unread (issue #187)\n%s", want, out)
}

// countShellScripts counts the files under the given repo-relative directories whose first line is a
// shell shebang — the same selection scripts/lint-shell.sh makes, written independently.
func countShellScripts(t *testing.T, root string, dirs ...string) int {
	t.Helper()

	count := 0

	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		require.NoErrorf(t, err, "%s is one of the trees the shell gate enumerates", dir)

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			body, err := os.ReadFile(filepath.Join(root, dir, entry.Name()))
			require.NoError(t, err)

			first, _, _ := strings.Cut(string(body), "\n")
			if strings.HasPrefix(first, "#!") && strings.HasSuffix(strings.TrimSpace(first), "sh") {
				count++
			}
		}
	}

	return count
}

// TestGitHooks_PrePush_BlocksAShellFinding is the "wire it into .githooks pre-push so it fails
// locally first" half of issues #121 and #122, and it is a separate assertion from the gate's own
// fixtures for a reason: the hook resolves its tools differently (through `tool`, which also looks
// in GOPATH/bin), captures the gate's output rather than streaming it, and — the part most likely to
// rot — decides for itself whether to run each check at all. A hook that quietly stopped calling the
// gate would leave every fixture above passing.
//
// The fixture is a git repository in t.TempDir() carrying the real gate script, the real
// .shellcheckrc and one deliberately word-splitting script, so what is proven is the wiring rather
// than a re-run of shellcheck.
func TestGitHooks_PrePush_BlocksAShellFinding(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the pre-push hook against a fixture repository; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	gitInit(t, tree)

	// The real script and the real rc file, copied in: a reimplementation would prove nothing about
	// the gate the hook actually calls.
	root := repoRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(tree, "scripts"), 0o755))
	copyFile(t, filepath.Join(root, "scripts", "lint-shell.sh"), filepath.Join(tree, "scripts", "lint-shell.sh"))
	copyFile(t, filepath.Join(root, ".shellcheckrc"), filepath.Join(tree, ".shellcheckrc"))
	writeFixtureScript(t, tree, "offender.sh", wordSplittingScript)

	out, code := runHook(t, hookPath(t, "pre-push"), tree, findGofumpt(t))

	require.NotZerof(t, code, "pre-push must block a push when a shell script has a shellcheck "+
		"finding — issues #121 and #122 both ask for the hook, not only the CI job\n%s", out)
	require.Containsf(t, out, "SC2086",
		"the hook must show the gate's diagnostic. A hook that blocks without saying why is one "+
			"people learn to push through with --no-verify\n%s", out)
}

// TestShellcheckRC_DisablesOnlyTheArguedRule is the promise .shellcheckrc itself makes.
//
// That file is a hole in a gate. One rule id is argued there in full — SC2016, which fires on the
// markdown backticks inside every single-quoted printf FORMAT string in this repository, of which
// there are eight, all of them user-facing instructions and none of them wrong. A second id appearing
// without that argument is how a gate stops reporting the thing somebody found inconvenient, so it
// fails here rather than passing quietly.
//
// The severity half matters as much: raising shellcheck to `warning` would silently drop SC2086,
// which is `info`, and SC2086 is the defect (#111) that bought the gate. A gate tuned until it no
// longer reports the bug it was bought for is worse than no gate at all.
func TestShellcheckRC_DisablesOnlyTheArguedRule(t *testing.T) {
	t.Parallel()

	var disabled []string

	for _, line := range strings.Split(readRepoFile(t, ".shellcheckrc"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		require.Truef(t, strings.HasPrefix(line, "disable="),
			".shellcheckrc carries a directive that is not a disable: %q. Every other setting changes "+
				"what the gate checks without saying so in the one file whose subject is the exception.",
			line)

		disabled = append(disabled, strings.Split(strings.TrimPrefix(line, "disable="), ",")...)
	}

	require.Equalf(t, []string{"SC2016"}, disabled,
		".shellcheckrc disables %v. Exactly one rule is argued in that file, and a second one added "+
			"without the same argument is a gate quietly reporting less (issue #122). If a new "+
			"exception is genuinely right, write the reason there and change this test in the same "+
			"commit — that is the review this assertion exists to force.", disabled)

	// The gate must not raise the severity floor either: SC2086 and SC2005, the two rules #122's
	// acceptance criterion names, are `info` and `style` respectively.
	require.NotContainsf(t, readRepoFile(t, "scripts/lint-shell.sh"), "--severity",
		"scripts/lint-shell.sh must run shellcheck at its default severity. Raising it to `warning` "+
			"drops SC2086, which is exactly the defect issue #111 was.")
}

// TestLintShell_CleanTree_PassesGate is the control for the whole file.
func TestLintShell_CleanTree_PassesGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintShellToolchain(t)

	tree := t.TempDir()
	writeFixtureScript(t, tree, "clean.sh", cleanScript)

	out, code := runRootedScript(t, scriptPath(t, "lint-shell.sh"), tree)

	require.Zerof(t, code, "a correct, formatted script must pass — a gate that fires on everything "+
		"gets disabled rather than obeyed\n%s", out)
	require.Containsf(t, out, "shell lint passed", "the gate must say what it checked\n%s", out)
}
