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
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/ui"
)

const (
	// dbPathEnv names the SQLite file the server uses.
	//
	// PR 3 made this real: migrateOnBoot and the readiness adapter both open it. What has NOT
	// changed is the fence it was put here for — canonical conventions §13, /healthz must not touch
	// the database, because a DB-touching healthcheck lets Docker kill the container mid-migration.
	// The database is opened on the boot path and by /readyz, and by nothing that /healthz reaches.
	dbPathEnv = "DKP_DB_PATH"

	// defaultAddr is the documented default listen address for the single binary.
	defaultAddr = ":8080"

	// apiBaseEnv names the value GET /config.json reports as API_BASE. Empty (the default) is
	// same-origin. It exists so a reverse-proxied or split deploy can point the SPA at the API's
	// real base without rebuilding the bundle — the capability that proves the SPA is a client.
	apiBaseEnv = "DKP_API_BASE"

	// readyDetailEnv names who may see a /readyz `detail`: never (the default), local, or always.
	//
	// It is the explicit signal #74 needed. The peer address cannot supply one behind the reverse
	// proxy this project recommends — every caller arrives from 127.0.0.1 there — so the endpoint
	// discloses nothing until an operator, who knows their own topology, says otherwise. Parsed by
	// api.ParseReadyDetailPolicy; an unrecognised value logs and falls back to never.
	readyDetailEnv = "DKP_READYZ_DETAIL"

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

	// checkMigrations is the `check` value of the schema-version check. It is a wire contract: the
	// migrations-pending body is specified literally in docs/development/first-ten-prs.md:167 and
	// docs/design/06-cicd-and-release.md:523, and the SPA renders the command it carries verbatim.
	checkMigrations = "migrations"

	// checkLedgerAppendOnly is the `check` value of the ledger's database-level append-only guarantee:
	// the four triggers that make an UPDATE or DELETE on ledger history raise, and the two tables they
	// are attached to.
	//
	// Named for the guarantee rather than for the triggers because it reports both halves, and a
	// monitoring rule that fires on `check=ledger_append_only` should not have to know that a dropped
	// TABLE takes its triggers with it — which is exactly the way through a triggers-only check
	// (internal/store.AppendOnlyState).
	checkLedgerAppendOnly = "ledger_append_only"
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

	// readyDetail is DKP_READYZ_DETAIL, resolved and already failed closed.
	readyDetail api.ReadyDetailPolicy
}

// readyDetailPolicy resolves DKP_READYZ_DETAIL.
//
// A value this binary does not understand is a misconfiguration, not a reason to refuse to boot:
// canonical §13 wants /healthz answering whatever else is wrong, and crash-looping over a typo in an
// optional disclosure setting is the opposite of that. So it logs the value and the accepted set — an
// operator who set it and sees no detail needs to be told why — and returns the policy
// ParseReadyDetailPolicy failed closed to.
func readyDetailPolicy(ctx context.Context) api.ReadyDetailPolicy {
	policy, err := api.ParseReadyDetailPolicy(os.Getenv(readyDetailEnv))
	if err != nil {
		slog.WarnContext(ctx, "unrecognised readiness detail policy; withholding every /readyz detail",
			"error", err, "env", readyDetailEnv, "policy", policy)
	}

	return policy
}

// readiness adapts the migrator to api.ReadyChecker.
//
// The adapter lives here rather than on either side of it so that internal/api does not import the
// migrator and internal/migrate does not import the API. cmd/ is where wiring belongs; both
// packages stay independently testable.
type readiness struct{ runner *migrate.Runner }

