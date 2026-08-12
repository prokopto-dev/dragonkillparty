package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseManifest_WellFormedManifest_ParsesEveryRow is the control, and it is what makes every
// rejection test below mean something: without it a parser that rejected everything would satisfy
// them all.
func TestParseManifest_WellFormedManifest_ParsesEveryRow(t *testing.T) {
	t.Parallel()

	one := sha256Hex("one")
	two := sha256Hex("two")

	rows, problems := parseManifest([]byte(manifestHeader +
		"\n" +
		"000001_init.sql " + one + "\n" +
		"# a comment between rows\n" +
		"   \n" +
		"000002_guild.sql " + two + "\n"))

	require.Empty(t, problems, "a well-formed manifest must produce no problems")
	// The header is ten lines, so the first row is on line 12: the line number is reported to a
	// human who then opens the file at it, which only helps if it is the real one.
	require.Equal(t, []row{
		{line: 12, name: "000001_init.sql", hash: one},
		{line: 15, name: "000002_guild.sql", hash: two},
	}, rows, "rows, their order and their line numbers are all part of what a failure reports")
}

// TestParseManifest_MalformedRow_IsAProblemAndNotARow closes the vacuous-pass hole, and it is the
// single most important property in this file.
//
// A parser that skips what it cannot read reports green on a truncated, half-written or hand-mangled
// manifest — the exact file states a bad merge produces — because the rows it silently dropped are
// the rows it would otherwise have had to check. Then the next release seals on top of a record that
// already lost rows. So every case below asserts BOTH halves: a problem is reported, and the line is
// not counted as a checked row.
func TestParseManifest_MalformedRow_IsAProblemAndNotARow(t *testing.T) {
	t.Parallel()

	valid := sha256Hex(cleanMigration)

	for _, tc := range []struct {
		name string
		row  string
		want string
	}{
		{"no hash", "000001_init.sql\n", "a row is exactly '<filename> <sha256>'"},
		{"a third field", "000001_init.sql " + valid + " extra\n", "a row is exactly '<filename> <sha256>'"},
		{"truncated hash", "000001_init.sql deadbeef\n", "64 lowercase hex"},
		{"uppercase hash", "000001_init.sql " + strings.ToUpper(valid) + "\n", "64 lowercase hex"},
		{"65 hex characters", "000001_init.sql " + valid + "a\n", "64 lowercase hex"},
		{"non-hex character", "000001_init.sql " + strings.Repeat("g", 64) + "\n", "64 lowercase hex"},
		{"a path, not a basename", "db/migrations-sqlite/000001_init.sql " + valid + "\n", "plain migration basename"},
		{"a parent traversal", "../000001_init.sql " + valid + "\n", "plain migration basename"},
		{"an absolute path", "/etc/passwd " + valid + "\n", "plain migration basename"},
		{"uppercase in the filename", "000001_Init.sql " + valid + "\n", "plain migration basename"},
		{"an indented comment", "  # not a comment in column 1\n", "a row is exactly '<filename> <sha256>'"},
		{"not a .sql file", "000001_init.txt " + valid + "\n", "must be a .sql migration"},
		{"a bare filename with no extension", "000001_init " + valid + "\n", "must be a .sql migration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows, problems := parseManifest([]byte("# fixture\n" + tc.row))

			require.Len(t, problems, 1, "exactly one problem for one malformed row")
			require.Contains(t, problems[0], tc.want)
			require.Contains(t, problems[0], lockFile+":2:",
				"a problem must name the manifest and the line, so the reader can open it")
			require.Empty(t, rows,
				"a rejected row must not also be counted as a checked row — that is how a mangled "+
					"manifest reports 'n rows unchanged' while checking nothing")
		})
	}
}

// TestParseManifest_DuplicateRow_IsRejected covers the one malformed shape that is not malformed
// line by line.
//
// Two rows for the same migration means somebody appended a second, corrected record instead of
// leaving the first alone — the manifest read as editable. Which of the two is then "the" recorded
// hash is a question with no answer, so the file is rejected rather than resolved.
func TestParseManifest_DuplicateRow_IsRejected(t *testing.T) {
	t.Parallel()

	h := sha256Hex(cleanMigration)

	rows, problems := parseManifest([]byte(
		"000001_init.sql " + h + "\n" +
			"000001_init.sql " + h + "\n"))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "000001_init.sql is listed twice")
	require.Contains(t, problems[0], lockFile+":2:")
	require.Len(t, rows, 1, "the first row still stands; only the repeat is rejected")
}

