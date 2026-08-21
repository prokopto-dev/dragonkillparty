package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestArch_MutatingOperations_RejectAZeroScopePAT is the second of the three architectural tests
// docs/design/03-security.md §4.1 names:
//
//  2. Every mutating operation rejects a zero-scope PAT with `403`. Enumerated from the registry,
//     never hand-written.
//
// IT ENUMERATES THE REGISTRY, so it covers a route added tomorrow by somebody who never read this
// file — which is the whole reason §4.6 calls the authorization matrix the highest-value test suite
// in the product. A hand-written list would cover exactly the routes its author remembered.
//
// THE ASSERTION IS STRONGER THAN "403", and deliberately. A zero-scope token could be refused for two
// quite different reasons: because its scopes reach nothing (the control), or because whichever
// subject the test happened to invent holds no roles (an accident that would also produce 403, and
// would keep producing it after somebody deleted the scope check). So this asserts the outcome is
// `session_required` or `insufficient_scope` — both decided from the credential ALONE, before any
// role is read — and never `permission_denied`, which would mean the answer came from the database
// rather than from the token being empty.
func TestArch_MutatingOperations_RejectAZeroScopePAT(t *testing.T) {
	t.Parallel()

	checker := newCapabilityChecker(t)

	// A token with NO scopes, belonging to a service account with no role assignments — the credential
	// ADR-0011 exists to deny, and the one Wave 0d could not refuse.
	zeroScope := &auth.Principal{
		Kind:        auditkinds.ActorServiceAccount,
		ID:          core.ULID("00000000000000000000000BOT"),
		Credential:  auth.CredentialToken,
		TokenPrefix: "00000000",
	}

	mutating := 0

	for _, op := range registeredOperations(t) {
		if !isMutating(op.Method) {
			continue
		}

		permission, _ := op.Op.Extensions[ExtensionPermission].(string)
		if permission == PermissionPublic {
			// A public operation takes no credential at all, so there is no zero-scope token case for
			// it. `public` is golden-file protected under CODEOWNERS (§4.1's third architectural test),
			// which is what keeps this exemption from becoming the hole.
			continue
		}

		mutating++

		requirement, declared := requirementFor(op.Op)
		require.Truef(t, declared, "%s declares no x-dkp-permission", op)

		decision, err := checker.Check(t.Context(), zeroScope, requirement)
		require.NoErrorf(t, err, "%s: the capability check could not be made", op)

		require.Falsef(t, decision.Allowed(),
			"%s admits a token minted with NO scopes. Effective capability is role permissions ∩ token "+
				"scopes (canonical §6), so an empty scope set intersects to nothing and this operation "+
				"must be unreachable.", op)

		require.Containsf(t,
			[]string{authz.OutcomeSessionRequired.String(), authz.OutcomeInsufficientScope.String()},
			decision.Outcome.String(),
			"%s refuses a zero-scope token, but for the wrong reason: %s is decided by reading role "+
				"assignments, so this route would still refuse a zero-scope token after the scope check "+
				"was removed. The refusal must come from the credential.", op, decision.Outcome)
	}

	require.NotZerof(t, mutating,
		"no mutating operation was examined, so this test asserted nothing. It passes vacuously the day "+
			"the registry loses its last PATCH — check registeredOperations and isMutating before "+
			"believing a green run here.")
}

// isMutating reports whether a method changes state. HEAD and GET do not; canonical §7 additionally
// forbids a GET that mutates, which arch_test.go asserts separately.
func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// newCapabilityChecker boots a real database with the catalogue reconciled. The zero-scope test never
// reaches a query — its refusals are decided from the credential — but the checker refuses to run
// without a store, and a fake one would be a fake this repository does not permit
// (.claude/rules/go-idioms.md: no mocks of the database).
func newCapabilityChecker(t *testing.T) *authz.Checker {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	authz.Boot(t, st, clk)

	return authz.NewChecker(st, clk)
}

