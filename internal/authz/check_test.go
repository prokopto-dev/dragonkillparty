package authz_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// checkEpoch is the instant every fixture's clock is frozen at, so a step-up window is arithmetic
// rather than a sleep (.claude/rules/go-idioms.md bans time.Sleep in tests).
var checkEpoch = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// checkFixture is a booted instance: a migrated database with the catalogue reconciled and the nine
// built-in roles seeded, which is the state cmd/dkp reaches before its listener opens.
type checkFixture struct {
	store   *store.Store
	clock   *clock.Fake
	checker *authz.Checker
}

func newCheckFixture(t *testing.T) checkFixture {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(checkEpoch)

	authz.Boot(t, st, clk)

	return checkFixture{store: st, clock: clk, checker: authz.NewChecker(st, clk)}
}

// grant assigns a built-in role to a fresh subject and returns the subject id. The subject is a bare
// ULID rather than a real app_user row: role_assignment carries no foreign key to either subject
// table (SQLite has no polymorphic reference — db/schema.hcl says so), and the effective-permission
// query joins role, not app_user. A test that needed the account's STATE would go through
// internal/auth, which is what the middleware tests do.
func (f checkFixture) grant(t *testing.T, kind, roleID string) core.ULID {
	t.Helper()

	subject := core.NewGenerator(f.clock).MustNew()
	authz.GrantRole(t, f.store, f.clock, kind, subject, roleID)

	return subject
}

// session builds a session principal for subject.
func session(subject core.ULID, steppedUpAt *core.Micros) *auth.Principal {
	return &auth.Principal{
		Kind:        auditkinds.ActorUser,
		ID:          subject,
		Name:        "officer",
		Credential:  auth.CredentialSession,
		SessionID:   core.ULID("00000000000000000000000001"),
		SteppedUpAt: steppedUpAt,
	}
}

// bearer builds a service-account principal carrying scopes. A nil scopes slice is the ZERO-SCOPE
// TOKEN — the credential ADR-0011 is about, and the one every mutating operation must refuse.
func bearer(subject core.ULID, scopes ...string) *auth.Principal {
	return &auth.Principal{
		Kind:        auditkinds.ActorServiceAccount,
		ID:          subject,
		Name:        "raidbot",
		Credential:  auth.CredentialToken,
		TokenID:     core.ULID("00000000000000000000000002"),
		TokenPrefix: "abcd1234",
		Scopes:      scopes,
	}
}

// micros is a pointer to a Micros literal, for SteppedUpAt.
func micros(t time.Time) *core.Micros {
	m := core.FromTime(t)

	return &m
}

