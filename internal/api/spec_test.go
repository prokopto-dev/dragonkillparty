package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// committedSpec reads openapi/openapi.json from the working tree.
func committedSpec(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "openapi", "openapi.json"))
	require.NoError(t, err,
		"openapi/openapi.json is missing. It is a committed generated artefact — run `make gen`.")

	return raw
}

// TestOpenAPI_SpecJSON_MatchesCommittedFile is acceptance criterion 4, in-process.
//
// "`go run ./cmd/dkp openapi` output equals the committed openapi/openapi.json byte for byte.
// Changing a handler's response struct without `make gen` fails the drift job."
//
// `make verify-generated` is the merge-blocking version of this. This one exists because it runs in
// milliseconds inside `make test-unit`, so an author who changes a response struct learns
// immediately rather than after a CI round trip — and because it names the fix in its failure
// message, which a digest comparison structurally cannot.
func TestOpenAPI_SpecJSON_MatchesCommittedFile(t *testing.T) {
	t.Parallel()

	got, err := SpecJSON()
	require.NoError(t, err)

	require.Equal(t, string(committedSpec(t)), string(got),
		"the committed spec is stale — run `make gen` and commit the diff")
}

// TestOpenAPI_SpecJSON_IsDeterministic guards the byte stability the drift gate depends on.
//
// `make verify-generated` hashes the openapi tree, regenerates, and hashes again. Any run-to-run
// variation reports drift on a tree nobody touched and blocks every PR in the repository until
// somebody works out why. Go's map iteration order is randomised per run, so this is a live hazard
// rather than a theoretical one — it holds only because Huma marshals through map[string]any and
// encoding/json sorts map keys.
func TestOpenAPI_SpecJSON_IsDeterministic(t *testing.T) {
	t.Parallel()

	first, err := SpecJSON()
	require.NoError(t, err)

	for range 20 {
		again, err := SpecJSON()
		require.NoError(t, err)
		require.Equal(t, string(first), string(again),
			"two calls produced different bytes; the drift gate will fail on an unmodified tree")
	}
}

// TestOpenAPI_ConfigVariation_ProducesIdenticalSpec keeps runtime values out of the document.
//
// This is the trap the `info.version` field sets. Making it the ldflags-stamped binary version reads
// as obviously correct — it is the version, after all — and it would mean every release build
// regenerated a different document, so `make verify-generated` would report drift on an untouched
// tree in exactly the build that must not fail. api.SpecVersion is the contract's version; the
// binary's version is runtime data and lives in the GET /api/v1/meta response.
func TestOpenAPI_ConfigVariation_ProducesIdenticalSpec(t *testing.T) {
	t.Parallel()

	baseline, err := SpecJSON()
	require.NoError(t, err)

	for _, cfg := range []Config{
		{},
		{Version: "9.9.9", Commit: "deadbeef", BuildDate: "2030-01-01T00:00:00Z"},
		{Version: "dev", Commit: "none", BuildDate: "unknown"},
		// A runtime value that is not a build stamp. /readyz is not a Huma operation, so its
		// disclosure policy has no business in the document — but "has no business" is what this test
		// exists to check rather than assume.
		{ReadyDetail: ReadyDetailAlways},
	} {
		doc, err := NewHumaAPI(cfg).OpenAPI().MarshalJSON()
		require.NoError(t, err)

		var got, want any
		require.NoError(t, json.Unmarshal(doc, &got))
		require.NoError(t, json.Unmarshal(baseline, &want))

		require.Equal(t, want, got,
			"Config{Version: %q} changed the OpenAPI document. Nothing in the spec may depend on a "+
				"runtime value.", cfg.Version)
	}
}

