// A skip that is honest on a laptop and a failure in CI (issue #177).
//
// THE DEFECT THIS CLOSES. Half the suites in this package reach their subject through a real tool —
// pnpm, atlas, gofumpt, actionlint, shellcheck, shfmt — and every one of them opened with
//
//	if _, err := exec.LookPath("pnpm"); err != nil { t.Skip("pnpm is not installed") }
//
// which is exactly right on a developer's machine and exactly wrong in CI. No job that ran
// `make test` installed Node, so TestNpmrc_SuppressesALifecycleScript — the ONLY functional proof
// that web/.npmrc's ignore-scripts actually stops a dependency's postinstall — skipped on every run
// of every PR, silently, while the job it lived in reported green. That is the #83/#156 defect class
// one step removed: not a job that fails for the wrong reason, but a job whose green includes a
// suite that never ran.
//
// THE RULE. A tool-missing skip is a statement about the ENVIRONMENT, and in CI the environment is
// configured by a file in this repository. So under CI it is not a skip, it is a finding: the
// workflow asked for a suite and did not install what the suite runs. The same shape as the golden
// harness refusing `-update` when CI is set, and the same shape as `make lint-go` hard-failing when
// golangci-lint is absent rather than exiting 0 over nothing.
//
// WHAT THIS IS NOT FOR. `testing.Short()` skips, which select a LANE rather than describe an
// environment: `make test-unit` is meant to skip these, and does, because every caller checks
// testing.Short() first and returns before reaching this helper.
package repo_test

import (
	"os"
	"os/exec"
	"testing"
)

// requireTool skips when tool is not on PATH, and FAILS instead when running under CI.
//
// `why` names the job that must install it, so the failure carries its own fix: the reader is a
// person looking at a red check in a workflow file they may not have opened.
func requireTool(t *testing.T, tool, why string) {
	t.Helper()

	if _, err := exec.LookPath(tool); err == nil {
		return
	}

	requireToolPresent(t, tool, why)
}

// requireToolAt is requireTool for a tool resolved by PATH rather than by name — the workspace
// eslint under web/node_modules/.bin, which exists only after a pnpm install.
func requireToolAt(t *testing.T, path, tool, why string) {
	t.Helper()

	if _, err := os.Stat(path); err == nil {
		return
	}

	requireToolPresent(t, tool, why)
}

// requireToolPresent is the shared verdict: a skip locally, a failure under CI.
//
// os.Getenv("CI") rather than a DKP_-prefixed variable of our own: GitHub Actions sets CI=true in
// every job unconditionally, so this needs nothing added to a workflow to take effect — and a gate
// that had to be opted into per job would be missing from the next job somebody adds, which is the
// defect it exists to catch.
func requireToolPresent(t *testing.T, tool, why string) {
	t.Helper()

	if os.Getenv("CI") == "" {
		t.Skipf("%s is not installed — run make setup. (In CI this is a FAILURE, not a skip: %s)", tool, why)
	}

	t.Fatalf("%s is not on PATH and CI is set, so this suite would have skipped in CI without saying so "+
		"(issue #177). %s", tool, why)
}
