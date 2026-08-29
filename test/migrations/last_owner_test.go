package migrations_test

import (
	"database/sql"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	rolekinds "github.com/prokopto-dev/dragonkillparty/internal/authz/role/kinds"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
)

// The last-admin.owner-holder triggers, against the write paths the product has no Go statement for.
//
// docs/design/03-security.md §4.3 requires "a DB-level check plus an integration test", and this file
// is the DB-level half's test: internal/authz/owner_test.go drives the two statements that exist
// (SetAppUserState, SoftDeleteAppUser) through the real service, and these drive the six that do not
// — a hard DELETE on app_user, a role_assignment deleted or re-pointed or time-boxed, the owner role
// soft-deleted or dropped, and the admin.owner grant revoked from the role.
//
// RAW SQL IS THE POINT HERE, not a shortcut. The rule has to hold against the role editor that has
// not been written, an import, a Discord sync and an officer with a SQLite client — so the test that
// proves it must reach the database the same way those would, without going through the Go that
// might have remembered to ask. That is also why this lives in test/migrations, where the harness
// already opens a raw handle with foreign keys enforced.
//
// EVERY SEEDED ROW USES A GO CONSTANT for its enum values and for the owner permission key, so a
// renamed catalogue value fails here rather than seeding a world the triggers correctly ignore.

// lastOwnerMigration is the migration carrying the eight triggers.
const lastOwnerMigration = "000011_bootstrap_token_and_last_owner_guard.sql"

// ownerTriggerNames are the eight triggers and the tables they defend, in the order the migration
// creates them. Hard-coded rather than read from sqlite_schema for the reason
// internal/store.AppendOnlyTriggers is: a catalogue derived from the database would agree with
// whatever the database happened to contain, including nothing.
var ownerTriggerNames = []string{
	"trg_app_user_owner_state",
	"trg_app_user_owner_soft_delete",
	"trg_app_user_owner_delete",
	"trg_role_assignment_owner_delete",
	"trg_role_assignment_owner_update",
	"trg_role_owner_soft_delete",
	"trg_role_owner_delete",
	"trg_role_permission_owner_revoke",
}

// TestOwnerTriggers_FreshInstall_AreAllPresent is the control the behavioural tests below need: a
// trigger that was never created cannot be watched to fire, and a 12-step rebuild of any of the four
// tables would drop them silently (.claude/rules/migrations.md).
func TestOwnerTriggers_FreshInstall_AreAllPresent(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dbPath := freshInstall(t)

	var found []string

	withRawFK(t, dbPath, func(handle *sql.DB) {
		rows, err := handle.QueryContext(t.Context(),
			`SELECT name FROM sqlite_schema WHERE type = 'trigger' AND name LIKE '%_owner_%' ORDER BY name`)
		require.NoError(t, err)

		defer func() { require.NoError(t, rows.Close()) }()

		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			found = append(found, name)
		}

		require.NoError(t, rows.Err())
	})

	want := append([]string(nil), ownerTriggerNames...)
	slices.Sort(want)
	require.Equal(t, want, found,
		"the last-owner triggers a fresh install produces are not the eight this migration creates")
}

// TestOwnerTriggers_LiteralsMatchTheGoCatalogues is the drift guard the trigger bodies need.
//
// A trigger body cannot be parameterised, so 'admin.owner', 'user', 'both' and 'active' are LITERALS
// in the migration — a second spelling of values internal/authz, internal/authz/role/kinds,
// internal/authz/roleassignment/kinds and internal/auth/appuser/kinds each own. A rename in Go would
// leave the triggers matching nothing and the invariant silently unenforced, with every test that
// only asserts the permissive direction still green. This reads the migration and requires each
// literal to be the Go constant it stands in for.
//
// It reads the SHIPPED migration rather than db/schema.hcl because Atlas cannot express a trigger at
// all: the file is where these live and where they will still be after the next `make gen`.
func TestOwnerTriggers_LiteralsMatchTheGoCatalogues(t *testing.T) {
	t.Parallel()

	body := lastOwnerMigrationText(t)

	for _, want := range []struct {
		what    string
		literal string
		source  string
	}{
		{"the owner permission key", authz.PermissionAdminOwner, "internal/authz.PermissionAdminOwner"},
		{"the user subject kind", assignmentkinds.SubjectKindUser, "internal/authz/roleassignment/kinds.SubjectKindUser"},
		{"the both-applicability value", rolekinds.AppliesToBoth, "internal/authz/role/kinds.AppliesToBoth"},
		{"the user-applicability value", rolekinds.AppliesToUser, "internal/authz/role/kinds.AppliesToUser"},
		{"the active account state", appuserkinds.StateActive, "internal/auth/appuser/kinds.StateActive"},
	} {
		require.Containsf(t, body, "'"+want.literal+"'",
			"%s no longer appears in %s as '%s'.\n"+
				"  A trigger body cannot be parameterised, so %s is written into the SQL. If %s changed,\n"+
				"  the triggers now match nothing and the last-owner rule is unenforced while every\n"+
				"  permissive test stays green. Write a NEW migration that recreates them.",
			want.what, lastOwnerMigration, want.literal, want.source, want.source)
	}
}

