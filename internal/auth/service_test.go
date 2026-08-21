package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// testEpoch is the instant the fake clock starts at. A fixed instant rather than time.Now: every
// expiry assertion below is a comparison against it, and a wall clock would make "expired" mean
// something different on a slow machine.
var testEpoch = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// resolverEnv is one seeded instance: a user with a session, and a service account with a token.
type resolverEnv struct {
	store          *store.Store
	clock          *clock.Fake
	keys           *auth.Keyring
	user           core.ULID
	cookie         *http.Cookie
	sessionID      core.ULID
	serviceAccount core.ULID
	token          string
}

// newResolver seeds the two credential classes and returns a resolver over them.
//
// BOTH CLASSES IN ONE FIXTURE, deliberately: precedence between them is a security property (§6.3,
// "cookies are ignored entirely when Authorization is present"), and a test that can only construct
// one of them cannot assert it.
func newResolver(t *testing.T) (*auth.Service, resolverEnv) {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(testEpoch)
	keys := auth.NewTestKeyring(t)
	svc := auth.NewService(st, clk, keys)

	user := auth.SeedUser(t, st, clk, "officer")
	cookie, sessionID := auth.SeedSession(t, svc, user)
	serviceAccount := auth.SeedServiceAccount(t, st, clk, user, "raidbot")

	token := auth.SeedToken(t, st, keys, clk, auth.SeedTokenParams{
		ServiceAccount: serviceAccount,
		CreatedBy:      user,
		Scopes:         "raids:read raids:write",
	})

	return svc, resolverEnv{
		store: st, clock: clk, keys: keys,
		user: user, cookie: cookie, sessionID: sessionID,
		serviceAccount: serviceAccount, token: token,
	}
}

// request builds a GET carrying whichever credentials the caller names.
func request(cookie *http.Cookie, bearer string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/guild", nil)

	if cookie != nil {
		req.AddCookie(cookie)
	}

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	return req
}

// TestResolve_Session_ProducesAUserPrincipal is the cookie happy path, asserted as a whole value
// rather than field by field: a cherry-picked assertion hides the field that changed
// (.claude/rules/go-idioms.md).
func TestResolve_Session_ProducesAUserPrincipal(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err)

	require.Equal(t, &auth.Principal{
		Kind:       auditkinds.ActorUser,
		ID:         env.user,
		Name:       "officer",
		Credential: auth.CredentialSession,
		SessionID:  env.sessionID,
	}, principal)

	require.True(t, principal.IsUser())
	require.False(t, principal.IsServiceAccount())
	require.False(t, principal.HasScope("raids:read"),
		"a session carries no scope; a session that claimed one would be the all-powerful token "+
			"ADR-0011 refuses")
}

// TestResolve_Token_ProducesAServiceAccountPrincipal is the bearer happy path, whole-value again.
//
// THE SUBJECT IS THE SERVICE ACCOUNT AND NOT ITS OWNER, which is the ADR-0011 property this
// assertion exists for: revoking the human does not kill the bot, and the audit trail still names a
// responsible person through OwnerUserID.
func TestResolve_Token_ProducesAServiceAccountPrincipal(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	principal, err := svc.ResolveRequest(t.Context(), request(nil, env.token))
	require.NoError(t, err)

	require.Equal(t, auditkinds.ActorServiceAccount, principal.Kind)
	require.Equal(t, env.serviceAccount, principal.ID)
	require.Equal(t, "raidbot", principal.Name)
	require.Equal(t, auth.CredentialToken, principal.Credential)
	require.Equal(t, env.user, principal.OwnerUserID)
	require.Equal(t, []string{"raids:read", "raids:write"}, principal.Scopes)
	require.Equal(t, env.token[len(auth.TokenScheme):len(auth.TokenScheme)+auth.TokenPrefixLen],
		principal.TokenPrefix, "the principal carries the PUBLIC prefix, which is what logs name")
	require.Empty(t, principal.SessionID)
	require.True(t, principal.HasScope("raids:write"))
	require.False(t, principal.HasScope("dkp:adjust"))
}

