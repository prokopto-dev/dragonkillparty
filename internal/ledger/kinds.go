package ledger

import (
	"errors"
	"fmt"
	"strings"
)

// The ledger enum catalogue — canonical §5's "one Go catalogue" for ledger_batch.kind and
// ledger_batch.source.
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
var ErrSchemaMarkersMissing = errors.New("db/schema.hcl generated-region markers not found")

// BatchKinds returns every legal ledger_batch.kind, in the order the CHECK constraint carries them.
//
// A FUNCTION returning a FRESH LITERAL, never a package-level var — .claude/rules/go-idioms.md bans
// package-level mutable state, and a shared slice is one append in a test away from an intermittent
// failure under -shuffle=on. internal/authz.Catalogue() is the same shape for the same reason.
//
// The order is not semantic (bid.tier is the one enum in the system whose declaration order is a
// rule, canonical §5) but it is FIXED: CheckExpr renders in this order, so reordering rewrites the
// CHECK expression, which Atlas sees as a schema change and a migration nobody wanted.
func BatchKinds() []string {
	return []string{
		"attendance",      // a raid tick's award
		"award",           // an item award and its zero-sum split
		"adjustment",      // an officer's manual correction of a balance
		"decay",           // a posted decay run — decay is posted, never computed
		"cap",             // a cap strategy trimming a balance to its ceiling
		"start_points",    // opening points for a new raider
		"zero_sum_credit", // the credit half of a split, when it is posted apart from its debit
		"reversal",        // undoes a prior batch; reverses_batch_id is set
		"correction",      // the net-delta batch a replay emits instead of rewriting history
		"re_attribution",  // moves attribution between characters; never moves a balance
		"migration",       // written by a schema or data migration
		"import",          // written by the EQdkp importer's commit phase
		"seed",            // written by `make seed` and the fixture seeder
		"write_off",       // a rotted item's debit, routed to the write_off system account
	}
}

// BatchSources returns every legal ledger_batch.source, in the order the CHECK constraint carries
// them. A fresh literal, for the reason BatchKinds is.
//
// The source is WHERE the write came from, not who authorised it — the actor is actor_user_id and
// actor_token_id. A bot posting through a PAT is 'api' whether or not a human triggered it.
func BatchSources() []string {
	return []string{
		"web",     // the SPA, on a session
		"api",     // the public API, on a PAT or a service-account token
		"discord", // the Discord integration
		"parser",  // an ingested P99 log artefact
		"import",  // the EQdkp importer
		"system",  // the binary itself — jobs, decay cadence, boot-time repair
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
// file exists to remove.
//
// It does not escape the values, and deliberately so: an enum value is lowercase snake_case by
// canonical §5, TestBatchKinds_Values_AreCanonicalEnumValues enforces that, and a quote-escaping
// path here would be dead code that makes a value containing a quote look supported.
func CheckExpr(column string, values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}

	return fmt.Sprintf("%s IN (%s)", column, strings.Join(quoted, ", "))
}

// The markers delimiting the generated region of db/schema.hcl. Everything between them is written
// by `make gen`; everything outside them is hand-authored schema truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine — the generated block has to be semantically identical to what it replaces or `make gen`
// would demand a migration on a change that moved no values.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — ledger enum CHECKs, from internal/ledger/kinds.go. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — ledger enum CHECKs."
)

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

// RenderSchemaHCL returns src with the generated region replaced by SchemaEnumBlock(), and is what
// `make gen` writes back over db/schema.hcl.
//
// It rewrites a MARKED REGION rather than pattern-matching the two `expr =` lines, because a regex
// over a schema file cannot tell the CHECK it means from the next one somebody names similarly, and
// because the markers are how a reader of schema.hcl learns those lines are generated at all.
//
// Idempotent: rendering an already-current file returns it unchanged, which is what lets the drift
// test be "generating again changes nothing" and lets `make gen` be safe to run at any time.
func RenderSchemaHCL(src string) (string, error) {
	lines := strings.Split(src, "\n")

	begin, end := -1, -1

	for i, line := range lines {
		switch line {
		case schemaEnumBegin:
			if begin >= 0 {
				return "", fmt.Errorf("%w: begin marker appears twice, at lines %d and %d",
					ErrSchemaMarkersMissing, begin+1, i+1)
			}

			begin = i
		case schemaEnumEnd:
			if end >= 0 {
				return "", fmt.Errorf("%w: end marker appears twice, at lines %d and %d",
					ErrSchemaMarkersMissing, end+1, i+1)
			}

			end = i
		}
	}

	if begin < 0 || end < 0 {
		return "", fmt.Errorf("%w: expected both\n  %s\n  %s\nrestore them around the two ledger_batch enum CHECKs",
			ErrSchemaMarkersMissing, schemaEnumBegin, schemaEnumEnd)
	}

	if end < begin {
		return "", fmt.Errorf("%w: end marker at line %d precedes begin marker at line %d",
			ErrSchemaMarkersMissing, end+1, begin+1)
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[:begin]...)
	out = append(out, SchemaEnumBlock())
	out = append(out, lines[end+1:]...)

	return strings.Join(out, "\n"), nil
}
