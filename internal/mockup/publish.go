package mockup

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Publish rewrites one vendored .dc.html mockup into a publishable page.
//
// The vendored files under docs/design/mockups/ stay byte-exact so a diff against a fresh export is
// readable. Everything needed to publish them happens here, on a copy:
//
//  1. point the runtime <script> at our own harness instead of the design tool's
//  2. drop the design tool's no-op namespace bundle
//  3. rewrite the design system's stylesheet path to the layout this repo uses
//  4. strip type="text/x-dc" so the authored logic executes as an ordinary classic script
//  5. lift <sc-for>/<sc-if> onto their child element where the child is unambiguous, and <x-import>
//     onto its child where it sits in table context
//  6. inject the "MOCKUP — not a live instance" banner
//  7. inject <meta name="robots" content="noindex">
//
// Step 5 is the one with teeth. An unknown element inside a <table> is *foster-parented* out of the
// table by the HTML5 tree construction algorithm — the browser hoists <sc-for> above the <table> and
// then discards the <tr> children it contained, because a <tr> is meaningless outside table context.
// 37 of the mockups' tables loop their rows that way, so left alone they render headers and no body.
// Moving the directive onto the repeated element itself (<tr data-sc-for="…" data-sc-as="…">) is
// valid HTML everywhere and survives parsing intact.
//
// name is used in error messages only; it is the source file's base name.
func Publish(name string, src []byte, title string) (Page, error) {
	toks, err := tokenize(src)
	if err != nil {
		return Page{}, fmt.Errorf("publish %s: %w", name, err)
	}

	if err := rewriteRuntimeRefs(toks); err != nil {
		return Page{}, fmt.Errorf("publish %s: %w", name, err)
	}

	lifted, err := liftDirectives(toks, 0, len(toks), inTableContext(toks))
	if err != nil {
		return Page{}, fmt.Errorf("publish %s: %w", name, err)
	}

	if err := assertNoDirectiveInTable(toks, name); err != nil {
		return Page{}, err
	}

	if err := injectChrome(name, toks, title); err != nil {
		return Page{}, err
	}

	out := emit(toks)

	if err := assertCustomElementsKeepTheirChildren(name, out); err != nil {
		return Page{}, err
	}

	return Page{Name: name, Title: title, HTML: out, Lifted: lifted}, nil
}

// Page is one published surface.
type Page struct {
	Name   string // source base name, e.g. "admin-console.dc.html"
	Title  string // the surface title shown in the banner
	HTML   []byte
	Lifted int // directives moved onto their child element
}

// dsStylesheet matches the design tool's per-project stylesheet path, which this repo serves from
// nocturne/ instead. Matched against a tokenizer-supplied attribute VALUE, never against the
// document — finding the attribute is the parser's job, and was the regex implementation's bug
// surface.
var dsStylesheet = regexp.MustCompile(`^(?:\./)?_ds/[^"]*/styles\.css$`)

// dsBundle matches the design tool's namespace bundle, a no-op stub that is dropped outright.
var dsBundle = regexp.MustCompile(`^(?:\./)?_ds/[^"]*/_ds_bundle\.js$`)

const (
	toolRuntimeSrc = "./support.js"
	ourRuntime     = `<script src="./mockup-runtime.js"></script>` + "\n" + `<script src="./ios-frame.js"></script>`
	dcScriptType   = "text/x-dc"
)

