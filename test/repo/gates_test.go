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

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	ledgerkinds "github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// A real 40-hex-character commit SHA, in the shape PIN001 demands.
const pinnedCheckoutSHA = "11bd71901bbe5b1630ceea73d27597364c9af683"

// repoRoot returns the absolute path of the git working tree holding this test. The working
// directory of a Go test is its own package directory, so it must never be assumed to be the root
// and an absolute path must never be hardcoded.
//
// testing.TB rather than *testing.T because the fixture builders take a TB.
func repoRoot(t testing.TB) string {
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
//
// extraEnv carries the PR context ADR001 reads (DKP_ADR_BASE_REF, DKP_ADR_PR_BODY). It is variadic
// rather than a second function because every other property of the run is identical: the ADR
// fixtures must go through the same entry point as every other gate, or they would prove that a
// script exits non-zero rather than that `make lint-repo` does.
func runGateScript(t *testing.T, script, tree string, extraEnv ...string) (output string, exitCode int) {
	t.Helper()

	require.NotEmpty(t, tree, "DKP_REPO_ROOT must not be empty — the scripts fall back to the real repo")
	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree, "NAME=ci_verify")
	cmd.Env = append(cmd.Env, extraEnv...)

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

// TestRepoGates_StrategyImportsStore_FailsGate covers PURE001, law 3's first clause: a strategy
// plans, it does not read the database. Everything a planner is allowed to know arrives through
// strategy.Ctx; a planner that could reach the store could decide on state its own Ctx never saw,
// and the batch that decision produced would not replay.
//
// The AST twin is TestArch_Strategy_ImportGraph_HasNoStore in internal/strategy, which walks the
// whole import graph and so also sees a TRANSITIVE path this grep cannot. That twin is why PURE001
// had no fixture until now: the law was covered, so nobody noticed the GATE was not. A pattern typo
// here — a stray anchor, a plural — would have gone unnoticed until the day the AST test was the one
// that broke, and `make check` would have stayed green throughout.
func TestRepoGates_StrategyImportsStore_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/strategy/zero_sum.go", `package strategy

import "github.com/prokopto-dev/dragonkillparty/internal/store"

func plan(q *store.Queries) error { return nil }
`)

	// A comment naming the rule must not fire it. internal/strategy/doc.go and strategy.go both
	// discuss this ban at length, so a gate that fired on its own documentation would be unusable
	// from the day law 3 was written down.
	writeGo(t, tree, "internal/strategy/doc.go", `package strategy

// internal/store is banned here: everything the planner may know arrives through Ctx.
`)

	// Scope control: internal/ledger is the package that is SUPPOSED to hold a store handle. PURE001
	// firing there would make the ledger unbuildable, and the first person to hit that would reach
	// for --no-verify rather than for the rule id.
	writeGo(t, tree, "internal/ledger/service.go", `package ledger

import "github.com/prokopto-dev/dragonkillparty/internal/store"

type Service struct{ q *store.Queries }
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "internal/strategy importing internal/store must fail the gates\n%s", out)
	require.Contains(t, out, "PURE001", "%s", out)
	require.Contains(t, out, "internal/strategy/zero_sum.go:",
		"PURE001 must name the offending file, repo-root-relative\n%s", out)
	require.NotContains(t, out, "internal/strategy/doc.go",
		"a whole-line comment describing the rule must not trip it\n%s", out)
	require.NotContains(t, out, "internal/ledger/service.go",
		"PURE001 is scoped to internal/strategy; the ledger is where the store belongs\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_MathRandInStrategy_FailsGate covers PURE002, law 3's second clause.
//
// The seeded Rng arrives through Ctx.Rng(), and its seed is persisted onto ledger_batch.rng_seed —
// that seed is the entire reason a batch replays byte-identically. A strategy that reached for
// math/rand instead would make the persisted seed a decoration and the determinism property a
// tautology: it would still pass, because it replays the recorded outcome, and nothing would say the
// tie-break had been a coin flip nobody can reproduce.
//
// The AST twin is TestArch_Strategy_Files_DoNotImportTimeOrMathRand, which resolves imports and so
// also catches an ALIASED one (`import r "math/rand"`) that this grep would miss.
func TestRepoGates_MathRandInStrategy_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/strategy/tiebreak.go", `package strategy

import "math/rand"

func breakTie(n int) int { return rand.Intn(n) }
`)

	// A comment naming the rule must not fire it.
	writeGo(t, tree, "internal/strategy/doc.go", `package strategy

// math/rand is banned here: the seeded Rng arrives through Ctx.Rng() and its seed is persisted.
`)

	// The sanctioned form must not fire: the ban is on the IMPORT, not on the idea of randomness. A
	// pattern widened to `rand` would reject the injected Rng this rule exists to require, which is
	// the shape of "fixing" a gate by making it useless.
	writeGo(t, tree, "internal/strategy/roll.go", `package strategy

func roll(ctx Ctx, sides int64) int64 { return ctx.Rng().Int63n(sides) }
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "math/rand in internal/strategy must fail the gates\n%s", out)
	require.Contains(t, out, "PURE002", "%s", out)
	require.Contains(t, out, "internal/strategy/tiebreak.go:",
		"PURE002 must name the offending file, repo-root-relative\n%s", out)
	require.NotContains(t, out, "internal/strategy/doc.go",
		"a whole-line comment describing the rule must not trip it\n%s", out)
	require.NotContains(t, out, "internal/strategy/roll.go",
		"the INJECTED Rng is the sanctioned source of randomness and must not trip the ban\n%s", out)
}

// TestRepoGates_WallClockOutsideClockPackage_FailsGate covers CLOCK001: `time.Now` belongs to
// internal/clock and nowhere else, because a service that reads the wall clock directly cannot be
// tested without sleeping and cannot be replayed at all.
//
// It asserts the ALLOWLIST in the same run, and that half is the one that matters: internal/clock's
// System.Now is the single call site in the repository that is allowed to call time.Now, so a gate
// that fired repo-wide would make the injected-clock design itself a violation.
//
// The forbidigo twin is `^time\.Now$` in .golangci.yml, proven by TestLintBan_TimeNowOutsideClock_FailsLint
// in internal/core. It resolves types and so also catches a dot-imported or aliased call; this grep
// is the cheap one, and until this fixture existed it was the untested one.
func TestRepoGates_WallClockOutsideClockPackage_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/ledger/batch.go", `package ledger

