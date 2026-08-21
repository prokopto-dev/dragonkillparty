package authz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The capability half of the choke point (docs/design/03-security.md §4). internal/auth answers "who
// is this"; this file answers "may they", and internal/api calls it once, in the one middleware,
// before the handler is entered.
//
// EFFECTIVE CAPABILITY = ROLE PERMISSIONS ∩ TOKEN SCOPES (canonical §6, 03-security.md §4.2). The two
// halves are read from two places because they are two different facts: the role permissions come
// from the database, through role_assignment → role_permission → permission, and the scopes come
// from the credential the request presented. A token can only ever NARROW what its service account's
// role already grants — there is no `admin:*` scope, no all-powerful token, and no way for a scope to
// add capability a role does not have (ADR-0011).
//
// THE SCOPE HALF IS EVALUATED AGAINST THE OPERATION, not against a scope→permission table, and that
// is the design rather than a shortcut. Every PAT-callable operation already declares BOTH the
// permission it needs and the scopes that reach it, so the operation IS the mapping — declared once,
// beside the route, where a reviewer reads it. A global scope→permission table would be a second
// hand-maintained list of exactly the kind canonical §6 exists to forbid, and it would have to agree
// with the per-operation declarations anyway.
//
// THERE IS NO SUPERADMIN BRANCH IN THIS FILE, and its absence is asserted rather than trusted.
// `admin.owner` is a permission row granted to a role like any other (03-security.md §4.3), evaluated
// by the same statement as `roster.read`. EQdkp Plus's "group id 2 short-circuits the ACL" is a named
// anti-pattern, and TestCheck_HasNoSuperadminBranch reads this source to prove the pattern was not
// reintroduced.

// StepUpWindow is how recently a session must have re-authenticated to satisfy a step-up
// requirement: five minutes (03-security.md §3.4, canonical §6).
//
// It is measured from Principal.SteppedUpAt, which internal/auth resolves from
// session.mfa_satisfied_at. A TOKEN HAS NONE, ever, which is the mechanical reason the capability
// floor is session-only: there is no field on a bearer credential that could hold a re-authentication
// instant, so "session + step-up" is one requirement rather than two.
const StepUpWindow = 5 * time.Minute

// Sentinel errors. A Check that returns one of these could not DECIDE — as distinct from deciding to
// refuse — and the caller answers 503 rather than 403: the caller has done nothing wrong and no
// credential they could present would help.
var (
	// ErrNoChecker is returned when the checker has no store. cmd/dkp enters that degraded state
	// deliberately when the database cannot be opened (canonical §13 keeps /healthz green), and a
	// checker that cannot read role assignments must refuse rather than allow.
	ErrNoChecker = errors.New("authz checker has no database")

	// ErrUnknownPermission is returned when the operation's permission key has no LIVE row: either no
	// row at all, or one stamped orphaned_at because the running binary stopped shipping the key. Boot
	// reconciliation refuses to start in either state (see requireKeys), so reaching it at request
	// time means the database changed under a running process.
	ErrUnknownPermission = errors.New("permission key has no live row")

	// ErrNoPrincipal is returned when Check is called without a principal. It is a wiring bug rather
	// than an anonymous request: the middleware refuses an operation that declares Security before
	// reaching here, so a nil principal means somebody called the capability check on a path the
	// authentication check did not cover.
	ErrNoPrincipal = errors.New("authorization check has no principal")
)

// Outcome is what a Check decided.
//
// AN INT ENUM, NOT THE WIRE STRINGS. The four refusals map one-to-one onto published error codes in
// internal/api's closed enum (docs/api/errors.md §"Authentication and authorization"), and this
// package must not hold a second copy of that vocabulary — internal/api owns it, and internal/authz
// cannot import internal/api. internal/api maps these to codes in one switch, and
// TestProblem_ForDecision_CoversEveryOutcome asserts the mapping is total, so a new outcome added
// here without a code there is a red test rather than a 500.
type Outcome int

const (
	// OutcomeAllowed is the only outcome that lets a request reach a handler.
	OutcomeAllowed Outcome = iota

	// OutcomeSessionRequired: a token was presented to an operation no token may reach. Either the
	// permission is in the capability floor, or the operation offers no `pat` alternative at all.
	OutcomeSessionRequired

	// OutcomeInsufficientScope: the token is valid and its account may well hold the permission, but
	// the token itself was minted without a scope that reaches this operation.
	OutcomeInsufficientScope

	// OutcomePermissionDenied: the credential class is fine and the scopes are fine; the principal's
	// roles do not grant the permission the operation declares.
	OutcomePermissionDenied

	// OutcomeStepUpRequired: a session exists, holds the permission, and has not re-authenticated
	// within StepUpWindow.
	OutcomeStepUpRequired
)

