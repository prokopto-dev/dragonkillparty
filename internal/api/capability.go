package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
)

// The capability half of the choke point, called from inside principalMiddleware — NOT from a second
// middleware, and that is the point rather than a tidiness choice (issue #276, item 4). The middleware
// already holds the resolved Principal and the huma.Operation; a second middleware would have to
// recover the first from the context and the second from the registry, which is two places that can
// disagree about whether a request was authenticated. One pass, one decision.
//
// WHAT RUNS HERE, IN ORDER, is authz.Check's contract and this file only renders it: credential class,
// then scopes, then the permission the principal's roles grant, then step-up. The evaluation is
// internal/authz's because it is the product's security model; this file is the translation between a
// huma.Operation and a Requirement, and between a Decision and the published error catalogue.

// authorize runs the capability check and reports whether the request may continue to the handler.
//
// It writes the refusal itself when it returns false, for the same reason refuse() does: the caller is
// a middleware whose only remaining move is to not call next, and splitting "decide" from "write"
// across that boundary is how a refused request ends up answering an empty 200.
func authorize(
	humaAPI huma.API,
	ctx huma.Context,
	op *huma.Operation,
	checker *authz.Checker,
	principal *auth.Principal,
) bool {
	// A public operation needs no capability: `public` means no credential at all, so there is nothing
	// to intersect. needsAuthorization is the same predicate the boot-state gate uses, deliberately —
	// two answers to "does this operation need authorization" is one answer too many.
	if !needsAuthorization(op) {
		return true
	}

	if op == nil {
		// Unreachable through Huma, which always sets the operation, and written closed anyway: the
		// default branch of a security control is the one place a future edit must not be able to open
		// by omission. 503 rather than 403 because a request with no operation is this server failing to
		// route, not a caller failing to qualify.
		return refuseUndecidable(humaAPI, ctx, op, errors.New("no operation in context"))
	}

	if checker == nil {
		return refuseUndecidable(humaAPI, ctx, op, authz.ErrNoChecker)
	}

	requirement, declared := requirementFor(op)
	if !declared {
		// AN OPERATION WITH NO x-dkp-permission IS REFUSED, not treated as `self`. The two produce the
		// same empty string and only one of them is a decision somebody made: `self` is a declaration
		// that any authenticated principal may act on their own records, and a missing extension is a
		// route that forgot to say anything. arch_test.go's permission coverage makes the second
		// unregisterable, so this is unreachable through the product — and it is written closed anyway,
		// because the default branch of a security control is the one place a future edit must not be
		// able to open by omission.
		return refuseUndecidable(humaAPI, ctx, op, errNoPermissionDeclared)
	}

	decision, err := checker.Check(ctx.Context(), principal, requirement)
	if err != nil {
		return refuseUndecidable(humaAPI, ctx, op, err)
	}

	if decision.Allowed() {
		return true
	}

	return refuseCapability(humaAPI, ctx, op, principal, decision)
}

// errNoPermissionDeclared is the fault behind a refusal of an operation that declares no
// x-dkp-permission. A sentinel rather than an inline errors.New so the log line reads the same every
// time and a grep finds every occurrence.
var errNoPermissionDeclared = errors.New("operation declares no x-dkp-permission")

// requirementFor translates one registered operation into the vocabulary authz.Check evaluates, and
// reports whether the operation declared a permission at all.
//
// THREE FIELDS, AND EACH COMES FROM THE DECLARATION A REVIEWER READS BESIDE THE ROUTE — never from a
// list kept somewhere else. What is deliberately NOT here is the capability floor: authz.Check reads
// that from authz.CapabilityFloor() rather than from this operation's x-dkp-pat-forbidden extension,
// so an operation that forgot the extension is still refused to a token. A control that can be
// disabled by a line somebody did not write is the silent-permissive failure 03-security.md §4.6 names.
//
// THE BOOLEAN SEPARATES "self" FROM "NOTHING", which is the whole reason it exists. Both end up as an
// empty Permission — `self` because there is no catalogue key to look up, a missing extension because
// there is nothing there — and the caller must not treat them alike: one is a decision, the other is
// an omission.
func requirementFor(op *huma.Operation) (authz.Requirement, bool) {
	permission, declared := op.Extensions[ExtensionPermission].(string)
	if !declared || permission == "" {
		return authz.Requirement{}, false
	}

	// `self` is internal/api's sentinel, not a catalogue key (permissions.go): "any authenticated
	// principal, constrained to its own records". It is translated to an empty permission here, at the
	// one place that knows the sentinels exist, so internal/authz never learns a vocabulary that is
	// this package's. The scope and step-up rules still apply — a `self` operation reachable by a token
	// still names the scopes that reach it.
	if permission == PermissionSelf {
		permission = ""
	}

	stepUp, _ := op.Extensions[ExtensionStepUp].(bool)

	return authz.Requirement{
		Permission: permission,
		ScopeSets:  patScopeSets(op),
		StepUp:     stepUp,
		// Target is the zero value — global — for every operation registered today. An operation whose
		// path names a pool or a raid group passes one when scoped role assignments get their first
		// route; the schema and the query already honour them.
	}, true
}

