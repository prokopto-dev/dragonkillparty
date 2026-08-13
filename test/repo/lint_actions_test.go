// Negative fixtures for scripts/lint-actions.sh — the workflow gate (issue #121).
//
// The rules of gates_test.go govern here too: fixtures live in t.TempDir() only, because a
// deliberately broken workflow committed under .github/workflows/ would fail this project's own CI;
// and every assertion names the DIAGNOSTIC, never only the exit code. A typo'd DKP_REPO_ROOT also
// exits non-zero, so exit-code-only assertions pass for the wrong reason.
//
// The fixtures are the shapes phase 0 actually paid for, not a tour of actionlint's rule set:
//
//	an undefined `needs:`            #101's shape — a job wired to something that is not there
//	an `if:` on an undeclared output  #82/#94's shape — a condition that is permanently false
//	an unquoted expansion in `run:`  #111's shape, embedded in YAML instead of in scripts/
//
// The last one is the reason this gate requires shellcheck as well as actionlint: `run:` blocks are
// shell that lives in YAML, so `lint / shell` cannot see them, and actionlint SILENTLY DROPS that
// check when shellcheck is absent. TestLintActions_WithoutItsTools_RefusesToRun holds that.
package repo_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// runRootedScript runs a gate script against a fixture tree and returns its combined output and exit code.
//
// The general form of runGateScript in gates_test.go, which is specific to repo-gates.sh's
// environment (it sets NAME and can carry the ADR pull-request context). This one sets DKP_REPO_ROOT
// and nothing else, and lets a caller append an environment entry that OVERRIDES an inherited one —
// which is what the without-its-tools fixture below needs to strip PATH.
//
// Environment built explicitly rather than with t.Setenv, for gates_test.go's reason: t.Setenv makes
// t.Parallel() panic.
func runRootedScript(t *testing.T, script, tree string, extraEnv ...string) (output string, exitCode int) {
	t.Helper()

	return runRootedScriptArgs(t, script, tree, nil, extraEnv...)
}

// runRootedScriptArgs is runRootedScript with arguments for the script itself — `--write`, so far.
func runRootedScriptArgs(t *testing.T, script, tree string, args []string, extraEnv ...string) (output string, exitCode int) {
	t.Helper()

	require.NotEmpty(t, tree, "DKP_REPO_ROOT must not be empty — the scripts fall back to the real repo")
	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree)
	cmd.Env = append(cmd.Env, extraEnv...)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run %s: %v\n%s", filepath.Base(script), err, out)

	return "", 0
}

// writeFixtureWorkflow writes a workflow into a fixture tree.
func writeFixtureWorkflow(t *testing.T, tree, name, body string) {
	t.Helper()

	dir := filepath.Join(tree, ".github", "workflows")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

// A workflow that is valid YAML, looks entirely plausible, and is wrong in three ways actionlint
// reads structurally. Each is a green-looking CI configuration that does not run what it says.
//
//   - needs: [buidl] names a job that does not exist, so `gate` never runs — and a required-check
//     gate that counts skipped as success reports it satisfied. Issue #101's shape.
//   - needs.build.outputs.deep reads an output `build` never declares, so the condition is
//     permanently false and the step never runs. Issue #82 and #94's shape — a gate that is present
//     in the checks list and does nothing.
//   - echo $files is an unquoted expansion inside a run: block, which is issue #111 living where
//     the shell gate over scripts/ cannot see it.
const brokenWorkflow = `name: fixture
on:
  pull_request:
    types: [opened, synchronize]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - run: echo build
  gate:
    needs: [buidl]
    runs-on: ubuntu-24.04
    steps:
      - run: echo gate
  report:
    needs: [build]
    runs-on: ubuntu-24.04
    steps:
      - if: needs.build.outputs.deep == 'true'
        run: echo report
      - run: |
          files=$(git ls-files)
          echo $files
`

// The same workflow with nothing wrong with it. The control: a gate that fired on everything would
// satisfy every assertion above while making the repository unlintable, and the first person to hit
// that would reach for --no-verify rather than for the diagnostic.
const cleanWorkflow = `name: fixture
on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - run: echo build
  gate:
    needs: [build]
    runs-on: ubuntu-24.04
    steps:
      - if: github.event.pull_request.draft == false
        run: echo gate
      - run: |
          files="$(git ls-files)"
          echo "$files"
`

// requireLintActionsToolchain skips on a laptop without the tools and FAILS in CI, where a workflow
// file is what installs them (issue #177).
func requireLintActionsToolchain(t *testing.T) {
	t.Helper()

	const why = "ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` must pass " +
		"actionlint and shellcheck in setup-toolchain's tools: input"

	requireTool(t, "actionlint", why)
	requireTool(t, "shellcheck", why)
}

// TestLintActions_BrokenWorkflow_FailsGate is issue #121's acceptance criterion: a deliberately
// broken workflow must fail `make lint-actions`, naming what is wrong.
func TestLintActions_BrokenWorkflow_FailsGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintActionsToolchain(t)

	tree := t.TempDir()
	writeFixtureWorkflow(t, tree, "broken.yml", brokenWorkflow)

	out, code := runRootedScript(t, scriptPath(t, "lint-actions.sh"), tree)

	require.NotZerof(t, code, "a workflow with an undefined needs:, a condition on an output that "+
		"does not exist, and an unquoted expansion must fail the gate\n%s", out)

	for _, want := range []struct{ fragment, why string }{
		{"buidl", "the undefined `needs:` target must be named — issue #101's shape, a job wired to " +
			"something that is not there and therefore never run"},
		{`"deep" is not defined`, "the output the `build` job never declares must be named — issue " +
			"#82 and #94's shape, a condition that is permanently false so the step never runs"},
		{"SC2086", "the unquoted expansion in the run: block must be reported, which happens ONLY " +
			"when actionlint is given shellcheck. Without it this gate checks less than it says"},
	} {
		require.Containsf(t, out, want.fragment, "%s\n%s", want.why, out)
	}
}

