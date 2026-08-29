package authz_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The last-holder rule, through the statements the product actually ships
// (docs/design/03-security.md §4.3: "the last account holding admin.owner cannot be demoted,
// disabled or deleted — a DB-level check plus an integration test").
//
// THESE ARE THE TWO PATHS A RUNNING BINARY HAS. SetAppUserState is what suspending or disabling an
// account calls and SoftDeleteAppUser is what deleting one calls; both ship ahead of their endpoints
// (db/queries/auth.sql), and both are how an officer tidying the roster would lock the guild out of
// its own instance. The other six write paths the triggers cover — a hard DELETE, a role_assignment
// removed or re-pointed, the owner role soft-deleted or dropped, the admin.owner grant revoked — have
// no Go statement at all yet, so they are exercised as raw SQL in
// test/migrations/last_owner_test.go, where raw SQL is what that package is for.
//
// WHY IT IS A DATABASE TRIGGER AND NOT A GO CHECK. An instance with no owner cannot mint a token,
// edit a role, take a backup, read PII or commit an import — every one of those is on the capability
// floor and session-and-step-up only — so it is not a degraded instance, it is a locked one, and the
// only way out is host access. A rule that lives in one Go function holds for the callers that
// remember to ask; this one has to hold against the role editor that has not been written yet, an
// import, a Discord sync and a support session with a SQLite client.

// ownerFixture is a bootstrapped instance: exactly one account holds admin.owner.
type ownerFixture struct {
	bootstrapFixture

	owner authz.Account
}

func newOwnerFixture(t *testing.T) ownerFixture {
	t.Helper()

	f := newBootstrapFixture(t)

	owner, err := f.boot.CreateAccount(t.Context(), ownerParams())
	require.NoError(t, err)

	return ownerFixture{bootstrapFixture: f, owner: owner}
}

// secondOwner adds another admin.owner holder and returns its id.
func (f ownerFixture) secondOwner(t *testing.T, username string) core.ULID {
	t.Helper()

	p := ownerParams()
	p.Username = username
	p.Email = username + "@example.com"

	account, err := f.boot.CreateAccount(t.Context(), p)
	require.NoError(t, err)

	return account.UserID
}

// setState runs the statement the suspend and reinstate endpoints will call, returning its error.
func (f ownerFixture) setState(t *testing.T, user core.ULID, state string) error {
	t.Helper()

	return f.store.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return q.SetAppUserState(ctx, sqlitegen.SetAppUserStateParams{
			State:     state,
			UpdatedAt: int64(core.FromTime(f.clock.Now())),
			ID:        user.String(),
		})
	})
}

// softDelete runs the statement the delete endpoint will call, returning its error.
func (f ownerFixture) softDelete(t *testing.T, user core.ULID) error {
	t.Helper()

	return f.store.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return q.SoftDeleteAppUser(ctx, sqlitegen.SoftDeleteAppUserParams{
			DeletedAt: int64(core.FromTime(f.clock.Now())),
			UpdatedAt: int64(core.FromTime(f.clock.Now())),
			ID:        user.String(),
		})
	})
}

// TestOwner_LastHolder_CannotBeDisabled is §4.3's "disabled", through SetAppUserState — every state
// that is not `active`, because the resolver refuses all three and any of them would leave the
// instance with nobody who can act.
func TestOwner_LastHolder_CannotBeDisabled(t *testing.T) {
	t.Parallel()

	for _, state := range []string{
		appuserkinds.StateSuspended,
		appuserkinds.StateDisabled,
		appuserkinds.StatePending,
	} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			f := newOwnerFixture(t)

			err := f.setState(t, f.owner.UserID, state)
			require.Error(t, err, "the last admin.owner holder must not be moved to %s", state)
			require.Contains(t, err.Error(), "last admin.owner holder")

			holders, countErr := f.boot.OwnerHolders(t.Context())
			require.NoError(t, countErr)
			require.Equal(t, int64(1), holders, "the refusal left the instance with its owner")
		})
	}
}

// TestOwner_LastHolder_CannotBeDeleted is §4.3's "deleted", through the soft delete that is this
// product's delete.
func TestOwner_LastHolder_CannotBeDeleted(t *testing.T) {
	t.Parallel()

	f := newOwnerFixture(t)

	err := f.softDelete(t, f.owner.UserID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "last admin.owner holder")

	row, readErr := f.store.Q().GetAppUser(t.Context(), f.owner.UserID.String())
	require.NoError(t, readErr)
	require.Nil(t, row.DeletedAt)
}

// TestOwner_WithASecondHolder_TheFirstMayBeDisabledAndDeleted is the permissive direction, and it is
// the half that makes the rule a LAST-holder rule rather than an owner-is-immortal rule. Without it a
// guild could never retire an officer who left.
func TestOwner_WithASecondHolder_TheFirstMayBeDisabledAndDeleted(t *testing.T) {
	t.Parallel()

	f := newOwnerFixture(t)
	second := f.secondOwner(t, "second")

	require.NoError(t, f.setState(t, f.owner.UserID, appuserkinds.StateSuspended),
		"with two owners, one may be suspended")

	require.NoError(t, f.softDelete(t, f.owner.UserID),
		"with two owners, one may be deleted")

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders)

	// And now the survivor is the last one again, so the rule reasserts itself.
	require.Error(t, f.setState(t, second, appuserkinds.StateSuspended),
		"the survivor is now the last holder")
}

// TestOwner_NonOwner_IsUnaffected is the negative control: the triggers must not turn every account
// into an undeletable one.
func TestOwner_NonOwner_IsUnaffected(t *testing.T) {
	t.Parallel()

	f := newOwnerFixture(t)

	member := ownerParams()
	member.Username = "member"
	member.Email = "member@example.com"
	member.RoleKey = authz.RoleKeyOfficer

	account, err := f.boot.CreateAccount(t.Context(), member)
	require.NoError(t, err)

	require.NoError(t, f.setState(t, account.UserID, appuserkinds.StateSuspended))
	require.NoError(t, f.softDelete(t, account.UserID))
}

// TestOwner_SuspendedHolder_IsNotAHolder is what makes "non-suspended" in
// docs/design/01-domain-model.md §4 mean something: an account that cannot act is not what keeps the
// instance open, so it neither counts nor protects.
func TestOwner_SuspendedHolder_IsNotAHolder(t *testing.T) {
	t.Parallel()

	f := newOwnerFixture(t)
	second := f.secondOwner(t, "second")

	require.NoError(t, f.setState(t, second, appuserkinds.StateSuspended))

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders, "a suspended owner is not counted")

	// And the ACTIVE one is therefore the last holder again.
	require.Error(t, f.setState(t, f.owner.UserID, appuserkinds.StateDisabled))
}

// TestOwner_UnbootstrappedInstance_HasNothingToProtect is the state a fresh install is in, and it is
// asserted because the triggers must not make an owner-less instance impossible to administer: every
// one of them is conditioned on the row being a live owner grant, so on an instance with none they
// never fire.
func TestOwner_UnbootstrappedInstance_HasNothingToProtect(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	user := auth.SeedUser(t, f.store, f.clock, "nobody")

	err := f.store.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return q.SetAppUserState(ctx, sqlitegen.SetAppUserStateParams{
			State:     appuserkinds.StateDisabled,
			UpdatedAt: int64(core.FromTime(f.clock.Now())),
			ID:        user.String(),
		})
	})
	require.NoError(t, err, "an instance with no owner has nothing for the last-holder rule to keep")
}
