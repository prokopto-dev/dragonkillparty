package repogate

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// ADR001 — a change that needs a decision record carries one.
//
// docs/adr/README.md and docs/design/07-documentation-system.md both said, in bold, that this was
// enforced in `lint / repo`. It was not (#85), and that is worse than an ordinary stale sentence
// because of who reads it: an agent reading the README concludes the gate will catch it, and a
// reviewer reading the same line concludes CI already asked. Neither was true.
//
// NEEDS THE PR BODY, which no scan over the tree can see, so the inputs arrive from the environment
// and ci.yml's `lint / repo` job supplies them:
//
//	DKP_ADR_BASE_REF   the PR base sha. UNSET => this is not a pull request; skip, loudly.
//	DKP_ADR_PR_BODY    the PR body. May legitimately be EMPTY — a PR with no body is a PR that has
//	                   not answered the question, so emptiness must fail rather than skip.
//
// Absence of the base ref disables the rule, which makes it fail-open in exactly one direction: a
// CI job that stopped passing the env would go quietly green. TestCI_LintRepoJob_PassesPullRequestContext
// is what holds that line. A base ref that IS set and cannot be read is a violation, never a skip:
// that is the shallow-clone case, and it is the configuration most likely to have it.
//
// TRIGGERS — the four in the two documents:
//
//	go.mod                a NEW DIRECT requirement, compared against the base blob. A version bump
//	                      or a new indirect does not fire, which is what keeps Renovate quiet.
//	deploy/Dockerfile     any change. The document says "new port, volume or process"; no scan can
//	db/schema.hcl         any change. …judge "new table or changed constraint" either.
//	internal/<pkg>/…      a file added under a top-level package that does not exist at the base.
//
// The two path triggers are deliberately BROADER than their parenthetical, and the docs say so.
// Over-triggering costs one line in the PR body and is visible; under-triggering is invisible,
// which is the failure mode this rule exists to end.
//
// SATISFIED BY either half of what the documents promise: a file ADDED under docs/adr/, or an
// `adr: n/a — <reason>` line in the body. The reason is required — a bare `adr: n/a` is the box
// ticked without the thought, and harvesting the reason is the entire point.

const (
	adrBaseRefEnv = "DKP_ADR_BASE_REF"
	adrBodyEnv    = "DKP_ADR_PR_BODY"
)

var (
	// adrWaiver matches the marker and everything after it, so the reason can be measured.
	adrWaiver = regexp.MustCompile(`(?i)^[ \t]*adr:[ \t]*`)

	// adrReason requires two whitespace-separated tokens, so a separator alone ("adr: n/a —") is
	// not a reason.
	adrReason = regexp.MustCompile(`[^ \t]+[ \t]+[^ \t]`)

	// adrLeadingPunctuation is what sits between `n/a` and the reason — an em dash, a hyphen, a
	// colon, whatever the author typed.
	adrLeadingPunctuation = regexp.MustCompile(`^[^a-zA-Z0-9]*`)

	// adrRecord is a decision record: docs/adr/<number>…, which is what separates a new record from
	// an edit to the README beside them.
	adrRecord = regexp.MustCompile(`^docs/adr/[0-9]`)

	adrPackagePath = regexp.MustCompile(`^internal/([^/]+)/`)

	// The two spellings of a require directive: the block form and the single-line one.
	adrRequireBlock = regexp.MustCompile(`^require[ \t]*\(`)
	adrRequireLine  = regexp.MustCompile(`^require[ \t]+`)
)

// runADRRule evaluates ADR001.
func runADRRule(root string, rep *report) {
	base := os.Getenv(adrBaseRefEnv)
	if base == "" {
		rep.note("skip", "[ADR001] "+adrBaseRefEnv+" is unset — no pull-request context to check against")

		return
	}

	git := gitRunner{root: root}

	if _, err := git.run("rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		rep.violation("ADR001", "the ADR base revision cannot be read, so the check cannot run",
			[]string{base + " is not in this checkout. The lint / repo job carries fetch-depth: 0\n" +
				"for this reason; a shallow clone must not turn this gate into a green check."})

		return
	}

	// base -> WORKING TREE, plus untracked files. In CI the tree is clean and this is exactly the
	// PR's diff; on a laptop it also sees work in progress, including an ADR that has been written
	// but not yet added. `--diff-filter=A` is what separates "added an ADR" from "touched the
	// index".
	untracked := git.lines("ls-files", "--others", "--exclude-standard")
	changed := union(git.lines("diff", "--name-only", base, "--"), untracked)
	added := union(git.lines("diff", "--name-only", "--diff-filter=A", base, "--"), untracked)

	triggers := adrTriggers(root, git, base, changed, added)
	if len(triggers) == 0 {
		return
	}

	if slices.ContainsFunc(added, adrRecord.MatchString) || adrWaived(os.Getenv(adrBodyEnv)) {
		return
	}

	rep.violation("ADR001",
		"this change needs an architecture decision record (docs/adr/README.md, docs/design/07 §ADRs)",
		[]string{"triggered by:\n" + strings.Join(triggers, "\n") + "\n\n" +
			"Add a file under docs/adr/ in this PR, or put a line in the PR BODY reading\n" +
			"  adr: n/a — <why this change does not need one>\n" +
			"A reason is required; the marker on its own is not one."})
}

