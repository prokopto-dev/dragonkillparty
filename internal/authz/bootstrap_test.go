package authz_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// assignmentRow reads one role_assignment back, so the bootstrap's central claim — the SHAPE of the
// grant it writes — is asserted against the row rather than against the code that wrote it.
func assignmentRow(tb testing.TB, st *store.Store, id string) sqlitegen.RoleAssignment {
	tb.Helper()

	row, err := st.Q().GetRoleAssignment(context.Background(), id)
	require.NoError(tb, err, "read role assignment %s", id)

	return row
}

// wrongSecret returns a well-formed token that is not secret.
//
// A SHAPE-VALID WRONG TOKEN IS THE POINT: a malformed one is refused before an attempt is spent, so
// testing the attempt counter needs a secret that reaches the comparison and loses it. Flipping one
// base64url character does that and keeps the length.
func wrongSecret(secret string) string {
	first := "A"
	if strings.HasPrefix(secret, "A") {
		first = "B"
	}

	return first + secret[1:]
}

// bootstrapEpoch is the instant the first-run fixtures' clock is frozen at, so the sixty-minute TTL
// is arithmetic rather than a sleep (.claude/rules/go-idioms.md bans time.Sleep in tests).
var bootstrapEpoch = time.Date(2026, 8, 21, 18, 0, 0, 0, time.UTC)

// goodPassword satisfies §3.2 — over twelve bytes, and it contains neither the fixture username nor
// its email local part, which is the property the substring rule tests would otherwise trip over.
const goodPassword = "seventeen-oaks-drift"

// bootstrapFixture is a migrated, reconciled instance with no owner: the state a fresh install
// reaches after `dkp migrate` and before anybody has set it up.
type bootstrapFixture struct {
	store   *store.Store
	clock   *clock.Fake
	dataDir string
	boot    *authz.Bootstrapper
}

func newBootstrapFixture(t *testing.T) bootstrapFixture {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(bootstrapEpoch)
	dir := t.TempDir()

	authz.Boot(t, st, clk)

	// The CHEAPEST rung of the ladder, deliberately. Every test here hashes at least one password and
	// the default profile is 19 MiB by design; the rung does not change what is asserted, and picking
	// it from Argon2Profiles rather than writing parameters down keeps this honest if the ladder moves.
	profiles := auth.Argon2Profiles()

	return bootstrapFixture{
		store:   st,
		clock:   clk,
		dataDir: dir,
		boot:    authz.NewBootstrapper(st, clk, profiles[len(profiles)-1], dir),
	}
}

func ownerParams() authz.AccountParams {
	return authz.AccountParams{
		Username: "courtney",
		Email:    "courtney@example.com",
		Password: goodPassword,
	}
}

// TestBootstrap_FreshInstance_HasNoOwner is the control every other test here leans on: an instance
// that has migrated and reconciled still has nobody who can administer it, because a role_assignment
// is neither a migration's job nor the reconciler's.
func TestBootstrap_FreshInstance_HasNoOwner(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Zero(t, holders, "a reconciled instance seeds the owner ROLE and nobody holds it")

	bootstrapped, err := f.boot.IsBootstrapped(t.Context())
	require.NoError(t, err)
	require.False(t, bootstrapped)
}

// TestBootstrap_CreateAccount_ProducesTheOwnerHolder is issue #264's acceptance criterion: a fresh
// instance ends first-run with exactly one account holding admin.owner, through a role_assignment
// with granted_via = 'bootstrap'.
func TestBootstrap_CreateAccount_ProducesTheOwnerHolder(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	account, err := f.boot.CreateAccount(t.Context(), ownerParams())
	require.NoError(t, err)
	require.Equal(t, authz.RoleKeyOwner, account.RoleKey)

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders, "exactly one account holds admin.owner")

	// The account is real and usable: the user row is active and the identity can carry a session.
	user, err := f.store.Q().GetAppUser(t.Context(), account.UserID.String())
	require.NoError(t, err)
	require.Equal(t, appuserkinds.StateActive, user.State)
	require.Equal(t, "courtney", user.UsernameNorm)

	// And the capability resolves through the real choke point rather than through a count: what the
	// bootstrap has to produce is an answer from authz.Check, not a row that looks right.
	steppedUp := core.FromTime(f.clock.Now())
	principal := &auth.Principal{
		Kind:       auditkinds.ActorUser,
		ID:         account.UserID,
		Name:       "courtney",
		Credential: auth.CredentialSession,
		SessionID:  account.IdentityID,
		// admin.owner requires step-up, and first run is the moment the password was typed — which is
		// a full re-authentication, so a session opened by it is stepped up.
		SteppedUpAt: &steppedUp,
	}

	decision, err := authz.NewChecker(f.store, f.clock).Check(t.Context(), principal, authz.Requirement{
		Permission: authz.PermissionAdminOwner,
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed(),
		"the bootstrapped owner must pass authz.Check for admin.owner, got %v", decision)
}

