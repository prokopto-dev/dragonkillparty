package repo_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The canonical-repository gate, and the defect it exists for.
//
// Twelve tracked references — four of them in the issue chooser a first-time reporter sees, two of
// them the `cosign` identity regexp an operator is told to verify a download against — pointed at
// `github.com/dragonkillparty/...` (#49). That organisation does not exist and never has, so every
// one of those links 404'd from the day it was written. The worst of them told somebody who had
// found a security bug, and who was doing exactly the right thing about it, to open a private
// advisory form that was not there.
//
// Nothing caught it. `make docs-links` walks 449 RELATIVE links and no absolute ones, on purpose:
// it must run on every PR with no network. The nightly lychee job does fetch external URLs, but a
// dead link to our own repository is not a flaky third-party host — it is a fact about this tree,
// knowable offline, and it should fail in the PR that introduces it rather than in a nightly nobody
// is watching.
//
// So this gate reads the tree, not the network. It asks one question of every `github.com/<owner>/`
// URL in every tracked file: if that URL names THIS project, does it name the owner this project
// actually lives under? The owner is read from go.mod rather than hard-coded, so the gate follows
// the repository if it ever moves and does not have to be remembered on the day it does.
//
// It is deliberately narrow. A pinned action (`github.com/actions/checkout`), a Go module, an
// upstream issue link — none of those name this project, and none of them are this gate's business.

// deadOwner is the organisation the twelve broken references named. It does not exist: both
// `gh api repos/dragonkillparty/dragonkillparty` and `gh api repos/dragonkillparty/dkp` 404, and so
// does the org itself. Banned outright rather than merely "not canonical", because there is no repo
// under it that could ever be a legitimate target.
const deadOwner = "dragonkillparty"

// repoNames are the repository names that mean "this project". A URL naming one of these under any
// owner but the canonical one is either a typo or a link to somebody's fork, and neither belongs in
// documentation that tells an operator what to clone, pull or verify against.
//
// `dkp` is here because it is what the twelve references used; the repository is named
// `dragonkillparty` and `dkp` is only the binary's name.
var repoNames = map[string]bool{"dragonkillparty": true, "dkp": true}

// selfPath is this file, which is skipped. It is a tracked file that necessarily contains the
// strings it searches for, so scanning it would report the gate as its own first violation.
const selfPath = "test/repo/canonical_repo_test.go"

// githubRef captures the owner and repository of a github.com URL. The repository half stops at the
// first character that cannot appear in a repository name, so surrounding markdown — a closing
// paren, a quote, a trailing slash and path — does not end up inside the capture.
var githubRef = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)

// canonicalOwner reads the owner out of go.mod's module path.
//
// The module path is `github.com/<owner>/<repo>`, and it is the one place in the tree where the
// repository's identity is not prose: it is compiled in, every import in the project spells it, and
// it cannot be wrong without the build failing. That makes it the right authority for a gate about
// which owner the documentation should name.
func canonicalOwner(t *testing.T, root string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "read go.mod")

	for line := range strings.Lines(string(body)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "module" {
			continue
		}

		parts := strings.Split(fields[1], "/")
		require.Len(t, parts, 3, "module path %q is not github.com/<owner>/<repo>", fields[1])

		return parts[1]
	}

	require.FailNow(t, "go.mod has no module line")

	return ""
}

// TestGitHubURLs_NameTheCanonicalRepository asserts that no tracked file points a reader at a
// repository this project does not live in.
func TestGitHubURLs_NameTheCanonicalRepository(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	owner := canonicalOwner(t, root)

	require.NotEqual(t, deadOwner, owner,
		"go.mod names the organisation this gate exists to ban; if the project really did move "+
			"there, delete deadOwner rather than making the gate vacuous")

	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root

	out, err := cmd.Output()
	require.NoError(t, err, "list tracked files with git ls-files")

	tracked := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, tracked, "git ls-files returned nothing — is this a git checkout?")

	var (
		bad     []string
		scanned int
	)

	for _, rel := range tracked {
		if rel == "" || rel == selfPath {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(root, rel))
		if errors.Is(readErr, os.ErrNotExist) {
			// Tracked but deleted in the working tree. Not this test's business.
			continue
		}

		require.NoError(t, readErr, "read tracked file %s", rel)

		scanned++

		for lineno, line := range strings.Split(string(body), "\n") {
			for _, m := range githubRef.FindAllStringSubmatch(line, -1) {
				refOwner, refRepo := m[1], trimRepoName(m[2])

				switch {
				case refOwner == deadOwner:
					bad = append(bad, fmt.Sprintf(
						"%s:%d: %s — the %q organisation does not exist", rel, lineno+1, m[0], deadOwner))
				case repoNames[refRepo] && refOwner != owner:
					bad = append(bad, fmt.Sprintf(
						"%s:%d: %s — this project lives under %q", rel, lineno+1, m[0], owner))
				}
			}
		}
	}

	require.NotZero(t, scanned, "scanned no files — the tracked-file walk is broken, not clean")

	require.Empty(t, bad, "github.com URLs naming a repository this project does not live in.\n"+
		"The canonical repository is %s/dragonkillparty, read from go.mod.\n\n  %s",
		owner, strings.Join(bad, "\n  "))
}

// trimRepoName strips what a repository name cannot contain but a URL in prose routinely carries: a
// `.git` suffix, and the full stop that ends the sentence the URL was written into.
func trimRepoName(repo string) string {
	repo = strings.TrimSuffix(repo, ".git")

	return strings.TrimRight(repo, ".")
}
