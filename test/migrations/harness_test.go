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
)

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
// files given as paths, and returns an fs.FS over it.
//
// On-disk rather than an in-memory FS because the extra files are real fixtures read from the
// repository, and because a failure is far easier to investigate when the directory the migrator
// actually walked is sitting in the test's temp dir.
func migrationDir(tb testing.TB, extra ...string) fs.FS {
	tb.Helper()

	dir := tb.TempDir()

	entries, err := fs.ReadDir(realMigrations(tb), ".")
	require.NoError(tb, err, "read the embedded migration set")
	require.NotEmpty(tb, entries, "the embedded migration set is empty — db/migrations-sqlite/ has no .sql files")

	for _, entry := range entries {
		body, err := fs.ReadFile(realMigrations(tb), entry.Name())
		require.NoError(tb, err, "read embedded migration %s", entry.Name())
		require.NoError(tb, os.WriteFile(filepath.Join(dir, entry.Name()), body, 0o644),
			"materialise embedded migration %s", entry.Name())
	}

	for _, src := range extra {
		body, err := os.ReadFile(src)
		require.NoError(tb, err, "read fixture migration %s", src)
		require.NoError(tb, os.WriteFile(filepath.Join(dir, filepath.Base(src)), body, 0o644),
			"copy fixture migration %s", src)
	}

	return os.DirFS(dir)
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
