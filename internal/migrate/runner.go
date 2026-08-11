package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// Config is everything the boot path needs to know.
type Config struct {
	// DBPath is the SQLite file, from DKP_DB_PATH.
	DBPath string
	// DataDir is the root that holds backups/, from DKP_DATA_DIR.
	DataDir string
	// BinaryVersion is this build's version. It names the snapshot and is recorded into dkp_meta so
	// that a later, older binary can tell an operator which image tag to run.
	BinaryVersion string
	// AutoMigrate is DKP_AUTO_MIGRATE. When false, pending migrations are reported rather than
	// applied and /readyz returns 503 with the command to run.
	AutoMigrate bool
	// Clock is injected. Nil means clock.System.
	Clock clock.Clock
}

// Runner performs the boot-time upgrade.
//
// It owns opening and closing the database around each operation rather than borrowing a handle,
// because the restore path has to replace the file — which is only safe once every handle on it is
// closed. Making that lifecycle the Runner's removes the possibility of a caller holding one open.
type Runner struct {
	fsys fs.FS
	cfg  Config
}

// New builds a Runner over an embedded migration set.
func New(fsys fs.FS, cfg Config) (*Runner, error) {
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("migrate: %w", store.ErrNoDatabasePath)
	}

	if cfg.DataDir == "" {
		return nil, errors.New("migrate: no data directory (DKP_DATA_DIR)")
	}

	if cfg.BinaryVersion == "" {
		// Not cosmetic. The version names the snapshot and is what the downgrade refusal quotes
		// back at an operator; an empty one produces `pre--20260806T…db.zst` and a message telling
		// them to run an image with no tag.
		return nil, errors.New("migrate: no binary version")
	}

	if cfg.Clock == nil {
		cfg.Clock = clock.System{}
	}

	return &Runner{fsys: fsys, cfg: cfg}, nil
}

// Status reports where the database is relative to this binary, and whether the ledger still has its
// database-level append-only protection, without changing anything.
//
// This is what /readyz reads. It opens and closes its own connection every call, which is
// affordable because /readyz is polled by a load balancer at human intervals and because the
// alternative — a long-lived handle — would put a *sql.DB in the readiness path that the restore
// path would then have to coordinate with.
func (r *Runner) Status(ctx context.Context) (Status, error) {
	s, err := store.Open(ctx, r.cfg.DBPath)
	if err != nil {
		return Status{}, fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil {
			slog.ErrorContext(ctx, "close database after status", "error", closeErr)
		}
	}()

	return r.status(ctx, s)
}

func (r *Runner) status(ctx context.Context, s *store.Store) (Status, error) {
	migrator, err := s.Migrator(r.fsys)
	if err != nil {
		return Status{}, err
	}

	applied, err := migrator.DBVersion(ctx)
	if err != nil {
		return Status{}, err
	}

	latest := migrator.MaxKnownVersion()

	// Recorded separately from the version, and read even when nothing is pending: it is only
	// useful in the one case where the binary cannot migrate at all.
	wroteBy, err := s.MetaValue(ctx, MetaKeyBinaryVersion)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Status{}, err
	}

	st := Status{Applied: applied, Latest: latest, WroteBy: wroteBy}

	switch {
	case applied > latest:
		st.State = StateAhead
	case applied == latest:
		st.State = StateUpToDate
	default:
		st.State = StatePending
	}

	// A database written by a newer binary is not asked what is pending: this one cannot name
	// migrations it does not carry, and the answer is not used — StateAhead is fatal at boot and
	// failed on /readyz.
	if st.State != StateAhead {
		pending, pendingErr := migrator.Pending(ctx)
		if pendingErr != nil {
			return Status{}, pendingErr
		}

		st.Pending = pending

		// Pending is authoritative over the version arithmetic above. Out-of-order migration numbers
		// from a bad branch merge can leave applied == latest with a gap in the middle, and reporting
		// "up to date" in that situation is how a migration gets skipped forever.
		if len(pending) > 0 {
			st.State = StatePending
		}
	}

	// Read last, because the table half of the answer depends on the state above being final.
	st.Protection = protection(ctx, s, st.State)

	return st, nil
}

