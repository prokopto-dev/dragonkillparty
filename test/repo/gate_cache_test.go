// Tests that CI's caches can make the repository FASTER but never QUIETER.
//
// Phase 0 gave `$GOCACHE` a rolling, roll-forward cache key so unchanged packages stop recompiling
// on every run (issue #153). That is free for compilation — Go's build cache is content-addressed,
// so a stale entry is a miss and never a wrong answer — and it is emphatically not free for
// `go test`'s RESULT cache, which is the other half of what a warm `$GOCACHE` restores.
//
// The result cache is keyed on the files and environment variables a test reads THROUGH GO. It
// cannot see what a SUBPROCESS reads. Most of this package reaches its subject through one:
// `bash scripts/repo-gates.sh`, `make licence-gate`, `go run ./internal/migrate/shippedlock`,
// `python3`, `eslint`, `pnpm`. Change the very script a gate polices and that gate's Go inputs are
// byte-identical, so `go test` is entitled to report `ok (cached)` having executed nothing — the
// gate reports green on exactly the change it exists to catch. `-count=1` is what forbids that, and
// this file is what keeps `-count=1` where it is:
//
//   - TestGoTest_ResultCache_MissesASubprocessInput_UnlessCountOne demonstrates the false hit on a
//     fixture, so the hazard is an executable fact rather than a comment somebody trims.
//   - TestMakefile_EveryGoTestRecipe_ForcesRerun asserts every `go test` this repository runs
//     carries the flag.
//   - TestSetupToolchain_GoBuildCache_WritesOnMainReadsEverywhere pins the cache doctrine itself.
//
// The other half of a cache that is quieter than it looks is a cache that is not the cache it says
// it is. `setup-go`'s `cache: true` archived `$GOCACHE` *and* `$GOMODCACHE` together under go.sum's
// hash, so after the rolling cache landed the build cache was stored twice and the second copy was
// never read back (issue #157). The remaining tests hold the two apart — and hold the pnpm store to
// the same rule, because setup-node's built-in `cache:` input is the identical trap in the Node
// half:
//
//   - TestSetupToolchain_ModuleCache_IsItsOwnCache
//   - TestSetupToolchain_PnpmStore_IsCachedOnce
//   - TestSetupToolchain_EveryCacheStep_IsAccountedFor
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

// toolchainActionRel is the composite action that installs Go for every job in every workflow, and
// therefore the one place the build cache has to be configured.
const toolchainActionRel = ".github/actions/setup-toolchain/action.yml"

// goTestInvocationRe matches a `go test` call — `$(GO) test` in the Makefile, a bare `go test` in a
// script. The trailing boundary keeps it off `go testdata` and friends.
var goTestInvocationRe = regexp.MustCompile(`(?:\$\(GO\)|(?:^|[;&|(\s])go)\s+test(?:\s|$)`)

// lineContinuation joins a shell/make backslash continuation so an invocation's flags are on one
// logical line, wherever the author wrapped it.
var lineContinuation = regexp.MustCompile(`\\\n\s*`)

