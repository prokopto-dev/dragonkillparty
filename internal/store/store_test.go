package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStore_Counted_RecordsSQLTextInOrder is the acceptance test for the piece every later
// statement budget and PR 9's EXPLAIN QUERY PLAN goldens read from.
//
// It exercises BOTH routes into the driver, because database/sql uses both and a counter watching
// only one would undercount exactly the code that matters. A prepared statement executed three
// times is three statements — that loop is the N+1 this exists to catch, and a counter that
// recorded the Prepare instead of the executions would report it as one.
//
// The assertion is on the whole slice rather than on a count, because the SQL text and its
// ordering are the part later PRs consume.
func TestStore_Counted_RecordsSQLTextInOrder(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	counter := Counted(t)

	require.Same(t, s.Counter(), counter, "Counted(t) must return the Store's own counter")

	createScratch(t, s)
	counter.Reset() // the fixture is not part of what is being measured

	const (
		direct   = "SELECT count(*) FROM scratch"
		prepared = "SELECT count(*) FROM scratch WHERE id > ?"
	)

	var n int

	// Route 1 — straight to the connection's QueryerContext.
	require.NoError(t, s.read.QueryRowContext(t.Context(), direct).Scan(&n))

	// Route 2 — a prepared statement, executed three times.
	stmt, err := s.read.PrepareContext(t.Context(), prepared)
	require.NoError(t, err, "prepare %q", prepared)

	t.Cleanup(func() { require.NoError(t, stmt.Close()) })

	for range 3 {
		require.NoError(t, stmt.QueryRowContext(t.Context(), 0).Scan(&n))
	}

	require.Equal(t, []Statement{
		{Op: OpQuery, SQL: direct, Args: 0},
		{Op: OpQuery, SQL: prepared, Args: 1},
		{Op: OpQuery, SQL: prepared, Args: 1},
		{Op: OpQuery, SQL: prepared, Args: 1},
	}, counter.Statements(),
		"three executions of one prepared statement are three statements, not one\n%s", counter)
}

// TestStore_Counted_CountsWrites asserts the write path is recorded too, and that BEGIN and COMMIT
// are not: they are driver transaction operations rather than statements, and counting them would
// inflate every budget by two per transaction and make the numbers in db/RECIPES.md wrong.
func TestStore_Counted_CountsWrites(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	counter := Counted(t)
	counter.Reset()

	err := s.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (?)", 1)

		return err
	})
	require.NoError(t, err)

	require.Equal(t, []Statement{
		{Op: OpExec, SQL: "INSERT INTO scratch (id) VALUES (?)", Args: 1},
	}, counter.Statements(), "one INSERT in one transaction is one statement\n%s", counter)
}

// TestStore_LeakedGoroutine_FailsThePackage proves the goleak wiring in TestMain actually bites.
//
// It re-execs this test binary with DKP_GOLEAK_FIXTURE=1; the child leaks a goroutine that never
// returns, and the child's TestMain has to fail the package for it. Doing it in a CHILD process is
// the whole trick: calling goleak.Find() in-process would race every other t.Parallel() test's
// goroutines and flake under -shuffle=on, and a leak committed in the parent would simply turn CI
// red for everyone.
func TestStore_LeakedGoroutine_FailsThePackage(t *testing.T) {
	if os.Getenv("DKP_GOLEAK_FIXTURE") == "1" {
		// This is the child. Leak a goroutine, then pass: goleak must fail the package anyway.
		go func() {
			// Blocks forever: nobody holds the other end of this channel. That is the leak.
			<-make(chan struct{})
		}()

		return
	}

	t.Parallel()

	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "DKP_GOLEAK_FIXTURE=1")

	out, err := cmd.CombinedOutput()

	require.Error(t, err,
		"a leaked goroutine must fail the package — is goleak.VerifyTestMain still in "+
			"internal/store/main_test.go?\n%s", out)
	require.Contains(t, string(out), "found unexpected goroutines",
		"the child process failed, but not because of goleak\n%s", out)
	require.Contains(t, string(out), "PASS: "+t.Name(),
		"the child's own test must PASS — the package fails only at goleak's verification, which "+
			"is what makes this a test of the harness rather than of the fixture\n%s", out)
}

