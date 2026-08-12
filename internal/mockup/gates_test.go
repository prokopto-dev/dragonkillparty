package mockup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCheckBindings_ExpressionBinding_IsReported is MOCK001's positive and negative cases.
//
// The runtime resolves a binding as a dotted path or a literal and nothing else, deliberately: there
// is no eval, no new Function and no expression evaluator to escape from. A binding it cannot walk
// renders as an empty string, silently, which is why this is a build gate and not a convention.
func TestCheckBindings_ExpressionBinding_IsReported(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "dotted paths and literals are fine",
			src:  `<p>{{ guildName }} {{ a.b.c }} {{ true }} {{ false }} {{ null }} {{ -12.5 }} {{ $x }}</p>`,
		},
		{
			name: "a binding in an attribute value is scanned too",
			src:  `<div style="width:{{ w || 10 }}px"></div>`,
			want: []string{"w || 10"},
		},
		{
			name: "a call expression",
			src:  `<p>{{ format(x) }}</p>`,
			want: []string{"format(x)"},
		},
		{
			name: "a ternary",
			src:  `<p>{{ a ? b : c }}</p>`,
			want: []string{"a ? b : c"},
		},
		{
			// The reason scripts/build-mockup-site.sh extracted these in Python rather than with
			// grep: a `[^}]*` character class cannot see this one, and a binding the gate cannot see
			// is exactly the one that slips through and renders empty.
			name: "a brace inside the expression",
			src:  `<p>{{ a } b }}</p>`,
			want: []string{"a } b"},
		},
		{
			// The runtime runs its regex over the PARSED text node, so an entity-escaped brace is a
			// binding to it. Scanning raw source bytes, as the regex implementation did, misses this.
			name: "braces written as entity references",
			src:  `<p>&#123;&#123; a || b &#125;&#125;</p>`,
			want: []string{"a || b"},
		},
		{
			name: "several offenders are reported once each, sorted",
			src:  `<p>{{ a || b }}{{ a || b }}{{ z(1) }}</p>`,
			want: []string{"a || b", "z(1)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bad, err := CheckBindings([]byte(tc.src))
			require.NoError(t, err)
			require.Equal(t, tc.want, emptyToNil(bad))
		})
	}
}

func TestCheckNoVendoredRuntime_VendoredFile_IsReported(t *testing.T) {
	t.Parallel()

	for _, name := range vendoredRuntimes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", name), []byte("//"), 0o644))

			found, err := CheckNoVendoredRuntime(dir)
			require.NoError(t, err)
			require.Len(t, found, 1, "%s is the design tool's unlicensed runtime and must be refused", name)
		})
	}
}

func TestCheckNoVendoredRuntime_CleanTree_IsAccepted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "mockup-runtime.js"), []byte("//"), 0o644))

	found, err := CheckNoVendoredRuntime(dir)
	require.NoError(t, err)
	require.Empty(t, found)
}

func TestCheckNoStaleRefs_SurvivingReference_IsReported(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		page   string
		marker string
	}{
		{"the design tool's asset layout", "<link href=\"_ds/x/styles.css\">", "_ds/"},
		{"the design tool's runtime", "<script src=\"./support.js\"></script>", "support.js"},
		{"the script type that stops the logic executing", `<script type="text/x-dc">`, "text/x-dc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			refs := CheckNoStaleRefs([]byte("<html>\n" + tc.page + "\n</html>"))
			require.Len(t, refs, 1)
			require.Equal(t, tc.marker, refs[0].Marker)
			require.Equal(t, 2, refs[0].Line, "the line number is what makes the failure fixable")
		})
	}
}

func TestCheckNoStaleRefs_RewrittenPage_IsAccepted(t *testing.T) {
	t.Parallel()

	require.Empty(t, CheckNoStaleRefs([]byte(
		"<link href=\"./nocturne/styles.css\">\n<script src=\"./mockup-runtime.js\"></script>\n")))
}

// TestCheckNoindex_Spelling_IsReadAsAParserWould is MOCK004.
//
// The shell gate matched `<meta[^>]*name="robots"[^>]*content="[^"]*noindex` over the file. Four of
// the cases below are ones that regex answers wrongly — three false negatives and, worse, one false
// positive on a tag inside a comment. A crawler reads the parsed document, so the gate does too.
func TestCheckNoindex_Spelling_IsReadAsAParserWould(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		head string
		want bool
	}{
		{"the tag this package writes", robotsMeta, true},
		{"single-quoted attributes", `<meta name='robots' content='noindex'>`, true},
		{"unquoted attributes", `<meta name=robots content=noindex>`, true},
		{"attributes in the other order", `<meta content="noindex" name="robots">`, true},
		{"noindex alongside another directive", `<meta name="robots" content="noindex, nofollow">`, true},
		{"an uppercase spelling", `<meta name="ROBOTS" content="NOINDEX">`, true},
		{"no robots meta at all", `<meta charset="utf-8">`, false},
		{"a robots meta that does not say noindex", `<meta name="robots" content="all">`, false},
		{"noindex only as a substring of another directive", `<meta name="robots" content="noindexing">`, false},
		{"the tag commented out", `<!-- <meta name="robots" content="noindex"> -->`, false},
		{"the tag quoted inside a script", `<script>var s = '<meta name="robots" content="noindex">';</script>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := CheckNoindex([]byte("<!DOCTYPE html><html><head>" + tc.head + "</head><body></body></html>"))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// emptyToNil normalises an empty slice to nil so a table can leave `want` unset for the happy case.
func emptyToNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}

	return s
}