// protection reads the ledger's database-level append-only guarantee out of sqlite_schema.
//
// It never fails. An unreadable answer is reported as Protection.Err, because every caller has to
// degrade to "unknown" rather than to "intact" or to a refusal: /readyz reports failed, and the boot
// path warns and starts. Returning an error here instead would make a database nobody can boot out of
// damage that predates the binary, which is exactly the outcome #39 decided against.
//
// The cost is one indexed read of sqlite_schema on a connection Status has already opened — strictly
// cheaper than the goose bookkeeping queries beside it, which is the property that matters: /readyz is
// polled forever, so a check that is not cheap is a check that gets removed.
func protection(ctx context.Context, s *store.Store, state State) Protection {
	found, err := s.AppendOnlyState(ctx)
	if err != nil {
		return Protection{Err: err}
	}

	p := Protection{MissingTriggers: found.MissingTriggers}

	// The table half is asserted only against a database that has applied everything this binary
	// carries. Anywhere else an absent ledger table is early rather than gone — see Protection.
	if state != StateUpToDate {
		return p
	}

	for _, table := range ledgerTables() {
		if !slices.Contains(found.Tables, table) {
			p.MissingTables = append(p.MissingTables, table)
		}
	}

	return p
}

// ledgerTables is the catalogued ledger tables, in catalogue order.
//
// Derived from internal/store's trigger catalogue rather than written out again here: that list is
// the repository's one independent statement of what the ledger's guarantee IS, and a second copy of
// it in a second package is a copy that can disagree.
func ledgerTables() []string {
	var out []string

	for _, trigger := range store.AppendOnlyTriggers() {
		if !slices.Contains(out, trigger.Table) {
			out = append(out, trigger.Table)
		}
	}

	return out
}

