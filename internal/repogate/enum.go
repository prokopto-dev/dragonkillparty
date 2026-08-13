package repogate

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ENUM001 — a string-enum CHECK comes from a Go catalogue, never from a literal.
//
// Canonical §5: "both the SQL CHECK constraint and the OpenAPI enum are generated from one Go
// catalogue". Every string-enum CHECK in db/schema.hcl now is — ledger_batch.kind/source,
// audit_log.actor_kind/outcome, account.kind/system_key, decay_run.state — and each has a test
// asserting its own
// region matches its own catalogue. NONE of them says anything about an EIGHTH enum, which is the
// hole this closes: a brand-new table added with
//
//	check "bid_session_state_enum" { expr = "state IN ('draft', 'open', 'extended')" }
//
// and no catalogue passes all four of those tests, `make verify-generated` and `make check`. The
// rule that it should not was prose in .claude/rules/migrations.md and AGENTS.md, and prose is what
// produced this finding three times already. decay_run.state (#192) is the rule working as intended:
// the table arrived with a literal CHECK, the gate refused it, and the catalogue is what landed.
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
// # Why this is an HCL parse and not a hand-written scanner (issue #116)
//
// It was a scanner: 90 lines of awk, then the same state machine in Go. Review of the PR that added
// it found five bypasses, one per round, and every one of them was an INPUT SHAPE THE SCANNER DID
// NOT MODEL rather than a logic error — a heredoc, a list wrapped over several lines, the keyword
// and its parenthesis split by a comment. The rate of discovery was the finding: a hand-written
// parser gets a new bypass per reviewer.
//
// hclsyntax hands back the `expr` attribute's EVALUATED string, so those shapes stop existing as a
// category. Quoted or heredoc, wrapped or not, escaped or not, the parser returns the same SQL, and
// what is left is one small scan over a real SQL string — the part that genuinely belongs to this
// rule.
//
// TWO THINGS THE AST DOES NOT HAND YOU, and both stay raw scans over the source lines:
//
//   - GENERATED REGIONS. The markers are comments, and comments are not in a parsed body.
//   - THE WAIVER, for the same reason.
//
// A SCHEMA THAT DOES NOT PARSE IS A VIOLATION, never a pass. ADR-0018 rejected an HCL parse partly
// because "a gate that reports could not parse is a gate that gets bypassed"; the answer is that it
// does not report a pass either. A merge-conflict marker or a half-written heredoc in db/schema.hcl
// is a tree where `make gen`, Atlas and sqlc are all already failing, and this rule saying so in one
// line is not what stops that person landing their change. Reporting green on a file nobody could
// read is.
const enumSchemaRel = "db/schema.hcl"

