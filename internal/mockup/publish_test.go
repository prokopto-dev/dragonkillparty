package mockup

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// wrap builds the smallest document Publish accepts, so a test can state only the fragment it is
// about. The three things Publish requires of an export — the tool's runtime script, a
// data-dc-script block and a <head>/<body> — are boilerplate here, and each has its own test below.
func wrap(body string) []byte {
	return []byte(`<!DOCTYPE html>
<html>
<head>
<script src="./support.js"></script>
<link rel="stylesheet" href="_ds/nocturne-abc/styles.css">
<script src="_ds/nocturne-abc/_ds_bundle.js"></script>
</head>
<body>
<x-dc>
` + body + `
</x-dc>
<script type="text/x-dc" data-dc-script data-props="{}">
class Component extends DCLogic {}
</script>
</body>
</html>
`)
}

// publish is the happy-path helper: publish a fragment and fail the test if it does not.
func publish(t *testing.T, body string) Page {
	t.Helper()

	page, err := Publish("fixture.dc.html", wrap(body), "Fixture")
	require.NoError(t, err)

	return page
}

func TestPublish_SingleChildDirective_IsLiftedOntoTheChild(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "sc-for onto a table row, the case that empties 37 tables",
			body: `<table><tbody><sc-for list="{{ rows }}" as="r"><tr><td>{{ r.name }}</td></tr></sc-for></tbody></table>`,
			want: `<tr data-sc-for="{{ rows }}" data-sc-as="r">`,
		},
		{
			name: "sc-for with no as= falls back to item, as the runtime does",
			body: `<sc-for list="{{ rows }}"><li>x</li></sc-for>`,
			want: `<li data-sc-for="{{ rows }}" data-sc-as="item">`,
		},
		{
			name: "sc-if onto its single child",
			body: `<sc-if value="{{ ok }}"><span>yes</span></sc-if>`,
			want: `<span data-sc-if="{{ ok }}">`,
		},
		{
			name: "an attribute containing > does not end the tag",
			body: `<sc-if value="{{ ok }}"><div title="a > b">x</div></sc-if>`,
			want: `<div title="a > b" data-sc-if="{{ ok }}">`,
		},
		{
			name: "a self-closing child is still a single child",
			body: `<sc-for list="{{ xs }}" as="x"><img src="a.png"/></sc-for>`,
			want: `<img src="a.png" data-sc-for="{{ xs }}" data-sc-as="x"/>`,
		},
		{
			name: "surrounding whitespace is not a sibling",
			body: "<sc-if value=\"{{ ok }}\">\n  <p>x</p>\n</sc-if>",
			want: `<p data-sc-if="{{ ok }}">`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page := publish(t, tc.body)

			require.Containsf(t, string(page.HTML), tc.want,
				"the directive was not lifted onto its child; published page:\n%s", page.HTML)
			require.NotContains(t, string(page.HTML), "<sc-for",
				"an element-form directive survived a single-child block")
			require.NotContains(t, string(page.HTML), "<sc-if",
				"an element-form directive survived a single-child block")
		})
	}
}

func TestPublish_AmbiguousDirective_KeepsTheElementForm(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		why  string
	}{
		{
			name: "two element children",
			body: `<sc-if value="{{ ok }}"><p>a</p><p>b</p></sc-if>`,
			why:  "lifting onto the first would let the second escape the condition",
		},
		{
			// The near-miss scripts/dc-publish.py's own comment documented: a self-closing tag must
			// not open a nesting level, or the <span> below is counted as the <img>'s child and the
			// block looks single-child.
			name: "a self-closing tag followed by a sibling",
			body: `<sc-for list="{{ xs }}" as="x"><img src="a.png"/><span>b</span></sc-for>`,
			why:  "a self-closing tag opens no level, so this is a two-child block",
		},
		{
			name: "no element children at all",
			body: `<sc-if value="{{ ok }}">bare text</sc-if>`,
			why:  "there is nothing to lift onto",
		},
		{
			// The regex implementation counted tags only, lifted onto the <b>, and left the copy
			// behind rendering unconditionally.
			name: "one element child plus loose text",
			body: `<sc-if value="{{ ok }}">Some copy<b>x</b></sc-if>`,
			why:  "the loose text is conditional too and cannot carry the attribute",
		},
		{
			// Lifting here would put data-sc-if on the <sc-for>, whose element-form branch in
			// mockup-runtime.js returns before it looks at data-* attributes — dropping the
			// condition silently.
			name: "a nested directive as the only child",
			body: `<sc-if value="{{ ok }}"><sc-for list="{{ xs }}" as="x"><p>a</p><p>b</p></sc-for></sc-if>`,
			why:  "the runtime's element-form branch never reads data-* attributes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page := publish(t, tc.body)
			out := string(page.HTML)

			require.Truef(t, strings.Contains(out, "<sc-if") || strings.Contains(out, "<sc-for"),
				"the element form should have been kept — %s; published page:\n%s", tc.why, out)
			require.NotContains(t, out, "data-sc-if=\"{{ ok }}\"><sc-for",
				"data-sc-if landed on a directive element, where the runtime never reads it")
		})
	}
}

