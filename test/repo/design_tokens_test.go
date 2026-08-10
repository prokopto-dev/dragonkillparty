package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Nocturne token contract, tested rather than trusted.
//
// docs/design/09-frontend-and-design-system.md §2 says of its own tables: "These values are the
// contract — a test asserts the shipped sheet still matches this table." This is that test, and it
// reads the DOCUMENT rather than a hard-coded copy of it, so the assertion cannot drift away from the
// normative text. Editing either side alone fails; editing both together is a design decision that
// shows up as a diff in both files.
//
// The values matter more than they look. Nocturne's ramps are generated in OKLCH on one shared
// lightness scale, so step N of any ramp carries the same visual weight as step N of any other; a
// hand-tweaked hex breaks that relationship in a way no screenshot review catches. --color-accent
// (#9184d9) and --color-accent-500 (#968ae0) are deliberately different values, which is exactly the
// kind of pair someone "fixes" by collapsing.
//
// Five assertions, each a different failure:
//
//   1. every token in §2 is in the sheet, byte-equal after whitespace normalisation
//   2. the --color-* namespace is CLOSED to §2 — one palette, so a new colour is a design decision
//   3. no token is declared twice, or the last declaration silently wins and (1) proves nothing
//   4. the /_design fixture renders every token and imports every component, so the vocabulary is
//      visible rather than merely present
//   5. no stylesheet declares a `transition` — Nocturne has no motion tokens and hover states snap

const (
	designDocRel   = "docs/design/09-frontend-and-design-system.md"
	tokensCSSRel   = "web/src/styles/tokens.css"
	baseCSSRel     = "web/src/styles/base.css"
	designRouteRel = "web/src/routes/design.tsx"
	componentsRel  = "web/src/components"

	// A floor on how many tokens §2 must yield, so a parser that quietly matches nothing cannot pass
	// every other assertion vacuously. §2 carries 66 today (6 roles + 27 ramp steps + 3 section +
	// 15 status + 3 type + 6 space + 3 radius + 3 elevation); the floor is deliberately below that so
	// adding a token to the table is not a test edit, while deleting the tables is.
	minimumContractTokens = 60
)

// readRepoFile lives in release_gates_test.go — same package, one helper.

// normaliseTokenValue collapses the spelling differences between a markdown table cell and a
// formatted stylesheet so the comparison is about the VALUE, not about whitespace.
//
// The document writes `rgba(0,0,0,.55)`; prettier-formatted CSS writes `rgba(0, 0, 0, 0.55)`. Both
// are the same colour, and a test that failed on the difference would be a test people delete. So:
// drop all whitespace, and restore the leading zero of a bare-dot decimal.
func normaliseTokenValue(v string) string {
	v = strings.Join(strings.Fields(v), "")
	v = strings.ReplaceAll(v, "(.", "(0.")
	v = strings.ReplaceAll(v, ",.", ",0.")

	return strings.ToLower(v)
}

var (
	// A markdown table cell holding a token name, with the backticks stripped by the caller.
	tokenNameRe = regexp.MustCompile(`^--[a-z0-9-]+$`)
	// A ramp-table column header: `--color-neutral-*`, or `--color-success-* (H 150)`.
	rampHeaderRe = regexp.MustCompile(`--([a-z0-9-]+)-\*`)
	// A ramp-table row label: the step number in the first cell.
	rampStepRe = regexp.MustCompile(`^[0-9]{3}$`)
	// A `--name: value` pair inside a single backticked span of prose.
	prosePairRe = regexp.MustCompile("`(--[a-z0-9-]+):\\s*([^`]+)`")
	// A CSS custom-property declaration.
	cssDeclRe = regexp.MustCompile(`(--[a-z0-9-]+)\s*:\s*([^;]+);`)
	// A CSS comment, in the one spelling CSS has.
	cssCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// A `transition` or `transition-*` property declaration.
	transitionRe = regexp.MustCompile(`(?m)(^|[;{\s])transition(-[a-z]+)?\s*:`)
)

