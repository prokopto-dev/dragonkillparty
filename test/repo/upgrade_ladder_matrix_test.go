// The nightly upgrade ladder's matrix plumbing (issue #255).
//
// nightly-verify concluded `failure` four nights running with all eighteen jobs green, no red
// check-run, and no `upgrade-ladder` job entry at all. `upgrade-ladder` builds its matrix from
// `fromJSON(needs.upgrade-ladder-enumerate.outputs.versions)`; the enumerate step printed its JSON
// array to stdout and wrote NOTHING to $GITHUB_OUTPUT, so the declared output was the empty string
// and `fromJSON(”)` failed. A strategy-expression error is reported against the RUN rather than
// against a job, which is exactly why there was nothing for a maintainer to open.
//
// Two halves, and both are asserted here because both are load-bearing:
//
//	the producer publishes `versions`, always as valid JSON   TestUpgradeLadderEnumerate_…
//	the consumer SKIPS an empty ladder instead of expanding   TestUpgradeLadder_EmptyMatrix_…
//
// The second is not made redundant by the first. `[]` is the correct answer before the first
// release — this repository's state until Phase 8 publishes one — and an empty matrix vector is its
// own expansion error ("Matrix vector 'version' does not contain any values"). A tree with nothing
// to upgrade-test must PASS.
//
// Aimed at the Phase 8 rewrite more than at today's tree. `upgrade-ladder-enumerate` is a stub
// emitting `[]`; when it grows the real GitHub-Releases enumeration, dropping either half puts back
// a nightly nobody can diagnose. So the producer assertion RUNS the target and reads the key it
// actually wrote, rather than grepping the recipe: a rewrite that moves the enumeration into a
// script keeps passing, and one that forgets the output does not.
package repo_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	nightlyWorkflowRel   = ".github/workflows/nightly-verify.yml"
	enumerateJobKey      = "upgrade-ladder-enumerate:"
	ladderJobKey         = "upgrade-ladder:"
	enumerateMakeTarget  = "upgrade-ladder-enumerate"
	enumerateOutputsExpr = "needs.upgrade-ladder-enumerate.outputs."
)

// jobOutputRe matches one entry of a job's `outputs:` mapping — six spaces, a name, a step
// reference. `versions: ${{ steps.e.outputs.versions }}`.
var jobOutputRe = regexp.MustCompile(`^ {6}([a-z][a-z0-9_-]*): \$\{\{ steps\.[a-z0-9_-]+\.outputs\.[a-z0-9_-]+ \}\}`)

// enumerateOutputName returns the name the enumerate job publishes for the ladder's matrix.
//
// Read out of the workflow rather than hardcoded, so the assertion below compares the two ends of
// the same contract — what the job SAYS it publishes against what the target actually writes —
// instead of comparing each independently against a constant in this file, which is how a test goes
// on passing over a rename that broke the wiring.
func enumerateOutputName(t *testing.T, workflow string) string {
	t.Helper()

	block := jobBlock(t, workflow, enumerateJobKey)

	var names []string

	for _, line := range strings.Split(block, "\n") {
		if m := jobOutputRe.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	}

	require.Lenf(t, names, 1,
		"the %s job must declare exactly one output — the matrix source — and it declares %d. "+
			"An output declared with no step writing it is the #255 defect: the value is the empty "+
			"string and the matrix below it fails to expand.",
		strings.TrimSuffix(enumerateJobKey, ":"), len(names))

	return names[0]
}

// envWithout returns the process environment with GITHUB_OUTPUT removed, plus extra.
//
// Built explicitly rather than with t.Setenv, which makes t.Parallel panic — runRootedScript's
// reason. Stripping GITHUB_OUTPUT is the load-bearing part: GitHub Actions sets it for EVERY step,
// so a test that inherited it would (a) append to the real step's output file and (b) never be able
// to exercise the outside-Actions path at all. The bug this file covers was a missing output write;
// a test that cannot tell the two environments apart cannot see it.
func envWithout(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))

	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GITHUB_OUTPUT=") {
			continue
		}

		env = append(env, kv)
	}

	return append(env, extra...)
}

// runEnumerate runs `make upgrade-ladder-enumerate` and returns its stdout.
//
// stdout alone, not CombinedOutput: the target's stdout IS the matrix, so a warning on stderr must
// not end up parsed as JSON. --no-print-directory for lint_cache_test.go's reason — GNU make wraps
// a `-C` build in "Entering directory" banners and BSD make on macOS does not, so without it this
// passes on a laptop and fails on CI.
func runEnumerate(t *testing.T, extraEnv ...string) string {
	t.Helper()

	cmd := exec.Command("make", "-C", repoRoot(t), "--no-print-directory", enumerateMakeTarget)
	cmd.Env = envWithout(extraEnv...)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	out, err := cmd.Output()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("make %s exited %d — a target that cannot enumerate must say so, but it must not "+
			"fail merely because nothing is published yet\nstdout: %s\nstderr: %s",
			enumerateMakeTarget, exitErr.ExitCode(), out, stderr.String())
	}

	require.NoErrorf(t, err, "run make %s\n%s", enumerateMakeTarget, stderr.String())

	return string(out)
}

