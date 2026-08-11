package repo_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The label gate, and the defect it exists for.
//
// An issue form may declare any label it likes. GitHub applies the ones that exist and DROPS THE
// REST WITHOUT SAYING SO — no error to the reporter, no warning to the maintainer, nothing in the
// audit log. That is how all five forms in .github/ISSUE_TEMPLATE/ came to declare `needs-triage`,
// `parser-bug`, `importer` and `parity` when none of the four had ever been created (#43): every
// issue filed from a form arrived bare, a parser bug was indistinguishable from anything else in
// the list, and the triage slot in docs/design/06-cicd-and-release.md had nothing to filter on.
//
// Nothing in GitHub will ever tell us that happened again. So .github/labels.yml is the declared
// set and this file is the gate: a form may only name a label the manifest carries. It runs on
// every `make check`, needs no network and no `gh` — the sync in the other direction is
// scripts/sync-labels.sh, run by a maintainer on the day a label is added.
//
// The parsers below are strict on purpose and fail on a line they do not understand. A parser that
// skips what it cannot read is a gate that goes quietly blind, which is the same failure mode
// GitHub's silent drop already demonstrated once.

// labelSpec is one entry of .github/labels.yml.
type labelSpec struct {
	name        string
	color       string
	description string
	line        int
}

// manifestColor is the colour form the manifest requires: six lowercase hex digits and no leading
// '#'. `gh label create` accepts both forms, so this is a house rule rather than an API constraint
// — but two spellings of one colour is two diffs for one change, and #FFF-style shorthand is not
// accepted by the API at all.
var manifestColor = regexp.MustCompile(`^[0-9a-f]{6}$`)

// parseLabelManifest reads .github/labels.yml into its entries.
//
// It is a hand parser rather than a YAML one, and the reason is a dependency: gopkg.in/yaml.v3 is
// in the module graph only as testify's indirect, and promoting it to a direct dependency to read
// one 20-entry file is not a trade this repo makes (AGENTS.md: propose it with the reason and the
// licence). The manifest's format is documented in its own header and the parser rejects anything
// else, so the two cannot drift apart silently.
func parseLabelManifest(path string) ([]labelSpec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read label manifest: %w", err)
	}

	var (
		specs   []labelSpec
		current *labelSpec
	)

	flush := func() {
		if current != nil {
			specs = append(specs, *current)
			current = nil
		}
	}

	for i, line := range strings.Split(string(body), "\n") {
		lineNo := i + 1

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		switch {
		case strings.HasPrefix(line, "- name:"):
			flush()

			current = &labelSpec{
				name: strings.TrimSpace(strings.TrimPrefix(line, "- name:")),
				line: lineNo,
			}
		case strings.HasPrefix(trimmed, "color:") && current != nil:
			current.color = strings.TrimSpace(strings.TrimPrefix(trimmed, "color:"))
		case strings.HasPrefix(trimmed, "description:") && current != nil:
			current.description = strings.Trim(
				strings.TrimSpace(strings.TrimPrefix(trimmed, "description:")), `"`)
		default:
			return nil, fmt.Errorf("%s:%d: unparseable line %q — the manifest format is "+
				"documented in its own header and this parser is the enforcement of it",
				filepath.Base(path), lineNo, line)
		}
	}

	flush()

	return specs, nil
}

// flowLabels matches the single-line form GitHub's docs use and every form in this repo is written
// in: `labels: ["parser-bug", "needs-triage"]`.
var flowLabels = regexp.MustCompile(`^labels:\s*\[(.*)\]\s*$`)

// parseFormLabels returns the labels each issue form under dir declares, keyed by file name.
//
// BOTH YAML sequence styles are handled — the flow form above and the block form
//
//	labels:
//	  - parser-bug
//
// because GitHub accepts both. Handling only the style the repo happens to use today would leave a
// form written in the other style unchecked, and an unchecked form is exactly the state #43
// described.
//
// config.yml is skipped: it configures the chooser (blank_issues_enabled, contact_links) and is not
// a form.
func parseFormLabels(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read issue template dir: %w", err)
	}

	declared := make(map[string][]string)

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "config.yml" {
			continue
		}

		if ext := filepath.Ext(name); ext != ".yml" && ext != ".yaml" {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			return nil, fmt.Errorf("read issue form %s: %w", name, readErr)
		}

		lines := strings.Split(string(body), "\n")

		for i, line := range lines {
			if m := flowLabels.FindStringSubmatch(line); m != nil {
				for _, raw := range strings.Split(m[1], ",") {
					if v := unquoteYAMLScalar(raw); v != "" {
						declared[name] = append(declared[name], v)
					}
				}

				break
			}

			// The block form: a bare top-level `labels:` followed by indented `- ` items.
			if strings.TrimRight(line, " \t") != "labels:" {
				continue
			}

			for _, next := range lines[i+1:] {
				if strings.TrimSpace(next) == "" {
					continue
				}

				// Column 0 means the sequence ended and the next top-level key started.
				if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
					break
				}

				item := strings.TrimSpace(next)
				if !strings.HasPrefix(item, "- ") {
					break
				}

				if v := unquoteYAMLScalar(strings.TrimPrefix(item, "- ")); v != "" {
					declared[name] = append(declared[name], v)
				}
			}

			break
		}
	}

	return declared, nil
}