// TestResolve_BearerAndCookie_BearerWins is §6.3's fixed precedence, and it is the confusion attack
// the rule exists to close: a low-privilege bearer alongside a high-privilege cookie must resolve to
// the BEARER, never to the union and never to the cookie.
func TestResolve_BearerAndCookie_BearerWins(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, env.token))
	require.NoError(t, err)

	require.Equal(t, auth.CredentialToken, principal.Credential,
		"Authorization must win outright: the cookie is not read at all when it is present")
	require.Equal(t, env.serviceAccount, principal.ID)
}

// TestResolve_MalformedBearerBesideAGoodCookie_IsRefused is the other half of precedence, and the
// half that would be easy to get wrong by "falling back" to the cookie. A bot whose token is
// mistyped must not be silently upgraded to whatever session its browser happens to hold.
func TestResolve_MalformedBearerBesideAGoodCookie_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, "not-a-token"))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrMalformedCredential)
}

// TestResolve_NoCredential_IsItsOwnSentinel: absence is not a failure, it is a fact the middleware
// needs in order to serve a public operation anonymously.
func TestResolve_NoCredential_IsItsOwnSentinel(t *testing.T) {
	t.Parallel()

	svc, _ := newResolver(t)

	principal, err := svc.ResolveRequest(t.Context(), request(nil, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrNoCredential)
}

// TestResolve_TokenInQueryString_IsRefused holds ADR-0011's transport rule. A token in a URL is a
// token in the access log, the proxy log and the browser history, so it is refused EXPLICITLY rather
// than ignored — and refused even when it would otherwise have been valid, which is the case a
// bot author is most likely to hit while porting from EQdkp.
func TestResolve_TokenInQueryString_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	for _, param := range []string{"atoken", "token", "api_key", "apikey", "access_token", "key"} {
		t.Run(param, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/guild?"+param+"="+env.token, nil)

			principal, err := svc.ResolveRequest(t.Context(), req)
			require.Nil(t, principal)
			require.ErrorIs(t, err, auth.ErrTokenInQueryString)
		})
	}
}

// TestResolve_TokenInQueryStringBesideAValidHeader_IsStillRefused: the leak has already happened by
// the time the request arrives, so a valid header does not make it fine.
func TestResolve_TokenInQueryStringBesideAValidHeader_IsStillRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/guild?atoken=leaked", nil)
	req.Header.Set("Authorization", "Bearer "+env.token)

	principal, err := svc.ResolveRequest(t.Context(), req)
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrTokenInQueryString)
}

// TestResolve_RevokedToken_IsRefusedImmediately is the assertion ADR-0011's whole argument rests on:
// revocation is one UPDATE on a row the auth path already reads, with no denylist, no propagation
// delay and no window in which the token still works.
func TestResolve_RevokedToken_IsRefusedImmediately(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	// It works, then it is revoked, then it does not. Both halves in one test, because "was it ever
	// valid" is exactly what a revocation test must not assume.
	_, err := svc.ResolveRequest(t.Context(), request(nil, env.token))
	require.NoError(t, err)

	revoked := auth.SeedToken(t, env.store, env.keys, env.clock, auth.SeedTokenParams{
		ServiceAccount: env.serviceAccount,
		CreatedBy:      env.user,
		RevokedAt:      ptr(core.FromTime(env.clock.Now())),
	})

	principal, err := svc.ResolveRequest(t.Context(), request(nil, revoked))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrRevokedCredential)
}

// TestResolve_ExpiredToken_IsRefused. Default expiry is 365 days (§6.2) and the mint endpoint
// applies it; this asserts the resolver honours whatever the row says.
func TestResolve_ExpiredToken_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	expired := auth.SeedToken(t, env.store, env.keys, env.clock, auth.SeedTokenParams{
		ServiceAccount: env.serviceAccount,
		CreatedBy:      env.user,
		ExpiresAt:      ptr(core.FromTime(env.clock.Now().Add(-time.Second))),
	})

	principal, err := svc.ResolveRequest(t.Context(), request(nil, expired))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrExpiredCredential)
}

