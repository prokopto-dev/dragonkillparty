package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHistoryCheck_AppendedRows_AreTheOnlyLegalChange is the control for the whole append-only half.
// Without it, a check that rejected every manifest would satisfy every rejection test below while
// making the Release PR — the one PR that must change this file — impossible to merge.
func TestHistoryCheck_AppendedRows_AreTheOnlyLegalChange(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// append is added to the manifest after the base commit.
		append string
	}{
		{"a new row", "000002_guild.sql " + sha256Hex(cleanMigration) + "\n"},
		{"a new row and a trailing comment", "000002_guild.sql " + sha256Hex(cleanMigration) + "\n# sealed for v0.2.0\n"},
		{"nothing at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := newTree(t)
			writeMigration(t, tr, "000001_init.sql", cleanMigration)
			writeLock(t, tr, "000001_init.sql", cleanMigration)
			commitBase(t, tr)

			writeLockRaw(t, tr, readLock(t, tr)+tc.append)

			notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
			require.NoError(t, err)
			require.Empty(t, problems, "appending is the one change the manifest exists to allow")
			require.Empty(t, notes, "the check ran, so it must not also claim it was skipped")
		})
	}
}

// TestHistoryCheck_RewrittenManifest_IsReported is the bypass that hashing alone cannot see, and it
// is the reason this check exists at all.
//
// SHIPPED.lock ships in the same commit as the migration it protects. So a change that touches BOTH
// halves leaves a perfectly self-consistent tree: edit the migration and rewrite its recorded hash,
// or simply delete the row and leave the file unlisted. Every hash matches, nothing is missing, and
// a migration that has already run on somebody's database is quietly editable again.
func TestHistoryCheck_RewrittenManifest_IsReported(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// rewrite produces the manifest the change under test leaves behind.
		rewrite func(base string) string
	}{
		{
			// Both halves changed together. The tree agrees with itself; only history disagrees.
			name: "a hash rewritten to match an edited migration",
			rewrite: func(base string) string {
				return strings.ReplaceAll(base, sha256Hex(cleanMigration), sha256Hex(tamperedMigration))
			},
		},
		{
			// The quieter one: drop the row and the file is simply not a shipped migration any more.
			name: "a row deleted so the migration is no longer listed",
			rewrite: func(base string) string {
				return "# SHIPPED.lock fixture\n"
			},
		},
		{
			// The header is bytes too. Re-heading the file is a rewrite of the record's provenance.
			name: "the header reworded, rows untouched",
			rewrite: func(base string) string {
				return strings.Replace(base, "# SHIPPED.lock fixture\n", "# rewritten header\n", 1)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := newTree(t)
			writeMigration(t, tr, "000001_init.sql", cleanMigration)
			writeLock(t, tr, "000001_init.sql", cleanMigration)
			commitBase(t, tr)

			writeLockRaw(t, tr, tc.rewrite(readLock(t, tr)))

			notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
			require.NoError(t, err)
			require.Empty(t, notes, "a rewrite must be a failure, not a skip")
			require.NotEmpty(t, problems)
			require.Contains(t, strings.Join(problems, "\n"), "REWRITTEN",
				"the failure must say the manifest itself was rewritten, not merely that a hash "+
					"disagreed — the whole point is that the hashes DO agree")
		})
	}
}