import "time"

func stamp() int64 { return time.Now().UnixMicro() }
`)

	// Allowlisted: the one implementation that is permitted to read the wall clock.
	writeGo(t, tree, "internal/clock/system.go", `package clock

import "time"

type System struct{}

func (System) Now() time.Time { return time.Now() }
`)

	// A comment naming the rule must not fire it.
	writeGo(t, tree, "internal/ledger/doc.go", `package ledger

// time.Now() is banned here: the clock is injected so a batch can be replayed.
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "time.Now outside internal/clock must fail the gates\n%s", out)
	require.Contains(t, out, "CLOCK001", "%s", out)
	require.Contains(t, out, "internal/ledger/batch.go:",
		"CLOCK001 must name the offending file, repo-root-relative\n%s", out)
	require.NotContains(t, out, "internal/clock/system.go",
		"internal/clock is where time.Now is SUPPOSED to be called — the allowlist is not working\n%s", out)
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

// TestRepoGates_FloatInPointArithmetic_FailsGate covers MONEY001: `float32`/`float64` do not exist
// in internal/ledger or internal/strategy. Point arithmetic is core.Centipoints (int64) only — a
// float in the point path does not fail, it DRIFTS, and a balance that is wrong by a fraction of a
// point for a year is discovered by a guild member disputing a bid, not by CI.
//
// The fixture taints BOTH gated trees in one run, because MONEY001 is a shell `for` loop over two
// directories: a fixture in only one of them would keep passing if the loop lost its second element,
// and internal/strategy is where the tempting float lives (an attendance ratio, a decay rate).
//
// The forbidigo twin is `\bfloat(32|64)\b` scoped by a path-except exclusion in .golangci.yml, proven
// by TestLintBan_FloatInLedger_FailsLint in internal/core.
func TestRepoGates_FloatInPointArithmetic_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/ledger/decay.go", `package ledger

var rate float64 = 0.5
`)
	writeGo(t, tree, "internal/strategy/attendance.go", `package strategy

func weight(attended, held int32) float32 { return float32(attended) / float32(held) }
`)

	// Scope control: float is legitimate outside the two arithmetic packages — internal/core's own
	// boundary conversion uses one — so a repo-wide ban would be wrong, and the .golangci.yml
	// exclusion says so in as many words.
	writeGo(t, tree, "internal/api/report.go", `package api

type Report struct{ Ratio float64 }
`)

	// A comment naming the rule must not fire it.
	writeGo(t, tree, "internal/ledger/doc.go", `package ledger

// float64 is banned here: point arithmetic is core.Centipoints (int64) only.
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a float in the point path must fail the gates\n%s", out)
	require.Contains(t, out, "MONEY001", "%s", out)
	require.Contains(t, out, "internal/ledger/decay.go:",
		"MONEY001 must name the offending file, repo-root-relative\n%s", out)
	require.Contains(t, out, "internal/strategy/attendance.go:",
		"MONEY001 covers internal/strategy as well as internal/ledger — has the loop lost a tree?\n%s", out)
	require.NotContains(t, out, "internal/api/report.go",
		"MONEY001 is scoped to the two arithmetic packages; a float elsewhere is legitimate\n%s", out)
	require.NotContains(t, out, "internal/ledger/doc.go",
		"a whole-line comment describing the rule must not trip it\n%s", out)
}

// TestRepoGates_RealColumnInMigration_FailsGate covers MONEY003, the schema half of the same rule.
//
// SQLite's type affinity makes this quiet in a way the Go ban is not: a REAL column accepts every
// integer a correct writer inserts, so the taint is invisible until a value arrives that cannot be
// represented exactly — and by then the column holds years of history. There is no compensating
// linter for this one; the grep is the only mechanism, which is why the fixture is not optional.
//
// All three banned type names appear, because the rule is a single alternation: dropping an arm
// while keeping the others is exactly the regression a one-type fixture would wave through.
func TestRepoGates_RealColumnInMigration_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeFile(t, tree, "db/migrations-sqlite/0002_ledger_entry.sql", `-- +goose Up
CREATE TABLE ledger_entry (
    id        TEXT    NOT NULL PRIMARY KEY,
    amount_cp REAL    NOT NULL,
    rate_bp   NUMERIC NOT NULL,
    fee_cp    DECIMAL NOT NULL
);
`)

	// The control: the sanctioned INTEGER column must not trip the gate, so a passing test means
	// "the float types specifically are rejected", not "any migration fails".
	writeFile(t, tree, "db/migrations-sqlite/0003_ledger_snapshot.sql", `-- +goose Up
CREATE TABLE ledger_snapshot (
    id        TEXT    NOT NULL PRIMARY KEY,
    amount_cp INTEGER NOT NULL
);
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a REAL column in a migration must fail the gates\n%s", out)
	require.Contains(t, out, "MONEY003", "%s", out)
	require.Contains(t, out, "db/migrations-sqlite/0002_ledger_entry.sql:",
		"MONEY003 must name the offending file, repo-root-relative\n%s", out)
	require.Contains(t, out, "amount_cp REAL", "the REAL arm of the alternation must fire\n%s", out)
	require.Contains(t, out, "rate_bp   NUMERIC", "the NUMERIC arm of the alternation must fire\n%s", out)
	require.Contains(t, out, "fee_cp    DECIMAL", "the DECIMAL arm of the alternation must fire\n%s", out)
	require.NotContains(t, out, "0003_ledger_snapshot.sql",
		"INTEGER is the sanctioned column type and must not trip the ban\n%s", out)
}

// TestRepoGates_RawFetchOutsideGeneratedClient_FailsGate covers WEB001, law 4: the SPA is an API
// client, and a component that calls the network itself is a capability the public API does not
// have. That is not a style preference — it is how "if the UI can do it, a bot can do it" stays true,
// and a back door added in a component is invisible in the OpenAPI document CI diffs.
//
// The eslint twin (no-restricted-globals, proven by TestWebLint_BareFetch_FailsLint) is AST-aware and
// catches shapes the grep cannot, but it needs a Node toolchain — so on a job that has only Go, this
// grep is law 4's entire enforcement.
//
// Both extensions are tainted deliberately: the include glob is `*.ts*`, and narrowing it to `*.tsx`
// would silently stop scanning every hook, loader and lib file in the SPA — which is where a raw
// fetch actually gets written.
func TestRepoGates_RawFetchOutsideGeneratedClient_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "web/src/routes/roster.tsx", `export async function loadRoster() {
  const res = await fetch("/api/v1/roster");
  return res.json();
}
`)
	writeRepoFile(t, tree, "web/src/lib/legacy.ts", `export function poll(url: string) {
  const xhr = new XMLHttpRequest();
  xhr.open("GET", url);
  return xhr;
}
`)

	// Allowlisted: the generated client is where a fetch is SUPPOSED to happen. Without this half,
	// a gate that fired on everything would pass the assertions above while making web/src/api
	// unlintable.
	writeRepoFile(t, tree, "web/src/api/client.ts", `export const client = createClient({
  fetch: (req: Request) => fetch(req),
});
`)

	// A comment naming the rule must not fire it.
	writeRepoFile(t, tree, "web/src/routes/doc.tsx", `// fetch( is banned here: every call goes through the generated client in src/api.
export const Doc = () => null;
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a raw fetch outside web/src/api must fail the gates\n%s", out)
	require.Contains(t, out, "WEB001", "%s", out)
	require.Contains(t, out, "web/src/routes/roster.tsx:",
		"WEB001 must name the offending file, repo-root-relative\n%s", out)
	require.Contains(t, out, "web/src/lib/legacy.ts:",
		"WEB001 must reach .ts as well as .tsx, and XMLHttpRequest as well as fetch\n%s", out)
	require.NotContains(t, out, "web/src/api/client.ts",
		"web/src/api is where the fetch belongs — the allowlist is not working\n%s", out)
	require.NotContains(t, out, "web/src/routes/doc.tsx",
		"a whole-line comment describing the rule must not trip it\n%s", out)
}

