package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// missingTargetLine matches the lines verify-commands.sh prints for each AGENTS.md row that has no
// Makefile target: `printf '  make %s\n'`.
var missingTargetLine = regexp.MustCompile(`(?m)^ {2}make ([a-z][a-z0-9-]*)$`)

// missingTargets extracts the target names verify-commands.sh reported as missing.
//
// Parsing the list, rather than only checking the exit code, is what makes the negative subtest
// below fail for the RIGHT reason: a broken DKP_REPO_ROOT also exits non-zero, and so would a
// regression that made `make -n migration` look absent because of its `ifndef NAME` guard.
func missingTargets(output string) []string {
	var names []string
	for _, m := range missingTargetLine.FindAllStringSubmatch(output, -1) {
		names = append(names, m[1])
	}

	return names
}

// copyFile copies src to dst verbatim.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()

	body, err := os.ReadFile(src)
	require.NoError(t, err, "read %s", src)
	require.NoError(t, os.WriteFile(dst, body, 0o644), "write %s", dst)
}

// TestMakefile_CanonicalCommandTable_EveryRowIsARealTarget asserts the mechanism that keeps
// AGENTS.md from rotting: every `make <target>` in its canonical command table resolves to a real
// Makefile target.
//
// The check itself is scripts/verify-commands.sh — the same code CI and `make check` run. This test
// does not reimplement it; reimplementing would test a second, unrun copy of the logic. What this
// test adds is the direction nobody can exercise by hand: proving the check FAILS when the table
// gains a row with no target behind it, which cannot be demonstrated without committing a broken
// AGENTS.md.
func TestMakefile_CanonicalCommandTable_EveryRowIsARealTarget(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "verify-commands.sh")
	root := repoRoot(t)

	t.Run("this repo's table is clean", func(t *testing.T) {
		t.Parallel()

		out, code := runGateScript(t, script, root)

		require.Zero(t, code, "every AGENTS.md command row must resolve to a Makefile target\n%s", out)
		require.Empty(t, missingTargets(out), "%s", out)
	})

	t.Run("an undocumented row fails", func(t *testing.T) {
		t.Parallel()

		// The fixture is a copy in t.TempDir(), never the repo: appending this row to the real
		// AGENTS.md would break `make check` for everyone.
		tree := t.TempDir()
		copyFile(t, filepath.Join(root, "AGENTS.md"), filepath.Join(tree, "AGENTS.md"))
		copyFile(t, filepath.Join(root, "Makefile"), filepath.Join(tree, "Makefile"))

		// The row must be in the shape verify-commands.sh greps for — a backtick-quoted
		// `make <name>`. A row it silently skips would make this subtest pass for the wrong
		// reason, which is why the assertion below names the target rather than trusting the
		// exit code.
		row := "| a row nobody added a target for | `make totally-bogus` | — |\n"

		fixture := filepath.Join(tree, "AGENTS.md")
		f, err := os.OpenFile(fixture, os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err, "open the AGENTS.md fixture for append")

		_, err = f.WriteString(row)
		require.NoError(t, err, "append the bogus row")
		require.NoError(t, f.Close(), "close the AGENTS.md fixture")

		out, code := runGateScript(t, script, tree)

		require.NotZero(t, code, "a table row with no Makefile target must fail the check\n%s", out)
		require.Equal(t, []string{"totally-bogus"}, missingTargets(out),
			"the check must report exactly the bogus target — no more (a false positive such as "+
				"`migration`, which needs NAME) and no fewer (the row was parsed, not skipped)\n%s", out)
	})
}