// contractTokens parses §2 of the normative document into token -> value.
//
// §2 states its tokens in three shapes and all three are load-bearing, so all three are parsed:
//
//	a two-column table            | `--color-bg` | `#161826` |
//	a four-column ramp table      | 100 | `#f3f5fe` | `#f5f4ff` | `#f5f4ff` |
//	a backticked prose pair       `--color-section: #262a60`
//
// Fenced code blocks are skipped: §2's `--soft` / `--tint` block is a specification of the two
// helpers with a `<p>` placeholder for the percentage, not a literal declaration.
func contractTokens(t *testing.T, doc string) map[string]string {
	t.Helper()

	tokens := map[string]string{}
	inSection := false
	inFence := false

	// Ramp tables map a column index to a token-name prefix, taken from the header row.
	var rampPrefixes map[int]string

	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## ") {
			// §2 runs from its own heading to the next one.
			inSection = strings.HasPrefix(trimmed, "## 2. ")

			continue
		}

		if !inSection {
			continue
		}

		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence

			continue
		}

		if inFence {
			continue
		}

		if !strings.HasPrefix(trimmed, "|") {
			// Prose. A backticked `--name: value` span is a declaration; a bare `--name` mention is not.
			for _, m := range prosePairRe.FindAllStringSubmatch(trimmed, -1) {
				tokens[m[1]] = strings.TrimSpace(m[2])
			}

			rampPrefixes = nil

			continue
		}

		cells := tableCells(trimmed)
		if len(cells) == 0 {
			continue
		}

		// A markdown alignment row (|---|---|) separates the header from the body; it carries no data
		// and must not clear the ramp header we just learned.
		if isAlignmentRow(cells) {
			continue
		}

		// A ramp header: two or more columns naming `--prefix-*`.
		if prefixes := rampHeader(cells); len(prefixes) > 0 {
			rampPrefixes = prefixes

			continue
		}

		// A ramp body row, if we are inside a ramp table.
		if rampPrefixes != nil && rampStepRe.MatchString(cells[0]) {
			for col, prefix := range rampPrefixes {
				if col >= len(cells) {
					continue
				}
				tokens["--"+prefix+"-"+cells[0]] = cells[col]
			}

			continue
		}

		// An ordinary `| token | value |` row — possibly twice on the same line, as the type / space /
		// radius / elevation table does with an empty spacer column between the pairs.
		for i, cell := range cells {
			if !tokenNameRe.MatchString(cell) {
				continue
			}
			if i+1 < len(cells) && cells[i+1] != "" {
				tokens[cell] = cells[i+1]
			}
		}
	}

	return tokens
}

// tableCells splits a markdown table row into trimmed, backtick-stripped cells.
func tableCells(row string) []string {
	row = strings.Trim(row, "|")

	cells := strings.Split(row, "|")
	for i, cell := range cells {
		cells[i] = strings.TrimSpace(strings.ReplaceAll(cell, "`", ""))
	}

	return cells
}

// isAlignmentRow reports whether every cell is a markdown alignment marker (---, :---, ---:).
func isAlignmentRow(cells []string) bool {
	for _, cell := range cells {
		if strings.Trim(cell, "-: ") != "" {
			return false
		}
	}

	return true
}

// rampHeader returns column index -> token prefix for a ramp table's header row, or nil if this row
// is not one. A ramp header names at least two `--prefix-*` columns; a single one would be ambiguous
// with an ordinary prose mention.
func rampHeader(cells []string) map[int]string {
	prefixes := map[int]string{}

	for i, cell := range cells {
		if m := rampHeaderRe.FindStringSubmatch(cell); m != nil {
			prefixes[i] = m[1]
		}
	}

	if len(prefixes) < 2 {
		return nil
	}

	return prefixes
}

// cssCustomProperties parses a stylesheet into token -> value, and token -> declaration count.
func cssCustomProperties(css string) (map[string]string, map[string]int) {
	css = cssCommentRe.ReplaceAllString(css, "")

	values := map[string]string{}
	counts := map[string]int{}

	for _, m := range cssDeclRe.FindAllStringSubmatch(css, -1) {
		values[m[1]] = strings.TrimSpace(m[2])
		counts[m[1]]++
	}

	return values, counts
}

