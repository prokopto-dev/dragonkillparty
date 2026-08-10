// Package migrate is the boot-time upgrade path: read the version, refuse a downgrade, snapshot,
// migrate one file at a time with an integrity check after each, and restore automatically on any
// failure.
//
// It holds no *sql.DB. Every statement it causes runs inside internal/store (law 2), which is why
// this package can be reasoned about as policy: what order things happen in, what is checked, when
// a snapshot is taken, and — the part an operator actually experiences — exactly what the process
// says before it exits.
//
// The audience is a volunteer officer who upgrades by replacing a binary and restarting, at
// whatever hour they happened to do it, with no backup discipline and ten years of guild data in
// one file. Every message in this package is written for that person at 1 a.m., not for us.
package migrate

import (
	"errors"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// State is what the boot path found. It is also what /readyz reports.
type State string

const (
	// StateUpToDate — the database matches the binary. Nothing to do.
	StateUpToDate State = "up_to_date"
	// StatePending — migrations are waiting. With DKP_AUTO_MIGRATE=false the process serves, and
	// /readyz returns 503 naming the command to run.
	StatePending State = "pending"
	// StateAhead — the database was written by a NEWER binary. This is the downgrade case and it
	// is fatal.
	StateAhead State = "ahead"
)

// MetaKeySchemaVersion and MetaKeyBinaryVersion are the dkp_meta rows the boot path maintains.
//
// docs/design/06-cicd-and-release.md:512 writes the first boot step as "read
// dkp_meta.schema_version", phrased as though it were a column. dkp_meta is key/value
// (docs/design/01-domain-model.md:1283, which is the schema authority), so these are keys. The
// contradiction is reported alongside this change rather than resolved by inventing a column.
const (
	MetaKeySchemaVersion = "schema_version"
	MetaKeyBinaryVersion = "binary_version"
)

// Status is the answer to "where is this database relative to this binary".
type Status struct {
	State State
	// Applied is the highest migration version recorded in the database.
	Applied int64
	// Latest is the highest migration version this binary carries.
	Latest int64
	// Pending lists the migrations not yet applied, lowest first.
	Pending []store.Migration
	// WroteBy is the binary version that last migrated this database, if it recorded one. Empty on
	// a database migrated by a build predating that record, which is why the downgrade message
	// treats it as optional rather than assuming it.
	WroteBy string
}

// ErrSchemaAhead is the sentinel for the downgrade refusal. Callers match with errors.Is; the
// message comes from SchemaAheadError.
var ErrSchemaAhead = errors.New("database schema is newer than this binary")

// SchemaAheadError is returned when the database was written by a newer binary.
//
// The refusal is the feature. An old binary writing to a new schema does not fail loudly — it
// writes rows the new columns do not constrain, drops values it does not know about, and corrupts
// the data quietly over days. Refusing to start is recoverable; starting is not.
type SchemaAheadError struct {
	// Applied is the version found in the database.
	Applied int64
	// Latest is the newest version this binary can apply.
	Latest int64
	// WroteBy is the binary version recorded as having migrated this database. It may be empty.
	WroteBy string
	// BackupDir is where this instance keeps its snapshots.
	BackupDir string
}

func (e *SchemaAheadError) Error() string {
	// The image tag comes from the database, not from a guess. This is the whole reason the boot
	// path records binary_version into dkp_meta after a successful migration: without it the most
	// useful sentence available is "use a newer version", which is exactly what the operator
	// already knows and cannot act on.
	tag := e.WroteBy
	if tag == "" {
		tag = "the version you upgraded from (this database predates the binary_version record)"
	} else {
		tag = "ghcr.io/dragonkillparty/dkp:" + tag
	}

	return fmt.Sprintf(
		"%s: the database is at schema version %d and this binary understands up to %d.\n\n"+
			"  Nothing has been changed. A downgrade is refused rather than attempted, because an\n"+
			"  old binary writing to a new schema corrupts data quietly instead of failing.\n\n"+
			"  Run this image instead:\n    %s\n\n"+
			"  Your pre-upgrade snapshots are in:\n    %s",
		ErrSchemaAhead.Error(), e.Applied, e.Latest, tag, e.BackupDir)
}

// Is makes errors.Is(err, ErrSchemaAhead) work.
func (e *SchemaAheadError) Is(target error) bool { return target == ErrSchemaAhead }

// ErrMigrationFailed is the sentinel for a failed migration that was rolled back.
var ErrMigrationFailed = errors.New("migration failed and the database was restored")

// ErrAppendOnlyTriggerLost is the sentinel for the third post-migration check: a migration applied
// cleanly, left every row intact, passed integrity_check and foreign_key_check, and dropped an
// append-only trigger on the way — so the ledger it handed back can be rewritten.
//
// A sentinel rather than a bare string because it is the one migration failure whose meaning is a
// product guarantee rather than a database fault, and a caller distinguishing "your data is fine but
// your ledger is no longer protected" from "your database is corrupt" should not have to match on
// prose. It arrives wrapped in a FailedError, whose Unwrap reaches it.
var ErrAppendOnlyTriggerLost = errors.New("a migration dropped an append-only ledger trigger")

// FailedError is returned when a migration failed and the snapshot was put back.
//
// It carries the snapshot path because that is the single most useful fact for the operator, and
// because docs/operations/troubleshooting.md:21 promises them that the database "was already
// restored" — a promise this type is responsible for keeping and for making legible.
type FailedError struct {
	// Migration is the file that failed. Naming it is only possible because the boot path applies
	// one migration at a time and checks after each; a bulk Up() would name the set.
	Migration store.Migration
	// Snapshot is the path to the pre-migration snapshot.
	Snapshot string
	// Restored reports whether the database was successfully put back. False means the operator
	// has to act, and the message says so in different words.
	Restored bool
	// RestoreErr is why the restore failed, when it did.
	RestoreErr error
	// Cause is the underlying migration or integrity-check failure.
	Cause error
	// DBPath is where the database lives, so the manual command can be copied verbatim.
	DBPath string
}

func (e *FailedError) Error() string {
	if !e.Restored {
		return fmt.Sprintf(
			"migration %s failed: %v\n\n"+
				"  THE AUTOMATIC RESTORE ALSO FAILED: %v\n\n"+
				"  Your database may be in a partially migrated state. Do not start this version\n"+
				"  again. Restore the pre-upgrade snapshot by hand:\n"+
				"    zstd -d %s -o %s\n",
			e.Migration, e.Cause, e.RestoreErr, e.Snapshot, e.DBPath)
	}

	return fmt.Sprintf(
		"migration %s failed: %v\n\n"+
			"  Your database was restored automatically from the snapshot taken immediately before\n"+
			"  this upgrade started. Nothing was lost:\n    %s\n\n"+
			"  Do not retry this version. Report the migration named above.\n"+
			"  To restore that snapshot by hand:\n    zstd -d %s -o %s\n",
		e.Migration, e.Cause, e.Snapshot, e.Snapshot, e.DBPath)
}

// Unwrap exposes the underlying failure to errors.Is and errors.As.
func (e *FailedError) Unwrap() error { return e.Cause }

// Is makes errors.Is(err, ErrMigrationFailed) work alongside Unwrap's chain to Cause.
func (e *FailedError) Is(target error) bool { return target == ErrMigrationFailed }
