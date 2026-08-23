package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
)

// The idempotency middleware over HTTP: an EXTERNAL test package, like auth_test.go and for the same
// reason — every assertion here is about what a bot sees, and an internal test could reach past the
// boundary the assertions are about.
//
// THE OPERATION IS A FIXTURE, and it has to be. Canonical §7 fences every mutating POST that creates
// domain state; the first one ships two waves from now, so there is nothing in the registry to point
// this at. api.NewWithOperations (export_test.go) registers the fixture into the REAL tree behind the
// REAL middleware in the REAL order, which is the difference between testing that this middleware
// works and testing that the product runs it.
//
// The fixture is deliberately shaped like the operation it stands in for: POST
// /api/v1/raids/{raid_id}/ticks, `raid.tick.create`, `raids:write`, a required Idempotency-Key. When
// the real one lands these tests point at it and nothing else changes.

// fencedPath is the fixture operation's, under canonical §7's `/api/v1/raids` fence.
const fencedPath = "/api/v1/raids/01JZ0RAID000000000000001/ticks"

// tickInput is the fixture operation's request. The Idempotency-Key is `required:"true"` exactly as
// .claude/rules/api-endpoints.md requires of a fenced POST — which is also what the middleware fences
// on and what TestArch_MutatingPost_RequiresIdempotencyKey asserts.
type tickInput struct {
	RaidID         string `path:"raid_id"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true"`

	Body struct {
		ValueCentipoints int64 `json:"value_centipoints"`
	}
}

// tickOutput carries an ETag and a Location, so the replay tests can assert the allowlist reproduces
// them.
type tickOutput struct {
	ETag     string `header:"ETag"`
	Location string `header:"Location"`

	Body struct {
		TickID string `json:"tick_id"`
		Effect int64  `json:"effect"`
	}
}

// fencedFixture is a booted instance plus the fenced operation and a counter of how many times its
// handler actually ran.
type fencedFixture struct {
	instance

	// effects counts handler entries. It is THE assertion of this file: "N replays, one effect" is a
	// claim about how many times the handler ran, not about how many 2xx the caller collected.
	effects *atomic.Int64
}

// handlerBehaviour lets one test make the fenced handler misbehave — panic, or answer 5xx — without a
// second fixture.
type handlerBehaviour struct {
	status int
	panics bool
}

func newFencedFixture(t *testing.T, behaviour handlerBehaviour) fencedFixture {
	t.Helper()

	effects := &atomic.Int64{}

	return fencedFixture{
		instance: newInstance(t, func(humaAPI huma.API) {
			registerFencedFixture(humaAPI, effects, behaviour)
		}),
		effects: effects,
	}
}

// registerFencedFixture declares the stand-in operation.
func registerFencedFixture(humaAPI huma.API, effects *atomic.Int64, behaviour handlerBehaviour) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createRaidTickFixture",
		Method:      http.MethodPost,
		Path:        api.BasePath + "/raids/{raid_id}/ticks",
		Summary:     "Create raid ticks (test fixture)",
		Tags:        []string{"raids"},
		Security: []map[string][]string{
			{"pat": {"raids:write"}},
			{"session": {}},
		},
		Extensions: map[string]any{
			api.ExtensionPermission: "raid.tick.create",
			api.ExtensionScopes:     []string{"raids:write"},
		},
		DefaultStatus: http.StatusCreated,
	}, func(_ context.Context, in *tickInput) (*tickOutput, error) {
		effect := effects.Add(1)

		if behaviour.panics {
			panic("the fenced handler exploded")
		}

		if behaviour.status >= http.StatusInternalServerError {
			return nil, api.NewProblem(behaviour.status, api.CodeInternalError, "internal error")
		}

		out := &tickOutput{
			ETag:     `"tick-etag"`,
			Location: api.BasePath + "/raids/" + in.RaidID + "/ticks/01JZ0TICK000000000000001",
		}
		out.Body.TickID = "01JZ0TICK000000000000001"

		// The effect NUMBER is in the body, so a replay is distinguishable from a second execution
		// even when both would answer 201: a replay carries effect 1 forever, a re-execution carries
		// 2. Asserting only the status could not tell them apart.
		out.Body.Effect = effect

		return out, nil
	})
}

