package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tracked git hooks live here and are installed by `make setup` via core.hooksPath. This test
// is the gate's own test: it proves the hooks exist, are executable, are wired by `make setup`, and
// — the part nobody can check by hand — actually format a staged file and block an unformatted push.
//
// "The gate is tested, not trusted." A formatter hook that silently stopped formatting would be
// invisible until CI rejected a push nobody expected it to; this asserts it does its job.

// hookPath returns the absolute path of one of the tracked git hooks and asserts it is executable.
func hookPath(t *testing.T, name string) string {
	t.Helper()

	p := filepath.Join(repoRoot(t), ".githooks", name)
	info, err := os.Stat(p)
	require.NoError(t, err, "git hook %s must exist under .githooks", name)
	require.Falsef(t, info.IsDir(), "%s must be a file, not a directory", name)
	require.NotZerof(t, info.Mode().Perm()&0o111, "%s must be executable (git will not run a non-+x hook)", name)

	return p
}

// findGofumpt resolves gofumpt the same way the hooks do: command -v first, then $GOTOOLS_BIN
// (GOBIN, else GOPATH/bin). Returns "" when it is not installed, so the functional subtests can skip
// cleanly rather than fail on a laptop without the toolchain. In CI's `test / integration` job
// setup-toolchain installs gofumpt, so there the skip never fires and the check runs for real.
func findGofumpt(t *testing.T) string {
	t.Helper()

	if p, err := exec.LookPath("gofumpt"); err == nil {
		return p
	}

	bin := os.Getenv("GOBIN")
	if bin == "" {
		out, err := exec.Command("go", "env", "GOPATH").Output()
		if err != nil {
			return ""
		}
		bin = filepath.Join(strings.TrimSpace(string(out)), "bin")
	}

	cand := filepath.Join(bin, "gofumpt")
	if info, err := os.Stat(cand); err == nil && info.Mode().Perm()&0o111 != 0 {
		return cand
	}

	return ""
}

// hookEnv returns an environment for running a hook in a fixture tree: the current environment with
// gofumpt's directory prepended to PATH, so `command -v gofumpt` inside the hook resolves even when
// the toolchain is only in GOPATH/bin and not on the test runner's own PATH.
func hookEnv(gofumpt string) []string {
	env := os.Environ()
	dir := filepath.Dir(gofumpt)
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
			return env
		}
	}

	return append(env, "PATH="+dir)
}

// gitInit makes tree a real git repository with an identity and an empty go.mod, so the hooks'
// `git rev-parse --show-toplevel` and `awk '$1=="module"'` both work. No network, no remote.
func gitInit(t *testing.T, tree string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tree
		// -c config on the command line rather than a global write, so the test cannot mutate the
		// developer's ~/.gitconfig.
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %s\n%s", strings.Join(args, " "), out)
	}

	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Repo Test")
	require.NoError(t, os.WriteFile(filepath.Join(tree, "go.mod"),
		[]byte("module example.test/fixture\n\ngo 1.24\n"), 0o644))
}

// runHook runs a hook binary inside tree with the gofumpt-augmented PATH, returning combined output
// and exit code.
func runHook(t *testing.T, hook, tree, gofumpt string) (output string, exitCode int) {
	t.Helper()

	cmd := exec.Command("bash", hook)
	cmd.Dir = tree
	cmd.Env = hookEnv(gofumpt)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run %s: %v\n%s", hook, err, out)

	return "", 0
}

// asExitError is errors.As specialised to *exec.ExitError, kept tiny to avoid importing errors just
// for one call while staying explicit about what is being matched.
func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}

	return false
}

// A deliberately mis-gofumpt'd but syntactically valid Go file. gofumpt collapses the extra blank
// lines and the padded declaration; goimports would also drop the unused-import-free file's noise.
// Kept parseable so gofumpt rewrites it rather than refusing (gofumpt is a no-op on a file it cannot
// parse, which would make the assertion pass for the wrong reason).
const misformattedGo = `package fixture



func  Add( a int ,b int )  int  {
	return a+b
}
`

// TestGitHooks_ExistAndExecutable asserts (a) of the deliverable: both hooks are present and +x.
func TestGitHooks_ExistAndExecutable(t *testing.T) {
	t.Parallel()

	hookPath(t, "pre-commit")
	hookPath(t, "pre-push")
}

// TestGitHooks_SetupWiresHooksPath asserts (b): `make setup` configures core.hooksPath to point at
// the tracked .githooks directory. Grepping the Makefile's setup target is the right check here —
// running `make setup` would install the whole toolchain over the network, which this hermetic suite
// must not do. The functional subtests below prove the hooks themselves work; this proves they get
// installed.
func TestGitHooks_SetupWiresHooksPath(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	require.NoError(t, err, "read Makefile")

	require.Contains(t, string(body), "git config core.hooksPath .githooks",
		"make setup must wire git at the tracked hooks directory")
	require.Contains(t, string(body), "chmod +x .githooks/*",
		"make setup must ensure the hooks are executable after a checkout")
}

