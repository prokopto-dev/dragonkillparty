package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the role_assignment enum catalogue — canonical §5's "one Go catalogue" for
// role_assignment.subject_kind, .scope_type and .granted_via.
//
// THREE COLUMNS, ONE PACKAGE, AND THE SPLIT FROM role IS MECHANICAL RATHER THAN CONCEPTUAL: a
// catalogue owns its generated region of db/schema.hcl through a `schemaEnumBegin`/`schemaEnumEnd`
// const PAIR, ENUM001 matches those by identifier name (internal/repogate/enum.go's enumMarkerDecl),
// and two pairs cannot exist in one Go package. `role` and `role_assignment` are separate `table`
// blocks and therefore separate regions, so they are separate catalogue packages;
// internal/authz/role/kinds is the other half.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY AND internal/schemaenum, which is itself
// such a leaf — the same hard constraint internal/ledger/kinds records. scripts/gen-enums.sh is the
// FIRST step of `make gen` and runs internal/ledger/enumgen, which reaches this; anything it compiles
// must build BEFORE sqlc runs (TestGen_EnumGenerator_DependsOnNoGeneratedCode). In particular this
// package does NOT import internal/authz, which reaches internal/store/sqlitegen.
//
// WHY THESE VOCABULARIES EXIST AT ALL is the scoping half of docs/design/01-domain-model.md §5: EQdkp
// expressed "raid leader, but only for the Tuesday group" as two hardcoded `*_grpleader` permissions,
// and this schema expresses it as any role plus a (scope_type, scope_id) pair. The subject side is the
// same story — a role assignment names a user or a service account, so the pair is polymorphic and the
// kind column is what makes it readable rather than a bare id nobody can resolve.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between.
//
// It IS schemaenum.ErrMarkersMissing rather than a second sentinel wrapping it: one condition, one
// value, so `errors.Is` gives the same answer whichever name the caller reaches for.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The role_assignment.subject_kind vocabulary: WHO holds the assignment.
//
// The (subject_kind, subject_id) pair is polymorphic and carries no foreign key, deliberately — app_user
// and service_account are two tables and SQLite has no polymorphic reference. That is why the kind is a
// column rather than an inference from which of two id columns is populated: a resolver, an audit row
// and the role editor all need to name it, and one column with a CHECK is cheaper and more readable
// than two nullable ids with a paired shape constraint.
//
// The values are deliberately the same two strings role.applies_to's first two are, and the two
// catalogues still do not import each other: they agree on a word today because the same two principal
// kinds exist, and coupling them would make adding a third principal look like widening every role's
// applicability. TestRoleAssignmentKinds_SubjectKinds_AreRoleAppliesToValues in internal/authz asserts
// the agreement instead — the same pattern internal/decay/kinds and internal/ledger/kinds use.
const (
	SubjectKindUser           = "user"
	SubjectKindServiceAccount = "service_account"
)

// SubjectKinds returns every legal role_assignment.subject_kind, in the order the CHECK constraint
// carries them.
//
// A FUNCTION returning a FRESH SLICE of the constants above, never a package-level var —
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on.
//
// The order is FIXED: CheckExpr renders in it, so reordering rewrites the CHECK expression, which
// Atlas sees as a schema change and a migration nobody wanted.
func SubjectKinds() []string {
	return []string{
		SubjectKindUser,
		SubjectKindServiceAccount,
	}
}

// IsSubjectKind reports whether v is a legal role_assignment.subject_kind.
func IsSubjectKind(v string) bool { return contains(SubjectKinds(), v) }

// The role_assignment.scope_type vocabulary: HOW FAR the assignment reaches.
//
//   - ScopeGlobal    the whole instance. The column's DEFAULT and the ordinary case; scope_id is NULL,
//     and the paired CHECK in db/schema.hcl makes that an equivalence rather than a convention.
//   - ScopePool      one point pool. "Officer, but only for the raiding pool."
//   - ScopeRaidGroup one raid group. This is the case EQdkp needed two hardcoded permissions for
//     (docs/design/01-domain-model.md §5): raid_group.id is a legal scope_id, so
//     "raid leader for Tuesday group" is an assignment rather than a permission key.
//
// There is no `character` or `person` scope, and that absence is the design: a member acting on their
// own records is OWNERSHIP, checked against the principal, not a role scoped to themselves. Adding one
// would give every member a role assignment row and make "who can do X?" a query over the roster.
const (
	ScopeGlobal    = "global"
	ScopePool      = "pool"
	ScopeRaidGroup = "raid_group"
)

// ScopeTypes returns every legal role_assignment.scope_type, in the order the CHECK constraint carries
// them: the widest first, then the two narrowing ones.
func ScopeTypes() []string {
	return []string{
		ScopeGlobal,
		ScopePool,
		ScopeRaidGroup,
	}
}