// TestUpgradeLadderEnumerate_EmitsValidJSON_AndPublishesTheMatrixOutput covers issue #255's
// producer half.
//
// Table-driven over the two environments the target runs in, because the difference between them is
// the whole defect: outside Actions it must still succeed and still print a usable array, and
// inside Actions it must ALSO write the output the matrix reads.
func TestUpgradeLadderEnumerate_EmitsValidJSON_AndPublishesTheMatrixOutput(t *testing.T) {
	t.Parallel()

	want := enumerateOutputName(t, readRepoFile(t, nightlyWorkflowRel))

	for _, tc := range []struct {
		name         string
		inActions    bool
		whyItMatters string
	}{
		{
			name:      "outside Actions",
			inActions: false,
			whyItMatters: "a contributor reproducing the ladder locally has no $GITHUB_OUTPUT, and " +
				"the target must neither fail nor go silent on them",
		},
		{
			name:      "in Actions",
			inActions: true,
			whyItMatters: "the workflow job publishes this output and `upgrade-ladder` expands it " +
				"with fromJSON — unwritten, it is the empty string and the RUN fails outside the " +
				"job graph",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				outputFile string
				extraEnv   []string
			)

			if tc.inActions {
				outputFile = filepath.Join(t.TempDir(), "github_output")
				require.NoError(t, os.WriteFile(outputFile, nil, 0o600))

				extraEnv = append(extraEnv, "GITHUB_OUTPUT="+outputFile)
			}

			stdout := strings.TrimSpace(runEnumerate(t, extraEnv...))

			var versions []string

			require.NoErrorf(t, json.Unmarshal([]byte(stdout), &versions),
				"make %s printed %q, which is not a JSON array. It is the matrix source: anything "+
					"fromJSON cannot read fails `upgrade-ladder`'s expansion, and an expansion "+
					"failure is reported against the RUN with no job entry to click (issue #255). "+
					"Emit `[]` when there is nothing to ladder — %s",
				enumerateMakeTarget, stdout, tc.whyItMatters)

			if !tc.inActions {
				return
			}

			written, err := os.ReadFile(outputFile)
			require.NoError(t, err, "read the fabricated $GITHUB_OUTPUT")

			lines := nonBlankLines(string(written))

			require.Lenf(t, lines, 1,
				"make %s wrote %d lines to $GITHUB_OUTPUT, want exactly one `%s=<json>` — %s\ngot: %s",
				enumerateMakeTarget, len(lines), want, tc.whyItMatters, written)

			require.Equalf(t, want+"="+stdout, lines[0],
				"make %s must publish the array it prints under the name %s job declares (%q), and "+
					"it wrote %q. The workflow reads `%s%s`; a key that does not match is an output "+
					"nothing sets, which is the #255 defect wearing a different name.",
				enumerateMakeTarget, strings.TrimSuffix(enumerateJobKey, ":"), want, lines[0],
				enumerateOutputsExpr, want)
		})
	}
}

// nonBlankLines splits s into its non-empty, non-whitespace lines.
func nonBlankLines(s string) []string {
	var out []string

	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}

	return out
}

// TestUpgradeLadder_EmptyMatrix_SkipsInsteadOfFailingTheRun covers issue #255's consumer half.
//
// A guarded job is the only thing that makes "nothing published yet" a PASS. GitHub evaluates a
// job's `if:` before its `strategy:`, so a false condition skips the job without ever expanding the
// matrix — and `skipped` is not `failure`, so the run concludes success. Remove the guard and an
// empty array is an expansion error again, in the same place and with the same unclickable
// signature as the empty string was.
func TestUpgradeLadder_EmptyMatrix_SkipsInsteadOfFailingTheRun(t *testing.T) {
	t.Parallel()

	workflow := readRepoFile(t, nightlyWorkflowRel)
	output := enumerateOutputName(t, workflow)
	block := jobBlock(t, workflow, ladderJobKey)
	expr := enumerateOutputsExpr + output

	var guard string

	for _, line := range strings.Split(block, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "if:") {
			guard = trimmed
		}
	}

	require.NotEmptyf(t, guard,
		"the %s job has no `if:`, so it expands its matrix unconditionally. With nothing published "+
			"to upgrade from, `%s` is `[]` and the expansion fails the whole nightly run outside "+
			"the job graph — eighteen green jobs and a red badge nobody can open (issue #255). A "+
			"pre-release tree with nothing to upgrade-test must pass.",
		strings.TrimSuffix(ladderJobKey, ":"), expr)

	require.Containsf(t, block, "fromJSON("+expr+")",
		"the %s job must expand the matrix from the output %s publishes (`%s`) — the guard and the "+
			"expansion have to read the SAME value or the guard protects nothing",
		strings.TrimSuffix(ladderJobKey, ":"), strings.TrimSuffix(enumerateJobKey, ":"), expr)

	for _, empty := range []struct{ literal, why string }{
		{
			literal: "''",
			why: "an UNSET output is the empty string, which is what issue #255 actually hit: " +
				"fromJSON('') is a parse error. An enumerate that dies before writing its output " +
				"must skip the ladder, never be read as though the ladder ran",
		},
		{
			literal: "'[]'",
			why: "an EMPTY array is the honest answer before the first release, and GitHub rejects " +
				"an empty matrix vector with \"Matrix vector 'version' does not contain any values\"",
		},
	} {
		require.Containsf(t, guard, expr+" != "+empty.literal,
			"the %s job's guard is %q, which does not exclude %s — %s",
			strings.TrimSuffix(ladderJobKey, ":"), guard, empty.literal, empty.why)
	}
}
