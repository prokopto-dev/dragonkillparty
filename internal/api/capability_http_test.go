package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The capability half of the choke point, over HTTP. auth_test.go covers what a credential proves;
// this file covers what it REACHES — the four published 403s, and the property that a refused request
// never entered a handler.
//
// EXTERNAL TEST PACKAGE, like auth_test.go and for the same reason: every assertion here is about what
// a caller over HTTP sees, and an internal test could reach past the boundary the assertions are about.

// TestCapability_ZeroScopeToken_IsRefused is the property ADR-0011 exists to state and the one Wave 0d
// could not enforce: a token minted with no scopes is not a token with its service account's whole
// role. EQdkp Plus's `api_key` impersonates the first superadmin; this is the row that says this
// product's does not.
//
// The service account here holds `bot_readonly`, which grants `roster.read`, so the ROLE reaches this
// operation and only the empty scope set stops it. That is what makes this an intersection test rather
// than a "the bot has no permissions" test.
func TestCapability_ZeroScopeToken_IsRefused(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	zeroScope := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user, Scopes: "",
	})

	res := f.do(t, "/api/v1/guild", nil, zeroScope)
	require.Equal(t, http.StatusForbidden, res.StatusCode,
		"a zero-scope token must be refused. Effective capability is role permissions ∩ token scopes "+
			"(canonical §6): an empty scope set intersects to nothing, whatever the role holds.")

	require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"))

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeInsufficientScope, problem.Code)
	require.Equal(t, []any{"roster:read"}, problem.Meta["required_scopes"],
		"docs/api/errors.md promises meta.required_scopes on every insufficient_scope, and the "+
			"catalogue says the message always names exactly what is missing")
	require.Empty(t, problem.Meta["token_scopes"], "this token carries none, and saying so is the fix")
	require.NotEmpty(t, problem.RequestID)
}

// TestCapability_WrongScopeToken_NamesBothHalves. A token that carries scopes and not the right ones
// is the ordinary bot-author mistake, and the response has to be a diff they can read.
func TestCapability_WrongScopeToken_NamesBothHalves(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	wrong := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user, Scopes: "raids:read calendar:read",
	})

	problem := decodeProblem(t, f.do(t, "/api/v1/guild", nil, wrong))
	require.Equal(t, api.CodeInsufficientScope, problem.Code)
	require.Equal(t, []any{"roster:read"}, problem.Meta["required_scopes"])
	require.Equal(t, []any{"raids:read", "calendar:read"}, problem.Meta["token_scopes"])
}

// TestCapability_TokenOnASessionOnlyOperation_Is403SessionRequired.
//
// PATCH /api/v1/guild declares `admin.settings` and offers no `pat` alternative — session-only by
// omission, because no PAT scope family covers instance configuration (canonical §6). No token
// reaches it, and the answer says so rather than asking for a scope that does not exist.
func TestCapability_TokenOnASessionOnlyOperation_Is403SessionRequired(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	res := f.patchGuild(t, nil, f.token)
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeSessionRequired, problem.Code)
	require.NotContains(t, problem.Meta, "required_scopes",
		"there is no scope that would work, so naming one would send a bot author to mint a token "+
			"that still cannot do this")
}

// TestCapability_SessionWithoutTheRole_Is403PermissionDenied. The member-hits-an-officer-route case:
// the credential is perfect and the roles do not reach.
func TestCapability_SessionWithoutTheRole_Is403PermissionDenied(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	// A second human, granted `guest` — six read keys, none of them admin.settings.
	member := auth.SeedUser(t, f.store, f.clock, "member")
	authz.GrantRole(t, f.store, f.clock, assignmentkinds.SubjectKindUser, member, authz.RoleIDGuest)
	cookie, _ := auth.SeedSession(t, f.auth, member)

	// The read they DO hold still works, which is what makes the refusal below about the key rather
	// than about the credential.
	require.Equal(t, http.StatusOK, f.do(t, "/api/v1/guild", cookie, "").StatusCode)

	res := f.patchGuild(t, cookie, "")
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodePermissionDenied, problem.Code)
	require.Equal(t, "admin.settings", problem.Meta["required_permission"],
		"docs/api/errors.md promises meta.required_permission: an admin has to widen the role, and "+
			"they need to be told which key")
}

