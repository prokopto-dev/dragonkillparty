package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the ledger enum catalogue — canonical §5's "one Go catalogue" for
// ledger_batch.kind and ledger_batch.source.
//
// It holds VALUES. The rendering and the marked-region rewrite are internal/schemaenum's, shared
// with internal/audit/kinds (audit_log.actor_kind) so that the CHECK expression every catalogue
// emits comes out of one formatter rather than one per table.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY AND internal/schemaenum, which is itself
// such a leaf. That is a hard constraint rather than a tidiness preference. scripts/gen-enums.sh is
// the FIRST step of `make gen` and runs internal/ledger/enumgen, which imports this. When the
// catalogue lived in internal/ledger, that command's dependency graph reached
// internal/store/sqlitegen — GENERATED code — so a tree whose sqlc output was stale, absent or
// momentarily unbuildable could not run `make gen` to repair itself: the first step failed to compile
// on the very artefacts the third step regenerates. TestGen_EnumGenerator_DependsOnNoGeneratedCode
// holds this open. Do not import internal/store, internal/ledger or anything generated from here,
// however convenient it looks.
//
// WHY THIS FILE EXISTS. Before it, the fourteen kinds and six sources were literals in
// db/schema.hcl and nowhere else. A kind added in Go but absent from the CHECK is a legal write that
// the database rejects at COMMIT time — the ledger is append-only, so the failure surfaces as a raid
// night's award vanishing, not as a compile error. Canonical §5 requires the CHECK to be GENERATED
// from a Go const catalogue with "a test asserts the copies agree"; internal/authz/catalogue.go is
// the same mechanism for permission keys, and this is its ledger twin.
//
// THE DIRECTION OF TRUTH IS: this file → db/schema.hcl (via `make gen`) → the migration (via
// `make migration`) → the database. Nothing reads it in the other direction, which is what makes
// adding a kind a one-line change with a red test rather than a four-file edit with three chances to
// forget one.
//
// ADDING A KIND IS STILL A SCHEMA CHANGE. Appending here does not change a deployed database by
// itself: run `make gen` to rewrite the CHECK in db/schema.hcl, then `make migration
// NAME=<snake_case>` to author the migration that rebuilds the constraint, and read what Atlas
// wrote — a CHECK change on SQLite is a 12-step table rebuild, and ledger_batch carries the
// append-only triggers that a rebuild silently drops (.claude/rules/migrations.md). Per
// .claude/rules/ledger-and-strategy.md, a new kind is also an OpenAPI enum and a docs page: stop and
// ask before adding one.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between.
//
// A sentinel rather than a silent no-op, because the no-op is the dangerous answer: a generator that
// cannot find its target and exits 0 leaves the CHECK frozen at whatever the file last said while
// every gate downstream reports success.
//
// It IS schemaenum.ErrMarkersMissing rather than a second sentinel wrapping it: one condition, one
// value, so `errors.Is` gives the same answer whichever name the caller reaches for.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The ledger_batch.kind vocabulary, as the Go const block canonical §5 requires.
//
// Named constants rather than bare literals for two reasons. Canonical §5 says "the enum catalogue is
// a Go const block", and a consumer needs a SYMBOL to reference: `Kind: KindAward` is a compile
// error when it is misspelled, where `Kind: "awrad"` is valid Go that only IsBatchKind catches, and
// only at runtime.
const (
	KindAttendance    = "attendance"      // a raid tick's award
	KindAward         = "award"           // an item award and its zero-sum split
	KindAdjustment    = "adjustment"      // an officer's manual correction of a balance
	KindDecay         = "decay"           // a posted decay run — decay is posted, never computed
	KindCap           = "cap"             // a cap strategy trimming a balance to its ceiling
	KindStartPoints   = "start_points"    // opening points for a new raider
	KindZeroSumCredit = "zero_sum_credit" // a split's credit half, posted apart from its debit
	KindReversal      = "reversal"        // undoes a prior batch; reverses_batch_id is set
	KindCorrection    = "correction"      // the net-delta batch a replay emits instead of rewriting
	KindReAttribution = "re_attribution"  // moves attribution between characters, never a balance
	KindMigration     = "migration"       // written by a schema or data migration
	KindImport        = "import"          // written by the EQdkp importer's commit phase
	KindSeed          = "seed"            // written by `make seed` and the fixture seeder
	KindWriteOff      = "write_off"       // a rotted item's debit, routed to the write_off account
)

// The ledger_batch.source vocabulary. The source is WHERE the write came from, not who authorised
// it — the actor is actor_user_id and actor_token_id. A bot posting through a PAT is SourceAPI
// whether or not a human triggered it.
const (
	SourceWeb     = "web"     // the SPA, on a session
	SourceAPI     = "api"     // the public API, on a PAT or a service-account token
	SourceDiscord = "discord" // the Discord integration
	SourceParser  = "parser"  // an ingested P99 log artefact
	SourceImport  = "import"  // the EQdkp importer
	SourceSystem  = "system"  // the binary itself — jobs, decay cadence, boot-time repair
)

