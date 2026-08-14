// Tests that a container image a workflow runs is pinned to a digest somebody could actually pull.
//
// PIN001 covers `uses:` — every action is a 40-character commit SHA, and a gate in
// internal/repogate/rules.hcl fails one that is not. Nothing covered `image:`, which is the other
// way a workflow names third-party code: a service container, a job container, an image handed to
// an action as an input.
//
// The gap is not hypothetical. `nightly-verify.yml`'s postgres service shipped pinned to
// `postgres:17-alpine@sha256:0000…0000` — a syntactically perfect digest that cannot exist. Docker
// retried the pull three times, the job died before its first step, and it did that every night for
// months while the checks list showed one more red nightly among several (issue #246). The pin
// looked reviewed. It read, in a diff, exactly like the working pins beside it.
//
// So the assertion has to be about the digest's CONTENT, not its shape:
//
//   - TestContainerPins_EveryImageRef_CarriesATagAndDigest is the PIN001 rule for `image:`.
//   - TestContainerPins_NoDigest_IsAPlaceholder is the one that would have caught it — a real
//     sha256 uses effectively the whole hex alphabet, and a digest typed by a human to be replaced
//     later uses one or two characters.
//
// Neither test resolves anything against a registry: test/repo runs offline and a gate that needs
// the network is a gate that fails for the wrong reason. That is a real limit — a pin that is
// well-formed, varied, and simply wrong (a typo, a digest from another repository) still gets past
// both, and the nightly pull is what finds it. What cannot get past them is the placeholder that
// nobody ever went back and filled in, which is the failure that actually happened.
package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// imageRefRe matches an `image:` key that carries a VALUE. The value is what separates a container
// reference from a job named `image:`, which ci.yml, edge.yml and release.yml each have.
var imageRefRe = regexp.MustCompile(`(?m)^[ \t]*image:[ \t]+(\S+)`)

// digestRe matches the `@sha256:` suffix of a pinned image reference.
var digestRe = regexp.MustCompile(`@sha256:([0-9a-f]{64})$`)

// imageRef is one `image:` value and where it was found.
type imageRef struct {
	rel   string
	line  int
	value string
}

// TestContainerPins_EveryImageRef_CarriesATagAndDigest holds `image:` to the rule PIN001 holds `uses:` to.
//
// A floating tag is a supply-chain hole and a reproducibility hole at once: `postgres:17-alpine`
// means a different image next week, so a nightly that passed on Monday can fail on Tuesday with no
// commit in between — and the job that finds it is the one nobody watches.
//
// The tag is required alongside the digest because the digest alone is unreadable. `postgres:17`
// tells a reviewer what is being run and lets Renovate resolve the next digest for it; a bare
// `@sha256:…` tells them nothing and pins them to it forever.
func TestContainerPins_EveryImageRef_CarriesATagAndDigest(t *testing.T) {
	t.Parallel()

	refs := workflowImageRefs(t)

	for _, ref := range refs {
		require.Regexpf(t, digestRe, ref.value,
			"%s:%d pins an image by tag alone. A tag is mutable, so this job runs different code from "+
				"one night to the next and a break arrives with no commit to blame it on — the same "+
				"argument PIN001 makes for `uses:`. Use `name:tag@sha256:<64 hex>`.\n  %s",
			ref.rel, ref.line, ref.value)

		name, _, _ := strings.Cut(ref.value, "@")
		require.Containsf(t, name, ":",
			"%s:%d pins a digest with no tag beside it. The digest is what makes the run reproducible "+
				"and the tag is what makes it reviewable — `postgres:17-alpine@sha256:…` says which "+
				"image a reader is looking at, and gives Renovate the tag to resolve the next digest "+
				"from.\n  %s", ref.rel, ref.line, ref.value)
	}
}

// minDistinctHexDigits is the floor a real digest clears and a hand-typed one does not.
//
// A sha256 is 64 hex characters; the chance that a genuine one uses fewer than 8 of the 16 possible
// digits is about 1 in 10^19. Every placeholder anyone actually writes — all zeros, all f's, a
// repeated `deadbeef` — is far below it. The test is therefore a statement about hand-authored
// strings, not about hashes, and it has no false positives to trade against.
const minDistinctHexDigits = 8

// TestContainerPins_NoDigest_IsAPlaceholder is issue #246's actual defect, as a test.
//
// `docker pull` is the only thing that ever objected to `sha256:0000…0000`, and it objected inside a
// nightly job on a runner, in a log nobody reads until the whole nightly is being triaged. Here it
// is a red `make check` on the commit that introduces it.
func TestContainerPins_NoDigest_IsAPlaceholder(t *testing.T) {
	t.Parallel()

	for _, ref := range workflowImageRefs(t) {
		m := digestRe.FindStringSubmatch(ref.value)
		if m == nil {
			continue // an unpinned image is the test above's finding, reported once rather than twice
		}

		require.Falsef(t, placeholderDigest(m[1]),
			"%s:%d pins a digest built from %d distinct hex characters, which no real sha256 is — this "+
				"is a placeholder somebody meant to fill in. `docker pull` fails with `manifest "+
				"unknown` before the job's first step, so the job is red every run and reports nothing "+
				"about its subject (issue #246). Resolve the real digest:\n"+
				"  docker manifest inspect <image>:<tag>\n  %s",
			ref.rel, ref.line, distinctHexDigits(m[1]), ref.value)
	}
}

