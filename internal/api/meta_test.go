package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMeta_GET_ReportsTheRunningBuild is the endpoint's whole job.
//
// GET /api/v1/meta is the capability-negotiation endpoint (docs/design/02-api-design.md:104): a bot
// reads it at boot to learn what it is talking to, and the person debugging that bot usually has no
// shell on the box. The build stamps therefore have to arrive over HTTP, unchanged.
func TestMeta_GET_ReportsTheRunningBuild(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)

	New(Config{
		Version:   "1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-08-07T00:00:00Z",
	}).ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusOK, res.StatusCode, "body: %s", rec.Body.String())

	var got MetaBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), "body: %s", rec.Body.String())

	require.Equal(t, MetaBody{
		Server:      MetaServer{Version: "1.2.3", Commit: "abc1234", BuiltAt: "2026-08-07T00:00:00Z"},
		APIVersions: []string{"v1"},
		SpecVersion: SpecVersion,
	}, got, "the whole value, not three cherry-picked fields — a fourth that changed would hide here")
}

// TestMeta_GET_NeedsNoCredential holds the `public` decision at the HTTP layer.
//
// The SPA reads this endpoint before it can log in, and a bot reads it to discover how to
// authenticate. An operation that grew an auth requirement here would break both in a way that looks
// like a client bug.
func TestMeta_GET_NeedsNoCredential(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	// Deliberately no Authorization header and no cookie.

	New(Config{}).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Result().StatusCode)
}

// TestMeta_Operation_DeclaresExplicitEmptySecurity is the `public` sentinel's mechanism.
//
// docs/design/02-api-design.md:144 requires a public operation to declare `security: []`
// EXPLICITLY. In OpenAPI an empty array and an absent key mean opposite things: the first overrides
// any document-level requirement and declares the operation open, the second inherits it. Huma
// marshals Security with omitNil rather than omitEmpty, which is the only reason a non-nil empty
// slice survives into the document at all.
//
// Delete this and the day someone adds a global security requirement, getMeta silently starts
// demanding a credential in the published spec while still serving unauthenticated — a spec that
// lies, which is the one failure the derive-from-code approach is supposed to make impossible.
func TestMeta_Operation_DeclaresExplicitEmptySecurity(t *testing.T) {
	t.Parallel()

	doc := NewHumaAPI(Config{}).OpenAPI()

	op := doc.Paths[BasePath+"/meta"]
	require.NotNil(t, op, "getMeta is not registered at %s/meta", BasePath)
	require.NotNil(t, op.Get)

	require.NotNil(t, op.Get.Security, "Security is nil, so the document will omit the key entirely")
	require.Empty(t, op.Get.Security, "getMeta is public and must require no security scheme")
	require.Equal(t, PermissionPublic, op.Get.Extensions[ExtensionPermission])

	// The marshalled form is what SDK generators and `make verify-spec` read, so assert on it rather
	// than trusting that the in-memory shape survives.
	raw, err := doc.MarshalJSON()
	require.NoError(t, err)

	var decoded struct {
		Paths map[string]struct {
			Get map[string]json.RawMessage `json:"get"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))

	get := decoded.Paths[BasePath+"/meta"].Get
	require.Contains(t, get, "security", "the emitted document omits `security` for a public operation")
	require.JSONEq(t, `[]`, string(get["security"]))
	require.JSONEq(t, `"public"`, string(get[ExtensionPermission]))
}

// TestMeta_ResponseBody_UsesSnakeCaseKeys holds canonical §16 at the wire.
//
// Go field names are PascalCase and JSON tags are the only thing standing between them and the wire.
// A forgotten tag ships `APIVersions` to every SDK consumer, and renaming it afterwards is a
// breaking change.
func TestMeta_ResponseBody_UsesSnakeCaseKeys(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	New(Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded), "body: %s", rec.Body.String())

	for key := range decoded {
		require.Regexp(t, `^[a-z][a-z0-9_]*$`, key, "response key %q is not snake_case", key)
	}

	require.ElementsMatch(t, []string{"server", "api_versions", "spec_version"},
		keysOf(decoded), "the meta body's key set changed; that is a wire contract change")
}

// keysOf returns a map's keys. Used to assert on a key set as a whole rather than probing for the
// keys somebody remembered.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
