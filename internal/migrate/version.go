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
	"strings"

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
	// Protection is the ledger's database-level append-only guarantee as this database stands right
	// now. Read on every Status call, so /readyz reports it on every probe — see Protection.
	Protection Protection
}

// Protection is what remains of the ledger's database-level append-only guarantee: the triggers that
// make an UPDATE or a DELETE on ledger history raise, and the tables they are attached to.
//
// It rides on Status because of the gap #39 left open and #59 named. The boot path refuses a
// migration that drops an append-only trigger, and a database that ARRIVED without one is logged at
// error level and boots anyway — deliberately, because refusing there would close an officer's
// upgrade path permanently over damage no version of this binary can undo. That log fires ONCE, into
// whatever sink the operator has, during a restart nobody watched. Everything after it looks entirely
// normal: the site serves, the balances add up, and the ledger can be rewritten.
//
// Carrying the same answer on Status is what makes the degraded state continuously visible instead of
// historically visible. It is the difference between "we detect it" and "somebody finds out".
type Protection struct {
	// MissingTriggers are catalogued append-only triggers absent from a ledger table that EXISTS, in
	// catalogue order. A trigger whose table is not there is not listed — see store.AppendOnlyState,
	// which owns that exemption and the reason for it.
	MissingTriggers []string
	// MissingTables are catalogued ledger tables absent from a database that has applied every
	// migration this binary carries, in catalogue order.
	//
	// Populated only on an up-to-date database, and that restriction is the whole reason this field
	// can exist at all. Before the migration that creates the ledger has been applied, an absent
	// ledger table is EARLY rather than gone: a check that could not tell those apart would report
	// every fresh install as tampered, and a check that cries wolf on first boot is a check operators
	// learn to ignore. The boot path catches the dropped-mid-upgrade case by comparing the table set
	// across each migration (see Runner.Migrate); this catches the one it cannot — a table already
	// missing from a database that says it is fully migrated.
	MissingTables []string
	// Err is why the guarantee could not be READ, when it could not be.
	//
	// Unknown is a third answer and not a synonym for either of the other two. /readyz reports it as
	// failed, because a check that could not be evaluated has not passed; the boot path warns and
	// still starts, which is the same asymmetry Runner.Migrate applies to a missing trigger.
	Err error
}

// Degraded reports whether the ledger has lost part of its database-level protection.
//
// A read that FAILED is not degraded — it is unknown, and Err carries why. Folding the two together
// would make an unreachable database indistinguishable from a tampered one in the one place an
// operator looks.
func (p Protection) Degraded() bool {
	return len(p.MissingTriggers) > 0 || len(p.MissingTables) > 0
}

// Detail is the sentence an operator reads on /readyz when the protection is not intact.
//
// It states the consequence in the terms the guarantee was sold in, for the same reason
// triggersLostError does: "trigger missing" reads as a schema detail, and "ledger history can be
// rewritten" reads as what it is. Tables come first when both are gone, because a missing table is
// the louder event — the rows went with it.
func (p Protection) Detail() string {
	var parts []string

	if len(p.MissingTables) > 0 {
		parts = append(parts, "missing ledger tables: "+strings.Join(p.MissingTables, ", "))
	}

	if len(p.MissingTriggers) > 0 {
		parts = append(parts, "missing append-only triggers: "+strings.Join(p.MissingTriggers, ", "))
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "; ") + ". Ledger history can be rewritten until this is restored. " +
		"This boot did not cause it: a migration that drops an append-only trigger is refused and " +
		"rolled back, so the damage predates this binary and needs a human."
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
		tag = "ghcr.io/prokopto-dev/dragonkillparty:" + tag
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

// ErrLedgerTableDropped is the same check's other half: a migration removed a ledger table outright
// and did not put it back.
//
// Distinct from ErrAppendOnlyTriggerLost because the events read differently to whoever gets paged —
// one says the ledger can be rewritten, this one says it is gone — and because a check that only
// counted triggers would never fire here at all: every trigger on a table that does not exist is
// vacuously present, which is the exemption that lets a fresh install run migration 000001.
var ErrLedgerTableDropped = errors.New("a migration dropped a ledger table")

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