// rewriteRuntimeRefs performs steps 1 to 4: everything that points at the design tool's own runtime
// or stylesheet layout, plus the script type that stops the authored logic from executing.
//
// Every one of these is now keyed off a parsed tag and a parsed attribute rather than a substring of
// the document. That matters most for step 4: the Python replaced the literal text
// `<script type="text/x-dc" data-dc-script`, so a refreshed export that reordered those two
// attributes would have left the type in place and the authored logic inert — silently, because
// str.replace with no match returns the string unchanged.
func rewriteRuntimeRefs(toks []token) error {
	sawSupport, sawDCScript := false, false

	for i := range toks {
		t := &toks[i]

		if t.typ != html.StartTagToken && t.typ != html.SelfClosingTagToken {
			continue
		}

		// 3 — the design system lives at nocturne/ here, not under the tool's _ds/<uuid>/ layout.
		for a := range t.attrs {
			if dsStylesheet.MatchString(t.attrs[a].Val) {
				t.attrs[a].Val = "./nocturne/styles.css"
				t.render()
			}
		}

		if t.name != "script" {
			continue
		}

		src, _ := t.attr("src")

		switch {
		// 1 — our harness replaces the design tool's runtime, and carries the iPhone frame the two
		// phone views import.
		case src == toolRuntimeSrc:
			t.raw = []byte(ourRuntime)
			t.attrs = nil
			dropThrough(toks, i, "script")

			sawSupport = true

		// 2 — the namespace bundle is a no-op stub. Drop the whitespace in front of it too, so the
		// line it occupied does not survive as a blank one.
		case dsBundle.MatchString(src):
			t.dropped = true

			dropThrough(toks, i, "script")
			trimTrailingSpaceBefore(toks, i)

		// 4 — let the authored logic run as a classic script, so the runtime needs no evaluator.
		default:
			if _, ok := t.attr("data-dc-script"); ok {
				sawDCScript = true

				if v, ok := t.attr("type"); ok && v == dcScriptType {
					t.delAttr("type")
				}
			}
		}
	}

	if !sawSupport {
		return fmt.Errorf("no <script src=%q> — this is not a design-tool export, or the export changed", toolRuntimeSrc)
	}

	if !sawDCScript {
		return fmt.Errorf("no <script data-dc-script> block — the authored component logic is missing")
	}

	return nil
}

// dropThrough marks every token after start up to and including the matching end tag as dropped.
// The start token itself is left to the caller, which either drops or replaces it.
func dropThrough(toks []token, start int, name string) {
	for j := start + 1; j < len(toks); j++ {
		toks[j].dropped = true

		if toks[j].typ == html.EndTagToken && toks[j].name == name {
			return
		}
	}
}

// trimTrailingSpaceBefore removes the whitespace run that immediately preceded a dropped token.
func trimTrailingSpaceBefore(toks []token, i int) {
	if i == 0 || toks[i-1].typ != html.TextToken {
		return
	}

	prev := &toks[i-1]
	trimmed := bytes.TrimRight(prev.raw, " \t\r\n")

	if len(bytes.TrimSpace(prev.raw)) == 0 {
		// An all-whitespace run: the Python's `\s*` prefix consumed it entirely.
		prev.raw = nil

		return
	}

	prev.raw = trimmed
}

// liftDirectives rewrites <sc-for>/<sc-if>, and an <x-import> in table context, onto their single
// child element, innermost blocks first, and returns how many were lifted.
//
// Innermost first, because whether a block has one child is a question about the block AFTER its own
// nested directives have collapsed: <sc-if><sc-for>…</sc-for></sc-if> has one child before lifting
// and however many the <sc-for> wrapped after it.
//
// inTable is indexed by token, and is what confines the <x-import> lift to the one place the element
// form cannot survive; see liftOnto.
func liftDirectives(toks []token, lo, hi int, inTable []bool) (int, error) {
	lifted := 0

	for i := lo; i < hi; i++ {
		t := toks[i]
		if t.typ != html.StartTagToken || !t.isBlock() {
			continue
		}

		end := matchBlock(toks, i, hi)
		if end < 0 {
			// Unbalanced. Leave the tag alone rather than corrupt the document; the table-context
			// assertion below still refuses to publish it if it sits anywhere dangerous.
			continue
		}

		inner, err := liftDirectives(toks, i+1, end, inTable)
		if err != nil {
			return 0, err
		}

		lifted += inner

		ok, err := liftOnto(toks, i, end, inTable[i])
		if err != nil {
			return 0, err
		}

		if ok {
			lifted++
		}

		i = end
	}

	return lifted, nil
}

