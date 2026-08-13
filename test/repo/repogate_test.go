// Negative fixtures for the rules the Go gate engine ADDED, and for the line that keeps the engine
// on the tooling side of the repository.
//
// Everything the engine carried over from `scripts/repo-gates.sh` keeps its fixture in
// gates_test.go, migration_gates_test.go, design_tokens_test.go and web_fonts_test.go, unchanged
// and still driven through `bash scripts/repo-gates.sh` — the script is now a shim over
// internal/repogate, so those fixtures point at the Go implementation without being edited. This
// file holds what is new.
package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRepoGates_RouteOutsideAPIPackage_FailsGate covers ROUTE001, law 1.
//
// AGENTS.md: internal/api is the only tree where an HTTP route may be declared. `.github/workflows/
// ci.yml` has advertised the ban in its own step comment since the gates were written, and there
// was no such rule — the shell script had no route gate at all, which
// TestArch_Routes_AreDeclaredOnlyInAPIPackage says in as many words. The law was carried entirely
// by that arch test, which needs a build; this is the cheap twin that runs in `lint / repo`.
//
// The whole API-first argument rests on there being no back door: delete the law and the SPA can be
// served from an operation absent from the published spec, which is exactly how "the UI needs it
// but a bot would not" endpoints appear.
//
// Both gated trees are tainted in one run. cmd/ is not decoration — a Cobra command that mounts its
// own handler is the same violation and the same invisible one, and a fixture in only one tree
// would keep passing if the rule lost its second.
func TestRepoGates_RouteOutsideAPIPackage_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// The violation, written the way it would really arrive: under an ALIASED import, which is what
	// somebody reaches for the first time a gate complains. A rule that assumed the package was
	// spelled `huma` would walk straight past it.
	writeGo(t, tree, "internal/cms/routes.go", `package cms

import h "github.com/danielgtaylor/huma/v2"

func register(api h.API) {
	h.Register(api, h.Operation{
		OperationID: "getArticleBySlug",
		Method:      http.MethodGet,
		Path:        "/api/v1/cms/articles/{slug}",
	}, nil)
}
`)

	writeGo(t, tree, "cmd/dkp/serve.go", `package main

import "github.com/danielgtaylor/huma/v2"

func mount(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "getDebugState"}, nil)
}
`)

	// Allowlisted: internal/api is where a route is SUPPOSED to be declared. Without this half a
	// rule that fired on everything would satisfy the assertions above while making the API package
	// unbuildable, and the first person to hit that would reach for --no-verify rather than for the
	// rule id.
	writeGo(t, tree, "internal/api/roster.go", `package api

import "github.com/danielgtaylor/huma/v2"

func registerRoster(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "listRoster"}, nil)
}
`)

	// A comment naming the rule must not fire it. The text gates this engine replaced needed a
	// comment-stripping pass for that; reading the syntax makes it free, and this fixture is what
	// says the property survived the move.
	writeGo(t, tree, "internal/cms/doc.go", `package cms

// huma.Register is banned here: routes are declared only in internal/api.
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "a route declared outside internal/api must fail the gates\n%s", out)
	require.Contains(t, out, "ROUTE001", "%s", out)
	require.Contains(t, out, "internal/cms/routes.go:",
		"ROUTE001 must name the offending file, repo-root-relative — and must see an ALIASED huma "+
			"import, which is the shape a resolved-local-name check exists for\n%s", out)
	require.Contains(t, out, "cmd/dkp/serve.go:",
		"ROUTE001 covers cmd/ as well as internal/ — a route mounted from the binary's own wiring "+
			"is the same back door\n%s", out)

	require.NotContains(t, out, "internal/api/roster.go",
		"internal/api is where a route BELONGS — the allowlist is not working\n%s", out)
	require.NotContains(t, out, "internal/cms/doc.go",
		"a comment describing the rule must not trip it\n%s", out)
	require.NotContains(t, out, tree,
		"reported paths must be repo-root-relative, not absolute temp paths\n%s", out)
}

// TestRepoGates_IsNotLinkedIntoTheBinary keeps the gate engine on the tooling side of the line.
//
// internal/repogate is repo tooling with its own main package, which is a deviation from "cmd/dkp
// is the only binary" and is allowed on exactly one condition: an operator never gets it. A product
// package importing the engine — for a "what rules does this repo have" endpoint, for a doctor
// check — would put a rule catalogue, a Go parser pass and an ANSI report inside the shipped
// binary, and nothing else in the repo would notice.
//
// AGENTS.md's repo map states this; this is the mechanism behind the statement, and it is the same
// one TestLicence_IsNotLinkedIntoTheBinary applies to internal/licence.
func TestRepoGates_IsNotLinkedIntoTheBinary(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("resolves the binary's package graph; run `make test` or `make check`")
	}

	cmd := exec.Command("go", "list", "-deps", "./cmd/dkp")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps ./cmd/dkp")

	packages := strings.Split(string(out), "\n")
	require.NotEmpty(t, packages, "the binary's package graph resolved to nothing")

	for _, p := range packages {
		require.NotContainsf(t, strings.TrimSpace(p), "internal/repogate",
			"%s is in the shipped binary's package graph. internal/repogate is repo tooling: it "+
				"walks the repository, parses its Go and writes ANSI to stdout. None of that "+
				"belongs in the binary a volunteer officer runs.", p)
	}
}

// TestRepoGates_WithoutTheGoToolchain_FailsClosed covers GATE000.
//
// MIG003 has always treated a missing `go` as a FAILURE rather than a skip, because a hash gate
// that cannot hash must not report green. Moving the rules into Go extends that to all of them, and
// the failure mode is the one that matters: a lint job whose toolchain step was dropped would
// otherwise run a script that finds no engine, prints nothing, and exits 0 — a green check over
// nothing at all, which is the exact shape of the defect this whole package exists to prevent.
//
// The fixture runs the script with a PATH that holds no `go`. Everything else it needs — `cd`,
// `printf`, `command` — is a bash builtin, so the run reaches the check rather than dying earlier
// for an unrelated reason.
func TestRepoGates_WithoutTheGoToolchain_FailsClosed(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	writeWorkflow(t, tree, "actions/checkout@"+pinnedCheckoutSHA)

	cmd := exec.Command("bash", scriptPath(t, "repo-gates.sh"))
	cmd.Env = []string{"DKP_REPO_ROOT=" + tree, "PATH=" + filepath.Join(tree, "no-tools"), "HOME=" + tree}

	out, err := cmd.CombinedOutput()

	require.Error(t, err, "no toolchain must not be a passing run\n%s", out)
	require.Contains(t, string(out), "GATE000",
		"the failure must name a rule id: a gate that cannot run has to say so in the same shape as "+
			"one that fired\n%s", out)
	require.Contains(t, string(out), "MIG003",
		"the message must name the frozen-migration rule, whose fail-closed posture this "+
			"generalises\n%s", out)
	require.NotContains(t, string(out), "repo gates passed", "%s", out)
}

// TestRepoGates_ScriptDelegatesToTheEngine is the anti-drift assertion for the shim.
//
// `scripts/repo-gates.sh` is the entry point every fixture in this package runs, and its whole job
// is now to hand the tree to internal/repogate. Two ways that could silently stop being true, and
// both would leave every fixture passing:
//
//   - a rule re-implemented in the script, so that the Go engine and the shell disagree and only
//     one of them is unit-tested;
//   - the delegation replaced by something that exits 0 on its own.
//
// So the script must invoke the engine, and must be short enough that nobody has hidden a rule in
// it. The line budget is deliberately generous — it is a smell test, not a formatter.
//
// It must also invoke it as a COMPILED BINARY rather than through `go run` (issue #142). `go run`
// exits 1 whatever the child exited with, which erases the engine's own distinction between "a rule
// fired" (1) and "the gates could not run" (2), and it appends `exit status 1` to stderr inside the
// failure block — where it reads as a crash in the tool rather than as a finding about the tree.
func TestRepoGates_ScriptDelegatesToTheEngine(t *testing.T) {
	t.Parallel()

	body := readRepoFile(t, "scripts/repo-gates.sh")

	require.Contains(t, body, "build -o \"$build_dir/repogate\" ./internal/repogate/cmd/repogate",
		"the gate script must delegate to the Go engine — that delegation is what makes the "+
			"fixtures in this package tests of the rules rather than tests of a shell pipeline")
	require.Contains(t, body, `env DKP_REPO_ROOT="$target" "$build_dir/repogate"`,
		"the gate script must RUN the binary it built, with the tree under inspection")

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		require.NotContains(t, trimmed, "go run",
			"the gates are invoked as a compiled binary, never through `go run`: it collapses the "+
				"engine's exit code and prints `exit status 1` into the failure block (issue #142)\n  %s",
			line)
	}

	var code int

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			code++
		}
	}

	require.Less(t, code, 30,
		"scripts/repo-gates.sh has grown %d lines of code. The rules live in internal/repogate "+
			"(issue #123) precisely so that they are unit-tested rather than only observed through "+
			"a subprocess; a rule implemented here would be neither.", code)
}
