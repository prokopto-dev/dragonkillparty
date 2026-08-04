package store

import (
	"net/url"
	"runtime"
)

// The pragma values, in one place, because two pools set them and three tests assert them.
//
// Every one of these is load-bearing and the reasons live in .claude/rules/store-and-sql.md:
//
//	journal_mode=WAL      readers never block the writer
//	busy_timeout=10000    absorbs the backup job's checkpoint pause
//	synchronous=NORMAL    the correct pairing with WAL; FULL fsyncs every commit for no gain
//	foreign_keys=ON       OFF by default in SQLite, and `ON DELETE SET NULL` must actually fire
//
// Both pools get all four. The read pool is not a second-class connection: a reader with
// foreign_keys off can observe a row a constraint should have pruned, and a reader without WAL
// would take the rollback journal's read lock and block the writer — the exact property WAL is
// here to provide.
const (
	journalMode  = "WAL"
	busyTimeout  = "10000" // milliseconds
	synchronous  = "NORMAL"
	foreignKeys  = "ON"
	writeTxLock  = "immediate"
	minReadConns = 4
)

// writeDSN builds the write pool's DSN: the four pragmas plus _txlock=immediate.
//
// _txlock=immediate makes database/sql issue BEGIN IMMEDIATE, which takes the write lock at BEGIN
// rather than at the first write. That converts a mid-transaction SQLITE_BUSY — which surfaces
// after the transaction has already done work and must be unwound — into a clean queue at the
// door, absorbed by busy_timeout.
func writeDSN(path string) string {
	return dsn(path, url.Values{
		"_txlock": []string{writeTxLock},
		"_pragma": pragmas(),
	})
}

// readDSN builds the read pool's DSN. Same four pragmas, no _txlock: the read pool never writes,
// and BEGIN IMMEDIATE from a reader would serialise reads against the writer for nothing.
func readDSN(path string) string {
	return dsn(path, url.Values{"_pragma": pragmas()})
}

// pragmas returns the _pragma values every connection in either pool applies at open.
//
// These are per-CONNECTION, not per-database (journal_mode is the one exception — it is persisted
// in the file header). database/sql opens connections lazily and replaces them over the pool's
// lifetime, so carrying them in the DSN rather than executing them once after Open is what makes
// them hold for every connection the pool ever creates.
func pragmas() []string {
	return []string{
		"journal_mode(" + journalMode + ")",
		"busy_timeout(" + busyTimeout + ")",
		"synchronous(" + synchronous + ")",
		"foreign_keys(" + foreignKeys + ")",
	}
}

// dsn assembles a SQLite URI filename.
//
// Built with net/url rather than string concatenation because the path is not ours: it comes from
// DKP_DB_PATH or from t.TempDir(). A path containing '?' or '#' concatenated straight into a URI
// silently truncates the filename and drops every pragma after it — the database would open, work,
// and be quietly missing foreign_keys and WAL. url.URL percent-encodes both.
func dsn(path string, q url.Values) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}

	return u.String()
}

// readPoolSize is the read pool's connection limit: max(4, NumCPU).
//
// The floor matters on the target hardware. A Raspberry Pi reports 4 cores and an SD card is the
// bottleneck long before the CPU is, but a single-core VPS reporting 1 would serialise every
// concurrent reader behind the slowest one for no reason — readers do not contend in WAL.
func readPoolSize() int {
	return max(minReadConns, runtime.NumCPU())
}