// post issues a fenced POST with the given key, body and credential.
func (f fencedFixture) post(t *testing.T, key, body string, cookie *http.Cookie, bearer string) *http.Response {
	t.Helper()

	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}

	return f.call(t, http.MethodPost, fencedPath, body, headers, cookie, bearer)
}

// postAsOfficer is the common case: the fixture's session, a fixed body.
func (f fencedFixture) postAsOfficer(t *testing.T, key string) *http.Response {
	t.Helper()

	return f.post(t, key, `{"value_centipoints":100}`, f.cookie, "")
}

const fixtureKey = "01JZ0IDEMPOTENCYKEY0000001"

// TestIdempotency_FirstRequest_Executes is the base case, and the control for everything below: the
// middleware is transparent to a well-formed first request.
func TestIdempotency_FirstRequest_Executes(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	res := f.postAsOfficer(t, fixtureKey)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.Empty(t, res.Header.Get("Idempotency-Replayed"),
		"a first request is not a replay, and saying it was would make a bot's own log lie")
	require.Equal(t, int64(1), f.effects.Load())
}

// TestIdempotency_MissingKey_Is400 is the code that had no emitter before this change.
//
// 400 AND NOT 422, which is what Huma would have produced on its own: the header is declared
// `required:"true"`, and huma answers a missing required parameter with 422 validation_failed
// (huma.go:899). docs/api/errors.md publishes `idempotency_key_required` at 400, and the middleware
// runs before huma's input parsing, which is what makes the published code reachable at all.
func TestIdempotency_MissingKey_Is400(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	res := f.postAsOfficer(t, "")
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"))

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeIdempotencyKeyRequired, problem.Code)
	require.Zero(t, f.effects.Load(), "a request refused for a missing key must never reach the handler")
}

// TestIdempotency_UnusableKey_Is400 covers the keys that are present and are not keys. Each is a
// different client bug with the same fix, so each gets the same published code.
func TestIdempotency_UnusableKey_Is400(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{name: "a counter", key: "1"},
		{name: "whitespace", key: "    "},
		{name: "too long", key: strings.Repeat("k", 256)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFencedFixture(t, handlerBehaviour{})

			res := f.postAsOfficer(t, tt.key)
			require.Equal(t, http.StatusBadRequest, res.StatusCode)
			require.Equal(t, api.CodeIdempotencyKeyRequired, decodeProblem(t, res).Code)
			require.Zero(t, f.effects.Load())
		})
	}
}

// TestIdempotency_HundredSequentialReplays_ProduceOneEffect is the acceptance criterion in its
// sequential form, and the one that pins the CONTENT of a replay rather than only its count.
//
// Every retry gets the original 201, the original body — effect 1, not 2 through 100 — the
// allowlisted headers, and Idempotency-Replayed: true.
func TestIdempotency_HundredSequentialReplays_ProduceOneEffect(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	first := f.postAsOfficer(t, fixtureKey)
	require.Equal(t, http.StatusCreated, first.StatusCode)

	want := readBody(t, first)

	for i := range 99 {
		res := f.postAsOfficer(t, fixtureKey)

		require.Equal(t, http.StatusCreated, res.StatusCode, "replay %d", i)
		require.Equal(t, "true", res.Header.Get("Idempotency-Replayed"), "replay %d", i)
		require.Equal(t, want, readBody(t, res),
			"replay %d returned a different body: a replay is the ORIGINAL response, so the effect "+
				"number in it must still be 1", i)
		require.Equal(t, `"tick-etag"`, res.Header.Get("ETag"),
			"the allowlist reproduces ETag, so a bot that retried a create still learns the validator")
		require.Equal(t, api.BasePath+"/raids/01JZ0RAID000000000000001/ticks/01JZ0TICK000000000000001",
			res.Header.Get("Location"), "the allowlist reproduces Location, so a replayed 201 still "+
				"names what was created")
	}

	require.Equal(t, int64(1), f.effects.Load(),
		"100 requests under one key must produce ONE effect — 2 would be a raid night credited twice")
}

