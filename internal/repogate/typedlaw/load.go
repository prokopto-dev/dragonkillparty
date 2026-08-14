package typedlaw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// tree is what every package of one run shares: where the module is, and the module's own import
// graph.
//
// The graph is restricted to the MAIN MODULE's packages, and that restriction is the difference
// between PURE001/PURE002 being useful and being noise. Walked over the full closure, PURE002 fires
// on this repository today: internal/strategy imports internal/core, which imports
// github.com/oklog/ulid/v2, which imports math/rand. That is a dependency's implementation detail,
// not a strategy reaching for unseeded dice — internal/core draws its own entropy from crypto/rand
// — and a pass that reported it every run would be a pass people learn to skim. The escape these
// laws exist to catch is OURS: a helper package under internal/ that rolls a die, imported by a
// strategy. A third-party module's internals are governed by the dependency review, the licence
// gate and ADR001, which is a different mechanism with a human in it.
type tree struct {
	// modulePath and moduleDir come from `go list`, never from a constant: the negative fixtures
	// are fabricated modules (example.com/tainted), and a rule that hardcoded this repository's
	// module path would pass vacuously in exactly the tree a test points it at.
	modulePath string
	moduleDir  string

	// imports maps each main-module package to its DIRECT imports, third-party and standard
	// library included. The walk below is transitive through the keys and terminal at the values.
	imports map[string][]string
}

// pkg is one type-checked package of the module under inspection, plus everything a rule needs to
// report a finding against a file the reader can open.
type pkg struct {
	// path is the package's import path, and dir its absolute directory.
	path string
	dir  string

	// rel is the module-relative directory — "internal/strategy" — which is what every rule's tree
	// scoping and every report line is expressed in.
	rel string

	t *tree

	fset  *token.FileSet
	files []*ast.File
	info  *types.Info
	types *types.Package

	// lines caches the source of each parsed file, so a finding can quote the offending line the
	// way internal/repogate's report does.
	lines map[string][]string
}

// listPkg is the subset of `go list -json` this package reads.
type listPkg struct {
	ImportPath string
	Dir        string
	Export     string
	GoFiles    []string
	Imports    []string
	Standard   bool
	Module     *struct {
		Path string
		Main bool
		Dir  string
	}
}

// load builds the tree at root and type-checks every package of the main module.
//
// EVERY failure here is a hard failure, deliberately and in both modes. This pass exists to say
// something about code the compiler has agreed is code; if the build did not happen, or a package
// did not type-check, the honest report is "the pass could not run", never "no findings". That is
// the rule scripts/migrate-lint.sh states for atlas and `make govulncheck` states for its binary,
// and it is the whole difference between an advisory and a green check that checked nothing.
func load(root string) ([]*pkg, error) {
	listed, t, err := goList(root)
	if err != nil {
		return nil, err
	}

	// Export data for the WHOLE closure, this module's packages and the third-party graph alike:
	// go/types resolves an import by reading the dependency's compiled API, so a missing entry is a
	// package that cannot be checked rather than one that checks clean.
	exports := make(map[string]string, len(listed))
	for _, p := range listed {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}

	fset := token.NewFileSet()

	// The documented module-aware form: ForCompiler with a non-nil lookup, fed the export files
	// `go list -export` just wrote. A nil lookup is the deprecated GOPATH path and would resolve
	// nothing in a module.
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s — the tree did not build", path)
		}

		reader, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("open export data for %s: %w", path, err)
		}

		return reader, nil
	})

	var out []*pkg

	for _, p := range listed {
		if p.Module == nil || !p.Module.Main || len(p.GoFiles) == 0 {
			continue
		}

		checked, err := checkPackage(fset, imp, p, t)
		if err != nil {
			return nil, err
		}

		out = append(out, checked)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no packages of the main module were type-checked under %s — "+
			"a pass that read nothing must not report that it found nothing", root)
	}

	return out, nil
}

