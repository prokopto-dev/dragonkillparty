package migrate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// snapshotMode is the mode every snapshot is created with.
//
// The same 0600 the live database gets, and for the same reason: a pre-migration snapshot is a
// complete copy of the guild's password hashes, PAT hashes, TOTP seeds and email addresses. On a
// shared box a 0644 backup is readable by every other uid with no DKP credential and no audit
// trail. docs/design/03-security.md:1091 requires 0600 under /data/backups/.
const snapshotMode = 0o600

// backupDirMode keeps the directory itself owner-only.
const backupDirMode = 0o700

// snapshotTimeLayout sorts lexicographically, which is the only property that matters: an operator
// looking for "the one from just before the upgrade" finds it with `ls`, and a retention sweep can
// order by name. RFC 3339 with colons would be a filename hazard on some filesystems.
const snapshotTimeLayout = "20060102T150405Z"

// backupDir is where snapshots live, under the configured data directory.
//
// docs/design/06-cicd-and-release.md:503 hardcodes the literal /data/backups/ into the abort string
// of every migration's Down block, because a SQL comment cannot be templated. That literal is
// correct for the container, whose DKP_DATA_DIR defaults to /data, and merely approximate for a
// binary installed elsewhere. Where the two disagree, the path in the error messages this package
// prints is the accurate one, because it is computed from the running configuration.
func (r *Runner) backupDir() string { return filepath.Join(r.cfg.DataDir, "backups") }

// snapshotPath builds <data-dir>/backups/pre-<version>-<timestamp>.db.zst.
//
// <version> is the version of the binary DOING the upgrade, not the one being left behind. That is
// the number the operator has in their hand — they know they pulled v1.4.0 and it failed — and it
// is what makes a directory listing answer "which upgrade attempt was this?".
func (r *Runner) snapshotPath(now time.Time) string {
	return filepath.Join(r.backupDir(),
		fmt.Sprintf("pre-%s-%s.db.zst", r.cfg.BinaryVersion, now.UTC().Format(snapshotTimeLayout)))
}

// snapshot takes the pre-migration copy and returns its path.
//
// Three steps, and the order is load-bearing:
//
//  1. Checkpoint the write-ahead log into the main file. Without this the snapshot is still
//     correct — VACUUM INTO reads through the WAL — but the LIVE file on disk is not, and the
//     whole recovery story is "put one file back".
//  2. VACUUM INTO a plain database beside the target. Not a file copy: VACUUM INTO produces a
//     single defragmented, fully-written database with no -wal or -shm beside it, which is what
//     makes restoring it a one-file operation rather than a three-file operation with a
//     checkpoint race in the middle.
//  3. Compress it with zstd and remove the plain copy.
//
// The intermediate plain file lives in the backup directory rather than in os.TempDir: on a
// container /tmp is frequently a small tmpfs, and a guild with a 4 GB database would fill it and
// fail the upgrade at the one moment the upgrade must not fail for an avoidable reason.
func (r *Runner) snapshot(ctx context.Context, s *store.Store, now time.Time) (string, error) {
	if err := os.MkdirAll(r.backupDir(), backupDirMode); err != nil {
		return "", fmt.Errorf("create backup directory %s: %w", r.backupDir(), err)
	}

	if err := s.Checkpoint(ctx); err != nil {
		return "", err
	}

	dst := r.snapshotPath(now)
	plain := dst + ".tmp"

	// VACUUM INTO refuses to overwrite. A collision means two upgrades started in the same second,
	// and quietly overwriting the first one's pre-migration state is the exact thing this whole
	// mechanism exists to prevent — so the leftover is removed only if a previous run died between
	// steps 2 and 3, which is what os.Remove of the .tmp handles.
	// Logged when it actually removes something, never silently. A leftover .tmp is a full,
	// UNCOMPRESSED copy of the database — every credential hash, PAT hash, TOTP seed and email the
	// guild has — written at 0600 by VacuumInto but left behind if the process died during
	// compression. It does not match the `*.db.zst` glob the Down blocks and the runbook tell
	// operators to look for, so without this line an operator auditing /data/backups/ after an
	// outage has no way to know it ever existed.
	if _, statErr := os.Stat(plain); statErr == nil {
		slog.WarnContext(ctx, "removing an uncompressed snapshot left by an interrupted run",
			"path", plain, "note", "a previous upgrade was killed while compressing its snapshot")
	}

	if err := os.Remove(plain); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clear stale snapshot scratch %s: %w", plain, err)
	}

	if err := s.VacuumInto(ctx, plain); err != nil {
		return "", err
	}

	defer func() { _ = os.Remove(plain) }()

	if err := compress(plain, dst); err != nil {
		return "", err
	}

	return dst, nil
}

// compress writes src to dst as zstd, at 0600.
func compress(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open snapshot %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, snapshotMode)
	if err != nil {
		return fmt.Errorf("create snapshot %s: %w", dst, err)
	}

	// SpeedDefault, not SpeedBestCompression. This runs while the server is not yet serving and an
	// officer is watching a restart, so seconds are the scarce resource, not bytes: default gets
	// most of the ratio on SQLite pages at several times the speed.
	enc, err := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		_ = out.Close()

		return fmt.Errorf("start zstd encoder for %s: %w", dst, err)
	}

	if _, err := io.Copy(enc, in); err != nil {
		_ = enc.Close()
		_ = out.Close()

		return fmt.Errorf("compress snapshot %s: %w", dst, err)
	}

	if err := enc.Close(); err != nil {
		_ = out.Close()

		return fmt.Errorf("finish zstd stream for %s: %w", dst, err)
	}

	// Sync before Close. A snapshot that is still in the page cache when the machine loses power
	// mid-migration is not a snapshot, and this is the one write in the product where that
	// distinction is the difference between an inconvenience and a guild losing its history.
	if err := out.Sync(); err != nil {
		_ = out.Close()

		return fmt.Errorf("sync snapshot %s: %w", dst, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("close snapshot %s: %w", dst, err)
	}

	return nil
}

// Decompress expands a zstd snapshot to dst.
//
// Exported because restoring a snapshot by hand is a documented operator procedure and because the
// migration tests verify the snapshot is a readable database rather than merely a file that exists
// — an assertion that needs to get at the bytes.
func Decompress(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open snapshot %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	dec, err := zstd.NewReader(in)
	if err != nil {
		return fmt.Errorf("start zstd decoder for %s: %w", src, err)
	}
	defer dec.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, snapshotMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, dec.IOReadCloser()); err != nil {
		_ = out.Close()

		return fmt.Errorf("decompress %s: %w", src, err)
	}

	if err := out.Sync(); err != nil {
		_ = out.Close()

		return fmt.Errorf("sync %s: %w", dst, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}

	return nil
}