func TestPublish_NestedDirectives_AreLiftedInnermostFirst(t *testing.T) {
	t.Parallel()

	// The inner sc-for collapses onto the <li>, which then makes the outer sc-if a single-child
	// block. Deciding the outer one first would see one child (<sc-for>), refuse to lift onto a
	// directive, and leave both element forms in place.
	page := publish(t, `<sc-if value="{{ ok }}"><sc-for list="{{ xs }}" as="x"><li>{{ x }}</li></sc-for></sc-if>`)

	require.Equal(t, 2, page.Lifted, "both directives should have been lifted")
	require.Contains(t, string(page.HTML), `<li data-sc-for="{{ xs }}" data-sc-as="x" data-sc-if="{{ ok }}">`)
}

func TestPublish_DirectiveSurvivingInsideTable_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := Publish("fixture.dc.html", wrap(
		`<table><tbody><sc-if value="{{ ok }}"><tr><td>a</td></tr><tr><td>b</td></tr></sc-if></tbody></table>`,
	), "Fixture")

	require.Error(t, err, "a two-child directive inside a tbody must not publish")
	require.Contains(t, err.Error(), "foster-parent")
	require.Contains(t, err.Error(), "<sc-if> survives inside <tbody>")
	require.Contains(t, err.Error(), "fixture.dc.html:", "the message must name the file and line")
}

// TestPublish_FosterParentedElement_IsRefusedEvenWhenItIsNotADirective is the gate the shell version
// could not express.
//
// <x-import> is one of the mockups' own elements and the lift does not touch it, so the
// "no directive in table context" check — the only structural assertion scripts/dc-publish.py made —
// passes this page happily. Handing the result to a real tree builder does not: the <x-import> is
// hoisted out of the table and arrives empty, and an empty custom element renders nothing.
func TestPublish_FosterParentedElement_IsRefusedEvenWhenItIsNotADirective(t *testing.T) {
	t.Parallel()

	body := `<table><tbody><x-import component-from-global-scope="Row"><tr><td>a</td></tr></x-import></tbody></table>`

	toks, err := tokenize(wrap(body))
	require.NoError(t, err)
	require.NoError(t, assertNoDirectiveInTable(toks, "fixture.dc.html"),
		"precondition: the directive-in-table check does not see this, which is the point")

	_, err = Publish("fixture.dc.html", wrap(body), "Fixture")
	require.Error(t, err, "an emptied custom element must not publish")
	require.Contains(t, err.Error(), "foster-parenting")
	require.Contains(t, err.Error(), "<x-import>")
}

// TestPublish_LiftedRowsSurviveARealParse renders the before and after through the same tree builder
// a browser uses, and shows the lift is what makes the difference.
func TestPublish_LiftedRowsSurviveARealParse(t *testing.T) {
	t.Parallel()

	const rows = `<table><tbody><sc-for list="{{ rows }}" as="r"><tr><td>{{ r.name }}</td></tr></sc-for></tbody></table>`

	before, err := html.Parse(bytes.NewReader(wrap(rows)))
	require.NoError(t, err)

	beforeCounts := map[string]int{}
	parsedChildCounts(before, beforeCounts)
	require.Zerof(t, beforeCounts[tagSCFor],
		"precondition: unpublished, the <sc-for> is foster-parented out of the table and left empty — "+
			"that is the bug the lift exists to fix")

	page := publish(t, rows)

	after, err := html.Parse(bytes.NewReader(page.HTML))
	require.NoError(t, err)

	require.Equal(t, 1, countElement(after, "tr"), "the row must survive the parse")
	require.Equal(t, 1, countElement(after, "td"), "the cell must survive the parse")
	require.Zero(t, countElement(after, tagSCFor), "no directive element should remain")

	tr := findElement(after, "tr")
	require.NotNil(t, tr)
	require.Equal(t, "tbody", tr.Parent.Data, "the row must still be inside the table")
	require.Equal(t, "{{ rows }}", attrValue(tr, "data-sc-for"))
	require.Equal(t, "r", attrValue(tr, "data-sc-as"))
}

func TestPublish_RuntimeReferences_ArePointedAtOurHarness(t *testing.T) {
	t.Parallel()

	page := publish(t, `<p>hello</p>`)
	out := string(page.HTML)

	require.Contains(t, out, `<script src="./mockup-runtime.js"></script>`)
	require.Contains(t, out, `<script src="./ios-frame.js"></script>`)
	require.Contains(t, out, `href="./nocturne/styles.css"`)
	require.NotContains(t, out, "support.js")
	require.NotContains(t, out, "_ds/")
	require.NotContains(t, out, "_ds_bundle.js")
	require.Empty(t, CheckNoStaleRefs(page.HTML), "MOCK003 must have nothing left to find")
}

