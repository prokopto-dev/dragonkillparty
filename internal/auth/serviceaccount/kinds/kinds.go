package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the service_account enum catalogue — canonical §5's "one Go catalogue" for
// service_account.state.
//
// ITS OWN PACKAGE for the constraint .claude/rules/migrations.md states: a catalogue owns its region
// through a schemaEnumBegin/schemaEnumEnd const pair, ENUM001 matches those by identifier name, so
// one package cannot declare two regions and a region cannot span two `table` blocks. app_user's
// state and this one are different columns of different tables with a different vocabulary, and
// merging them would mean one of the two tables' CHECK could never be regenerated.
//
// A LEAF PACKAGE: standard library and internal/schemaenum only, never internal/auth. See the note
// in internal/auth/appuser/kinds.
//
// WHY A BOT IS NOT A USER (ADR-0011, docs/design/03-security.md §6.2). Tokens belong to service
// accounts, not people; a service account has a human owner_user_id for audit and notification, and
// deactivating the human does not kill the bot. That is why this vocabulary is SHORTER than
// app_user's: 'pending' has no meaning for an identity that never logs in, and 'suspended' versus
// 'disabled' is a distinction about a person's standing in the guild, not about whether a bot may
// call the API. Two values, and the middleware refuses a token whose account is not active.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between. It IS schemaenum.ErrMarkersMissing rather than a second sentinel
// wrapping it: one condition, one value.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The service_account.state vocabulary (docs/design/01-domain-model.md §4.3).
//
//   - Active   the bot may authenticate, subject to its tokens' own expiry and revocation.
//     Every token the account owns stops working the moment it leaves this state.
//   - Disabled turned off by an officer. Reversible, and distinct from revoking the tokens: a
//     disabled account keeps its token rows so `GET /tokens/{id}/activity` still answers "did the
//     leaked token do anything?".
//
// ORPHANED IS NOT A STATE. §6.2 says a service account whose owner is deactivated "is flagged
// orphaned and an owner-reassignment task appears" — that is a fact about owner_user_id, derived by
// a join, not a third value here. Making it a state would mean a bot could be simultaneously
// orphaned and disabled and the column could only say one of them.
const (
	StateActive   = "active"
	StateDisabled = "disabled"
)

// DefaultState is the column DEFAULT in db/schema.hcl, restated here so the schema's default is a
// catalogue value rather than a literal nothing checks — the default is written where `make gen`
// does not rewrite and ENUM001 does not read.
func DefaultState() string { return StateActive }

// States returns every legal service_account.state, in the order the CHECK constraint carries them.
// A FRESH SLICE from a function, never a package-level var; the order is fixed because CheckExpr
// renders in it.
func States() []string {
	return []string{
		StateActive,
		StateDisabled,
	}
}

// IsState reports whether v is a legal service_account.state — the runtime half, so a writer refuses
// a bad state with a Go error rather than having SQLite name a constraint from inside a write
// transaction that has already done work.
func IsState(v string) bool {
	for _, candidate := range States() {
		if candidate == v {
			return true
		}
	}

	return false
}

// The column this catalogue governs. Unexported: StateCheckExpr is the only thing that needs to name
// it.
const stateColumn = "state"

// StateCheckExpr renders the body of service_account's state CHECK constraint:
//
//	state IN ('active', 'disabled')
func StateCheckExpr() string {
	return schemaenum.CheckExpr(stateColumn, States())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table
// "service_account"`. HCL line comments, so Atlas parses the file unchanged; the marker text names
// the catalogue because each region is found by an exact whole-line match on ITS OWN markers.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — service_account enum CHECK, from internal/auth/serviceaccount/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — service_account enum CHECK."
)

func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the service_account state CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "service_account"`. No trailing newline: Replace joins it back into the file's line
// stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestServiceAccountKinds_CheckMatchesCatalogue fails on.",
		`  check "service_account_state_enum" {`,
		fmt.Sprintf(`    expr = %q`, StateCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock().
// Idempotent, and it touches only this catalogue's region.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