// adrTriggers returns the documented triggers this change meets, in the documents' order.
func adrTriggers(root string, git gitRunner, base string, changed, added []string) []string {
	var triggers []string

	// The file test as well as the changed-path test: deleting go.mod entirely is a change to it,
	// and reading a file that is no longer there must not abort the rule.
	if fileExists(root, "go.mod") && slices.Contains(changed, "go.mod") {
		before := directRequires(strings.Split(git.text("show", base+":go.mod"), "\n"))
		after := directRequires(readLines(root, "go.mod"))

		var fresh []string

		for _, mod := range after {
			if !slices.Contains(before, mod) {
				fresh = append(fresh, mod)
			}
		}

		if len(fresh) > 0 {
			triggers = append(triggers, "go.mod — new direct dependency: "+strings.Join(fresh, " "))
		}
	}

	for _, path := range []string{"deploy/Dockerfile", "db/schema.hcl"} {
		if slices.Contains(changed, path) {
			triggers = append(triggers, path+" — changed")
		}
	}

	var packages []string

	for _, path := range added {
		if m := adrPackagePath.FindStringSubmatch(path); m != nil && !slices.Contains(packages, m[1]) {
			packages = append(packages, m[1])
		}
	}

	slices.Sort(packages)

	for _, pkg := range packages {
		if _, err := git.run("cat-file", "-e", base+":internal/"+pkg); err != nil {
			triggers = append(triggers, "internal/"+pkg+" — new top-level package")
		}
	}

	return triggers
}

// directRequires returns the direct module paths of a go.mod. `// indirect` is tested first, so it
// wins over both the block and the single-line spelling.
func directRequires(lines []string) []string {
	var (
		out     []string
		inBlock bool
	)

	for _, line := range lines {
		fields := strings.Fields(line)

		switch {
		case strings.Contains(line, "// indirect"):
			continue
		case adrRequireBlock.MatchString(line):
			inBlock = true
		case inBlock && strings.HasPrefix(line, ")"):
			inBlock = false
		case inBlock && len(fields) >= 2 && strings.HasPrefix(fields[1], "v"):
			out = append(out, fields[0])
		case adrRequireLine.MatchString(line) && len(fields) >= 3 && strings.HasPrefix(fields[2], "v"):
			out = append(out, fields[1])
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// adrWaived reports whether the PR body carries `adr: n/a` WITH a reason.
func adrWaived(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		loc := adrWaiver.FindStringIndex(line)
		if loc == nil {
			continue
		}

		rest := line[loc[1]:]
		if !strings.HasPrefix(strings.ToLower(rest), "n/a") {
			continue
		}

		rest = adrLeadingPunctuation.ReplaceAllString(rest[len("n/a"):], "")

		if adrReason.MatchString(rest) {
			return true
		}
	}

	return false
}

// gitRunner runs git against the tree under inspection.
//
// Every call tolerates failure and yields nothing, because the one failure that matters — a base
// revision that cannot be read — is checked explicitly and reported as a violation before any of
// these run. A tree that is not a git repository at all reaches here only when DKP_ADR_BASE_REF is
// set, which is CI's business to get right.
type gitRunner struct{ root string }

func (g gitRunner) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.root

	out, err := cmd.Output()

	return string(out), err
}

func (g gitRunner) text(args ...string) string {
	out, err := g.run(args...)
	if err != nil {
		return ""
	}

	return out
}

func (g gitRunner) lines(args ...string) []string {
	return splitNonEmpty(g.text(args...))
}

// union merges two path lists into one sorted, deduplicated list — `sort -u` over both.
func union(a, b []string) []string {
	merged := append(slices.Clone(a), b...)
	slices.Sort(merged)

	return slices.Compact(merged)
}

func splitNonEmpty(s string) []string {
	var out []string

	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}

	return out
}

func fileExists(root, rel string) bool {
	info, err := os.Stat(joinRel(root, rel))

	return err == nil && !info.IsDir()
}

func readLines(root, rel string) []string {
	body, err := os.ReadFile(joinRel(root, rel))
	if err != nil {
		return nil
	}

	return strings.Split(string(body), "\n")
}
