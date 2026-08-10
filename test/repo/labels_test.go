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
