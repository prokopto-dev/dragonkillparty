package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"

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

// Status reports where the database is relative to this binary, without changing anything.
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

		return st, nil
	case applied == latest:
		st.State = StateUpToDate
	default:
		st.State = StatePending
	}

	pending, err := migrator.Pending(ctx)
	if err != nil {
		return Status{}, err
	}

	st.Pending = pending

	// Pending is authoritative over the version arithmetic above. Out-of-order migration numbers
	// from a bad branch merge can leave applied == latest with a gap in the middle, and reporting
	// "up to date" in that situation is how a migration gets skipped forever.
	if len(pending) > 0 {
		st.State = StatePending
	}

	return st, nil
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

	// One at a time, with both checks after each. A bulk apply would discover the corruption after
	// the migrations that followed it had also run, and would name the wrong file — and the file
	// name is the entire actionable content of the message an operator gets.
	for {
		applied, applyErr := migrator.ApplyNext(ctx)
		if errors.Is(applyErr, store.ErrNoPendingMigration) {
			break
		}

		if applyErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, applyErr)
		}

		if checkErr := s.IntegrityCheck(ctx); checkErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, checkErr)
		}

		// Separate from integrity_check, which does not validate foreign keys. A rebuild that
		// copied rows in the wrong order leaves dangling references integrity_check calls healthy.
		if checkErr := s.ForeignKeyCheck(ctx); checkErr != nil {
			return r.failed(ctx, s, closeStore, applied, snapshot, checkErr)
		}

		slog.InfoContext(ctx, "migration applied", "migration", applied.Source, "version", applied.Version)
	}

	final, err := migrator.DBVersion(ctx)
	if err != nil {
		return err
	}

	return r.recordVersion(ctx, s, final)
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
