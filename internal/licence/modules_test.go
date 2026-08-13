package licence_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/licence"
)

// TestGatePlatforms_AreASubsetOfReleasePlatforms is the assertion the consolidation exists to make
// possible (issue #130).
//
// The licence gate and the third-party notices generator used to enumerate the release platforms
// separately, in two shell dialects, under a comment asking whoever edited one to "keep in step
// with" the other. Nothing could see a divergence: a platform added to the notices list and not to
// the gate's is a module whose licence is never classified, and it fails silently and permanently.
//
// Now there is one list and one documented subset of it, and this is the relationship stated as a
// test. TestLicenceGate_PlatformSubset_CoversTheFullMatrix in test/repo does the expensive half —
// it resolves the whole matrix and requires the subset to reach every module in it.
func TestGatePlatforms_AreASubsetOfReleasePlatforms(t *testing.T) {
	t.Parallel()

	release := licence.ReleasePlatforms()
	require.NotEmpty(t, release, "the release platform list is empty")

	gate := licence.GatePlatforms()
	require.NotEmpty(t, gate, "the gate would resolve no module graph at all")

	for _, p := range gate {
		require.Containsf(t, release, p,
			"the licence gate queries %s, which is not a platform this project releases. Either the "+
				"release matrix lost a platform the gate still classifies, or the gate gained one "+
				"nobody ships — and the two lists have drifted, which is what merging them was for.", p)
	}

	// Every released OS must be reachable, whatever the arch subset. `go list` resolves build
	// constraints one GOOS/GOARCH at a time, so an OS the gate never queries is an entire class of
	// `//go:build` -gated dependency whose licence is never seen.
	for _, p := range release {
		require.Truef(t, slices.ContainsFunc(gate, func(q licence.Platform) bool { return q.GOOS == p.GOOS }),
			"no gate platform runs on %s, so a dependency behind `//go:build %s` would ship "+
				"unclassified", p.GOOS, p.GOOS)
	}
}

// TestPlatform_String_IsTheToolchainSpelling pins the form used in error messages and in the
// GOOS/GOARCH pair the Go toolchain and .goreleaser.yaml both write.
func TestPlatform_String_IsTheToolchainSpelling(t *testing.T) {
	t.Parallel()

	require.Equal(t, "windows/arm64", licence.Platform{GOOS: "windows", GOARCH: "arm64"}.String())
}

// TestRuntimeModules_NoPlatforms_IsAnError closes the emptiest vacuous-pass path there is.
//
// An empty platform list resolves an empty graph, and an empty graph looks exactly like a clean one
// to any caller that only counts violations. The gate has no vacuous-pass path by design, so this is
// an error rather than a quiet zero.
func TestRuntimeModules_NoPlatforms_IsAnError(t *testing.T) {
	t.Parallel()

	_, err := licence.RuntimeModules(t.TempDir(), nil, "./...")

	require.Error(t, err, "resolving no platforms must never look like resolving a clean graph")
	require.Contains(t, err.Error(), "no platforms")
}

// TestRuntimeModules_TreeWithNoModule_IsAnError covers the tree that cannot be resolved at all.
//
// `go list` fails outright outside a module, and the gate must say so rather than report an empty
// dependency set — the difference between "this project depends on nothing" and "nothing was read"
// is the whole fail-closed argument.
func TestRuntimeModules_TreeWithNoModule_IsAnError(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to `go list`; run `make test` or `make check`")
	}

	_, err := licence.RuntimeModules(t.TempDir(), []licence.Platform{{GOOS: "linux", GOARCH: "amd64"}}, "./...")

	require.Error(t, err, "a tree with no go.mod must be an error, never an empty module set")
	require.Contains(t, err.Error(), "go list", "the error must name what failed")
}

