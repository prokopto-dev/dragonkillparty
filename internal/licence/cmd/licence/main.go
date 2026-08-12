// Command licence is the repository's dependency licence tooling: the gate `make lint` runs, and
// the runtime-graph enumeration scripts/third-party-notices.sh generates its attribution file from.
//
// It is repo tooling, not product surface — it is deliberately NOT a `dkp` subcommand, because
// nothing an operator runs should carry the gate, and cmd/dkp stays the only shipped binary.
//
//	licence gate       classify the runtime module graph and web/'s dependencies (LIC001/002/003)
//	licence modules    print the runtime module union as "path\tversion\tdir", one per line
//
// Both read the tree named by DKP_REPO_ROOT, defaulting to the working directory. That override is
// what lets test/repo/licence_gate_test.go point the gate at a fabricated GPL module tree in
// t.TempDir(): such a fixture cannot live inside this repo, because the real `make licence-gate`
// would find it and fail the project's own CI.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/prokopto-dev/dragonkillparty/internal/licence"
)

func main() {
	if len(os.Args) != 2 {
		fail(errors.New("usage: licence <gate|modules>"))
	}

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}

	switch os.Args[1] {
	case "gate":
		if err := licence.Run(root, os.Stdout); err != nil {
			// A violation has already printed its own report, with the rule ids that fired. Any
			// other error means the gate could not run at all, and says so.
			if errors.Is(err, licence.ErrViolations) {
				os.Exit(1)
			}

			fail(err)
		}
	case "modules":
		if err := printModules(root); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown subcommand %q — usage: licence <gate|modules>", os.Args[1]))
	}
}

// repoRoot resolves the tree to inspect. An empty DKP_REPO_ROOT is the working directory rather
// than the empty path, matching `${DKP_REPO_ROOT:-...}` in the scripts this replaced.
func repoRoot() (string, error) {
	root := os.Getenv("DKP_REPO_ROOT")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve the working directory: %w", err)
		}

		return wd, nil
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve DKP_REPO_ROOT %s: %w", root, err)
	}

	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("DKP_REPO_ROOT %s is not a directory", abs)
	}

	return abs, nil
}

// printModules writes the runtime module union for the release platforms, tab-separated, for
// scripts/third-party-notices.sh to walk. Scoped to ./cmd/dkp because attribution is owed for what
// the binary links, and unioned across every release platform because the shipped darwin and
// windows binaries must carry the notices for what THEY link.
func printModules(root string) error {
	modules, err := licence.RuntimeModules(root, licence.ReleasePlatforms(), "./cmd/dkp")
	if err != nil {
		return err
	}

	seen := make(map[string]bool)

	for _, m := range licence.Dependencies(modules) {
		if seen[m.Path] {
			continue
		}

		seen[m.Path] = true

		if _, err := fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", m.Path, m.Version, m.Dir); err != nil {
			return fmt.Errorf("write the module list: %w", err)
		}
	}

	return nil
}

// fail prints why the tool could not do its job and exits non-zero, in the gate's own shape.
//
// To STDERR, which matters for `licence modules`: scripts/third-party-notices.sh redirects this
// tool's stdout into the file it walks, and a diagnostic landing there would be read as a module.
func fail(err error) {
	if _, printErr := fmt.Fprintf(os.Stderr, "\033[31mFAIL\033[0m %v\n", err); printErr != nil {
		os.Exit(1)
	}

	os.Exit(1)
}
