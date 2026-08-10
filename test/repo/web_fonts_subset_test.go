package repo_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The vendored faces are DERIVED, and these are the gates that keep that honest.
//
// web_fonts_test.go asserts the faces are declared, licensed and reachable. It cannot see the thing
// issue #47 was actually about: the two .woff2 files are no longer byte-for-byte upstream. They are
// a Latin subset this repository generated, which means the provenance argument now rests on a
// derivation — scripts/subset-fonts.sh — rather than on "download the archive and compare".
//
// `make verify-fonts` is the real proof: it re-runs the derivation and diffs the bytes. It needs the
// network (a 33 MB archive and two wheels), so it runs nightly rather than in PR CI, where a GitHub
// outage would fail an unrelated parser fix. That leaves a gap between merge and the next nightly,
// and everything in this file exists to close the parts of it that can be checked offline in
// milliseconds:
//
//  1. the checksum table in web/src/assets/fonts/README.md still describes the committed bytes. A
//     table nobody checks is decoration, and decoration is exactly what a hand-edited font would
//     hide behind;
//  2. the provenance hashes in that table are the ones the script verifies its inputs against, so
//     "the README says the input was X" and "the script accepts input X" cannot drift apart;
//  3. the stylesheet's unicode-range still equals the range the bytes were cut to — a face that
//     claims coverage it does not have renders tofu, and one that under-claims stops downloading
//     for text it could have drawn;
//  4. no stylesheet asks for an OpenType feature the subset dropped. This one is the reason the
//     feature list can be tight: pyftsubset's default list does NOT include tnum, the mockups set
//     `font-variant-numeric: tabular-nums` 221 times, and a subset that silently unaligns every
//     balance column would look perfect in review and wrong in production.

const (
	subsetScriptRel = "scripts/subset-fonts.sh"
	mockupsDirRel   = "docs/design/mockups"
	fontsReadmeRel  = fontsDirRel + "/README.md"
)

var (
	// A provenance row: | `Inter-Regular-latin.woff2` | 18232 | `a375…` |
	checksumRowRe = regexp.MustCompile("(?m)^\\|\\s*`([^`]+)`\\s*\\|\\s*([0-9]+)\\s*\\|\\s*`([0-9a-f]{64})`\\s*\\|")
	// A shell constant assignment: NAME="value". Values in subset-fonts.sh are always double-quoted
	// and single-line, which the script's own comments say is deliberate — a constant a test cannot
	// read is a constant that drifts.
	shellConstRe = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)="([^"]*)"$`)
	// `unicode-range: U+0000-00FF, …;` — the value may wrap across lines, so match up to the `;`.
	unicodeRangeRe = regexp.MustCompile(`unicode-range:\s*([^;]+);`)
	// A CSS or inline-style font-variant declaration, in either spelling:
	//   font-variant-numeric: tabular-nums     (CSS, and HTML style="…")
	//   fontVariantNumeric: "tabular-nums"     (JSX inline style)
	fontVariantCSSRe = regexp.MustCompile(`font-variant(?:-numeric|-caps|-ligatures|-position|-alternates|-east-asian)?\s*:\s*([^;"'}]+)`)
	fontVariantJSXRe = regexp.MustCompile(`fontVariant(?:Numeric|Caps|Ligatures|Position|Alternates|EastAsian)?\s*:\s*["']([^"']+)["']`)
	// font-feature-settings names its tags directly: `font-feature-settings: "ss01" 1, "cv05";`.
	// It is read with a strict two-part tokeniser rather than one loose capture, because a capture
	// that ran to the end of the declaration would swallow the rest of an HTML line — and `class="card"`
	// is a quoted four-character token that looks exactly like a feature tag.
	featureSettingsPropRe = regexp.MustCompile(`(?:font-feature-settings|fontFeatureSettings)\s*:\s*["']?`)
	featureSettingsItemRe = regexp.MustCompile(`^\s*["']([a-z0-9]{4})["']\s*(?:on|off|[0-9]+)?\s*,?`)
)

// fontVariantFeatures maps the CSS font-variant-* keywords to the OpenType features they turn on.
// Only the keywords that map to a FEATURE are here: `normal`, `none` and the CSS-wide values turn
// nothing on and need no entry.
var fontVariantFeatures = map[string][]string{
	// font-variant-numeric
	"tabular-nums":       {"tnum"},
	"proportional-nums":  {"pnum"},
	"lining-nums":        {"lnum"},
	"oldstyle-nums":      {"onum"},
	"slashed-zero":       {"zero"},
	"ordinal":            {"ordn"},
	"diagonal-fractions": {"frac"},
	"stacked-fractions":  {"afrc"},
	// font-variant-caps
	"small-caps":       {"smcp"},
	"all-small-caps":   {"c2sc", "smcp"},
	"petite-caps":      {"pcap"},
	"all-petite-caps":  {"c2pc", "pcap"},
	"unicase":          {"unic"},
	"titling-caps":     {"titl"},
	"titling-capitals": {"titl"},
	// font-variant-ligatures
	"common-ligatures":        {"liga", "clig"},
	"discretionary-ligatures": {"dlig"},
	"historical-ligatures":    {"hlig"},
	"contextual":              {"calt"},
	// font-variant-position
	"super": {"sups"},
	"sub":   {"subs"},
}

