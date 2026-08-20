// Tests that ci.yml's path filters select the jobs that police the changed files.
//
// A path filter is the one part of the job graph that can silently REMOVE coverage. ci-required
// counts `skipped` as success — it has to, or a path-filtered job would wedge every PR — so a job
// that a wrong filter skipped is indistinguishable from a job that was correctly filtered out. The
// header of ci.yml's own filter block names this failure mode and fixes it three times:
// scripts/** and .githooks/** are pinned to `code` so their negative fixtures in test/repo run;
// internal/api/EXAMPLE_ENDPOINT.md and db/RECIPES.md are pinned to `code` so the snippet-compile
// gate runs; internal/migrate/** and the migration fixtures are pinned to `db` so test / migrations
// runs.
//
// Issue #94 is the same omission left unfixed for the fourth case: test/repo runs under `make
// test-unit` and `make test`, both gated on `go`, but design_tokens_test.go, e2e_gate_test.go,
// web_fonts_test.go, web_fonts_subset_test.go and npmrc_test.go read their INPUTS from web/ and
// docs/design/. A web-only PR selected `web` and not `go`, so every job that runs those tests was
// skipped and ci-required reported green — observed on PR #91, which changed five of these files.
//
// Issue #159 then split `go` in two, which is the same hazard from the other end: the heavy suites
// gate on the narrower `code`, and every pattern left behind in `go` needs a suite that still runs
// on it. TestCIFilters_CodeFilter_IsASubsetOfGo and
// TestCIFilters_GoOnlyPatterns_HaveAShortRunningReader are the two halves of that argument, and
// TestCIFilters_ClosureFilters_CoverTheirDependencies keeps the two derived filters derived.
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

// filterAnchorRe matches an inserted YAML anchor — `- *build`, `- *code`, `- *api`.
var filterAnchorRe = regexp.MustCompile(`^\s*- \*([a-z][a-z0-9-]*)\s*$`)

// pathFilterPatterns returns the glob patterns declared for one filter in ci.yml's `changes` job,
// with every YAML anchor EXPANDED.
//
// Expansion is what makes the assertions below mean anything now that the filters nest (issue
// #159): `go` is `*code` plus a handful of patterns, and a reader that skipped the anchor would
// conclude that `go` no longer selects scripts/** or deploy/** — the opposite of the truth, and in
// the direction that reads as "this coverage was deleted". dorny/paths-filter flattens a nested
// sequence recursively, which is what makes the nesting legal in the first place; this mirrors it.
func pathFilterPatterns(t *testing.T, workflow, filter string) []string {
	t.Helper()

	return expandFilter(t, workflow, filter, nil)
}

