package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate/sqlscan"
)

// committedMigrations is db/migrations-sqlite, relative to this package. A Go test's working
// directory is its own package directory, so the path is fixed rather than discovered.
const committedMigrations = "../../../db/migrations-sqlite"

// atlasOutput is the shape Atlas actually emits for a SQLite diff: backtick-quoted identifiers and
// a Down block containing real DDL. Both of the things rewrite exists to remove, in one input.
const atlasOutput = "-- +goose Up\n" +
	"-- create \"dkp_meta\" table\n" +
	"CREATE TABLE `dkp_meta` (`key` text NOT NULL, `value` text NOT NULL, PRIMARY KEY (`key`)) STRICT;\n" +
	"\n" +
	"-- +goose Down\n" +
	"-- reverse: create \"dkp_meta\" table\n" +
	"DROP TABLE `dkp_meta`;\n"

// TestRewrite_BacktickedIdentifiers_BecomeDoubleQuoted covers the sqlc half.
//
// sqlc's SQLite parser does not accept MySQL-style backtick quoting, and it does not say so: it
// parses no table out of the schema and reports `relation "dkp_meta" does not exist` against the
// QUERY file, which is the one file that was correct.
func TestRewrite_BacktickedIdentifiers_BecomeDoubleQuoted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "table and column",
			in:   "CREATE TABLE `dkp_meta` (`key` text NOT NULL);",
			want: `CREATE TABLE "dkp_meta" ("key" text NOT NULL);`,
		},
		{
			name: "several on one line",
			in:   "CREATE INDEX `ix_a` ON `t` (`a`, `b`);",
			want: `CREATE INDEX "ix_a" ON "t" ("a", "b");`,
		},
		{
			name: "leading underscore is an identifier",
			in:   "SELECT `_private`;",
			want: `SELECT "_private";`,
		},
		{
			name: "a backtick pair that is not a bare identifier is left alone",
			in:   "-- see `a b` for the rationale",
			want: "-- see `a b` for the rationale",
		},
		{
			name: "an unpaired backtick is left alone",
			in:   "-- one ` backtick",
			want: "-- one ` backtick",
		},
		{
			name: "double quotes are already correct and are not touched",
			in:   `CREATE TABLE "dkp_meta" ("key" text NOT NULL);`,
			want: `CREATE TABLE "dkp_meta" ("key" text NOT NULL);`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in + "\n")
			require.NoError(t, err)
			require.Equal(t, tc.want+"\n"+downBlock, got)
		})
	}
}

// TestRewrite_BacktickInStringLiteral_Refuses is the regression pin for the one input the rewrite
// would silently corrupt, and the reason this code is worth having in Go at all.
//
// `CHECK (a <> '` + "`abc`" + `')` rewritten becomes `CHECK (a <> '"abc"')` — still valid SQL, still
// applies cleanly, and now means something different, in a file that is append-only and permanent
// once committed. The whole file must be left exactly as Atlas wrote it.
func TestRewrite_BacktickInStringLiteral_Refuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		in       string
		wantLine int
	}{
		{
			name:     "a DEFAULT wrapping an identifier-shaped word",
			in:       "-- +goose Up\nCREATE TABLE t (note text NOT NULL DEFAULT 'the `value` column');\n",
			wantLine: 2,
		},
		{
			name:     "a CHECK comparing against a backticked word",
			in:       "-- +goose Up\nCREATE TABLE t (a text, CHECK (a <> '`abc`'));\n",
			wantLine: 2,
		},
		{
			name:     "one backtick inside a literal, with no closing pair",
			in:       "-- +goose Up\nINSERT INTO t VALUES ('a ` b');\n",
			wantLine: 2,
		},
		{
			name: "only in the Atlas Down block, which is about to be discarded anyway",
			in: "-- +goose Up\nCREATE TABLE t (a text);\n\n-- +goose Down\n" +
				"INSERT INTO t VALUES ('the `value` column');\n",
			wantLine: 5,
		},
		{
			name:     "after a backtick-free literal on the same line",
			in:       "-- +goose Up\nINSERT INTO t VALUES ('', 'the `value` column');\n",
			wantLine: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in)

			require.ErrorIs(t, err, sqlscan.ErrBacktickInStringLiteral,
				"the rewrite would change what that literal MEANS; it must refuse")
			require.Empty(t, got, "a refusal must produce no output at all")

			var lit sqlscan.LiteralBacktick
			require.ErrorAs(t, err, &lit)
			require.Equal(t, tc.wantLine, lit.Line, "the refusal must name the offending line")
		})
	}
}

