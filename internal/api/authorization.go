package api

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// unavailableDetail is the sentence a refused operation carries on the wire.
//
// FIXED TEXT, and never the underlying error. The reason reconciliation failed is a database
// message — a table name, a file path, a locking state — and this response is unauthenticated by
// construction, because refusing before authentication is what "fail closed" means. /readyz already
// owns the disclosure decision for the same fact and gates it behind DKP_READYZ_DETAIL (#74); the
// operator's copy is the boot log, which carries the error in full.
const unavailableDetail = "Authorization is unavailable. This server could not prepare its " +
	"permission catalogue at startup, so it refuses every operation that requires a permission. " +
	"GET /readyz reports the condition and the process log names the fault."

// AuthorizationState is what the boot path learned about this instance's ability to authorize a
// request at all: did the permission catalogue reconcile into the database before the listener
// opened?
//
// Declared HERE, by the consumer, for the same reason ReadyChecker is: internal/api must not import
// internal/authz's store-backed half — internal/api's own tests import internal/authz, so the cycle
// would be immediate — and cmd/dkp is the wiring point that holds both ends.
//
// THE ZERO VALUE IS UNAVAILABLE, and that is the design rather than a default nobody chose. A Config
// that does not mention this field describes a process that never reconciled anything, and a control
// whose unset state is open is not a control: the failure this exists to prevent is a code path that
// forgets to set it, which is exactly the path a zero value serves. Opening the gate therefore takes
// a call to AuthorizationReconciled by name, in a diff a reviewer reads.
type AuthorizationState struct {
	// reconciled is true only when a boot reconciliation completed. Unexported, so the open state
	// cannot be produced by a struct literal.
	reconciled bool

	// reason is why authorization is unavailable, in the words of the error that caused it. It is for
	// the operator — the boot log and, when DKP_READYZ_DETAIL allows it, the /readyz detail — and
	// never for a refused caller. Empty in the reconciled state and in the zero value.
	reason string
}

// AuthorizationReconciled reports that the permission catalogue reconciled and this instance may
// serve operations that require a permission.
func AuthorizationReconciled() AuthorizationState {
	return AuthorizationState{reconciled: true}
}

// AuthorizationUnavailable reports that reconciliation did not complete, with the reason for the
// operator.
//
// The reason is a courtesy to whoever has to fix it and never a condition: an empty one produces the
// same refusal, because a control that opened when its explanation was missing would be a control
// with a hole in exactly the shape of a caller who did not fill in a string.
func AuthorizationUnavailable(reason string) AuthorizationState {
	return AuthorizationState{reason: reason}
}

// Available reports whether this instance can authorize a request.
func (s AuthorizationState) Available() bool { return s.reconciled }

// Reason explains an unavailable state, for an operator-facing surface — the /readyz detail, which
// applies its own disclosure policy on top. It is empty when Available reports true.
//
// An unavailable state with no stated reason still answers with a sentence rather than an empty
// string: this value reaches a readiness body, and a readiness check whose detail is blank tells the
// operator less than the state alone did.
func (s AuthorizationState) Reason() string {
	switch {
	case s.reconciled:
		return ""
	case s.reason == "":
		return "the permission catalogue was never reconciled"
	default:
		return s.reason
	}
}

// authorizationGate refuses every operation that requires a permission while the authorization
// state is unavailable, and lets the public ones through.
//
// THIS IS THE FAIL-CLOSED HALF OF THE BOOT SPLIT (issue #272), and it is a middleware rather than a
// decision not to register the routes because the two answers say different things to a bot: an
// unregistered path is 404 — "there is no such operation" — and this instance HAS the operation and
// cannot serve it. 503 with a stable `code` is the honest answer, and it is one an SDK already
// discriminates on.
//
// WHAT STAYS UP is the point of doing it here rather than by refusing to boot: /healthz answers 200
// so Docker's HEALTHCHECK does not kill the container (canonical §13), /readyz says what is wrong,
// /config.json, the docs, the spec and the SPA are all served, and GET /api/v1/meta still identifies
// the build to whoever is debugging it. What is refused is every operation whose authorization this
// process cannot resolve.
//
// It must be installed BEFORE huma.Register runs for any operation: Huma captures the middleware
// chain at registration time (huma.go:881), so a middleware added afterwards is registered into
// nothing and silently never runs.
func authorizationGate(humaAPI huma.API, state AuthorizationState) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if state.Available() || !needsAuthorization(ctx.Operation()) {
			next(ctx)

			return
		}

		// NOT LOGGED PER REQUEST. The boot path already logged the fault once, at error level, with
		// the underlying error; a line per refused request adds nothing to that and a retrying bot
		// turns it into the log that buries it. WriteErr renders through huma.NewErrorWithContext, so
		// the body is the same ProblemDetail every other error uses, request_id and instance included.
		//
		// The error is waived rather than handled for the reason .claude/rules/go-idioms.md permits:
		// it means the response is already committed or the connection is gone, and Huma has logged
		// it. There is nothing left to write it to.
		_ = huma.WriteErr(humaAPI, ctx, http.StatusServiceUnavailable, unavailableDetail)
	}
}

// needsAuthorization reports whether an operation may be served by an instance that cannot authorize
// anybody.
//
// ONLY `public` IS LET THROUGH, and `self` deliberately is not. `self` means "any authenticated
// principal, constrained to its own records" (docs/design/02-api-design.md §4.1) — it still requires
// a principal, and resolving one runs through the same authorization state this gate exists because
// the process does not have. A carve-out for the sentinel that looks harmless is how a fail-closed
// control acquires the one open door nobody re-reads.
//
// An operation with no x-dkp-permission at all is refused too. arch_test.go makes that unregisterable
// — permission coverage is asserted over the whole registry — so this branch is unreachable through
// the product, and it is written the closed way because the default branch of a security control is
// the one place a future edit must not be able to open by omission.
func needsAuthorization(op *huma.Operation) bool {
	if op == nil {
		return true
	}

	key, ok := op.Extensions[ExtensionPermission].(string)
	if !ok {
		return true
	}

	return key != PermissionPublic
}
