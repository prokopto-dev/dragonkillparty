package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the idempotency_record enum catalogue — canonical §5's "one Go catalogue" for
// idempotency_record.state.
//
// ITS OWN PACKAGE for the constraint .claude/rules/migrations.md states: one region per catalogue,
// matched by the schemaEnumBegin/schemaEnumEnd identifier pair, so one package cannot own two
// regions. It is a subpackage of internal/idempotency for the same reason internal/auth's four are
// subpackages of internal/auth: the table and its replay service are one subsystem.
//
// A LEAF PACKAGE: standard library and internal/schemaenum only, never internal/idempotency. The
// generator that writes this CHECK into db/schema.hcl runs BEFORE sqlc, so a catalogue that reached
// its own service package would reach internal/store/sqlitegen — generated code — and `make gen`
// would be unable to repair a tree whose generated code does not build.
//
// TWO VALUES AND NOT THREE. A record is in flight while its request is executing and complete once a
// response was recorded; there is no `failed`. A request that ends 5xx has its record DELETED rather
// than marked, because docs/api/idempotency-and-concurrency.md promises the retry executes normally
// — a third state would have to mean "retry me", which is what absence already means, and it would
// keep a row that only ever answers "pretend I am not here".

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between. It IS schemaenum.ErrMarkersMissing rather than a second sentinel
// wrapping it: one condition, one value.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The idempotency_record.state vocabulary (docs/api/idempotency-and-concurrency.md).
//
//   - StateInFlight  the request that claimed this key is executing. A duplicate arriving now is
//     409 idempotency_key_in_flight with Retry-After, never a second execution.
//   - StateComplete  a response was recorded. A duplicate arriving now replays it byte for byte.
const (
	StateInFlight = "in_flight"
	StateComplete = "complete"
)

// States returns every legal idempotency_record.state, in the order the CHECK constraint carries
// them. A FRESH SLICE from a function, never a package-level var; the order is fixed because
// CheckExpr renders in it, and reordering rewrites the CHECK.
func States() []string {
	return []string{
		StateInFlight,
		StateComplete,
	}
}

// IsState reports whether v is a legal idempotency_record.state — the runtime half, so a caller
// refuses an unknown state with a Go error rather than having SQLite name a constraint from inside a
// write transaction.
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

// StateCheckExpr renders the body of idempotency_record's state CHECK constraint:
//
//	state IN ('in_flight', 'complete')
func StateCheckExpr() string {
	return schemaenum.CheckExpr(stateColumn, States())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table
// "idempotency_record"`. HCL line comments, so Atlas parses the file unchanged; the marker text names
// the catalogue because each region is found by an exact whole-line match on ITS OWN markers.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — idempotency_record enum CHECK, from internal/idempotency/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — idempotency_record enum CHECK."
)

func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the idempotency_record state CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "idempotency_record"`. No trailing newline: Replace joins it back into the file's
// line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestIdempotencyKinds_CheckMatchesCatalogue fails on.",
		`  check "idempotency_record_state_enum" {`,
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
