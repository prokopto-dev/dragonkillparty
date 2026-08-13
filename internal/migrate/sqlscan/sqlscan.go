// Package sqlscan reads the TEXT of a goose migration: where its Down block begins, and what each
// backtick in it belongs to.
//
// TWO CALLERS, ONE SCANNER, and that is the whole reason this is a package rather than a file:
//
//   - internal/migrate/migrationfmt rewrites a freshly generated migration, and must REFUSE when a
//     backtick is inside a string literal — rewriting it would change the value db/schema.hcl asked
//     for, in a file nothing may edit afterwards.
//   - internal/repogate fails MIG001 on DDL inside a Down block and MIG002 on a backtick-quoted
//     identifier, and both questions are about the same two properties.
//
// The second caller is why the first one's answer had to become importable. MIG002 used to fail on
// ANY backtick, which made the generator's refusal message untrue: it offers a hand-fix path (issue
// #138) that the gate then refused to let anyone land. A gate and a generator disagreeing about
// what a backtick is, is not a thing two implementations can be trusted to keep in step, so there
// is one.
//
// IT IS A SCANNER, NOT A PATTERN, for the reason migrationfmt's package doc gives at length: a
// SQLite string literal may span physical lines, a doubled quote inside one is an escape rather
// than a close, and a comment is not SQL. Every one of those is invisible to anything reading a
// line at a time, and each of them is a way for a backtick to be classified wrongly.
package sqlscan

import (
	"errors"
	"fmt"
	"strings"
)

// DownMarker is the line goose uses to separate a migration's Up and Down halves. The match is
// whole-line and exact: a line carrying trailing text is not a marker, which is also how goose
// itself reads it.
const DownMarker = "-- +goose Down"

// ErrBacktickInStringLiteral is returned instead of a rewrite whose result would be silently wrong.
//
// A backtick INSIDE a single-quoted string wrapping something identifier-shaped is the one input
// the identifier rewrite would corrupt: `CHECK (a <> '` + "`abc`" + `')` would become
// `CHECK (a <> '"abc"')` — still valid SQL, still applies cleanly, and now meaning something
// different, with nothing in the diff to suggest a generator did it. Refusing costs a re-run;
// guessing costs wrong data in a file nothing may rewrite afterwards.
var ErrBacktickInStringLiteral = errors.New("backtick inside a string literal")

// LiteralBacktick locates one occurrence of ErrBacktickInStringLiteral, so the refusal can quote the
// offending line rather than make the reader find it.
//
// OpenedAt is where the literal STARTED, which is not always where the backtick is: a SQLite string
// literal may span physical lines, and in that case the line to look at is the one that opened the
// quote. They are reported separately when they differ.
type LiteralBacktick struct {
	Line     int
	OpenedAt int
	Text     string
}

func (e LiteralBacktick) Error() string {
	if e.OpenedAt != e.Line {
		return fmt.Sprintf("line %d (literal opened at line %d): %s: %s",
			e.Line, e.OpenedAt, ErrBacktickInStringLiteral, strings.TrimSpace(e.Text))
	}

	return fmt.Sprintf("line %d: %s: %s", e.Line, ErrBacktickInStringLiteral, strings.TrimSpace(e.Text))
}

func (e LiteralBacktick) Unwrap() error { return ErrBacktickInStringLiteral }

// Position is one line of a migration, as a finding names it: 1-based, with the line verbatim.
type Position struct {
	Line int
	Text string
}

// DownBlockStart returns the 1-based line number of the whole-line goose Down marker, or 0 when the
// file has none.
//
// Whole-line and exact, matching migrationfmt's truncation and goose's own reading. A migration with
// no marker has no Down block, and a rule scoped to one must then match nothing rather than
// everything — the difference between MIG001 as written and MIG001 as documented (issue #137).
func DownBlockStart(lines []string) int {
	for i, line := range lines {
		if line == DownMarker {
			return i + 1
		}
	}

	return 0
}

// RewriteBackticks rewrites every backtick-quoted bare identifier to a double-quoted one, and
// refuses if a backtick appears inside a string literal.
func RewriteBackticks(src string) (string, error) {
	return scan(src, true, nil)
}

// BackticksOutsideStringLiterals returns one Position per LINE carrying a backtick that is not
// inside a string literal — the backticks that quote an identifier, and therefore exactly what
// MIG002 fails on.
//
// PER LINE rather than per backtick, because that is the shape a gate reports in and the shape the
// rule reported in when it was a grep: `CREATE TABLE ` + "`a` (`b` text)" is one mistake, not three.
//
// It never returns an error. A backtick inside a literal is not a finding here — it is the input
// migrationfmt refuses on, and a file carrying one must remain landable once its author has fixed
// the identifiers by hand (issue #138).
func BackticksOutsideStringLiterals(src string) []Position {
	var (
		hits []Position
		last int
	)

	// The error is unreachable: refuse is false, and scan returns an error only on the refusal path.
	_, _ = scan(src, false, func(line int, text string) {
		if line == last {
			return
		}

		last = line
		hits = append(hits, Position{Line: line, Text: text})
	})

	return hits
}

// sqlState is where the scanner is: which construct the byte it is looking at belongs to.
type sqlState int

const (
	stateSQL          sqlState = iota // ordinary SQL, where a backtick quotes an identifier
	stateString                       // '…' — a VALUE. A backtick here is data, and is refused
	stateQuotedIdent                  // "…" or […] — already correctly quoted; left alone
	stateLineComment                  // -- … to end of line
	stateBlockComment                 // /* … */, which may span lines
)