// patScopeSets returns the scope sets a token may satisfy: one entry per `pat` alternative in the
// operation's Security, holding every scope that alternative names.
//
// IT READS `Security`, NOT `x-dkp-scopes`, and the difference is structural rather than stylistic.
// OpenAPI's `security` is a list of ALTERNATIVES whose scopes are CONJUNCTIVE within one entry, so
// `[{"pat": ["bids:manage", "loot:award"]}]` requires both — which is exactly what settling a bid
// session means (docs/api/auth-and-scopes.md, "Two deliberate couplings": running an auction and
// moving money are different powers). A flat any-of list would hand a token holding either one both.
// x-dkp-scopes is the flat published vocabulary for readers and for the arch gate, and
// TestArch_ScopeCoverage_MatchesSecurity asserts it is exactly the union of what this returns — so
// the thing the middleware enforces and the thing the document promises cannot drift apart.
func patScopeSets(op *huma.Operation) [][]string {
	var sets [][]string

	for _, requirement := range op.Security {
		scopes, ok := requirement[SchemePAT]
		if !ok {
			continue
		}

		sets = append(sets, scopes)
	}

	return sets
}

// refuseCapability writes the 403 the decision names and logs WHY, on the same split refuse() uses:
// the caller gets the code docs/api/errors.md promises and the `meta` that makes it actionable; the
// log gets the principal, the operation and the outcome, which is what answers "who tried what" during
// an incident.
//
// WARN, NOT ERROR. A 403 is a caller who does not hold something — a member hitting an officer route,
// a bot whose token was minted too narrow — and a wall of them at ERROR level teaches an operator to
// filter out the level that also carries the server's own faults.
func refuseCapability(
	humaAPI huma.API,
	ctx huma.Context,
	op *huma.Operation,
	principal *auth.Principal,
	decision authz.Decision,
) bool {
	problem := problemForDecision(decision)

	middleware.Logger(ctx.Context()).WarnContext(ctx.Context(), "capability refused",
		"operation", operationID(op),
		"outcome", decision.Outcome.String(),
		"required_permission", decision.RequiredPermission,
		// The principal itself, not a hand-picked field list: Principal implements slog.LogValuer
		// precisely so a future field that holds a secret is invisible here until somebody decides it
		// should not be (internal/auth/principal.go).
		"principal", principal)

	// Waived for the reason .claude/rules/go-idioms.md permits: by the time huma reports a write error
	// the response is committed and there is nothing left to say to this caller. huma logs it itself.
	_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)

	return false
}

// refuseUndecidable writes the 503 for a check that could not be MADE, as distinct from one that
// refused. No store, no live permission row, no principal on a path that requires one: the caller has
// done nothing wrong and no credential they could present would help, so 403 would be a lie that sends
// a bot author hunting a scope and a member to a login screen.
//
// ERROR level, and the reason is in the log rather than on the wire: every one of these is a wiring
// bug, a database that changed under a running process, or a boot that half-completed, and each is
// something an operator must see. The caller gets undecidableDetail below — a fixed sentence naming
// no cause, because the cause is a database message or an internal invariant and this response is
// reached by whoever asked.
func refuseUndecidable(humaAPI huma.API, ctx huma.Context, op *huma.Operation, err error) bool {
	middleware.Logger(ctx.Context()).ErrorContext(ctx.Context(),
		"cannot decide authorization; refusing the operation",
		"operation", operationID(op),
		slog.Any("error", err))

	problem := NewProblem(http.StatusServiceUnavailable, CodeServiceUnavailable, undecidableDetail)

	_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)

	return false
}