// Ready answers GET /readyz.
//
// It is an ORDERED LADDER over the checks, reporting the first one that is not ready, because the
// response envelope carries one check (api.ReadyReport) and the migrations body is a wire contract.
// Migrations therefore come first and that ordering is load-bearing rather than incidental: an
// instance with pending migrations has to keep answering
// {"check":"migrations","state":"pending","command":"dkp migrate"} whatever else is true of its
// database, or the SPA's upgrade banner goes blank for the operator who needs it. A degraded ledger on
// a pending database is reported as soon as the migration it is waiting for runs, and is logged loudly
// at every boot in the meantime.
//
// A nil runner means the boot path could not build one at all — an unset or unusable DKP_DB_PATH.
// That reports failed rather than ready: the whole reason /readyz exists separately from /healthz
// is to be the endpoint that is allowed to say no.
func (r readiness) Ready() api.ReadyReport {
	if r.runner == nil {
		return api.ReadyReport{
			Check:  checkMigrations,
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
			Check:  checkMigrations,
			State:  api.ReadyStateFailed,
			Detail: err.Error(),
		}
	}

	switch status.State {
	case migrate.StatePending:
		// This exact body is a contract: docs/development/first-ten-prs.md:167 and
		// docs/design/06-cicd-and-release.md:523 both specify it, and the SPA renders a banner
		// containing the command verbatim.
		return api.ReadyReport{
			Check: checkMigrations, State: api.ReadyStatePending, Command: "dkp migrate",
		}
	case migrate.StateAhead:
		return api.ReadyReport{
			Check:  checkMigrations,
			State:  api.ReadyStateFailed,
			Detail: "database schema is newer than this binary",
		}
	case migrate.StateUpToDate:
		if report, notIntact := appendOnlyReport(status.Protection); notIntact {
			return report
		}

		return api.ReadyReport{Check: checkMigrations, State: api.ReadyStateReady}
	}

	return api.ReadyReport{Check: checkMigrations, State: api.ReadyStateFailed, Detail: "unknown state"}
}

