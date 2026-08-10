// Package migrations_test holds the migration suite: fresh install, downgrade refusal, and the
// snapshot/auto-restore path.
//
// It lives under test/ rather than beside internal/migrate for a reason that is a rule rather than
// a preference. docs/design/04-testing.md:541: "migration tests may not import domain packages.
// They run against SQL only, so a domain refactor can never silently rewrite what a historical
// migration meant." A test that asked internal/store what the schema is would agree with whatever
// internal/store believed on the day it ran; these tests read sqlite_schema directly and compare
// against a committed fingerprint, so they disagree when the schema changes and someone has to say
// why.
//
// Two imports are deliberate exceptions and neither is a domain package: `db` for the embedded
// migration set (the artefact under test) and `internal/migrate` (the code under test).
package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/prokopto-dev/dragonkillparty/db"
)

// Fixture migrations, named as constants so that renaming one breaks the build rather than
// silently skipping a test at runtime.
const (
	// brokenFixture leaves the database failing PRAGMA integrity_check.
	brokenFixture = "../fixtures/migrations/broken/000002_broken_integrity.sql"
	// futureFixture is a valid migration a later release would carry, used to stamp a database
	// above what a given binary understands.
	futureFixture = "../fixtures/migrations/future/000002_future_table.sql"
	// ledgerRebuildFixture is a CORRECT SQLite 12-step rebuild of ledger_entry: it re-creates the
	// table's four indexes AND its two append-only triggers after the rename, as
	// .claude/rules/migrations.md requires of any rebuild of a table carrying a trigger.
	ledgerRebuildFixture = "../fixtures/migrations/rebuild/000002_ledger_entry_rebuild.sql"
	// ledgerRebuildNoTriggersFixture is that same rebuild with the trigger re-creation missing. It
	// is the negative control: without it, "the triggers still fire after an upgrade" is an
	// assertion nobody has ever seen fail.
	ledgerRebuildNoTriggersFixture = "../fixtures/migrations/rebuild/000002_ledger_entry_rebuild_no_triggers.sql"
	// batchRebuildFixture is a correct rebuild of ledger_batch — the PARENT table — done the only
	// way that works on a populated database: outside goose's transaction, so that the
	// `PRAGMA foreign_keys = off` Atlas emits is not silently ignored.
	batchRebuildFixture = "../fixtures/migrations/rebuild/000002_ledger_batch_rebuild.sql"
	// batchRebuildInTransactionFixture is that same rebuild exactly as Atlas generates it, inside
	// goose's transaction. It FAILS on any populated database and passes on an empty one, which is
	// the "works on fresh install, breaks on upgrade" class .claude/rules/migrations.md calls the
	// most damaging bug this product can ship.
	batchRebuildInTransactionFixture = "../fixtures/migrations/rebuild/000002_ledger_batch_rebuild_in_transaction.sql"
	// dropLedgerEntryFixture removes a ledger table and does not put it back. It is the way THROUGH
	// a trigger check that only counts triggers: a trigger on a table that does not exist is
	// vacuously present, so this destroys every entry and reports nothing missing.
	dropLedgerEntryFixture = "../fixtures/migrations/drop/000002_drop_ledger_entry.sql"
)

// gooseUpStatements returns the executable statements in a migration's Up block.
//
// It exists so a test can apply a fixture DIRECTLY to a database, bypassing internal/migrate
// entirely. That is the only way left to demonstrate what a forgetful rebuild does to SQLITE, as
// opposed to what the boot path now does about it: once the runner refuses such a migration, running
// the fixture through the runner can no longer show the state it produces.
//
// Comment lines are dropped, including goose's annotations — the caller is not goose and has no
// transaction to opt out of. BEGIN … END blocks are kept whole, because a trigger body contains
// semicolons and splitting on them would cut one in half and fail in a way that looked like SQLite's
// fault.
func gooseUpStatements(tb testing.TB, path string) []string {
	tb.Helper()

	body, err := os.ReadFile(path)
	require.NoError(tb, err, "read migration fixture %s", path)

	var (
		sql   []string
		inUp  bool
		lines = strings.Split(string(body), "\n")
	)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "-- +goose Up"):
			inUp = true

			continue
		case strings.HasPrefix(trimmed, "-- +goose Down"):
			inUp = false

			continue
		case !inUp, trimmed == "", strings.HasPrefix(trimmed, "--"):
			continue
		}

		sql = append(sql, trimmed)
	}

	require.NotEmpty(tb, sql, "%s has no statements in its Up block", path)

	var (
		out     []string
		current strings.Builder
	)

	for _, line := range sql {
		if current.Len() > 0 {
			current.WriteString(" ")
		}

		current.WriteString(line)

		if !strings.HasSuffix(line, ";") {
			continue
		}

		// A statement that opened a BEGIN block is finished by END;, not by the first semicolon.
		stmt := current.String()
		if strings.Contains(strings.ToUpper(stmt), " BEGIN ") && !strings.HasSuffix(strings.ToUpper(stmt), "END;") {
			continue
		}

		out = append(out, strings.TrimSuffix(stmt, ";"))
		current.Reset()
	}

	require.Zero(tb, current.Len(), "%s ends mid-statement: %q", path, current.String())

	return out
}