// expandFilter is pathFilterPatterns with the cycle guard an anchor graph needs.
func expandFilter(t *testing.T, workflow, filter string, seen []string) []string {
	t.Helper()

	for _, s := range seen {
		require.NotEqualf(t, s, filter,
			"ci.yml's filters reference each other in a cycle through %q (%v) — a cycle is not valid "+
				"YAML and the workflow would not load", filter, seen)
	}

	seen = append(seen, filter)

	lines := strings.Split(workflow, "\n")

	start := -1

	// `filter:` or `filter: &anchor` — a filter that is reused by another one declares an anchor on
	// the same line.
	for i, l := range lines {
		if trimmed := strings.TrimSpace(l); strings.HasPrefix(l, "            "+filter+":") &&
			(trimmed == filter+":" || strings.HasPrefix(trimmed, filter+": &")) {
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

		if m := filterAnchorRe.FindStringSubmatch(l); m != nil {
			patterns = append(patterns, expandFilter(t, workflow, m[1], seen)...)

			continue
		}

		break // the next filter, or the end of the block
	}

	require.NotEmptyf(t, patterns, "no patterns parsed for the %q filter", filter)

	return patterns
}

// selects reports whether a filter's patterns select a repo-relative path.
func selects(patterns []string, path string) bool {
	for _, p := range patterns {
		if matchesPattern(p, path) {
			return true
		}
	}

	return false
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
		{"docs/design/mockups/admin-console.dc.html", "mockup_gates_test.go — MOCK001-004 over the real surfaces"},
		{"docs/design/mockups/index.html", "mockup_gates_test.go — MOCK004 over the hand-written page"},
		{"docs/design/mockups/harness/mockup-runtime.js", "mockup_gates_test.go — the runtime MOCK001 protects"},
		{"NOTICE", "web_fonts_test.go — a font landing with no licence record"},
		{"THIRD_PARTY_NOTICES.txt", "third_party_notices_test.go, web_fonts_subset_test.go"},
		{"deploy/Dockerfile", "release_gates_test.go, spa_pipeline_test.go, npmrc_test.go"},
		{".dockerignore", "spa_pipeline_test.go — the build-context allowlist"},
		{"CONTRIBUTING.md", "contributing_claims_test.go — every `Enforced by:` claim resolves"},
		{"web/package.json", "web_overrides_test.go — a pnpm override with no reviewed row (issue #168)"},
		{"web/pnpm-lock.yaml", "web_overrides_test.go — an override edited without re-locking is inert"},
		{"web/OVERRIDES.md", "web_overrides_test.go — the register that carries each override's exit condition"},
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

// TestCIFilters_ActionsAndShell_SelectTheirOwnInputs covers the two path-filtered jobs added by
// issues #121 and #122.
//
// A gate conditioned on a filter that does not select its own definition is the #101 shape: it sits
// in the checks list, never runs, and ci-required counts the skip as success. Both filters therefore
// have to select the gate script AND the pin the script reads AND the tree it lints — and the
// Makefile is in each of them through the *build anchor, because both jobs are `make <target>`.
func TestCIFilters_ActionsAndShell_SelectTheirOwnInputs(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	for _, tc := range []struct {
		filter string
		inputs []struct{ path, why string }
	}{
		{
			filter: "actions",
			inputs: []struct{ path, why string }{
				{".github/workflows/ci.yml", "the tree actionlint lints"},
				{".github/actions/setup-toolchain/action.yml", "actionlint resolves `uses:` inputs " +
					"against a local action's own action.yml, so a renamed input there is a workflow error"},
				{"scripts/lint-actions.sh", "the gate script itself — a filter that excludes the file " +
					"defining a job is the same defect as a target that exits 0 without doing the work"},
				{"Makefile", "the pin the installer reads, and the target the job runs"},
			},
		},
		{
			filter: "shell",
			inputs: []struct{ path, why string }{
				{"scripts/repo-gates.sh", "the tree shellcheck and shfmt read"},
				{"scripts/lint-shell.sh", "the gate script itself, which this gate also lints"},
				{".githooks/pre-push", "the hooks are shell too, and they carry no .sh suffix"},
				{".claude/hooks/guard-bash.sh", "the fail-open command guard — it decides whether a " +
					"tool call runs at all, its own header says an unparseable payload allows the " +
					"command, and until issue #187 that tree was in no filter and no enumeration, so " +
					"a hooks-only PR selected nothing and ci-required counted the skips as success"},
				{".shellcheckrc", "the file that decides which rules run at all"},
				{"Makefile", "the pins and the target the job runs"},
			},
		},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			t.Parallel()

			patterns := pathFilterPatterns(t, workflow, tc.filter)

			for _, input := range tc.inputs {
				_, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(input.path)))
				require.NoErrorf(t, err,
					"%s no longer exists, so this row and the ci.yml filter line it justifies are stale",
					input.path)

				require.Truef(t, selects(patterns, input.path),
					"ci.yml's `%s` path filter does not select %s (%s). A job gated on a filter that "+
						"misses its own inputs never runs, and ci-required counts a skip as success — "+
						"which is the shape issue #101 deleted two jobs for.",
					tc.filter, input.path, input.why)
			}
		})
	}
}