var (
	// The marker constants the Go catalogues declare, in the const block each one keeps them in:
	//
	//	internal/*/kinds/*.go:  schemaEnumBegin = "  // BEGIN GENERATED — … Run `make gen`."
	enumMarkerDecl = regexp.MustCompile(`^[ \t]*schemaEnum(Begin|End)[ \t]*=[ \t]*"`)

	enumCommentLine = regexp.MustCompile(`^[ \t]*(//|#)`)

	// The waiver, and the same reason requirement the ADR waiver carries: two whitespace-separated
	// tokens after the marker, so a separator alone is not a reason.
	enumWaiverWithReason = regexp.MustCompile(`dkp:enum-literal[ \t]+[^ \t]+[ \t]*[^ \t]`)

	// enumInList is the SQL keyword and the parenthesis that opens its list. `[Ii][Nn]`, because SQL
	// keywords are case-insensitive: the uppercase the generator emits is a convention rather than a
	// rule, and a hand-written CHECK — the only kind this rule ever sees — is written in whatever
	// case its author was typing in. The leading non-word character keeps JOIN and MIN out.
	//
	// Whitespace between the two now includes a NEWLINE, which is what the AST bought: the
	// expression arrives as one string however its author wrapped it, so "the keyword is at the end
	// of a line" is no longer a separate case to model.
	enumInList = regexp.MustCompile(`(^|[^A-Za-z0-9_])[Ii][Nn][ \t\r\n]*\(`)
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

	if hits := scanEnums(lines, declaredEnumMarkers(s)); len(hits) > 0 {
		rep.violation("ENUM001",
			"hand-written string-enum CHECK in "+enumSchemaRel+" — the values come from a Go catalogue "+
				"between the BEGIN/END GENERATED markers (canonical §5, .claude/rules/migrations.md)",
			hits)
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

// finding is one thing the rule has to say, and the line to say it about.
type finding struct {
	line int
	msg  string
}

// scanEnums returns every ENUM001 finding in a schema, in line order.
//
// THREE PASSES, and the split is the rule's whole shape: the markers and the waiver are comments and
// are read from the source lines, the CHECK blocks are read from the parsed body, and neither pass
// can do the other's job.
func scanEnums(lines []string, declared map[string]bool) []string {
	regions, found := enumRegions(lines, declared)
	found = append(found, bareWaivers(lines)...)
	found = append(found, checkFindings(lines, regions)...)

	slices.SortStableFunc(found, func(a, b finding) int { return a.line - b.line })

	hits := make([]string, 0, len(found))
	for _, f := range found {
		hits = append(hits, fmt.Sprintf("%s:%d: %s", enumSchemaRel, f.line, f.msg))
	}

	return hits
}

// region is a half-open span of generated lines: [begin, end).
type region struct{ begin, end int }

// enumRegions reads the generated-region markers and returns the spans they open, plus a finding per
// marker that no catalogue owns.
//
// An UNCLOSED `BEGIN GENERATED` is itself a violation AND still exempts the rest of the file, which
// is the pair that matters: without the violation, one unbalanced marker line silently exempts every
// check after it and the gate stays green while doing nothing; without the exemption, the same line
// would produce a wall of findings that buries the one that explains them.
func enumRegions(lines []string, declared map[string]bool) (regions []region, found []finding) {
	open := 0

	for i, text := range lines {
		no := i + 1

		switch {
		case strings.Contains(text, "BEGIN GENERATED"):
			if !declared[text] {
				found = append(found, finding{no, "BEGIN GENERATED marker no Go catalogue declares — " +
					"a region is generated only if a catalogue in internal/*/kinds owns it:" + text})

				continue
			}

			open = no

		case strings.Contains(text, "END GENERATED"):
			if !declared[text] {
				found = append(found, finding{no, "END GENERATED marker no Go catalogue declares:" + text})

				continue
			}

			if open > 0 {
				regions = append(regions, region{begin: open, end: no})
				open = 0
			}
		}
	}

	if open > 0 {
		found = append(found, finding{
			open,
			"unclosed BEGIN GENERATED marker — every check after it is silently exempt",
		})
		regions = append(regions, region{begin: open, end: len(lines) + 1})
	}

	return regions, found
}

// bareWaivers reports every `dkp:enum-literal` carrying no reason.
//
// The reason is the artefact and the marker is only its carrier — a waiver that costs one token is a
// waiver that gets pasted onto the next literal. This is the same requirement, and the same
// argument, as ADR001's `adr: n/a — <reason>`.
func bareWaivers(lines []string) (found []finding) {
	for i, text := range lines {
		if !enumCommentLine.MatchString(text) || !strings.Contains(text, "dkp:enum-literal") {
			continue
		}

		if !enumWaiverWithReason.MatchString(text) {
			found = append(found, finding{
				i + 1,
				"dkp:enum-literal with no reason — the reason is the point of the waiver",
			})
		}
	}

	return found
}

// checkFindings parses the schema and reports every `check` block whose expression writes a
// vocabulary down.
//
// A PARSE FAILURE IS A FINDING. See the package-level note above: the alternative is a rule that
// reports green on a file nobody could read, which is the failure mode every other rule here is
// written to avoid.
func checkFindings(lines []string, regions []region) (found []finding) {
	src := []byte(strings.Join(lines, "\n"))

	file, diags := hclsyntax.ParseConfig(src, enumSchemaRel, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return []finding{{
			diagLine(diags[0]),
			"the schema does not parse, so the string-enum CHECK scan did not run — this is a gate " +
				"failure, not a pass: " + diags[0].Summary + ": " + diags[0].Detail,
		}}
	}

	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return []finding{{1, "the schema parsed into a body this rule cannot read — gate failure, not a pass"}}
	}

	for _, blk := range checkBlocks(body) {
		line := blk.DefRange().Start.Line

		if inRegion(line, regions) || waived(lines, line) {
			continue
		}

		attr, hasExpr := blk.Body.Attributes["expr"]
		if !hasExpr {
			continue
		}

		if !quotedInList(stripSQLComments(exprText(attr, src))) {
			continue
		}

		found = append(found, finding{
			line,
			fmt.Sprintf("check %q: %s", label(blk), strings.TrimRight(lines[line-1], " \t")),
		})
	}

	return found
}

// diagLine is the line a diagnostic points at, or 1 when it points at nothing.
//
// `Subject` is a POINTER and hcl leaves it nil for a diagnostic about the file as a whole, so
// reading through it is a panic waiting for the one input this branch exists to handle. A gate that
// crashes on an unparseable schema and one that passes it are the same outcome to a CI log: no rule
// id, no line, nothing to fix.
func diagLine(d *hcl.Diagnostic) int {
	if d.Subject == nil {
		return 1
	}

	return d.Subject.Start.Line
}

// checkBlocks returns every `check` block in the file, at any depth.
//
// Depth-independent because the rule is about the CHECK, not about where somebody nested it: Atlas
// puts them inside `table`, and a schema that grew another container would otherwise silently stop
// being scanned.
func checkBlocks(body *hclsyntax.Body) (blocks []*hclsyntax.Block) {
	for _, blk := range body.Blocks {
		if blk.Type == "check" {
			blocks = append(blocks, blk)
		}

		if blk.Body != nil {
			blocks = append(blocks, checkBlocks(blk.Body)...)
		}
	}

	return blocks
}

// label is the check's name — what a reviewer keys a waiver to, so it is carried into the message.
func label(blk *hclsyntax.Block) string {
	if len(blk.Labels) == 0 {
		return blk.Type
	}

	return blk.Labels[0]
}

func inRegion(line int, regions []region) bool {
	for _, r := range regions {
		if line >= r.begin && line < r.end {
			return true
		}
	}

	return false
}

// waived reports whether a `dkp:enum-literal` waiver with a reason sits immediately above line.
//
// IMMEDIATELY ABOVE, through any run of comment lines and nothing else. The waiver applies to the
// check block it sits on top of, not to the rest of the file — a waiver that outlived its own block
// would exempt everything after it, which is the bypass the reason requirement exists to make
// visible rather than to create.
func waived(lines []string, line int) bool {
	for i := line - 2; i >= 0; i-- {
		text := lines[i]

		if strings.TrimSpace(text) == "" || !enumCommentLine.MatchString(text) {
			return false
		}

		if strings.Contains(text, "dkp:enum-literal") {
			return enumWaiverWithReason.MatchString(text)
		}
	}

	return false
}

// exprText returns the SQL a `check` block's expr attribute carries.
//
// The EVALUATED string where hclsyntax can produce one, which is the whole point of the parse: a
// heredoc, a wrapped list and an escaped quote all arrive here as the same SQL that reaches SQLite.
// Where it cannot — an expression referencing something, which `expr` never is today — the raw
// source of the expression is scanned instead. That is the fail-closed direction: an expression this
// rule could not evaluate must not become an expression it did not read.
func exprText(attr *hclsyntax.Attribute, src []byte) string {
	if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && !v.IsNull() && v.Type().FriendlyName() == "string" {
		return v.AsString()
	}

	rng := attr.Expr.Range()
	if rng.Start.Byte < 0 || rng.End.Byte > len(src) || rng.Start.Byte > rng.End.Byte {
		return ""
	}

	return string(src[rng.Start.Byte:rng.End.Byte])
}

// stripSQLComments removes SQL comments from an expression before it is scanned.
//
// `state IN -- why these\n  ('a')` and `state IN /* set */ ('a')` are the same CHECK as the one
// written without them, so the scanner has to see the same thing. Removing the comments is what
// makes the token boundary between the keyword and its parenthesis insensitive to whatever a person
// wrote in the gap, rather than a list of gaps the pattern happens to allow — the enumeration
// failure this rule was bitten by three times.
//
// STRING CONTEXT IS DELIBERATELY NOT TRACKED, and the direction of that error is the reason it is
// safe. A `--` inside a value truncates the rest of the line, which can only REMOVE text from the
// scan — and never the quote that opens the literal it appears in, since that quote comes first and
// has already been counted. So this can lose the closing parenthesis of a list it has already
// entered; it cannot hide a vocabulary.
func stripSQLComments(s string) string {
	var out strings.Builder

	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "--"):
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				return out.String()
			}

			// The newline survives: it is whitespace the keyword and its parenthesis may be
			// separated by, and swallowing it would fuse the tokens on either side.
			out.WriteByte('\n')

			i += end + 1

		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return out.String()
			}

			out.WriteByte(' ')

			i += 2 + end + 2

		default:
			out.WriteByte(s[i])
			i++
		}
	}

	return out.String()
}

// quotedInList reports whether any `IN (…)` list in the expression holds a quoted value.
//
// Character-wise rather than by regex because the question is about the text BETWEEN the
// parentheses — `IN (0, 1)` is a boolean and must not match, a quote anywhere in a list is a
// vocabulary — and because nesting has to be counted rather than assumed away.
func quotedInList(sql string) bool {
	for i := 0; i < len(sql); {
		loc := enumInList.FindStringIndex(sql[i:])
		if loc == nil {
			return false
		}

		// Just past the opening parenthesis the match ended on.
		j := i + loc[1]
		depth := 1

		for ; j < len(sql) && depth > 0; j++ {
			switch sql[j] {
			case '(':
				depth++
			case ')':
				depth--
			case '\'', '"':
				return true
			}
		}

		i = j
	}

	return false
}