// TestRequirementFor_ReadsTheDeclarationAndNothingElse pins the translation between a registered
// operation and what authz.Check evaluates. Each row is a property some other file relies on.
func TestRequirementFor_ReadsTheDeclarationAndNothingElse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		op             huma.Operation
		want           authz.Requirement
		wantUndeclared bool
	}{
		{
			name: "a PAT-callable operation carries its scopes as one alternative",
			op: huma.Operation{
				Security: []map[string][]string{{"pat": {"roster:read"}}, {"session": {}}},
				Extensions: map[string]any{
					ExtensionPermission: "roster.read",
					ExtensionScopes:     []string{"roster:read"},
				},
			},
			want: authz.Requirement{
				Permission: "roster.read",
				ScopeSets:  [][]string{{"roster:read"}},
			},
		},
		{
			name: "a session-only operation carries no scope sets, so no token can satisfy it",
			op: huma.Operation{
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{ExtensionPermission: "admin.settings"},
			},
			want: authz.Requirement{Permission: "admin.settings"},
		},
		{
			// The scopes within ONE alternative are conjunctive, which is OpenAPI's own semantics and
			// what makes "settle requires bids:manage AND loot:award" expressible at all.
			name: "one alternative naming two scopes stays one set",
			op: huma.Operation{
				Security:   []map[string][]string{{"pat": {"bids:manage", "loot:award"}}},
				Extensions: map[string]any{ExtensionPermission: "bid.manage"},
			},
			want: authz.Requirement{
				Permission: "bid.manage",
				ScopeSets:  [][]string{{"bids:manage", "loot:award"}},
			},
		},
		{
			name: "two pat alternatives stay two sets",
			op: huma.Operation{
				Security: []map[string][]string{
					{"pat": {"roster:read"}}, {"pat": {"roster:write"}}, {"session": {}},
				},
				Extensions: map[string]any{ExtensionPermission: "roster.read"},
			},
			want: authz.Requirement{
				Permission: "roster.read",
				ScopeSets:  [][]string{{"roster:read"}, {"roster:write"}},
			},
		},
		{
			// `self` is this package's sentinel, not a catalogue key, and internal/authz must never
			// learn it: an empty Permission is "no key to look up", which is what `self` means.
			name: "the self sentinel becomes an empty permission",
			op: huma.Operation{
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{ExtensionPermission: PermissionSelf},
			},
			want: authz.Requirement{},
		},
		{
			// A ROUTE THAT SAID NOTHING IS NOT A ROUTE THAT SAID `self`. Both produce an empty
			// Permission, and only one of them is a decision — so the caller is told which, and refuses
			// the omission with a 503 rather than serving it to any authenticated principal.
			name: "a missing x-dkp-permission is reported as undeclared",
			op: huma.Operation{
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{},
			},
			wantUndeclared: true,
		},
		{
			name: "an empty x-dkp-permission is reported as undeclared",
			op: huma.Operation{
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{ExtensionPermission: ""},
			},
			wantUndeclared: true,
		},
		{
			name: "x-dkp-stepup is carried through",
			op: huma.Operation{
				Security: []map[string][]string{{"session": {}}},
				Extensions: map[string]any{
					ExtensionPermission: "ledger.reverse",
					ExtensionStepUp:     true,
				},
			},
			want: authz.Requirement{Permission: "ledger.reverse", StepUp: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, declared := requirementFor(&tc.op)

			if tc.wantUndeclared {
				require.False(t, declared,
					"an operation with no x-dkp-permission must be reported as undeclared, so the "+
						"middleware can refuse it rather than treat it as the `self` sentinel")
				require.Equal(t, authz.Requirement{}, got)

				return
			}

			require.True(t, declared)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestProblem_ForDecision_CoversEveryOutcome walks every authz.Outcome and asserts each maps to the
// status and `code` docs/api/errors.md publishes.
//
// THE POINT IS TOTALITY. internal/authz owns the outcome vocabulary and cannot import this package's
// closed error enum, so the mapping is a switch — and a switch with a permissive default is how an
// outcome added over there becomes a request answered with the wrong reason over here. Adding a case
// to authz.Outcome without adding one below fails this test rather than shipping a caller a sentence
// that is not true about why they were refused.
func TestProblem_ForDecision_CoversEveryOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		outcome  authz.Outcome
		wantCode Code
	}{
		{outcome: authz.OutcomeSessionRequired, wantCode: CodeSessionRequired},
		{outcome: authz.OutcomeStepUpRequired, wantCode: CodeStepUpRequired},
		{outcome: authz.OutcomeInsufficientScope, wantCode: CodeInsufficientScope},
		{outcome: authz.OutcomePermissionDenied, wantCode: CodePermissionDenied},
	}

	// Every refusing outcome authz declares must appear above. The catalogue of outcomes is a
	// contiguous int enum starting at OutcomeAllowed, so walking it is walking the vocabulary — a new
	// member lands past the last one here and its String() is "unknown".
	for outcome := authz.OutcomeAllowed; outcome.String() != "unknown"; outcome++ {
		if outcome == authz.OutcomeAllowed {
			continue
		}

		found := false

		for _, tc := range tests {
			if tc.outcome == outcome {
				found = true
			}
		}

		require.Truef(t, found,
			"authz.Outcome %q has no row in this test and therefore no verified error code. Add both.",
			outcome)
	}

	for _, tc := range tests {
		t.Run(tc.outcome.String(), func(t *testing.T) {
			t.Parallel()

			problem := problemForDecision(authz.Decision{Outcome: tc.outcome})

			require.Equal(t, http.StatusForbidden, problem.Status,
				"every capability refusal is 403: the caller is authenticated and lacks capability, "+
					"which is a different answer from 401 and must not be conflated with it")
			require.Equal(t, tc.wantCode, problem.Code)
			require.NotEmpty(t, problem.Detail, "a refusal with no sentence is a refusal nobody can act on")
			require.Contains(t, string(problem.Type), string(tc.wantCode),
				"the type URI resolves to the page for this code")
		})
	}
}

// TestProblem_ForDecision_CarriesTheDocumentedMeta pins the `meta` keys docs/api/errors.md names for
// the two codes that carry one. They are what turns a 403 into a fix rather than a guess, and they are
// the reason the catalogue promises the message "always says exactly what is missing".
func TestProblem_ForDecision_CarriesTheDocumentedMeta(t *testing.T) {
	t.Parallel()

	scope := problemForDecision(authz.Decision{
		Outcome:        authz.OutcomeInsufficientScope,
		RequiredScopes: []string{"bids:manage", "loot:award"},
		TokenScopes:    []string{"bids:read"},
	})
	require.Equal(t, []string{"bids:manage", "loot:award"}, scope.Meta["required_scopes"])
	require.Equal(t, []string{"bids:read"}, scope.Meta["token_scopes"])

	denied := problemForDecision(authz.Decision{
		Outcome:            authz.OutcomePermissionDenied,
		RequiredPermission: "dkp.adjust",
	})
	require.Equal(t, "dkp.adjust", denied.Meta["required_permission"])

	// A `self` operation has no catalogue key, so there is nothing to name and the key is absent
	// rather than present and empty — an SDK reading meta.required_permission must not get "".
	selfDenied := problemForDecision(authz.Decision{Outcome: authz.OutcomePermissionDenied})
	require.NotContains(t, selfDenied.Meta, "required_permission")
}

// TestIsPublic_RequiresBothDeclarations is the truth table for the one predicate standing between an
// unauthenticated request and a handler.
//
// TestArch_PublicOperations_DeclareItBothWays makes the contradictory rows unregisterable, so these
// are the cases that CANNOT reach the middleware today — which is exactly why the predicate is worth
// pinning. A gate and the code it guards are not the same thing, and the day somebody relaxes the
// arch test (or adds a route through a path it does not walk), this is what keeps the closed answer
// closed.
func TestIsPublic_RequiresBothDeclarations(t *testing.T) {
	t.Parallel()

	empty := []map[string][]string{}
	credentialled := []map[string][]string{{"session": {}}}

	tests := []struct {
		name     string
		security []map[string][]string
		perm     string
		want     bool
	}{
		{
			name:     "empty Security and the public sentinel is the only public shape",
			security: empty, perm: PermissionPublic, want: true,
		},
		{
			// The hole this predicate closes. An anonymous request carries no principal, so the
			// capability check never runs — and `self` means "any AUTHENTICATED principal".
			name:     "empty Security with the self sentinel is not public",
			security: empty, perm: PermissionSelf, want: false,
		},
		{
			name:     "empty Security with a real catalogue key is not public",
			security: empty, perm: "roster.read", want: false,
		},
		{
			name:     "the public sentinel with a Security requirement is not public",
			security: credentialled, perm: PermissionPublic, want: false,
		},
		{
			name:     "an ordinary authenticated operation is not public",
			security: credentialled, perm: "roster.read", want: false,
		},
		{
			// OMITTED Security, which in OpenAPI inherits the document-level requirement rather than
			// waiving it. A route that simply forgot the field must not become public by omission.
			name:     "nil Security is not public whatever the permission says",
			security: nil, perm: PermissionPublic, want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			op := huma.Operation{
				Security:   tc.security,
				Extensions: map[string]any{ExtensionPermission: tc.perm},
			}

			require.Equal(t, tc.want, isPublic(&op))
		})
	}
}