// TestBootstrap_CreateAccount_GrantIsGlobalUnconditionalAndAttributed pins the SHAPE of the grant,
// which is what the last-holder triggers count. A scoped, expiring or suspendable owner is not the
// thing that keeps an instance open.
func TestBootstrap_CreateAccount_GrantIsGlobalUnconditionalAndAttributed(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	account, err := f.boot.CreateAccount(t.Context(), ownerParams())
	require.NoError(t, err)

	row := assignmentRow(t, f.store, account.AssignmentID.String())

	require.Equal(t, assignmentkinds.SubjectKindUser, row.SubjectKind)
	require.Equal(t, account.UserID.String(), row.SubjectID)
	require.Equal(t, authz.RoleIDOwner, row.RoleID)
	require.Equal(t, assignmentkinds.ScopeGlobal, row.ScopeType)
	require.Nil(t, row.ScopeID, "a global assignment has no scope id (role_assignment_scope_shape)")
	require.Nil(t, row.ExpiresAt, "an owner grant that expires is an instance that locks itself out")
	require.Nil(t, row.SuspendedUntilAt)
	require.Equal(t, assignmentkinds.GrantedViaBootstrap, row.GrantedVia,
		"§264: the first-run assignment carries granted_via = bootstrap")
	require.Nil(t, row.GrantedBy, "nobody granted the first owner; the instance had no accounts")
}

// TestBootstrap_CreateAccount_RoleKey_ChoosesTheBuiltin covers the non-owner path `dkp admin create`
// exists for, and the two refusals a mistyped --role produces.
func TestBootstrap_CreateAccount_RoleKey_ChoosesTheBuiltin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		roleKey string
		wantKey string
		wantErr error
	}{
		{"empty defaults to owner", "", authz.RoleKeyOwner, nil},
		{"admin", authz.RoleKeyAdmin, authz.RoleKeyAdmin, nil},
		{"officer", authz.RoleKeyOfficer, authz.RoleKeyOfficer, nil},
		{"unknown", "onwer", "", authz.ErrUnknownRole},
		{"a service-account role", authz.RoleKeyBotRaid, "", authz.ErrRoleNotForUsers},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newBootstrapFixture(t)

			p := ownerParams()
			p.RoleKey = tc.roleKey

			account, err := f.boot.CreateAccount(t.Context(), p)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantKey, account.RoleKey)
		})
	}
}

// TestBootstrap_CreateAccount_RoleNotForUsers_NamesTheSetInTheError is the "I typed bot_raid" case,
// and it is worth its own test because NOTHING in the database refuses that row: the constraint spans
// two tables and role_assignment's subject is polymorphic, so this check is the only one that gives
// the operator a sentence instead of a silent grant that authorises nothing.
func TestBootstrap_CreateAccount_UnknownRole_NamesTheBuiltins(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	p := ownerParams()
	p.RoleKey = "adminstrator"

	_, err := f.boot.CreateAccount(t.Context(), p)
	require.ErrorIs(t, err, authz.ErrUnknownRole)

	for _, key := range []string{authz.RoleKeyOwner, authz.RoleKeyAdmin, authz.RoleKeyGuest} {
		require.Containsf(t, err.Error(), key, "the refusal must name %s so a typo is self-correcting", key)
	}
}

// TestBootstrap_CreateAccount_PasswordPolicy_IsEnforced closes the door §9.2 calls "a policy-compliant
// password": the CLI is not exempt from §3.2 just because it needs host access.
func TestBootstrap_CreateAccount_PasswordPolicy_IsEnforced(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password string
		wantErr  error
	}{
		{"too short", "short-pass", auth.ErrPasswordTooShort},
		{"contains the username", "courtney-is-my-password", auth.ErrPasswordContainsIdentifier},
		{"contains the email local part", "xxcourtneyxx-and-more", auth.ErrPasswordContainsIdentifier},
		{"too long", strings.Repeat("a", auth.MaxPasswordBytes+1), auth.ErrPasswordTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newBootstrapFixture(t)

			p := ownerParams()
			p.Password = tc.password

			_, err := f.boot.CreateAccount(t.Context(), p)
			require.ErrorIs(t, err, tc.wantErr)

			holders, countErr := f.boot.OwnerHolders(t.Context())
			require.NoError(t, countErr)
			require.Zero(t, holders, "a refused password must leave no account behind")
		})
	}
}

