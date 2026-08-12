package mockup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// token is one HTML5 token together with the exact source bytes it was read from.
//
// raw is what gets written back out. Every token this package does not deliberately change is
// emitted byte for byte, so publishing is a surgical edit of the vendored file rather than a
// re-serialisation of it — a re-serialised 330 KB mockup would diff against a fresh export on every
// line, and readable diffs are the whole reason the .dc.html files are vendored byte-exact.
//
// dropped tokens are omitted from the output. That is how a lifted <sc-for> disappears without
// disturbing anything around it.
type token struct {
	typ     html.TokenType
	name    string // lowercased tag name; empty for text, comments and the doctype
	attrs   []html.Attribute
	raw     []byte
	line    int // 1-based line of the token's first byte, for error messages
	dropped bool
}

// isTag reports whether t opens or closes an element.
func (t token) isTag() bool {
	return t.typ == html.StartTagToken || t.typ == html.EndTagToken || t.typ == html.SelfClosingTagToken
}

// attr returns the value of the named attribute, and whether it was present.
func (t token) attr(key string) (string, bool) {
	for _, a := range t.attrs {
		if a.Key == key {
			return a.Val, true
		}
	}

	return "", false
}

// addAttrs appends attributes by splicing them into the tag's existing source bytes, immediately
// before its closing `>`.
//
// A splice rather than a re-render, so that adding data-sc-for to a tag leaves every byte the author
// wrote untouched. It matters more than tidiness: the tokenizer reports attribute names lowercased,
// and the mockups author their handlers as onClick/onChange/onScroll. Re-rendering a lifted <button>
// would silently rewrite those — harmlessly, since the browser lowercases them too, but it would put
// a spurious change on every one of the 267 lifted tags and bury the real diff.
//
// Finding the `>` is the tokenizer's job, not a scan: raw is exactly the tag, and whether a trailing
// `/` is a self-closing marker or the last character of an unquoted value is already decided, by the
// token's type. That decision — `<div data-x=a/>` is not self-closing — is one a hand-written
// scanner has to get right and this one cannot get wrong.
func (t *token) addAttrs(attrs ...html.Attribute) {
	at := len(t.raw) - 1
	if t.typ == html.SelfClosingTagToken {
		at--
	}

	var b bytes.Buffer

	for _, a := range attrs {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteString(`="`)
		b.WriteString(escapeAttr(a.Val))
		b.WriteByte('"')
	}

	spliced := make([]byte, 0, len(t.raw)+b.Len())
	spliced = append(spliced, t.raw[:at]...)
	spliced = append(spliced, b.Bytes()...)
	spliced = append(spliced, t.raw[at:]...)

	t.raw = spliced
	t.attrs = append(t.attrs, attrs...)
}

// delAttr removes an attribute if present and re-renders the token's raw bytes.
func (t *token) delAttr(key string) {
	kept := t.attrs[:0]

	for _, a := range t.attrs {
		if a.Key != key {
			kept = append(kept, a)
		}
	}

	t.attrs = kept
	t.render()
}

// render rewrites raw from name and attrs.
//
// Used only where an attribute is REMOVED or its value REPLACED, which cannot be spliced; adding one
// goes through addAttrs and leaves the source bytes alone. So the normalisation this applies —
// lowercased tag and attribute names, every value double-quoted — lands on two tags per page, not on
// all of them.
//
// A valueless attribute is written back valueless. HTML5 treats `data-dc-script` and
// `data-dc-script=""` identically, but only one of them is what the file already said.
func (t *token) render() {
	var b bytes.Buffer

	b.WriteByte('<')

	if t.typ == html.EndTagToken {
		b.WriteByte('/')
	}

	b.WriteString(t.name)

	for _, a := range t.attrs {
		b.WriteByte(' ')

		if a.Namespace != "" {
			b.WriteString(a.Namespace)
			b.WriteByte(':')
		}

		b.WriteString(a.Key)

		if a.Val == "" {
			continue
		}

		b.WriteString(`="`)
		b.WriteString(escapeAttr(a.Val))
		b.WriteByte('"')
	}

	if t.typ == html.SelfClosingTagToken {
		b.WriteByte('/')
	}

	b.WriteByte('>')

	t.raw = b.Bytes()
}

