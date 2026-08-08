package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHealthcheck_UnreadableDatabase_StillSucceeds is the PR 7 acceptance criterion for the
// container HEALTHCHECK, and it is the same fence as canonical §13 seen from the command side:
// `dkp healthcheck` must exit 0 while the database is unreachable, because Docker uses its exit code
// to decide whether to kill the container. A healthcheck that failed during a long migration would
// let Docker restart the container mid-migration, which is how a guild loses its ledger.
//
// The setup deliberately mirrors TestServe_UnreadableDatabasePath_HealthzStillReturns200: a real
// `dkp serve` is booted against a DKP_DB_PATH that cannot be opened, and then `dkp healthcheck` is
// pointed at the address the kernel assigned. The command opens no database of its own — it is a
// plain loopback GET /healthz — so the only way it could fail is if /healthz itself depended on the
// database, which is precisely what this asserts it does not.
//
// No t.Parallel: t.Setenv panics in a parallel test, and the unreadable path is the subject.
func TestHealthcheck_UnreadableDatabase_StillSucceeds(t *testing.T) {
	// A path under a directory that does not exist: unopenable and unusable as a SQLite file. The
	// server must serve /healthz anyway, and the healthcheck command must be satisfied by it.
	t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "nonexistent", "dkp.db"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addrCh := make(chan net.Addr, 1)

	serveCmd := newServeCmd(func(addr net.Addr) { addrCh <- addr })
	serveCmd.SetOut(io.Discard)
	serveCmd.SetErr(io.Discard)
	serveCmd.SetArgs([]string{"--addr", "127.0.0.1:0"})

	serveErr := make(chan error, 1)

	go func() { serveErr <- serveCmd.ExecuteContext(ctx) }()

	var bound net.Addr

	select {
	case bound = <-addrCh:
	case err := <-serveErr:
		require.FailNowf(t, "server exited before it bound an address", "error: %v", err)
	}

	url := "http://" + bound.String() + "/healthz"

	// Run the real command, not just runHealthcheck: the wiring (flag parsing, RunE) is part of what
	// the Dockerfile invokes, so a change that broke the command surface should fail here.
	hc := newHealthcheckCmd()
	hc.SetOut(io.Discard)
	hc.SetErr(io.Discard)
	hc.SetArgs([]string{"--url", url})

	require.NoError(t, hc.ExecuteContext(ctx),
		"dkp healthcheck must exit 0 against a live /healthz even with an unreadable database")

	cancel()

	require.NoError(t, <-serveErr, "serve must shut down cleanly when its context is cancelled")
}

// TestHealthcheck_ServerDown_Fails proves the command is not tautological: with nothing listening,
// it must fail. A healthcheck that always exits 0 is worse than none — Docker would never restart a
// dead container. Using a port that was bound and immediately released makes "connection refused"
// deterministic without racing a real server.
func TestHealthcheck_ServerDown_Fails(t *testing.T) {
	t.Parallel()

	// Bind :0 to get a free port from the kernel, then close it — the port is now almost certainly
	// free and refuses connections, which is what we want to probe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "reserve a free port")

	url := "http://" + ln.Addr().String() + "/healthz"
	require.NoError(t, ln.Close(), "release the reserved port")

	hc := newHealthcheckCmd()
	hc.SetOut(io.Discard)
	hc.SetErr(io.Discard)
	hc.SetArgs([]string{"--url", url})

	require.Error(t, hc.ExecuteContext(t.Context()),
		"dkp healthcheck must fail when nothing is listening")
}

// TestHealthcheck_Non200_Fails asserts the status-code mapping directly: a server that answers
// something other than 200 on the probed path makes the command fail. Without this, the command
// could treat any answer as healthy, and a half-broken server would look up.
func TestHealthcheck_Non200_Fails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	hc := newHealthcheckCmd()
	hc.SetOut(io.Discard)
	hc.SetErr(io.Discard)
	hc.SetArgs([]string{"--url", srv.URL + "/healthz"})

	require.Error(t, hc.ExecuteContext(t.Context()),
		"dkp healthcheck must fail on a non-200 response")
}
