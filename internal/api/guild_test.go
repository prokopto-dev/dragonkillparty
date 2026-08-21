package api

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

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
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
//
// IT ALSO SEEDS A USER AND OPENS A SESSION, and the client it returns carries that session's cookie.
// Since Phase 2 Wave 0d both guild operations declare `Security`, so the middleware refuses them
// without a credential — which is the point of the change and is asserted directly by
// TestGuild_Unauthenticated_Is401 in test/integration. Every OTHER guild test is about ETags,
// validation and the patch semantics, and each of them would otherwise assert 401 by accident.
func newGuildServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client) {
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

	clk := fixedClock{t: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	authService := auth.NewTestService(t, s, clk)

	srv := httptest.NewServer(New(Config{
		Store: s,
		Clock: clk,
		// The state a booted instance has, on both gates: cmd/dkp reconciled the permission catalogue
		// before the listener opened, and it wired a credential resolver. Both are stated rather than
		// defaulted because both zero values REFUSE — an unreconciled catalogue answers 503 to every
		// operation that declares a permission (#272), and a nil resolver answers 503 to every
		// operation that declares Security — which is both guild operations either way. A harness that
		// omitted either would be testing a gate rather than the resource. authorization_test.go and
		// auth_test.go are where the omitted cases belong, and each asserts exactly that.
		Authorization: AuthorizationReconciled(),
		Auth:          authService,
	}))
	t.Cleanup(srv.Close)

	// The BOOT STEP, not a fixture convenience. A migrated database has an empty permission table —
	// the catalogue is projected into it by authz.Reconcile on the boot path, because at migration
	// time role_permission's foreign key has nothing to resolve against — so without this every
	// operation below answers 503 "permission key has no live row", which is the fail-closed
	// behaviour working rather than a harness bug.
	authz.Boot(t, s, clk)

	// A REAL ROLE, granted through the real assignment statement. Since Wave 0e the middleware checks
	// capability as well as identity, so a session alone reaches nothing: `admin` is the built-in role
	// holding both keys this resource declares — roster.read for the GET and admin.settings for the
	// PATCH. Granting it is what keeps every test below about ETags, validation and patch semantics
	// rather than about 403; the capability cases live in capability_test.go and auth_test.go.
	user := auth.SeedUser(t, s, clk, "officer")
	authz.GrantRole(t, s, clk, assignmentkinds.SubjectKindUser, user, authz.RoleIDAdmin)

	cookie, _ := auth.SeedSession(t, authService, user)

	return srv, s, clientWithCookie(t, srv.URL, cookie)
}

// clientWithCookie returns an http.Client that sends cookie to base on every request.
//
// A JAR RATHER THAN A HEADER ON EACH CALL, so the two request helpers below keep the signatures they
// had and the cookie cannot be forgotten on one of them. The jar sends it over plain HTTP because
// the test cookie is not marked Secure — see auth.SeedSession, which explains why the real one is.
func clientWithCookie(t *testing.T, base string, cookie *http.Cookie) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	u, err := url.Parse(base)
	require.NoError(t, err)

	jar.SetCookies(u, []*http.Cookie{cookie})

	return &http.Client{Jar: jar}
}

// getGuild issues GET /api/v1/guild and returns the response and the decoded DTO.
func getGuild(t *testing.T, c *http.Client, base string) (*http.Response, GuildDTO) {
	t.Helper()

	res, err := c.Get(base + "/api/v1/guild") //nolint:noctx // test client
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	var dto GuildDTO
	if res.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(res.Body).Decode(&dto))
	}

	return res, dto
}

// patchGuild issues PATCH /api/v1/guild with the given If-Match (omitted when empty) and body.
func patchGuild(t *testing.T, c *http.Client, base, ifMatch string, body map[string]any) *http.Response {
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

	res, err := c.Do(req)
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

	srv, _, client := newGuildServer(t)

	res, dto := getGuild(t, client, srv.URL)
	require.Equal(t, http.StatusOK, res.StatusCode)

	etag := res.Header.Get("ETag")
	require.NotEmpty(t, etag, "a mutable resource must carry an ETag (canonical §7)")
	require.True(t, len(etag) >= 2 && etag[0] == '"', "the ETag must be a strong, quoted validator: %q", etag)

	require.Equal(t, "Kittens Who Say Ni", dto.Name)
	require.Equal(t, "KWSN", dto.Tag)
	require.Nil(t, dto.InactiveAfterDays, "an unset inactive_after_days must serialise as null")
}

