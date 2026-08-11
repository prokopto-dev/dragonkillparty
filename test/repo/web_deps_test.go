// Tests that the web dependency install is unconditional.
//
// `make vet` and `make dev` used to install web dependencies only when web/node_modules was ABSENT,
// which read a directory that EXISTS as a directory that is CURRENT. Any merge adding a web
// dependency left every existing checkout stale, the guard skipped the install, and `make check`
// failed thirty lines into tsc with implicit-any errors on files the reader had never opened —
// issue #64. CI never saw it: a fresh checkout has no node_modules, so the guard always fired there.
//
// The Makefile's own header states the rule this broke: "A target that CAN do real work must do it
// unconditionally. A guard that skips the work when its inputs are missing turns into a guard that
// hides a broken toolchain the moment the inputs exist."
package repo_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// makePrerequisites returns the prerequisites declared on a Makefile target's rule line.
func makePrerequisites(t *testing.T, target string) []string {
	t.Helper()

	for _, line := range strings.Split(readRepoFile(t, "Makefile"), "\n") {
		if !strings.HasPrefix(line, target+":") {
			continue
		}

		return strings.Fields(strings.TrimPrefix(line, target+":"))
	}

	t.Fatalf("the Makefile has no %s target", target)

	return nil
}

// TestWebDeps_InstallIsUnconditional asserts the recipe that installs the SPA's dependencies does it
// every time, and that no target has grown the directory guard back.
func TestWebDeps_InstallIsUnconditional(t *testing.T) {
	t.Parallel()

	recipe := makeRecipe(t, "web-deps")

	require.Contains(t, recipe, "pnpm install --frozen-lockfile --ignore-scripts",
		"make web-deps must run the frozen install: it is the one place every other target gets its "+
			"web dependencies from")
	require.NotContains(t, recipe, "web/node_modules",
		"make web-deps must not test for web/node_modules. A directory that exists is not a directory "+
			"that is current — that assumption is issue #64, and `pnpm install --frozen-lockfile` is "+
			"already a ~0.3 s no-op when the tree matches the lockfile.")

	// The guard is gone from the whole file, not just from the target it was extracted into. It lived
	// in two places (dev and vet) and a third copy would fail in exactly the same misdiagnosable way.
	makefile := readRepoFile(t, "Makefile")

	guard := regexp.MustCompile(`(?m)^[^#\n]*\[\s*-d\s+web/node_modules\s*\]`)
	require.NotRegexp(t, guard, makefile,
		"a `[ -d web/node_modules ] ||` guard is back in the Makefile. It skips the install exactly "+
			"when a dependency has just been added, which is the only time the install matters (issue #64).")

	// Every install in the file stays frozen and script-free: a bare `pnpm install` could resolve a
	// different tree than CI did and rewrite pnpm-lock.yaml as a side effect of running a make target.
	for _, line := range strings.Split(makefile, "\n") {
		if !strings.Contains(line, "pnpm install") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		require.Containsf(t, line, "--frozen-lockfile",
			"every `pnpm install` in the Makefile must be frozen — the lockfile is the contract:\n  %s", line)
		require.Containsf(t, line, "--ignore-scripts",
			"every `pnpm install` in the Makefile must skip lifecycle scripts (docs/design/03-security.md "+
				"§supply chain):\n  %s", line)
	}
}

// TestWebDeps_EveryTargetThatNeedsNodeModulesDependsOnIt asserts the three recipes that read
// web/node_modules get the install as a prerequisite rather than assuming one happened.
//
// Make resolves a prerequisite once per invocation, so `make check` — which runs both lint and vet —
// pays for exactly one install. That is what makes "always install" affordable enough to be honest.
func TestWebDeps_EveryTargetThatNeedsNodeModulesDependsOnIt(t *testing.T) {
	t.Parallel()

	// Each target runs a tool that lives in web/node_modules: tsc, eslint and vite respectively.
	for _, target := range []string{"vet", "lint-web", "dev"} {
		require.Containsf(t, makePrerequisites(t, target), "web-deps",
			"`make %s` runs a tool out of web/node_modules, so it must depend on web-deps. Without it "+
				"the target fails against a stale tree with an error that names a module, not the cause "+
				"(issue #64).", target)
	}
}