// goModVersion returns the `go` directive of the repository's go.mod ("1.24.0").
//
// The fixture module below reuses it rather than hardcoding a version: CI sets GOTOOLCHAIN=local, so
// a fixture asking for a language version newer than the installed toolchain fails to build, and the
// failure would read as this test being broken rather than as the pin being wrong.
func goModVersion(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	require.NoError(t, err, "read go.mod")

	m := regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`).FindStringSubmatch(string(body))
	require.NotNil(t, m, "go.mod has no `go` directive")

	return m[1]
}

// TestGoTest_ResultCache_MissesASubprocessInput_UnlessCountOne is the reason `-count=1` is not
// optional, shown rather than asserted.
//
// A fixture package in t.TempDir() shells out to a script and checks what it prints — the exact
// shape of every gate in this directory. The test then changes what the script prints, which is a
// change the fixture MUST notice, and watches `go test` report a cached pass anyway. Only
// `-count=1` catches it.
//
// If this test ever fails at the "still cached" step, the Go toolchain has started tracking
// subprocess inputs and `-count=1` could be reconsidered — read that failure as news, not as a
// broken test, and do not delete the assertion to go green.
func TestGoTest_ResultCache_MissesASubprocessInput_UnlessCountOne(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("compiles and runs a fixture module four times; runs under `make test`")
	}

	if strings.TrimSpace(goEnv(t, "GOCACHE")) == "off" {
		t.Skip("GOCACHE=off — there is no result cache to demonstrate")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "gate.sh")

	writeFixtureFile(t, script, "#!/usr/bin/env bash\necho ALLOWED\n", 0o755)
	writeFixtureFile(t, filepath.Join(dir, "go.mod"),
		fmt.Sprintf("module gatecachefixture\n\ngo %s\n", goModVersion(t)), 0o644)

	// The fixture's SOURCE embeds its own directory, which is unique per run. Without that, two runs
	// of this test compile byte-identical source, and the FIRST invocation below could be served
	// from the cache a previous run left behind — the test would then assert its way through a
	// sequence it never actually observed.
	writeFixtureFile(t, filepath.Join(dir, "gate_test.go"), fmt.Sprintf(`package gate

import (
	"os/exec"
	"strings"
	"testing"
)

// Fixture for %[1]s. The path is here to keep this source unique per run.
func TestGate(t *testing.T) {
	out, err := exec.Command("bash", %[1]q).CombinedOutput()
	if err != nil {
		t.Fatalf("run the gate script: %%v\n%%s", err, out)
	}

	if got := strings.TrimSpace(string(out)); got != "ALLOWED" {
		t.Fatalf("the gate script now says %%q", got)
	}
}
`, script), 0o644)

	run := func(t *testing.T, flags ...string) (output string, passed bool) {
		t.Helper()

		cmd := exec.Command("go", append(append([]string{"test"}, flags...), ".")...)
		cmd.Dir = dir
		// GOFLAGS is cleared so a developer's `-count=1` there cannot make the "still cached" step
		// pass for the wrong reason, and GOWORK so a workspace above t.TempDir() cannot adopt the
		// fixture module.
		cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")

		out, err := cmd.CombinedOutput()

		return string(out), err == nil
	}

	first, passed := run(t)
	require.True(t, passed, "the fixture gate must pass before anything is changed\n%s", first)
	require.NotContains(t, first, "(cached)", "the first run of a unique package cannot be cached\n%s", first)

	second, passed := run(t)
	require.True(t, passed, "an unchanged fixture must still pass\n%s", second)
	require.Contains(t, second, "(cached)",
		"go test did not cache an unchanged package, so the rest of this test would prove nothing\n%s", second)

	// The guarded input changes: the script now prints something the fixture rejects. A gate that
	// re-ran would fail here — that is the whole point of the fixture.
	writeFixtureFile(t, script, "#!/usr/bin/env bash\necho DENIED\n", 0o755)

	third, passed := run(t)
	require.True(t, passed,
		"THE HAZARD: go test's result cache tracks files read through Go, not through a subprocess, "+
			"so a changed script is expected to be invisible here. A failure means the toolchain now "+
			"tracks subprocess inputs — news worth acting on, not a test to delete\n%s", third)
	require.Contains(t, third, "(cached)",
		"the false cache hit this file exists to document did not occur\n%s", third)

	fourth, passed := run(t, "-count=1")
	require.False(t, passed,
		"-count=1 must force the fixture to re-run and SEE the changed script. If this passes, every "+
			"shell-out gate in test/repo can report green without executing (issue #153)\n%s", fourth)
	require.Contains(t, fourth, "DENIED",
		"the re-run must fail on the script's new output, not on something else\n%s", fourth)
	require.NotContains(t, fourth, "(cached)", "-count=1 must not serve a cached result\n%s", fourth)
}

// TestMakefile_EveryGoTestRecipe_ForcesRerun asserts that every `go test` this repository runs —
// from the Makefile, from a gate script, from a git hook, from a skill's verify script — passes
// `-count=1`.
//
// Whole-file scan rather than a list of known recipes: the next `go test` somebody adds is the one
// that would be missed, and it is also the one most likely to be a new gate. The single exemption
// is a benchmark recipe that runs no tests at all, and the test checks that the exemption is that
// narrow rather than trusting the word `-bench`.
func TestMakefile_EveryGoTestRecipe_ForcesRerun(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	files := []string{filepath.Join(root, "Makefile")}
	for _, tree := range []string{"scripts", ".githooks", ".claude/skills"} {
		files = append(files, shellFilesUnder(t, filepath.Join(root, filepath.FromSlash(tree)))...)
	}

	checked := 0

	for _, path := range files {
		body, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", path)

		rel, err := filepath.Rel(root, path)
		require.NoError(t, err, "relativise %s", path)

		for _, line := range strings.Split(lineContinuation.ReplaceAllString(string(body), " "), "\n") {
			// A comment mentioning `go test` is prose about the rule, not a breach of it — the same
			// treatment scripts/repo-gates.sh gives every gate it runs.
			if trimmed := strings.TrimLeft(line, " \t"); strings.HasPrefix(trimmed, "#") {
				continue
			}

			if !goTestInvocationRe.MatchString(line) {
				continue
			}

			checked++

			// `-bench` with an empty `-run` selects zero tests, and `go test` never caches a
			// benchmark result, so `-count` there means "how many samples" and must stay free.
			// Requiring the empty `-run` keeps the exemption honest: a `-bench` recipe that ALSO ran
			// tests would otherwise slip through it. Matched as a prefix because make doubles the
			// dollar — `-run '^$$'` in the Makefile is `-run '^$'` in a script.
			if strings.Contains(line, "-bench") {
				require.Contains(t, line, `-run '^$`,
					"%s runs benchmarks without an empty `-run`, so it also runs tests and cannot be "+
						"exempt from -count=1:\n%s", rel, strings.TrimSpace(line))

				continue
			}

			require.Contains(t, line, "-count=1",
				"%s runs `go test` without -count=1. go test's result cache tracks files read through "+
					"Go, NOT files a subprocess reads, so a gate that shells out reports `ok (cached)` "+
					"when only its script or workflow changed — green on the change it exists to catch "+
					"(issue #153, docs/design/04-testing.md). The line:\n%s",
				rel, strings.TrimSpace(line))
		}
	}

	// A regex that matched nothing would pass every assertion above having checked no recipe — the
	// vacuous green this repository fails targets for elsewhere.
	require.GreaterOrEqual(t, checked, 5,
		"only %d `go test` invocations found across the Makefile, scripts/, .githooks/ and "+
			".claude/skills/ — the scan is broken, not the repository", checked)
}