// TestOwnerTriggers_EveryWritePath_RefusesTheLastHolder is the whole rule, one subtest per path.
//
// The world is seeded through raw SQL with the same shape the bootstrap writes: one active user, one
// owner role granting admin.owner, one global unconditional assignment. Each subtest then attempts
// the write that would leave the instance with nobody able to administer it.
func TestOwnerTriggers_EveryWritePath_RefusesTheLastHolder(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	cases := []struct {
		name string
		stmt string
	}{
		{"app_user is suspended", `UPDATE app_user SET state = 'suspended', updated_at = 2 WHERE id = 'u1'`},
		{"app_user is soft-deleted", `UPDATE app_user SET deleted_at = 2, updated_at = 2 WHERE id = 'u1'`},
		{"app_user is deleted outright", `DELETE FROM app_user WHERE id = 'u1'`},
		{"the assignment is deleted", `DELETE FROM role_assignment WHERE id = 'ra1'`},
		{"the assignment is re-pointed at another role", `UPDATE role_assignment SET role_id = 'r_member' WHERE id = 'ra1'`},
		{"the assignment is re-pointed at another subject", `UPDATE role_assignment SET subject_id = 'u9' WHERE id = 'ra1'`},
		{"the assignment is given an expiry", `UPDATE role_assignment SET expires_at = 9999 WHERE id = 'ra1'`},
		{"the assignment is suspended", `UPDATE role_assignment SET suspended_until_at = 9999 WHERE id = 'ra1'`},
		{"the owner role is soft-deleted", `UPDATE role SET deleted_at = 2 WHERE id = 'r_owner'`},
		{"the owner role is deleted outright", `DELETE FROM role WHERE id = 'r_owner'`},
		{
			"admin.owner is revoked from the role",
			`DELETE FROM role_permission WHERE role_id = 'r_owner' AND permission_key = 'admin.owner'`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dbPath := freshInstall(t)

			withRawFK(t, dbPath, func(handle *sql.DB) {
				seedOwnerWorld(t, handle)

				_, err := handle.ExecContext(t.Context(), tc.stmt)
				require.Error(t, err, "this would have left the instance with no admin.owner holder")
				require.Contains(t, err.Error(), "last admin.owner holder",
					"the refusal must name the rule so an operator can act on it")

				require.Equal(t, 1, ownerHolderCount(t, handle),
					"the refusal left the instance with its owner")
			})
		})
	}
}

// TestOwnerTriggers_WithASecondHolder_EveryWritePathIsAllowed is the permissive direction, and it is
// what makes this a LAST-holder rule rather than an owner-is-immortal one. A guild that could never
// remove a departed officer's owner grant would end up sharing a login, which is the thing the whole
// identity model exists to stop.
func TestOwnerTriggers_WithASecondHolder_EveryWritePathIsAllowed(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	cases := []struct {
		name string
		stmt string
	}{
		{"app_user is suspended", `UPDATE app_user SET state = 'suspended', updated_at = 2 WHERE id = 'u1'`},
		{"app_user is soft-deleted", `UPDATE app_user SET deleted_at = 2, updated_at = 2 WHERE id = 'u1'`},
		{"app_user is deleted outright", `DELETE FROM app_user WHERE id = 'u1'`},
		{"the assignment is deleted", `DELETE FROM role_assignment WHERE id = 'ra1'`},
		{"the assignment is given an expiry", `UPDATE role_assignment SET expires_at = 9999 WHERE id = 'ra1'`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dbPath := freshInstall(t)

			withRawFK(t, dbPath, func(handle *sql.DB) {
				seedOwnerWorld(t, handle)
				seedSecondOwner(t, handle)

				_, err := handle.ExecContext(t.Context(), tc.stmt)
				require.NoError(t, err, "with a second owner this is an ordinary roster edit")

				require.Equal(t, 1, ownerHolderCount(t, handle))
			})
		})
	}
}

