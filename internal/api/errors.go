package api

import (
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
)

// docsErrorBase is the prefix of the `type` URI. One docs page per code lives under it.
//
// Canonical §7 requires `type` to resolve to a real page. That is why `code` — not `type` — is what
// SDKs discriminate on: a documentation address moves when the docs site is reorganised, and an
// SDK whose error handling breaks because a docs URL changed is an SDK nobody trusts.
const docsErrorBase = "https://docs.dragonkillparty.org/errors/"

// ContentTypeProblemJSON is the media type of every error response (RFC 9457).
const ContentTypeProblemJSON = "application/problem+json"

// Code is the stable machine-readable error discriminator.
//
// The enum is CLOSED, and it is not this file's invention: the members below are exactly the
// catalogue published in docs/api/errors.md, in that document's order.
// TestErrors_Enum_MatchesPublishedCatalogue compares the two and fails on any divergence in either
// direction.
//
// That mechanism matters more than it looks. docs/README.md:162 records that
// `reference/errors/<code>.md` is GENERATED from this enum in phase 2, and
// docs/api/errors.md:164-169 records that the eventual source of truth is a hand-authored
// `openapi/registry/errors.yaml`. Until that registry exists, this file is the source and the guide
// is the specification — so a code here that the guide does not list would generate a reference page
// contradicting the published guide, and a code the guide lists but this file omits would be
// missing from the SDKs' discriminated union while the docs promise it.
//
// Adding a member is therefore a spec change in the full sense: it needs a row in
// docs/api/errors.md, it changes both generated SDKs, and the test will fail until both sides agree.
type Code string

// Authentication and authorization (docs/api/errors.md §"Authentication and authorization").
const (
	CodeUnauthenticated    Code = "unauthenticated"
	CodeTokenInvalid       Code = "token_invalid"
	CodeTokenExpired       Code = "token_expired"
	CodeTokenRevoked       Code = "token_revoked"
	CodeTokenInQueryString Code = "token_in_query_string"
	CodeSessionRequired    Code = "session_required"
	CodeStepUpRequired     Code = "step_up_required"
	CodeInsufficientScope  Code = "insufficient_scope"
	CodePermissionDenied   Code = "permission_denied"
)

// Request shape (docs/api/errors.md §"Request shape").
const (
	CodeNotFound             Code = "not_found"
	CodeMethodNotAllowed     Code = "method_not_allowed"
	CodeUnsupportedMediaType Code = "unsupported_media_type"
	CodePayloadTooLarge      Code = "payload_too_large"
	CodeValidationFailed     Code = "validation_failed"
	CodeUnknownField         Code = "unknown_field"
	CodeInvalidFilter        Code = "invalid_filter"
	CodeInvalidSortField     Code = "invalid_sort_field"
	CodeInvalidExpand        Code = "invalid_expand"
	CodeCursorInvalid        Code = "cursor_invalid"
	CodeCursorFilterMismatch Code = "cursor_filter_mismatch"

	// CodeBadRequest is the generic 400, and it is the ONE member of this enum that PR 4 added to
	// the published catalogue rather than copied from it.
	//
	// The guide's other 400s are all specific — a bad filter, a bad sort field, a tampered cursor,
	// a missing idempotency key — and none of them covers "the request was malformed in a way no
	// other code names", which is what Huma raises for an unparseable body. Without it,
	// codeForStatus had nowhere to send a 400 and would have fallen through to internal_error: a
	// 500-shaped code on a 400 response, which is both wrong and the kind of wrong a bot author
	// reports as a server bug.
	//
	// It is unreachable in PR 4's configuration — one GET operation, no body, no parameters — and
	// becomes reachable with PR 5's first request body. Added now, with its row in
	// docs/api/errors.md in the same change, because adding it later would be a breaking change to
	// both SDKs' error union.
	CodeBadRequest Code = "bad_request"
)