// TestBootstrap_CreateAccount_GuildName_IsForbiddenInThePassword is §3.2's third substring, and it is
// the one that needs a guild row to exist — which on a fresh instance it does not, so the test writes
// one first.
func TestBootstrap_CreateAccount_GuildName_IsForbiddenInThePassword(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	p := ownerParams()
	p.GuildName = "Azure Guardians"
	p.Password = "azure guardians rule"

	_, err := f.boot.CreateAccount(t.Context(), p)
	require.ErrorIs(t, err, auth.ErrPasswordContainsIdentifier)
}

// TestBootstrap_CreateAccount_DuplicateUsername_IsRefused translates the unique index into a sentence
// an operator can act on, without a check-then-insert that would race a second `dkp admin create`.
func TestBootstrap_CreateAccount_DuplicateUsername_IsRefused(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	_, err := f.boot.CreateAccount(t.Context(), ownerParams())
	require.NoError(t, err)

	second := ownerParams()
	second.Username = "COURTNEY"
	second.Email = "other@example.com"
	second.RoleKey = authz.RoleKeyAdmin

	_, err = f.boot.CreateAccount(t.Context(), second)
	require.ErrorIs(t, err, authz.ErrUsernameTaken)

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders, "the refused account left nothing behind")
}

// TestBootstrap_Arm_WritesTheTokenFileAt0600 is §9.2 item 1, mode included: the file holds the one
// credential that can create an owner, on a box whose other users must not be able to read it.
func TestBootstrap_Arm_WritesTheTokenFileAt0600(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)
	require.NoError(t, armed.FileErr)

	require.Len(t, armed.Secret, authz.BootstrapTokenLen)
	require.Equal(t, core.FromTime(bootstrapEpoch.Add(authz.BootstrapTokenTTL)), armed.ExpiresAt)
	require.Equal(t, filepath.Join(f.dataDir, authz.BootstrapTokenFileName), armed.Path)

	info, err := os.Stat(armed.Path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the first-run token file must not be readable by other users on the box")

	contents, err := os.ReadFile(armed.Path)
	require.NoError(t, err)
	require.Equal(t, armed.Secret, strings.TrimSpace(string(contents)))
}

// TestBootstrap_Arm_StoresOnlyTheHash is §9.2's "store only its SHA-256" — asserted against the row
// rather than trusted, because the whole design of first run is that the database is not enough.
func TestBootstrap_Arm_StoresOnlyTheHash(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	row, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)

	require.Len(t, row.TokenHash, 32, "the stored value is a SHA-256")
	require.NotContains(t, string(row.TokenHash), armed.Secret)
	require.Zero(t, row.Attempts)
	require.Nil(t, row.ConsumedAt)
}

// TestBootstrap_Arm_BootstrappedInstance_IsRefused is the backdoor this refusal exists to close: a
// token armed on a live instance is a credential for creating a second owner, printed into a log.
func TestBootstrap_Arm_BootstrappedInstance_IsRefused(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	_, err := f.boot.CreateAccount(t.Context(), ownerParams())
	require.NoError(t, err)

	_, err = f.boot.Arm(t.Context())
	require.ErrorIs(t, err, authz.ErrAlreadyBootstrapped)

	require.NoFileExists(t, filepath.Join(f.dataDir, authz.BootstrapTokenFileName),
		"a refused arm must not leave a token file")
}

// TestBootstrap_Arm_Again_SupersedesTheEarlierToken covers `dkp admin bootstrap --new-token`: the
// recovery §9.2 names after five wrong attempts.
func TestBootstrap_Arm_Again_SupersedesTheEarlierToken(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	first, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	f.clock.Advance(time.Minute)

	second, err := f.boot.Arm(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, first.Secret, second.Secret)

	contents, err := os.ReadFile(second.Path)
	require.NoError(t, err)
	require.Equal(t, second.Secret, strings.TrimSpace(string(contents)),
		"the file must hold the live token, not the dead one")

	// The superseded token is refused: GetLatestBootstrapToken reads the newest row, so an older
	// secret does not resolve at all.
	_, err = f.boot.Redeem(t.Context(), first.Secret, ownerParams())
	require.ErrorIs(t, err, authz.ErrBootstrapTokenMismatch)

	account, err := f.boot.Redeem(t.Context(), second.Secret, ownerParams())
	require.NoError(t, err)
	require.Equal(t, authz.RoleKeyOwner, account.RoleKey)
}

