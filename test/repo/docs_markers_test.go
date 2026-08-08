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

// TestDocs_NoPendingMarkers polices the `PENDING PR 6` placeholder that PR 5b left in step 6 of
// EXAMPLE_ENDPOINT.md, where the generated TypeScript client call belonged. PR 6 (the SPA and its
// generated client) replaced that placeholder with the real caller, so the token must now appear
// NOWHERE under docs/ or internal/api/*.md. This test was deferred with a t.Skip until the client
// existed to write against; PR 6 is that change, so the Skip is gone and the scan is live.
//
// The token is NOT absent from the whole tree: docs/development/first-ten-prs.md carries it in
// planning prose (it describes the marker and this very test). That is why the scan below excludes the
// docs/development/** tree — it DESCRIBES the marker rather than carrying it as a live placeholder, and
// flagging it would go red on prose nobody is expected to edit. This test's own repo/ package never
// contains the literal either: pendingPR6Marker is assembled from fragments (see its comment) so the
// scan cannot match its own source.
//
// The second half of PR 6's acceptance criterion — that step 6's snippet type-checks under
// `tsc --noEmit` — is met by web/src/examples/guild-endpoint.ts, the real file the doc's ```ts fence
// transcribes. That file is compiled by `make vet` and CI's Node-having `typecheck` job; it is NOT
// checked here, because this Go test runs in `test / integration`, a job with no Node toolchain. A
// tsc/node call from here would fail-loud there, so the type-check deliberately lives where Node is.
func TestDocs_NoPendingMarkers(t *testing.T) {
	t.Parallel()

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
