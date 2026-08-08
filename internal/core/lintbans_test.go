package core_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// golangciMu serialises golangci-lint invocations. golangci-lint takes a process-global lock and
// refuses to run while another instance is active ("parallel golangci-lint is running"), so the
// t.Parallel() lint-ban tests must not spawn it concurrently. They keep t.Parallel() — they simply
// queue here, which costs nothing since each run is a second or two and there are only three.
var golangciMu sync.Mutex

// The lint-ban acceptance criteria (docs/development/first-ten-prs.md §PR 8): each fixture under
// testdata/lintfixtures/ must make `make lint`'s mechanism exit non-zero, ONE test per ban, "so a
// disabled rule is a red test rather than a silent hole".
//
// The fixtures live under testdata/, which golangci-lint skips by default, so they never break the
// real `make lint` run — only these tests, which place each fixture at the SCOPED path its rule
// watches (internal/ledger for the float ban, a non-clock package for the time.Now ban, db/ for the
// total() ban) and run the actual gate against it. This mirrors test/repo/gates_test.go: the gate is
// tested, not trusted.
//
// They shell out to golangci-lint (~1–2 s each) and repo-gates.sh, so they skip under -short for the
// same reason the spec-gate fixtures do — `make test-unit` has a < 5 s budget. They are NOT excluded
// from anything that gates a merge: `make check` runs `make test` (not -short) and CI's lint job runs
// the real config against the real tree on every PR.

// lintFixture returns the absolute path of a fixture file, asserting it exists.
func lintFixture(t *testing.T, name string) string {
	t.Helper()

	p := filepath.Join(repoRootDir(t), "testdata", "lintfixtures", name)
	_, err := os.Stat(p)
	require.NoError(t, err, "fixture %s must exist", name)

	return p
}

// golangciConfig returns the absolute path of the repo's real .golangci.yml. The tests run against
// the SAME config the project lints with — a test against a bespoke config would prove nothing about
// whether `make lint` fails.
func golangciConfig(t *testing.T) string {
	t.Helper()

	p := filepath.Join(repoRootDir(t), ".golangci.yml")
	_, err := os.Stat(p)
	require.NoError(t, err)

	return p
}

// runGolangciAgainstFixture writes fixtureSrc into a temp Go module at relPath, copies the real
// config in, and runs golangci-lint over it. It returns the combined output and exit code.
//
// A temp module is necessary because the rule scoping is by PATH: the float ban fires only under
// internal/ledger|internal/strategy, and the time.Now ban is excluded under internal/clock, so a
// fixture has to sit at a path that matches to exercise the rule. golangci-lint needs a module to
// type-check (analyze-types is on), hence the go.mod.
func runGolangciAgainstFixture(t *testing.T, relPath, fixtureName string) (string, int) {
	t.Helper()

	if testing.Short() {
		t.Skip("lint-ban fixtures shell out to golangci-lint; run `make test` or `make check`")
	}

	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		// The tool is pinned and installed by `make setup`; a missing binary is an environment gap,
		// not a passing test. Fail loudly rather than skip, so a CI runner without it goes red.
		t.Fatalf("golangci-lint not on PATH — run `make setup` (%v)", err)
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module lintfixturetest\n\ngo 1.26\n"), 0o644))

	dst := filepath.Join(dir, filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))

	src, err := os.ReadFile(lintFixture(t, fixtureName))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, src, 0o644))

	cfg, err := os.ReadFile(golangciConfig(t))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".golangci.yml"), cfg, 0o644))

	// One golangci-lint at a time from this test binary — it refuses to run in parallel with itself.
	golangciMu.Lock()
	defer golangciMu.Unlock()

	cmd := exec.Command(bin, "run", "./...")
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()

	return string(out), exitCodeOf(t, runErr)
}

// exitCodeOf extracts an exit code from an *exec.ExitError, or 0 for a nil error.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()

	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	t.Fatalf("command failed to run: %v", err)

	return -1
}

