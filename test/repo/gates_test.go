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

// writeGo writes a Go source file into tree at the given repo-relative path.
//
// The bodies are never compiled — the gates are greps — but they are written as real Go so that a
// future reader cannot mistake the fixture for something that only looks like a violation.
func writeGo(t *testing.T, tree, rel, body string) {
	t.Helper()

	path := filepath.Join(tree, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestRepoGates_MisplacedSQLOpen_FailsGate is the law-2 half of PR 2's acceptance criteria: a
// *sql.DB opened, queried or executed outside internal/store must make repo-gates.sh exit
// non-zero, naming SQL001 and SQL002.
//
// It asserts the ALLOWLIST as well as the ban, in the same run. A gate that fired on everything
// would pass a ban-only test while making internal/store itself unbuildable, and the first person
// to hit that would reach for `git commit --no-verify` rather than for the rule id.
func TestRepoGates_MisplacedSQLOpen_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// The violation: a handler holding its own database handle.
	writeGo(t, tree, "internal/api/handler.go", `package api

func handler(ctx context.Context) error {
	db, err := sql.Open("sqlite", "file:dkp.db")
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, "SELECT 1")
	_ = rows
	return err
}
`)

	// Permitted: the same calls, inside the one package law 2 allows them in.
	writeGo(t, tree, "internal/store/store.go", `package store

func open(ctx context.Context) error {
	db, err := sql.Open("sqlite", "file:dkp.db")
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA optimize")
	return err
}
`)

	// Permitted: a zero-argument .Query() is net/url's accessor, not a database call. Without that
	// exclusion SQL002 would fire on most of internal/api.
	writeGo(t, tree, "internal/api/params.go", `package api

func params(r *http.Request) url.Values {
	return r.URL.Query()
}
`)

	// The trap: a real violation on a line that ALSO contains r.URL.Query(). This is the most
	// natural shape a law-2 violation takes — read a query parameter, run a query — and if the
	// zero-argument exclusion is ever moved from the pattern into the allowlist, this whole line
	// gets dropped and the gate goes quietly blind. It is a separate file so the assertion below
	// cannot be satisfied by handler.go's hit.
	writeGo(t, tree, "internal/api/search.go", `package api

func search(ctx context.Context, r *http.Request, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, "SELECT 1 WHERE x = "+r.URL.Query().Get("q"))
	_ = rows
	return err
}
`)

	// A call whose arguments wrap to the next line. grep is line-based, so the character after the
	// opening paren is the end of the line — which the SQL002 pattern's `|$` arm is what handles.
	// Whether `$` is an anchor inside an alternation is a regex-flavour question (POSIX ERE says
	// yes, context-independently; BSD and GNU grep agree), and this fixture is how CI finds out
	// rather than the pattern going quietly blind on a different runner.
	writeGo(t, tree, "internal/api/wrapped.go", `package api

func wrapped(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(
		ctx,
		"DELETE FROM ledger_entry",
	)
	return err
}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a misplaced sql.Open must fail the gates\n%s", out)
	require.Contains(t, out, "SQL001", "sql.Open outside internal/store must fire SQL001\n%s", out)
	require.Contains(t, out, "SQL002", ".QueryContext outside internal/store must fire SQL002\n%s", out)
	require.Contains(t, out, "internal/api/handler.go:",
		"the failure must name the offending file, repo-root-relative\n%s", out)

	require.NotContains(t, out, "internal/store/store.go",
		"internal/store is where a *sql.DB is SUPPOSED to live — the allowlist is not working\n%s", out)
	require.NotContains(t, out, "internal/api/params.go",
		"r.URL.Query() is not a database call; SQL002 must not fire on a zero-argument .Query()\n%s", out)
	require.Contains(t, out, "internal/api/search.go:",
		"SQL002 went blind on a line that mentions r.URL.Query() — the zero-argument exclusion "+
			"belongs in the pattern, not in the line-scoped allowlist\n%s", out)
	require.Contains(t, out, "internal/api/wrapped.go:",
		"SQL002 missed a call whose arguments wrap to the next line — this grep flavour does not "+
			"treat `$` as an anchor inside the alternation\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_ForTestHelperOutsideTest_FailsGate covers SQL003: the internal/store raw-SQL test
// helpers (ExecForTest and friends) must never be called from production code. A call in a
// non-_test.go file makes repo-gates.sh exit non-zero naming SQL003; the same call in a _test.go file,
// and the definition file itself, are allowlisted so the gate does not fire on legitimate use. This is
// the machine check that lets the helpers keep honest names rather than being disguised to slip past
// the SQL002 grep.
func TestRepoGates_ForTestHelperOutsideTest_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// Production leak: a non-test file under internal/ calls a ForTest helper. This must fire SQL003.
	writeGo(t, tree, "internal/ledger/leak.go", `package ledger

func leak(s *store.Store) { _ = s.ExecForTest(nil, "SELECT 1") }
`)
	// Allowlisted: the same family of helper in a _test.go file is legitimate test-only use.
	writeGo(t, tree, "internal/ledger/ok_test.go", `package ledger

func okUse(s *store.Store) { _ = s.TxForTest(nil, nil) }
`)
	// Allowlisted: the definition file exports the helpers.
	writeGo(t, tree, "internal/store/testing.go", `package store

func (s *Store) ExecForTest(tb any, q string, a ...any) error { return nil }
`)

	out, code := runGateScript(t, script, tree)

	require.NotEqual(t, 0, code,
		"a ForTest raw-SQL helper called in a non-test file must fail the gate\n%s", out)
	require.Contains(t, out, "SQL003",
		"the ForTest-outside-_test.go leak must fire SQL003\n%s", out)
	require.Contains(t, out, "internal/ledger/leak.go",
		"SQL003 must name the offending production file\n%s", out)
	require.NotContains(t, out, "ok_test.go",
		"SQL003 must not fire on a _test.go call site\n%s", out)
	require.NotContains(t, out, "internal/store/testing.go",
		"SQL003 must not fire on the definition file\n%s", out)
}

// TestRepoGates_TotalInGoSQL_FailsGate covers the other half of the query bans PR 2 installs.
//
// total() returns a REAL where sum() returns an INTEGER, so a single total() silently converts the
// centipoint ledger to floating point — no error, no warning, just a balance that is wrong by a
// fraction of a point for years. The ban has to reach SQL embedded in Go, not only db/*.sql, or it
// covers nothing at all until PR 3 creates that directory.
func TestRepoGates_TotalInGoSQL_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/ledger/balance.go", `package ledger

const balanceQuery = "SELECT total(amount_cp) FROM ledger_entry WHERE account_id = ?"
`)

	// A comment naming the rule must not fire it — otherwise no file could document the ban.
	writeGo(t, tree, "internal/ledger/doc.go", `package ledger

// total( is banned here: it returns a REAL. Use sum() with COALESCE.
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "total() in embedded SQL must fail the gates\n%s", out)
	require.Contains(t, out, "MONEY002", "%s", out)
	require.Contains(t, out, "internal/ledger/balance.go:", "%s", out)
	require.NotContains(t, out, "internal/ledger/doc.go",
		"a whole-line comment describing the rule must not trip it\n%s", out)
}

// TestRepoGates_RealClockInStrategy_FailsGate covers CLOCK002, added in Phase 0 PR 10b to close a
// hole CLOCK001 could not see.
//
// internal/strategy legitimately imports internal/clock, because strategy.Ctx.Clock() returns a
// clock.Clock. Nothing stopped a strategy from then constructing the real one: `clock.System{}.Now()`
// reads the wall clock, and CLOCK001 greps for `time.Now(`, which that is not. A plan whose effective
// time depends on when it ran cannot be replayed, which is the entire reason the clock is injected —
// so law 3 was enforced by convention there rather than mechanically.
//
// The AST twin is TestArch_Strategy_DoesNotConstructTheRealClock in internal/strategy, which also
// catches an ALIASED import that this grep would miss. Both exist because the grep is the cheap one
// that runs on every PR and the AST one is the thorough one.
func TestRepoGates_RealClockInStrategy_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/strategy/decay.go", `package strategy

import "github.com/prokopto-dev/dragonkillparty/internal/clock"

func now() int64 { return clock.System{}.Now().UnixMicro() }
`)

	// A comment naming the rule must not fire it, and neither must the LEGITIMATE use: the clock is
	// injected as an interface value, which is the whole point of the ban.
	writeGo(t, tree, "internal/strategy/doc.go", `package strategy

// clock.System is banned here: the clock arrives through Ctx.Clock() as an injected interface.
`)
	writeGo(t, tree, "internal/strategy/ctx.go", `package strategy

import "github.com/prokopto-dev/dragonkillparty/internal/clock"

type Ctx interface{ Clock() clock.Clock }
`)

	// And cmd/ may construct one: that is where a real clock is supposed to come from.
	writeGo(t, tree, "cmd/dkp/main.go", `package main

import "github.com/prokopto-dev/dragonkillparty/internal/clock"

var wall = clock.System{}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "constructing the real clock in internal/strategy must fail the gates\n%s", out)
	require.Contains(t, out, "CLOCK002", "%s", out)
	require.Contains(t, out, "internal/strategy/decay.go:", "%s", out)
	require.NotContains(t, out, "internal/strategy/doc.go",
		"a whole-line comment describing the rule must not trip it\n%s", out)
	require.NotContains(t, out, "internal/strategy/ctx.go",
		"depending on the clock.Clock INTERFACE is the sanctioned injection and must not trip it\n%s", out)
	require.NotContains(t, out, "cmd/dkp/main.go",
		"CLOCK002 is scoped to internal/strategy; main wiring is where a real clock comes from\n%s", out)
}