// TestSetupToolchain_GoBuildCache_WritesOnMainReadsEverywhere pins the rolling `$GOCACHE` cache
// (issue #153) to the doctrine ci.yml's Docker layer cache already follows: PR branches restore,
// only main writes.
//
// Actions caches are branch-scoped with read-through to the default branch, so a write from a PR is
// invisible to every other PR and is evicted almost immediately — it burns quota against the shared
// 10 GB limit while evicting the entries that ARE shared. Swapping the restore-only step for the
// full action is a one-word edit that costs nothing visible and quietly makes every PR slower.
func TestSetupToolchain_GoBuildCache_WritesOnMainReadsEverywhere(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainActionRel)

	restore := toolchainCacheStep(t, action, buildCacheRestoreStep)
	save := toolchainCacheStep(t, action, buildCacheSaveStep)

	require.Contains(t, restore.body, "github.ref != 'refs/heads/main'",
		"the restore-only step must be the one that runs OFF main:\n%s", restore.body)
	require.Contains(t, save.body, "github.ref == 'refs/heads/main'",
		"only main may save the shared build cache — a PR's write is invisible to every other PR and "+
			"evicts the entries that are shared:\n%s", save.body)

	// GitHub does not support YAML anchors in workflow or action files, so the key and restore-keys
	// are a literal copy in the two steps. This is the assertion that stops the copies drifting —
	// a PR restoring under a key main never writes is a cache that silently never hits.
	require.Equal(t, restore.path, save.path, "both cache steps must archive the same directory")
	require.Equal(t, restore.key, save.key, "the two cache steps must share one key")
	require.Equal(t, restore.restoreKeys, save.restoreKeys, "the two cache steps must share one restore-keys list")

	require.True(t, strings.HasSuffix(restore.key, "${{ github.sha }}"),
		"the key must end in the commit sha so it ROLLS FORWARD: a key that is hit exactly is never "+
			"rewritten, which is precisely why setup-go's go.sum-keyed archive goes stale (issue #153). "+
			"Got %q", restore.key)

	require.NotEmpty(t, restore.restoreKeys, "without restore-keys the per-commit key can never hit")

	for _, prefix := range restore.restoreKeys {
		require.True(t, strings.HasSuffix(prefix, "-"),
			"a restore-key is matched as a PREFIX; %q does not end in a separator, so it can match a "+
				"neighbouring key it was never meant to", prefix)
		require.True(t, strings.HasPrefix(restore.key, prefix),
			"restore-key %q is not a prefix of the key %q, so it can only ever restore another job's "+
				"cache or nothing at all", prefix, restore.key)
	}

	// The narrowest fallback first: `go test -race` objects are not the objects `go build` produced,
	// so a job restoring another job's lane gets a cache that mostly misses.
	for i := 1; i < len(restore.restoreKeys); i++ {
		require.Less(t, len(restore.restoreKeys[i]), len(restore.restoreKeys[i-1]),
			"restore-keys are tried in order, so each must be a SHORTER prefix than the one before "+
				"it — otherwise the broadest lane answers first and the specific one never runs:\n%v",
			restore.restoreKeys)
	}
}