// TestParseManifest_IgnorableLines_AreIgnored pins what the header promises a reader: blank lines
// and lines starting with # are ignored. It also pins the two shapes that are NOT ignorable, because
// the boundary is where a hand-edit shows up.
func TestParseManifest_IgnorableLines_AreIgnored(t *testing.T) {
	t.Parallel()

	h := sha256Hex(cleanMigration)

	t.Run("comments, blanks and whitespace-only lines", func(t *testing.T) {
		t.Parallel()

		rows, problems := parseManifest([]byte("# one\n\n\t \n#two\n000001_init.sql " + h + "\n\n"))

		require.Empty(t, problems)
		require.Len(t, rows, 1)
	})

	t.Run("a CRLF line ending is stripped, not parsed as part of the hash", func(t *testing.T) {
		t.Parallel()

		rows, problems := parseManifest([]byte("# one\r\n000001_init.sql " + h + "\r\n"))

		require.Empty(t, problems, "a manifest edited on Windows is not a tampered manifest")
		require.Equal(t, h, rows[0].hash)
	})

	t.Run("an indented row is still a row", func(t *testing.T) {
		t.Parallel()

		rows, problems := parseManifest([]byte("   000001_init.sql " + h + "\n"))

		require.Empty(t, problems)
		require.Len(t, rows, 1,
			"leading whitespace does not excuse a row from being checked; it is the '#' that has "+
				"to be in column 1, not the row")
	})
}

// TestParseManifest_SeveralBadRows_AreAllReported keeps the failure honest about how much is wrong.
// Stopping at the first problem turns one bad merge into as many rounds of CI as it has bad rows.
func TestParseManifest_SeveralBadRows_AreAllReported(t *testing.T) {
	t.Parallel()

	rows, problems := parseManifest([]byte(
		"000001_init.sql\n" +
			"000002_guild.sql deadbeef\n" +
			"000003_ledger.sql " + sha256Hex(cleanMigration) + "\n"))

	require.Len(t, problems, 2)
	require.Len(t, rows, 1, "the one good row is still checked")
}