// TestCapability_SessionWithNoRoleAtAll_IsRefused is the fresh-instance state SECURITY.md records:
// a reconciled catalogue, nine seeded roles and no assignments, because #264 has not shipped. A live
// session reaches nothing, which is the correct direction to fail.
func TestCapability_SessionWithNoRoleAtAll_IsRefused(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	stranger := auth.SeedUser(t, f.store, f.clock, "stranger")
	cookie, _ := auth.SeedSession(t, f.auth, stranger)

	res := f.do(t, "/api/v1/guild", cookie, "")
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	require.Equal(t, api.CodePermissionDenied, decodeProblem(t, res).Code)
}

// TestCapability_RefusedRequest_NeverReachesTheHandler is the assertion that makes all of the above
// mean something. A 403 rendered AFTER a handler wrote a row would be a status code, not a control.
//
// It is the same proof TestGuild_Unauthenticated_Is401 uses for the 401: refuse the PATCH, then read
// the resource back and require it unchanged. 03-security.md §4.1 puts it plainly — "the middleware
// enforces it BEFORE the handler, so a handler cannot forget to check" — and this is the sentence
// asserted rather than believed.
func TestCapability_RefusedRequest_NeverReachesTheHandler(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	before := f.guildName(t)

	member := auth.SeedUser(t, f.store, f.clock, "member2")
	authz.GrantRole(t, f.store, f.clock, assignmentkinds.SubjectKindUser, member, authz.RoleIDGuest)
	cookie, _ := auth.SeedSession(t, f.auth, member)

	require.Equal(t, http.StatusForbidden, f.patchGuild(t, cookie, "").StatusCode)

	require.Equal(t, before, f.guildName(t),
		"the refused PATCH changed the guild, so the capability check ran after the handler rather "+
			"than before it")
}

// TestCapability_RoleRevocationTakesEffectImmediately. Nothing is cached, deliberately: a suspension
// entered through the role editor has to stop working on the next request rather than on the next
// cache expiry. docs/api/auth-and-scopes.md states it as "reducing the owner's role immediately
// reduces every token they minted", and this is the smallest form of that claim.
func TestCapability_RoleRevocationTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	member := auth.SeedUser(t, f.store, f.clock, "temp")
	until := core.FromTime(f.clock.Now().Add(time.Hour))

	authz.Grant(t, f.store, f.clock, authz.GrantParams{
		SubjectKind: assignmentkinds.SubjectKindUser, SubjectID: member,
		RoleID: authz.RoleIDGuest, SuspendedUntilAt: &until,
	})

	cookie, _ := auth.SeedSession(t, f.auth, member)

	require.Equal(t, http.StatusForbidden, f.do(t, "/api/v1/guild", cookie, "").StatusCode,
		"a suspended assignment grants nothing while the suspension runs")

	f.clock.Advance(2 * time.Hour)

	require.Equal(t, http.StatusOK, f.do(t, "/api/v1/guild", cookie, "").StatusCode,
		"and it grants again the moment the suspension lapses, with no job having run")
}

// patchGuild issues a PATCH with whichever credential the caller names. The body and If-Match are
// deliberately well-formed: a request refused at the choke point must be refused BEFORE validation
// and before the precondition, so a malformed one would prove nothing about where the refusal
// happened.
func (f authFixture) patchGuild(t *testing.T, cookie *http.Cookie, bearer string) *http.Response {
	t.Helper()

	body := strings.NewReader(`{"name":"Renamed By A Request That Should Not Land"}`)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch,
		f.server.URL+"/api/v1/guild", body)
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", f.guildETag(t))

	if cookie != nil {
		req.AddCookie(cookie)
	}

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// guildETag and guildName read the resource through the fixture's own privileged session, so a test
// asserting that a refusal changed nothing is comparing against the real row.
func (f authFixture) guildETag(t *testing.T) string {
	t.Helper()

	res := f.do(t, "/api/v1/guild", f.cookie, "")
	require.Equal(t, http.StatusOK, res.StatusCode)

	return res.Header.Get("ETag")
}

func (f authFixture) guildName(t *testing.T) string {
	t.Helper()

	res := f.do(t, "/api/v1/guild", f.cookie, "")
	require.Equal(t, http.StatusOK, res.StatusCode)

	var dto struct {
		Name string `json:"name"`
	}

	require.NoError(t, json.NewDecoder(res.Body).Decode(&dto))

	return dto.Name
}
