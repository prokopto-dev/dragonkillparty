package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Atlas takes a MACHINE-WIDE advisory lock for the length of a `migrate diff`, and it derives that
// lock's name from the dev-url: `atlas_migrate_diff_<hash-of-url>`. One fixed dev-url therefore
// gives every concurrent Atlas invocation on the machine a single lock to contend for — and the
// losers do not queue, they exit 1 with
//
//	Error: acquiring database lock: sql/sqlite: lock on "atlas_migrate_diff_41401a1" already taken
//
// which is what issue #36 was: `make check` failing intermittently in this package, with a message
// that names Atlas and the community build and so reads as a toolchain or schema fault. The person
// most likely to see it is whoever just touched migrations, and they will reasonably assume they
// caused it and start undoing correct work. Measured before the fix: 7 failures in 8 concurrent
// diffs.
//
// atlas.hcl now derives a fresh dev database name per invocation. The two tests here hold the two
// halves of that: the name really is per-invocation (statically, always), and real concurrent
// invocations really do all succeed (dynamically, wherever Atlas is installed).

// fixedAtlasDevURL is the dev-url that must never be pinned again, assembled from fragments so that
// this file is not itself a match for the scan below — the same reason docs_markers_test.go builds
// its marker that way.
var fixedAtlasDevURL = "sqlite://" + "file?mode=memory"

// TestAtlasHCL_DevURL_IsPerInvocation is the static half, and it is the half that still runs when
// Atlas is not installed and under `-short`, where the concurrency test below skips.
//
// It asserts the property rather than the mechanism: the dev-url must INTERPOLATE something. A
// literal is a shared lock however it is spelled, so re-pinning it under a different database name
// would fail here too.
func TestAtlasHCL_DevURL_IsPerInvocation(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "atlas.hcl"))
	require.NoError(t, err, "read atlas.hcl")

	var devLines []string

	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "dev") && strings.Contains(trimmed, "=") {
			devLines = append(devLines, trimmed)
		}
	}

	require.Len(t, devLines, 1,
		"atlas.hcl must declare exactly one dev-url — every Atlas invocation goes through "+
			"`--env sqlite` so that it is declared once, and a second one is a second lock name "+
			"nobody is checking: %v", devLines)

	require.Contains(t, devLines[0], "${",
		"atlas.hcl's dev-url is a fixed string, so every concurrent `atlas migrate diff` on this "+
			"machine will contend for one advisory lock and the losers will fail with `acquiring "+
			"database lock: ... already taken` (issue #36). The dev database is scratch and its name "+
			"reaches no output — give each invocation its own:\n  %s", devLines[0])
}

// TestAtlas_FixedDevURL_AppearsNowhere is the fence around the other call sites.
//
// The concurrency test below exercises atlas.hcl, which is where every `--env sqlite` invocation
// gets its dev-url. It does not exercise the places that pass `--dev-url` directly — today that is
// one Go test, tomorrow it is whatever a new one copies from. The fixed string is the thing that
// gets copied, so nothing in the tree may carry it.
func TestAtlas_FixedDevURL_AppearsNowhere(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	listed, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	require.NoError(t, err, "list tracked files with git ls-files")

	var offenders []string

	for _, rel := range strings.Split(strings.TrimSuffix(string(listed), "\x00"), "\x00") {
		if rel == "" {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if readErr != nil {
			continue // a tracked path that is not readable here (a submodule, a deleted file) is not this test's business
		}

		if strings.Contains(string(body), fixedAtlasDevURL) {
			offenders = append(offenders, rel)
		}
	}

	require.Empty(t, offenders,
		"%q is the dev-url every Atlas invocation used to share, and sharing it means sharing one "+
			"machine-wide advisory lock — concurrent invocations fail rather than queue (issue #36). "+
			"Give the invocation its own database name instead; the dev database is scratch and its "+
			"name reaches no output. This scan is deliberately dumb and does not spare prose: a "+
			"tracked file carrying the string is indistinguishable from a call site using it, and a "+
			"document telling someone to run it is how the string spread in the first place — "+
			"describe it rather than quoting it. Found in: %v", fixedAtlasDevURL, offenders)
}

// TestAtlas_ConcurrentInvocations_DoNotShareALock is the dynamic half: the property under the real
// script, the real atlas.hcl and a real Atlas.
//
// Eight, because that is the count the flake was measured at — seven of eight failed when the
// dev-url was fixed, so a regression here is a near-certain red rather than an occasional one. Each
// invocation gets its own tree, so the only thing they share is whatever atlas.hcl gives them.
//
// This is the reason `migrationFixture` copies the repository's atlas.hcl instead of writing its
// own: a fixture carrying a second, hand-written config would go on sharing one lock name after the
// real file stopped, and this test would be measuring the fixture rather than the repository.
func TestAtlas_ConcurrentInvocations_DoNotShareALock(t *testing.T) {
	t.Parallel()

	// Skipped under -short for the same reason TestNewMigration_BacktickInStringLiteral_Refuses is:
	// it invokes Atlas, and `make test-unit` is at ~4s against a <5s budget that CI pays cold. It
	// runs under `make test`, `make check` and the test / integration job.
	if testing.Short() {
		t.Skip("invokes atlas concurrently; run `make test` or `make check`")
	}

	if _, err := exec.LookPath("atlas"); err != nil {
		t.Skip("atlas is not installed; run make setup")
	}

	const invocations = 8

	trees := make([]string, invocations)
	for i := range trees {
		trees[i] = migrationFixture(t, fixtureSchema)
	}

	type run struct {
		out string
		err error
	}

	// Results are collected here and asserted on the test goroutine: require's FailNow is
	// runtime.Goexit, which outside the test goroutine ends the goroutine and lets the test keep
	// running as though nothing failed.
	runs := make([]run, invocations)

	var wg sync.WaitGroup

	for i := range trees {
		wg.Add(1)

		go func() {
			defer wg.Done()

			out, err := runNewMigration(t, trees[i], "add_thing")
			runs[i] = run{out: out, err: err}
		}()
	}

	wg.Wait()

	for i, r := range runs {
		require.NoErrorf(t, r.err,
			"concurrent invocation %d of %d failed. If it says `acquiring database lock: ... already "+
				"taken`, the Atlas dev-url is shared again and every concurrent invocation on the "+
				"machine is contending for one lock (issue #36):\n%s", i+1, invocations, r.out)

		require.FileExistsf(t, filepath.Join(trees[i], "db", "migrations-sqlite", "000001_add_thing.sql"),
			"concurrent invocation %d exited 0 without writing its migration\n%s", i+1, r.out)
	}
}
