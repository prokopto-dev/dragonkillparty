package seed_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/prokopto-dev/dragonkillparty/db"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestMain builds the store template once, from the real migrations, so every test here can clone a
// migrated database — with the ledger schema, the four append-only triggers and the seeded pool and
// system accounts — through store.NewDB. The same ~20-line shape as internal/ledger/main_test.go.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the seed template database", "error", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}