func TestDesignTokens_ShippedSheet_MatchesTheNormativeTable(t *testing.T) {
	t.Parallel()

	expected := contractTokens(t, readRepoFile(t, designDocRel))
	require.GreaterOrEqual(t, len(expected), minimumContractTokens,
		"parsed only %d tokens out of %s §2 — the tables moved or this parser is broken; it must not pass vacuously",
		len(expected), designDocRel)

	// Spot checks on the three table shapes, so a parser that handles one and silently drops another
	// cannot satisfy the floor above with two-thirds of the contract.
	require.Equal(t, "#161826", expected["--color-bg"], "the roles table must parse")
	require.Equal(t, "#b5afe8", expected["--color-accent-2-400"], "the ramp tables must parse")
	require.Equal(t, "#4c5397", expected["--color-section-ghost"], "the section prose must parse")
	require.Equal(t, "oklch(0.283 0.061 25)", expected["--color-danger-900"], "the status table must parse")
	require.Equal(t, "8px", expected["--radius-md"], "the four-column scale table must parse")

	actual, _ := cssCustomProperties(readRepoFile(t, tokensCSSRel))

	for _, token := range sortedKeys(expected) {
		got, ok := actual[token]
		require.Truef(t, ok,
			"%s declares no %s, which %s §2 lists as part of the contract", tokensCSSRel, token, designDocRel)
		require.Equalf(t, normaliseTokenValue(expected[token]), normaliseTokenValue(got),
			"%s value for %s does not match %s §2 (want %q, got %q)",
			tokensCSSRel, token, designDocRel, expected[token], got)
	}
}

func TestDesignTokens_ColourNamespace_IsClosedToTheNormativeTable(t *testing.T) {
	t.Parallel()

	expected := contractTokens(t, readRepoFile(t, designDocRel))
	require.GreaterOrEqual(t, len(expected), minimumContractTokens, "the §2 parser must not pass vacuously")

	actual, _ := cssCustomProperties(readRepoFile(t, tokensCSSRel))

	// "One palette." The dimensional scales are open by an explicit decision — §3 tells us to extend
	// them — but the colours are not: a hue that is not in §2 cannot be added to the sheet, because a
	// guild themes by overriding token VALUES and a colour with no rung in the table is a colour no
	// theme can reach. Derived colours (--soft-*, --tint-*, --scrim) live outside this namespace for
	// exactly that reason.
	for _, token := range sortedKeys(actual) {
		if !strings.HasPrefix(token, "--color-") {
			continue
		}
		_, ok := expected[token]
		require.Truef(t, ok,
			"%s declares %s, which is not in %s §2. The --color-* namespace is closed to that table: "+
				"add the token to §2 in the same change, or express the value as a --soft-*/--tint-* rung.",
			tokensCSSRel, token, designDocRel)
	}
}

func TestDesignTokens_EveryToken_IsDeclaredExactlyOnce(t *testing.T) {
	t.Parallel()

	_, counts := cssCustomProperties(readRepoFile(t, tokensCSSRel))

	for _, token := range sortedKeys(counts) {
		require.Equalf(t, 1, counts[token],
			"%s declares %s %d times. A second declaration silently wins, which would let the sheet "+
				"disagree with %s §2 while every value check still passed.",
			tokensCSSRel, token, counts[token], designDocRel)
	}
}

func TestDesignFixture_Route_RendersEveryToken(t *testing.T) {
	t.Parallel()

	tokens, _ := cssCustomProperties(readRepoFile(t, tokensCSSRel))
	fixture := readRepoFile(t, designRouteRel)

	for _, token := range sortedKeys(tokens) {
		// Anchored on the right so --soft-4 is not satisfied by --soft-45, and --space-1 not by
		// --space-16. Every token name in the fixture is a quoted string, so the boundary is real.
		re := regexp.MustCompile(regexp.QuoteMeta(token) + `([^0-9a-z-]|$)`)
		require.Truef(t, re.MatchString(fixture),
			"%s never names %s. Every token in %s must be visible on /_design — a token nobody can see "+
				"is a token nobody can check against a theme override.",
			designRouteRel, token, tokensCSSRel)
	}
}

func TestDesignFixture_Route_ImportsEveryComponent(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	fixture := readRepoFile(t, designRouteRel)

	entries, err := os.ReadDir(filepath.Join(root, componentsRel))
	require.NoError(t, err)

	found := 0

	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".tsx")
		if !ok {
			continue
		}
		// *.test-d.tsx holds type-level negative tests (@ts-expect-error blocks that tsc enforces).
		// They are deliberately imported by nothing — that is what keeps them out of the bundle — so
		// requiring the fixture to render them would be requiring the opposite of their purpose.
		if strings.HasSuffix(name, ".test-d") {
			continue
		}
		found++
		require.Containsf(t, fixture, "@/components/"+name,
			"%s does not import @/components/%s. Every component belongs on /_design: a component the "+
				"fixture does not render is one nobody looks at until a screen ships it.",
			designRouteRel, name)
	}

	require.NotZero(t, found, "found no components under %s — this check must not pass vacuously", componentsRel)
}

