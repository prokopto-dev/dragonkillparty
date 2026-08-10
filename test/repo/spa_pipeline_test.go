// Tests that every build path which produces a shipped binary builds the SPA first.
//
// internal/ui/dist carries COMMITTED PLACEHOLDERS so the package compiles with no JS toolchain, and
// that safety net is what made issue #55 survive a whole phase: deploy/Dockerfile ran `go build`
// directly, so the image compiled, linked, booted, passed its healthcheck, passed its first-boot
// smoke — and served "web UI not yet built into this binary" to anyone who opened it. Nothing about
// a placeholder embed is detectable from anywhere except the bytes that come back from `GET /`.
//
// So the gates are here rather than beside the feature, per AGENTS.md: "test/repo/ — tests about the
// repository itself, not the product: they assert the gates in this file actually fire."
//
// Three layers, and each one is tested for the direction that matters:
//
//	scripts/verify-spa-dist.sh   fails the BUILD when the staged dist is not the real SPA
//	scripts/smoke-spa.sh         fails the SMOKE when a running server does not serve it
//	the config gates below       fail the PR when a build path stops calling either
package repo_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// placeholderMarker is the sentence internal/ui's committed placeholder index.html carries, and the
// one both gate scripts grep for to name the cause in their failure message. Kept in one place here
// so a test that fabricates a placeholder fixture cannot drift from the scripts under test.
const placeholderMarker = "web UI not yet built into this binary"

// runScript runs a repo script with arguments, returning its combined output and exit code.
//
// Separate from runGateScript: these two scripts take a positional argument (a directory, a base
// URL) rather than reading DKP_REPO_ROOT, because they are called from a Dockerfile and from a smoke
// script, not only from a test.
func runScript(t *testing.T, name string, args ...string) (output string, exitCode int) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{scriptPath(t, name)}, args...)...)
	cmd.Dir = repoRoot(t)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run %s: %v\n%s", name, err, out)

	return "", 0
}

// writeDist materialises a SPA dist fixture: files keyed by their path relative to the dist root.
func writeDist(t *testing.T, files map[string]string) string {
	t.Helper()

	dist := t.TempDir()

	for rel, body := range files {
		full := filepath.Join(dist, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}

	return dist
}

// realIndex is an index.html shaped like Vite's: a content-hashed module script and stylesheet.
const realIndex = `<!doctype html>
<html lang="en">
  <head>
    <script type="module" crossorigin src="/assets/index-D5VmbsKU.js"></script>
    <link rel="stylesheet" crossorigin href="/assets/index-RtrperdL.css">
  </head>
  <body><div id="root"></div></body>
</html>
`

// placeholderIndex is the committed placeholder's shape: a document with no bundle at all.
const placeholderIndex = `<!doctype html>
<html lang="en">
  <head><title>Dragon Kill Party</title></head>
  <body><div id="root">Dragon Kill Party — ` + placeholderMarker + `.</div></body>
</html>
`

// TestVerifySPADist_RejectsAnythingButTheBuiltSPA is the build-time gate's own test.
//
// Every case here is a real state a build path has produced or plausibly would: the placeholder
// (issue #55 itself), a placeholder left beside the real output because the copy merged instead of
// replacing, a stale index pointing at a hash that no longer exists, and a build with hashing turned
// off — which would silently break the one-year immutable cache header internal/ui sends.
func TestVerifySPADist_RejectsAnythingButTheBuiltSPA(t *testing.T) {
	t.Parallel()

	bundle := "console.log('spa');"

	tests := []struct {
		name    string
		files   map[string]string
		wantOK  bool
		wantMsg string
	}{
		{
			name: "the built SPA passes",
			files: map[string]string{
				"index.html":                    realIndex,
				"assets/index-D5VmbsKU.js":      bundle,
				"assets/index-RtrperdL.css":     "body{}",
				"assets/Inter-Regular-X1.woff2": "font",
			},
			wantOK: true,
		},
		{
			name: "the committed placeholder fails",
			files: map[string]string{
				"index.html":                placeholderIndex,
				"assets/app-placeholder.js": bundle,
			},
			wantMsg: "COMMITTED PLACEHOLDER",
		},
		{
			name: "a placeholder bundle surviving beside the real output fails",
			files: map[string]string{
				"index.html":                realIndex,
				"assets/index-D5VmbsKU.js":  bundle,
				"assets/index-RtrperdL.css": "body{}",
				"assets/app-placeholder.js": bundle,
			},
			wantMsg: "app-placeholder.js is still present",
		},
		{
			name: "an index referencing a bundle that is not there fails",
			files: map[string]string{
				"index.html":                realIndex,
				"assets/index-RtrperdL.css": "body{}",
			},
			wantMsg: "references assets that are not in",
		},
		{
			name: "an index with no bundle at all fails",
			files: map[string]string{
				"index.html": "<!doctype html><html><body><div id=\"root\"></div></body></html>",
			},
			wantMsg: "references no JavaScript bundle",
		},
		{
			name: "an unhashed bundle fails",
			files: map[string]string{
				"index.html":    `<!doctype html><script type="module" src="/assets/app.js"></script>`,
				"assets/app.js": bundle,
			},
			wantMsg: "no referenced bundle carries a content hash",
		},
		{
			name:    "a dist with no index at all fails",
			files:   map[string]string{"assets/index-D5VmbsKU.js": bundle},
			wantMsg: "index.html is missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, code := runScript(t, "verify-spa-dist.sh", writeDist(t, tc.files))

			if tc.wantOK {
				require.Zerof(t, code, "the built SPA must pass the gate\n%s", out)

				return
			}

			require.NotZerof(t, code, "this dist must NOT pass the gate — it would ship a broken SPA\n%s", out)
			require.Containsf(t, out, tc.wantMsg, "the failure must name the cause\n%s", out)
		})
	}
}