// Migrate runs the boot sequence: read the version, refuse a downgrade, snapshot, apply one
// migration at a time checking after each, and restore automatically on any failure.
//
// It returns nil when there was nothing to do AND when migrations are pending but DKP_AUTO_MIGRATE
// is false — in that second case the process is expected to serve, with /readyz reporting 503 and
// the command to run. A pending migration is not an error; it is a state.
func (r *Runner) Migrate(ctx context.Context) error {
	// Before the database is opened, and before anything else: finish a restore that a previous
	// boot was killed in the middle of. Opening first would create a WAL beside a file that is
	// about to be replaced, and would let the version check below read a half-restored database.
	if err := r.resumeRestore(ctx); err != nil {
		return err
	}

	s, err := store.Open(ctx, r.cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	// closed guards against the double-close that would otherwise happen on the restore path,
	// where the store must be closed early so the file can be replaced.
	closed := false
	closeStore := func() {
		if closed {
			return
		}

		closed = true

		if closeErr := s.Close(); closeErr != nil {
			slog.ErrorContext(ctx, "close database after migrate", "error", closeErr)
		}
	}
	defer closeStore()

	st, err := r.status(ctx, s)
	if err != nil {
		return err
	}

	if st.State == StateAhead {
		return &SchemaAheadError{
			Applied:   st.Applied,
			Latest:    st.Latest,
			WroteBy:   st.WroteBy,
			BackupDir: r.backupDir(),
		}
	}

	// Before anything is applied, and on every boot including the ones with nothing to do: does this
	// database still have its append-only protection?
	//
	// This only warns. Failing here would refuse to start a database that arrived already degraded —
	// from a past upgrade, a fork's build, or a support session with a SQLite client — and locking
	// an officer out of their guild's site over damage that is already done helps nobody, least of
	// all at 1 a.m. What the loop below refuses is a migration that makes it WORSE, which is a thing
	// this boot did and can undo.
	//
	// The answer comes from the status read above rather than from a second query, and the same answer
	// is what /readyz now reports on every probe (#59) — one read, two audiences, no chance of the log
	// and the endpoint disagreeing.
	r.warnIfDegraded(ctx, st.Protection)

	if len(st.Pending) == 0 {
		return r.recordVersion(ctx, s, st.Applied)
	}

	if !r.cfg.AutoMigrate {
		slog.WarnContext(ctx, "migrations pending and automatic migration is disabled",
			"pending", len(st.Pending), "applied", st.Applied, "latest", st.Latest,
			"command", "dkp migrate")

		return nil
	}

	snapshot, err := r.snapshot(ctx, s, r.cfg.Clock.Now())
	if err != nil {
		return fmt.Errorf("take pre-migration snapshot: %w", err)
	}

	slog.InfoContext(ctx, "pre-migration snapshot taken",
		"path", snapshot, "pending", len(st.Pending), "from_version", st.Applied)

	migrator, err := s.Migrator(r.fsys)
	if err != nil {
		return err
	}

	// The baseline the append-only check below compares against: which ledger tables exist and which
	// of their triggers are already absent, before any of this boot's migrations run.
	//
	// Read here rather than at the warn above, and its failure is fatal rather than logged. Nothing
	// has been applied yet, so returning is safe and needs no restore — and proceeding on a baseline
	// nobody could establish is the one option that must not be available: it would make every
	// comparison below vacuous on exactly the database too damaged to read, which is the database
	// that most needs them.
	guard, err := s.AppendOnlyState(ctx)
	if err != nil {
		return fmt.Errorf("read the ledger's append-only state before migrating: %w", err)
	}

	// One at a time, with all three checks after each. A bulk apply would discover the corruption
	// after the migrations that followed it had also run, and would name the wrong file — and the
	// file name is the entire actionable content of the message an operator gets.
	for {
		applied, applyErr := migrator.ApplyNext(ctx)
		if errors.Is(applyErr, store.ErrNoPendingMigration) {
			break
		}

		if applyErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, applyErr)
		}

		// First, before anything is checked: put foreign-key enforcement back.
		//
		// A migration annotated NO TRANSACTION — which is what a rebuild of a table with children
		// has to be, because SQLite ignores the pragma inside a transaction — runs its statements
		// straight on the write pool's single connection, and `PRAGMA foreign_keys = off` is
		// connection state. One that forgets to turn it back on leaves every LATER migration in this
		// same boot applying with no referential integrity, silently, because nothing reports a
		// pragma. Re-asserting it here makes the pattern in .claude/rules/migrations.md safe to
		// follow rather than merely documented.
		if checkErr := s.RestoreForeignKeyEnforcement(ctx); checkErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, checkErr)
		}

		if checkErr := s.IntegrityCheck(ctx); checkErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, checkErr)
		}

		// Separate from integrity_check, which does not validate foreign keys. A rebuild that
		// copied rows in the wrong order leaves dangling references integrity_check calls healthy.
		if checkErr := s.ForeignKeyCheck(ctx); checkErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, checkErr)
		}

		// And the question the other two cannot ask: are the append-only triggers still there?
		//
		// A migration that rebuilds a ledger table and forgets to re-create its triggers passes both
		// checks above, loses no row, and hands back a database whose history is editable — the
		// failure mode .claude/rules/migrations.md warns about, reproduced in
		// test/fixtures/migrations/rebuild/. It is fatal for the same reason the other two are: the
		// pre-migration snapshot is still on disk, and restoring it is strictly better than serving
		// a ledger that no longer refuses to be rewritten.
		//
		// The comparison is against the state BEFORE this migration, not against the full catalogue,
		// and that is what keeps the check from being a trap. A database that arrived already
		// missing a trigger would otherwise fail this check on the first migration it was ever
		// offered — the officer's upgrade path would be closed for good by damage that predates the
		// binary, and the only escape would be the flag that turns migrations off. What is refused
		// here is a migration that LOST something that was present when it started.
		//
		// Both halves of that state are compared, and the table half is not decoration. A trigger on
		// a table that does not exist is vacuously fine — that exemption is what lets a fresh
		// install apply migration 000001 without demanding triggers for tables migration 000003
		// creates. Without the table comparison it is also a way straight through the check: a
		// migration that DROPS ledger_entry takes the rows and both triggers with it, dangles no
		// foreign key, corrupts no page, and would leave nothing "missing" to report.
		found, checkErr := s.AppendOnlyState(ctx)
		if checkErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, checkErr)
		}

		// Tables that were there before this migration and are not there now.
		if dropped := notIn(guard.Tables, found.Tables); len(dropped) > 0 {
			return r.failed(ctx, s, closeStore, applied, snapshot, tablesDroppedError(dropped))
		}

		// Triggers missing now that were not missing before.
		if lost := notIn(found.MissingTriggers, guard.MissingTriggers); len(lost) > 0 {
			return r.failed(ctx, s, closeStore, applied, snapshot, triggersLostError(lost))
		}

		guard = found

		slog.InfoContext(ctx, "migration applied", "migration", applied.Source, "version", applied.Version)
	}

	final, err := migrator.DBVersion(ctx)
	if err != nil {
		return err
	}

	return r.recordVersion(ctx, s, final)
}