// snapshotNames lists the snapshot files in dir, sorted. A missing directory is an empty list, not
// an error: "no snapshots were taken" is a normal and frequently asserted outcome.
func snapshotNames(tb testing.TB, dir string) []string {
	tb.Helper()

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(tb, err, "read snapshot directory %s", dir)

	var names []string

	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	sort.Strings(names)

	return names
}

// realMigrations returns the embedded production migration set.
func realMigrations(tb testing.TB) fs.FS {
	tb.Helper()

	fsys, err := db.SQLiteMigrations()
	require.NoError(tb, err, "the embedded migration set could not be rooted — did db/embed.go's go:embed pattern change?")

	return fsys
}

// migrationDir materialises the embedded migration set into a directory on disk, plus any extra
// fixture files given as paths, and returns an fs.FS over it.
//
// On-disk rather than an in-memory FS because the extra files are real fixtures read from the
// repository, and because a failure is far easier to investigate when the directory the migrator
// actually walked is sitting in the test's temp dir.
//
// Each extra fixture is RENUMBERED to sit one past the highest real migration, in the order given.
// The fixtures represent "the next migration a broken or future release would carry", which is a
// version RELATIVE to the current release, not an absolute number — so when a real migration lands
// (PR 5a's 000002_guild, PR 9's ledger, ...) the fixtures must move with it. Renumbering here keeps
// the fixture files under test/fixtures byte-identical and CODEOWNERS-clean while making the tests
// immune to the next migration's number. fixtureName reports the renumbered name a test asserts on.
func migrationDir(tb testing.TB, extra ...string) fs.FS {
	tb.Helper()

	return migrationDirUpTo(tb, 0, extra...)
}

// migrationDirUpTo is migrationDir truncated to the real migrations at or below maxVersion. A
// maxVersion of 0 means all of them, which is what migrationDir asks for.
//
// It exists so a test can stand a database up at an INTERMEDIATE release, put data in it, and then
// walk the remaining real migrations forward one at a time. Every other test in this package
// applies the whole set to an empty database, which is the fresh-install path; the upgrade path is
// a different question and it is the one that has to hold on a populated ledger.
//
// Non-migration files (atlas.sum, which versionOf reports as 0) are copied whatever the cap is:
// they are not migrations and truncating them would change what the directory means.
func migrationDirUpTo(tb testing.TB, maxVersion int, extra ...string) fs.FS {
	tb.Helper()

	dir := tb.TempDir()

	entries, err := fs.ReadDir(realMigrations(tb), ".")
	require.NoError(tb, err, "read the embedded migration set")
	require.NotEmpty(tb, entries, "the embedded migration set is empty — db/migrations-sqlite/ has no .sql files")

	var highest int

	for _, entry := range entries {
		if v := versionOf(entry.Name()); maxVersion > 0 && v > maxVersion {
			continue
		}

		body, err := fs.ReadFile(realMigrations(tb), entry.Name())
		require.NoError(tb, err, "read embedded migration %s", entry.Name())
		require.NoError(tb, os.WriteFile(filepath.Join(dir, entry.Name()), body, 0o644),
			"materialise embedded migration %s", entry.Name())

		if v := versionOf(entry.Name()); v > highest {
			highest = v
		}
	}

	for i, src := range extra {
		body, err := os.ReadFile(src)
		require.NoError(tb, err, "read fixture migration %s", src)
		require.NoError(tb, os.WriteFile(filepath.Join(dir, renumberedFixtureName(highest+1+i, src)), body, 0o644),
			"copy fixture migration %s", src)
	}

	return os.DirFS(dir)
}

// realMigrationVersions lists the versions in the embedded set, lowest first. Non-migration files
// are excluded.
func realMigrationVersions(tb testing.TB) []int {
	tb.Helper()

	entries, err := fs.ReadDir(realMigrations(tb), ".")
	require.NoError(tb, err, "read the embedded migration set")

	var versions []int

	for _, entry := range entries {
		if v := versionOf(entry.Name()); v > 0 {
			versions = append(versions, v)
		}
	}

	require.NotEmpty(tb, versions, "the embedded migration set contains no numbered migrations")
	sort.Ints(versions)

	return versions
}

