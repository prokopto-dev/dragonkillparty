package store

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"go.uber.org/goleak"

	"github.com/prokopto-dev/dragonkillparty/db"
)

// TestMain builds the template once, from the real migrations, and verifies no goroutine outlives
// the package.
//
// PR 2 built this template from a stand-in: one STRICT table padded with deterministic rows to the
// ~250 KB docs/design/04-testing.md projected for the real schema, because migrations did not exist
// yet. That stand-in and its ballast are gone. testing.go's SchemaFunc was left as an injected
// parameter for exactly this moment, and the swap is a one-line change to this call — which is what
// "PR 3 passes the goose runner here and deletes nothing else" meant.
//
// One consequence is worth stating rather than discovering: the real schema is currently ONE table,
// so the template is a few KB rather than ~250 KB, and BenchmarkNewDB_Clone now measures a much
// smaller copy. Item V4 in docs/development/verify-before-phase-0.md is updated with the real
// number and the reason it moved; it will drift back up as the schema grows, and that is the point
// of measuring it against the real thing.
//
// goleak.VerifyTestMain calls os.Exit itself, so a deferred cleanup in TestMain would never run —
// goleak.Cleanup is the hook that lets the template directory be removed at all.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := InitTemplate(context.Background(), ApplySchema(fsys))
	if err != nil {
		slog.Error("build the store package's template database", "error", err)
		os.Exit(1)
	}

	goleak.VerifyTestMain(m, goleak.Cleanup(func(exitCode int) {
		cleanup()
		os.Exit(exitCode)
	}))
}
