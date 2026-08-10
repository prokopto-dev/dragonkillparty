package repo_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// sha256Hex is the hash SHIPPED.lock records: lowercase hex over the file's exact bytes.
func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// writeShippedLock writes a SHIPPED.lock manifest into a fixture tree from `basename -> body`
// pairs, hashing each body the way the release seal does.
//
// The bodies are passed rather than the hashes so a test cannot accidentally assert against a hash
// it typed itself: `writeShippedLock(t, tree, "000001_init.sql", original)` followed by
// `writeMigration(t, tree, "000001_init.sql", tampered)` is the modification case, and the
// difference between the two arguments IS the defect under test.
func writeShippedLock(tb testing.TB, tree string, nameBodyPairs ...string) {
	tb.Helper()

	require.Zero(tb, len(nameBodyPairs)%2, "writeShippedLock takes name/body pairs")

	dir := filepath.Join(tree, "db", "migrations-sqlite")
	require.NoError(tb, os.MkdirAll(dir, 0o755))

	var b strings.Builder

	b.WriteString("# SHIPPED.lock fixture — see scripts/shipped-lock.sh\n")

	for i := 0; i < len(nameBodyPairs); i += 2 {
		b.WriteString(nameBodyPairs[i] + " " + sha256Hex(nameBodyPairs[i+1]) + "\n")
	}

	require.NoError(tb, os.WriteFile(filepath.Join(dir, "SHIPPED.lock"), []byte(b.String()), 0o644))
}