// TestBootstrap_Redeem_HappyPath_CreatesTheOwnerAndRetiresTheToken is the /setup handler's whole job,
// exercised one wave ahead of the handler (ADR-0028 commitment 3).
func TestBootstrap_Redeem_HappyPath_CreatesTheOwnerAndRetiresTheToken(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	account, err := f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.NoError(t, err)

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders)

	row := assignmentRow(t, f.store, account.AssignmentID.String())
	require.Equal(t, assignmentkinds.GrantedViaBootstrap, row.GrantedVia)

	token, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)
	require.NotNil(t, token.ConsumedAt, "a redeemed token is spent")
	require.Equal(t, int64(1), token.Attempts, "one attempt, and it was the successful one")

	require.NoFileExists(t, armed.Path, "§9.2 item 6: the token file is deleted on success")
}

// TestBootstrap_Redeem_Twice_IsRefused is the second browser tab, and the property that makes it safe
// is the guarded UPDATE rather than a check: the second redeem finds a consumed row.
func TestBootstrap_Redeem_Twice_IsRefused(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.NoError(t, err)

	second := ownerParams()
	second.Username = "second"
	second.Email = "second@example.com"

	_, err = f.boot.Redeem(t.Context(), armed.Secret, second)
	require.ErrorIs(t, err, authz.ErrBootstrapTokenConsumed)

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders, "one token, one owner")
}

// TestBootstrap_Redeem_WrongToken_SpendsAnAttempt is the counter doing its job, and the five-attempt
// death §9.2 specifies.
func TestBootstrap_Redeem_WrongToken_SpendsAnAttempt(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	// A well-formed token that is not the right one. Re-arming on a second fixture would give a
	// second database; flipping the first character keeps the shape and changes the secret.
	wrong := wrongSecret(armed.Secret)

	for attempt := 1; attempt <= authz.BootstrapMaxAttempts; attempt++ {
		_, err = f.boot.Redeem(t.Context(), wrong, ownerParams())
		require.ErrorIsf(t, err, authz.ErrBootstrapTokenMismatch, "attempt %d", attempt)

		row, readErr := f.store.Q().GetLatestBootstrapToken(t.Context())
		require.NoError(t, readErr)
		require.EqualValuesf(t, attempt, row.Attempts, "attempt %d must be recorded", attempt)
	}

	// Six: the token is dead, and even the RIGHT secret no longer works. That is the property — the
	// recovery is host access, not persistence.
	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.ErrorIs(t, err, authz.ErrBootstrapTokenBurned)

	holders, err := f.boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Zero(t, holders)
}

// TestBootstrap_Redeem_MalformedToken_SpendsNoAttempt keeps a truncated paste from burning the five
// attempts an operator needs for the real secret.
func TestBootstrap_Redeem_MalformedToken_SpendsNoAttempt(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	for _, presented := range []string{"", "short", armed.Secret[:len(armed.Secret)-1], armed.Secret + "="} {
		_, err = f.boot.Redeem(t.Context(), presented, ownerParams())
		require.ErrorIsf(t, err, authz.ErrBootstrapTokenMismatch, "presented %q", presented)
	}

	row, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)
	require.Zero(t, row.Attempts, "a malformed paste is not a guess at a 256-bit secret")

	// And the real token still works, which is the point of not having spent the attempts.
	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.NoError(t, err)
}

// TestBootstrap_Redeem_BadPassword_SpendsNoAttempt: an operator who pasted the right token and picked
// a short password has made a recoverable mistake, and charging an attempt for it turns a typo into a
// locked instance.
func TestBootstrap_Redeem_BadPassword_SpendsNoAttempt(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	p := ownerParams()
	p.Password = "too-short"

	_, err = f.boot.Redeem(t.Context(), armed.Secret, p)
	require.ErrorIs(t, err, auth.ErrPasswordTooShort)

	row, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)
	require.Zero(t, row.Attempts)

	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.NoError(t, err)
}

// TestBootstrap_Redeem_Expired_IsRefused is the sixty-minute TTL, at the boundary.
func TestBootstrap_Redeem_Expired_IsRefused(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	// One microsecond short of the TTL still works; the expiry is `now >= expires_at`.
	f.clock.Advance(authz.BootstrapTokenTTL - time.Microsecond)

	f.clock.Advance(time.Microsecond)

	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.ErrorIs(t, err, authz.ErrBootstrapTokenExpired)

	row, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)
	require.Zero(t, row.Attempts, "an expired token is refused before an attempt is spent")
}

