// Package repo_test holds tests that assert the repository's own gates actually fire.
//
// Every test here is a NEGATIVE fixture test: it builds a tree that should fail a gate and
// requires that the gate says so, naming the rule that fired. "The gate is tested, not trusted"
// (docs/development/first-ten-prs.md). A gate nobody has ever seen go red is a gate nobody knows
// works.
//
// Two rules govern everything in this package:
//
//  1. Fixtures live in t.TempDir() only. A tainted fixture committed under the repo would be found
//     by the real `make lint-repo` and fail the project's own CI — which is exactly why the scripts
//     honour DKP_REPO_ROOT.
//  2. Assert on the rule id in the output, never on the exit code alone. A typo'd DKP_REPO_ROOT
//     also exits non-zero (`cd: No such file or directory`), so exit-code-only assertions pass for
//     the wrong reason.
//
// The shared helpers (repoRoot, runGateScript) live in this file and are used by makefile_test.go
// as well.
package repo_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A real 40-hex-character commit SHA, in the shape PIN001 demands.
const pinnedCheckoutSHA = "11bd71901bbe5b1630ceea73d27597364c9af683"

// repoRoot returns the absolute path of the git working tree holding this test. The working
// directory of a Go test is its own package directory, so it must never be assumed to be the root
// and an absolute path must never be hardcoded.
func repoRoot(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	require.NoError(t, err, "locate the repo root with git rev-parse --show-toplevel")

	root := strings.TrimSpace(string(out))
	require.True(t, filepath.IsAbs(root), "git returned a non-absolute repo root %q", root)

	return root
}

// scriptPath returns the absolute path of one of the repo's gate scripts.
func scriptPath(t *testing.T, name string) string {
	t.Helper()

	p := filepath.Join(repoRoot(t), "scripts", name)
	_, err := os.Stat(p)
	require.NoError(t, err, "gate script %s must exist", name)

	return p
}

// runGateScript runs a gate script against tree, returning its combined output and exit code.
//
// tree becomes the script's DKP_REPO_ROOT, which is the whole mechanism these tests rest on. It
// MUST be absolute: the scripts `cd` to it from the caller's working directory. It must also never
// be the empty string — the scripts use `${DKP_REPO_ROOT:-...}`, so an empty value silently falls
// back to the real checkout and the test would pass while inspecting the wrong tree.
//
// The environment is built explicitly rather than with t.Setenv because t.Setenv makes t.Parallel()
// panic. NAME is set because the Makefile guards `migration` with `ifndef NAME` -> $(error ...),
// so a bare `make -n migration` exits 2; verify-commands.sh survives that via its `grep '^target:'`
// fallback, but setting NAME keeps the dry run honest instead of relying on the fallback.
func runGateScript(t *testing.T, script, tree string) (output string, exitCode int) {
	t.Helper()

	require.NotEmpty(t, tree, "DKP_REPO_ROOT must not be empty — the scripts fall back to the real repo")
	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree, "NAME=ci_verify")

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run %s: %v\n%s", script, err, out)

	return "", 0
}

// writeWorkflow writes a minimal but structurally real workflow into tree, so the PIN001 gate has
// something to grep. repo-gates.sh SKIPS a gate whose target tree holds no files, so an empty
// t.TempDir() would make this pass vacuously rather than fail.
func writeWorkflow(t *testing.T, tree, uses string) {
	t.Helper()

	dir := filepath.Join(tree, ".github", "workflows")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	body := "name: fixture\n" +
		"on: push\n" +
		"jobs:\n" +
		"  build:\n" +
		"    runs-on: ubuntu-latest\n" +
		"    steps:\n" +
		"      - uses: " + uses + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture.yml"), []byte(body), 0o644))
}

