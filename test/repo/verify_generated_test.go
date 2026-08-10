package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestVerifyGenerated_FailingGenerator_FailsTheTarget runs the real target with a `go` on PATH that
// refuses to run, so the first generator dies exactly as it would on a broken toolchain.
//
// It runs the REAL Makefile rather than a fixture copy, because the bug was in the recipe's shell
// punctuation — a fixture reproducing the recipe would be a second copy of the thing under test, and
// the copy is what would get fixed.
//
// SHADOWING `go` RATHER THAN STRIPPING PATH, and the first version of this test got that wrong in a
// way only CI could show. Removing every PATH entry holding a `go` binary looks equivalent and is
// not. The Ubuntu runner carries /usr/bin/go, so the strip removed /usr/bin — taking `awk` with it —
// and then this Makefile put Go back by itself: `GOTOOLS_BIN := $(shell $(GO) env GOPATH)/bin`
// evaluates to `/bin` when go cannot run, `export PATH := $(PATH):$(GOTOOLS_BIN)` appends it, and on
// Ubuntu /bin aliases /usr/bin. The runner's Go 1.24 came back, failed the go.mod floor instead, and
// the test failed on its own fixture rather than on the target.
//
// Prepending one temp directory changes exactly one thing, leaves every coreutil where the platform
// put it, and cannot be undone by the Makefile's own PATH arithmetic.
func TestVerifyGenerated_FailingGenerator_FailsTheTarget(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	schema := filepath.Join(root, "db", "schema.hcl")

	before, err := os.ReadFile(schema)
	require.NoError(t, err, "read db/schema.hcl before the run")

	cmd := exec.Command("make", "verify-generated")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+pathWithStubbedGo(t))

	out, runErr := cmd.CombinedOutput()

	require.Error(t, runErr,
		"verify-generated exited 0 with a generator that could not run — the recipe is swallowing "+
			"`make gen`'s status again, and the required codegen-drift job can now false-green:\n%s", out)

	require.NotContains(t, string(out), "generated files match their sources",
		"verify-generated claimed the tree was in sync without having regenerated it:\n%s", out)

	// Fail for the RIGHT reason, asserted at both ends. The sentinel proves the go that ran was the
	// stub — not a real toolchain that failed for its own reasons, which is exactly how the first
	// version of this fixture went wrong — and the die message proves gen-enums.sh was the step that
	// carried the failure up. This pair is what caught the fixture being wrong rather than the target.
	require.Contains(t, string(out), stubGoSentinel,
		"the go the generator ran was not the stub, so this proves nothing about the recipe:\n%s", out)

	require.Contains(t, string(out), "enumgen failed",
		"expected scripts/gen-enums.sh to be the step that died:\n%s", out)

	// And the failed run left the schema alone — a generator that dies mid-write would be a worse
	// bug than the one this test is about.
	after, err := os.ReadFile(schema)
	require.NoError(t, err, "read db/schema.hcl after the run")
	require.Equal(t, string(before), string(after), "a failed verify-generated rewrote db/schema.hcl")
}

// pathWithStubbedGo returns the caller's PATH with a temp directory prepended that holds a `go`
// which exits non-zero.
//
// scripts/gen-enums.sh finds it with `command -v go`, runs `go run ./internal/ledger/enumgen`, gets
// a failure and dies — which is the state under test: `make gen` returning non-zero without having
// changed a file, so the before and after digests match and only the recipe's own punctuation
// decides whether verify-generated reports success.
func pathWithStubbedGo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	stub := "#!/bin/sh\necho '" + stubGoSentinel + "' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), []byte(stub), 0o755), "write the go stub")

	return dir + string(filepath.ListSeparator) + os.Getenv("PATH")
}

// stubGoSentinel is what the stub prints, and it is deliberately a string no real toolchain, make
// recipe or gate script would ever emit — the assertion on it is only worth anything if a passing
// test cannot be produced by some other program failing.
const stubGoSentinel = "dkp-test-stub-go: refusing to run, by test fixture"

// TestGen_EnumGenerator_DependsOnNoGeneratedCode holds the bootstrap property that `make gen` must
// be able to repair its own artefacts.
//
// scripts/gen-enums.sh is the FIRST step of `make gen` and it compiles internal/ledger/enumgen. If
// that command's dependency graph reaches generated code, a tree whose generated code does not build
// cannot run the command that regenerates it — the failure mode is circular and the error message
// points at the wrong file. It is not hypothetical: the catalogue's first home was internal/ledger,
// which reaches internal/store/sqlitegen through commit.go, so a stale or absent sqlc output would
// have broken `make gen` until someone hand-repaired the generated package.
//
// The fix was to move the catalogue to the leaf package internal/ledger/kinds. This is what stops it
// drifting back: an innocent-looking import of internal/ledger from the catalogue or the generator
// would restore the cycle and nothing else in the suite would notice.
func TestGen_EnumGenerator_DependsOnNoGeneratedCode(t *testing.T) {
	t.Parallel()

	// The trees AGENTS.md marks GENERATED and `make gen` writes. A generator that imports any of
	// them cannot be the thing that fixes them.
	generated := []string{
		"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen",
		"github.com/prokopto-dev/dragonkillparty/internal/store/pggen",
	}

	cmd := exec.Command("go", "list", "-deps", "./internal/ledger/enumgen")
	cmd.Dir = repoRoot(t)

	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps ./internal/ledger/enumgen")

	deps := string(out)

	for _, pkg := range generated {
		require.NotContains(t, deps, pkg,
			"the first step of `make gen` imports %s, which `make gen` generates — a tree whose "+
				"generated code does not build can no longer be repaired by running `make gen`. Keep "+
				"internal/ledger/kinds a leaf; see its package comment", pkg)
	}
}
