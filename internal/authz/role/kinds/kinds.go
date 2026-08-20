package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the role enum catalogue — canonical §5's "one Go catalogue" for role.applies_to.
//
// ONE COLUMN, ONE PACKAGE, AND THE SPLIT FROM role_assignment IS MECHANICAL RATHER THAN CONCEPTUAL.
// The RBAC tables are one subsystem and it would be natural to catalogue all four of their
// vocabularies together, but a catalogue owns its generated region of db/schema.hcl through a
// `schemaEnumBegin`/`schemaEnumEnd` const PAIR, and ENUM001 matches those by identifier name
// (internal/repogate/enum.go's enumMarkerDecl). Two pairs cannot exist in one Go package, and a
// region cannot span two `table` blocks, so `role` and `role_assignment` need one catalogue package
// each. internal/authz/roleassignment/kinds is the other half.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY AND internal/schemaenum, which is itself
// such a leaf — the same hard constraint internal/ledger/kinds records. scripts/gen-enums.sh is the
// FIRST step of `make gen` and runs internal/ledger/enumgen, which reaches this; anything it compiles
// must build BEFORE sqlc runs, or a tree whose generated code does not build can no longer run
// `make gen` to repair itself (TestGen_EnumGenerator_DependsOnNoGeneratedCode). In particular this
// package does NOT import internal/authz, which reaches internal/store/sqlitegen.
//
// THE DIRECTION OF TRUTH IS: this file → db/schema.hcl (via `make gen`) → the migration (via
// `make migration`) → the database, and separately this file → the role editor's constants. Nothing
// reads it in the other direction.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between.
//
// It IS schemaenum.ErrMarkersMissing rather than a second sentinel wrapping it: one condition, one
// value, so `errors.Is` gives the same answer whichever name the caller reaches for.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The role.applies_to vocabulary: WHICH KIND OF PRINCIPAL may hold this role
// (docs/design/01-domain-model.md §5).
//
// It exists because the built-in role seed is split down that line — `guest`, `member`, `raider`,
// `raid_leader`, `officer`, `admin` and `owner` apply to users, while `bot_readonly` and `bot_raid`
// apply to service accounts — and a bot holding `owner` is the EQdkp defect this product's founding
// claim is about (ADR-0011: there is no all-powerful token). The column is what lets the role editor
// refuse that assignment before it is offered, rather than relying on nobody choosing it.
//
// AppliesToBoth is the DEFAULT because a guild's own custom role is ordinarily assignable to either,
// and a default that narrows would silently make every hand-made role unassignable to the bot it was
// created for.
const (
	AppliesToUser           = "user"
	AppliesToServiceAccount = "service_account"
	AppliesToBoth           = "both"
)

// AppliesTo returns every legal role.applies_to value, in the order the CHECK constraint carries them.
//
// A FUNCTION returning a FRESH SLICE of the constants above, never a package-level var —
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on. Every other catalogue in the repository is
// the same shape for the same reason.
//
// The order is FIXED: CheckExpr renders in it, so reordering rewrites the CHECK expression, which
// Atlas sees as a schema change and a migration nobody wanted.
func AppliesTo() []string {
	return []string{
		AppliesToUser,
		AppliesToServiceAccount,
		AppliesToBoth,
	}
}

// DefaultAppliesTo is the value db/schema.hcl gives role.applies_to.
//
// Named rather than left as a bare `default = "both"` in the schema with a matching literal in Go: the
// column default is a SECOND spelling of a catalogue value, in a file the generator does not rewrite
// (it is a column attribute, not a check block, so ENUM001 does not read it either).
// TestRoleKinds_SchemaDefault_MatchesTheCatalogue is what ties the two together.
func DefaultAppliesTo() string { return AppliesToBoth }

// IsAppliesTo reports whether v is a legal role.applies_to value.
//
// The RUNTIME half of the catalogue: the role editor can refuse a bad value with a Go error naming the
// legal ones, rather than have SQLite name a constraint from inside a write transaction that has
// already done work.
func IsAppliesTo(v string) bool {
	for _, candidate := range AppliesTo() {
		if candidate == v {
			return true
		}
	}

	return false
}

// appliesToColumn is the column this catalogue governs. Unexported: CheckExpr below is the only thing
// that needs to name it, and a caller wanting the string wants the whole expression.
const appliesToColumn = "applies_to"

// AppliesToCheckExpr renders the body of role's applies_to CHECK constraint:
//
//	applies_to IN ('user', 'service_account', 'both')
//
// The PLAIN form, not the nullable one: the column is NOT NULL with a default, so there is no NULL arm
// to admit.
func AppliesToCheckExpr() string {
	return schemaenum.CheckExpr(appliesToColumn, AppliesTo())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table "role"`.
// Everything between them is written by `make gen`; everything outside them is hand-authored schema
// truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine. The marker text names the catalogue because db/schema.hcl carries several generated regions
// and each is found by an exact whole-line match on ITS OWN markers: two regions sharing a marker line
// would each rewrite the other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — role enum CHECK, from internal/authz/role/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — role enum CHECK."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason AppliesTo is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the role applies_to CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "role"`.
//
// No trailing newline: Replace joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestRoleKinds_CheckMatchesCatalogue fails on.",
		`  check "role_applies_to_enum" {`,
		fmt.Sprintf(`    expr = %q`, AppliesToCheckExpr()),
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
