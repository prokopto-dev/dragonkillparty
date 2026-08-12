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

// onBlock returns the lines of ci.yml's top-level `on:` mapping — the trigger declaration.
func onBlock(t *testing.T, workflow string) []string {
	t.Helper()

	lines := strings.Split(workflow, "\n")
	start := -1

	for i, l := range lines {
		if l == "on:" {
			start = i + 1

			break
		}
	}

	require.NotEqual(t, -1, start, "ci.yml has no top-level `on:` key")

	for i := start; i < len(lines); i++ {
		l := lines[i]
		if l == "" || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "#") {
			continue
		}

		return lines[start:i] // a new top-level key ends the block
	}

	return lines[start:]
}

// deepAssertion returns the body of ci-required's `if [ "$DEEP" = "true" ]` block — the assertion
// that a job gated on `deep` was not silently skipped on a reviewable PR.
func deepAssertion(t *testing.T, workflow string) string {
	t.Helper()

	return tierAssertion(t, workflow, "DEEP")
}

// tierAssertion returns the body of one of ci-required's `if [ "$TIER" = "true" ]` blocks — the
// assertion that a job gated on that tier was not silently skipped on a run where it had to fire.
func tierAssertion(t *testing.T, workflow, variable string) string {
	t.Helper()

	marker := `if [ "$` + variable + `" = "true" ]; then`

	start := strings.Index(workflow, marker)
	require.NotEqualf(t, -1, start,
		"ci-required's Gate step no longer contains the `%s` assertion", marker)

	// The block ends at the `fi` closing it: the first line at the same indentation as the `if`.
	indent := ""
	if bol := strings.LastIndex(workflow[:start], "\n"); bol != -1 {
		indent = workflow[bol+1 : start]
	}

	rest := workflow[start:]

	end := strings.Index(rest, "\n"+indent+"fi\n")
	require.NotEqualf(t, -1, end, "the %s assertion block is unterminated", variable)

	return rest[:end]
}

// tierGatedJobs returns every job whose `if:` reads one of the `changes` tier outputs.
func tierGatedJobs(t *testing.T, workflow, output string) []string {
	t.Helper()

	var (
		jobs    []string
		current string
	)

	for _, l := range jobsBlock(t, workflow) {
		if m := jobIDRe.FindStringSubmatch(l); m != nil {
			current = m[1]

			continue
		}

		if strings.HasPrefix(l, "    if:") && strings.Contains(l, "needs.changes.outputs."+output) {
			jobs = append(jobs, current)
		}
	}

	require.NotEmptyf(t, jobs,
		"no job in ci.yml gates on needs.changes.outputs.%s — that tier has been removed, or the "+
			"`if:` lines no longer parse", output)

	return jobs
}

// deepGatedJobs returns every job whose `if:` reads needs.changes.outputs.deep.
func deepGatedJobs(t *testing.T, workflow string) []string {
	t.Helper()

	return tierGatedJobs(t, workflow, "deep")
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
// is what stops a path-filtered job from wedging a PR that never merges — so a supply-chain job that
// acquired an `if:` would silently stop gating anything while still appearing in the needs list.
// The positive assertion in ci-required's Gate step is what closes that hole, and this test is what
// keeps the two jobs inside it.
func TestCIRequired_SupplyChainJobs_AreAlwaysOnAndBlocking(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	// security-osv joined the other two with issue #132. It is the npm graph's only vulnerability
	// coverage, so the same three properties matter for it and for the same reason: in the needs
	// list, named in the always-on assertion, and unconditional at the source.
	for _, job := range []string{"security-licences", "security-govulncheck", "security-osv"} {
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

// TestCIWorkflow_PullRequestTrigger_ListensForReadyForReview closes issue #82.
//
// `deep` is a draft gate — it reads github.event.pull_request.draft, and three jobs hang off it:
// test / e2e — and, when issue #82 was found, build / image and test / importer too. The gate
// assumes leaving draft re-evaluates it,
// and that only happens if the workflow LISTENS for the event that clears it. `on: pull_request:`
// with no `types:` defaults to [opened, synchronize, reopened], which does not include
// ready_for_review — so a PR opened as a draft, marked ready and merged with no further push
// reached main with all three never having run, ci-required green the whole way. That is the flow
// CONTRIBUTING.md documents, not an unusual one. There is no merge queue, so merge_group is not a
// fallback, and push happens after the merge rather than before it.
//
// The three defaults are asserted alongside it because declaring `types:` REPLACES the default list
// rather than extending it: a future edit that trims this to [ready_for_review] would stop CI
// running on every push to an open PR, which is a strictly worse hole than the one being fixed.
func TestCIWorkflow_PullRequestTrigger_ListensForReadyForReview(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	var types string

	inPullRequest := false

	for _, l := range onBlock(t, workflow) {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
			inPullRequest = strings.TrimSpace(l) == "pull_request:"

			continue
		}

		if inPullRequest && strings.HasPrefix(strings.TrimSpace(l), "types:") {
			types = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "types:"))

			break
		}
	}

	require.NotEmpty(t, types,
		"ci.yml's `on: pull_request:` declares no `types:`, so it defaults to "+
			"[opened, synchronize, reopened]. Leaving draft fires `ready_for_review`, which is not in "+
			"that list, so the `deep` jobs never re-run and a draft PR can be merged without them "+
			"(issue #82).")

	for _, event := range []string{"opened", "synchronize", "reopened", "ready_for_review"} {
		require.Containsf(t, types, event,
			"ci.yml's `on: pull_request: types:` must name %q. Declaring `types:` REPLACES the default "+
				"[opened, synchronize, reopened] rather than extending it, and `ready_for_review` is the "+
				"event that clears the draft gate (issue #82). Current list: %s", event, types)
	}
}

