package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the decay_run enum catalogue — canonical §5's "one Go catalogue" for
// decay_run.state.
//
// WHY THIS FILE EXISTS BEFORE ITS WRITER DOES. decay_run (docs/design/01-domain-model.md §12.3) is
// the table that makes "decay is posted, not computed" true: a decay run is a ROW with a lifecycle —
// planned, previewed, committed — not a number computed inside Spendable(). The state column is the
// lifecycle, and the moment it is written as a literal CHECK in db/schema.hcl it becomes a
// vocabulary with no symbol: the Phase 3 decay job would then name 'committed' as a bare string, the
// API would name it again in a DTO, and the three lists would drift the way ledger_batch.kind (#29),
// audit_log.actor_kind (#40) and account.kind (#53) each did before their catalogue existed. ENUM001
// in internal/repogate is the machine half of that sentence and refuses the literal outright.
//
// WHY internal/decay AND NOT internal/ledger/kinds OR internal/strategy. decay_run is not a ledger
// table — it is the SCHEDULE that decides when a ledger batch gets posted, and the batch it produces
// is an ordinary `kind = 'decay'` row the ledger already knows about. It is not the strategy's
// either: internal/strategy is pure (law 3) and computes what a decay would do, while the state
// column describes what an operator and a job actually did about it. Both sides need to name the
// vocabulary — the job to advance a run, the API to render one — and neither owns it, which is the
// same argument internal/account/kinds records for account.system_key.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY AND internal/schemaenum, which is itself
// such a leaf. That is a hard constraint rather than a tidiness preference: scripts/gen-enums.sh is
// the FIRST step of `make gen` and runs internal/ledger/enumgen, which reaches this. Anything it
// compiles must build BEFORE sqlc runs, or a tree whose generated code does not build can no longer
// run `make gen` to repair itself. TestGen_EnumGenerator_DependsOnNoGeneratedCode holds that open.
// So the Phase 3 decay service — which needs internal/store and internal/jobs — lands beside this
// package in internal/decay and never inside it.
//
// THE DIRECTION OF TRUTH IS: this file → db/schema.hcl (via `make gen`) → the migration (via
// `make migration`) → the database, and separately this file → the decay job's and the API's
// constants. Nothing reads it in the other direction.
//
// ADDING A STATE IS A SCHEMA CHANGE. Appending here does not change a deployed database: run
// `make gen` to rewrite the CHECK in db/schema.hcl, then `make migration NAME=<snake_case>` and read
// what Atlas wrote — a CHECK change on SQLite is the 12-step table rebuild
// (.claude/rules/migrations.md). It is also an OpenAPI enum the day POST /decay-runs exists
// (docs/design/02-api-design.md:348).

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between.
//
// It IS schemaenum.ErrMarkersMissing rather than a second sentinel wrapping it: one condition, one
// value, so `errors.Is` gives the same answer whichever name the caller reaches for.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The decay_run.state vocabulary, as the Go const block canonical §5 requires: HOW FAR a scheduled
// decay period has got. The order is the lifecycle, then its two terminal states.
//
//   - StatePlanned   the period exists and is due. The column's DEFAULT, and the row a periodic job
//     writes when it notices a cadence boundary has passed.
//   - StatePreview   a dry run has been computed into dry_run_result_json and nobody has committed
//     it. Decay is previewable BEFORE it moves anybody's points, which is the half of
//     EQdkp's APA rules that lived in a PHP file outside the database.
//   - StateCommitted the ledger batch has been posted; ledger_batch_id names it. TERMINAL — the
//     ledger is append-only, so a committed run is undone by a reversal batch and a NEW
//     run is never a repair primitive.
//   - StateSkipped   an officer deliberately let the period pass. Terminal, and DISTINCT from a run
//     that never existed: the unique index means the period can never be run later, so
//     "we skipped December" has to be a row that says so rather than an absence.
//   - StateFailed    the run was attempted and did not post. Terminal; `error` carries why.
//
// A row is never deleted to re-run a period — ux_decay_period is the idempotency
// (docs/design/00-canonical-conventions.md §10: "decay is posted, not computed ... idempotency key
// (pool_id, cadence_period)"), and deleting the row to try again is exactly the double-decay that
// key exists to prevent.
const (
	StatePlanned   = "planned"
	StatePreview   = "preview"
	StateCommitted = "committed"
	StateSkipped   = "skipped"
	StateFailed    = "failed"
)

// States returns every legal decay_run.state, in the order the CHECK constraint carries them.
//
// A FUNCTION returning a FRESH SLICE of the constants above, never a package-level var —
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on. internal/account/kinds.Kinds() and
// internal/audit/kinds.ActorKinds() are the same shape for the same reason.
//
// The order is the lifecycle's and it is FIXED: CheckExpr renders in this order, so reordering
// rewrites the CHECK expression, which Atlas sees as a schema change and a migration nobody wanted.
func States() []string {
	return []string{
		StatePlanned,
		StatePreview,
		StateCommitted,
		StateSkipped,
		StateFailed,
	}
}

// DefaultState is the value db/schema.hcl gives decay_run.state, and the state a run is born in.
//
// Named rather than left as a bare `default = "planned"` in the schema with a matching literal in Go:
// the column default is a SECOND spelling of a catalogue value, in a file the generator does not
// rewrite (it is a column attribute, not a check block, so ENUM001 does not read it either).
// TestDecayKinds_SchemaDefault_MatchesTheCatalogue is what ties the two together.
func DefaultState() string { return StatePlanned }

// IsState reports whether v is a legal decay_run.state.
//
// The RUNTIME half of the catalogue: the Phase 3 decay job can refuse a bad state with a Go error
// naming the legal values, rather than have SQLite name a constraint from inside a write transaction
// that has already done work. There is no decay_run writer yet — this exists so the first one has
// something to call instead of a literal, which is how every defect the package comment lists came to
// exist.
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

// StateCheckExpr renders the body of decay_run's state CHECK constraint:
//
//	state IN ('planned', 'preview', 'committed', 'skipped', 'failed')
//
// The PLAIN form, not the nullable one: the column is NOT NULL with a default, so there is no NULL
// arm to admit. Named for its column, as internal/account/kinds' two renderers are, so a second
// vocabulary on this table cannot be rendered against the wrong one.
func StateCheckExpr() string {
	return schemaenum.CheckExpr(stateColumn, States())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside
// `table "decay_run"`. Everything between them is written by `make gen`; everything outside them is
// hand-authored schema truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine. The marker text names the catalogue because db/schema.hcl carries several generated regions
// and each is found by an exact whole-line match on ITS OWN markers: two regions sharing a marker line
// would each rewrite the other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — decay_run enum CHECK, from internal/decay/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — decay_run enum CHECK."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason States is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the decay_run state CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "decay_run"`.
//
// No trailing newline: Replace joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestDecayKinds_CheckMatchesCatalogue fails on.",
		`  check "decay_run_state_enum" {`,
		fmt.Sprintf(`    expr = %q`, StateCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock(),
// and is one of the four rewrites `make gen` composes before writing db/schema.hcl back.
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing" and lets this
// render compose with the other catalogues' in any order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
