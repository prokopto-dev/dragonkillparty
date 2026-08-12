package repo_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The shell gates, and the one shape that makes a check unable to fail.
//
// scripts/smoke-local.sh printed `smoke-local: /readyz reachable` on every run from PR 7 to issue
// #84, because the line ended `&& echo reachable || echo reachable`: the command's exit status was
// discarded by construction. Three defects were stacked in it and each hid the next — the tautology,
// a host-mapped port probed from inside the container's network namespace where nothing listens on
// it, and a probe of /healthz rather than /readyz. The tautology is why the other two were invisible
// for that long, and it is the only one of the three a grep can find.
//
// So the grep exists. A check that cannot fail is worse than an absent one: the CI log for
// `build / image` said `/readyz reachable` on every run, which is a claim of coverage nobody had.

// echoTautologyRe matches `… && echo WORD || echo WORD`. RE2 has no backreferences, so the two words
// are captured and compared in Go rather than in the pattern.
//
// The word class is a character set and NOT `\S+`, which is the obvious spelling and the wrong one:
// these lines live inside a command substitution, so `\S+` runs past the word and swallows the
// closing `)"`. The second capture then reads `reachable)"`, which does not equal `reachable`, and
// the scan silently finds nothing — on the exact line it was written for. That is the same defect
// this test exists to catch, one level up, which is why TestShellGates_NoEchoTautology proves the
// pattern can fail before it trusts a clean sweep.
var echoTautologyRe = regexp.MustCompile(`&&\s*echo\s+["']?([A-Za-z0-9_.:/-]+)["']?\s*\|\|\s*echo\s+["']?([A-Za-z0-9_.:/-]+)["']?`)

// TestSmokeLocal_ReadyzCheckCanActuallyFail pins the fix for issue #84.
func TestSmokeLocal_ReadyzCheckCanActuallyFail(t *testing.T) {
	t.Parallel()

	const rel = "scripts/smoke-local.sh"

	body := readRepoFile(t, rel)

	require.Containsf(t, body, "/readyz",
		"%s must probe /readyz. `dkp healthcheck` covers /healthz, and canonical §13 splits the two "+
			"precisely because they answer different questions — /readyz is the one that knows about "+
			"migrations and about the ledger's append-only protection", rel)

	// The probe has to run on the host. $base is the port `docker port` mapped on the HOST; inside
	// the container namespace nothing listens there, so an in-container probe of it cannot succeed.
	require.Regexpf(t, `curl[^\n]*\$\{base\}/readyz`, body,
		"%s must probe ${base}/readyz with curl from the host. The published port only exists in the "+
			"host's namespace: `docker exec … --url ${base}/…` probes an address the container "+
			"cannot reach, which is what made the tautology invisible (issue #84).", rel)

	// A status code alone is not evidence. When Config.Readiness is nil the route is never
	// registered and internal/ui's SPA catch-all answers /readyz with index.html and a 200.
	require.Containsf(t, body, `"state"`,
		"%s must assert the body is a readiness report, not just that something answered: the SPA "+
			"catch-all serves index.html with a 200 for every path no route claimed, /readyz "+
			"included.", rel)
}

// TestReleaseSmoke_ProbesReadyzOnThePublishedDigest pins the fix for issue #99.
//
// The sibling of #84, and the more expensive one: this script runs against the PUBLISHED digest on
// real amd64 and arm64 hardware and is what gates the moving tags advancing. It carried a comment
// promising a /readyz probe and never made one — the line under the comment ran `dkp version`, and
// the only health-adjacent call in the file was the /healthz boot poll. The comment is what made it
// invisible: a reader auditing release coverage greps for /readyz, finds the word, and moves on. So
// the assertions below are about the probe, never about the word appearing somewhere in the file.
func TestReleaseSmoke_ProbesReadyzOnThePublishedDigest(t *testing.T) {
	t.Parallel()

	const rel = "scripts/release-smoke.sh"

	body := readRepoFile(t, rel)

	// From the HOST, at the published port — the same shape smoke-local.sh had to be corrected into.
	// Inside the container's namespace nothing listens on a host-mapped port, so an in-container
	// probe of it cannot succeed (issue #84).
	require.Regexpf(t, `curl[^\n]*\$\{base\}/readyz`, body,
		"%s must probe ${base}/readyz with curl from the host. `dkp healthcheck` covers /healthz "+
			"only, and canonical §13 splits the two precisely because they answer different "+
			"questions — /readyz is the one that knows about migrations and about the ledger's "+
			"append-only protection (issue #99).", rel)

	// A status code alone is not evidence: when Config.Readiness is nil the route is never
	// registered and internal/ui's SPA catch-all answers /readyz with index.html and a 200.
	require.Containsf(t, body, `"state"`,
		"%s must assert the body is a readiness report, not just that something answered: the SPA "+
			"catch-all serves index.html with a 200 for every path no route claimed, /readyz "+
			"included.", rel)

	// 503 is a CORRECT answer on a first boot that is still applying migrations (state "pending",
	// internal/api/ready.go). A probe that demanded 200 would fail the release for a healthy image.
	require.Containsf(t, body, "200 | 503",
		"%s must accept 200 or 503 from /readyz: a first boot may still be applying migrations, "+
			"which handleReadyz reports as a 503 with state \"pending\"", rel)

	// The published port has to be discovered BEFORE the probe that uses it. It used to be derived
	// further down, immediately before smoke-spa.sh; a probe of an empty $base would fail the
	// release on a healthy image, which is the way this fix could go wrong.
	discovery := strings.Index(body, `port="$(docker port`)
	require.NotEqualf(t, -1, discovery, "%s must discover the published host port", rel)

	probe := regexp.MustCompile(`curl[^\n]*\$\{base\}/readyz`).FindStringIndex(body)
	require.NotNil(t, probe, "the /readyz probe must be a curl call") // already asserted above
	require.Lessf(t, discovery, probe[0],
		"%s discovers the published port AFTER probing ${base}/readyz, so the probe runs against an "+
			"empty base URL and fails on an image that is perfectly healthy", rel)
}

