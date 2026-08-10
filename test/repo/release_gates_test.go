package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests assert the SHAPE of the release train's config files. They cannot run `docker buildx`
// or `cosign` in a unit test, and they do not try to: what is checkable offline, and what a
// regression would actually break, is the structural guarantees — the base is scratch, the container
// runs unprivileged, the healthcheck is not the database-touching one, moving tags advance only
// after smoke, and the high-risk dependencies never automerge. Each is a line an agent under
// wall-clock pressure would plausibly "simplify" away, so each is pinned.

// readRepoFile reads a repo-relative file, failing the test if it is absent.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	require.NoErrorf(t, err, "%s must exist", rel)

	return string(data)
}

// TestDockerfile_IsScratchAndHardened asserts the PR 7 image criteria: multi-stage ending
// FROM scratch, CA certs and tzdata copied in, USER 65532, VOLUME /data, and the HEALTHCHECK
// invoking `dkp healthcheck` — NOT `dkp doctor`, which touches the database (canonical §13).
func TestDockerfile_IsScratchAndHardened(t *testing.T) {
	t.Parallel()

	df := readRepoFile(t, "deploy/Dockerfile")

	require.Contains(t, df, "FROM scratch", "the final stage must be FROM scratch")
	require.Contains(t, df, "USER 65532", "the container must run as the non-root uid 65532")
	require.Contains(t, df, "VOLUME /data", "the writable volume must be declared")
	require.Contains(t, df, "ca-certificates.crt", "CA certificates must be copied into the scratch image")
	require.Contains(t, df, "zoneinfo", "the tzdata/zoneinfo database must be copied into the scratch image")

	require.Contains(t, df, "HEALTHCHECK", "the image must declare a HEALTHCHECK")
	require.Contains(t, df, "dkp\", \"healthcheck", "HEALTHCHECK must invoke `dkp healthcheck`")

	// Check the HEALTHCHECK instruction itself, not the whole file: the surrounding comments
	// legitimately name `dkp doctor` to explain why it is NOT used. What must never happen is the
	// instruction invoking it — a database-touching healthcheck lets Docker kill the container
	// mid-migration (canonical §13).
	hc := healthcheckInstruction(t, df)
	require.NotContains(t, hc, "doctor",
		"canonical §13: the HEALTHCHECK must be `dkp healthcheck`, never `dkp doctor`:\n  %s", hc)

	// A multi-stage build: at least two FROM lines, one of them the build stage.
	require.GreaterOrEqual(t, strings.Count(df, "FROM "), 2, "the Dockerfile must be multi-stage")
}

// healthcheckInstruction returns the full HEALTHCHECK instruction, joining any `\`-continued lines,
// so a test can assert on the command it runs rather than on the whole file.
func healthcheckInstruction(t *testing.T, dockerfile string) string {
	t.Helper()

	lines := strings.Split(dockerfile, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "HEALTHCHECK") {
			continue
		}

		instr := line
		for strings.HasSuffix(strings.TrimSpace(instr), "\\") && i+1 < len(lines) {
			i++
			instr = strings.TrimSuffix(strings.TrimRight(instr, " "), "\\") + " " + lines[i]
		}

		return instr
	}

	t.Fatal("no HEALTHCHECK instruction found in the Dockerfile")

	return ""
}

// TestReleaseWorkflow_PromoteNeedsSmoke asserts the ordering rule that makes a release safe: the
// moving tags (:1, :1.x, :latest) advance only after smoke. Encoded as `promote needs smoke`. If a
// refactor dropped `smoke` from `promote`'s needs, a build that failed to boot could still move :1,
// which is the exact incident the whole design exists to prevent.
func TestReleaseWorkflow_PromoteNeedsSmoke(t *testing.T) {
	t.Parallel()

	rel := readRepoFile(t, ".github/workflows/release.yml")

	promote := jobBlock(t, rel, "promote:")
	require.Contains(t, promote, "smoke",
		"the promote job must depend on smoke — moving tags advance only after the smoke gate passes")

	// The manifest/immutable job publishes first and does NOT advance a moving tag; that is what
	// leaves :1 on the previous digest when a build is broken. A smoke-less promote would defeat it.
	require.Contains(t, rel, "needs: [prepare, manifest, smoke]",
		"promote must need prepare, manifest AND smoke")
}

