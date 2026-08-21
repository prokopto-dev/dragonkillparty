package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
)

// The generator's own tests. `make verify-generated` already proves the committed pages are what a
// regeneration produces — it hashes docs/reference, regenerates and hashes again — so what these add
// is the half a hash cannot: WHICH page lost WHICH key, said in one line, on a laptop, before CI.
//
// They also cover the file handling, because a generator that cannot write and exits 0 leaves every
// gate downstream reporting success over a page nobody rewrote.

// repoRoot returns the directory holding go.mod, walked rather than assumed, so these tests find
// docs/ regardless of where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")

		dir = parent
	}
}

// committed reads one of the two pages as it is committed.
func committed(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), defaultDir, name)

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s — run `make gen`", path)

	return string(raw)
}

// TestDocgen_CommittedPages_MatchTheCatalogue is the drift assertion, said in the generator's own
// terms: regenerating changes nothing.
//
// The same question `make verify-generated` asks of every generated tree, asked here as an ordinary
// test so the failure names the page rather than a hash.
func TestDocgen_CommittedPages_MatchTheCatalogue(t *testing.T) {
	t.Parallel()

	for name, render := range map[string]func() string{
		permissionsFile: permissionsPage,
		scopesFile:      scopesPage,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, render(), committed(t, name),
				"docs/reference/%s has drifted from internal/authz/catalogue.go — run `make gen` and "+
					"commit the diff. The page is generated; edit the catalogue, not the Markdown.", name)
		})
	}
}

// TestDocgen_PermissionsPage_CarriesEveryKey asserts the page is complete rather than merely current.
//
// A page that agrees with a renderer which itself drops keys would satisfy the drift test above and
// still publish a partial catalogue — the reference page is where a bot author looks up what a
// permission is called, so a missing row is a key nobody outside this repository can discover.
func TestDocgen_PermissionsPage_CarriesEveryKey(t *testing.T) {
	t.Parallel()

	page := committed(t, permissionsFile)

	for _, p := range authz.Catalogue() {
		require.Containsf(t, page, "`"+p.Key+"`", "permissions.md does not mention %s", p.Key)
		require.Containsf(t, page, p.Description, "permissions.md does not carry %s's description", p.Key)
	}

	for _, key := range authz.CapabilityFloor() {
		require.Containsf(t, page, "- `"+key+"`",
			"permissions.md's capability-floor list is missing %s", key)
	}
}

// TestDocgen_ScopesPage_CarriesEveryScope is the same completeness check for the scope list.
func TestDocgen_ScopesPage_CarriesEveryScope(t *testing.T) {
	t.Parallel()

	page := committed(t, scopesFile)

	for _, s := range authz.Scopes() {
		require.Containsf(t, page, "`"+s.Key+"`", "scopes.md does not mention %s", s.Key)
		require.Containsf(t, page, s.Description, "scopes.md does not carry %s's description", s.Key)
	}
}

// TestDocgen_StepUpMarkers_AreExactlyTheCapabilityFloor ties the rendered flag to the set that
// defines it.
//
// The Flags column is what an officer reads when deciding whether a role should hold a key, and
// "step-up" there means "this needs a re-authenticated session and no PAT can do it". A marker on the
// wrong row tells them the opposite of the truth about a capability-floor key.
func TestDocgen_StepUpMarkers_AreExactlyTheCapabilityFloor(t *testing.T) {
	t.Parallel()

	floor := map[string]bool{}
	for _, key := range authz.CapabilityFloor() {
		floor[key] = true
	}

	for _, p := range authz.Catalogue() {
		row := tableRow(t, committed(t, permissionsFile), p.Key)

		if floor[p.Key] {
			require.Containsf(t, row, "**step-up**",
				"%s is in the capability floor and its row carries no step-up marker", p.Key)

			continue
		}

		require.NotContainsf(t, row, "**step-up**",
			"%s is not in the capability floor and its row is marked step-up", p.Key)
	}
}

