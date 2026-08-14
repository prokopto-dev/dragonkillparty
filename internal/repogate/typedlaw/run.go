package typedlaw

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrFindings is returned in ENFORCE mode when at least one law reported. It is the only error a
// caller should translate into a non-zero exit without printing anything more: the report has
// already named every id that fired and quoted the lines that did it.
//
// In advise mode the same findings are printed and Run returns nil, which is what "advisory by
// construction" means here — the mode changes the verdict, never the analysis.
var ErrFindings = errors.New("typed architectural laws reported findings")

// maxHits is how many offending lines one law prints, and the truncation is ANNOUNCED. Same number
// and same reason as internal/repogate's: a law newly pointed at a tree nobody cleaned can match
// hundreds of times, and a screen of them buries the id that says what to do about it.
const maxHits = 20

const (
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

// result is what one law had to say about the whole tree.
type result struct {
	ID   string
	Desc string
	Hits []string
}

// Run builds the tree at root, type-checks it, evaluates every law and writes the report to out.
//
// enforce decides the VERDICT and nothing else. Both modes run the same analysis and print the same
// findings; enforce returns ErrFindings where advise returns nil. A broken invocation — the tree did
// not build, a package did not type-check — returns a different error in BOTH modes, because a pass
// that never ran must not report as a pass that found nothing.
func Run(root string, out io.Writer, enforce bool) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repo root %s: %w", root, err)
	}

	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return fmt.Errorf("repo root %s is not a directory: %w", abs, statErr)
	}

	pkgs, err := load(abs)
	if err != nil {
		return fmt.Errorf("load the buildable tree at %s: %w", abs, err)
	}

	results := evaluate(pkgs)

	return report(out, results, len(pkgs), enforce)
}

// evaluate runs every law over every package and returns one result per law that fired.
//
// Separate from the printing so that the package's own tests can assert on findings
// rather than on formatted text — the assertion internal/repogate's rules get from being ordinary
// Go, and the reason both engines are Go rather than shell (ADR-0018).
func evaluate(pkgs []*pkg) []result {
	var results []result

	for _, l := range laws() {
		var hits []string

		for _, p := range pkgs {
			hits = append(hits, l.run(p)...)
		}

		if len(hits) == 0 {
			continue
		}

		// Sorted and de-duplicated: the type checker's maps iterate in a random order, and a report
		// that reshuffles itself between two runs over the same tree is one nobody can diff.
		slices.Sort(hits)
		hits = slices.Compact(hits)

		results = append(results, result{ID: l.id, Desc: l.desc, Hits: hits})
	}

	return results
}

// report writes the human-readable form and decides the verdict.
func report(out io.Writer, results []result, packages int, enforce bool) error {
	printf := func(format string, args ...any) {
		// The write error is deliberately discarded, once, here — the same waiver
		// internal/repogate's reporter takes. This is a command whose entire output is this report:
		// if stdout is gone there is nothing to report the failure to.
		_, _ = fmt.Fprintf(out, format, args...)
	}

	printf("typed architectural laws — %d package(s) of the main module\n", packages)

	if len(results) == 0 {
		printf("  %sno findings%s — the type-aware pass agrees with `make lint-repo`\n", ansiGreen, ansiReset)

		return nil
	}

	for _, r := range results {
		kind, colour := "WARN", ansiYellow
		if enforce {
			kind, colour = "FAIL", ansiRed
		}

		printf("%s%s%s [%s] %s\n", colour, kind, ansiReset, r.ID, r.Desc)

		hits, elided := r.Hits, 0
		if len(hits) > maxHits {
			elided = len(hits) - maxHits
			hits = hits[:maxHits]
		}

		for _, hit := range hits {
			printf("  %s\n", strings.TrimRight(hit, "\n"))
		}

		if elided > 0 {
			printf("  … and %d more\n", elided)
		}
	}

	if enforce {
		printf("\n%styped architectural laws failed%s — see the rule ids above.\n", ansiRed, ansiReset)
		printf("These are structural rules, not style. Do not disable one to land a change (AGENTS.md).\n")

		return ErrFindings
	}

	// A GitHub Actions annotation, so the findings surface on the PR's Files-changed tab rather than
	// only in a log nobody opens. Outside Actions it is a harmless line of text.
	printf("::warning title=typed architectural laws::%d law(s) reported. ADVISORY (issue #172) — "+
		"this does not block the merge. `make lint-repo` is the gate; this pass sees what a syntax "+
		"rule cannot, so a finding here is a law broken in a way nothing else will catch.\n", len(results))
	printf("  %styped laws: advisory%s — the findings above did NOT fail the build\n", ansiYellow, ansiReset)

	return nil
}
