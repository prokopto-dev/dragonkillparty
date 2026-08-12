// Command migrationfmt rewrites one freshly generated SQLite migration into the form this
// repository commits, and refuses rather than guess when it cannot do so safely.
//
// It is the second half of `make migration`: scripts/new-migration.sh validates the name, picks the
// next sequence number and runs `atlas migrate diff`; this rewrites the file Atlas wrote. Two
// changes, in one pass:
//
//  1. Backtick-quoted identifiers become double-quoted ones. Atlas emits `dkp_meta` for SQLite,
//     which SQLite accepts as a MySQL compatibility extension — but sqlc's SQLite parser does not,
//     and its failure mode is the worst kind: it does not reject the schema file. It parses no table
//     out of it, generates an empty package, and then reports `relation "dkp_meta" does not exist`
//     against the QUERY, pointing at the one file that is correct. Double quotes are the SQL
//     standard identifier quote and are what a SQLite file should have contained in the first place.
//     Gate MIG002 in scripts/repo-gates.sh is the backstop for a migration that arrived some other
//     way.
//
//  2. Everything from the `-- +goose Down` marker onwards is replaced by downBlock. This project is
//     forward-only (docs/design/06-cicd-and-release.md §8) and recovery is the pre-migration
//     snapshot, never a down migration. Atlas's generated Down is not even self-consistent: a
//     12-step rebuild emits `DROP TABLE new_<table>` for a table the Up block already renamed away.
//     Gate MIG001 fails DDL in a Down block, and it is right to.
//
// THE REWRITE IS DETERMINISTIC, and that is what keeps `make gen` honest: regenerating a migration
// from the same db/schema.hcl produces these bytes again, so `verify-generated`'s
// `git diff --exit-code` stays a real assertion rather than permanent noise. Its output is also a
// fixed point of itself — TestRewrite_Output_IsAFixedPoint, and
// TestRewrite_CommittedMigrations_AreFixedPoints against the migrations actually in the tree.
//
// IT LIVES IN GO RATHER THAN IN THE SHELL SCRIPT (issue #128) because it is string surgery on a
// file that is append-only and permanent from the moment it is committed — the one class of
// artefact in this repository where a wrong rewrite is unrecoverable for a user, and, as a `sed`
// pipeline, the one that could only ever be tested through a subprocess.
//
// IT IS A SCANNER, NOT A PATTERN. Every backtick is classified by walking the file once and knowing
// what the byte before it was part of — a string literal, a quoted identifier, a comment, or SQL.
// The shell version could not: `grep` and `sed` see one physical line, and a SQLite string literal
// may span as many as it likes, so a backtick on the second line of a multiline DEFAULT read as
// ordinary SQL and was rewritten — changing the stored value while removing the backtick that
// MIG002 would have caught it by. The scanner also knows a doubled quote inside a literal is an
// escaped quote rather than a close followed by an open, so a literal containing one ends where SQL
// says it ends; and it knows a comment is not SQL, so the apostrophe in the Down block's own
// "RAISE()'s message" does not open a literal that swallows the rest of the file.
//
// It lives beside internal/migrate, which applies migrations at boot, rather than in cmd/dkp, for
// the reason internal/ledger/enumgen gives: cmd/dkp is the product binary and an officer never runs
// a code generator. It imports the standard library and nothing else, so a tree whose generated
// code does not build can still author a migration.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gooseDownMarker is the line goose uses to separate a migration's Up and Down halves. The match is
// whole-line and exact: a line carrying trailing text is not a marker, which is also how goose
// itself reads it.
const gooseDownMarker = "-- +goose Down"

// downBlock replaces everything from gooseDownMarker onwards.
//
// The prose is part of the artefact, not a comment about it: this block is what an operator reads at
// 2am when goose has refused to go backwards, so it names the file to restore rather than merely
// declining.
const downBlock = `-- +goose Down
-- Forward-only. This project ships no down migrations, ever: a down migration is code that runs
-- exactly once, in an emergency, on data that cannot be reproduced, written months earlier by
-- someone who never tested it against your database. Recovery is restoring the snapshot taken
-- immediately before this migration ran:
--
--     /data/backups/pre-<version>-<timestamp>.db.zst
--
-- The statement below aborts if goose is ever asked to run this block. Note that SQLite discards
-- RAISE()'s message outside a trigger body and reports "RAISE() may only be used within a
-- trigger-program" instead, so the path above — not the string below — is what an operator can
-- actually read.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
`

