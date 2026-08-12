package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Two migration bodies. The tampered one differs from the clean one in bytes and therefore in hash,
// and in nothing else — an edit to a shipped migration is not a syntactically special thing, which
// is exactly why only a recorded hash catches it.
const (
	cleanMigration = "-- +goose Up\nCREATE TABLE \"thing\" (\"id\" text NOT NULL PRIMARY KEY) STRICT;\n" +
		"\n-- +goose Down\nSELECT RAISE(ABORT, 'DKP migrations are forward-only');\n"

	tamperedMigration = "-- +goose Up\nCREATE TABLE \"thing\" (\"id\" text NOT NULL PRIMARY KEY, \"smuggled\" text) STRICT;\n" +
		"\n-- +goose Down\nSELECT RAISE(ABORT, 'DKP migrations are forward-only');\n"
)

// newTree returns a fixture checkout holding an empty db/migrations-sqlite and no manifest.
func newTree(t *testing.T) tree {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(migrationDir)), 0o755))

	return tree{root: root, baseRef: defaultBaseRef}
}

// sha256Hex is the hash SHIPPED.lock records: lowercase hex over the file's exact bytes.
func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// writeMigration drops a .sql file into the fixture's migration directory.
func writeMigration(t *testing.T, tr tree, name, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(tr.migrationPath(name), []byte(body), 0o644))
}

// writeLock writes a manifest from `basename -> body` pairs, hashing each body the way a seal does.
//
// The bodies are passed rather than the hashes so a test cannot accidentally assert against a hash
// it typed itself: writeLock with one body followed by writeMigration with another IS the
// modification under test, and the difference between the two arguments is the whole fixture.
func writeLock(t *testing.T, tr tree, nameBodyPairs ...string) {
	t.Helper()

	require.Zero(t, len(nameBodyPairs)%2, "writeLock takes name/body pairs")

	var b strings.Builder

	b.WriteString("# SHIPPED.lock fixture\n")

	for i := 0; i < len(nameBodyPairs); i += 2 {
		b.WriteString(nameBodyPairs[i] + " " + sha256Hex(nameBodyPairs[i+1]) + "\n")
	}

	writeLockRaw(t, tr, b.String())
}

// writeLockRaw writes a manifest verbatim, for the crafted-manifest tests where the bytes are the
// point.
func writeLockRaw(t *testing.T, tr tree, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(tr.lockPath(), []byte(body), 0o644))
}

// readLock returns the manifest's current bytes.
func readLock(t *testing.T, tr tree) string {
	t.Helper()

	body, err := os.ReadFile(tr.lockPath())
	require.NoError(t, err)

	return string(body)
}

// commitBase turns the fixture into a git repository whose origin/main is its current contents, so
// the append-only half has a real merge base to compare against.
//
// A REAL repository rather than an injected "pretend this was the base" knob, deliberately. The
// check reads history through git, and a hook that handed it a base instead would be both a new way
// to weaken the gate and a second code path that CI never executes. Everything after this call is
// "the working tree of a PR".
func commitBase(t *testing.T, tr tree) {
	t.Helper()

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "fixture@example.invalid"},
		{"config", "user.name", "fixture"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-q", "--no-verify", "-m", "base"},
		// The default base ref. No remote exists, so the remote-tracking ref is written directly.
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tr.root

		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v in the fixture tree: %s", args, out)
	}
}
