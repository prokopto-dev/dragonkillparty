package lockmanifest

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// invoke runs one whole invocation against a fixture tree and returns its exit code with both
// streams, the way a caller sees it. MIG003 captures them merged, so a test asserting on "the
// output" gets them merged here too.
func invoke(t *testing.T, tr tree, args ...string) (out string, code int) {
	t.Helper()

	var buf bytes.Buffer

	code = run(args, tr, output{stdout: &buf, stderr: &buf})

	return buf.String(), code
}

// TestRun_UnchangedManifest_Passes is the control for every failure test below, and it pins the
// count line.
//
// The count is load-bearing rather than decorative: a manifest that parsed to zero rows would
// otherwise print a cheerful green line having checked nothing at all, which is the vacuous pass
// this command exists to prevent.
func TestRun_UnchangedManifest_Passes(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	// A migration added on a feature branch has not shipped and must not be listed yet. The per-PR
	// gate has to accept exactly this, or it fires on the one change it is supposed to permit.
	writeMigration(t, tr, "000002_guild.sql", cleanMigration)

	out, code := invoke(t, tr, "verify")

	require.Zero(t, code, "%s", out)
	require.Contains(t, out, "1 shipped migration(s) unchanged", "%s", out)
}

// TestRun_ModifiedOrDeletedMigration_Fails is MIG003's first half end to end: the exit code, the
// named file, and the explanation a contributor is left holding.
func TestRun_ModifiedOrDeletedMigration_Fails(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, tr tree)
		want   string
	}{
		{
			name: "edited",
			mutate: func(t *testing.T, tr tree) {
				t.Helper()
				writeMigration(t, tr, "000001_init.sql", tamperedMigration)
			},
			want: "MODIFIED",
		},
		{
			name: "deleted",
			mutate: func(t *testing.T, tr tree) {
				t.Helper()
				require.NoError(t, os.Remove(tr.migrationPath("000001_init.sql")))
			},
			want: "DELETED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := newTree(t)
			writeMigration(t, tr, "000001_init.sql", cleanMigration)
			writeLock(t, tr, "000001_init.sql", cleanMigration)
			commitBase(t, tr)

			tc.mutate(t, tr)

			out, code := invoke(t, tr, "verify")

			require.Equal(t, exitFail, code, "%s", out)
			require.Contains(t, out, "000001_init.sql", "%s", out)
			require.Contains(t, out, tc.want, "%s", out)
			require.Contains(t, out, "write a NEW migration: make migration NAME=<snake_case>",
				"the failure must end with the thing to do instead\n%s", out)
		})
	}
}

// TestRun_MalformedManifest_FailsRatherThanSkips is the same property manifest_test.go asserts
// against the parser, made at the boundary that matters: the exit code.
//
// A gate that skips what it cannot parse reports green on a truncated, half-written or hand-mangled
// manifest — the exact file states a bad merge produces — and then the next release seals on top of
// a record that already lost rows.
func TestRun_MalformedManifest_FailsRatherThanSkips(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLockRaw(t, tr, "# fixture\n000001_init.sql\n")

	out, code := invoke(t, tr, "verify")

	require.Equal(t, exitFail, code, "%s", out)
	require.Contains(t, out, "a row is exactly '<filename> <sha256>'", "%s", out)
	require.NotContains(t, out, "shipped migration(s) unchanged",
		"a manifest that failed to parse must not also report how many rows it checked\n%s", out)
}

// TestRun_NoManifest_PassesPerPRButFailsARelease pins the difference between the two callers.
//
// Nothing has shipped is a true and ordinary state for this repository today, so the per-PR gate
// says so and passes. A release cannot record which migrations shipped without the file, so the
// release path fails and names the way to create it.
func TestRun_NoManifest_PassesPerPRButFailsARelease(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)

	out, code := invoke(t, tr, "verify")
	require.Zero(t, code, "%s", out)
	require.Contains(t, out, "nothing has shipped yet", "%s", out)

	out, code = invoke(t, tr, "verify", "--complete")
	require.Equal(t, exitFail, code, "%s", out)
	require.Contains(t, out, "A release cannot record which migrations", "%s", out)
}

