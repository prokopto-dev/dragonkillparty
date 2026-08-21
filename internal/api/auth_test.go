package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The middleware's tests, and they are an EXTERNAL test package (api_test) where the rest of this
// directory is internal. That is deliberate: these assert what a caller over HTTP sees — a status, a
// header, a problem body — and an internal test could reach past the boundary the assertions are
// about.

// authFixture is a server with authentication wired, and one credential of each class.
type authFixture struct {
	server *httptest.Server
	clock  *clock.Fake
	store  *store.Store
	keys   *auth.Keyring
	user   core.ULID
	cookie *http.Cookie
	bot    core.ULID
	token  string
}

func newAuthFixture(t *testing.T) authFixture {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	keys := auth.NewTestKeyring(t)
	svc := auth.NewService(st, clk, keys)

	err := st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		_, insErr := q.InsertGuild(ctx, sqlitegen.InsertGuildParams{
			Name: "Kittens Who Say Ni", Tag: "KWSN", Timezone: "America/New_York",
			WeekStart: 1, PointsLabel: "DKP", PointsPrecision: 2,
			CreatedAt: 1_000, UpdatedAt: 1_000,
		})

		return insErr
	})
	require.NoError(t, err, "seed the guild row")

	user := auth.SeedUser(t, st, clk, "officer")
	cookie, _ := auth.SeedSession(t, svc, user)
	bot := auth.SeedServiceAccount(t, st, clk, user, "raidbot")

	token := auth.SeedToken(t, st, keys, clk, auth.SeedTokenParams{
		ServiceAccount: bot, CreatedBy: user, Scopes: "roster:read",
	})

	srv := httptest.NewServer(api.New(api.Config{Store: st, Clock: clk, Auth: svc}))
	t.Cleanup(srv.Close)

	return authFixture{
		server: srv, clock: clk, store: st, keys: keys,
		user: user, cookie: cookie, bot: bot, token: token,
	}
}

// do issues a GET against path with whichever credentials the caller names.
func (f authFixture) do(t *testing.T, path string, cookie *http.Cookie, bearer string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.server.URL+path, nil)
	require.NoError(t, err)

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

// TestMiddleware_AuthenticatedOperation_AcceptsBothCredentialClasses is the property
// docs/design/03-security.md §5 is built around: the SPA has no privileged channel, so a session and
// a token reach the same handler and get the same answer.
func TestMiddleware_AuthenticatedOperation_AcceptsBothCredentialClasses(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	require.Equal(t, http.StatusOK, f.do(t, "/api/v1/guild", f.cookie, "").StatusCode,
		"a session must reach the handler")
	require.Equal(t, http.StatusOK, f.do(t, "/api/v1/guild", nil, f.token).StatusCode,
		"a token must reach the same handler, with the same result")
}

// TestMiddleware_PublicOperation_IsServedAnonymously. `GET /api/v1/meta` declares an explicitly
// empty Security, which in OpenAPI means "no credential required" — as opposed to an OMITTED
// security, which means "inherit the document's". The middleware honours the difference.
func TestMiddleware_PublicOperation_IsServedAnonymously(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	require.Equal(t, http.StatusOK, f.do(t, "/api/v1/meta", nil, "").StatusCode)
}

// TestMiddleware_PublicOperation_StillRefusesABadCredential.
//
// An expired cookie on a public endpoint answers 401 rather than being quietly ignored: the SPA has
// to learn its session ended somewhere, and a bot whose token was revoked must not be told that
// everything is fine by whichever endpoint it happens to poll. Only the complete ABSENCE of a
// credential is anonymous.
func TestMiddleware_PublicOperation_StillRefusesABadCredential(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	res := f.do(t, "/api/v1/meta", nil, "dkp_pat_00000000_"+
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

// TestMiddleware_NoCredential_Is401WithTheDocumentedShape asserts the whole refusal: the status, the
// RFC 9457 media type, the RFC 9110 challenge header, and the `code` docs/api/errors.md publishes.
func TestMiddleware_NoCredential_Is401WithTheDocumentedShape(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	res := f.do(t, "/api/v1/guild", nil, "")
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"))
	require.Equal(t, `Bearer realm="dkp"`, res.Header.Get("WWW-Authenticate"),
		"RFC 9110 §15.5.2 makes WWW-Authenticate mandatory on a 401")

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeUnauthenticated, problem.Code)
	require.NotEmpty(t, problem.RequestID, "every problem body carries the support workflow's grep key")
}

// TestMiddleware_TokenFailures_CarryTheirPublishedCodes walks the three token states
// docs/api/errors.md gives distinct codes and distinct advice: mint a new one, stop entirely, check
// what you pasted.
//
// A BOT AUTHOR IS TOLD WHICH ONE, and a browser is not (see the session case below): the three have
// different fixes and a bot cannot ask a human. `meta.token_prefix` is the public half — the same
// eight characters that appear in the token list and in `dkp token revoke <prefix>` — and never the
// secret.
func TestMiddleware_TokenFailures_CarryTheirPublishedCodes(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)
	now := core.FromTime(f.clock.Now())

	expired := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user,
		ExpiresAt: at(now.Add(-time.Hour)),
	})

	revoked := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user,
		RevokedAt: at(now),
	})

	unknown := "dkp_pat_zzzzzzzz_" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	tests := []struct {
		name     string
		bearer   string
		wantCode api.Code
		wantMeta string
	}{
		{name: "expired", bearer: expired, wantCode: api.CodeTokenExpired, wantMeta: "expired_at"},
		{name: "revoked", bearer: revoked, wantCode: api.CodeTokenRevoked, wantMeta: "revoked_at"},
		{name: "unknown prefix", bearer: unknown, wantCode: api.CodeTokenInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := f.do(t, "/api/v1/guild", nil, tt.bearer)
			require.Equal(t, http.StatusUnauthorized, res.StatusCode)

			problem := decodeProblem(t, res)
			require.Equal(t, tt.wantCode, problem.Code)
			require.Equal(t, tt.bearer[len(auth.TokenScheme):len(auth.TokenScheme)+auth.TokenPrefixLen],
				problem.Meta["token_prefix"],
				"the public prefix is what a caller needs to name the token; the secret is not returned")
			require.NotContains(t, res.Header.Get("WWW-Authenticate"), tt.bearer)

			if tt.wantMeta != "" {
				require.Contains(t, problem.Meta, tt.wantMeta,
					"docs/api/errors.md promises this field in meta")
			}
		})
	}
}

