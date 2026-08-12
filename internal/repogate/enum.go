package repogate

import (
	"fmt"
	"regexp"
	"strings"
)

// ENUM001 — a string-enum CHECK comes from a Go catalogue, never from a literal.
//
// Canonical §5: "both the SQL CHECK constraint and the OpenAPI enum are generated from one Go
// catalogue". Every string-enum CHECK in db/schema.hcl now is — ledger_batch.kind/source,
// audit_log.actor_kind/outcome, account.kind/system_key — and each has a test asserting its own
// region matches its own catalogue. NONE of them says anything about a SEVENTH enum, which is the
// hole this closes: a brand-new table added with
//
//	check "bid_session_state_enum" { expr = "state IN ('draft', 'open', 'extended')" }
//
// and no catalogue passes all three of those tests, `make verify-generated` and `make check`. The
// rule that it should not was prose in .claude/rules/migrations.md and AGENTS.md, and prose is what
// produced this finding three times already.
//
// canonical §5 makes bid.tier's DECLARATION ORDER semantic — the resolution ladder — so a literal
// that agrees with the Go list on values and disagrees on order is a resolver bug that no schema
// comparison would ever see. That is the expensive version of this defect.
//
// SCOPED TO `check` BLOCKS, structurally, and to lists containing a QUOTED value:
//
//	caught   expr = "state IN ('draft', 'open')"                     the plain form (CheckExpr)
//	caught   expr = "k IS NULL OR k IN ('a', 'b')"                   the nullable form (NullableCheckExpr)
//	caught   expr = "state IN (\"draft\", \"open\")"                 SQLite's double-quoted literals
//	caught   expr = "state in ('draft', 'open')"                     SQL keywords are case-insensitive
//	caught   expr = "state IN /* set */ ('draft', 'open')"           comments are removed before scanning
//	ignored  expr = "hide_inactive IN (0, 1)"                        a boolean, not a string enum
//	ignored  expr = "retry IN (0, -- not 'draft'\n 1)"               a comment is not a vocabulary
//	ignored  where = "state IN ('open', 'extended')"                 an index predicate, NOT a check
//
// BOTH SQL QUOTE FORMS, and that pair is closed rather than an enumeration that might be missing an
// arm: SQLite produces a string literal from an apostrophe and — through the double-quoted-
// identifier misfeature it keeps for compatibility — from a double quote whose token resolves to no
// column. The other two quoting characters it accepts, backticks and brackets, are identifier
// quoting and cannot express a value. So a hand-written enum can be spelled exactly two ways and
// the rule reads both; changing quote style is not a way past it.
//
// The index-predicate exclusion is deliberate (#97): a partial index over a SUBSET of a vocabulary
// is not the vocabulary, so it cannot be rendered from a catalogue as-is and a rule that demanded
// it would fire on correct work.
//
// The waiver is `// dkp:enum-literal <reason>` on the line above the check, the same in-tree marker
// idiom as `-- dkp:destructive-approved:`. It lives in the schema rather than in an allowlist here
// so that an exception is visible in the diff a reviewer reads, and it requires a reason for the
// same purpose the ADR waiver does.
//
// # Why this is a hand-written scanner and not an HCL parse
//
// Issue #123's direction was hashicorp/hcl/v2, and it is the right library for reading a schema.
// It is also a new direct dependency, which AGENTS.md makes a human decision, and this rule does
// not need it: the question is not "what does this schema mean" but "is a vocabulary written down
// here", and the answer has to survive a file that does not parse. A tainted schema mid-edit, a
// heredoc a generator half-wrote, a merge conflict marker — an HCL parse fails on all three and a
// gate that reports "could not parse" on them is a gate that gets bypassed. The scan is
// line-ordered and stateful for the same reason `make gen` finds its own regions that way.

// enumSchemaRel is the one file this rule reads. Hardcoded, because it is the single source of
// schema truth and a rule that searched for schemas would find the fixtures.
const enumSchemaRel = "db/schema.hcl"

