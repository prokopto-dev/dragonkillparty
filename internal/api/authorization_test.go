package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"
)

// The fail-closed authorization gate (issue #272). What is asserted here is the handler tree's half:
// which operations an instance that could not reconcile its permission catalogue still serves, what
// the refused ones answer, and that the refusal is what an unset Config produces rather than
// something a caller has to opt into.
//
// cmd/dkp/authz_test.go asserts the other half — that a failed reconciliation produces this state and
// does not abort the boot — and cmd/dkp/ready_test.go drives both through the real binary.

// request runs one request against a handler tree built from cfg and returns the response and body.
func request(t *testing.T, cfg Config, method, target string) (*http.Response, string) {
	t.Helper()

	rec := httptest.NewRecorder()
	New(cfg).ServeHTTP(rec, httptest.NewRequest(method, target, nil))

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	return res, rec.Body.String()
}

// TestAuthorization_Unavailable_RefusesEveryPermissionBearingOperation is the control itself.
//
// Both guild operations declare an x-dkp-permission — roster.read and admin.settings — so neither may
// be served by a process that never established what a permission means in this database. 503 with
// `service_unavailable` rather than 404: the operation exists and this instance cannot serve it, and
// a bot that gets 404 concludes the endpoint was removed and stops retrying.
//
// The status is already in each operation's declared Errors list (internal/api/guild.go), so this
// refusal is inside the published contract rather than a status the spec does not admit.
func TestAuthorization_Unavailable_RefusesEveryPermissionBearingOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		target string
	}{
		{name: "getGuild declares roster.read", method: http.MethodGet, target: "/api/v1/guild"},
		{name: "updateGuild declares admin.settings", method: http.MethodPatch, target: "/api/v1/guild"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{Authorization: AuthorizationUnavailable("no such table: permission")}
			res, body := request(t, cfg, tc.method, tc.target)

			require.Equal(t, http.StatusServiceUnavailable, res.StatusCode,
				"an instance that cannot authorize anybody served %s %s. Authorization is the one "+
					"cross-cutting concern whose failure mode is silently permissive (#272); body: %s",
				tc.method, tc.target, body)

			problem := decodeProblem(t, res, body)
			require.Equal(t, CodeServiceUnavailable, problem.Code)
			require.Equal(t, unavailableDetail, problem.Detail)
		})
	}
}

// TestAuthorization_Unavailable_DoesNotLeakTheReason pins the disclosure boundary.
//
// The reason is a database message — a table name, a file path, a locking state — and this response
// is unauthenticated by construction, because refusing before authentication is the whole point.
// /readyz makes the same disclosure decision explicitly and gates it behind DKP_READYZ_DETAIL (#74);
// a 503 that volunteered the same string on an open endpoint would undo that.
func TestAuthorization_Unavailable_DoesNotLeakTheReason(t *testing.T) {
	t.Parallel()

	const reason = "attempt to write a readonly database: /srv/dkp/data/dkp.db"

	cfg := Config{Authorization: AuthorizationUnavailable(reason)}
	res, body := request(t, cfg, http.MethodGet, "/api/v1/guild")

	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	require.NotContains(t, body, "readonly database",
		"the refusal told an unauthenticated caller why the database failed")
	require.NotContains(t, body, "/srv/dkp/data/dkp.db",
		"the refusal told an unauthenticated caller where the database lives")
}

// TestAuthorization_Unavailable_PublicOperationsStillServe is what keeps this a gate rather than an
// outage.
//
// getMeta carries the `public` sentinel: no credential, no permission, nothing to authorize. It is
// also how a bot author or an operator finds out which build they are talking to, which is exactly
// the question being asked while everything else answers 503. Refusing it would buy no safety and
// would remove the surface that explains the state.
func TestAuthorization_Unavailable_PublicOperationsStillServe(t *testing.T) {
	t.Parallel()

	cfg := Config{Authorization: AuthorizationUnavailable("no such table: permission")}

	res, body := request(t, cfg, http.MethodGet, "/api/v1/meta")
	require.Equal(t, http.StatusOK, res.StatusCode,
		"a public operation was refused; body: %s", body)

	// The infrastructure endpoints are not Huma operations and never reach the gate, asserted here
	// because "canonical §13 keeps /healthz answering" is the reason this is a middleware at all.
	res, body = request(t, cfg, http.MethodGet, "/healthz")
	require.Equal(t, http.StatusOK, res.StatusCode, "/healthz was refused; body: %s", body)
	require.JSONEq(t, `{"status":"ok"}`, body)
}

// TestAuthorization_ZeroValue_FailsClosed is the decision that makes the rest of it hold.
//
// A Config that does not mention Authorization describes a process that never reconciled anything.
// The zero value of a security control is the state every future code path that forgets to set it
// will have, so it is the closed one — and this test is what stops a later "sensible default" from
// quietly making it the open one.
func TestAuthorization_ZeroValue_FailsClosed(t *testing.T) {
	t.Parallel()

	res, body := request(t, Config{}, http.MethodGet, "/api/v1/guild")

	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode,
		"Config{} served an operation that requires a permission. The unset state of an "+
			"authorization control must be the refusing one (#272); body: %s", body)
	require.Equal(t, unavailableDetail, decodeProblem(t, res, body).Detail)
}