// appendOnlyReport is the second rung of the ladder: is the ledger still protected against being
// rewritten? It reports (report, true) when the answer is anything other than yes.
//
// This is the whole of issue #59. The boot path already asks this question and logs the answer — once,
// at startup, into whatever sink the operator has. A guild whose ledger became editable through a
// fork's build, a patched image or a support session with a SQLite client would otherwise serve
// indefinitely, looking entirely normal, with that one message scrolled past during a restart nobody
// watched. Asking on every probe is what turns a detection into a notification.
//
// Degraded rather than failed for real damage, and failed for a read that did not complete: an
// operator paging on this has to be able to tell "your ledger is unprotected" from "I could not find
// out". Both answer 503; neither touches /healthz, so Docker will not kill the container over it.
func appendOnlyReport(p migrate.Protection) (api.ReadyReport, bool) {
	switch {
	case p.Err != nil:
		return api.ReadyReport{
			Check:  checkLedgerAppendOnly,
			State:  api.ReadyStateFailed,
			Detail: p.Err.Error(),
		}, true
	case p.Degraded():
		return api.ReadyReport{
			Check:  checkLedgerAppendOnly,
			State:  api.ReadyStateDegraded,
			Detail: p.Detail(),
		}, true
	default:
		return api.ReadyReport{}, false
	}
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
			"DKP_DB_PATH selects the SQLite database file.\n" +
			"DKP_READYZ_DETAIL (never|local|always) says who may see a /readyz detail; the\n" +
			"default withholds it from everyone.",
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
				readyDetail: readyDetailPolicy(ctx),
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
// tree — router, Huma mount and middleware — arrives as api.New(api.Config{...}).
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

	// The store the query-backed operations read and write. Opened AFTER migrateOnBoot, so its pools
	// connect to a fully-migrated database — a store opened before migration would cache a schema
	// that the next statement invalidates. It is a separate handle from the migrator's short-lived
	// ones (the Runner opens and closes a store per operation); this one lives for the process.
	//
	// A failure here is NOT fatal, for the same reason migrateOnBoot's is not: canonical §13 requires
	// /healthz to keep answering 200 whatever the database is doing, so a container with an unreadable
	// DB path stays up rather than crash-looping. The store-backed operations degrade to 500 in that
	// state — which is correct, an unreadable database cannot serve the guild — while /healthz and
	// /readyz keep telling an operator what is wrong. api.New tolerates a nil Store: the operations
	// still register, and their handlers surface the missing store as a 500.
	var st *store.Store

	if opened, openErr := store.Open(ctx, cfg.dbPath); openErr != nil {
		slog.ErrorContext(ctx, "could not open the database for serving; serving anyway so /healthz stays up",
			"error", openErr, "db_path", cfg.dbPath)
	} else {
		st = opened
		defer func() {
			if closeErr := st.Close(); closeErr != nil {
				slog.ErrorContext(ctx, "close database", "error", closeErr, "db_path", cfg.dbPath)
			}
		}()
	}

	// The permission catalogue, projected into the database before the listener opens. A route whose
	// permission key does not resolve is an operation the middleware cannot authorize, and canonical §6
	// makes that a boot failure rather than a 403 discovered by a member.
	// api.DeclaredPermissions() reads the registry this binary serves, with the `public` and `self`
	// sentinels already dropped. cmd/ is the wiring point because internal/authz must not import
	// internal/api — internal/api's own tests import internal/authz, so the cycle would be immediate.
	if err := reconcileOnBoot(ctx, st, api.DeclaredPermissions()); err != nil {
		return err
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	// Build the embedded SPA handler. A failure here is DKP_WEB_DIR pointing somewhere unusable;
	// like a failed migrator build, it is logged and the server still boots serving the API and the
	// docs, because a working API with no UI beats refusing to start. api.New treats a nil WebUI as
	// "no SPA mounted".
	webUI, err := ui.Handler()
	if err != nil {
		slog.ErrorContext(ctx, "could not build the web UI handler; serving the API without a SPA",
			"error", err, "web_dir", os.Getenv(ui.WebDirEnv))

		webUI = nil
	}

	srv := &http.Server{
		Handler: api.New(api.Config{
			// The link-time stamps, so GET /api/v1/meta can report which build this is to a bot
			// whose author has no shell access to the box. They are runtime values only: the
			// committed openapi/openapi.json must not vary with them, which is why the spec's
			// info.version is api.SpecVersion and not this.
			Version:   version,
			Commit:    commit,
			BuildDate: date,
			Clock:     clock.System{},
			Readiness: readiness{runner: runner},
			// Who may see a /readyz detail. Never, unless DKP_READYZ_DETAIL says otherwise: the
			// peer address is not a trust signal behind a reverse proxy (#74).
			ReadyDetail: cfg.readyDetail,
			// DKP_API_BASE is reported by GET /config.json as API_BASE. Empty (the default) means
			// same-origin, which is what a co-hosted binary serves.
			APIBase: os.Getenv(apiBaseEnv),
			WebUI:   webUI,
			Store:   st,
		}),
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

// reconcileOnBoot projects internal/authz's permission catalogue into the database, and decides
// whether a failure is fatal.
//
// THE SPLIT IS THE SAME SHAPE AS migrateOnBoot'S, AND IT IS NOT SYMMETRIC:
//
//   - A MISSING REQUIRED KEY is fatal. A route declares a permission this binary's catalogue does not
//     ship, so there is a registered operation whose authorization cannot be resolved against the
//     table role_permission is FK-constrained to. Canonical §6 calls that a boot failure in as many
//     words, and serving anyway would mean answering requests for an operation nobody can be
//     authorized for — silently permissive is the one way authorization is allowed to fail, so it is
//     the one this refuses.
//
//   - ANY OTHER ERROR — no store at all, an unreadable database, a permission table that does not
//     exist because DKP_AUTO_MIGRATE is false and the migration has not run — is logged and the
//     server starts anyway. That is canonical §13: /healthz must answer 200 so Docker's HEALTHCHECK
//     does not kill the container, and /readyz already reports the pending migration that explains it.
//
// Returning an error from here aborts the boot; returning nil serves.
//
// required is a parameter rather than a call to api.DeclaredPermissions() inside the body so that the
// fatal branch is reachable from a test. The registry and the catalogue agree — SPEC005 keeps them
// agreeing — so the only way to watch that branch execute is to hand it a key that is not there.
func reconcileOnBoot(ctx context.Context, st *store.Store, required []string) error {
	// The report is not logged here: Reconcile already logs the counts, and a boot that says the same
	// thing twice is a boot log an operator stops reading.
	_, err := authz.NewReconciler(st, clock.System{}).Reconcile(ctx, required)
	if err == nil {
		return nil
	}

	if errors.Is(err, authz.ErrMissingPermission) {
		return err
	}

	// Logged at ERROR, never discarded. Every operation that needs a permission will fail once the
	// listener opens, and this line plus the /readyz body are the only two places that say why.
	slog.ErrorContext(ctx, "could not reconcile the permission catalogue; serving anyway so /healthz stays up",
		"error", err, "readyz", "will report the underlying database state")

	return nil
}

// closedOrErr maps the sentinel http.ErrServerClosed to nil — a deliberate shutdown is not a
// failure — and wraps anything else with the address it happened on.
func closedOrErr(err error, addr net.Addr) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve on %s: %w", addr, err)
}
