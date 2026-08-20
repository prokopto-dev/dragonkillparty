package authz_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// These tests exercise the reconciler against a real SQLite database carrying the real permission
// table and the real FK from role_permission — there is no fake Queries implementation and a lint
// rule forbids adding one (.claude/rules/go-idioms.md). TestMain builds the template once; each test
// clones it through store.NewDB.
//
// WHAT IS BEING PROTECTED. The permission table is the only table in the product whose contents are
// decided by code rather than by an officer, and the reconciliation is the only writer. Its two
// dangerous directions are opposite: writing too little leaves a route unauthorizable, and deleting
// too much strips capability from every role holding an orphaned key — silently, permanently, on a
// downgrade, which is the moment an operator is least able to investigate.

// bootTime is the instant the fake clock reads. A fixed value rather than time.Now, so the
// orphaned_at assertions compare against a number the test wrote.
var bootTime = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// newReconciler opens a fresh migrated database and returns a reconciler over it, plus the store so a
// test can read the table back.
func newReconciler(t *testing.T) (*authz.Reconciler, *store.Store) {
	t.Helper()

	s := store.NewDB(t)

	return authz.NewReconciler(s, clock.NewFake(bootTime)), s
}

// permissionRows reads the whole permission table, keyed by permission key.
func permissionRows(t *testing.T, s *store.Store) map[string]sqlitegen.Permission {
	t.Helper()

	rows, err := s.Q().ListPermissions(t.Context())
	require.NoError(t, err, "list permissions")

	byKey := make(map[string]sqlitegen.Permission, len(rows))
	for _, row := range rows {
		byKey[row.Key] = row
	}

	return byKey
}

// insertPermission writes a row directly, for the fixtures that need a key the catalogue does not
// ship. It goes through the same typed upsert production uses; there is no second write path.
func insertPermission(t *testing.T, s *store.Store, key string) {
	t.Helper()

	err := s.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return q.UpsertPermission(ctx, sqlitegen.UpsertPermissionParams{
			Key:         key,
			Category:    "administration",
			Label:       "A key from an older binary",
			Description: "Shipped by a version this test is pretending to have downgraded from.",
			SortOrder:   999,
		})
	})
	require.NoError(t, err, "insert %s", key)
}

// TestReconcile_FreshInstall_WritesTheWholeCatalogue is the base case: an empty table becomes the
// catalogue, exactly, with every field carried across.
//
// The whole-row comparison rather than a key-set comparison is deliberate (.claude/rules/go-idioms.md:
// whole-value comparisons over cherry-picked fields). Asserting the keys alone would pass with every
// label empty, every requires_step_up zero and every sort_order zero — and requires_step_up reaching
// the table as 0 is the step-up the spec promises silently not happening.
func TestReconcile_FreshInstall_WritesTheWholeCatalogue(t *testing.T) {
	t.Parallel()

	r, s := newReconciler(t)

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	catalogue := authz.Catalogue()

	require.Equal(t, len(catalogue), report.Live)
	require.Equal(t, len(catalogue), report.Inserted, "every key is new on a fresh install")
	require.Zero(t, report.Updated)
	require.Empty(t, report.Orphaned)
	require.Empty(t, report.Restored)

	rows := permissionRows(t, s)
	require.Len(t, rows, len(catalogue), "the table holds exactly the catalogue")

	for _, p := range catalogue {
		row, ok := rows[p.Key]
		require.Truef(t, ok, "%s has no row", p.Key)

		require.Equal(t, sqlitegen.Permission{
			Key:            p.Key,
			Category:       p.Category,
			Label:          p.Label,
			Description:    p.Description,
			IsDangerous:    boolAsInt(p.IsDangerous),
			RequiresStepUp: boolAsInt(p.RequiresStepUp),
			OrphanedAt:     nil,
			SortOrder:      p.SortOrder,
		}, row, "the row for %s does not match the catalogue entry", p.Key)
	}
}