// TestRepoGates_DangerouslySetInnerHTML_FailsGate covers WEB002.
//
// internal/cms accepts untrusted rich text — articles, comments, signatures written by whoever the
// officers gave an account to — and this is the one prop that turns it into script running with the
// reader's session. The repo has no eslint react/no-danger rule, so unlike WEB001 this grep is not
// defence in depth: it is the only thing standing between a CMS field and stored XSS, which makes a
// silent regression in its pattern the most expensive one in this file.
func TestRepoGates_DangerouslySetInnerHTML_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "web/src/components/Article.tsx", `export function Article({ html }: { html: string }) {
  return <div dangerouslySetInnerHTML={{ __html: html }} />;
}
`)

	// A comment naming the rule must not fire it — the CMS components have every reason to document
	// why they render sanitised text the long way round.
	writeRepoFile(t, tree, "web/src/components/SafeArticle.tsx", `// dangerouslySetInnerHTML is banned here: CMS bodies are rendered from sanitised nodes.
export const SafeArticle = () => null;
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "dangerouslySetInnerHTML must fail the gates\n%s", out)
	require.Contains(t, out, "WEB002", "%s", out)
	require.Contains(t, out, "web/src/components/Article.tsx:",
		"WEB002 must name the offending file, repo-root-relative\n%s", out)
	require.NotContains(t, out, "web/src/components/SafeArticle.tsx",
		"a whole-line comment describing the rule must not trip it\n%s", out)
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

// TestRepoGates_UpdateFlagInCICommand_FailsGate covers GOLD001, the golden-file rewrite fence.
//
// `go test -update` regenerates test/golden/ to match whatever the code currently produces. Run on a
// laptop that is a deliberate act; run in CI it is a machine that makes every golden assertion agree
// with the change under test, and the parser suite — the thing standing between a P99 log format and
// silently wrong attendance — stops being a test at all. AGENTS.md bans it in prose ("do not
// -update a test to make CI green"); this is the mechanism.
//
// Nothing else enforces this: there is no linter for a shell command inside a YAML string. The gate
// is also the most pattern-dependent one in the script — three alternatives for where the flag may
// appear and a six-term exclusion list — so it is the one most likely to rot into a no-op, and the
// one where a no-op is invisible.
func TestRepoGates_UpdateFlagInCICommand_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// The action is SHA-pinned and no step mentions QEMU, so PIN001 and QEMU001 stay quiet and a red
	// run can only mean GOLD001. The two violations are the two shapes the flag really takes: a
	// single-line `run:`, and a continuation line inside a `run: |` block — which is why the pattern
	// has a bare `\s{4,}` arm at all. A fixture with only the first would let that arm be deleted.
	writeRepoFile(t, tree, ".github/workflows/golden.yml", `name: fixture
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@`+pinnedCheckoutSHA+`
      # Never pass -update in CI: it rewrites test/golden/ to agree with the change under test.
      - run: sudo apt-get update
      - run: go test ./internal/parse -update
      - run: |
          make test-golden --update
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "'-update' in a CI command must fail the gates\n%s", out)
	require.Contains(t, out, "GOLD001", "%s", out)
	require.Contains(t, out, ".github/workflows/golden.yml:",
		"GOLD001 must name the offending file, repo-root-relative\n%s", out)
	require.Contains(t, out, "go test ./internal/parse -update",
		"GOLD001 must quote the offending line\n%s", out)
	require.Contains(t, out, "make test-golden --update",
		"GOLD001 missed a `--update` on a continuation line inside a `run: |` block — the "+
			"indentation arm of the pattern is what covers multi-line run steps\n%s", out)

	require.NotContains(t, out, "apt-get",
		"`apt-get update` is a package index refresh, not a golden rewrite — it is allowlisted\n%s", out)
	require.NotContains(t, out, "actions/checkout",
		"an `actions/` line is allowlisted; a pinned checkout must not read as a golden rewrite\n%s", out)
	require.NotContains(t, out, "PIN001",
		"the fixture pins its action — a PIN001 hit means this test proves the wrong thing\n%s", out)
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

// The base tree the ADR001 fixtures branch from: one of every file the rule watches, in the state
// that must NOT trigger it. Everything a case does afterwards is "the working tree of a PR".
const (
	adrBaseGoMod = `module github.com/prokopto-dev/dragonkillparty

go 1.25

require (
	github.com/danielgtaylor/huma/v2 v2.0.0
	github.com/stretchr/testify v1.9.0
	golang.org/x/sys v0.20.0 // indirect
)
`
	adrBaseDockerfile = "FROM gcr.io/distroless/static-debian12\nENTRYPOINT [\"/dkp\"]\n"
	adrBaseSchema     = "table \"pool\" {\n  column \"id\" {\n    type = text\n  }\n}\n"
)

