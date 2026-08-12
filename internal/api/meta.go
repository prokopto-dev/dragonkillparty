package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// MetaOutput is the response envelope for getMeta.
type MetaOutput struct {
	Body MetaBody
}

// MetaBody is the body of GET /api/v1/meta.
//
// This is the capability-negotiation endpoint (docs/design/02-api-design.md:104): a well-written bot
// reads it at boot to learn what it is talking to, and a badly written one still works because
// nothing here is required to make a request.
//
// docs/design/02-api-design.md:166-168 lists six things this endpoint eventually reports: server
// version, feature flags, enabled login providers, point label, deprecations and api_versions.
// THREE OF THEM ARE ABSENT HERE, and their absence is the honest form rather than a gap:
// feature flags, login providers and the point label are guild configuration, and PR 4 has neither
// a settings table (PR 5 adds `guild`) nor an auth package to enumerate providers from. A field
// reporting an empty object for each would be a promise, not data — and canonical §7 makes v1
// additive-only, so adding them when they can be populated is a non-breaking change that costs
// nothing to defer.
type MetaBody struct {
	Server MetaServer `json:"server" doc:"Build identity of the running binary"`

	// APIVersions is what a bot checks to decide whether it can talk to this instance at all.
	// Canonical §7: a breaking change mints /api/v2 and v1 lives at least 18 months alongside it,
	// so this is a list rather than a scalar from the first release.
	APIVersions []string `json:"api_versions" doc:"Versioned API prefixes this instance serves"`

	// SpecVersion is the OpenAPI document's info.version — the contract's version, not the
	// binary's. The two move independently and conflating them is why this field is named for the
	// spec rather than folded into Server.
	SpecVersion string `json:"spec_version" doc:"Version of the OpenAPI contract this instance implements"`
}

// MetaServer is the running binary's identity.
//
// The same three stamps `dkp version` prints, over HTTP, because the person debugging a bot rarely
// has shell access to the box running it.
type MetaServer struct {
	Version string `json:"version" doc:"Release version, or \"dev\" for an unstamped build"`
	Commit  string `json:"commit"  doc:"Git commit the binary was built from, or \"none\""`

	// BuiltAt is an OPAQUE string, deliberately not format:"date-time". An unstamped local build
	// reports "unknown" (cmd/dkp/version.go:22), and declaring a format the default value cannot
	// satisfy would make every developer build emit a response its own schema rejects — which the
	// response-validation middleware in a later phase would then fail on.
	BuiltAt string `json:"built_at" doc:"Build timestamp as stamped at link time, or \"unknown\""`
}

// registerMeta declares GET /api/v1/meta.
//
// This is route #1 and the only operation in the registry at PR 4. Every field below is mandatory
// per .claude/rules/api-endpoints.md, and two of them deserve a note:
//
//   - Security is an EXPLICITLY EMPTY slice, not an omitted field. In OpenAPI those mean opposite
//     things: `security: []` overrides any document-level requirement and declares the operation
//     open, while omitting it inherits the document default. Huma marshals this field with omitNil
//     rather than omitEmpty (openapi.go, Operation.MarshalJSON), so a non-nil empty slice does reach
//     the document as `"security": []` — which is what docs/design/02-api-design.md:144 requires of
//     a `public` operation, and what TestMeta_Operation_DeclaresExplicitEmptySecurity holds.
//   - x-dkp-permission goes in Extensions, NOT in Metadata. .claude/rules/api-endpoints.md:32
//     prescribes `Metadata: map[string]any{"x-dkp-permission": ...}` and that idiom is wrong:
//     huma.Operation.Metadata is tagged `yaml:"-"` and never reaches the document, so an operation
//     written that way emits a spec with no x-dkp-permission at all — and `make verify-spec`
//     (internal/specgate, SPEC004) asserts on the committed JSON. Following the rule file
//     verbatim would fail the gate the rule file describes. Extensions is the field that marshals
//     inline, it is equally readable from the registry for arch_test.go, and the rule file is
//     corrected in this PR. Its value here is the `public` sentinel, not a catalogue key —
//     see SentinelPermissions.
//
// The endpoint reads no database. Runner.Status() — the only source of schema state — opens both
// pools, pings twice and chmods on every call (internal/migrate/runner.go:65-83), which is priced
// for a load balancer polling /readyz at human intervals and not for an endpoint the SPA reads on
// every page load. Keeping getMeta DB-free is also what leaves store.Tx's signature to PR 5, which
// is where internal/store/tx.go says that change belongs.
func registerMeta(api huma.API, cfg Config) {
	huma.Register(api, huma.Operation{
		OperationID: "getMeta",
		Method:      http.MethodGet,
		Path:        BasePath + "/meta",
		Summary:     "Get server capabilities and build information",
		Description: "Returns the running binary's version, the API versions this instance serves, " +
			"and the version of the OpenAPI contract it implements. Unauthenticated: a client needs " +
			"this before it can decide how to authenticate.",
		Tags:          []string{"meta"},
		Security:      []map[string][]string{},
		Extensions:    map[string]any{ExtensionPermission: PermissionPublic},
		DefaultStatus: http.StatusOK,
	}, func(_ context.Context, _ *struct{}) (*MetaOutput, error) {
		return &MetaOutput{Body: MetaBody{
			Server: MetaServer{
				Version: cfg.Version,
				Commit:  cfg.Commit,
				BuiltAt: cfg.BuildDate,
			},
			APIVersions: []string{"v1"},
			SpecVersion: SpecVersion,
		}}, nil
	})
}