// Idempotency and concurrency (docs/api/errors.md §"Idempotency and concurrency").
const (
	CodeIdempotencyKeyRequired Code = "idempotency_key_required"
	CodeIdempotencyKeyReused   Code = "idempotency_key_reused"
	CodeIdempotencyKeyInFlight Code = "idempotency_key_in_flight"
	CodePreconditionRequired   Code = "precondition_required"
	CodePreconditionFailed     Code = "precondition_failed"
	CodeConflict               Code = "conflict"
)

// Roster, ingest and reconciliation (docs/api/errors.md §"Roster, ingest and reconciliation").
const (
	CodeUnknownCharacter    Code = "unknown_character"
	CodeUnknownItem         Code = "unknown_item"
	CodeAmbiguousItem       Code = "ambiguous_item"
	CodeArtifactUnparseable Code = "artifact_unparseable"
	CodeSubmissionStale     Code = "submission_stale"
	CodeRaidFinalized       Code = "raid_finalized"
	CodeInvalidFieldValue   Code = "invalid_field_value"
)

// Ledger (docs/api/errors.md §"Ledger").
const (
	CodeInsufficientBalance  Code = "insufficient_balance"
	CodeInvariantViolation   Code = "invariant_violation"
	CodeLedgerImmutable      Code = "ledger_immutable"
	CodeBatchAlreadyReversed Code = "batch_already_reversed"
	CodePoolStrategyMismatch Code = "pool_strategy_mismatch"
	CodePoolIsMirroring      Code = "pool_is_mirroring"
)

// Bidding (docs/api/errors.md §"Bidding").
const (
	CodeSessionStateInvalid   Code = "session_state_invalid"
	CodeSessionClosed         Code = "session_closed"
	CodeOutbid                Code = "outbid"
	CodeInvalidIncrement      Code = "invalid_increment"
	CodeHoldConflict          Code = "hold_conflict"
	CodeBidNotPermitted       Code = "bid_not_permitted"
	CodeSealedBidsNotRevealed Code = "sealed_bids_not_revealed"
	CodeSessionAlreadySettled Code = "session_already_settled"
	CodeResolutionFailed      Code = "resolution_failed"
)

// Infrastructure (docs/api/errors.md §"Infrastructure").
const (
	CodeRateLimited        Code = "rate_limited"
	CodeSetupRequired      Code = "setup_required"
	CodeEngineUnsupported  Code = "engine_unsupported"
	CodeServiceUnavailable Code = "service_unavailable"
	CodeInternalError      Code = "internal_error"
)

// AllCodes returns every member of the closed enum, in docs/api/errors.md's order.
//
// That order is load-bearing rather than cosmetic: it is the order the JSON Schema enum is emitted
// in, so a stable order keeps the committed spec byte-stable, and
// TestErrors_Enum_MatchesPublishedCatalogue compares this slice against the guide element by
// element rather than as a set — which catches a code moved into the wrong section as well as one
// missing entirely.
//
// Most members have no emitter yet; PR 4 registers one read-only operation. They are declared now
// because the enum is what both SDKs generate their discriminated error union from, and growing it
// one code per PR would make every early PR a breaking SDK change.
func AllCodes() []Code {
	return []Code{
		CodeUnauthenticated, CodeTokenInvalid, CodeTokenExpired, CodeTokenRevoked,
		CodeTokenInQueryString, CodeSessionRequired, CodeStepUpRequired, CodeInsufficientScope,
		CodePermissionDenied,

		CodeNotFound, CodeMethodNotAllowed, CodeUnsupportedMediaType, CodePayloadTooLarge,
		CodeValidationFailed, CodeUnknownField, CodeInvalidFilter, CodeInvalidSortField,
		CodeInvalidExpand, CodeCursorInvalid, CodeCursorFilterMismatch, CodeBadRequest,

		CodeIdempotencyKeyRequired, CodeIdempotencyKeyReused, CodeIdempotencyKeyInFlight,
		CodePreconditionRequired, CodePreconditionFailed, CodeConflict,

		CodeUnknownCharacter, CodeUnknownItem, CodeAmbiguousItem, CodeArtifactUnparseable,
		CodeSubmissionStale, CodeRaidFinalized, CodeInvalidFieldValue,

		CodeInsufficientBalance, CodeInvariantViolation, CodeLedgerImmutable,
		CodeBatchAlreadyReversed, CodePoolStrategyMismatch, CodePoolIsMirroring,

		CodeSessionStateInvalid, CodeSessionClosed, CodeOutbid, CodeInvalidIncrement,
		CodeHoldConflict, CodeBidNotPermitted, CodeSealedBidsNotRevealed,
		CodeSessionAlreadySettled, CodeResolutionFailed,

		CodeRateLimited, CodeSetupRequired, CodeEngineUnsupported, CodeServiceUnavailable,
		CodeInternalError,
	}
}

