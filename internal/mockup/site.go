// Package mockup assembles the publishable UI-mockup site from docs/design/mockups/.
//
// The vendored .dc.html files stay byte-exact — they are the design reference, and a diff against a
// fresh export has to be readable. Every adjustment needed to publish them happens here, on copies.
// See docs/design/mockups/README.md.
//
// It lives under internal/ rather than in cmd/dkp for the reason internal/ledger/enumgen gives:
// cmd/dkp is the product binary, and an officer never publishes a design reference. `make
// mockup-site` runs it; .github/workflows/pages.yml deploys what it writes.
//
// It imports the standard library and golang.org/x/net/html, and nothing else from this repository.
package mockup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// surfaceTitles maps a mockup file to the surface name shown in its banner. A file not listed here
// still publishes, under a generic title — the map is presentation, not a gate.
var surfaceTitles = map[string]string{
	"admin-console.dc.html": "Admin console",
	"guild-portal.dc.html":  "Guild portal",
	"my-characters.dc.html": "My characters",
	"public-site.dc.html":   "Public site",
	"first-run.dc.html":     "First run",
}

// titleFor returns the banner title for a mockup file name.
func titleFor(name string) string {
	if t, ok := surfaceTitles[name]; ok {
		return t
	}

	return "Mockup"
}

// copies are the files published verbatim, as source path relative to the mockups directory mapped
// to destination path relative to the output directory.
var copies = [][2]string{
	{"nocturne/styles.css", "nocturne/styles.css"},
	{"harness/mockup-runtime.js", "mockup-runtime.js"},
	{"harness/ios-frame.js", "ios-frame.js"},
	{"index.html", "index.html"},
}

// progress carries the build's own commentary and remembers the first write failure.
//
// A type rather than six `_ = fmt.Fprintf(...)` waivers. AGENTS.md reserves the blank waiver for a
// call whose failure there is nothing to do about, and this is not one: the lines below are how a
// person knows which gates ran, so a build whose output went nowhere should fail rather than look
// like a build that passed.
type progress struct {
	w   io.Writer
	err error
}

func (p *progress) printf(format string, args ...any) {
	if p.err != nil {
		return
	}

	if _, err := fmt.Fprintf(p.w, format, args...); err != nil {
		p.err = fmt.Errorf("write progress: %w", err)
	}
}

// Build assembles the mockup site from srcDir into outDir, running every gate, and reports progress
// to w. outDir is removed and rebuilt.
func Build(srcDir, outDir string, w io.Writer) error {
	if fi, err := os.Stat(srcDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotFound, srcDir)
	}

	p := &progress{w: w}

	if err := gateBindings(srcDir, p); err != nil {
		return err
	}

	if err := gateVendoredRuntime(srcDir, p); err != nil {
		return err
	}

	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clear %s: %w", outDir, err)
	}

	for _, c := range copies {
		if err := copyFile(filepath.Join(srcDir, c[0]), filepath.Join(outDir, c[1])); err != nil {
			return err
		}
	}

	sources, err := filepath.Glob(filepath.Join(srcDir, "*.dc.html"))
	if err != nil {
		return fmt.Errorf("glob %s/*.dc.html: %w", srcDir, err)
	}

	sort.Strings(sources)

	// A gate that runs over an empty set is a green build that published nothing. Every assertion
	// below is per page, so the count is the only thing that can say the set was not empty.
	if len(sources) == 0 {
		return fmt.Errorf("no *.dc.html in %s — nothing to publish, so nothing was gated", srcDir)
	}

	for _, src := range sources {
		name := filepath.Base(src)

		body, err := readFile(src)
		if err != nil {
			return err
		}

		page, err := Publish(name, body, titleFor(name))
		if err != nil {
			return err
		}

		if err := writeFile(filepath.Join(outDir, name), page.HTML); err != nil {
			return err
		}

		p.printf("  built %-24s %-15s (%d blocks lifted)\n", name, "("+page.Title+")", page.Lifted)
	}

	if err := gateStaleRefs(outDir, p); err != nil {
		return err
	}

	if err := gateNoindex(outDir, p); err != nil {
		return err
	}

	p.printf("mockup site ready: %s\n", outDir)

	return p.err
}

