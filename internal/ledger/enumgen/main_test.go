package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	decaykinds "github.com/prokopto-dev/dragonkillparty/internal/decay/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
)

// The generator's own tests. Each catalogue's own test file proves its RENDERING is right; these
// prove the file handling is — that a stale region is actually rewritten, that an up-to-date file is
// left alone, and that a file it cannot understand makes it refuse instead of exit 0.
//
// The last one is the case worth a test: a generator that cannot find its target and succeeds
// anyway leaves every gate downstream reporting a clean tree it never wrote to. It matters more with
// every region added — there are FOUR — because a file carrying only some of them must refuse, not
// quietly rewrite the regions it recognises and leave the rest frozen.

// fixture writes a miniature schema.hcl into t.TempDir() and returns its path. The markers are taken
// from the real rendering, so a change to the marker text cannot leave this test passing against a
// file shape that no longer exists.
func fixture(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "schema.hcl")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	return path
}

// markedSchema returns a fixture body whose four generated regions ALL hold stale values: each
// catalogue's real rendered block with one value dropped from its CHECK. Every marker survives, so
// the generator's job is to notice all four regions are out of date and replace them.
//
// All four are stale rather than one, because the failure this shape catches is a generator that
// stops after the first render — which would leave the later regions frozen while reporting success.
func markedSchema(t *testing.T) string {
	t.Helper()

	stale := func(block, value string) string {
		t.Helper()

		out := strings.Replace(block, value, "", 1)
		require.NotEqual(t, block, out, "fixture is stale: a rendered block no longer contains %q", value)

		return out
	}

	return "table \"ledger_batch\" {\n" + stale(kinds.SchemaEnumBlock(), "'attendance', ") + "\n}\n\n" +
		"table \"audit_log\" {\n" + stale(auditkinds.SchemaEnumBlock(), "'boot', ") + "\n}\n\n" +
		"table \"account\" {\n" + stale(accountkinds.SchemaEnumBlock(), "'write_off', ") + "\n}\n\n" +
		"table \"decay_run\" {\n" + stale(decaykinds.SchemaEnumBlock(), "'skipped', ") + "\n}\n"
}

func TestRun_StaleRegion_IsRewrittenFromTheCatalogue(t *testing.T) {
	t.Parallel()

	path := fixture(t, markedSchema(t))

	require.NoError(t, run(path))

	got, err := os.ReadFile(path)
	require.NoError(t, err)

	require.Contains(t, string(got), kinds.CheckExpr("kind", kinds.BatchKinds()))
	require.Contains(t, string(got), kinds.CheckExpr("source", kinds.BatchSources()))
	require.Contains(t, string(got), auditkinds.ActorKindCheckExpr())
	require.Contains(t, string(got), auditkinds.OutcomeCheckExpr())
	require.Contains(t, string(got), accountkinds.KindCheckExpr())
	require.Contains(t, string(got), accountkinds.SystemKeyCheckExpr())
	require.Contains(t, string(got), decaykinds.StateCheckExpr())

	// And the rewrite is exactly what rendering the ORIGINAL through every catalogue would produce —
	// no drift between the generator's write path and the drift tests' comparison.
	want, err := kinds.RenderSchemaHCL(markedSchema(t))
	require.NoError(t, err)

	want, err = auditkinds.RenderSchemaHCL(want)
	require.NoError(t, err)

	want, err = accountkinds.RenderSchemaHCL(want)
	require.NoError(t, err)

	want, err = decaykinds.RenderSchemaHCL(want)
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

// TestRun_MissingMarkers_RefusesAndLeavesTheFileAlone covers the whole file being unrecognisable and
// — the case a second catalogue introduces — a file carrying one region and not the other.
//
// A HALF-MARKED FILE MUST REFUSE, not rewrite what it recognises: a partial write would leave
// db/schema.hcl holding one freshly generated region and one frozen at whatever it last said, which
// is drift produced BY the generator and blessed by its own exit code.
func TestRun_MissingMarkers_RefusesAndLeavesTheFileAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "no region marked",
			body: "table \"ledger_batch\" {\n  check \"ledger_batch_kind_enum\" {\n  }\n}\n",
		},
		{
			name: "ledger region marked, the other two missing",
			body: "table \"ledger_batch\" {\n" + kinds.SchemaEnumBlock() + "\n}\n",
		},
		{
			name: "audit region marked, the other two missing",
			body: "table \"audit_log\" {\n" + auditkinds.SchemaEnumBlock() + "\n}\n",
		},
		{
			name: "account region marked, the other two missing",
			body: "table \"account\" {\n" + accountkinds.SchemaEnumBlock() + "\n}\n",
		},
		{
			name: "only the account region missing — the one a new catalogue forgets",
			body: "table \"ledger_batch\" {\n" + kinds.SchemaEnumBlock() + "\n}\n\n" +
				"table \"audit_log\" {\n" + auditkinds.SchemaEnumBlock() + "\n}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := fixture(t, tt.body)

			err := run(path)
			require.ErrorIs(t, err, kinds.ErrSchemaMarkersMissing)

			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, tt.body, string(got), "a refused render must not touch the schema")
		})
	}
}

func TestRun_UnreadablePath_IsAnError(t *testing.T) {
	t.Parallel()

	err := run(filepath.Join(t.TempDir(), "does-not-exist.hcl"))
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}