// TestCheckRows_ModifiedOrDeletedMigration_IsReported is the first half of MIG003: the manifest and
// the tree must agree.
//
// The two failures are named differently on purpose. An edit at least leaves a diff on a file a
// reviewer is looking at; removing the file entirely leaves goose's applied-version table naming a
// migration that no longer exists, and a fresh install simply never runs it.
func TestCheckRows_ModifiedOrDeletedMigration_IsReported(t *testing.T) {
	t.Parallel()

	t.Run("unchanged", func(t *testing.T) {
		t.Parallel()

		tr := newTree(t)
		writeMigration(t, tr, "000001_init.sql", cleanMigration)

		problems, err := checkRows(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
		require.NoError(t, err)
		require.Empty(t, problems)
	})

	t.Run("modified", func(t *testing.T) {
		t.Parallel()

		tr := newTree(t)
		writeMigration(t, tr, "000001_init.sql", tamperedMigration)

		problems, err := checkRows(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
		require.NoError(t, err)
		require.Len(t, problems, 1)
		require.Contains(t, problems[0], "000001_init.sql — MODIFIED.")
		require.Contains(t, problems[0], "expected "+sha256Hex(cleanMigration))
		require.Contains(t, problems[0], "found "+sha256Hex(tamperedMigration),
			"both hashes are printed: the recorded one is what a reviewer compares against history")
	})

	t.Run("deleted", func(t *testing.T) {
		t.Parallel()

		tr := newTree(t)

		problems, err := checkRows(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
		require.NoError(t, err)
		require.Len(t, problems, 1)
		require.Contains(t, problems[0], "000001_init.sql — DELETED.")
	})

	t.Run("replaced by a directory", func(t *testing.T) {
		t.Parallel()

		tr := newTree(t)
		require.NoError(t, os.Mkdir(tr.migrationPath("000001_init.sql"), 0o755))

		problems, err := checkRows(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
		require.NoError(t, err, "a directory where a migration should be is a finding, not a crash")
		require.Len(t, problems, 1)
		require.Contains(t, problems[0], "DELETED")
	})
}

// TestCheckRows_UnhashableMigration_IsAnErrorAndNotAPass is the Go form of the rule the shell
// implementation stated about its two sha256 binaries: a hash gate that cannot hash must not report
// green.
//
// An unreadable file is not "unchanged" and it is not "deleted" — it is unknown, and the one answer
// this command may never give about a shipped migration is a confident one it did not check. So it
// is an error rather than a problem: errors stop the run instead of being counted.
func TestCheckRows_UnhashableMigration_IsAnErrorAndNotAPass(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	require.NoError(t, os.Chmod(tr.migrationPath("000001_init.sql"), 0o000))

	if _, err := os.ReadFile(tr.migrationPath("000001_init.sql")); err == nil {
		t.Skip("this user can read a 0o000 file (root?), so unreadability cannot be expressed here")
	}

	rows := []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}}

	problems, err := checkRows(tr, rows)
	require.Error(t, err, "an unreadable shipped migration must stop the run")
	require.Empty(t, problems)

	// And the whole invocation must fail rather than print a count.
	out, code := invoke(t, tr, "verify")
	require.Equal(t, exitFail, code, "%s", out)
	require.NotContains(t, out, "shipped migration(s) unchanged", "%s", out)
}

// TestCheckRows_ProblemsNameTheMigrationAndNotTheTree pins the reporting contract MIG003 rests on.
//
// The gate runs against a t.TempDir() in every negative test, and it runs against a contributor's
// checkout for real. A message carrying the absolute root would be correct in neither: it tells the
// reader the path of a directory that no longer exists, instead of the repo-relative file they can
// open.
func TestCheckRows_ProblemsNameTheMigrationAndNotTheTree(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", tamperedMigration)

	problems, err := checkRows(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
	require.NoError(t, err)
	require.NotContains(t, problems[0], tr.root, "a problem must not print the inspected tree's path")
}

// TestCheckComplete_UnsealedMigration_IsReported pins the one assertion that separates the release
// path from the per-PR gate.
//
// At a tag, every migration in the tree ships with that tag — so a migration missing from the
// manifest is not "not yet shipped", it is a hole in the record that nobody will notice until
// somebody edits that file two releases later and every gate says fine.
func TestCheckComplete_UnsealedMigration_IsReported(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeMigration(t, tr, "000002_guild.sql", cleanMigration)

	problems, err := checkComplete(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
	require.NoError(t, err)
	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "000002_guild.sql")
	require.Contains(t, problems[0], "make shipped-lock-seal",
		"the failure must name the command that fixes it")
}

// TestCheckComplete_EveryMigrationListed_IsClean is the control for the release gate: it must accept
// the state a sealed Release PR is in, or the release path is a wall rather than a gate.
func TestCheckComplete_EveryMigrationListed_IsClean(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)

	problems, err := checkComplete(tr, []row{{name: "000001_init.sql", hash: sha256Hex(cleanMigration)}})
	require.NoError(t, err)
	require.Empty(t, problems)
}

// TestMigrationFiles_ReturnsSqlFilesInOrder pins what "every migration present" means, because the
// seal appends in exactly this order and the manifest's row order is the order they shipped.
func TestMigrationFiles_ReturnsSqlFilesInOrder(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000002_guild.sql", cleanMigration)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeMigration(t, tr, "atlas.sum", "not a migration\n")
	writeMigration(t, tr, ".000003_swap.sql", "an editor swap file\n")
	require.NoError(t, os.Mkdir(tr.migrationPath("nested.sql"), 0o755))

	names, err := migrationFiles(tr)
	require.NoError(t, err)
	require.Equal(t, []string{"000001_init.sql", "000002_guild.sql"}, names)
}

// TestMigrationFiles_MissingDirectory_IsEmptyNotAnError keeps the gate usable against the fabricated
// trees repo-gates.sh drives it with, where db/migrations-sqlite may not exist at all.
func TestMigrationFiles_MissingDirectory_IsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	names, err := migrationFiles(tree{root: t.TempDir()})
	require.NoError(t, err)
	require.Empty(t, names)
}