// shellConst reads a constant out of a shell script, resolving the one form of expansion
// subset-fonts.sh uses: a second assignment that appends to the first (`X="${X},more"`).
func shellConst(t *testing.T, script, name string) string {
	t.Helper()

	value := ""
	found := false

	for _, m := range shellConstRe.FindAllStringSubmatch(script, -1) {
		if m[1] != name {
			continue
		}

		value = strings.ReplaceAll(m[2], "${"+name+"}", value)
		found = true
	}

	require.Truef(t, found, "%s declares no %s= constant — has the script been restructured?", subsetScriptRel, name)

	return value
}

// sha256OfRepoFile is the same hash the provenance table and the subsetting script speak in.
func sha256OfRepoFile(t *testing.T, abs string) (string, int64) {
	t.Helper()

	data, err := os.ReadFile(abs) //nolint:gosec // a path this test built from the repo root
	require.NoErrorf(t, err, "reading %s", abs)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), int64(len(data))
}

func TestWebFonts_ProvenanceTable_DescribesTheCommittedBytes(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	readme := readRepoFile(t, fontsReadmeRel)

	type row struct {
		bytes int64
		sha   string
	}

	rows := map[string]row{}

	for _, m := range checksumRowRe.FindAllStringSubmatch(readme, -1) {
		n, err := strconv.ParseInt(m[2], 10, 64)
		require.NoErrorf(t, err, "byte count %q in %s is not a number", m[2], fontsReadmeRel)
		rows[m[1]] = row{bytes: n, sha: m[3]}
	}

	require.NotEmptyf(t, rows, "%s has no `file | bytes | sha256` table — that table IS the provenance",
		fontsReadmeRel)

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(fontsDirRel)))
	require.NoError(t, err)

	seen := map[string]bool{}

	for _, entry := range entries {
		// The README cannot record its own hash, and nothing else in the directory is shipped.
		if entry.IsDir() || entry.Name() == "README.md" {
			continue
		}

		got, size := sha256OfRepoFile(t, filepath.Join(root, filepath.FromSlash(fontsDirRel), entry.Name()))

		want, ok := rows[entry.Name()]
		require.Truef(t, ok,
			"%s/%s is committed and embedded in every binary, but %s does not record it. "+
				"Run `make subset-fonts` and paste the printed table.",
			fontsDirRel, entry.Name(), fontsReadmeRel)

		require.Equalf(t, want.sha, got,
			"%s/%s does not match its recorded SHA-256.\n"+
				"  README %s\n  file   %s\n"+
				"Either the bytes were changed without regenerating the table, or they were edited by "+
				"hand — a derived font whose checksum nobody can re-derive is exactly what issue #47 "+
				"forbade. `make verify-fonts` re-runs the derivation.",
			fontsDirRel, entry.Name(), want.sha, got)

		require.Equalf(t, want.bytes, size, "%s/%s is %d bytes; %s says %d",
			fontsDirRel, entry.Name(), size, fontsReadmeRel, want.bytes)

		seen[entry.Name()] = true
	}

	// And the other direction: a row for a file nobody committed means the table describes a tree
	// that no longer exists, which is the same failure wearing a checksum's clothes.
	for name := range rows {
		require.Truef(t, seen[name],
			"%s records `%s`, but no such file is committed under %s", fontsReadmeRel, name, fontsDirRel)
	}
}

func TestWebFonts_ProvenanceInputs_MatchTheSubsettingScript(t *testing.T) {
	t.Parallel()

	readme := readRepoFile(t, fontsReadmeRel)
	script := readRepoFile(t, subsetScriptRel)

	// The script verifies each of these before it subsets anything. The README is where a human
	// checks them. If the two disagree, one of them is lying and there is no way to tell which.
	for _, name := range []string{
		"ARCHIVE_SHA256",
		"SRC_REGULAR_SHA256",
		"SRC_MEDIUM_SHA256",
		"SRC_LICENCE_SHA256",
		"FONTTOOLS_VERSION",
		"BROTLI_VERSION",
		"FEATURES",
		"UNICODES",
	} {
		value := shellConst(t, script, name)
		require.NotEmptyf(t, value, "%s in %s is empty", name, subsetScriptRel)
		require.Containsf(t, readme, value,
			"%s pins %s=%q, and %s never says so. The provenance table is only checkable if it "+
				"records the same inputs and parameters the script enforces.",
			subsetScriptRel, name, value, fontsReadmeRel)
	}
}

