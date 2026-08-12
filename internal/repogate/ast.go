package repogate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// astRule is one Go-syntax law: a tree, an allowlist, and a pure function over one parsed file.
//
// The shape is `go/analysis`'s — {id, doc, run over a file, report a position} — deliberately, so
// that adopting the real framework later is a change of driver rather than a rewrite of the rules.
// What it does not do is type-check, and the package doc records why: every negative fixture these
// rules exist for is a tree that does not build, and so is a repository mid-sequence.
//
// One entry per (rule, tree) pair, matching textRule and for the same reason: a rule that covers
// two trees appears twice, so losing one is a diff rather than a silence.
type astRule struct {
	id   string
	desc string
	tree string

	// reject drops a hit whose rendered "path:line:text" matches — the same line-scoped allowlist
	// the text rules use, so `^internal/store/` means the same thing in both catalogues.
	reject []*regexp.Regexp

	// inspect returns the 1-based line numbers of every violation in one file.
	inspect func(gf *goFile) []int
}

// goFile is one parsed Go file, with the source lines kept so a failure can quote the offending
// line the way `grep -n` did.
type goFile struct {
	fset  *token.FileSet
	file  *ast.File
	lines []string
}

// line returns the source line a node starts on.
func (gf *goFile) line(pos token.Pos) int {
	return gf.fset.Position(pos).Line
}

// text returns the source of a 1-based line, or "" when the line is out of range.
func (gf *goFile) text(line int) string {
	if line < 1 || line > len(gf.lines) {
		return ""
	}

	return gf.lines[line-1]
}

// importLine returns the line of the first import whose path satisfies match, or 0.
func (gf *goFile) importLine(match func(path string) bool) int {
	for _, spec := range gf.file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !match(path) {
			continue
		}

		return gf.line(spec.Pos())
	}

	return 0
}

// locals returns the names a package is reachable under in this file: the conventional one, plus
// whatever the import block actually bound it to.
//
// BOTH, and the pair is the point. Resolving the import catches an ALIASED one — `import c
// ".../internal/clock"` is the first thing anybody reaches for when a gate starts complaining, and
// a check an alias defeats is a check that teaches people to alias. Keeping the conventional name
// catches the fixture trees, which are fragments with no import block at all: a negative fixture
// asserting that `sql.Open` fires must fire on a file that never declared where `sql` came from,
// because that is what the tainted file the rule exists to catch looks like before it compiles.
func (gf *goFile) locals(conventional string, match func(path string) bool) map[string]bool {
	out := map[string]bool{conventional: true}

	for _, spec := range gf.file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !match(path) {
			continue
		}

		if spec.Name == nil {
			// No alias: the local name is the last segment, with a major-version suffix skipped —
			// `huma/v2` is reached as `huma`.
			out[defaultLocalName(path)] = true

			continue
		}

		if spec.Name.Name != "_" && spec.Name.Name != "." {
			out[spec.Name.Name] = true
		}
	}

	return out
}

// defaultLocalName is the name an unaliased import binds: the last path segment, or the one before
// it when the last is a `/vN` module major version.
func defaultLocalName(path string) string {
	segments := strings.Split(path, "/")

	last := segments[len(segments)-1]
	if len(segments) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		last = segments[len(segments)-2]
	}

	return last
}

// selectorLines returns the line of every `<local>.<name>` selector in the file, where local is one
// of the given names and name is one of the wanted ones.
//
// It reports a REFERENCE, not only a call. `time.Now` assigned to a variable reads the wall clock
// just as surely as `time.Now()` does, and the shell gate's trailing `(` was an artefact of grep
// rather than a decision.
func selectorLines(gf *goFile, locals map[string]bool, wanted ...string) []int {
	var lines []int

	ast.Inspect(gf.file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !slices.Contains(wanted, sel.Sel.Name) {
			return true
		}

		if ident, ok := sel.X.(*ast.Ident); ok && locals[ident.Name] {
			lines = append(lines, gf.line(sel.Pos()))
		}

		return true
	})

	return lines
}

