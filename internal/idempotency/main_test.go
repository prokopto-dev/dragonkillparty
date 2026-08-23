package idempotency_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"go.uber.org/goleak"

	"github.com/prokopto-dev/dragonkillparty/db"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestMain builds the store template once, from the real migrations, so every test in this package
// can clone a migrated database through store.NewDB. It mirrors internal/guild/main_test.go: the
// template is built from db.SQLiteMigrations, and goleak.Cleanup is the hook that removes the
// template directory, because goleak.VerifyTestMain calls os.Exit itself and a deferred cleanup
// would never run.
//
// REAL SQLITE, NEVER A FAKE Queries (.claude/rules/go-idioms.md). The property this package exists
// to hold — one effect from N concurrent duplicates — is a property of a unique index and a
// single-writer pool. A fake would assert it against a Go map, which is a different claim about a
// different thing.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the idempotency package's template database", "error", err)
		os.Exit(1)
	}

	goleak.VerifyTestMain(m, goleak.Cleanup(func(exitCode int) {
		cleanup()
		os.Exit(exitCode)
	}))
}
