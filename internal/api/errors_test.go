package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
)

// decodeProblem reads a response body as a problem document, failing the test if it is not one.
func decodeProblem(t *testing.T, res *http.Response, body string) ProblemDetail {
	t.Helper()

	require.Equal(t, ContentTypeProblemJSON, res.Header.Get("Content-Type"),
		"an error response must be application/problem+json (RFC 9457, canonical §7); body was: %s", body)

	var p ProblemDetail
	require.NoError(t, json.Unmarshal([]byte(body), &p), "decode problem body: %s", body)

	return p
}

// do runs one request against a freshly built handler tree and returns the response and body.
func do(t *testing.T, method, target string) (*http.Response, string) {
	t.Helper()

	rec := httptest.NewRecorder()
	New(Config{}).ServeHTTP(rec, httptest.NewRequest(method, target, nil))

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	return res, rec.Body.String()
}

// publishedCatalogueRow matches a code row in docs/api/errors.md's catalogue tables:
// "| `code_name` | 404 | ... |".
var publishedCatalogueRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]{3})")

// publishedCatalogue reads the error codes docs/api/errors.md documents, in document order.
func publishedCatalogue(t *testing.T) []Code {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "api", "errors.md"))
	require.NoError(t, err, "read the published error catalogue")

	matches := publishedCatalogueRow.FindAllStringSubmatch(string(raw), -1)
	require.NotEmpty(t, matches,
		"parsed no codes out of docs/api/errors.md — has the table format changed? This test would "+
			"otherwise pass vacuously.")

	out := make([]Code, 0, len(matches))
	for _, m := range matches {
		out = append(out, Code(m[1]))
	}

	return out
}

// TestErrors_Enum_MatchesPublishedCatalogue ties the Go enum to docs/api/errors.md.
//
// docs/api/errors.md is the published contract a bot author reads, and docs/README.md:162 records
// that `reference/errors/<code>.md` is GENERATED from this enum in phase 2. So the two must agree
// exactly, in both directions and in the same order:
//
//   - A code in Go that the guide does not list generates a reference page contradicting the guide,
//     and reaches an SDK consumer as a value the documentation never mentions.
//   - A code the guide lists that Go omits is missing from both SDKs' discriminated error union
//     while the documentation promises clients can switch on it.
//
// This test caught a real divergence when it was written: the enum had `request_too_large` where the
// guide says `payload_too_large`, and had invented `not_acceptable`, which the guide does not list
// and which Huma cannot raise in this configuration (content negotiation falls back to JSON rather
// than refusing).
//
// Order, not just membership: the order here is the order the JSON Schema enum is emitted in, so
// comparing element by element also catches a code filed under the wrong section.
func TestErrors_Enum_MatchesPublishedCatalogue(t *testing.T) {
	t.Parallel()

	require.Equal(t, publishedCatalogue(t), AllCodes(),
		"internal/api/errors.go and docs/api/errors.md disagree about the closed error-code enum. "+
			"Adding a code needs a row in the guide in the same change; renaming one is a breaking "+
			"change to both generated SDKs.")
}

// TestErrors_SchemaEnum_IsDerivedFromAllCodes proves the published schema cannot drift.
//
// Code implements huma.SchemaProvider precisely so the `code` enum in openapi.json is generated from
// AllCodes() rather than hand-copied into a struct tag. If that method is ever removed, Huma falls
// back to a bare `type: string` with no enum — both SDKs lose their discriminated error union, the
// spec stops constraining the field, and nothing else fails.
func TestErrors_SchemaEnum_IsDerivedFromAllCodes(t *testing.T) {
	t.Parallel()

	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)

	schema := Code("").Schema(registry)
	require.NotNil(t, schema)
	require.Equal(t, "string", schema.Type)

	want := make([]any, 0, len(AllCodes()))
	for _, c := range AllCodes() {
		want = append(want, string(c))
	}

	require.Equal(t, want, schema.Enum,
		"the schema enum is not AllCodes(); the two have become independent lists again")
}