// TestReconcile_SecondBoot_WritesNothing asserts the steady state is a no-op.
//
// It matters for two reasons and neither is performance. The reconciliation runs inside the single
// write transaction every other boot step queues behind, so a restart that rewrites fifty-eight
// identical rows is fifty-eight WAL frames for nothing; and the boot log's "updated" count is how an
// operator sees that an upgrade changed a permission, which is meaningless if every restart reports
// the whole catalogue.
func TestReconcile_SecondBoot_WritesNothing(t *testing.T) {
	t.Parallel()

	r, _ := newReconciler(t)

	_, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	require.Zero(t, report.Inserted, "a second boot inserted rows")
	require.Zero(t, report.Updated, "a second boot rewrote rows that had not changed")
	require.Empty(t, report.Orphaned)
	require.Empty(t, report.Restored)
}

// TestReconcile_StaleRow_IsUpdatedNotDuplicated covers the ordinary upgrade: the key is the same and
// its wording, category, policy flags or sort order have moved.
func TestReconcile_StaleRow_IsUpdatedNotDuplicated(t *testing.T) {
	t.Parallel()

	r, s := newReconciler(t)

	_, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	// Rewrite one row to what an older binary might have said, through the same typed path.
	err = s.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return q.UpsertPermission(ctx, sqlitegen.UpsertPermissionParams{
			Key:            "dkp.adjust",
			Category:       "points",
			Label:          "Adjust DKP",
			Description:    "An older description.",
			IsDangerous:    1,
			RequiresStepUp: 1,
			SortOrder:      1,
		})
	})
	require.NoError(t, err)

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	require.Zero(t, report.Inserted)
	require.Equal(t, 1, report.Updated, "exactly the one stale row was rewritten")

	var want authz.Permission

	for _, p := range authz.Catalogue() {
		if p.Key == "dkp.adjust" {
			want = p
		}
	}

	row := permissionRows(t, s)["dkp.adjust"]
	require.Equal(t, want.Description, row.Description)
	require.Equal(t, want.Label, row.Label)
	require.Equal(t, want.SortOrder, row.SortOrder)
	require.Zero(t, row.IsDangerous, "dkp.adjust is not a dangerous key")
	require.Zero(t, row.RequiresStepUp, "dkp.adjust is not in the capability floor")
}

// TestReconcile_RetiredKey_IsOrphanedNotDeleted is the downgrade case, and the reason this whole
// mechanism is a reconciliation rather than a seed.
//
// role_permission is FK-constrained to permission(key). Deleting a key an older binary shipped would
// either fail against the grants that reference it or — with a cascade — strip that capability from
// every role holding it, permanently and invisibly, at the exact moment an operator is rolling back
// from something else that went wrong. The row is stamped instead: the grant survives, referential
// integrity survives, and a boot of a binary that ships the key again clears the stamp.
func TestReconcile_RetiredKey_IsOrphanedNotDeleted(t *testing.T) {
	t.Parallel()

	r, s := newReconciler(t)

	_, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	insertPermission(t, s, "raid.timewarp")

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	require.Equal(t, []string{"raid.timewarp"}, report.Orphaned)

	row, ok := permissionRows(t, s)["raid.timewarp"]
	require.True(t, ok, "the row was DELETED. It must be stamped and kept: role_permission has a "+
		"foreign key to permission(key), and deleting the row takes every grant that referenced it")
	require.NotNil(t, row.OrphanedAt, "the row survived but carries no orphaned_at stamp")
	require.Equal(t, bootTime.UnixMicro(), *row.OrphanedAt)
}