// unquoteYAMLScalar trims whitespace and one layer of surrounding quotes from a scalar.
func unquoteYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)

	return strings.TrimSpace(s)
}

// missingFormLabels returns, per form, the labels it declares that the manifest does not carry.
func missingFormLabels(declared map[string][]string, manifest []labelSpec) map[string][]string {
	known := make(map[string]bool, len(manifest))
	for _, spec := range manifest {
		known[spec.name] = true
	}

	missing := make(map[string][]string)

	for form, labels := range declared {
		for _, l := range labels {
			if !known[l] {
				missing[form] = append(missing[form], l)
			}
		}
	}

	return missing
}

// TestIssueForms_DeclaredLabels_AllExistInManifest is the gate itself: no issue form may declare a
// label .github/labels.yml does not carry.
//
// The negative subtests are the point. A gate asserting only that today's tree is clean would pass
// just as happily if the parsers returned nothing at all — which is how a gate rots into a no-op
// without anyone noticing, and the failure it guards against is already invisible on GitHub's side.
func TestIssueForms_DeclaredLabels_AllExistInManifest(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	t.Run("this repo's forms are clean", func(t *testing.T) {
		t.Parallel()

		manifest, err := parseLabelManifest(filepath.Join(root, ".github", "labels.yml"))
		require.NoError(t, err)

		declared, err := parseFormLabels(filepath.Join(root, ".github", "ISSUE_TEMPLATE"))
		require.NoError(t, err)

		// NO VACUOUS PASS. Every form must declare at least one label, or an issue filed from it
		// lands unlabelled and the triage query in docs/design/06-cicd-and-release.md cannot see
		// it. This is also what stops a parser regression — a `labels:` line the scanner stopped
		// recognising — from reading as "clean".
		require.NotEmpty(t, declared, "no issue form declared any label — has the scanner gone blind?")

		forms, err := os.ReadDir(filepath.Join(root, ".github", "ISSUE_TEMPLATE"))
		require.NoError(t, err)

		for _, f := range forms {
			name := f.Name()
			if f.IsDir() || name == "config.yml" {
				continue
			}

			require.NotEmpty(t, declared[name],
				"%s declares no labels: an issue filed from it arrives bare", name)
		}

		require.Empty(t, missingFormLabels(declared, manifest),
			"an issue form declares a label .github/labels.yml does not carry. GitHub DROPS an "+
				"unknown label silently, so the issue would arrive without it and nobody would be "+
				"told. Add the label to the manifest and run `make labels-sync ARGS=--apply`")
	})

	t.Run("a form declaring an unknown label fails", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRepoFile(t, dir, "labels.yml", "- name: bug\n  color: d73a4a\n  description: \"x\"\n")
		writeRepoFile(t, dir, "ISSUE_TEMPLATE/bug_report.yml",
			"name: Bug\nlabels: [\"bug\", \"needs-triage\"]\nbody: []\n")

		manifest, err := parseLabelManifest(filepath.Join(dir, "labels.yml"))
		require.NoError(t, err)

		declared, err := parseFormLabels(filepath.Join(dir, "ISSUE_TEMPLATE"))
		require.NoError(t, err)

		require.Equal(t, map[string][]string{"bug_report.yml": {"needs-triage"}},
			missingFormLabels(declared, manifest),
			"the gate must name the undeclared label and only that one — `bug` IS in the "+
				"fixture manifest, and a gate that flagged it too would be flagging everything")
	})

	t.Run("a form written in block style is scanned too", func(t *testing.T) {
		t.Parallel()

		// GitHub accepts both sequence styles. This repo's five forms all use the flow style, so
		// a scanner that only understood that one would pass every assertion above while leaving
		// the first block-style form anyone writes completely unchecked.
		dir := t.TempDir()
		writeRepoFile(t, dir, "labels.yml", "- name: bug\n  color: d73a4a\n  description: \"x\"\n")
		writeRepoFile(t, dir, "ISSUE_TEMPLATE/block.yml",
			"name: Block\nlabels:\n  - bug\n  - \"ghost-label\"\nbody: []\n")

		manifest, err := parseLabelManifest(filepath.Join(dir, "labels.yml"))
		require.NoError(t, err)

		declared, err := parseFormLabels(filepath.Join(dir, "ISSUE_TEMPLATE"))
		require.NoError(t, err)

		require.Equal(t, []string{"bug", "ghost-label"}, declared["block.yml"],
			"the block sequence form must be parsed, both items of it")
		require.Equal(t, map[string][]string{"block.yml": {"ghost-label"}},
			missingFormLabels(declared, manifest))
	})

	t.Run("a manifest line the parser cannot read is an error, not a skip", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRepoFile(t, dir, "labels.yml",
			"- name: bug\n  color: d73a4a\n  description: \"x\"\n  colour: d73a4a\n")

		_, err := parseLabelManifest(filepath.Join(dir, "labels.yml"))
		require.Error(t, err,
			"a misspelled key must fail the parse — skipping it would drop the label from the "+
				"sync plan while the gate reported success")
		require.Contains(t, err.Error(), "labels.yml:4")
	})
}

