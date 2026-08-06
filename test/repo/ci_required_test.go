// Tests that ci-required actually gates what it claims to gate.
//
// `ci-required` is the only check in branch protection, so a job missing from its `needs:` list is
// a job that cannot block a merge — it reports its own red X on the PR and nothing enforces it.
// That failure is invisible: the job still runs, still goes red, and still looks like a gate.
//
// docs/development/first-ten-prs.md makes "every acceptance criterion is a test or gate in this PR,
// not a promise" a rule for all ten PRs. "`govulncheck` is wired into `ci-required` and is not
// `continue-on-error`" is such a criterion, and these tests are what discharge it.
//
// ci.yml is parsed by indentation rather than with a YAML library on purpose: gopkg.in/yaml.v3 is
// only an indirect dependency today, and promoting it to a direct one to read a file whose shape is
// this regular would mean adding a dependency for a test — which AGENTS.md requires a human to
// approve. The parsing is deliberately dumb, in the same spirit as the grep gates.
package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// jobIDRe matches a job id: exactly two spaces of indent, inside the top-level `jobs:` block.
var jobIDRe = regexp.MustCompile(`^ {2}([a-z][a-z0-9-]*):\s*$`)

// needsEntryRe matches one entry of a `needs:` sequence.
var needsEntryRe = regexp.MustCompile(`^ {6}- ([a-z][a-z0-9-]*)\s*$`)

// readCIWorkflow returns the text of .github/workflows/ci.yml.
func readCIWorkflow(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	require.NoError(t, err, "read .github/workflows/ci.yml")

	return string(b)
}

// jobsBlock returns the lines of ci.yml's top-level `jobs:` mapping.
//
// Scoping to that block matters: `on:` also carries two-space keys, so a naive scan over the whole
// file reports `push` as a job.
func jobsBlock(t *testing.T, workflow string) []string {
	t.Helper()

	lines := strings.Split(workflow, "\n")
	start := -1
	for i, l := range lines {
		if l == "jobs:" {
			start = i + 1

			break
		}
	}
	require.NotEqual(t, -1, start, "ci.yml has no top-level `jobs:` key")

	for i := start; i < len(lines); i++ {
		l := lines[i]
		if l == "" || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "#") {
			continue
		}

		return lines[start:i] // a new top-level key ends the block
	}

	return lines[start:]
}

// ciJobIDs returns every job id declared in ci.yml.
func ciJobIDs(t *testing.T, workflow string) []string {
	t.Helper()

	var ids []string
	for _, l := range jobsBlock(t, workflow) {
		if m := jobIDRe.FindStringSubmatch(l); m != nil {
			ids = append(ids, m[1])
		}
	}
	require.NotEmpty(t, ids, "no job ids parsed out of ci.yml — has its formatting changed?")

	return ids
}

// ciRequiredNeeds returns the job ids listed in ci-required's `needs:`.
func ciRequiredNeeds(t *testing.T, workflow string) []string {
	t.Helper()

	lines := strings.Split(workflow, "\n")
	start := -1
	for i, l := range lines {
		if l == "  ci-required:" {
			start = i

			break
		}
	}
	require.NotEqual(t, -1, start, "ci.yml has no ci-required job")

	inNeeds := false

	var needs []string

	for i := start; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "needs:" {
			inNeeds = true

			continue
		}
		if !inNeeds {
			continue
		}
		if m := needsEntryRe.FindStringSubmatch(l); m != nil {
			needs = append(needs, m[1])

			continue
		}
		if strings.TrimSpace(l) != "" {
			break // the sequence ended
		}
	}
	require.NotEmpty(t, needs, "ci-required's needs list did not parse — has its formatting changed?")

	return needs
}

// jobKeys returns the keys declared directly on one job — the four-space-indented mapping keys of
// its block, stopping at the next job id.
func jobKeys(t *testing.T, workflow, job string) []string {
	t.Helper()

	block := jobsBlock(t, workflow)
	start := -1
	for i, l := range block {
		if m := jobIDRe.FindStringSubmatch(l); m != nil && m[1] == job {
			start = i + 1

			break
		}
	}
	require.NotEqual(t, -1, start, "ci.yml has no job %q", job)

	keyRe := regexp.MustCompile(`^ {4}([a-z][a-z-]*):`)

	var keys []string

	for i := start; i < len(block); i++ {
		if jobIDRe.MatchString(block[i]) {
			break // the next job
		}
		if m := keyRe.FindStringSubmatch(block[i]); m != nil {
			keys = append(keys, m[1])
		}
	}
	require.NotEmpty(t, keys, "no keys parsed for job %q — has ci.yml's formatting changed?", job)

	return keys
}

