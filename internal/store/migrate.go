package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/pressly/goose/v3"
	"modernc.org/sqlite"
)

// This file is where goose lives, and it lives here for one reason: goose.NewProvider takes a
// *sql.DB, and law 2 says internal/store is the only package that may hold one.
//
// The alternative — handing the write pool out to internal/migrate — would be a hole in the law
// exactly the size of the most dangerous code in the product. So the split is: this file owns every
// statement that reaches SQLite during an upgrade, and internal/migrate owns the policy around
// them (when to snapshot, what to check, when to restore, and what to tell the operator). That
// leaves internal/migrate testable against a fake, which is what lets the restore path be tested
// without corrupting a real database to get there.

// ErrNoPendingMigration is returned by ApplyNext when the database is already at the newest
// migration the binary carries. It is the normal loop-termination signal, not a failure.
var ErrNoPendingMigration = errors.New("no pending migration")

// Migration identifies one migration file in the embedded set.
type Migration struct {
	// Version is the numeric prefix: 1 for 000001_init.sql.
	Version int64
	// Source is the base filename. It is what an operator sees in a failure message and what they
	// quote in a bug report, so it is the filename and not a path into the embedded FS.
	Source string
}

// String renders "000001_init.sql (version 1)".
func (m Migration) String() string { return fmt.Sprintf("%s (version %d)", m.Source, m.Version) }

// Migrator applies an embedded migration set to this store's database, one migration at a time.
//
// One at a time is the whole design. goose's Up() would apply the entire pending set in a single
// call, and the boot sequence has to run PRAGMA integrity_check between each one — otherwise a
// corruption introduced by migration 7 is discovered after 8, 9 and 10 have also run on top of it,
// and the failure message names the wrong file.
type Migrator struct {
	store    *Store
	provider *goose.Provider
	sources  []Migration
}

// Migrator builds a migrator over the write pool for the given embedded migration set.
//
// The write pool, never the read pool: it is capped at one connection, so two processes racing to
// migrate the same file queue at the door rather than interleaving DDL.
func (s *Store) Migrator(fsys fs.FS) (*Migrator, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.write, fsys,
		// The global registry is how goose's package-level API shares state between callers. It is
		// process-wide mutable state, it would make two Migrators in one test binary interfere, and
		// nothing here uses Go migrations. Off.
		goose.WithDisableGlobalRegistry(true),
		// Out-of-order application would let a migration numbered below the current version apply
		// later — which is exactly what merging two branches that each added 000007 produces. It
		// must be an error, not a silent apply against a schema that never existed in that shape.
		goose.WithAllowOutofOrder(false),
	)
	if err != nil {
		return nil, fmt.Errorf("build migration provider: %w", err)
	}

	sources := make([]Migration, 0, len(provider.ListSources()))
	for _, src := range provider.ListSources() {
		sources = append(sources, Migration{Version: src.Version, Source: path.Base(src.Path)})
	}

	return &Migrator{store: s, provider: provider, sources: sources}, nil
}

// Sources returns every migration the binary carries, lowest version first.
func (m *Migrator) Sources() []Migration {
	out := make([]Migration, len(m.sources))
	copy(out, m.sources)

	return out
}

// MaxKnownVersion is the highest migration version this binary can apply. A database stamped above
// it was written by a newer binary and must not be touched — see internal/migrate.
func (m *Migrator) MaxKnownVersion() int64 {
	var maxVersion int64
	for _, src := range m.sources {
		if src.Version > maxVersion {
			maxVersion = src.Version
		}
	}

	return maxVersion
}

// DBVersion is the highest migration version recorded as applied in the database.
//
// A database with no goose bookkeeping table yet reports 0, which is correct: nothing has been
// applied. goose creates the table on first apply.
func (m *Migrator) DBVersion(ctx context.Context) (int64, error) {
	version, err := m.provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read applied schema version: %w", err)
	}

	return version, nil
}

// Pending returns the migrations not yet applied, lowest version first.
func (m *Migrator) Pending(ctx context.Context) ([]Migration, error) {
	statuses, err := m.provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read migration status: %w", err)
	}

	var pending []Migration

	for _, st := range statuses {
		if st.State == goose.StatePending {
			pending = append(pending, Migration{
				Version: st.Source.Version,
				Source:  path.Base(st.Source.Path),
			})
		}
	}

	return pending, nil
}

