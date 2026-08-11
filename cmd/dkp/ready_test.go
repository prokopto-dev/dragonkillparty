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

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
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

// TestReady_MissingAppendOnlyTrigger_Returns503Degraded is issue #59 end to end, through the real
// binary, the real migration set and a real HTTP request.
//
// The database here is fully migrated and then damaged the way a real one gets damaged: a trigger
// dropped behind the boot path's back, as a fork's build, a patched image or a support session with a
// SQLite client would leave it. Nothing else is wrong — integrity_check passes, foreign_key_check
// passes, no row moved — which is exactly why this was invisible. The boot path logs it once, at error
// level, and before this change nothing said so again: the site served a ledger with no database-level
// protection indefinitely, looking entirely normal.
//
// Three assertions, and the third is the one that keeps this honest:
//
//   - /readyz answers 503 with state `degraded`, so a blackbox monitor that only reads status codes
//     fires, and keeps firing, for as long as the damage is there.
//   - the body names the missing trigger, because this request comes from loopback AND the operator
//     has set DKP_READYZ_DETAIL=local. Both halves are required since #74; the test below is the
//     default, where the same request gets the verdict alone.
//   - /healthz still answers 200. Canonical §13: Docker's HEALTHCHECK calls /healthz, and a
//     healthcheck that goes red here would kill the container of a guild whose ledger is already
//     unprotected — losing the raid night on top of it and fixing nothing.
//
// No t.Parallel: t.Setenv panics in a parallel test.
func TestReady_MissingAppendOnlyTrigger_Returns503Degraded(t *testing.T) {
	t.Setenv(readyDetailEnv, string(api.ReadyDetailLocal))

	base := serveDamagedLedger(t)

	status, body := get(t, base+"/readyz")
	require.Equal(t, http.StatusServiceUnavailable, status,
		"a database whose ledger history can be rewritten reported itself ready. That is the whole of "+
			"issue #59: the boot log fires once into a sink nobody watched, and monitoring never hears "+
			"about it again.")
	require.Contains(t, body, `"check":"ledger_append_only"`)
	require.Contains(t, body, `"state":"degraded"`)
	require.Contains(t, body, "trg_ledger_entry_no_update",
		"the detail must name the missing trigger for an operator who asked for it with "+
			"DKP_READYZ_DETAIL=local — that is the actionable content, and this request came from "+
			"loopback with nothing claiming to relay it")

	status, body = get(t, base+"/healthz")
	require.Equal(t, http.StatusOK, status,
		"/healthz went unhealthy because the ledger lost a trigger — that lets Docker kill the "+
			"container of a guild that has already lost the guarantee (canonical §13)")
	require.JSONEq(t, `{"status":"ok"}`, body)
}

// TestReady_MissingAppendOnlyTrigger_DefaultPolicyWithholdsTheDetail is issue #74 through the real
// binary, and it is the same damaged database as the test above with one thing removed: the operator
// never set DKP_READYZ_DETAIL.
//
// That default used to disclose. `dkp serve` recommends running behind a reverse proxy on the same
// host, where every caller in the world reaches the listener from 127.0.0.1 — so "loopback sees the
// detail" meant the public internet saw which append-only guarantee this guild's ledger had lost. The
// request here comes from loopback, which is the shape that used to qualify, and must now get the
// verdict and nothing else.
//
// The verdict itself is asserted alongside, because a fix that stopped reporting the damage would
// satisfy the redaction and undo #59.
//
// No t.Parallel: t.Setenv panics in a parallel test.
func TestReady_MissingAppendOnlyTrigger_DefaultPolicyWithholdsTheDetail(t *testing.T) {
	base := serveDamagedLedger(t)

	status, body := get(t, base+"/readyz")
	require.Equal(t, http.StatusServiceUnavailable, status,
		"the redaction ate the verdict: monitoring must still see that this instance is not ready")
	require.Contains(t, body, `"check":"ledger_append_only"`)
	require.Contains(t, body, `"state":"degraded"`)
	require.NotContains(t, body, "trg_ledger_entry_no_update",
		"a caller who could be anyone behind the recommended reverse proxy was told which append-only "+
			"trigger this guild's ledger is missing, with no operator opt-in (#74)")
	require.NotContains(t, body, `"detail"`)
}

// serveDamagedLedger boots `dkp serve` over a fully migrated database with one append-only trigger
// dropped behind the boot path's back, and returns its base URL.
//
// The damage is applied AFTER migrating, so the database reports itself fully up to date — which is
// the state the check has to be able to disbelieve, and the state a fork's build, a patched image or a
// support session with a SQLite client leaves behind. Nothing else is wrong: integrity_check passes,
// foreign_key_check passes, no row moved, which is exactly why this was invisible before #59.
//
// It calls t.Setenv, so no caller may be parallel.
func serveDamagedLedger(t *testing.T) string {
	t.Helper()

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)
	t.Setenv(autoMigrateEnv, "true")

	runner, err := newMigrator(dbPath, true)
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	st, err := store.Open(t.Context(), dbPath)
	require.NoError(t, err)

	st.ExecForTest(t, "DROP TRIGGER trg_ledger_entry_no_update")
	require.NoError(t, st.Close())

	return startServe(t)
}

// TestReadyDetailPolicy_Env_ResolvesAndFailsClosed covers the wiring between DKP_READYZ_DETAIL and the
// policy the server is built with.
//
// api.ParseReadyDetailPolicy owns the grammar and has its own table test; what is asserted here is the
// part cmd/ owns and could get wrong on its own — that the variable is read at all, and that a value
// the binary does not understand becomes "disclose to nobody" rather than a boot failure or, far
// worse, a permissive fallback. A misconfigured instance stays up and stays quiet: canonical §13 wants
// /healthz answering whatever else is wrong.
//
// No t.Parallel: t.Setenv panics in a parallel test and the environment is the subject.
func TestReadyDetailPolicy_Env_ResolvesAndFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  api.ReadyDetailPolicy
	}{
		// Empty is what os.Getenv returns for an unset variable, so this row IS the unset case —
		// written as an explicit empty value rather than by leaving the environment alone, because a
		// developer's shell is not a controlled input.
		{name: "unset is the default", value: "", want: api.ReadyDetailNever},
		{name: "never", value: "never", want: api.ReadyDetailNever},
		{name: "local", value: "local", want: api.ReadyDetailLocal},
		{name: "always", value: "always", want: api.ReadyDetailAlways},
		{
			name: "a value this binary does not know fails closed", value: "yes-please",
			want: api.ReadyDetailNever,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(readyDetailEnv, tc.value)

			require.Equal(t, tc.want, readyDetailPolicy(t.Context()))
		})
	}
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
