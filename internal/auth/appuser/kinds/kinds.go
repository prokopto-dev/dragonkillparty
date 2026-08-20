package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the app_user enum catalogue — canonical §5's "one Go catalogue" for
// app_user.state.
//
// WHY THIS IS ITS OWN PACKAGE, one directory away from the three other auth catalogues. A catalogue
// owns its region of db/schema.hcl through a schemaEnumBegin/schemaEnumEnd const PAIR, and ENUM001
// matches those by identifier NAME (internal/repogate/enum.go) — so two pairs cannot live in one Go
// package and a region cannot span two `table` blocks. Four auth tables carry a string enum, so the
// auth catalogues are four packages: this one, internal/auth/useridentity/kinds,
// internal/auth/serviceaccount/kinds and internal/auth/feedtoken/kinds. internal/authz's role and
// role_assignment pair is the worked example (#261); .claude/rules/migrations.md states the
// constraint. Do not "tidy" them together.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY AND internal/schemaenum, which is itself
// such a leaf. That is a hard constraint rather than a preference: scripts/gen-enums.sh is the FIRST
// step of `make gen` and compiles internal/ledger/enumgen, which reaches this. Anything it compiles
// must build BEFORE sqlc runs, or a tree whose generated code does not build can no longer run
// `make gen` to repair itself. In particular it must never import internal/auth, its own parent.
//
// WHAT DRIFT COSTS HERE. state is the column every credential check reads before it believes a
// session: a suspended or disabled account keeps its rows and stops being an identity. A value in Go
// and not in the CHECK is an account state the product can name and the database refuses, discovered
// inside the write transaction that was disabling somebody; the reverse is a state a row can hold
// that no Go branch handles, which fails OPEN — the direction that matters for an auth table.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between. It IS schemaenum.ErrMarkersMissing rather than a second sentinel
// wrapping it: one condition, one value.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The app_user.state vocabulary (docs/design/01-domain-model.md §4.1). WHAT THE ACCOUNT MAY DO, as
// opposed to what it holds:
//
//   - Pending   created and not yet usable — an invitation accepted, an email not yet confirmed.
//   - Active    the only state that authenticates.
//   - Suspended a deliberate, reversible officer action. Rows and history are untouched.
//   - Disabled  deactivated: the person left the guild. Also reversible, and distinguished from
//     Suspended because the two answer different questions in an audit ("punished" vs "gone").
//
// DELETION IS NOT A STATE. app_user carries deleted_at and the unique indexes are partial over it,
// so a deleted username frees up while its rows remain referenced by ledger and audit history.
const (
	StatePending   = "pending"
	StateActive    = "active"
	StateSuspended = "suspended"
	StateDisabled  = "disabled"
)

// DefaultState is the column DEFAULT in db/schema.hcl, restated here so the schema's default is a
// catalogue value rather than a literal nothing checks. TestAppUserKinds_SchemaDefault_MatchesTheCatalogue
// ties the two together — the default is a value written where `make gen` does not rewrite and
// ENUM001 does not read.
//
// It is 'active' because the first-run owner and every officer-created account are usable
// immediately; 'pending' belongs to the invitation and email-confirmation flows, which set it
// explicitly (issue #264).
func DefaultState() string { return StateActive }

// States returns every legal app_user.state, in the order the CHECK constraint carries them.
//
// A FUNCTION returning a FRESH SLICE, never a package-level var — .claude/rules/go-idioms.md bans
// package-level mutable state, and a shared slice is one append in a test away from an intermittent
// failure under -shuffle=on.
//
// The order is not semantic but it IS fixed: CheckExpr renders in this order, so reordering rewrites
// the CHECK expression, which Atlas sees as a schema change and a migration nobody wanted.
func States() []string {
	return []string{
		StatePending,
		StateActive,
		StateSuspended,
		StateDisabled,
	}
}

// IsState reports whether v is a legal app_user.state — the runtime half, so a writer refuses a bad
// state with a Go error naming the legal values rather than having SQLite name a constraint from
// inside a write transaction that has already done work.
func IsState(v string) bool {
	for _, candidate := range States() {
		if candidate == v {
			return true
		}
	}

	return false
}

// The column this catalogue governs. Unexported: StateCheckExpr is the only thing that needs to name
// it, and a caller wanting the string wants the whole expression.
const stateColumn = "state"

// StateCheckExpr renders the body of app_user's state CHECK constraint:
//
//	state IN ('pending', 'active', 'suspended', 'disabled')
func StateCheckExpr() string {
	return schemaenum.CheckExpr(stateColumn, States())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table
// "app_user"`. HCL line comments, so Atlas parses the file unchanged and the region is invisible to
// the diff engine. The marker text names the catalogue because db/schema.hcl carries several
// generated regions and each is found by an exact whole-line match on ITS OWN markers.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — app_user enum CHECK, from internal/auth/appuser/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — app_user enum CHECK."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason States is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the app_user state CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "app_user"`. No trailing newline: Replace joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestAppUserKinds_CheckMatchesCatalogue fails on.",
		`  check "app_user_state_enum" {`,
		fmt.Sprintf(`    expr = %q`, StateCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock().
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing" and lets this
// render compose with the other catalogues' in any order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