// TestCIFilters_CodeFilter_SelectsTheWorkflowFiles closes issue #161.
//
// `.github/workflows/**` was in NO filter: not in *build, not in `go`, and not in `docs`, whose
// `**/*.md` cannot match a .yml. A workflow-only PR therefore selected nothing at all, every test job
// was skipped, ci-required counted the skips as success, and every suite below — each of which reads
// a workflow file as its INPUT — silently did not run. Renaming a job out of ci-required's `needs:`
// list, or out of a CONTRIBUTING.md citation, is exactly the change that ran none of the gates that
// catch it. The fourth instance of the omission the filter block's own header warns about, after
// scripts/**, .githooks/** and the web inputs of issue #94.
//
// Asserted against `code` AND NOT `go`, which is where the issue proposed it, because the rule the
// filter block states decides it: a pattern belongs in `go` alone only while its reader still runs
// under -short, and gate_cache_test.go, migrate_lint_test.go and this file's own
// TestCIFilters_ClosureFilters_CoverTheirDependencies all skip there. Their input has to reach
// `test / integration`, so it goes in the tier that does. `code` is nested inside `go`, so this is
// strictly the stronger placement and TestCIFilters_CodeFilter_IsASubsetOfGo keeps it that way.
func TestCIFilters_CodeFilter_SelectsTheWorkflowFiles(t *testing.T) {
	t.Parallel()

	patterns := pathFilterPatterns(t, readCIWorkflow(t), "code")

	for _, input := range []struct{ path, reader string }{
		{
			".github/workflows/ci.yml",
			"ci_required_test.go (every job reports into the gate), ci_path_filters_test.go (this " +
				"block), ci_toolchain_test.go, gate_cache_test.go, migration_gates_test.go, " +
				"migrate_lint_test.go, contributing_claims_test.go, docker_layer_cache_test.go",
		},
		{
			".github/workflows/release.yml",
			"release_gates_test.go, smoke_scripts_test.go, spa_pipeline_test.go, docker_layer_cache_test.go",
		},
		{".github/workflows/edge.yml", "smoke_scripts_test.go, docker_layer_cache_test.go"},
		{
			".github/workflows/nightly-verify.yml",
			"ci_toolchain_test.go, release_gates_test.go, upgrade_ladder_matrix_test.go",
		},
		{".github/workflows/pages.yml", "mockup_gates_test.go"},
	} {
		t.Run(input.path, func(t *testing.T) {
			t.Parallel()

			// The input must still exist, or the row is protecting a file nobody has.
			_, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(input.path)))
			require.NoErrorf(t, err,
				"%s no longer exists, so this row is stale — move it to whatever replaced the workflow",
				input.path)

			require.Truef(t, selects(patterns, input.path),
				"ci.yml's `code` path filter does not select %s, which %s reads. A workflow-only PR then "+
					"skips `test / unit` and `test / integration`, ci-required counts the skips as "+
					"success, and the suites that assert about this file do not run — including the ones "+
					"that would catch a job renamed out of the gate (issue #161).",
				input.path, input.reader)
		})
	}
}

// TestCIFilters_CodeFilter_IsASubsetOfGo is the safety argument for issue #159's split, in one
// assertion.
//
// The heavy suites moved from `go` to the narrower `code`. That is only safe while `go` remains a
// strict superset: every input issue #94 pinned to `go` must still select `test / unit`, which is
// where the suites that read those inputs run under -short. Nesting the filters (`go` is `*code`
// plus a list) makes it structurally true; this is what notices when somebody unpicks the nesting
// and copies the patterns instead.
func TestCIFilters_CodeFilter_IsASubsetOfGo(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	code := pathFilterPatterns(t, workflow, "code")
	inGo := make(map[string]bool)

	for _, p := range pathFilterPatterns(t, workflow, "go") {
		inGo[p] = true
	}

	for _, p := range code {
		require.Truef(t, inGo[p],
			"the `code` filter selects %q and the `go` filter does not. `go` must stay a superset: the "+
				"heavy suites gate on `code`, and `test / unit` — the job that runs test/repo's -short "+
				"suites on a web-only PR — gates on `go` (issue #159).", p)
	}

	require.Greater(t, len(inGo), len(code),
		"`go` and `code` now select the same patterns, so the split buys nothing and one of them "+
			"should go. `go` exists to carry the non-source inputs of test/repo (issue #94).")
}

