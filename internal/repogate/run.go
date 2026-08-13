package repogate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrViolations is returned when at least one rule fired. It is the only error a caller should
// translate into a non-zero exit code without also printing something: the report has already named
// every rule that went red and quoted the lines that did it.
var ErrViolations = errors.New("repo gates failed")

// Run evaluates every rule against the tree at root and writes the report to out.
//
// root is the tree being INSPECTED, which for a negative fixture is a t.TempDir() with nothing in
// it but the taint under test. That is what DKP_REPO_ROOT selects, and it is what makes these rules
// TESTED RATHER THAN TRUSTED: such a fixture cannot live inside this repository, because the real
// `make lint-repo` would find it and fail the project's own CI.
//
// The order is law 1 through law 4, then money, migrations, documentation and supply chain, and the
// AGPL firewall last — the order the rules are written down in AGENTS.md, so that a failing run
// reads top to bottom the way the conventions do.
func Run(root string, out io.Writer) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repo root %s: %w", root, err)
	}

	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return fmt.Errorf("repo root %s is not a directory: %w", abs, statErr)
	}

	rep := &report{w: out}
	scan := newScanner(abs)

	rep.printf("repo gates\n")

	runASTRules(scan, rep)
	runTextRules(scan, rep)
	runMigrationRules(scan, rep)
	runShippedLockRule(scan, rep)
	runEnumRule(scan, rep)
	runADRRule(abs, rep)

	if rep.finish() {
		return ErrViolations
	}

	return nil
}