// TestLabelManifest_IsWellFormed asserts the manifest is in the shape scripts/sync-labels.sh will
// hand to `gh`: unique names, a six-hex-digit colour, and a description on every entry.
//
// The description is not decoration. A label with no description is a word whose meaning lives in
// one maintainer's head, and this set carries policy — `dormant` and `needs-info` name the two
// rows of the stale policy that can close an issue.
func TestLabelManifest_IsWellFormed(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRoot(t), ".github", "labels.yml")

	specs, err := parseLabelManifest(path)
	require.NoError(t, err)
	require.NotEmpty(t, specs, "the manifest declared no labels")

	seen := make(map[string]int, len(specs))

	for _, s := range specs {
		require.NotEmpty(t, s.name, "labels.yml:%d: a label with no name", s.line)
		require.NotEmpty(t, s.description, "labels.yml:%d: %s has no description", s.line, s.name)
		require.Regexp(t, manifestColor, s.color,
			"labels.yml:%d: %s: colour must be six lowercase hex digits with no leading '#'",
			s.line, s.name)

		if first, dup := seen[s.name]; dup {
			require.Failf(t, "duplicate label",
				"labels.yml:%d: %s is already declared on line %d — the second block would "+
					"silently overwrite the first on sync", s.line, s.name, first)
		}

		seen[s.name] = s.line
	}

	// The stale policy in docs/design/06-cicd-and-release.md drives "never auto-close a bug" off
	// label identity: a label named there and absent here is a policy that cannot fire.
	for _, required := range []string{"needs-info", "cannot-reproduce", "support", "dormant"} {
		_, ok := seen[required]
		require.True(t, ok,
			"%s is named by the stale policy in docs/design/06-cicd-and-release.md and must "+
				"exist; the policy is keyed on label identity", required)
	}
}

// TestLabelManifest_CoversEveryLabelTheDocsTellPeopleToUse ties the manifest to AGENTS.md, which
// tells every agent working in this repo to file findings with `bug`, `documentation` or
// `enhancement`. Those three are GitHub defaults today, so nothing would notice if one were renamed
// out of the manifest — until the next `make labels-sync` stopped managing it.
func TestLabelManifest_CoversEveryLabelTheDocsTellPeopleToUse(t *testing.T) {
	t.Parallel()

	specs, err := parseLabelManifest(filepath.Join(repoRoot(t), ".github", "labels.yml"))
	require.NoError(t, err)

	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.name)
	}

	sort.Strings(names)

	for _, required := range []string{"bug", "documentation", "enhancement"} {
		require.Contains(t, names, required,
			"AGENTS.md tells agents to file issues with the existing labels %q — it must exist",
			required)
	}
}