// TestErrors_AllCodes_AreSnakeCaseAndUnique holds canonical §16's shape for an error code.
func TestErrors_AllCodes_AreSnakeCaseAndUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[Code]bool)

	for _, c := range AllCodes() {
		require.False(t, seen[c], "duplicate code %q in AllCodes()", c)
		seen[c] = true

		require.Regexp(t, `^[a-z][a-z0-9_]*$`, string(c),
			"error codes are snake_case (canonical §16), because they are a closed enum SDKs "+
				"switch on rather than prose")
		require.True(t, c.Valid(), "%q is in AllCodes() but Valid() rejects it", c)
	}

	require.False(t, Code("definitely_not_a_real_code").Valid(),
		"Valid() accepts an unknown code, so the closed enum is not closed")
}

// TestErrors_UnroutedPath_ReturnsProblemJSON is the whole reason middleware.Problem exists.
//
// At route #1 almost every path on this server is unrouted, and net/http's built-in answer is
// `404 page not found` as text/plain. A bot whose error handling parses problem+json cannot read
// that, and canonical §7's promise is not just "never 200 with an error body" but that an error is
// always the same shape.
//
// Delete this and the regression is invisible from the browser — a human sees a 404 either way.
func TestErrors_UnroutedPath_ReturnsProblemJSON(t *testing.T) {
	t.Parallel()

	res, body := do(t, http.MethodGet, "/no/such/path")

	require.Equal(t, http.StatusNotFound, res.StatusCode)

	p := decodeProblem(t, res, body)

	require.Equal(t, CodeNotFound, p.Code)
	require.Equal(t, http.StatusNotFound, p.Status)
	require.Equal(t, "/no/such/path", p.Instance, "the problem body must name the path that failed")
	require.NotEmpty(t, p.RequestID, "every problem body carries the request id (criterion 6)")
	require.Equal(t, docsErrorBase+string(CodeNotFound), p.Type)
}

// TestErrors_MethodMismatch_ReturnsProblemJSONAndPreservesAllow covers the other half.
//
// Two things at once, and the second is easy to lose. A 405 must be problem+json like every other
// error; and RFC 9110 §15.5.6 makes the Allow header mandatory, so replacing ServeMux's response
// wholesale must not drop the one header that tells the caller which verb to use.
//
// This also pins the decision recorded in middleware.Problem: a `/` catch-all would have been the
// obvious way to convert 404s and would have swallowed this 405 entirely.
func TestErrors_MethodMismatch_ReturnsProblemJSONAndPreservesAllow(t *testing.T) {
	t.Parallel()

	res, body := do(t, http.MethodPost, "/api/v1/meta")

	require.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)

	// "GET, HEAD", not "GET": registering `GET /path` on a ServeMux also serves HEAD, and the
	// router's own Allow header says so. Asserting the exact string rather than `Contains` is what
	// makes this a test of "the router's value survived intact" rather than of "something was set".
	require.Equal(t, "GET, HEAD", res.Header.Get("Allow"),
		"a 405 without Allow leaves the caller guessing which method to use (RFC 9110 §15.5.6)")

	p := decodeProblem(t, res, body)
	require.Equal(t, CodeMethodNotAllowed, p.Code)
	require.NotEmpty(t, p.RequestID)
}