// TestRun_ReleaseMode_RequiresEveryMigrationBeSealed is the assertion that makes the manifest
// complete rather than merely consistent.
//
// At a tag, every migration in the tree ships with that tag — so a migration missing from the
// manifest is not "not yet shipped", it is a hole in the record that nobody will notice until
// somebody edits that file two releases later and every gate says fine. Without --complete the flag
// would be decorative and the manifest would drift one migration at a time.
func TestRun_ReleaseMode_RequiresEveryMigrationBeSealed(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	writeMigration(t, tr, "000002_guild.sql", cleanMigration)

	out, code := invoke(t, tr, "verify")
	require.Zero(t, code, "an unsealed NEW migration must not fail the per-PR check\n%s", out)

	out, code = invoke(t, tr, "verify", "--complete")
	require.Equal(t, exitFail, code, "a release must not proceed with an unsealed migration\n%s", out)
	require.Contains(t, out, "000002_guild.sql", "%s", out)
	require.Contains(t, out, "make shipped-lock-seal", "%s", out)
}

// TestRun_Seal_AppendsWithoutTouchingExistingRows asserts the property the word "append-only" names,
// at the only place it can actually be checked: the bytes on disk before and after.
func TestRun_Seal_AppendsWithoutTouchingExistingRows(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	writeMigration(t, tr, "000002_guild.sql", cleanMigration)

	before := readLock(t, tr)

	out, code := invoke(t, tr, "seal")
	require.Zero(t, code, "sealing a clean tree must succeed\n%s", out)

	after := readLock(t, tr)
	require.True(t, strings.HasPrefix(after, before),
		"seal rewrote or reordered existing rows; the manifest is append-only\nbefore:\n%s\nafter:\n%s",
		before, after)
	require.Contains(t, after, "000002_guild.sql "+sha256Hex(cleanMigration))

	out, code = invoke(t, tr, "verify", "--complete")
	require.Zero(t, code, "the sealed manifest must satisfy the release gate\n%s", out)
}

// TestRun_Seal_RefusesATamperedTree is the property that keeps the manifest evidence rather than an
// echo of whatever is on disk.
//
// Sealing recomputes hashes. A seal that ran on a tampered tree would quietly overwrite the recorded
// hash with the tampered one and launder the edit into the permanent record — and because the record
// is what every later check compares against, nothing downstream would ever disagree with it again.
func TestRun_Seal_RefusesATamperedTree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, tr tree)
	}{
		{
			name: "a shipped migration was edited",
			mutate: func(t *testing.T, tr tree) {
				t.Helper()
				writeMigration(t, tr, "000001_init.sql", tamperedMigration)
			},
		},
		{
			name: "the manifest itself was rewritten",
			mutate: func(t *testing.T, tr tree) {
				t.Helper()
				writeMigration(t, tr, "000001_init.sql", tamperedMigration)
				writeLock(t, tr, "000001_init.sql", tamperedMigration)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := newTree(t)
			writeMigration(t, tr, "000001_init.sql", cleanMigration)
			writeLock(t, tr, "000001_init.sql", cleanMigration)
			commitBase(t, tr)

			tc.mutate(t, tr)
			writeMigration(t, tr, "000002_guild.sql", cleanMigration)

			out, code := invoke(t, tr, "seal")

			require.Equal(t, exitFail, code, "%s", out)
			require.Contains(t, out, "refusing to seal", "%s", out)
			require.NotContains(t, readLock(t, tr), "000002_guild.sql",
				"seal appended a row despite refusing — it must verify before it writes")
		})
	}
}

// TestRun_Seal_OnACleanTreeIsIdempotent covers the second run of the same command. Preparing a
// release is a loop of "seal, look, commit", and a second seal that appended a duplicate row would
// produce a manifest the gate then rejects for being listed twice.
func TestRun_Seal_OnACleanTreeIsIdempotent(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)
	commitBase(t, tr)

	before := readLock(t, tr)

	out, code := invoke(t, tr, "seal")
	require.Zero(t, code, "%s", out)
	require.Contains(t, out, "nothing to seal", "%s", out)
	require.Equal(t, before, readLock(t, tr), "a seal with nothing to do must not write")
}

