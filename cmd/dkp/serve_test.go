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
// THIS ASSERTION IS NO LONGER VACUOUS, and the change is worth recording because the comment here
// used to say the opposite. Until PR 3 there was no database code at all — serve read DKP_DB_PATH
// into a config field and nothing opened it — so no change to the code could have made this test
// fail. PR 3 landed migrate-on-boot and the readiness checker, and both open that path; PR 4 put a
// Huma mount and two middleware layers in front of the handler. The unreadable path below is now
// genuinely exercised on the boot path, and /healthz answering 200 through it is a real result.
//
// Why it matters: Docker's HEALTHCHECK calls /healthz, and a healthcheck that fails during a long
// migration makes Docker kill the container mid-migration, which is how a guild loses its ledger.
// This is the tripwire that goes red on the day someone makes /healthz "useful" by pinging the
// database or reporting the migration version. PR 4 considered making it a Huma operation and
// decided against it for a related reason — see internal/api/health.go.
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

// TestServe_MalformedTrustedProxies_RefusesToStart is the "fail the parse loudly" half of issue #98.
//
// THE ASYMMETRY WITH DKP_READYZ_DETAIL IS THE POINT. That variable is a disclosure preference, so an
// unrecognised value logs and falls back to the closed setting — the process keeps serving and
// /healthz keeps answering, which canonical §13 requires. DKP_TRUSTED_PROXIES is a TRUST BOUNDARY,
// and both of its failure modes are silent from outside: an operator who typoed a CIDR would get
// "trust nothing", which is every client behind their proxy sharing one rate-limit bucket and one
// recorded address, with nothing but their own memory to say why. §9.1 already refuses to start on a
// malformed secrets file for the same reason, and an environment variable is fixed in the time it
// takes to read the message.
//
// No t.Parallel: t.Setenv panics in a parallel test, and the variable is the subject.
func TestServe_MalformedTrustedProxies_RefusesToStart(t *testing.T) {
	t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "dkp.db"))
	t.Setenv(trustedProxiesEnv, "127.0.0.1/32, proxy.example.com")

	bound := make(chan net.Addr, 1)

	cmd := newServeCmd(func(addr net.Addr) { bound <- addr })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--addr", "127.0.0.1:0"})

	err := cmd.ExecuteContext(t.Context())

	require.Error(t, err, "a malformed trust list must stop the boot, not be silently discarded")
	require.Contains(t, err.Error(), trustedProxiesEnv,
		"the error must name the variable; an operator reading it has to know what to fix")
	require.Contains(t, err.Error(), "proxy.example.com",
		"the error must name the entry that failed, not just that something did")
	require.Empty(t, bound, "the listener was bound despite an unusable trust list")
}

// TestServe_WellFormedTrustedProxies_Boots keeps the fatal branch above from becoming a boot failure
// for the operators who configured the variable correctly.
//
// A test that only ever watches a gate refuse is a test that would still pass if the gate refused
// everything, which for this one means every proxied deployment crash-looping.
func TestServe_WellFormedTrustedProxies_Boots(t *testing.T) {
	t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "dkp.db"))
	t.Setenv(trustedProxiesEnv, " 127.0.0.1 , 10.0.0.0/8 ,")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addrCh := make(chan net.Addr, 1)

	cmd := newServeCmd(func(addr net.Addr) { addrCh <- addr })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--addr", "127.0.0.1:0"})

	serveErr := make(chan error, 1)

	go func() { serveErr <- cmd.ExecuteContext(ctx) }()

	select {
	case <-addrCh:
	case err := <-serveErr:
		require.FailNowf(t, "server exited instead of binding", "error: %v", err)
	}

	cancel()

	require.NoError(t, <-serveErr)
}