var (
	// The marker constants the Go catalogues declare, in the const block each one keeps them in:
	//
	//	internal/*/kinds/*.go:  schemaEnumBegin = "  // BEGIN GENERATED — … Run `make gen`."
	enumMarkerDecl = regexp.MustCompile(`^[ \t]*schemaEnum(Begin|End)[ \t]*=[ \t]*"`)

	enumCommentLine = regexp.MustCompile(`^[ \t]*(//|#)`)
	enumCheckStart  = regexp.MustCompile(`^[ \t]*check[ \t]+"`)

	// The waiver, and the same reason requirement the ADR waiver carries: two whitespace-separated
	// tokens after the marker, so a separator alone is not a reason.
	enumWaiverWithReason = regexp.MustCompile(`dkp:enum-literal[ \t]+[^ \t]+[ \t]*[^ \t]`)

	enumLeadingParen = regexp.MustCompile(`^[ \t]*\(`)
	enumInParen      = regexp.MustCompile(`(^|[^A-Za-z0-9_])[Ii][Nn][ \t]*\(`)
	enumInEOL        = regexp.MustCompile(`(^|[^A-Za-z0-9_])[Ii][Nn][ \t]*$`)
)

// runEnumRule evaluates ENUM001.
func runEnumRule(s *scanner, rep *report) {
	if !s.exists(enumSchemaRel) {
		rep.skip("ENUM001", enumSchemaRel)

		return
	}

	lines := s.lines(enumSchemaRel)
	if len(lines) == 0 {
		// A scan that CANNOT RUN is a failure, never a pass. Without this an unreadable schema
		// leaves the hit list empty, which reads exactly like a clean one — the shape of bug that
		// makes a gate stop meaning anything without anybody noticing.
		rep.violation("ENUM001", "the string-enum CHECK scan did not run — this is a gate failure, not a pass",
			[]string{enumSchemaRel + ": empty or unreadable"})

		return
	}

	scan := &enumScan{declared: declaredEnumMarkers(s)}

	for i, line := range lines {
		scan.line(i+1, line)
	}

	scan.finish()

	if len(scan.hits) > 0 {
		rep.violation("ENUM001",
			"hand-written string-enum CHECK in "+enumSchemaRel+" — the values come from a Go catalogue "+
				"between the BEGIN/END GENERATED markers (canonical §5, .claude/rules/migrations.md)",
			scan.hits)
	}
}

// declaredEnumMarkers reads the marker lines the Go catalogues declare.
//
// A GENERATED REGION IS ONE A CATALOGUE OWNS, NOT ONE THE SCHEMA CLAIMS. The markers are comments,
// so without this the exemption is self-service: wrap a new literal in a balanced
// `// BEGIN GENERATED` / `// END GENERATED` pair and the rule steps over it — and nothing
// downstream notices either, because `make gen` only rewrites the regions its catalogues declare
// and a fabricated one is not among them.
//
// So a marker line must match, WHOLE, a marker some catalogue declares in Go, and the marker set is
// read out of the Go source rather than restated here. Whole-line identity is the same rule
// schemaenum.Region.Replace uses to find its own region, which is what makes the two agree by
// construction.
//
// Empty when internal/ does not exist yet, which fails CLOSED: with no catalogue to own it, no
// region is generated and every marker is unrecognised.
func declaredEnumMarkers(s *scanner) map[string]bool {
	declared := make(map[string]bool)

	for _, rel := range s.paths("internal", []string{"*.go"}) {
		for _, line := range s.lines(rel) {
			if !enumMarkerDecl.MatchString(line) {
				continue
			}

			// Everything up to and including the opening quote, then the closing quote and any
			// trailing space. The marker may hold anything except a newline, so this stays a slice
			// rather than a Go string-literal unquote: an escape the catalogue does not use must
			// not silently change what the schema is compared against.
			open := strings.Index(line, `"`)
			if open < 0 {
				continue
			}

			marker := strings.TrimRight(line[open+1:], " \t")
			marker = strings.TrimSuffix(marker, `"`)

			if marker != "" {
				declared[marker] = true
			}
		}
	}

	return declared
}

