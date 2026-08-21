// Tests for the `security / osv` supply-chain gate (issue #132).
//
// osv-scanner closes one specific hole: the ~275 package npm dependency graph in web/pnpm-lock.yaml
// had NO vulnerability scanning at all. `security / licences` reads that graph for LICENCES, and
// `security / govulncheck` reads the GO graph for vulnerabilities — nothing read the npm graph for
// vulnerabilities, and the first scan after this gate landed found three (#133, #134, #135).
//
// Four things can quietly undo that, and there is a test here for each:
//
//  1. The job stops scanning one of the two lockfiles. A scan target is a list, and a list is the
//     easiest thing in a diff to shorten by one line. `make osv-scan` and ci.yml each carry a copy,
//     so the copies are compared to each other rather than each to a hardcoded expectation.
//  2. osv-scanner is treated as a REPLACEMENT for govulncheck. It is not: govulncheck is
//     reachability-aware and osv-scanner is not, so dropping govulncheck would trade call-graph
//     analysis for a noisier feed and call it consolidation.
//  3. osv-scanner.toml becomes the gate's off switch. It is a required blocking job, so every
//     ignored id is a deliberate hole; the rules that keep it from being a silent one are enforced
//     here rather than trusted.
//  4. A waiver's PREMISE stops being true. The rules in (3) constrain a waiver's shape and cannot
//     read its reason. GO-2026-5932's reason is one checkable fact — nothing here imports
//     golang.org/x/crypto/openpgp — and osv-scanner matches at module granularity, so it would keep
//     filtering that advisory on the day the fact changed. So the fact is checked (#280).
//
// The always-on and blocking properties live in ci_required_test.go alongside the other two
// supply-chain jobs, because they are the same three assertions for the same reason.
package repo_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/licence"
)

// The two lockfiles the gate exists to cover. go.mod is the graph govulncheck already reads
// (osv-scanner adds unreachable findings there, which is the lower-value half); web/pnpm-lock.yaml
// is the one nothing read at all, and is the entire point of issue #132.
var osvRequiredLockfiles = []string{"go.mod", "web/pnpm-lock.yaml"}

// No `[[IgnoredVulns]]` entry may be dated beyond this year.
//
// A crude bound, deliberately: it does not make a waiver reasonable, it only stops one being
// effectively permanent. `ignoreUntil = 2099-01-01` satisfies "has an expiry" while meaning
// "forever", and the difference is invisible in a diff. Bumping this constant is meant to be a
// prompt to re-read the whole waiver file, so it rotting is the feature.
const maxIgnoreUntilYear = 2030

// readRepoFile lives in release_gates_test.go; this file is in the same package and uses it.

// osvScanArgsFromCI returns the lines of the `scan-args:` block on the security-osv job.
//
// Parsed by indentation for ci_required_test.go's reason: gopkg.in/yaml.v3 is an indirect
// dependency, and promoting it to a direct one to read a file this regular would mean adding a
// dependency for a test, which AGENTS.md requires a human to approve.
func osvScanArgsFromCI(t *testing.T) []string {
	t.Helper()

	workflow := readCIWorkflow(t)

	marker := "scan-args: |-"
	at := strings.Index(workflow, marker)
	require.NotEqual(t, -1, at,
		"ci.yml has no `scan-args: |-` block. The security / osv job must name its scan targets "+
			"explicitly; the action's default is `--recursive ./`, which scans whatever it happens "+
			"to walk into and reports a lockfile that moved as clean rather than as unscanned.")

	lines := strings.Split(workflow[at:], "\n")[1:]

	// The block's own indentation is set by its first line; it ends at the first line indented less.
	indent := len(lines[0]) - len(strings.TrimLeft(lines[0], " "))
	require.Positive(t, indent, "the scan-args block is not indented — has ci.yml's formatting changed?")

	var args []string

	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}

		if len(l)-len(strings.TrimLeft(l, " ")) < indent {
			break
		}

		args = append(args, strings.TrimSpace(l))
	}

	require.NotEmpty(t, args, "ci.yml's scan-args block parsed as empty")

	return args
}