// ApplyNext applies the single lowest pending migration and reports which one it was. It returns
// ErrNoPendingMigration when there is nothing left to apply.
func (m *Migrator) ApplyNext(ctx context.Context) (Migration, error) {
	result, err := m.provider.UpByOne(ctx)
	if err != nil {
		if errors.Is(err, goose.ErrNoNextVersion) {
			return Migration{}, ErrNoPendingMigration
		}

		return Migration{}, fmt.Errorf("apply migration: %w", err)
	}

	return Migration{Version: result.Source.Version, Source: path.Base(result.Source.Path)}, nil
}

// ApplyAll applies every pending migration. It exists for the test template only, where the
// per-migration integrity checking the boot path does would be pure cost.
//
// The boot path must NOT use this: it needs to know which migration failed, and Up() reports the
// set rather than the file.
func (m *Migrator) ApplyAll(ctx context.Context) error {
	if _, err := m.provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}

// ApplySchema returns a SchemaFunc that applies the whole embedded migration set.
//
// This is the seam testing.go's SchemaFunc was left open for: the test template is now built from
// the real migrations rather than from a stand-in, so every integration test in the repository runs
// against the schema an officer's database actually has.
func ApplySchema(fsys fs.FS) SchemaFunc {
	return func(ctx context.Context, db *sql.DB) error {
		provider, err := goose.NewProvider(goose.DialectSQLite3, db, fsys,
			goose.WithDisableGlobalRegistry(true),
			goose.WithAllowOutofOrder(false),
		)
		if err != nil {
			return fmt.Errorf("build migration provider: %w", err)
		}

		if _, err := provider.Up(ctx); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}

		return nil
	}
}

// IntegrityCheck runs PRAGMA integrity_check and returns an error naming what it found.
//
// On a STRICT database this checks column CONTENT types as well as page structure, which is what
// makes it the cheapest available guard against a value of the wrong shape reaching a column — the
// reason canonical conventions §8 requires STRICT in the first place.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	rows, err := s.write.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("run integrity check: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var problems []string

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("scan integrity check: %w", err)
		}
		// A healthy database returns exactly one row containing "ok". Anything else is a list of
		// findings, and every one of them is reported: truncating to the first would hide the
		// extent of the damage in the one message an operator gets.
		if line != "ok" {
			problems = append(problems, line)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read integrity check: %w", err)
	}

	if len(problems) > 0 {
		return fmt.Errorf("integrity check failed: %s", strings.Join(problems, "; "))
	}

	return nil
}

// ForeignKeyCheck runs PRAGMA foreign_key_check.
//
// Separate from IntegrityCheck because SQLite's integrity_check does not validate foreign keys —
// docs/design/04-testing.md:536 requires both after every migration, and only that document says
// so. A migration that rebuilds a table and copies rows in the wrong order leaves dangling
// references that integrity_check calls healthy.
func (s *Store) ForeignKeyCheck(ctx context.Context) error {
	rows, err := s.write.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run foreign key check: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Any row at all is a violation. The columns are (table, rowid, parent, fkid); the count is
	// what the operator needs, and the rows themselves are not useful without the schema in front
	// of you.
	violations := 0
	for rows.Next() {
		violations++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read foreign key check: %w", err)
	}

	if violations > 0 {
		return fmt.Errorf("foreign key check failed: %d violation(s)", violations)
	}

	return nil
}

// RestoreForeignKeyEnforcement puts `PRAGMA foreign_keys` back ON for the write pool and verifies
// that it took.
//
// This exists because of one specific, verified leak. A migration annotated NO TRANSACTION — the only
// form in which a rebuild of a table that HAS CHILDREN can work, since SQLite ignores
// `PRAGMA foreign_keys` inside a transaction — runs its statements directly on a connection, and
// `PRAGMA foreign_keys = off` is CONNECTION state. The write pool is capped at one connection, so a
// migration that fails between the `off` and the `on`, or simply forgets the `on`, hands the pool
// back with foreign keys unenforced. Everything that ran afterwards on that connection, including
// the next migration, would apply with no referential integrity at all — silently, since nothing
// reports a pragma.
//
// Calling this after every migration makes that unrecoverable-by-inspection state impossible rather
// than merely discouraged. The DSN already sets the pragma for every connection either pool opens
// (see pragma.go); this restores the same invariant on a connection a migration perturbed.
//
// It is a no-op on the overwhelming majority of migrations, which never touch the pragma.
func (s *Store) RestoreForeignKeyEnforcement(ctx context.Context) error {
	if _, err := s.write.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("restore foreign key enforcement: %w", err)
	}

	// Read back rather than trust. A pragma issued inside a transaction is silently ignored, and
	// "the statement returned no error" is exactly the evidence that failure mode produces.
	var on int
	if err := s.write.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
		return fmt.Errorf("read back foreign key enforcement: %w", err)
	}

	if on != 1 {
		return errors.New("restore foreign key enforcement: the pragma did not take, so this " +
			"connection is applying writes with no referential integrity")
	}

	return nil
}

