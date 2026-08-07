package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// restoreMarkerMode keeps the marker owner-only. It holds a path under the backup directory, which
// is not a secret, but it sits beside a 0600 database and inherits that posture rather than
// advertising where the snapshots live to every uid on the box.
const restoreMarkerMode = 0o600

// restore puts the database back from a snapshot.
//
// The caller must have CLOSED the store first. Replacing a file underneath open SQLite handles
// leaves those handles pointing at an unlinked inode, so the process would keep reading the old
// database while the new one sat on disk, and every subsequent write would land in a file nobody
// can see. This function does not close the store itself because it does not own it; runner.go
// does, and the ordering is asserted there.
//
// Four properties, each of which is a way restores go wrong:
//
//   - The -wal and -shm siblings MUST go, and must go BEFORE the rename. SQLite treats a -wal
//     beside a database as committed data to replay, so a stale WAL from the failed migration
//     sitting next to a freshly restored file would reintroduce, silently and on the next open,
//     exactly the change that was just rolled back. Doing it the other way round — rename first,
//     then clean up — leaves precisely that window open.
//
//   - The crash window is closed by a MARKER, not by hope. Between removing the siblings and
//     completing the rename there is an instant in which a crash would leave the failed
//     migration's database on disk — and it would not be recoverable by re-running, because goose
//     has already recorded that migration as applied, so the next boot finds nothing pending and
//     never re-checks.
//
//     It is worth being precise about why the obvious alternative does not work. One might hope to
//     keep the main database file pristine during the migration by disabling WAL auto-checkpoint,
//     so that a crash before the rename leaves the pre-migration file intact. Measured: with
//     `PRAGMA wal_autocheckpoint = 0` a 20,000-row transaction does leave the main file at 4 KB
//     with an 11.8 MB WAL beside it — but closing the last connection checkpoints and deletes the
//     WAL regardless, and the restore path is *required* to close the store before it can replace
//     the file. So by the time a restore begins, the migration's bytes are always in the main file,
//     whatever that pragma says.
//
//     Hence writeRestoreMarker below, and resumeRestore in runner.go, which finishes an
//     interrupted restore on the next boot.
//
//   - The restored file must be BYTE-IDENTICAL to the snapshot. This is why verification happens on
//     a scratch copy opened READ-ONLY, and not on the file after it is in place: a normal
//     store.Open applies journal_mode=WAL, which rewrites the journal-mode bytes of the SQLite
//     header. The database would still be correct, and it would no longer be the snapshot — and
//     "byte-identical" is the assertion that distinguishes a real restore from one that produced
//     some third state nobody has ever tested.
//
//   - It must be verified before it is trusted. A restore that quietly produced an unreadable file
//     turns a recoverable failed upgrade into a lost database, discovered at the next start.
//
//   - The final move must be atomic. os.Rename within one directory leaves no window in which the
//     database file does not exist.
func (r *Runner) restore(ctx context.Context, snapshot string) error {
	// First, before anything on disk changes. If the process dies at any point from here to the
	// rename, the next boot finds this file and finishes the job.
	if err := os.WriteFile(r.restoreMarkerPath(), []byte(snapshot), restoreMarkerMode); err != nil {
		return fmt.Errorf("write restore marker %s: %w", r.restoreMarkerPath(), err)
	}

	for _, sibling := range []string{r.cfg.DBPath + "-wal", r.cfg.DBPath + "-shm"} {
		if err := os.Remove(sibling); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", sibling, err)
		}
	}

	// Beside the target, not in os.TempDir: the rename below must not cross a filesystem boundary,
	// and on a container /tmp is routinely a separate, small tmpfs.
	scratch := r.cfg.DBPath + ".restoring"

	if err := os.Remove(scratch); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear stale restore scratch %s: %w", scratch, err)
	}

	if err := Decompress(snapshot, scratch); err != nil {
		return err
	}

	if err := store.VerifyDatabaseFile(ctx, scratch); err != nil {
		// Leave the scratch file for inspection: at this point the snapshot itself is suspect and
		// deleting the evidence is the last thing anyone wants.
		return fmt.Errorf("snapshot %s did not verify: %w", snapshot, err)
	}

	if err := os.Rename(scratch, r.cfg.DBPath); err != nil {
		return fmt.Errorf("move restored database into place: %w", err)
	}

	// Last. The database is now the snapshot, so there is nothing left to finish.
	if err := os.Remove(r.restoreMarkerPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear restore marker %s: %w", r.restoreMarkerPath(), err)
	}

	return nil
}

// restoreMarkerPath names the file that records "a restore was in progress".
//
// Beside the database rather than in the backup directory, because it is a property of THIS
// database file and must travel with it — an operator who moves dkp.db to another machine
// mid-incident must not leave the marker behind.
func (r *Runner) restoreMarkerPath() string { return r.cfg.DBPath + ".restore-pending" }

// resumeRestore finishes a restore that a crash interrupted.
//
// Called at the top of Migrate, BEFORE the database is opened — opening it would create a WAL
// beside a file that is about to be replaced, and would let the rest of the boot sequence read a
// half-restored database as though it were the guild's real data.
//
// Re-running the restore is safe and idempotent: it decompresses the same snapshot to the same
// scratch path, verifies it again, and renames it into place again. The snapshot is the only
// authority, and it does not change.
func (r *Runner) resumeRestore(ctx context.Context) error {
	raw, err := os.ReadFile(r.restoreMarkerPath())
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read restore marker %s: %w", r.restoreMarkerPath(), err)
	}

	snapshot := strings.TrimSpace(string(raw))
	if snapshot == "" {
		return fmt.Errorf("restore marker %s is empty; restore by hand from %s",
			r.restoreMarkerPath(), r.backupDir())
	}

	slog.WarnContext(ctx, "a previous restore did not finish; completing it before starting",
		"snapshot", snapshot, "database", r.cfg.DBPath)

	if err := r.restore(ctx, snapshot); err != nil {
		return &FailedError{
			Snapshot:   snapshot,
			RestoreErr: err,
			Cause:      errors.New("a previous upgrade was interrupted while restoring its snapshot"),
			DBPath:     r.cfg.DBPath,
		}
	}

	slog.WarnContext(ctx, "interrupted restore completed", "snapshot", snapshot)

	return nil
}
