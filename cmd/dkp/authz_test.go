package main

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The boot wiring for the permission catalogue (issue #261). internal/authz has the reconciler's own
// tests against a real database; what is asserted HERE is the half cmd/ owns and could get wrong on
// its own — that `dkp serve` calls it at all, before the listener opens, that a failure to reconcile
// is fatal only for the one reason that must be, and that the failures which are NOT fatal still
// leave the instance unable to serve an operation that requires a permission (issue #272).
//
// "Not fatal" and "fine" are different states, and conflating them was the finding: before #272 every
// non-fatal branch returned nil and the listener opened with no authorization source at all.
//
// It calls t.Setenv, so none of these may be parallel.

// TestServe_Boot_ReconcilesThePermissionCatalogue is the end-to-end proof of the wiring: an empty
// database, a real `dkp serve`, and afterwards the permission table holds the catalogue.
//
// Through the real command rather than by calling reconcileOnBoot directly, because the thing most
// likely to go wrong is not the reconciliation — that has its own suite — but the call being in the
// wrong place or absent. A unit test of a function nobody invokes passes forever.
func TestServe_Boot_ReconcilesThePermissionCatalogue(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)
	t.Setenv(autoMigrateEnv, "true")

	base := startServe(t)

	// The server is bound, so the boot path has run to completion. Open the same file separately —
	// WAL lets a second reader in while the server holds it — and read the table back.
	code, _ := get(t, base+"/healthz")
	require.Equal(t, 200, code, "the server did not come up")

	st, err := store.Open(t.Context(), dbPath)
	require.NoError(t, err)

	defer func() { require.NoError(t, st.Close()) }()

	rows, err := st.Q().ListPermissions(t.Context())
	require.NoError(t, err, "read the permission table")

	require.Len(t, rows, len(authz.Catalogue()),
		"the permission table does not hold the catalogue. `dkp serve` must reconcile it on the boot "+
			"path: role_permission is FK-constrained to permission(key), so an unreconciled table "+
			"makes every role grant unwritable")

	live := make(map[string]bool, len(rows))
	for _, row := range rows {
		require.Nil(t, row.OrphanedAt, "%s is orphaned on a fresh install", row.Key)

		live[row.Key] = true
	}

	// Every key a registered route names resolves. This is the property the boot failure protects,
	// asserted positively so the negative test below is not the only thing describing it.
	for _, key := range api.DeclaredPermissions() {
		require.Truef(t, live[key], "route permission %q has no live row after boot", key)
	}

	// And the roles, which are the half that makes the permission rows usable: a table of keys nobody
	// can be granted is an instance where the documented bootstrap has nothing to hand out.
	roles, err := st.Q().ListRoles(t.Context())
	require.NoError(t, err, "read the role table")
	require.Len(t, roles, len(authz.BuiltinRoles()),
		"`dkp serve` must seed the built-in roles (docs/design/01-domain-model.md §5.1) on the boot "+
			"path — the migration cannot, because role_permission references permission(key) and the "+
			"permission table is empty until this same transaction fills it")

	var grants int

	require.NoError(t, st.QueryRowForTest(t, `SELECT count(*) FROM role_permission`).Scan(&grants))
	require.Positive(t, grants, "the roles were seeded with no grants")

	// The owner role exists and holds the owner capability. Who HOLDS the role is Wave 0d's — a
	// role_assignment needs an app_user — but the role itself has to be here for that to be possible.
	var ownerGrants int

	require.NoError(t, st.QueryRowForTest(t,
		`SELECT count(*) FROM role_permission WHERE role_id = ? AND permission_key = ?`,
		authz.RoleIDOwner, "admin.owner").Scan(&ownerGrants))
	require.Equal(t, 1, ownerGrants, "the owner role does not grant admin.owner")
}

// TestReconcileOnBoot_NoStore_IsNotFatal covers the degraded boot canonical §13 requires.
//
// cmd/dkp deliberately comes up with a nil store when DKP_DB_PATH is unusable, so that /healthz keeps
// answering 200 and Docker's HEALTHCHECK does not kill a container mid-migration. Reconciliation
// cannot run in that state, and the correct response is a loud log and a server that boots — not a
// crash loop, and not a silent success either.
//
// TestServe_UnreadableDatabasePath_HealthzStillReturns200 covers the same path end to end; this pins
// the decision itself, because the difference between the two failure classes lives in one errors.Is.
func TestReconcileOnBoot_NoStore_IsNotFatal(t *testing.T) {
	state, err := reconcileOnBoot(t.Context(), nil, api.DeclaredPermissions())

	require.NoError(t, err,
		"a missing store must not abort the boot: /healthz has to keep answering (canonical §13), "+
			"and /readyz already reports why the database is unusable")
	require.False(t, state.Available(),
		"the boot continued AND reported that this instance can authorize a request. Not aborting is "+
			"about /healthz staying up; it is not a statement that authorization works (#272)")
	require.Contains(t, state.Reason(), authz.ErrNoStore.Error(),
		"the state must carry why, because it becomes the /readyz detail an operator reads")
}

// TestReconcileOnBoot_MissingRequiredKey_IsFatal is the asymmetry itself, and it is the assertion the
// whole boot check exists for.
//
// It cannot be provoked through `dkp serve` — the registry and the catalogue agree, and SPEC005 keeps
// them agreeing — which is why reconcileOnBoot takes the required set as an argument: without that
// seam its fatal branch is a line nobody has ever watched execute, on the one cross-cutting concern
// that fails silently-permissive (03-security.md §4.6).
func TestReconcileOnBoot_MissingRequiredKey_IsFatal(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)

	runner, err := newMigrator(dbPath, true)
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	st, err := store.Open(t.Context(), dbPath)
	require.NoError(t, err)

	defer func() { require.NoError(t, st.Close()) }()

	state, err := reconcileOnBoot(t.Context(), st, []string{"roster.read", "raid.timewarp"})

	require.False(t, state.Available(),
		"a fatal reconciliation returned an available authorization state; if a caller ignored the "+
			"error it would serve every protected operation")
	require.ErrorIs(t, err, authz.ErrMissingPermission,
		"a route naming a key the catalogue does not ship must be a boot failure (canonical §6)")
	require.ErrorContains(t, err, "raid.timewarp")
}

