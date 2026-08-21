package auth_test

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
// can clone a migrated database through store.NewDB. It mirrors internal/authz/main_test.go.
//
// THE RESOLVER TESTS NEED A REAL DATABASE and there is no way around it: there is no fake Queries
// implementation in this repository and a lint rule forbids adding one (.claude/rules/go-idioms.md).
// A resolver asserted against a mock would prove that the mock agrees with the code — not that a
// revoked row is actually refused, which is the property ADR-0011 sells, and not that the unique
// index on token_hash exists, which is what stops a duplicate secret being minted.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the auth package's template database", "error", err)
		os.Exit(1)
	}

	goleak.VerifyTestMain(m, goleak.Cleanup(func(exitCode int) {
		cleanup()
		os.Exit(exitCode)
	}))
}