// The `name:` of every cache step in the composite action. Steps are selected by name rather than by
// `uses:` because there are now four caches and three of them are `actions/cache@` — the helper this
// replaced demanded exactly one such step, which the module cache alone would have broken.
const (
	moduleCacheStep       = "Restore and save the Go module cache"
	buildCacheRestoreStep = "Restore the Go build cache"
	buildCacheSaveStep    = "Restore and save the Go build cache (main only)"
	pnpmStoreCacheStep    = "Restore and save the pnpm store"
)

// TestSetupToolchain_ModuleCache_IsItsOwnCache is issue #157: the module cache and the build cache
// are two caches with two keys, and `setup-go`'s `cache: true` is not a way to have the first one.
//
// setup-go's cache path list is hardcoded to BOTH `go env GOMODCACHE` and `go env GOCACHE`, archived
// under one key derived from go.sum. That key is right for the modules — they change when and only
// when go.sum does — and wrong for the build cache, which every source edit invalidates. Turning it
// back on would not "also cache the modules": it would restore the second, stale copy of $GOCACHE
// this repository already pays a rolling cache to keep warm, and upload it again on every go.sum
// change.
//
// The failure this guards against is silent in both directions. Re-enabling `cache: true` costs
// quota and nothing else visible; deleting the explicit module cache below makes every Go job
// re-download the whole graph and shows up only as CI slowly getting slower.
func TestSetupToolchain_ModuleCache_IsItsOwnCache(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainActionRel)

	// Anchored to the YAML line, not to the string: this file's own comments discuss `cache: true`
	// in prose, and a substring match on the comment would pass while the setting said something
	// else — the tautology this package exists to avoid.
	require.NotRegexp(t, `(?m)^\s+cache: true$`, action,
		"setup-go's `cache:` must stay false: it archives $GOCACHE alongside $GOMODCACHE under "+
			"go.sum's hash, so with the rolling build cache below it stores the build cache twice and "+
			"reads the second copy back never (issue #157). The module cache is the %q step.",
		moduleCacheStep)

	modules := toolchainCacheStep(t, action, moduleCacheStep)
	build := toolchainCacheStep(t, action, buildCacheRestoreStep)

	require.NotEqual(t, modules.path, build.path,
		"the module cache and the build cache must archive DIFFERENT directories — one directory "+
			"under two keys is exactly the duplication issue #157 is about")

	require.Contains(t, modules.key, "hashFiles('go.sum')",
		"the module cache must be keyed on go.sum's hash: $GOMODCACHE changes when and only when "+
			"go.sum does. Got %q", modules.key)
	require.NotContains(t, modules.key, "github.sha",
		"the module cache key must NOT roll per commit — that is the build cache's shape, and here it "+
			"would upload the whole module graph on every push. Got %q", modules.key)

	require.NotEmpty(t, modules.restoreKeys,
		"without a prefix restore-key a go.sum bump starts from a cold module cache instead of "+
			"restoring the previous graph and downloading only what moved")

	for _, prefix := range modules.restoreKeys {
		require.True(t, strings.HasSuffix(prefix, "-"),
			"a restore-key is matched as a PREFIX; %q does not end in a separator, so it can match a "+
				"neighbouring key it was never meant to", prefix)
		require.True(t, strings.HasPrefix(modules.key, prefix),
			"restore-key %q is not a prefix of the key %q, so it can only ever restore another "+
				"cache's entry or nothing at all", prefix, modules.key)
	}

	// Both paths come from `go env`, not from a literal: $GOCACHE and $GOMODCACHE are
	// platform-dependent and either may be preset by the runner image, and a cache that archives the
	// wrong directory is a silent no-op rather than an error.
	require.Contains(t, action, `printf 'modules=%s\n' "$(go env GOMODCACHE)"`,
		"the module cache's path must be read from `go env GOMODCACHE`")
	require.Contains(t, action, `printf 'build=%s\n' "$(go env GOCACHE)"`,
		"the build cache's path must be read from `go env GOCACHE`")
}