// TestReconcileOnBoot_UnusableDatabase_IsNotFatal is the other side of that asymmetry, and the case an
// officer actually meets: DKP_AUTO_MIGRATE=false with the migration not yet applied, so the permission
// table does not exist.
//
// That must not stop the boot. /readyz already answers {"check":"migrations","state":"pending"} with
// the command to run, the SPA renders that banner, and a binary that refused to start would take away
// the only surface telling the operator what to do next.
func TestReconcileOnBoot_UnusableDatabase_IsNotFatal(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)

	// Opened, never migrated: an empty file with no permission table in it.
	st, err := store.Open(t.Context(), dbPath)
	require.NoError(t, err)

	defer func() { require.NoError(t, st.Close()) }()

	state, err := reconcileOnBoot(t.Context(), st, api.DeclaredPermissions())

	require.NoError(t, err,
		"an unmigrated database must not abort the boot — /readyz reports the pending migration and "+
			"the SPA renders the command that fixes it")
	require.False(t, state.Available(),
		"an unmigrated database has no permission table, so nothing in it can authorize a request. "+
			"Serving the protected operations anyway is issue #272: the instance would answer them "+
			"with no authorization source at all")
	require.NotEmpty(t, state.Reason())
}

// TestServe_ReconciliationFails_ServesHealthzAndRefusesProtectedOperations is issue #272 end to end,
// through the real binary, the real migration set and real HTTP requests.
//
// The database here is fully migrated and then broken the way a real one breaks at exactly the wrong
// moment: the permission table refuses writes. A trigger is the smallest honest way to produce that
// on demand — the shapes the issue names (a locked table, a corrupt page, a failed catalogue query, a
// failed role seed) all arrive at the same place, an error from inside Reconcile's transaction that
// is not ErrMissingPermission.
//
// Before this change that error was logged and the listener opened anyway, with every operation
// served and /readyz answering ready, because readiness consumed the migration state and never the
// reconciliation result. Four assertions, and they are the four halves of "fail closed" that have to
// hold together:
//
//   - /healthz answers 200. Canonical §13: Docker's HEALTHCHECK calls it, and a container killed here
//     is a container killed over a fault a restart cannot fix.
//   - /readyz answers 503 and names the check, so a load balancer takes the instance out of rotation
//     and monitoring keeps firing for as long as the state lasts.
//   - an operation that declares a permission is REFUSED, rather than served by a process that has no
//     authorization source at all.
//   - a public operation is still served, because refusing it would buy nothing and would remove the
//     surface that tells whoever is debugging which build this is.
//
// No t.Parallel: t.Setenv panics in a parallel test.
func TestServe_ReconciliationFails_ServesHealthzAndRefusesProtectedOperations(t *testing.T) {
	const reason = "simulated permission table failure"

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)
	t.Setenv(autoMigrateEnv, "true")

	// Migrated first and damaged afterwards, so the boot path finds nothing pending: the migration
	// rung of the readiness ladder has to be satisfied for the authorization rung to be the answer,
	// and an unmigrated database would report the pending migration instead — correctly, and without
	// exercising anything this test is about.
	runner, err := newMigrator(dbPath, true)
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	st, err := store.Open(t.Context(), dbPath)
	require.NoError(t, err)
	st.ExecForTest(t, `CREATE TRIGGER trg_test_permission_unwritable BEFORE INSERT ON permission
		BEGIN SELECT RAISE(ABORT, '`+reason+`'); END`)
	require.NoError(t, st.Close())

	base := startServe(t)

	status, body := get(t, base+"/healthz")
	require.Equal(t, http.StatusOK, status,
		"/healthz went unhealthy because the permission catalogue could not be reconciled — that lets "+
			"Docker kill the container over a fault restarting does not fix (canonical §13)")
	require.JSONEq(t, `{"status":"ok"}`, body)

	status, body = get(t, base+"/readyz")
	require.Equal(t, http.StatusServiceUnavailable, status,
		"an instance that could not prepare its authorization source reported itself ready. That is "+
			"the whole of issue #272: the boot log fires once and the load balancer keeps sending it "+
			"traffic")
	require.Contains(t, body, `"check":"authorization"`)
	require.Contains(t, body, `"state":"failed"`)
	require.NotContains(t, body, reason,
		"the readiness detail was disclosed with DKP_READYZ_DETAIL unset; the new rung must honour "+
			"the same default the append-only rung does (#74)")

	status, body = get(t, base+"/api/v1/guild")
	require.Equal(t, http.StatusServiceUnavailable, status,
		"GET /api/v1/guild declares x-dkp-permission roster.read and was served by a process that "+
			"never established what a permission means in this database (#272)")
	require.Contains(t, body, `"code":"service_unavailable"`)
	require.NotContains(t, body, reason,
		"the refusal told an unauthenticated caller why the database failed")

	status, _ = get(t, base+"/api/v1/meta")
	require.Equal(t, http.StatusOK, status,
		"a public operation was refused. getMeta carries the `public` sentinel: there is nothing to "+
			"authorize, and it is how whoever is debugging finds out which build is running")
}