// goList runs the build-and-report step, returning every package in the closure plus the shared
// tree: where the main module is, and its own import graph.
func goList(root string) ([]listPkg, *tree, error) {
	// -deps for the closure (export data for the third-party graph is what the importer reads),
	// -export to build it and report where each compiled API landed. No -e: a package that does not
	// load is a broken invocation, and the error text `go list` writes to stderr is better than
	// anything this could synthesise from an Error field.
	cmd := exec.Command("go", "list", "-json", "-deps", "-export", "./...")
	cmd.Dir = root

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	stdout, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("go list -deps -export ./... in %s: %w\n%s", root, err, stderr.String())
	}

	var listed []listPkg

	t := &tree{imports: map[string][]string{}}

	dec := json.NewDecoder(bytes.NewReader(stdout))

	for {
		var p listPkg

		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("decode `go list -json` output: %w", err)
		}

		if p.Module != nil && p.Module.Main {
			if t.moduleDir == "" {
				t.modulePath, t.moduleDir = p.Module.Path, p.Module.Dir
			}

			t.imports[p.ImportPath] = p.Imports
		}

		listed = append(listed, p)
	}

	if t.moduleDir == "" {
		return nil, nil, fmt.Errorf("`go list` reported no main module under %s — "+
			"the type-aware pass needs a buildable module, which is why it is the second opinion "+
			"and internal/repogate is the gate", root)
	}

	return listed, t, nil
}

// checkPackage parses and type-checks one package of the main module.
func checkPackage(fset *token.FileSet, imp types.Importer, p listPkg, t *tree) (*pkg, error) {
	rel, err := filepath.Rel(t.moduleDir, p.Dir)
	if err != nil {
		return nil, fmt.Errorf("locate %s under %s: %w", p.ImportPath, t.moduleDir, err)
	}

	out := &pkg{
		path:  p.ImportPath,
		dir:   p.Dir,
		rel:   filepath.ToSlash(rel),
		t:     t,
		fset:  fset,
		lines: map[string][]string{},
	}

	for _, name := range p.GoFiles {
		abs := filepath.Join(p.Dir, name)

		src, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", abs, err)
		}

		file, err := parser.ParseFile(fset, abs, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", abs, err)
		}

		out.files = append(out.files, file)
		out.lines[abs] = strings.Split(string(src), "\n")
	}

	out.info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}

	conf := types.Config{Importer: imp}

	tpkg, err := conf.Check(p.ImportPath, fset, out.files, out.info)
	if err != nil {
		return nil, fmt.Errorf("type-check %s: %w", p.ImportPath, err)
	}

	out.types = tpkg

	return out, nil
}

// pos renders a node's position as the module-relative "path:line:col" every report line uses.
func (p *pkg) pos(node ast.Node) token.Position {
	return p.fset.Position(node.Pos())
}

// hit builds one finding line: "internal/foo/bar.go:12:3  the source line".
//
// The format mirrors internal/repogate's `hit`, down to the two spaces, so that a reader who has
// seen one report can read the other without being told which pass wrote it.
func (p *pkg) hit(node ast.Node) string {
	position := p.pos(node)

	rel := position.Filename
	if r, err := filepath.Rel(p.t.moduleDir, position.Filename); err == nil {
		rel = filepath.ToSlash(r)
	}

	return fmt.Sprintf("%s:%d:%d  %s", rel, position.Line, position.Column,
		strings.TrimSpace(p.text(position.Filename, position.Line)))
}

// text returns the source of a 1-based line, or "" when the line is out of range.
func (p *pkg) text(file string, line int) string {
	lines := p.lines[file]
	if line < 1 || line > len(lines) {
		return ""
	}

	return lines[line-1]
}

// under reports whether the package sits at or below a module-relative directory —
// `internal/store` matches internal/store and every subpackage of it.
func (p *pkg) under(dir string) bool {
	return p.rel == dir || strings.HasPrefix(p.rel, dir+"/")
}

// reaches walks the MAIN MODULE's import graph out from this package and returns the first path to
// a dependency satisfying match — the chain, not only the endpoint, because a transitive finding
// nobody can trace is a finding nobody acts on.
//
// Transitive through this module's packages and TERMINAL at everything else: see the tree type for
// why a third-party module's own imports are somebody else's mechanism.
//
// Breadth-first over a sorted frontier, so the reported chain is the shortest one and is the same
// chain on every run — `go list` orders Imports already, and a report that reshuffles between two
// runs over one tree is one nobody can diff.
func (p *pkg) reaches(match func(path string) bool) ([]string, bool) {
	type step struct {
		path  string
		chain []string
	}

	seen := map[string]bool{p.path: true}
	queue := []step{{path: p.path, chain: []string{p.path}}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, imported := range p.t.imports[current.path] {
			chain := append(append([]string{}, current.chain...), imported)

			if match(imported) {
				return chain, true
			}

			if _, ours := p.t.imports[imported]; !ours || seen[imported] {
				continue
			}

			seen[imported] = true
			queue = append(queue, step{path: imported, chain: chain})
		}
	}

	return nil, false
}