// Schema makes Code carry its own JSON Schema, with the enum derived from AllCodes().
//
// This replaces the obvious approach — an `enum:"a,b,c,..."` struct tag on ProblemDetail.Code —
// and the reason is that a struct tag is a compile-time literal that cannot be derived from the
// constants, so it would be a hand-maintained second copy of a 54-member list. The first version of
// this file did exactly that and needed a test whose whole job was to notice when the two copies
// drifted. huma.SchemaProvider removes the copy instead of policing it: there is now one list, and
// the published `code` enum cannot disagree with the Go constants because it is generated from them.
func (Code) Schema(_ huma.Registry) *huma.Schema {
	values := make([]any, 0, len(AllCodes()))
	for _, c := range AllCodes() {
		values = append(values, string(c))
	}

	return &huma.Schema{
		Type:        "string",
		Description: "Stable machine-readable discriminator from a closed enum",
		Enum:        values,
	}
}

// Valid reports whether c is a member of the closed enum.
func (c Code) Valid() bool {
	for _, known := range AllCodes() {
		if c == known {
			return true
		}
	}

	return false
}

// ErrorDetail is one entry in a problem body's `errors` array.
//
// Field names follow .claude/rules/api-endpoints.md: `location` is a path-like string into the
// request (`body.items[3].tags`, `query.limit`), `code` is a per-field reason, and `suggestions`
// exists so a 422 on an enum can name the legal values instead of making the caller read the docs.
type ErrorDetail struct {
	Location    string   `json:"location,omitempty" doc:"Where the error occurred, e.g. 'body.items[3].tags'"`
	Code        string   `json:"code,omitempty"     doc:"A per-field reason code"`
	Message     string   `json:"message"            doc:"Human-readable explanation of this one problem"`
	Suggestions []string `json:"suggestions,omitempty" doc:"Legal values or a corrected form, when they can be named"`
}

// ProblemDetail is the ONLY error body this API produces (RFC 9457).
//
// It is Huma's ErrorModel plus the two fields canonical §7 and docs/design/06-cicd-and-release.md
// require and Huma does not have: `code`, the closed-enum discriminator, and `request_id`, which is
// what turns a user's screenshot into a grep key. Huma derives the error schema in the OpenAPI
// document by reflecting over whatever huma.NewError returns (huma.go:1763), so installing the hook
// below is what puts THIS type in the spec — the Go type and the published schema cannot drift.
type ProblemDetail struct {
	Type   string `json:"type"                 format:"uri" doc:"URI reference to documentation for this error type"`
	Title  string `json:"title"                doc:"Short, stable summary of the problem type"`
	Status int    `json:"status"               doc:"HTTP status code"`
	// No `enum` tag: Code implements huma.SchemaProvider and supplies its own, derived from
	// AllCodes(). A tag here would be a second hand-maintained copy of the same 54 values.
	Code      Code           `json:"code"`
	Detail    string         `json:"detail,omitempty"     doc:"Explanation specific to this occurrence"`
	Instance  string         `json:"instance,omitempty"   doc:"URI reference identifying this specific occurrence"`
	RequestID string         `json:"request_id,omitempty" doc:"Correlation id, echoed in the X-Request-Id header"`
	Meta      map[string]any `json:"meta,omitempty"       doc:"Structured, code-specific context a client can act on"`
	Errors    []ErrorDetail  `json:"errors,omitempty"     doc:"Per-field problems, populated for validation failures"`
}