// makefileTargetRecipe returns the recipe lines of one Makefile target.
func makefileTargetRecipe(t *testing.T, target string) string {
	t.Helper()

	makefile := readRepoFile(t, "Makefile")

	at := strings.Index(makefile, "\n"+target+":\n")
	require.NotEqualf(t, -1, at, "the Makefile has no %s target", target)

	rest := makefile[at+1:]

	var recipe []string

	for i, l := range strings.Split(rest, "\n") {
		if i == 0 {
			continue // the target line itself
		}

		if l != "" && !strings.HasPrefix(l, "\t") {
			break
		}

		recipe = append(recipe, l)
	}

	return strings.Join(recipe, "\n")
}

// osvIgnoreIDRe pulls the `id` out of one ignore block. One copy, shared by every test here that
// has to know which advisories are waived.
var osvIgnoreIDRe = regexp.MustCompile(`(?m)^\s*id\s*=\s*"([^"]+)"`)

// lockfileArgs pulls every `--lockfile=<path>` out of a blob of text.
var lockfileArgRe = regexp.MustCompile(`--lockfile=(\S+)`)

func lockfileArgs(text string) []string {
	var out []string
	for _, m := range lockfileArgRe.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.Trim(m[1], `"'`))
	}

	return out
}

// TestOSV_ScanTargets_CoverBothDependencyGraphs is issue #132's acceptance criterion, stated twice
// because the gate is declared twice.
//
// "Run osv-scanner over go.mod/go.sum AND web/pnpm-lock.yaml" is only true while both paths are
// actually named. A recursive scan would satisfy the sentence and not the requirement: it covers
// whatever it walks into, so a lockfile that moves reads as clean rather than as unscanned.
func TestOSV_ScanTargets_CoverBothDependencyGraphs(t *testing.T) {
	t.Parallel()

	ci := strings.Join(osvScanArgsFromCI(t), "\n")
	mk := makefileTargetRecipe(t, "osv-scan")

	for _, site := range []struct{ name, text string }{
		{"ci.yml security-osv", ci},
		{"make osv-scan", mk},
	} {
		t.Run(site.name, func(t *testing.T) {
			t.Parallel()

			got := lockfileArgs(site.text)

			for _, want := range osvRequiredLockfiles {
				require.Containsf(t, got, want,
					"%s does not scan %q. Issue #132 exists BECAUSE the npm graph had no "+
						"vulnerability scanning; dropping either lockfile from the target list "+
						"reopens exactly that hole, and nothing else in CI would notice.\nScanned: "+
						"%v\nIn:\n%s", site.name, want, got, site.text)
			}

			require.Containsf(t, site.text, "--config=osv-scanner.toml",
				"%s does not pass --config=osv-scanner.toml. osv-scanner otherwise looks for a "+
					"config beside each scanned lockfile, which would mean a second waiver file "+
					"under web/ — and two waiver files is how one of them stops being read.\n%s",
				site.name, site.text)
		})
	}
}

// TestOSV_ScanTargets_AreTheSameInCIAndTheMakefile keeps the two copies of the target list in step.
//
// A lockfile added to one and not the other gives a laptop and CI different answers about the same
// tree, and the direction that hurts is the silent one: `make osv-scan` clean, CI red, or worse the
// reverse. Same reasoning as setup-toolchain reading its tool pins out of the Makefile rather than
// repeating them.
func TestOSV_ScanTargets_AreTheSameInCIAndTheMakefile(t *testing.T) {
	t.Parallel()

	ci := lockfileArgs(strings.Join(osvScanArgsFromCI(t), "\n"))
	mk := lockfileArgs(makefileTargetRecipe(t, "osv-scan"))

	require.ElementsMatch(t, ci, mk,
		"ci.yml's security-osv job and `make osv-scan` scan different lockfiles.\n"+
			"  ci.yml:   %v\n  Makefile: %v\n"+
			"Whichever is shorter is the one covering less than the project thinks it does.", ci, mk)
}