// TestRewrite_AdjacentLiterals_AreNotAFalsePositive is the other half of the refusal, and the case
// the shell version had to dodge by hand.
//
// A naive `'[^']*` + backtick + `[^']*'` reports a backtick identifier sitting BETWEEN two literals
// as being inside one, because its runs hop the `', '` boundary between them — and two TEXT columns
// with defaults is what any ordinary table produces. Refusing that would refuse correct work, which
// is how a safety check stops being believed. Stripping the backtick-free literals first is what
// makes the check mean what it says.
func TestRewrite_AdjacentLiterals_AreNotAFalsePositive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a backticked column between an empty default and a real one",
			in:   "CREATE TABLE t (a text DEFAULT '', `b` text DEFAULT 'UTC');",
			want: `CREATE TABLE t (a text DEFAULT '', "b" text DEFAULT 'UTC');`,
		},
		{
			name: "two backticked columns between three literals",
			in:   "INSERT INTO t VALUES ('x', `a`, 'y', `b`, 'z');",
			want: `INSERT INTO t VALUES ('x', "a", 'y', "b", 'z');`,
		},
		{
			name: "a literal before a backticked identifier",
			in:   "CREATE TABLE t (a text DEFAULT 'UTC', `b` text);",
			want: `CREATE TABLE t (a text DEFAULT 'UTC', "b" text);`,
		},
		{
			name: "a trigger body quoting SQL either side of a backticked table",
			in:   "CREATE TRIGGER g BEFORE UPDATE ON `ledger_entry` BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;",
			want: `CREATE TRIGGER g BEFORE UPDATE ON "ledger_entry" BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in + "\n")
			require.NoError(t, err, "this is correct input and must not be refused")
			require.Equal(t, tc.want+"\n"+downBlock, got)
		})
	}
}

// TestRewrite_MultilineStringLiteral_Refuses is the case a per-line check cannot see, and the one
// that would have done the most damage.
//
// A SQLite string literal may span physical lines. Read one line at a time — which is all `grep` and
// `sed` can do, and what the shell version this replaced therefore did — the backtick on the
// continuation line is indistinguishable from an identifier quote. It was rewritten: the stored
// DEFAULT silently became something else, AND the backtick that MIG002 would have caught it by was
// removed on the way, so nothing downstream could notice either.
func TestRewrite_MultilineStringLiteral_Refuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		in           string
		wantLine     int
		wantOpenedAt int
	}{
		{
			name: "a backtick on the continuation line of a DEFAULT",
			in: "-- +goose Up\n" +
				"CREATE TABLE t (\n" +
				"  note text NOT NULL DEFAULT 'first line\n" +
				"`value`\n" +
				"last line'\n" +
				");\n",
			wantLine:     4,
			wantOpenedAt: 3,
		},
		{
			name: "a backtick several lines into the literal",
			in: "-- +goose Up\n" +
				"INSERT INTO t VALUES ('a\n" +
				"b\n" +
				"c `d` e\n" +
				"f');\n",
			wantLine:     4,
			wantOpenedAt: 2,
		},
		{
			name: "a backtick after a doubled-quote escape inside the literal",
			in: "-- +goose Up\n" +
				"INSERT INTO t VALUES ('it''s\n" +
				"`value`');\n",
			wantLine:     3,
			wantOpenedAt: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in)

			require.ErrorIs(t, err, sqlscan.ErrBacktickInStringLiteral,
				"a backtick inside a multiline literal is data, and rewriting it changes the value")
			require.Empty(t, got)

			var lit sqlscan.LiteralBacktick
			require.ErrorAs(t, err, &lit)
			require.Equal(t, tc.wantLine, lit.Line, "the refusal must name the line the backtick is on")
			require.Equal(t, tc.wantOpenedAt, lit.OpenedAt,
				"and the line the literal opened on, which is where the reader has to look")
		})
	}
}

// TestRewrite_MultilineStringLiteral_ClosesWhereSQLSaysItDoes is the control for the test above.
//
// Refusing every file containing a multiline literal would satisfy the test above and be useless:
// the identifier after the literal must still be rewritten, which is only possible if the scanner
// knows the literal ended. The doubled-quote escape is here for the same reason — read as
// close-then-open, everything after it is in the wrong state.
func TestRewrite_MultilineStringLiteral_ClosesWhereSQLSaysItDoes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a backticked column after a multiline literal",
			in:   "INSERT INTO t VALUES ('first\nsecond');\nCREATE INDEX `ix_a` ON t (a);\n",
			want: "INSERT INTO t VALUES ('first\nsecond');\nCREATE INDEX \"ix_a\" ON t (a);\n",
		},
		{
			name: "a backticked column after a doubled-quote escape",
			in:   "INSERT INTO t VALUES ('it''s');\nCREATE INDEX `ix_a` ON t (a);\n",
			want: "INSERT INTO t VALUES ('it''s');\nCREATE INDEX \"ix_a\" ON t (a);\n",
		},
		{
			name: "a backticked column after a literal that is only a doubled quote",
			in:   "INSERT INTO t VALUES ('''');\nCREATE INDEX `ix_a` ON t (a);\n",
			want: "INSERT INTO t VALUES ('''');\nCREATE INDEX \"ix_a\" ON t (a);\n",
		},
		{
			name: "a backticked column inside a multiline literal's own line, after it closes",
			in:   "INSERT INTO t VALUES ('a\nb'), (`c`);\n",
			want: "INSERT INTO t VALUES ('a\nb'), (\"c\");\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in)
			require.NoError(t, err, "this is correct input and must not be refused")
			require.Equal(t, tc.want+downBlock, got)
		})
	}
}

// TestRewrite_CommentQuotes_DoNotOpenALiteral is what makes scanning the whole file safe.
//
// A scanner that took every apostrophe seriously would be broken by prose, and this generator writes
// the prose itself: the Down block it appends contains "RAISE()'s message". That single quote would
// open a literal that never closes, and every migration in the repository would be refused —
// including on the next run over a file this generator produced.
func TestRewrite_CommentQuotes_DoNotOpenALiteral(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "an apostrophe in a line comment",
			in:   "-- RAISE()'s message is discarded outside a trigger body\nCREATE INDEX `ix_a` ON t (a);\n",
			want: "-- RAISE()'s message is discarded outside a trigger body\nCREATE INDEX \"ix_a\" ON t (a);\n",
		},
		{
			name: "an apostrophe in a block comment spanning lines",
			in:   "/* it's fine\n   and still fine */\nCREATE INDEX `ix_a` ON t (a);\n",
			want: "/* it's fine\n   and still fine */\nCREATE INDEX \"ix_a\" ON t (a);\n",
		},
		{
			name: "a backtick in a comment is still rewritten, because MIG002 fails on any backtick",
			in:   "-- the `ix_a` index\nSELECT 1;\n",
			want: "-- the \"ix_a\" index\nSELECT 1;\n",
		},
		{
			name: "a comment marker inside a literal is not a comment",
			in:   "INSERT INTO t VALUES ('a -- not a comment');\nCREATE INDEX `ix_a` ON t (a);\n",
			want: "INSERT INTO t VALUES ('a -- not a comment');\nCREATE INDEX \"ix_a\" ON t (a);\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want+downBlock, got)
		})
	}
}

// TestRewrite_BacktickInAQuotedIdentifier_IsLeftAlone covers the other quoting construct.
//
// `"a` + "`b`" + `c"` is one identifier containing backticks. The shell version rewrote the inner
// pair and produced `"a"b"c"` — three tokens where there was one. Leaving it untouched means MIG002
// fails the file and a human looks at an identifier nobody meant to write, which is the right
// outcome; silently splitting it is not.
func TestRewrite_BacktickInAQuotedIdentifier_IsLeftAlone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
	}{
		{name: "double-quoted", in: "CREATE TABLE \"a`b`c\" (x text);\n"},
		{name: "bracket-quoted", in: "CREATE TABLE [a`b`c] (x text);\n"},
		{name: "double-quoted with an escaped quote", in: "CREATE TABLE \"a\"\"b`c`d\" (x text);\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.in+downBlock, got, "a quoted identifier must survive byte for byte")
		})
	}
}

// TestRewrite_LiteralsOnSeparateLines_AreNotAFalsePositive holds the boundary between two literals.
//
// Each literal opens and closes on its own line, so the backtick between them is an identifier quote
// and must be rewritten. This is the shape a whole-file regex gets wrong in the other direction from
// the multiline case — Go's `[^']` matches a newline, so it would pair the quote on one line with a
// quote several lines below and refuse a migration that is fine.
func TestRewrite_LiteralsOnSeparateLines_AreNotAFalsePositive(t *testing.T) {
	t.Parallel()

	in := "-- +goose Up\n" +
		"INSERT INTO t VALUES ('first');\n" +
		"CREATE INDEX `ix_a` ON t (a);\n" +
		"INSERT INTO t VALUES ('second');\n"

	got, err := rewrite(in)
	require.NoError(t, err)
	require.Contains(t, got, `CREATE INDEX "ix_a" ON t (a);`)
}

// TestRewrite_AtlasDownBlock_IsReplaced covers the forward-only half.
//
// Atlas's generated Down is not merely unwanted, it is not self-consistent: a 12-step rebuild emits
// `DROP TABLE new_<table>` for a table the Up block already renamed away. Gate MIG001 fails DDL in a
// Down block; this is why no generated migration ever reaches it.
func TestRewrite_AtlasDownBlock_IsReplaced(t *testing.T) {
	t.Parallel()

	got, err := rewrite(atlasOutput)
	require.NoError(t, err)

	require.NotContains(t, got, "DROP TABLE", "Atlas's Down DDL must not survive")
	require.NotContains(t, got, "reverse: create", "nothing after the marker survives")
	require.NotContains(t, got, "`", "no backtick may reach the committed file")

	require.True(t, strings.HasSuffix(got, downBlock), "the file must end with the forward-only block")
	require.Equal(t, 1, strings.Count(got, sqlscan.DownMarker), "exactly one Down marker")

	require.Equal(t,
		"-- +goose Up\n"+
			"-- create \"dkp_meta\" table\n"+
			"CREATE TABLE \"dkp_meta\" (\"key\" text NOT NULL, \"value\" text NOT NULL, PRIMARY KEY (\"key\")) STRICT;\n"+
			"\n"+
			downBlock,
		got)
}

// TestRewrite_DownMarker_MatchesWholeLinesOnly pins the marker rule.
//
// The match is whole-line and exact. Matching loosely — a prefix, a contains — would truncate a
// migration at a near miss, silently dropping DDL from a file that a user's database is about to
// execute exactly once, and the truncation would look like a correctly generated migration.
//
// None of these lines is a legal thing to write in a migration: goose treats any comment line
// carrying its annotation marker as a directive, which is why .claude/rules/migrations.md says never
// to write one out in prose. The assertion is only that this rewrite does not quietly delete the
// rest of the file when it meets one.
func TestRewrite_DownMarker_MatchesWholeLinesOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "trailing space", line: "-- +goose Down "},
		{name: "leading space", line: " -- +goose Down"},
		{name: "inside a longer comment", line: "-- the marker -- +goose Down ends the Up half"},
		{name: "different case", line: "-- +goose down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite("-- +goose Up\n" + tc.line + "\nSELECT 1;\n")
			require.NoError(t, err)
			require.Contains(t, got, "SELECT 1;", "a near-miss marker must not truncate the file")
		})
	}
}