// TestGitHooks_PreCommit_FormatsAndRestagesStagedGo asserts the functional half of (c): a staged,
// mis-gofumpt'd Go file is reformatted AND re-staged by .githooks/pre-commit, and an UNSTAGED file
// is left untouched.
func TestGitHooks_PreCommit_FormatsAndRestagesStagedGo(t *testing.T) {
	t.Parallel()

	gofumpt := findGofumpt(t)
	if gofumpt == "" {
		t.Skip("gofumpt not installed — skipping the functional pre-commit test (CI's test/integration job installs it, so it runs there)")
	}

	hook := hookPath(t, "pre-commit")
	tree := t.TempDir()
	gitInit(t, tree)

	// One file staged (must be formatted + re-staged), one identical file left unstaged (must be
	// left exactly as written — the hook must never touch unstaged work).
	staged := filepath.Join(tree, "staged.go")
	unstaged := filepath.Join(tree, "unstaged.go")
	require.NoError(t, os.WriteFile(staged, []byte(misformattedGo), 0o644))
	require.NoError(t, os.WriteFile(unstaged, []byte(misformattedGo), 0o644))

	add := exec.Command("git", "add", "staged.go")
	add.Dir = tree
	addOut, err := add.CombinedOutput()
	require.NoErrorf(t, err, "git add staged.go\n%s", addOut)

	out, code := runHook(t, hook, tree, gofumpt)
	require.Zerof(t, code, "pre-commit must not block a commit\n%s", out)

	// The working-tree file was reformatted.
	got, err := os.ReadFile(staged)
	require.NoError(t, err)
	require.NotEqual(t, misformattedGo, string(got), "pre-commit must reformat the staged file\n%s", out)

	want, err := exec.Command(gofumpt, staged).Output()
	require.NoError(t, err, "run gofumpt to compute the expected formatting")
	require.Equal(t, string(want), string(got), "the file must match gofumpt's own output")

	// And the reformatted content was re-staged: the staged blob equals the working tree, so
	// `git diff --cached` shows the formatted version and there is nothing left unstaged for it.
	diff := exec.Command("git", "diff", "--name-only")
	diff.Dir = tree
	dOut, err := diff.Output()
	require.NoError(t, err)
	require.NotContains(t, string(dOut), "staged.go",
		"pre-commit must re-stage the formatted file, leaving no unstaged remainder\n%s", dOut)

	// The unstaged file is byte-for-byte what we wrote — the hook never reached it.
	untouched, err := os.ReadFile(unstaged)
	require.NoError(t, err)
	require.Equal(t, misformattedGo, string(untouched), "pre-commit must not touch unstaged files")
}

// TestGitHooks_PrePush_BlocksUnformattedTrackedGo asserts the second functional half of (c):
// pre-push exits non-zero when a TRACKED Go file is unformatted (the merge-commit drift case that
// pre-commit cannot catch), and exits zero once the tree is clean.
func TestGitHooks_PrePush_BlocksUnformattedTrackedGo(t *testing.T) {
	t.Parallel()

	gofumpt := findGofumpt(t)
	if gofumpt == "" {
		t.Skip("gofumpt not installed — skipping the functional pre-push test (CI's test/integration job installs it, so it runs there)")
	}

	hook := hookPath(t, "pre-push")
	tree := t.TempDir()
	gitInit(t, tree)

	dirty := filepath.Join(tree, "dirty.go")
	require.NoError(t, os.WriteFile(dirty, []byte(misformattedGo), 0o644))

	commit := func() {
		add := exec.Command("git", "add", "-A")
		add.Dir = tree
		out, err := add.CombinedOutput()
		require.NoErrorf(t, err, "git add -A\n%s", out)

		// --no-verify: we are staging a KNOWN-bad file on purpose to reproduce the merge-drift case
		// pre-push exists for. Letting pre-commit format it here would defeat the point.
		c := exec.Command("git", "commit", "-q", "--no-verify", "-m", "fixture")
		c.Dir = tree
		out, err = c.CombinedOutput()
		require.NoErrorf(t, err, "git commit\n%s", out)
	}
	commit()

	// A tracked, committed, unformatted file must block the push.
	out, code := runHook(t, hook, tree, gofumpt)
	require.NotZerof(t, code, "pre-push must block a push when a tracked Go file is unformatted\n%s", out)
	require.Contains(t, out, "dirty.go", "pre-push must name the offending file\n%s", out)
	require.Contains(t, out, "make fmt", "pre-push must tell the user how to fix it\n%s", out)

	// Format it, commit the fix, and the push must now be allowed.
	formatted, err := exec.Command(gofumpt, dirty).Output()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dirty, formatted, 0o644))
	commit()

	out, code = runHook(t, hook, tree, gofumpt)
	require.Zerof(t, code, "pre-push must allow a push once the tree is gofumpt-clean\n%s", out)
}
