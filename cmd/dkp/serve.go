package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

const (
	// dbPathEnv names the SQLite file the server will use once storage exists. serve resolves it
	// into config and NEVER OPENS IT. That is canonical conventions §13: /healthz must not touch
	// the database, because a DB-touching healthcheck lets Docker kill the container mid-migration.
	// There is no database code in this repo yet, which is precisely why the fence goes in now.
	dbPathEnv = "DKP_DB_PATH"

	// defaultAddr is the documented default listen address for the single binary.
	defaultAddr = ":8080"

	// readHeaderTimeout bounds how long a client may take to send its request headers. Required:
	// an http.Server without it is a Slowloris target and a gosec G112 failure.
	readHeaderTimeout = 10 * time.Second

	// readTimeout bounds headers plus body. ReadHeaderTimeout alone does not cover the body, which
	// leaves the RUDY shape open: send headers promptly, then dribble one body byte every few
	// seconds forever.
	readTimeout = 30 * time.Second

	// writeTimeout bounds how long a handler may take to write its response.
	//
	// SSE CAVEAT, and it is not hypothetical: canonical §7 puts a long-lived /events stream in this
	// server. A global WriteTimeout applies per connection, so once that endpoint exists this value
	// will silently cut every stream at 30s. The fix at that point is a per-route exemption
	// (http.ResponseController.SetWriteDeadline on the streaming handler), NOT deleting this line
	// and reopening the hole for every other route. Written down here because the symptom — a
	// stream that drops at exactly 30 seconds — reads like a client bug and is expensive to chase.
	writeTimeout = 30 * time.Second

	// idleTimeout bounds a keep-alive connection between requests. Without it, Go falls back to
	// ReadTimeout, and with both unset there is NO idle bound at all: an unauthenticated client can
	// open connections, complete one legitimate request on each, then hold them open indefinitely,
	// consuming a goroutine and a file descriptor apiece until the fd table is exhausted.
	idleTimeout = 120 * time.Second

	// shutdownTimeout bounds the graceful drain after the first signal. A second signal is not
	// caught — signal.NotifyContext restores the default disposition, so it kills the process.
	shutdownTimeout = 15 * time.Second

	// readyCheckTimeout bounds one /readyz evaluation. A readiness probe that can hang is a
	// readiness probe that turns a slow database into an unbounded pile of goroutines.
	readyCheckTimeout = 5 * time.Second
)

// serveConfig is the resolved configuration for one `dkp serve` invocation.
type serveConfig struct {
	// addr is the host:port to listen on.
	addr string

	// dbPath is DKP_DB_PATH.
	dbPath string

	// autoMigrate is DKP_AUTO_MIGRATE. False means pending migrations are reported through /readyz
	// rather than applied.
	autoMigrate bool
}

// readiness adapts the migrator to api.ReadyChecker.
//
// The adapter lives here rather than on either side of it so that internal/api does not import the
// migrator and internal/migrate does not import the API. cmd/ is where wiring belongs; both
// packages stay independently testable.
type readiness struct{ runner *migrate.Runner }

// Ready answers GET /readyz.
//
// A nil runner means the boot path could not build one at all — an unset or unusable DKP_DB_PATH.
// That reports failed rather than ready: the whole reason /readyz exists separately from /healthz
// is to be the endpoint that is allowed to say no.
func (r readiness) Ready() api.ReadyReport {
	if r.runner == nil {
		return api.ReadyReport{
			Check:  "migrations",
			State:  api.ReadyStateFailed,
			Detail: "no database configured",
		}
	}

	// Its own context: the readiness probe must not inherit a request's cancellation from a client
	// that hung up, and it must not outlive the check either.
	ctx, cancel := context.WithTimeout(context.Background(), readyCheckTimeout)
	defer cancel()

	status, err := r.runner.Status(ctx)
	if err != nil {
		return api.ReadyReport{
			Check:  "migrations",
			State:  api.ReadyStateFailed,
			Detail: err.Error(),
		}
	}

	switch status.State {
	case migrate.StatePending:
		// This exact body is a contract: docs/development/first-ten-prs.md:167 and
		// docs/design/06-cicd-and-release.md:523 both specify it, and the SPA renders a banner
		// containing the command verbatim.
		return api.ReadyReport{Check: "migrations", State: api.ReadyStatePending, Command: "dkp migrate"}
	case migrate.StateAhead:
		return api.ReadyReport{
			Check:  "migrations",
			State:  api.ReadyStateFailed,
			Detail: "database schema is newer than this binary",
		}
	case migrate.StateUpToDate:
		return api.ReadyReport{Check: "migrations", State: api.ReadyStateReady}
	}

	return api.ReadyReport{Check: "migrations", State: api.ReadyStateFailed, Detail: "unknown state"}
}