// TestErrors_NoHandlerReturns200WithErrorBody is named by first-ten-prs.md PR 4.
//
// "Never return 200 with an error body — EQdkp did that and every bot author suffered" (AGENTS.md).
// The check runs in both directions available at route #1: no success response in the published
// spec may be shaped like the error model, and a live successful response must carry no error keys.
//
// The spec half is the durable one. It scales to every operation added later without anyone
// remembering to write a test, which is exactly what an architectural assertion is for.
func TestErrors_NoHandlerReturns200WithErrorBody(t *testing.T) {
	t.Parallel()

	t.Run("no 2xx response in the spec is shaped like an error", func(t *testing.T) {
		t.Parallel()

		for _, op := range registeredOperations(t) {
			for status, response := range op.Op.Responses {
				if !strings.HasPrefix(status, "2") || response == nil {
					continue
				}

				for mediaType, content := range response.Content {
					require.NotEqual(t, ContentTypeProblemJSON, mediaType,
						"%s declares a %s response as problem+json", op, status)

					if content == nil || content.Schema == nil {
						continue
					}

					require.NotContains(t, content.Schema.Ref, "ProblemDetail",
						"%s returns the error model on %s", op, status)
				}
			}
		}
	})

	t.Run("a live 200 body carries no error keys", func(t *testing.T) {
		t.Parallel()

		res, body := do(t, http.MethodGet, "/api/v1/meta")
		require.Equal(t, http.StatusOK, res.StatusCode)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &decoded), "body: %s", body)

		for _, forbidden := range []string{"error", "errors", "code", "detail", "title"} {
			require.NotContains(t, decoded, forbidden,
				"a 2xx body contains %q, which is the error envelope leaking into a success "+
					"response", forbidden)
		}
	})
}

// TestErrors_RenderProblem_UnknownCode_FailsClosed covers the renderer's own guard.
//
// RenderProblem takes a code as a string, because middleware.Renderer must not import this package.
// That means a typo'd code would otherwise reach the wire as a value the published enum does not
// list, and every generated SDK would fail to parse it. Failing closed to internal_error keeps the
// response schema-valid and turns the caller's bug into a 500 in the logs rather than a parse error
// in somebody's bot.
func TestErrors_RenderProblem_UnknownCode_FailsClosed(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anywhere", nil)

	problemRenderer{}.RenderProblem(rec, req, http.StatusTeapot, "not_a_member_of_the_enum", "hi")

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusInternalServerError, res.StatusCode,
		"an unknown code must not be served with the caller's status as though it were valid")

	p := decodeProblem(t, res, rec.Body.String())
	require.Equal(t, CodeInternalError, p.Code)
	require.True(t, p.Code.Valid())
}

// TestErrors_HumaHook_ProducesProblemDetail proves the huma.NewError override is installed.
//
// Huma calls NewError(0, "") at registration time and reflects over the result to build the error
// schema (huma/v2 huma.go:1763). If the override in init() ever stops being installed — an import
// cycle broken the wrong way, a refactor that moves it — the spec would advertise Huma's ErrorModel,
// which has no `code` field, while the wire carried ours. Both files would regenerate consistently,
// so the drift gate would stay green and the SDKs would be wrong.
func TestErrors_HumaHook_ProducesProblemDetail(t *testing.T) {
	t.Parallel()

	err := huma.NewError(http.StatusConflict, "something clashed")

	p, ok := err.(*ProblemDetail)
	require.True(t, ok, "huma.NewError returned %T, not *ProblemDetail — the init() hook is not installed", err)
	require.Equal(t, CodeConflict, p.Code)
	require.Equal(t, http.StatusConflict, p.GetStatus())
	require.Equal(t, ContentTypeProblemJSON, p.ContentType("application/json"))
}