// TestIdempotency_HundredConcurrentDuplicates_ProduceOneEffect is the acceptance criterion's other
// half, and the one the record's shape exists for: not "the retry came later" but "they arrived at
// once".
//
// Every response is a legitimate one — the 201, a replay of it, or the 409 that says an identical
// request is still running — and the handler ran exactly once. A 5xx here would be the middleware
// racing itself.
func TestIdempotency_HundredConcurrentDuplicates_ProduceOneEffect(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	const duplicates = 100

	statuses := make([]int, duplicates)

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)

	start.Add(1)

	for i := range duplicates {
		done.Add(1)

		go func() {
			defer done.Done()

			start.Wait()

			statuses[i] = f.postAsOfficer(t, fixtureKey).StatusCode
		}()
	}

	start.Done()
	done.Wait()

	created := 0

	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("duplicate %d answered %d; the only legitimate answers are 201 (the original or "+
				"a replay of it) and 409 (an identical request is still executing)", i, status)
		}
	}

	require.Positive(t, created, "at least one duplicate must have been served")
	require.Equal(t, int64(1), f.effects.Load(),
		"exactly one of %d concurrent duplicates may reach the handler", duplicates)
}

// TestIdempotency_SameKeyDifferentBody_Is422 is the key-generation bug, and it has to be loud: a bot
// reusing one key across operations would otherwise have the second silently discarded.
func TestIdempotency_SameKeyDifferentBody_Is422(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	require.Equal(t, http.StatusCreated, f.postAsOfficer(t, fixtureKey).StatusCode)

	res := f.post(t, fixtureKey, `{"value_centipoints":9999}`, f.cookie, "")
	require.Equal(t, http.StatusUnprocessableEntity, res.StatusCode)

	problem := decodeProblem(t, res)
	require.Equal(t, api.CodeIdempotencyKeyReused, problem.Code)
	require.NotEmpty(t, problem.Meta["original_request_hash"],
		"docs/api/errors.md promises meta.original_request_hash: without it a bot author is told the "+
			"two requests differ with no way to see how")

	require.Equal(t, int64(1), f.effects.Load(),
		"a reused key must not execute the second request — that is the whole refusal")
}

// TestIdempotency_KeysAreScopedToThePrincipalNotTheToken.
//
// Canonical §7's hardest sentence. Two principals may hold the same key by coincidence and both must
// run; a bot's key must not be answered by a human's record or vice versa. This drives the two
// credential CLASSES against one key, which is the strongest available form of the assertion here —
// the token-rotation half of the same rule is internal/auth's, because a rotated token resolves to
// the same service account and therefore to the same principal_ref.
func TestIdempotency_KeysAreScopedToThePrincipalNotTheToken(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	asOfficer := f.postAsOfficer(t, fixtureKey)
	require.Equal(t, http.StatusCreated, asOfficer.StatusCode)

	asBot := f.post(t, fixtureKey, `{"value_centipoints":100}`, nil, f.token)
	require.Equal(t, http.StatusCreated, asBot.StatusCode)
	require.Empty(t, asBot.Header.Get("Idempotency-Replayed"),
		"a different principal's identical key is a different operation, not a replay of somebody "+
			"else's")

	require.Equal(t, int64(2), f.effects.Load(),
		"two principals under one key are two operations")
}

// TestIdempotency_ServerFault_ReleasesTheKey is the row docs/api/idempotency-and-concurrency.md
// publishes: "the original failed with a 5xx — the record rolled back with the transaction; your
// retry executes normally".
//
// Leaving the claim behind would turn a transient fault into 24 hours of 409s on the endpoint a raid
// night depends on, which is a worse outcome than the fault.
func TestIdempotency_ServerFault_ReleasesTheKey(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{status: http.StatusInternalServerError})

	require.Equal(t, http.StatusInternalServerError, f.postAsOfficer(t, fixtureKey).StatusCode)
	require.Equal(t, int64(1), f.effects.Load())

	// The retry EXECUTES rather than replaying the 500.
	require.Equal(t, http.StatusInternalServerError, f.postAsOfficer(t, fixtureKey).StatusCode)
	require.Equal(t, int64(2), f.effects.Load(),
		"a 5xx is not an outcome to make permanent for a day: the operation has not happened yet, "+
			"which is the whole point of the caller's retry")
}

