package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The test harness lives in the production package, and stays there. Do not move it to a
// storetest/ subpackage.
//
// The cost is real and known: this file imports `testing`, so `testing` and `flag` link into the
// binary, and InitTemplate, NewDB, CloneTemplate, Counted and Budget sit on the shipped API
// surface. That is the same trade net/http/httptest makes.
//
// The alternative is worse, and it is worse in the direction that matters here. A sibling package
// cannot reach Store.write, so buildTemplate's `apply(ctx, s.write)` seam would have to become an
// exported *sql.DB accessor — and law 2 is precisely that no such accessor exists. Trading a
// linked test package for a hole in the law the whole package exists to enforce is not a security
// improvement; it moves a *sql.DB one call from every package in the repo in order to avoid
// shipping ~100 KB of stdlib.
//
// The residual risk is WithStatementCounter, which retains SQL text unboundedly and is one call
// from any future wiring in cmd/. It is off by default and nothing enables it; if serve.go ever
// grows a debug flag that does, the counter needs a retention cap in the same change.

// SchemaFunc puts a database into the state the template should be cloned from.
//
// This is a parameter, and that is the whole design. docs/development/first-ten-prs.md specifies
// the template as "built once by TestMain via migrate + VACUUM INTO", but migrations do not exist
// until PR 3 and this is PR 2. Rather than guess at a schema or skip the harness, the schema step
// is injected: PR 3 passes the goose runner here and deletes nothing else.
type SchemaFunc func(context.Context, *sql.DB) error

// templatePath is the template built by InitTemplate, and counters maps a running test to its
// statement counter.
//
// Both are package-level mutable state, which .claude/rules/go-idioms.md bans — so the exemption
// is argued rather than assumed. templatePath is written once, from TestMain, before any test
// starts and never again. counters is keyed by TB.Name(), which is unique per running test, is
// written by NewDB and removed by that test's Cleanup. Neither can be perturbed by -shuffle=on or
// t.Parallel(), which is the failure mode the ban exists to prevent. Both are test-only.
var (
	templatePath string
	counters     sync.Map // map[string]*Counter
)

// InitTemplate builds the package's template database once and returns a cleanup function.
//
// Call it from TestMain. The returned cleanup must run after the tests, which with goleak means
// passing it through goleak.Cleanup — goleak.VerifyTestMain calls os.Exit itself, so a plain defer
// in TestMain never runs.
func InitTemplate(ctx context.Context, apply SchemaFunc) (func(), error) {
	dir, err := os.MkdirTemp("", "dkp-store-template-")
	if err != nil {
		return nil, fmt.Errorf("create template directory: %w", err)
	}

	cleanup := func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			// Nothing to return to at this point in TestMain, and a leaked temp directory is not
			// worth failing a green suite over — but it must not vanish silently either.
			slog.Error("remove template directory", "dir", dir, "error", rmErr)
		}
	}

	path := filepath.Join(dir, "template.db")
	if err := buildTemplate(ctx, path, apply); err != nil {
		cleanup()

		return nil, fmt.Errorf("build template database: %w", err)
	}

	templatePath = path

	return cleanup, nil
}

// buildTemplate applies the schema to a scratch database and compacts it into path with
// VACUUM INTO.
//
// VACUUM INTO rather than copying the scratch file: it writes a single, fully-checkpointed,
// defragmented database with no WAL or shm alongside it. That is what makes a clone a one-file
// copy instead of a three-file copy with a checkpoint race in the middle.
func buildTemplate(ctx context.Context, path string, apply SchemaFunc) error {
	scratch := filepath.Join(filepath.Dir(path), "build.db")

	s, err := Open(ctx, scratch)
	if err != nil {
		return fmt.Errorf("open scratch database: %w", err)
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil {
			slog.Error("close scratch database", "path", scratch, "error", closeErr)
		}
	}()

	if apply != nil {
		if err := apply(ctx, s.write); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}

	// The destination is a bound parameter, so a path containing a quote cannot alter the
	// statement. VACUUM INTO refuses to overwrite an existing file, which is the behaviour we
	// want: a stale template is a silent source of wrong test results.
	if _, err := s.write.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into %s: %w", path, err)
	}

	// VACUUM INTO creates its output at SQLite's default 0644 — it does not inherit from the source
	// database the way a recreated -wal does. It does not matter here (the template lives inside an
	// 0700 os.MkdirTemp and holds no real data), and it will matter the moment the streamed backup
	// reuses VACUUM INTO to write a file somebody downloads.
	if err := restrictMode(path); err != nil {
		return err
	}

	return nil
}