// TestDocgen_DangerousMarker_IsOnTheOneDocumentedKey pins the other flag.
func TestDocgen_DangerousMarker_IsOnTheOneDocumentedKey(t *testing.T) {
	t.Parallel()

	page := committed(t, permissionsFile)

	for _, p := range authz.Catalogue() {
		row := tableRow(t, page, p.Key)

		if p.IsDangerous {
			require.Containsf(t, row, "**dangerous**", "%s is dangerous and its row says nothing", p.Key)

			continue
		}

		require.NotContainsf(t, row, "**dangerous**", "%s is not dangerous and its row is marked", p.Key)
	}
}

// tableRow returns the one line of page whose first cell is the given key.
func tableRow(t *testing.T, page, key string) string {
	t.Helper()

	prefix := "| `" + key + "` |"

	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}

	require.FailNowf(t, "no table row", "permissions.md has no row for %s", key)

	return ""
}

// TestDocgen_Pages_CarryTheGeneratedBanner is small and load-bearing.
//
// The banner is the only thing standing between a contributor who spots a typo and an edit that the
// next `make gen` silently erases — after they have committed it, and after review has read it. It
// names the file to edit instead.
func TestDocgen_Pages_CarryTheGeneratedBanner(t *testing.T) {
	t.Parallel()

	for _, name := range []string{permissionsFile, scopesFile} {
		page := committed(t, name)

		require.Truef(t, strings.HasPrefix(page, generatedBanner),
			"%s does not open with the generated banner, so nothing tells a reader that editing it "+
				"is pointless", name)
	}
}

// TestDocgen_Run_IsIdempotentAndCreatesTheDirectory covers the file handling against a scratch tree:
// a first run creates the directory and both pages, and a second changes nothing.
//
// The no-op path matters for the reason enumgen's does — `make gen` runs reflexively, and a generator
// that rewrites its output every time makes every build look like the docs had moved. The probe is
// the file MODE rather than the mtime: writeIfChanged renames a fresh 0644 temp file into place, so a
// 0600 file that is still 0600 afterwards is proof no write happened, where an mtime comparison could
// pass by landing inside one filesystem timestamp tick.
func TestDocgen_Run_IsIdempotentAndCreatesTheDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "reference")

	require.NoError(t, run(dir))

	for _, name := range []string{permissionsFile, scopesFile} {
		path := filepath.Join(dir, name)

		require.FileExists(t, path)
		require.NoError(t, os.Chmod(path, 0o600))
	}

	require.NoError(t, run(dir), "a second run must be a no-op")

	for _, name := range []string{permissionsFile, scopesFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
			"%s was rewritten by a run that had nothing to change", name)
	}
}

// TestDocgen_Run_RewritesAStalePage is the positive control for the no-op above: a page that has
// drifted IS rewritten, and to exactly what the renderer produces.
//
// Without it, the idempotence test is indistinguishable from a generator that never writes at all.
func TestDocgen_Run_RewritesAStalePage(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "reference")
	require.NoError(t, run(dir))

	path := filepath.Join(dir, permissionsFile)
	require.NoError(t, os.WriteFile(path, []byte("# Permissions\n\nsomebody edited this by hand\n"), 0o644))

	require.NoError(t, run(dir))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, permissionsPage(), string(got), "a hand-edited page survived a regeneration")
}

// TestDocgen_Run_UnwritableDirectory_IsAnError proves the generator refuses rather than exiting 0
// when it cannot write, which is the failure that would leave every downstream gate green over a
// page nobody rewrote.
func TestDocgen_Run_UnwritableDirectory_IsAnError(t *testing.T) {
	t.Parallel()

	// A FILE where the output directory should be: MkdirAll cannot create a directory over it.
	blocked := filepath.Join(t.TempDir(), "reference")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o644))

	require.Error(t, run(blocked))
}
