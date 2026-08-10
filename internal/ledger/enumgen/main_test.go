package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
)

// The generator's own tests. internal/ledger/kinds_test.go proves the RENDERING is right; these
// prove the file handling is — that a stale region is actually rewritten, that an up-to-date file is
// left alone, and that a file it cannot understand makes it refuse instead of exit 0.
//
// The last one is the case worth a test: a generator that cannot find its target and succeeds
// anyway leaves every gate downstream reporting a clean tree it never wrote to.

// fixture writes a miniature schema.hcl into t.TempDir() and returns its path. The markers are taken
// from the real rendering, so a change to the marker text cannot leave this test passing against a
// file shape that no longer exists.
func fixture(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "schema.hcl")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	return path
}

// markedSchema returns a fixture body whose generated region holds stale values: the real rendered
// block with one kind dropped from the CHECK. Both markers survive, so the generator's job is to
// notice the region is out of date and replace it.
func markedSchema(t *testing.T) string {
	t.Helper()

	current := ledger.SchemaEnumBlock()

	stale := strings.Replace(current, "'attendance', ", "", 1)
	require.NotEqual(t, current, stale, "fixture is stale: the rendered block no longer contains 'attendance'")

	return "table \"ledger_batch\" {\n" + stale + "\n}\n"
}

func TestRun_StaleRegion_IsRewrittenFromTheCatalogue(t *testing.T) {
	t.Parallel()

	path := fixture(t, markedSchema(t))

	require.NoError(t, run(path))

	got, err := os.ReadFile(path)
	require.NoError(t, err)

	require.Contains(t, string(got), ledger.CheckExpr("kind", ledger.BatchKinds()))
	require.Contains(t, string(got), ledger.CheckExpr("source", ledger.BatchSources()))

	// And the rewrite is exactly what a render of the ORIGINAL would produce — no drift between the
	// generator's write path and the drift test's comparison.
	want, err := ledger.RenderSchemaHCL(markedSchema(t))
	require.NoError(t, err)
	require.Equal(t, want, string(got))
}

// TestRun_UpToDateFile_IsNotRewritten pins the no-op path. `make gen` runs reflexively, and a
// generator that rewrote the single source of schema truth on every invocation would make every
// build look like the schema had moved.
//
// The probe is the file MODE, not its mtime: writeAtomic renames a fresh temp file into place and
// chmods it 0644, so a 0600 file that is still 0600 afterwards is proof no write happened — an
// mtime comparison could pass by landing inside the same filesystem timestamp tick.
func TestRun_UpToDateFile_IsNotRewritten(t *testing.T) {
	t.Parallel()

	path := fixture(t, markedSchema(t))
	require.NoError(t, run(path))
	require.NoError(t, os.Chmod(path, 0o600))

	before, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NoError(t, run(path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "an up-to-date file was rewritten")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

func TestRun_MissingMarkers_RefusesAndLeavesTheFileAlone(t *testing.T) {
	t.Parallel()

	body := "table \"ledger_batch\" {\n  check \"ledger_batch_kind_enum\" {\n  }\n}\n"
	path := fixture(t, body)

	err := run(path)
	require.ErrorIs(t, err, ledger.ErrSchemaMarkersMissing)

	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, body, string(got), "a refused render must not touch the schema")
}

func TestRun_UnreadablePath_IsAnError(t *testing.T) {
	t.Parallel()

	err := run(filepath.Join(t.TempDir(), "does-not-exist.hcl"))
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}
