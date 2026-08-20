package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
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
func newServer(t *testing.T) (*httptest.Server, *store.Store) {
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

	// Authorization is stated, because the zero value fails closed (#272): an instance that never
	// reconciled its permission catalogue refuses every operation that declares a permission, and
	// both guild operations do. This harness is the booted, reconciled instance — which is also what
	// makes the known-Phase-0-gap test below meaningful, since a 503 from the gate would mask the
	// missing credential check rather than assert it.
	srv := httptest.NewServer(api.New(api.Config{
		Store: s, Clock: fixedClock{}, Authorization: api.AuthorizationReconciled(),
	}))
	t.Cleanup(srv.Close)

	return srv, s
}

// getJSON issues GET /api/v1/guild and decodes the body into dst, returning the response.
func getJSON(t *testing.T, url string, dst any) *http.Response {
	t.Helper()

	res, err := http.Get(url) //nolint:noctx // test client
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	if dst != nil && res.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(res.Body).Decode(dst))
	}

	return res
}

// patchJSON issues PATCH /api/v1/guild with the given headers and JSON body.
func patchJSON(t *testing.T, url string, body map[string]any, header http.Header) *http.Response {
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

	res, err := http.DefaultClient.Do(req)
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

	srv, _ := newServer(t)

	// Declared after the server is built, so boot statements are excluded. A singleton read is one
	// statement; an N+1 fails here.
	store.Counted(t).Budget(t, 1)

	var dto api.GuildDTO
	res := getJSON(t, srv.URL+"/api/v1/guild", &dto)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.NotEmpty(t, res.Header.Get("ETag"), "a mutable resource must carry an ETag")
	require.Equal(t, "Kittens Who Say Ni", dto.Name)
}

// TestUpdateGuild_NoIfMatch_Returns428 asserts a missing precondition is 428 precondition_required —
// not the 422 a required parameter would produce. Status AND code, so a 422 does not masquerade as a
// passing negative test.
func TestUpdateGuild_NoIfMatch_Returns428(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)

	res := patchJSON(t, srv.URL+"/api/v1/guild", map[string]any{"name": "Kittens"}, nil)
	require.Equal(t, http.StatusPreconditionRequired, res.StatusCode)

	pd := decodeProblem(t, res)
	require.Equal(t, api.CodePreconditionRequired, pd.Code)
}

// TestUpdateGuild_StaleIfMatch_Returns412 asserts a stale precondition is 412 with the current
// representation in meta.current and its ETag in meta.current_etag.
func TestUpdateGuild_StaleIfMatch_Returns412(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)

	var cur api.GuildDTO
	getRes := getJSON(t, srv.URL+"/api/v1/guild", &cur)
	currentETag := getRes.Header.Get("ETag")

	res := patchJSON(t, srv.URL+"/api/v1/guild",
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

	srv, _ := newServer(t)

	var before api.GuildDTO
	getRes := getJSON(t, srv.URL+"/api/v1/guild", &before)
	currentETag := getRes.Header.Get("ETag")

	res := patchJSON(t, srv.URL+"/api/v1/guild",
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

	srv, _ := newServer(t)

	// Set inactive_after_days to 30 (turn the sweep on).
	var g0 api.GuildDTO
	getRes := getJSON(t, srv.URL+"/api/v1/guild", &g0)
	setRes := patchJSON(t, srv.URL+"/api/v1/guild",
		map[string]any{"inactive_after_days": 30},
		http.Header{"If-Match": {getRes.Header.Get("ETag")}})
	require.Equal(t, http.StatusOK, setRes.StatusCode)

	var withDays api.GuildDTO
	require.NoError(t, json.NewDecoder(setRes.Body).Decode(&withDays))
	require.NotNil(t, withDays.InactiveAfterDays)
	require.Equal(t, int64(30), *withDays.InactiveAfterDays)

	// Now PATCH ONLY the name; inactive_after_days is omitted and must be preserved.
	res := patchJSON(t, srv.URL+"/api/v1/guild",
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

// TestGuild_Unauthenticated_IsAKnownPhase0Gap pins the deliberate Phase 0 gap: both operations are
// served with NO credential, while the published spec declares security: [{pat},{session}]. There is
// no auth middleware and no authz.Check until ROADMAP Phase 2 deliverable 1 (decision record §Q1).
//
// This test asserts the CURRENT behaviour — an unauthenticated GET and, more importantly, an
// unauthenticated PATCH both succeed — so the day the auth middleware lands it goes RED, and closing
// the gap is a deliberate deletion of this test rather than a silent discovery. The gap is named in
// SECURITY.md alongside this test. It is the same "tripwire installed ahead of the code it gates"
// pattern as internal/api/arch_test.go's idempotency test.
func TestGuild_Unauthenticated_IsAKnownPhase0Gap(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)

	// An unauthenticated GET is served.
	getRes := getJSON(t, srv.URL+"/api/v1/guild", nil)
	require.Equal(t, http.StatusOK, getRes.StatusCode,
		"PHASE 0 GAP: GET /api/v1/guild is served with no credential. When auth lands this line must "+
			"change to expect 401 — see SECURITY.md.")

	// The load-bearing half: an unauthenticated PATCH — the product's first mutating endpoint —
	// succeeds. When auth lands, an unauthenticated PATCH must be 401, and this assertion goes red.
	var before api.GuildDTO
	getRes2 := getJSON(t, srv.URL+"/api/v1/guild", &before)
	etag := getRes2.Header.Get("ETag")

	patchRes := patchJSON(t, srv.URL+"/api/v1/guild",
		map[string]any{"name": "Unauthenticated Write"},
		http.Header{"If-Match": {etag}})
	require.Equal(t, http.StatusOK, patchRes.StatusCode,
		"PHASE 0 GAP: an unauthenticated PATCH /api/v1/guild currently SUCCEEDS. This is pinned so the "+
			"day auth middleware lands it goes red and closing the gap is a deliberate change — see "+
			"SECURITY.md, 'Known Phase 0 gap: the guild resource is unauthenticated'.")
}