// BatchKinds returns every legal ledger_batch.kind, in the order the CHECK constraint carries them.
//
// A FUNCTION returning a FRESH SLICE of the constants above, never a package-level var —
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on. internal/authz.Catalogue() is the same
// shape for the same reason. The const block and the fresh slice are not in tension: the constants
// are the immutable vocabulary, this is the ordered view of it that renders the CHECK.
//
// The order is not semantic (bid.tier is the one enum in the system whose declaration order is a
// rule, canonical §5) but it is FIXED: CheckExpr renders in this order, so reordering rewrites the
// CHECK expression, which Atlas sees as a schema change and a migration nobody wanted.
func BatchKinds() []string {
	return []string{
		KindAttendance,
		KindAward,
		KindAdjustment,
		KindDecay,
		KindCap,
		KindStartPoints,
		KindZeroSumCredit,
		KindReversal,
		KindCorrection,
		KindReAttribution,
		KindMigration,
		KindImport,
		KindSeed,
		KindWriteOff,
	}
}

// BatchSources returns every legal ledger_batch.source, in the order the CHECK constraint carries
// them. A fresh slice over the constants above, for the reason BatchKinds is.
func BatchSources() []string {
	return []string{
		SourceWeb,
		SourceAPI,
		SourceDiscord,
		SourceParser,
		SourceImport,
		SourceSystem,
	}
}

// IsBatchKind reports whether v is a legal ledger_batch.kind.
//
// This is the RUNTIME half of the catalogue and the reason it is worth having one: Commit.validate
// calls it before opening a transaction, so a kind that is not in the CHECK is refused by a Go error
// naming the field and the legal values, rather than by SQLite naming a constraint from inside the
// single write connection. Without it the catalogue would generate a CHECK that only the database
// ever consults — which is the failure this whole file exists to remove, moved one layer out.
//
// A linear scan over fourteen values, called once per batch. A package-level set would be
// package-level mutable state (.claude/rules/go-idioms.md) to save nothing measurable.
func IsBatchKind(v string) bool {
	return contains(BatchKinds(), v)
}

// IsBatchSource reports whether v is a legal ledger_batch.source. The runtime half of BatchSources,
// for the reason IsBatchKind is.
func IsBatchSource(v string) bool {
	return contains(BatchSources(), v)
}

func contains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}

	return false
}

// CheckExpr renders the body of a SQL CHECK constraint restricting column to values:
//
//	kind IN ('attendance', 'award', …)
//
// Exported because the generator (internal/ledger/enumgen) and the drift test are two callers that
// must agree byte for byte; a second copy of this formatting in either one is exactly the drift this
// file exists to remove. The formatting itself is internal/schemaenum's, shared with every other
// catalogue for the same reason — see that package's comment.
func CheckExpr(column string, values []string) string {
	return schemaenum.CheckExpr(column, values)
}

// The markers delimiting this catalogue's generated region of db/schema.hcl. Everything between them
// is written by `make gen`; everything outside them is hand-authored schema truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine — the generated block has to be semantically identical to what it replaces or `make gen`
// would demand a migration on a change that moved no values.
//
// db/schema.hcl carries more than one generated region now (audit_log's actor_kind CHECK is
// internal/audit/kinds'), and each is found by an exact whole-line match on ITS OWN markers. That is
// why the marker text names the catalogue: two regions sharing a marker line would each rewrite the
// other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — ledger enum CHECKs, from internal/ledger/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — ledger enum CHECKs."
)

// schemaRegion is the marked region this catalogue owns.
//
// A FUNCTION rather than a package-level var, for the reason BatchKinds is one: a package-level
// struct is mutable state (.claude/rules/go-idioms.md), and the value is three strings assembled from
// constants — there is nothing to cache.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the two ledger_batch enum CHECKs",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "ledger_batch"`.
//
// No trailing newline: RenderSchemaHCL joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestLedgerKinds_CheckMatchesCatalogue fails on.",
		`  check "ledger_batch_kind_enum" {`,
		fmt.Sprintf(`    expr = %q`, CheckExpr("kind", BatchKinds())),
		"  }",
		"",
		`  check "ledger_batch_source_enum" {`,
		fmt.Sprintf(`    expr = %q`, CheckExpr("source", BatchSources())),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock(),
// and is one of the two rewrites `make gen` composes before writing db/schema.hcl back.
//
// It rewrites a MARKED REGION rather than pattern-matching the two `expr =` lines, because a regex
// over a schema file cannot tell the CHECK it means from the next one somebody names similarly, and
// because the markers are how a reader of schema.hcl learns those lines are generated at all.
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing", lets
// `make gen` be safe to run at any time, and lets internal/audit/kinds' render compose with this one
// in either order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