// matchBlock returns the index of the end tag closing the block that opens at start, or -1 if there
// is none inside [start, hi).
func matchBlock(toks []token, start, hi int) int {
	depth := 0

	for i := start; i < hi; i++ {
		switch {
		case toks[i].typ == html.StartTagToken && toks[i].isBlock():
			depth++
		case toks[i].typ == html.EndTagToken && toks[i].isBlock():
			depth--
			if depth == 0 {
				if toks[i].name != toks[start].name {
					return -1 // </sc-if> closing an <sc-for>: mismatched, so not a block we touch
				}

				return i
			}
		}
	}

	return -1
}

// inTableContext reports, per token index, whether that token sits inside an element from which the
// HTML5 tree construction algorithm foster-parents an unknown child.
//
// The same element stack assertNoDirectiveInTable walks, computed once up front: the lift needs the
// answer BEFORE it starts dropping tokens, and an index into this slice stays valid afterwards
// because tokens are only ever marked dropped, never removed.
func inTableContext(toks []token) []bool {
	out := make([]bool, len(toks))

	var stack []string

	for i, t := range toks {
		if t.dropped || !t.isTag() {
			continue
		}

		if t.typ == html.EndTagToken {
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j] == t.name {
					stack = stack[:j]

					break
				}
			}

			continue
		}

		for _, open := range stack {
			if tableContext[open] {
				out[i] = true

				break
			}
		}

		if t.opensLevel() {
			stack = append(stack, t.name)
		}
	}

	return out
}

// liftOnto moves the block opening at start onto its single element child, if it has exactly one and
// that child is not itself a block. It reports whether it lifted.
func liftOnto(toks []token, start, end int, inTable bool) (bool, error) {
	d := toks[start]

	// An <x-import> is lifted ONLY in table context, and that restraint is the point rather than
	// caution. Its element form is what the mockups are authored in and what the runtime has always
	// rendered — including the two IOSDevice frames in guild-portal.dc.html, each of which is a
	// single-child block and would otherwise be rewritten for no reason. Inside a <table> the element
	// form is not a style choice: it is foster-parented out and arrives empty, and there the
	// attribute form is the only shape that survives the parser. Everywhere else, leave it alone.
	if d.name == tagXImport && !inTable {
		return false, nil
	}

	kids, text := topLevelChildren(toks, start+1, end)

	// Exactly one element child and nothing else. `text` guards a case the regex implementation got
	// wrong: <sc-if>Some copy<b>x</b></sc-if> counted one child, lifted onto the <b>, and left the
	// copy behind unconditionally — rendering text that was supposed to be hidden. Several siblings,
	// or one sibling plus loose text, keep the element form.
	//
	// The same condition is what makes the <x-import> lift faithful rather than merely convenient:
	// the runtime hands a component ALL of the element's children as a fragment, so moving the import
	// onto one child preserves the fragment exactly when that child is the only thing in it.
	if len(kids) != 1 || text {
		return false, nil
	}

	child := &toks[kids[0]]

	// Never lift onto another of these elements: <sc-if><sc-for>…</sc-for></sc-if> would put
	// data-sc-if on the <sc-for>, and mockup-runtime.js's element-form branch returns before it
	// looks at data-* attributes — silently dropping the condition. The same is true of an
	// <x-import> child, whose element-form branch returns before the attribute form is read. Keep
	// the outer element form.
	if child.isBlock() {
		return false, nil
	}

	switch d.name {
	case tagSCFor:
		list, _ := d.attr("list")

		as, ok := d.attr("as")
		if !ok || as == "" {
			as = "item"
		}

		child.addAttrs(
			html.Attribute{Key: "data-sc-for", Val: list},
			html.Attribute{Key: "data-sc-as", Val: as},
		)
	case tagSCIf:
		value, _ := d.attr("value")
		child.addAttrs(html.Attribute{Key: "data-sc-if", Val: value})
	case tagXImport:
		child.addAttrs(importAttrs(d)...)
	default:
		return false, fmt.Errorf("liftOnto: %q is not a liftable element", d.name)
	}

	toks[start].dropped = true
	toks[end].dropped = true

	return true, nil
}