// String renders an outcome for a log line. Deliberately NOT the wire code — see Outcome's comment.
func (o Outcome) String() string {
	switch o {
	case OutcomeAllowed:
		return "allowed"
	case OutcomeSessionRequired:
		return "session_required"
	case OutcomeInsufficientScope:
		return "insufficient_scope"
	case OutcomePermissionDenied:
		return "permission_denied"
	case OutcomeStepUpRequired:
		return "step_up_required"
	default:
		return "unknown"
	}
}

// Target is the resource an operation acts on, for a role assignment that is scoped rather than
// global (`role_assignment.scope_type` / `scope_id`).
//
// THE ZERO VALUE IS GLOBAL, which is what every operation registered today is: a global assignment
// reaches everything, a scoped one reaches only its own target, and the schema's
// role_assignment_scope_shape CHECK makes "scope_id is NULL exactly when the scope is global" an
// equivalence rather than a convention. A raid-leader scoped to one raid group is the case this
// exists for; the endpoints that carry a pool or a raid-group id pass one.
type Target struct {
	// Type is one of internal/authz/roleassignment/kinds' scope kinds. Empty means global.
	Type string

	// ID is the pool or raid-group the scope names. Empty when the scope is global.
	ID core.ULID
}

// normalise returns the pair the query binds: a scope kind that is never empty, and a scope id that
// is nil exactly when the scope is global.
func (t Target) normalise() (string, *string) {
	if t.Type == "" || t.Type == assignmentkinds.ScopeGlobal || t.ID == "" {
		return assignmentkinds.ScopeGlobal, nil
	}

	id := t.ID.String()

	return t.Type, &id
}

// Requirement is what one operation demands, in the vocabulary this package can evaluate.
//
// internal/api builds it from the huma.Operation — the permission extension, the `pat` entries of
// Security, and the step-up extension — so the declaration a reviewer reads beside the route is the
// declaration the middleware enforces. Nothing here is a second list.
type Requirement struct {
	// Permission is the operation's x-dkp-permission: a catalogue key, or EMPTY.
	//
	// EMPTY IS THE `self` CASE and is not a hole. docs/design/02-api-design.md §4.1 defines `self` as
	// "any authenticated principal, constrained to its own records" — the constraint is the handler's,
	// over the rows it returns, and there is no catalogue key to look up. The sentinel vocabulary is
	// internal/api's (permissions.go), so it is translated there rather than learned here; `public`
	// never reaches Check at all, because a public operation needs no principal.
	Permission string

	// ScopeSets are the PAT scope sets that reach this operation: ONE ENTRY PER `pat` ALTERNATIVE in
	// the operation's Security, each holding every scope that alternative names.
	//
	// A TOKEN SATISFIES ALL OF ANY ONE SET, which is OpenAPI's own semantics for a security
	// requirement — the objects in `security` are alternatives, and the scopes within one object are
	// conjunctive. It matters: settling a bid session requires `bids:manage` AND `loot:award`
	// (docs/api/auth-and-scopes.md, "Two deliberate couplings"), because running an auction and moving
	// money are different powers, and a flat any-of list would hand either one both.
	//
	// EMPTY MEANS NO TOKEN MAY REACH THIS OPERATION. That covers both session-only cases: the
	// capability floor, and an operation that is session-only merely because no scope family covers it
	// (canonical §6 — `admin.settings`, `ops.read`). The two get the same answer on the wire and are
	// distinguished only in the log.
	ScopeSets [][]string

	// StepUp is the operation's x-dkp-stepup extension: this operation requires a recent
	// re-authentication even though its permission is not in the capability floor.
	//
	// IT IS ADDITIVE, NEVER SUBTRACTIVE. Every floor key requires step-up from DATA — the permission
	// row's requires_step_up column, which Reconcile writes from the catalogue on every boot — so an
	// operation cannot opt OUT of step-up by omitting the extension. This field exists for the
	// operations 03-security.md §3.4 lists that are NOT in the floor: reversing a ledger batch older
	// than thirty days, changing another user's credentials, disabling MFA.
	StepUp bool

	// Target is the resource a scoped role assignment must name. The zero value is global.
	Target Target
}

// Decision is what Check decided, and everything the caller needs to render the refusal.
//
// The three slices are what docs/api/errors.md promises in `meta`: `required_permission` on a
// permission_denied, `required_scopes` and `token_scopes` on an insufficient_scope. A bot author is
// told exactly what is missing, because the alternative — "403, work it out" — is the EQdkp support
// burden this catalogue exists to end.
type Decision struct {
	// Outcome is the verdict.
	Outcome Outcome

	// RequiredPermission is the catalogue key the operation declared, empty for a `self` operation.
	RequiredPermission string

	// RequiredScopes is the union of every scope set that would have reached this operation, in
	// declaration order, set on OutcomeInsufficientScope. Any one complete set suffices.
	RequiredScopes []string

	// TokenScopes is what the presented token actually carries, set on OutcomeInsufficientScope. It is
	// public information: the bearer already knows it, and naming both halves is what turns a 403 into
	// a fix rather than a guess.
	TokenScopes []string
}

