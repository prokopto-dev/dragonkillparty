// Tests that the e2e gate keeps doing real work.
//
// `make test-e2e` was a `notyet` stub for the whole of Phase 0, so CI's `test / e2e (1)` and
// `test / e2e (2)` reported green while executing nothing (issue #33). That was honest as a stub and
// invisible as a regression: a target that exits 0 without running anything looks exactly like a
// target that ran and passed, and the checks list implies browser coverage either way.
//
// AGENTS.md puts these here rather than beside the feature: "test/repo/ — tests about the repository
// itself, not the product: they assert the gates in this file actually fire. Add one when you add a
// gate, not when you add a feature."
package repo_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// makeRecipe returns the recipe lines of one Makefile target — the tab-indented block following its
// `name:` line. Parsed by indentation for the same reason ci_required_test.go parses YAML that way:
// the shape is regular, and a parser dependency for a test is a dependency a human has to approve.
func makeRecipe(t *testing.T, target string) string {
	t.Helper()

	lines := strings.Split(readRepoFile(t, "Makefile"), "\n")

	start := -1

	for i, line := range lines {
		if strings.HasPrefix(line, target+":") {
			start = i + 1

			break
		}
	}

	require.NotEqual(t, -1, start, "the Makefile has no %s target", target)

	var recipe []string

	for i := start; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "\t") {
			break
		}

		recipe = append(recipe, lines[i])
	}

	require.NotEmpty(t, recipe, "%s has an empty recipe", target)

	return strings.Join(recipe, "\n")
}

// TestE2E_MakeTarget_DoesRealWork asserts test-e2e has not slid back into a stub.
//
// The `notyet` helper is the exact shape the regression would take, and it is a one-line edit that
// turns the whole browser suite off while leaving every job name, every required check and every
// green tick in place.
func TestE2E_MakeTarget_DoesRealWork(t *testing.T) {
	t.Parallel()

	recipe := makeRecipe(t, "test-e2e")

	require.NotContains(t, recipe, "notyet",
		"make test-e2e is a stub again. A stubbed target exits 0, so CI's `test / e2e` jobs would go "+
			"green having opened no browser — the defect issue #33 was filed for.")
	require.Contains(t, recipe, "scripts/test-e2e.sh",
		"make test-e2e must run scripts/test-e2e.sh, which boots the built binary and fails when a "+
			"shard selects zero tests")
}

// TestE2E_CIJob_InstallsNode asserts the e2e job asks setup-toolchain for the toolchain it needs.
//
// Every job shells out to a make target and gets its tools from that composite action, whose inputs
// all default to false. A `make test-e2e` on a runner with no Node fails "pnpm is not installed" —
// loudly, which is the right direction, but the failure is in CI on a green laptop and the cause is
// one missing line in a file the author never opened.
func TestE2E_CIJob_InstallsNode(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	start := strings.Index(workflow, "\n  test-e2e:\n")
	require.NotEqual(t, -1, start, "ci.yml has no test-e2e job")

	job := workflow[start:]
	if next := strings.Index(job[1:], "\n  test-importer:"); next != -1 {
		job = job[:next]
	}

	require.Contains(t, job, `node: "true"`,
		"ci.yml's test-e2e job must request Node from setup-toolchain: Playwright is a Node tool and "+
			"`make test-e2e` drives it through pnpm")
}

// axeAllowlist mirrors web/e2e/axe-allowlist.json.
type axeAllowlist struct {
	Routes map[string][]struct {
		Rule   string `json:"rule"`
		Target string `json:"target"`
		Issue  int    `json:"issue"`
		Why    string `json:"why"`
	} `json:"routes"`
}

