package migrate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// These are the package-local unit tests docs/development/first-ten-prs.md's files-touched block
// names (`internal/migrate/*_test.go`). They cover the pure logic only — message formatting, path
// construction, config validation — and deliberately touch no database: the behavioural tests live
// in test/migrations/, where 04-testing.md requires them to run against SQL only.
//
// The split matters for the fast loop. Everything here runs under `make test-unit` in microseconds,
// so a regression in the one thing an operator actually reads at 1 a.m. — the failure message — is
// caught in the sub-five-second budget rather than only by the slower migration suite.

// TestSchemaAheadError_RecordedBinaryVersion_NamesTheImageTag is the whole reason binary_version is
// written into dkp_meta.
//
// Without it the most useful sentence available is "use a newer version", which is precisely what
// the operator already knows and cannot act on. With it, the refusal is a command they can run.
func TestSchemaAheadError_RecordedBinaryVersion_NamesTheImageTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		wroteBy     string
		wantContain string
		wantAbsent  string
	}{
		{
			name:        "a recorded version becomes a pullable image tag",
			wroteBy:     "v1.9.2",
			wantContain: "ghcr.io/prokopto-dev/dragonkillparty:v1.9.2",
		},
		{
			name:    "no recorded version says so instead of inventing a tag",
			wroteBy: "",
			wantContain: "the version you upgraded from (this database predates the binary_version " +
				"record)",
			wantAbsent: "ghcr.io/prokopto-dev/dragonkillparty:\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := &SchemaAheadError{Applied: 9, Latest: 4, WroteBy: tc.wroteBy, BackupDir: "/data/backups"}
			msg := err.Error()

			require.Contains(t, msg, tc.wantContain)
			require.Contains(t, msg, "/data/backups", "the refusal must name where the snapshots are")
			require.Contains(t, msg, "9", "the refusal must name the schema version found")
			require.Contains(t, msg, "4", "the refusal must name the version this binary understands")

			if tc.wantAbsent != "" {
				require.NotContains(t, msg, tc.wantAbsent, "an image tag with no version is worse than none")
			}
		})
	}
}

// TestSchemaAheadError_IsSentinel keeps errors.Is working for cmd/dkp, which decides between
// exiting 1 and serving anyway on exactly this distinction.
func TestSchemaAheadError_IsSentinel(t *testing.T) {
	t.Parallel()

	err := &SchemaAheadError{Applied: 2, Latest: 1}
	require.ErrorIs(t, err, ErrSchemaAhead)
}

// TestFailedError_Restored_TellsTheOperatorNothingWasLost pins the two messages apart.
//
// docs/operations/troubleshooting.md:21 promises an operator that their database "was already
// restored". Whether that promise is true is the difference between a support ticket and a panic,
// so the restored and not-restored cases must not read alike.
func TestFailedError_Restored_TellsTheOperatorNothingWasLost(t *testing.T) {
	t.Parallel()

	base := &FailedError{
		Migration: store.Migration{Version: 7, Source: "000007_add_bid_hold.sql"},
		Snapshot:  "/data/backups/pre-v1.4.0-20260806T101500Z.db.zst",
		DBPath:    "/data/dkp.db",
		Cause:     errNamed("integrity check failed: CHECK constraint failed in ledger_entry"),
	}

	t.Run("restored", func(t *testing.T) {
		t.Parallel()

		restored := *base
		restored.Restored = true
		msg := restored.Error()

		require.Contains(t, msg, "000007_add_bid_hold.sql", "the failing migration must be named")
		require.Contains(t, msg, "Nothing was lost")
		require.Contains(t, msg, restored.Snapshot)
		require.Contains(t, msg, "zstd -d "+restored.Snapshot+" -o "+restored.DBPath,
			"the manual restore command must be copy-pasteable, with both real paths")
		require.NotContains(t, msg, "THE AUTOMATIC RESTORE ALSO FAILED")
	})

	t.Run("not restored", func(t *testing.T) {
		t.Parallel()

		failed := *base
		failed.Restored = false
		failed.RestoreErr = errNamed("disk full")
		msg := failed.Error()

		require.Contains(t, msg, "THE AUTOMATIC RESTORE ALSO FAILED",
			"a failed restore must not read like a successful one")
		require.Contains(t, msg, "disk full", "the reason the restore failed must be shown")
		require.Contains(t, msg, "Do not start this version")
		require.NotContains(t, msg, "Nothing was lost",
			"claiming nothing was lost when the restore failed is the worst possible message")
	})
}