// TestReleaseSmoke_SupplyChainVerificationIsOptIn pins the fix for issue #107.
//
// The verification section used to be gated on `command -v`, which reads as a portable skip and is
// really a coin toss on the runner image. cosign is on no runner, so it skipped — including on the
// release path, where it is the gate. `gh` IS preinstalled on GitHub-hosted ubuntu runners, so the
// EDGE channel ran `gh attestation verify` against a digest that has no attestation (only release.yml
// writes one) with no token, and failed every push to main.
//
// Which channel signs its images is a property of the WORKFLOW, so the workflow is what says so.
func TestReleaseSmoke_SupplyChainVerificationIsOptIn(t *testing.T) {
	t.Parallel()

	const rel = "scripts/release-smoke.sh"

	script := readRepoFile(t, rel)

	require.Containsf(t, script, `[ "${VERIFY_SUPPLY_CHAIN:-0}" = "1" ]`,
		"%s must gate the supply-chain section on an explicit VERIFY_SUPPLY_CHAIN=1 that defaults "+
			"to off, not on whether a tool happens to be installed (issue #107)", rel)

	gate := strings.Index(script, "VERIFY_SUPPLY_CHAIN")
	for _, cmd := range []string{`cosign verify "${ref}"`, "gh attestation verify"} {
		at := strings.Index(script, cmd)
		require.NotEqualf(t, -1, at, "%s must still run `%s` when verification is opted into", rel, cmd)
		require.Lessf(t, gate, at,
			"%s runs `%s` before the VERIFY_SUPPLY_CHAIN gate, so an unsigned channel would run it "+
				"anyway — that is issue #107 verbatim", rel, cmd)
	}

	// Opted in, a missing tool is a FAILURE. A warn-and-continue branch here is the laundered gate:
	// the release train's only supply-chain check becomes a log line on any runner without the tool.
	require.NotContainsf(t, script, "skipping signature verification",
		"%s must not skip cosign verification when the caller asked for it — VERIFY_SUPPLY_CHAIN=1 "+
			"means verify or fail", rel)
	for _, tool := range []string{"cosign", "gh"} {
		require.Containsf(t, script, "if ! command -v "+tool,
			"%s must treat a missing %s as a failure under VERIFY_SUPPLY_CHAIN=1, not as a skip", rel, tool)
	}

	// The release channel signs and attests, so it is the channel that verifies — with a token, the
	// permission to read the attestation, and cosign actually installed.
	release := jobBlock(t, readRepoFile(t, ".github/workflows/release.yml"), "smoke:")
	for _, want := range []struct{ needle, why string }{
		{`VERIFY_SUPPLY_CHAIN: "1"`, "the release channel must opt into supply-chain verification"},
		{"GH_TOKEN:", "gh attestation verify reads the attestation through the GitHub API"},
		{"attestations: read", "without it `gh attestation verify` fails on a real tag, just later"},
		{"packages: read", "the smoke pulls the published digest from GHCR"},
		{"cosign-installer", "cosign is on no runner image, and a missing cosign now fails the job"},
	} {
		require.Containsf(t, release, want.needle,
			"release.yml's smoke job must carry %q — %s", want.needle, want.why)
	}

	// And the edge channel must NOT opt in: edge publishes no signature and no attestation, so
	// there is nothing there to verify. Comments are stripped first, exactly as QEMU001 does — the
	// job's prose explains the opt-in, and prose about a rule is not a breach of it.
	edge := jobBlock(t, readRepoFile(t, ".github/workflows/edge.yml"), "smoke:")

	var steps []string
	for _, line := range strings.Split(edge, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			steps = append(steps, line)
		}
	}

	require.NotContains(t, strings.Join(steps, "\n"), "VERIFY_SUPPLY_CHAIN",
		"edge.yml's smoke job must leave VERIFY_SUPPLY_CHAIN unset: edge images carry no cosign "+
			"signature and no release attestation, so verification there can only fail (issue #107)")
}

