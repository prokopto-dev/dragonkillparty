// Package schemaenum is the MECHANISM half of canonical §5's "one Go catalogue": the SQL CHECK
// renderer and the marked-region rewrite that `make gen` applies to db/schema.hcl.
//
// The VALUES live in the catalogue that owns the table — internal/ledger/kinds for ledger_batch,
// internal/audit/kinds for audit_log — and nothing in here knows a single enum value. The split is
// what lets a second catalogue exist at all: before it, CheckExpr and the region rewrite lived in
// internal/ledger/kinds, so audit_log's catalogue would have had to either import the ledger's (a
// dependency from audit to ledger that no reader could justify) or copy the formatter. A second copy
// of `%s IN ('a', 'b')` that has to agree with the first BYTE FOR BYTE is the same
// two-sources-of-truth defect the catalogues exist to remove, moved one layer down: the day the
// separator or the quoting differs by a space, `make gen` rewrites a CHECK, Atlas sees a schema
// change, and a migration nobody wanted rebuilds a table carrying the append-only triggers.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY, and that is a hard constraint rather than
// a tidiness preference. scripts/gen-enums.sh is the FIRST step of `make gen` and runs
// internal/ledger/enumgen, which reaches this through both catalogues. Anything it compiles must
// build BEFORE sqlc runs, or a tree whose generated code does not build can no longer run `make gen`
// to repair itself. TestGen_EnumGenerator_DependsOnNoGeneratedCode holds that open.
package schemaenum

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMarkersMissing reports that db/schema.hcl no longer carries the markers a Region rewrites
// between.
//
// A sentinel rather than a silent no-op, because the no-op is the dangerous answer: a generator that
// cannot find its target and exits 0 leaves the CHECK frozen at whatever the file last said while
// every gate downstream reports success.
var ErrMarkersMissing = errors.New("db/schema.hcl generated-region markers not found")

// CheckExpr renders the body of a SQL CHECK constraint restricting column to values:
//
//	kind IN ('attendance', 'award', …)
//
// THE ONE FORMATTER. Every generated CHECK in db/schema.hcl comes through here, including the ", "
// separator — which is what makes a regenerated expression identical to the one that shipped and
// therefore migration-free. The generators and the drift tests are separate callers that must agree
// with the committed schema exactly, so there is deliberately nowhere else to render one.
//
// It does not escape the values, and deliberately so: an enum value is lowercase snake_case by
// canonical §5, each catalogue's own test enforces that, and a quote-escaping path here would be
// dead code that makes a value containing a quote look supported.
func CheckExpr(column string, values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}

	return fmt.Sprintf("%s IN (%s)", column, strings.Join(quoted, ", "))
}

// Region is one generated region of db/schema.hcl: the two marker lines and the subject named when
// they are missing.
//
// The markers are HCL line comments, so Atlas parses the file unchanged and the region is invisible
// to the diff engine — a generated block has to be semantically identical to what it replaces or
// `make gen` would demand a migration on a change that moved no values.
//
// A VALUE TYPE with no constructor: a catalogue declares its Region as a package-level const-backed
// literal, which is not mutable state (.claude/rules/go-idioms.md) and reads at the point of use.
type Region struct {
	// Begin and End are the marker lines, matched WHOLE. A whole-line match rather than a prefix or a
	// regex, because the file is HCL and a comment that merely starts the same way — someone
	// documenting the markers in a neighbouring comment, say — must not be mistaken for one.
	Begin string
	End   string

	// Subject names what the markers surround, for the error a missing marker produces: "the two
	// ledger_batch enum CHECKs". It is read by a human repairing db/schema.hcl by hand, which is the
	// only situation in which it is ever printed.
	Subject string
}

// Replace returns src with the region's contents replaced by block, markers included.
//
// It rewrites a MARKED REGION rather than pattern-matching the `expr =` lines, because a regex over a
// schema file cannot tell the CHECK it means from the next one somebody names similarly, and because
// the markers are how a reader of schema.hcl learns those lines are generated at all.
//
// Idempotent: replacing an already-current region returns src unchanged, which is what lets a drift
// test be "generating again changes nothing" and lets `make gen` be safe to run at any time.
//
// block carries its own markers and no trailing newline — it is joined back into the file's line
// stream, so a trailing newline here would grow the file by one blank line per run.
func (r Region) Replace(src, block string) (string, error) {
	lines := strings.Split(src, "\n")

	begin, end := -1, -1

	for i, line := range lines {
		switch line {
		case r.Begin:
			if begin >= 0 {
				return "", fmt.Errorf("%w: begin marker appears twice, at lines %d and %d",
					ErrMarkersMissing, begin+1, i+1)
			}

			begin = i
		case r.End:
			if end >= 0 {
				return "", fmt.Errorf("%w: end marker appears twice, at lines %d and %d",
					ErrMarkersMissing, end+1, i+1)
			}

			end = i
		}
	}

	if begin < 0 || end < 0 {
		return "", fmt.Errorf("%w: expected both\n  %s\n  %s\nrestore them around %s",
			ErrMarkersMissing, r.Begin, r.End, r.Subject)
	}

	if end < begin {
		return "", fmt.Errorf("%w: end marker at line %d precedes begin marker at line %d",
			ErrMarkersMissing, end+1, begin+1)
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[:begin]...)
	out = append(out, block)
	out = append(out, lines[end+1:]...)

	return strings.Join(out, "\n"), nil
}
