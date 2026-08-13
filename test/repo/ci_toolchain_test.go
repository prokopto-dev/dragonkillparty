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
// The two jobs issue #164 added to the list are here now, and one of them is in another file: #159
// moved `test / importer` to nightly-verify.yml, so its zero-input call was fixed in its new home
// and the row below reads that workflow. A table entry names the workflow it applies to for exactly
// that reason — a job can move, and the assertion should move with it rather than quietly stop
// covering anything.
func TestCIJobs_RunningGoTargets_DeclareTheGoToolchain(t *testing.T) {
	t.Parallel()

	action := readRepoFile(t, toolchainActionRel)
	require.Contains(t, action, "  go:",
		"the setup-toolchain action must expose a `go` input — a job that runs a Go target needs a "+
			"way to say so")

	for _, tc := range []struct {
		workflow string
		job      string
		why      string
		want     map[string]string
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
		{
			job: "bundle-budget:",
			why: "runs `make budget-bundle`, which BUILDS the SPA with Vite before measuring it (issue " +
				"#166) and reads the budget with python3 — the #83 defect class, where an undeclared " +
				"interpreter makes the gate fail as though its SUBJECT were wrong (issue #164). Drop " +
				"node and the job fails `pnpm: command not found`; drop python and it fails as if the " +
				"bundle were over budget",
			want: map[string]string{"python": "true", "node": "true"},
		},
		{
			job: "test-integration:",
			why: "runs `make test`, which is the ONLY lane the pnpm-gated suites execute in — above " +
				"all TestNpmrc_SuppressesALifecycleScript, the only functional proof that web/.npmrc's " +
				"ignore-scripts stops a dependency's postinstall. No job that ran `make test` " +
				"installed Node, so it skipped in CI on every run for the whole of phase 0 (issue " +
				"#177). It also drives actionlint, shellcheck and shfmt through the negative fixtures " +
				"of the two gates added by #121 and #122",
			want: map[string]string{
				"go":     "true",
				"node":   "true",
				"python": "true",
				"tools":  "golangci-lint gofumpt atlas actionlint shellcheck shfmt",
			},
		},
		{
			workflow: ".github/workflows/nightly-verify.yml",
			job:      "shuffled-suite:",
			why: "runs the same two suites as `test / integration` with -shuffle=on, so it needs the " +
				"same toolchain — the two shared the Node omission issue #177 found, because #159 " +
				"gave this job that job's `with:` block including the gap",
			want: map[string]string{
				"go":     "true",
				"node":   "true",
				"python": "true",
				"tools":  "golangci-lint gofumpt atlas actionlint shellcheck shfmt",
			},
		},
		{
			job: "lint-actions:",
			why: "runs `make lint-actions`, which needs actionlint AND shellcheck — actionlint pipes " +
				"every run: block through shellcheck and silently drops that rule when it is absent " +
				"(issue #121). Go, because both the actionlint install and the shellcheck installer's " +
				"destination come from the Go toolchain",
			want: map[string]string{"go": "true", "tools": "actionlint shellcheck"},
		},
		{
			job: "lint-shell:",
			why: "runs `make lint-shell`, which is shellcheck plus shfmt over scripts/ and .githooks/ " +
				"(issue #122)",
			want: map[string]string{"go": "true", "tools": "shellcheck shfmt"},
		},
		{
			workflow: ".github/workflows/nightly-verify.yml",
			job:      "replay-ledger:",
			why: "runs `make verify-ledger` (issue #198), which compiles cmd/dkp and runs the binary " +
				"to seed and replay a 520k-entry ledger. Nothing else in the job needs a toolchain — " +
				"no SPA, no Python — so `go` is the whole declaration, and without it the job would " +
				"build the product on whatever compiler the runner image happened to ship",
			want: map[string]string{"go": "true"},
		},
		{
			workflow: ".github/workflows/nightly-verify.yml",
			job:      "importer:",
			why: "runs `make test-importer`, a Go test binary driving testcontainers once Phase 5 fills " +
				"the stub. It was `test / importer` in ci.yml until issue #159 moved it here",
			want: map[string]string{"go": "true"},
		},
	} {
		t.Run(strings.TrimSuffix(tc.job, ":"), func(t *testing.T) {
			t.Parallel()

			path, workflow := ".github/workflows/ci.yml", readCIWorkflow(t)
			if tc.workflow != "" {
				path, workflow = tc.workflow, readRepoFile(t, tc.workflow)
			}

			got := jobToolchainInputs(t, workflow, tc.job)

			require.NotEmptyf(t, got,
				"%s's %s job calls setup-toolchain with NO inputs, then %s. Every input defaults "+
					"to \"false\", so the job installs nothing (issue #156).", path, tc.job, tc.why)

			for input, value := range tc.want {
				require.Equalf(t, value, got[input],
					"%s's %s job %s, so it must pass %s: %q to setup-toolchain. Got %q.",
					path, tc.job, tc.why, input, value, got[input])
			}
		})
	}
}

// jobToolchainInputs returns the inputs one workflow job passes to the setup-toolchain composite
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
		"expected exactly one `%s` step in the %s job, found %d", uses, jobKey, len(found))

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
