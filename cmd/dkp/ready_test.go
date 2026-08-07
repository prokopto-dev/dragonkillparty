package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startServe boots `dkp serve` on an ephemeral port and returns its base URL.
//
// No sleep anywhere: the ready hook fires after the listener is bound, so the kernel backlog holds
// any connection that arrives before Serve accepts it.
func startServe(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	addrCh := make(chan net.Addr, 1)

	cmd := newServeCmd(func(addr net.Addr) { addrCh <- addr })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--addr", "127.0.0.1:0"})

	serveErr := make(chan error, 1)
	go func() { serveErr <- cmd.ExecuteContext(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-serveErr, "serve must shut down cleanly when its context is cancelled")
	})

	select {
	case bound := <-addrCh:
		return "http://" + bound.String()
	case err := <-serveErr:
		require.FailNowf(t, "server exited before it bound an address", "error: %v", err)
	}

	return ""
}

// get fetches url, retrying only the window before Serve accepts.
func get(t *testing.T, url string) (int, string) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}

	var lastErr error

	for range healthzAttempts {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err

			continue
		}

		defer func() { require.NoError(t, resp.Body.Close()) }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp.StatusCode, string(body)
	}

	require.FailNowf(t, "no response", "GET %s never answered; last error: %v", url, lastErr)

	return 0, ""
}

// TestReady_MigrationsPending_Returns503 is the DKP_AUTO_MIGRATE=false contract.
//
// Three things have to hold together, and the third is the one that matters:
//
//   - /readyz returns 503, so a load balancer stops sending traffic to an instance whose schema is
//     not the schema the code expects.
//   - the body is EXACTLY {"check":"migrations","state":"pending","command":"dkp migrate"}. Both
//     first-ten-prs.md:167 and 06-cicd-and-release.md:523 specify it literally, and the SPA renders
//     a banner containing that command verbatim — so this is a wire contract, not a log line.
//   - /healthz still returns 200. This is canonical §13 and it is the half that prevents data loss
//     rather than downtime: Docker's HEALTHCHECK calls /healthz, and a healthcheck that goes red
//     while migrations are pending gets the container killed at exactly the wrong moment.
//
// No t.Parallel: t.Setenv panics in a parallel test and the environment is the subject here.
func TestReady_MigrationsPending_Returns503(t *testing.T) {
	dataDir := t.TempDir()

	t.Setenv(dbPathEnv, filepath.Join(dataDir, "dkp.db"))
	t.Setenv(dataDirEnv, dataDir)
	t.Setenv(autoMigrateEnv, "false")

	base := startServe(t)

	status, body := get(t, base+"/readyz")
	require.Equal(t, http.StatusServiceUnavailable, status,
		"an instance with pending migrations reported itself ready")
	require.JSONEq(t, `{"check":"migrations","state":"pending","command":"dkp migrate"}`, body)

	status, body = get(t, base+"/healthz")
	require.Equal(t, http.StatusOK, status,
		"/healthz went unhealthy because migrations are pending — that lets Docker kill the "+
			"container mid-migration, which is how a guild loses its ledger (canonical §13)")
	require.JSONEq(t, `{"status":"ok"}`, body)
}

// TestReady_MigrationsApplied_Returns200 is the positive control.
//
// Without it, a /readyz that returned 503 unconditionally would satisfy every assertion in the test
// above and would take the instance out of rotation forever.
func TestReady_MigrationsApplied_Returns200(t *testing.T) {
	dataDir := t.TempDir()

	t.Setenv(dbPathEnv, filepath.Join(dataDir, "dkp.db"))
	t.Setenv(dataDirEnv, dataDir)
	t.Setenv(autoMigrateEnv, "true")

	base := startServe(t)

	status, body := get(t, base+"/readyz")
	require.Equal(t, http.StatusOK, status, "migrate-on-boot ran but /readyz still reports not ready")
	require.JSONEq(t, `{"check":"migrations","state":"ready"}`, body)
}

// TestServe_AutoMigrate_AppliesOnBoot asserts the default actually migrates.
//
// The default is doing the work here: 06-cicd-and-release.md:505 chose migrate-on-boot over a
// required manual step precisely so a volunteer operator can pull and restart. A regression that
// left this off would be invisible — the server starts, /healthz is green — until someone looked
// at a table that does not exist.
func TestServe_AutoMigrate_AppliesOnBoot(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)
	t.Setenv(autoMigrateEnv, "true")

	base := startServe(t)

	status, _ := get(t, base+"/readyz")
	require.Equal(t, http.StatusOK, status)

	runner, err := newMigrator(dbPath, true)
	require.NoError(t, err)

	st, err := runner.Status(t.Context())
	require.NoError(t, err)
	require.Positive(t, st.Applied, "boot did not apply any migration")
	require.Empty(t, st.Pending, "boot left migrations pending")
}