// gateBindings is MOCK001, over every source mockup.
func gateBindings(srcDir string, p *progress) error {
	sources, err := filepath.Glob(filepath.Join(srcDir, "*.dc.html"))
	if err != nil {
		return fmt.Errorf("glob %s/*.dc.html: %w", srcDir, err)
	}

	sort.Strings(sources)

	seen := map[string]bool{}

	for _, src := range sources {
		body, err := readFile(src)
		if err != nil {
			return err
		}

		bad, err := CheckBindings(body)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(src), err)
		}

		for _, expr := range bad {
			seen[expr] = true
		}
	}

	if len(seen) > 0 {
		exprs := make([]string, 0, len(seen))
		for expr := range seen {
			exprs = append(exprs, "  "+expr)
		}

		sort.Strings(exprs)

		return fmt.Errorf(
			"[MOCK001] binding(s) the runtime cannot resolve without an expression evaluator:\n%s\n"+
				"Compute it in renderVals(), or extend docs/design/mockups/harness/mockup-runtime.js.\n"+
				"Do not let a binding render empty",
			strings.Join(exprs, "\n"))
	}

	p.printf("  [MOCK001] all bindings are plain paths or literals\n")

	return nil
}

// gateVendoredRuntime is MOCK002.
func gateVendoredRuntime(srcDir string, p *progress) error {
	found, err := CheckNoVendoredRuntime(srcDir)
	if err != nil {
		return err
	}

	if len(found) > 0 {
		return fmt.Errorf(
			"[MOCK002] %s is the design tool's unlicensed runtime and must not be vendored.\n"+
				"See NOTICE and docs/design/mockups/README.md. Use harness/mockup-runtime.js instead",
			filepath.Base(found[0]))
	}

	p.printf("  [MOCK002] no third-party runtime vendored\n")

	return nil
}

// gateStaleRefs is MOCK003, over every published surface.
func gateStaleRefs(outDir string, p *progress) error {
	pages, err := filepath.Glob(filepath.Join(outDir, "*.dc.html"))
	if err != nil {
		return fmt.Errorf("glob %s/*.dc.html: %w", outDir, err)
	}

	sort.Strings(pages)

	for _, page := range pages {
		body, err := readFile(page)
		if err != nil {
			return err
		}

		refs := CheckNoStaleRefs(body)
		if len(refs) == 0 {
			continue
		}

		var b strings.Builder

		for i, r := range refs {
			if i == 5 {
				break
			}

			fmt.Fprintf(&b, "  %d: %s\n", r.Line, r.Text)
		}

		return fmt.Errorf(
			"[MOCK003] %s still references the design tool's runtime after rewriting:\n%s",
			filepath.Base(page), b.String())
	}

	p.printf("  [MOCK003] %d surfaces rewritten, no stale runtime references\n", len(pages))

	return nil
}

// gateNoindex is MOCK004, over every published page — the surfaces and the hand-written index.
func gateNoindex(outDir string, p *progress) error {
	pages, err := filepath.Glob(filepath.Join(outDir, "*.html"))
	if err != nil {
		return fmt.Errorf("glob %s/*.html: %w", outDir, err)
	}

	sort.Strings(pages)

	var missing []string

	for _, page := range pages {
		body, err := readFile(page)
		if err != nil {
			return err
		}

		ok, err := CheckNoindex(body)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(page), err)
		}

		if !ok {
			missing = append(missing, filepath.Base(page))
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf(
			"[MOCK004] page(s) published without a noindex robots meta:\n  %s\n"+
				"Surfaces get it from internal/mockup's injectChrome; index.html carries it inline",
			strings.Join(missing, " "))
	}

	p.printf("  [MOCK004] every published page is noindex\n")

	return nil
}

func copyFile(src, dest string) error {
	body, err := readFile(src)
	if err != nil {
		return err
	}

	return writeFile(dest, body)
}

func writeFile(dest string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}

	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}

	return nil
}

// ErrNotFound is returned when the mockups directory does not exist.
var ErrNotFound = errors.New("mockups directory not found")