// cssClassSelectors returns every class name appearing in a stylesheet's SELECTORS. Declaration
// values are excluded on purpose: `calc(var(--radius-md) * 0.75)` and `1.5px` both contain a dot.
func cssClassSelectors(css string) []string {
	css = cssCommentRe.ReplaceAllString(css, "")

	classRe := regexp.MustCompile(`\.([a-zA-Z][a-zA-Z0-9_-]*)`)
	seen := map[string]bool{}

	// Flat CSS only: this system has no nested rules. Everything from the previous } or { up to the
	// next { is a selector list; at-rules are skipped.
	for _, chunk := range strings.FieldsFunc(css, func(r rune) bool { return r == '}' }) {
		selector, _, ok := strings.Cut(chunk, "{")
		if !ok {
			continue
		}

		selector = strings.TrimSpace(selector)
		if strings.HasPrefix(selector, "@") {
			continue
		}

		for _, m := range classRe.FindAllStringSubmatch(selector, -1) {
			seen[m[1]] = true
		}
	}

	out := make([]string, 0, len(seen))
	for class := range seen {
		out = append(out, class)
	}

	sort.Strings(out)

	return out
}

func TestDesignSystem_EveryClass_IsReferencedFromSource(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	sheets := []string{baseCSSRel}

	for _, dir := range []string{componentsRel, "web/src/routes"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		require.NoError(t, err)

		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".css") {
				sheets = append(sheets, filepath.ToSlash(filepath.Join(dir, entry.Name())))
			}
		}
	}

	// Every hand-written module the SPA ships, concatenated. A class must be named by at least one of
	// them, or the rule it carries is unreachable — which is how a stylesheet accumulates classes for
	// components that were renamed and screens that were never built.
	var source strings.Builder

	require.NoError(t, filepath.WalkDir(filepath.Join(root, "web/src"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		source.Write(data)

		return nil
	}))

	checked := 0

	for _, sheet := range sheets {
		for _, class := range cssClassSelectors(readRepoFile(t, sheet)) {
			checked++
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(class) + `([^a-zA-Z0-9_-]|$)`)
			require.Truef(t, re.MatchString(source.String()),
				"%s defines .%s but nothing under web/src names it. Reproduce the system's class names "+
					"rather than inventing parallel ones — and delete the ones no component uses.",
				sheet, class)
		}
	}

	require.NotZero(t, checked, "found no class selectors to check — this check must not pass vacuously")
}

func TestDesignSystem_Stylesheets_DeclareNoTransition(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	checked := 0

	require.NoError(t, filepath.WalkDir(filepath.Join(root, "web/src"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".css") {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		checked++

		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)

		css := cssCommentRe.ReplaceAllString(string(data), "")
		require.Falsef(t, transitionRe.MatchString(css),
			"%s declares a transition. Nocturne has no motion tokens and no `transition` anywhere — "+
				"hover states snap, and the feel is wrong with it (docs/design/09 §2, §4).", rel)

		return nil
	}))

	require.NotZero(t, checked, "found no stylesheets to check — this check must not pass vacuously")
}

// cssRule is one selector and the properties it declares.
type cssRule struct {
	sheet    string
	selector string
	props    map[string]string
}

// cssRules parses a flat stylesheet into one entry per selector (a comma-separated selector list
// becomes one entry each, which is how the cascade sees them).
func cssRules(sheet, css string) []cssRule {
	css = cssCommentRe.ReplaceAllString(css, "")

	var out []cssRule

	for _, block := range regexp.MustCompile(`(?s)([^{}]*)\{([^{}]*)\}`).FindAllStringSubmatch(css, -1) {
		selectors := strings.TrimSpace(block[1])
		if selectors == "" || strings.HasPrefix(selectors, "@") {
			continue
		}

		props := map[string]string{}

		for _, decl := range strings.Split(block[2], ";") {
			name, value, ok := strings.Cut(decl, ":")
			name = strings.TrimSpace(name)
			if !ok || name == "" || strings.HasPrefix(name, "--") {
				continue
			}
			props[name] = strings.TrimSpace(value)
		}

		if len(props) == 0 {
			continue
		}

		for _, selector := range strings.Split(selectors, ",") {
			selector = strings.Join(strings.Fields(selector), " ")
			if selector != "" {
				out = append(out, cssRule{sheet: sheet, selector: selector, props: props})
			}
		}
	}

	return out
}