// TestRewrite_NoDownMarker_StillAppendsTheBlock covers the input with nothing to truncate.
//
// Every committed migration must carry the forward-only block whether or not Atlas wrote a Down
// section, because goose's behaviour on a migration with no Down block is to succeed at going
// backwards by doing nothing — which is worse than refusing.
func TestRewrite_NoDownMarker_StillAppendsTheBlock(t *testing.T) {
	t.Parallel()

	got, err := rewrite("-- +goose Up\nSELECT 1;\n")
	require.NoError(t, err)
	require.Equal(t, "-- +goose Up\nSELECT 1;\n"+downBlock, got)
}

// TestRewrite_LineTermination_IsNormalised covers awk's record model, which the output shape still
// depends on.
func TestRewrite_LineTermination_IsNormalised(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "a final line with no newline gets one", in: "SELECT 1;", want: "SELECT 1;\n" + downBlock},
		{name: "a final newline does not add a blank line", in: "SELECT 1;\n", want: "SELECT 1;\n" + downBlock},
		{name: "an empty file is just the block", in: "", want: downBlock},
		{name: "interior blank lines survive", in: "a;\n\nb;\n", want: "a;\n\nb;\n" + downBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewrite(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRewrite_Output_IsAFixedPoint is the determinism property `make gen` rests on.
//
// `make verify-generated` regenerates and runs `git diff --exit-code`. A rewrite that changed its
// own output would make that diff permanent noise, and a gate that is always red stops being read.
func TestRewrite_Output_IsAFixedPoint(t *testing.T) {
	t.Parallel()

	once, err := rewrite(atlasOutput)
	require.NoError(t, err)

	twice, err := rewrite(once)
	require.NoError(t, err)

	require.Equal(t, once, twice, "rewriting the committed form must be a no-op")
}

// TestRewrite_CommittedMigrations_AreFixedPoints is the parity assertion against the shell version
// this replaced.
//
// Every migration in db/migrations-sqlite was produced by the `sed`/`awk` pipeline that used to live
// in scripts/new-migration.sh. Each one is a fixed point of the Go rewrite, so the two agree byte for
// byte on the only inputs that have ever actually been generated — which is what makes the move a
// refactor rather than a new implementation with the same intent.
func TestRewrite_CommittedMigrations_AreFixedPoints(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join(committedMigrations, "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no committed migrations found — the relative path is wrong")

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			src, readErr := os.ReadFile(path)
			require.NoError(t, readErr)

			got, rewriteErr := rewrite(string(src))
			require.NoError(t, rewriteErr)
			require.Equal(t, string(src), got,
				"%s is not a fixed point: the Go rewrite disagrees with the shell rewrite that wrote it",
				path)
		})
	}
}

