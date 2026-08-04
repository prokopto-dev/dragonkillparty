package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// templateTargetBytes is the size the stand-in template is padded to.
//
// docs/design/04-testing.md projects the real schema plus its immutable reference data at ~250 KB,
// and item V4 of docs/development/verify-before-phase-0.md asks whether cloning that costs ~0.3 ms
// or ~3 ms. File size dominates a file copy, so benchmarking an empty 4 KB database would answer a
// much easier question and report V4 as resolved when it is not. Padding to the projected size is
// what makes BenchmarkNewDB_Clone's number mean something today.
//
// PR 3 deletes standInSchema and everything below it: the goose runner supplies the real schema,
// the real size, and a number that needs no caveat.
const templateTargetBytes = 256 << 10

// ballastRowPayload is a fixed 96-byte payload. Fixed, not random: two runs of the suite must
// build byte-identical templates, and internal/store has no injected RNG to seed from.
var ballastRowPayload = strings.Repeat("dkp", 32)

// TestMain builds the template once and verifies no goroutine outlives the package.
//
// goleak.VerifyTestMain calls os.Exit itself, so a deferred cleanup in TestMain would never run —
// goleak.Cleanup is the hook that lets the template directory be removed at all.
func TestMain(m *testing.M) {
	cleanup, err := InitTemplate(context.Background(), standInSchema)
	if err != nil {
		slog.Error("build the store package's template database", "error", err)
		os.Exit(1)
	}

	goleak.VerifyTestMain(m, goleak.Cleanup(func(exitCode int) {
		cleanup()
		os.Exit(exitCode)
	}))
}

// standInSchema is PR 2's stand-in for PR 3's migrations.
//
// It creates one STRICT table — STRICT because every table in this product is, and a harness that
// exercised a laxer table would not exercise the type checking the real schema depends on — and
// fills it until the file crosses templateTargetBytes.
func standInSchema(ctx context.Context, db *sql.DB) error {
	const ddl = `
CREATE TABLE template_ballast (
    id      INTEGER PRIMARY KEY,
    payload TEXT NOT NULL
) STRICT;`

	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create template_ballast: %w", err)
	}

	// Batched in one transaction: a row at a time would fsync per insert and turn a ~100 ms
	// template build into several seconds.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ballast transaction: %w", err)
	}

	for id := 1; ; id++ {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO template_ballast (id, payload) VALUES (?, ?)", id, ballastRowPayload); err != nil {
			return fmt.Errorf("insert ballast row %d: %w", id, err)
		}

		if id%256 != 0 {
			continue
		}

		size, err := fileSize(ctx, tx)
		if err != nil {
			return err
		}

		if size >= templateTargetBytes {
			break
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ballast transaction: %w", err)
	}

	return nil
}

// fileSize reports the database's size in bytes, from the page count and page size rather than
// from os.Stat: inside an open write transaction the pages are still in the WAL, so os.Stat would
// report the pre-transaction size and the loop above would never terminate.
func fileSize(ctx context.Context, tx *sql.Tx) (int64, error) {
	var pageCount, pageSize int64

	if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("read page_count: %w", err)
	}

	if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("read page_size: %w", err)
	}

	return pageCount * pageSize, nil
}