// newADRFixture builds that tree and seals it as the base revision.
//
// sealFixtureBase (migration_gates_test.go) is reused rather than reimplemented: MIG003 needs a real
// merge base for exactly the same reason ADR001 does, and its comment records why an injected
// "pretend this was the base" knob would be both a way to weaken the gate and a second code path CI
// never runs.
func newADRFixture(t *testing.T) string {
	t.Helper()

	tree := t.TempDir()

	writeRepoFile(t, tree, "go.mod", adrBaseGoMod)
	writeRepoFile(t, tree, "deploy/Dockerfile", adrBaseDockerfile)
	writeRepoFile(t, tree, "db/schema.hcl", adrBaseSchema)
	writeRepoFile(t, tree, "docs/adr/README.md", "# Architecture decision records\n")
	writeGo(t, tree, "internal/api/handler.go", "package api\n\nfunc handler() {}\n")

	sealFixtureBase(t, tree)

	return tree
}

// runADRGate runs the gates against tree with the pull-request context ADR001 reads.
//
// body is passed even when empty, and that is the point: a PR with no body has not answered the
// question, so it must FAIL rather than skip. Only an absent DKP_ADR_BASE_REF means "this is not a
// pull request".
func runADRGate(t *testing.T, tree, body string) (output string, exitCode int) {
	t.Helper()

	return runGateScript(t, scriptPath(t, "repo-gates.sh"), tree,
		"DKP_ADR_BASE_REF=origin/main", "DKP_ADR_PR_BODY="+body)
}

// requireOnlyRule asserts that exactly one rule id went red, and that it is the expected one.
//
// A `require.Contains(out, "ADR001")` alone passes on a run that also tripped ENUM001 or AGPL002 on
// the fixture's own schema file — the fixture would then be proving that the gates exit non-zero,
// not that this rule fires. That is the failure the whole package exists to make impossible, and
// requireOnlyMIG003 above is the same assertion written for one rule.
func requireOnlyRule(t *testing.T, out, id string) {
	t.Helper()

	var fired []string

	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "FAIL") {
			continue
		}

		// LastIndex, not Index: the line opens with the ANSI sequence that colours FAIL red, and
		// `\033[31m` carries a bracket of its own.
		shut := strings.Index(line, "]")
		if shut < 0 {
			continue
		}

		if open := strings.LastIndex(line[:shut], "["); open >= 0 {
			fired = append(fired, line[open+1:shut])
		}
	}

	require.Equal(t, []string{id}, fired,
		"exactly one rule must have fired, and it must be %s — otherwise this fixture proves the "+
			"gates went red, not that %s did\n%s", id, id, out)
}

// TestRepoGates_AdrTriggerWithoutRecord_FailsGate covers ADR001 (#85).
//
// docs/adr/README.md and docs/design/07-documentation-system.md both said, in bold, that this
// requirement was "part of the `lint / repo` job". It was not — there was no step, no rule and no
// grep. That is worse than an ordinary stale sentence because of who reads it: an agent reading the
// README concludes the gate will catch it if an ADR is needed, and a reviewer reading the same line
// concludes CI already asked. The requirement was carried entirely by whoever happened to remember
// it.
//
// All four documented triggers are exercised, because they are four independent branches of the
// rule rather than four spellings of one: two are path tests, one compares go.mod against the base
// blob, and one asks git whether a top-level package existed before. A fixture for one would leave
// the other three free to be deleted in silence.
func TestRepoGates_AdrTriggerWithoutRecord_FailsGate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		mutate  func(t *testing.T, tree string)
		trigger string
	}{
		{
			name: "a new direct dependency in go.mod",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeRepoFile(t, tree, "go.mod", strings.Replace(adrBaseGoMod,
					"\tgithub.com/stretchr/testify v1.9.0\n",
					"\tgithub.com/stretchr/testify v1.9.0\n\tgithub.com/redis/go-redis/v9 v9.5.1\n", 1))
			},
			trigger: "go.mod",
		},
		{
			name: "deploy/Dockerfile gains a port",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeRepoFile(t, tree, "deploy/Dockerfile", adrBaseDockerfile+"EXPOSE 9090\n")
			},
			trigger: "deploy/Dockerfile",
		},
		{
			name: "db/schema.hcl gains a table",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeRepoFile(t, tree, "db/schema.hcl", adrBaseSchema+
					"\ntable \"bid_session\" {\n  column \"id\" {\n    type = text\n  }\n}\n")
			},
			trigger: "db/schema.hcl",
		},
		{
			name: "a new top-level internal package",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeGo(t, tree, "internal/bids/session.go", "package bids\n\ntype Session struct{}\n")
			},
			trigger: "internal/bids",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newADRFixture(t)
			tc.mutate(t, tree)

			out, code := runADRGate(t, tree, "## What and why\n\nCloses #123.\n")

			require.NotZero(t, code, "%s must require a decision record\n%s", tc.name, out)
			requireOnlyRule(t, out, "ADR001")
			require.Contains(t, out, tc.trigger,
				"ADR001 must name what triggered it — a gate that says only \"you need an ADR\" "+
					"leaves the author guessing which of four rules fired\n%s", out)
			require.Contains(t, out, "adr: n/a",
				"the failure must print the escape hatch verbatim; the author cannot look it up "+
					"from a rule id\n%s", out)
		})
	}
}

// TestRepoGates_AdrRecordPresent_PassesGate is the half that keeps ADR001 landable.
//
// The documents promise two ways to satisfy it and this asserts both. Without them a gate that
// rejected every triggering change would pass the test above — and the first author to hit an
// unsatisfiable rule reaches for a way around it rather than for the rule id.
func TestRepoGates_AdrRecordPresent_PassesGate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		body   string
		record func(t *testing.T, tree string)
	}{
		{
			name: "an adr: n/a line with a reason",
			body: "## What and why\n\nRe-orders two Dockerfile layers for cache hits.\n\nadr: n/a — no new port, volume or process\n",
		},
		{
			name: "a new file under docs/adr",
			body: "## What and why\n\nCloses #123.\n",
			record: func(t *testing.T, tree string) {
				t.Helper()
				writeRepoFile(t, tree, "docs/adr/0016-expose-a-metrics-port.md",
					"# 16. Expose a metrics port\n\nStatus: accepted\n")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newADRFixture(t)
			writeRepoFile(t, tree, "deploy/Dockerfile", adrBaseDockerfile+"EXPOSE 9090\n")

			if tc.record != nil {
				tc.record(t, tree)
			}

			out, code := runADRGate(t, tree, tc.body)

			require.Zero(t, code, "%s must satisfy ADR001\n%s", tc.name, out)
			require.NotContains(t, out, "FAIL", "%s", out)
		})
	}
}

