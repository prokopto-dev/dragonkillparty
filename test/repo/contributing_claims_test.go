// The CONTRIBUTING claims gate, and the defect it exists for.
//
// CONTRIBUTING.md states its rules in the house "**Enforced by:**" form, which is a promise that a
// machine — not a reviewer — is checking. Four of those promises named a file that has never existed
// in this tree (#139): `lefthook.yml` installing a `prepare-commit-msg` hook so the DCO trailer
// appears by itself, `pr-title-lint.yml` linting the PR title, a CI analyser diffing every
// `**/*_test.go` against `main`, and `ci-budget.yml` measuring the last 200 runs weekly.
//
// The cost is not the cost of an ordinary stale sentence, which is why this is a gate rather than a
// review note. `ADR001`'s header in scripts/repo-gates.sh says it about the same class of defect and
// the reasoning transfers exactly: an agent reading the claim concludes the gate will catch it, and a
// reviewer reading the same line concludes CI already asked. Neither was true. The DCO row is the one
// that bites hardest — a first-time contributor who believes it pushes ten commits, gets a red
// required check, and rebases the lot, having done exactly what this file told them.
//
// What is checkable here is the NAME. A promise citing a workflow, a CI job or a path is a promise
// about a file, and a file either exists or does not; whether the job inside it does what the
// sentence says is a review question and stays one. So this gate resolves every name an "Enforced
// by:" block cites, which is what stops the next four claims drifting — the same move
// TestCI_LintRepoJob_FetchesFullHistory makes for a job's configuration, and the same move
// canonical_repo_test.go makes for a documented URL.
package repo_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// enforcedByMarker is the house promise form. Prose that merely describes a mechanism is not a
// promise and is not policed here; this exact bolded lead-in is.
const enforcedByMarker = "**Enforced by:**"

// backtickedRe matches one inline-code span. Everything this gate resolves is cited in backticks,
// which is also what keeps it away from prose: an unquoted "ci.yml" in a sentence is a mention, a
// backticked one is a citation.
var backtickedRe = regexp.MustCompile("`([^`\n]+)`")