// DefaultScopeType is the value db/schema.hcl gives role_assignment.scope_type.
//
// Named rather than left as a bare `default = "global"` in the schema with a matching literal in Go:
// the column default is a SECOND spelling of a catalogue value, in a file the generator does not
// rewrite (it is a column attribute, not a check block, so ENUM001 does not read it either).
// TestRoleAssignmentKinds_SchemaDefaults_MatchTheCatalogue is what ties the two together.
func DefaultScopeType() string { return ScopeGlobal }

// IsScopeType reports whether v is a legal role_assignment.scope_type.
func IsScopeType(v string) bool { return contains(ScopeTypes(), v) }

// The role_assignment.granted_via vocabulary: WHERE the grant came from.
//
// It is provenance, and it is a real column because every one of these has a different revocation
// story. A Discord-synced assignment (docs/design/01-domain-model.md §4) is rewritten by the next sync
// and re-granting it by hand is futile; an imported one came from a guild's EQdkp groups and is the
// first thing to audit after a migration; a bootstrap one is the first owner the setup wizard created
// and is the assignment nobody may be left without. Losing that distinction turns "why does this person
// have officer?" into archaeology over the audit log.
const (
	GrantedViaManual      = "manual"
	GrantedViaInvitation  = "invitation"
	GrantedViaDiscordSync = "discord_sync"
	GrantedViaImport      = "import"
	GrantedViaBootstrap   = "bootstrap"
)

// GrantedVia returns every legal role_assignment.granted_via, in the order the CHECK constraint
// carries them.
func GrantedVia() []string {
	return []string{
		GrantedViaManual,
		GrantedViaInvitation,
		GrantedViaDiscordSync,
		GrantedViaImport,
		GrantedViaBootstrap,
	}
}

// DefaultGrantedVia is the value db/schema.hcl gives role_assignment.granted_via, for the reason
// DefaultScopeType is named.
func DefaultGrantedVia() string { return GrantedViaManual }

// IsGrantedVia reports whether v is a legal role_assignment.granted_via.
func IsGrantedVia(v string) bool { return contains(GrantedVia(), v) }

func contains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}

	return false
}

// The columns this catalogue governs. Unexported: the CheckExpr functions below are the only things
// that need to name them, and a caller wanting the string wants the whole expression.
const (
	subjectKindColumn = "subject_kind"
	scopeTypeColumn   = "scope_type"
	grantedViaColumn  = "granted_via"
)

// SubjectKindCheckExpr renders the body of role_assignment's subject_kind CHECK constraint:
//
//	subject_kind IN ('user', 'service_account')
//
// Named for its column, as internal/account/kinds' two renderers are: this catalogue governs three
// columns, so an unqualified CheckExpr would be an invitation to render one column's values against
// another's — a CHECK that compiles, applies, and rejects every row.
func SubjectKindCheckExpr() string {
	return schemaenum.CheckExpr(subjectKindColumn, SubjectKinds())
}

// ScopeTypeCheckExpr renders the body of role_assignment's scope_type CHECK constraint:
//
//	scope_type IN ('global', 'pool', 'raid_group')
//
// The PLAIN form, not the nullable one: the column is NOT NULL with a default. The related shape
// constraint — scope_id is NULL exactly when the scope is global — is a hand-authored CHECK outside
// this region, because it is about two columns agreeing rather than about a vocabulary.
func ScopeTypeCheckExpr() string {
	return schemaenum.CheckExpr(scopeTypeColumn, ScopeTypes())
}

// GrantedViaCheckExpr renders the body of role_assignment's granted_via CHECK constraint:
//
//	granted_via IN ('manual', 'invitation', 'discord_sync', 'import', 'bootstrap')
func GrantedViaCheckExpr() string {
	return schemaenum.CheckExpr(grantedViaColumn, GrantedVia())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside
// `table "role_assignment"`. Everything between them is written by `make gen`; everything outside them
// is hand-authored schema truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine. The marker text names the catalogue because db/schema.hcl carries several generated regions
// and each is found by an exact whole-line match on ITS OWN markers: two regions sharing a marker line
// would each rewrite the other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — role_assignment enum CHECKs, from internal/authz/roleassignment/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — role_assignment enum CHECKs."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason SubjectKinds is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the three role_assignment enum CHECKs",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "role_assignment"`.
//
// No trailing newline: Replace joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestRoleAssignmentKinds_CheckMatchesCatalogue fails on.",
		`  check "role_assignment_subject_kind_enum" {`,
		fmt.Sprintf(`    expr = %q`, SubjectKindCheckExpr()),
		"  }",
		"",
		`  check "role_assignment_scope_type_enum" {`,
		fmt.Sprintf(`    expr = %q`, ScopeTypeCheckExpr()),
		"  }",
		"",
		`  check "role_assignment_granted_via_enum" {`,
		fmt.Sprintf(`    expr = %q`, GrantedViaCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock(),
// and is one of the rewrites `make gen` composes before writing db/schema.hcl back.
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing" and lets this
// render compose with the other catalogues' in any order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
