package api

import (
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
)

// The authentication choke point: ONE middleware, mounted before every Huma operation, that turns a
// cookie or a bearer into exactly one Principal (docs/design/03-security.md §4.1, §5).
//
// WHY IT IS HUMA MIDDLEWARE AND NOT AN http.Handler WRAPPER. The decision it makes is per-operation
// — does this route require a credential — and the answer is the operation's own `Security`
// declaration, which only exists inside the Huma registry. A transport-level wrapper would have to
// re-derive the route from the path, which is a second routing table that disagrees with the first
// one the day somebody adds a route. RequestID and Problem stay where they are: they answer
// questions about a request that has no operation yet.
//
// WHAT IT ENFORCES TODAY, STATED PRECISELY, because the gap matters more than the feature:
// AUTHENTICATION ONLY. An operation declaring `Security` requires a live credential and answers 401
// without one — which is what closes the Phase 0 gap SECURITY.md named and
// TestGuild_Unauthenticated_IsAKnownPhase0Gap pinned. It does NOT yet check that the principal holds
// the operation's `x-dkp-permission`, nor that a token's scopes reach it, nor the capability floor
// that makes token minting and role edits session-and-step-up only. Those are authz.Check and Wave
// 0e (issue #273's successor), and until they land EVERY AUTHENTICATED PRINCIPAL PASSES EVERY
// OPERATION — a member's session can PATCH /api/v1/guild, and so can a zero-scope token. The
// published security schemes say so, SECURITY.md says so, and both notices are tripwires to delete
// with 0e.

// requireCredential reports whether an operation may only be reached with a credential.
//
// THE THREE CASES ARE `nil`, EMPTY AND NON-EMPTY, and they are not two:
//
//	Security: nil                          the field was never declared — FAIL CLOSED, require one
//	Security: []map[string][]string{}      declared EMPTY: a deliberately public operation
//	Security: [{"pat": …}, {"session": …}] a credential is required
//
// In OpenAPI an omitted `security` inherits the document-level requirement and an empty one
// overrides it, so the two spellings genuinely mean opposite things — which is why
// .claude/rules/api-endpoints.md requires the empty slice to be written out. A route that simply
// forgot the field must not become public by omission, so the nil case joins the required side. The
// architectural tests already refuse an operation with no `Security`; this is the same rule enforced
// where it costs something to get wrong.
func requireCredential(op *huma.Operation) bool {
	return op == nil || op.Security == nil || len(op.Security) > 0
}

// principalMiddleware resolves the request's credential and puts the Principal in the context.
//
// A REQUEST THAT PRESENTS SOMETHING INVALID IS REFUSED EVEN ON A PUBLIC OPERATION. An expired cookie
// on `GET /api/v1/meta` answers 401 rather than being quietly ignored: the SPA needs to learn its
// session ended somewhere, and a bot whose token was revoked must not be told that everything is
// fine by whichever endpoint it happens to poll. Only the complete ABSENCE of a credential is
// anonymous, and only where the operation says it may be.
func principalMiddleware(humaAPI huma.API, svc *auth.Service) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()

		if svc == nil {
			if requireCredential(op) {
				unconfigured(humaAPI, ctx, op)

				return
			}

			// A public operation needs no principal, so a missing resolver costs it nothing — and
			// dropping the request here instead of calling next would answer an empty 200, which is
			// the one response shape this API must never produce.
			next(ctx)

			return
		}

		req, _ := humago.Unwrap(ctx)

		principal, err := svc.ResolveRequest(ctx.Context(), req)
		if err == nil {
			next(huma.WithContext(ctx, auth.NewContext(ctx.Context(), principal)))

			return
		}

		if errors.Is(err, auth.ErrNoCredential) && !requireCredential(op) {
			next(ctx)

			return
		}

		refuse(humaAPI, ctx, op, err)
	}
}

// unconfigured answers an operation that needs a credential when no auth service was wired.
//
// IT FAILS CLOSED AND LOUDLY, rather than letting the operation through. A nil service is a wiring
// bug — cmd/dkp always builds one — and the alternative reading, "no auth service means no
// authentication", is precisely the silent hole this middleware exists to close: every gate in the
// product would be off and every test would pass. 503 rather than 401 because the caller has done
// nothing wrong and no credential they could send would help.
func unconfigured(humaAPI huma.API, ctx huma.Context, op *huma.Operation) {
	middleware.Logger(ctx.Context()).ErrorContext(ctx.Context(),
		"no authentication service is wired; refusing an operation that requires a credential",
		"operation", operationID(op))

	problem := NewProblem(http.StatusServiceUnavailable, CodeServiceUnavailable,
		"Authentication is not configured on this instance, so no credential can be verified.")

	// The write error is deliberately waived: the response is already committed by the time huma
	// reports one, and there is nothing left to say to this caller. huma prints it to stderr itself.
	_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)
}

