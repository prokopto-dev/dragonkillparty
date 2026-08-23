package api_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
)

// The precondition middleware over HTTP. Same shape as idempotency_test.go and for the same reason:
// the operations canonical §7 requires If-Match on are two waves away, so the transitions here are
// fixtures registered into the real tree behind the real middleware.
//
// WHAT THIS FILE ADDS OVER guild_test.go. PATCH /api/v1/guild already answers 428 and 412, and its
// handler checks for itself. These fixtures deliberately do NOT: their handlers ignore If-Match
// entirely, so a 428 here can only have come from the middleware. That is the property #290 adds —
// before it, an author who forgot the check shipped a silent lost-update window and no gate noticed,
// because arch_test.go only ever asserted that the parameter was DECLARED.

const (
	transitionPath      = "/api/v1/raids/01JZ0RAID000000000000001/finalize"
	patchTransitionPath = "/api/v1/raids/01JZ0RAID000000000000001"
	unguardedPath       = "/api/v1/raids/01JZ0RAID000000000000001/notes"
)

// transitionInput is a POST transition: If-Match declared OPTIONAL, exactly as etag.go requires — a
// `required:"true"` tag makes Huma answer 422 for an absent header instead of the 428 canonical §7
// specifies.
type transitionInput struct {
	RaidID  string `path:"raid_id"`
	IfMatch string `header:"If-Match"`
}

// patchInput is the PATCH shape the arch gate already covers, with a handler that checks nothing.
type patchInput struct {
	RaidID  string `path:"raid_id"`
	IfMatch string `header:"If-Match"`

	Body struct {
		Name string `json:"name"`
	}
}

// unguardedInput declares no If-Match at all: the scope control.
type unguardedInput struct {
	RaidID string `path:"raid_id"`
}

// fencedTransitionInput declares BOTH preconditions, which is the shape
// docs/api/idempotency-and-concurrency.md names as needing both — "settling a bid session, because it
// transitions AND writes a ledger batch".
type fencedTransitionInput struct {
	RaidID         string `path:"raid_id"`
	IfMatch        string `header:"If-Match"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true"`
}

type okOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// preconditionFixture is an instance carrying four fixture operations and a count of handler
// entries.
type preconditionFixture struct {
	instance

	entries *atomic.Int64
}

func newPreconditionFixture(t *testing.T) preconditionFixture {
	t.Helper()

	entries := &atomic.Int64{}

	return preconditionFixture{
		instance: newInstance(t, func(humaAPI huma.API) {
			registerTransitionFixtures(humaAPI, entries)
		}),
		entries: entries,
	}
}

// registerTransitionFixtures declares the four stand-in operations.
//
// EVERY HANDLER IGNORES If-Match. That is the point: it makes each assertion below a statement about
// the middleware rather than about a handler that happened to remember.
func registerTransitionFixtures(humaAPI huma.API, entries *atomic.Int64) {
	security := []map[string][]string{
		{"pat": {"raids:write"}},
		{"session": {}},
	}
	extensions := map[string]any{
		api.ExtensionPermission: "raid.update",
		api.ExtensionScopes:     []string{"raids:write"},
	}

	ok := func() *okOutput {
		entries.Add(1)

		out := &okOutput{}
		out.Body.OK = true

		return out
	}

	huma.Register(humaAPI, huma.Operation{
		OperationID: "finalizeRaidFixture",
		Method:      http.MethodPost,
		Path:        api.BasePath + "/raids/{raid_id}/finalize",
		Summary:     "Finalize a raid (test fixture)",
		Tags:        []string{"raids"},
		Security:    security,
		Extensions:  extensions,
	}, func(_ context.Context, _ *transitionInput) (*okOutput, error) {
		return ok(), nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "updateRaidFixture",
		Method:      http.MethodPatch,
		Path:        api.BasePath + "/raids/{raid_id}",
		Summary:     "Update a raid (test fixture)",
		Tags:        []string{"raids"},
		Security:    security,
		Extensions:  extensions,
	}, func(_ context.Context, _ *patchInput) (*okOutput, error) {
		return ok(), nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "appendRaidNoteFixture",
		Method:      http.MethodPost,
		Path:        api.BasePath + "/raids/{raid_id}/notes",
		Summary:     "Append a raid note (test fixture, no precondition)",
		Tags:        []string{"raids"},
		Security:    security,
		Extensions:  extensions,
	}, func(_ context.Context, _ *unguardedInput) (*okOutput, error) {
		return ok(), nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "settleRaidFixture",
		Method:      http.MethodPost,
		Path:        api.BasePath + "/raids/{raid_id}/settle",
		Summary:     "Settle a raid (test fixture, both preconditions)",
		Tags:        []string{"raids"},
		Security:    security,
		Extensions:  extensions,
	}, func(_ context.Context, _ *fencedTransitionInput) (*okOutput, error) {
		return ok(), nil
	})
}