// TestRepoGates_AdrWaiverWithoutReason_FailsGate pins the one property that makes the waiver worth
// having.
//
// `adr: n/a` on its own is the box ticked without the thought. Both documents specify
// `adr: n/a — <reason>`, and harvesting that reason is the entire value of the escape hatch: the
// reasons are what a later reader searches when the question is re-litigated. A marker that costs
// one token gets pasted onto the next PR too, and the gate becomes a formality within a month.
func TestRepoGates_AdrWaiverWithoutReason_FailsGate(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"adr: n/a",
		"adr: n/a —",
		"adr: n/a - ",
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			tree := newADRFixture(t)
			writeRepoFile(t, tree, "deploy/Dockerfile", adrBaseDockerfile+"EXPOSE 9090\n")

			out, code := runADRGate(t, tree, body)

			require.NotZero(t, code, "%q is a marker, not a reason\n%s", body, out)
			requireOnlyRule(t, out, "ADR001")
		})
	}
}

// TestRepoGates_AdrNonTriggeringChange_PassesGate is the scope control, and it is what stops ADR001
// from becoming a tax on every PR.
//
// Each case is a change that touches a WATCHED FILE without meeting the documented trigger. The
// go.mod pair is the operationally important one: Renovate bumps versions and adds indirects
// continuously, and a rule that fired on those would demand a waiver line on every dependency PR —
// which is how a gate stops being read and starts being pasted past.
func TestRepoGates_AdrNonTriggeringChange_PassesGate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, tree string)
	}{
		{
			name: "a version bump of an existing direct dependency",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeRepoFile(t, tree, "go.mod",
					strings.Replace(adrBaseGoMod, "testify v1.9.0", "testify v1.10.0", 1))
			},
		},
		{
			name: "a new indirect dependency",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeRepoFile(t, tree, "go.mod", strings.Replace(adrBaseGoMod,
					"\tgolang.org/x/sys v0.20.0 // indirect\n",
					"\tgolang.org/x/sys v0.20.0 // indirect\n\tgolang.org/x/text v0.15.0 // indirect\n", 1))
			},
		},
		{
			name: "a new file in an existing internal package",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeGo(t, tree, "internal/api/roster.go", "package api\n\nfunc roster() {}\n")
			},
		},
		{
			name: "a new sub-package of an existing internal package",
			mutate: func(t *testing.T, tree string) {
				t.Helper()
				writeGo(t, tree, "internal/api/compat/shim.go", "package compat\n\nfunc shim() {}\n")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newADRFixture(t)
			tc.mutate(t, tree)

			out, code := runADRGate(t, tree, "## What and why\n\nCloses #123.\n")

			require.Zero(t, code, "%s is not a documented ADR trigger\n%s", tc.name, out)
			require.NotContains(t, out, "ADR001",
				"ADR001 must stay silent on %s — a rule that fires on ordinary work is a rule "+
					"people learn to paste past\n%s", tc.name, out)
		})
	}
}

// TestRepoGates_AdrWithoutPullRequestContext_Skips records the one direction in which ADR001 is
// fail-open, so that nobody discovers it by accident.
//
// The rule reads the PR body, which exists only on a pull_request event. A local `make check` has
// none, so the gate SKIPS — loudly, with the rule id, the same way MIG003 skips without git
// history. What stops that skip from becoming the normal case is not this test but
// TestCI_LintRepoJob_PassesPullRequestContext, which pins the env block in ci.yml.
func TestRepoGates_AdrWithoutPullRequestContext_Skips(t *testing.T) {
	t.Parallel()

	tree := newADRFixture(t)
	writeRepoFile(t, tree, "deploy/Dockerfile", adrBaseDockerfile+"EXPOSE 9090\n")

	// No DKP_ADR_BASE_REF: exactly what a laptop run looks like.
	out, code := runGateScript(t, scriptPath(t, "repo-gates.sh"), tree, "DKP_ADR_BASE_REF=")

	require.Zero(t, code, "a run with no pull-request context must not fail\n%s", out)
	require.Contains(t, out, "[ADR001]",
		"the skip must name the rule — a gate that vanishes silently is indistinguishable in a CI "+
			"log from a gate that ran\n%s", out)
	require.Contains(t, out, "skip", "%s", out)
}

// TestRepoGates_AdrUnreadableBaseRef_FailsGate is the other side of that decision: a base ref that
// IS supplied and cannot be read is a VIOLATION, never a skip.
//
// That is the shallow-clone case, and it is the configuration most likely to have it — which is
// precisely why it must not be the quiet one. verify-spec.py makes the same distinction for
// SPEC003, and MIG003's fetch-depth: 0 exists for the same reason.
func TestRepoGates_AdrUnreadableBaseRef_FailsGate(t *testing.T) {
	t.Parallel()

	tree := newADRFixture(t)

	out, code := runGateScript(t, scriptPath(t, "repo-gates.sh"), tree,
		"DKP_ADR_BASE_REF=refs/remotes/origin/does-not-exist", "DKP_ADR_PR_BODY=")

	require.NotZero(t, code, "an unreadable base revision must fail, not skip\n%s", out)
	requireOnlyRule(t, out, "ADR001")
	require.Contains(t, out, "does-not-exist",
		"the failure must name the revision it could not read\n%s", out)
}

// TestPRTemplate_DoesNotPreSatisfyTheADRGate is the anti-vacuity test for ADR001.
//
// The template is the natural place to tell an author about the requirement, and the natural way to
// write that is a filled-in example — which every PR then inherits, satisfying the gate for every
// change in the repository forever. `github.event.pull_request.body` carries HTML comments too, so
// commenting the example out does not help. The guidance therefore has to describe the line without
// being one, and this test is what keeps a well-meaning edit from undoing that.
func TestPRTemplate_DoesNotPreSatisfyTheADRGate(t *testing.T) {
	t.Parallel()

	template := readRepoFile(t, ".github/PULL_REQUEST_TEMPLATE.md")

	for i, line := range strings.Split(template, "\n") {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		require.False(t, strings.HasPrefix(trimmed, "adr:"),
			".github/PULL_REQUEST_TEMPLATE.md:%d starts a line with `adr:`. Every PR inherits the "+
				"template, so a line ADR001 accepts here satisfies the gate for every change in "+
				"the repository — including inside an HTML comment, which the PR body still "+
				"carries. Describe the line instead of writing one.\n  %s", i+1, line)
	}
}