// AppendOnlyTrigger names one append-only trigger and the table whose history it protects.
type AppendOnlyTrigger struct {
	// Table is the table the trigger is attached to.
	Table string
	// Name is the trigger's name in sqlite_schema.
	Name string
}

// appendOnlyTriggers is the database half of the append-only invariant, written out.
//
// HARD-CODED, and deliberately not derived from the migration set. Deriving it would mean parsing
// SQL to decide what the SQL was supposed to say, which cannot fail in the one direction that
// matters: a migration that dropped a trigger would simply produce a smaller expectation and the
// check would agree with the damage. A literal list is a second, independent statement of what the
// ledger's guarantee IS, and the whole value of a runtime check is that it disagrees with the
// database rather than describing it.
//
// The cost is that adding a trigger means editing this list. That is not left to memory:
// TestAppendOnlyTriggers_Catalogue_MatchesAFreshInstall applies the real migration set and requires
// this list to be exactly the triggers on the ledger's two tables, so a trigger added to a migration
// and not to this list fails in internal/store rather than silently narrowing the boot check.
var appendOnlyTriggers = []AppendOnlyTrigger{
	{Table: "ledger_batch", Name: "trg_ledger_batch_no_delete"},
	{Table: "ledger_batch", Name: "trg_ledger_batch_no_update"},
	{Table: "ledger_entry", Name: "trg_ledger_entry_no_delete"},
	{Table: "ledger_entry", Name: "trg_ledger_entry_no_update"},
}

// AppendOnlyTriggers returns the catalogue above, sorted by table then name.
//
// A copy, because a package-level slice handed out by reference is one append away from being
// rewritten by a caller — and this particular list is a security guarantee, not a configuration.
func AppendOnlyTriggers() []AppendOnlyTrigger {
	out := make([]AppendOnlyTrigger, len(appendOnlyTriggers))
	copy(out, appendOnlyTriggers)

	return out
}