// errBacktickInStringLiteral is returned instead of a rewrite whose result would be silently wrong.
//
// A backtick INSIDE a single-quoted string wrapping something identifier-shaped is the one input
// the identifier rewrite would corrupt: `CHECK (a <> '` + "`abc`" + `')` would become
// `CHECK (a <> '"abc"')` — still valid SQL, still applies cleanly, and now meaning something
// different, with nothing in the diff to suggest a generator did it. Refusing costs a re-run;
// guessing costs wrong data in a file nothing may rewrite afterwards.
var errBacktickInStringLiteral = errors.New("backtick inside a string literal")

// literalBacktick locates one occurrence of errBacktickInStringLiteral, so the refusal can quote the
// offending line rather than make the reader find it.
//
// openedAt is where the literal STARTED, which is not always where the backtick is: a SQLite string
// literal may span physical lines, and in that case the line to look at is the one that opened the
// quote. They are reported separately when they differ.
type literalBacktick struct {
	line     int
	openedAt int
	text     string
}

func (e literalBacktick) Error() string {
	if e.openedAt != e.line {
		return fmt.Sprintf("line %d (literal opened at line %d): %s: %s",
			e.line, e.openedAt, errBacktickInStringLiteral, strings.TrimSpace(e.text))
	}

	return fmt.Sprintf("line %d: %s: %s", e.line, errBacktickInStringLiteral, strings.TrimSpace(e.text))
}

func (e literalBacktick) Unwrap() error { return errBacktickInStringLiteral }

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: migrationfmt <migration.sql>")
		os.Exit(2)
	}

	path := os.Args[1]

	if err := run(path); err != nil {
		var lit literalBacktick
		if errors.As(err, &lit) {
			fmt.Fprint(os.Stderr, refusalMessage(displayPath(path), lit))
			os.Exit(1)
		}

		fmt.Fprintf(os.Stderr, "migrationfmt: %v\n", err)
		os.Exit(1)
	}
}

// run rewrites path in place, or writes nothing at all.
//
// Nothing is written on the refusal path, deliberately: "refuse rather than corrupt" is about the
// bytes on disk, not about the exit code. What is left behind is exactly what Atlas produced, which
// is what the refusal tells the reader to fix by hand.
func run(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	out, err := rewrite(string(src))
	if err != nil {
		return fmt.Errorf("rewrite %s: %w", path, err)
	}

	return writeAtomic(path, []byte(out))
}

