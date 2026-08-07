package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeMigration drops a .sql file into a fixture tree's migration directory.
func writeMigration(tb testing.TB, tree, name, body string) {
	tb.Helper()

	dir := filepath.Join(tree, "db", "migrations-sqlite")
	require.NoError(tb, os.MkdirAll(dir, 0o755))
	require.NoError(tb, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

// TestRepoGates_BacktickedMigration_FailsGate proves MIG002 fires.
//
// The gate exists because Atlas emits `dkp_meta` — MySQL-style backtick quoting, which SQLite
// accepts as a compatibility extension and sqlc's SQLite parser does not. sqlc's failure mode is
// the reason this is a gate rather than a convention: it does not reject the schema file. It parses
// no table out of it, generates an empty package, and then reports `relation "dkp_meta" does not
// exist` against the QUERY file — pointing at the one file that was correct. Nobody debugging that
// message looks at identifier quoting in a generated migration.
//
// scripts/new-migration.sh rewrites them at generation time. This is the backstop for a migration
// that arrived some other way.
func TestRepoGates_BacktickedMigration_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeMigration(t, tree, "000001_init.sql",
		"-- +goose Up\nCREATE TABLE `dkp_meta` (`key` text NOT NULL, PRIMARY KEY (`key`)) STRICT;\n")

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a backtick-quoted migration must fail the gates\n%s", out)
	require.Contains(t, out, "MIG002",
		"the gates went red, but not because of the backtick rule\n%s", out)
	require.Contains(t, out, "db/migrations-sqlite/000001_init.sql:",
		"MIG002 must name the offending file, repo-root-relative\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_DoubleQuotedMigration_Passes is the control.
//
// Without it, a gate that fired on every migration — or a harness that never found the fixture at
// all — would make the test above green while enforcing nothing. It also pins the shape
// new-migration.sh actually produces, so a change to that rewrite that stopped producing valid
// SQLite would be caught here rather than by sqlc three steps later.
func TestRepoGates_DoubleQuotedMigration_Passes(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeMigration(t, tree, "000001_init.sql",
		`-- +goose Up`+"\n"+
			`CREATE TABLE "dkp_meta" ("key" text NOT NULL, PRIMARY KEY ("key")) WITHOUT ROWID, STRICT;`+"\n"+
			"\n-- +goose Down\nSELECT RAISE(ABORT, 'DKP migrations are forward-only');\n")

	out, code := runGateScript(t, script, tree)

	require.Zero(t, code, "a standards-quoted migration must pass the gates\n%s", out)
	require.NotContains(t, out, "MIG002", "MIG002 must not fire on double quotes\n%s", out)
	require.NotContains(t, out, "MIG001",
		"MIG001 must not fire on a Down block whose only statement is a RAISE\n%s", out)
}

// TestRepoGates_DDLInDownBlock_FailsGate proves MIG001 fires.
//
// MIG001 predates this PR and has never been exercised: db/migrations-sqlite/ was an empty directory
// until now, so the gate skipped vacuously on every CI run since it was written. PR 3 is what makes
// it live, so PR 3 is what owes it a negative fixture.
func TestRepoGates_DDLInDownBlock_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeMigration(t, tree, "000002_add_thing.sql",
		"-- +goose Up\nCREATE TABLE \"thing\" (\"id\" text NOT NULL PRIMARY KEY) STRICT;\n"+
			"\n-- +goose Down\nDROP TABLE \"thing\";\n")

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "DDL in a Down block must fail the gates\n%s", out)
	require.Contains(t, out, "MIG001",
		"the gates went red, but not because of the forward-only rule\n%s", out)
}

// TestMakefile_InstallAtlas_StripsRepoRootEnv mirrors the existing hostile-env tests for lint-repo
// and licence-gate.
//
// scripts/install-atlas.sh honours DKP_REPO_ROOT so its negative case can be driven against a
// fabricated tree. `make install-atlas` must strip it, or a value left in a developer's shell would
// make `make setup` read ATLAS_VERSION out of some other directory and install a version this
// repository never pinned — while printing that it succeeded.
//
// The discriminator is the error text: pointed at an empty tree the script cannot find a Makefile
// and fails on the pin lookup. Reaching the real tree, it finds ATLAS_VERSION and gets as far as
// the checksum, which is already installed.
func TestMakefile_InstallAtlas_StripsRepoRootEnv(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("make", "install-atlas")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+t.TempDir())

	raw, err := cmd.CombinedOutput()
	out := string(raw)

	require.NotContains(t, out, "no ATLAS_VERSION in Makefile",
		"make install-atlas read its version out of the hostile DKP_REPO_ROOT tree instead of this "+
			"repository — is `env -u DKP_REPO_ROOT` still on the install-atlas recipe?\n%s", out)
	require.NoError(t, err, "install-atlas must succeed against the real tree\n%s", out)
}

// TestInstallAtlas_MissingChecksum_FailsBeforeNetwork asserts the ordering that makes the pin
// meaningful.
//
// A version bump that forgets scripts/atlas.sha256 must fail on the missing checksum row, not
// download an unverified binary and then discover there is nothing to compare it against. Driven
// through DKP_REPO_ROOT against a fabricated Makefile so no network is touched at all — which is
// also what proves the check happens first.
func TestInstallAtlas_MissingChecksum_FailsBeforeNetwork(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tree, "Makefile"),
		[]byte("ATLAS_VERSION          ?= v0.0.0-nonexistent\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tree, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tree, "scripts", "atlas.sha256"),
		[]byte("# no rows\n"), 0o644))

	cmd := exec.Command("bash", scriptPath(t, "install-atlas.sh"), filepath.Join(tree, "bin"))
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree)

	raw, err := cmd.CombinedOutput()
	out := string(raw)

	require.Error(t, err, "a version with no pinned checksum must fail\n%s", out)
	require.Contains(t, out, "no checksum pinned",
		"it failed, but not on the missing checksum — check that the lookup happens before the "+
			"download\n%s", out)
	require.NoFileExists(t, filepath.Join(tree, "bin", "atlas"),
		"a binary was installed despite having no pinned checksum")
}