// TestCheck_TheMatrix walks effective capability across both credential classes.
//
// THIS IS THE AUTHORIZATION MATRIX IN MINIATURE — 03-security.md §4.6 calls the full one "the
// highest-value test suite in the product", because authorization is the only cross-cutting concern
// that fails SILENTLY PERMISSIVE: omit idempotency and a test fails; omit a capability check and
// everything works beautifully, for everyone. Every row below therefore names what would be true if
// the check were absent, not just what is expected.
func TestCheck_TheMatrix(t *testing.T) {
	t.Parallel()

	f := newCheckFixture(t)

	admin := f.grant(t, assignmentkinds.SubjectKindUser, authz.RoleIDAdmin)
	guest := f.grant(t, assignmentkinds.SubjectKindUser, authz.RoleIDGuest)
	owner := f.grant(t, assignmentkinds.SubjectKindUser, authz.RoleIDOwner)
	bot := f.grant(t, assignmentkinds.SubjectKindServiceAccount, authz.RoleIDBotReadonly)
	raidBot := f.grant(t, assignmentkinds.SubjectKindServiceAccount, authz.RoleIDBotRaid)
	ungranted := core.NewGenerator(f.clock).MustNew()

	readGuild := authz.Requirement{
		Permission: "roster.read",
		ScopeSets:  [][]string{{"roster:read"}},
	}

	// admin.settings is session-only BY OMISSION rather than by being in the floor (canonical §6): no
	// PAT scope family covers instance configuration, so the operation declares no scopes at all.
	settings := authz.Requirement{Permission: "admin.settings"}

	// token.mint IS in the floor. Session and step-up only, no scope, ever.
	mint := authz.Requirement{Permission: "token.mint"}

	// The conjunctive case: settling a bid session requires bids:manage AND loot:award, because
	// running an auction and moving money are different powers (docs/api/auth-and-scopes.md).
	settle := authz.Requirement{
		Permission: "bid.manage",
		ScopeSets:  [][]string{{"bids:manage", "loot:award"}},
	}

	tests := []struct {
		name      string
		principal *auth.Principal
		req       authz.Requirement
		want      authz.Outcome
	}{
		{
			name:      "a session whose role grants the key is allowed",
			principal: session(admin, nil),
			req:       readGuild,
			want:      authz.OutcomeAllowed,
		},
		{
			name:      "a session with no role assignment at all is denied",
			principal: session(ungranted, nil),
			req:       readGuild,
			want:      authz.OutcomePermissionDenied,
		},
		{
			name:      "a member's session cannot reach an officer key",
			principal: session(guest, nil),
			req:       settings,
			want:      authz.OutcomePermissionDenied,
		},
		{
			name:      "a scoped token whose account holds the key is allowed",
			principal: bearer(bot, "roster:read"),
			req:       readGuild,
			want:      authz.OutcomeAllowed,
		},
		{
			// THE HEADLINE ROW. Without the intersection this is a 200, and a token minted with no
			// scopes is a token with its service account's whole role — which is EQdkp Plus's api_key
			// with extra steps, the property ADR-0011 exists to deny.
			name:      "a ZERO-SCOPE token is denied on an operation its role could otherwise reach",
			principal: bearer(bot),
			req:       readGuild,
			want:      authz.OutcomeInsufficientScope,
		},
		{
			name:      "a token scoped to the wrong family is denied",
			principal: bearer(bot, "roster:write", "raids:read"),
			req:       readGuild,
			want:      authz.OutcomeInsufficientScope,
		},
		{
			name:      "a token holding one half of a conjunctive scope set is denied",
			principal: bearer(raidBot, "bids:manage"),
			req:       settle,
			want:      authz.OutcomeInsufficientScope,
		},
		{
			// The scopes are satisfied and the ROLE is not: bot_raid deliberately has no bid.manage.
			// This is the row that proves the intersection is an intersection rather than an either-or.
			name:      "a token holding both scopes is still bounded by its account's role",
			principal: bearer(raidBot, "bids:manage", "loot:award"),
			req:       settle,
			want:      authz.OutcomePermissionDenied,
		},
		{
			name:      "a token cannot reach a session-only-by-omission operation whatever it holds",
			principal: bearer(bot, "roster:read", "roster:write"),
			req:       settings,
			want:      authz.OutcomeSessionRequired,
		},
		{
			// "A PAT — any PAT, regardless of scopes — can never do the following" (03-security.md §6.4).
			// The subject here holds `owner`, which grants token.mint, and it still cannot.
			name:      "a token cannot reach a capability-floor operation even when its role grants it",
			principal: bearer(owner, "roster:read"),
			req:       mint,
			want:      authz.OutcomeSessionRequired,
		},
		{
			name:      "a session holding a floor key without a recent step-up is refused",
			principal: session(owner, nil),
			req:       mint,
			want:      authz.OutcomeStepUpRequired,
		},
		{
			name:      "a session that just re-authenticated may exercise a floor key",
			principal: session(owner, micros(checkEpoch)),
			req:       mint,
			want:      authz.OutcomeAllowed,
		},
		{
			name:      "a step-up exactly at the window boundary still counts",
			principal: session(owner, micros(checkEpoch.Add(-authz.StepUpWindow))),
			req:       mint,
			want:      authz.OutcomeAllowed,
		},
		{
			name:      "a step-up one second past the window does not",
			principal: session(owner, micros(checkEpoch.Add(-authz.StepUpWindow-time.Second))),
			req:       mint,
			want:      authz.OutcomeStepUpRequired,
		},
		{
			// PERMISSION BEFORE STEP-UP, and the outcome is the whole reason for that order: telling a
			// guest to re-authenticate would send them through an MFA prompt to arrive at the same
			// refusal. step_up_required is only ever returned to somebody it would actually help.
			name:      "a session that does not hold the floor key is denied, not asked to step up",
			principal: session(guest, nil),
			req:       mint,
			want:      authz.OutcomePermissionDenied,
		},
		{
			name:      "a `self` operation needs no catalogue key",
			principal: session(ungranted, nil),
			req:       authz.Requirement{},
			want:      authz.OutcomeAllowed,
		},
		{
			// A `self` operation that names no scope is still session-only: `self` narrows what a
			// principal may touch, it does not widen which credentials may reach the route.
			name:      "a `self` operation with no scopes still refuses a token",
			principal: bearer(bot, "roster:read"),
			req:       authz.Requirement{},
			want:      authz.OutcomeSessionRequired,
		},
		{
			name:      "an operation declaring step-up outside the floor still requires one",
			principal: session(admin, nil),
			req:       authz.Requirement{Permission: "ledger.reverse", StepUp: true},
			want:      authz.OutcomeStepUpRequired,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decision, err := f.checker.Check(t.Context(), tc.principal, tc.req)
			require.NoError(t, err)
			require.Equal(t, tc.want.String(), decision.Outcome.String())
			require.Equal(t, tc.req.Permission, decision.RequiredPermission)
		})
	}
}

