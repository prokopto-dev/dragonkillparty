// Package integration holds black-box integration tests: they build the real HTTP server over a real
// SQLite database and drive it with an ordinary http.Client, asserting the behaviour a bot or the SPA
// would see. There is no testenv package and no ClientAs — there is no auth package until ROADMAP
// Phase 2 deliverable 1, so there is no such thing as an officer to be (decision record §Q2).
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/prokopto-dev/dragonkillparty/db"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestMain builds the store template once, from the real migrations, so every test can clone a
// migrated database through store.NewDB. It is a ~20-line copy of internal/store/main_test.go, which
// is the shape the decision record and EXAMPLE_ENDPOINT.md's step 7 prescribe: store.InitTemplate
// over store.ApplySchema of the embedded migration set.
//
// No goleak here: the httptest servers the tests start are closed in each test's Cleanup, and the
// store package already runs goleak over the pool lifecycle. Adding it here would duplicate that
// coverage and flake on any goroutine an httptest server leaves briefly draining.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the integration template database", "error", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}
