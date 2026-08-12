// Command verifyspec is `make verify-spec`: the merge-blocking gate over openapi/openapi.json.
//
// Everything it asserts, and why each rule exists, is in internal/specgate. This file is the process
// boundary and nothing else — resolve the tree, resolve the base revision, exit 0 or 1 — for the same
// reason cmd/dkp holds Cobra wiring and no logic: the rules are unit-tested, and a rule that lived
// here would only ever be tested through a subprocess.
//
// It lives beside the package it runs rather than in cmd/dkp because it is dev tooling, exactly as
// internal/ledger/enumgen does: cmd/dkp is the product binary and an officer never runs a repo gate.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/specgate"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-spec: %v\n", err)
		os.Exit(1)
	}

	// LookupEnv, not Getenv: an empty DKP_SPEC_BASE_REF is a MEANINGFUL value — it disables the
	// operationId-rename check — and is not the same as an absent one, which selects origin/main. The
	// Makefile recipe strips the variable with `env -u` so that only a direct invocation can set it,
	// and TestMakefile_VerifySpec_StripsBaseRefEnv asserts the strip is still there.
	baseRef := specgate.DefaultBaseRef
	if v, ok := os.LookupEnv(specgate.EnvBaseRef); ok {
		baseRef = v
	}

	os.Exit(specgate.Run(root, baseRef, os.Stdout, os.Stderr))
}

// repoRoot returns the tree to inspect: DKP_REPO_ROOT when set and non-empty, otherwise the git
// working tree containing the current directory, otherwise the current directory.
//
// The git step is what makes `go run ./internal/specgate/verifyspec` correct from anywhere in the
// checkout rather than only from the root, which is the property the Python got from `__file__` and a
// compiled binary has no equivalent of. An EMPTY DKP_REPO_ROOT falls through to it deliberately: the
// gate scripts spell that fallback `${DKP_REPO_ROOT:-...}` and test/repo's runners assert the variable
// is never empty precisely because an empty value must not be able to point the gate at nothing.
func repoRoot() (string, error) {
	if root := os.Getenv(specgate.EnvRepoRoot); root != "" {
		return root, nil
	}

	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return root, nil
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate the repository root: %w", err)
	}

	return wd, nil
}