// The phase-label half, and the three-way disagreement it exists for.
//
// `phase-0` … `phase-9` were created in repo settings by hand and never written down: the manifest
// did not carry them, and AGENTS.md said in as many words that "there are no phase labels in this
// repo; do not invent one" while twenty of the twenty-five open issues carried one
// (#68, #79, #86). Both halves cost something. A label that exists only in repo settings is outside
// the mechanism this file is — it cannot be reviewed in a diff, which is the property #43 was fixed
// to get. And an agent obeying AGENTS.md filed phase-less issues next to labelled ones, so the
// phase sweep silently missed them, while an agent following the repo's evident practice was
// knowingly disobeying AGENTS.md — which is worse for every other rule in that file.
//
// So the two gates below pin the manifest to ROADMAP.md and to the AGENTS.md bullet that tells
// agents what to file. Neither needs the network; what they cannot see is repo settings, which is
// scripts/sync-labels.sh's job and reported, never enforced (Renovate and GitHub both create
// labels, and this repo is not the authority on whether one of those was a mistake).

// roadmapPhaseHeading matches the phase headings of ROADMAP.md — `## Phase 4 — Raid operations and
// log ingest (≈62 pt, 14%)`. Anchored at `## ` so the phase named in a risk-register table cell or
// a sub-heading is not counted as a phase of its own.
var roadmapPhaseHeading = regexp.MustCompile(`^##\s+Phase\s+(\d+)\s`)

// phaseLabelName matches a manifest entry in the phase family.
var phaseLabelName = regexp.MustCompile(`^phase-(\d+)$`)

// roadmapPhases returns the phase numbers ROADMAP.md declares, in file order.
func roadmapPhases(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read roadmap: %w", err)
	}

	var phases []string

	for _, line := range strings.Split(string(body), "\n") {
		if m := roadmapPhaseHeading.FindStringSubmatch(line); m != nil {
			phases = append(phases, m[1])
		}
	}

	return phases, nil
}

// manifestPhases returns the phase numbers the manifest declares a label for, in file order.
func manifestPhases(specs []labelSpec) []string {
	var phases []string

	for _, s := range specs {
		if m := phaseLabelName.FindStringSubmatch(s.name); m != nil {
			phases = append(phases, m[1])
		}
	}

	return phases
}

// TestLabelManifest_PhaseLabels_MatchTheRoadmap asserts the phase family is exactly ROADMAP.md's
// phases — in BOTH directions, because each direction is a different mistake. A phase heading with
// no label is the state this test was written for: work targeted at a phase nobody can filter for.
// A label with no heading is a typo (`phase-10` for `phase-1`) that would sit in the manifest
// looking deliberate, and be pushed into repo settings by the next `--apply`.
func TestLabelManifest_PhaseLabels_MatchTheRoadmap(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	t.Run("the manifest covers every roadmap phase and nothing else", func(t *testing.T) {
		t.Parallel()

		specs, err := parseLabelManifest(filepath.Join(root, ".github", "labels.yml"))
		require.NoError(t, err)

		phases, err := roadmapPhases(filepath.Join(root, "ROADMAP.md"))
		require.NoError(t, err)

		require.NotEmpty(t, phases,
			"no `## Phase N` heading was found in ROADMAP.md — has the heading style changed, or "+
				"has this scanner gone blind? A vacuous pass here is the failure it guards against")

		require.Equal(t, phases, manifestPhases(specs),
			"the phase labels in .github/labels.yml must be exactly ROADMAP.md's phases, in order. "+
				"A phase with no label cannot be filtered for and the sweep for it misses work; a "+
				"label with no phase is a typo the next `make labels-sync ARGS=--apply` pushes "+
				"into repo settings")

		for _, s := range specs {
			m := phaseLabelName.FindStringSubmatch(s.name)
			if m == nil {
				continue
			}

			// The description is where a triager reads what the label means, and the only
			// pointer they get to the document that decides which one to apply.
			require.Contains(t, s.description, "Phase "+m[1],
				"labels.yml:%d: %s must name the phase it targets", s.line, s.name)
			require.Contains(t, s.description, "ROADMAP.md",
				"labels.yml:%d: %s must point at ROADMAP.md, which is how a phase is chosen",
				s.line, s.name)
		}
	})

	t.Run("a roadmap phase with no label fails", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRepoFile(t, dir, "ROADMAP.md", "## Phase 0 — Foundations\n\n## Phase 1 — Ledger\n")
		writeRepoFile(t, dir, "labels.yml",
			"- name: phase-0\n  color: 5319e7\n  description: \"Phase 0 (see ROADMAP.md)\"\n")

		specs, err := parseLabelManifest(filepath.Join(dir, "labels.yml"))
		require.NoError(t, err)

		phases, err := roadmapPhases(filepath.Join(dir, "ROADMAP.md"))
		require.NoError(t, err)

		require.Equal(t, []string{"0", "1"}, phases)
		require.NotEqual(t, phases, manifestPhases(specs),
			"a phase heading with no label must be a difference this gate can see")
	})

	t.Run("a label with no roadmap phase fails", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRepoFile(t, dir, "ROADMAP.md", "## Phase 0 — Foundations\n")
		writeRepoFile(t, dir, "labels.yml",
			"- name: phase-0\n  color: 5319e7\n  description: \"x\"\n"+
				"- name: phase-10\n  color: 5319e7\n  description: \"x\"\n")

		specs, err := parseLabelManifest(filepath.Join(dir, "labels.yml"))
		require.NoError(t, err)

		require.Equal(t, []string{"0", "10"}, manifestPhases(specs),
			"the phase scanner must read the whole family, not stop at the first entry")

		phases, err := roadmapPhases(filepath.Join(dir, "ROADMAP.md"))
		require.NoError(t, err)

		require.NotEqual(t, phases, manifestPhases(specs))
	})

	t.Run("a phase named in a table cell is not a heading", func(t *testing.T) {
		t.Parallel()

		// ROADMAP.md's risk register and sequencing tables discuss phases in prose — "Phase 2's
		// exit criterion required PAT-parity green". A scanner that counted those would demand
		// labels for phases that do not exist, and the fix would be to weaken this gate.
		dir := t.TempDir()
		writeRepoFile(t, dir, "ROADMAP.md",
			"| Phase 2's exit criterion was circular | fixed |\n"+
				"### Phase 3 sub-heading\n"+
				"## Phase 0 — Foundations\n")

		phases, err := roadmapPhases(filepath.Join(dir, "ROADMAP.md"))
		require.NoError(t, err)

		require.Equal(t, []string{"0"}, phases,
			"only a `## Phase N` heading declares a phase")
	})
}