// TestReconcile_OrphanedKey_KeepsItsFirstStamp asserts the timestamp is not rewritten on every boot.
//
// What is worth knowing is when the key stopped being shipped, not when the process last restarted.
// The guard lives in the SQL (`WHERE ... AND orphaned_at IS NULL`) as well as in Go, so this fails if
// either half is removed.
func TestReconcile_OrphanedKey_KeepsItsFirstStamp(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	clk := clock.NewFake(bootTime)
	r := authz.NewReconciler(s, clk)

	insertPermission(t, s, "raid.timewarp")

	_, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	clk.Advance(72 * time.Hour)

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	require.Equal(t, []string{"raid.timewarp"}, report.Orphaned,
		"an already-orphaned key is still reported, because the condition is still true")

	row := permissionRows(t, s)["raid.timewarp"]
	require.NotNil(t, row.OrphanedAt)
	require.Equal(t, bootTime.UnixMicro(), *row.OrphanedAt,
		"the stamp was rewritten by the second boot; it must record when the key was retired")
}

// TestReconcile_RestoredKey_ClearsTheStamp is the other half of the downgrade story: rolling forward
// again brings the capability back.
//
// Without it, the retired-key handling would be a one-way door — the officer who rolled back and then
// rolled forward would keep a permission row marked as belonging to no binary, and any UI filtering on
// orphaned_at would go on hiding a live capability.
func TestReconcile_RestoredKey_ClearsTheStamp(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	clk := clock.NewFake(bootTime)
	r := authz.NewReconciler(s, clk)

	// Stamp a key the catalogue DOES ship, by orphaning it through the same path a downgrade would:
	// write the row, then reconcile with a catalogue that has it. To reach the stamped state the test
	// writes the stamp directly through the typed orphan statement.
	_, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	stamp := bootTime.UnixMicro()
	err = s.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return q.OrphanPermission(ctx, sqlitegen.OrphanPermissionParams{OrphanedAt: &stamp, Key: "dkp.adjust"})
	})
	require.NoError(t, err)

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	require.Equal(t, []string{"dkp.adjust"}, report.Restored)
	require.Empty(t, report.Orphaned)

	require.Nil(t, permissionRows(t, s)["dkp.adjust"].OrphanedAt,
		"a key the running binary ships must be live; a stale orphaned_at hides it from the role editor")
}

// TestReconcile_RequiredKey_ThatIsNotShipped_IsABootFailure is the gate the issue calls the keystone:
// a route naming a permission the catalogue does not have must stop the boot.
//
// Canonical §6 makes a divergent permission list a boot failure rather than a style issue, and this is
// the code path that keeps that sentence true. The alternative — serving anyway — is an operation
// whose authorization cannot be resolved against the table role_permission is FK-constrained to, and
// authorization is the one cross-cutting concern that fails silently-permissive (03-security.md §4.6).
func TestReconcile_RequiredKey_ThatIsNotShipped_IsABootFailure(t *testing.T) {
	t.Parallel()

	r, _ := newReconciler(t)

	_, err := r.Reconcile(t.Context(), []string{"roster.read", "raid.timewarp"})

	require.ErrorIs(t, err, authz.ErrMissingPermission)
	require.ErrorContains(t, err, "raid.timewarp")
	require.NotErrorIs(t, err, authz.ErrNoStore)
}

// TestReconcile_RequiredKey_ThatIsOrphaned_IsABootFailure closes the subtler half of the same hole.
//
// A row that EXISTS satisfies a naive existence check while meaning the opposite of what the check is
// for: orphaned means the running binary does not ship the key, so a route declaring it is exactly the
// divergence the boot failure exists to catch. This is the downgrade-with-a-newer-route case, and it
// is the one a fork or a partial upgrade produces.
func TestReconcile_RequiredKey_ThatIsOrphaned_IsABootFailure(t *testing.T) {
	t.Parallel()

	r, s := newReconciler(t)

	insertPermission(t, s, "raid.timewarp")

	_, err := r.Reconcile(t.Context(), []string{"raid.timewarp"})

	require.ErrorIs(t, err, authz.ErrMissingPermission,
		"an orphaned row satisfied the required-key check. It must not: the row exists precisely "+
			"because the running binary does NOT ship the key")
}