// TestLintActions_CleanWorkflow_PassesGate is the control.
func TestLintActions_CleanWorkflow_PassesGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintActionsToolchain(t)

	tree := t.TempDir()
	writeFixtureWorkflow(t, tree, "clean.yml", cleanWorkflow)

	out, code := runRootedScript(t, scriptPath(t, "lint-actions.sh"), tree)

	require.Zerof(t, code, "a correct workflow must pass the gate — a linter that fires on "+
		"everything gets disabled rather than obeyed\n%s", out)
	require.Containsf(t, out, "workflow lint passed", "the gate must say what it checked\n%s", out)
}

// TestLintActions_NoWorkflows_FailsRatherThanPassesVacuously covers the failure mode this repository
// keeps finding in its own gates: a scan that ran over nothing reporting as a scan that found
// nothing. An empty tree means the invocation is broken, not that the workflows are fine.
func TestLintActions_NoWorkflows_FailsRatherThanPassesVacuously(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintActionsToolchain(t)

	out, code := runRootedScript(t, scriptPath(t, "lint-actions.sh"), t.TempDir())

	require.NotZerof(t, code, "a tree with no workflows must FAIL the gate\n%s", out)
	require.Containsf(t, out, "no workflow files",
		"the failure must say the gate found nothing to lint, not merely exit non-zero\n%s", out)
}

// TestLintActions_WithoutItsTools_RefusesToRun is the one that keeps this gate honest.
//
// actionlint pipes every `run:` block through shellcheck and DISABLES that rule — without a word on
// stderr and without a non-zero exit — when the binary is not on PATH. A gate that quietly checks
// less than it claims is the defect ci.yml's header calls self-concealing, and it is why
// scripts/lint-actions.sh requires both tools rather than only the one it is named after.
func TestLintActions_WithoutItsTools_RefusesToRun(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	requireLintActionsToolchain(t)

	tree := t.TempDir()
	writeFixtureWorkflow(t, tree, "clean.yml", cleanWorkflow)

	// A PATH with neither tool on it. Stripping shellcheck alone is not portable — the two may live
	// in the same directory — so the assertion is on the message, which names the tool that stopped
	// the run.
	out, code := runRootedScript(t, scriptPath(t, "lint-actions.sh"), tree, "PATH=/nonexistent-for-this-test")

	require.NotZerof(t, code, "the gate must fail when its tools are absent, never exit 0 over "+
		"an unchecked tree\n%s", out)
	require.Containsf(t, out, "not installed",
		"the failure must name the missing tool and point at `make setup`\n%s", out)
}
