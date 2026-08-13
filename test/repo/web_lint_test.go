package repo_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The client-purity lint gate, tested rather than trusted (docs/development/first-ten-prs.md).
//
// Law 4 has two mechanisms and TestLintRepo_HostileRepoRootEnv already proves the grep half fires.
// This file proves the AST-aware half: eslint must reject a bare fetch outside src/api
// (no-restricted-globals) AND a useEffect containing a fetch (no-restricted-syntax), the second of
// which the grep cannot see. The fixtures live under web/test-fixtures/lint/, in eslint.config.js's
// `ignores` so a bare `eslint .` (pnpm run lint) never flags them.
//
// These need `make test` with a Node toolchain, WHICH CI NOW HAS. That sentence used to read "which
// no CI job runs", and it was accurate and quietly expensive: `test / integration` installed no Node,
// so all five suites here skipped on every run and the only thing actually proving the fixtures trip
// their rules was scripts/lint-web-fixtures.sh in the `lint / web` job. Issue #177 gave the
// integration job Node and a frozen install, and eslintBin below now FAILS rather than skips when CI
// is set — so both mechanisms run on every PR, which is what the paragraph below always claimed.
//
// The CI-side lock remains scripts/lint-web-fixtures.sh, invoked as `pnpm run lint:fixtures` from
// `make lint-web` — it runs eslint --no-ignore over the same fixtures and fails if a deliberate
// violation is NOT caught. So the negative fixtures are proven to trip their rule in two places, in
// two different jobs. Neither is redundant: this one asserts the rule id a violation must produce,
// that one asserts the shell gate around it still fails a clean-looking tree.
//
// It runs eslint through the workspace's own binary (web/node_modules/.bin/eslint) with --no-ignore,
// because eslint.config.js lists test-fixtures/** in `ignores` so a bare `eslint .` stays green.

// webRoot returns the absolute path of the web/ directory.
func webRoot(t *testing.T) string {
	t.Helper()

	return filepath.Join(repoRoot(t), "web")
}

// eslintBin returns the path to the workspace eslint binary.
//
// It SKIPS the calling test when web dependencies are not installed, and FAILS it instead when CI is
// set (issue #177): these five suites skipped in every CI run because no job that ran `make test`
// installed Node, so the AST-aware half of law 4 was proven only by `lint / web`'s
// `pnpm run lint:fixtures`. `test / integration` installs web dependencies now, which is what makes
// the failure the right verdict.
func eslintBin(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(webRoot(t), "node_modules", ".bin", "eslint")
	requireToolAt(t, bin, "web/node_modules/.bin/eslint",
		"ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` must pass "+
			"node: \"true\" and run `pnpm install --frozen-lockfile --ignore-scripts` in web/")

	return bin
}

// runEslint runs the workspace eslint against one fixture, with --no-ignore so the ignored fixtures
// directory is linted. It returns the combined output and exit code.
func runEslint(t *testing.T, eslint, fixture string) (string, int) {
	t.Helper()

	cmd := exec.Command(eslint, "--no-ignore", fixture)
	cmd.Dir = webRoot(t)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run eslint: %v\n%s", err, out)

	return "", 0
}

// TestWebLint_BareFetch_FailsLint asserts a component making a bare fetch() outside src/api makes
// eslint exit non-zero, naming no-restricted-globals. This is the mechanism behind the PR 6
// acceptance criterion "a fixture component with a bare fetch() makes make lint exit non-zero".
func TestWebLint_BareFetch_FailsLint(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs eslint through the web toolchain; run `make test` or `make lint`")
	}

	eslint := eslintBin(t)

	fixture := filepath.Join(webRoot(t), "test-fixtures", "lint", "bare-fetch.tsx")
	out, code := runEslint(t, eslint, fixture)

	require.NotZero(t, code, "a bare fetch outside src/api must fail eslint\n%s", out)
	require.Contains(t, out, "no-restricted-globals",
		"the bare fetch must be rejected by no-restricted-globals\n%s", out)
}

