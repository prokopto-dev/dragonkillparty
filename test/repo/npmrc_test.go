// Tests the committed .npmrc files — the supply-chain baseline of docs/design/03-security.md
// section 12, landed by issue #87.
//
// The protection existed before this as a flag repeated at six call sites (the Makefile's `setup`
// and `web-deps`, scripts/build-web.sh, scripts/gen-client.sh, scripts/test-e2e.sh,
// deploy/Dockerfile's web stage and the `lint / web` CI job), and web_lint_test.go's
// TestWebLint_PostinstallGuard_ScriptDoesNotRun proves the flag works. The gap was that the safe
// behaviour was opt-in per call site and the unsafe one is what a human gets by typing the obvious
// command — `cd web && pnpm install` — which is what every pnpm tutorial says and what somebody
// types when a make target fails for an unrelated reason.
//
// The issue asks for the test in the same change, and says why: a one-line config file with no test
// is the kind of thing a later "clean up dotfiles" PR removes.
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

// npmrcFiles are the two committed .npmrc files and what each one covers.
//
// BOTH, not one. The issue assumed "pnpm reads it from the project directory upward, so one file at
// the repo root covers web/". It does not: pnpm resolves the project .npmrc from the directory
// holding package.json and does not walk up, so with pnpm 9.15.9 and no pnpm-workspace.yaml,
// `cd web && pnpm config get ignore-scripts` returns `undefined` against a root-only file. web/ is
// where every install in this repository runs, so a root-only file would have been a decoration.
var npmrcFiles = map[string]string{
	".npmrc":     "a package manager run at the repository root",
	"web/.npmrc": "web/, where package.json lives and every install in this repository runs",
}

// TestNpmrc_IgnoreScripts_IsTheDefault asserts the setting is present and not negated.
func TestNpmrc_IgnoreScripts_IsTheDefault(t *testing.T) {
	t.Parallel()

	for path, covers := range npmrcFiles {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			var settings []string

			for _, line := range strings.Split(readRepoFile(t, path), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
					continue
				}

				settings = append(settings, line)
			}

			require.Containsf(t, settings, "ignore-scripts=true",
				"%s must set ignore-scripts=true — it covers %s. Lifecycle scripts are the primary npm "+
					"attack vector (docs/design/03-security.md section 12), and without this file the "+
					"obvious command runs every dependency's postinstall with the developer's credentials "+
					"(issue #87).", path, covers)

			require.NotContainsf(t, settings, "ignore-scripts=false",
				"%s re-enables lifecycle scripts wholesale. A dependency that genuinely needs one gets a "+
					"reviewed `pnpm.onlyBuiltDependencies` entry naming that package, never a blanket "+
					"re-enable.", path)
		})
	}
}

// TestNpmrc_ImageDependencyLayer_CopiesTheConfig keeps the Dockerfile's claim true.
//
// The web stage copies the manifest and lockfile into their own layer before the source tree, so a
// .tsx edit does not re-resolve the dependency graph — and its comment says that layer installs with
// "the same posture CI and build-web.sh install with". Without web/.npmrc in that COPY the posture
// is the flag alone, and the two could drift silently: the file is what makes the DEFAULT safe, and
// an image built from a layer that never saw it is an image where a future `pnpm install` typed
// without the flag runs lifecycle scripts as root at build time.
func TestNpmrc_ImageDependencyLayer_CopiesTheConfig(t *testing.T) {
	t.Parallel()

	dockerfile := readRepoFile(t, "deploy/Dockerfile")

	var copyLine string

	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(line, "COPY ") && strings.Contains(line, "web/pnpm-lock.yaml") {
			copyLine = line

			break
		}
	}

	require.NotEmpty(t, copyLine,
		"deploy/Dockerfile has no COPY of web/pnpm-lock.yaml — has the web stage been restructured?")

	require.Containsf(t, copyLine, "web/.npmrc",
		"deploy/Dockerfile's dependency layer copies the manifest and lockfile but not web/.npmrc, so "+
			"the install in that layer is protected only by the --ignore-scripts flag (issue #87):\n\t%s",
		copyLine)
}