// identLines returns the line of every identifier with one of the given names, wherever it appears.
func identLines(gf *goFile, wanted ...string) []int {
	var lines []int

	ast.Inspect(gf.file, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && slices.Contains(wanted, ident.Name) {
			lines = append(lines, gf.line(ident.Pos()))
		}

		return true
	})

	return lines
}

// underPackage reports whether an import path passes through the given repo-relative package path —
// `internal/store` matches the package itself and every subpackage of it, under any module path.
//
// The module path is not assumed, because the negative fixtures are fabricated modules
// (`example.com/tainted`) and because a rule that hardcoded this repository's module path would
// pass vacuously in exactly the tree a test points it at.
func underPackage(path, pkg string) bool {
	return strings.Contains("/"+path+"/", "/"+pkg+"/")
}

// astRules is the Go-syntax law catalogue, in the order the rules run and report.
func astRules() []astRule {
	var rules []astRule

	rules = append(rules, routeRules()...)
	rules = append(rules, storeRules()...)
	rules = append(rules, purityRules()...)

	return rules
}

// --- Law 1: routes are declared only in internal/api --------------------------------------------

func routeRules() []astRule {
	// ci.yml has advertised this ban since the gates were written and nothing implemented it: the
	// shell script had no route rule at all. internal/api/arch_test.go's
	// TestArch_Routes_AreDeclaredOnlyInAPIPackage covers the same law over the same trees, and this
	// is its cheap twin — it runs in `lint / repo`, which needs no build, and it runs on a tree
	// where internal/api does not exist yet.
	//
	// Delete the law and the SPA can be served from an operation absent from the published spec,
	// which is exactly how "the UI needs it but a bot would not" endpoints appear.
	inAPI := regexp.MustCompile(`^internal/api/`)

	inspect := func(gf *goFile) []int {
		locals := gf.locals("huma", func(path string) bool {
			return strings.Contains(path, "danielgtaylor/huma")
		})

		return selectorLines(gf, locals, "Register")
	}

	var rules []astRule

	for _, tree := range []string{"internal", "cmd"} {
		rules = append(rules, astRule{
			id: "ROUTE001", desc: "huma.Register outside internal/api (routes are declared only there)",
			tree: tree, reject: []*regexp.Regexp{inAPI}, inspect: inspect,
		})
	}

	return rules
}

// --- Law 2: *sql.DB is held only by internal/store ----------------------------------------------

func storeRules() []astRule {
	// cmd/ is gated as well as internal/: `sql.Open` in a Cobra command is the same violation, and
	// it is where a "just for the migrate subcommand" handle would be reached for first.
	inStore := regexp.MustCompile(`^internal/store/`)

	isDatabaseSQL := func(path string) bool { return path == "database/sql" }

	// sql.OpenDB, not only sql.Open. Interposing anything on connections — the statement counter in
	// internal/store does exactly this — requires OpenDB, so a rule that watched only sql.Open
	// would be blind to the very call the escape hatch needs.
	openInspect := func(gf *goFile) []int {
		return selectorLines(gf, gf.locals("sql", isDatabaseSQL), "Open", "OpenDB")
	}

	// Receiver-independent: the shell's first form matched only receivers literally named db or DB,
	// so renaming the variable to `conn` walked straight through it.
	//
	// A ZERO-ARGUMENT call is excluded, and that exclusion is the whole reason this rule is now
	// syntax rather than text. `r.URL.Query()` is net/url's accessor, it appears all over
	// internal/api, and it cannot be a database call because every real one takes at least the SQL
	// string. In a line-scoped text rule the exclusion could not live in the allowlist — an
	// allowlist drops the entire `path:line:text` line, so exempting `.Query()` there would also
	// drop
	//
	//	conn.QueryContext(ctx, "SELECT ..."+r.URL.Query().Get("q"))
	//
	// which is the single most natural shape a law-2 violation takes. The text rule had to encode
	// the exclusion in the pattern instead, as `\(([^)]|$)`, whose `|$` arm existed only to keep a
	// call whose arguments wrap to the next line matched. Reading the call site answers both
	// questions directly: the arguments are the arguments, wherever the author put the newline.
	queryInspect := func(gf *goFile) []int {
		var lines []int

		ast.Inspect(gf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			switch sel.Sel.Name {
			case "Query", "QueryRow", "Exec", "QueryContext", "QueryRowContext", "ExecContext":
				lines = append(lines, gf.line(call.Pos()))
			}

			return true
		})

		return lines
	}

	// internal/store exports raw-SQL TEST helpers so that tests in other packages can reach the
	// unexported pool and *sql.Tx state a genuine mutation runs against. Production code must never
	// call them: they take a testing.TB (which cannot exist in a shipped binary) precisely so
	// misuse fails to compile, and this rule is the belt to that suspenders. The allowlist exempts
	// *_test.go call sites and the definition file; a hit anywhere else is a test affordance
	// leaking into the binary. This machine check replaces the earlier, fragile convention of
	// naming the wrapper method `Do` so that a text rule could not see `h.Do(...)`.
	forTestInspect := func(gf *goFile) []int {
		return identLines(gf,
			"ExecForTest", "ExecErrForTest", "QueryForTest", "QueryRowForTest", "TxForTest", "TxHandleForTest")
	}

	forTestAllow := regexp.MustCompile(`(_test\.go:|store/testing\.go:)`)

	var rules []astRule

	for _, tree := range []string{"internal", "cmd"} {
		rules = append(rules,
			astRule{
				id: "SQL001", desc: "sql.Open/sql.OpenDB outside internal/store",
				tree: tree, reject: []*regexp.Regexp{inStore}, inspect: openInspect,
			},
			astRule{
				id: "SQL002", desc: ".Query/.Exec outside internal/store",
				tree: tree, reject: []*regexp.Regexp{inStore}, inspect: queryInspect,
			},
			astRule{
				id: "SQL003", desc: "ForTest raw-SQL helper called outside _test.go",
				tree: tree, reject: []*regexp.Regexp{forTestAllow}, inspect: forTestInspect,
			},
		)
	}

	return rules
}