// Allowed reports whether the request may proceed to the handler.
func (d Decision) Allowed() bool { return d.Outcome == OutcomeAllowed }

// Checker evaluates capability against the database.
//
// It holds a store and a clock and nothing else (.claude/rules/go-idioms.md): no ctx field, no cached
// permission set, no mutable state. Nothing is memoised, deliberately — a role revoked through the
// role editor takes effect on the next request, and a cache is how "reducing the owner's role
// immediately reduces every token they minted" (docs/api/auth-and-scopes.md) stops being true.
type Checker struct {
	store *store.Store
	clock clock.Clock
}

// NewChecker builds a Checker.
//
// A nil clock is a programming error the caller fixes, not a default this package invents — the same
// rule NewReconciler follows. A nil store is NOT a programming error: it is the degraded boot state
// cmd/dkp enters when the database is unusable, and Check answers it with ErrNoChecker, which the
// caller renders as 503.
func NewChecker(st *store.Store, clk clock.Clock) *Checker {
	return &Checker{store: st, clock: clk}
}

// Check decides whether principal may perform the operation req describes.
//
// THE ORDER OF THE FOUR REFUSALS IS A DESIGN DECISION, not an implementation detail, because each
// tells the caller something different and the first one that fires is the one they act on:
//
//  1. CREDENTIAL CLASS FIRST. A token presented to a session-only operation is refused before any
//     database read, because the answer cannot depend on what the token's account may do: "a PAT —
//     any PAT, regardless of scopes — can never do the following" (03-security.md §6.4). Reading the
//     roles first would let a well-scoped token's refusal depend on a role edit, which is exactly the
//     variability the floor exists to remove.
//
//  2. SCOPE BEFORE PERMISSION. docs/api/errors.md defines the two codes against each other:
//     `insufficient_scope` is "the token lacks a required scope", and `permission_denied` is "scope is
//     fine; the principal's role does not hold the permission". Checking scopes first is what makes
//     that sentence true, and it is also the cheaper half — a scope check is a slice comparison and a
//     permission check is a query.
//
//  3. PERMISSION BEFORE STEP-UP. A member who does not hold `admin.roles.manage` gets
//     `permission_denied`, not `step_up_required`: telling them to re-authenticate would send them
//     through an MFA prompt to arrive at the same refusal. `step_up_required` is only ever returned to
//     a principal who WOULD be allowed after re-authenticating, which is what makes it actionable.
//
//  4. STEP-UP LAST, from the permission row's own column plus the operation's declaration.
//
// A DECISION AND AN ERROR ARE DIFFERENT ANSWERS. A Decision is authorization working: this principal
// may or may not do this thing. An error is authorization being UNABLE to decide — no store, no live
// permission row, no principal — and the caller answers 503 rather than 403, because the caller did
// nothing wrong and no credential would help.
func (c *Checker) Check(ctx context.Context, principal *auth.Principal, req Requirement) (Decision, error) {
	decision := Decision{RequiredPermission: req.Permission}

	if principal == nil {
		return decision, fmt.Errorf("check %s: %w", req.Permission, ErrNoPrincipal)
	}

	if c.store == nil {
		return decision, fmt.Errorf("check %s: %w", req.Permission, ErrNoChecker)
	}

	// 1 and 2: what the CREDENTIAL may reach, before anything is read.
	if principal.Credential == auth.CredentialToken {
		if outcome, ok := c.checkToken(principal, req, &decision); !ok {
			decision.Outcome = outcome

			return decision, nil
		}
	}

	// 3: what the SUBJECT holds. A `self` operation has no catalogue key to look up: the constraint is
	// the handler's, over its own rows, and every credential-class rule above still applied.
	stepUp := req.StepUp

	if req.Permission != "" {
		row, err := c.store.Q().EffectivePermission(ctx, sqlitegen.EffectivePermissionParams{
			SubjectKind:   principal.Kind,
			SubjectID:     principal.ID.String(),
			Now:           c.clock.Now().UnixMicro(),
			ScopeType:     scopeTypeOf(req.Target),
			ScopeID:       scopeIDOf(req.Target),
			PermissionKey: req.Permission,
		})

		switch {
		case errors.Is(err, sql.ErrNoRows):
			return decision, fmt.Errorf("check %s: %w", req.Permission, ErrUnknownPermission)
		case err != nil:
			return decision, fmt.Errorf("read effective permission %s: %w", req.Permission, err)
		}

		if row.Granted == 0 {
			decision.Outcome = OutcomePermissionDenied

			return decision, nil
		}

		// The COLUMN, not the catalogue in memory. Reconcile writes it from Catalogue() on every boot
		// and requireKeys refuses to start when a declared key is missing or orphaned, so the two agree
		// by construction — and when they somehow do not, the database an officer can inspect is the one
		// that decides (docs/design/01-domain-model.md §5).
		stepUp = stepUp || row.RequiresStepUp != 0
	}

	// 4: step-up. A token has no SteppedUpAt and never can, so a token that reaches here on a §3.4
	// step-up operation outside the floor is refused too — correctly: re-authentication is a session
	// act, and an operation requiring one is beyond a bearer credential by construction.
	if stepUp && !c.steppedUpRecently(principal) {
		decision.Outcome = OutcomeStepUpRequired

		return decision, nil
	}

	decision.Outcome = OutcomeAllowed

	return decision, nil
}