// TestCheck_InsufficientScope_NamesBothHalves pins the `meta` docs/api/errors.md promises:
// "`meta.required_scopes`, `meta.token_scopes` … The message always says exactly what is missing."
//
// It is asserted here rather than only at the HTTP edge because the decision is where the two lists
// are computed, and a bot author who is told "403" and nothing else has to guess which of
// twenty-seven scopes to mint.
func TestCheck_InsufficientScope_NamesBothHalves(t *testing.T) {
	t.Parallel()

	f := newCheckFixture(t)
	bot := f.grant(t, assignmentkinds.SubjectKindServiceAccount, authz.RoleIDBotReadonly)

	decision, err := f.checker.Check(t.Context(), bearer(bot, "raids:read"), authz.Requirement{
		Permission: "bid.manage",
		ScopeSets:  [][]string{{"bids:manage", "loot:award"}, {"bids:read"}},
	})
	require.NoError(t, err)

	require.Equal(t, authz.OutcomeInsufficientScope.String(), decision.Outcome.String())
	require.Equal(t, []string{"bids:manage", "loot:award", "bids:read"}, decision.RequiredScopes,
		"required_scopes is the union of every alternative, in declaration order and without repeats: "+
			"any one COMPLETE alternative suffices, so naming only the first would tell a bot author to "+
			"mint a scope they may not need")
	require.Equal(t, []string{"raids:read"}, decision.TokenScopes)
}

// TestCheck_AssignmentLifecycle covers the three ways a grant stops working without being deleted.
//
// Each is a column the schema carries for a reason db/schema.hcl states, and each is a branch of the
// effective-permission query: an expiry compared against now rather than swept, a temporary
// suspension (which is why this schema needs no deny rule), and a scope that reaches only its own
// target. A grant that outlived its expiry because a sweep had not run would be the whole point of
// comparing in SQL, undone.
func TestCheck_AssignmentLifecycle(t *testing.T) {
	t.Parallel()

	f := newCheckFixture(t)

	readGuild := authz.Requirement{Permission: "roster.read", ScopeSets: [][]string{{"roster:read"}}}
	pool := core.ULID("00000000000000000000000POOL")

	expiredAt := core.FromTime(checkEpoch.Add(-time.Second))
	suspendedUntil := core.FromTime(checkEpoch.Add(time.Hour))

	expired := core.NewGenerator(f.clock).MustNew()
	authz.Grant(t, f.store, f.clock, authz.GrantParams{
		SubjectKind: assignmentkinds.SubjectKindUser, SubjectID: expired,
		RoleID: authz.RoleIDAdmin, ExpiresAt: &expiredAt,
	})

	suspended := core.NewGenerator(f.clock).MustNew()
	authz.Grant(t, f.store, f.clock, authz.GrantParams{
		SubjectKind: assignmentkinds.SubjectKindUser, SubjectID: suspended,
		RoleID: authz.RoleIDAdmin, SuspendedUntilAt: &suspendedUntil,
	})

	scoped := core.NewGenerator(f.clock).MustNew()
	authz.Grant(t, f.store, f.clock, authz.GrantParams{
		SubjectKind: assignmentkinds.SubjectKindUser, SubjectID: scoped,
		RoleID: authz.RoleIDAdmin, ScopeType: assignmentkinds.ScopePool, ScopeID: pool,
	})

	tests := []struct {
		name    string
		subject core.ULID
		target  authz.Target
		want    authz.Outcome
	}{
		{
			name:    "an expired assignment grants nothing",
			subject: expired,
			want:    authz.OutcomePermissionDenied,
		},
		{
			name:    "a suspended assignment grants nothing while the suspension runs",
			subject: suspended,
			want:    authz.OutcomePermissionDenied,
		},
		{
			name:    "a pool-scoped assignment does not reach a global operation",
			subject: scoped,
			want:    authz.OutcomePermissionDenied,
		},
		{
			name:    "a pool-scoped assignment reaches its own pool",
			subject: scoped,
			target:  authz.Target{Type: assignmentkinds.ScopePool, ID: pool},
			want:    authz.OutcomeAllowed,
		},
		{
			name:    "a pool-scoped assignment does not reach a different pool",
			subject: scoped,
			target: authz.Target{
				Type: assignmentkinds.ScopePool,
				ID:   core.ULID("0000000000000000000000POOL2"),
			},
			want: authz.OutcomePermissionDenied,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := readGuild
			req.Target = tc.target

			decision, err := f.checker.Check(t.Context(), session(tc.subject, nil), req)
			require.NoError(t, err)
			require.Equal(t, tc.want.String(), decision.Outcome.String())
		})
	}
}

