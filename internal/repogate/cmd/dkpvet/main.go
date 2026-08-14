// Command dkpvet is the type-aware second opinion on the architectural laws — the analyzers
// `make lint-laws` runs over a tree that BUILDS, as an additional signal to `make lint-repo`.
//
// It is repo tooling, not product surface, on the same terms as repogate: deliberately NOT a `dkp`
// subcommand, because nothing an operator runs should carry the gates and cmd/dkp stays the only
// shipped binary.
//
//	dkpvet          analyse DKP_REPO_ROOT (default: the working directory)
//
// DKP_REPO_ROOT names the tree to INSPECT, the same contract every gate script here honours. Unlike
// repogate's, that tree has to be a Go module that builds — which is the whole reason this pass is
// the second opinion and repogate is the gate (see internal/repogate/typedlaw's package doc).
//
// MODE=advise (the default) prints findings and exits 0. MODE=enforce exits 1 on a finding. A
// BROKEN INVOCATION — no module, a tree that does not build, a package that does not type-check —
// exits 2 in both modes, because a pass that never ran must not report as one that found nothing.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/prokopto-dev/dragonkillparty/internal/repogate/typedlaw"
)

func main() {
	if len(os.Args) != 1 {
		fail(fmt.Errorf("usage: dkpvet (no arguments; the tree comes from DKP_REPO_ROOT, the mode from MODE)"))
	}

	mode := os.Getenv("MODE")
	if mode == "" {
		mode = "advise"
	}

	if mode != "advise" && mode != "enforce" {
		fail(fmt.Errorf("MODE=%s is not a mode: expected advise or enforce", mode))
	}

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}

	if err := typedlaw.Run(root, os.Stdout, mode == "enforce"); err != nil {
		// A finding has already printed its own report, with the ids that fired. Any other error
		// means the pass could not run at all, and says so — a distinction `go run` would collapse,
		// which is why scripts/typed-laws.sh builds this binary first (ADR-0022).
		if errors.Is(err, typedlaw.ErrFindings) {
			os.Exit(1)
		}

		fail(err)
	}
}

// repoRoot resolves the tree to inspect. An empty DKP_REPO_ROOT is the working directory rather
// than the empty path, matching `${DKP_REPO_ROOT:-...}` in the script that invokes this.
func repoRoot() (string, error) {
	if root := os.Getenv("DKP_REPO_ROOT"); root != "" {
		return root, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve the working directory: %w", err)
	}

	return wd, nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "dkpvet: %v\n", err)
	os.Exit(2)
}
