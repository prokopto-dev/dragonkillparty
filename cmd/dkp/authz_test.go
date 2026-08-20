package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The boot wiring for the permission catalogue (issue #261). internal/authz has the reconciler's own
// tests against a real database; what is asserted HERE is the half cmd/ owns and could get wrong on
// its own — that `dkp serve` calls it at all, before the listener opens, and that a failure to
// reconcile is fatal only for the one reason that must be.
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
	require.NoError(t, reconcileOnBoot(t.Context(), nil, api.DeclaredPermissions()),
		"a missing store must not abort the boot: /healthz has to keep answering (canonical §13), "+
			"and /readyz already reports why the database is unusable")
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

	err = reconcileOnBoot(t.Context(), st, []string{"roster.read", "raid.timewarp"})

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

	require.NoError(t, reconcileOnBoot(t.Context(), st, api.DeclaredPermissions()),
		"an unmigrated database must not abort the boot — /readyz reports the pending migration and "+
			"the SPA renders the command that fixes it")
}