// TestNpmrc_CarriesNoCredential asserts neither committed file holds registry auth.
//
// An .npmrc is the canonical place people put `//registry.npmjs.org/:_authToken=…`, and these two
// are committed. Adding the file is the moment to close that, not after somebody has done it.
func TestNpmrc_CarriesNoCredential(t *testing.T) {
	t.Parallel()

	for path := range npmrcFiles {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			for i, line := range strings.Split(readRepoFile(t, path), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#") {
					continue // prose about the rule is not the rule
				}

				for _, secret := range []string{"_authToken", "_auth=", "_password", "certfile", "keyfile"} {
					require.NotContainsf(t, trimmed, secret,
						"%s:%d looks like a credential (%s). This file is committed — registry auth belongs "+
							"in ~/.npmrc or an environment variable:\n\t%s", path, i+1, secret, line)
				}
			}
		})
	}
}

// TestNpmrc_SuppressesALifecycleScript is the functional half, and the reason this file is not just
// three string assertions.
//
// It installs the postinstall-guard fixture — whose postinstall writes a sentinel — with NO
// --ignore-scripts flag, so the only thing that can stop the script is the .npmrc beside the
// manifest. Then it does the same install WITHOUT the .npmrc, and requires the sentinel to appear:
// without that control the check would pass on any pnpm that happened not to run postinstall
// scripts, and would keep passing after somebody deleted the file it exists to protect.
//
// Gated on pnpm's presence and skipped under -short, mirroring
// TestWebLint_PostinstallGuard_ScriptDoesNotRun, which proves the same property for the flag.
func TestNpmrc_SuppressesALifecycleScript(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs pnpm install against a fixture; run `make test`")
	}

	// A FAILURE, not a skip, when CI is set: this test skipped in every CI run for the whole of
	// phase 0 because no job that runs `make test` installed Node, and it is the only functional
	// proof web/.npmrc does anything (issue #177). `test / integration` and nightly's
	// `suite / shuffled` now pass node: "true".
	requireTool(t, "pnpm", "ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` "+
		"must pass node: \"true\" to setup-toolchain")

	const sentinel = "postinstall-ran.sentinel"

	manifest, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "test-fixtures", "postinstall-guard", "package.json"))
	require.NoError(t, err, "read the postinstall-guard fixture package.json")

	npmrc := []byte(readRepoFile(t, "web/.npmrc"))

	// install copies the fixture into a fresh directory, optionally drops the repo's web/.npmrc in
	// beside it, runs a bare `pnpm install`, and reports whether the postinstall ran.
	install := func(t *testing.T, withNpmrc bool) bool {
		t.Helper()

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), manifest, 0o644))

		if withNpmrc {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".npmrc"), npmrc, 0o644))
		}

		// No --ignore-scripts: the flag is what the other test proves. This one is about the file.
		cmd := exec.Command("pnpm", "install")
		cmd.Dir = dir

		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "pnpm install must succeed (npmrc=%v)\n%s", withNpmrc, out)

		_, statErr := os.Stat(filepath.Join(dir, sentinel))

		return !errors.Is(statErr, os.ErrNotExist)
	}

	require.True(t, install(t, false),
		"the control install did not run the fixture's postinstall, so this test cannot tell a working "+
			".npmrc from a pnpm that ignores lifecycle scripts anyway — and would keep passing after the "+
			"file was deleted. Check web/test-fixtures/postinstall-guard/package.json.")

	require.False(t, install(t, true),
		"a bare `pnpm install` executed the fixture's postinstall with web/.npmrc beside the manifest. "+
			"ignore-scripts=true is not taking effect, so `cd web && pnpm install` runs every "+
			"dependency's lifecycle scripts (issue #87).")
}