// Error satisfies the error interface.
func (p *ProblemDetail) Error() string {
	if p.Detail != "" {
		return p.Detail
	}

	return p.Title
}

// GetStatus satisfies huma.StatusError, which is how Huma learns the response status.
func (p *ProblemDetail) GetStatus() int {
	return p.Status
}

// ContentType satisfies huma.ContentTypeFilter, turning the negotiated `application/json` into
// `application/problem+json` (RFC 9457 §3). Without it Huma would serve a correct body under the
// wrong media type, and a client keying on the media type to pick its error parser would miss it.
func (p *ProblemDetail) ContentType(ct string) string {
	if ct == "application/json" {
		return ContentTypeProblemJSON
	}

	return ct
}

// compile-time proof that ProblemDetail is usable everywhere Huma needs an error.
var (
	_ huma.StatusError       = (*ProblemDetail)(nil)
	_ huma.ContentTypeFilter = (*ProblemDetail)(nil)
	_ error                  = (*ProblemDetail)(nil)
)

// NewProblem builds a problem body for status and code.
//
// Callers pass a code rather than letting it be inferred, because the status→code mapping is
// many-to-one: 409 is `conflict`, `insufficient_balance` or `idempotency_key_in_flight` depending
// on what actually happened, and only the caller knows which.
func NewProblem(status int, code Code, detail string) *ProblemDetail {
	return &ProblemDetail{
		Type:   docsErrorBase + string(code),
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
	}
}

// codeForStatus is the fallback used when an error arrives from inside Huma's pipeline, which knows
// a status but has never heard of this enum.
//
// It maps only statuses Huma itself raises. The default is CodeInternalError rather than an empty
// string on purpose: a status nobody mapped is a bug here, and it should surface as a 500-shaped
// "we do not know what happened" rather than as a body whose `code` is "" — which every SDK's
// switch statement would fall through.
func codeForStatus(status int) Code {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthenticated
	case http.StatusForbidden:
		return CodePermissionDenied
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusConflict:
		return CodeConflict
	case http.StatusPreconditionFailed:
		return CodePreconditionFailed
	case http.StatusRequestEntityTooLarge:
		return CodePayloadTooLarge
	case http.StatusUnsupportedMediaType:
		return CodeUnsupportedMediaType
	case http.StatusUnprocessableEntity:
		return CodeValidationFailed
	case http.StatusPreconditionRequired:
		return CodePreconditionRequired
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusNotImplemented:
		return CodeEngineUnsupported
	case http.StatusServiceUnavailable:
		return CodeServiceUnavailable
	default:
		return CodeInternalError
	}
}