// refuse writes the 401 (or 500) for a credential that did not resolve, and logs WHY.
//
// THE SPLIT IS THE WHOLE DESIGN. The caller gets the code docs/api/errors.md promises them and
// nothing more; the log gets the sentinel, the operation and the token prefix, which is what answers
// "was this token used after we revoked it, and when" during an incident. The secret half of a
// credential appears in neither, because it never leaves internal/auth.
func refuse(humaAPI huma.API, ctx huma.Context, op *huma.Operation, err error) {
	problem := problemForResolution(err)

	middleware.Logger(ctx.Context()).WarnContext(ctx.Context(), "credential refused",
		"operation", operationID(op),
		"code", string(problem.Code),
		"reason", err.Error())

	if problem.Status == http.StatusUnauthorized {
		// RFC 9110 §15.5.2 makes this MUST on a 401. `Bearer` rather than a cookie challenge because
		// a browser shows no dialog for it — a `Basic` challenge would put a native username-password
		// box in front of a member whose session simply expired, and the SPA's login screen is where
		// they belong.
		ctx.SetHeader("WWW-Authenticate", `Bearer realm="dkp"`)
	}

	_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)
}

// problemForResolution maps a resolution failure to the published error catalogue
// (docs/api/errors.md §"Authentication and authorization").
//
// WHAT THE CALLER IS TOLD, AND WHY IT DIFFERS BY CREDENTIAL CLASS. A bot author is told which of
// their token's three fatal states applies, because the catalogue promises it and because the three
// have different fixes: mint a new one, stop entirely, or check what you typed. A browser is told
// only `unauthenticated`, because every session failure has one fix — sign in again — and the SPA
// would branch on nothing.
//
// NOTHING HERE NAMES A SECRET. `meta.token_prefix` is the public 8-character half that appears in
// logs and in the token list; the timestamps are facts about a row the caller already holds.
func problemForResolution(err error) *ProblemDetail {
	var failure *auth.ResolutionError

	hasFailure := errors.As(err, &failure)

	switch {
	case errors.Is(err, auth.ErrTokenInQueryString):
		return NewProblem(http.StatusUnauthorized, CodeTokenInQueryString,
			"Send the token as `Authorization: Bearer dkp_pat_…`. A token in a query string ends up "+
				"in access logs, proxy logs and browser history, so it is refused.")

	case errors.Is(err, auth.ErrNoPepper):
		// The caller's credential may be perfectly good: this instance cannot verify ANY token
		// because its secrets file was not readable. That is a server fault and gets a server's
		// status — a 401 here would send a bot author hunting a token that is not the problem.
		return NewProblem(http.StatusInternalServerError, CodeInternalError,
			"This instance cannot verify tokens right now.")

	case !hasFailure || failure.Credential != auth.CredentialToken:
		return NewProblem(http.StatusUnauthorized, CodeUnauthenticated,
			"This operation requires a credential: a session cookie, or "+
				"`Authorization: Bearer dkp_pat_…`.")
	}

	switch {
	case errors.Is(err, auth.ErrExpiredCredential):
		return withTokenMeta(NewProblem(http.StatusUnauthorized, CodeTokenExpired,
			"This token has expired. Mint a new one and tell whoever owns the bot."),
			failure, "expired_at")

	case errors.Is(err, auth.ErrRevokedCredential):
		return withTokenMeta(NewProblem(http.StatusUnauthorized, CodeTokenRevoked,
			"This token was revoked. Do not retry — a revoked token being retried looks like an attack."),
			failure, "revoked_at")

	case errors.Is(err, auth.ErrPrincipalNotActive):
		return withTokenMeta(NewProblem(http.StatusUnauthorized, CodeUnauthenticated,
			"The service account this token belongs to is not active."), failure, "")

	case errors.Is(err, auth.ErrUnknownCredential):
		return withTokenMeta(NewProblem(http.StatusUnauthorized, CodeTokenInvalid,
			"This token is not valid. Check what was pasted; do not retry."), failure, "")

	default:
		// A malformed bearer, or anything else this package has not enumerated. The catalogue's
		// `unauthenticated` row covers "no credential, or a malformed Authorization header", and an
		// unenumerated failure must not be more specific than the truth.
		return NewProblem(http.StatusUnauthorized, CodeUnauthenticated,
			"This operation requires a credential: a session cookie, or "+
				"`Authorization: Bearer dkp_pat_…`.")
	}
}

// withTokenMeta attaches the public facts about a failed token: its prefix, and the instant the
// reason names when there is one.
func withTokenMeta(problem *ProblemDetail, failure *auth.ResolutionError, instantKey string) *ProblemDetail {
	if failure == nil {
		return problem
	}

	meta := make(map[string]any, 2)

	if failure.TokenPrefix != "" {
		meta["token_prefix"] = failure.TokenPrefix
	}

	if instantKey != "" && failure.At != nil {
		meta[instantKey] = *failure.At
	}

	if len(meta) > 0 {
		problem.Meta = meta
	}

	return problem
}

// operationID is the operation's id, or a placeholder for a request that reached the middleware
// without one. It exists so a log line always has the field, rather than sometimes having it.
func operationID(op *huma.Operation) string {
	if op == nil || op.OperationID == "" {
		return "unknown"
	}

	return op.OperationID
}