// TestCheck_SuspensionEndsWithTheClock is the other half of the suspension case: a suspension is
// temporary, and the check is against now rather than against a job having run.
func TestCheck_SuspensionEndsWithTheClock(t *testing.T) {
	t.Parallel()

	f := newCheckFixture(t)

	until := core.FromTime(checkEpoch.Add(time.Hour))
	subject := core.NewGenerator(f.clock).MustNew()
	authz.Grant(t, f.store, f.clock, authz.GrantParams{
		SubjectKind: assignmentkinds.SubjectKindUser, SubjectID: subject,
		RoleID: authz.RoleIDAdmin, SuspendedUntilAt: &until,
	})

	req := authz.Requirement{Permission: "roster.read", ScopeSets: [][]string{{"roster:read"}}}

	decision, err := f.checker.Check(t.Context(), session(subject, nil), req)
	require.NoError(t, err)
	require.Equal(t, authz.OutcomePermissionDenied.String(), decision.Outcome.String())

	f.clock.Advance(2 * time.Hour)

	decision, err = f.checker.Check(t.Context(), session(subject, nil), req)
	require.NoError(t, err)
	require.Equal(t, authz.OutcomeAllowed.String(), decision.Outcome.String(),
		"a suspension is temporary; past its date the grant works again with no job having run")
}

// TestCheck_Undecidable_IsAnErrorAndNotADenial. The three states in which the check cannot be MADE
// are errors rather than refusals, because the caller renders them as 503: a database that is not
// there, a permission key with no live row, and no principal at all are all the SERVER failing to
// answer, and telling a caller "403" would send a member to a login screen and a bot author hunting a
// scope for a fault neither of them caused.
func TestCheck_Undecidable_IsAnErrorAndNotADenial(t *testing.T) {
	t.Parallel()

	f := newCheckFixture(t)
	admin := f.grant(t, assignmentkinds.SubjectKindUser, authz.RoleIDAdmin)

	t.Run("no store", func(t *testing.T) {
		t.Parallel()

		_, err := authz.NewChecker(nil, clock.NewFake(checkEpoch)).
			Check(t.Context(), session(admin, nil), authz.Requirement{Permission: "roster.read"})
		require.ErrorIs(t, err, authz.ErrNoChecker)
	})

	t.Run("no principal", func(t *testing.T) {
		t.Parallel()

		_, err := f.checker.Check(t.Context(), nil, authz.Requirement{Permission: "roster.read"})
		require.ErrorIs(t, err, authz.ErrNoPrincipal)
	})

	t.Run("a permission key with no live row", func(t *testing.T) {
		t.Parallel()

		_, err := f.checker.Check(t.Context(), session(admin, nil),
			authz.Requirement{Permission: "roster.invented"})
		require.ErrorIs(t, err, authz.ErrUnknownPermission,
			"boot reconciliation refuses to start when a registered route names a key the catalogue "+
				"does not ship, so reaching this at request time means the database changed under a "+
				"running process — which is a 503, not a 403")
	})
}