// TestRun_Seal_WithNoManifest_WritesTheHeaderFirst covers the bootstrap the release path relies on:
// the first seal of a repository that has never shipped.
func TestRun_Seal_WithNoManifest_WritesTheHeaderFirst(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)

	out, code := invoke(t, tr, "seal")
	require.Zero(t, code, "%s", out)

	body := readLock(t, tr)
	require.True(t, strings.HasPrefix(body, manifestHeader),
		"a manifest seal created must carry the header that explains it\n%s", body)
	require.Contains(t, body, "000001_init.sql "+sha256Hex(cleanMigration))
}

// TestRun_Seal_MissingMigrationDirectory_Fails keeps the seal from creating a manifest in a tree that
// has no migrations to record. Writing one there would produce an empty record that every later
// check happily agrees with.
func TestRun_Seal_MissingMigrationDirectory_Fails(t *testing.T) {
	t.Parallel()

	tr := tree{root: t.TempDir(), baseRef: defaultBaseRef}

	out, code := invoke(t, tr, "seal")

	require.Equal(t, exitFail, code, "%s", out)
	require.Contains(t, out, migrationDir+" does not exist", "%s", out)
	require.NoFileExists(t, tr.lockPath())
}

// TestRun_Init_WritesAnEmptyManifest covers the bootstrap, and TestRun_Init_RefusesAnExistingManifest
// covers the only thing init must never do.
func TestRun_Init_WritesAnEmptyManifest(t *testing.T) {
	t.Parallel()

	tr := newTree(t)

	out, code := invoke(t, tr, "init")

	require.Zero(t, code, "%s", out)
	require.Equal(t, manifestHeader, readLock(t, tr), "init writes the header and no rows")

	out, code = invoke(t, tr, "verify")
	require.Zero(t, code, "%s", out)
	require.Contains(t, out, "lists no migrations yet", "%s", out)
}

// TestRun_Init_RefusesAnExistingManifest is the one property that makes init safe to type by
// accident. Overwriting a manifest destroys the record of what has already run on a user's database,
// which is unrecoverable from inside this repository — the rows are the only copy of that fact.
func TestRun_Init_RefusesAnExistingManifest(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)
	writeLock(t, tr, "000001_init.sql", cleanMigration)

	before := readLock(t, tr)

	out, code := invoke(t, tr, "init")

	require.Equal(t, exitFail, code, "%s", out)
	require.Contains(t, out, "init never overwrites a manifest", "%s", out)
	require.Equal(t, before, readLock(t, tr), "init overwrote a manifest that already had rows")
}

// TestRun_Init_MissingMigrationDirectory_Fails mirrors the seal case: a manifest outside a migration
// directory records nothing.
func TestRun_Init_MissingMigrationDirectory_Fails(t *testing.T) {
	t.Parallel()

	tr := tree{root: t.TempDir(), baseRef: defaultBaseRef}

	out, code := invoke(t, tr, "init")

	require.Equal(t, exitFail, code, "%s", out)
	require.Contains(t, out, migrationDir+" does not exist", "%s", out)
}

// TestRun_UnknownArguments_AreAUsageError keeps a mistyped invocation distinguishable from a
// tampered manifest. `verify --complte` exiting 1 would read, in a release log, as "the manifest is
// incomplete" — the failure it was asked to check for.
func TestRun_UnknownArguments_AreAUsageError(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"bogus"},
		{"verify", "--complte"},
		{"verify", "--complete", "extra"},
		{"seal", "--complete"},
		{"init", "--force"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			tr := newTree(t)

			out, code := invoke(t, tr, args...)

			require.Equal(t, exitUsage, code, "%s", out)
			require.Contains(t, out, "usage: shippedlock", "%s", out)
		})
	}
}

// TestRun_NoArguments_Verifies pins the default mode. MIG003 passes `verify` explicitly, but a
// developer typing the command with no arguments must get the read-only check and never a write.
func TestRun_NoArguments_Verifies(t *testing.T) {
	t.Parallel()

	tr := newTree(t)
	writeMigration(t, tr, "000001_init.sql", cleanMigration)

	out, code := invoke(t, tr)

	require.Zero(t, code, "%s", out)
	require.Contains(t, out, "nothing has shipped yet", "%s", out)
	require.NoFileExists(t, tr.lockPath(), "the default mode must not write a manifest")
}