// TestResolve_TokenFromAnotherPepper_IsUnknown is the pepper doing its job: the same 32 random bytes
// hashed under a different root key must not resolve. This is what makes a database-only leak
// useless — an attacker with every row and no secrets.json cannot mint a token that verifies.
func TestResolve_TokenFromAnotherPepper_IsUnknown(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	other, err := auth.NewKeyring([]byte("a-completely-different-32-byte!!"))
	require.NoError(t, err)

	foreign := auth.SeedToken(t, env.store, other, env.clock, auth.SeedTokenParams{
		ServiceAccount: env.serviceAccount,
		CreatedBy:      env.user,
	})

	principal, err := svc.ResolveRequest(t.Context(), request(nil, foreign))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrUnknownCredential,
		"a row whose hash came from another pepper must look exactly like a token nobody minted")
}

// TestResolve_RevokedSession_IsRefused: sign out this device, and the next request from it fails.
func TestResolve_RevokedSession_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	auth.RevokeSession(t, env.store, env.sessionID, core.FromTime(env.clock.Now()))

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrRevokedCredential)
}

// TestResolve_IdleExpiredSession_IsRefused walks the fake clock past the 14-day idle window without
// a request in between, which is what an abandoned browser tab looks like.
func TestResolve_IdleExpiredSession_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	env.clock.Advance(auth.SessionIdleWindow + time.Second)

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrExpiredCredential)
}

// TestResolve_ActiveSession_SlidesTheIdleWindowButNotTheCeiling is the sliding-expiry contract of
// §3.6, and the second half is the one that bounds a stolen cookie: use it every day for a month and
// it still dies at the absolute ceiling.
func TestResolve_ActiveSession_SlidesTheIdleWindowButNotTheCeiling(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	// Thirteen days later — inside the idle window — a request lands and pushes it out again.
	env.clock.Advance(13 * 24 * time.Hour)

	_, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err)

	// Two days after that, the ORIGINAL idle window has passed. The slide is what keeps it alive.
	env.clock.Advance(2 * 24 * time.Hour)

	_, err = svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err, "an actively used session must not die on its original idle expiry")

	// Past the absolute ceiling, nothing keeps it alive.
	env.clock.Advance(auth.SessionAbsoluteWindow)

	_, err = svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.ErrorIs(t, err, auth.ErrExpiredCredential,
		"the absolute ceiling is not extendable; a session held open by a polling tab still ends")
}

// TestResolve_UnknownCookie_IsRefused: a cookie of the right shape that names no row.
func TestResolve_UnknownCookie_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	forged := &http.Cookie{Name: env.cookie.Name, Value: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	require.Len(t, forged.Value, auth.SessionSecretLen)

	principal, err := svc.ResolveRequest(t.Context(), request(forged, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrUnknownCredential)
}

// TestResolve_MalformedCookie_NeverReachesTheDatabase. A cookie of the wrong shape is refused on
// shape, so a scanner cannot use the auth path as a query generator.
func TestResolve_MalformedCookie_NeverReachesTheDatabase(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	for _, value := range []string{"short", "not-base64url!!!", ""} {
		forged := &http.Cookie{Name: env.cookie.Name, Value: value}

		principal, err := svc.ResolveRequest(t.Context(), request(forged, ""))
		require.Nil(t, principal)

		if value == "" {
			// An empty cookie is indistinguishable from no cookie: net/http omits it on the way out
			// and there is nothing to look up either way.
			require.ErrorIs(t, err, auth.ErrNoCredential)

			continue
		}

		require.ErrorIs(t, err, auth.ErrMalformedCredential)
	}
}

// ptr is a local helper for the optional Micros fields of SeedTokenParams.
func ptr(m core.Micros) *core.Micros { return &m }

// TestCreateSession_ForAnInactiveUser_IsRefused puts the failure at the login attempt, where somebody
// is watching, rather than on the next request.
func TestCreateSession_ForAnInactiveUser_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	_, err := svc.CreateSession(context.Background(), auth.CreateSessionParams{
		UserID: core.ULID("01J000000000000000000NOPE"),
	})
	require.ErrorIs(t, err, store.ErrNotFound, "a session for a user that does not exist")

	// The seeded user IS active, so this one succeeds — the control assertion that keeps the one
	// above from passing for the wrong reason.
	_, err = svc.CreateSession(context.Background(), auth.CreateSessionParams{UserID: env.user})
	require.NoError(t, err)
}