// importAttrs renders an <x-import> as the attributes its lifted form carries: the component name,
// then one data-sc-prop-* attribute per prop, in source order.
//
// `from` is dropped with the component name because the runtime ignores it — it names the design
// tool's own module path, which nothing here loads. Everything else is passed through untouched,
// including the authoring hints: what counts as a prop is mockup-runtime.js's decision in ONE place,
// and a lift that also filtered would be a second place to keep in step with it.
func importAttrs(d token) []html.Attribute {
	attrs := make([]html.Attribute, 0, len(d.attrs))

	name, _ := d.attr(attrImportComponent)
	attrs = append(attrs, html.Attribute{Key: attrImport, Val: name})

	for _, a := range d.attrs {
		if a.Key == attrImportComponent || a.Key == attrImportFrom {
			continue
		}

		attrs = append(attrs, html.Attribute{Key: attrImportProp + a.Key, Val: a.Val})
	}

	return attrs
}

// topLevelChildren returns the indices of the start tags of the immediate element children of
// [lo, hi), and whether any non-whitespace text sits directly between them.
//
// The two things the regex implementation had to hand-write are now the tokenizer's answers: a
// self-closing tag opens no level (miss that and every sibling after an `<x/>` is counted one level
// too deep, so a multi-child block looks like a single-child one), and a void element has no end
// tag. Comments and the doctype are not children.
func topLevelChildren(toks []token, lo, hi int) ([]int, bool) {
	var (
		kids  []int
		text  bool
		depth int
	)

	for i := lo; i < hi; i++ {
		t := toks[i]
		if t.dropped {
			continue
		}

		switch t.typ {
		case html.TextToken:
			if depth == 0 && strings.TrimSpace(textOf(t)) != "" {
				text = true
			}
		case html.EndTagToken:
			if depth > 0 {
				depth--
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			if depth == 0 {
				kids = append(kids, i)
			}

			if t.opensLevel() {
				depth++
			}
		}
	}

	return kids, text
}

// assertNoDirectiveInTable refuses to publish a page where an <sc-for>/<sc-if> element survives
// inside table context.
//
// This is the whole reason liftDirectives exists. It walks the token stream keeping a stack of open
// elements and fails the moment a directive opens while a table element is anywhere below it,
// naming the enclosing element and the line — which is what a person needs in order to fix the
// mockup. assertRowsSurviveParsing then proves the same property from the other end, by asking a
// real tree builder.
func assertNoDirectiveInTable(toks []token, name string) error {
	var stack []string

	for _, t := range toks {
		if t.dropped || !t.isTag() {
			continue
		}

		if t.typ == html.EndTagToken {
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == t.name {
					stack = stack[:i]

					break
				}
			}

			continue
		}

		if t.typ == html.StartTagToken && t.isDirective() {
			for i := len(stack) - 1; i >= 0; i-- {
				if tableContext[stack[i]] {
					return fmt.Errorf(
						"%s:%d: <%s> survives inside <%s> — the HTML parser will foster-parent it out of "+
							"the table and drop the rows it contains. It needs to be lifted onto its child element",
						name, t.line, t.name, stack[i])
				}
			}
		}

		if t.opensLevel() {
			stack = append(stack, t.name)
		}
	}

	return nil
}

// customElementNames are the elements the mockups invent and mockup-runtime.js gives meaning to.
// Every one is an unknown element to a tree builder, which is exactly why one inside a <table> is
// foster-parented out of it — and, crucially, arrives at its new position EMPTY.
//
// A sorted slice, and the iteration order of the check below, so one broken page always produces the
// same failure message. Ranging a map here meant the message named whichever element Go's randomised
// map order reached first, which for a foster-parented <x-import> was as often the <x-dc> that
// RECEIVED it as the <x-import> that lost it.
var customElementNames = []string{"helmet", tagSCFor, tagSCIf, "x-dc", "x-import"}

var customElements = func() map[string]bool {
	m := make(map[string]bool, len(customElementNames))
	for _, n := range customElementNames {
		m[n] = true
	}

	return m
}()