// TestPublish_ScriptType_IsStrippedWhateverTheAttributeOrder pins a defect in the implementation
// this replaces.
//
// scripts/dc-publish.py replaced the literal text `<script type="text/x-dc" data-dc-script`. A
// refreshed export that emitted the two attributes the other way round would have kept the type, and
// a script with an unknown type does not execute — so every binding on the page would have rendered
// empty, silently, because str.replace with no match returns the string unchanged.
func TestPublish_ScriptType_IsStrippedWhateverTheAttributeOrder(t *testing.T) {
	t.Parallel()

	src := bytes.Replace(wrap(`<p>x</p>`),
		[]byte(`<script type="text/x-dc" data-dc-script data-props="{}">`),
		[]byte(`<script data-dc-script data-props="{}" type="text/x-dc">`), 1)

	page, err := Publish("fixture.dc.html", src, "Fixture")
	require.NoError(t, err)
	require.NotContains(t, string(page.HTML), "text/x-dc",
		"the authored logic will not execute while it carries an unknown script type")
	require.Contains(t, string(page.HTML), "data-dc-script")
}

func TestPublish_BannerAndNoindex_AreInjected(t *testing.T) {
	t.Parallel()

	page, err := Publish("fixture.dc.html", wrap(`<p>x</p>`), "Guild portal")
	require.NoError(t, err)

	out := string(page.HTML)
	require.Contains(t, out, robotsMeta)
	require.Contains(t, out, `id="dkp-mockup-banner"`)
	require.Contains(t, out, `<span class="dkp-mockup-surface">Guild portal</span>`)

	noindex, err := CheckNoindex(page.HTML)
	require.NoError(t, err)
	require.True(t, noindex, "MOCK004 must pass on a page this package produced")

	// The banner goes inside <body> and the noindex inside <head>, so both have to be where the
	// parser puts them, not merely present in the bytes.
	doc, err := html.Parse(bytes.NewReader(page.HTML))
	require.NoError(t, err)

	banner := findElement(doc, "div")
	require.NotNil(t, banner)
	require.Equal(t, "dkp-mockup-banner", attrValue(banner, "id"),
		"the banner must be the first div in the document, immediately after <body>")
}

func TestPublish_BannerTitle_IsEscaped(t *testing.T) {
	t.Parallel()

	page, err := Publish("fixture.dc.html", wrap(`<p>x</p>`), `<script>alert(1)</script>`)
	require.NoError(t, err)
	require.NotContains(t, string(page.HTML), "<script>alert(1)</script>",
		"a surface title is interpolated into the banner and must not be able to inject markup")
	require.Contains(t, string(page.HTML), "&lt;script&gt;alert(1)&lt;/script&gt;")
}

// TestPublish_MalformedExport_IsRefused covers the anchors Publish needs. Each was a silent failure
// in the Python for the same reason — str.replace and re.search return "nothing happened" — and each
// is now a named refusal.
func TestPublish_MalformedExport_IsRefused(t *testing.T) {
	t.Parallel()

	full := string(wrap(`<p>x</p>`))

	for _, tc := range []struct {
		name    string
		remove  string
		wantErr string
	}{
		{"no design-tool runtime", `<script src="./support.js"></script>`, "not a design-tool export"},
		{"no authored logic", `<script type="text/x-dc" data-dc-script data-props="{}">`, "data-dc-script"},
		{"no head", `</head>`, "no </head>"},
		{"no body", `<body>`, "no <body>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			src := strings.Replace(full, tc.remove, "", 1)
			require.NotEqual(t, full, src, "the fixture did not change — this test is asserting nothing")

			_, err := Publish("fixture.dc.html", []byte(src), "Fixture")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestPublish_UnmodifiedMarkup_IsCopiedByteForByte(t *testing.T) {
	t.Parallel()

	// Attribute names the tokenizer would lowercase, a value the standard escaper would rewrite, and
	// a valueless attribute: all three survive because a lifted tag is spliced, not re-rendered.
	page := publish(t, `<sc-for list="{{ xs }}" as="x"><button onClick="{{ x.pick }}" data-flag `+
		`title="it&apos;s &quot;quoted&quot;">go</button></sc-for>`)

	require.Contains(t, string(page.HTML),
		`<button onClick="{{ x.pick }}" data-flag title="it&apos;s &quot;quoted&quot;" `+
			`data-sc-for="{{ xs }}" data-sc-as="x">`,
		"a lifted tag must keep every byte the author wrote, with only the directive appended")
}

// countElement tallies element nodes with the given name.
func countElement(n *html.Node, name string) int {
	count := 0

	if n.Type == html.ElementNode && n.Data == name {
		count++
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		count += countElement(c, name)
	}

	return count
}

// findElement returns the first element node with the given name, in document order.
func findElement(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Data == name {
		return n
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, name); found != nil {
			return found
		}
	}

	return nil
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}

	return ""
}