// TestRun_RewritesInPlace covers the file half: the bytes, and the mode.
//
// 0644 rather than whatever the umask gives a temp file: a migration is committed, world-readable
// source read inside a container by a user who is not the one who generated it.
func TestRun_RewritesInPlace(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "000001_init.sql")
	require.NoError(t, os.WriteFile(path, []byte(atlasOutput), 0o600))

	require.NoError(t, run(path))

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	want, err := rewrite(atlasOutput)
	require.NoError(t, err)
	require.Equal(t, want, string(body))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

// TestRun_Refusal_WritesNothing is what "refuse rather than corrupt" means on disk.
//
// The file is left exactly as Atlas wrote it, which is what the refusal tells the reader to fix by
// hand. A partial rewrite would be the corruption the refusal exists to prevent, arrived at by a
// different route.
func TestRun_Refusal_WritesNothing(t *testing.T) {
	t.Parallel()

	src := "-- +goose Up\nCREATE TABLE `t` (a text DEFAULT 'the `value` column');\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "000001_init.sql")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	err := run(path)
	require.ErrorIs(t, err, sqlscan.ErrBacktickInStringLiteral)

	body, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, src, string(body), "the refused file was modified")

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "a temp file was left behind")
}

// TestRun_MissingFile_Errors keeps the read failure a named error rather than a panic on empty
// input, which would produce a valid-looking migration containing only the Down block.
func TestRun_MissingFile_Errors(t *testing.T) {
	t.Parallel()

	err := run(filepath.Join(t.TempDir(), "nope.sql"))
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestRefusalMessage_CarriesThePhraseTheFixtureAssertsOn ties this package to the end-to-end
// negative fixture.
//
// TestNewMigration_BacktickInStringLiteral_Refuses in test/repo runs the real script and asserts on
// this wording. Rewording the message without that test failing first would leave the fixture
// asserting on a string nothing produces, which is a gate that has quietly stopped checking.
func TestRefusalMessage_CarriesThePhraseTheFixtureAssertsOn(t *testing.T) {
	t.Parallel()

	path := "db/migrations-sqlite/000005_add_thing.sql"

	msg := refusalMessage(path, sqlscan.LiteralBacktick{Line: 7, OpenedAt: 7, Text: "  DEFAULT 'the `value` column'"})

	require.Contains(t, msg, "backtick inside a string literal")
	require.Contains(t, msg, path+":7")
	require.Contains(t, msg, "DEFAULT 'the `value` column'", "the offending line must be quoted back")
	require.Contains(t, msg, "db/schema.hcl", "the message must name where the fix goes")
	require.Contains(t, msg, "atlas migrate hash --env sqlite", "and how to recover by hand")
	require.NotContains(t, msg, "literal opened at line",
		"a single-line literal must not be told where it opened — it opened right there")

	// The multiline case is the one where the line the reader is sent to is not the line the backtick
	// is on, so the message has to say both.
	multiline := refusalMessage(path, sqlscan.LiteralBacktick{Line: 9, OpenedAt: 7, Text: "`value`"})

	require.Contains(t, multiline, path+":9")
	require.Contains(t, multiline, "opened at line 7")
}

// TestDisplayPath_UnderTheWorkingDirectory_IsRelative keeps the refusal naming the file the way the
// diff, git and the gates name it.
//
// The script has to pass an absolute path, for the reason displayPath's comment gives. An operator
// reading `/Users/someone/Git/DragonKillParty/db/migrations-sqlite/000005_add_thing.sql` at the top
// of a refusal has to work out that it is the file they just generated.
func TestDisplayPath_UnderTheWorkingDirectory_IsRelative(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	require.Equal(t, filepath.Join("db", "migrations-sqlite", "000005_add_thing.sql"),
		displayPath(filepath.Join(cwd, "db", "migrations-sqlite", "000005_add_thing.sql")))

	// A fixture tree in t.TempDir() is not under the module root, and `../../../../var/folders/…` is
	// not an improvement on the absolute path.
	outside := filepath.Join(t.TempDir(), "000001_init.sql")
	require.Equal(t, outside, displayPath(outside))
}

// TestUnwrap_IsErrBacktickInStringLiteral keeps the sentinel reachable through the wrapping run adds,
// which is what the command's exit path and both tests above match on.
func TestUnwrap_IsErrBacktickInStringLiteral(t *testing.T) {
	t.Parallel()

	err := sqlscan.LiteralBacktick{Line: 3, OpenedAt: 3, Text: "x"}

	require.True(t, errors.Is(err, sqlscan.ErrBacktickInStringLiteral))
	require.Contains(t, err.Error(), "line 3")
}
