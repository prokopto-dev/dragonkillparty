// Tests that ci.yml's path filters select the jobs that police the changed files.
//
// A path filter is the one part of the job graph that can silently REMOVE coverage. ci-required
// counts `skipped` as success — it has to, or a path-filtered job would wedge every PR — so a job
// that a wrong filter skipped is indistinguishable from a job that was correctly filtered out. The
// header of ci.yml's own filter block names this failure mode and fixes it three times:
// scripts/** and .githooks/** are pinned to `go` so their negative fixtures in test/repo run;
// internal/api/EXAMPLE_ENDPOINT.md and db/RECIPES.md are pinned to `go` so the snippet-compile gate
// runs; internal/migrate/** and the migration fixtures are pinned to `db` so test / migrations runs.
//
// Issue #94 is the same omission left unfixed for the fourth case: test/repo runs under `make
// test-unit` and `make test`, both gated on `go`, but design_tokens_test.go, e2e_gate_test.go,
// web_fonts_test.go, web_fonts_subset_test.go and npmrc_test.go read their INPUTS from web/ and
// docs/design/. A web-only PR selected `web` and not `go`, so every job that runs those tests was
// skipped and ci-required reported green — observed on PR #91, which changed five of these files.
package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// filterEntryRe matches one path pattern inside a filter's sequence: twelve spaces of indent under
// the inline `filters: |` block, a dash, and a single-quoted glob.
var filterEntryRe = regexp.MustCompile(`^ {14}- '([^']+)'\s*$`)

// pathFilterPatterns returns the glob patterns declared for one filter in ci.yml's `changes` job.
//
// The `*build` YAML anchor is skipped rather than expanded: it carries the Makefile and the
// setup-toolchain action, neither of which is an input any assertion below names.
func pathFilterPatterns(t *testing.T, workflow, filter string) []string {
	t.Helper()

	lines := strings.Split(workflow, "\n")

	start := -1

	for i, l := range lines {
		if l == "            "+filter+":" {
			start = i + 1

			break
		}
	}

	require.NotEqualf(t, -1, start,
		"ci.yml's `changes` job declares no %q filter — has the filters block been reformatted?", filter)

	var patterns []string

	for i := start; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}

		if m := filterEntryRe.FindStringSubmatch(l); m != nil {
			patterns = append(patterns, m[1])

			continue
		}

		if strings.HasPrefix(strings.TrimSpace(l), "- *") {
			continue // the *build anchor
		}

		break // the next filter, or the end of the block
	}

	require.NotEmptyf(t, patterns, "no patterns parsed for the %q filter", filter)

	return patterns
}

// matchesPattern reports whether one dorny/paths-filter glob selects a repo-relative path.
//
// Deliberately partial, in the same spirit as the rest of this directory's parsing: it understands
// the three shapes ci.yml actually uses — an exact path, a `prefix/**` subtree, and a `**/*.ext`
// suffix — and returns false for anything else. False is the safe direction: an unrecognised
// pattern makes the assertion below FAIL with the path it could not account for, which is a loud,
// one-line fix, rather than passing on a filter nobody verified.
func matchesPattern(pattern, path string) bool {
	switch {
	case strings.HasPrefix(pattern, "**/*."):
		return strings.HasSuffix(path, strings.TrimPrefix(pattern, "**/*"))
	case strings.HasSuffix(pattern, "/**"):
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "**"))
	default:
		return pattern == path
	}
}

// TestCIFilters_GoFilter_SelectsEveryTestRepoInput closes issue #94.
//
// Each path below is read by a test/repo suite that only ever runs in a job gated on the `go`
// filter. Naming the concrete file rather than the pattern is the point: the assertion survives a
// reformatting of the filter block, and it fails when somebody deletes the one line that was
// covering three of these.
func TestCIFilters_GoFilter_SelectsEveryTestRepoInput(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)
	patterns := pathFilterPatterns(t, workflow, "go")

	for _, input := range []struct{ path, reader string }{
		{"web/src/styles/tokens.css", "design_tokens_test.go — the normative token table"},
		{"web/src/styles/base.css", "design_tokens_test.go — the closed --color-* namespace, no transitions"},
		{"web/src/styles/fonts.css", "web_fonts_test.go — a declared face with no committed file"},
		{"web/src/routes/design.tsx", "design_tokens_test.go — /_design renders every token"},
		{"web/src/components/Table.css", "design_tokens_test.go — the .table mockup fidelity diff"},
		{"web/src/assets/fonts/Inter-Regular-latin.woff2", "web_fonts_subset_test.go — the vendored subsets"},
		{"web/e2e/axe-allowlist.json", "e2e_gate_test.go — an allowlist entry with no issue number"},
		{"web/playwright.config.ts", "e2e_gate_test.go — the harness being repointed at Vite"},
		{".npmrc", "npmrc_test.go — the ignore-scripts baseline"},
		{"web/.npmrc", "npmrc_test.go — the ignore-scripts baseline that actually binds"},
		{"docs/design/09-frontend-and-design-system.md", "design_tokens_test.go — section 2's token table"},
		{"docs/design/mockups/nocturne/styles.css", "design_tokens_test.go — the fidelity source"},
		{"NOTICE", "web_fonts_test.go — a font landing with no licence record"},
		{"THIRD_PARTY_NOTICES.txt", "third_party_notices_test.go, web_fonts_subset_test.go"},
		{"deploy/Dockerfile", "release_gates_test.go, spa_pipeline_test.go, npmrc_test.go"},
		{".dockerignore", "spa_pipeline_test.go — the build-context allowlist"},
	} {
		t.Run(input.path, func(t *testing.T) {
			t.Parallel()

			// The input must still exist, or the row above is protecting a file nobody has and the
			// filter line it justifies is dead weight the next reader will delete.
			_, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(input.path)))
			require.NoErrorf(t, err,
				"%s no longer exists, so this row and the ci.yml filter line it justifies are stale",
				input.path)

			matched := false

			for _, p := range patterns {
				if matchesPattern(p, input.path) {
					matched = true

					break
				}
			}

			require.Truef(t, matched,
				"ci.yml's `go` path filter does not select %s, which %s reads. test/repo runs only in "+
					"jobs gated on `go`, so a PR changing that file skips every one of them and "+
					"ci-required counts the skip as success (issue #94). Add the path to the `go` filter.",
				input.path, input.reader)
		})
	}
}