// TestRepoGates_UnpinnedAction_FailsGate is the supply-chain half of the acceptance criteria: a
// fixture workflow containing an unpinned `actions/checkout@v4` must make repo-gates.sh exit
// non-zero, and PIN001 must be the rule that says so.
func TestRepoGates_UnpinnedAction_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()
	writeWorkflow(t, tree, "actions/checkout@v4")

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "unpinned action must fail the gates\n%s", out)
	require.Contains(t, out, "PIN001",
		"the gates went red, but not because of the pin rule — check which rule actually fired\n%s", out)
	require.Contains(t, out, ".github/workflows/fixture.yml:",
		"PIN001 must name the offending file, repo-root-relative\n%s", out)
	require.Contains(t, out, "actions/checkout@v4", "PIN001 must quote the offending line\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_CleanTree_Passes is the control for the test above. Without it, a harness that is
// simply broken — a bad script path, a DKP_REPO_ROOT that never resolves — would still make
// TestRepoGates_UnpinnedAction_FailsGate go green.
func TestRepoGates_CleanTree_Passes(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()
	writeWorkflow(t, tree, "actions/checkout@"+pinnedCheckoutSHA)

	out, code := runGateScript(t, script, tree)

	require.Zero(t, code, "a SHA-pinned workflow must pass the gates\n%s", out)
	require.Contains(t, out, "repo gates passed", "%s", out)
	require.NotContains(t, out, "PIN001", "PIN001 must not fire on a pinned action\n%s", out)
}

// TestFourLaws_AppearsInExactlyOneTrackedFile asserts both halves of the acceptance criterion:
// the architectural-laws heading lives in exactly one tracked file (AGENTS.md), and CLAUDE.md
// delegates to it with an `@AGENTS.md` line instead of restating it. Two copies of a normative rule
// is two rules, and the stale one wins whichever agent reads it first.
//
// The assertion is on IDENTITY, not on count: a count-only check stays green if the laws move out
// of AGENTS.md into some other single file.
func TestFourLaws_AppearsInExactlyOneTrackedFile(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// DO NOT "simplify" this into a string literal.
	//
	// The needle is assembled at run time on purpose. This file is itself a tracked file, so
	// writing the searched-for heading here verbatim would make this source a second match and
	// the test would fail on its own text — a self-reference, not a real defect. Any edit that
	// inlines the constant breaks the build.
	needle := "The " + "four" + " laws"

	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "list tracked files with git ls-files")

	tracked := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, tracked, "git ls-files returned nothing — is this a git checkout?")

	var matches []string

	for _, rel := range tracked {
		if rel == "" {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(root, rel))
		if errors.Is(readErr, os.ErrNotExist) {
			// Tracked but deleted in the working tree. Not this test's business.
			continue
		}

		require.NoError(t, readErr, "read tracked file %s", rel)

		if strings.Contains(string(body), needle) {
			matches = append(matches, rel)
		}
	}

	require.Equal(t, []string{"AGENTS.md"}, matches,
		"%q must appear in AGENTS.md and nowhere else in the tracked tree", needle)

	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err, "read CLAUDE.md")

	var hasImport bool

	for _, line := range strings.Split(string(claude), "\n") {
		if strings.TrimSpace(line) == "@AGENTS.md" {
			hasImport = true

			break
		}
	}

	require.True(t, hasImport, "CLAUDE.md must contain a line reading exactly @AGENTS.md")
	require.NotContains(t, string(claude), needle,
		"CLAUDE.md must delegate to AGENTS.md, not restate its laws")
}

// TestLintRepo_HostileRepoRootEnv_StillScansTheRealTree closes the hole that DKP_REPO_ROOT opens.
//
// The override exists so the tests above can point the gate scripts at a fixture tree. But an
// existing-but-empty directory makes every gate skip vacuously and repo-gates.sh still prints
// "repo gates passed" and exits 0 — and three of the gates (GOLD001, PIN001, AGPL001) print nothing
// at all when their tree is missing, so a vacuous run is indistinguishable from a real one in the
// CI log. The two that vanish silently are the action-pin gate and the AGPL licence firewall.
//
// So `make lint-repo` strips the variable with `env -u`. Without that, one line of `env:` added to
// a CI job by someone chasing a green build would disable the repo's entire gate suite while still
// reporting a passing check. This test is what keeps the strip in place.
func TestLintRepo_HostileRepoRootEnv_StillScansTheRealTree(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// An existing, empty, readable directory — the dangerous case. A nonexistent path would make
	// the script fail loudly on `cd`, which is not the hole being tested.
	empty := t.TempDir()

	cmd := exec.Command("make", "lint-repo")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+empty)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "make lint-repo must still pass on the real tree:\n%s", out)

	// Proof it inspected the real repository rather than the empty fixture.
	//
	// Choosing this marker took one wrong turn worth recording: asserting on WEB001 would be
	// TAUTOLOGICAL, because web/src does not exist in either tree, so WEB001 prints the same skip
	// line in both. The only honest discriminators are the gates whose tree exists in the real
	// checkout and not in an empty one. SQL001 is scoped to internal/, which is populated here, so
	// it RUNS (printing nothing) against the real repo and SKIPS loudly against an empty tree.
	//
	// If internal/ is ever emptied this assertion inverts and starts failing — which is the
	// correct direction for it to break.
	require.NotContains(t, string(out), "[SQL001]",
		"lint-repo skipped SQL001, which means it scanned %s instead of the repo — is "+
			"`env -u DKP_REPO_ROOT` still on the lint-repo recipe in the Makefile?", empty)
}
