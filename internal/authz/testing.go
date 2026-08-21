package authz

import (
	"context"
	"testing"

	rolekinds "github.com/prokopto-dev/dragonkillparty/internal/authz/role/kinds"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The test harness lives in the production package, on the same terms internal/auth/testing.go and
// internal/store/testing.go argue for: this file imports `testing`, so `testing` and `flag` link into
// the binary and these helpers sit on the shipped API surface — the trade net/http/httptest makes.
//
// WHY IT CANNOT LIVE IN A SIBLING PACKAGE, and the reason is the same one that put the auth harness
// here. A migrated database has an EMPTY permission table: the catalogue is projected into it by
// Reconcile on the boot path, not by a migration, because at migration time role_permission's foreign
// key has nothing to resolve against. So every test of anything downstream of authorization — the
// middleware, the guild resource, an integration case — has to run the real boot step first, and a
// sibling reimplementing it would prove the sibling agrees with itself.
//
// THE PRODUCT PATH IS THE ONLY PATH, exactly as internal/seed writes every row through
// ledger.Service.Commit rather than a bulk insert. Boot() below calls Reconcile; Grant() writes the
// statement the role editor and the first-run bootstrap (#264) will call. Nothing here fabricates a
// permission row, a role or a grant by hand, so a test that passes proves the real reconciliation and
// the real assignment shape work — not that a fixture does.

// Boot reconciles the permission catalogue and seeds the built-in roles into st, exactly as cmd/dkp's
// boot path does, and returns the report.
//
// EVERY TEST THAT REACHES THE CHOKE POINT NEEDS THIS, including tests that are not about
// authorization at all: authz.Check reads permission rows, and a database that never reconciled has
// none, so a guild-resource test would answer 503 with "permission key has no live row" rather than
// the 200 it is asserting about. That is the fail-closed behaviour working, and this is the one line
// that puts a test on the other side of it.
//
// The required set is empty because a test harness registers no routes of its own; Reconcile's own
// tests cover the required-key path.
func Boot(tb testing.TB, st *store.Store, clk clock.Clock) Report {
	tb.Helper()

	report, err := NewReconciler(st, clk).Reconcile(context.Background(), nil)
	if err != nil {
		tb.Fatalf("reconcile the permission catalogue: %v", err)
	}

	return report
}

// GrantParams is what a test varies about one role assignment. The zero value is a live, global,
// never-expiring, unsuspended grant — the shape a role editor writes by default.
type GrantParams struct {
	// SubjectKind is internal/authz/roleassignment/kinds' subject vocabulary, which is also
	// Principal.Kind: `user` for a human, `service_account` for a bot.
	SubjectKind string

	// SubjectID is the app_user or service_account the grant names.
	SubjectID core.ULID

	// RoleID is the role granted — one of the RoleID* constants for a built-in.
	RoleID string

	// ScopeType and ScopeID narrow the grant to one pool or raid group. Both zero means global, and
	// the schema's role_assignment_scope_shape CHECK makes the pairing an equivalence: a scoped grant
	// with no id, or a global one with an id, is refused by the database rather than by this helper.
	ScopeType string
	ScopeID   core.ULID

	// SuspendedUntilAt and ExpiresAt are the two ways a grant stops working without being deleted —
	// temporary revocation and a time-boxed grant. Both nil is the ordinary case, and both are here so
	// the branches authz.Check has for them are reachable from a test.
	SuspendedUntilAt *core.Micros
	ExpiresAt        *core.Micros
}

// Grant writes one role assignment and returns its id.
//
// It goes through InsertRoleAssignment — the statement the role editor and #264's first-run bootstrap
// will call — rather than assembling a row here, so a test proves the real assignment shape resolves
// through the real effective-permission query.
//
// IT DOES NOT CHECK THE ROLE'S applies_to, AND MUST NOT START. A grant whose subject kind the role
// does not admit — a service account handed `owner` — is exactly the row this helper has to be able
// to produce, because no database constraint can refuse it (the constraint spans two tables and the
// subject is polymorphic) and therefore neither can the statement. TestCheck_RoleAppliesTo_IsEnforced
// writes one and requires the evaluation to grant nothing. Validating here would move the rule into
// the fixture and leave the authorization source untested against the case it exists to refuse.
func Grant(tb testing.TB, st *store.Store, clk clock.Clock, p GrantParams) core.ULID {
	tb.Helper()

	if !assignmentkinds.IsSubjectKind(p.SubjectKind) {
		tb.Fatalf("%q is not a role_assignment subject_kind", p.SubjectKind)
	}

	scopeType := p.ScopeType
	if scopeType == "" {
		scopeType = assignmentkinds.ScopeGlobal
	}

	if !assignmentkinds.IsScopeType(scopeType) {
		tb.Fatalf("%q is not a role_assignment scope_type", scopeType)
	}

	var scopeID *string

	if p.ScopeID != "" {
		id := p.ScopeID.String()
		scopeID = &id
	}

	id := core.NewGenerator(clk).MustNew()
	now := int64(core.FromTime(clk.Now()))

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return q.InsertRoleAssignment(ctx, sqlitegen.InsertRoleAssignmentParams{
			ID:               id.String(),
			SubjectKind:      p.SubjectKind,
			SubjectID:        p.SubjectID.String(),
			RoleID:           p.RoleID,
			ScopeType:        scopeType,
			ScopeID:          scopeID,
			SuspendedUntilAt: microsPtr(p.SuspendedUntilAt),
			GrantedVia:       assignmentkinds.GrantedViaManual,
			ExpiresAt:        microsPtr(p.ExpiresAt),
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	})
	if err != nil {
		tb.Fatalf("grant role %s to %s %s: %v", p.RoleID, p.SubjectKind, p.SubjectID, err)
	}

	return id
}

// GrantRole is the common case: a live, global grant of one built-in role.
func GrantRole(
	tb testing.TB, st *store.Store, clk clock.Clock, subjectKind string, subject core.ULID, roleID string,
) core.ULID {
	tb.Helper()

	return Grant(tb, st, clk, GrantParams{
		SubjectKind: subjectKind,
		SubjectID:   subject,
		RoleID:      roleID,
	})
}

// microsPtr converts an optional Micros to the *int64 the generated params carry. nil stays nil,
// which is what the columns mean: no suspension, no expiry.
func microsPtr(m *core.Micros) *int64 {
	if m == nil {
		return nil
	}

	v := int64(*m)

	return &v
}

// SeedRoleParams is a guild's OWN role — the kind the role editor creates, as distinct from the nine
// built-ins Boot seeds.
//
// It exists for one property no built-in can exercise: `applies_to = "both"`. All nine built-ins
// declare a single kind (seven `user`, two `service_account`, docs/design/01-domain-model.md §5.1),
// while the schema's DEFAULT is `both` — so a role assignable to either principal kind is the
// ordinary custom role and the one shape the seed cannot produce.
type SeedRoleParams struct {
	// ID, Key and Name identify the role. Key is empty for a guild's own role: a non-NULL key is what
	// MARKS a role built-in, so a custom role must not carry one.
	ID   string
	Name string

	// AppliesTo is internal/authz/role/kinds' vocabulary — user, service_account or both.
	AppliesTo string

	// Permissions are the catalogue keys the role grants. Each must be a live permission row; the FK
	// from role_permission to permission(key) refuses anything else, which is the point.
	Permissions []string
}

// SeedRole writes one custom role and its grants, through the same statements the built-in seed uses.
func SeedRole(tb testing.TB, st *store.Store, clk clock.Clock, p SeedRoleParams) {
	tb.Helper()

	if !rolekinds.IsAppliesTo(p.AppliesTo) {
		tb.Fatalf("%q is not a role applies_to value", p.AppliesTo)
	}

	now := int64(core.FromTime(clk.Now()))

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		if err := q.InsertRole(ctx, sqlitegen.InsertRoleParams{
			ID: p.ID,
			// NULL: a non-NULL key is what marks a role built-in, and this is the guild's own.
			Key:         nil,
			Name:        p.Name,
			NameNorm:    p.Name,
			Description: "a guild's own role, seeded by a test",
			IsBuiltin:   0,
			AppliesTo:   p.AppliesTo,
			SortOrder:   100,
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			return err
		}

		for _, permission := range p.Permissions {
			if err := q.InsertRolePermission(ctx, sqlitegen.InsertRolePermissionParams{
				RoleID:        p.ID,
				PermissionKey: permission,
			}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		tb.Fatalf("seed role %s: %v", p.Name, err)
	}
}
