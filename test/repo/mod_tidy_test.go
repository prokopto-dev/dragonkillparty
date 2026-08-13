// The tidiness gate for go.mod and go.sum (issue #149).
//
// Nothing asserted this property, so tidiness was whoever last happened to run `go mod tidy` — and
// the cleanup always arrived attached to an unrelated change. #126 added one direct requirement,
// tidied alongside it, and pruned ~100 stale `/go.mod` hash lines from go.sum (ClickHouse, docker,
// gin, antlr — goose's optional-driver graph, none of which this project builds) inside a pull
// request about HTML parsing, where they were pure noise a reviewer had to read past and decide was
// safe. The other direction is worse: a PR that ADDS a dependency without tidying leaves the module
// graph and the lock disagreeing, and nothing said so.
//
// The fixture is a real module in t.TempDir(), for gates_test.go's first rule: an untidy go.mod
// committed to this repository would fail the very gate under test.
package repo_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// runMakeIn runs one make target against a directory, returning combined output and exit code.
//
// `make -C` rather than cmd.Dir, so the Makefile under test is unambiguous, and with the caller's
// environment so GOFLAGS and the module cache are the ones the real target would see.
func runMakeIn(t *testing.T, dir, target string) (output string, exitCode int) {
	t.Helper()

	cmd := exec.Command("make", "-C", dir, target)
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run make %s in %s: %v\n%s", target, dir, err, out)

	return "", 0
}

// tidyFixture builds a module in t.TempDir() carrying ONLY the Makefile's mod-tidy-check recipe,
// extracted from the real Makefile so the fixture cannot drift from the target it tests.
//
// Extracted rather than reimplemented: a hand-written copy of the recipe would keep passing after
// somebody rewrote the real one, which is the failure mode this whole package exists to prevent.
func tidyFixture(t *testing.T, gomod, gosum string) string {
	t.Helper()

	tree := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tree, "go.mod"), []byte(gomod), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tree, "go.sum"), []byte(gosum), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tree, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))

	recipe := makeRecipe(t, "mod-tidy-check")
	require.NotEmpty(t, recipe, "the Makefile must still have a mod-tidy-check target (issue #149)")

	// SHELL and GO mirror the real Makefile's own definitions: the recipe uses bash-isms and $(GO).
	makefile := "SHELL := /bin/bash\nGO ?= go\n\n.PHONY: mod-tidy-check\nmod-tidy-check:\n" + recipe + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(tree, "Makefile"), []byte(makefile), 0o644))

	return tree
}

// A module that needs no network to tidy: no requirements at all, so `go mod tidy` resolves nothing.
// That matters because this test must pass on a laptop with no connectivity, which is the bar
// `make check` is held to.
const tidyGoMod = "module example.com/tidyfixture\n\ngo 1.26\n"

// The same module with a requirement nothing imports. `go mod tidy` removes it, so the file it
// leaves differs from the committed one — which is precisely the state the gate must reject.
const untidyGoMod = "module example.com/tidyfixture\n\ngo 1.26\n\nrequire example.com/never/imported v1.2.3\n"

// TestModTidy_UntidyGoMod_FailsTheTarget is issue #149's acceptance criterion.
func TestModTidy_UntidyGoMod_FailsTheTarget(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to make and go mod tidy; run `make test` or `make check`")
	}

	tree := tidyFixture(t, untidyGoMod, "")

	out, code := runMakeIn(t, tree, "mod-tidy-check")

	require.NotZerof(t, code, "an untidy go.mod must fail the target\n%s", out)
	require.Containsf(t, out, "not tidy",
		"the failure must say the tree is untidy and name the fix, not merely exit non-zero\n%s", out)
	require.Containsf(t, out, "never/imported",
		"the diff must show WHAT is untidy — a gate that says 'run go mod tidy' without showing "+
			"the change leaves the reader running a command whose effect they cannot predict\n%s", out)
}

// TestModTidy_TidyGoMod_PassesTheTarget is the control, and it also holds the property that makes
// this target safe to put in `make check`: the target must leave the tree exactly as it found it.
func TestModTidy_TidyGoMod_PassesTheTarget(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to make and go mod tidy; run `make test` or `make check`")
	}

	tree := tidyFixture(t, tidyGoMod, "")

	out, code := runMakeIn(t, tree, "mod-tidy-check")

	require.Zerof(t, code, "a tidy module must pass\n%s", out)

	after, err := os.ReadFile(filepath.Join(tree, "go.mod"))
	require.NoError(t, err)
	require.Equalf(t, tidyGoMod, string(after),
		"mod-tidy-check must restore go.mod byte for byte. It runs inside `make check`, which people "+
			"run mid-change: a target that rewrites the module file as a side effect of checking it "+
			"would be indistinguishable from a bug in whatever they were doing\n%s", out)
}

// TestModTidy_RestoresTheTree_EvenOnFailure is the same property on the path that actually risks it.
//
// The recipe restores through a `trap ... EXIT`, so the failure path is the interesting one: an
// implementation that restored only after a successful comparison would leave a developer's go.mod
// rewritten by the gate that was supposed to be reporting on it.
func TestModTidy_RestoresTheTree_EvenOnFailure(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to make and go mod tidy; run `make test` or `make check`")
	}

	tree := tidyFixture(t, untidyGoMod, "")

	_, code := runMakeIn(t, tree, "mod-tidy-check")
	require.NotZero(t, code, "the fixture must fail, or this test proves nothing")

	after, err := os.ReadFile(filepath.Join(tree, "go.mod"))
	require.NoError(t, err)
	require.Equal(t, untidyGoMod, string(after),
		"mod-tidy-check must restore go.mod on the FAILURE path too — otherwise the gate silently "+
			"fixes the thing it is reporting, and the next run passes over a change nobody made "+
			"deliberately")
}

// TestModTidy_IsInCheckAndInCI keeps the gate wired where issue #149 asked for it. A target nothing
// calls is a target that stops being true without anybody noticing.
func TestModTidy_IsInCheckAndInCI(t *testing.T) {
	t.Parallel()

	require.Containsf(t, makePrerequisites(t, "check"), "mod-tidy-check",
		"`make check` must run mod-tidy-check: issue #149's whole point is that the tidy-up rides "+
			"along with an unrelated PR when nothing asserts it")

	workflow := readCIWorkflow(t)
	require.Containsf(t, jobBlock(t, workflow, "lint-go:"), "make mod-tidy-check",
		"the `lint / go` job must run mod-tidy-check. It is the job whose `code` path filter already "+
			"selects on go.mod, go.sum and the Makefile, so the inputs that can make it fail are "+
			"exactly the inputs that run it")
}
