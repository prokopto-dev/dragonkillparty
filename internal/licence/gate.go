package licence

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ErrViolations is returned by [Run] when the gate found something. The report has already been
// written to the caller's writer by then; the error is the exit status, not the message.
var ErrViolations = errors.New("licence gate failed")

// primaryGrant matches the name of a module's own grant: <NAME> or <NAME>.<ext>, never
// <NAME>-something.
var primaryGrant = regexp.MustCompile(`(?i)^(LICENSE|LICENCE|COPYING|UNLICENSE)(\.[A-Za-z0-9]+)?$`)

// licenceFilePrefixes are the names collected from a module root. Everything matching is read;
// [IsPrimaryGrant] then splits the grant from the auxiliary files.
var licenceFilePrefixes = []string{"LICENSE", "LICENCE", "COPYING", "UNLICENSE", "COPYRIGHT", "NOTICE"}

// IsPrimaryGrant reports whether a file name is a module's OWN licence — the exact conventional
// names, LICENSE, LICENSE.md, COPYING and so on.
//
// A suffixed name such as LICENSE-GO or LICENSE-3RD-PARTY.md is auxiliary by definition: it is
// either a second grant or a bibliography, and in neither case does it define what the module itself
// is licensed under. The two halves are then treated differently — the grant is fully classified,
// the rest is deny-scanned by LIC003 — so this predicate decides which question each file is asked,
// and it is wrong in both directions if it drifts. Reading a bibliography as a grant fails
// modernc.org/memory, which ships a LICENSE-LOGO containing nothing but a URL; missing a suffixed
// file lets the standard dual-licence layout hide a copyleft grant beside a permissive one.
func IsPrimaryGrant(name string) bool { return primaryGrant.MatchString(name) }

// reporter accumulates the gate's verdict while writing its report.
type reporter struct {
	w      io.Writer
	failed bool
}

// printf writes one line of the report.
//
// The write error is deliberately discarded, once, here. This is a CLI whose entire output is this
// report: if stdout is gone there is nothing to report the failure to and nothing to do about it,
// and the alternative is thirty error checks that can only ever end in the same shrug.
func (r *reporter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, format, args...)
}

// violation records a failure against a rule id and prints it, in the same shape as
// scripts/repo-gates.sh: the rule, what it means, then the offending subjects indented beneath.
func (r *reporter) violation(rule, description string, detail ...string) {
	r.failed = true

	r.printf("\033[31mFAIL\033[0m [%s] %s\n", rule, description)

	for _, d := range detail {
		r.printf("  %s\n", d)
	}
}

// Run is the licence gate: it classifies every module in root's runtime graph, then the JavaScript
// dependency graph under web/, writing its report to w.
//
// It returns [ErrViolations] when a rule fired, and an ordinary error when the gate could not run
// at all — a missing toolchain, a missing go.mod, a `go list` that failed or resolved nothing. Both
// are failures; the distinction is whether the report beneath is worth reading.
func Run(root string, w io.Writer) error {
	rep := &reporter{w: w}

	rep.printf("licence gate\n")

	// Vacuous-pass guards. Every input here exists from the moment there is a go.mod, so each of
	// these is an error rather than a skip.
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("the Go toolchain is not installed or not on PATH — run make setup")
	}

	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("no go.mod in %s — the licence gate has nothing to resolve", root)
	}

	modules, err := RuntimeModules(root, GatePlatforms(), "./...")
	if err != nil {
		return err
	}

	// This repository is Apache-2.0 by definition and is not a dependency of itself. Identified by
	// the Main flag, NOT by an empty version: under a go.work file every workspace member also
	// reports an empty version, so a third-party module brought in with `use ./thing` would be
	// skipped as though it were first-party and the gate would print its success banner having
	// examined nothing. GOWORK=off in RuntimeModules closes that too; this is the second lock.
	deps := Dependencies(modules)

	verdicts := make(map[string]int)

	for _, m := range deps {
		if verdict := gateModule(rep, root, m); verdict != "" {
			verdicts[verdict]++
		}
	}

	// A count of zero is NOT an error. A module whose only external dependencies are test-only has
	// an empty runtime graph, which is a true statement about it — and was this repository's own
	// state until modernc.org/sqlite landed. The vacuous-pass path the gate has to avoid is
	// `go list` resolving nothing, which RuntimeModules catches.
	if !rep.failed {
		for _, id := range slices.Sorted(maps.Keys(verdicts)) {
			rep.printf("  %-12s %d\n", id, verdicts[id])
		}

		rep.printf("  \033[32m%d runtime dependencies, all under allowed licences\033[0m\n", len(deps))
	}

	if err := runJS(rep, root); err != nil {
		return err
	}

	if rep.failed {
		rep.printf("\n\033[31mlicence gate failed\033[0m — see the rule ids above.\n")
		rep.printf("This project is Apache-2.0. A copyleft or source-available dependency contaminates the tree\n")
		rep.printf("and breaks its relationship with EQdkp Plus. Do not disable this gate (AGENTS.md); drop the\n")
		rep.printf("dependency, or take it to a human with the licence named.\n")

		return ErrViolations
	}

	return nil
}

