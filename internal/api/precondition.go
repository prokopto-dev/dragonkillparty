package api

import (
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
)

// The precondition half of the write-safety pair: the middleware that makes an operation's declared
// If-Match mean something (issue #290).
//
// WHAT WAS ALREADY TRUE AND WHY IT WAS NOT ENOUGH. PR 5a shipped the 428 and the 412 for one
// resource, and arch_test.go's TestArch_StateChangingOperation_RequiresIfMatch asserts that every
// PATCH, PUT and DELETE DECLARES an If-Match parameter. Neither half enforces the precondition: the
// gate reads a declaration, and the handler check is a line an author has to remember to write. This
// is what closes the gap — a declared If-Match is refused when absent BEFORE the handler runs, so
// forgetting the check is no longer a way to ship a route with a silent lost-update window.
//
// THE 412 STAYS IN THE DOMAIN, and that division is not arbitrary. Whether the caller's tag matches
// is a question only the resource can answer, under the same transaction as the write; whether an
// absent tag is an error is a fact about the route. So this middleware owns 428 and nothing else,
// and `meta.current` keeps arriving from the service that read the current row (etag.go,
// internal/guild).
//
// IT RUNS AFTER AUTHORIZATION AND BEFORE IDEMPOTENCY. After, because a caller who may not perform
// the operation at all must get the 403 that says so rather than a 428 telling them to fetch an
// ETag they will not be allowed to use. Before, because a request refused for a missing precondition
// must not consume the caller's Idempotency-Key — burning a key on a request that never executed
// would make the retry that fixes the precondition replay the 428 forever.

// headerIfMatch is the precondition header, spelled once. RFC 9110 §13.1.1.
const headerIfMatch = "If-Match"

// headerParameter reports whether op declares a header parameter called name, and whether that
// parameter is required.
//
// IT IS THE ONE READER OF A DECLARATION, shared by the middleware that enforces it and by the
// architectural gates that assert it exists (arch_test.go). That sharing is the point rather than
// tidiness: a gate that reads the declaration one way and a middleware that reads it another is a
// gate which passes while the thing it describes is not enforced — which is exactly the vacuity
// issue #290 exists to end.
//
// The comparison is case-insensitive because HTTP field names are (RFC 9110 §5.1), and `In` is
// matched exactly because "header" and "query" are OpenAPI keywords rather than field names — a
// parameter named If-Match in the query string is not a precondition, and treating it as one would
// let a route satisfy the gate with something no client sends.
func headerParameter(op *huma.Operation, name string) (declared, required bool) {
	if op == nil {
		return false, false
	}

	for _, param := range op.Parameters {
		if param == nil || param.In != "header" || !strings.EqualFold(param.Name, name) {
			continue
		}

		declared = true
		required = param.Required
	}

	return declared, required
}

// preconditionMiddleware refuses a request to an operation that declares If-Match and did not send
// one, with the 428 canonical §7 requires.
//
// The parameter is declared OPTIONAL on the input struct and the presence check lives here — see
// requireIfMatch in etag.go for why a `required:"true"` tag would answer 422 instead of 428 under
// Huma v2.39.1. That is also why this cannot be left to Huma's own validation: Huma has one status
// for a missing required parameter and it is the wrong one.
func preconditionMiddleware(humaAPI huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()

		if declared, _ := headerParameter(op, headerIfMatch); !declared {
			next(ctx)

			return
		}

		// requireIfMatch is the SAME function the handlers call, so the middleware and a handler that
		// still checks for itself cannot answer a missing precondition differently. Defence in depth
		// with one implementation, rather than two implementations that agree today.
		if _, problem := requireIfMatch(ctx.Header(headerIfMatch)); problem != nil {
			middleware.Logger(ctx.Context()).WarnContext(ctx.Context(), "precondition required",
				"operation", operationID(op),
				"status", problem.Status,
				"code", string(problem.Code))

			// Waived for the reason .claude/rules/go-idioms.md permits: by the time huma reports a
			// write error the response is committed and there is nothing left to say to this caller.
			_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)

			return
		}

		next(ctx)
	}
}