// TestOwnerTriggers_ServiceAccountRole_IsNotAReplacementOwner pins the applicability half of the
// predicate. role.applies_to says which KIND of principal may hold a role, EffectivePermission
// enforces it, and a row granting `owner` to a user through a service-account-only role authorises
// nothing — so it must not count as the survivor that makes demoting the real owner safe.
//
// The row it needs is one no constraint can refuse: the rule spans two tables and role_assignment's
// subject is polymorphic, so it is one INSERT away from a bootstrap, an import or a bug.
func TestOwnerTriggers_ServiceAccountRole_IsNotAReplacementOwner(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dbPath := freshInstall(t)

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedOwnerWorld(t, handle)

		exec(t, handle,
			`INSERT INTO role (id, key, name, name_norm, description, is_builtin, applies_to,
			   sort_order, created_at, updated_at)
			 VALUES ('r_bot', 'bot_owner', 'Bot Owner', 'bot owner', '', 0, ?, 20, 1, 1)`,
			rolekinds.AppliesToServiceAccount)
		exec(t, handle,
			`INSERT INTO role_permission (role_id, permission_key) VALUES ('r_bot', ?)`,
			authz.PermissionAdminOwner)
		seedUserRow(t, handle, "u2", "two")
		exec(t, handle,
			`INSERT INTO role_assignment (id, subject_kind, subject_id, role_id, scope_type, scope_id,
			   granted_via, created_at, updated_at)
			 VALUES ('ra2', ?, 'u2', 'r_bot', ?, NULL, ?, 1, 1)`,
			assignmentkinds.SubjectKindUser, assignmentkinds.ScopeGlobal, assignmentkinds.GrantedViaManual)

		require.Equal(t, 1, ownerHolderCount(t, handle),
			"a grant through a service-account role is not a holder")

		_, err := handle.ExecContext(t.Context(), `DELETE FROM role_assignment WHERE id = 'ra1'`)
		require.Error(t, err, "the bot-role grant must not make the real owner removable")
		require.Contains(t, err.Error(), "last admin.owner holder")
	})
}

// TestOwnerTriggers_TimeBoxedGrant_IsNotAReplacementOwner pins the time-free half of the predicate.
//
// An assignment with expires_at or suspended_until_at set does not count, and that is deliberate: a
// trigger cannot read a clock without making a schema-level guarantee depend on wall time, so a grant
// that stops working at midnight is never the thing that keeps an instance open. It is conservative
// in the safe direction — this refuses a change that might have been survivable, and never permits
// one that is not.
func TestOwnerTriggers_TimeBoxedGrant_IsNotAReplacementOwner(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dbPath := freshInstall(t)

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedOwnerWorld(t, handle)
		seedUserRow(t, handle, "u2", "two")
		exec(t, handle,
			`INSERT INTO role_assignment (id, subject_kind, subject_id, role_id, scope_type, scope_id,
			   expires_at, granted_via, created_at, updated_at)
			 VALUES ('ra2', ?, 'u2', 'r_owner', ?, NULL, 99999999, ?, 1, 1)`,
			assignmentkinds.SubjectKindUser, assignmentkinds.ScopeGlobal, assignmentkinds.GrantedViaManual)

		_, err := handle.ExecContext(t.Context(), `DELETE FROM role_assignment WHERE id = 'ra1'`)
		require.Error(t, err, "a grant that expires is not what keeps an instance open")
		require.Contains(t, err.Error(), "last admin.owner holder")
	})
}

// TestOwnerTriggers_UnrelatedRows_AreUntouched is the negative control for the whole file: the
// triggers must not turn every roster edit into a refusal.
func TestOwnerTriggers_UnrelatedRows_AreUntouched(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dbPath := freshInstall(t)

	withRawFK(t, dbPath, func(handle *sql.DB) {
		seedOwnerWorld(t, handle)
		seedUserRow(t, handle, "u2", "two")
		exec(t, handle,
			`INSERT INTO role_assignment (id, subject_kind, subject_id, role_id, scope_type, scope_id,
			   granted_via, created_at, updated_at)
			 VALUES ('ra2', ?, 'u2', 'r_member', ?, NULL, ?, 1, 1)`,
			assignmentkinds.SubjectKindUser, assignmentkinds.ScopeGlobal, assignmentkinds.GrantedViaManual)

		for _, stmt := range []string{
			`UPDATE app_user SET state = 'suspended', updated_at = 3 WHERE id = 'u2'`,
			`UPDATE app_user SET deleted_at = 3, updated_at = 3 WHERE id = 'u2'`,
			`DELETE FROM role_assignment WHERE id = 'ra2'`,
			`DELETE FROM app_user WHERE id = 'u2'`,
			`UPDATE role SET deleted_at = 3 WHERE id = 'r_member'`,
			`UPDATE role_assignment SET updated_at = 4 WHERE id = 'ra1'`,
		} {
			_, err := handle.ExecContext(t.Context(), stmt)
			require.NoErrorf(t, err, "an edit that touches no owner must be allowed: %s", stmt)
		}
	})
}