// TestResolve_StaleSessionEpoch_IsRefused is "sign out everywhere" (§3.6): one UPDATE on the user
// row kills every session that user holds, on every device, without touching the session table.
//
// It is its own sentinel rather than "revoked" because it arrives in BULK — a hundred of these in a
// minute is a password change, not an attack — and an officer reading the log needs to see which.
func TestResolve_StaleSessionEpoch_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	_, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err, "the session works before the bump")

	auth.BumpSessionEpoch(t, env.store, env.clock, env.user)

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrStaleSessionEpoch)
}

// TestResolve_InactiveUser_IsRefused: suspending or disabling a person ends their sessions at the
// next request, on every device, rather than requiring somebody to hunt down rows.
//
// THE SESSION IS OPENED WHILE THE ACCOUNT IS ACTIVE and the state changes underneath it, which is
// the sequence that actually happens — an officer is suspended at 19:55 and the raid starts at
// 20:00. Seeding the account dead and trying to open a session would prove something about
// CreateSession instead, which is the assertion below it.
//
// PENDING IS IN THE TABLE and it matters: an invited account that has not confirmed its email must
// not be able to act, and 'pending' is the state the invitation flow writes.
func TestResolve_InactiveUser_IsRefused(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"pending", "suspended", "disabled"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			svc, env := newResolver(t)

			_, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
			require.NoError(t, err, "the session works while the account is active")

			auth.SetUserState(t, env.store, env.clock, env.user, state)

			principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
			require.Nil(t, principal)
			require.ErrorIs(t, err, auth.ErrPrincipalNotActive)
		})
	}
}

// TestCreateSession_InactiveUser_IsRefused is the other half of the same rule, at the other end: an
// account that may not act cannot be GIVEN a session either. The failure belongs at the login
// attempt, where somebody is watching, not on the next request.
func TestCreateSession_InactiveUser_IsRefused(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(testEpoch)
	svc := auth.NewTestService(t, st, clk)

	for _, state := range []string{"pending", "suspended", "disabled"} {
		dead := auth.SeedUserInState(t, st, clk, "dead-"+state, state)

		_, err := svc.CreateSession(context.Background(), auth.CreateSessionParams{UserID: dead})
		require.ErrorIsf(t, err, auth.ErrPrincipalNotActive,
			"a session must not be opened for an account in state %q", state)
	}
}

// TestResolve_DeletedUser_IsRefused: a soft-deleted account keeps its rows so the audit trail still
// names who did what, and stops being an identity immediately.
func TestResolve_DeletedUser_IsRefused(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	auth.SoftDeleteUser(t, env.store, env.clock, env.user)

	principal, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrPrincipalNotActive)
}

// TestResolve_DisabledServiceAccount_RefusesItsTokens is the bot half of the same rule: turning off
// a service account turns off every token it owns, without revoking them one at a time.
func TestResolve_DisabledServiceAccount_RefusesItsTokens(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(testEpoch)
	keys := auth.NewTestKeyring(t)
	svc := auth.NewService(st, clk, keys)

	owner := auth.SeedUser(t, st, clk, "officer")
	disabled := auth.SeedServiceAccountInState(t, st, clk, owner, "retired-bot", "disabled")

	token := auth.SeedToken(t, st, keys, clk, auth.SeedTokenParams{
		ServiceAccount: disabled,
		CreatedBy:      owner,
		Scopes:         "raids:read",
	})

	principal, err := svc.ResolveRequest(t.Context(), request(nil, token))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrPrincipalNotActive)
}

// TestResolve_WiringBugs_FailClosed covers the two nil-shaped mistakes that would otherwise be a
// panic in the middleware — which reads as a crashed server rather than as a misconfigured one.
//
// Neither can happen through the wiring cmd/dkp does: it builds a Service only when the store opened,
// and humago always hands the middleware a request. That is exactly why the day one of them does
// happen, the answer must be a named refusal rather than a stack trace in a raid-night log.
func TestResolve_WiringBugs_FailClosed(t *testing.T) {
	t.Parallel()

	clk := clock.NewFake(testEpoch)

	noStore := auth.NewService(nil, clk, auth.NewTestKeyring(t))

	principal, err := noStore.ResolveRequest(t.Context(), request(nil, ""))
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrNoStore)

	svc, _ := newResolver(t)

	principal, err = svc.ResolveRequest(t.Context(), nil)
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrMalformedCredential)
}