func TestWebFonts_StylesheetUnicodeRange_MatchesTheSubsetRange(t *testing.T) {
	t.Parallel()

	css := cssCommentRe.ReplaceAllString(readRepoFile(t, fontsCSSRel), "")
	want := normaliseUnicodeRange(shellConst(t, readRepoFile(t, subsetScriptRel), "UNICODES"))

	ranges := unicodeRangeRe.FindAllStringSubmatch(css, -1)
	require.Lenf(t, ranges, 2,
		"%s must declare a unicode-range on both faces. The faces are a subset; a face that does not "+
			"say what it covers invites the next person to assume it covers everything.", fontsCSSRel)

	for _, m := range ranges {
		require.Equalf(t, want, normaliseUnicodeRange(m[1]),
			"a unicode-range in %s does not match the range the bytes were cut to in %s.\n"+
				"  stylesheet %s\n  subset     %s\n"+
				"Claiming coverage the face lacks paints tofu; claiming less than it has stops the "+
				"download for text it could have drawn.",
			fontsCSSRel, subsetScriptRel, normaliseUnicodeRange(m[1]), want)
	}
}

// normaliseUnicodeRange puts the two spellings on the same footing: CSS wraps and puts a space after
// each comma, the pyftsubset flag is one unbroken token, and hex case is free in both.
func normaliseUnicodeRange(s string) string {
	var out []string

	for _, part := range strings.Split(s, ",") {
		if p := strings.ToUpper(strings.Join(strings.Fields(part), "")); p != "" {
			out = append(out, p)
		}
	}

	return strings.Join(out, ",")
}

func TestWebFonts_SubsetKeepsEveryFeatureTheStylesheetsAsk(t *testing.T) {
	t.Parallel()

	kept := map[string]bool{}
	for _, f := range strings.Split(shellConst(t, readRepoFile(t, subsetScriptRel), "FEATURES"), ",") {
		kept[strings.TrimSpace(f)] = true
	}

	require.NotEmpty(t, kept, "%s pins an empty --layout-features list", subsetScriptRel)

	// web/src is what ships. docs/design/mockups/ is what web/src gets built FROM — .claude/rules/
	// says to read the screen before writing it, so a feature the drawn screen uses is a feature the
	// shipped screen will use, and catching it here beats catching it after the subset shipped.
	requested := map[string][]string{} // feature tag -> the files that ask for it

	for _, dir := range []string{"web/src", mockupsDirRel} {
		for tag, files := range featuresRequestedUnder(t, dir) {
			requested[tag] = append(requested[tag], files...)
		}
	}

	// NO VACUOUS PASS. This test is only worth having because tabular-nums is really out there; if
	// the scan finds nothing, the scanner broke rather than the tree got clean.
	require.NotEmptyf(t, requested,
		"scanned web/src and %s and found no font-variant-* or font-feature-settings declaration at "+
			"all. The mockups carry hundreds — the scanner is broken, not the tree.", mockupsDirRel)

	for tag, files := range requested {
		require.Truef(t, kept[tag],
			"a stylesheet asks for the OpenType feature %q, which the Latin subset drops.\n"+
				"  asked for in: %s\n"+
				"  kept by %s: %s\n"+
				"The feature is in the upstream face and not in ours, so the declaration would do "+
				"nothing at all — silently. Add %q to FEATURES and run `make subset-fonts`, or drop "+
				"the declaration.",
			tag, strings.Join(dedupeStrings(files), ", "), subsetScriptRel,
			strings.Join(sortedKeys(kept), ","), tag)
	}
}

// featuresRequestedUnder returns every OpenType feature the stylesheets under dir turn on, mapped to
// the files that ask for it.
func featuresRequestedUnder(t *testing.T, dir string) map[string][]string {
	t.Helper()

	root := repoRoot(t)
	found := map[string][]string{}

	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "dist" {
				return filepath.SkipDir
			}

			return nil
		}

		switch filepath.Ext(d.Name()) {
		case ".css", ".html", ".ts", ".tsx":
		default:
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // walking a directory this test chose
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		body := cssCommentRe.ReplaceAllString(string(data), "")

		for _, re := range []*regexp.Regexp{fontVariantCSSRe, fontVariantJSXRe} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				for _, word := range strings.Fields(m[1]) {
					for _, tag := range fontVariantFeatures[strings.ToLower(strings.Trim(word, `"';`))] {
						found[tag] = append(found[tag], filepath.ToSlash(rel))
					}
				}
			}
		}

		for _, tag := range featureSettingsTags(body) {
			found[tag] = append(found[tag], filepath.ToSlash(rel))
		}

		return nil
	})
	require.NoErrorf(t, err, "walking %s", dir)

	return found
}

// featureSettingsTags returns every OpenType tag a `font-feature-settings` declaration in body turns
// on, consuming one well-formed `"tag" <value>` item at a time and stopping at anything else.
func featureSettingsTags(body string) []string {
	var tags []string

	for _, loc := range featureSettingsPropRe.FindAllStringIndex(body, -1) {
		rest := body[loc[1]:]

		for {
			m := featureSettingsItemRe.FindStringSubmatchIndex(rest)
			if m == nil {
				break
			}

			tags = append(tags, rest[m[2]:m[3]])
			rest = rest[m[1]:]
		}
	}

	return tags
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}

	var out []string

	for _, s := range in {
		if !seen[s] {
			seen[s] = true

			out = append(out, s)
		}
	}

	return out
}