// TestSetupToolchain_PnpmStore_IsCachedOnce holds the Node half to the same rule.
//
// `actions/setup-node` has a `cache:` input that archives the pnpm store from its own post step
// under its own key. Switching it on alongside the explicit cache below would archive one directory
// twice — issue #157 again, in the half of the toolchain where nothing had been cached at all.
func TestSetupToolchain_PnpmStore_IsCachedOnce(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainActionRel)

	require.NotRegexp(t, `(?m)^\s+cache: (pnpm|npm|yarn)\s*$`, action,
		"setup-node must not enable its own package-manager cache: it would archive the same pnpm "+
			"store a second time, under a second key, from a second post step. The store is cached by "+
			"the %q step.", pnpmStoreCacheStep)

	store := toolchainCacheStep(t, action, pnpmStoreCacheStep)

	require.Contains(t, store.key, "hashFiles('web/pnpm-lock.yaml')",
		"the pnpm store must be keyed on the lockfile — web/ holds the only package.json in the "+
			"repository. Got %q", store.key)
	require.NotEmpty(t, store.restoreKeys, "without a prefix restore-key a lockfile bump starts cold")

	require.Contains(t, action, `pnpm store path`,
		"the store's location must be read from `pnpm store path`: it is platform-dependent and "+
			"settable from .npmrc, so a literal path would archive the wrong directory and look like a "+
			"cache that simply never hits")
}

// TestSetupToolchain_EveryCacheStep_IsAccountedFor keeps the inventory closed.
//
// docs/design/06-cicd-and-release.md budgets the caches against the 10 GB per-repository limit, and
// that budget is only meaningful while this action is the one place they are declared. A fifth cache
// added here without a row there is how a repository ends up evicting the entries it reads to make
// room for ones it does not.
func TestSetupToolchain_EveryCacheStep_IsAccountedFor(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainActionRel)

	want := []string{moduleCacheStep, buildCacheRestoreStep, buildCacheSaveStep, pnpmStoreCacheStep}

	require.ElementsMatch(t, want, toolchainCacheStepNames(t, action),
		"the caches declared in %s have changed. Each is budgeted in "+
			"docs/design/06-cicd-and-release.md section 4 and each needs a write policy (immutable key "+
			"-> save anywhere; rolling key -> restore everywhere, save on main). Add the step to this "+
			"list and to that table in the same change.", toolchainActionRel)
}

// cacheStep is the parsed shape of one `actions/cache` step in a workflow or composite action.
type cacheStep struct {
	body        string
	uses        string
	path        string
	key         string
	restoreKeys []string
}

