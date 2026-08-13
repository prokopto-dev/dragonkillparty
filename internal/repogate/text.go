package repogate

import "regexp"

// textRule is one config-shaped rule: a tree, a file glob, a pattern, and the lines that are
// allowed to match anyway.
//
// It is DATA. Everything a rule can express is a field here, and the catalogue in rules.go holds no
// logic at all — which is the property that makes a rule reviewable in a diff rather than
// reconstructible from a shell pipeline. A rule that needs behaviour the fields cannot express is a
// rule that belongs in ast.go, enum.go or adr.go, not a new flag.
type textRule struct {
	id   string
	desc string

	// tree is walked, and is also the guard: the rule skips when it does not exist yet.
	tree string

	// extra names files scanned in addition to the tree. WEB003 uses it for web/index.html — the
	// document the browser parses first, and the only place its violation can be written outside
	// web/src.
	extra []string

	// include are base-name globs. Empty means every file in the tree.
	include []string

	// pattern is matched against each line.
	pattern *regexp.Regexp

	// reject drops a hit whose rendered "path:line:text" matches. The rendered form is what the
	// pattern is applied to, so a reject may key on the path (`^internal/store/`) or on the text.
	//
	// SQL003's `(_test\.go:|store/testing\.go:)` is why the whole line rather than the path alone:
	// it exempts a call site by file suffix, and the trailing colon is what stops it matching a
	// path that merely contains the string.
	reject []*regexp.Regexp

	// strip are the whole-line comment openers dropped before matching. Nil strips nothing, which
	// is the AGPL firewall's deliberate choice — see the package doc.
	strip []string

	// quiet suppresses the skip note. The rules that carried their own `if has …` block in the
	// shell script printed nothing when their tree was absent, and a skip line appearing where one
	// never did before would be a behaviour change in the CI log for no gain.
	quiet bool
}

// runTextRules evaluates every config-shaped rule in the catalogue against the tree.
//
// A catalogue that cannot be read is GATE000 — the same posture a missing toolchain gets, and for
// the same reason: these rules are the whole of laws 1-4's text half, the money rules, the design
// tokens, the supply-chain pins and the AGPL firewall, and reporting green having run none of them
// is the one outcome that must be impossible.
func runTextRules(s *scanner, rep *report) {
	rules, err := textRules()

	reportTextRules(rules, err, s, rep)
}

// reportTextRules is the half a test can drive with a catalogue that failed to load. The failure
// path is the one branch here that no fixture tree can reach — the catalogue is embedded, so a tree
// cannot corrupt it — and it is also the branch whose regression would silently disable half the
// rules in the repository.
func reportTextRules(rules []textRule, err error, s *scanner, rep *report) {
	if err != nil {
		rep.violation("GATE000", errNoCatalogue.Error()+", so no text rule ran", []string{err.Error()})

		return
	}

	for _, rule := range rules {
		if !s.hasTree(rule.tree) {
			if !rule.quiet {
				rep.skip(rule.id, rule.tree)
			}

			continue
		}

		if hits := rule.scan(s); len(hits) > 0 {
			rep.violation(rule.id, rule.desc, hits)
		}
	}
}

// scan returns every hit the rule finds, in path then line order.
func (r textRule) scan(s *scanner) []string {
	var hits []string

	paths := s.paths(r.tree, r.include)

	for _, extra := range r.extra {
		if s.exists(extra) && matchesGlob(baseName(extra), r.include) {
			paths = append(paths, extra)
		}
	}

	for _, path := range paths {
		for i, line := range s.lines(path) {
			if isComment(line, r.strip) {
				continue
			}

			if !r.pattern.MatchString(line) {
				continue
			}

			rendered := hit(path, i+1, line)
			if rejected(rendered, r.reject) {
				continue
			}

			hits = append(hits, rendered)
		}
	}

	return hits
}

// rejected reports whether a rendered hit matches any of the rule's allowlist patterns.
func rejected(rendered string, reject []*regexp.Regexp) bool {
	for _, re := range reject {
		if re.MatchString(rendered) {
			return true
		}
	}

	return false
}