// versionOf extracts the numeric version prefix from a migration filename, or 0 if it has none
// (atlas.sum and any non-migration file).
func versionOf(name string) int {
	base := filepath.Base(name)

	digits := 0
	for digits < len(base) && base[digits] >= '0' && base[digits] <= '9' {
		digits++
	}

	if digits == 0 {
		return 0
	}

	v := 0
	for i := range digits {
		v = v*10 + int(base[i]-'0')
	}

	return v
}

// renumberedFixtureName replaces a fixture's version prefix with version, zero-padded to six digits,
// keeping the descriptive suffix. "000002_broken_integrity.sql" at version 3 becomes
// "000003_broken_integrity.sql". A fixture name always has an underscore after its digits; a name
// that somehow does not keeps its whole self as the suffix, which fails loudly downstream rather than
// silently misnumbering.
func renumberedFixtureName(version int, src string) string {
	base := filepath.Base(src)

	suffix := base
	if i := strings.IndexByte(base, '_'); i > 0 {
		suffix = base[i:]
	}

	return fmt.Sprintf("%06d%s", version, suffix)
}

// fixtureName reports the on-disk name a fixture is materialised under by migrationDir: its version
// renumbered to one past the highest real migration, plus the given index for multiple fixtures.
// A test that asserts which migration failed uses this rather than a hard-coded 000002, so the
// assertion tracks the fixture as real migrations accumulate ahead of it.
func fixtureName(tb testing.TB, src string, index int) string {
	tb.Helper()

	entries, err := fs.ReadDir(realMigrations(tb), ".")
	require.NoError(tb, err, "read the embedded migration set")

	var highest int
	for _, entry := range entries {
		if v := versionOf(entry.Name()); v > highest {
			highest = v
		}
	}

	return renumberedFixtureName(highest+1+index, src)
}

// openRaw opens a database directly, with no help from internal/store.
//
// Deliberate: these tests assert what is IN the file, and routing that through the package whose
// behaviour they are checking would let a change to internal/store move the answer. SQL001 and
// SQL002 in scripts/repo-gates.sh scan `internal` and `cmd`, not `test`, precisely so this is
// possible here and nowhere else.
func openRaw(tb testing.TB, path string) *sql.DB {
	tb.Helper()

	handle, err := sql.Open("sqlite", "file:"+path)
	require.NoError(tb, err, "open %s", path)
	tb.Cleanup(func() { require.NoError(tb, handle.Close(), "close %s", path) })

	return handle
}

// withRawFK is openRaw with foreign keys enforced and a deterministic close.
//
// Foreign keys, because SQLite defaults the pragma OFF per connection: openRaw's handle will
// happily insert a ledger_entry pointing at a batch that does not exist. A test that seeds "a real
// ledger" through that handle is seeding rows the production connection (internal/store/pragma.go
// turns the pragma on for every connection in both pools) would have rejected, and would keep
// passing after a migration copied rows in an order that broke the references.
//
// Deterministic close — the handle is gone before this returns, rather than at the end of the test
// — because the caller interleaves reads with migrations. The migrator takes a VACUUM INTO snapshot
// and runs DDL on every step, and leaving one idle handle per step alive until cleanup is how a
// suite acquires a lock-contention flake that gets blamed on the migration under test.
func withRawFK(tb testing.TB, path string, fn func(handle *sql.DB)) {
	tb.Helper()

	handle, err := sql.Open("sqlite", fkDSN(path))
	require.NoError(tb, err, "open %s with foreign keys on", path)

	defer func() { require.NoError(tb, handle.Close(), "close %s", path) }()

	requireForeignKeysOn(tb, handle)
	fn(handle)
}

// fkDSN is the one place the foreign-keys DSN is spelled.
func fkDSN(path string) string { return "file:" + path + "?_pragma=foreign_keys(1)" }

// applyStatements executes statements against ONE connection, in order.
//
// One connection, not the pool, and that is the whole reason this helper exists rather than a loop
// over handle.ExecContext. A 12-step rebuild begins with `PRAGMA foreign_keys = off`, and a pragma
// is CONNECTION state: run through a pool, the pragma can land on one connection and the DROP it
// was meant to protect on another. The result is a test that passes or fails depending on which
// connection database/sql happened to hand out, which is the worst kind of migration test.
func applyStatements(tb testing.TB, handle *sql.DB, statements []string) {
	tb.Helper()

	conn, err := handle.Conn(context.Background())
	require.NoError(tb, err, "take a single connection")

	defer func() { require.NoError(tb, conn.Close()) }()

	for _, stmt := range statements {
		_, execErr := conn.ExecContext(context.Background(), stmt)
		require.NoError(tb, execErr, "apply statement: %s", stmt)
	}
}