// TestRuntimeModules_ReportsTheMainModule covers the flag every caller filters on.
//
// The main module is returned rather than dropped inside the query on purpose: its presence is what
// makes an empty result mean "go list resolved nothing" rather than "this module has no runtime
// dependencies", which is a legitimate state and was this repository's own until sqlite landed.
// [licence.Dependencies] is then the one place that filter is expressed.
func TestRuntimeModules_ReportsTheMainModule(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to `go list`; run `make test` or `make check`")
	}

	tree := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(tree, name), []byte(body), 0o644))
	}

	write("go.mod", "module fixture\n\ngo 1.26\n")
	write("main.go", "package main\n\nfunc main() {}\n")

	modules, err := licence.RuntimeModules(tree,
		[]licence.Platform{{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "windows", GOARCH: "amd64"}}, "./...")
	require.NoError(t, err)

	require.Len(t, modules, 1, "a dependency-free module resolves to itself alone, once per union: %+v", modules)
	require.True(t, modules[0].Main, "the module being inspected must be flagged Main: %+v", modules[0])
	require.Equal(t, "fixture", modules[0].Path)

	require.Empty(t, licence.Dependencies(modules),
		"a module whose only entry is itself has no dependencies — and that is a legitimate state, "+
			"not the vacuous pass the empty-graph guard exists for")
}

// TestRuntimeModules_HostileGOFLAGS_ResolvesTheSameGraph fences the environment variable that can
// hand this function's two callers two different dependency graphs (issue #141).
//
// `make licence-gate` has stripped GOFLAGS from its recipe since it was written: it can carry
// -mod=vendor or -tags, either of which changes which modules `go list` resolves, and a developer's
// environment must not decide which graph the licence firewall inspects. `make third-party-notices`
// never had that guard. While the two were separate shell implementations the asymmetry was easy to
// miss; since they call THIS function the same enumeration could be handed two environments, so the
// gate would classify one graph and the THIRD_PARTY_NOTICES.txt attached to every release archive
// and Linux package would describe another.
//
// The fixture makes both hostile values observable, which is the whole point of doing it here rather
// than on a recipe: -tags=extra SILENTLY adds a module to the graph (the failure mode that ships),
// and -mod=vendor fails the query outright against a tree with no vendor directory. The dependency
// is a filesystem `replace`, so the test needs no network and no module cache.
func TestRuntimeModules_HostileGOFLAGS_ResolvesTheSameGraph(t *testing.T) {
	// No t.Parallel: t.Setenv panics in a parallel test, and the environment is the subject here.
	if testing.Short() {
		t.Skip("shells out to `go list`; run `make test` or `make check`")
	}

	tree := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(tree, name)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(tree, name), []byte(body), 0o644))
	}

	write("dep/go.mod", "module example.test/dep\n\ngo 1.26\n")
	write("dep/dep.go", "package dep\n\nfunc Hello() string { return \"hi\" }\n")
	write("go.mod", "module fixture\n\ngo 1.26\n\nrequire example.test/dep v0.0.0\n\n"+
		"replace example.test/dep => ./dep\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	// Reachable only under `-tags extra`, which is exactly how a GOFLAGS value changes the answer.
	write("tagged.go", "//go:build extra\n\npackage main\n\nimport _ \"example.test/dep\"\n")

	platforms := []licence.Platform{{GOOS: "linux", GOARCH: "amd64"}}

	clean, err := licence.RuntimeModules(tree, platforms, "./...")
	require.NoError(t, err)
	require.Len(t, clean, 1,
		"precondition: without the tag the fixture depends on nothing: %+v", clean)

	for _, goflags := range []string{"-tags=extra", "-mod=vendor", "-mod=vendor -tags=extra"} {
		t.Run(goflags, func(t *testing.T) {
			t.Setenv("GOFLAGS", goflags)

			got, err := licence.RuntimeModules(tree, platforms, "./...")
			require.NoErrorf(t, err,
				"GOFLAGS=%q reached `go list`, so a developer's shell decides which graph is "+
					"inspected — is GOFLAGS still cleared in RuntimeModules?", goflags)
			require.Equalf(t, clean, got,
				"GOFLAGS=%q changed the resolved module set. The licence gate and the notices "+
					"generator read the graph through this one function; a value that reaches it "+
					"classifies one graph and ships attribution for another.", goflags)
		})
	}
}