// TestStore_Open_Directory_FailsAtOpen asserts Open pings rather than trusting database/sql's lazy
// connect. Without the ping, an unusable path surfaces at the first query instead of at boot, and
// the process comes up reporting itself healthy.
func TestStore_Open_Directory_FailsAtOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := Open(t.Context(), dir) // a directory, not a database file
	require.Error(t, err, "opening a directory as a database must fail at Open")
	require.True(t, strings.Contains(err.Error(), dir),
		"the error must name the path that failed: %v", err)
}

// TestStore_Open_EmptyPath_Fails closes a trap the ping cannot.
//
// SQLite reads an empty URI path as "private temporary database": it opens, it works, both pings
// pass, and the file is unlinked on close. So `DKP_DB_PATH=` boots green, takes a raid night's
// writes, and loses every one of them at the next restart — with no error anywhere. The only place
// to catch it is before the DSN is built.
func TestStore_Open_EmptyPath_Fails(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"", "   "} {
		_, err := Open(t.Context(), path)
		require.ErrorIs(t, err, ErrNoDatabasePath,
			"Open(%q) must be refused, not silently backed by a temporary database", path)
	}
}

// TestStore_Open_RelativePath_IsResolved asserts a relative path is made absolute rather than
// landing in the URI's authority position, where SQLite rejects it as "invalid uri authority".
//
// resolvePath is exercised directly rather than through Open, because the alternative is t.Chdir,
// which changes the working directory for every t.Parallel() test running alongside it.
func TestStore_Open_RelativePath_IsResolved(t *testing.T) {
	t.Parallel()

	got, err := resolvePath("dkp.db")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(got), "resolvePath must return an absolute path, got %q", got)
	require.Equal(t, "dkp.db", filepath.Base(got))
}

// TestStore_Open_FileMode_IsOwnerOnly asserts the database and its WAL siblings are not left
// world-readable.
//
// SQLite creates them 0644 less umask, and 0022 is the default umask on every platform this ships
// to. That file holds every password hash, PAT hash, TOTP seed and email address in the guild, and
// a second uid on a shared box reads it with no DKP credential and no audit trail — the read never
// reaches this process.
//
// It opens a FRESH database rather than calling NewDB, and that is load-bearing. CloneTemplate
// creates its file with O_EXCL and mode 0600, and SQLite does not change the mode of a file that
// already exists — so a clone is 0600 whether or not Open does anything, and asserting against one
// is a test that passes with the protection removed. Verified by removing restrictMode: the clone
// version stayed green, this one goes red.
func TestStore_Open_FileMode_IsOwnerOnly(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fresh.db")

	s, err := Open(t.Context(), path)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, s.Close()) })

	// Force the WAL and shm into existence, so this is not asserted against files that are simply
	// absent.
	createScratch(t, s)

	requireOwnerOnly(t, path)

	// The second half, and the one that proves restrictMode's comment rather than restating it.
	//
	// Close checkpoints and removes the -wal and -shm. On the next Open they do not exist, so
	// restrictMode skips them — the only thing that can secure the WAL that the write below
	// recreates is SQLite giving it the main database's mode. Assert that rather than assume it: if
	// it were false, every WAL after the first checkpoint would silently be 0644 and this whole
	// protection would cover the first boot only.
	require.NoError(t, s.Close())

	_, err = os.Stat(path + "-wal")
	require.ErrorIs(t, err, os.ErrNotExist, "Close must checkpoint and remove the -wal")

	reopened, err := Open(t.Context(), path)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	require.NoError(t, reopened.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
		_, execErr := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (1)")

		return execErr
	}))

	requireOwnerOnly(t, path)
}