// --- Law 3: internal/strategy is pure, and money is an integer ----------------------------------

func purityRules() []astRule {
	rules := []astRule{
		{
			// A strategy plans, it does not read the database. Everything a planner is allowed to
			// know arrives through strategy.Ctx; a planner that could reach the store could decide
			// on state its own Ctx never saw, and the batch that decision produced would not
			// replay.
			//
			// The AST twin is TestArch_Strategy_ImportGraph_HasNoStore, which walks the whole
			// import graph and so also sees a TRANSITIVE path this cannot.
			id: "PURE001", desc: "internal/strategy imports internal/store",
			tree: "internal/strategy",
			inspect: func(gf *goFile) []int {
				line := gf.importLine(func(path string) bool { return underPackage(path, "internal/store") })
				if line == 0 {
					return nil
				}

				return []int{line}
			},
		},
		{
			// The seeded Rng arrives through Ctx.Rng(), and its seed is persisted onto
			// ledger_batch.rng_seed — that seed is the entire reason a batch replays
			// byte-identically. A strategy that reached for math/rand instead would make the
			// persisted seed a decoration and the determinism property a tautology.
			//
			// math/rand/v2 counts. The text rule matched the quoted literal `"math/rand"`, which a
			// `/v2` suffix walked straight past; an import path is a path here, so both spellings
			// are the same ban.
			id: "PURE002", desc: "math/rand in internal/strategy (use the injected seeded Rng)",
			tree: "internal/strategy",
			inspect: func(gf *goFile) []int {
				line := gf.importLine(func(path string) bool {
					return path == "math/rand" || strings.HasPrefix(path, "math/rand/")
				})
				if line == 0 {
					return nil
				}

				return []int{line}
			},
		},
		{
			// time.Now belongs to internal/clock and nowhere else, because a service that reads the
			// wall clock directly cannot be tested without sleeping and cannot be replayed at all.
			// internal/clock's System.Now is the single call site in the repository that is allowed
			// to make the call, so a repo-wide ban would make the injected-clock design itself a
			// violation — which is what the allowlist is.
			//
			// The forbidigo twin is `^time\.Now$` in .golangci.yml, proven by
			// TestLintBan_TimeNowOutsideClock_FailsLint.
			id: "CLOCK001", desc: "time.Now outside internal/clock (use the injected Clock)",
			tree: "internal", reject: []*regexp.Regexp{regexp.MustCompile(`^internal/clock/`)},
			inspect: func(gf *goFile) []int {
				return selectorLines(gf, gf.locals("time", func(p string) bool { return p == "time" }), "Now")
			},
		},
		{
			// CLOCK002 closes the hole CLOCK001 cannot see.
			//
			// internal/strategy legitimately imports internal/clock, because strategy.Ctx.Clock()
			// returns a clock.Clock. Nothing above stops a strategy from then writing
			// `clock.System{}.Now()`, which reads the REAL wall clock: CLOCK001 looks for time.Now,
			// which that is not; the arch test's direct-import ban sees only internal/clock, which
			// is allowed; and forbidigo's `^time\.Now$` resolves to a method on clock.System. A
			// plan that depends on when it ran cannot be replayed, which is the entire reason the
			// clock is injected.
			//
			// clock.System is the ONLY real-clock path out of that package — Clock is an interface,
			// Fake is a test double, and System.Now is the one function in the repository that
			// calls time.Now — so banning the identifier closes it rather than narrowing it. Scoped
			// to internal/strategy: cmd/ wiring constructs a System on purpose, which is where a
			// real clock is supposed to come from.
			id: "CLOCK002", desc: "clock.System in internal/strategy (the clock is injected through Ctx.Clock)",
			tree: "internal/strategy",
			inspect: func(gf *goFile) []int {
				locals := gf.locals("clock", func(p string) bool { return underPackage(p, "internal/clock") })

				return selectorLines(gf, locals, "System")
			},
		},
	}

	// A float in the point path does not fail, it DRIFTS, and a balance that is wrong by a fraction
	// of a point for a year is discovered by a guild member disputing a bid, not by CI. Both
	// arithmetic trees are covered; internal/strategy is where the tempting float lives (an
	// attendance ratio, a decay rate).
	//
	// The forbidigo twin is `\bfloat(32|64)\b` scoped by a path-except exclusion in .golangci.yml,
	// proven by TestLintBan_FloatInLedger_FailsLint.
	for _, tree := range []string{"internal/ledger", "internal/strategy"} {
		rules = append(rules, astRule{
			id: "MONEY001", desc: "float type in " + tree,
			tree: tree,
			inspect: func(gf *goFile) []int {
				return identLines(gf, "float32", "float64")
			},
		})
	}

	return rules
}

