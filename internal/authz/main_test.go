package authz_test

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
// template directory, because goleak.VerifyTestMain calls os.Exit itself and a deferred cleanup would
// never run.
//
// The catalogue tests need none of this — they read a Markdown file — but the reconciliation tests
// need a real SQLite database with the real permission table, because there is no fake Queries
// implementation in this repository and a lint rule forbids adding one
// (.claude/rules/go-idioms.md). A reconciliation asserted against a mock proves the mock agrees with
// the code, not that the FK to permission(key) does.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the authz package's template database", "error", err)
		os.Exit(1)
	}

	goleak.VerifyTestMain(m, goleak.Cleanup(func(exitCode int) {
		cleanup()
		os.Exit(exitCode)
	}))
}