// distinctHexDigits counts how much of the hex alphabet a digest uses.
func distinctHexDigits(digest string) int {
	seen := map[rune]bool{}
	for _, c := range digest {
		seen[c] = true
	}

	return len(seen)
}

// placeholderDigest reports whether a 64-hex-character digest was typed by a person rather than
// produced by a hash.
func placeholderDigest(digest string) bool {
	return distinctHexDigits(digest) < minDistinctHexDigits
}

// TestContainerPins_ThePlaceholderRule_SeparatesAHashFromAHandTypedString exercises the rule above
// on strings rather than on the tree, so the gate has been SEEN to go red.
//
// The tree is expected to be clean, which means the two tests above pass without ever running their
// failure branch — the shape this repository distrusts everywhere else. The digests here are the
// ones a person actually types when they mean "fill this in later", against the real digest this
// repository pins.
//
// The last case is the honest limit of the rule, written down rather than left for someone to
// discover: a hand-typed string that walks the whole hex alphabet passes. Nobody types that by
// accident, and the rule is about accidents.
func TestContainerPins_ThePlaceholderRule_SeparatesAHashFromAHandTypedString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		digest      string
		placeholder bool
	}{
		{
			name:        "the postgres:17-alpine index this repository pins",
			digest:      "d4bb0a8c1b7bb2e29f976d099e7bfb9a5d8858cffe9e46b35cd302cd1f1f8168",
			placeholder: false,
		},
		{
			name:        "the checkout action's commit SHA, padded — a second well-mixed witness",
			digest:      pinnedCheckoutSHA + strings.Repeat(pinnedCheckoutSHA[:8], 3),
			placeholder: false,
		},
		{
			name:        "all zeros — the one that shipped (issue #246)",
			digest:      strings.Repeat("0", 64),
			placeholder: true,
		},
		{
			name:        "all f's",
			digest:      strings.Repeat("f", 64),
			placeholder: true,
		},
		{
			name:        "a repeated deadbeef",
			digest:      strings.Repeat("deadbeef", 8),
			placeholder: true,
		},
		{
			name:        "0123456789abcdef repeated — a hand-typed string that is NOT caught",
			digest:      strings.Repeat("0123456789abcdef", 4),
			placeholder: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Len(t, tc.digest, 64, "a sha256 is 64 hex characters; this fixture is malformed")
			require.Equalf(t, tc.placeholder, placeholderDigest(tc.digest),
				"placeholderDigest(%q) used %d distinct hex characters against a floor of %d",
				tc.digest, distinctHexDigits(tc.digest), minDistinctHexDigits)
		})
	}
}

// TestContainerPins_TheScan_ReadsAValueAndNotAJobNamedImage pins the parsing decision the scan rests
// on, because getting it wrong is silent in both directions.
//
// `ci.yml`, `edge.yml` and `release.yml` each declare a JOB called `image`, which is the line
// `  image:` with nothing after it. A pattern that matched those would fail the gate on three
// workflows that reference no container at all; a pattern that stopped matching a real declaration
// would leave the gate reading an empty list, which the scan's own NotEmpty check turns into a
// failure rather than a pass.
func TestContainerPins_TheScan_ReadsAValueAndNotAJobNamedImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "a service container", line: "        image: postgres:17-alpine@sha256:abc", want: "postgres:17-alpine@sha256:abc"},
		{name: "a job named image", line: "  image:", want: ""},
		{name: "a job named image, trailing space", line: "  image: ", want: ""},
		{name: "a job container", line: "    image: ghcr.io/x/y:1@sha256:abc", want: "ghcr.io/x/y:1@sha256:abc"},
		{name: "an action input", line: "          image: alpine:3@sha256:abc", want: "alpine:3@sha256:abc"},
		{name: "a key that merely ends in image", line: "    base-image: alpine:3", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got string
			if m := imageRefRe.FindStringSubmatch(tc.line); m != nil {
				got = m[1]
			}

			require.Equal(t, tc.want, got, "imageRefRe read %q as %q", tc.line, got)
		})
	}
}

// workflowImageRefs returns every `image:` value declared under .github, with its location.
//
// Both workflows and composite actions are read: an action can name an image too, and a rule that
// covered only .github/workflows would leave the tree next door uncovered for no reason a reader
// could reconstruct.
func workflowImageRefs(t *testing.T) []imageRef {
	t.Helper()

	root := repoRoot(t)
	dir := filepath.Join(root, ".github")

	var refs []imageRef

	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || (!strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml")) {
			return nil
		}

		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		for i, line := range strings.Split(string(body), "\n") {
			// Prose about an image is not a declaration of one. Every workflow in this repository
			// explains its pins in comments, and this file's own subject matter appears in several.
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
				continue
			}

			if m := imageRefRe.FindStringSubmatch(line); m != nil {
				refs = append(refs, imageRef{rel: filepath.ToSlash(rel), line: i + 1, value: m[1]})
			}
		}

		return nil
	}), "walk .github for image: declarations")

	// A regex that stopped matching would pass both tests having read nothing — the vacuous green
	// this package exists to refuse.
	require.NotEmpty(t, refs,
		"no `image:` declaration found anywhere under .github. Either the last container reference was "+
			"removed — in which case delete these tests rather than leave a scan with no subject — or "+
			"the pattern no longer matches the file format")

	return refs
}
