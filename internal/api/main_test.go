package api

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/prokopto-dev/dragonkillparty/db"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestMain builds the store template once so the store-backed handler tests in this package —
// guild_test.go — can clone a migrated database through store.NewDB. The tests that need no database
// (meta, errors, docs, arch, spec) ignore it and pay only the one-time template build, which is a
// few kilobytes at this schema size.
//
// No goleak here: this package starts no long-lived goroutines of its own, and the httptest servers
// the tests spin up are closed in each test's Cleanup. The store package already runs goleak over
// the pool lifecycle.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the api package's template database", "error", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}