// TestSnapshotPath_NamesFollowTheDocumentedPattern pins the filename.
//
// It is not cosmetic: every migration's Down block hardcodes the glob
// `/data/backups/pre-<ver>-*.db.zst`, and docs/operations/upgrade-and-backup.md tells operators to
// look for exactly that. A change here silently invalidates instructions already shipped inside
// tagged releases.
func TestSnapshotPath_NamesFollowTheDocumentedPattern(t *testing.T) {
	t.Parallel()

	r, err := New(nil, Config{DBPath: "/data/dkp.db", DataDir: "/data", BinaryVersion: "v1.4.0"})
	require.NoError(t, err)

	at := time.Date(2026, 8, 6, 10, 15, 0, 0, time.UTC)
	got := r.snapshotPath(at)

	require.Equal(t, filepath.Join("/data", "backups", "pre-v1.4.0-20260806T101500Z.db.zst"), got)
	require.True(t, strings.HasSuffix(got, ".db.zst"),
		"the Down blocks tell operators to look for *.db.zst")
}

// TestSnapshotPath_NonUTCClock_StillNamesUTC guards the one property of the injected clock that a
// filename depends on.
//
// A snapshot named in local time sorts differently on two machines and repeats an hour across a DST
// boundary — for a file whose entire job is to be found again, in order, during an incident.
func TestSnapshotPath_NonUTCClock_StillNamesUTC(t *testing.T) {
	t.Parallel()

	r, err := New(nil, Config{DBPath: "/data/dkp.db", DataDir: "/data", BinaryVersion: "v1.4.0"})
	require.NoError(t, err)

	// 10:15 UTC expressed in a zone eight hours ahead.
	zone := time.FixedZone("UTC+8", 8*60*60)
	local := time.Date(2026, 8, 6, 18, 15, 0, 0, zone)

	require.Equal(t, r.snapshotPath(time.Date(2026, 8, 6, 10, 15, 0, 0, time.UTC)), r.snapshotPath(local),
		"the snapshot name changed with the operator's timezone")
}

// TestNew_MissingConfig_Fails covers the three fields whose absence produces a broken message or a
// broken path rather than an obvious failure.
func TestNew_MissingConfig_Fails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "no database path", cfg: Config{DataDir: "/data", BinaryVersion: "v1"}},
		{name: "no data directory", cfg: Config{DBPath: "/data/dkp.db", BinaryVersion: "v1"}},
		// An empty version yields `pre--2026….db.zst` and a refusal telling the operator to run an
		// image with no tag. Both are worse than refusing to start.
		{name: "no binary version", cfg: Config{DBPath: "/data/dkp.db", DataDir: "/data"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(nil, tc.cfg)
			require.Error(t, err)
		})
	}
}

// TestNew_NilClock_DefaultsToSystem — a nil clock must not be a nil-pointer dereference at the one
// moment the process is mid-upgrade.
func TestNew_NilClock_DefaultsToSystem(t *testing.T) {
	t.Parallel()

	r, err := New(nil, Config{DBPath: "/data/dkp.db", DataDir: "/data", BinaryVersion: "v1"})
	require.NoError(t, err)
	require.NotNil(t, r.cfg.Clock)
	require.False(t, r.cfg.Clock.Now().IsZero())
}

// TestRestoreMarkerPath_SitsBesideTheDatabase — the marker records that THIS database file is
// mid-restore, so it must travel with it rather than living in the backup directory.
func TestRestoreMarkerPath_SitsBesideTheDatabase(t *testing.T) {
	t.Parallel()

	r, err := New(nil, Config{DBPath: "/srv/dkp/dkp.db", DataDir: "/srv/dkp", BinaryVersion: "v1"})
	require.NoError(t, err)

	require.Equal(t, "/srv/dkp/dkp.db.restore-pending", r.restoreMarkerPath())
	require.NotEqual(t, r.cfg.DBPath, r.restoreMarkerPath(),
		"the marker must never be the database path itself")
}

// errNamed is a tiny error whose message is exactly what is passed in, so the assertions above test
// formatting rather than a sentinel's own text.
type errNamed string

func (e errNamed) Error() string { return string(e) }