// seedOwnerWorld writes the shape the first-run bootstrap produces: one active user holding
// admin.owner through a global, unconditional assignment on a built-in owner role — plus a `member`
// role that grants nothing, so the negative controls have something harmless to edit.
//
// The permission and role rows are written here rather than by authz.Reconcile because this package
// talks to a raw handle: what is under test is the DATABASE's behaviour, and routing the seed through
// the Go that also writes the real thing would make a passing test a statement about the seed.
func seedOwnerWorld(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	exec(tb, handle,
		`INSERT INTO permission (key, category, label, description, is_dangerous, requires_step_up,
		   sort_order)
		 VALUES (?, 'admin', 'Owner', 'the owner capability', 1, 1, 1)`,
		authz.PermissionAdminOwner)

	for _, role := range []struct{ id, key, name string }{
		{"r_owner", authz.RoleKeyOwner, "Owner"},
		{"r_member", authz.RoleKeyMember, "Member"},
	} {
		exec(tb, handle,
			`INSERT INTO role (id, key, name, name_norm, description, is_builtin, applies_to,
			   sort_order, created_at, updated_at)
			 VALUES (?, ?, ?, ?, '', 1, ?, 1, 1, 1)`,
			role.id, role.key, role.name, strings.ToLower(role.name), rolekinds.AppliesToUser)
	}

	exec(tb, handle,
		`INSERT INTO role_permission (role_id, permission_key) VALUES ('r_owner', ?)`,
		authz.PermissionAdminOwner)

	seedUserRow(tb, handle, "u1", "one")

	exec(tb, handle,
		`INSERT INTO role_assignment (id, subject_kind, subject_id, role_id, scope_type, scope_id,
		   granted_via, created_at, updated_at)
		 VALUES ('ra1', ?, 'u1', 'r_owner', ?, NULL, ?, 1, 1)`,
		assignmentkinds.SubjectKindUser, assignmentkinds.ScopeGlobal, assignmentkinds.GrantedViaBootstrap)
}

// seedSecondOwner adds a second active account holding admin.owner the same way.
func seedSecondOwner(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	seedUserRow(tb, handle, "u2", "two")

	exec(tb, handle,
		`INSERT INTO role_assignment (id, subject_kind, subject_id, role_id, scope_type, scope_id,
		   granted_via, created_at, updated_at)
		 VALUES ('ra2', ?, 'u2', 'r_owner', ?, NULL, ?, 1, 1)`,
		assignmentkinds.SubjectKindUser, assignmentkinds.ScopeGlobal, assignmentkinds.GrantedViaManual)
}

// seedUserRow writes one active app_user.
func seedUserRow(tb testing.TB, handle *sql.DB, id, username string) {
	tb.Helper()

	exec(tb, handle,
		`INSERT INTO app_user (id, username, username_norm, display_name, state, session_epoch,
		   failed_logins, mfa_required, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, 0, 0, 1, 1)`,
		id, username, username, username, appuserkinds.StateActive)
}

// ownerHolderCount is the triggers' own predicate, asked as a query — the raw-SQL twin of
// db/queries/authz.sql's CountOwnerHolders, so a test asserting "the refusal left the owner in place"
// is asking the same question the trigger asked.
func ownerHolderCount(tb testing.TB, handle *sql.DB) int {
	tb.Helper()

	var holders int

	err := handle.QueryRowContext(tb.Context(),
		`SELECT COUNT(DISTINCT ra.subject_id)
		   FROM role_assignment ra
		   JOIN role r ON r.id = ra.role_id
		   JOIN role_permission rp ON rp.role_id = ra.role_id AND rp.permission_key = ?
		   JOIN app_user u ON u.id = ra.subject_id
		  WHERE ra.subject_kind = ?
		    AND ra.expires_at IS NULL AND ra.suspended_until_at IS NULL
		    AND r.deleted_at IS NULL AND (r.applies_to = ? OR r.applies_to = ra.subject_kind)
		    AND u.state = ? AND u.deleted_at IS NULL`,
		authz.PermissionAdminOwner, assignmentkinds.SubjectKindUser,
		rolekinds.AppliesToBoth, appuserkinds.StateActive).Scan(&holders)
	require.NoError(tb, err)

	return holders
}

// exec runs one statement and fails the test with the statement text if it does not apply.
func exec(tb testing.TB, handle *sql.DB, stmt string, args ...any) {
	tb.Helper()

	_, err := handle.ExecContext(tb.Context(), stmt, args...)
	require.NoErrorf(tb, err, "seed statement failed: %s", stmt)
}

// lastOwnerMigrationText reads the migration carrying the triggers, out of the EMBEDDED set.
//
// Embedded rather than off disk, because that is the copy the binary ships and applies: a test that
// read the working tree could pass against a file `go:embed` no longer picks up.
func lastOwnerMigrationText(tb testing.TB) string {
	tb.Helper()

	raw, err := fs.ReadFile(realMigrations(tb), lastOwnerMigration)
	require.NoError(tb, err, "read %s out of the embedded migration set", lastOwnerMigration)

	return string(raw)
}