// One simple selector: a pseudo-element, a pseudo-class (with its argument), a class, an id, an
// attribute, a type, or the universal selector.
var simpleSelectorRe = regexp.MustCompile(`::[a-z-]+|:[a-z-]+(\([^)]*\))?|\.[a-zA-Z][\w-]*|#[a-zA-Z][\w-]*|\[[^\]]*\]|[a-zA-Z][\w-]*|\*`)

// specificity returns the (id, class, type) triple for a selector, per CSS Selectors level 3.
//
// Simplifications, all safe for this system's flat selectors: `:not(...)` contributes its argument's
// specificity (correct), and `:has(...)` is treated the same way (also correct in level 4). `*`
// contributes nothing. No selector here nests further than that.
func specificity(selector string) [3]int {
	var out [3]int

	for _, tok := range simpleSelectorRe.FindAllString(selector, -1) {
		switch {
		case strings.HasPrefix(tok, "::"):
			out[2]++
		case strings.HasPrefix(tok, ":"):
			if open := strings.Index(tok, "("); open >= 0 {
				inner := specificity(tok[open+1 : len(tok)-1])
				out[0] += inner[0]
				out[1] += inner[1]
				out[2] += inner[2]

				continue
			}
			out[1]++
		case strings.HasPrefix(tok, "#"):
			out[0]++
		case strings.HasPrefix(tok, "."), strings.HasPrefix(tok, "["):
			out[1]++
		case tok == "*":
		default:
			out[2]++
		}
	}

	return out
}

func specificityLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}

	return false
}

// baseRowRule maps a pseudo-class suffix to the .table rule a spacer row must out-specify.
var spacerBaseRules = map[string]string{
	"":       ".table tbody tr",
	":hover": ".table tbody tr:hover",
	" td":    ".table td",
}

// TestDesignSystem_VirtualTableSpacer_OutSpecifiesTheBaseRowRule locks a bug that did not look like
// one: a rule written to suppress a base rule, which silently lost the cascade.
//
// VirtualTable.css shipped `.virtual-table-spacer { background: none }` — (0,1,0) against
// `.table tbody tr` at (0,1,2). No source order could make it win, so the spacer rows kept the fading
// row hairline and the hover paint the comment above them claimed to suppress, and `.table td`'s
// padding they also kept inflated every spacer past the height the virtualizer had computed, drifting
// the scroll height from the measured total. Nothing failed; the sheet just did not do what it said.
// Found in review.
//
// EQUAL specificity is a failure here, not a pass: the winner would then be decided by source order,
// and Vite orders CSS from the module import graph rather than from anything visible in these files.
// A rule that wins because of an import order nobody chose is not a rule.
//
// This is deliberately TARGETED at the spacer rather than a general cascade checker. Deciding whether
// two arbitrary selectors can match the same element needs the markup, not just the sheets — the first
// attempt at a general version compared only selectors that *subsume* each other and therefore skipped
// this very pair, which is exactly the wrong direction for "a shorter selector fails to win". The
// general guarantee is a computed-style assertion in a browser, tracked in issue #33.
func TestDesignSystem_VirtualTableSpacer_OutSpecifiesTheBaseRowRule(t *testing.T) {
	t.Parallel()

	tableRules := cssRules("Table.css", readRepoFile(t, componentsRel+"/Table.css"))
	spacerRules := cssRules("VirtualTable.css", readRepoFile(t, componentsRel+"/VirtualTable.css"))

	baseFor := func(selector string) ([3]int, bool) {
		for _, rule := range tableRules {
			if rule.selector == selector {
				return specificity(selector), true
			}
		}

		return [3]int{}, false
	}

	checked := 0

	for suffix, baseSelector := range spacerBaseRules {
		want := ".table tbody tr.virtual-table-spacer" + suffix

		var found *cssRule

		for i, rule := range spacerRules {
			if !strings.Contains(rule.selector, "virtual-table-spacer") {
				continue
			}
			if strings.HasSuffix(rule.selector, suffix) && (suffix != "" || !strings.ContainsAny(rule.selector, ":")) {
				if suffix == "" && strings.HasSuffix(rule.selector, " td") {
					continue
				}
				found = &spacerRules[i]

				break
			}
		}

		require.NotNilf(t, found,
			"VirtualTable.css has no spacer rule for %q. It must exist and be scoped through the base "+
				"row selector, e.g. `%s`.", baseSelector, want)

		baseSpec, ok := baseFor(baseSelector)
		require.Truef(t, ok, "Table.css no longer declares %q — this check has lost its subject", baseSelector)

		gotSpec := specificity(found.selector)
		checked++

		require.Falsef(t, specificityLess(gotSpec, baseSpec) || gotSpec == baseSpec,
			"VirtualTable.css `%s` (%v) does not out-specify Table.css `%s` (%v), so the spacer keeps the "+
				"base row paint. Scope it through the base rule's own selector: `%s`.",
			found.selector, gotSpec, baseSelector, baseSpec, want)
	}

	require.Equal(t, len(spacerBaseRules), checked, "every spacer rule must be checked")
}

