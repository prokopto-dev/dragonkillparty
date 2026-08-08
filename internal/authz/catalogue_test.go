package authz_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
)

// The canonical §6 fenced blocks are the source of truth, and this file compares the Go catalogue
// against them element by element and in both directions.
//
// This is the same mechanism internal/api/errors_test.go's TestErrors_Enum_MatchesPublishedCatalogue
// uses against docs/api/errors.md, and for the same reason: without it, authz/catalogue.go is a
// second hand-maintained copy of a list canonical §6 forbids duplicating. "In both directions"
// matters — one direction catches a key the catalogue dropped, the other catches a key the catalogue
// invented, and only checking both catches a key silently moved to the wrong spelling.

// canonicalConventionsPath locates docs/design/00-canonical-conventions.md from the repo root, which
// is the directory above internal/ holding go.mod. Not filepath.Abs("../../.."), which produces the
// wrong answer silently the day this file moves.
func canonicalConventionsPath(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return filepath.Join(dir, "docs", "design", "00-canonical-conventions.md")
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")

		dir = parent
	}
}

// fencedListAfter reads the canonical conventions file and returns the whitespace-separated tokens
// of the first ``` fenced block whose opening fence follows a line containing marker.
//
// The §6 key list and scope list are each a run of space-and-newline-separated tokens inside a fenced
// block, introduced by a bold sentence ("**Permission keys** are ...", "**PAT scopes** are ..."). We
// find that sentence, then take the next fenced block and split it on any whitespace.
func fencedListAfter(t *testing.T, marker string) []string {
	t.Helper()

	f, err := os.Open(canonicalConventionsPath(t))
	require.NoError(t, err, "open canonical conventions")
	defer func() { require.NoError(t, f.Close()) }()

	var (
		seenMarker bool
		inFence    bool
		tokens     []string
	)

	sc := bufio.NewScanner(f)
	// The fenced blocks are short; the default 64 KiB line limit is ample, but be explicit so a
	// future long line does not truncate silently.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()

		if !seenMarker {
			if strings.Contains(line, marker) {
				seenMarker = true
			}

			continue
		}

		if strings.TrimSpace(line) == "```" {
			if inFence {
				// End of the block we wanted.
				break
			}

			inFence = true

			continue
		}

		if inFence {
			tokens = append(tokens, strings.Fields(line)...)
		}
	}

	require.NoError(t, sc.Err(), "scan canonical conventions")
	require.True(t, seenMarker, "did not find the marker %q in the canonical conventions", marker)
	require.NotEmpty(t, tokens, "the fenced block after %q was empty", marker)

	return tokens
}

// TestCatalogue_Permissions_MatchCanonicalConventions compares authz.Catalogue()'s keys against the
// fenced §6 permission list, element by element and in both directions.
func TestCatalogue_Permissions_MatchCanonicalConventions(t *testing.T) {
	t.Parallel()

	want := fencedListAfter(t, "**Permission keys** are")

	got := make([]string, 0, len(authz.Catalogue()))
	for _, p := range authz.Catalogue() {
		got = append(got, p.Key)
	}

	// Whole-slice comparison, in order. require reports the first divergence and stops, which is
	// exactly what is wanted: element-by-element, both directions, and a stable failure.
	require.Equal(t, want, got,
		"authz.Catalogue() must list exactly the canonical §6 permission keys, in order. "+
			"A key here that §6 does not have is an invented permission; a §6 key missing here is a "+
			"boot failure waiting to happen — role_permission is FK-constrained to permission(key).")
}

// TestCatalogue_Scopes_MatchCanonicalConventions does the same for the PAT scope list.
func TestCatalogue_Scopes_MatchCanonicalConventions(t *testing.T) {
	t.Parallel()

	want := fencedListAfter(t, "**PAT scopes** are")

	got := make([]string, 0, len(authz.Scopes()))
	for _, s := range authz.Scopes() {
		got = append(got, s.Key)
	}

	require.Equal(t, want, got,
		"authz.Scopes() must list exactly the canonical §6 PAT scopes, in order.")
}

// TestCatalogue_Permissions_AreWholeQuotedLiterals is the SPEC005 contract, asserted in Go.
//
// scripts/verify-spec.py's SPEC005 greps this package's source for `"<key>"` — a whole quoted
// literal — so a key composed from parts (Resource + "." + Action) produces the right runtime value
// and fails the gate. This test proves the source-text property directly, so a refactor that breaks
// it goes red here in `make test-unit` before it reaches the spec gate, with a message that explains
// why rather than a bare SPEC005 substring miss.
func TestCatalogue_Permissions_AreWholeQuotedLiterals(t *testing.T) {
	t.Parallel()

	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")

	source, err := os.ReadFile(filepath.Join(dir, "catalogue.go"))
	require.NoError(t, err, "read catalogue.go source")

	text := string(source)

	for _, p := range authz.Catalogue() {
		require.Contains(t, text, `"`+p.Key+`"`,
			"permission key %q does not appear as a whole quoted literal in catalogue.go. SPEC005 "+
				"greps for the quoted key, so a composed key fails the spec gate even though the "+
				"runtime value is correct. See doc.go.", p.Key)
	}
}

// TestCatalogue_Permissions_CarryLabelAndDescription asserts the catalogue is populated, not a bare
// key list. Every permission has a category, a label and a description, because the reference page
// and the role editor render all three.
func TestCatalogue_Permissions_CarryLabelAndDescription(t *testing.T) {
	t.Parallel()

	for _, p := range authz.Catalogue() {
		require.NotEmpty(t, p.Category, "%s has no Category", p.Key)
		require.NotEmpty(t, p.Label, "%s has no Label", p.Key)
		require.NotEmpty(t, p.Description, "%s has no Description", p.Key)
	}
}

// TestCatalogue_Permissions_KeysAreUnique guards against a copy-paste duplicate that the ordered
// comparison against §6 would catch only if §6 itself were duplicate-free. A duplicate key would
// seed the permission table twice and violate its primary key at boot.
func TestCatalogue_Permissions_KeysAreUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	for _, p := range authz.Catalogue() {
		_, dup := seen[p.Key]
		require.False(t, dup, "permission key %q is declared twice", p.Key)
		seen[p.Key] = struct{}{}
	}
}

// TestCapabilityFloor_KeysAreInCatalogue asserts every capability-floor key is a real permission.
// A floor key that is not in the catalogue is a rule the FK cannot enforce and the arch test would
// derive its x-dkp-pat-forbidden set from a phantom key.
func TestCapabilityFloor_KeysAreInCatalogue(t *testing.T) {
	t.Parallel()

	keys := make(map[string]struct{}, len(authz.Catalogue()))
	for _, p := range authz.Catalogue() {
		keys[p.Key] = struct{}{}
	}

	for _, k := range authz.CapabilityFloor() {
		_, ok := keys[k]
		require.True(t, ok, "capability-floor key %q is not in the permission catalogue", k)
	}
}
