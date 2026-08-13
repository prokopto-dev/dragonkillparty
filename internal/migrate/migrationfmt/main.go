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
// THAT SCANNER NOW LIVES IN internal/migrate/sqlscan, because MIG001 and MIG002 ask it the same two
// questions this command does (issue #138): where the Down block starts, and whether a backtick is
// data or an identifier quote. The gate used to answer the second one with a pattern that fired on
// every backtick, which made the refusal below untrue — it offers a hand-fix path the gate then
// refused to let anyone land. One scanner, two callers, is the only version of that agreement
// nobody has to maintain.
//
// It lives beside internal/migrate, which applies migrations at boot, rather than in cmd/dkp, for
// the reason internal/ledger/enumgen gives: cmd/dkp is the product binary and an officer never runs
// a code generator. It imports the standard library and one package of this repository's own, so a
// tree whose generated code does not build can still author a migration.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate/sqlscan"
)

// downBlock replaces everything from sqlscan.DownMarker onwards.
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

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: migrationfmt <migration.sql>")
		os.Exit(2)
	}

	path := os.Args[1]

	if err := run(path); err != nil {
		var lit sqlscan.LiteralBacktick
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

// rewrite returns the committed form of one generated migration, or
// sqlscan.ErrBacktickInStringLiteral.
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
	scanned, err := sqlscan.RewriteBackticks(src)
	if err != nil {
		return "", err
	}

	var b strings.Builder

	for _, line := range splitLines(scanned) {
		if line == sqlscan.DownMarker {
			break
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	b.WriteString(downBlock)

	return b.String(), nil
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
func refusalMessage(path string, lit sqlscan.LiteralBacktick) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\033[31m  %s:%d contains a backtick inside a string literal.\033[0m\n", path, lit.Line)
	fmt.Fprintf(&b, "      %s\n", strings.TrimSpace(lit.Text))

	// Only when they differ, and then it is the whole explanation: the reader is looking at a line
	// whose backtick is data because a quote several lines above is still open.
	if lit.OpenedAt != lit.Line {
		fmt.Fprintf(&b, "  The literal opened at line %d and has not closed.\n", lit.OpenedAt)
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