// init installs ProblemDetail as Huma's error type, for both the wire and the spec.
//
// This is a package-level assignment into a third-party package, which .claude/rules/go-idioms.md
// otherwise bans — so here is why it is the exception and why it is safe. Huma offers exactly one
// extension point for the error model: these two vars. They are written once, at package
// initialisation, before any goroutine exists and before huma.Register can run, and never mutated
// afterwards; the failure mode the ban exists to prevent (state that changes during the run, which
// -shuffle=on and t.Parallel() turn into an intermittent failure) cannot occur.
//
// BOTH must be overridden, and for different reasons:
//
//   - NewError is what huma.Register reflects over to derive the error schema in the OpenAPI
//     document (huma.go:1763). Override it and the published schema is ProblemDetail; leave it and
//     the spec advertises Huma's ErrorModel — with no `code` field — while the wire carries ours.
//     That is spec drift the drift gate cannot see, because both files regenerate consistently.
//   - NewErrorWithContext is the one that runs per request, and it is the only place with a
//     huma.Context to read the request id out of. Huma's default implementation just calls
//     NewError, discarding the context, so overriding NewError alone yields correct bodies with an
//     empty request_id — which is the field the whole support workflow depends on.
func init() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return newProblemFromHuma(status, msg, errs...)
	}

	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		p := newProblemFromHuma(status, msg, errs...)

		if ctx != nil {
			p.RequestID = middleware.IDFromContext(ctx.Context())
			p.Instance = ctx.URL().Path
		}

		return p
	}
}

// newProblemFromHuma converts Huma's (status, message, errors) triple into a ProblemDetail.
//
// The `errs` are Huma's per-field validation details. They are copied rather than referenced so the
// wire shape is ours — Huma's ErrorDetail has no `code` or `suggestions`, and a future validator
// that populates them must not have to change this type.
func newProblemFromHuma(status int, msg string, errs ...error) *ProblemDetail {
	p := NewProblem(status, codeForStatus(status), msg)

	for _, err := range errs {
		if err == nil {
			continue
		}

		if detailer, ok := err.(huma.ErrorDetailer); ok {
			d := detailer.ErrorDetail()
			p.Errors = append(p.Errors, ErrorDetail{
				Location: d.Location,
				Message:  d.Message,
			})

			continue
		}

		p.Errors = append(p.Errors, ErrorDetail{Message: err.Error()})
	}

	return p
}

// problemRenderer satisfies middleware.Renderer, so the transport middleware can produce a problem
// body without importing this package (which would close an import cycle).
type problemRenderer struct{}

var _ middleware.Renderer = problemRenderer{}

// RenderProblem writes an RFC 9457 body for a request that never reached a Huma operation.
//
// It marshals by hand rather than through Huma's serialiser because there is no huma.Context here —
// by construction, since the two callers are an unrouted path and a recovered panic. The media type
// and the field names are the same ones ProblemDetail declares, and
// TestErrors_UnroutedPath_ReturnsProblemJSON asserts they have not diverged.
func (problemRenderer) RenderProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	c := Code(code)
	if !c.Valid() {
		// An unknown code here would put a value in the wire enum that the spec does not list.
		// Failing closed to internal_error keeps the response schema-valid; the caller's bug shows
		// up as a 500 in the logs rather than as an SDK parse failure in somebody's bot.
		c = CodeInternalError
		status = http.StatusInternalServerError
	}

	p := NewProblem(status, c, detail)
	p.RequestID = middleware.IDFromContext(r.Context())
	p.Instance = r.URL.Path

	body, err := json.Marshal(p)
	if err != nil {
		// Unreachable for a struct of scalars with a nil Meta, and handled anyway: the error path is
		// the worst possible place to panic.
		middleware.Logger(r.Context()).ErrorContext(r.Context(), "marshal problem body", "error", err)
		http.Error(w, `{"code":"internal_error"}`, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", ContentTypeProblemJSON)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// lowerCamelCase reports whether s is lowerCamelCase: a lowercase letter followed by letters and
// digits only.
//
// It lives here rather than in the test because arch_test.go and scripts/verify-spec.sh must agree
// on the definition — one checks the Huma registry in Go, the other checks the committed JSON in
// shell, and an operationId that passes one and fails the other is a gate that blocks a merge for a
// reason nobody can reproduce.
func lowerCamelCase(s string) bool {
	if s == "" {
		return false
	}

	if s[0] < 'a' || s[0] > 'z' {
		return false
	}

	for i := 1; i < len(s); i++ {
		c := s[i]

		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !isLetter && (c < '0' || c > '9') {
			return false
		}
	}

	return true
}