// TestWebLint_UseEffectFetch_FailsLint asserts a useEffect body containing a fetch makes eslint exit
// non-zero, naming no-restricted-syntax — the rule the grep gate cannot express.
func TestWebLint_UseEffectFetch_FailsLint(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs eslint through the web toolchain; run `make test` or `make lint`")
	}

	eslint := eslintBin(t)

	fixture := filepath.Join(webRoot(t), "test-fixtures", "lint", "useeffect-fetch.tsx")
	out, code := runEslint(t, eslint, fixture)

	require.NotZero(t, code, "a useEffect containing a fetch must fail eslint\n%s", out)
	require.Contains(t, out, "no-restricted-syntax",
		"the useEffect-wrapped fetch must be rejected by no-restricted-syntax\n%s", out)
}

// TestWebLint_RawTokenValues_FailsLint covers canonical §17's token-layer rule over CODE.
//
// DS001/DS002 in scripts/repo-gates.sh enforce the same rule over CSS and deliberately do not scan
// TS/TSX: a grep cannot tell `style={{ padding: "4px" }}` from web/src/routes/design.tsx's visible
// copy "Base unit 4px x 0.70". An AST can, because JSX text is not a string literal — so this half
// is the only thing standing between a component and an inline raw value, and §17 says the rule holds
// throughout web/src rather than only in its stylesheets.
//
// The fixture carries every shape the rule must catch (a whole-value length, a whole-value colour,
// inline and multi-value declarations, and the template-literal spelling) AND a line of prose
// containing both "4px" and "#ff0000" that must NOT trip it.
func TestWebLint_RawTokenValues_FailsLint(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs eslint through the web toolchain; run `make test` or `make lint`")
	}

	eslint := eslintBin(t)

	fixture := filepath.Join(webRoot(t), "test-fixtures", "lint", "raw-token-values.tsx")
	out, code := runEslint(t, eslint, fixture)

	require.NotZero(t, code, "a raw px or hex in a component must fail eslint\n%s", out)
	require.Contains(t, out, "no-restricted-syntax",
		"the raw token value must be rejected by no-restricted-syntax\n%s", out)

	// The prose line is the last one in the fixture. If the rule fired on it, the AST selectors have
	// been widened into a grep and the "cannot tell a value from prose" argument no longer holds —
	// which is the failure mode that makes a gate get disabled rather than fixed.
	require.NotContains(t, out, "32:",
		"the rule fired on JSX text containing \"4px\" — it must match literals, not prose\n%s", out)
}

// TestWebLint_CleanComponent_PassesLint is the control: a component using the generated client the
// sanctioned way must PASS eslint. Without it, a config that fired on everything would satisfy both
// negative tests above while making the real SPA unlintable — and the first person to hit that would
// reach for a disable comment rather than for the rule.
func TestWebLint_CleanComponent_PassesLint(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs eslint through the web toolchain; run `make test` or `make lint`")
	}

	eslint := eslintBin(t)

	// The landing route uses useSuspenseQuery over the generated client — the sanctioned pattern.
	clean := filepath.Join(webRoot(t), "src", "routes", "index.tsx")
	out, code := runEslint(t, eslint, clean)

	require.Zero(t, code, "the sanctioned client pattern must pass eslint\n%s", out)
}

// TestWebLint_PostinstallGuard_ScriptDoesNotRun proves CI's `pnpm install --frozen-lockfile
// --ignore-scripts` does not execute lifecycle scripts. The fixture package's postinstall would
// write a sentinel file; installing it with --ignore-scripts must leave the sentinel absent.
//
// This is the mechanism behind the acceptance criterion "a fixture with a postinstall proves the
// script does not execute". It needs pnpm, not eslint, so it gates on pnpm's presence.
func TestWebLint_PostinstallGuard_ScriptDoesNotRun(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs pnpm install against a fixture; run `make test`")
	}

	requireTool(t, "pnpm", "ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` "+
		"must pass node: \"true\" to setup-toolchain (issue #177)")

	src := filepath.Join(webRoot(t), "test-fixtures", "postinstall-guard")

	// Copy the fixture into t.TempDir() so the install writes there, never into the repo.
	dir := t.TempDir()
	pkg, err := os.ReadFile(filepath.Join(src, "package.json"))
	require.NoError(t, err, "read the postinstall-guard fixture package.json")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), pkg, 0o644))

	cmd := exec.Command("pnpm", "install", "--ignore-scripts")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "pnpm install --ignore-scripts must succeed\n%s", out)

	_, statErr := os.Stat(filepath.Join(dir, "postinstall-ran.sentinel"))
	require.True(t, errors.Is(statErr, os.ErrNotExist),
		"the postinstall script executed despite --ignore-scripts — the sentinel exists\n%s", out)
}