// TestRepoGates_EQdkpIdentifierOutsideAllowlist_FailsGate covers AGPL001, the licence firewall.
//
// EQdkp Plus is AGPL-3.0 and this project is Apache-2.0, so transcribing their PHP is not a style
// problem, it is a licence violation — and the moment it happens is when the task is "match EQdkp's
// behaviour", which is most of the importer's specification. `pdh_`, `gen_class`, `plus_exchange`
// and `__multidkp2event` are distinctive enough that a hit is always transcription and never
// coincidence.
//
// Two properties are asserted that no other test in this file asserts, and both are deliberate
// design decisions in the script rather than accidents of it:
//
//   - AGPL001 does NOT strip comments. Everywhere else a banned token inside a comment is prose about
//     the rule; here it is the thing itself, because pasting AGPL source into a Go comment "just as a
//     reference" infringes exactly as much as pasting it into code. A well-meaning refactor that gave
//     this gate the same `strip_comments` pipeline as its neighbours — for consistency — would open
//     the firewall, and this fixture is what would say so.
//   - The loop covers internal, web, cmd and db. A transcription can land in any of them; losing a
//     tree from that list is a silent hole.
func TestRepoGates_EQdkpIdentifierOutsideAllowlist_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeGo(t, tree, "internal/importer/points.go", `package importer

func readPoints(row map[string]any) int64 { return row["pdh_points"].(int64) }
`)

	// The comment case — a violation, not prose about one.
	writeGo(t, tree, "internal/importer/notes.go", `package importer

// gen_class is EQdkp's class table; the column list was copied from their schema.
`)

	// One violation per remaining gated tree, so that dropping ANY SINGLE element from the loop
	// turns this test red. Three of four trees is not the property this test claims: it leaves the
	// fourth free to be deleted in silence, which is the precise regression the assertions below
	// describe.
	writeRepoFile(t, tree, "web/src/lib/legacy.ts", `export const EXCHANGE_TABLE = "plus_exchange";
`)
	writeGo(t, tree, "cmd/dkp/import.go", `package main

const legacyEventTable = "__multidkp2event"
`)

	// The db/ leg, in the shape a transcription really takes there: a column name copied verbatim
	// out of EQdkp's schema into DKP's own. db/schema.hcl is the single source of schema truth, so a
	// name that lands here propagates into the migrations, the generated queries and the wire — and
	// unlike internal/importer, db/ has no allowlisted file where an EQdkp name is ever legitimate.
	writeRepoFile(t, tree, "db/schema.hcl", `table "member" {
  column "gen_class" { type = text }
}
`)

	// Allowlisted, and this half is what keeps the importer possible at all: reading a user's
	// database at runtime requires naming their tables somewhere. legacy_names.go is that somewhere,
	// and the compat shim answers their api.php function names.
	writeGo(t, tree, "internal/importer/legacy_names.go", `package importer

var legacyTables = []string{"pdh_points", "gen_class", "plus_exchange", "__multidkp2event"}
`)
	writeGo(t, tree, "internal/api/compat/shim.go", `package compat

const pointsColumn = "pdh_points"
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "an EQdkp identifier outside the allowlist must fail the gates\n%s", out)
	require.Contains(t, out, "AGPL001", "%s", out)
	require.Contains(t, out, "internal/importer/points.go:",
		"AGPL001 must name the offending file, repo-root-relative\n%s", out)
	require.Contains(t, out, "internal/importer/notes.go:",
		"AGPL001 must NOT strip comments: transcribing AGPL source into a Go comment is the same "+
			"licence violation as transcribing it into code\n%s", out)
	require.Contains(t, out, "web/src/lib/legacy.ts:",
		"AGPL001 scans web/ too — has the tree loop lost an element?\n%s", out)
	require.Contains(t, out, "cmd/dkp/import.go:",
		"AGPL001 scans cmd/ too — has the tree loop lost an element?\n%s", out)
	require.Contains(t, out, "db/schema.hcl:",
		"AGPL001 scans db/ too — has the tree loop lost an element?\n%s", out)

	require.NotContains(t, out, "internal/importer/legacy_names.go",
		"legacy_names.go is where EQdkp's table names are ALLOWED to be written down; the importer "+
			"cannot read their database without naming it\n%s", out)
	require.NotContains(t, out, "internal/api/compat/shim.go",
		"the api.php compat shim answers EQdkp's own function names by design\n%s", out)
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

// enumFixtureSchema is the db/schema.hcl the ENUM001 fixtures share.
//
// One file rather than one per case, because the rule is a state machine over the whole file —
// region in/out, inside/outside a check block, waived/not — and a fixture per case would exercise
// each transition against a fresh, empty state. The cases that MUST fire and the cases that MUST
// NOT are interleaved on purpose: the gate has to tell them apart in one pass, which is the thing
// that breaks when somebody widens the pattern.
//
// The marker text is the fixture catalogue's, byte for byte — writeEnumCatalogue declares the same
// two strings in Go. That linkage is the point: the generated region here is honoured *because* a
// catalogue in the fixture tree owns those markers, and TestRepoGates_FabricatedGeneratedMarker_FailsGate
// is the same schema with a pair nothing owns.
const (
	enumFixtureBegin = "  // BEGIN GENERATED — account enum CHECKs, from internal/account/kinds. Run make gen."
	enumFixtureEnd   = "  // END GENERATED — account enum CHECKs."
)

const enumFixtureSchema = `// The header prose. enums are text + check (x IN ('a','b')), lowercase snake_case — a gate that
// fires on the documentation of its own rule is a gate people route around.
table "account" {
` + enumFixtureBegin + `
  check "account_kind_enum" {
    expr = "kind IN ('person', 'system')"
  }

  check "account_system_key_enum" {
    expr = "system_key IS NULL OR system_key IN ('guild_bank', 'residue')"
  }
` + enumFixtureEnd + `

  check "account_person_shape" {
    expr = "((kind = 'person') = (person_id IS NOT NULL))"
  }
}

table "bid_session" {
  check "bid_session_state_enum" {
    expr = "state IN ('draft', 'open', 'extended', 'closing', 'resolved')"
  }

  check "bid_session_mode_enum" {
    expr = "mode IS NULL OR mode IN ('auction_open', 'auction_sealed_first')"
  }

  check "bid_session_visibility_enum" {
    expr = "visibility IN (\"blind\", \"open\")"
  }

  check "bid_session_blind_bool" {
    expr = "blind IN (0, 1)"
  }

  index "ux_bid_live" {
    where   = "state IN ('open', 'extended')"
    columns = [column.item_instance_id]
  }

  // dkp:enum-literal — a vendor vocabulary the importer reads, not a DKP catalogue.
  check "legacy_status_enum" {
    expr = "legacy_status IN ('a', 'b')"
  }
}
`

// TestRepoGates_HandWrittenEnumCheck_FailsGate covers ENUM001, the gate that closes canonical §5's
// last hole (#72).
//
// Every string-enum CHECK in db/schema.hcl is generated from a Go catalogue — ledger_batch.kind and
// .source (#29), audit_log.actor_kind (#40), audit_log.outcome plus account.kind and .system_key
// (#51/#53). Each has a test asserting that ITS OWN region matches ITS OWN catalogue, and none of
// them says anything about a seventh vocabulary: a new table whose enum CHECK is a literal passes
// all three, `make verify-generated` and `make check`. That is not hypothetical — the same finding
// has now been made three times, and canonical §5 lists ten more enums that Phase 1 and Phase 2
// land one table at a time.
//
// BOTH RENDERED FORMS are tainted, because internal/schemaenum renders two — CheckExpr's plain
// `x IN (…)` and NullableCheckExpr's `x IS NULL OR x IN (…)`. A fixture carrying only the first
// would let the second be forgotten, and the nullable form is the one a person account's
// system_key uses.
//
// The must-NOT-fire half is the larger one and it is what keeps the gate usable: a generated
// region, a shape CHECK that merely quotes a value, a boolean `IN (0, 1)`, an index predicate, and
// the file's own prose all have to stay quiet, or the first author to hit a false positive reaches
// for --no-verify rather than for the rule id.
// writeEnumCatalogue writes the fixture's Go catalogue — the package that OWNS the generated region
// in enumFixtureSchema, in the shape internal/account/kinds keeps its markers in.
//
// It exists because ENUM001 does not take the schema's word for what is generated: a region is
// exempt only when its marker line matches, whole, a marker some catalogue declares in Go. So a
// fixture whose generated region must be honoured has to carry the catalogue that owns it, and the
// linkage is what the test is asserting rather than an incidental setup step.
func writeEnumCatalogue(t *testing.T, tree, begin, end string) {
	t.Helper()

	writeGo(t, tree, "internal/account/kinds/kinds.go", "package kinds\n\nconst (\n"+
		"\tschemaEnumBegin = \""+begin+"\"\n"+
		"\tschemaEnumEnd   = \""+end+"\"\n)\n")
}

func TestRepoGates_HandWrittenEnumCheck_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "db/schema.hcl", enumFixtureSchema)
	writeEnumCatalogue(t, tree, enumFixtureBegin, enumFixtureEnd)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a hand-written string-enum CHECK must fail the gates\n%s", out)
	require.Contains(t, out, "ENUM001", "%s", out)
	require.Contains(t, out, "bid_session_state_enum",
		"ENUM001 must fire on a literal in the plain CheckExpr form and name the check\n%s", out)
	require.Contains(t, out, "bid_session_mode_enum",
		"ENUM001 must fire on the NullableCheckExpr form too — `x IS NULL OR x IN (…)` is the "+
			"second shape internal/schemaenum renders, and account.system_key uses it\n%s", out)
	require.Contains(t, out, "bid_session_visibility_enum",
		"ENUM001 must fire on DOUBLE-quoted values too. SQLite treats a double-quoted token that "+
			"resolves to no column as a string literal, so `IN (\"blind\", \"open\")` is a "+
			"hand-written vocabulary — changing quote style must not be a way past the gate\n%s", out)
	require.Contains(t, out, "db/schema.hcl:",
		"ENUM001 must name the offending file and line, repo-root-relative\n%s", out)

	require.NotContains(t, out, "account_kind_enum",
		"a CHECK between the BEGIN/END GENERATED markers is generated from a catalogue — that is "+
			"the sanctioned form and the whole point of the rule\n%s", out)
	require.NotContains(t, out, "account_system_key_enum",
		"the nullable form INSIDE a generated region is equally sanctioned\n%s", out)
	require.NotContains(t, out, "account_person_shape",
		"a shape CHECK quoting one value is not a vocabulary; ENUM001 must not fire on it\n%s", out)
	require.NotContains(t, out, "bid_session_blind_bool",
		"`IN (0, 1)` is a boolean, not a string enum — no catalogue could generate it\n%s", out)
	require.NotContains(t, out, "ux_bid_live",
		"an index predicate is not a CHECK: a partial index over a SUBSET of a vocabulary cannot "+
			"be rendered from a catalogue as-is, so ENUM001 is scoped to check blocks (#97)\n%s", out)
	require.NotContains(t, out, "lowercase snake_case",
		"ENUM001 fired on the schema's own header prose — comment lines are stripped\n%s", out)
	require.NotContains(t, out, "legacy_status_enum",
		"a `dkp:enum-literal` waiver WITH a reason is the documented exception and must be "+
			"honoured, or the first legitimate one is landed by weakening the gate\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_EnumLiteralWaiverWithoutReason_FailsGate is the other half of the waiver.
//
// `// dkp:enum-literal` with nothing after it is the box ticked without the thought — the same
// defect as a bare `adr: n/a`, and the same answer: the reason is the artefact, the marker is only
// its carrier. Without this test the reason requirement is decorative, and a waiver that costs one
// token is a waiver that gets pasted onto the next literal too.
func TestRepoGates_EnumLiteralWaiverWithoutReason_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "db/schema.hcl", `table "bid_session" {
  // dkp:enum-literal
  check "bid_tier_enum" {
    expr = "tier IN ('main', 'main_offspec', 'alt', 'anyone')"
  }
}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a waiver with no reason must not exempt a literal\n%s", out)
	require.Contains(t, out, "ENUM001", "%s", out)
	require.Contains(t, out, "no reason",
		"ENUM001 must say the waiver lacks a reason, not merely that a literal exists\n%s", out)
	require.Contains(t, out, "bid_tier_enum",
		"the unexempted check must still be reported — canonical §5 makes bid.tier's declaration "+
			"ORDER semantic, so this is the literal that costs the most\n%s", out)
}

// TestRepoGates_UnclosedGeneratedMarker_FailsGate closes the bypass that would otherwise make
// ENUM001 self-disabling.
//
// The rule is "a string-enum CHECK lies between BEGIN and END GENERATED". Region state is
// line-ordered, so a BEGIN with no matching END exempts every check after it — the entire rest of
// the file — from one unbalanced comment line. A gate whose own escape hatch is a typo is worse
// than no gate: it reports green while checking nothing, and the marker text is exactly the kind
// of line a careless merge mangles.
func TestRepoGates_UnclosedGeneratedMarker_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// The catalogue DOES own this marker pair, so the failure can only be the missing END — a
	// fixture whose marker was also unrecognised would go red for the other reason and prove
	// nothing about unbalanced regions.
	writeEnumCatalogue(t, tree, enumFixtureBegin, enumFixtureEnd)
	writeRepoFile(t, tree, "db/schema.hcl", `table "bid_session" {
`+enumFixtureBegin+`
  check "bid_session_state_enum" {
    expr = "state IN ('draft', 'open')"
  }
}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "an unclosed BEGIN GENERATED marker must fail the gates\n%s", out)
	require.NotContains(t, out, "no Go catalogue declares",
		"the fixture's marker IS declared — a hit here means this test is proving the wrong "+
			"thing\n%s", out)
	require.Contains(t, out, "ENUM001", "%s", out)
	require.Contains(t, out, "unclosed BEGIN GENERATED",
		"ENUM001 must name the unbalanced marker: it silently exempts every check below it\n%s", out)
}

