package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// testEpoch is the instant the fixed clock stamps updated_at with, so a successful PATCH is
// deterministic across runs.
var testEpoch = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// fixedClock is a clock.Clock frozen at testEpoch. internal/clock has no Fixed helper until Phase 0
// PR 8; a two-line struct stands in.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return testEpoch }

var _ clock.Clock = fixedClock{}

// newServer clones the template, seeds the singleton guild row, and starts a real HTTP server over
// it. It returns the server and the store so a test can declare a statement budget. This is the
// harness the decision record specifies: store.NewDB + httptest.NewServer(api.New(...)), no testenv,
// no generated client (PR 6 fills in the client).
//
// SINCE PHASE 2 WAVE 0d IT ALSO OPENS A SESSION, and the client it returns carries the cookie. Both
// guild operations declare `Security`, so the middleware refuses them without a credential — which
// is what TestGuild_Unauthenticated_Is401 below asserts on purpose, and what every other test here
// would otherwise assert by accident while believing it was testing ETags.
func newServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client) {
	t.Helper()

	s := store.NewDB(t)

	err := s.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		_, insErr := q.InsertGuild(ctx, sqlitegen.InsertGuildParams{
			Name:            "Kittens Who Say Ni",
			Tag:             "KWSN",
			Timezone:        "America/New_York",
			WeekStart:       1,
			PointsLabel:     "DKP",
			PointsPrecision: 2,
			AutoSetInactive: 0,
			HideInactive:    0,
			CreatedAt:       1_000,
			UpdatedAt:       1_000,
		})

		return insErr
	})
	require.NoError(t, err, "seed the guild row")

	authService := auth.NewTestService(t, s, fixedClock{})

	// Both gates are stated, because both zero values fail closed. An instance that never reconciled
	// its permission catalogue refuses every operation that declares a permission (#272); one with no
	// credential resolver refuses every operation that declares Security. Both guild operations
	// declare both. This harness is the fully booted instance — which is what makes
	// TestGuild_Unauthenticated_Is401 below meaningful, since a 503 from either gate would mask the
	// credential check rather than assert it.
	srv := httptest.NewServer(api.New(api.Config{
		Store:         s,
		Clock:         fixedClock{},
		Authorization: api.AuthorizationReconciled(),
		Auth:          authService,
	}))
	t.Cleanup(srv.Close)

	cookie, _ := auth.SeedSession(t, authService, auth.SeedUser(t, s, fixedClock{}, "officer"))

	return srv, s, clientWithCookie(t, srv.URL, cookie)
}

// clientWithCookie returns an http.Client that sends cookie to base on every request.
//
// A JAR RATHER THAN A HEADER PER CALL, so the request helpers keep their signatures and no test can
// forget the credential on one of two requests and then explain the resulting 401 as a bug in the
// thing it was actually testing.
func clientWithCookie(t *testing.T, base string, cookie *http.Cookie) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	u, err := url.Parse(base)
	require.NoError(t, err)

	jar.SetCookies(u, []*http.Cookie{cookie})

	return &http.Client{Jar: jar}
}

// getJSON issues GET /api/v1/guild and decodes the body into dst, returning the response.
func getJSON(t *testing.T, c *http.Client, url string, dst any) *http.Response {
	t.Helper()

	res, err := c.Get(url) //nolint:noctx // test client
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	if dst != nil && res.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(res.Body).Decode(dst))
	}

	return res
}

// patchJSON issues PATCH /api/v1/guild with the given headers and JSON body.
func patchJSON(t *testing.T, c *http.Client, url string, body map[string]any, header http.Header) *http.Response {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, url, bytes.NewReader(raw))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	res, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// decodeProblem reads a response body as an api.ProblemDetail.
func decodeProblem(t *testing.T, res *http.Response) api.ProblemDetail {
	t.Helper()

	require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"),
		"an error response must be application/problem+json")

	var p api.ProblemDetail
	require.NoError(t, json.NewDecoder(res.Body).Decode(&p))

	return p
}

// TestGetGuild_Singleton_ReturnsETag reads the guild over the real server, at a statement budget of
// one, and checks the strong ETag.
func TestGetGuild_Singleton_ReturnsETag(t *testing.T) {
	t.Parallel()

	srv, _, client := newServer(t)

	// Declared after the server is built, so boot statements and the session seed are excluded. TWO
	// statements since Phase 2 Wave 0d: the credential lookup, then the singleton read. ADR-0011 chose
	// opaque credentials over JWTs to pay exactly one indexed round trip per request in exchange for
	// instant revocation, and this is where that price is asserted rather than assumed. A third would
	// be an N+1 — or an authorization layer reading role assignments per request, which is the
	// pressure Wave 0e should feel here.
	store.Counted(t).Budget(t, 2)

	var dto api.GuildDTO
	res := getJSON(t, client, srv.URL+"/api/v1/guild", &dto)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.NotEmpty(t, res.Header.Get("ETag"), "a mutable resource must carry an ETag")
	require.Equal(t, "Kittens Who Say Ni", dto.Name)
}