// runASTRules evaluates every Go-syntax law against the tree.
func runASTRules(s *scanner, rep *report) {
	for _, rule := range astRules() {
		if !s.hasTree(rule.tree) {
			rep.skip(rule.id, rule.tree)

			continue
		}

		var hits []string

		for _, rel := range s.paths(rule.tree, []string{"*.go"}) {
			gf := s.parse(rel)
			if gf == nil {
				continue
			}

			for _, line := range dedupe(rule.inspect(gf)) {
				rendered := hit(rel, line, gf.text(line))
				if !rejected(rendered, rule.reject) {
					hits = append(hits, rendered)
				}
			}
		}

		if len(hits) > 0 {
			rep.violation(rule.id, rule.desc, hits)
		}
	}
}

// dedupe sorts line numbers and drops repeats, so a line holding two violations of one rule is
// reported once — the way a line-based scan reported it.
func dedupe(lines []int) []int {
	slices.Sort(lines)

	return slices.Compact(lines)
}

// parse returns the parsed form of a repo-relative Go file, cached, or nil when it does not parse.
//
// A FILE THAT DOES NOT PARSE IS SKIPPED, silently, and that is the same choice
// internal/api/arch_test.go's route scan records: a file that is not Go cannot be declaring a
// route, holding a database handle or importing math/rand, and a gate that reported syntax errors
// would become the messenger for every typo in the repository while the compiler — which is the
// mechanism for "this file is Go" — says it better one step later.
func (s *scanner) parse(rel string) *goFile {
	if cached, ok := s.parsed[rel]; ok {
		return cached
	}

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, s.abs(rel), nil, parser.SkipObjectResolution)
	if err != nil {
		s.parsed[rel] = nil

		return nil
	}

	gf := &goFile{fset: fset, file: file, lines: s.lines(rel)}
	s.parsed[rel] = gf

	return gf
}