// TestGetGuild_StatementBudgetIsThree is the N+1 tripwire: one statement to resolve the credential,
// one to decide capability, one to read the singleton. The budget is declared AFTER the server is
// built so boot statements are excluded.
//
// IT HAS BEEN ONE, TWO AND NOW THREE, and each raise was written down here rather than letting a
// budget quietly follow the code:
//
//   - PR 5a: one. The read.
//   - Wave 0d (#273): two. ADR-0011 chose opaque credentials over JWTs precisely to pay one indexed
//     round trip per request in exchange for instant revocation, and this is where that price shows.
//   - Wave 0e (#276): three. The capability check. The previous revision of this comment named it in
//     advance — "what it still refuses is a THIRD ... which is the pressure Wave 0e needs to feel
//     when it adds authz.Check" — and this is that pressure arriving. The price is the same trade
//     ADR-0011 made one layer up: nothing is cached, so revoking a role or suspending an assignment
//     takes effect on the next request rather than on the next cache expiry.
//
// THE THIRD IS ONE STATEMENT, AND THAT IS THE ASSERTION WORTH KEEPING. authz.Check answers two
// questions — does this subject hold this key, and does this key require step-up — in a single
// EffectivePermission round trip, and a FOURTH still fails here. The obvious ways to acquire one are
// reading permission.requires_step_up separately from the grant, listing every permission a subject
// holds and intersecting in Go, or resolving a role scope with its own lookup. Each is a reasonable-
// looking refactor and each doubles the authorization cost of every request in the product.
//
// The touch write is deliberately NOT in the count and must not become a way to sneak past this. The
// resolver stamps last_seen_at at most once a minute (internal/auth's touchInterval) and the session
// here was opened by the same fixed clock this request runs under, so its stamp is current and no
// write is due. A budget that included an occasional write would be a flaky budget.
func TestGetGuild_StatementBudgetIsThree(t *testing.T) {
	t.Parallel()

	srv, _, client := newGuildServer(t)

	// Declared after the server is built, so boot statements, the catalogue reconciliation, the role
	// grant and the session seed are all outside the window.
	store.Counted(t).Budget(t, 3)

	res, _ := getGuild(t, client, srv.URL)
	require.Equal(t, http.StatusOK, res.StatusCode)
}

// TestUpdateGuild_NoIfMatch_Returns428 asserts a missing precondition is 428 precondition_required —
// NOT 422. The If-Match parameter is declared optional precisely so Huma does not raise a 422 for its
// absence; the handler returns the 428. The test asserts the status AND the code, because a 422 would
// otherwise look like a passing negative test.
func TestUpdateGuild_NoIfMatch_Returns428(t *testing.T) {
	t.Parallel()

	srv, _, client := newGuildServer(t)

	res := patchGuild(t, client, srv.URL, "", map[string]any{"name": "Renamed"})
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

	srv, _, client := newGuildServer(t)

	// Capture the current representation and ETag.
	getRes, cur := getGuild(t, client, srv.URL)
	require.Equal(t, http.StatusOK, getRes.StatusCode)
	currentETag := getRes.Header.Get("ETag")

	res := patchGuild(t, client, srv.URL, `"stale-etag"`, map[string]any{"name": "Renamed"})
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

	srv, _, client := newGuildServer(t)

	getRes, before := getGuild(t, client, srv.URL)
	require.Equal(t, http.StatusOK, getRes.StatusCode)
	currentETag := getRes.Header.Get("ETag")

	res := patchGuild(t, client, srv.URL, currentETag, map[string]any{"name": "Renamed"})
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

	srv, _, client := newGuildServer(t)

	getRes, _ := getGuild(t, client, srv.URL)
	etag := getRes.Header.Get("ETag")

	res := patchGuild(t, client, srv.URL, etag, map[string]any{"points_precision": 9})
	require.Equal(t, http.StatusUnprocessableEntity, res.StatusCode,
		"points_precision above 2 must fail schema validation as 422")

	p := decodeProblemBody(t, res)
	require.Equal(t, CodeValidationFailed, p.Code)
}
