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

// trackedLines walks every tracked file and hands fn one line at a time, so the gates below share
// one traversal rule rather than three copies of it. This file is skipped for the reason selfPath
// records. It asserts the walk was not vacuous: a gate that scanned nothing passes, and a passing
// gate that proved nothing is worse than no gate.
func trackedLines(t *testing.T, root string, fn func(rel string, lineno int, line string)) {
	t.Helper()

	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root

	out, err := cmd.Output()
	require.NoError(t, err, "list tracked files with git ls-files")

	tracked := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, tracked, "git ls-files returned nothing — is this a git checkout?")

	var scanned int

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
			fn(rel, lineno+1, line)
		}
	}

	require.NotZero(t, scanned, "scanned no files — the tracked-file walk is broken, not clean")
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

	var bad []string

	trackedLines(t, root, func(rel string, lineno int, line string) {
		for _, m := range githubRef.FindAllStringSubmatch(line, -1) {
			refOwner, refRepo := m[1], trimRepoName(m[2])

			switch {
			case refOwner == deadOwner:
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — the %q organisation does not exist", rel, lineno, m[0], deadOwner))
			case repoNames[refRepo] && refOwner != owner:
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — this project lives under %q", rel, lineno, m[0], owner))
			}
		}
	})

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

// ---------------------------------------------------------------------------------------------
// The container-image half of the same defect (#70).
//
// A GHCR namespace mirrors a GitHub owner, so the dead organisation above was also the registry
// path seventeen tracked references told an operator to pull. Worse than a dead link: the release
// workflow publishes `ghcr.io/${{ github.repository_owner }}/…`, so what the docs named and what
// the pipeline produced were two different images and neither side could tell. The reference an
// officer meets at the worst possible moment is the downgrade refusal in `internal/migrate`, whose
// entire design goal is to name a tag they can pull instead of saying "use a newer version".
//
// Two gates, because the strings live in two forms that no single regexp sees:
//
//  1. the reader-facing form, `ghcr.io/<owner>/<name>` written out in a doc, a script or a Go
//     string — checked against the closed set of images this project publishes;
//  2. the workflow form, `ghcr.io/${{ github.repository_owner }}/<name>`, which is the definition
//     the first set follows — checked so a rename cannot land without the documentation sweep.
// ---------------------------------------------------------------------------------------------

// productImage is what the container image is called. It is the REPOSITORY's name, not the
// binary's: `dkp` is the executable, and an image called `dkp` is the one #70 was about — every
// documented pull named it and no workflow ever published it.
const productImage = "dragonkillparty"

// publishedImages is the closed set of image names published under the canonical owner, each with
// the workflow that publishes it. Anything else under our own namespace is a reference to an image
// that does not exist, which is the whole bug: the set is closed so that inventing a name in prose
// fails here rather than in somebody's `docker pull`.
var publishedImages = map[string]string{
	productImage:   "release.yml (and edge.yml for :edge)",
	"dkp-refdb":    "release.yml",
	"dkp-fixtures": "fixtures.yml",
}