// TestOSV_ActionPin_MatchesTheMakefilePin — one pin, two places, and they must agree.
//
// The Makefile's OSV_SCANNER_VERSION is what `make osv-scan` tells a contributor to install; the
// action's trailing version comment is what CI actually runs. A laptop and CI disagreeing about a
// scanner version is how "it was clean locally" becomes a merge argument instead of a bug report.
//
// PIN001 in scripts/repo-gates.sh already asserts the SHAPE of every action pin (a 40-hex SHA). It
// cannot assert that a digest is the commit its comment claims — the ci.yml header says so — and
// this test does not either. It asserts only that the two version NUMBERS in this repository agree.
func TestOSV_ActionPin_MatchesTheMakefilePin(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	pinRe := regexp.MustCompile(`uses: google/osv-scanner-action/[^@]+@([0-9a-f]{40}) # (v[0-9]+\.[0-9]+\.[0-9]+)`)

	m := pinRe.FindStringSubmatch(workflow)
	require.NotNil(t, m,
		"ci.yml has no SHA-pinned google/osv-scanner-action step with a `# vX.Y.Z` trailing "+
			"comment. Issue #132 asked for the action to be pinned by SHA like the rest, and "+
			"PIN001 requires the shape.")

	makefile := readRepoFile(t, "Makefile")

	mkRe := regexp.MustCompile(`(?m)^OSV_SCANNER_VERSION\s+\?=\s+(v[0-9]+\.[0-9]+\.[0-9]+)`)
	mk := mkRe.FindStringSubmatch(makefile)
	require.NotNil(t, mk,
		"the Makefile has no OSV_SCANNER_VERSION pin in the `NAME ?= value` shape the other pins "+
			"use. `make osv-scan` names it when the binary is missing, so it is the version a "+
			"contributor installs.")

	require.Equalf(t, mk[1], m[2],
		"the osv-scanner version CI runs (%s, from the action pin) and the one the Makefile tells "+
			"a contributor to install (%s) disagree. Bump both, or a local scan and CI's scan are "+
			"different scanners.", m[2], mk[1])
}

// TestOSV_IsNotAReplacementForGovulncheck — the anti-consolidation assertion.
//
// Issue #132 says it in as many words: "keep govulncheck for Go — it is reachability-aware,
// osv-scanner is not (so osv is noisier for Go and shouldn't replace it)". Two jobs whose names both
// read as "dependency vulnerabilities" is exactly the pair a later tidying pass merges, and the
// coverage lost would be the call-graph analysis rather than the noise.
func TestOSV_IsNotAReplacementForGovulncheck(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	require.Contains(t, ciJobIDs(t, workflow), "security-govulncheck",
		"the security-govulncheck job is gone. osv-scanner does NOT replace it: govulncheck does "+
			"call-graph analysis and reports only REACHABLE vulnerabilities, which osv-scanner "+
			"cannot do at all. Issue #132 required it be kept.")

	require.Contains(t, workflow, "run: make govulncheck",
		"ci.yml no longer runs `make govulncheck`")

	require.Contains(t, readRepoFile(t, "Makefile"), "\ngovulncheck:\n",
		"the govulncheck Makefile target is gone")

	// And the licence gate, which reads the same two graphs for a different property. osv-scanner
	// has a --licenses mode; adopting it to retire LIC001/LIC002 would drop the closed allowlist,
	// the fail-closed posture on an unidentifiable licence, and the AGPL firewall this project's
	// Apache-2.0 licence depends on.
	require.Contains(t, ciJobIDs(t, workflow), "security-licences",
		"the security-licences job is gone. osv-scanner's --licenses mode is not a substitute for "+
			"internal/licence: LIC002 fails CLOSED on a licence it cannot identify, and the "+
			"allowlist is closed rather than a denylist.")
}

// ignoredVulnBlocks splits osv-scanner.toml into its `[[IgnoredVulns]]` blocks.
//
// Hand-parsed rather than with a TOML library, for the reason ci_required_test.go gives about YAML:
// the file's shape is this regular, and adding a dependency for a test needs a human to approve it.
func ignoredVulnBlocks(t *testing.T) []string {
	t.Helper()

	return splitIgnoredVulnBlocks(readRepoFile(t, "osv-scanner.toml"))
}

// splitIgnoredVulnBlocks is the parsing half of ignoredVulnBlocks, taking the text rather than
// reading it, so TestOSVConfig_BlockParser_StillMatches can exercise it on input that is not
// whatever osv-scanner.toml happens to contain today.
func splitIgnoredVulnBlocks(text string) []string {
	parts := strings.Split(text, "[[IgnoredVulns]]")
	if len(parts) == 1 {
		return nil
	}

	return parts[1:]
}