// MissingAppendOnlyTriggers returns the catalogued triggers this database does not have, in
// catalogue order.
//
// This is the third question the boot path asks after each migration, alongside integrity_check and
// foreign_key_check, and it is the one neither of those can answer. SQLite's DROP TABLE takes every
// trigger attached to the table and re-creates nothing, silently: a migration that rebuilds a ledger
// table and forgets the trigger re-creation passes both PRAGMAs, loses no row, and leaves a database
// whose history is editable. "Your ledger cannot be edited" is the product's entire trust argument
// and this is the only runtime verification of the database half of it.
//
// It reports a SET rather than pass/fail because the policy is not this package's to make — this
// file owns the statements an upgrade runs and internal/migrate owns what to do about the answers.
// The difference matters here more than usual: what internal/migrate does with this is compare the
// set before a migration with the set after it, so that a database which ARRIVED degraded can still
// be upgraded while a migration that degrades one is refused.
//
// It is strictly cheaper than integrity_check, which already runs at the same points: one read of
// sqlite_schema against a whole-file page scan.
//
// A trigger whose TABLE does not exist yet is not missing — it is early. The boot path runs this
// after every migration including the ones that precede the ledger's own, so a check that demanded
// trg_ledger_entry_no_update on a database that has not yet created ledger_entry would fail every
// fresh install on migration 1. Presence of the table is what makes its triggers required.
func (s *Store) MissingAppendOnlyTriggers(ctx context.Context) ([]string, error) {
	// sqlite_schema rows for the ledger's two tables: the tables themselves and everything attached
	// to them. One read rather than two, so the table set and the trigger set cannot be observed at
	// different moments.
	rows, err := s.write.QueryContext(ctx,
		`SELECT type, name, tbl_name FROM sqlite_schema
		 WHERE tbl_name IN ('ledger_batch', 'ledger_entry') AND type IN ('table', 'trigger')`)
	if err != nil {
		return nil, fmt.Errorf("read append-only triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		tables  = map[string]bool{}
		present = map[string]bool{}
	)

	for rows.Next() {
		var objType, name, tblName string
		if err := rows.Scan(&objType, &name, &tblName); err != nil {
			return nil, fmt.Errorf("scan append-only triggers: %w", err)
		}

		switch objType {
		case "table":
			tables[name] = true
		case "trigger":
			present[name] = true
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read append-only triggers: %w", err)
	}

	var missing []string

	for _, want := range appendOnlyTriggers {
		if tables[want.Table] && !present[want.Name] {
			missing = append(missing, want.Name)
		}
	}

	return missing, nil
}

// QuickCheck runs PRAGMA quick_check — integrity_check without the (expensive) index content
// verification. It is what a restored snapshot is validated with before the process trusts it.
func (s *Store) QuickCheck(ctx context.Context) error {
	var result string
	if err := s.write.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("run quick check: %w", err)
	}

	if result != "ok" {
		return fmt.Errorf("quick check failed: %s", result)
	}

	return nil
}

// VerifyDatabaseFile opens path READ-ONLY and runs PRAGMA quick_check.
//
// Read-only is the entire point, and it is not a precaution — it is a correctness requirement. A
// normal Open applies journal_mode=WAL, which rewrites bytes 18 and 19 of the SQLite header. The
// caller is internal/migrate verifying a freshly decompressed snapshot that it is about to move
// into place, and that file has to remain byte-identical to the snapshot it came from. Verifying it
// with a writing open would silently break the one guarantee the whole restore path is judged on.
//
// quick_check rather than integrity_check: this runs at the end of a failed upgrade with an
// operator waiting, and the difference is index-content verification on a file that VACUUM INTO
// wrote and nothing has touched since. docs/design/04-testing.md:536 asks for quick_check
// specifically on restored snapshots.
func VerifyDatabaseFile(ctx context.Context, path string) error {
	// Built here rather than through Open: Open is the production path and owns the pragmas that
	// make this unsuitable. `_pragma=query_only(1)` is belt and braces on top of mode=ro.
	connector, err := sqlite.NewConnector("file:" + path + "?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return fmt.Errorf("build read-only connector for %s: %w", path, err)
	}

	handle := sql.OpenDB(connector)
	defer func() { _ = handle.Close() }()

	var result string
	if err := handle.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("quick check %s: %w", path, err)
	}

	if result != "ok" {
		return fmt.Errorf("quick check %s: %s", path, result)
	}

	return nil
}

// Checkpoint flushes the write-ahead log into the main database file and truncates it.
//
// This is what makes a snapshot meaningful. Without it, VACUUM INTO still produces a correct
// database — but the source file on disk is missing everything still sitting in the -wal, so the
// "restore the file" story would depend on three files moving together instead of one.
func (s *Store) Checkpoint(ctx context.Context) error {
	var busy, walPages, checkpointed int
	if err := s.write.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &walPages, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint write-ahead log: %w", err)
	}

	// busy=1 means a reader held the WAL open and the checkpoint did not complete. Proceeding
	// would snapshot a file that is missing recent commits, so this is fatal rather than a warning.
	if busy != 0 {
		return errors.New("checkpoint write-ahead log: database busy, a reader is holding the WAL open")
	}

	return nil
}

// VacuumInto writes a compacted, fully-checkpointed copy of the database to dst.
//
// dst must not exist: SQLite refuses to overwrite, and that refusal is wanted. A snapshot path
// collision means two upgrades started in the same second, and silently overwriting the first one's
// pre-migration state is the one thing this whole mechanism exists to prevent.
func (s *Store) VacuumInto(ctx context.Context, dst string) error {
	// Bound parameter, so a path containing a quote cannot alter the statement.
	if _, err := s.write.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dst, err)
	}

	// VACUUM INTO creates its output at SQLite's default 0644 rather than inheriting the source
	// file's mode. The snapshot holds every password hash, PAT hash, TOTP seed and email address
	// the guild has, so it gets the same 0600 the live database does.
	if err := restrictMode(dst); err != nil {
		return err
	}

	return nil
}
