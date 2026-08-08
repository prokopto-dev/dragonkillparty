package repo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pendingPR6Marker is the placeholder PR 5b leaves in internal/api/EXAMPLE_ENDPOINT.md step 6, where
// the generated TypeScript client call belongs. PR 6's acceptance criterion is that this string
// appears NOWHERE under docs/ or internal/api/*.md once the client exists.
//
// Assembled at run time from fragments so this test file is not itself a match: it is a tracked file
// the scan below reads, and a verbatim literal here would make the test fail on its own source.
var pendingPR6Marker = "PENDING" + " PR " + "6"

// TestDocs_NoPendingMarkers is DEFERRED, and the deferral is deliberate — see coupling #2 in the
// PR 6 brief and docs/development/first-ten-prs.md §PR 5b.
//
// The LIVE placeholder it polices — the `PENDING PR 6` token in step 6 of EXAMPLE_ENDPOINT.md, where
// the generated TypeScript client call belongs — does not exist on this branch. PR 5b rewrites
// EXAMPLE_ENDPOINT.md from PR 5a's merged code and is what INSERTS that placeholder. PR 5b is being
// built in parallel and is not on PR 6's base, so removing the Skip now would pass vacuously against a
// document that has no live placeholder to remove — a green test that proves nothing, which is the
// failure mode this project's test doctrine exists to prevent.
//
// The token is NOT absent from the tree, though: docs/development/first-ten-prs.md carries it in
// planning prose (it describes the marker and this very test). That is why the scan below excludes the
// docs/development/** tree and this test's own repo/ package — both DESCRIBE the marker rather than
// carrying it as a live placeholder, and a scan that flagged them would go red the instant the Skip is
// removed, on prose nobody is expected to edit. The exclusions are in place NOW so that removing the
// Skip later is a one-line change with a body that already does the right thing.
//
// So the test ships SKIPPED with the reason attached, and its body is written and correct. The scan is
// real; only the t.Skip guard defers it. When PR 5b has merged and EXAMPLE_ENDPOINT.md carries the
// live placeholder, the follow-up that removes the placeholder deletes the Skip line below in the same
// change — at which point this test does its job. The second half of the criterion (step 6's snippet
// type-checks under `tsc --noEmit`) lands with that follow-up too, because the snippet it checks is
// the one PR 5b writes.
func TestDocs_NoPendingMarkers(t *testing.T) {
	t.Parallel()

	t.Skip("deferred until PR 5b inserts the `PENDING PR 6` placeholder into internal/api/EXAMPLE_ENDPOINT.md; " +
		"see coupling #2 in the PR 6 brief. The body and its exclusions are correct now, so removing this " +
		"Skip is the one-line follow-up that lands with the change removing the placeholder.")

	root := repoRoot(t)

	// Where the placeholder could live: the docs tree and the in-repo API markdown (EXAMPLE_ENDPOINT.md,
	// and any sibling .md under internal/api).
	roots := []string{
		filepath.Join(root, "docs"),
		filepath.Join(root, "internal", "api"),
	}

	// Files that DESCRIBE the marker rather than carrying it as a live placeholder. The planning tree
	// documents the whole PR sequence, including this marker and this test; excluding it keeps the scan
	// pointed at live placeholders, not prose about them. Paths are relative to root, slash-separated.
	excluded := func(rel string) bool {
		rel = filepath.ToSlash(rel)

		return strings.HasPrefix(rel, "docs/development/")
	}

	var offenders []string

	for _, base := range roots {
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // a missing tree is not this test's failure
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}

			rel, relErr := filepath.Rel(root, path)
			if relErr != nil || excluded(rel) {
				return nil //nolint:nilerr
			}

			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil //nolint:nilerr
			}
			if strings.Contains(string(body), pendingPR6Marker) {
				offenders = append(offenders, filepath.ToSlash(rel))
			}

			return nil
		})
	}

	require.Empty(t, offenders,
		"the %q placeholder must be removed once the generated client exists — it remains in: %v",
		pendingPR6Marker, offenders)
}