// TestCheck_HasNoSuperadminBranch is 03-security.md §4.3's mechanism, made real:
//
//	| There is no hardcoded superadmin branch | `admin.owner` is a role with every permission granted
//	| *as data*, evaluated by the same code path as any other role. A test asserts `authz.Check`
//	| contains no early return. EQdkp's "group id 2 short-circuits the ACL" is a named anti-pattern.
//
// IT READS THE SOURCE, because that is the only thing that can see the ABSENCE of a branch. A
// behavioural test can show that an owner is allowed and a guest is not — TestCheck_TheMatrix does —
// and both outcomes are identical whether the answer came from the role_permission table or from an
// `if role == owner { return allowed }`. The difference only shows up the day somebody revokes
// `admin.owner` through the role editor and it keeps working.
//
// WHAT IT FORBIDS is any mention of the owner role or the owner permission anywhere in the files that
// decide a request: the identifiers RoleIDOwner / RoleKeyOwner, and the literals "owner" and
// "admin.owner". A short-circuit has to name its subject somehow, and every spelling of the subject
// is in that set. Comments are exempt — go/parser is told to keep them out of the walk — because the
// design record for why there is no branch belongs beside the code that does not have one.
func TestCheck_HasNoSuperadminBranch(t *testing.T) {
	t.Parallel()

	forbidden := map[string]string{
		"RoleIDOwner":  "the owner role's seeded id",
		"RoleKeyOwner": "the owner role's key",
		"owner":        "the owner role, by name",
		"admin.owner":  "the owner permission key",
	}

	// check.go decides; mint.go is the scope-subsetting rule beside it. Neither may branch on who the
	// principal is.
	for _, file := range []string{"check.go", "mint.go"} {
		fset := token.NewFileSet()

		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "parse %s", file)

		ast.Inspect(parsed, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				reason, found := forbidden[node.Name]
				require.Falsef(t, found, "%s:%s names %s (%s). 03-security.md §4.3: admin.owner is a "+
					"role with every permission granted AS DATA, evaluated by the same code path as any "+
					"other role. A branch here is EQdkp's \"group id 2 short-circuits the ACL\", which is "+
					"a named anti-pattern in this repository.",
					file, fset.Position(node.Pos()), node.Name, reason)

			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}

				value := strings.Trim(node.Value, "`\"")

				reason, found := forbidden[value]
				require.Falsef(t, found, "%s:%s contains the literal %q (%s). See the Ident case above.",
					file, fset.Position(node.Pos()), value, reason)
			}

			return true
		})
	}
}

// TestCheck_OwnerIsEvaluatedAsData is the behavioural companion to the source test above, and it is
// the case the two of them together actually pin: the owner's capability comes from role_permission
// rows, so removing one removes the capability.
//
// It cannot revoke a grant — there is no statement that deletes a role_permission row, because the
// role editor has not shipped — so it takes the other route to the same property: `admin.owner` is a
// key the owner role holds and `person.merge` is a key it does not... except that it does. The
// honest form is therefore the one below: a subject holding `owner` is refused a key the SEED does
// not grant it, which is impossible for any implementation that short-circuits on the role.
func TestCheck_OwnerIsEvaluatedAsData(t *testing.T) {
	t.Parallel()

	f := newCheckFixture(t)
	owner := f.grant(t, assignmentkinds.SubjectKindUser, authz.RoleIDOwner)

	// bank.fulfil is a real catalogue key that NO built-in role grants — one of the twenty added when
	// the UI mockups were reconciled (canonical §6), after §5.1's role table was written. An
	// implementation with a superadmin branch answers "allowed" here.
	decision, err := f.checker.Check(t.Context(), session(owner, micros(checkEpoch)),
		authz.Requirement{Permission: "bank.fulfil"})
	require.NoError(t, err)
	require.Equal(t, authz.OutcomePermissionDenied.String(), decision.Outcome.String(),
		"the owner role holds exactly the keys the seed grants it and no more. If this row is ever "+
			"allowed, either the seed widened (a deliberate, reviewable change — see issue #267) or a "+
			"superadmin branch was reintroduced (the thing 03-security.md §4.3 forbids).")
}