// TestRecipes_TotalIsBanned is the db/*.sql half of the total() ban, and the anchor for the bold note
// in db/RECIPES.md. The Go half above (TestRepoGates_TotalInGoSQL_FailsGate) proves the ban reaches
// SQL embedded in Go; this proves it reaches a recipe written the way db/RECIPES.md shows them — a
// -- name: directive over a SELECT — because that is the shape an author copies. A recipe using
// total() instead of COALESCE(sum(...), 0) silently converts the centipoint ledger to floating point,
// so MONEY002 must reject it in a .sql file, naming the file and the rule.
func TestRecipes_TotalIsBanned(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// A fixture recipe in the same shape db/RECIPES.md documents — the tempting-but-wrong one: a
	// -- name: directive over a SELECT that reaches for total() instead of COALESCE(sum(...), 0).
	writeFile(t, tree, "db/queries/balance.sql", `-- name: BalanceAsOfSeq :one
SELECT total(amount_cp) AS amount_cp
FROM ledger_entry
WHERE account_id = ? AND pool_id = ? AND balance_kind = ?;
`)

	// The control: a correct recipe using COALESCE(sum(...), 0) must NOT trip the gate, so a passing
	// test means "total() specifically is rejected", not "any query fails".
	writeFile(t, tree, "db/queries/good.sql", `-- name: GoodBalance :one
SELECT COALESCE(sum(amount_cp), 0) AS amount_cp FROM ledger_entry WHERE account_id = ?;
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "total() in a db/*.sql recipe must fail the gates\n%s", out)
	require.Contains(t, out, "MONEY002",
		"the gates went red, but not because of the money rule — check which rule fired\n%s", out)
	require.Contains(t, out, "db/queries/balance.sql:",
		"MONEY002 must name the offending file, repo-root-relative\n%s", out)
	require.NotContains(t, out, "db/queries/good.sql",
		"COALESCE(sum(...)) is the sanctioned form and must not trip the ban\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// writeFile writes an arbitrary text file into tree at the given repo-relative path. Used for SQL
// fixtures, which writeGo (Go-only) does not fit.
func writeFile(t *testing.T, tree, rel, body string) {
	t.Helper()

	path := filepath.Join(tree, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
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

// TestRepoGates_QemuInWorkflow_FailsGate is PR 7's "no QEMU" acceptance criterion, as a negative
// fixture. Multi-arch is cross-compiled and joined with imagetools; a QEMU-emulated build is 10-25x
// slower and the predictable response is dropping arm64 "to make CI fast". QEMU001 bans the string
// in any workflow so that reintroduction is a red gate rather than a quiet edit.
//
// The fixture pins the action to a real SHA so that PIN001 does NOT fire — this test must prove
// QEMU001 fires, not merely that the gates went red for some other reason. The action value itself
// carries the banned string, which is exactly the shape a reintroduction takes.
func TestRepoGates_QemuInWorkflow_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()
	writeWorkflow(t, tree, "docker/setup-qemu-action@"+pinnedCheckoutSHA)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a QEMU step in a workflow must fail the gates\n%s", out)
	require.Contains(t, out, "QEMU001",
		"the gates went red, but not because of the QEMU rule — check which rule actually fired\n%s", out)
	require.Contains(t, out, ".github/workflows/fixture.yml:",
		"QEMU001 must name the offending file, repo-root-relative\n%s", out)
}

// TestRepoGates_QemuInComment_PassesGate is the counterpart: the committed workflows document the
// "No QEMU" choice in prose, and prose about a rule is not a breach of it. A comment line mentioning
// QEMU must NOT fire QEMU001 — every other gate here strips comments for the same reason, and a gate
// that fired on its own documentation is a gate people route around.
func TestRepoGates_QemuInComment_PassesGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	dir := filepath.Join(tree, ".github", "workflows")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "name: fixture\n" +
		"on: push\n" +
		"jobs:\n" +
		"  build:\n" +
		"    runs-on: ubuntu-latest\n" +
		"    steps:\n" +
		"      # No QEMU here: multi-arch is cross-compiled. See release.yml.\n" +
		"      - uses: actions/checkout@" + pinnedCheckoutSHA + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture.yml"), []byte(body), 0o644))

	out, code := runGateScript(t, script, tree)

	require.Zero(t, code, "a workflow that only mentions QEMU in a comment must pass\n%s", out)
	require.NotContains(t, out, "QEMU001", "QEMU001 must not fire on a comment\n%s", out)
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

// writeRepoFile writes an arbitrary file into tree at the given repo-relative path.
//
// writeGo above is the same three calls with a name that promises Go. The gates are greps, so the
// content type never matters to them — but a fixture named `schema.hcl` written by `writeGo` reads
// as a mistake, and the next person would spend a minute deciding whether it was one.
func writeRepoFile(t *testing.T, tree, rel, body string) {
	t.Helper()

	path := filepath.Join(tree, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestRepoGates_EQdkpConfigKeyInSchema_FailsGate covers AGPL002.
//
// The defect this rule exists for is not hypothetical and was not caught by review: the `/guild`
// row in docs/design/02-api-design.md shipped `inactive_period` and `auto_set_active`, transcribed
// from docs/design/05-migration.md's list of EQdkp `<prefix>config` keys rather than from DKP's own
// schema. `auto_set_active` is the OPPOSITE control from DKP's `auto_set_inactive`, so a client
// written from the published contract would have set the wrong value and nothing would have said
// so. Other keys in the same row had been renamed correctly (`dkp_name` -> `points_label`,
// `guildtag` -> `tag`), which is precisely what made the two survivors invisible.
//
// The fixture asserts both directions in one run, because a ban-only test would pass just as
// happily against a gate that fired on every schema file — and the first person to hit that would
// reach for --no-verify rather than for the rule id.
func TestRepoGates_EQdkpConfigKeyInSchema_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "db/schema.hcl", `table "guild" {
  # EQdkp's inactive_period is carried by the importer, not by this schema.
  column "inactive_after_days" { type = integer }
  column "auto_set_active"     { type = integer }
  column "dkp_name"            { type = text }
}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "the gate accepted EQdkp config keys as column names\n%s", out)
	require.Contains(t, out, "[AGPL002]", "%s", out)

	// The violation is the two column names, NOT the comment above them. strip_comments exists so a
	// gate never fires on the prose documenting it, and AGPL002 relies on that: db/schema.hcl's real
	// header comments discuss the conventions at length.
	require.Contains(t, out, "auto_set_active", "%s", out)
	require.Contains(t, out, "dkp_name", "%s", out)
	require.NotContains(t, out, "EQdkp's inactive_period is carried",
		"AGPL002 fired on a comment explaining the rule; strip_comments should have dropped it\n%s", out)
}

// TestRepoGates_DKPOwnColumnNames_PassGate is the allowlist half, and it is the half that matters.
//
// `hide_inactive` and `timezone` appear in EQdkp's config list AND are DKP's own column names: the
// concepts coincide and the words are ordinary English. Banning them would make db/schema.hcl
// unbuildable against its own conventions. Without this test, widening AGPL002's pattern to "every
// key in the migration doc" would satisfy every assertion above.
func TestRepoGates_DKPOwnColumnNames_PassGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "db/schema.hcl", `table "guild" {
  column "timezone"            { type = text }
  column "hide_inactive"       { type = integer }
  column "inactive_after_days" { type = integer }
  column "auto_set_inactive"   { type = integer }
  column "points_label"        { type = text }
  column "points_precision"    { type = integer }
}
`)

	out, code := runGateScript(t, script, tree)

	require.Zero(t, code, "the gate rejected DKP's own column names\n%s", out)
	require.NotContains(t, out, "[AGPL002]", "%s", out)
}
