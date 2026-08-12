// Tests that a CI job declares the toolchain it runs on.
//
// Every input of the `setup-toolchain` composite action defaults to "false", so a job that calls it
// with no inputs installs nothing and then runs whatever the runner image happens to ship. Three
// jobs did exactly that while running Go targets (issue #156), and the shape of the eventual failure
// is what makes it worth a test: `ci.yml` sets `GOTOOLCHAIN=local` deliberately — a runner-image bump
// must not silently change compilers — so the first `go.mod` bump past the image's Go turns those
// jobs red with a toolchain error rather than a test failure, in jobs whoever bumped `go.mod` never
// touched. Until then they pay a cold compile on every run, which is the cost the module and build
// caches exist to remove and which they cannot remove from a job that never installs Go.
//
// Same defect class as issue #83 and the `python:` input: a job that does not declare its toolchain
// works until the day it does not, and the failure names the wrong thing.
package repo_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// toolchainInputRe matches one input of a `with:` mapping — `go: "true"`, `tools: "oasdiff"`.
var toolchainInputRe = regexp.MustCompile(`^([a-z][a-z0-9-]*):\s*(.*)$`)

// TestCIJobs_RunningGoTargets_DeclareTheGoToolchain covers issue #156.
//
// The jobs are named individually rather than derived from their `make` targets, for the same reason
// python_floor_test.go names its two: all three targets are `notyet` stubs today, so a scan of the
// Makefile recipes would conclude — correctly, and uselessly — that none of them runs Go yet. What
// is being asserted is what the job WILL run, which is a fact about the design and belongs in a list
// a human maintains.
//
// Two more jobs have the same zero-input shape and are tracked in issue #164 rather than fixed here:
// `test / importer` (`go`) and `budget / bundle` (`python`). Add them to this table with that fix.
func TestCIJobs_RunningGoTargets_DeclareTheGoToolchain(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainActionRel)
	require.Contains(t, action, "  go:",
		"the setup-toolchain action must expose a `go` input — a job that runs a Go target needs a "+
			"way to say so")

	workflow := readCIWorkflow(t)

	for _, tc := range []struct {
		job  string
		why  string
		want map[string]string
	}{
		{
			job:  "test-golden:",
			why:  "runs `make test-golden`, the parser golden suite — a Go test binary once Phase 4 fills the stub",
			want: map[string]string{"go": "true"},
		},
		{
			job:  "test-authz:",
			why:  "runs `make test-authz`, which enumerates the matrix from the Huma registry — a Go test binary",
			want: map[string]string{"go": "true"},
		},
		{
			job: "api-breaking:",
			why: "runs `make api-breaking`, which drives oasdiff — a Go program this action installs with `go install`",
			// oasdiff as well as Go: the tool is what the job exists to run, and the action's
			// installer needs a compiler before it can produce it.
			want: map[string]string{"go": "true", "tools": "oasdiff"},
		},
	} {
		t.Run(strings.TrimSuffix(tc.job, ":"), func(t *testing.T) {
			t.Parallel()

			got := jobToolchainInputs(t, workflow, tc.job)

			require.NotEmptyf(t, got,
				"ci.yml's %s job calls setup-toolchain with NO inputs, then %s. Every input defaults "+
					"to \"false\", so the job installs nothing (issue #156).", tc.job, tc.why)

			for input, value := range tc.want {
				require.Equalf(t, value, got[input],
					"ci.yml's %s job %s, so it must pass %s: %q to setup-toolchain. Got %q.",
					tc.job, tc.why, input, value, got[input])
			}
		})
	}
}

// jobToolchainInputs returns the inputs one ci.yml job passes to the setup-toolchain composite
// action, with their surrounding quotes stripped.
//
// Parsed rather than substring-matched: these jobs carry comments that quote the inputs they discuss,
// and an assertion a comment can satisfy is not an assertion.
func jobToolchainInputs(t *testing.T, workflow, jobKey string) map[string]string {
	t.Helper()

	const uses = "uses: ./.github/actions/setup-toolchain"

	var found []string

	for _, step := range strings.Split(jobBlock(t, workflow, jobKey), "\n      - ")[1:] {
		if strings.HasPrefix(step, uses) {
			found = append(found, step)
		}
	}

	require.Lenf(t, found, 1,
		"expected exactly one `%s` step in ci.yml's %s job, found %d", uses, jobKey, len(found))

	inputs := map[string]string{}
	inWith := false

	for _, line := range strings.Split(found[0], "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "#"), trimmed == "":
			continue
		case trimmed == "with:":
			inWith = true
		case !inWith:
			continue
		// An input of the `with:` mapping is indented exactly ten spaces: two for the job, four for
		// the step, two for `with:`, two for the input. Anchoring to that keeps a key from anywhere
		// else in the step out of the map.
		case !strings.HasPrefix(line, strings.Repeat(" ", 10)):
			continue
		default:
			if m := toolchainInputRe.FindStringSubmatch(trimmed); m != nil {
				inputs[m[1]] = strings.Trim(strings.TrimSpace(m[2]), `"`)
			}
		}
	}

	return inputs
}