// TestVerifySPADist_AgreesWithTheCheckedOutTree runs the gate against the REAL internal/ui/dist and
// requires its verdict to match what is actually staged there.
//
// The fixtures above prove the logic; this proves the logic is pointed at the right tree and that
// the marker it greps for is still the marker the committed placeholder carries. Both directions are
// asserted rather than skipped, because a checkout is in one of exactly two states: pristine (the
// placeholder is staged, so the gate MUST fire) or post-`make build` (the real SPA is staged, so it
// MUST pass). A skip on either would be a gate that never runs anywhere.
func TestVerifySPADist_AgreesWithTheCheckedOutTree(t *testing.T) {
	t.Parallel()

	dist := filepath.Join(repoRoot(t), "internal", "ui", "dist")

	_, err := os.Stat(filepath.Join(dist, "assets", "app-placeholder.js"))
	pristine := err == nil

	out, code := runScript(t, "verify-spa-dist.sh", dist)

	if pristine {
		require.NotZerof(t, code,
			"internal/ui/dist holds the committed placeholder, so the gate must fire on it — that is "+
				"the state every image shipped in for the whole of issue #55\n%s", out)
		require.Containsf(t, out, "COMMITTED PLACEHOLDER",
			"the gate must recognise the committed placeholder by its marker; if the placeholder's "+
				"wording changed, scripts/verify-spa-dist.sh's placeholder_marker has to change with it\n%s", out)

		return
	}

	require.Zerof(t, code,
		"`make build` has staged the real SPA into internal/ui/dist and the gate rejected it\n%s", out)
}