// TestCIRequired_DeepGatedJobs_AreAssertedNotSkipped keeps ci-required's deep assertion in step with
// the jobs that are actually gated on `deep`.
//
// ci-required counts `skipped` as success, which is right for path filtering and wrong for the draft
// gate: once a PR is reviewable there is no legitimate reason for a deep job to be absent. Step 3 of
// the Gate step is what states that, and it states it by NAME — so a new deep-gated job that nobody
// adds to it is a job whose skip is once again indistinguishable from a correct filter. Issue #82 is
// the same invariant failing from the trigger side; this is the job-graph side of it.
func TestCIRequired_DeepGatedJobs_AreAssertedNotSkipped(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)
	assertion := deepAssertion(t, workflow)

	for _, job := range deepGatedJobs(t, workflow) {
		require.Containsf(t, assertion, `"`+job+`"`,
			"job %q is gated on needs.changes.outputs.deep but is not named in ci-required's "+
				"`if [ \"$DEEP\" = \"true\" ]` block. ci-required counts a skip as success, so on a "+
				"reviewable PR that job can be skipped and merged past. Add it to the assertion in "+
				".github/workflows/ci.yml.", job)
	}
}

// TestCIRequired_PostMergeJobs_AreAssertedNotSkipped is the deep-tier assertion above applied to
// the tier issue #159 added, and it is the assertion that makes that tier legitimate at all.
//
// A post-merge job is SKIPPED on every pull request by design. That is the exact shape issue #101
// deleted two jobs for — `mq / image-arm64` and `mq / upgrade-from-latest-release` sat in
// ci-required's needs, never ran, and reported satisfied on every PR — and the only thing that
// distinguishes `build / image` from them is that its skip is asserted against on the runs where it
// must not happen. Without this block a typo in the `postmerge` output would produce a job that
// never runs anywhere and a checks list that never says so.
func TestCIRequired_PostMergeJobs_AreAssertedNotSkipped(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)
	assertion := tierAssertion(t, workflow, "POSTMERGE")

	for _, job := range tierGatedJobs(t, workflow, "postmerge") {
		require.Containsf(t, assertion, `"`+job+`"`,
			"job %q is gated on needs.changes.outputs.postmerge but is not named in ci-required's "+
				"`if [ \"$POSTMERGE\" = \"true\" ]` block. It is skipped on every PR by design, so "+
				"nothing else would notice if it stopped running on main either — which is a job that "+
				"exists in the needs list and nowhere else (issue #101). Add it to the assertion in "+
				".github/workflows/ci.yml.", job)
	}
}

// TestCIWorkflow_NoJob_IsGatedOnMergeGroup closes issue #101.
//
// This repository has no merge queue — `required_merge_queue` is null on `main` — so `merge_group`
// never fires and a job conditioned on it never runs. That would be harmless if it read as absent,
// and it does not: ci-required counts `skipped` as success, so such a job reports satisfied on every
// PR and is indistinguishable in the checks list from one that did the work. ci.yml carried two of
// them (`mq / image-arm64`, `mq / upgrade-from-latest-release`), named in ci-required's needs and
// described in docs/design/06-cicd-and-release.md §4 as "required in queue", plus two
// `|| github.event_name == 'merge_group'` escape hatches on test-migrations and test-importer that
// read as "the merge queue catches the rest" while the queue caught nothing. All four are gone;
// issues #108 and #109 track the coverage they claimed.
//
// Two mentions survive on purpose and neither is a job condition: the `on: merge_group:` trigger,
// and the `merge_group` term in `changes`' `deep` output. Together they mean that if a queue is ever
// switched on in repository settings — which is not a PR, and so cannot be caught in review here —
// the workflow runs in it as it does on a PR with the deep tier on, rather than ci-required never
// reporting and the queue wedging. The `deep` line is the one exception below; the trigger is
// outside the jobs block and never reaches this scan.
//
// The scan is over lines rather than over parsed `if:` keys deliberately: a condition folded across
// lines with `if: >` is the same defect spelled differently, and would slip past a key-based check.
//
// If the merge queue is ever adopted, this test is where that decision gets recorded — re-add the
// expensive tier and update this assertion in the same change, so §4's table describes checks that
// run rather than checks that would.
func TestCIWorkflow_NoJob_IsGatedOnMergeGroup(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	for _, l := range jobsBlock(t, workflow) {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "merge_group") {
			continue // prose about the rule is not the rule
		}

		require.Truef(t, strings.HasPrefix(trimmed, "deep:"),
			"ci.yml conditions a job on `merge_group`:\n\t%s\nThere is no merge queue on this "+
				"repository, so that never runs — and ci-required counts a skip as success, so it "+
				"reports green on every PR as though it had. Gate the job on something that fires, or "+
				"enable the merge queue and say so here (issue #101).", l)
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