// TestOSVConfig_EveryIgnore_HasReasonIssueAndExpiry is what stops osv-scanner.toml from being the
// gate's off switch.
//
// `security / osv` is required and blocking, so the cheapest way to make it green is to add an id
// here — which AGENTS.md forbids in general terms ("do not disable a lint rule, a hook, or a CI gate
// to land a change") and which nothing mechanical would otherwise catch. The three rules below are
// the same shape as web/e2e/axe-allowlist.json's: an entry with no issue behind it fails the build.
//
//   - `reason` names a filed issue (#NNN), so the waiver is tracked somewhere that gets read again.
//   - `ignoreUntil` is set, so the waiver expires and the job goes red on its own schedule rather
//     than when somebody remembers.
//   - `ignoreUntil` is not effectively permanent.
//
// Deliberately NOT asserted: that the date is still in the future. That would need the wall clock —
// which CLOCK001 bans outside internal/clock — and would turn `make test` red on the expiry date,
// which is the wrong build to break. Expiry is osv-scanner's job to enforce, and it does: past that
// date the ignore stops applying and `security / osv` goes red, which is exactly the intended alarm.
func TestOSVConfig_EveryIgnore_HasReasonIssueAndExpiry(t *testing.T) {
	t.Parallel()

	blocks := ignoredVulnBlocks(t)

	reasonRe := regexp.MustCompile(`(?m)^\s*reason\s*=\s*"([^"]*)"`)
	untilRe := regexp.MustCompile(`(?m)^\s*ignoreUntil\s*=\s*(\d{4}-\d{2}-\d{2})`)
	issueRe := regexp.MustCompile(`#\d+`)

	for _, block := range blocks {
		id := osvIgnoreIDRe.FindStringSubmatch(block)
		require.NotNilf(t, id, "an [[IgnoredVulns]] block has no `id`:\n%s", block)

		t.Run(id[1], func(t *testing.T) {
			t.Parallel()

			reason := reasonRe.FindStringSubmatch(block)
			require.NotNilf(t, reason,
				"the waiver for %s has no `reason`. A blocking gate's exception has to say why it "+
					"exists, or the next reader can only guess whether it is still true.", id[1])

			require.Regexpf(t, issueRe, reason[1],
				"the waiver for %s does not reference a filed issue (#NNN). A waiver nobody tracks "+
					"is a decision nobody revisits — the same rule web/e2e/axe-allowlist.json lives "+
					"under. File one with `gh issue create` and name it here.\nReason: %s",
				id[1], reason[1])

			until := untilRe.FindStringSubmatch(block)
			require.NotNilf(t, until,
				"the waiver for %s has no `ignoreUntil` date. An open-ended ignore is a deletion "+
					"with extra steps: it removes the finding permanently while looking temporary. "+
					"Set a date (unquoted TOML local date, e.g. `ignoreUntil = 2026-11-09`).", id[1])

			parsed, err := time.Parse(time.DateOnly, until[1])
			require.NoErrorf(t, err, "the waiver for %s has an unparseable ignoreUntil %q", id[1], until[1])

			require.LessOrEqualf(t, parsed.Year(), maxIgnoreUntilYear,
				"the waiver for %s expires in %d, which is far enough out to be permanent. An "+
					"expiry that never arrives satisfies the letter of the rule and none of its "+
					"point. If the fix genuinely cannot happen sooner, say so in the issue and "+
					"raise maxIgnoreUntilYear deliberately — that edit is meant to prompt a re-read "+
					"of the whole waiver file.", id[1], parsed.Year())
		})
	}
}

