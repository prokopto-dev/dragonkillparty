// Negative fixture tests for the mockup site build — MOCK001 to MOCK004 and the foster-parenting
// refusal.
//
// These gates used to live in scripts/build-mockup-site.sh and scripts/dc-publish.py, where their
// logic could only be exercised as a black box: run the script, grep its stdout. Issue #126 moved
// them to internal/mockup on a real HTML5 parser, so each one can be driven against a purpose-broken
// tree and required to say which rule fired. Every fixture the shell version had is carried over
// here unchanged in substance; nothing is skipped and nothing is relaxed.
package repo_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/mockup"
)

// mockupsRel is where the vendored surfaces live.
const mockupsRel = "docs/design/mockups"

// fixtureSurface is the smallest .dc.html the build accepts: the design tool's runtime script, its
// stylesheet and namespace bundle, a <helmet>, an <x-dc> host and the authored logic block.
const fixtureSurface = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script src="./support.js"></script>
</head>
<body>
<x-dc>
<helmet>
<link rel="stylesheet" href="_ds/nocturne-abc/styles.css">
<script src="_ds/nocturne-abc/_ds_bundle.js"></script>
</helmet>
%s
</x-dc>
<script type="text/x-dc" data-dc-script data-props="{}">
class Component extends DCLogic { renderVals() { return {}; } }
</script>
</body>
</html>
`

// fixtureIndex is the hand-written landing page. It carries its own noindex, exactly as the real
// docs/design/mockups/index.html does — MOCK004 is checked over the output, so this file has to
// clear it by a different route than the surfaces do.
const fixtureIndex = `<!DOCTYPE html>
<html>
<head><meta name="robots" content="noindex"><title>Mockups</title></head>
<body><a href="./fixture.dc.html">Fixture</a></body>
</html>
`

// mockupFixture builds a minimal but structurally real mockups directory in t.TempDir() and returns
// its path. body is spliced into the surface template.
func mockupFixture(t *testing.T, body string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "mockups")

	write := func(rel, content string) {
		t.Helper()

		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	write("fixture.dc.html", strings.Replace(fixtureSurface, "%s", body, 1))
	write("index.html", fixtureIndex)
	write("nocturne/styles.css", ":root{--color-bg:#161826}\n")
	write("harness/mockup-runtime.js", "// runtime\n")
	write("harness/ios-frame.js", "// frame\n")

	return dir
}

// buildFixture runs the whole site build against a fixture tree, returning its progress output and
// the error it failed with, if any.
func buildFixture(t *testing.T, srcDir string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := mockup.Build(srcDir, filepath.Join(t.TempDir(), "_site"), &out)

	return out.String(), err
}

// TestMockupSite_RealSurfaces_PublishAndPassEveryGate is the positive case, over the tree that
// actually ships.
//
// It is what `make mockup-site` does, minus writing into the repository. Without it every negative
// fixture below could pass while the real build was broken.
func TestMockupSite_RealSurfaces_PublishAndPassEveryGate(t *testing.T) {
	t.Parallel()

	src := filepath.Join(repoRoot(t), mockupsRel)
	outDir := filepath.Join(t.TempDir(), "_site")

	var out bytes.Buffer

	require.NoError(t, mockup.Build(src, outDir, &out), "the vendored mockups must publish:\n%s", out.String())

	for _, id := range []string{"[MOCK001]", "[MOCK002]", "[MOCK003]", "[MOCK004]"} {
		require.Containsf(t, out.String(), id,
			"%s did not report — a gate that does not run is not a gate:\n%s", id, out.String())
	}

	surfaces, err := filepath.Glob(filepath.Join(outDir, "*.dc.html"))
	require.NoError(t, err)
	require.Len(t, surfaces, 5, "five surfaces ship; a change to that set is a change to the design reference")

	for _, p := range surfaces {
		body, err := os.ReadFile(p)
		require.NoError(t, err)

		noindex, err := mockup.CheckNoindex(body)
		require.NoError(t, err)
		require.Truef(t, noindex, "%s published without a noindex", filepath.Base(p))
		require.Emptyf(t, mockup.CheckNoStaleRefs(body), "%s still references the design tool", filepath.Base(p))
		require.Containsf(t, string(body), `id="dkp-mockup-banner"`,
			"%s published without the MOCKUP banner", filepath.Base(p))
	}

	for _, rel := range []string{"index.html", "mockup-runtime.js", "ios-frame.js", "nocturne/styles.css"} {
		_, err := os.Stat(filepath.Join(outDir, rel))
		require.NoErrorf(t, err, "%s was not published", rel)
	}
}

// TestMockupSite_ExpressionBinding_FailsMOCK001 is the gate that keeps the runtime free of an
// expression evaluator.
//
// harness/mockup-runtime.js resolves a binding as a dotted path or a literal and nothing else, with
// no eval and no new Function. A refreshed export that introduced `{{ a || b }}` would render that
// binding as an empty string, silently — so the build refuses instead, naming the expression.
func TestMockupSite_ExpressionBinding_FailsMOCK001(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"in a text node", `<p>{{ a || b }}</p>`, "a || b"},
		{"in an attribute value", `<div style="width:{{ w * 2 }}px"></div>`, "w * 2"},
		{"a call the resolver cannot walk", `<p>{{ fmt(x) }}</p>`, "fmt(x)"},
		// A `[^}]*` character class cannot see this one, which is why the gate has never been a grep.
		{"a brace inside the expression", `<p>{{ a } b }}</p>`, "a } b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildFixture(t, mockupFixture(t, tc.body))

			require.Error(t, err)
			require.Contains(t, err.Error(), "[MOCK001]")
			require.Contains(t, err.Error(), tc.want, "the gate must name the offending expression")
		})
	}
}

// TestMockupSite_VendoredRuntime_FailsMOCK002 refuses the design tool's own runtime by filename.
//
// NOTICE states no third-party source is vendored here, and that runtime carries no licence. The
// filenames are refused outright rather than trusted to stay deleted.
func TestMockupSite_VendoredRuntime_FailsMOCK002(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"support.js", "ios-frame.jsx", "_ds_bundle.js"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := mockupFixture(t, `<p>x</p>`)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", name), []byte("//"), 0o644))

			_, err := buildFixture(t, dir)

			require.Error(t, err)
			require.Contains(t, err.Error(), "[MOCK002]")
			require.Contains(t, err.Error(), name)
			require.Contains(t, err.Error(), "NOTICE", "the failure must point at the licence reason")
		})
	}
}

// TestMockupSite_StaleReference_FailsMOCK003 covers a reference the rewrites cannot reach.
//
// The rewrites repoint every <script> and stylesheet the export declares. They do not, and should
// not, rewrite the authored JavaScript — so a `_ds/` path written inside it survives into the
// published page, and MOCK003 is what notices.
func TestMockupSite_StaleReference_FailsMOCK003(t *testing.T) {
	t.Parallel()

	src := mockupFixture(t, `<p>x</p>`)
	page := filepath.Join(src, "fixture.dc.html")

	body, err := os.ReadFile(page)
	require.NoError(t, err)

	tainted := strings.Replace(string(body),
		"renderVals() { return {}; }",
		`renderVals() { return { logo: "_ds/nocturne-abc/logo.svg" }; }`, 1)
	require.NotEqual(t, string(body), tainted, "the fixture did not change — this test is asserting nothing")
	require.NoError(t, os.WriteFile(page, []byte(tainted), 0o644))

	_, err = buildFixture(t, src)

	require.Error(t, err)
	require.Contains(t, err.Error(), "[MOCK003]")
	require.Contains(t, err.Error(), "_ds/")
}

// TestMockupSite_PageWithoutNoindex_FailsMOCK004 checks the gate over the OUTPUT rather than the
// source.
//
// index.html carries the tag by hand while the surfaces get it from the publish step, and the
// property has to hold no matter which path produced the file. These are fabricated guild rosters,
// balances and bids for a product that has not shipped; a search result for one would read as a live
// instance, and a search snippet does not show the banner that says otherwise.
func TestMockupSite_PageWithoutNoindex_FailsMOCK004(t *testing.T) {
	t.Parallel()

	src := mockupFixture(t, `<p>x</p>`)

	stripped := strings.Replace(fixtureIndex, `<meta name="robots" content="noindex">`, "", 1)
	require.NotEqual(t, fixtureIndex, stripped, "the fixture did not change — this test is asserting nothing")
	require.NoError(t, os.WriteFile(filepath.Join(src, "index.html"), []byte(stripped), 0o644))

	_, err := buildFixture(t, src)

	require.Error(t, err)
	require.Contains(t, err.Error(), "[MOCK004]")
	require.Contains(t, err.Error(), "index.html")
}

// TestMockupSite_DirectiveLeftInsideTable_IsRefused is the foster-parenting refusal.
//
// An unknown element inside a <table> is foster-parented out of it by the HTML5 tree construction
// algorithm, and arrives at its new position empty — the runtime repeats a directive's CHILDREN, so
// an emptied one renders nothing. That silently emptied 37 of the mockups' tables. The publish step
// lifts each directive onto the element it repeats; a block with several children cannot be lifted,
// so inside a table it is refused rather than published broken.
func TestMockupSite_DirectiveLeftInsideTable_IsRefused(t *testing.T) {
	t.Parallel()

	src := mockupFixture(t,
		`<table><tbody><sc-for list="{{ rows }}" as="r"><tr><td>a</td></tr><tr><td>b</td></tr></sc-for></tbody></table>`)

	_, err := buildFixture(t, src)

	require.Error(t, err)
	require.Contains(t, err.Error(), "foster-parent")
	require.Contains(t, err.Error(), "fixture.dc.html:", "the failure must name the file and the line")
}

// TestMockupSite_LoopedRowsInATable_Publish is the same shape, lifted rather than refused — the
// case that covers 37 real tables.
func TestMockupSite_LoopedRowsInATable_Publish(t *testing.T) {
	t.Parallel()

	src := mockupFixture(t,
		`<table><tbody><sc-for list="{{ rows }}" as="r"><tr><td>{{ r.name }}</td></tr></sc-for></tbody></table>`)
	outDir := filepath.Join(t.TempDir(), "_site")

	require.NoError(t, mockup.Build(src, outDir, io.Discard))

	body, err := os.ReadFile(filepath.Join(outDir, "fixture.dc.html"))
	require.NoError(t, err)

	require.Contains(t, string(body), `<tr data-sc-for="{{ rows }}" data-sc-as="r">`,
		"the directive must move onto the row it repeats, which is valid HTML inside a table")
	require.NotContains(t, string(body), "<sc-for")
}

// TestMockupSite_EmptyMockupsDir_IsRefused closes the vacuous pass.
//
// Every assertion in the build is per page, so a directory with no surfaces in it would clear all
// four gates having examined nothing and report a green build that published no mockups.
func TestMockupSite_EmptyMockupsDir_IsRefused(t *testing.T) {
	t.Parallel()

	src := mockupFixture(t, `<p>x</p>`)
	require.NoError(t, os.Remove(filepath.Join(src, "fixture.dc.html")))

	_, err := buildFixture(t, src)

	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing was gated")
}

// TestMockupSite_MissingMockupsDir_IsRefused is the same failure one level up.
func TestMockupSite_MissingMockupsDir_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := buildFixture(t, filepath.Join(t.TempDir(), "does-not-exist"))

	require.ErrorIs(t, err, mockup.ErrNotFound)
}

// TestMockupSite_MakefileTarget_RunsTheGoCommand pins the wiring.
//
// The gates are only gates because `make mockup-site` and .github/workflows/pages.yml run them. A
// target that quietly went back to publishing the files with `cp` would leave every test above green
// and every gate unenforced.
func TestMockupSite_MakefileTarget_RunsTheGoCommand(t *testing.T) {
	t.Parallel()

	makefile := readRepoFile(t, "Makefile")

	require.Contains(t, makefile, "run ./internal/mockup/sitegen",
		"`make mockup-site` must run the Go command that holds MOCK001-004")

	// Invocation, not mention: the recipe's comment names both scripts because it records why they
	// went away, and a gate that cannot tell a reference from a call is a gate that gets deleted.
	for _, call := range []string{"bash scripts/build-mockup-site.sh", "python3 scripts/dc-publish.py"} {
		require.NotContainsf(t, makefile, call,
			"the Makefile still calls `%s`, which was replaced by internal/mockup in issue #126", call)
	}

	for _, gone := range []string{"scripts/build-mockup-site.sh", "scripts/dc-publish.py"} {
		_, err := os.Stat(filepath.Join(repoRoot(t), gone))
		require.Truef(t, os.IsNotExist(err), "%s should have been deleted with its replacement", gone)
	}
}

// TestPagesWorkflow_WatchesTheSourcesItBuildsFrom pins the path filter.
//
// pages.yml deploys what `make mockup-site` writes. When the build logic moved out of scripts/ the
// filter had to move with it, or a change to the publish step would deploy nothing and the stale
// site would look like a successful one.
func TestPagesWorkflow_WatchesTheSourcesItBuildsFrom(t *testing.T) {
	t.Parallel()

	workflow := readRepoFile(t, ".github/workflows/pages.yml")

	require.Contains(t, workflow, "'internal/mockup/**'",
		"pages.yml must rebuild when the publish logic changes")
	require.Contains(t, workflow, "'docs/design/mockups/**'",
		"pages.yml must rebuild when a surface changes")
	require.NotContains(t, workflow, "scripts/dc-publish.py",
		"the filter still names a script that no longer exists, so it can never match")
	require.Contains(t, workflow, "setup-toolchain",
		"`make mockup-site` is `go run` now — the job needs the Go the module asks for")
}
