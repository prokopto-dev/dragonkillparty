// Tests for the `security / osv` supply-chain gate (issue #132).
//
// osv-scanner closes one specific hole: the ~275 package npm dependency graph in web/pnpm-lock.yaml
// had NO vulnerability scanning at all. `security / licences` reads that graph for LICENCES, and
// `security / govulncheck` reads the GO graph for vulnerabilities — nothing read the npm graph for
// vulnerabilities, and the first scan after this gate landed found three (#133, #134, #135).
//
// Three things can quietly undo that, and there is a test here for each:
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
//
// The always-on and blocking properties live in ci_required_test.go alongside the other two
// supply-chain jobs, because they are the same three assertions for the same reason.
package repo_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
			"scripts/licence-gate.sh: LIC002 fails CLOSED on a licence it cannot identify, and the "+
			"allowlist is closed rather than a denylist.")
}

// ignoredVulnBlocks splits osv-scanner.toml into its `[[IgnoredVulns]]` blocks.
//
// Hand-parsed rather than with a TOML library, for the reason ci_required_test.go gives about YAML:
// the file's shape is this regular, and adding a dependency for a test needs a human to approve it.
func ignoredVulnBlocks(t *testing.T) []string {
	t.Helper()

	text := readRepoFile(t, "osv-scanner.toml")

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

	idRe := regexp.MustCompile(`(?m)^\s*id\s*=\s*"([^"]+)"`)
	reasonRe := regexp.MustCompile(`(?m)^\s*reason\s*=\s*"([^"]*)"`)
	untilRe := regexp.MustCompile(`(?m)^\s*ignoreUntil\s*=\s*(\d{4}-\d{2}-\d{2})`)
	issueRe := regexp.MustCompile(`#\d+`)

	for _, block := range blocks {
		id := idRe.FindStringSubmatch(block)
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

	require.NotEmpty(t, ignoredVulnBlocks(t),
		"osv-scanner.toml carries no [[IgnoredVulns]] entries. That is a GOOD state — it means "+
			"every finding has been fixed rather than waived. Delete this assertion when it "+
			"happens; it exists only so the parser above cannot silently stop matching and take "+
			"TestOSVConfig_EveryIgnore_HasReasonIssueAndExpiry vacuous with it.")
}
