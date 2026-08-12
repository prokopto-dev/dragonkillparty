package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// historyCheck requires the manifest at the MERGE BASE to be an exact PREFIX of the manifest now.
//
// # Why this exists, and why checkRows is not enough
//
// checkRows only proves the manifest and the working tree AGREE. That catches editing a shipped
// migration, and nothing else — because the manifest is in the same commit as the migration. Change
// both together, and the tree is self-consistent again:
//
//	edit db/migrations-sqlite/000003_ledger.sql
//	replace its row in SHIPPED.lock with the new hash     -> checkRows passes, MIG003 green
//	delete its row from SHIPPED.lock entirely             -> the file is simply unlisted, green
//
// Both are the exact failure this file exists to prevent, and neither is caught by hashing alone.
// Nor is either caught elsewhere: the Claude hook is a local editor guard, not a CI control, and the
// Release PR must legitimately modify this file, so it cannot be CODEOWNERS-frozen outright.
// The only durable answer is to compare the file against its own history.
//
// # Why a prefix and not a set
//
// A set comparison ("every old row is still present somewhere") permits reordering and re-heading,
// which is a rewrite with extra steps. The file is append-only in the strict sense: whatever the
// merge base said, byte for byte, must still be the beginning of what this change says. The only
// legal edit is more bytes at the end.
//
// # Why the merge base and not origin/main
//
// A branch cut before a release legitimately lacks the rows that release appended. Comparing against
// the tip of main would fail it for being behind, which is not what this rule is about, and the fix
// people would reach for is disabling the check.
//
// # When the base cannot be read
//
// It SKIPS, loudly, naming why, through the notes it returns. That is not a hole being waved
// through: this check runs in ci.yml's `lint / repo` job, which carries `fetch-depth: 0`, and
// TestCI_LintRepoJob_FetchesFullHistory fails if that is ever removed. Hard-failing instead would
// break every shallow-checkout job that runs `make lint-repo` through a test, and a gate that
// red-lights honest jobs gets deleted.
//
// A skip covers only the cases enumerated below — no git, no work tree, no base ref, no merge base.
// Once the merge base IS readable, anything that goes wrong is an error and a hard failure, because
// at that point the check could have run and did not.
func historyCheck(t tree, current []byte) (notes, problems []string, err error) {
	skip := func(why string) []string {
		return []string{"append-only history NOT checked: " + why}
	}

	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		return skip("git is not on PATH"), nil, nil
	}

	if _, gitErr := gitOutput(t.root, "rev-parse", "--is-inside-work-tree"); gitErr != nil {
		return skip("this is not a git work tree"), nil, nil
	}

	if _, gitErr := gitOutput(t.root, "rev-parse", "--verify", "--quiet", t.baseRef); gitErr != nil {
		return skip(t.baseRef + " is not available (shallow clone? CI needs fetch-depth: 0; " +
			"locally, git fetch origin main)"), nil, nil
	}

	mergeBase, gitErr := gitOutput(t.root, "merge-base", "HEAD", t.baseRef)
	if mergeBase = strings.TrimSpace(mergeBase); gitErr != nil || mergeBase == "" {
		return skip("HEAD and " + t.baseRef + " have no merge base"), nil, nil
	}

	// `<rev>:./<path>` resolves relative to the working directory git is run in, so this is correct
	// even when the tree under inspection is a subdirectory of the repository rather than its root.
	blob := mergeBase + ":./" + lockFile

	if _, gitErr := gitOutput(t.root, "cat-file", "-e", blob); gitErr != nil {
		return []string{lockFile + " does not exist at the merge base — this is the change that introduces it"},
			nil, nil
	}

	base, gitErr := gitOutput(t.root, "show", blob)
	if gitErr != nil {
		// cat-file -e just said this blob exists, so a failure here is not a missing base: it is
		// git being unable to hand over history it has. Failing hard is the fail-closed reading.
		return nil, nil, fmt.Errorf("read %s at the merge base: %w", lockFile, gitErr)
	}

	// Trailing newlines are trimmed from both sides before comparing, which is what the shell
	// implementation's command substitution did. It equates files that differ only in how many
	// newlines they end with; it cannot equate two files that differ in a row, because removing or
	// rewriting a row changes bytes that are not newlines.
	haveNow := strings.TrimRight(string(current), "\n")
	hadThen := strings.TrimRight(base, "\n")

	if strings.HasPrefix(haveNow, hadThen) {
		return nil, nil, nil
	}

	problems = []string{
		lockFile + " was REWRITTEN, not appended to. It is the record of what has already run on a",
		"user's database; the only legal change is new rows at the end.",
	}

	// Name the rows that stopped saying what they said, so the failure points at a line rather than
	// at a file. A row that is gone and a row whose hash changed are both "no longer present as
	// recorded", and both mean the same thing: a shipped migration just became editable.
	nowLines := make(map[string]bool)
	for _, l := range strings.Split(haveNow, "\n") {
		nowLines[l] = true
	}

	for _, l := range strings.Split(hadThen, "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}

		if !nowLines[l] {
			problems = append(problems, "  no longer recorded as it was at the merge base: "+l)
		}
	}

	return nil, problems, nil
}

// gitOutput runs git inside root and returns its standard output.
//
// root rather than the process working directory, because the tree under inspection is a
// DKP_REPO_ROOT fixture in every negative test and this checkout only in the real run.
func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}

	return string(out), nil
}