// TestRepoGates_FabricatedGeneratedMarker_FailsGate closes the self-service exemption, and it is the
// bypass that mattered most: everything else ENUM001 does is undone by two comment lines without it.
//
// The markers are comments. Nothing stops an author writing a balanced
// `// BEGIN GENERATED` / `// END GENERATED` pair around a brand-new literal — and nothing downstream
// notices either, because `make gen` rewrites only the regions its catalogues declare, so a
// fabricated one is invisible to `make verify-generated` as well. The region would be "generated"
// in the sense that nothing generates it.
//
// So a marker counts only when a catalogue declares it in Go, and this fixture asserts both
// directions in one run: the declared pair exempts its region, the fabricated pair does not exempt
// anything and is itself reported. Asserting only the second would pass against a gate that had
// stopped honouring generated regions at all, which is the "fix" a red build invites.
func TestRepoGates_FabricatedGeneratedMarker_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeEnumCatalogue(t, tree, enumFixtureBegin, enumFixtureEnd)
	writeRepoFile(t, tree, "db/schema.hcl", `table "account" {
`+enumFixtureBegin+`
  check "account_kind_enum" {
    expr = "kind IN ('person', 'system')"
  }
`+enumFixtureEnd+`
}

table "bid_session" {
  // BEGIN GENERATED — bid_session enum CHECKs, from internal/bids/kinds. Run make gen.
  check "bid_session_state_enum" {
    expr = "state IN ('draft', 'open', 'extended')"
  }
  // END GENERATED — bid_session enum CHECKs.
}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a fabricated generated region must not exempt a literal\n%s", out)
	require.Contains(t, out, "ENUM001", "%s", out)
	require.Contains(t, out, "no Go catalogue declares",
		"ENUM001 must report the marker itself: a region nothing generates is a claim, not a "+
			"fact, and saying only \"hand-written CHECK\" would leave the author re-adding the "+
			"markers more carefully\n%s", out)
	require.Contains(t, out, "internal/bids/kinds",
		"the failure must quote the offending marker line\n%s", out)
	require.Contains(t, out, "bid_session_state_enum",
		"the smuggled CHECK must be reported too — the fabricated markers exempt nothing\n%s", out)

	require.NotContains(t, out, "account_kind_enum",
		"the region the fixture's catalogue DOES declare must still be exempt; a gate that "+
			"stopped honouring generated regions would satisfy every assertion above\n%s", out)
}

// TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues is ENUM001's Go twin, and it closes the
// one residue the shell gate cannot see.
//
// That gate honours a marker line because some `internal/*/kinds` package declares it — which leaves
// a narrow path open: declare a NEW marker const in Go, wire it into no generator, and the region it
// delimits is exempt from the gate while nothing regenerates it. This asserts the stronger property
// the shell cannot ask about without a Go toolchain: the marker pairs in db/schema.hcl are EXACTLY
// the pairs the registered catalogues own — no fabricated region, and no catalogue whose region has
// gone missing from the schema.
//
// The three catalogues are named here rather than enumerated, which is deliberate: a fourth added to
// `internal/ledger/enumgen`'s catalogues() puts a fourth marker pair in the schema, and this test
// then fails until it is listed here too. That is the correct direction for it to break — the
// alternative, reflecting over the tree, would silently accept a catalogue nobody registered.
func TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues(t *testing.T) {
	t.Parallel()

	var wantBegin, wantEnd []string

	for _, block := range []string{
		ledgerkinds.SchemaEnumBlock(),
		auditkinds.SchemaEnumBlock(),
		accountkinds.SchemaEnumBlock(),
	} {
		lines := strings.Split(block, "\n")
		require.GreaterOrEqual(t, len(lines), 2, "a generated block is at least its two markers")

		wantBegin = append(wantBegin, lines[0])
		wantEnd = append(wantEnd, lines[len(lines)-1])
	}

	var gotBegin, gotEnd []string

	for _, line := range strings.Split(readRepoFile(t, "db/schema.hcl"), "\n") {
		switch {
		case strings.Contains(line, "BEGIN GENERATED"):
			gotBegin = append(gotBegin, line)
		case strings.Contains(line, "END GENERATED"):
			gotEnd = append(gotEnd, line)
		}
	}

	require.ElementsMatch(t, wantBegin, gotBegin,
		"db/schema.hcl's BEGIN GENERATED markers must be exactly the ones the registered catalogues "+
			"declare. An extra one is a region nothing generates — ENUM001 would step over it and "+
			"`make gen` would never rewrite it; a missing one means a catalogue lost its region")
	require.ElementsMatch(t, wantEnd, gotEnd,
		"db/schema.hcl's END GENERATED markers must be exactly the ones the registered catalogues "+
			"declare")
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