// issueFilingBullet returns the "Use the existing labels" bullet of AGENTS.md § "Out-of-scope
// findings: file an issue" — the one instruction in the repo that tells an agent which labels to
// put on an issue it files. Empty if the bullet is gone.
//
// A bullet runs from its `- ` to the next line that starts one or to the first blank line, which is
// how every list in AGENTS.md is written.
func issueFilingBullet(body string) string {
	const marker = "- **Use the existing labels**"

	lines := strings.Split(body, "\n")

	for i, line := range lines {
		if !strings.HasPrefix(line, marker) {
			continue
		}

		bullet := []string{line}

		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" || strings.HasPrefix(next, "- ") {
				break
			}

			bullet = append(bullet, next)
		}

		return strings.Join(bullet, "\n")
	}

	return ""
}

// backtickedName matches one `code span`.
var backtickedName = regexp.MustCompile("`([^`]+)`")

// labelShapedToken is the shape a GitHub label name takes in this repo: lowercase, digits, spaces
// (`good first issue`), hyphens and underscores. It is what separates a label from the other things
// the bullet names in backticks — a path (`.github/ISSUE_TEMPLATE/`), a document (`ROADMAP.md`), a
// heading (`## Phase N`). Those carry a `/`, a `.`, a `#` or a capital, so none of them is
// mistakable for a label and none needs an exception list that would rot.
var labelShapedToken = regexp.MustCompile(`^[a-z0-9][a-z0-9 _-]*$`)

// unresolvedIssueFilingNames returns the label-shaped names the bullet uses that are neither a
// label in the manifest nor an issue form in formDir.
//
// Both halves are legitimate: the bullet names labels to apply AND forms to file from, and
// `parser-bug` is deliberately both. What is not legitimate is a name that resolves to neither,
// because that is an agent being told to use something that does not exist — and GitHub drops an
// unknown label silently, which is the whole reason this file exists.
func unresolvedIssueFilingNames(bullet string, manifest []labelSpec, formDir string) []string {
	known := make(map[string]bool, len(manifest))
	for _, spec := range manifest {
		known[spec.name] = true
	}

	var unresolved []string

	seen := make(map[string]bool)

	for _, m := range backtickedName.FindAllStringSubmatch(bullet, -1) {
		name := m[1]
		if !labelShapedToken.MatchString(name) || seen[name] {
			continue
		}

		seen[name] = true

		if known[name] {
			continue
		}

		isForm := false

		for _, ext := range []string{".yml", ".yaml"} {
			if _, err := os.Stat(filepath.Join(formDir, name+ext)); err == nil {
				isForm = true

				break
			}
		}

		if !isForm {
			unresolved = append(unresolved, name)
		}
	}

	sort.Strings(unresolved)

	return unresolved
}