// runShippedLock runs scripts/shipped-lock.sh against tree with the given arguments.
//
// runGateScript cannot be reused: it passes no arguments, and the release path is exactly the
// argument (`verify --complete`) that distinguishes it from the per-PR gate. The DKP_REPO_ROOT
// contract is identical and is asserted the same way — an empty or relative value would silently
// inspect the real checkout.
func runShippedLock(t *testing.T, tree string, args ...string) (output string, exitCode int) {
	t.Helper()

	require.NotEmpty(t, tree, "DKP_REPO_ROOT must not be empty — the script falls back to the real repo")
	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("bash", append([]string{scriptPath(t, "shipped-lock.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree)

	raw, err := cmd.CombinedOutput()
	if err == nil {
		return string(raw), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(raw), exitErr.ExitCode()
	}

	t.Fatalf("run shipped-lock.sh %v: %v\n%s", args, err, raw)

	return "", 0
}

// A migration body that passes every other migration gate, so a MIG003 fixture fails for the
// reason under test and not because of a backtick or a Down-block DROP.
const cleanMigration = `-- +goose Up
CREATE TABLE "thing" ("id" text NOT NULL PRIMARY KEY) STRICT;

-- +goose Down
SELECT RAISE(ABORT, 'DKP migrations are forward-only');
`

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

// TestRepoGates_ModifiedShippedMigration_FailsGate proves MIG003 fires on an EDIT.
//
// A migration listed in db/migrations-sqlite/SHIPPED.lock has appeared in a tagged release, which
// means it has already executed against somebody's database. Editing it makes an existing install
// and a fresh install end up with different schemas — "works on a fresh install, breaks on upgrade",
// the most damaging bug class for an audience of volunteer officers with ten years of guild DKP and
// no backup discipline.
//
// atlas.sum does NOT cover this, and that gap is why the gate exists. atlas.sum protects the
// current set as it is: edit a migration, re-run `atlas migrate hash`, and it agrees again, because
// `make verify-generated` only asks whether regenerating changes anything. SHIPPED.lock records
// history rather than state, so the same edit leaves it disagreeing for good.
func TestRepoGates_ModifiedShippedMigration_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	const shipped = `-- +goose Up
CREATE TABLE "ledger_entry" ("id" text NOT NULL PRIMARY KEY, "amount_cp" integer NOT NULL) STRICT;
`

	// The lock records the bytes that shipped; the tree holds a file that no longer matches them.
	writeShippedLock(t, tree, "000001_init.sql", shipped)
	writeMigration(t, tree, "000001_init.sql", shipped+
		`ALTER TABLE "ledger_entry" ADD COLUMN "note" text;`+"\n")

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "editing a shipped migration must fail the gates\n%s", out)
	require.Contains(t, out, "MIG003",
		"the gates went red, but not because of the frozen-migration rule\n%s", out)
	require.Contains(t, out, "000001_init.sql",
		"MIG003 must name the migration whose hash no longer matches\n%s", out)
	require.Contains(t, out, "MODIFIED",
		"MIG003 must say which way the manifest and the tree disagree\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_DeletedShippedMigration_FailsGate is the other half of the finding: "no file whose
// basename appears in SHIPPED.lock is modified OR DELETED".
//
// Deletion is the quieter of the two. An edit at least leaves a diff on a file a reviewer is
// looking at; removing the file entirely leaves goose's applied-version table naming a migration
// that no longer exists, and a fresh install simply never runs it.
func TestRepoGates_DeletedShippedMigration_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// Listed, and absent from the tree — the shape a `git rm` of a shipped migration leaves behind.
	writeShippedLock(t, tree, "000001_init.sql", cleanMigration)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "deleting a shipped migration must fail the gates\n%s", out)
	require.Contains(t, out, "MIG003",
		"the gates went red, but not because of the frozen-migration rule\n%s", out)
	require.Contains(t, out, "000001_init.sql", "MIG003 must name the missing migration\n%s", out)
	require.Contains(t, out, "DELETED",
		"MIG003 must distinguish a deleted migration from an edited one\n%s", out)
}

// TestRepoGates_UnchangedShippedMigration_PassesGate is the control, and it asserts the ALLOWLIST
// half in the same run: adding a NEW migration must not fire MIG003.
//
// Without this, a gate that fired on every migration directory would satisfy both tests above while
// making it impossible to add a migration at all — and the first person to hit that would reach for
// `--no-verify` rather than for the rule id. The new file is deliberately absent from SHIPPED.lock,
// because that is the truth on a feature branch: it has not shipped yet. Completeness is a
// release-time question (`make release-shipped-lock`), never a per-PR one.
func TestRepoGates_UnchangedShippedMigration_PassesGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeShippedLock(t, tree, "000001_init.sql", cleanMigration)
	writeMigration(t, tree, "000001_init.sql", cleanMigration)
	writeMigration(t, tree, "000002_add_thing.sql", cleanMigration)

	out, code := runGateScript(t, script, tree)

	require.Zero(t, code, "an untouched shipped migration plus a new one must pass the gates\n%s", out)
	require.NotContains(t, out, "MIG003",
		"MIG003 fired on a tree where nothing shipped was touched\n%s", out)
	require.Contains(t, out, "1 shipped migration(s) unchanged",
		"MIG003 must report how many rows it checked — a manifest that parsed to nothing would "+
			"otherwise pass silently\n%s", out)
}

// TestShippedLock_MalformedManifest_FailsVerify closes the vacuous-pass hole.
//
// A gate that skips what it cannot parse reports green on a truncated, half-written or hand-mangled
// manifest — the exact file states a bad merge produces. Then the next release seals on top of a
// record that already lost rows. So a row that is not `<filename> <sha256>` is a failure, not a
// skipped line.
func TestShippedLock_MalformedManifest_FailsVerify(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		row  string
		want string
	}{
		{"no hash", "000001_init.sql\n", "<filename> <sha256>"},
		{"truncated hash", "000001_init.sql deadbeef\n", "64 lowercase hex"},
		{"uppercase hash", "000001_init.sql " + strings.ToUpper(sha256Hex(cleanMigration)) + "\n", "64 lowercase hex"},
		{"a path, not a basename", "db/migrations-sqlite/000001_init.sql " + sha256Hex(cleanMigration) + "\n", "plain migration basename"},
		{"listed twice", "000001_init.sql " + sha256Hex(cleanMigration) + "\n000001_init.sql " + sha256Hex(cleanMigration) + "\n", "listed twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := t.TempDir()
			writeMigration(t, tree, "000001_init.sql", cleanMigration)
			writeRepoFile(t, tree, "db/migrations-sqlite/SHIPPED.lock", "# fixture\n"+tc.row)

			out, code := runShippedLock(t, tree, "verify")

			require.NotZero(t, code, "a malformed manifest row must fail verification\n%s", out)
			require.Contains(t, out, tc.want, "%s", out)
		})
	}
}

