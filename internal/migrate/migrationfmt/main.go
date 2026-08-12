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
// pipeline, the one that could only ever be tested through a subprocess. Both of the inputs that
// matter — the literal that must be refused, and the adjacent-literal shape a naive regex
// misreports as one — are unit tests here.
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
	"regexp"
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

// The three patterns, and the order the last two are applied in, are the whole safety argument.
//
// backtickIdent requires the quoted text to be a BARE IDENTIFIER, so a backtick inside a longer
// string — a CHECK expression, a DEFAULT — is not rewritten by accident.
//
// plainLiteral matches a single-quoted literal that contains no backtick; removing every one of
// those from a line first is what makes literalWithBacktick mean what it says. A naive
// `'[^']*` + "`" + `[^']*'` on its own cannot do this: its runs hop the `', '` boundary BETWEEN two
// adjacent literals, so a backtick identifier sitting between an empty-string default and a
// `DEFAULT 'UTC'` — which is what any table with two TEXT-default columns produces — reads as
// "inside a literal" when it is plainly between them. Stripping the backtick-free literals first
// leaves only literals that actually contain a backtick, which is exactly the corrupting input.
// TestRewrite_AdjacentLiterals_AreNotAFalsePositive is that case.
var (
	backtickIdent       = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")
	plainLiteral        = regexp.MustCompile("'[^'`]*'")
	literalWithBacktick = regexp.MustCompile("'[^']*`[^']*'")
)

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
type literalBacktick struct {
	line int
	text string
}

func (e literalBacktick) Error() string {
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
			fmt.Fprint(os.Stderr, refusalMessage(displayPath(path), lit.line, lit.text))
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
// The whole file is inspected for the corrupting literal before any of it is rewritten — including
// the Atlas Down block that is about to be discarded. A literal that only appears down there still
// came from db/schema.hcl and will be back in the next migration; reporting it now is the cheap
// half of that conversation.
func rewrite(src string) (string, error) {
	lines := splitLines(src)

	for i, line := range lines {
		if hasBacktickInStringLiteral(line) {
			return "", literalBacktick{line: i + 1, text: line}
		}
	}

	var b strings.Builder

	for _, line := range lines {
		if line == gooseDownMarker {
			break
		}

		b.WriteString(backtickIdent.ReplaceAllString(line, `"${1}"`))
		b.WriteByte('\n')
	}

	b.WriteString(downBlock)

	return b.String(), nil
}

// hasBacktickInStringLiteral reports whether one LINE carries a backtick inside a single-quoted
// literal.
//
// Per line rather than per file, and that is not an optimisation: Go's `[^']` matches a newline
// where the grep this replaced could not, so a whole-file match would pair an opening quote on one
// line with a closing quote several lines below and refuse a file that is fine.
func hasBacktickInStringLiteral(line string) bool {
	return literalWithBacktick.MatchString(plainLiteral.ReplaceAllString(line, ""))
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
func refusalMessage(path string, line int, text string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\033[31m  %s:%d contains a backtick inside a string literal.\033[0m\n", path, line)
	fmt.Fprintf(&b, "      %s\n", strings.TrimSpace(text))
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