// TestCIFilters_GoOnlyPatterns_HaveAShortRunningReader is the OTHER direction, and the one that can
// actually lose coverage.
//
// A pattern in `go` but not in `code` is a file that no longer reaches `test / integration`. That
// is fine exactly when the suite reading it also runs under -short, because `test / unit` still
// fires on `go` — and it is a silent hole the moment that suite gains a `testing.Short()` skip.
// Each row below is that argument, made once and then checked on every run.
func TestCIFilters_GoOnlyPatterns_HaveAShortRunningReader(t *testing.T) {
	t.Parallel()

	// Every pattern `go` carries and `code` does not, with the suite that still polices it. A new
	// entry in this table is a decision to stop running the full suite on that input.
	readers := map[string]string{
		"web/src/**":               "test/repo/design_tokens_test.go",
		"web/e2e/**":               "test/repo/e2e_gate_test.go",
		"web/playwright.config.ts": "test/repo/e2e_gate_test.go",
		"docs/design/09-frontend-and-design-system.md": "test/repo/design_tokens_test.go",
		"docs/design/mockups/**":                       "test/repo/mockup_gates_test.go",
		"NOTICE":                                       "test/repo/web_fonts_test.go",
		"THIRD_PARTY_NOTICES.txt":                      "test/repo/third_party_notices_test.go",
		".dockerignore":                                "test/repo/spa_pipeline_test.go",
		"CONTRIBUTING.md":                              "test/repo/contributing_claims_test.go",
	}

	// web/package.json, web/pnpm-lock.yaml and web/OVERRIDES.md were rows here until issue #186 gave
	// web_overrides_test.go three checks that read the installed node_modules for each parent's
	// DECLARED ranges. Those skip under -short, so `test / unit` stopped being enough and the three
	// patterns moved into `code`. That is this table's rule taking effect rather than being argued
	// with, and it is why the rule is a check and not a paragraph.

	workflow := readCIWorkflow(t)

	inCode := make(map[string]bool)
	for _, p := range pathFilterPatterns(t, workflow, "code") {
		inCode[p] = true
	}

	var goOnly []string

	for _, p := range pathFilterPatterns(t, workflow, "go") {
		if !inCode[p] {
			goOnly = append(goOnly, p)
		}
	}

	require.ElementsMatch(t, keysOf(readers), goOnly,
		"the patterns `go` carries beyond `code` have changed. Each one is a file that no longer "+
			"reaches `test / integration`, so each needs a suite that runs under -short and therefore "+
			"still runs in `test / unit`. Name it in this table in the same change (issue #159).")

	for pattern, reader := range readers {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()

			body := readRepoFile(t, reader)

			require.NotContainsf(t, body, "testing.Short()",
				"%s is the only thing still policing %s on a web-only PR — that pattern is in the `go` "+
					"filter and not in `code`, so `test / integration` no longer runs on it. This file "+
					"now skips under -short, which means `test / unit` does not run it either and the "+
					"input is policed by nothing. Either drop the skip or put %s back in the `code` "+
					"filter (issue #159).", reader, pattern, pattern)
		})
	}
}

// TestCIFilters_ClosureFilters_CoverTheirDependencies keeps the two derived filters derived.
//
// `pointmath` and `authz` are not "the directories somebody thought of" — each is the dependency
// closure of the packages its job runs, and a closure grows when somebody adds an import. That is
// the whole failure mode: `test / property` gated on internal/ledger and internal/strategy would
// stop running the day the ledger starts importing a package the filter never heard of, and the
// skip would count as success. So the closure is recomputed here rather than reviewed.
func TestCIFilters_ClosureFilters_CoverTheirDependencies(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to `go list -deps`; runs under `make test`")
	}

	workflow := readCIWorkflow(t)
	module := modulePath(t)

	for _, tc := range []struct {
		filter   string
		job      string
		packages []string
	}{
		{
			filter: "pointmath",
			job:    "test / property and test / coverage-floor",
			// What `make test-property` and `make test-coverage-floor` run.
			packages: []string{
				"./internal/ledger/...", "./internal/strategy/...", "./internal/audit/kinds",
				"./internal/account/kinds", "./internal/schemaenum",
				"./internal/authz/role/kinds", "./internal/authz/roleassignment/kinds",
			},
		},
		{
			filter: "authz",
			job:    "test / authz-matrix",
			// `make test-authz` is a Phase 2 stub, so this is the surface it will enumerate from
			// rather than a recipe's package list: the matrix is derived from the Huma registry.
			packages: []string{"./internal/api/...", "./internal/authz/..."},
		},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			t.Parallel()

			patterns := pathFilterPatterns(t, workflow, tc.filter)

			for _, pkg := range goListDeps(t, tc.packages) {
				dir := strings.TrimPrefix(pkg, module+"/")
				if dir == pkg {
					continue // not this module: the standard library and the third-party graph
				}

				require.Truef(t, selects(patterns, dir+"/x.go"),
					"ci.yml's `%s` filter does not select %s, which %s depends on. A change there can "+
						"break that job, the filter would skip it, and ci-required counts a skip as "+
						"success. Add '%s/**' to the filter (issue #159).", tc.filter, dir, tc.job, dir)
			}
		})
	}
}

// goListDeps returns the transitive dependency closure of package patterns, this module's packages
// and everything else, exactly as the toolchain reports it.
func goListDeps(t *testing.T, patterns []string) []string {
	t.Helper()

	return goListPackages(t, append([]string{"-deps"}, patterns...))
}