// TestOpenAPI_Document_IsOpenAPI31 pins the version the whole toolchain assumes.
//
// Huma emits 3.1 with JSON Schema 2020-12, and that choice is load-bearing downstream: the
// `webhooks` block below is a 3.1-only keyword, and docs/design/04-testing.md records an unresolved
// question about which response validator handles 3.1 correctly. A silent downgrade to 3.0 would
// make the webhooks block invalid rather than merely unread.
func TestOpenAPI_Document_IsOpenAPI31(t *testing.T) {
	t.Parallel()

	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
	}
	require.NoError(t, json.Unmarshal(committedSpec(t), &doc))

	require.Equal(t, "3.1.0", doc.OpenAPI)
	require.Equal(t, SpecVersion, doc.Info.Version,
		"info.version must be the API contract's version, never the binary's")
	require.NotEmpty(t, doc.Info.Title)
}

// TestOpenAPI_Webhooks_PlaceholderIsPresentAndParses is item V7 of verify-before-phase-0.md.
//
// V7 asks whether the "one document" promise holds — whether webhooks can live in the same OpenAPI
// file as the REST surface. `webhooks` is a top-level OpenAPI 3.1 keyword with no 3.0 equivalent, so
// it is the single construct most likely to break a 3.0-era consumer.
//
// PR 4 answers only the first half: Huma emits it and the document still parses. The three-generator
// confirmation — openapi-typescript, openapi-python-client and Scalar over the committed file — is
// PR 6's, and this test deliberately does not claim it.
func TestOpenAPI_Webhooks_PlaceholderIsPresentAndParses(t *testing.T) {
	t.Parallel()

	raw := committedSpec(t)

	// Round-tripping through `any` is the parse check: encoding/json rejects a malformed document,
	// and re-marshalling proves the structure survives a generic decode — which is what every
	// downstream generator does first.
	var generic any
	require.NoError(t, json.Unmarshal(raw, &generic), "the committed document is not valid JSON")

	_, err := json.Marshal(generic)
	require.NoError(t, err, "the document does not survive a generic decode/encode round trip")

	var doc struct {
		Webhooks map[string]struct {
			Post *struct {
				OperationID string `json:"operationId"`
				RequestBody *struct {
					Required bool `json:"required"`
				} `json:"requestBody"`
				Responses map[string]any `json:"responses"`
			} `json:"post"`
		} `json:"webhooks"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	require.Len(t, doc.Webhooks, 1, "V7 asks for exactly one placeholder entry")

	entry, ok := doc.Webhooks[WebhookPlaceholderEvent]
	require.True(t, ok, "the placeholder webhook %q is missing", WebhookPlaceholderEvent)
	require.NotNil(t, entry.Post, "a webhook entry with no operation describes nothing")
	require.Equal(t, "webhookPing", entry.Post.OperationID)
	require.NotNil(t, entry.Post.RequestBody)
	require.True(t, entry.Post.RequestBody.Required)
	require.Contains(t, entry.Post.Responses, "204")
}

// TestOpenAPI_ErrorResponses_UseProblemDetail proves the error model reached the published spec.
//
// Huma derives the error schema by reflecting over whatever huma.NewError returns. If the init()
// hook in errors.go were ever dropped, the spec would advertise Huma's ErrorModel — which has no
// `code` field — while the wire carried ours. Both files would regenerate consistently, so the drift
// gate would stay green and only a bot author would find out.
func TestOpenAPI_ErrorResponses_UseProblemDetail(t *testing.T) {
	t.Parallel()

	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(committedSpec(t), &doc))

	problem, ok := doc.Components.Schemas["ProblemDetail"]
	require.True(t, ok, "the published spec has no ProblemDetail schema; the huma.NewError hook is not installed")

	for _, field := range []string{"type", "title", "status", "code", "request_id"} {
		require.Contains(t, problem.Properties, field,
			"ProblemDetail is missing %q, which first-ten-prs.md PR 4 requires on every error", field)
	}

	codes := make([]string, 0, len(AllCodes()))
	for _, c := range AllCodes() {
		codes = append(codes, string(c))
	}

	require.Equal(t, codes, problem.Properties["code"].Enum,
		"the published `code` enum and AllCodes() disagree, so the generated SDKs' discriminated "+
			"error union is missing members the server can emit")
}