// The var-resolving fidelity diff, and the one divergence it sanctions.
//
// Every component sheet in this system opens with "transcribed from mockups/nocturne/styles.css".
// That claim was prose until here: this compares the SHIPPED `.table` rules against the source
// sheet's, declaration by declaration, with `var(--token)` resolved on both sides — ours through
// web/src/styles/tokens.css, the mockup's through its own `:root`. A transcription is faithful when
// the resolved declarations are equal, which is the only sense in which one sheet full of tokens can
// be "the same" as another sheet full of different tokens.
//
// `.table` is the scope, and deliberately: it is the surface issue #34 changed. Extending the diff to
// every transcribed component sheet is worth doing and is tracked separately — a check that grew to
// cover everything at once would have to grandfather whatever it found, which is the opposite of what
// this one is for.
//
// THE DIVERGENCE IS THE POINT. docs/design/09 §4 sanctions exactly one break with the fading
// hairline: a sticky header sticks the <th> and not the <tr> that paints the rule, so a scrolling
// table moves the rule onto the cell and gives up the 48px end-fade. §4 also scopes it — to the
// virtualised viewport and nowhere else — and a scoped exception is only worth the name if something
// fails when it spreads. These two tests are that something:
//
//	1. Table.css still matches the source sheet exactly, so the ORDINARY table did not quietly pay
//	   for the scrolling one;
//	2. every rule in VirtualTable.css that overrides a `.table` rule is in the list below, with a
//	   reason, and is scoped through `.virtual-table`.
//
// A new override fails (1) or (2) until someone writes down why it exists. That is the difference
// between a sanctioned divergence and a surprise diff.

const mockupSheetRel = "docs/design/mockups/nocturne/styles.css"

// sanctionedTableDivergences: every place the shipped table deliberately disagrees with Nocturne.
//
// The key is the selector as VirtualTable.css writes it; the value is the reason, which is here so a
// reader of the failure does not have to go and find it. Both entries implement the single decision
// recorded in docs/design/09 §4 and issue #34 — they are one exception in two rules, because
// suppressing the row's paint and painting the cell instead is one move.
var sanctionedTableDivergences = map[string]string{
	".virtual-table .table thead th": "docs/design/09 §4, issue #34: the sticky header carries the rule, " +
		"as a solid strip that cannot fade at the ends, plus the opaque ground the data rows scroll under. " +
		"The source sheet has no scrolling table and so has nothing to say here.",
	".virtual-table .table thead tr": "docs/design/09 §4, issue #34: the row's own fading rule is suppressed " +
		"because a sticky <th> leaves its <tr> behind — the hairline would scroll away from the labels it " +
		"belongs to, and two hairlines a scroll apart is worse than the one that never fades.",
}

// varRefRe matches a `var(--token)` reference. No declaration in either sheet uses the fallback form,
// so a fallback is a change worth failing on rather than quietly resolving.
var varRefRe = regexp.MustCompile(`var\(\s*(--[a-z0-9-]+)\s*\)`)

// resolveVars expands every var() reference against a token map, repeatedly, because tokens reference
// tokens — `--soft-8` is a color-mix over `--color-text`.
//
// An unresolvable reference is left as written: it then fails the comparison naming the token, which
// is a better failure than a silent empty string. The depth bound is a cycle backstop; the real
// nesting here is two.
func resolveVars(value string, tokens map[string]string) string {
	for depth := 0; depth < 8 && strings.Contains(value, "var("); depth++ {
		replaced := varRefRe.ReplaceAllStringFunc(value, func(ref string) string {
			name := varRefRe.FindStringSubmatch(ref)[1]
			if resolved, ok := tokens[name]; ok {
				return resolved
			}

			return ref
		})

		if replaced == value {
			break
		}

		value = replaced
	}

	return value
}