// scan walks src once, rewriting backtick-quoted bare identifiers as it goes and reporting every
// backtick that is NOT inside a string literal to outside.
//
// ONE PASS WITH STATE, rather than a pattern per line, because the thing being decided is what the
// backtick belongs to and that is not a property of its line:
//
//   - A string literal may span physical lines. A backtick on the second line of a multiline DEFAULT
//     looks exactly like an identifier quote to anything reading one line at a time, so the shell
//     version rewrote it — changing the value the schema asked for AND removing the backtick that
//     MIG002 would otherwise have caught it by. That input is refused, and it is the case the
//     `_MultilineLiteral_` tests pin.
//   - A doubled quote inside a literal is an escaped quote, not two literals. Treating it as a close
//     followed by an open puts everything after it in the wrong state.
//   - Comments are not SQL. The Down block migrationfmt appends contains the apostrophe in
//     "RAISE()'s message"; a scanner that took it for an opening quote would refuse every migration
//     in the repository, and the fixed-point test over db/migrations-sqlite is what would say so.
//     Backticks inside a comment are still rewritten, because a comment cannot change meaning — and
//     they are still reported to outside, because MIG002 is a rule about the bytes in the file.
//   - A backtick inside a double-quoted identifier is left exactly as it is. The shell version would
//     have rewritten a `"a` + "`b`" + `c"` pair into `"a"b"c"`, silently splitting one identifier
//     into three tokens. Leaving it means MIG002 fails the file and a human looks at it, which is
//     the correct outcome for an identifier nobody meant to write.
//
// refuse is what separates the two callers, and it is the ONLY thing that does. The generator stops
// at the first backtick inside a literal because it is about to write bytes to disk; the gate keeps
// walking, because a file may hold both a refused literal AND an identifier quote and the second one
// is still a finding.
func scan(src string, refuse bool, outside func(line int, text string)) (string, error) {
	var b strings.Builder

	b.Grow(len(src))

	state := stateSQL
	line := 1
	openedAt := 0
	closer := byte(0)

	for i := 0; i < len(src); {
		c := src[i]
		if c == '\n' {
			line++
		}

		switch state {
		case stateString:
			switch {
			case c == '`':
				if refuse {
					return "", LiteralBacktick{Line: line, OpenedAt: openedAt, Text: lineAt(src, i)}
				}
			case c == '\'' && i+1 < len(src) && src[i+1] == '\'':
				// The doubled-quote escape: one quote of data, not a close followed by an open.
				b.WriteString("''")
				i += 2

				continue
			case c == '\'':
				state = stateSQL
			}

			b.WriteByte(c)
			i++

		case stateQuotedIdent:
			switch {
			case c == closer && closer == '"' && i+1 < len(src) && src[i+1] == '"':
				b.WriteString(`""`)
				i += 2

				continue
			case c == closer:
				state = stateSQL
			case c == '`':
				report(outside, line, src, i)
			}

			b.WriteByte(c)
			i++

		case stateLineComment:
			if c == '\n' {
				state = stateSQL
				b.WriteByte(c)
				i++

				continue
			}

			i += writeBacktickOrByte(&b, src, i, line, outside)

		case stateBlockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state = stateSQL
				b.WriteString("*/")
				i += 2

				continue
			}

			i += writeBacktickOrByte(&b, src, i, line, outside)

		case stateSQL:
			switch {
			case c == '\'':
				state, openedAt = stateString, line
			case c == '"':
				state, closer = stateQuotedIdent, '"'
			case c == '[':
				state, closer = stateQuotedIdent, ']'
			case c == '-' && i+1 < len(src) && src[i+1] == '-':
				state = stateLineComment
				b.WriteString("--")
				i += 2

				continue
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = stateBlockComment
				b.WriteString("/*")
				i += 2

				continue
			default:
				i += writeBacktickOrByte(&b, src, i, line, outside)

				continue
			}

			b.WriteByte(c)
			i++
		}
	}

	return b.String(), nil
}

// writeBacktickOrByte writes the construct starting at src[i] and returns how many bytes it
// consumed: a backtick-quoted BARE IDENTIFIER becomes a double-quoted one, and anything else is
// copied through unchanged.
//
// The closing backtick must be on the same line, and the content must be an identifier and nothing
// else. Both restrictions keep the rewrite to the case Atlas actually emits: a pair spanning lines,
// or wrapping an expression, is something this generator did not write and must not reinterpret.
func writeBacktickOrByte(b *strings.Builder, src string, i, line int, outside func(int, string)) int {
	if src[i] != '`' {
		b.WriteByte(src[i])

		return 1
	}

	report(outside, line, src, i)

	end := strings.IndexAny(src[i+1:], "`\n")
	if end < 0 || src[i+1+end] != '`' {
		b.WriteByte('`')

		return 1
	}

	name := src[i+1 : i+1+end]
	if !isBareIdentifier(name) {
		b.WriteByte('`')

		return 1
	}

	b.WriteByte('"')
	b.WriteString(name)
	b.WriteByte('"')

	return end + 2
}

// report hands one outside-a-literal backtick to the caller that asked for them, with the whole
// physical line it sits on.
func report(outside func(int, string), line int, src string, i int) {
	if outside == nil {
		return
	}

	outside(line, lineAt(src, i))
}

// isBareIdentifier reports whether s is what Atlas puts between backticks: a table, column or index
// name, and nothing that could be an expression.
func isBareIdentifier(s string) bool {
	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}

	return s != ""
}

// lineAt returns the whole physical line containing src[i], so a finding can quote it.
func lineAt(src string, i int) string {
	start := strings.LastIndexByte(src[:i], '\n') + 1

	end := strings.IndexByte(src[i:], '\n')
	if end < 0 {
		return src[start:]
	}

	return src[start : i+end]
}
