package mockup

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// The build gates. Each keeps the identifier it had in scripts/build-mockup-site.sh, because those
// identifiers are what docs/design/mockups/README.md, NOTICE and every past failure message name.
//
//	MOCK001  every binding is a plain path or a literal, so the runtime needs no expression evaluator
//	MOCK002  no third-party runtime is vendored
//	MOCK003  no published page still references the design tool's layout
//	MOCK004  every published page is noindex

// bindingRe finds a {{ … }} binding. It is the same pattern mockup-runtime.js resolves with, so what
// this gate calls a binding is what the runtime will call one.
var bindingRe = regexp.MustCompile(`\{\{(.*?)\}\}`)

// pathOrLiteral is everything the runtime can resolve: a dotted path, or one of the four literal
// forms. Deliberately nothing else.
var pathOrLiteral = regexp.MustCompile(
	`^(?:[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)*` +
		`|true|false|null|-?[0-9]+(?:\.[0-9]+)?)$`)

// CheckBindings is MOCK001: the runtime has no expression evaluator.
//
// harness/mockup-runtime.js resolves a binding as a dotted path or a literal and nothing else. That
// is safe to do without eval precisely because no mockup needs more. If a refreshed export ever
// introduces `{{ a || b }}` or `{{ f(x) }}`, the binding would silently render empty — so fail here
// instead, loudly, with the offending expressions named.
//
// Driven off the token stream rather than a regex over the file: a character class of "not }" cannot
// see `{{ a } b }}`, and a binding the gate cannot see is exactly the one that would slip through
// and render empty. Text nodes are scanned with their entity references resolved, because that is
// what the runtime's own regex runs over.
func CheckBindings(src []byte) ([]string, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	note := func(s string) {
		for _, m := range bindingRe.FindAllStringSubmatch(s, -1) {
			expr := strings.TrimSpace(m[1])
			if !pathOrLiteral.MatchString(expr) {
				seen[expr] = true
			}
		}
	}

	for _, t := range toks {
		if t.typ == html.TextToken {
			note(textOf(t))
		}

		for _, a := range t.attrs {
			note(a.Val)
		}
	}

	bad := make([]string, 0, len(seen))
	for expr := range seen {
		bad = append(bad, expr)
	}

	sort.Strings(bad)

	return bad, nil
}

// vendoredRuntimes are the design tool's runtime filenames.
//
// NOTICE states no third-party source is vendored. The design tool's runtime is unlicensed, so its
// filenames are refused outright rather than trusted to stay deleted.
var vendoredRuntimes = []string{"support.js", "ios-frame.jsx", "_ds_bundle.js"}

// CheckNoVendoredRuntime is MOCK002. It returns the repo-relative paths of any offending file.
func CheckNoVendoredRuntime(srcDir string) ([]string, error) {
	var found []string

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		for _, name := range vendoredRuntimes {
			if d.Name() == name {
				found = append(found, path)
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s for vendored runtimes: %w", srcDir, err)
	}

	sort.Strings(found)

	return found, nil
}

// staleRefs are the markers of the design tool's layout that must not survive the rewrite.
var staleRefs = []string{"_ds/", "support.js", "text/x-dc"}

// StaleRef is one surviving reference to the design tool's runtime, with the line it is on.
type StaleRef struct {
	Marker string
	Line   int
	Text   string
}

// CheckNoStaleRefs is MOCK003: the rewrites must leave nothing pointing at the design tool's layout.
//
// A byte scan of the finished page, on purpose, and the one gate here that is not a parse. The
// property is "this string appears nowhere in what we publish" — inside an attribute, inside the
// authored JavaScript, inside a comment, anywhere. Narrowing it to parsed attribute values would
// make it a weaker gate that happened to use a better tool.
func CheckNoStaleRefs(page []byte) []StaleRef {
	var out []StaleRef

	for n, line := range strings.Split(string(page), "\n") {
		for _, marker := range staleRefs {
			if strings.Contains(line, marker) {
				out = append(out, StaleRef{Marker: marker, Line: n + 1, Text: strings.TrimSpace(line)})
			}
		}
	}

	return out
}

// CheckNoindex is MOCK004: every published page carries a noindex robots meta.
//
// Checked over the OUTPUT, not the source: index.html carries the tag by hand while the surfaces get
// it from injectChrome, and this has to hold no matter which path produced the file.
//
// Parsed rather than grepped. The shell gate matched
// `<meta[^>]*name="robots"[^>]*content="[^"]*noindex` over the file, which cannot see a single-quoted
// or unquoted attribute, cannot see the attributes in the other order without the two `[^>]*` runs
// happening to line up, and would accept the tag inside a comment or inside the authored script's
// string literals. html.Parse answers the question the gate is actually asking: does the document a
// crawler builds contain this meta element?
func CheckNoindex(page []byte) (bool, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return false, fmt.Errorf("parse published page: %w", err)
	}

	return hasNoindexMeta(doc), nil
}

func hasNoindexMeta(n *html.Node) bool {
	if n.Type == html.ElementNode && n.Data == "meta" {
		name, content := "", ""

		for _, a := range n.Attr {
			switch a.Key {
			case "name":
				name = strings.ToLower(strings.TrimSpace(a.Val))
			case "content":
				content = a.Val
			}
		}

		if name == "robots" {
			for _, directive := range strings.Split(content, ",") {
				if strings.EqualFold(strings.TrimSpace(directive), "noindex") {
					return true
				}
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasNoindexMeta(c) {
			return true
		}
	}

	return false
}

// readFile is a thin wrapper that keeps the error message pointing at the file rather than at a
// bare syscall.
func readFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return b, nil
}