// rewrite returns the committed form of one generated migration, or errBacktickInStringLiteral.
//
// The WHOLE file is scanned, including the Atlas Down block that is about to be discarded: a literal
// that only appears down there still came from db/schema.hcl and will be back in the next migration,
// so reporting it now is the cheap half of that conversation. Scanning first also means a refused
// file is never partially rewritten.
//
// The truncation is line-based while the scan is not, and that is deliberate. goose reads its
// annotations line by line, so a line that reads exactly `-- +goose Down` ends the Up half as far as
// goose is concerned wherever it appears — matching that is agreeing with the tool that will run the
// file, not with the SQL grammar.
func rewrite(src string) (string, error) {
	scanned, err := rewriteBackticks(src)
	if err != nil {
		return "", err
	}

	var b strings.Builder

	for _, line := range splitLines(scanned) {
		if line == gooseDownMarker {
			break
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	b.WriteString(downBlock)

	return b.String(), nil
}

// sqlState is where the scanner is: which construct the byte it is looking at belongs to.
type sqlState int

const (
	stateSQL          sqlState = iota // ordinary SQL, where a backtick quotes an identifier
	stateString                       // '…' — a VALUE. A backtick here is data, and is refused
	stateQuotedIdent                  // "…" or […] — already correctly quoted; left alone
	stateLineComment                  // -- … to end of line
	stateBlockComment                 // /* … */, which may span lines
)

// rewriteBackticks rewrites every backtick-quoted bare identifier to a double-quoted one, and
// refuses if a backtick appears inside a string literal.
//
// ONE PASS WITH STATE, rather than a pattern per line, because the thing being decided is what the
// backtick belongs to and that is not a property of its line:
//
//   - A string literal may span physical lines. A backtick on the second line of a multiline DEFAULT
//     looks exactly like an identifier quote to anything reading one line at a time, so the shell
//     version rewrote it — changing the value the schema asked for AND removing the backtick that
//     MIG002 would otherwise have caught it by. That input is now refused, and it is the case the
//     `_MultilineLiteral_` tests pin.
//   - A doubled quote inside a literal is an escaped quote, not two literals. Treating it as a close
//     followed by an open puts everything after it in the wrong state.
//   - Comments are not SQL. The Down block this generator itself appends contains the apostrophe in
//     "RAISE()'s message"; a scanner that took it for an opening quote would refuse every migration
//     in the repository, and the fixed-point test over db/migrations-sqlite is what would say so.
//     Backticks inside a comment are still rewritten, because MIG002 fails on any backtick anywhere
//     and a comment cannot change meaning.
//   - A backtick inside a double-quoted identifier is left exactly as it is. The shell version would
//     have rewritten a `"a` + "`b`" + `c"` pair into `"a"b"c"`, silently splitting one identifier
//     into three tokens. Leaving it means MIG002 fails the file and a human looks at it, which is
//     the correct outcome for an identifier nobody meant to write.
func rewriteBackticks(src string) (string, error) {
	var b strings.Builder

	b.Grow(len(src))

	state := stateSQL
	line := 1
	openedAt := 0
	closer := byte(0)

	for i := 0; i < len(src); {
		c := src[i]
		if c == '\n' {
			line++
		}

		switch state {
		case stateString:
			switch {
			case c == '`':
				return "", literalBacktick{line: line, openedAt: openedAt, text: lineAt(src, i)}
			case c == '\'' && i+1 < len(src) && src[i+1] == '\'':
				// The doubled-quote escape: one quote of data, not a close followed by an open.
				b.WriteString("''")
				i += 2

				continue
			case c == '\'':
				state = stateSQL
			}

			b.WriteByte(c)
			i++

		case stateQuotedIdent:
			switch {
			case c == closer && closer == '"' && i+1 < len(src) && src[i+1] == '"':
				b.WriteString(`""`)
				i += 2

				continue
			case c == closer:
				state = stateSQL
			}

			b.WriteByte(c)
			i++

		case stateLineComment:
			if c == '\n' {
				state = stateSQL
				b.WriteByte(c)
				i++

				continue
			}

			i += writeBacktickOrByte(&b, src, i)

		case stateBlockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state = stateSQL
				b.WriteString("*/")
				i += 2

				continue
			}

			i += writeBacktickOrByte(&b, src, i)

		case stateSQL:
			switch {
			case c == '\'':
				state, openedAt = stateString, line
			case c == '"':
				state, closer = stateQuotedIdent, '"'
			case c == '[':
				state, closer = stateQuotedIdent, ']'
			case c == '-' && i+1 < len(src) && src[i+1] == '-':
				state = stateLineComment
				b.WriteString("--")
				i += 2

				continue
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = stateBlockComment
				b.WriteString("/*")
				i += 2

				continue
			default:
				i += writeBacktickOrByte(&b, src, i)

				continue
			}

			b.WriteByte(c)
			i++
		}
	}

	return b.String(), nil
}

// writeBacktickOrByte writes the construct starting at src[i] and returns how many bytes it
// consumed: a backtick-quoted BARE IDENTIFIER becomes a double-quoted one, and anything else is
// copied through unchanged.
//
// The closing backtick must be on the same line, and the content must be an identifier and nothing
// else. Both restrictions keep the rewrite to the case Atlas actually emits: a pair spanning lines,
// or wrapping an expression, is something this generator did not write and must not reinterpret.
func writeBacktickOrByte(b *strings.Builder, src string, i int) int {
	if src[i] != '`' {
		b.WriteByte(src[i])

		return 1
	}

	end := strings.IndexAny(src[i+1:], "`\n")
	if end < 0 || src[i+1+end] != '`' {
		b.WriteByte('`')

		return 1
	}

	name := src[i+1 : i+1+end]
	if !isBareIdentifier(name) {
		b.WriteByte('`')

		return 1
	}

	b.WriteByte('"')
	b.WriteString(name)
	b.WriteByte('"')

	return end + 2
}

// isBareIdentifier reports whether s is what Atlas puts between backticks: a table, column or index
// name, and nothing that could be an expression.
func isBareIdentifier(s string) bool {
	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}

	return s != ""
}