// undecidableDetail is the sentence a caller gets when the capability check could not be made.
//
// A SEPARATE SENTENCE FROM unavailableDetail, which authorizationGate uses, because the two are
// different events and the operator-facing advice differs. That one is a BOOT failure — the
// catalogue never reconciled — and /readyz reports it, so its text says so. This one is a request-time
// fault: a database that went away, a permission row that vanished under a running process, a wiring
// bug. /readyz may well be green. Reusing the boot sentence here would send whoever is debugging it
// to a readiness check that says everything is fine.
//
// IT NAMES NO CAUSE, like every other refusal on this path: the reason is a database message or an
// internal invariant, and this response is reached by whoever asked. The operator's copy is the log
// line above, which carries the error in full.
const undecidableDetail = "This server could not determine whether you are permitted to perform " +
	"this operation, so it refused. Your credential is not the problem; retry shortly. The process " +
	"log names the fault."

// problemForDecision maps a refusal onto the published catalogue (docs/api/errors.md §"Authentication
// and authorization"). All four are 403: the caller is authenticated, and what they lack is capability
// rather than identity.
//
// THE SWITCH IS TOTAL AND ITS DEFAULT IS THE SAFE ANSWER.
// TestProblem_ForDecision_CoversEveryOutcome walks every authz.Outcome and asserts each maps to the
// code the catalogue publishes, so an outcome added in internal/authz without a code here is a red
// test rather than a request that falls through to `permission_denied` and tells a caller something
// untrue about why.
func problemForDecision(decision authz.Decision) *ProblemDetail {
	switch decision.Outcome {
	case authz.OutcomeSessionRequired:
		return NewProblem(http.StatusForbidden, CodeSessionRequired,
			"This operation cannot be performed with a token. It alters authentication, authorization "+
				"or bulk-export state, or no token scope reaches it, so it requires a browser session — "+
				"by design, and there is no token that would work. Ask a human to do it in the browser.")

	case authz.OutcomeStepUpRequired:
		// NO meta.step_up_url, and its absence is deliberate rather than forgotten. docs/api/errors.md
		// promises the field; there is nowhere to point it, because there is no login route, no
		// re-authentication route and no MFA challenge yet (#264, and MFA enrolment is Wave 2). A
		// plausible-looking path in a published contract is a path a client would follow to a 404
		// while holding a session it believes it can rescue. Issue #287 carries it, and either the
		// field arrives with the step-up flow or that row of the catalogue is corrected.
		return NewProblem(http.StatusForbidden, CodeStepUpRequired,
			"This operation requires a recent re-authentication. Sign in again to confirm it is you, "+
				"then retry within five minutes.")

	case authz.OutcomeInsufficientScope:
		problem := NewProblem(http.StatusForbidden, CodeInsufficientScope,
			"This token was minted without a scope that reaches this operation. Mint a new token "+
				"carrying the scopes named in meta.required_scopes.")
		problem.Meta = map[string]any{
			"required_scopes": decision.RequiredScopes,
			// The bearer already knows what it holds, so naming it discloses nothing and turns a refusal
			// into a diff a bot author can read at a glance.
			"token_scopes": decision.TokenScopes,
		}

		return problem

	case authz.OutcomePermissionDenied:
		problem := NewProblem(http.StatusForbidden, CodePermissionDenied,
			"Your roles do not grant the permission this operation requires. Effective capability is "+
				"role permissions intersected with token scopes; an administrator has to widen the role.")
		if decision.RequiredPermission != "" {
			problem.Meta = map[string]any{"required_permission": decision.RequiredPermission}
		}

		return problem

	case authz.OutcomeAllowed:
		// Unreachable: refuseCapability is called only when Allowed() is false. Written as the closed
		// answer rather than omitted, because a permissive default in this switch would turn a future
		// mistake into an authorization bypass instead of a wrong error message.
		fallthrough

	default:
		return NewProblem(http.StatusForbidden, CodePermissionDenied,
			"This operation is not permitted for the credential you presented.")
	}
}