// alwaysOnAssertion returns the jq expression in ci-required's Gate step that names the jobs which
// must have actually run — the list that turns "skipped counts as success" from a hole into a
// deliberate, bounded choice.
func alwaysOnAssertion(t *testing.T, workflow string) string {
	t.Helper()

	const marker = `missing=$(jq`

	start := strings.Index(workflow, marker)
	require.NotEqual(t, -1, start,
		"ci-required's Gate step no longer contains the always-on `missing=$(jq` assertion")

	rest := workflow[start:]
	end := strings.Index(rest, "')")
	require.NotEqual(t, -1, end, "the always-on jq expression is unterminated")

	return rest[:end]
}

// TestCIRequired_EveryJob_IsAGatedDependency asserts branch protection actually covers the whole
// workflow. A job absent from `needs:` runs, can go red, and still lets the PR merge.
func TestCIRequired_EveryJob_IsAGatedDependency(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	needed := make(map[string]bool)
	for _, n := range ciRequiredNeeds(t, workflow) {
		needed[n] = true
	}

	for _, job := range ciJobIDs(t, workflow) {
		if job == "ci-required" {
			continue // it cannot depend on itself
		}
		require.True(t, needed[job],
			"job %q is not in ci-required's needs list, so nothing blocks a merge on it. "+
				"Add it to the needs: sequence in .github/workflows/ci.yml.", job)
	}
}

// TestCIRequired_NeedsList_NamesOnlyRealJobs is the reverse direction. A typo'd entry in `needs:`
// makes the whole workflow invalid on GitHub, which surfaces as a confusing "workflow not found"
// rather than as a naming error.
func TestCIRequired_NeedsList_NamesOnlyRealJobs(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	declared := make(map[string]bool)
	for _, j := range ciJobIDs(t, workflow) {
		declared[j] = true
	}

	for _, n := range ciRequiredNeeds(t, workflow) {
		require.True(t, declared[n], "ci-required needs %q, which is not a job in ci.yml", n)
	}
}

// TestCIRequired_SupplyChainJobs_AreAlwaysOnAndBlocking discharges the acceptance criterion in
// docs/development/first-ten-prs.md: govulncheck wired into ci-required, not continue-on-error.
//
// Being in `needs:` alone is not enough. ci-required treats `skipped` as success by design — that
// is what stops a path-filtered job from wedging the merge queue — so a supply-chain job that
// acquired an `if:` would silently stop gating anything while still appearing in the needs list.
// The positive assertion in ci-required's Gate step is what closes that hole, and this test is what
// keeps the two jobs inside it.
func TestCIRequired_SupplyChainJobs_AreAlwaysOnAndBlocking(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	for _, job := range []string{"security-licences", "security-govulncheck"} {
		t.Run(job, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, ciRequiredNeeds(t, workflow), job,
				"%s must be in ci-required's needs list", job)

			// Scoped to the always-on jq array, not searched across the whole file: a job name
			// quoted anywhere else — a comment, a different jq expression — would otherwise
			// satisfy this and the assertion would pass without the job being asserted at all.
			require.Contains(t, alwaysOnAssertion(t, workflow), `"`+job+`"`,
				"%s must appear in ci-required's always-on assertion list, or a stray `if:` would "+
					"let it be skipped and counted as success", job)

			// Unconditional at the source, not only asserted after the fact. scripts/** is in no
			// path filter, so a supply-chain job gated on `changes` would stop running exactly when
			// the script defining it was edited.
			keys := jobKeys(t, workflow, job)
			require.NotContains(t, keys, "if",
				"%s must be unconditional — an `if:` makes it skippable, and ci-required counts "+
					"skipped as success", job)
			require.NotContains(t, keys, "needs",
				"%s must not depend on the `changes` filter job", job)
		})
	}
}

// TestCIWorkflow_NoContinueOnError asserts no job or step opts out of failing the build.
//
// `continue-on-error: true` is the quiet way to keep a gate in the workflow while removing its
// teeth: the job still appears, still runs, still shows its output, and never blocks anything.
// AGENTS.md forbids disabling a CI gate to land a change; this is the shape that would take.
func TestCIWorkflow_NoContinueOnError(t *testing.T) {
	t.Parallel()

	for i, line := range strings.Split(readCIWorkflow(t), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // prose about the rule is not the rule
		}
		require.NotContains(t, trimmed, "continue-on-error",
			"ci.yml:%d opts a gate out of failing the build:\n\t%s", i+1, line)
	}
}