// TestOSVConfig_WaiverSet_MatchesTheOwnedGoldenFile puts a code-owner review in front of every new
// waiver.
//
// The two controls above constrain the SHAPE of a waiver — it must name an issue, it must expire —
// but neither requires a human to agree the reason is a good one, and `security / osv` is a
// required blocking job. So the cheapest way to make that job green is still to add an id to
// osv-scanner.toml, which is the shape AGENTS.md forbids: "do not disable a lint rule, a hook, or a
// CI gate to land a change."
//
// osv-scanner.toml itself is not CODEOWNERS-protected. test/golden/ IS
// (`/test/golden/ @prokopto-dev`), and AGENTS.md separately forbids rewriting anything under it to
// go green — so mirroring the waiver set there and asserting the two agree means a new ignore cannot
// land without an edit that requests a code owner's review.
//
// Second-best and knowingly so: owning osv-scanner.toml directly would be more direct, and that
// one-line CODEOWNERS change is staged for the maintainer. This is the version implementable with
// the ownership surface that already exists, so the gate is not left unguarded in the meantime. Keep
// both if the CODEOWNERS entry lands — two independent locks on a waiver list is defence in depth.
func TestOSVConfig_WaiverSet_MatchesTheOwnedGoldenFile(t *testing.T) {
	t.Parallel()

	configured := make(map[string]bool)

	for _, block := range ignoredVulnBlocks(t) {
		if m := osvIgnoreIDRe.FindStringSubmatch(block); m != nil {
			configured[m[1]] = true
		}
	}

	golden := make(map[string]bool)

	for _, line := range strings.Split(readRepoFile(t, "test/golden/supply-chain/osv-waivers.txt"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		golden[line] = true
	}

	const remedy = "\n\nAdding a waiver requires editing BOTH osv-scanner.toml and " +
		"test/golden/supply-chain/osv-waivers.txt. That second file is CODEOWNERS-protected, which " +
		"is the point: a hole cut in a required supply-chain gate should need a code owner to agree " +
		"the reason is a good one. Do not resolve this by deleting the assertion."

	for id := range configured {
		require.Truef(t, golden[id],
			"osv-scanner.toml waives %s, but test/golden/supply-chain/osv-waivers.txt does not list "+
				"it — so a blocking gate was weakened without the owned file changing.%s", id, remedy)
	}

	for id := range golden {
		require.Truef(t, configured[id],
			"test/golden/supply-chain/osv-waivers.txt lists %s, but osv-scanner.toml no longer "+
				"waives it. If the advisory was fixed, delete the id from the golden file too; the "+
				"two are meant to be identical.%s", id, remedy)
	}
}

// TestOSVConfig_IsReferencedByBothCallSites guards the file itself from becoming decoration.
//
// A waiver file nothing passes `--config` to is read by nobody: osv-scanner would look beside each
// lockfile instead, find nothing, and report the waived findings — or, worse, somebody would "fix"
// that by copying the file under web/ and the two would drift.
func TestOSVConfig_IsReferencedByBothCallSites(t *testing.T) {
	t.Parallel()

	require.FileExists(t, filepath.Join(repoRoot(t), "osv-scanner.toml"),
		"osv-scanner.toml is gone, but ci.yml and the Makefile pass --config=osv-scanner.toml. "+
			"osv-scanner fails on a config path that does not exist.")
}

// TestOSVConfig_BlockParser_StillMatches keeps TestOSVConfig_EveryIgnore_HasReasonIssueAndExpiry
// from going silently vacuous.
//
// This assertion used to live in TestOSVConfig_IsReferencedByBothCallSites as a `NotEmpty` over the
// real file, with a comment saying to delete it once every finding was fixed rather than waived.
// That happened: #133, #134 and #135 were all cleared by a dependency bump, so osv-scanner.toml now
// carries no [[IgnoredVulns]] blocks and a `NotEmpty` over it can only fail.
//
// Deleting it outright would drop the property it was actually protecting — that splitIgnoredVulnBlocks
// still recognises a block when there IS one. With an empty config, a parser that had stopped
// matching entirely would look exactly like a clean waiver list, and every per-block rule
// (issue reference, expiry, the owned-golden cross-check) would pass over zero blocks. So the check
// moves off the real file and onto fixed input, where it holds whether or not a waiver exists today
// and is strictly harder to satisfy by accident than the `NotEmpty` it replaces.
func TestOSVConfig_BlockParser_StillMatches(t *testing.T) {
	t.Parallel()

	const oneBlock = `
[[IgnoredVulns]]
id = "GHSA-aaaa-bbbb-cccc"
ignoreUntil = 2026-11-09
reason = "Issue #1. Because."
`

	tests := []struct {
		name string
		text string
		want int
	}{
		{"no blocks", "# a config with only comments\n", 0},
		{"one block", "# header\n" + oneBlock, 1},
		{"three blocks", "# header\n" + oneBlock + oneBlock + oneBlock, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Lenf(t, splitIgnoredVulnBlocks(tc.text), tc.want,
				"splitIgnoredVulnBlocks no longer finds [[IgnoredVulns]] blocks it should. Every "+
					"per-waiver rule in this file iterates over its output, so a parser that stops "+
					"matching turns all of them into assertions over an empty slice — a green build "+
					"that checks nothing.\nInput:\n%s", tc.text)
		})
	}
}