// TestPrecondition_DeclaredIfMatchAbsent_Is428 is the assertion the middleware exists for, on an
// operation whose handler does not check.
//
// 428 AND NOT 422, which is what Huma produces for a missing required parameter (huma.go:899, and
// etag.go records the line numbers). That is why the parameter is declared optional and the presence
// check lives in the middleware: a `required:"true"` If-Match would answer a missing precondition
// with `validation_failed`, and a bot following docs/api/errors.md would never see the code the
// catalogue promises.
func TestPrecondition_DeclaredIfMatchAbsent_Is428(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "a POST transition", method: http.MethodPost, path: transitionPath},
		{name: "a PATCH", method: http.MethodPatch, path: patchTransitionPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newPreconditionFixture(t)

			res := f.call(t, tt.method, tt.path, `{"name":"Renamed"}`, nil, f.cookie, "")
			require.Equal(t, http.StatusPreconditionRequired, res.StatusCode)
			require.Equal(t, api.ContentTypeProblemJSON, res.Header.Get("Content-Type"))

			problem := decodeProblem(t, res)
			require.Equal(t, api.CodePreconditionRequired, problem.Code)
			require.NotEmpty(t, problem.RequestID)

			require.Zero(t, f.entries.Load(),
				"the handler must never run: it does not check If-Match, so reaching it is the lost "+
					"update this middleware exists to prevent")
		})
	}
}

// TestPrecondition_EmptyIfMatch_IsTreatedAsAbsent. A client that sends the header with nothing in it
// has sent no precondition, and treating whitespace as a validator would let `If-Match: ` disable the
// check on every transition in the product.
func TestPrecondition_EmptyIfMatch_IsTreatedAsAbsent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "   "} {
		f := newPreconditionFixture(t)

		res := f.call(t, http.MethodPost, transitionPath, "",
			map[string]string{"If-Match": value}, f.cookie, "")

		require.Equal(t, http.StatusPreconditionRequired, res.StatusCode,
			"If-Match: %q is not a precondition", value)
		require.Zero(t, f.entries.Load())
	}
}

// TestPrecondition_IfMatchPresent_ReachesTheHandler is the other half, and the one that stops the
// middleware being "fixed" into something that always fires.
//
// WHETHER THE TAG MATCHES IS NOT THIS MIDDLEWARE'S QUESTION. Only the resource can answer it, under
// the same transaction as the write, so a present tag — matching or stale — goes through and the
// 412 comes from the domain. TestUpdateGuild_StaleIfMatch_Returns412WithCurrent is that half, on the
// one resource that has a service behind it.
func TestPrecondition_IfMatchPresent_ReachesTheHandler(t *testing.T) {
	t.Parallel()

	f := newPreconditionFixture(t)

	res := f.call(t, http.MethodPost, transitionPath, "",
		map[string]string{"If-Match": `"whatever-the-client-last-read"`}, f.cookie, "")

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, int64(1), f.entries.Load())
}

// TestPrecondition_UndeclaredIfMatch_IsUntouched is the scope control. An operation that declares no
// If-Match is not a transition, and a middleware that demanded one everywhere would pass every
// assertion above while breaking every other write in the product.
func TestPrecondition_UndeclaredIfMatch_IsUntouched(t *testing.T) {
	t.Parallel()

	f := newPreconditionFixture(t)

	res := f.call(t, http.MethodPost, unguardedPath, "", nil, f.cookie, "")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, int64(1), f.entries.Load())
}

// TestPrecondition_RunsBeforeIdempotency_SoA428BurnsNoKey is the ordering assertion, and it is the
// one neither middleware's own tests can make.
//
// An operation carrying BOTH preconditions — docs/api's "settling a bid session, because it
// transitions and writes a ledger batch" — is refused for the missing If-Match. If the key had been
// claimed first, the corrected retry would meet a 409 and then, once the claim expired, a replay of
// the 428: a client that fixed its request would be answered with the error it fixed, for a day.
func TestPrecondition_RunsBeforeIdempotency_SoA428BurnsNoKey(t *testing.T) {
	t.Parallel()

	f := newPreconditionFixture(t)

	settlePath := "/api/v1/raids/01JZ0RAID000000000000001/settle"
	key := map[string]string{"Idempotency-Key": "01JZ0IDEMPOTENCYKEY0000009"}

	refused := f.call(t, http.MethodPost, settlePath, "", key, f.cookie, "")
	require.Equal(t, http.StatusPreconditionRequired, refused.StatusCode)
	require.Equal(t, api.CodePreconditionRequired, decodeProblem(t, refused).Code)

	// The same key, now with the precondition the caller was missing. It EXECUTES: the refusal above
	// claimed nothing.
	withTag := map[string]string{
		"Idempotency-Key": "01JZ0IDEMPOTENCYKEY0000009",
		"If-Match":        `"whatever-the-client-last-read"`,
	}

	accepted := f.call(t, http.MethodPost, settlePath, "", withTag, f.cookie, "")
	require.Equal(t, http.StatusOK, accepted.StatusCode)
	require.Empty(t, accepted.Header.Get("Idempotency-Replayed"),
		"a 428 must not have been recorded under this key")
	require.Equal(t, int64(1), f.entries.Load())
}

// TestPrecondition_RunsAfterAuthorization pins the other end of the ordering.
//
// A caller who may not perform the operation at all must get the 403 that says so, not a 428 telling
// them to fetch an ETag they will not be allowed to use. The token here carries `raids:read`, which
// does not reach a transition.
func TestPrecondition_RunsAfterAuthorization(t *testing.T) {
	t.Parallel()

	f := newPreconditionFixture(t)

	narrow := auth.SeedToken(t, f.store, f.keys, f.clock, auth.SeedTokenParams{
		ServiceAccount: f.bot, CreatedBy: f.user, Scopes: "raids:read",
	})

	res := f.call(t, http.MethodPost, transitionPath, "", nil, nil, narrow)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	require.Equal(t, api.CodeInsufficientScope, decodeProblem(t, res).Code)
}