// parseCacheStep reads the cache inputs out of one step's body.
//
// Steps are read as text rather than through a YAML library: this package has no YAML dependency,
// and adding one to parse files whose shape is asserted line by line anyway would be a dependency
// decision (AGENTS.md) taken for a test.
func parseCacheStep(body string) cacheStep {
	parsed := cacheStep{body: body}

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "uses:"):
			parsed.uses = strings.Fields(strings.TrimPrefix(trimmed, "uses:"))[0]
		case strings.HasPrefix(trimmed, "path:"):
			parsed.path = strings.TrimSpace(strings.TrimPrefix(trimmed, "path:"))
		case strings.HasPrefix(trimmed, "key:"):
			parsed.key = strings.TrimSpace(strings.TrimPrefix(trimmed, "key:"))
		case trimmed == "restore-keys: |":
			parsed.restoreKeys = nil
		case strings.HasPrefix(trimmed, "${{ runner.os }}") && parsed.key != "":
			parsed.restoreKeys = append(parsed.restoreKeys, trimmed)
		}
	}

	return parsed
}

// isCacheAction reports whether a `uses:` value is one of the two cache actions.
func isCacheAction(uses string) bool {
	return strings.HasPrefix(uses, "actions/cache@") || strings.HasPrefix(uses, "actions/cache/restore@")
}

// toolchainSteps splits the composite action into its steps. A step's body carries the comment lines
// that follow it, which is harmless: every assertion here matches a YAML key at the start of a
// trimmed line, and a comment line starts with `#`.
func toolchainSteps(action string) []string {
	return strings.Split(action, "\n    - ")[1:]
}

// toolchainCacheStepNames returns the `name:` of every cache step in the composite action.
func toolchainCacheStepNames(t *testing.T, action string) []string {
	t.Helper()

	var names []string

	for _, step := range toolchainSteps(action) {
		if !strings.HasPrefix(step, "name: ") {
			continue
		}

		name := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(step, "name: "), "\n", 2)[0])
		if !isCacheAction(parseCacheStep(step).uses) {
			continue
		}

		names = append(names, name)
	}

	require.NotEmpty(t, names, "no cache steps parsed out of %s — has its formatting changed?", toolchainActionRel)

	return names
}

// toolchainCacheStep extracts the single step of the setup-toolchain action named name, and parses
// the cache inputs out of it.
func toolchainCacheStep(t *testing.T, action, name string) cacheStep {
	t.Helper()

	var found []string

	for _, step := range toolchainSteps(action) {
		if strings.HasPrefix(step, "name: "+name+"\n") {
			found = append(found, step)
		}
	}

	require.Lenf(t, found, 1,
		"expected exactly one step named %q in %s, found %d — the caches are configured here and "+
			"nowhere else (issues #153 and #157)", name, toolchainActionRel, len(found))

	parsed := parseCacheStep(found[0])

	require.Regexpf(t, `@[0-9a-f]{40}$`, parsed.uses,
		"every action is pinned to a 40-character commit SHA (PIN001): %s", parsed.uses)
	require.Truef(t, isCacheAction(parsed.uses),
		"step %q is not a cache step; it uses %s", name, parsed.uses)
	require.NotEmptyf(t, parsed.path, "no `path:` in the %q step", name)
	require.NotEmptyf(t, parsed.key, "no `key:` in the %q step", name)

	return parsed
}

// goEnv returns one `go env` value.
func goEnv(t *testing.T, name string) string {
	t.Helper()

	out, err := exec.Command("go", "env", name).Output()
	require.NoError(t, err, "go env %s", name)

	return string(out)
}

// writeFixtureFile writes one file of the throwaway module in t.TempDir().
func writeFixtureFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(body), mode), "write %s", path)
}

// shellFilesUnder returns every *.sh under dir, recursively. A missing directory returns nothing
// rather than failing: the trees scanned are the ones that hold executables today, and a repository
// layout change should not fail this test for an unrelated reason.
func shellFilesUnder(t *testing.T, dir string) []string {
	t.Helper()

	var out []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(path, ".sh") {
			out = append(out, path)
		}

		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err, "walk %s", dir)

	return out
}