// TestHistoryCheck_ReorderedRows_AreARewrite is the property that makes this a PREFIX comparison and
// not a set comparison, and it is the one no black-box test of the shell implementation ever made.
//
// A set comparison — "every row that was there is still there somewhere" — accepts reordering. But
// the manifest's order IS information: it is the order the migrations shipped, which is the order
// goose applied them to a user's database. A file whose rows have been reshuffled has been rewritten
// by something, and whatever rewrote it could as easily have dropped one. The rule is strict: what
// the merge base said, byte for byte, must still be the BEGINNING of what this change says.
func TestHistoryCheck_ReorderedRows_AreARewrite(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeMigration(t, tr, "000002_guild.sql", tamperedMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration, "000002_guild.sql", tamperedMigration)
	commitBase(t, tr)

	// The same two rows, both still present, both still correct — in the other order.
	writeLockRaw(t, tr, "# SHIPPED.lock fixture\n"+
		"000002_guild.sql "+sha256Hex(tamperedMigration)+"\n"+
		"000001_init.sql "+sha256Hex(cleanMigration)+"\n")

	notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
	require.NoError(t, err)
	require.Empty(t, notes)
	require.NotEmpty(t, problems,
		"reordering leaves the SET of rows identical, so a set comparison would pass this — which "+
			"is why the comparison is a byte prefix")
	require.Contains(t, strings.Join(problems, "\n"), "REWRITTEN")
}

// TestHistoryCheck_RewrittenManifest_NamesTheRowThatChanged keeps the failure pointing at a line
// rather than at a file. A row that is gone and a row whose hash changed are both "no longer present
// as recorded", and both mean the same thing: a shipped migration just became editable.
func TestHistoryCheck_RewrittenManifest_NamesTheRowThatChanged(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeMigration(t, tr, "000002_guild.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration, "000002_guild.sql", cleanMigration)
	commitBase(t, tr)

	// 000001's row is rewritten; 000002's is left exactly as it was.
	writeLockRaw(t, tr, "# SHIPPED.lock fixture\n"+
		"000001_init.sql "+sha256Hex(tamperedMigration)+"\n"+
		"000002_guild.sql "+sha256Hex(cleanMigration)+"\n")

	_, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
	require.NoError(t, err)

	joined := strings.Join(problems, "\n")
	require.Contains(t, joined, "no longer recorded as it was at the merge base: 000001_init.sql "+
		sha256Hex(cleanMigration))
	require.NotContains(t, joined, "no longer recorded as it was at the merge base: 000002_guild.sql",
		"the untouched row must not be named; a failure that lists every row tells the reader nothing")
}

// TestHistoryCheck_NoManifestAtTheMergeBase_IsANoteNotAFailure covers the change that introduces the
// file. There is nothing to compare against yet, and refusing it would mean the manifest could never
// be created in the first place.
func TestHistoryCheck_NoManifestAtTheMergeBase_IsANoteNotAFailure(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	writeLock(t, tr, "000001_init.sql", cleanMigration)

	notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
	require.NoError(t, err)
	require.Empty(t, problems)
	require.Len(t, notes, 1)
	require.Contains(t, notes[0], "does not exist at the merge base")
}

// TestHistoryCheck_UnreadableHistory_SkipsLoudly pins the deliberate hole and its exact boundary.
//
// The check reads the merge base through git, so it skips when history is not there — a shallow
// checkout, a tarball, a fixture tree that was never committed. Hard-failing instead would break
// every shallow-checkout job that runs `make lint-repo` through a test, and a gate that red-lights
// honest jobs gets deleted. What keeps the skip from becoming the normal case is elsewhere:
// ci.yml's `lint / repo` job carries `fetch-depth: 0`, and TestCI_LintRepoJob_FetchesFullHistory
// fails if that line is ever removed.
//
// Each case asserts the skip NAMES its reason. A silent skip is the same as no check at all, and the
// reader who sees "0 problems" has no way to tell which they got.
func TestHistoryCheck_UnreadableHistory_SkipsLoudly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) tree
		want  string
	}{
		{
			name: "not a git work tree",
			setup: func(t *testing.T) tree {
				t.Helper()

				tr := newTree(t)
				writeLock(t, tr, "000001_init.sql", cleanMigration)

				return tr
			},
			want: "this is not a git work tree",
		},
		{
			name: "the base ref is not available",
			setup: func(t *testing.T) tree {
				t.Helper()

				tr := newTree(t)
				writeLock(t, tr, "000001_init.sql", cleanMigration)
				commitBase(t, tr)
				tr.baseRef = "origin/does-not-exist"

				return tr
			},
			want: "origin/does-not-exist is not available (shallow clone?",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := tc.setup(t)

			notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
			require.NoError(t, err)
			require.Empty(t, problems)
			require.Len(t, notes, 1)
			require.Contains(t, notes[0], "append-only history NOT checked: "+tc.want)
		})
	}
}