// gateModule classifies one runtime dependency, reporting any violation it finds. It returns the
// allowed licence the module was cleared under, or "" when it was not cleared.
func gateModule(rep *reporter, root string, m Module) string {
	ref := m.Path + "@" + m.Version

	dir := m.Dir
	// Under -mod=vendor the module cache is not consulted and Dir is empty, but the licence is
	// sitting in the vendor tree.
	if dir == "" {
		vendored := filepath.Join(root, "vendor", filepath.FromSlash(m.Path))
		if info, err := os.Stat(vendored); err == nil && info.IsDir() {
			dir = vendored
		}
	}

	if dir == "" {
		rep.violation("LIC002", "runtime dependency is not unpacked on disk — run go mod download", ref)

		return ""
	}

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		rep.violation("LIC002", "runtime dependency is not unpacked on disk — run go mod download", ref)

		return ""
	}

	// Module root only. Every licence-shaped file is collected, then split in two: the PRIMARY
	// grant, which is fully classified, and everything else, which LIC003 deny-scans.
	primary, auxiliary, err := licenceFiles(dir)
	if err != nil {
		rep.violation("LIC002", "could not read the module directory", ref+"  ("+dir+")")

		return ""
	}

	// LIC003 — a denied licence in an AUXILIARY licence or notice file.
	//
	// A module's primary LICENSE is its grant. Everything else beside it — LICENSE-GPL,
	// LICENSE-3RD-PARTY.md, NOTICE, COPYRIGHT — is either a second grant in a dual-licensed module
	// or a bibliography of embedded code. Both matter, and neither is classifiable the same way:
	//
	//   * Reading them as grants would fire on every licence a bibliography names, and would fail
	//     modernc.org/memory today, which ships LICENSE-LOGO containing nothing but a URL to a
	//     Wikimedia image page. A gate that goes red on that gets deleted.
	//   * Ignoring them lets a module ship a permissive LICENSE beside its real copyleft grant in
	//     LICENSE-GPL — the standard dual-licence layout — and pass. modernc.org/memory really does
	//     ship LICENSE, LICENSE-GO, LICENSE-MMAP-GO and LICENSE-LOGO.
	//
	// So the denylist alone is applied here, never the allowlist. The question for an auxiliary
	// file is "does this module carry something we cannot ship", not "what is this module licensed
	// under".
	for _, f := range auxiliary {
		text, err := os.ReadFile(f)
		if err != nil {
			rep.violation("LIC002", "could not read a licence file beside the module's own grant",
				ref+"  "+filepath.Base(f))

			continue
		}

		if found := DenyHitsAuxiliary(Normalise(string(text))); len(found) > 0 {
			rep.violation("LIC003",
				"denied licence ("+strings.Join(found, " ")+") in a licence or notice file beside the module's own grant",
				ref+"  "+filepath.Base(f))
		}
	}

	if len(primary) == 0 {
		rep.violation("LIC002", "runtime dependency ships no licence file", ref+"  ("+dir+")")

		return ""
	}

	// EVERY licence file must clear, not merely the first one that happens to be recognisable.
	//
	// A permissive LICENSE beside a copyleft COPYING is a dual-licensed module and the copyleft half
	// still binds. A permissive LICENSE beside a file this gate cannot classify is a module nobody
	// has actually vetted. Taking the first allowed licence as the verdict would wave both through
	// and leave the fail-closed guarantee holding only for modules that ship exactly one licence
	// file — which is not a guarantee, it is a coincidence.
	var (
		combined     Classification
		unidentified []string
		names        []string
	)

	for _, f := range primary {
		base := filepath.Base(f)
		names = append(names, base)

		text, err := os.ReadFile(f)
		if err != nil {
			unidentified = append(unidentified, base)

			continue
		}

		c := Classify(string(text))
		combined.Denied = append(combined.Denied, c.Denied...)
		combined.Recognised = append(combined.Recognised, c.Recognised...)

		if c.Unidentified() {
			unidentified = append(unidentified, base)
		}
	}

	subject := ref + " " + strings.Join(names, " ")

	if len(combined.Denied) > 0 {
		rep.violation("LIC001",
			"runtime dependency under a denied licence ("+strings.Join(dedupe(combined.Denied), " ")+")",
			subject)

		return ""
	}

	if len(unidentified) > 0 {
		rep.violation("LIC002",
			"licence file this gate cannot identify — add it to the denylist in internal/licence, or take it to a human",
			ref+"  unidentified: "+strings.Join(unidentified, " "))

		return ""
	}

	if unvetted := combined.Unvetted(); len(unvetted) > 0 {
		rep.violation("LIC002",
			"licence recognised but not on the allowlist ("+strings.Join(dedupe(unvetted), " ")+") — a human decides",
			subject)

		return ""
	}

	verdict := combined.Verdict()
	if verdict == "" {
		rep.violation("LIC002", "no allowed licence identified", subject)

		return ""
	}

	return verdict
}

// licenceFiles returns the licence-shaped files in a module root, split into the module's own grant
// and everything else. Module root only — maxdepth 1.
func licenceFiles(dir string) (primary, auxiliary []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read module directory %s: %w", dir, err)
	}

	var names []string

	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}

		upper := strings.ToUpper(e.Name())

		for _, prefix := range licenceFilePrefixes {
			if strings.HasPrefix(upper, prefix) {
				names = append(names, e.Name())

				break
			}
		}
	}

	slices.Sort(names)

	for _, name := range names {
		if IsPrimaryGrant(name) {
			primary = append(primary, filepath.Join(dir, name))
		} else {
			auxiliary = append(auxiliary, filepath.Join(dir, name))
		}
	}

	return primary, auxiliary, nil
}

// dedupe removes repeats while preserving order, so a family named by two licence files of the same
// module is reported once.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))

	for _, id := range ids {
		if !seen[id] {
			seen[id] = true

			out = append(out, id)
		}
	}

	return out
}