// checkToken applies the two rules that depend only on the credential being a token. It reports the
// refusing outcome and false, or OutcomeAllowed and true when the token clears both.
//
// THE FLOOR IS READ FROM CapabilityFloor(), not from the operation's x-dkp-pat-forbidden extension,
// and that is the fail-closed direction. An operation that forgot the extension would otherwise be
// PAT-reachable because of a line that is NOT there — the silent-permissive failure mode
// 03-security.md §4.6 names as the reason authorization is the highest-value test suite in the
// product. The extension stays on the published document, where a bot author reads it, and
// TestArch_ScopeCoverage_MatchesSecurity asserts it equals this set.
func (c *Checker) checkToken(principal *auth.Principal, req Requirement, decision *Decision) (Outcome, bool) {
	if inCapabilityFloor(req.Permission) {
		return OutcomeSessionRequired, false
	}

	if len(req.ScopeSets) == 0 {
		return OutcomeSessionRequired, false
	}

	for _, set := range req.ScopeSets {
		if satisfies(principal.Scopes, set) {
			return OutcomeAllowed, true
		}
	}

	decision.RequiredScopes = unionScopes(req.ScopeSets)
	decision.TokenScopes = slices.Clone(principal.Scopes)

	return OutcomeInsufficientScope, false
}

// steppedUpRecently reports whether the principal re-authenticated within StepUpWindow.
//
// A NIL SteppedUpAt IS "NEVER", which is what every token has and what a session has until it
// re-authenticates. The comparison is against the injected clock, so a step-up window is testable
// without sleeping.
func (c *Checker) steppedUpRecently(principal *auth.Principal) bool {
	if principal.SteppedUpAt == nil {
		return false
	}

	now := core.Micros(c.clock.Now().UnixMicro())

	// A step-up stamped in the future is not treated as valid forever: the window is an interval
	// around now in one direction, and clock skew that moves a stamp ahead must not extend it.
	elapsed := now.Sub(*principal.SteppedUpAt)

	return elapsed >= 0 && elapsed <= StepUpWindow
}

// satisfies reports whether held contains EVERY scope in want. An empty want is satisfied by
// anything, which is why Check refuses an empty ScopeSets before ever reaching here — an operation
// that named no scope would otherwise be reachable by a zero-scope token, which is the exact property
// ADR-0011 denies.
func satisfies(held, want []string) bool {
	if len(want) == 0 {
		return false
	}

	for _, scope := range want {
		if !slices.Contains(held, scope) {
			return false
		}
	}

	return true
}

// unionScopes flattens the alternatives into `meta.required_scopes`, in declaration order and without
// repeats. It is the union rather than one alternative because any complete set suffices, and naming
// only the first would tell a bot author to mint a scope they may not need.
func unionScopes(sets [][]string) []string {
	union := make([]string, 0, len(sets))

	for _, set := range sets {
		for _, scope := range set {
			if !slices.Contains(union, scope) {
				union = append(union, scope)
			}
		}
	}

	return union
}

// inCapabilityFloor reports whether key is one of the session-and-step-up-only permissions canonical
// §6 enumerates. CapabilityFloor() is the one authority; this is a lookup over it, never a copy.
func inCapabilityFloor(key string) bool {
	return key != "" && slices.Contains(CapabilityFloor(), key)
}

// scopeTypeOf and scopeIDOf bind the target pair. Two functions rather than a multi-value call at the
// binding site so the params literal stays one struct literal and reads as the query does.
func scopeTypeOf(t Target) string {
	kind, _ := t.normalise()

	return kind
}

func scopeIDOf(t Target) *string {
	_, id := t.normalise()

	return id
}