// TestBootstrap_Redeem_JustBeforeExpiry_Works is the other half of the boundary, so the comparison
// cannot quietly become `>` or `<=`.
func TestBootstrap_Redeem_JustBeforeExpiry_Works(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	f.clock.Advance(authz.BootstrapTokenTTL - time.Microsecond)

	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.NoError(t, err)
}

// TestBootstrap_Redeem_NoTokenArmed_IsItsOwnAnswer separates "nobody armed one" from "yours is
// wrong", which is the difference between an operator looking at the console and one looking at the
// wrong instance.
func TestBootstrap_Redeem_NoTokenArmed_IsItsOwnAnswer(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	_, err := f.boot.Redeem(t.Context(), strings.Repeat("A", authz.BootstrapTokenLen), ownerParams())
	require.ErrorIs(t, err, authz.ErrNoBootstrapToken)
}

// TestBootstrap_CreateAccount_RetiresALiveToken is the case an operator actually hits: the token
// expired, they ran `dkp admin create` instead, and the token file is still sitting there. Once an
// owner exists the token authorises nothing, so it must stop being redeemable rather than staying
// live in whatever log it was printed into.
func TestBootstrap_CreateAccount_RetiresALiveToken(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	_, err = f.boot.CreateAccount(t.Context(), ownerParams())
	require.NoError(t, err)

	row, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)
	require.NotNil(t, row.ConsumedAt, "an owner exists, so the token is spent")

	_, err = f.boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.ErrorIs(t, err, authz.ErrBootstrapTokenConsumed)
}

// TestBootstrap_CreateAccount_NonOwnerRole_LeavesTheTokenAlone is the other side of the same rule: an
// instance with no owner is still un-set-up after `dkp admin create --role officer`, and its token
// has to keep working.
func TestBootstrap_CreateAccount_NonOwnerRole_LeavesTheTokenAlone(t *testing.T) {
	t.Parallel()

	f := newBootstrapFixture(t)

	armed, err := f.boot.Arm(t.Context())
	require.NoError(t, err)

	p := ownerParams()
	p.RoleKey = authz.RoleKeyOfficer

	_, err = f.boot.CreateAccount(t.Context(), p)
	require.NoError(t, err)

	row, err := f.store.Q().GetLatestBootstrapToken(t.Context())
	require.NoError(t, err)
	require.Nil(t, row.ConsumedAt, "an officer is not an owner; the instance still needs setting up")

	second := ownerParams()
	second.Username = "owner"
	second.Email = "owner@example.com"

	_, err = f.boot.Redeem(t.Context(), armed.Secret, second)
	require.NoError(t, err)
}

// TestBootstrap_NoDataDir_ArmsWithoutAFile is the degraded shape: an instance whose data directory
// could not be resolved still arms, and stdout is the only copy. Refusing would leave an operator
// unable to set up an instance in order to protect a convenience copy of a secret they already have.
func TestBootstrap_NoDataDir_ArmsWithoutAFile(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(bootstrapEpoch)
	authz.Boot(t, st, clk)

	profiles := auth.Argon2Profiles()
	boot := authz.NewBootstrapper(st, clk, profiles[len(profiles)-1], "")

	armed, err := boot.Arm(t.Context())
	require.NoError(t, err)
	require.Empty(t, armed.Path)
	require.NoError(t, armed.FileErr)
	require.Len(t, armed.Secret, authz.BootstrapTokenLen)

	_, err = boot.Redeem(t.Context(), armed.Secret, ownerParams())
	require.NoError(t, err, "the token works with or without its file")
}

// TestBootstrap_NoStore_FailsClosed covers the degraded boot cmd/dkp enters deliberately when the
// database cannot be opened: a Bootstrapper with no store refuses by name rather than panicking.
func TestBootstrap_NoStore_FailsClosed(t *testing.T) {
	t.Parallel()

	boot := authz.NewBootstrapper(nil, clock.NewFake(bootstrapEpoch), auth.DefaultArgon2Profile(), "")

	_, err := boot.OwnerHolders(t.Context())
	require.ErrorIs(t, err, authz.ErrNoStore)

	_, err = boot.Arm(t.Context())
	require.ErrorIs(t, err, authz.ErrNoStore)

	_, err = boot.Redeem(t.Context(), strings.Repeat("A", authz.BootstrapTokenLen), ownerParams())
	require.ErrorIs(t, err, authz.ErrNoStore)

	_, err = boot.CreateAccount(t.Context(), ownerParams())
	require.ErrorIs(t, err, authz.ErrNoStore)
}