// warnIfDegraded reports a database that arrived without its append-only protection, and does not
// stop the boot.
//
// The asymmetry with the migration loop is the whole design, and it is a decision rather than an
// oversight. Inside the loop, a missing trigger means a migration THIS boot applied destroyed the
// guarantee: the culprit has a name, the pre-migration snapshot exists, and restoring is both
// possible and obviously right. Here, the damage predates this boot — there is no snapshot to go
// back to and no migration to name — so the choice is between a loud log and a site an officer
// cannot start. It logs at error level because the ledger really is editable and somebody has to
// see it; a guild whose site refuses to boot at 1 a.m. would learn nothing more and lose the raid.
//
// A failure to ASK the question is reported as itself rather than as a missing trigger, and neither
// stops the boot: this is a diagnostic on a path that has not been asked to do anything yet. The
// migration loop reads the same state again as its baseline, and there the read failing IS fatal —
// see Migrate.
//
// This log is no longer the only place the answer appears. /readyz reports the same Protection on
// every probe, which is what #59 asked for: one line at 1 a.m. is a detection, not a notification.
func (r *Runner) warnIfDegraded(ctx context.Context, p Protection) {
	if p.Err != nil {
		slog.WarnContext(ctx, "could not verify the ledger's append-only triggers",
			"error", p.Err, "db_path", r.cfg.DBPath)

		return
	}

	// Separate from the trigger line below, and not folded into it: "a table is gone" and "a table can
	// be edited" are different events to whoever reads them, and a single message would have to hedge
	// about which one happened.
	if len(p.MissingTables) > 0 {
		slog.ErrorContext(ctx, "a ledger table is absent from a database that reports itself fully migrated",
			"missing", p.MissingTables, "db_path", r.cfg.DBPath,
			"detail", "the rows are gone with the table. This database arrived in this state; this "+
				"boot did not cause it.")
	}

	if len(p.MissingTriggers) > 0 {
		slog.ErrorContext(ctx, "the ledger's append-only triggers are not all present on this database",
			"missing", p.MissingTriggers, "db_path", r.cfg.DBPath,
			"detail", "this database arrived in this state; this boot did not cause it. Ledger history "+
				"can be rewritten until the triggers are restored.")
	}
}

// notIn returns the elements of want that do not appear in have.
//
// The subtraction is the whole point of the check it serves: it separates "this migration destroyed
// a guarantee" from "this database was already like that", and only the first is something the boot
// path can or should act on.
func notIn(want, have []string) []string {
	present := make(map[string]bool, len(have))
	for _, name := range have {
		present[name] = true
	}

	var out []string

	for _, name := range want {
		if !present[name] {
			out = append(out, name)
		}
	}

	return out
}