// TestUpdateGuild_NoIfMatch_Returns428 asserts a missing precondition is 428 precondition_required —
// not the 422 a required parameter would produce. Status AND code, so a 422 does not masquerade as a
// passing negative test.
func TestUpdateGuild_NoIfMatch_Returns428(t *testing.T) {
	t.Parallel()

	srv, _, client := newServer(t)

	res := patchJSON(t, client, srv.URL+"/api/v1/guild", map[string]any{"name": "Kittens"}, nil)
	require.Equal(t, http.StatusPreconditionRequired, res.StatusCode)

	pd := decodeProblem(t, res)
	require.Equal(t, api.CodePreconditionRequired, pd.Code)
}

// TestUpdateGuild_StaleIfMatch_Returns412 asserts a stale precondition is 412 with the current
// representation in meta.current and its ETag in meta.current_etag.
func TestUpdateGuild_StaleIfMatch_Returns412(t *testing.T) {
	t.Parallel()

	srv, _, client := newServer(t)

	var cur api.GuildDTO
	getRes := getJSON(t, client, srv.URL+"/api/v1/guild", &cur)
	currentETag := getRes.Header.Get("ETag")

	res := patchJSON(t, client, srv.URL+"/api/v1/guild",
		map[string]any{"name": "Kittens"},
		http.Header{"If-Match": {`"stale-etag"`}})
	require.Equal(t, http.StatusPreconditionFailed, res.StatusCode)

	pd := decodeProblem(t, res)
	require.Equal(t, api.CodePreconditionFailed, pd.Code)

	require.NotNil(t, pd.Meta, "a 412 must carry meta")
	current, ok := pd.Meta["current"].(map[string]any)
	require.True(t, ok, "meta.current must be the current representation")
	require.Equal(t, cur.Name, current["name"],
		"412 must return the current representation so a bot merges in one round trip")
	require.Equal(t, currentETag, pd.Meta["current_etag"],
		"meta.current_etag must be the ETag a fresh GET returns")
}

// TestUpdateGuild_CurrentIfMatch_Succeeds is the positive control: a PATCH with the current ETag
// succeeds and returns a DIFFERENT ETag, without which the two negative tests pass against an
// endpoint that always fails.
func TestUpdateGuild_CurrentIfMatch_Succeeds(t *testing.T) {
	t.Parallel()

	srv, _, client := newServer(t)

	var before api.GuildDTO
	getRes := getJSON(t, client, srv.URL+"/api/v1/guild", &before)
	currentETag := getRes.Header.Get("ETag")

	res := patchJSON(t, client, srv.URL+"/api/v1/guild",
		map[string]any{"name": "Kittens"},
		http.Header{"If-Match": {currentETag}})
	require.Equal(t, http.StatusOK, res.StatusCode)

	newETag := res.Header.Get("ETag")
	require.NotEqual(t, currentETag, newETag, "a successful PATCH must return a different ETag")

	var after api.GuildDTO
	require.NoError(t, json.NewDecoder(res.Body).Decode(&after))
	require.Equal(t, "Kittens", after.Name)
	// Fixed six-digit microsecond precision, always Z (canonical §2). testEpoch is a whole second,
	// so the fraction is all zeros — which the fixed layout keeps and time.RFC3339Nano would strip.
	require.Equal(t, "2026-08-07T12:00:00.000000Z", after.UpdatedAt,
		"updated_at must be stamped from the injected clock, at fixed microsecond precision")
}

// TestUpdateGuild_OmittedInactiveAfterDays_IsPreservedOverTheWire guards the exact data-loss
// regression that the wire *int64 / service **int64 boundary invites: an omitted inactive_after_days
// must LEAVE the stored value alone, never clear it to NULL. This is the HTTP path
// (internal/api/guild.go toInput), which the service-level test cannot reach — the service tests pass
// an explicit nil double pointer, while a real request always arrives as a nil *int64 that the
// handler must map to "absent, leave unchanged".
func TestUpdateGuild_OmittedInactiveAfterDays_IsPreservedOverTheWire(t *testing.T) {
	t.Parallel()

	srv, _, client := newServer(t)

	// Set inactive_after_days to 30 (turn the sweep on).
	var g0 api.GuildDTO
	getRes := getJSON(t, client, srv.URL+"/api/v1/guild", &g0)
	setRes := patchJSON(t, client, srv.URL+"/api/v1/guild",
		map[string]any{"inactive_after_days": 30},
		http.Header{"If-Match": {getRes.Header.Get("ETag")}})
	require.Equal(t, http.StatusOK, setRes.StatusCode)

	var withDays api.GuildDTO
	require.NoError(t, json.NewDecoder(setRes.Body).Decode(&withDays))
	require.NotNil(t, withDays.InactiveAfterDays)
	require.Equal(t, int64(30), *withDays.InactiveAfterDays)

	// Now PATCH ONLY the name; inactive_after_days is omitted and must be preserved.
	res := patchJSON(t, client, srv.URL+"/api/v1/guild",
		map[string]any{"name": "Just A Rename"},
		http.Header{"If-Match": {setRes.Header.Get("ETag")}})
	require.Equal(t, http.StatusOK, res.StatusCode)

	var after api.GuildDTO
	require.NoError(t, json.NewDecoder(res.Body).Decode(&after))
	require.Equal(t, "Just A Rename", after.Name)
	require.NotNil(t, after.InactiveAfterDays,
		"omitting inactive_after_days must NOT clear it — that would silently disable the sweep")
	require.Equal(t, int64(30), *after.InactiveAfterDays)
}