// NewDB clones the template into tb.TempDir() and opens it with production pragmas, both pools and
// a fresh statement counter.
//
// Every test gets its own file, so every test is t.Parallel(). The only serialisation is inside a
// single database, which is exactly the production topology.
func NewDB(tb testing.TB) *Store {
	tb.Helper()

	if templatePath == "" {
		tb.Fatal("store.NewDB: no template database — the package needs a TestMain that calls " +
			"store.InitTemplate (see internal/store/main_test.go)")
	}

	path := filepath.Join(tb.TempDir(), "test.db")
	CloneTemplate(tb, path)

	counter := NewCounter()

	s, err := Open(tb.Context(), path, WithStatementCounter(counter))
	if err != nil {
		tb.Fatalf("store.NewDB: open %s: %v", path, err)
	}

	name := tb.Name()
	counters.Store(name, counter)

	tb.Cleanup(func() {
		counters.Delete(name)

		if err := s.Close(); err != nil {
			tb.Errorf("store.NewDB: close %s: %v", path, err)
		}
	})

	return s
}

// CloneTemplate copies the template database to dst.
//
// A byte copy, NOT os.Link. docs/design/04-testing.md sketches "os.Link where possible", and that
// is wrong for a database that is about to be written to: a hard link shares the inode, so the
// first write through the clone mutates the template itself and silently contaminates every test
// that clones it afterwards — including tests that already passed. The saving is microseconds; the
// failure is a cross-test corruption that reproduces only under -shuffle=on.
func CloneTemplate(tb testing.TB, dst string) {
	tb.Helper()

	src, err := os.Open(templatePath)
	if err != nil {
		tb.Fatalf("store.CloneTemplate: open template %s: %v", templatePath, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			tb.Errorf("store.CloneTemplate: close template: %v", err)
		}
	}()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		tb.Fatalf("store.CloneTemplate: create %s: %v", dst, err)
	}

	if _, err := io.Copy(out, src); err != nil {
		tb.Fatalf("store.CloneTemplate: copy template to %s: %v", dst, err)
	}

	if err := out.Close(); err != nil {
		tb.Fatalf("store.CloneTemplate: close %s: %v", dst, err)
	}
}

// Counted returns the statement counter belonging to the Store that NewDB opened for tb.
//
// Pass the same testing.TB you passed to NewDB: the lookup is by test name, so a subtest asking
// for its parent's counter will not find it.
func Counted(tb testing.TB) *Counter {
	tb.Helper()

	v, ok := counters.Load(tb.Name())
	if !ok {
		tb.Fatalf("store.Counted: no counter for %s — call store.NewDB(t) with the same t first",
			tb.Name())

		return nil
	}

	c, ok := v.(*Counter)
	if !ok {
		tb.Fatalf("store.Counted: counter for %s has type %T", tb.Name(), v)

		return nil
	}

	return c
}

// Budget fails tb at cleanup time if more than maxStatements were executed after this call.
//
// Declared on every test that reads a collection. Raising a budget is a review signal, not a fix
// (.claude/rules/store-and-sql.md) — the test-integrity-auditor subagent flags it, and the failure
// message prints the offending SQL in order so that raising it is never the easiest option.
func (c *Counter) Budget(tb testing.TB, maxStatements int) {
	tb.Helper()

	start := c.Count()

	tb.Cleanup(func() {
		all := c.Statements()
		if len(all) < start {
			// Reset was called after the budget was declared; the window is meaningless.
			tb.Errorf("statement budget: the counter was reset after Budget was declared")

			return
		}

		used := all[start:]
		if len(used) > maxStatements {
			tb.Errorf("statement budget exceeded: %d statements, budget %d\n%s",
				len(used), maxStatements, format(used))
		}
	})
}