// requireForeignKeysOn verifies the pragma rather than assuming it: a typo in the DSN is accepted
// silently by the driver and leaves the constraints off, which is the one failure these helpers
// exist to rule out.
func requireForeignKeysOn(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	var on int
	require.NoError(tb, handle.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&on),
		"read back PRAGMA foreign_keys")
	require.Equal(tb, 1, on,
		"foreign_keys is OFF on this handle — the DSN did not take, and every FK assertion made "+
			"through it would pass vacuously")
}

// schemaFingerprint is the normalised SHA-256 of everything in sqlite_schema.
//
// Every row type is included — table, index, trigger and view. Triggers especially: Atlas cannot
// express them and a 12-step table rebuild DROPs the table they hang off without recreating them,
// silently (item V6 in docs/development/verify-before-phase-0.md). This fingerprint is the only
// mechanism in the repository that notices, and PR 9's append-only ledger guarantee rests on it.
//
// goose's own bookkeeping table is excluded: its DDL is goose's to change across a dependency
// bump, and a version bump of a library is not a schema change to this product.
func schemaFingerprint(tb testing.TB, handle *sql.DB) string {
	tb.Helper()

	rows, err := handle.QueryContext(context.Background(),
		`SELECT type, name, COALESCE(tbl_name, ''), COALESCE(sql, '')
		 FROM sqlite_schema
		 WHERE name NOT LIKE 'sqlite_%' AND tbl_name <> 'goose_db_version'`)
	require.NoError(tb, err, "read sqlite_schema")
	defer func() { require.NoError(tb, rows.Close()) }()

	var lines []string

	for rows.Next() {
		var objType, name, tblName, ddl string
		require.NoError(tb, rows.Scan(&objType, &name, &tblName, &ddl), "scan sqlite_schema row")
		lines = append(lines, objType+"\x1f"+name+"\x1f"+tblName+"\x1f"+normaliseDDL(ddl))
	}

	require.NoError(tb, rows.Err(), "iterate sqlite_schema")
	require.NotEmpty(tb, lines, "sqlite_schema is empty — did the migrations apply at all?")

	// Sorted in Go, not by SQL: sqlite_schema's natural order depends on creation order, so a
	// migration that created the same objects in a different sequence would change the fingerprint
	// without changing the schema.
	sort.Strings(lines)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))

	return hex.EncodeToString(sum[:])
}

// normaliseDDL collapses whitespace so that a reformatting of the generated SQL is not mistaken
// for a schema change. Identifier quoting is NOT normalised: `x` and "x" mean the same thing to
// SQLite but not to sqlc, and gate MIG002 exists because that difference is load-bearing here.
func normaliseDDL(ddl string) string { return strings.Join(strings.Fields(ddl), " ") }

// fileSHA256 is the hash of a file's bytes, which is what "byte-identical" means in the acceptance
// criterion this file's headline test exists to satisfy.
func fileSHA256(tb testing.TB, path string) string {
	tb.Helper()

	f, err := os.Open(path)
	require.NoError(tb, err, "open %s for hashing", path)
	defer func() { require.NoError(tb, f.Close()) }()

	h := sha256.New()
	_, err = io.Copy(h, f)
	require.NoError(tb, err, "hash %s", path)

	return hex.EncodeToString(h.Sum(nil))
}

// seedRows writes recognisable rows so that a restore which produced an empty-but-valid database
// would fail rather than pass.
func seedRows(tb testing.TB, handle *sql.DB, n int) {
	tb.Helper()

	for i := range n {
		_, err := handle.ExecContext(context.Background(),
			`INSERT INTO dkp_meta (key, value, updated_at) VALUES (?, ?, ?)`,
			fmt.Sprintf("seed:%03d", i), fmt.Sprintf("value-%03d", i), int64(1_700_000_000_000_000+i))
		require.NoError(tb, err, "seed dkp_meta row %d", i)
	}
}

// countSeedRows is the "did the data survive" check. A restore that yields a valid but empty
// database satisfies every structural assertion and is still a total loss.
//
// It counts only the seeded rows, on purpose. dkp_meta also holds the boot path's own bookkeeping —
// schema_version and binary_version — so a plain count(*) would move whenever that bookkeeping
// changed, and a test whose expected number depends on an unrelated implementation detail is a test
// somebody eventually "fixes" by editing the number.
func countSeedRows(tb testing.TB, handle *sql.DB) int {
	tb.Helper()

	var n int
	require.NoError(tb,
		handle.QueryRowContext(context.Background(),
			`SELECT count(*) FROM dkp_meta WHERE key LIKE 'seed:%'`).Scan(&n),
		"count seeded rows in dkp_meta")

	return n
}
