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
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
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

	// checkAuthorization is the `check` value of the authorization bootstrap: did this process
	// project the permission catalogue into the database before it opened the listener?
	//
	// Named for the capability rather than for the table, as checkLedgerAppendOnly is: a monitoring
	// rule that fires on `check=authorization` should not have to know that the fact is stored in a
	// `permission` table, and the thing an operator needs to be told is that this instance cannot
	// authorize anybody rather than which query failed.
	checkAuthorization = "authorization"
)

// serveConfig is the resolved configuration for one `dkp serve` invocation.
type serveConfig struct {
	// addr is the host:port to listen on.
	addr string

	// dbPath is DKP_DB_PATH.
	dbPath string

	// dataDir is DKP_DATA_DIR, already defaulted by resolveDataDir to the database's directory. It
	// holds <data-dir>/secrets.json, the instance root key every credential pepper is derived from
	// (docs/design/03-security.md §9.1) — the same directory the pre-migration snapshots land in, so
	// "back up the data directory" stays one complete instruction.
	dataDir string

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
type readiness struct {
	runner *migrate.Runner

	// authz is the boot reconciliation's verdict, decided once by reconcileOnBoot and carried here
	// unchanged for the life of the process.
	//
	// A BOOT FACT, NOT A PROBE, and that is deliberate: reconciliation writes, and a readiness
	// endpoint a load balancer calls every few seconds must not open a write transaction. So an
	// instance that failed to reconcile keeps saying so until it is restarted — which is also the
	// truth about it, because nothing after boot projects the catalogue.
	authz api.AuthorizationState
}

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
//
// The rungs below migrations are, in order: can this instance authorize a request (issue #272), and
// is the ledger still protected against being rewritten (#59). Each function that owns a rung says
// why it sits where it does.
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
		if report, unavailable := authorizationReport(r.authz); unavailable {
			return report
		}

		if report, notIntact := appendOnlyReport(status.Protection); notIntact {
			return report
		}

		return api.ReadyReport{Check: checkMigrations, State: api.ReadyStateReady}
	}

	return api.ReadyReport{Check: checkMigrations, State: api.ReadyStateFailed, Detail: "unknown state"}
}

// authorizationReport is the second rung of the ladder: can this instance authorize a request at
// all? It reports (report, true) when the answer is no.
//
// This is issue #272's readiness half. Before it, the boot path logged a failed reconciliation once
// and started the listener, and /readyz — which consumes the migration state and nothing else —
// answered ready: an instance that could not resolve a single permission key was in the load
// balancer's rotation and looked identical to a healthy one.
//
// AFTER MIGRATIONS, and that order is the wire contract rather than a preference. An unmigrated
// database is the ordinary cause of a failed reconciliation — there is no permission table yet — and
// the body {"check":"migrations","state":"pending","command":"dkp migrate"} is specified literally in
// two documents and rendered verbatim by the SPA's upgrade banner. Reporting this rung first would
// replace the one message that tells the operator what to type with a consequence of not having
// typed it.
//
// BEFORE the append-only rung, and that order is a judgement: both answer 503, and this is the one
// that describes what the instance is doing to requests arriving right now. A degraded ledger is
// serving; an instance with no authorization state is refusing every operation that needs a
// permission, and the operator reading /readyz to find out why is looking at this.
//
// `failed` rather than `degraded`: degraded is an evaluated check with a bad answer on an instance
// still doing its job — the ledger's case. This one could not be established at all, and until the
// process is restarted successfully there is nothing that can establish it.
func authorizationReport(state api.AuthorizationState) (api.ReadyReport, bool) {
	if state.Available() {
		return api.ReadyReport{}, false
	}

	return api.ReadyReport{
		Check:  checkAuthorization,
		State:  api.ReadyStateFailed,
		Detail: state.Reason(),
	}, true
}

