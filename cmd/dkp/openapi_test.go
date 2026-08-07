package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAPICmd_Output_MatchesTheCommittedFileByteForByte is acceptance criterion 4 of
// docs/development/first-ten-prs.md PR 4, asserted on the command rather than on the library.
//
// internal/api's TestOpenAPI_SpecJSON_MatchesCommittedFile already covers api.SpecJSON(). This
// covers the three lines between that function and stdout, which are exactly where a byte-for-byte
// guarantee gets lost: an fmt.Fprintln instead of a Write, a trailing newline added or dropped, a
// cobra flag that prefixes something. scripts/gen-openapi.sh redirects this command into the
// committed file, so those three lines ARE the generator.
//
// It runs the command in-process with a bytes.Buffer rather than shelling out to `go run`, which
// would cost a compile inside a `make test-unit` budget of under five seconds and would test the Go
// toolchain more than this code.
func TestOpenAPICmd_Output_MatchesTheCommittedFileByteForByte(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	cmd := newOpenAPICmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute(), "dkp openapi failed: %s", out.String())

	committed, err := os.ReadFile(filepath.Join(repoRootForTest(t), "openapi", "openapi.json"))
	require.NoError(t, err, "openapi/openapi.json is missing — run `make gen`")

	require.Equal(t, string(committed), out.String(),
		"`dkp openapi` and the committed openapi/openapi.json disagree. Run `make gen` and commit "+
			"the diff; if they still disagree, the command is adding or dropping bytes that "+
			"scripts/gen-openapi.sh does not.")
}

// TestOpenAPICmd_RejectsArguments keeps the generator's interface closed.
//
// The command takes no arguments and no flags on purpose: `make gen` redirects its stdout into a
// committed, diff-gated file, so every option is another way for the emitted document and the
// committed one to differ. cobra.NoArgs is what enforces that, and this is what proves it is set.
func TestOpenAPICmd_RejectsArguments(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	cmd := newOpenAPICmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"unexpected"})

	require.Error(t, cmd.Execute(), "dkp openapi accepted a positional argument")
}

// TestOpenAPICmd_IsRegisteredOnTheRootCommand catches the wiring being forgotten.
//
// A command that exists but is not added to the tree compiles, tests green in isolation, and does
// not exist for a user or for `make gen`.
func TestOpenAPICmd_IsRegisteredOnTheRootCommand(t *testing.T) {
	t.Parallel()

	var found bool

	for _, sub := range newRootCmd().Commands() {
		if sub.Name() == "openapi" {
			found = true
		}
	}

	require.True(t, found, "`dkp openapi` is not registered on the root command")
}

// repoRootForTest walks up to the directory holding go.mod.
func repoRootForTest(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")

		dir = parent
	}
}