// TestSmokeSPA_DistinguishesTheServedSPAFromThePlaceholder is the runtime gate's own test.
//
// It runs the script against httptest servers that imitate what a real dkp answers, because the
// property being tested is the one thing a container smoke cannot get from status codes: internal/ui
// falls back to index.html with a 200 for every path it does not have, so a placeholder image, a
// half-staged bundle and a healthy SPA are indistinguishable to anything that reads only the code.
func TestSmokeSPA_DistinguishesTheServedSPAFromThePlaceholder(t *testing.T) {
	t.Parallel()

	const bundlePath = "/assets/index-D5VmbsKU.js"

	// spa serves an index and resolves the bundle the way internal/ui does, including its fallback.
	spa := func(index string, bundle string, cache string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == bundlePath && bundle != "" {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				if cache != "" {
					w.Header().Set("Cache-Control", cache)
				}
				_, _ = w.Write([]byte(bundle))

				return
			}

			// Everything else — including a bundle the binary does not have — is the index, at 200.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(index))
		}
	}

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantOK  bool
		wantMsg string
	}{
		{
			name:    "a served build passes",
			handler: spa(realIndex, "console.log('spa');", "public, max-age=31536000, immutable"),
			wantOK:  true,
		},
		{
			name:    "the placeholder fails",
			handler: spa(placeholderIndex, "", ""),
			wantMsg: "COMMITTED PLACEHOLDER",
		},
		{
			name: "an index with no bundle fails",
			handler: spa(
				"<!doctype html><html><body><div id=\"root\"></div></body></html>", "", ""),
			wantMsg: "references no JavaScript bundle",
		},
		{
			// The trap: the index names a bundle the binary does not carry, so the server answers the
			// index fallback with a 200 and a browser evaluates HTML as a module.
			name:    "a bundle that falls back to the index fails",
			handler: spa(realIndex, "", ""),
			wantMsg: "served HTML, not JavaScript",
		},
		{
			name:    "a bundle without the immutable cache header fails",
			handler: spa(realIndex, "console.log('spa');", "no-cache"),
			wantMsg: "did not carry an immutable Cache-Control header",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			out, code := runScript(t, "smoke-spa.sh", srv.URL)

			if tc.wantOK {
				require.Zerof(t, code, "a server sending the real SPA must pass the smoke\n%s", out)

				return
			}

			require.NotZerof(t, code, "this server does not serve a working SPA and the smoke must say so\n%s", out)
			require.Containsf(t, out, tc.wantMsg, "the failure must name the cause\n%s", out)
		})
	}
}

// TestDockerfile_BuildsTheSPABeforeItCompiles pins the fix for issue #55 in the file that had the
// defect. Each assertion is a line whose removal restores the bug in full, silently.
func TestDockerfile_BuildsTheSPABeforeItCompiles(t *testing.T) {
	t.Parallel()

	df := readRepoFile(t, "deploy/Dockerfile")

	require.Contains(t, df, "AS web",
		"deploy/Dockerfile must have a web build stage: the binary embeds the SPA, and an image that "+
			"never runs the Vite build ships internal/ui's placeholder (issue #55)")
	require.Regexp(t, `(?m)^FROM .*node:.*@sha256:[0-9a-f]{64} AS web$`, df,
		"the web stage's base must be pinned by digest as well as tag, like the Go base above it")
	require.Contains(t, df, "scripts/build-web.sh",
		"the web stage must run scripts/build-web.sh — the same script `make build` runs, so the image's "+
			"SPA cannot drift from the local one")
	require.Contains(t, df, "COPY --from=web /src/internal/ui/dist ./internal/ui/dist",
		"the built SPA must be staged into internal/ui/dist, which is what //go:embed reads")
	require.Contains(t, df, "rm -rf internal/ui/dist",
		"the committed placeholder must be REMOVED before the real output is copied in: COPY merges "+
			"into an existing directory, so assets/app-placeholder.js would otherwise survive and ship")
	require.Contains(t, df, "scripts/verify-spa-dist.sh",
		"the build must assert what it staged; a placeholder embed compiles and boots perfectly, so "+
			"nothing else in the image build can catch it")

	// Order is the whole point: staging after `go build` would compile the placeholder into the
	// binary and then tidily stage the real SPA into a directory nothing reads again.
	stage := strings.Index(df, "COPY --from=web")
	verify := strings.Index(df, "scripts/verify-spa-dist.sh")
	compile := strings.Index(df, "go build -trimpath")

	require.NotEqual(t, -1, compile, "the build stage must still compile ./cmd/dkp")
	require.Less(t, stage, compile, "the SPA must be staged BEFORE `go build`, not after")
	require.Less(t, verify, compile, "the SPA must be verified BEFORE `go build`, not after")
}