// appendOnlyReport is the third rung of the ladder: is the ledger still protected against being
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
				dataDir:     resolveDataDir(os.Getenv(dbPathEnv)),
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
	// makes that a boot failure rather than a 403 discovered by a member. Every OTHER way this can fail
	// keeps the process up and refuses the operations that declare a permission — see reconcileOnBoot,
	// and api.AuthorizationState for what the handler tree does with the verdict.
	// api.DeclaredPermissions() reads the registry this binary serves, with the `public` and `self`
	// sentinels already dropped. cmd/ is the wiring point because internal/authz must not import
	// internal/api — internal/api's own tests import internal/authz, so the cycle would be immediate.
	authorization, err := reconcileOnBoot(ctx, st, api.DeclaredPermissions())
	if err != nil {
		return err
	}

	// The credential resolver, and the instance root key it derives the token pepper from.
	//
	// A BAD SECRETS FILE IS FATAL, where a bad database is not (§9.1: "if /data/secrets.json exists
	// but is unreadable or malformed, refuse to start, naming the file and the recovery path"). The
	// asymmetry is deliberate. An unreadable database degrades to 500s while /healthz and /readyz
	// keep telling an operator what is wrong; an unreadable secrets file cannot be degraded around —
	// booting anyway would either serve with no way to verify a token, or silently generate a new key
	// and invalidate every session and every bot token in the guild at once, which looks exactly like
	// a mass-logout bug and is unrecoverable.
	//
	// A nil store means no resolver, and api.Config's contract takes it from there: operations that
	// declare Security answer 503 rather than being served unauthenticated. That is the same
	// degradation the store-backed operations get, applied to the thing that guards them.
	var authService *auth.Service

	if st != nil {
		keyring, keyErr := auth.LoadOrCreateKeyring(cfg.dataDir)
		if keyErr != nil {
			return fmt.Errorf("load %s: %w", filepath.Join(cfg.dataDir, auth.SecretsFileName), keyErr)
		}

		authService = auth.NewService(st, clock.System{}, keyring)
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
			Readiness: readiness{runner: runner, authz: authorization},
			// Who may see a /readyz detail. Never, unless DKP_READYZ_DETAIL says otherwise: the
			// peer address is not a trust signal behind a reverse proxy (#74).
			ReadyDetail: cfg.readyDetail,
			// DKP_API_BASE is reported by GET /config.json as API_BASE. Empty (the default) means
			// same-origin, which is what a co-hosted binary serves.
			APIBase: os.Getenv(apiBaseEnv),
			WebUI:   webUI,
			Store:   st,
			// The authentication choke point. Nil only when the database could not be opened, in
			// which case every operation declaring Security answers 503 (api.Config).
			Auth: authService,
			// The fail-closed half of the boot split (#272). Unavailable here means every operation
			// that declares a permission answers 503 while /healthz, /readyz, /config.json, the docs
			// and the public operations keep serving.
			//
			// The two are independent gates on the same request and both must be open: Auth decides
			// WHO is asking, Authorization decides whether this instance is in a state to answer that
			// question at all. What JOINS them is authz.Check, which api.New builds from Store and
			// Clock and runs inside the credential middleware (Wave 0e, #276) — so a request that gets
			// past both gates has been checked against its operation's permission, scopes and step-up
			// requirement before any handler sees it.
			Authorization: authorization,
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

// reconcileOnBoot projects internal/authz's permission catalogue into the database, and decides what
// a failure costs.
//
// It returns the authorization state the handler tree is built with, and an error only when the boot
// must not continue at all. THE THREE OUTCOMES ARE NOT SYMMETRIC:
//
//   - RECONCILED. Every required key resolves to a live row in this officer's database. The listener
//     opens and operations that declare a permission are served.
//
//   - A MISSING REQUIRED KEY is fatal, and it is the one failure that aborts the boot. A route
//     declares a permission this binary's catalogue does not ship, so there is a registered operation
//     whose authorization cannot be resolved against the table role_permission is FK-constrained to.
//     Canonical §6 calls that a boot failure in as many words. It is a defect in the build rather
//     than in the officer's database — no restart, no repair and no amount of waiting fixes it — so
//     there is nothing for a running process to report that exiting does not say better.
//
//   - ANY OTHER ERROR — no store at all, an unreadable database, a permission table that does not
//     exist because DKP_AUTO_MIGRATE is false and the migration has not run, a transient failure
//     inside the transaction — serves, and FAILS CLOSED (issue #272). The process stays up because
//     canonical §13 requires /healthz to answer 200 whatever the database is doing: Docker's
//     HEALTHCHECK calls it, and a container killed here is a container killed mid-upgrade. What does
//     NOT stay up is anything that needs a permission. Before #272 this branch logged and served
//     everything, which made an instance whose authorization source had never been prepared
//     indistinguishable from a healthy one — /readyz included, because it consumed the migration
//     state and never this result.
//
// The state also reaches /readyz, which reports `authorization` `failed` until a boot reconciles.
// Nothing re-reconciles afterwards, so the way out is to fix the database and restart.
//
// required is a parameter rather than a call to api.DeclaredPermissions() inside the body so that the
// fatal branch is reachable from a test. The registry and the catalogue agree — SPEC005 keeps them
// agreeing — so the only way to watch that branch execute is to hand it a key that is not there.
func reconcileOnBoot(ctx context.Context, st *store.Store, required []string) (api.AuthorizationState, error) {
	// The report is not logged here: Reconcile already logs the counts, and a boot that says the same
	// thing twice is a boot log an operator stops reading.
	_, err := authz.NewReconciler(st, clock.System{}).Reconcile(ctx, required)
	if err == nil {
		return api.AuthorizationReconciled(), nil
	}

	if errors.Is(err, authz.ErrMissingPermission) {
		return api.AuthorizationState{}, err
	}

	// Logged at ERROR, once, with the underlying error in full — this is the operator's copy. The
	// refused requests carry a fixed sentence and no database detail, and the /readyz detail is
	// gated by DKP_READYZ_DETAIL, so this line is the only place the actual fault is written down.
	slog.ErrorContext(ctx, "could not reconcile the permission catalogue; refusing every operation "+
		"that requires a permission, so /healthz stays up and /readyz can say why",
		"error", err, "readyz", "reports check=authorization state=failed until a boot reconciles")

	return api.AuthorizationUnavailable(err.Error()), nil
}

// closedOrErr maps the sentinel http.ErrServerClosed to nil — a deliberate shutdown is not a
// failure — and wraps anything else with the address it happened on.
func closedOrErr(err error, addr net.Addr) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve on %s: %w", addr, err)
}