// yamlFileRe matches a cited YAML file, as a bare name (`ci.yml`) or a path (`.github/labels.yml`).
var yamlFileRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+\.ya?ml$`)

// ciJobNameRe matches a cited CI job as CONTRIBUTING writes it — `lint / repo`, `docs / build`. The
// spaces are what keep it disjoint from the path shape below, which forbids them.
var ciJobNameRe = regexp.MustCompile(`^[a-z][a-z-]* / [a-z][a-z-]*$`)

// citedPathRe matches a cited repo path: a slash, and no character that makes it a pattern rather
// than a path. The exclusions are load-bearing, not defensive — `**/*_test.go` is a glob and
// `docs/reference/strategies/<id>.md` is a template, and both are legitimate prose that names no
// single file. Resolving either would fail on text that is not wrong.
var citedPathRe = regexp.MustCompile(`^[^\s*<>{}?|]+/[^\s*<>{}?|]*$`)

// ciJobNameLineRe matches a job's `name:` in ci.yml — four spaces of indent, which is job level.
// Deeper `name:` keys are artifact and step names and are not what a document cites.
var ciJobNameLineRe = regexp.MustCompile(`(?m)^ {4}name: (.+)$`)

// enforcedByBlocks returns the markdown blocks of text that make an "Enforced by:" promise.
//
// A block is a run of non-blank lines, so a claim that trails a table carries the table with it.
// That is deliberate: the doc-update table at the "What to update" section is a list of paths its
// own "Enforced by:" line promises are checked, and a dead path there is the same defect one line
// later.
func enforcedByBlocks(doc string) []string {
	var blocks []string

	for _, block := range strings.Split(doc, "\n\n") {
		if strings.Contains(block, enforcedByMarker) {
			blocks = append(blocks, block)
		}
	}

	return blocks
}

// citations returns the inline-code spans of a block, deduplicated and sorted.
func citations(block string) []string {
	seen := make(map[string]bool)

	for _, m := range backtickedRe.FindAllStringSubmatch(block, -1) {
		seen[m[1]] = true
	}

	return slices.Sorted(maps.Keys(seen))
}

// resolvesInTree reports whether a cited path exists IN THIS REPOSITORY, as a file or a directory.
//
// A bare YAML name resolves against .github/workflows first, because that is where a document citing
// `ci.yml` means, and against the root and .github after it, for the config files that live there.
//
// "In this repository" is the whole contract, so the escape is closed before the stat rather than
// hoped away: filepath.Join CLEANS `..`, so a citation of `../../etc/hosts` — or of
// `../<this-repo>/CONTRIBUTING.md`, which exists — would stat successfully and read as resolved while
// naming nothing a contributor can find here. An absolute path or a `..` segment is not a
// repo-relative citation and is rejected outright; the containment check after the join is the belt
// to that pair of braces, because a symlink or a future candidate shape could still land outside.
func resolvesInTree(root, cited string) bool {
	if filepath.IsAbs(cited) || strings.HasPrefix(cited, "/") {
		return false
	}

	if slices.Contains(strings.Split(filepath.ToSlash(cited), "/"), "..") {
		return false
	}

	candidates := []string{cited}
	if !strings.Contains(cited, "/") {
		candidates = []string{
			filepath.Join(".github", "workflows", cited),
			cited,
			filepath.Join(".github", cited),
		}
	}

	for _, c := range candidates {
		path := filepath.Join(root, filepath.FromSlash(c))
		if !strings.HasPrefix(path, root+string(filepath.Separator)) {
			continue
		}

		if _, err := os.Stat(path); err == nil {
			return true
		}
	}

	return false
}

// ciJobNames returns the names of every job declared in ci.yml.
func ciJobNames(workflow string) []string {
	var names []string

	for _, m := range ciJobNameLineRe.FindAllStringSubmatch(workflow, -1) {
		names = append(names, strings.TrimSpace(m[1]))
	}

	return names
}

// unresolvedClaims returns the citations of a document's "Enforced by:" blocks that name nothing
// real: a YAML file that is not in the tree, a repo path that is not in the tree, or a CI job that
// ci.yml does not declare.
//
// Split out from the test so both directions are drivable with synthetic input. A gate that has only
// ever been run against a tree it passes on is a gate nobody has watched fail.
func unresolvedClaims(root, doc, ciWorkflow string) []string {
	jobs := ciJobNames(ciWorkflow)

	var bad []string

	for _, block := range enforcedByBlocks(doc) {
		claim := strings.SplitN(block, "\n", 2)[0]

		for _, cited := range citations(block) {
			switch {
			case ciJobNameRe.MatchString(cited):
				if !slices.Contains(jobs, cited) {
					bad = append(bad, fmt.Sprintf("CI job %q is not declared in ci.yml — claimed by: %s", cited, claim))
				}
			case yamlFileRe.MatchString(cited), citedPathRe.MatchString(cited):
				if !resolvesInTree(root, cited) {
					bad = append(bad, fmt.Sprintf("%q does not exist — claimed by: %s", cited, claim))
				}
			}
		}
	}

	return bad
}

// TestContributing_EnforcedByClaims_ResolveInTheTree is the gate issue #139 asks for.
//
// Every file, path and CI job cited inside an "Enforced by:" block must exist. Nothing here judges
// whether the mechanism does what the sentence claims — that is review's job — but a promise citing
// a file nobody has is wrong on its face, and it is wrong in the direction that costs a contributor
// their first PR.
func TestContributing_EnforcedByClaims_ResolveInTheTree(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	doc := readRepoFile(t, "CONTRIBUTING.md")
	ci := readCIWorkflow(t)

	blocks := enforcedByBlocks(doc)
	require.NotEmptyf(t, blocks,
		"CONTRIBUTING.md contains no %s block. Either the house promise form has been renamed — in "+
			"which case rename it here too — or the claims have been deleted; this gate is pointed at "+
			"nothing either way.", enforcedByMarker)

	// The gate must have live inputs, not just live blocks. A rewrite that leaves the promises in
	// place but stops citing anything would make every assertion below vacuous, and vacuous is the
	// one failure mode a green run cannot distinguish from correct.
	var cited int

	for _, block := range blocks {
		for _, c := range citations(block) {
			if ciJobNameRe.MatchString(c) || yamlFileRe.MatchString(c) || citedPathRe.MatchString(c) {
				cited++
			}
		}
	}

	require.NotZerof(t, cited,
		"no %s block cites a file, path or CI job, so this gate resolved nothing. A promise with no "+
			"mechanism named is the defect #139 is about, one step further along.", enforcedByMarker)

	require.Empty(t, unresolvedClaims(root, doc, ci),
		"CONTRIBUTING.md promises enforcement by something that does not exist.\n"+
			"Either build the mechanism or say plainly that it is not built — see issue #139 for what "+
			"each false promise cost:\n\n  %s",
		strings.Join(unresolvedClaims(root, doc, ci), "\n  "))
}

// TestContributing_NamedYAMLFiles_ExistOrAreDeclaredAbsent covers the citations OUTSIDE an "Enforced
// by:" block, where `ci-budget.yml` hid: the CI-budget section said it "measures the last 200 runs
// weekly and files an issue", which is an enforcement promise in every respect except the bolded
// lead-in the gate above keys on.
//
// A workflow filename is the shape worth policing file-wide, because it is the shape that reads as a
// live mechanism wherever it appears. The exception list is short and each entry states why the name
// is in the file at all — a name deliberately cited as ABSENT is the honest case, and it must stay
// possible to write.
func TestContributing_NamedYAMLFiles_ExistOrAreDeclaredAbsent(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	doc := readRepoFile(t, "CONTRIBUTING.md")

	// Cited precisely to say it does not exist. Adding a row here is a claim that the surrounding
	// prose says "there is no such file", and a reviewer can check that in one line.
	declaredAbsent := map[string]string{
		"dependabot.yml": "§Proposing a dependency names it to say this repo has none — Renovate is the updater, and running both fights over the same PRs",
	}

	var bad []string

	for _, cited := range citations(doc) {
		if !yamlFileRe.MatchString(cited) {
			continue
		}

		if _, ok := declaredAbsent[cited]; ok {
			continue
		}

		if !resolvesInTree(root, cited) {
			bad = append(bad, cited)
		}
	}

	require.Empty(t, bad,
		"CONTRIBUTING.md names a workflow file that is not in this tree: %s\n"+
			"A contributor reads a named .yml as a mechanism that runs today. Build it, stop naming it, "+
			"or — if the point of the sentence is that it does NOT exist — add it to declaredAbsent "+
			"above with the reason.", strings.Join(bad, ", "))

	// The exception list must not outlive its sentence: a row for a name CONTRIBUTING no longer
	// mentions is a waiver nobody is using, and the next false claim inherits it.
	for name := range declaredAbsent {
		require.Containsf(t, doc, "`"+name+"`",
			"declaredAbsent carries %q, which CONTRIBUTING.md no longer cites — drop the row", name)
	}
}

// TestContributingClaims_GateFixtures_FailAndPass drives every failure branch with synthetic input,
// because the assertions above pass on the tree as it now is and a gate that has only ever passed
// proves nothing about what it would catch.
//
// The false-claim fixtures are the historical text VERBATIM, from CONTRIBUTING.md as issue #139 found
// it. That is the strongest form available: the gate is demonstrated against the exact sentences it
// exists to have caught, not against a plausible imitation of them.
func TestContributingClaims_GateFixtures_FailAndPass(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// A minimal stand-in for ci.yml, so the job-name branch is driven by a known set rather than by
	// whatever the real workflow happens to declare today.
	const ciFixture = `jobs:
  lint-repo:
    name: lint / repo
    steps:
      - uses: actions/checkout@v4
        with:
          name: not-a-job-name
`

	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "lefthook installing a prepare-commit-msg hook (#139 row 1)",
			doc: "**Enforced by:** the DCO GitHub App, a required status check named `DCO`. " +
				"`lefthook.yml` installs a\n`prepare-commit-msg` hook so a local commit picks up the " +
				"trailer automatically.",
			want: `"lefthook.yml" does not exist`,
		},
		{
			name: "the PR-title lint workflow (#139 row 2)",
			doc:  "release notes. **Enforced by:** `pr-title-lint.yml`.",
			want: `"pr-title-lint.yml" does not exist`,
		},
		{
			name: "a CI job that ci.yml does not declare",
			doc:  "**Enforced by:** the `lint / assertions` CI job diffs every test against main.",
			want: `CI job "lint / assertions" is not declared in ci.yml`,
		},
		{
			name: "a path that has moved out from under the citation (#145's class)",
			doc:  "**Enforced by:** `scripts/verify-spec.py`, which reads the catalogue as text.",
			want: `"scripts/verify-spec.py" does not exist`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bad := unresolvedClaims(root, tc.doc, ciFixture)
			require.Len(t, bad, 1, "expected exactly one unresolved claim, got %v", bad)
			require.Contains(t, bad[0], tc.want)
		})
	}

	// The other direction, in the same fixture shape: a promise citing things that are all real must
	// come back empty, or the gate is noise a contributor learns to route around.
	t.Run("claims that resolve pass", func(t *testing.T) {
		t.Parallel()

		doc := "**Enforced by:** the `lint / repo` CI job greps for those identifiers; " +
			"`.github/CODEOWNERS` puts `test/golden/` behind a named reviewer; `ci.yml` runs it."

		require.Empty(t, unresolvedClaims(root, doc, ciFixture))
	})

	// Prose that is not a promise is not policed. A design document may name a workflow that does not
	// exist yet — docs/design/06-cicd-and-release.md does, deliberately — and the gate must not
	// punish a sentence for containing a filename.
	t.Run("a citation outside an Enforced by block is not a claim", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, unresolvedClaims(root,
			"The title lint is designed but not built; `pr-title-lint.yml` lives in the design doc.",
			ciFixture))
	})

	// A citation that leaves the repository resolves to nothing a contributor can find, however well
	// it stats. The traversal below points back at THIS repository's own CONTRIBUTING.md through its
	// parent directory, so the file genuinely exists on any machine and filepath.Join cleans the `..`
	// away — which is exactly what made it resolve before the containment check went in. What is wrong
	// with it is not that the file is missing; it is that the citation is not repo-relative.
	t.Run("a citation that escapes the tree does not resolve", func(t *testing.T) {
		t.Parallel()

		for _, cited := range []string{
			"../" + filepath.Base(root) + "/CONTRIBUTING.md",
			"/etc/hosts",
		} {
			bad := unresolvedClaims(root, "**Enforced by:** `"+cited+"`.", ciFixture)
			require.Lenf(t, bad, 1, "%q resolved from outside the tree, got %v", cited, bad)
			require.Contains(t, bad[0], cited)
		}
	})

	// Globs and templates are legitimate prose that names no single file, and resolving either would
	// fail on text that is not wrong.
	t.Run("globs and templates are not paths", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, unresolvedClaims(root,
			"**Enforced by:** a reviewer reading `**/*_test.go` and `docs/reference/strategies/<id>.md`.",
			ciFixture))
	})
}