// normaliseDeclaration collapses the spelling differences between two hand-written sheets — the
// mockup packs declarations onto one line, ours is prettier-formatted — so the comparison is about
// the VALUE. Same normalisation as normaliseTokenValue: whitespace is not a difference.
func normaliseDeclaration(value string) string {
	return normaliseTokenValue(value)
}

// tableRulesBySelector indexes a sheet's rules that target `.table`, keyed by selector.
func tableRulesBySelector(rules []cssRule) map[string]cssRule {
	out := map[string]cssRule{}

	for _, rule := range rules {
		if strings.Contains(rule.selector, ".table") {
			out[rule.selector] = rule
		}
	}

	return out
}

func TestDesignSystem_TableCSS_ResolvesToTheSourceSheet(t *testing.T) {
	t.Parallel()

	ourTokens, _ := cssCustomProperties(readRepoFile(t, tokensCSSRel))
	mockup := readRepoFile(t, mockupSheetRel)
	mockupTokens, _ := cssCustomProperties(mockup)

	ours := tableRulesBySelector(cssRules("Table.css", readRepoFile(t, componentsRel+"/Table.css")))
	theirs := tableRulesBySelector(cssRules("nocturne", mockup))

	// The source sheet's six .table rules. A floor rather than an equality, so a mockup that gains a
	// rule does not fail this test — but a parser that matched nothing cannot pass it either.
	require.GreaterOrEqual(t, len(theirs), 6,
		"parsed only %d .table rules out of %s — the sheet moved or cssRules is broken", len(theirs), mockupSheetRel)

	for _, selector := range sortedKeys(theirs) {
		theirRule := theirs[selector]

		ourRule, ok := ours[selector]
		require.Truef(t, ok,
			"Table.css declares no %q, which %s does. The class is transcribed from that sheet: reproduce the "+
				"rule, or record the omission in docs/design/09 §4 and in sanctionedTableDivergences.",
			selector, mockupSheetRel)

		for _, property := range sortedKeys(theirRule.props) {
			want := normaliseDeclaration(resolveVars(theirRule.props[property], mockupTokens))

			got, declared := ourRule.props[property]
			require.Truef(t, declared,
				"Table.css `%s` does not declare %s, which %s sets to %q", selector, property, mockupSheetRel,
				theirRule.props[property])

			require.Equalf(t, want, normaliseDeclaration(resolveVars(got, ourTokens)),
				"Table.css `%s { %s }` does not resolve to what %s paints.\n  ours:   %s\n  source: %s\n"+
					"The tokens differ by design; the resolved value must not. If this divergence is intended it "+
					"belongs in docs/design/09 §4 and in sanctionedTableDivergences, not in a passing diff.",
				selector, property, mockupSheetRel, got, theirRule.props[property])
		}
	}

	// The fading thead rule specifically, because it is the thing issue #34's exception gives up and
	// the assertion above would still pass if BOTH sheets lost it.
	thead, ok := ours[".table thead tr"]
	require.True(t, ok, "Table.css no longer declares `.table thead tr` — the ordinary table's fading rule is gone")
	require.Contains(t, thead.props["background"], "var(--hairline-fade)",
		"`.table thead tr` no longer fades over var(--hairline-fade). The sticky-header exception is scoped to "+
			"`.virtual-table` precisely so the ORDINARY table keeps this (docs/design/09 §4).")
}

func TestDesignSystem_VirtualTable_DivergesOnlyWhereSanctioned(t *testing.T) {
	t.Parallel()

	virtual := cssRules("VirtualTable.css", readRepoFile(t, componentsRel+"/VirtualTable.css"))

	found := map[string]bool{}

	for _, rule := range virtual {
		// The spacer rules suppress a base rule for a row that carries no data; they are covered by
		// TestDesignSystem_VirtualTableSpacer_OutSpecifiesTheBaseRowRule and are not a look divergence.
		if !strings.Contains(rule.selector, ".table") || strings.Contains(rule.selector, "virtual-table-spacer") {
			continue
		}

		_, sanctioned := sanctionedTableDivergences[rule.selector]
		require.Truef(t, sanctioned,
			"VirtualTable.css `%s` overrides a `.table` rule that %s paints, and nothing records why. "+
				"Nocturne's fading hairline has exactly one sanctioned exception (docs/design/09 §4, issue #34). "+
				"Add this one to §4 and to sanctionedTableDivergences with its reason, or scope the rule so the "+
				"base table's look is unchanged.",
			rule.selector, mockupSheetRel)

		found[rule.selector] = true
	}

	for _, selector := range sortedKeys(sanctionedTableDivergences) {
		require.Truef(t, found[selector],
			"sanctionedTableDivergences lists `%s`, which VirtualTable.css no longer declares. A divergence list "+
				"that outlives its divergence is how the next one gets waved through: delete the entry, and delete "+
				"the exception from docs/design/09 §4 if this was the last of it.", selector)

		// Scope is the exception, not a detail of it. `.virtual-table` is the scroll viewport; a rule
		// that reached a `.table` outside one would take the end-fade off every table in the product.
		require.Truef(t, strings.HasPrefix(selector, ".virtual-table "),
			"sanctionedTableDivergences lists `%s`, which is not scoped through `.virtual-table`. §4 sanctions "+
				"the exception for the SCROLLING case only; unscoped, it applies to every table there is.", selector)
	}
}

