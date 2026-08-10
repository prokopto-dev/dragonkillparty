package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// migrationFixture builds a minimal tree that scripts/new-migration.sh can run against: a Makefile
// carrying the ATLAS_VERSION pin shape, the repository's own atlas.hcl, a db/schema.hcl and an
// empty migration directory.
//
// Same DKP_REPO_ROOT mechanism as the licence-gate and install-atlas fixtures, for the same reason:
// a script whose refusals have never been observed firing is a script nobody knows refuses
// anything, and a fixture that triggers them cannot live in this checkout without breaking the real
// `make migration`.
//
// THE REAL atlas.hcl IS COPIED IN rather than re-typed here, and that is load-bearing. Its paths
// are relative (`file://db/schema.hcl`, `file://db/migrations-sqlite`), so it resolves against this
// tree unchanged. A hand-written second copy drifts: the version this fixture carried before issue
// #36 pinned the dev database to one fixed name, so the fix to the real file would have left every
// fixture-driven invocation still contending for the one machine-wide Atlas lock — the flake would
// have survived its own fix, in the tests that exist to catch it.
func migrationFixture(tb testing.TB, schema string) string {
	tb.Helper()

	tree := tb.TempDir()

	write := func(rel, body string) {
		tb.Helper()
		full := filepath.Join(tree, filepath.FromSlash(rel))
		require.NoError(tb, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(tb, os.WriteFile(full, []byte(body), 0o644))
	}

	write("Makefile", "ATLAS_VERSION          ?= v1.3.0\n")

	realHCL, err := os.ReadFile(filepath.Join(repoRoot(tb), "atlas.hcl"))
	require.NoError(tb, err, "read the repository's atlas.hcl")
	write("atlas.hcl", string(realHCL))

	write("db/schema.hcl", schema)
	require.NoError(tb, os.MkdirAll(filepath.Join(tree, "db", "migrations-sqlite"), 0o755))

	return tree
}

// runNewMigration invokes the script with NAME set, against a fixture tree.
func runNewMigration(t *testing.T, tree, name string) (string, error) {
	t.Helper()

	cmd := exec.Command("bash", scriptPath(t, "new-migration.sh"))
	cmd.Dir = tree
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree, "NAME="+name)

	out, err := cmd.CombinedOutput()

	return string(out), err
}

const fixtureSchema = `schema "main" {
}
table "dkp_meta" {
  schema = schema.main
  column "key" {
    null = false
    type = text
  }
  primary_key {
    columns = [column.key]
  }
  strict = true
}
`

// TestNewMigration_BadName_Refuses covers the snake_case rule.
//
// The name becomes a filename that is append-only and therefore permanent — canonical §16 fixes it
// as NNNNNN_snake_case.sql — so it is worth being strict once rather than living with
// 000007_addBidHold.sql forever.
func TestNewMigration_BadName_Refuses(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"addBidHold", "add-bid-hold", "Add_Bid_Hold", "1_add_hold", "add__hold", "add_hold_"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := migrationFixture(t, fixtureSchema)
			out, err := runNewMigration(t, tree, name)

			require.Error(t, err, "%q is not snake_case and must be refused\n%s", name, out)
			require.Contains(t, out, "must be snake_case", "%s", out)

			entries, readErr := os.ReadDir(filepath.Join(tree, "db", "migrations-sqlite"))
			require.NoError(t, readErr)
			require.Empty(t, entries, "a refused name still wrote a migration\n%s", out)
		})
	}
}

// TestNewMigration_DuplicateName_Refuses covers the append-only rule.
//
// Two migrations sharing a name make `git log` on a filename ambiguous and leave an operator
// reading a failure message unable to tell which one failed — and the fix people reach for is
// renaming or renumbering, which makes an applied version disagree with the file that produced it.
func TestNewMigration_DuplicateName_Refuses(t *testing.T) {
	t.Parallel()

	tree := migrationFixture(t, fixtureSchema)

	existing := filepath.Join(tree, "db", "migrations-sqlite", "000001_init.sql")
	require.NoError(t, os.WriteFile(existing, []byte("-- +goose Up\nSELECT 1;\n"), 0o644))

	out, err := runNewMigration(t, tree, "init")

	require.Error(t, err, "a duplicate migration name must be refused\n%s", out)
	require.Contains(t, out, "already exists", "%s", out)
	require.Contains(t, out, "append-only", "the refusal must say why\n%s", out)

	body, readErr := os.ReadFile(existing)
	require.NoError(t, readErr)
	require.Equal(t, "-- +goose Up\nSELECT 1;\n", string(body),
		"the existing migration was modified by a refused run")
}

// TestNewMigration_BacktickInStringLiteral_Refuses is the regression pin for the one input the
// backtick-to-double-quote rewrite would silently corrupt.
//
// Atlas emits backtick-quoted identifiers and sqlc cannot parse them, so the script rewrites them.
// A backtick INSIDE a single-quoted string wrapping something identifier-shaped —
// `DEFAULT 'the ` + "`" + `value` + "`" + ` column'` — would be rewritten too, producing SQL that is
// still valid, still applies cleanly, and now means something different, with nothing in the diff
// to show the generator did it. The script must refuse rather than guess.
func TestNewMigration_BacktickInStringLiteral_Refuses(t *testing.T) {
	t.Parallel()

	// Skipped under -short for the same reason licence_gate_test.go skips its fixtures: this is the
	// only test in the file that actually invokes Atlas, and `make test-unit` is at ~4s against a
	// <5s budget that CI pays cold. It still runs under `make test`, `make check` and the
	// test / integration job.
	if testing.Short() {
		t.Skip("invokes atlas to generate a real migration; run `make test` or `make check`")
	}

	if _, err := exec.LookPath("atlas"); err != nil {
		t.Skip("atlas is not installed; run make setup")
	}

	schema := "schema \"main\" {\n}\ntable \"thing\" {\n  schema = schema.main\n" +
		"  column \"id\" {\n    null = false\n    type = text\n  }\n" +
		"  column \"note\" {\n    null    = false\n    type    = text\n" +
		"    default = \"the `value` column\"\n  }\n" +
		"  primary_key {\n    columns = [column.id]\n  }\n  strict = true\n}\n"

	tree := migrationFixture(t, schema)
	out, err := runNewMigration(t, tree, "add_thing")

	require.Error(t, err,
		"a backtick inside a string literal must be refused, not rewritten — the rewrite would "+
			"change what the literal MEANS\n%s", out)
	require.Contains(t, out, "backtick inside a string literal", "%s", out)
}
