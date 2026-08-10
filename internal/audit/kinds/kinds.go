package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the audit_log enum catalogue — canonical §5's "one Go catalogue" for
// audit_log.actor_kind and audit_log.outcome.
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
// WHY outcome IS HERE TOO, and why it arrived a PR later. Its three values had no Go list at all
// (#53): db/schema.hcl's CHECK was the only place they were written, and internal/ledger/commit.go —
// the one writer — passed 'success' as a bare literal. That is the weaker defect, because there was
// no second list to drift from; it is worth closing now rather than later because domain model §17
// has the Phase 2 HTTP middleware writing an audit row for every mutating action, with 'denied' and
// 'error' as its whole point. That writer will name the three values somewhere, and without a
// catalogue it will name them as literals — which is exactly how the validActorKinds map above came
// to exist. A literal is not a symbol: `Outcome: "sucess"` is valid Go that reaches the database and
// fails a CHECK inside the commit transaction, where OutcomeSuccess is a compile error.
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

// The audit_log.outcome vocabulary: WHAT HAPPENED when the actor tried. A denied or errored action is
// audited too — the forensic question "who TRIED this?" is at least as important as "who did it", and
// a table that recorded only successes would answer neither during an incident.
//
// The distinction between the two failures is the one a reader needs: 'denied' is the system working
// (authorisation refused the action), 'error' is the system failing (it tried and could not). Merging
// them would make "are we under attack?" and "are we broken?" the same query.
const (
	OutcomeSuccess = "success" // the action completed and its state change is committed
	OutcomeDenied  = "denied"  // authorisation refused it; nothing changed
	OutcomeError   = "error"   // it was permitted and failed
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

// Outcomes returns every legal audit_log.outcome, in the order the CHECK constraint carries them. A
// fresh slice over the constants above, for the reason ActorKinds is one.
func Outcomes() []string {
	return []string{
		OutcomeSuccess,
		OutcomeDenied,
		OutcomeError,
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
func IsActorKind(v string) bool { return contains(ActorKinds(), v) }

// IsOutcome reports whether v is a legal audit_log.outcome. The runtime half of Outcomes, for the
// reason IsActorKind is.
//
// It has no caller in production code today — internal/ledger/commit.go names OutcomeSuccess, a
// constant, so there is nothing to validate — and it exists for the Phase 2 middleware, which will
// map a request's fate onto this vocabulary and needs somewhere to refuse a value that is not in it.
func IsOutcome(v string) bool { return contains(Outcomes(), v) }

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
	actorKindColumn = "actor_kind"
	outcomeColumn   = "outcome"
)

// ActorKindCheckExpr renders the body of audit_log's actor_kind CHECK constraint:
//
//	actor_kind IN ('user', 'service_account', …)
//
// Named for its column rather than taking one as a parameter, where internal/ledger/kinds.CheckExpr
// takes one: naming the column at the call site would be an invitation to render the actor kinds
// against some other column, which is a CHECK that compiles, applies and rejects every row. It was
// simply `CheckExpr` while this catalogue governed one column; outcome is the second, and an
// unqualified name for one of two is worse than either. The formatting itself is
// internal/schemaenum's, shared with every other catalogue so that one separator change cannot
// rewrite one CHECK and not another.
func ActorKindCheckExpr() string {
	return schemaenum.CheckExpr(actorKindColumn, ActorKinds())
}

// OutcomeCheckExpr renders the body of audit_log's outcome CHECK constraint:
//
//	outcome IN ('success', 'denied', 'error')
//
// NOT the nullable form: the column is NOT NULL, because an audit row that does not say how the
// action ended is not an audit row.
func OutcomeCheckExpr() string {
	return schemaenum.CheckExpr(outcomeColumn, Outcomes())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside
// `table "audit_log"`. Everything between them is written by `make gen`; everything outside them is
// hand-authored schema truth.
//
// ONE REGION FOR BOTH CHECKS, as internal/ledger/kinds has one for ledger_batch's kind and source.
// The alternative — a region per column — would make this catalogue's render two rewrites of one file
// to buy nothing: the two CHECKs are adjacent, they are written by the same `make gen` step from the
// same package, and a reader who finds one generated wants to know the other is too. The marker text
// therefore names the CATALOGUE and not the column, and it changed when outcome joined (#53).
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine. The marker text names the catalogue because db/schema.hcl carries more than one generated
// region and each is found by an exact whole-line match on ITS OWN markers: two regions sharing a
// marker line would each rewrite the other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — audit_log enum CHECKs, from internal/audit/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — audit_log enum CHECKs."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason ActorKinds is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the two audit_log enum CHECKs",
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
		fmt.Sprintf(`    expr = %q`, ActorKindCheckExpr()),
		"  }",
		"",
		`  check "audit_log_outcome_enum" {`,
		fmt.Sprintf(`    expr = %q`, OutcomeCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock(),
// and is one of the three rewrites `make gen` composes before writing db/schema.hcl back.
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing" and lets this
// render compose with internal/ledger/kinds' in either order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