// TestE2E_AxeAllowlist_EveryEntryIsAccountedFor asserts the accessibility allowlist stays a list of
// reviewed exceptions rather than a place to put a failing scan.
//
// docs/design/04-testing.md §Accessibility puts the allowlist "under the same anti-tampering rules as
// golden files". The shrink-only half is enforced by the suite itself — web/e2e/a11y.spec.ts fails
// when an entry matches nothing, so a fixed violation cannot leave its exception behind. What that
// cannot check is whether an entry was ever agreed to by anybody, which is what an issue number is.
func TestE2E_AxeAllowlist_EveryEntryIsAccountedFor(t *testing.T) {
	t.Parallel()

	var allowlist axeAllowlist

	require.NoError(t, json.Unmarshal([]byte(readRepoFile(t, "web/e2e/axe-allowlist.json")), &allowlist),
		"web/e2e/axe-allowlist.json must parse")

	for route, entries := range allowlist.Routes {
		for _, entry := range entries {
			require.NotEmpty(t, entry.Rule, "%s: an allowlist entry with no rule id allows everything", route)
			require.NotEmpty(t, entry.Target, "%s: an allowlist entry with no target allows the rule everywhere", route)
			require.NotZero(t, entry.Issue,
				"%s: %s on %s has no issue number. An accessibility exception nobody has agreed to is "+
					"not an exception, it is a lowered bar.", route, entry.Rule, entry.Target)
			require.NotEmpty(t, entry.Why, "%s: %s on %s must say why it cannot be fixed here",
				route, entry.Rule, entry.Target)
		}
	}
}

// TestE2E_AxeScans_HaveHeadroomOverTheDefaultTimeout closes issue #88.
//
// The axe sweep over /_design was observed at 44.7 s against Playwright's 30 s default, and passes
// in 2-11 s when the file runs alone — contention, not a defect in the scan. `fullyParallel: true`
// puts the whole suite on the machine at once and /_design is the heaviest page in the repo: every
// token plus a 200-row x 12-column virtualised table, all of which axe walks. With `retries: 0`
// (deliberate — a flaky e2e is quarantined, never retried) a budget smaller than the observed worst
// case is a red build with no product defect behind it, which is how a suite gets `.skip`ped.
//
// The rule is per-test rather than a config-wide `timeout:` bump, because the headroom belongs to
// the tests that need it: raising the default for all twenty-odd would hide a genuinely hung page
// in one of the fast ones. `test.slow()` triples this test's budget and changes no assertion, so it
// masks nothing — a scan that never finishes still fails, at 90 s instead of 30 s.
func TestE2E_AxeScans_HaveHeadroomOverTheDefaultTimeout(t *testing.T) {
	t.Parallel()

	spec := readRepoFile(t, "web/e2e/a11y.spec.ts")

	// One block per `test(...)` declaration, so the assertion is about the test that scans rather
	// than about the file containing the call somewhere.
	blocks := strings.Split(spec, "\n  test(")
	require.Greater(t, len(blocks), 1,
		"web/e2e/a11y.spec.ts declares no tests — has the file been reformatted or emptied?")

	scanning := 0

	for _, block := range blocks[1:] {
		if !strings.Contains(block, "scan(page)") {
			continue
		}

		scanning++

		title, _, _ := strings.Cut(block, "\n")

		require.Containsf(t, block, "test.slow()",
			"the axe test %s runs a full page scan without test.slow(). Playwright's 30 s default is "+
				"under the 44.7 s this scan was observed at under parallel load, and retries are zero "+
				"(issue #88).", strings.TrimSpace(title))
	}

	require.Equalf(t, 2, scanning,
		"expected exactly 2 axe-scanning tests in web/e2e/a11y.spec.ts (the /_design sweep and the open "+
			"dialog), found %d. A new one needs the same budget; a removed one needs this number "+
			"updated and a reason in the diff.", scanning)
}

// TestE2E_Specs_RunAgainstTheBuiltBinary asserts the harness has not been repointed at Vite.
//
// docs/design/04-testing.md is explicit that E2E "runs against the shipped binary, not a dev server",
// and half of what this suite uniquely proves is in the Go half: the embedded SPA, its cache headers,
// its CSP and the index.html fallback that gives the client-side router /_design at all. Pointing
// webServer at :5173 would keep every assertion passing while testing none of that.
func TestE2E_Specs_RunAgainstTheBuiltBinary(t *testing.T) {
	t.Parallel()

	config := readRepoFile(t, "web/playwright.config.ts")

	require.Contains(t, config, "bin/dkp",
		"web/playwright.config.ts must boot bin/dkp — the suite exists to exercise the shipped binary")

	// Comments stripped before the negative check: this file's own prose explains why it does NOT
	// point at Vite, and a naive substring search reads that explanation as the violation.
	code := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(config, "")
	code = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(code, "")

	require.NotContains(t, code, "5173",
		"web/playwright.config.ts must not point at the Vite dev server: a dev server serves neither "+
			"the go:embed'd SPA nor its headers, so the suite would pass against a broken binary")
}