// TestGuild_Unauthenticated_Is401 is the CLOSURE of the Phase 0 gap, and it replaces the tripwire
// that pinned it.
//
// WHAT WAS HERE. TestGuild_Unauthenticated_IsAKnownPhase0Gap asserted the opposite of this: that an
// unauthenticated GET and, worse, an unauthenticated PATCH both SUCCEEDED, while the published spec
// declared `security: [{pat}, {session}]` on both. It was installed deliberately ahead of the code it
// gates (SECURITY.md, "Known Phase 0 gaps"), so that the day authentication landed it would go red
// and closing the gap would be a deliberate deletion rather than a silent discovery. Phase 2 Wave 0d
// is that day, and this is that deletion.
//
// THE PATCH HALF IS THE LOAD-BEARING ONE. A 401 on a read is a nuisance; a 401 on the product's
// first mutating endpoint is the difference between a guild's settings being editable by anyone who
// can reach the port and not. It is asserted with a CURRENT ETag — fetched with a credential — so
// the request fails on authentication rather than on a precondition, which is the failure that would
// have made this test pass for the wrong reason.
//
// WHAT IT DOES NOT ASSERT, because Wave 0d does not implement it: that a principal holding no
// permission is refused. Any live credential passes every operation until authz.Check lands in Wave
// 0e — a member's session can still PATCH the guild. SECURITY.md names that narrower gap where it
// named this one.
func TestGuild_Unauthenticated_Is401(t *testing.T) {
	t.Parallel()

	srv, _, client := newServer(t)

	// A credentialled read first, both to prove the fixture works and to get a current ETag.
	var before api.GuildDTO
	authed := getJSON(t, client, srv.URL+"/api/v1/guild", &before)
	require.Equal(t, http.StatusOK, authed.StatusCode)

	etag := authed.Header.Get("ETag")
	require.NotEmpty(t, etag)

	anonymous := &http.Client{}

	getRes := getJSON(t, anonymous, srv.URL+"/api/v1/guild", nil)
	require.Equal(t, http.StatusUnauthorized, getRes.StatusCode,
		"GET /api/v1/guild declares Security and must refuse an anonymous caller")
	requireUnauthenticatedProblem(t, getRes)

	patchRes := patchJSON(t, anonymous, srv.URL+"/api/v1/guild",
		map[string]any{"name": "Unauthenticated Write"},
		http.Header{"If-Match": {etag}})
	require.Equal(t, http.StatusUnauthorized, patchRes.StatusCode,
		"an unauthenticated PATCH of the guild must be refused before the handler runs")
	requireUnauthenticatedProblem(t, patchRes)

	// AND THE WRITE DID NOT HAPPEN. A 401 whose handler already ran is not a control, and nothing
	// about the status code alone would say so.
	var after api.GuildDTO
	require.Equal(t, http.StatusOK,
		getJSON(t, client, srv.URL+"/api/v1/guild", &after).StatusCode)
	require.Equal(t, before.Name, after.Name,
		"the refused PATCH must not have reached the handler")
}

// requireUnauthenticatedProblem asserts the wire shape of a refusal: RFC 9457 problem+json carrying
// the published `unauthenticated` code, and the WWW-Authenticate header RFC 9110 requires on a 401.
func requireUnauthenticatedProblem(t *testing.T, res *http.Response) {
	t.Helper()

	require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"),
		"every error body is application/problem+json, including this one")
	require.Equal(t, `Bearer realm="dkp"`, res.Header.Get("WWW-Authenticate"),
		"RFC 9110 makes WWW-Authenticate mandatory on a 401")

	var problem api.ProblemDetail
	require.NoError(t, json.NewDecoder(res.Body).Decode(&problem))
	require.Equal(t, api.CodeUnauthenticated, problem.Code,
		"docs/api/errors.md: no credential answers `unauthenticated`")
	require.Equal(t, http.StatusUnauthorized, problem.Status)
}
