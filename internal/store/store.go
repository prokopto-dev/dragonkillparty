package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

// Sentinel errors. They live here because store is the owning package (AGENTS.md), and callers
// compare with errors.Is so that a wrapped error from four layers down still matches.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")

	// ErrNoDatabasePath is returned by Open for an empty path. It is a sentinel rather than a
	// generic error because the caller — the serve command reading DKP_DB_PATH — is the only place
	// that can say anything useful about it.
	ErrNoDatabasePath = errors.New("no database path")
)

// dbFileMode is the mode the database file and its WAL siblings are held at.
//
// SQLite creates them at 0644 less umask, and the default umask everywhere is 0022, so left alone
// the file lands world-readable. That file is the entire credential and PII corpus of the guild —
// password hashes, PAT hashes, TOTP seeds, emails — and on a shared box or a multi-tenant VPS any
// other uid can read it directly, with no DKP credential and no audit trail, because the read
// never goes through this process. docs/design/03-security.md requires 0600 for it.
const dbFileMode = 0o600

// Store owns the process's database handles. It is the only place in the repo that may hold a
// *sql.DB — law 2, enforced by the SQL001/SQL002 gates in scripts/repo-gates.sh rather than by
// trust.
//
// Two pools, one file:
//
//   - write, capped at ONE connection. Not a performance tuning: the cap is what makes
//     `SELECT COALESCE(max(seq),0)+1` a correct sequence allocator on SQLite, and what turns
//     "two officers award points at once" from a lock-ordering problem into a queue. It is also
//     the reason a long import must commit in chunks (.claude/rules/store-and-sql.md).
//   - read, capped at max(4, NumCPU). Readers never block the writer in WAL, so this is free
//     concurrency.
//
// Both are unexported and there is no accessor. A domain package that wants to touch the database
// goes through Tx or, from PR 3, through the Queries interface — never through a handle.
type Store struct {
	path    string
	write   *sql.DB
	read    *sql.DB
	counter *Counter
}

// Option configures Open.
type Option func(*config)

type config struct {
	counter *Counter
}

// WithStatementCounter interposes a statement counter on both pools.
//
// Off by default, and deliberately so: the counter retains the SQL text of every statement, which
// is exactly what a test wants and exactly what a six-month-uptime server does not. Tests get it
// from NewDB; production never sets it.
func WithStatementCounter(c *Counter) Option {
	return func(cfg *config) { cfg.counter = c }
}

// Open opens both pools against the SQLite database at path, creating it if it does not exist.
//
// Both pools are pinged before returning. Without that, a DSN typo or an unreadable file surfaces
// at the first query instead of at boot — database/sql connects lazily — and the process would
// come up "healthy" and fail on the first officer to load standings.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}

	path, err := resolvePath(path)
	if err != nil {
		return nil, err
	}

	write, err := openPool(writeDSN(path), 1, cfg.counter)
	if err != nil {
		return nil, fmt.Errorf("open write pool %s: %w", path, err)
	}

	read, err := openPool(readDSN(path), readPoolSize(), cfg.counter)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open read pool %s: %w", path, err), write.Close())
	}

	s := &Store{path: path, write: write, read: read, counter: cfg.counter}

	if err := write.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping write pool %s: %w", path, err), s.Close())
	}

	if err := read.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping read pool %s: %w", path, err), s.Close())
	}

	// After the pings, because the first connection is what creates the file and its WAL.
	if err := restrictMode(path); err != nil {
		return nil, errors.Join(err, s.Close())
	}

	return s, nil
}

// resolvePath rejects an empty database path and makes a relative one absolute.
//
// An empty path is not a harmless default. SQLite treats an empty URI path as a private TEMPORARY
// database, so `DKP_DB_PATH=` — a typo'd systemd `Environment=`, an empty Compose variable, a
// ConfigMap key with no value — boots green, serves a whole raid night, and unlinks the file on
// restart. Open's pings cannot catch it, because there genuinely is a working database; it is just
// the wrong one, and it is gone by morning.
//
// A relative path is resolved rather than rejected: url.URL{Scheme: "file"} puts a relative path in
// the URI's AUTHORITY position (`file://dkp.db`), which SQLite rejects with "invalid uri
// authority" — an incomprehensible error for something a person reasonably types.
func resolvePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrNoDatabasePath
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path %q: %w", path, err)
	}

	return abs, nil
}

// restrictMode tightens the database file and its WAL siblings to dbFileMode.
//
// The siblings are chmodded too, not just the main file: the first connection has already created
// them at the looser mode by the time Open returns. After that, SQLite gives a recreated `-wal` the
// main database's mode, so tightening the main file is what keeps them tight across checkpoints —
// asserted, not assumed, by the second half of TestStore_Open_FileMode_IsOwnerOnly.
//
// A chmod failure fails Open. Coming up anyway would mean serving from a database this process has
// been told it cannot secure, which is the decision an operator should make deliberately rather
// than discover in an incident.
func restrictMode(path string) error {
	var errs []error

	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		err := os.Chmod(p, dbFileMode)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("restrict %s to %#o: %w", p, dbFileMode, err))
		}
	}

	return errors.Join(errs...)
}

// openPool builds one pool.
//
// sql.OpenDB with a Connector, not sql.Open with a driver name. sql.Register is process-global,
// panics on a name it has already seen and cannot be undone, so attaching a per-Store statement
// counter through it would mean inventing a unique driver name per open and leaking one
// registration per test. A Connector carries the counter as ordinary state and registers nothing.
func openPool(dataSourceName string, maxConns int, counter *Counter) (*sql.DB, error) {
	base, err := sqlite.NewConnector(dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("build connector: %w", err)
	}

	connector := driver.Connector(base)
	if counter != nil {
		connector = countingConnector{Connector: base, counter: counter}
	}

	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(maxConns)
	// Idle cap equal to the open cap. The default is 2, so a read pool sized to 8 would close and
	// reopen six connections under load, re-running every pragma in the DSN each time.
	db.SetMaxIdleConns(maxConns)

	return db, nil
}

// Close closes both pools. Safe to call on a partially-opened Store.
func (s *Store) Close() error {
	var errs []error

	if s.write != nil {
		if err := s.write.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close write pool: %w", err))
		}
	}

	if s.read != nil {
		if err := s.read.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close read pool: %w", err))
		}
	}

	return errors.Join(errs...)
}

// Path returns the database file this Store was opened against.
func (s *Store) Path() string { return s.path }

// Counter returns the statement counter interposed on this Store, or nil when none was configured.
func (s *Store) Counter() *Counter { return s.counter }