// TestMiddleware_SessionFailure_IsGenericallyUnauthenticated is the deliberate asymmetry with the
// test above: every session failure answers one code, because the SPA's response to all of them is
// identical — send the user to the login screen — and there is a human present who can simply sign
// in again. The distinctions stay in the server log, where the sentinels are.
func TestMiddleware_SessionFailure_IsGenericallyUnauthenticated(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	f.clock.Advance(auth.SessionIdleWindow + time.Hour)

	res := f.do(t, "/api/v1/guild", f.cookie, "")
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeUnauthenticated, problem.Code)
	require.Empty(t, problem.Meta, "a session failure discloses nothing about which session it was")
}

// TestMiddleware_TokenInQueryString_IsRefusedAndExplained. Fifteen years of EQdkp bots send
// `?atoken=`, so a silent 401 would read as "my token is wrong" when the fix is "move it to a
// header" — which is why ADR-0011 gives this its own code and its own sentence.
func TestMiddleware_TokenInQueryString_IsRefusedAndExplained(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	res := f.do(t, "/api/v1/guild?atoken="+f.token, nil, "")
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeTokenInQueryString, problem.Code)
	require.Contains(t, problem.Detail, "Authorization",
		"the body must name the transport the caller should have used")
}

// TestMiddleware_BearerWins_OverAHigherPrivilegeCookie is §6.3's precedence rule at the HTTP
// boundary — the confusion attack of sending both and getting the union.
func TestMiddleware_BearerWins_OverAHigherPrivilegeCookie(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t)

	// A revoked token beside a perfectly good session. If the cookie were consulted at all, this
	// would be a 200.
	revoked := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user,
		RevokedAt: at(core.FromTime(f.clock.Now())),
	})

	res := f.do(t, "/api/v1/guild", f.cookie, revoked)
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	require.Equal(t, api.CodeTokenRevoked, decodeProblem(t, res).Code,
		"the bearer's verdict applies; the cookie is not read at all")
}

// TestMiddleware_NoAuthService_FailsClosed is the wiring-bug assertion, and it is the one that would
// otherwise be silent: skipping the middleware when nothing is wired would turn every gate in the
// product off and pass every test. A public operation is unaffected — it needs no principal.
func TestMiddleware_NoAuthService_FailsClosed(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)

	err := st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		_, insErr := q.InsertGuild(ctx, sqlitegen.InsertGuildParams{
			Name: "Kittens", Tag: "K", Timezone: "UTC", WeekStart: 1,
			PointsLabel: "DKP", PointsPrecision: 2, CreatedAt: 1, UpdatedAt: 1,
		})

		return insErr
	})
	require.NoError(t, err)

	srv := httptest.NewServer(api.New(api.Config{Store: st}))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/api/v1/guild") //nolint:noctx // test client
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode,
		"an operation that requires a credential must not be served when nothing can verify one")
	require.Equal(t, api.CodeServiceUnavailable, decodeProblem(t, res).Code)

	public, err := http.Get(srv.URL + "/api/v1/meta") //nolint:noctx // test client
	require.NoError(t, err)
	t.Cleanup(func() { _ = public.Body.Close() })

	require.Equal(t, http.StatusOK, public.StatusCode,
		"a public operation needs no principal, so a missing resolver costs it nothing")
}

// at is a pointer to a Micros, for the optional instants of auth.SeedTokenParams.
func at(m core.Micros) *core.Micros { return &m }

// decodeProblem reads an RFC 9457 body.
func decodeProblem(t *testing.T, res *http.Response) api.ProblemDetail {
	t.Helper()

	require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"))

	var problem api.ProblemDetail
	require.NoError(t, decodeJSON(res, &problem))

	return problem
}

// decodeJSON is the one-line body decode the fixture needs; it exists so decodeProblem reads as one
// assertion rather than three.
func decodeJSON(res *http.Response, dst any) error {
	return json.NewDecoder(res.Body).Decode(dst)
}