// ---------------------------------------------------------------------------------------------
// The GO-2026-5932 waiver's premise, checked rather than asserted (#280).
//
// Every rule above constrains the SHAPE of a waiver — it names an issue, it expires, a code owner
// agreed to it. None of them can tell whether the REASON is still true, and GO-2026-5932's reason
// rests on a single checkable fact: nothing in this module imports golang.org/x/crypto/openpgp.
// The module is required for argon2id (03-security.md §3.1); the packages the advisory is actually
// about are in no build graph here.
//
// Nothing checked that fact, and osv-scanner cannot: it matches at MODULE granularity, so on the
// day something here did import openpgp it would go on filtering the finding exactly as before.
// The waiver would hold a required gate green while the reason printed in its own filter line had
// quietly become false — the failure mode this file exists to prevent, arriving through the one
// door left open. It is checkable in a tenth of a second, offline, so it is checked.
//
// This also retires the manual half of the expiry. osv-scanner.toml says one year forces a re-check
// that "we still do not import openpgp"; that re-check now runs on every `make test`, and what the
// date forces a human to do is re-read the DECISION rather than re-run a grep.
// ---------------------------------------------------------------------------------------------

const (
	// The advisory osv-scanner.toml waives, and the import path it is about. The affected block
	// lists only subpackages of that path — openpgp itself plus packet, armor, clearsign, errors,
	// elgamal and s2k — all of which this prefix covers.
	openPGPWaiverID   = "GO-2026-5932"
	openPGPImportPath = "golang.org/x/crypto/openpgp"
)

// osvWaivesID reports whether osv-scanner.toml carries an ignore for one advisory id.
func osvWaivesID(t *testing.T, id string) bool {
	t.Helper()

	for _, block := range ignoredVulnBlocks(t) {
		if m := osvIgnoreIDRe.FindStringSubmatch(block); m != nil && m[1] == id {
			return true
		}
	}

	return false
}

// platformGraph pairs one release platform with the package listing `go list` resolved for it.
type platformGraph struct {
	platform licence.Platform
	listing  string
}

