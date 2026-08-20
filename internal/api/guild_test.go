package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// fixedClock satisfies clock.Clock; the assertion catches a signature change at compile time.
var _ clock.Clock = fixedClock{}

// These are the handler-level cases for the guild resource: they drive the real Huma pipeline over a
// real SQLite database through httptest, so validation, the problem+json shape, the ETag header and
// the 428/412/200 statuses are all exercised end to end. The cross-cutting integration cases — the
// unauthenticated gap, the statement budget — live in test/integration/guild_test.go.

// fixedClock is a clock.Clock frozen at a chosen instant. internal/clock has no Fixed helper until
// Phase 0 PR 8, so a two-line struct stands in.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// newGuildServer opens a fresh database, seeds the singleton guild row, and returns a server built
// over it plus the store (so a test can declare a statement budget).
func newGuildServer(t *testing.T) (*httptest.Server, *store.Store) {
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

	srv := httptest.NewServer(New(Config{
		Store: s,
		Clock: fixedClock{t: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)},
		// The state a booted instance has: cmd/dkp reconciled the permission catalogue before the
		// listener opened. It is stated rather than defaulted because the zero value refuses every
		// operation that declares a permission (#272), which is both guild operations — so a harness
		// that omits it is testing the gate rather than the resource. authorization_test.go is where
		// the omitted case belongs and asserts exactly that.
		Authorization: AuthorizationReconciled(),
	}))
	t.Cleanup(srv.Close)

	return srv, s
}

// getGuild issues GET /api/v1/guild and returns the response and the decoded DTO.
func getGuild(t *testing.T, base string) (*http.Response, GuildDTO) {
	t.Helper()

	res, err := http.Get(base + "/api/v1/guild") //nolint:noctx // test client
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	var dto GuildDTO
	if res.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(res.Body).Decode(&dto))
	}

	return res, dto
}

// patchGuild issues PATCH /api/v1/guild with the given If-Match (omitted when empty) and body.
func patchGuild(t *testing.T, base, ifMatch string, body map[string]any) *http.Response {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch,
		base+"/api/v1/guild", bytes.NewReader(raw))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// decodeProblemBody reads a response body as a ProblemDetail.
func decodeProblemBody(t *testing.T, res *http.Response) ProblemDetail {
	t.Helper()

	require.Equal(t, ContentTypeProblemJSON, res.Header.Get("Content-Type"),
		"an error response must be application/problem+json")

	var p ProblemDetail
	require.NoError(t, json.NewDecoder(res.Body).Decode(&p))

	return p
}

// TestGetGuild_ReturnsSingletonWithStrongETag is the read path: 200, a strong (unquoted-W) ETag
// header, and the seeded fields.
func TestGetGuild_ReturnsSingletonWithStrongETag(t *testing.T) {
	t.Parallel()

	srv, _ := newGuildServer(t)

	res, dto := getGuild(t, srv.URL)
	require.Equal(t, http.StatusOK, res.StatusCode)

	etag := res.Header.Get("ETag")
	require.NotEmpty(t, etag, "a mutable resource must carry an ETag (canonical §7)")
	require.True(t, len(etag) >= 2 && etag[0] == '"', "the ETag must be a strong, quoted validator: %q", etag)

	require.Equal(t, "Kittens Who Say Ni", dto.Name)
	require.Equal(t, "KWSN", dto.Tag)
	require.Nil(t, dto.InactiveAfterDays, "an unset inactive_after_days must serialise as null")
}

// TestGetGuild_StatementBudgetIsOne is the N+1 tripwire: the singleton read must cost exactly one
// statement. The budget is declared AFTER the server is built so boot statements are excluded.
func TestGetGuild_StatementBudgetIsOne(t *testing.T) {
	t.Parallel()

	srv, _ := newGuildServer(t)

	// Declared after the server is built, so boot statements are outside the window. The GET reads
	// the singleton in exactly one statement; anything more is an N+1 and fails the budget.
	store.Counted(t).Budget(t, 1)

	res, _ := getGuild(t, srv.URL)
	require.Equal(t, http.StatusOK, res.StatusCode)
}