// TestHistoryCheck_NoGitOnPath_SkipsLoudly is the remaining skip, split out because it manipulates
// PATH for the whole process and so cannot be a parallel test — t.Setenv panics in one.
//
// It is worth the awkwardness: "git is missing" is the skip most likely to be hit for real, on a
// minimal container, and the difference between a named skip and a crash is whether the person
// staring at the log knows the manifest went unchecked.
func TestHistoryCheck_NoGitOnPath_SkipsLoudly(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	tr := newTree(t)
	writeLock(t, tr, "000001_init.sql", cleanMigration)

	notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
	require.NoError(t, err)
	require.Empty(t, problems)
	require.Len(t, notes, 1)
	require.Contains(t, notes[0], "append-only history NOT checked: git is not on PATH")
}

// TestHistoryCheck_TrailingNewlines_AreNotARewrite pins the one thing the byte comparison
// deliberately tolerates.
//
// Trailing newlines are trimmed from both sides before comparing, which is what the shell
// implementation's command substitution did. It equates files that differ only in how many newlines
// they end with — an editor's "ensure final newline" — and it cannot equate two files that differ in
// a row, because removing or rewriting a row changes bytes that are not newlines.
func TestHistoryCheck_TrailingNewlines_AreNotARewrite(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	writeLockRaw(t, tr, strings.TrimRight(readLock(t, tr), "\n")+"\n\n\n")

	_, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
	require.NoError(t, err)
	require.Empty(t, problems)
}

// TestHistoryCheck_MergeBaseNotTheBranchTip_IsWhatIsCompared covers the reason this compares against
// a merge base rather than against origin/main directly.
//
// A branch cut before a release legitimately lacks the rows that release appended. Comparing against
// the tip of main would fail it for being behind, which is not what this rule is about, and the fix
// people would reach for is disabling the check.
func TestHistoryCheck_MergeBaseNotTheBranchTip_IsWhatIsCompared(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	// main moves on and seals another migration; this branch never saw it.
	base := readLock(t, tr)
	writeMigration(t, tr, "000002_guild.sql", cleanMigration)
	writeLockRaw(t, tr, base+"000002_guild.sql "+sha256Hex(cleanMigration)+"\n")
	gitCommit(t, tr, "seal 000002")
	runGit(t, tr, "update-ref", "refs/remotes/origin/main", "HEAD")

	// The branch goes back to where it was cut and changes nothing about the manifest.
	runGit(t, tr, "reset", "-q", "--hard", "HEAD~1")

	notes, problems, err := historyCheck(tr, []byte(readLock(t, tr)))
	require.NoError(t, err)
	require.Empty(t, notes)
	require.Empty(t, problems,
		"a branch cut before a release is BEHIND origin/main, not a rewrite of it; comparing "+
			"against the tip would fail every long-lived branch and the check would be turned off")
}

// gitCommit stages and commits everything in the fixture tree.
func gitCommit(t *testing.T, tr tree, message string) {
	t.Helper()

	runGit(t, tr, "add", "-A")
	runGit(t, tr, "commit", "-q", "--no-verify", "-m", message)
}

// runGit runs one git command in the fixture tree and fails the test if it does not succeed.
func runGit(t *testing.T, tr tree, args ...string) {
	t.Helper()

	out, err := gitOutput(tr.root, args...)
	require.NoErrorf(t, err, "git %v in the fixture tree: %s", args, out)
}