// triggersLostError is what the operator reads when a migration dropped a trigger.
//
// It names every lost trigger and states the consequence in the terms the guarantee was sold in,
// because "trigger missing" reads as a schema detail and "your ledger can now be edited" reads as
// what it is.
func triggersLostError(lost []string) error {
	return fmt.Errorf("%w: %s. An UPDATE or DELETE on the affected table will now succeed. "+
		"A migration that rebuilds a table carrying a trigger must re-create it after the rename, "+
		"in the same file (.claude/rules/migrations.md case 1)",
		ErrAppendOnlyTriggerLost, strings.Join(lost, ", "))
}

// tablesDroppedError is the louder sibling: not "the ledger can be edited" but "the ledger is gone".
//
// Separate from triggersLostError because the two are different events to whoever reads them, and
// because a rebuild is the ONE legitimate reason a ledger table is dropped at all — a correct one
// re-creates it under the same name within the same migration, so this fires only when it was still
// absent at the end.
func tablesDroppedError(dropped []string) error {
	return fmt.Errorf("%w: %s. The rows are gone with the table. A 12-step rebuild must re-create "+
		"the table under the same name within the same migration; nothing else may drop one "+
		"(.claude/rules/migrations.md)",
		ErrLedgerTableDropped, strings.Join(dropped, ", "))
}

// failed restores the snapshot and builds the error the operator sees.
//
// applied may be the zero Migration when ApplyNext failed before it could report which file it was
// working on; the message degrades to naming the version rather than pretending to know the file.
func (r *Runner) failed(
	ctx context.Context,
	s *store.Store,
	closeStore func(),
	applied store.Migration,
	snapshot string,
	cause error,
) error {
	if applied.Source == "" {
		if pending, err := r.pendingAfterFailure(ctx, s); err == nil && len(pending) > 0 {
			applied = pending[0]
		}
	}

	slog.ErrorContext(ctx, "migration failed, restoring pre-migration snapshot",
		"migration", applied.Source, "snapshot", snapshot, "error", cause)

	// The store must be closed before the file underneath it is replaced. Every handle has to be
	// gone or the process keeps reading an unlinked inode.
	closeStore()

	failure := &FailedError{
		Migration: applied,
		Snapshot:  snapshot,
		Cause:     cause,
		DBPath:    r.cfg.DBPath,
	}

	if restoreErr := r.restore(ctx, snapshot); restoreErr != nil {
		failure.RestoreErr = restoreErr

		return failure
	}

	failure.Restored = true

	return failure
}

// pendingAfterFailure is a best-effort read of what was about to be applied, used only to name a
// file in an error message. Its own failure is not worth reporting over the failure that caused it.
func (r *Runner) pendingAfterFailure(ctx context.Context, s *store.Store) ([]store.Migration, error) {
	migrator, err := s.Migrator(r.fsys)
	if err != nil {
		return nil, err
	}

	return migrator.Pending(ctx)
}

// recordVersion writes the applied schema version and this binary's version into dkp_meta.
//
// binary_version is what makes the downgrade refusal able to name an image tag instead of saying
// "use a newer version", which is the thing the operator already knows and cannot act on. It is
// written after every successful run, including runs with nothing to migrate, so that a database
// migrated by an older build acquires the record the first time a build that knows about it starts.
func (r *Runner) recordVersion(ctx context.Context, s *store.Store, version int64) error {
	now := r.cfg.Clock.Now().UnixMicro()

	if err := s.SetMetaValue(ctx, MetaKeySchemaVersion, strconv.FormatInt(version, 10), now); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}

	if err := s.SetMetaValue(ctx, MetaKeyBinaryVersion, r.cfg.BinaryVersion, now); err != nil {
		return fmt.Errorf("record binary version: %w", err)
	}

	return nil
}
