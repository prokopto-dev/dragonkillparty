// Command repogate is the repository's architectural gate engine — the rules `make lint-repo`
// runs: the architectural laws, the money rules, the supply-chain pins and the AGPL firewall.
//
// It is repo tooling, not product surface — deliberately NOT a `dkp` subcommand, because nothing an
// operator runs should carry the gates and cmd/dkp stays the only shipped binary.
//
//	repogate        evaluate every rule against DKP_REPO_ROOT (default: the working directory)
//
// DKP_REPO_ROOT names the tree to INSPECT. That override is what lets test/repo point the gates at
// a deliberately tainted tree in t.TempDir() — an unpinned action, a stray sql.Open — and require a
// non-zero exit: such a fixture cannot live inside this repository, because the real
// `make lint-repo` would find it and fail the project's own CI.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/prokopto-dev/dragonkillparty/internal/repogate"
)

func main() {
	if len(os.Args) != 1 {
		fail(fmt.Errorf("usage: repogate (no arguments; the tree comes from DKP_REPO_ROOT)"))
	}

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}

	if err := repogate.Run(root, os.Stdout); err != nil {
		// A violation has already printed its own report, with the rule ids that fired. Any other
		// error means the gates could not run at all, and says so.
		if errors.Is(err, repogate.ErrViolations) {
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
	fmt.Fprintf(os.Stderr, "repogate: %v\n", err)
	os.Exit(2)
}