// TestShellGates_NoEchoTautology scans every gate script for the defect class rather than the
// instance.
//
// Not limited to smoke-local.sh on purpose. The shape is easy to write by accident when converting
// `if cmd; then` into a one-liner, it survives review because both branches read as deliberate, and
// it silently converts a gate into a printf. One grep costs nothing and covers the ones nobody has
// looked at since they were written.
func TestShellGates_NoEchoTautology(t *testing.T) {
	t.Parallel()

	// NO VACUOUS PASS. A scan that matches nothing passes every tree, and this pattern is easy to
	// get subtly wrong (see echoTautologyRe's comment — the first draft of it did). So it is first
	// run against the line issue #84 actually reported, verbatim, and against the correctly-written
	// line three lines above it in the same script, which must NOT be flagged.
	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		{
			name: "the line issue #84 reported",
			line: `readyz="$(docker exec "$name" /usr/local/bin/dkp healthcheck --url ` +
				`"${base}/healthz" >/dev/null 2>&1 && echo reachable || echo reachable)"`,
			want: true,
		},
		{
			name: "the /healthz poll in the same script, which is correct",
			line: `    --entrypoint /usr/local/bin/dkp "$ref" healthcheck >/dev/null 2>&1 && echo ok || echo no)"`,
			want: false,
		},
	} {
		m := echoTautologyRe.FindStringSubmatch(tc.line)
		got := m != nil && m[1] == m[2]
		require.Equalf(t, tc.want, got,
			"echoTautologyRe reports tautology=%v for %s, want %v. The pattern is broken, so a clean "+
				"sweep below would prove nothing:\n    %s", got, tc.name, tc.want, tc.line)
	}

	scripts, err := filepath.Glob(filepath.Join(repoRoot(t), "scripts", "*.sh"))
	require.NoError(t, err, "glob scripts/*.sh")
	require.GreaterOrEqual(t, len(scripts), 10,
		"found almost no scripts/*.sh — the glob is broken, not the tree")

	for _, path := range scripts {
		rel := filepath.ToSlash(filepath.Join("scripts", filepath.Base(path)))

		for i, line := range strings.Split(readRepoFile(t, rel), "\n") {
			// A comment line is prose, not a check. smoke-local.sh quotes the offending line
			// verbatim where it explains what replaced it, and a scan that flagged that would be a
			// gate mis-firing on the documentation of its own defect — which is how a useful gate
			// becomes one people delete. Only the leading `#` is skipped, so a tautology with a
			// trailing comment is still caught.
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}

			m := echoTautologyRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}

			require.NotEqualf(t, m[1], m[2],
				"%s:%d prints the same word on success and on failure, so the command's exit status "+
					"is discarded and the check cannot fail:\n    %s\n"+
					"Test the status instead (`if cmd; then … else … fi`), or delete the line. This is "+
					"issue #84, which reported `/readyz reachable` on every CI run for months.",
				rel, i+1, strings.TrimSpace(line))
		}
	}
}

// TestMockupsReadme_SaysWhatAssertsAgainstThem pins the correction for issue #89.
//
// The README said "no test asserts against them" of the vendored mockups. That stopped being true
// when the fidelity diff landed, and the sentence is load-bearing in both directions: the paragraph
// after it is the refresh procedure, so a reader refreshing nocturne/styles.css on the strength of
// it expects a docs-only change and gets a red test/repo run whose message talks about sanctioned
// divergences. It also hides the converse — that a mockup which fails accessibility is fixed by
// diverging the SHIPPED sheet and documenting it, precisely because the vendored file is byte-exact.
func TestMockupsReadme_SaysWhatAssertsAgainstThem(t *testing.T) {
	t.Parallel()

	const rel = "docs/design/mockups/README.md"

	readme := readRepoFile(t, rel)

	require.NotContainsf(t, readme, "no test asserts against them",
		"%s claims nothing asserts against the vendored mockups. design_tokens_test.go reads "+
			"%s and diffs the shipped .table against it (issue #89).", rel, mockupSheetRel)

	require.Containsf(t, readme, "design_tokens_test.go",
		"%s must name the test that asserts against these files, so a refresh knows what it will "+
			"have to clear", rel)
}
