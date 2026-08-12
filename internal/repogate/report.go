package repogate

import (
	"fmt"
	"io"
	"strings"
)

// maxHits is how many offending lines one rule prints. A rule that has just been installed against
// a tree nobody cleaned yet can match hundreds of times, and a screen of them buries the rule id
// that says what to do about it.
//
// Truncation is ANNOUNCED. The shell script capped the rules that went through its `gate` helper at
// twenty and left the hand-written blocks — the AGPL firewall among them — unbounded, so unifying
// the cap would have quietly shortened the firewall's report. A trailing count is what makes the
// unification honest: nothing is hidden, and "and 43 more" is the number that tells you this is a
// tree to clean rather than a line to fix.
const maxHits = 20

// The ANSI sequences the shell script used, kept byte-identical so a CI log reads the same across
// the move. They are written unconditionally rather than behind an isatty check: the gates run in
// CI far more often than in a terminal, and GitHub's log viewer renders them.
const (
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

// report accumulates what the run has to say and whether anything failed.
//
// The failure FORMAT is load-bearing, not cosmetic. test/repo's requireOnlyRule parses it — it
// takes each line containing FAIL, finds the first `]`, and reads the rule id out of the `[…]`
// before it — so that a fixture can assert "exactly one rule fired, and it is this one" rather than
// the much weaker "the gates went red". A reformat that moved the id or added a bracket ahead of it
// would make several fixtures prove nothing while still passing.
type report struct {
	w      io.Writer
	failed bool
}

// printf writes one line of the report.
//
// The write error is deliberately discarded, once, here — the same waiver internal/licence's
// reporter takes and for the same reason. This is a command whose entire output is this report: if
// stdout is gone there is nothing to report the failure to and nothing to do about it, and the
// alternative is thirty error checks that can only ever end in the same shrug.
func (r *report) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, format, args...)
}

// note prints a non-fatal line: a skip, or the output of a delegated check.
//
// Every skip carries its rule id for the reason given in the package doc — a gate that vanishes
// silently is indistinguishable in a CI log from a gate that ran.
func (r *report) note(kind, msg string) {
	r.printf("  %s%s%s %s\n", ansiYellow, kind, ansiReset, msg)
}

// skip records that a rule did not run because the tree it gates does not exist yet.
func (r *report) skip(id, tree string) {
	r.note("skip", fmt.Sprintf("[%s] %s does not exist yet", id, tree))
}

// print writes a line through unchanged. Used for the output of a delegated check that passed.
func (r *report) print(s string) {
	r.printf("%s\n", strings.TrimRight(s, "\n"))
}

// violation records a failed rule and prints its hits, indented, capped at maxHits.
func (r *report) violation(id, desc string, hits []string) {
	r.failed = true

	r.printf("%sFAIL%s [%s] %s\n", ansiRed, ansiReset, id, desc)

	elided := 0
	if len(hits) > maxHits {
		elided = len(hits) - maxHits
		hits = hits[:maxHits]
	}

	for _, hit := range hits {
		// A hit may itself be several lines — ADR001's trigger list and MIG003's delegated report
		// both are — and every line of it is indented, exactly as the shell's `sed 's/^/  /'` did.
		for _, line := range strings.Split(strings.TrimRight(hit, "\n"), "\n") {
			r.printf("  %s\n", line)
		}
	}

	if elided > 0 {
		r.printf("  … and %d more\n", elided)
	}
}

// finish prints the closing summary and reports whether the run failed.
func (r *report) finish() bool {
	if r.failed {
		r.printf("\n%srepo gates failed%s — see the rule ids above.\n", ansiRed, ansiReset)
		r.printf("These are structural rules, not style. Do not disable one to land a change (AGENTS.md).\n")

		return true
	}

	r.printf("  %srepo gates passed%s\n", ansiGreen, ansiReset)

	return false
}