// TestAuthorization_Reconciled_ReachesTheHandler is the positive control, and without it every
// assertion above is satisfied by a gate that refuses unconditionally.
//
// The store is nil here, so the handler answers 503 too — the degraded no-database boot maps
// guild.ErrNoStore to service_unavailable — and the discriminator is the DETAIL, which names the
// database rather than the catalogue. That is the honest unit-level control; the end-to-end one is
// test/integration/guild_test.go, where a real store answers 200 through the same gate.
func TestAuthorization_Reconciled_ReachesTheHandler(t *testing.T) {
	t.Parallel()

	cfg := Config{Authorization: AuthorizationReconciled()}
	res, body := request(t, cfg, http.MethodGet, "/api/v1/guild")

	require.NotEqual(t, unavailableDetail, decodeProblem(t, res, body).Detail,
		"a reconciled instance was refused by the gate: the request never reached the handler")
}

// TestAuthorizationState_Reason_IsNeverEmptyWhenUnavailable covers the value the /readyz detail is
// built from.
//
// An unavailable state with no stated reason still has to say something: the report it feeds is the
// operator's answer to "why is this instance not ready", and a blank detail there tells them less
// than the state alone did.
func TestAuthorizationState_Reason_IsNeverEmptyWhenUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     AuthorizationState
		available bool
		reason    string
	}{
		{
			name: "reconciled", state: AuthorizationReconciled(), available: true, reason: "",
		},
		{
			name:   "unavailable with a reason",
			state:  AuthorizationUnavailable("no such table: permission"),
			reason: "no such table: permission",
		},
		{
			name:   "unavailable with no reason still explains itself",
			state:  AuthorizationUnavailable(""),
			reason: "the permission catalogue was never reconciled",
		},
		{
			name:   "the zero value is unavailable",
			state:  AuthorizationState{},
			reason: "the permission catalogue was never reconciled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.available, tc.state.Available())
			require.Equal(t, tc.reason, tc.state.Reason())
		})
	}
}

// samplePathParam fills a path template's parameters. Its VALUE does not matter and that is the
// point: the gate runs before Huma parses anything, so a refused operation answers 503 whatever is
// in the path — and an operation that slips past the gate answers 404, 422 or 200 instead, which is
// what makes this assertion discriminating rather than lucky. A ULID is used anyway, because a
// readable failure beats a puzzling one.
const samplePathParam = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// pathParameter matches one {name} placeholder in an OpenAPI path template.
var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

// TestAuthorization_Unavailable_RefusesEveryNonPublicOperationInTheRegistry is the coverage half, and
// it is the one that keeps working as the API grows.
//
// The table test above names the two operations that exist today, which is readable and which stops
// describing the product the moment somebody adds a third. This drives the REGISTRY: every operation
// whose x-dkp-permission is not the `public` sentinel must be refused, and a new endpoint is covered
// by the act of registering it. An operation that reaches its handler on an instance with no
// authorization source fails here, in the PR that adds it, rather than in whatever Phase 2 middleware
// happens to be wired at the time.
func TestAuthorization_Unavailable_RefusesEveryNonPublicOperationInTheRegistry(t *testing.T) {
	t.Parallel()

	doc := NewHumaAPI(Config{}).OpenAPI()
	require.NotNil(t, doc, "the operation registry is empty; this test would pass vacuously")

	cfg := Config{Authorization: AuthorizationUnavailable("no such table: permission")}

	var refused, public int

	for path, item := range doc.Paths {
		for _, candidate := range []struct {
			method string
			op     *huma.Operation
		}{
			{http.MethodGet, item.Get},
			{http.MethodPut, item.Put},
			{http.MethodPost, item.Post},
			{http.MethodDelete, item.Delete},
			{http.MethodPatch, item.Patch},
			{http.MethodHead, item.Head},
		} {
			if candidate.op == nil {
				continue
			}

			if key, ok := candidate.op.Extensions[ExtensionPermission].(string); ok && key == PermissionPublic {
				public++

				continue
			}

			refused++

			target := pathParameter.ReplaceAllString(path, samplePathParam)
			res, body := request(t, cfg, candidate.method, target)

			require.Equalf(t, http.StatusServiceUnavailable, res.StatusCode,
				"%s (%s %s) does not carry the `public` sentinel and was served by an instance that "+
					"cannot authorize anybody (#272); body: %s",
				candidate.op.OperationID, candidate.method, target, body)
			require.Equalf(t, unavailableDetail, decodeProblem(t, res, body).Detail,
				"%s answered 503 for some other reason; the gate did not refuse it", candidate.op.OperationID)
		}
	}

	require.Positive(t, refused, "no permission-bearing operation was exercised; this test is vacuous")
	require.Positive(t, public, "no public operation is registered, so nothing proves the gate is "+
		"selective rather than a blanket refusal")
}