// TestLintBan_FloatInLedger_FailsLint proves the float ban: the fixture, placed under
// internal/ledger, makes golangci-lint exit non-zero and name the float rule. The path matters — the
// same file under internal/api would pass, which is what TestLintBan_FloatOutsideLedger_Passes
// asserts, so the ban is proven to be SCOPED and not repo-wide.
func TestLintBan_FloatInLedger_FailsLint(t *testing.T) {
	t.Parallel()

	out, code := runGolangciAgainstFixture(t, "internal/ledger/bad.go", "float_in_ledger.go")

	require.NotZero(t, code, "float in internal/ledger must fail lint\n%s", out)
	require.Contains(t, out, "float32/float64 are banned",
		"the failure must name the float ban, not some other lint\n%s", out)
	require.Contains(t, out, "forbidigo", "%s", out)
}

// TestLintBan_FloatOutsideLedger_Passes is the scoping control. Without it, TestLintBan_FloatInLedger
// would pass just as happily against a float ban that fired repo-wide — which would break
// internal/core's own boundary conversion and every legitimate float in the codebase.
func TestLintBan_FloatOutsideLedger_Passes(t *testing.T) {
	t.Parallel()

	out, code := runGolangciAgainstFixture(t, "internal/api/ok.go", "float_in_ledger.go")

	// Exit code is the authoritative signal: golangci-lint exits 0 when it finds nothing. (An issue's
	// text can echo in a stderr warning under cache contention, so asserting on the exit code and the
	// "0 issues" summary is more robust than a NotContains on the message.)
	require.Zero(t, code, "float outside the two arithmetic packages must be allowed\n%s", out)
	require.Contains(t, out, "0 issues", "%s", out)
}

// TestLintBan_TimeNowOutsideClock_FailsLint proves the time.Now ban: the fixture, placed in a
// non-clock package, makes golangci-lint exit non-zero and name the time.Now rule.
func TestLintBan_TimeNowOutsideClock_FailsLint(t *testing.T) {
	t.Parallel()

	out, code := runGolangciAgainstFixture(t, "internal/service/bad.go", "timenow_outside_clock.go")

	require.NotZero(t, code, "time.Now outside internal/clock must fail lint\n%s", out)
	require.Contains(t, out, "time.Now is banned outside internal/clock",
		"the failure must name the time.Now ban\n%s", out)
	require.Contains(t, out, "forbidigo", "%s", out)
}

// TestLintBan_TimeNowInsideClock_Passes is the exclusion control: the SAME fixture under
// internal/clock is allowed, because internal/clock is the one place System.Now may call time.Now.
// Without it, a ban with no carve-out would make internal/clock itself unbuildable against the rule.
func TestLintBan_TimeNowInsideClock_Passes(t *testing.T) {
	t.Parallel()

	// The fixture declares `package lintfixtures`; under internal/clock golangci-lint only needs the
	// PATH to match the exclusion, and the package name does not affect that. It compiles as its own
	// package in the temp module regardless of the directory name.
	out, code := runGolangciAgainstFixture(t, "internal/clock/ok.go", "timenow_outside_clock.go")

	// Exit code is authoritative — see the note in TestLintBan_FloatOutsideLedger_Passes.
	require.Zero(t, code, "time.Now inside internal/clock must be allowed\n%s", out)
	require.Contains(t, out, "0 issues", "%s", out)
}

// TestLintBan_TotalInQuery_FailsRepoGate proves the total() ban. It is enforced by repo-gate MONEY002
// rather than golangci-lint, because total() lives in SQL — a *.sql query or a Go string — which
// forbidigo cannot see inside (see .golangci.yml's note). `make lint` runs repo-gates.sh via
// lint-repo, so this fixture does make `make lint` exit non-zero. The gate is run against a temp db/
// tree, exactly as test/repo does.
func TestLintBan_TotalInQuery_FailsRepoGate(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("lint-ban fixtures shell out to a shell script; run `make test` or `make check`")
	}

	root := repoRootDir(t)
	script := filepath.Join(root, "scripts", "repo-gates.sh")

	dir := t.TempDir()
	dst := filepath.Join(dir, "db", "queries", "bad.sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))

	src, err := os.ReadFile(lintFixture(t, "total_in_query.sql"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, src, 0o644))

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+dir, "NAME=ci_verify")
	out, runErr := cmd.CombinedOutput()

	require.NotZero(t, exitCodeOf(t, runErr),
		"the banned aggregate in a query must fail the repo gates\n%s", out)
	require.Contains(t, string(out), "MONEY002",
		"the failure must name MONEY002, not some other rule\n%s", out)
	require.Contains(t, string(out), "db/queries/bad.sql:",
		"MONEY002 must name the offending file\n%s", string(out))
}