// releaseBuildGraphs returns one `go list -deps -test ./...` listing per platform the release ships.
//
// EVERY RELEASE TARGET, NOT THE HOST. `go list` resolves build constraints for one GOOS/GOARCH at a
// time, so an import behind `//go:build windows` is invisible to a query run on the linux CI runner
// — while osv-scanner's module-level ignore would go on suppressing GO-2026-5932 for the Windows
// binary that shipped it. A host-only check is green in exactly the case the waiver is most wrong.
// The licence gate met this first and its comment on gatePlatforms carries the measurement: a
// linux-only query resolves 11 modules where the union resolves 14, so "a GPL dependency behind
// `//go:build windows` would ship unnoticed".
//
// The platform list is internal/licence's, which owns the ONE enumeration of it — issue #130
// consolidated two shell copies that had already drifted, and a third copy here would be that bug
// again. TestLicencePlatforms_CoverTheGoreleaserBuildMatrix ties it to what actually ships.
//
// It is the FULL release matrix rather than licence.GatePlatforms(). That subset is one GOARCH per
// GOOS, on the measured ground that a build constraint which adds a MODULE is GOOS-gated, and
// TestLicenceGate_PlatformSubset_CoversTheFullMatrix holds it to that at module granularity. This
// test asks a PACKAGE-level question, where an import behind `//go:build amd64` needs no new module
// and that proof does not carry.
//
// `-test` is deliberate too, and is the stronger reading of the waiver's own words: the reason says
// no openpgp package appears in any build graph here, and a test-only import is still one, which
// osv-scanner would keep filtering just the same.
func releaseBuildGraphs(t *testing.T) []platformGraph {
	t.Helper()

	platforms := licence.ReleasePlatforms()
	if testing.Short() {
		// The inner loop keeps the host platform only: `make test-unit`'s budget is under five
		// seconds and seven graph resolutions do not fit in it. `make test` — what `make check`
		// runs and what CI gates on — resolves the whole matrix, so the cross-platform hole above
		// is closed by the gate rather than by the fast lane.
		platforms = []licence.Platform{{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	}

	root := repoRoot(t)

	type result struct {
		listing string
		stderr  string
		err     error
	}

	var (
		wg      sync.WaitGroup
		results = make([]result, len(platforms))
	)

	// Concurrently, for licence.RuntimeModules's reason: the queries are independent, and serially
	// this is the kind of thing that becomes the slowest part of `make check`.
	for i, p := range platforms {
		wg.Add(1)

		go func() {
			defer wg.Done()

			var stdout, stderr bytes.Buffer

			cmd := exec.Command("go", "list", "-deps", "-test", "./...")
			cmd.Dir = root
			// GOWORK=off and GOFLAGS= are load-bearing for licence.RuntimeModules's reasons: a
			// go.work file or a GOFLAGS carrying -tags or -mod=vendor changes which packages
			// resolve, and a developer's environment must not decide which graph a supply-chain
			// claim is checked against.
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOOS="+p.GOOS, "GOARCH="+p.GOARCH)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			results[i] = result{listing: stdout.String(), stderr: stderr.String(), err: err}
		}()
	}

	wg.Wait()

	graphs := make([]platformGraph, 0, len(platforms))

	for i, p := range platforms {
		r := results[i]

		// A hard failure rather than `-e`: a partial listing is exactly the vacuity this test
		// exists to rule out. If a package here is ever legitimately buildable on only some
		// platforms, the answer is `-e` plus a check that the union still resolved — not a
		// narrower platform list, which would put the hole back.
		require.NoErrorf(t, r.err,
			"go list -deps -test ./... could not resolve the graph for %s\n%s", p, r.stderr)

		// A graph without argon2 in it is not the graph this test means to read — an empty or
		// truncated listing would otherwise pass the assertion below for the wrong reason, and
		// `go list` EXITS ZERO when it matches no packages, warning only on stderr. That is the
		// same vacuity TestOSVConfig_BlockParser_StillMatches guards against upstream of it.
		require.Containsf(t, r.listing, "golang.org/x/crypto/argon2",
			"`go list -deps -test ./...` resolved a graph for %s with no golang.org/x/crypto/argon2 "+
				"in it, so this test is not reading what it thinks it is. argon2id is why x/crypto is "+
				"required at all (03-security.md §3.1); if it has genuinely gone, the %s waiver goes "+
				"with it.\nstderr: %s", p, openPGPWaiverID, r.stderr)

		graphs = append(graphs, platformGraph{platform: p, listing: r.listing})
	}

	return graphs
}

// openPGPPackages returns the packages in `go list -deps` output that GO-2026-5932 is about.
//
// Split from the exec half for TestOSVWaiver_OpenPGPMatcher_StillMatches's sake: a matcher that had
// stopped matching would be indistinguishable from a graph with no openpgp in it, and would report
// the same green.
//
// A `vendor/` prefix marks the STDLIB's vendored copies — crypto/tls reaches
// vendor/golang.org/x/crypto/chacha20poly1305 that way. Those are the toolchain's own tree, not
// entries in the module graph osv-scanner reads out of go.mod, so they are excluded rather than
// matched: the question here is what THIS module requires.
func openPGPPackages(listing string) []string {
	var found []string

	for _, line := range strings.Split(listing, "\n") {
		pkg := strings.TrimSpace(line)
		if pkg == "" || strings.HasPrefix(pkg, "vendor/") {
			continue
		}

		if pkg == openPGPImportPath || strings.HasPrefix(pkg, openPGPImportPath+"/") {
			found = append(found, pkg)
		}
	}

	return found
}

// TestOSVWaiver_OpenPGP_IsInNoBuildGraph is the GO-2026-5932 waiver's reason, re-derived.
//
// Two assertions, in this order, because the second only means anything while the first holds:
//
//  1. The waiver still exists. If it has been cleared — x/crypto left the graph, or osv-scanner's
//     Go call analysis was enabled and the finding stopped needing one (#280) — this test is a rule
//     with nothing behind it, and says so rather than standing as an unexplained ban on a package
//     somebody may one day have a reason to import.
//  2. No openpgp package is in the build graph of any platform the release ships. This is the fact
//     the waiver rests on, and the only one that can make it wrong. Every release target is
//     queried rather than the host alone, because a build-constrained import is invisible to the
//     one and shipped by the other — see releaseBuildGraphs.
func TestOSVWaiver_OpenPGP_IsInNoBuildGraph(t *testing.T) {
	t.Parallel()

	require.Truef(t, osvWaivesID(t, openPGPWaiverID),
		"osv-scanner.toml no longer waives %s, so this test guards the premise of a waiver that is "+
			"gone. That is a good thing to have happened — delete this test, its helpers and its "+
			"fixture alongside the waiver and the id in test/golden/supply-chain/osv-waivers.txt, "+
			"and close #280 saying which of its three exits was taken. Do not leave it here as a "+
			"standing ban on %s that no longer has a reason attached to it.",
		openPGPWaiverID, openPGPImportPath)

	var offenders []string

	for _, g := range releaseBuildGraphs(t) {
		for _, pkg := range openPGPPackages(g.listing) {
			offenders = append(offenders, g.platform.String()+": "+pkg)
		}
	}

	require.Emptyf(t, offenders,
		"%s is now in a shipped build graph, and osv-scanner.toml waives %s on the explicit ground "+
			"that it is not:\n\n  %s\n\n"+
			"osv-scanner matches at MODULE granularity, so it will keep filtering that advisory and "+
			"`security / osv` will stay green — the waiver is now hiding a finding about code this "+
			"repository really does build. If the platforms above are not the one you develop on, "+
			"that is the point: an import behind a build constraint ships to those targets while "+
			"every host-only check stays green. The advisory has no fixed version, because the "+
			"package is permanently deprecated rather than patchable, so upgrading is not an "+
			"exit.\n\n"+
			"Remove the import, or take the decision deliberately: delete the waiver from "+
			"osv-scanner.toml AND the id from test/golden/supply-chain/osv-waivers.txt, let the "+
			"gate go red, and say on #280 why an unmaintained OpenPGP implementation is the right "+
			"dependency. Do not widen the waiver to cover it.",
		openPGPImportPath, openPGPWaiverID, strings.Join(offenders, "\n  "))
}

// TestOSVWaiver_OpenPGPMatcher_StillMatches keeps the test above from going silently vacuous, the
// same way TestOSVConfig_BlockParser_StillMatches does for the per-waiver rules.
//
// Its assertion is an emptiness check over a matcher's output, which is the shape that passes
// whether the matcher is correct or broken. So the matcher is exercised on fixed input, where the
// cases that must fire and the cases that must not are both visible.
func TestOSVWaiver_OpenPGPMatcher_StillMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		listing string
		want    []string
	}{
		{
			// The graph as it actually stands: x/crypto is present, openpgp is not.
			name:    "argon2 and blake2b are not openpgp",
			listing: "golang.org/x/crypto/blake2b\ngolang.org/x/crypto/argon2\n",
			want:    nil,
		},
		{
			name:    "the advisory's own package",
			listing: "golang.org/x/crypto/argon2\ngolang.org/x/crypto/openpgp\n",
			want:    []string{"golang.org/x/crypto/openpgp"},
		},
		{
			// The affected block lists the subpackages individually, and an import of any one of
			// them is an import of the advisory's surface.
			name:    "a subpackage of it",
			listing: "golang.org/x/crypto/openpgp/packet\ngolang.org/x/crypto/openpgp/armor\n",
			want:    []string{"golang.org/x/crypto/openpgp/packet", "golang.org/x/crypto/openpgp/armor"},
		},
		{
			// The stdlib's vendored copies are the toolchain's, not go.mod's.
			name:    "the stdlib's vendored tree is not this module's graph",
			listing: "vendor/golang.org/x/crypto/openpgp\nvendor/golang.org/x/crypto/chacha20poly1305\n",
			want:    nil,
		},
		{
			// A different OpenPGP implementation is a different module with its own advisories;
			// waiving GO-2026-5932 says nothing about it either way.
			name:    "another library's openpgp is another advisory",
			listing: "github.com/ProtonMail/go-crypto/openpgp\ngolang.org/x/crypto/openpgpx\n",
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equalf(t, tc.want, openPGPPackages(tc.listing),
				"openPGPPackages no longer classifies this listing correctly. "+
					"TestOSVWaiver_OpenPGP_IsInNoBuildGraph asserts its output is EMPTY, so a "+
					"matcher that has stopped matching reads exactly like a clean graph — a green "+
					"build that checks nothing.\nInput:\n%s", tc.listing)
		})
	}
}