// requireOwnerOnly asserts the database and both WAL siblings are 0600.
func requireOwnerOnly(tb testing.TB, path string) {
	tb.Helper()

	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix

		info, err := os.Stat(p)
		require.NoError(tb, err, "stat %s", p)
		require.Equal(tb, os.FileMode(0o600), info.Mode().Perm(),
			"%s is %#o — group and other must have no access", p, info.Mode().Perm())
	}
}

// BenchmarkNewDB_Clone measures what an integration test pays to get a database: clone the
// template, open both pools, ping both.
//
// This is the measurement behind item V4 of docs/development/verify-before-phase-0.md, and the
// whole test pyramid rests on the answer — "prefer integration tests, they are nearly free" is
// only true if this number is a fraction of a millisecond.
//
// The p50 is computed by `make bench-clone`, which runs this with -benchtime=1x -count=200 and
// takes the median of the 200 samples the testing package reports. Deliberately NOT computed in
// Go: that would need time.Now, which is grep-banned outside internal/clock (CLOCK001), and the
// testing package already times this correctly.
func BenchmarkNewDB_Clone(b *testing.B) {
	for b.Loop() {
		NewDB(b)
	}
}

// BenchmarkCloneTemplate_FileOnly splits the file copy out of the number above.
//
// It exists because the two halves scale differently and the difference decides what PR 3 should
// expect: the copy grows with the schema, while opening two pools and pinging them is a fixed cost
// no amount of schema will change. Without this split, a slow NewDB in PR 3 would be blamed on
// whichever half the reader already suspected.
func BenchmarkCloneTemplate_FileOnly(b *testing.B) {
	for b.Loop() {
		CloneTemplate(b, filepath.Join(b.TempDir(), "clone.db"))
	}
}

// recordingTB captures what a helper reports instead of failing the real test, so that a helper
// whose entire job is to fail can itself be tested.
//
// The embedded testing.TB satisfies the interface's unexported method; every method Budget calls
// is overridden below.
type recordingTB struct {
	testing.TB

	cleanups []func()
	errs     []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// runCleanups runs them last-registered-first, as the testing package does.
func (r *recordingTB) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

// TestCounter_Budget_Exceeded_FailsWithTheSQL is the negative fixture for the statement budget.
//
// A budget nobody has seen fail is a budget nobody knows works — and this one has to do more than
// fail. The message must carry the offending SQL in order, because a budget failure that prints
// only a number turns into "raise the number" and the N+1 ships.
func TestCounter_Budget_Exceeded_FailsWithTheSQL(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	counter := Counted(t)
	counter.Reset()

	rec := &recordingTB{TB: t}
	counter.Budget(rec, 1)

	for id := range 3 {
		err := s.Tx(t.Context(), func(ctx context.Context, tx DBTX) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO scratch (id) VALUES (?)", id)

			return err
		})
		require.NoError(t, err)
	}

	rec.runCleanups()

	require.Len(t, rec.errs, 1, "an exceeded budget must report exactly once")
	require.Contains(t, rec.errs[0], "statement budget exceeded: 3 statements, budget 1")
	require.Contains(t, rec.errs[0], "INSERT INTO scratch",
		"the failure must name the statements, not just the count")
}

// TestCounter_Budget_WithinBudget_Passes is the control. A budget that fired on a compliant test
// would be worse than no budget at all: it would train people to raise it.
func TestCounter_Budget_WithinBudget_Passes(t *testing.T) {
	t.Parallel()

	s := NewDB(t)
	createScratch(t, s)

	counter := Counted(t)
	counter.Reset()

	rec := &recordingTB{TB: t}
	counter.Budget(rec, 4)

	require.Equal(t, 0, scratchRows(t, s))

	rec.runCleanups()

	require.Empty(t, rec.errs, "a test inside its budget must not be reported\n%s", counter)
}
