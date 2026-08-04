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

// healthzAttempts bounds the readiness retry. The listener is already bound when the ready hook
// fires, so the kernel backlog holds the connection and the first attempt normally wins; the
// retries only cover the sliver before Serve accepts. No sleep, fixed or otherwise: time.Sleep is
// the largest flake source in Go services and is grep-banned in tests.
const healthzAttempts = 50

// TestServe_UnreadableDatabasePath_HealthzStillReturns200 fences canonical conventions §13:
// /healthz must never touch the database.
//
// READ THIS ASSERTION HONESTLY. It is vacuously true today. There is no database code anywhere in
// this repo — serve reads DKP_DB_PATH into a config field and nothing opens it — so no change to
// today's code could make this test fail, and passing it proves nothing about today's behaviour.
//
// Its entire value is in the future. PR 3 adds migrate-on-boot, and with it the first moment where
// making /healthz "useful" (ping the DB, check the migration version) looks like an improvement.
// It is not: Docker's HEALTHCHECK calls /healthz, and a healthcheck that fails during a long
// migration makes Docker kill the container mid-migration. This test is the tripwire that goes red
// on the day someone reaches for that improvement. It is installed before the code it gates, which
// is the point, not a hole.
//
// No t.Parallel here, deliberately: t.Setenv panics in a parallel test. The env var is the subject
// of the test, so it wins over the house rule — and the conflict is noted rather than hidden.
func TestServe_UnreadableDatabasePath_HealthzStillReturns200(t *testing.T) {
	// A path under a directory that does not exist: unreadable, unopenable, and unusable as a
	// SQLite file. Nothing must care.
	t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "nonexistent", "dkp.db"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addrCh := make(chan net.Addr, 1)

	cmd := newServeCmd(func(addr net.Addr) { addrCh <- addr })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--addr", "127.0.0.1:0"})

	serveErr := make(chan error, 1)

	go func() { serveErr <- cmd.ExecuteContext(ctx) }()

	var bound net.Addr

	select {
	case bound = <-addrCh:
	case err := <-serveErr:
		require.FailNowf(t, "server exited before it bound an address", "error: %v", err)
	}

	healthURL := "http://" + bound.String() + "/healthz"
	client := &http.Client{Timeout: 5 * time.Second}

	var (
		resp    *http.Response
		lastErr error
	)

	for range healthzAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		require.NoError(t, err, "build healthz request")

		got, err := client.Do(req)
		if err != nil {
			// Kept, not swallowed: if every attempt fails, this is the only clue why.
			lastErr = err

			continue
		}

		resp = got

		break
	}

	require.NotNilf(t, resp, "GET %s never answered in %d attempts; last error: %v",
		healthURL, healthzAttempts, lastErr)

	defer func() { require.NoError(t, resp.Body.Close(), "close healthz body") }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "read healthz body")

	require.Equal(t, http.StatusOK, resp.StatusCode, "healthz must not depend on the database")
	require.JSONEq(t, `{"status":"ok"}`, string(body))

	cancel()

	require.NoError(t, <-serveErr, "serve must shut down cleanly when its context is cancelled")
}