// ghcrRef captures the owner and image name of a GHCR reference. The name half stops before a `:`
// tag, an `@sha256:` digest, or the markdown around it, exactly as githubRef does for a URL.
var ghcrRef = regexp.MustCompile(`ghcr\.io/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)

// ownerTemplate is how a workflow spells the canonical owner. It is deliberately not hard-coded
// there: a fork's CI must push into the fork's own namespace, not fail against ours.
const ownerTemplate = "ghcr.io/${{ github.repository_owner }}/"

// TestContainerImages_TrackedReferences_NameAPublishedImage asserts that no tracked file tells
// anyone to pull an image this project does not publish.
//
// Deliberately narrow, like the github.com gate: a third-party image under somebody else's
// namespace is not this gate's business. What it catches is our own namespace naming an image that
// is not published, and any namespace naming one of our images.
func TestContainerImages_TrackedReferences_NameAPublishedImage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	owner := canonicalOwner(t, root)

	var bad []string

	trackedLines(t, root, func(rel string, lineno int, line string) {
		for _, m := range ghcrRef.FindAllStringSubmatch(line, -1) {
			refOwner, refImage := m[1], trimRepoName(m[2])
			_, published := publishedImages[refImage]

			switch {
			case refOwner == deadOwner:
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — the %q organisation does not exist, so neither does that image",
					rel, lineno, m[0], deadOwner))
			case refOwner == owner && !published:
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — nothing publishes %q under %s; the image is %q",
					rel, lineno, m[0], refImage, owner, productImage))
			case published && refOwner != owner:
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — %q is published under %q, not %q",
					rel, lineno, m[0], refImage, owner, refOwner))
			}
		}
	})

	require.Empty(t, bad, "GHCR references naming an image this project does not publish.\n"+
		"The product image is ghcr.io/%s/%s, published by release.yml.\n\n  %s",
		owner, productImage, strings.Join(bad, "\n  "))
}

// TestWorkflowImages_PublishTheDocumentedPath asserts the other direction: that the pipeline
// publishes what the documentation says to pull.
//
// The gate above cannot see a workflow's `IMAGE:` line, because the owner there is a template
// expression rather than a name. That is precisely the line that decides what exists, so it is
// checked directly: the owner stays templated (a fork pushes into its own namespace), the image is
// one of the published names, and `IMAGE` is the product image. Rename the image here and the
// documentation sweep is not optional — this fails until it happens.
func TestWorkflowImages_PublishTheDocumentedPath(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "read .github/workflows")

	// The value is matched to end of line, not as a run of non-space: `${{ github.repository_owner }}`
	// contains spaces, and a `\S+` capture silently stops at the first one and asserts about half a
	// path.
	env := regexp.MustCompile(`(?m)^[ \t]*(IMAGE|REFDB|FIXTURES):[ \t]*(.+?)[ \t]*$`)

	var (
		bad        []string
		checked    int
		sawImage   bool
		sawRelease bool
	)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, readErr, "read workflow %s", e.Name())

		for _, m := range env.FindAllStringSubmatch(string(body), -1) {
			key, value := m[1], m[2]

			checked++

			if e.Name() == "release.yml" {
				sawRelease = true
			}

			image, templated := strings.CutPrefix(value, ownerTemplate)
			if !templated {
				bad = append(bad, fmt.Sprintf(
					"%s: %s: %s — must be %s<image> so a fork publishes into its own namespace",
					e.Name(), key, value, ownerTemplate))

				continue
			}

			if key == "IMAGE" {
				sawImage = true

				if image != productImage {
					bad = append(bad, fmt.Sprintf(
						"%s: %s: %s — the product image is %q. Every documented `docker pull` in "+
							"the tree names it, so renaming it here means sweeping those too",
						e.Name(), key, value, productImage))
				}

				continue
			}

			if _, published := publishedImages[image]; !published {
				bad = append(bad, fmt.Sprintf(
					"%s: %s: %s — %q is not in publishedImages; add it there in the same change so "+
						"a reference to it stops reading as a typo", e.Name(), key, value, image))
			}
		}
	}

	require.Empty(t, bad, "workflow image paths that do not match the documented ones.\n\n  %s",
		strings.Join(bad, "\n  "))
	require.NotZero(t, checked, "no workflow declares an image — the scan is broken, not clean")
	require.True(t, sawImage, "no workflow declares IMAGE; the product image would be undefined")
	require.True(t, sawRelease, "release.yml declares no image path — it is the definition the "+
		"documentation follows, so its absence makes this gate vacuous")
}

// ---------------------------------------------------------------------------------------------
// The Homebrew tap (#71).
//
// Homebrew resolves `brew install <owner>/<tap>/<formula>` to the repository
// `<owner>/homebrew-<tap>`, which makes the documented command a restatement of goreleaser's config
// and nothing else. They were allowed to disagree: goreleaser wrote the formula into one
// repository, the install page named another, and both sat under an org that does not exist — so
// the documented command 404'd on a tap that had never been written. goreleaser only runs on a tag,
// so nothing would have noticed until the first release.
//
// The tap cannot live in this repository (Homebrew mandates a separate one), so this gate cannot
// check that it exists. What it can do is make the two references derive from one place.
// ---------------------------------------------------------------------------------------------

const goreleaserPath = ".goreleaser.yaml"

// tapRef is the tap as goreleaser defines it: the repository it writes the formula into, and the
// formula's name.
type tapRef struct {
	owner   string
	repo    string
	formula string
}

// brewInstall captures the three segments of a documented `brew install <owner>/<tap>/<formula>`.
// The two-segment form (`brew install gitleaks`) is a core formula and not a tap reference, so it
// does not match and is not this gate's business.
var brewInstall = regexp.MustCompile(
	`brew install ([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)

// homebrewRepo captures any mention of a tap repository by name, wherever it is written — a prose
// table in a design document names one just as bindingly as the config does.
var homebrewRepo = regexp.MustCompile(`homebrew-[A-Za-z0-9_.-]+`)

// goreleaserTap reads the tap out of `.goreleaser.yaml`'s `brews:` block, which is the only place
// in the tree that decides it. Indentation-tracked rather than regexped over the whole file: the
// `repository:` sub-block has its own `name:`, and confusing it with the formula's `name:` is how a
// gate ends up asserting something is equal to itself.
func goreleaserTap(t *testing.T, root string) tapRef {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, goreleaserPath))
	require.NoError(t, err, "read %s", goreleaserPath)

	var (
		got         tapRef
		inBrews     bool
		repoIndent  = -1
		listName    = regexp.MustCompile(`^-\s+name:\s*(\S+)$`)
		scalarEntry = regexp.MustCompile(`^([A-Za-z_]+):\s*(\S+)$`)
	)

	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))

		if indent == 0 {
			inBrews = trimmed == "brews:"
			repoIndent = -1

			continue
		}

		if !inBrews {
			continue
		}

		if repoIndent >= 0 && indent <= repoIndent {
			repoIndent = -1
		}

		switch {
		case repoIndent >= 0:
			m := scalarEntry.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}

			switch m[1] {
			case "owner":
				got.owner = m[2]
			case "name":
				got.repo = m[2]
			}
		case trimmed == "repository:":
			repoIndent = indent
		case got.formula == "":
			if m := listName.FindStringSubmatch(trimmed); m != nil {
				got.formula = m[1]
			}
		}
	}

	require.NotEmpty(t, got.formula, "%s: brews[] declares no formula name", goreleaserPath)
	require.NotEmpty(t, got.owner, "%s: brews[].repository declares no owner", goreleaserPath)
	require.NotEmpty(t, got.repo, "%s: brews[].repository declares no name", goreleaserPath)

	return got
}