// TestReconcile_RequiredKeys_AreAllReported asserts the error names every missing key, not the first.
//
// An operator repairing a build wants the whole list. Returning on the first miss turns one fix into
// a sequence of restarts, each revealing one more name.
func TestReconcile_RequiredKeys_AreAllReported(t *testing.T) {
	t.Parallel()

	r, _ := newReconciler(t)

	_, err := r.Reconcile(t.Context(), []string{"raid.timewarp", "roster.read", "bank.teleport"})

	require.ErrorIs(t, err, authz.ErrMissingPermission)
	require.ErrorContains(t, err, "raid.timewarp")
	require.ErrorContains(t, err, "bank.teleport")
}

// TestReconcile_RequiredKeys_ThatAreShipped_Succeed is the positive control for the two tests above.
//
// Without it they would pass against a reconciler that rejected every required set, which is the same
// class of mistake as a gate nobody has watched go green.
func TestReconcile_RequiredKeys_ThatAreShipped_Succeed(t *testing.T) {
	t.Parallel()

	r, _ := newReconciler(t)

	report, err := r.Reconcile(t.Context(), []string{"roster.read", "admin.settings", "ops.read"})
	require.NoError(t, err)
	require.Equal(t, len(authz.Catalogue()), report.Inserted)
}

// TestReconcile_NoStore_IsAnErrorNotAPanic covers the degraded boot state cmd/dkp enters when
// DKP_DB_PATH is unusable: /healthz must keep answering 200 (canonical §13), so the process comes up
// with a nil store and every store-backed path has to say so rather than dereference it.
func TestReconcile_NoStore_IsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	_, err := authz.NewReconciler(nil, clock.NewFake(bootTime)).Reconcile(t.Context(), nil)

	require.ErrorIs(t, err, authz.ErrNoStore)
}

// TestReconcile_GrantedPermission_SurvivesOrphaning is the FK half of the design, asserted against the
// real constraint rather than against the Go code that avoids tripping it.
//
// It is the test that would fail if somebody "tidied" the reconciliation into a delete-and-reinsert,
// which is the obvious shape and the wrong one: with the grant present, the delete either raises a
// foreign-key error and fails the boot, or cascades and takes the grant with it. Both are worse than
// a stamped row, and neither is visible without a role_permission row in the fixture.
func TestReconcile_GrantedPermission_SurvivesOrphaning(t *testing.T) {
	t.Parallel()

	r, s := newReconciler(t)

	insertPermission(t, s, "raid.timewarp")

	// A role that grants the retired key. Written with the raw test helper rather than through a
	// typed query because role and role_permission have no queries yet — their writers land with the
	// role editor — and the FK is a property of the schema, which is what this test is about.
	s.ExecForTest(t, `INSERT INTO role (id, key, name, name_norm, description, is_builtin, applies_to,
		sort_order, created_at, updated_at) VALUES (?, NULL, 'Timewarpers', 'timewarpers', '', 0, 'user', 0, ?, ?)`,
		"01J0000000000000000000ROLE", bootTime.UnixMicro(), bootTime.UnixMicro())
	s.ExecForTest(t, `INSERT INTO role_permission (role_id, permission_key) VALUES (?, ?)`,
		"01J0000000000000000000ROLE", "raid.timewarp")

	report, err := r.Reconcile(t.Context(), nil)
	require.NoError(t, err, "reconciliation failed with a grant present — the retired key was "+
		"probably deleted rather than stamped, and the foreign key refused it")

	require.Equal(t, []string{"raid.timewarp"}, report.Orphaned)

	var grants int

	require.NoError(t, s.QueryRowForTest(t,
		`SELECT count(*) FROM role_permission WHERE permission_key = ?`, "raid.timewarp").Scan(&grants))
	require.Equal(t, 1, grants,
		"the grant was destroyed. An orphaned permission keeps its grants; that is the whole reason "+
			"the row is stamped instead of deleted")
}

// boolAsInt renders a Go bool the way the reconciler writes it into an INTEGER boolean column.
func boolAsInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}