// TestUpdateGuild_NoIfMatch_Returns428 asserts a missing precondition is 428 precondition_required —
// NOT 422. The If-Match parameter is declared optional precisely so Huma does not raise a 422 for its
// absence; the handler returns the 428. The test asserts the status AND the code, because a 422 would
// otherwise look like a passing negative test.
func TestUpdateGuild_NoIfMatch_Returns428(t *testing.T) {
	t.Parallel()

	srv, _ := newGuildServer(t)

	res := patchGuild(t, srv.URL, "", map[string]any{"name": "Renamed"})
	require.Equal(t, http.StatusPreconditionRequired, res.StatusCode,
		"a PATCH with no If-Match must be 428, not the 422 a required-param would produce")

	p := decodeProblemBody(t, res)
	require.Equal(t, CodePreconditionRequired, p.Code)
}

// TestUpdateGuild_StaleIfMatch_Returns412WithCurrent asserts a stale precondition is 412 carrying the
// current representation in meta.current and its ETag in meta.current_etag, so a bot merges in one
// round trip.
func TestUpdateGuild_StaleIfMatch_Returns412WithCurrent(t *testing.T) {
	t.Parallel()

	srv, _ := newGuildServer(t)

	// Capture the current representation and ETag.
	getRes, cur := getGuild(t, srv.URL)
	require.Equal(t, http.StatusOK, getRes.StatusCode)
	currentETag := getRes.Header.Get("ETag")

	res := patchGuild(t, srv.URL, `"stale-etag"`, map[string]any{"name": "Renamed"})
	require.Equal(t, http.StatusPreconditionFailed, res.StatusCode)

	p := decodeProblemBody(t, res)
	require.Equal(t, CodePreconditionFailed, p.Code)

	require.NotNil(t, p.Meta, "a 412 must carry meta")
	current, ok := p.Meta["current"].(map[string]any)
	require.True(t, ok, "meta.current must be the current representation")
	require.Equal(t, cur.Name, current["name"],
		"meta.current must be the current representation so a bot merges in one round trip")
	require.Equal(t, currentETag, p.Meta["current_etag"],
		"meta.current_etag must equal the ETag a fresh GET returns — the caller's next If-Match")
}

// TestUpdateGuild_CurrentIfMatch_Succeeds is the positive control: a PATCH with the current ETag
// succeeds, applies the change, and returns a DIFFERENT ETag. Without it, the two negative tests
// above would pass against an endpoint that always fails.
func TestUpdateGuild_CurrentIfMatch_Succeeds(t *testing.T) {
	t.Parallel()

	srv, _ := newGuildServer(t)

	getRes, before := getGuild(t, srv.URL)
	require.Equal(t, http.StatusOK, getRes.StatusCode)
	currentETag := getRes.Header.Get("ETag")

	res := patchGuild(t, srv.URL, currentETag, map[string]any{"name": "Renamed"})
	require.Equal(t, http.StatusOK, res.StatusCode)

	newETag := res.Header.Get("ETag")
	require.NotEmpty(t, newETag)
	require.NotEqual(t, currentETag, newETag,
		"a successful PATCH must return a different ETag, or the next If-Match cannot detect a race")

	var after GuildDTO
	require.NoError(t, json.NewDecoder(res.Body).Decode(&after))
	require.Equal(t, "Renamed", after.Name, "the patched field must change")
	require.Equal(t, before.Tag, after.Tag, "an unpatched field must be preserved")
}

// TestUpdateGuild_ValidationFailure_Is422 asserts a body that violates a field constraint (a
// points_precision above the 0..2 bound) is rejected by Huma's schema validation as 422, distinct
// from the 428/412 precondition statuses. This is the case that proves the 428 is a deliberate
// handler decision and not just Huma's default for a bad request.
func TestUpdateGuild_ValidationFailure_Is422(t *testing.T) {
	t.Parallel()

	srv, _ := newGuildServer(t)

	getRes, _ := getGuild(t, srv.URL)
	etag := getRes.Header.Get("ETag")

	res := patchGuild(t, srv.URL, etag, map[string]any{"points_precision": 9})
	require.Equal(t, http.StatusUnprocessableEntity, res.StatusCode,
		"points_precision above 2 must fail schema validation as 422")

	p := decodeProblemBody(t, res)
	require.Equal(t, CodeValidationFailed, p.Code)
}