// TestHomebrewTap_DocumentedCommand_ResolvesToThePublishedRepository asserts that every way the tap
// is named in this tree resolves to the one repository goreleaser writes to.
func TestHomebrewTap_DocumentedCommand_ResolvesToThePublishedRepository(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	owner := canonicalOwner(t, root)
	tap := goreleaserTap(t, root)

	require.Equal(t, owner, tap.owner,
		"%s writes the formula into %q, which is not the owner this project publishes under. "+
			"HOMEBREW_TAP_TOKEN would need write access to a repository under someone else",
		goreleaserPath, tap.owner)

	name, ok := strings.CutPrefix(tap.repo, "homebrew-")
	require.True(t, ok, "%s: a Homebrew tap repository must be named homebrew-<tap>; %q is not, so "+
		"`brew install %s/…` cannot resolve to it", goreleaserPath, tap.repo, tap.owner)
	require.NotEmpty(t, name, "%s: the tap repository is named %q, leaving no tap name for "+
		"`brew install <owner>/<tap>/<formula>`", goreleaserPath, tap.repo)

	want := fmt.Sprintf("brew install %s/%s/%s", tap.owner, name, tap.formula)

	var (
		bad        []string
		documented int
	)

	trackedLines(t, root, func(rel string, lineno int, line string) {
		for _, m := range brewInstall.FindAllStringSubmatch(line, -1) {
			documented++

			if m[0] != want {
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — resolves to %s/homebrew-%s, and goreleaser writes to %s/%s",
					rel, lineno, m[0], m[1], m[2], tap.owner, tap.repo))
			}
		}

		for _, m := range homebrewRepo.FindAllString(line, -1) {
			if repo := trimRepoName(m); repo != tap.repo {
				bad = append(bad, fmt.Sprintf(
					"%s:%d: %s — the tap repository is %s/%s", rel, lineno, repo, tap.owner, tap.repo))
			}
		}
	})

	require.Empty(t, bad, "Homebrew references that do not resolve to the tap goreleaser writes.\n"+
		"The tap is %s/%s, and the command that reaches it is `%s`.\n\n  %s",
		tap.owner, tap.repo, want, strings.Join(bad, "\n  "))

	require.NotZero(t, documented, "no page documents `%s`. The tap is written on every release and "+
		"advertised nowhere, which is how the two drifted apart in the first place", want)
}
