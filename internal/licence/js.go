package licence

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// jsAllowedIDs is the JavaScript allowlist: the Go set, plus BSD's SPDX spellings, plus two
// reviewed additions that appear in the current lock — Python-2.0 (argparse) and CC-BY-4.0
// (caniuse-lite's data). As in the Go half the default is DENY: an id not named here is a human
// decision, never a silent pass.
var jsAllowedIDs = []string{
	"Apache-2.0", "MIT", "ISC", "MPL-2.0", "CC0-1.0", "Unlicense", "Zlib",
	"BSD-2-Clause", "BSD-3-Clause", "0BSD", "BSD",
	"Python-2.0", "CC-BY-4.0",
}

// JSAllowedIDs returns the JavaScript allowlist.
func JSAllowedIDs() []string { return slices.Clone(jsAllowedIDs) }

// JSAllowed reports whether one SPDX id is on the JavaScript allowlist.
func JSAllowed(id string) bool { return slices.Contains(jsAllowedIDs, id) }

// JSExpressionAllowed reports whether an SPDX EXPRESSION is allowed — true iff EVERY token in it is.
//
// For an AND that is exactly right: both grants bind. For an OR the recipient could pick just one
// branch, so requiring all is STRICTER than SPDX demands; that is a deliberate fail-closed choice,
// not a correctness claim. It can only over-deny (forcing a human to review, e.g. an
// "(MIT OR GPL-3.0)" dependency), never admit a copyleft branch — a GPL inside an OR is denied,
// which is the security-critical direction. "(MIT OR CC0-1.0)" passes because BOTH branches are
// permissive, so the strict rule and the lenient rule agree on the one such key in the lock today.
//
// A WITH exception ("GPL-2.0 WITH Classpath-exception") collapses to its base, which is denied —
// the safe direction, since no such licence is expected here.
func JSExpressionAllowed(expr string) bool {
	for _, tok := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(expr)) {
		switch tok {
		case "OR", "AND", "WITH":
			continue
		}

		if !JSAllowed(tok) {
			return false
		}
	}

	return true
}

// jsPackage is one entry of `pnpm licenses list --json`, which emits an object keyed by SPDX id
// with an array of packages under each.
type jsPackage struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
}

// runJS classifies the SPA's dependency graph, appending to the report rep is building.
//
// Scope: the WHOLE dependency graph, not just `--prod`. The Go gate scopes to the runtime graph
// because a Go test-only import is genuinely not linked into dkp. npm has no such boundary that
// `pnpm licenses list` exposes reliably per-package, and a devDependency's licence still ships in
// the lockfile and runs on every contributor's machine and in CI. Failing closed over all ~200 is
// the safe direction; a narrower scope would be a hole nobody could see. `--prod` alone reports only
// the runtime packages (all MIT today), which would leave the other ~190 exactly as unchecked as
// before this section existed.
//
// It DEGRADES rather than dies when pnpm is absent. The Go half is toolchain-mandatory: `go` is
// required for the whole build, so a missing Go is a real error. pnpm is not. It is installed for
// the web-facing CI jobs (`node: "true"`) but NOT for `test / unit` or `test / integration`, whose
// runners have only Go — and those two jobs run test/repo/licence_gate_test.go, which drives this
// gate against the real tree on a box that cannot resolve the JS graph. The authoritative JS
// enforcement lives in the `security / licences` CI job, which installs node+pnpm and runs
// UNCONDITIONALLY, on every PR, gated by no path filter (see .github/workflows/ci.yml) — so a change
// here cannot arrange for it not to run.
//
// The classification itself no longer needs node. The shell this replaced piped `pnpm licenses list
// --json` through an inline `node -e` script to reshape it; encoding/json does that here, and node
// remains a requirement only because pnpm is a node program.
func runJS(rep *reporter, root string) error {
	web := filepath.Join(root, "web")

	if _, err := os.Stat(filepath.Join(web, "package.json")); err != nil {
		return nil
	}

	rep.printf("\nlicence gate — JavaScript (web/)\n")

	if _, err := exec.LookPath("pnpm"); err != nil {
		rep.printf("  note: skipping JS dependency licences — pnpm not installed (enforced by the security/licences CI job)\n")

		return nil
	}

	// The generator is not needed, but the graph must be resolved on disk for `pnpm licenses list`
	// to read each package's licence. Same frozen/no-scripts posture as the build and CI.
	if _, err := os.Stat(filepath.Join(web, "node_modules")); err != nil {
		install := exec.Command("pnpm", "install", "--frozen-lockfile", "--ignore-scripts")
		install.Dir = web

		if out, err := install.CombinedOutput(); err != nil {
			return fmt.Errorf("pnpm install in %s: %w\n%s", web, err, indent(string(out)))
		}
	}

	// pnpm exits non-zero when it finds licences it considers problematic, and this gate does its
	// own classification below rather than trusting that verdict — so the exit status is ignored and
	// an EMPTY result is what must be caught, as a failure rather than a vacuous pass.
	list := exec.Command("pnpm", "licenses", "list", "--json")
	list.Dir = web

	stdout, err := list.Output()
	if len(stdout) == 0 {
		if err != nil {
			return fmt.Errorf("pnpm licenses list in %s produced no output — the JS dependency set could not be read: %w", web, err)
		}

		return errors.New("pnpm licenses list produced no output — the JS dependency set could not be read")
	}

	var report map[string][]jsPackage
	if err := json.Unmarshal(stdout, &report); err != nil {
		return fmt.Errorf("parse pnpm licenses output: %w", err)
	}

	total := 0
	for _, pkgs := range report {
		total += len(pkgs)
	}

	if total == 0 {
		return errors.New("the JS dependency graph resolved to nothing — the gate examined no packages")
	}

	counts := make(map[string]int, len(report))

	for _, id := range slices.Sorted(maps.Keys(report)) {
		pkgs := report[id]

		names := make([]string, 0, len(pkgs))
		for _, p := range pkgs {
			names = append(names, p.Name+"@"+strings.Join(p.Versions, "/"))
		}

		subject := strings.Join(names, " ")

		// An empty, "Unknown" or "UNLICENSED" id is a package pnpm could not classify — deny, as
		// LIC002 does for Go. Fail closed: an unidentifiable licence is never a silent pass.
		switch id {
		case "", "Unknown", "UNLICENSED":
			rep.violation("LIC002", "JS dependency with no identifiable licence", subject)

			continue
		}

		if !JSExpressionAllowed(id) {
			rep.violation("LIC001",
				"JS dependency under a licence not on the allowlist ("+id+") — a human decides", subject)

			continue
		}

		counts[id] = len(pkgs)
	}

	if !rep.failed {
		for _, id := range slices.Sorted(maps.Keys(counts)) {
			rep.printf("  %-16s %d\n", id, counts[id])
		}

		rep.printf("  \033[32m%d JS dependencies, all under allowed licences\033[0m\n", total)
	}

	return nil
}