// The DS001 / DS002 negative fixtures. AGENTS.md: add a test when you add a gate, not when you add a
// feature — a gate nobody proved can fire is a gate that quietly stops firing.
//
// These rules exist because canonical §17 promised them and nothing delivered: it names ESLint as the
// mechanism, ESLint does not lint CSS, and `web/src/styles/base.css` duly shipped a
// `text-underline-offset: 3px` that no gate caught. Both directions are asserted in the same run,
// because a ban-only test passes just as happily against a rule that fires on every stylesheet — and
// the first person to hit that reaches for `--no-verify` rather than for the rule id.

func TestRepoGates_RawHexInComponentCSS_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "web/src/styles/tokens.css", ":root {\n  --color-accent: #9184d9;\n}\n")
	writeRepoFile(t, tree, "web/src/components/Rogue.css", `/* A #ff0000 in prose is documentation, not a violation. */
.rogue {
  color: #ff0000;
}
`)

	out, code := runGateScript(t, script, tree)

	require.NotZero(t, code, "the gate accepted a raw hex colour in component CSS\n%s", out)
	require.Contains(t, out, "[DS001]", "%s", out)
	require.Contains(t, out, "Rogue.css", "%s", out)
	require.NotContains(t, out, "in prose is documentation",
		"DS001 fired on the comment explaining it; CSS comment lines must be stripped\n%s", out)
}

func TestRepoGates_RawPxInComponentCSS_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	writeRepoFile(t, tree, "web/src/styles/tokens.css", ":root {\n  --space-1: 2.8px;\n}\n")
	writeRepoFile(t, tree, "web/src/styles/base.css", `/*
 * A 1px accent border, described over 48px of prose, is not a violation.
 */
a {
  text-underline-offset: 3px;
}
`)

	out, code := runGateScript(t, script, tree)

	// base.css is deliberately the fixture: canonical §17 scopes the exemption to the TOKEN LAYER,
	// which is tokens.css alone. .claude/rules/web.md's looser "outside web/src/styles/" wording is
	// what let the real slip through, and this assertion is what stops the gate being narrowed to it.
	require.NotZero(t, code, "the gate accepted a raw px in base.css — the exemption is tokens.css, not all of web/src/styles\n%s", out)
	require.Contains(t, out, "[DS002]", "%s", out)
	require.Contains(t, out, "base.css", "%s", out)
	require.NotContains(t, out, "described over 48px of prose",
		"DS002 fired on the comment explaining it; CSS comment lines must be stripped\n%s", out)
}

func TestRepoGates_TokenSheetAndTokenisedCSS_PassGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")
	tree := t.TempDir()

	// The allowlist half, and the half that matters. tokens.css is where every hex and every px in
	// the product lives; a rule that flagged them would make the design system unimplementable, and
	// widening DS001/DS002 to "every stylesheet" would satisfy both fixtures above.
	writeRepoFile(t, tree, "web/src/styles/tokens.css", `:root {
  --color-bg: #161826;
  --space-1: 2.8px;
  --hairline: 1px;
  --shadow-sm: 0 0 0 1px #3f424d;
}
`)
	writeRepoFile(t, tree, "web/src/components/Card.css", `.card {
  background: var(--color-bg);
  padding: var(--space-1);
  border: var(--hairline) solid transparent;
}
`)

	out, code := runGateScript(t, script, tree)

	require.Zero(t, code, "the gate rejected a correctly tokenised sheet\n%s", out)
	require.NotContains(t, out, "[DS001]", "%s", out)
	require.NotContains(t, out, "[DS002]", "%s", out)
}

// sortedKeys returns a map's keys in a stable order, so a failure names the same token every run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}