// TestDockerignore_KeepsWhatTheWebBuildNeeds asserts the build context still carries the SPA sources.
//
// .dockerignore is an exclusion list, so this is a gate against a future "keep the context small"
// change: excluding web/ or scripts/ would not fail any build — the web stage would fail on a
// missing file, or worse, a `COPY` of an absent directory would leave the stage's dist empty and the
// only symptom would be the placeholder shipping again. The two entries that MUST stay excluded are
// asserted too: they are build output, and a laptop's stale copy leaking into the image is the
// mirror-image bug.
func TestDockerignore_KeepsWhatTheWebBuildNeeds(t *testing.T) {
	t.Parallel()

	var patterns []string

	for _, line := range strings.Split(readRepoFile(t, ".dockerignore"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		patterns = append(patterns, strings.Trim(line, "/"))
	}

	for _, needed := range []string{
		"web/package.json",
		"web/pnpm-lock.yaml",
		"web/index.html",
		"web/src",
		"web/vite.config.ts",
		"web/tsconfig.json",
		"scripts/build-web.sh",
		"scripts/verify-spa-dist.sh",
	} {
		for _, pattern := range patterns {
			require.Falsef(t, needed == pattern || strings.HasPrefix(needed, pattern+"/"),
				".dockerignore excludes %q, which the image's web stage needs to build the SPA (pattern %q)",
				needed, pattern)
		}
	}

	require.Contains(t, patterns, "web/dist",
		"web/dist is build output — the image rebuilds it, and a laptop's stale copy must not leak in")
	require.Contains(t, patterns, "web/node_modules",
		"web/node_modules must not enter the context: the web stage installs from the lockfile")
}

// TestSmokeScripts_AssertTheServedSPA keeps the runtime half wired into both smoke paths.
//
// `make smoke-local` is the PR-time check in CI's `build / image` job and release-smoke.sh is the
// gate the release train runs against the published digest before any moving tag advances. Both
// passed for the whole of issue #55, because "it boots" is exactly what a placeholder image does.
func TestSmokeScripts_AssertTheServedSPA(t *testing.T) {
	t.Parallel()

	for _, script := range []string{"scripts/smoke-local.sh", "scripts/release-smoke.sh"} {
		require.Containsf(t, readRepoFile(t, script), "smoke-spa.sh",
			"%s must run scripts/smoke-spa.sh: booting is not evidence the image serves the SPA", script)
	}

	// And the PR path must still run the local smoke at all.
	workflow := readCIWorkflow(t)
	require.Contains(t, jobBlock(t, workflow, "build-image:"), "make smoke-local",
		"ci.yml's build / image job must run `make smoke-local` — it is where the served-SPA assertion runs")
}

// TestGoreleaser_BuildsTheSPABeforeItCompiles covers the other shipped-binary channel.
//
// goreleaser compiles ./cmd/dkp directly for six OS/arch targets, so it has issue #55's defect for
// exactly the same reason the Dockerfile did: nothing in its config stages the SPA. Every tarball,
// deb, rpm and Homebrew install would embed the placeholder.
func TestGoreleaser_BuildsTheSPABeforeItCompiles(t *testing.T) {
	t.Parallel()

	cfg := readRepoFile(t, ".goreleaser.yaml")

	require.Contains(t, cfg, "scripts/build-web.sh",
		"a before-hook must build and stage the SPA: goreleaser runs `go build` itself, so without it "+
			"every published binary embeds internal/ui's placeholder")
	require.Contains(t, cfg, "scripts/verify-spa-dist.sh",
		"a before-hook must verify the staged SPA before six binaries are cut from it")

	hooks := cfg[strings.Index(cfg, "before:"):strings.Index(cfg, "builds:")]
	require.Contains(t, hooks, "scripts/build-web.sh",
		"the SPA build must be a BEFORE hook — after the builds it would stage into a directory "+
			"nothing reads again")

	// The hook needs a toolchain the job must ask for; setup-toolchain's inputs all default to false.
	rel := readRepoFile(t, ".github/workflows/release.yml")
	require.Contains(t, jobBlock(t, rel, "binaries:"), `node: "true"`,
		"release.yml's binaries job must request Node from setup-toolchain: the goreleaser before-hook "+
			"runs the Vite build through pnpm and would fail `pnpm: command not found` without it")
}