// enumScan is the line-ordered state of one pass over the schema.
//
// The scan tracks the check block STRUCTURALLY and carries an unfinished IN list across lines, so a
// wrapped or heredoc expression is read as the one expression it is. A line-scoped scan misses that
// shape entirely — `IN (` alone on its line, then the values on theirs — which would have made the
// longest vocabularies, the ones most worth generating, the ones that walked through.
type enumScan struct {
	declared map[string]bool
	hits     []string

	inRegion  bool
	beginLine int

	waived     bool
	bareWaiver int

	inCheck    bool
	depth      int
	name       string
	thisWaived bool

	listDepth int
	pendingIn bool
	listLine  int
	listText  string
	reported  bool

	inBlockComment bool
}

// line advances the scan by one source line.
func (e *enumScan) line(no int, text string) {
	if strings.Contains(text, "BEGIN GENERATED") {
		if e.declared[text] {
			e.inRegion = true
			e.beginLine = no
		} else {
			e.report(no, "BEGIN GENERATED marker no Go catalogue declares — a region is generated "+
				"only if a catalogue in internal/*/kinds owns it:"+text)
		}

		return
	}

	if strings.Contains(text, "END GENERATED") {
		if e.declared[text] {
			e.inRegion = false
		} else {
			e.report(no, "END GENERATED marker no Go catalogue declares:"+text)
		}

		return
	}

	// Comment lines, in both HCL spellings. A gate that fires on the prose documenting it is a gate
	// people route around — and db/schema.hcl's own header names the enum shape.
	if enumCommentLine.MatchString(text) {
		switch {
		case enumWaiverWithReason.MatchString(text):
			e.waived = true
		case strings.Contains(text, "dkp:enum-literal"):
			e.waived = false
			e.bareWaiver = no
		}

		return
	}

	if enumCheckStart.MatchString(text) {
		e.inCheck = true
		e.depth = 0
		e.name = checkName(text)
		e.thisWaived = e.waived

		// A list cannot span two check blocks. Clearing it here means an unbalanced parenthesis in
		// one block cannot swallow the next one.
		e.listDepth = 0
		e.pendingIn = false
		e.inBlockComment = false
	}

	if e.inCheck {
		// The order of these conditions is load-bearing: the list scan runs only when the check is
		// neither generated nor waived, so a waived block does not leave half-open list state
		// behind for the next one. The list is then reported at the line it STARTED on — where the
		// author is looking — and once, however many quoted values follow it.
		if !e.inRegion && !e.thisWaived && e.quotedInList(e.stripSQLComments(text), no, text) && !e.reported {
			e.reported = true
			e.report(e.listLine, fmt.Sprintf("check %q: %s", e.name, e.listText))
		}

		e.depth += strings.Count(text, "{") - strings.Count(text, "}")
		if e.depth <= 0 {
			e.inCheck = false
			e.listDepth = 0
			e.pendingIn = false
			e.inBlockComment = false
		}
	}

	// A blank line or any other statement ends the waiver's reach: it applies to the check block it
	// sits above, not to the rest of the file.
	if !e.inCheck {
		e.waived = false
	}
}

// finish emits the two whole-file findings.
func (e *enumScan) finish() {
	// An UNCLOSED `BEGIN GENERATED` is itself a violation. Without it, one unbalanced marker line
	// exempts every check after it — the whole rest of the file — and the gate stays green while
	// doing nothing.
	if e.inRegion {
		e.report(e.beginLine, "unclosed BEGIN GENERATED marker — every check after it is silently exempt")
	}

	if e.bareWaiver > 0 {
		e.report(e.bareWaiver, "dkp:enum-literal with no reason — the reason is the point of the waiver")
	}
}

func (e *enumScan) report(line int, msg string) {
	e.hits = append(e.hits, fmt.Sprintf("%s:%d: %s", enumSchemaRel, line, msg))
}

// checkName pulls the block name out of a `check "…" {` line. It is what a reviewer keys a waiver
// to, so it is carried into the message.
func checkName(text string) string {
	rest := enumCheckStart.ReplaceAllString(text, "")

	if end := strings.Index(rest, `"`); end >= 0 {
		return rest[:end]
	}

	return rest
}