// TestReleaseWorkflow_NoQemuAnywhere is the workflow-file half of the "no QEMU" criterion, checked
// against the real committed workflows rather than a fixture. Multi-arch is cross-compiled; the
// string must not appear in a step. Comments explaining the choice are allowed (they contain "No
// QEMU"), so this checks non-comment lines only, mirroring QEMU001's comment stripping.
func TestReleaseWorkflow_NoQemuAnywhere(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	commentLine := regexp.MustCompile(`^\s*#`)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)

		for _, line := range strings.Split(string(body), "\n") {
			if commentLine.MatchString(line) {
				continue // prose about "No QEMU" is not a QEMU step
			}

			require.NotContainsf(t, strings.ToLower(line), "qemu",
				"%s has a non-comment QEMU line — multi-arch is cross-compiled, never emulated:\n  %s",
				e.Name(), line)
		}
	}
}

// TestRenovate_HighRiskDepsNeverAutomerge asserts the release-policy criterion: huma, goose, river,
// the SQLite driver (and the toolchain pins) never automerge — they sit behind dashboard approval.
// A green CI run is not sufficient evidence to bump the storage engine or the job driver.
func TestRenovate_HighRiskDepsNeverAutomerge(t *testing.T) {
	t.Parallel()

	cfg := readRepoFile(t, ".github/renovate.json5")

	// The high-risk rule must name each of these and set dependencyDashboardApproval with no
	// automerge. We assert the names are present and that the block carries the approval gate; the
	// json5 is small enough that substring presence is a faithful check.
	for _, dep := range []string{
		"github.com/danielgtaylor/huma/**",
		"github.com/pressly/goose/**",
		"github.com/riverqueue/river",
		"modernc.org/sqlite",
	} {
		require.Containsf(t, cfg, dep,
			"renovate.json5 must list %q in the high-risk, never-automerge block", dep)
	}

	require.Contains(t, cfg, "dependencyDashboardApproval: true",
		"the high-risk block must gate on dashboard approval")

	// The auth firewall: whatever internal/auth ends up depending on must not automerge either. The
	// package does not exist yet, so this is enforced by a path rule that freezes automerge for any
	// module a future internal/auth pulls in — asserted here so the rule is not quietly dropped.
	require.Contains(t, cfg, "internal/auth",
		"renovate.json5 must carry an internal/auth dependency-set rule (PR 7 criterion): auth deps "+
			"never automerge")
}