// lineAt returns the whole physical line containing src[i], so a refusal can quote it.
func lineAt(src string, i int) string {
	start := strings.LastIndexByte(src[:i], '\n') + 1

	end := strings.IndexByte(src[i:], '\n')
	if end < 0 {
		return src[start:]
	}

	return src[start : i+end]
}

// splitLines splits src into lines, dropping the empty trailing element a final newline produces.
//
// This is awk's record model, which the shell version used and the output shape still depends on: a
// final line with no newline terminator is still a line, and it acquires one on the way out.
func splitLines(src string) []string {
	if src == "" {
		return nil
	}

	lines := strings.Split(src, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return lines
}

// displayPath is path as the reader should see it named: repo-root-relative where that is what it
// is, and absolute otherwise.
//
// scripts/new-migration.sh must pass an absolute path — it runs this with the module root as the
// working directory, and a relative one would resolve against the real checkout rather than the tree
// being operated on. That is a fact about the invocation, not something an operator should have to
// read: under `make migration` the working directory IS the repository, so the message says
// `db/migrations-sqlite/000005_add_thing.sql`, which is the name in the diff, in git and in the
// gates. Anywhere else — a fixture tree in t.TempDir(), a file passed by hand — the relative form
// would be `../../..`-prefixed nonsense, and the absolute path is the honest answer.
func displayPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}

	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}

	return rel
}

// refusalMessage is what an operator sees instead of a rewritten migration.
//
// It is a function rather than an inline printf because scripts/new-migration.sh's negative fixture
// (TestNewMigration_BacktickInStringLiteral_Refuses in test/repo) asserts on this wording end to
// end, and TestRefusalMessage_CarriesThePhraseTheFixtureAssertsOn holds the two together.
func refusalMessage(path string, lit literalBacktick) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\033[31m  %s:%d contains a backtick inside a string literal.\033[0m\n", path, lit.line)
	fmt.Fprintf(&b, "      %s\n", strings.TrimSpace(lit.text))

	// Only when they differ, and then it is the whole explanation: the reader is looking at a line
	// whose backtick is data because a quote several lines above is still open.
	if lit.openedAt != lit.line {
		fmt.Fprintf(&b, "  The literal opened at line %d and has not closed.\n", lit.openedAt)
	}

	b.WriteString("  The backtick-to-double-quote rewrite would change what that literal MEANS, so this\n")
	b.WriteString("  generator is refusing rather than guessing. Fix the expression in db/schema.hcl to\n")
	b.WriteString("  avoid the backtick, or rewrite the identifiers in this file by hand and re-run\n")
	b.WriteString("  `atlas migrate hash --env sqlite`.\n")

	return b.String()
}

// writeAtomic replaces path's contents through a temp file in the same directory and a rename.
//
// Same reasoning as internal/ledger/enumgen's: the obvious direct write truncates the target before
// it knows it has anything to put there, and this particular target is a numbered, append-only file
// that a user's database will execute exactly once.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".migrationfmt-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}

	tmpName := tmp.Name()

	defer func() {
		// Best-effort cleanup on the failure paths; on success the rename has already moved it and
		// this removes nothing.
		_ = os.Remove(tmpName)
	}()

	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("write %s: %w", tmpName, err)
	}

	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	// CreateTemp makes the file 0600. A migration is committed, world-readable source and is read
	// inside a container by a user who is not the one who generated it.
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}

	return nil
}