// stripSQLComments removes SQL comments from an expression before it is scanned.
//
// `state IN -- why these\n  ('a')` and `state IN /* set */ ('a')` are the same CHECK as the one
// written without them, so the scanner has to see the same thing. Removing the comments is what
// makes the token boundary between the keyword and its parenthesis insensitive to whatever a person
// wrote in the gap, rather than a list of gaps the pattern happens to allow — the enumeration
// failure this rule has now been bitten by three times.
//
// Block-comment state is file-scoped, like the list state, because `/*` may close lines later.
//
// STRING CONTEXT IS DELIBERATELY NOT TRACKED, and the direction of that error is the reason it is
// safe. A `--` inside a value truncates the rest of the line, which can only REMOVE text from the
// scan — and never the quote that opens the literal it appears in, since that quote comes first and
// has already been counted. So this can lose the closing parenthesis of a list it has already
// reported; it cannot hide a vocabulary.
func (e *enumScan) stripSQLComments(s string) string {
	var out strings.Builder

	for i := 0; i < len(s); {
		ch := s[i]

		var next byte
		if i+1 < len(s) {
			next = s[i+1]
		}

		if e.inBlockComment {
			if ch == '*' && next == '/' {
				e.inBlockComment = false
				i += 2
			} else {
				i++
			}

			continue
		}

		if ch == '-' && next == '-' {
			break
		}

		if ch == '/' && next == '*' {
			e.inBlockComment = true
			i += 2

			continue
		}

		out.WriteByte(ch)
		i++
	}

	return out.String()
}

// quotedInList reports whether an IN list on this line holds a quoted value, carrying an unfinished
// list ACROSS LINES:
//
//	expr = <<-SQL
//	  state IN (
//	    'draft',
//	    'open'
//	  )
//	SQL
//
// A line-scoped scanner reads `IN (` with no quote after it, then two value lines with no `IN (` on
// them, and finds nothing. listDepth is therefore scan-scoped state: the walk runs character by
// character, entering a list at `IN (`, and staying in it until the parenthesis that closes it
// however many lines later.
//
// Character-wise rather than by regex because the question is about the text BETWEEN the
// parentheses — `IN (0, 1)` is a boolean and must not match, a quote anywhere in a list is a
// vocabulary — and because nesting has to be counted rather than assumed away.
//
// `[Ii][Nn]`, because SQL keywords are case-insensitive: the uppercase the generator emits is a
// convention rather than a rule, and a hand-written CHECK — the only kind this rule ever sees — is
// written in whatever case its author was typing in. The leading non-word character keeps JOIN and
// MIN out: what precedes the keyword decides, not its case.
//
// The keyword and its parenthesis may also be split across the line break — the same shape one
// token earlier — so a line ending in the bare keyword arms pendingIn and the next line opening
// with `(` enters the list. A line with nothing left on it returns early rather than clearing that
// state, which is what carries the keyword across a blank or comment-only line between the two.
func (e *enumScan) quotedInList(s string, no int, full string) bool {
	if strings.TrimLeft(s, " \t") == "" {
		return false
	}

	i := 0

	if e.listDepth == 0 && e.pendingIn {
		if loc := enumLeadingParen.FindStringIndex(s); loc != nil {
			i = loc[1]
			e.listDepth = 1
			e.reported = false
		}
	}

	e.pendingIn = false

	hit := false

	for i < len(s) {
		if e.listDepth > 0 {
			switch s[i] {
			case '(':
				e.listDepth++
			case ')':
				e.listDepth--
			case '\'', '"':
				hit = true
			}

			i++

			continue
		}

		loc := enumInParen.FindStringIndex(s[i:])
		if loc == nil {
			if enumInEOL.MatchString(s[i:]) {
				e.pendingIn = true
				e.listLine = no
				e.listText = full
			}

			break
		}

		// Just past the opening parenthesis the match ended on.
		i += loc[1]
		e.listDepth = 1
		e.listLine = no
		e.listText = full
		e.reported = false
	}

	return hit
}
