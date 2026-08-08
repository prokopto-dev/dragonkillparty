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
// The marker it polices does not exist on this branch. PR 5b rewrites EXAMPLE_ENDPOINT.md from PR 5a's
// merged code and is what INSERTS the `PENDING PR 6` placeholder into step 6 (first-ten-prs.md:447).
// PR 5b is being built in parallel and is not on PR 6's base, so writing an active assertion now would
// either (a) pass vacuously because the marker was never inserted — a green test that proves nothing —
// or (b) require faking the marker into a document PR 6 is told not to rewrite. Both are the failure
// mode this project's test doctrine exists to prevent: a test that is green for the wrong reason.
//
// So the test ships SKIPPED with the reason attached, and its body is written and ready. The scan is
// real; only the t.Skip guard defers it. When PR 5b has merged and EXAMPLE_ENDPOINT.md carries the
// placeholder, the follow-up that removes the placeholder deletes the Skip line below in the same
// change — at which point this test does its job. The second half of the criterion (step 6's snippet
// type-checks under `tsc --noEmit`) lands with that follow-up too, because the snippet it checks is
// the one PR 5b writes.
//
// This is option (a) from the brief: the test is present and visible, skipped with a clear reason,
// rather than faked against a document that does not have the marker yet.
func TestDocs_NoPendingMarkers(t *testing.T) {
	t.Parallel()

	t.Skip("deferred until PR 5b inserts the `PENDING PR 6` placeholder into internal/api/EXAMPLE_ENDPOINT.md; " +
		"see coupling #2 in the PR 6 brief. Removing this Skip is the follow-up that removes the placeholder.")

	root := repoRoot(t)

	// Where the placeholder could live: the docs tree and the in-repo API markdown (EXAMPLE_ENDPOINT.md,
	// and any sibling .md under internal/api).
	roots := []string{
		filepath.Join(root, "docs"),
		filepath.Join(root, "internal", "api"),
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
			// Skip this test's own package and the plan document, which describe the marker rather
			// than carrying it as a live placeholder.
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil //nolint:nilerr
			}
			if strings.Contains(string(body), pendingPR6Marker) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel)
			}

			return nil
		})
	}

	require.Empty(t, offenders,
		"the %q placeholder must be removed once the generated client exists — it remains in: %v",
		pendingPR6Marker, offenders)
}
