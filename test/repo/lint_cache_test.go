// The golangci-lint cache is scoped to the checkout (issue #117).
//
// THE BUG. With GOLANGCI_LINT_CACHE unset, golangci-lint uses one per-user cache —
// ~/Library/Caches/golangci-lint on macOS — so a result cached by ANY checkout is reachable from
// every other one, and the cached entry still carries the absolute path of the tree that produced
// it. This repository's agent workflow creates and destroys sibling worktrees continuously, so the
// precondition is the normal state of a development machine here. Delete the worktree that filled
// the entry and `make lint` prints:
//
//	level=warning msg="[runner] Can't process results by generated_file_filter processor: ...
//	FromLinter:\"forbidigo\", Text:\"use of `float64` forbidden because \\\"float32/float64 are
//	banned in internal/ledger and internal/strategy (canonical §1) ...\"
//	Filename:\"/Users/…/dragonkillparty-8/internal/core/centipoints.go\" ... no such file"
//
// on a clean tree, in a run that ends `0 issues.`
//
// WHY THAT IS WORTH A GATE rather than tolerating. It is an ENVIRONMENT fault wearing a CONTENT
// fault's clothes — the shape test/repo/python_floor_test.go already names as the most expensive one
// a gate failure can have. It quotes the ledger's float ban and names internal/core/centipoints.go,
// so the natural next move is to go hunting for a canonical §1 violation that does not exist.
package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLintCache_IsScopedToThisCheckout asserts the value make actually computes, not the text of the
// assignment.
//
// EXPANDED, not grepped. `make -p` prints a recursively-expanded variable's DEFINITION — the literal
// `$(CURDIR)/.cache/golangci-lint` — and a grep of the Makefile would pass just as happily on
// `GOLANGCI_LINT_CACHE ?= /tmp/shared`, which is the bug with extra steps. So make is asked to
// expand it, through a throwaway second makefile in t.TempDir(): `-f Makefile -f <tmp>` reads both,
// and nothing is added to the repository for the benefit of a test.
func TestLintCache_IsScopedToThisCheckout(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to make; run `make test` or `make check`")
	}

	root := repoRoot(t)

	probe := filepath.Join(t.TempDir(), "probe.mk")
	require.NoError(t, os.WriteFile(probe,
		[]byte(".PHONY: dkp-print-golangci-cache\ndkp-print-golangci-cache:\n\t@printf '%s\\n' '$(GOLANGCI_LINT_CACHE)'\n"),
		0o644))

	// --no-print-directory, and it is not decoration: GNU make wraps a `-C` build in "Entering
	// directory"/"Leaving directory" banners on stdout, so without it this reads the banner as the
	// variable's value and reports a perfectly correct absolute path as relative. BSD make on macOS
	// prints no banner, which is exactly why the first version of this test passed on a laptop and
	// failed on CI — the shape this whole file exists to complain about, arriving in its own test.
	cmd := exec.Command("make", "-C", root, "--no-print-directory",
		"-f", "Makefile", "-f", probe, "dkp-print-golangci-cache")

	out, err := cmd.Output()
	require.NoErrorf(t, err, "make dkp-print-golangci-cache\n%s", out)

	value := strings.TrimSpace(string(out))

	require.NotEmptyf(t, value,
		"the Makefile does not define GOLANGCI_LINT_CACHE. Unset, golangci-lint shares one cache "+
			"across every checkout on the machine, and a result cached by a worktree that has since "+
			"been deleted makes `make lint` warn about a file that does not exist — quoting the "+
			"ledger's float64 ban at internal/core/centipoints.go on a clean tree (issue #117).")

	require.Truef(t, filepath.IsAbs(value),
		"GOLANGCI_LINT_CACHE is %q, which is relative. golangci-lint runs with the repository root as "+
			"its working directory today, but a relative cache path means the cache moves with the "+
			"caller — scope it to $(CURDIR).", value)

	require.Truef(t, strings.HasPrefix(value, root+string(filepath.Separator)),
		"GOLANGCI_LINT_CACHE is %q, which is outside this checkout (%s). The whole point of issue "+
			"#117 is that two worktrees of this repository must not be able to serve each other a "+
			"cached result carrying the other's absolute paths.", value, root)
}

// TestLintCache_IsIgnoredEverywhereItMustBe: the cache is derived, per-checkout and large. Ignored by
// git so it cannot be committed and cannot make `git status` noisy mid-review; ignored by Docker so
// it never enters a build context that has no use for it and that every `make lint` would
// invalidate; removed by `make clean` because it is build output in every sense that matters.
func TestLintCache_IsIgnoredEverywhereItMustBe(t *testing.T) {
	t.Parallel()

	require.Containsf(t, readRepoFile(t, ".gitignore"), "/.cache/",
		".gitignore must ignore /.cache/ — it is where `make lint-go` puts the golangci-lint cache "+
			"since issue #117, and an uncommittable directory in a tracked tree is a `git status` "+
			"nobody can read")

	require.Containsf(t, readRepoFile(t, ".dockerignore"), "/.cache",
		".dockerignore must exclude /.cache — hundreds of megabytes of analysis results that no build "+
			"stage reads, invalidating the context layer on every lint run")

	require.Containsf(t, makeRecipe(t, "clean"), ".cache",
		"`make clean` must remove .cache along with the other build output")
}