// newServeCmd builds `dkp serve`.
//
// ready, when non-nil, is called exactly once with the bound address after the listener exists and
// before the first request is accepted. Production passes nil; it is the address-discovery hook a
// test uses to bind 127.0.0.1:0 and learn the kernel-assigned port, instead of guessing a fixed
// port or sleeping and hoping.
func newServeCmd(ready func(net.Addr)) *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Dragon Kill Party HTTP server",
		Long: "Run the HTTP server. It serves the public API, the embedded web UI and the\n" +
			"container healthcheck endpoint /healthz.\n\n" +
			"DKP_DB_PATH selects the SQLite database file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The signal context is derived from the command context, so a caller that cancels its
			// own context (a test, a supervisor) drains the server the same way SIGTERM does.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cfg := serveConfig{
				addr:        addr,
				dbPath:      os.Getenv(dbPathEnv),
				autoMigrate: autoMigrateEnabled(),
			}

			return runServe(ctx, cfg, ready)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", defaultAddr, "host:port to listen on")

	return cmd
}

// runServe binds cfg.addr, serves until ctx is cancelled, then drains within shutdownTimeout.
//
// It declares no routes. Law 1: routes are declared only in internal/api, so the whole handler
// tree arrives as api.NewMux().
func runServe(ctx context.Context, cfg serveConfig, ready func(net.Addr)) error {
	// Migrate BEFORE the listener opens. Not for tidiness: a listener bound first would accept
	// requests during the migration, and the SPA would render half-migrated data as though it were
	// the guild's real standings.
	//
	// A failure here is fatal only when it is a failed migration or a downgrade — see
	// migrateOnBoot. Everything else serves, because /healthz must keep answering 200 whatever the
	// database is doing (canonical §13).
	runner, err := newMigrator(cfg.dbPath, cfg.autoMigrate)
	if err != nil {
		slog.ErrorContext(ctx, "could not build the migrator; serving anyway so /healthz stays up",
			"error", err, "db_path", cfg.dbPath)
	}

	if runner != nil {
		if migrateErr := migrateOnBoot(ctx, runner); migrateErr != nil {
			return migrateErr
		}
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	srv := &http.Server{
		Handler:           api.NewMuxWithReadiness(readiness{runner: runner}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	bound := ln.Addr()

	slog.InfoContext(ctx, "server listening", "addr", bound.String(), "db_path", cfg.dbPath)

	// The listener is already bound here, so a connection that arrives before Serve accepts it
	// waits in the kernel backlog rather than being refused. That is what makes the hook a
	// readiness signal and not a race.
	if ready != nil {
		ready(bound)
	}

	serveErr := make(chan error, 1)

	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return closedOrErr(err, bound)
	case <-ctx.Done():
	}

	slog.InfoContext(ctx, "shutting down", "addr", bound.String(), "timeout", shutdownTimeout)

	// WithoutCancel: ctx is already done. Shutdown needs a live deadline to drain in-flight
	// requests against, and inheriting a cancelled context would abandon them immediately.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down server on %s: %w", bound, err)
	}

	return closedOrErr(<-serveErr, bound)
}

// closedOrErr maps the sentinel http.ErrServerClosed to nil — a deliberate shutdown is not a
// failure — and wraps anything else with the address it happened on.
func closedOrErr(err error, addr net.Addr) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve on %s: %w", addr, err)
}