// attrEscaper rewrites the four characters that cannot appear literally in a double-quoted attribute
// value, using the named references the mockups are already written with.
//
// html.EscapeString is not used here for one reason: it spells `"` as `&#34;`, so re-rendering a tag
// whose value contains the `&quot;`-heavy data-props JSON would rewrite every one of them to a
// numeric reference. Identical to a browser, noise to a reviewer. `'` needs no escape inside double
// quotes.
var attrEscaper = strings.NewReplacer(
	"&", "&amp;",
	`"`, "&quot;",
	"<", "&lt;",
	">", "&gt;",
)

func escapeAttr(s string) string { return attrEscaper.Replace(s) }

// tokenize runs the HTML5 tokenizer over src.
//
// This is the whole point of the package. The tokenizer is a real implementation of the WHATWG
// tokenization algorithm: it knows that a `>` inside a quoted attribute value does not end the tag,
// that `<script>`, `<style>`, `<title>` and `<textarea>` hold raw text in which `<div>` is not a
// tag, and that `<br/>` is self-closing while `<br>` is void. Every one of those was a hand-written
// regex or a character scan in the Python this replaces, and the two the comments there flagged as
// near-misses — a self-closing tag opening a nesting level, and an attribute containing `>`
// swallowing the document — are not expressible here.
func tokenize(src []byte) ([]token, error) {
	z := html.NewTokenizer(bytes.NewReader(src))

	var out []token

	line := 1

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if err := z.Err(); !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("tokenize: %w", err)
			}

			return out, nil
		}

		// Raw() must be copied before Token(), which unescapes in place over the same buffer.
		raw := bytes.Clone(z.Raw())
		t := token{typ: tt, raw: raw, line: line}
		line += bytes.Count(raw, []byte("\n"))

		if t.isTag() {
			parsed := z.Token()
			t.name = parsed.Data
			t.attrs = parsed.Attr
		}

		out = append(out, t)
	}
}

// emit concatenates every token that has not been dropped.
func emit(toks []token) []byte {
	var b bytes.Buffer

	for _, t := range toks {
		if !t.dropped {
			b.Write(t.raw)
		}
	}

	return b.Bytes()
}

// voidElements is the HTML5 set of elements that never have an end tag. It is spec data, not
// parsing: the tokenizer reports `<br>` as a start tag, and only the caller knows it opens no level.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
}

// opensLevel reports whether t pushes a nesting level: a start tag for a non-void element.
func (t token) opensLevel() bool {
	return t.typ == html.StartTagToken && !voidElements[t.name]
}

// isDirective reports whether t is one of the design tool's <sc-for>/<sc-if> template elements.
func (t token) isDirective() bool {
	return t.name == tagSCFor || t.name == tagSCIf
}

const (
	tagSCFor = "sc-for"
	tagSCIf  = "sc-if"
)

// tableContext is the set of elements inside which an unknown element is foster-parented out of the
// table by the HTML5 tree construction algorithm, taking its children with it.
var tableContext = map[string]bool{
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true,
}

// textOf returns a text token as the runtime will see it: entity references resolved.
//
// Resolved, not raw, because that is what the binding scan has to look at. mockup-runtime.js runs
// its BINDING regex over `node.nodeValue` — the parsed text node — so `&#123;&#123; a || b }}` is a
// binding to the runtime even though the source bytes contain no `{`. Matching raw bytes, as the
// regex implementation did, would let exactly that spelling through the gate.
func textOf(t token) string {
	return html.UnescapeString(string(t.raw))
}