// assertCustomElementsKeepTheirChildren parses the finished page with the real HTML5 tree
// construction algorithm and fails if it moved anything out of one of the mockups' own elements.
//
// This is the assertion the Python could not make, and it is worth being precise about what the
// failure looks like, because the obvious formulation does not work. Foster parenting does not
// DELETE the rows: `<table><tbody><sc-for><tr>…</tr></sc-for></tbody></table>` parses to an empty
// <sc-for> hoisted in front of the table and a <tr> still inside the tbody. Count <tr> before and
// after and nothing is missing. What is missing is the RELATIONSHIP — the runtime repeats a
// directive's children, and the directive now has none, so the loop renders nothing and the table
// shows one static row. That is how 37 tables came out empty.
//
// So the property checked is the relationship: every custom element must have, after parsing, the
// same number of element children the markup gave it. The scripts/build-mockup-site.sh version
// checked a PROXY of this — that no directive remains in table context — which covers the cause we
// know about. This covers the failure itself, including through <x-import> and <helmet>, which the
// lift does not touch and the proxy check never looked at.
func assertCustomElementsKeepTheirChildren(name string, out []byte) error {
	toks, err := tokenize(out)
	if err != nil {
		return fmt.Errorf("%s: re-tokenize published page: %w", name, err)
	}

	doc, err := html.Parse(bytes.NewReader(out))
	if err != nil {
		return fmt.Errorf("%s: the published page does not parse as HTML5: %w", name, err)
	}

	written := markupChildCounts(toks)

	parsed := map[string]int{}
	parsedChildCounts(doc, parsed)

	// Losses first, gains second. Foster parenting moves children rather than deleting them, so one
	// emptied element is always paired with another that gained what it lost — and the element that
	// LOST them is the one a person has to go and fix. Reporting the receiver first sends them to the
	// wrong end of the document.
	var lost, gained []string

	for _, el := range customElementNames {
		switch {
		case parsed[el] < written[el]:
			lost = append(lost, fmt.Sprintf(
				"  <%s> lost %d: the markup gives them %d element children, the parse finds %d",
				el, written[el]-parsed[el], written[el], parsed[el]))
		case parsed[el] > written[el]:
			gained = append(gained, fmt.Sprintf(
				"  <%s> gained %d, which is where they went",
				el, parsed[el]-written[el]))
		}
	}

	if len(lost) == 0 && len(gained) == 0 {
		return nil
	}

	return fmt.Errorf(
		"%s: an HTML5 parse of the published page does not give the mockups' own elements the children "+
			"the markup gives them:\n%s\n"+
			"That is foster-parenting: an element that is not valid table content is hoisted out of a "+
			"<table> and arrives EMPTY, because its rows stay behind. mockup-runtime.js repeats and "+
			"conditions an element's CHILDREN, so an emptied one renders nothing.\n"+
			"THE FIX IS IN THE HARNESS, NOT IN THE MOCKUP. The vendored .dc.html files are byte-exact "+
			"against the design tool's export and are never edited (docs/design/mockups/README.md). A "+
			"single-child <sc-for>, <sc-if> or <x-import> is already lifted onto its child by liftOnto "+
			"in internal/mockup/publish.go; a block this build refuses is one that has no attribute "+
			"form to lift onto, so give it one in docs/design/mockups/harness/mockup-runtime.js and add "+
			"the case beside the others there",
		name, strings.Join(append(lost, gained...), "\n"))
}

// markupChildCounts totals, per custom element, the element children the markup gives it — read off
// the token stream with an element stack, which is what the markup says regardless of what a parser
// would do with it.
func markupChildCounts(toks []token) map[string]int {
	counts := map[string]int{}

	var stack []string

	for _, t := range toks {
		if t.dropped || !t.isTag() {
			continue
		}

		if t.typ == html.EndTagToken {
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == t.name {
					stack = stack[:i]

					break
				}
			}

			continue
		}

		if len(stack) > 0 && customElements[stack[len(stack)-1]] {
			counts[stack[len(stack)-1]]++
		}

		if t.opensLevel() {
			stack = append(stack, t.name)
		}
	}

	return counts
}

// parsedChildCounts totals, per custom element, the element children it actually has in the
// document a browser would build.
func parsedChildCounts(n *html.Node, into map[string]int) {
	if n.Type == html.ElementNode && customElements[n.Data] {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				into[n.Data]++
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		parsedChildCounts(c, into)
	}
}