// TestShippedLock_ReleaseMode_RequiresEveryMigrationBeSealed pins the one assertion that separates
// the release path from the per-PR gate, and it is the assertion that makes the manifest complete
// rather than merely consistent.
//
// At a tag, every migration in the tree ships with that tag — so a migration missing from
// SHIPPED.lock is not "not yet shipped", it is a hole in the record that nobody will notice until
// somebody edits that file two releases later and every gate says fine. `--complete` is what turns
// "the Release PR forgot to seal" into a failed release; without it the flag would be decorative
// and the manifest would drift one migration at a time.
func TestShippedLock_ReleaseMode_RequiresEveryMigrationBeSealed(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()

	writeShippedLock(t, tree, "000001_init.sql", cleanMigration)
	writeMigration(t, tree, "000001_init.sql", cleanMigration)
	writeMigration(t, tree, "000002_add_thing.sql", cleanMigration)

	// The per-PR gate must accept this: 000002 has not shipped.
	out, code := runShippedLock(t, tree, "verify")
	require.Zero(t, code, "an unsealed NEW migration must not fail the per-PR check\n%s", out)

	// The release gate must not: at a tag, 000002 ships whether or not anyone recorded it.
	out, code = runShippedLock(t, tree, "verify", "--complete")
	require.NotZero(t, code, "a release must not proceed with an unsealed migration\n%s", out)
	require.Contains(t, out, "000002_add_thing.sql", "%s", out)
	require.Contains(t, out, "make shipped-lock-seal",
		"the failure must name the command that fixes it\n%s", out)
}

// TestShippedLock_Seal_IsAppendOnlyAndRefusesTamperedTrees asserts the two properties that make the
// seal safe to run: it never rewrites an existing row, and it refuses to run at all on a tree where
// a shipped migration has already been altered.
//
// The second is the one that matters. Sealing recomputes hashes; a seal that ran on a tampered tree
// would quietly overwrite the recorded hash with the tampered one and launder the edit into the
// permanent record — turning the manifest from evidence into an echo of whatever is on disk.
func TestShippedLock_Seal_IsAppendOnlyAndRefusesTamperedTrees(t *testing.T) {
	t.Parallel()

	t.Run("appends without touching existing rows", func(t *testing.T) {
		t.Parallel()

		tree := t.TempDir()
		writeShippedLock(t, tree, "000001_init.sql", cleanMigration)
		writeMigration(t, tree, "000001_init.sql", cleanMigration)
		writeMigration(t, tree, "000002_add_thing.sql", cleanMigration)

		lockPath := filepath.Join(tree, "db", "migrations-sqlite", "SHIPPED.lock")
		before, err := os.ReadFile(lockPath)
		require.NoError(t, err)

		out, code := runShippedLock(t, tree, "seal")
		require.Zero(t, code, "sealing a clean tree must succeed\n%s", out)

		after, err := os.ReadFile(lockPath)
		require.NoError(t, err)

		require.True(t, strings.HasPrefix(string(after), string(before)),
			"seal rewrote or reordered existing rows; the manifest is append-only\nbefore:\n%s\nafter:\n%s",
			before, after)
		require.Contains(t, string(after), "000002_add_thing.sql "+sha256Hex(cleanMigration),
			"seal must append the newly shipped migration\n%s", after)

		out, code = runShippedLock(t, tree, "verify", "--complete")
		require.Zero(t, code, "the sealed manifest must satisfy the release gate\n%s", out)
	})

	t.Run("refuses a tree where a shipped migration was altered", func(t *testing.T) {
		t.Parallel()

		tree := t.TempDir()
		writeShippedLock(t, tree, "000001_init.sql", cleanMigration)
		writeMigration(t, tree, "000001_init.sql", cleanMigration+"-- tampered\n")
		writeMigration(t, tree, "000002_add_thing.sql", cleanMigration)

		out, code := runShippedLock(t, tree, "seal")

		require.NotZero(t, code, "seal must refuse a tree with an altered shipped migration\n%s", out)
		require.Contains(t, out, "refusing to seal", "%s", out)

		body, err := os.ReadFile(filepath.Join(tree, "db", "migrations-sqlite", "SHIPPED.lock"))
		require.NoError(t, err)
		require.NotContains(t, string(body), "000002_add_thing.sql",
			"seal appended a row despite refusing — it must verify before it writes\n%s", body)
	})
}

// TestShippedLock_ThisRepo_ExistsAndVerifies asserts the manifest is real in THIS checkout, not only
// in fixtures.
//
// .claude/rules/migrations.md, .claude/hooks/guard-protected-paths.sh, the schema-migration-reviewer
// subagent and the add-migration skill all instruct a reader to check a file at this exact path. A
// procedure written against a file that does not exist finds nothing to check against and reports
// that everything is fine, which is worse than having no procedure.
func TestShippedLock_ThisRepo_ExistsAndVerifies(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	_, err := os.Stat(filepath.Join(root, "db", "migrations-sqlite", "SHIPPED.lock"))
	require.NoError(t, err,
		"db/migrations-sqlite/SHIPPED.lock must exist: .claude/rules/migrations.md, the "+
			"guard-protected-paths hook and the schema-migration-reviewer all check migrations against it")

	out, code := runShippedLock(t, root, "verify")
	require.Zero(t, code, "this repository's SHIPPED.lock must verify\n%s", out)
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
