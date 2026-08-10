package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the audit_log enum catalogue — canonical §5's "one Go catalogue" for
// audit_log.actor_kind.
//
// WHY THIS FILE EXISTS. Before it, the six actor kinds were a literal in db/schema.hcl's CHECK and a
// SECOND literal in internal/ledger/commit.go's `validActorKinds` map, which is two lists and two
// ways to be wrong: a value added to the CHECK alone makes Commit refuse a legal actor kind with
// ErrInvalidRequest, and a value added to the map alone waves through a value SQLite rejects from
// INSIDE the write transaction — the failure canonical §5 exists to prevent. The map was also
// package-level mutable state, which .claude/rules/go-idioms.md bans: one stray assignment in a test
// away from an intermittent failure under -shuffle=on. #40; the same defect #29 removed for
// ledger_batch.kind and ledger_batch.source.
//
// WHY IT LIVES UNDER internal/audit AND NOT internal/ledger/kinds. audit_log is not the ledger's
// table. internal/ledger is its only WRITER today, which is a fact about Phase 0 rather than about
// ownership: domain model §17 has the Phase 2 HTTP middleware writing an audit row for every mutating
// action, most of which touch no ledger at all, and audit rows are prunable by retention where a
// ledger row is not. Filing actor_kind under the ledger's catalogue would make every one of those
// later writers import internal/ledger/kinds to name a vocabulary the ledger does not own, and would
// leave two unrelated CHECKs on two unrelated tables sharing one generated region.
//
// WHY A SUBPACKAGE OF internal/audit RATHER THAN internal/audit ITSELF. Same reason
// internal/ledger/kinds is a subpackage, learned the expensive way in #29: scripts/gen-enums.sh is
// the FIRST step of `make gen` and compiles this, so anything it reaches must build BEFORE sqlc runs.
// The Phase 2 audit service — the pruner, the chain verifier, the forensic queries — needs
// internal/store, and a catalogue sitting in the same package as that service would drag
// internal/store/sqlitegen into `make gen`'s first step, at which point a tree whose generated code
// does not build can no longer run `make gen` to repair itself. Putting the catalogue in a leaf on
// day one costs one directory and means the audit service can land without moving it.
// TestGen_EnumGenerator_DependsOnNoGeneratedCode holds this open. Import nothing from here but the
// standard library and internal/schemaenum, which is itself such a leaf.
//
// THE DIRECTION OF TRUTH IS: this file → db/schema.hcl (via `make gen`) → the migration (via
// `make migration`) → the database, and separately this file → internal/ledger/commit.go's
// validation. Nothing reads it in the other direction.
//
// ADDING AN ACTOR KIND IS A SCHEMA CHANGE. Appending here does not change a deployed database:
// run `make gen` to rewrite the CHECK in db/schema.hcl, then `make migration NAME=<snake_case>` and
// read what Atlas wrote — a CHECK change on SQLite is the 12-step table rebuild, and audit_log
// carries an append-only UPDATE trigger that a rebuild silently drops unless the migration re-creates
// it (.claude/rules/migrations.md). It is also an OpenAPI enum the day an audit endpoint exists.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between.
//
// It IS schemaenum.ErrMarkersMissing rather than a second sentinel wrapping it: one condition, one
// value, so `errors.Is` gives the same answer whichever name the caller reaches for.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The audit_log.actor_kind vocabulary, as the Go const block canonical §5 requires: WHAT KIND OF
// PRINCIPAL acted, not who they were (actor_label) nor what authority they used (the Phase 2
// actor_user_id / actor_token_id / permission_used columns).
//
// Named constants rather than bare literals because a consumer needs a SYMBOL to reference:
// `Actor{Kind: ActorSystem}` is a compile error when it is misspelled, where `Kind: "sytem"` is valid
// Go that only IsActorKind catches, and only at runtime.
const (
	ActorUser           = "user"            // a human acting on a session
	ActorServiceAccount = "service_account" // a bot acting on a service-account token
	ActorSystem         = "system"          // the binary itself — jobs, decay cadence, a Phase 0 commit
	ActorBoot           = "boot"            // startup: migrations, boot-time repair, first-run seeding
	ActorImport         = "import"          // the EQdkp importer's commit phase
	ActorAnonymous      = "anonymous"       // an unauthenticated request that reached an audited path
)

// ActorKinds returns every legal audit_log.actor_kind, in the order the CHECK constraint carries
// them.
//
// A FUNCTION returning a FRESH SLICE of the constants above, never a package-level var —
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on. internal/ledger/kinds.BatchKinds() and
// internal/authz.Catalogue() are the same shape for the same reason.
//
// The order is not semantic but it is FIXED: CheckExpr renders in this order, so reordering rewrites
// the CHECK expression, which Atlas sees as a schema change and a migration nobody wanted.
func ActorKinds() []string {
	return []string{
		ActorUser,
		ActorServiceAccount,
		ActorSystem,
		ActorBoot,
		ActorImport,
		ActorAnonymous,
	}
}

// IsActorKind reports whether v is a legal audit_log.actor_kind.
//
// This is the RUNTIME half of the catalogue and the reason it is worth having one:
// internal/ledger/commit.go's validate calls it before opening a transaction, so an actor kind that
// is not in the CHECK is refused by a Go error naming the field and the legal values, rather than by
// SQLite naming a constraint from inside the single write connection — after the batch, its entries
// and its snapshots have already been written and must now be rolled back.
//
// A linear scan over six values, called once per commit. A package-level set would be package-level
// mutable state to save nothing measurable.
func IsActorKind(v string) bool {
	for _, candidate := range ActorKinds() {
		if candidate == v {
			return true
		}
	}

	return false
}

// column is the one column this catalogue governs. Unexported: CheckExpr below is the only thing that
// needs to name it, and a caller wanting the string wants CheckExpr's whole expression.
const column = "actor_kind"

// CheckExpr renders the body of audit_log's actor_kind CHECK constraint:
//
//	actor_kind IN ('user', 'service_account', …)
//
// No column parameter, where internal/ledger/kinds.CheckExpr takes one: that catalogue governs two
// columns and this governs one, so naming it at the call site would be an invitation to render the
// actor kinds against some other column. The formatting itself is internal/schemaenum's, shared with
// every other catalogue so that one separator change cannot rewrite one CHECK and not another.
func CheckExpr() string {
	return schemaenum.CheckExpr(column, ActorKinds())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside
// `table "audit_log"`. Everything between them is written by `make gen`; everything outside them —
// including the neighbouring audit_log_outcome_enum CHECK, which has no catalogue yet — is
// hand-authored schema truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine. The marker text names the catalogue because db/schema.hcl carries more than one generated
// region and each is found by an exact whole-line match on ITS OWN markers: two regions sharing a
// marker line would each rewrite the other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — audit_log.actor_kind CHECK, from internal/audit/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — audit_log.actor_kind CHECK."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason ActorKinds is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "audit_log's actor_kind CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "audit_log"`.
//
// No trailing newline: Replace joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestAuditKinds_CheckMatchesCatalogue fails on.",
		`  check "audit_log_actor_kind_enum" {`,
		fmt.Sprintf(`    expr = %q`, CheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock(),
// and is one of the two rewrites `make gen` composes before writing db/schema.hcl back.
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing" and lets this
// render compose with internal/ledger/kinds' in either order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