// TestAgentsMD_IssueFilingLabels_AgreeWithTheManifest is the gate on the instruction itself.
//
// TestLabelManifest_CoversEveryLabelTheDocsTellPeopleToUse above hardcodes the three kind labels;
// this one derives the whole set from the bullet, so a label added to that instruction tomorrow is
// checked without anyone remembering to add it here. It also asserts the bullet still carries the
// phase convention — deleting that sentence is how the repo got into the state #68 described, and a
// deletion nothing fails on is a deletion that survives review.
func TestAgentsMD_IssueFilingLabels_AgreeWithTheManifest(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	formDir := filepath.Join(root, ".github", "ISSUE_TEMPLATE")

	t.Run("this repo's instruction is clean", func(t *testing.T) {
		t.Parallel()

		body, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		require.NoError(t, err)

		bullet := issueFilingBullet(string(body))
		require.NotEmpty(t, bullet,
			"AGENTS.md § \"Out-of-scope findings\" must keep its \"Use the existing labels\" "+
				"bullet: it is the only place an agent is told what to put on an issue it files")

		manifest, err := parseLabelManifest(filepath.Join(root, ".github", "labels.yml"))
		require.NoError(t, err)

		require.Empty(t, unresolvedIssueFilingNames(bullet, manifest, formDir),
			"AGENTS.md tells agents to use a name that is neither a label in "+
				".github/labels.yml nor a form in .github/ISSUE_TEMPLATE/. GitHub DROPS an "+
				"unknown label silently, so an agent that obeys the instruction files an issue "+
				"without it and nobody is told")

		// Non-vacuous: the bullet must actually name phase labels, and they must be real ones.
		// Without this the assertion above passes just as happily on a bullet that says the phase
		// labels do not exist — which is exactly what it said before #68.
		named := 0

		for _, m := range backtickedName.FindAllStringSubmatch(bullet, -1) {
			if phaseLabelName.MatchString(m[1]) {
				named++
			}
		}

		require.NotZero(t, named,
			"the bullet must tell agents to apply a roadmap-phase label and name at least one "+
				"of them; ten exist and are in use, and an instruction that omits them produces "+
				"phase-less issues the sweep for that phase cannot see (#68, #79, #86)")
	})

	t.Run("a bullet naming a label that does not exist fails", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRepoFile(t, dir, "labels.yml", "- name: bug\n  color: d73a4a\n  description: \"x\"\n")
		writeRepoFile(t, dir, "ISSUE_TEMPLATE/parity-gap.yml", "name: Parity\nbody: []\n")

		bullet := issueFilingBullet(
			"- **Use the existing labels** — `bug`, `ghost-label` — and the matching form in\n" +
				"  `.github/ISSUE_TEMPLATE/` where one fits: `parity-gap`, `phantom-form`.\n" +
				"- **Do not expand the PR to fix it.** `bug` again, outside the bullet.\n")

		manifest, err := parseLabelManifest(filepath.Join(dir, "labels.yml"))
		require.NoError(t, err)

		require.Equal(t, []string{"ghost-label", "phantom-form"},
			unresolvedIssueFilingNames(bullet, manifest, filepath.Join(dir, "ISSUE_TEMPLATE")),
			"the gate must name the two that resolve to nothing and only those — `bug` IS in the "+
				"fixture manifest and `parity-gap` IS a fixture form, and a gate that flagged "+
				"either would be flagging everything")
	})

	t.Run("the bullet ends where the next one starts", func(t *testing.T) {
		t.Parallel()

		// The scanner must not swallow the rest of the list: the following bullets name `gh issue
		// create`, file paths and prose in backticks, and reading those as labels would make this
		// gate fire on text that is not an instruction about labels at all.
		bullet := issueFilingBullet(
			"- **Use the existing labels** — `bug` — and\n" +
				"  a continuation line naming `phase-0`.\n" +
				"- **Do not expand the PR to fix it.** Naming `ghost-label` here.\n")

		require.NotContains(t, bullet, "ghost-label")
		require.Contains(t, bullet, "phase-0", "an indented continuation line is part of the bullet")
	})

	t.Run("a missing bullet is empty, not a false pass", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, issueFilingBullet("# AGENTS\n\nNo such bullet here.\n"))
	})
}
