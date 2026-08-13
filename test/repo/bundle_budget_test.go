// Tests that the bundle budget is a measurement of the bundle.
//
// Two separate defects met in issue #166, and either one alone makes the gate report a pass it has
// not earned.
//
// The first: `budget / bundle` is in ci-required's `needs:` list — a REQUIRED, blocking job — and
// `make check` did not run it, while the Makefile two lines above `check` argues that a required job
// this target does not run makes its promise false. That argument was written for
// test-coverage-floor and applied word for word here. The cost of finding out was real: the esbuild
// bump behind #133/#134/#135 changed the minifier Vite ships, a green `make check` said nothing about
// the bundle, and the number had to be taken by hand to know the PR was safe.
//
// The second, found while fixing the first: scripts/budget-bundle.sh falls back to internal/ui/dist
// when web/dist is absent, and what lives there in a clean checkout is the committed placeholder. So
// `make budget-bundle` on its own — in CI, in the required job, on any laptop — was gzipping a few
// hundred bytes of scaffold and reporting 99% headroom. Adding a target that measures the placeholder
// to `make check` would have satisfied the issue and changed nothing, which is why the build is now
// part of the target rather than an instruction in a comment.
package repo_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCheck_RunsTheBundleBudget is the first half: the required job is one `make check` runs.
func TestCheck_RunsTheBundleBudget(t *testing.T) {
	t.Parallel()

	require.Contains(t, makePrerequisites(t, "check"), "budget-bundle",
		"`budget / bundle` is in ci-required's `needs:` list, so it is a required, blocking CI job, and "+
			"`make check` must run it — AGENTS.md tells every contributor and every agent that `make "+
			"check` is what \"done\" means. The two required jobs deliberately absent are security/osv "+
			"and security/govulncheck, and their reason does not apply here: they query api.osv.dev, "+
			"and `make check` must work on a laptop with no network. A Vite build and a gzip do not "+
			"(issue #166).")
}

// TestBundleBudget_BuildsTheBundleItMeasures is the second half, and the one that decides whether
// the first is worth anything.
//
// Asserted through the recipe rather than by running it: `make budget-bundle` is a pnpm install and a
// Vite build, which this package must not pay for in every run, and the recipe is where the defect
// would return — someone removing the build line to make the target faster would restore exactly the
// state #166 found.
func TestBundleBudget_BuildsTheBundleItMeasures(t *testing.T) {
	t.Parallel()

	recipe := makeRecipe(t, "budget-bundle")

	require.Contains(t, recipe, "scripts/build-web.sh",
		"`make budget-bundle` must BUILD the SPA before measuring it. Without a build there is no "+
			"web/dist, scripts/budget-bundle.sh falls back to the committed internal/ui/dist "+
			"placeholder, and the required gate measures a few hundred bytes of scaffold and passes "+
			"with 99% headroom whatever the real bundle does (issue #166).")
	require.Contains(t, recipe, "scripts/budget-bundle.sh",
		"`make budget-bundle` must still run the measurement it is named for")

	require.Less(t,
		strings.Index(recipe, "scripts/build-web.sh"), strings.Index(recipe, "scripts/budget-bundle.sh"),
		"the build must come BEFORE the measurement, or the first run of the target measures whatever "+
			"the previous one left in web/dist")

	require.Contains(t, makePrerequisites(t, "budget-bundle"), "web-deps",
		"budget-bundle runs Vite out of web/node_modules, so it takes the install as a prerequisite "+
			"like vet, lint-web and dev do — make resolves it once per invocation, so `make check` pays "+
			"for one install across all four (issue #64)")
}

// TestBundleBudget_DoesNotDirtyTheTree is why the build above is DKP_WEB_STAGE=0.
//
// scripts/build-web.sh stages its output into internal/ui/dist by deleting what is there, and what is
// there is TRACKED: the committed placeholders that keep internal/ui buildable with no JS toolchain.
// A full build therefore leaves `git status` showing two deleted files. That was an acceptable cost
// for `make build`, which a contributor types deliberately; it is not one for `make check`, which
// they are told to run before claiming any task is done — a gate that dirties the tree every run
// trains people to commit the dirt.
func TestBundleBudget_DoesNotDirtyTheTree(t *testing.T) {
	t.Parallel()

	require.Contains(t, makeRecipe(t, "budget-bundle"), "DKP_WEB_STAGE=0",
		"budget-bundle must build with DKP_WEB_STAGE=0: it measures web/dist, and staging into "+
			"internal/ui/dist would delete the tracked placeholders and leave every `make check` with a "+
			"dirty tree (issue #166)")

	// The variable has to be one the script actually honours. A recipe exporting a name nothing reads
	// is the #119 shape: a policy expressed as an assignment and implemented nowhere.
	script := readRepoFile(t, "scripts/build-web.sh")
	require.Contains(t, script, `"${DKP_WEB_STAGE:-1}"`,
		"scripts/build-web.sh must read DKP_WEB_STAGE, defaulting to staging. `make budget-bundle` sets "+
			"it, and a variable the script ignores would silently stage anyway — dirtying the tree on "+
			"every `make check` while the Makefile comment says it does not.")
	require.Contains(t, script, "cp -R web/dist/.",
		"scripts/build-web.sh must still stage for go:embed by default — DKP_WEB_STAGE=0 is the "+
			"measure-only path, not the new normal. `make build` produces the binary a guild installs.")
}