// TestImageSize_ReleaseEnforcesBudget backs the "hard gate" claim with a test. The 30 MB compressed
// budget is advisory on the PR path (MODE=advise → exit 0 + `::warning::`) but a HARD gate on the
// release path. The only thing that makes it a gate is scripts/release-image.sh invoking
// scripts/image-size.sh with MODE=enforce AFTER the image is built and pushed: image-size.sh's
// enforce branch (`[ "$MODE" = "enforce" ] && exit 1`) is dead code unless a caller sets it, and the
// release matrix is fail-fast so a single over-budget arch stops the release before the manifest is
// joined. If that enforcing invocation is ever removed, an oversized image would warn-and-ship — so
// this test fails the moment the gate is laundered back into a no-op.
func TestImageSize_ReleaseEnforcesBudget(t *testing.T) {
	t.Parallel()

	// The release-path caller must invoke the measurement in enforce mode.
	rel := readRepoFile(t, "scripts/release-image.sh")
	require.Regexp(t, regexp.MustCompile(`MODE=enforce[^\n]*image-size\.sh`), rel,
		"scripts/release-image.sh must invoke scripts/image-size.sh with MODE=enforce so a "+
			"breach of the 30 MB budget HARD-FAILS the release (not just warns)")

	// And image-size.sh's enforce branch must actually exit non-zero on a breach — otherwise
	// MODE=enforce would be cosmetic and the gate a no-op even with the caller wired up.
	script := readRepoFile(t, "scripts/image-size.sh")
	require.Contains(t, script, `[ "$MODE" = "enforce" ] && exit 1`,
		"image-size.sh's enforce mode must exit non-zero on a breach; without it MODE=enforce is a "+
			"laundered gate")

	// The enforcement runs where a built image and its size genuinely exist and can fail the release:
	// after the per-arch image is pushed. Assert the enforcing call lives AFTER the buildx push, not
	// before it (where there would be nothing to measure and the gate could not fire).
	push := strings.Index(rel, "--push")
	enforce := strings.Index(rel, "MODE=enforce")
	require.NotEqual(t, -1, push, "release-image.sh must push the image it then measures")
	require.NotEqual(t, -1, enforce, "release-image.sh must enforce the image-size budget")
	require.Greater(t, enforce, push,
		"the MODE=enforce measurement must run AFTER the image is pushed, so it measures a real image")
}

// TestReleaseWorkflow_PrepareVerifiesShippedLock keeps the shipped-migration manifest wired into the
// release, and wired in at the only place where a failure is still free.
//
// db/migrations-sqlite/SHIPPED.lock records which migrations have run on a user's database. Every
// migration present at a tag ships with that tag, so the release must assert the manifest already
// lists them all — an unsealed one leaves a hole nobody notices until somebody edits that file two
// releases later and MIG003, which only checks the rows that ARE there, says fine.
//
// Three things have to hold together, and each is a line a refactor could drop without breaking
// anything visible: the step runs in `prepare` (before any image, binary, attestation or moving tag
// exists), the Makefile target passes --complete (without it the release runs the per-PR check and
// the completeness assertion silently disappears), and the script's --complete branch actually
// fails. The middle one is the laundering risk: `verify` alone still prints a reassuring green line.
func TestReleaseWorkflow_PrepareVerifiesShippedLock(t *testing.T) {
	t.Parallel()

	rel := readRepoFile(t, ".github/workflows/release.yml")

	prepare := jobBlock(t, rel, "prepare:")
	require.Contains(t, prepare, "make release-shipped-lock",
		"the prepare job must verify db/migrations-sqlite/SHIPPED.lock before anything is published")

	mk := readRepoFile(t, "Makefile")
	require.Regexp(t, regexp.MustCompile(`release-shipped-lock:\n\t@[^\n]*shipped-lock\.sh verify --complete`), mk,
		"`make release-shipped-lock` must run the manifest check with --complete; plain `verify` is "+
			"the per-PR gate and would pass a release whose manifest was never sealed")

	script := readRepoFile(t, "scripts/shipped-lock.sh")
	require.Contains(t, script, "--complete) complete=1",
		"shipped-lock.sh must honour --complete; without it the flag is cosmetic and the release "+
			"gate is a laundered no-op")
}

// jobBlock extracts the text of a top-level workflow job, from its `name:` key to the next
// top-level job key (a line indented by exactly two spaces ending in `:`), so an assertion about one
// job cannot accidentally match text in another.
func jobBlock(t *testing.T, workflow, jobKey string) string {
	t.Helper()

	// jobKey is like "promote:"; jobs are indented two spaces under `jobs:`.
	marker := "  " + jobKey
	start := strings.Index(workflow, marker)
	require.NotEqualf(t, -1, start, "job %q not found in workflow", jobKey)

	rest := workflow[start+len(marker):]
	next := regexp.MustCompile(`\n {2}[a-zA-Z0-9_-]+:`).FindStringIndex(rest)
	if next == nil {
		return workflow[start:]
	}

	return workflow[start : start+len(marker)+next[0]]
}
