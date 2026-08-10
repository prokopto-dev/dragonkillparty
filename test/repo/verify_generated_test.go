package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The gate on the gate: `make verify-generated` must FAIL when `make gen` fails.
//
// Found in review of the ledger enum catalogue PR. A make recipe is one shell invocation and make
// judges it by the LAST command's exit status, so the `;`-separated version of verify-generated
// reported success whenever a generator died without writing anything — the before and after digests
// matched because nothing had run, and the closing printf exited 0. Every gen script dies rather than
// soft-skipping when its tool is missing (scripts/gen-db.sh's header explains why), so this was not
// a theoretical path: `codegen-drift` is a required job and it would have gone green having
// regenerated nothing at all.
//
// The recipe now chains with `&&`. This test is what stops it from going back.

// TestVerifyGenerated_FailingGenerator_FailsTheTarget runs the real target with a PATH that has no
// Go on it, which is the cheapest way to make the first generator die before it writes anything.
//
// It runs the REAL Makefile rather than a fixture copy, because the bug was in the recipe's shell
// punctuation — a fixture reproducing the recipe would be a second copy of the thing under test, and
// the copy is what would get fixed.
//
// Nothing is mutated: scripts/gen-enums.sh is the first step of `make gen` and it exits before
// touching db/schema.hcl. The digest pass that precedes it only reads.
func TestVerifyGenerated_FailingGenerator_FailsTheTarget(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	cmd := exec.Command("make", "verify-generated")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+pathWithoutGo(t))

	out, err := cmd.CombinedOutput()

	require.Error(t, err,
		"verify-generated exited 0 with a generator that could not run — the recipe is swallowing "+
			"`make gen`'s status again, and the required codegen-drift job can now false-green:\n%s", out)

	require.NotContains(t, string(out), "generated files match their sources",
		"verify-generated claimed the tree was in sync without having regenerated it:\n%s", out)

	// Fail for the RIGHT reason: the generator refused because Go was absent, not because the
	// target broke some other way.
	require.Contains(t, string(out), "go is not installed",
		"expected the first generator to die on the missing toolchain:\n%s", out)
}

// pathWithoutGo returns the caller's PATH minus every directory that holds a `go` binary, and
// asserts the result really cannot find one.
//
// Subtracting directories rather than building a curated PATH keeps every other tool the recipe
// needs — find, sort, xargs, awk, sha256sum or shasum, bash, env — exactly where the platform put
// them, so this works on both a macOS laptop and the Ubuntu runner without a list of absolute paths
// that would be wrong on one of them.
func pathWithoutGo(t *testing.T) string {
	t.Helper()

	var kept []string

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}

		if info, err := os.Stat(filepath.Join(dir, "go")); err == nil && !info.IsDir() {
			continue
		}

		kept = append(kept, dir)
	}

	stripped := strings.Join(kept, string(filepath.ListSeparator))

	// The assertion that keeps this test honest. If a `go` survives on the stripped PATH the
	// generator would succeed, verify-generated would pass, and the test would fail for a reason
	// that has nothing to do with the recipe's punctuation.
	//
	// Searched by hand rather than with exec.LookPath, which reads the PROCESS environment and would
	// need t.Setenv — and t.Setenv panics in a parallel test.
	for _, dir := range kept {
		info, err := os.Stat(filepath.Join(dir, "go"))
		require.True(t, err != nil || info.IsDir(),
			"stripping PATH left a usable go in %s — the fixture is not hostile", dir)
	}

	return stripped
}
