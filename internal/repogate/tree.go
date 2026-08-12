package repogate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scanner reads the tree under inspection. One instance serves a whole run, so a file that several
// rules scan — internal/**/*.go is read by four of them — is read from disk once.
//
// root is the tree being INSPECTED, which for a negative fixture is a t.TempDir() with nothing else
// in it. Every path this type returns is repo-root-relative with forward slashes, because that is
// what a failure has to quote: an absolute temp path tells a reader nothing, and several fixtures
// assert the absence of one.
type scanner struct {
	root   string
	files  map[string][]string // repo-relative path -> lines
	parsed map[string]*goFile  // repo-relative path -> parsed Go file, nil when it does not parse
}

func newScanner(root string) *scanner {
	return &scanner{
		root:   root,
		files:  make(map[string][]string),
		parsed: make(map[string]*goFile),
	}
}

// abs resolves a repo-relative slash path against the tree under inspection.
func (s *scanner) abs(rel string) string { return joinRel(s.root, rel) }

// joinRel resolves a repo-relative slash path against a root, in the host's path syntax.
func joinRel(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

// hasTree reports whether rel exists and holds at least one file, recursively.
//
// Both halves matter. A missing tree is the ordinary "installed before the code it gates" case; an
// existing but EMPTY one is what a half-created directory looks like, and treating it as populated
// would make every rule over it match nothing and report success. The distinction is invisible in a
// CI log, which is why the caller prints the skip.
func (s *scanner) hasTree(rel string) bool {
	dir := filepath.Join(s.root, filepath.FromSlash(rel))

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}

	found := false

	// The walk stops at the first file rather than counting them: the question is existence, and a
	// populated tree can be large (internal/ui/dist ships the built SPA).
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subdirectory is not an answer to "is this populated"
		}

		if !d.IsDir() {
			found = true

			return filepath.SkipAll
		}

		return nil
	})

	return found
}

// paths returns every file under rel whose base name matches one of the globs, sorted.
//
// A nil globs matches every file, which is what the AGPL firewall needs: a transcription can land
// in a .go file, a .ts file or a schema, and a firewall that only read the extensions somebody
// thought of is a firewall with a hole per extension.
//
// The order is deterministic (filepath.WalkDir walks lexically) so a failure names the same file
// first on every run — a gate whose output reorders between runs is one nobody can diff.
func (s *scanner) paths(rel string, globs []string) []string {
	dir := filepath.Join(s.root, filepath.FromSlash(rel))

	var out []string

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal: the rule still runs on the rest
		}

		if d.IsDir() || !matchesGlob(d.Name(), globs) {
			return nil
		}

		relPath, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			return nil //nolint:nilerr // a path outside the root cannot be reported repo-relative
		}

		out = append(out, filepath.ToSlash(relPath))

		return nil
	})

	return out
}

// baseName is path.Base for a repo-relative slash path, written out so that this file does not
// import "path" alongside "path/filepath" — two Base functions in one file is a bug waiting to be
// written.
func baseName(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}

	return rel
}

// matchesGlob reports whether name matches any of the base-name globs. No globs means every file.
func matchesGlob(name string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}

	for _, g := range globs {
		if ok, err := filepath.Match(g, name); err == nil && ok {
			return true
		}
	}

	return false
}

// lines returns the contents of a repo-relative path, split into lines and cached.
//
// A file that cannot be read yields nothing rather than an error. That is the same posture `grep
// -r` takes and the right one here: one unreadable file must not take the other twenty-six rules
// down with it, and a rule whose whole tree is unreadable reports no hits, which is visible.
func (s *scanner) lines(rel string) []string {
	if cached, ok := s.files[rel]; ok {
		return cached
	}

	body, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil {
		s.files[rel] = nil

		return nil
	}

	// A trailing newline would otherwise produce a final empty line that no rule wants and every
	// "does this file end in X" assertion has to special-case.
	text := strings.TrimSuffix(string(body), "\n")
	if text == "" {
		s.files[rel] = nil

		return nil
	}

	split := strings.Split(text, "\n")
	s.files[rel] = split

	return split
}

// exists reports whether a repo-relative path is a regular file.
func (s *scanner) exists(rel string) bool {
	info, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(rel)))

	return err == nil && !info.IsDir()
}

// hit renders one match the way `grep -rn` did: path, line number, and the offending line verbatim.
//
// Verbatim including its leading whitespace, because several fixtures assert on the quoted text —
// MONEY003 checks that all three of `amount_cp REAL`, `rate_bp   NUMERIC` and `fee_cp    DECIMAL`
// appear, which is how it proves the alternation kept every arm.
func hit(rel string, line int, text string) string {
	return fmt.Sprintf("%s:%d:%s", rel, line, text)
}

// commentPrefixes are the whole-line comment openers each rule strips, as data rather than as a
// mode flag: what counts as a comment is a property of the files a rule reads, and the AGPL
// firewall's answer is "none of them" (see the package doc).
var (
	// hashAndSlash is what the general text rules strip — the two spellings a Go, SQL, YAML, HCL or
	// TypeScript file uses between them.
	hashAndSlash = []string{"#", "//"}

	// hashOnly is for the workflow rules. YAML has one comment syntax, and a `//` there is part of
	// a URL or a path far more often than it is a comment.
	hashOnly = []string{"#"}

	// cssComments covers a component stylesheet, whose every value is documented in prose beside
	// it — "a 1px accent border", "fades over 48px at each end". `*` is the continuation line of a
	// block comment.
	cssComments = []string{"/*", "*", "//"}

	// webComments adds HTML's opener, because WEB003 reads index.html as well as the stylesheets.
	webComments = []string{"/*", "*", "//", "#", "<!--"}
)

// isComment reports whether a line is a whole-line comment in one of the given syntaxes.
//
// WHOLE-LINE only, and deliberately so. A trailing comment sits on a line whose code is the thing
// being judged, so stripping it would need a lexer per language and would buy nothing: the case
// this exists for is a line that is entirely prose about the rule.
func isComment(text string, prefixes []string) bool {
	trimmed := strings.TrimLeft(text, " \t")

	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}

	return false
}