// TestErrors_HumaErrorWithContext_CarriesRequestIDAndInstance covers the request-scoped hook.
//
// This is the half of acceptance criterion 6 that the 404 and 405 tests above do NOT reach. Those
// errors are rendered by middleware.Problem, which reads the request id itself. An error raised from
// INSIDE Huma's pipeline — a validation failure, a bad path parameter, an unreadable body — goes
// through huma.NewErrorWithContext instead, and that is a separate code path with a separate way to
// lose the id.
//
// It matters because Huma's own default for NewErrorWithContext discards the context entirely and
// delegates to NewError. Override only NewError and every validation failure ships with an empty
// request_id, which is the field the whole support workflow depends on ("paste me the request id").
// The bodies would still be valid problem+json, so nothing else would notice.
//
// The huma.Context is built with humago.NewContext — the same constructor the adapter uses per
// request — so this exercises the real path rather than a hand-rolled stand-in.
func TestErrors_HumaErrorWithContext_CarriesRequestIDAndInstance(t *testing.T) {
	t.Parallel()

	const supplied = "01JZ8QKB4N7Y3F0S6M2W9D5H1T"

	var got huma.StatusError

	// Run through the real middleware so the request id reaches the context the way it does in
	// production, rather than being planted directly.
	handler := middleware.RequestID(clock.System{},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hc := humago.NewContext(&huma.Operation{Method: http.MethodGet, Path: "/api/v1/thing"}, r, w)
			got = huma.NewErrorWithContext(hc, http.StatusUnprocessableEntity, "validation failed",
				&huma.ErrorDetail{Message: "expected string", Location: "body.name", Value: 5})
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing?x=1", nil)
	req.Header.Set(middleware.HeaderRequestID, supplied)

	handler.ServeHTTP(httptest.NewRecorder(), req)

	p, ok := got.(*ProblemDetail)
	require.True(t, ok, "huma.NewErrorWithContext returned %T, not *ProblemDetail", got)

	require.Equal(t, supplied, p.RequestID,
		"a Huma-raised error lost the request id; the NewErrorWithContext hook is not installed or "+
			"is not reading the context")
	require.Equal(t, "/api/v1/thing", p.Instance, "instance must be the request path, without the query")
	require.Equal(t, CodeValidationFailed, p.Code)

	require.Len(t, p.Errors, 1, "Huma's per-field details were dropped")
	require.Equal(t, "body.name", p.Errors[0].Location)
	require.Equal(t, "expected string", p.Errors[0].Message)
}

// TestErrors_CodeForStatus_CoversEveryStatusHumaRaises.
//
// codeForStatus defaults to internal_error, so a status nobody mapped produces a plausible-looking
// 500-shaped body rather than an empty code. This test names the statuses that must map to something
// more specific, so the default stays a backstop rather than the answer.
func TestErrors_CodeForStatus_CoversEveryStatusHumaRaises(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   Code
	}{
		{name: "bad request", status: http.StatusBadRequest, want: CodeBadRequest},
		{name: "unauthorized", status: http.StatusUnauthorized, want: CodeUnauthenticated},
		{name: "forbidden", status: http.StatusForbidden, want: CodePermissionDenied},
		{name: "not found", status: http.StatusNotFound, want: CodeNotFound},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, want: CodeMethodNotAllowed},
		{name: "conflict", status: http.StatusConflict, want: CodeConflict},
		{name: "precondition failed", status: http.StatusPreconditionFailed, want: CodePreconditionFailed},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, want: CodeValidationFailed},
		{name: "too many requests", status: http.StatusTooManyRequests, want: CodeRateLimited},
		{name: "unmapped falls back", status: http.StatusVariantAlsoNegotiates, want: CodeInternalError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := codeForStatus(tc.status)

			require.Equal(t, tc.want, got)
			require.True(t, slices.Contains(AllCodes(), got),
				"codeForStatus returned %q, which is not in the closed enum", got)
		})
	}
}

// TestErrors_LowerCamelCase covers the shared definition arch_test.go and verify-spec.py both rely on.
func TestErrors_LowerCamelCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "simple", input: "getMeta", want: true},
		{name: "single word", input: "list", want: true},
		{name: "with digits", input: "getV2Thing", want: true},
		{name: "empty", input: "", want: false},
		{name: "PascalCase", input: "GetMeta", want: false},
		{name: "snake_case", input: "get_meta", want: false},
		{name: "kebab-case", input: "get-meta", want: false},
		{name: "dotted", input: "meta.get", want: false},
		{name: "leading digit", input: "2get", want: false},
		{name: "trailing space", input: "getMeta ", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, lowerCamelCase(tc.input))
		})
	}
}