// TestIdempotency_HandlerPanic_ReleasesTheKey is the same promise on the path that does not return:
// the deferred release, not the return-value branch.
//
// A panic is the case a `if status >= 500` check alone would miss — nothing was written, so the
// recorder holds no status at all — and it is the one where a stuck claim would be hardest to
// explain: the process logged a panic and then answered 409 for a day.
func TestIdempotency_HandlerPanic_ReleasesTheKey(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{panics: true})

	require.Equal(t, http.StatusInternalServerError, f.postAsOfficer(t, fixtureKey).StatusCode)
	require.Equal(t, int64(1), f.effects.Load())

	require.Equal(t, http.StatusInternalServerError, f.postAsOfficer(t, fixtureKey).StatusCode)
	require.Equal(t, int64(2), f.effects.Load(),
		"a panicking handler must leave no claim behind, or the retry meets a 409 for 24 hours")
}

// TestIdempotency_ClientError_IsRecordedAndReplayed. A 4xx is deterministic for a given body, so
// replaying it is both cheap and true — and a client that fixes its body is sending a DIFFERENT
// request, which collides as 422 idempotency_key_reused and is told to use a fresh key.
//
// The 422 here is huma's own validation of the fixture's body, which is exactly the shape a real
// client error takes: it happens INSIDE the operation, downstream of this middleware.
func TestIdempotency_ClientError_IsRecordedAndReplayed(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	first := f.post(t, fixtureKey, `{"value_centipoints":"not an integer"}`, f.cookie, "")
	require.Equal(t, http.StatusUnprocessableEntity, first.StatusCode)
	require.Zero(t, f.effects.Load(), "validation failed, so the handler never ran")

	second := f.post(t, fixtureKey, `{"value_centipoints":"not an integer"}`, f.cookie, "")
	require.Equal(t, http.StatusUnprocessableEntity, second.StatusCode)
	require.Equal(t, "true", second.Header.Get("Idempotency-Replayed"))
	require.Equal(t, api.ContentTypeProblemJSON, second.Header.Get("Content-Type"),
		"the allowlist reproduces Content-Type, or a replayed problem+json arrives labelled "+
			"application/json and the caller's error parser does not recognise its own error")
}

// TestIdempotency_AfterTTL_ExecutesAgain. Records expire after 24 hours; a key reused after that
// writes again, which is why the published guidance is one key per logical OPERATION rather than one
// per bot lifetime.
func TestIdempotency_AfterTTL_ExecutesAgain(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	require.Equal(t, http.StatusCreated, f.postAsOfficer(t, fixtureKey).StatusCode)
	require.Equal(t, "true", f.postAsOfficer(t, fixtureKey).Header.Get("Idempotency-Replayed"))

	f.clock.Advance(25 * time.Hour)

	res := f.postAsOfficer(t, fixtureKey)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.Empty(t, res.Header.Get("Idempotency-Replayed"))
	require.Equal(t, int64(2), f.effects.Load())
}

// TestIdempotency_UnfencedOperation_NeedsNoKey is the scope control, and it is what stops the
// middleware being "fixed" into something that fires on everything.
//
// GET /api/v1/guild declares no Idempotency-Key, so the middleware must be invisible to it. A
// middleware that demanded a key everywhere would pass every assertion above and break every read in
// the product.
func TestIdempotency_UnfencedOperation_NeedsNoKey(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	res := f.call(t, http.MethodGet, "/api/v1/guild", "", nil, f.cookie, "")
	require.Equal(t, http.StatusOK, res.StatusCode)
}

// TestIdempotency_RefusedByCapability_ClaimsNothing pins the middleware ORDER, which is the half of
// this design that a unit test of either middleware alone cannot see.
//
// A bot whose scopes do not reach the operation is refused upstream, by the capability check, and
// must never reach a record — a 403 recorded under a key would be replayed to the same caller after
// an administrator widened their role, which is a refusal outliving the reason for it.
func TestIdempotency_RefusedByCapability_ClaimsNothing(t *testing.T) {
	t.Parallel()

	f := newFencedFixture(t, handlerBehaviour{})

	narrow := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user, Scopes: "raids:read",
	})

	refused := f.post(t, fixtureKey, `{"value_centipoints":100}`, nil, narrow)
	require.Equal(t, http.StatusForbidden, refused.StatusCode)
	require.Equal(t, api.CodeInsufficientScope, decodeProblem(t, refused).Code)

	// The key is untouched, so the officer's request under the same key still executes.
	res := f.postAsOfficer(t, fixtureKey)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.Empty(t, res.Header.Get("Idempotency-Replayed"),
		"the refused request must have claimed nothing")
}

// readBody drains a response and returns it as a string.
func readBody(t *testing.T, res *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	return string(raw)
}
