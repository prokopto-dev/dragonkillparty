package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The self-hosted type, tested rather than trusted.
//
// docs/design/09-frontend-and-design-system.md §3 is normative: "Inter at 400 (body) and 500
// (headings). Loaded self-hosted, not through the Google Fonts @import the source sheet uses — the
// binary serves the SPA offline and a render-blocking third-party request contradicts that."
//
// Nothing else in the repository can check this. scripts/licence-gate.sh and
// scripts/third-party-notices.sh both read the GO module graph (`go list -deps ./cmd/dkp`), and a
// font is not a module, so a vendored face is invisible to the licence machinery — it can be added,
// or deleted, or left unattributed, and every existing gate stays green. These tests are that gate.
//
// Five failures, each one a real way this regresses:
//
//  1. a declared face points at a file nobody committed — the browser silently falls back and the
//     product renders in system-ui on the one deployment target that matters, a volunteer's server
//     talking to a browser that has never seen Inter;
//  2. a committed face nothing declares — ~110 KB of dead weight embedded in the binary;
//  3. base.css stops importing fonts.css, so the faces exist and are never loaded;
//  4. the token stacks stop naming the family the faces declare, which is the same failure wearing
//     a token's clothes;
//  5. the OFL stops travelling with the font. The licence requires the copyright notice and licence
//     text to ship with the Font Software, and the Font Software ships inside the binary.
//
// The sixth is WEB003 in scripts/repo-gates.sh, whose negative fixture is at the bottom of this file.

const (
	fontsCSSRel = "web/src/styles/fonts.css"
	fontsDirRel = "web/src/assets/fonts"
	noticeRel   = "NOTICE"
	noticesRel  = "THIRD_PARTY_NOTICES.txt"
)

var (
	// A `src: url("…") format("woff2")` declaration inside an @font-face block.
	fontSrcRe = regexp.MustCompile(`src:\s*url\(\s*["']?([^"')]+)["']?\s*\)`)
	// A `font-weight: 400` declaration.
	fontWeightRe = regexp.MustCompile(`font-weight:\s*([0-9]+)\s*;`)
	// A `font-family: "Inter"` declaration, capturing the quoted family name.
	fontFamilyRe = regexp.MustCompile(`font-family:\s*["']([^"']+)["']\s*;`)
	// The first family of a token's font stack: `--font-body: "Inter", system-ui, sans-serif;`.
	fontStackRe = regexp.MustCompile(`--font-(?:heading|body):\s*["']([^"']+)["']`)
)

// fontFaceBlock is one @font-face rule, reduced to the three things that decide whether it renders.
type fontFaceBlock struct {
	family string
	weight string
	// src is the url() target, as written — a path relative to the stylesheet.
	src string
}

// parseFontFaces returns every @font-face block in a stylesheet, with comments stripped so a rule
// quoted in prose is not mistaken for a declared one.
func parseFontFaces(css string) []fontFaceBlock {
	css = cssCommentRe.ReplaceAllString(css, "")

	var faces []fontFaceBlock

	for _, chunk := range strings.Split(css, "@font-face")[1:] {
		body, _, found := strings.Cut(chunk, "}")
		if !found {
			continue
		}

		face := fontFaceBlock{}
		if m := fontFamilyRe.FindStringSubmatch(body); m != nil {
			face.family = m[1]
		}

		if m := fontWeightRe.FindStringSubmatch(body); m != nil {
			face.weight = m[1]
		}

		if m := fontSrcRe.FindStringSubmatch(body); m != nil {
			face.src = m[1]
		}

		faces = append(faces, face)
	}

	return faces
}

func TestWebFonts_DeclaredFaces_ResolveToCommittedFiles(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	faces := parseFontFaces(readRepoFile(t, fontsCSSRel))

	require.Lenf(t, faces, 2,
		"%s must declare exactly the two faces §3 names — 400 for body and 500 for headings. "+
			"A third weight is a design change: hierarchy in Nocturne is size and space, not weight.", fontsCSSRel)

	byWeight := map[string]fontFaceBlock{}

	for _, face := range faces {
		require.NotEmptyf(t, face.src, "an @font-face in %s declares no src url()", fontsCSSRel)
		require.NotEmptyf(t, face.weight, "the @font-face for %s declares no font-weight", face.src)
		require.Equalf(t, "Inter", face.family,
			"the @font-face for %s declares family %q — §3 says Inter", face.src, face.family)

		// The url() is relative to the stylesheet, which is how Vite resolves it at build time.
		abs := filepath.Join(root, filepath.Dir(filepath.FromSlash(fontsCSSRel)), filepath.FromSlash(face.src))
		info, err := os.Stat(abs)
		require.NoErrorf(t, err,
			"%s declares src url(%s) but no such file is committed — the browser falls back to "+
				"system-ui and the product does not render in its own typeface", fontsCSSRel, face.src)
		require.NotZerof(t, info.Size(), "%s is empty", face.src)

		// No absolute URL. WEB003 in scripts/repo-gates.sh says the same thing to the whole tree; this
		// says it about the one file where the mistake is most tempting.
		require.NotContainsf(t, face.src, "//",
			"%s loads a face from another origin — §3 requires self-hosted faces so the binary works offline",
			fontsCSSRel)

		byWeight[face.weight] = face
	}

	require.Containsf(t, byWeight, "400", "%s declares no weight-400 face — §3 sets body at 400", fontsCSSRel)
	require.Containsf(t, byWeight, "500", "%s declares no weight-500 face — §3 sets every heading at 500", fontsCSSRel)
}

func TestWebFonts_CommittedFaces_AreAllDeclared(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	css := cssCommentRe.ReplaceAllString(readRepoFile(t, fontsCSSRel), "")

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(fontsDirRel)))
	require.NoErrorf(t, err, "%s must exist — it holds the vendored faces", fontsDirRel)

	found := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".woff2") {
			continue
		}

		found++

		require.Containsf(t, css, entry.Name(),
			"%s/%s is committed and embedded in the binary but no @font-face in %s names it — "+
				"an undeclared face is ~110 KB of dead weight in every release",
			fontsDirRel, entry.Name(), fontsCSSRel)
	}

	require.NotZerof(t, found, "%s holds no .woff2 — §3 requires the faces be self-hosted", fontsDirRel)
}

func TestWebFonts_BaseStylesheet_ImportsTheFaces(t *testing.T) {
	t.Parallel()

	base := cssCommentRe.ReplaceAllString(readRepoFile(t, baseCSSRel), "")

	require.Containsf(t, base, `@import "./fonts.css"`,
		"%s does not import fonts.css — the faces would be committed, licensed, embedded and never loaded",
		baseCSSRel)

	// Order matters as much as presence: CSS requires @import at the top of the sheet, and a face
	// declared after the first rule that uses it paints one frame in the fallback.
	fonts := strings.Index(base, `@import "./fonts.css"`)
	tokens := strings.Index(base, `@import "./tokens.css"`)
	require.NotEqual(t, -1, tokens, "%s no longer imports tokens.css", baseCSSRel)
	require.Lessf(t, fonts, tokens, "%s imports fonts.css after tokens.css — the faces must be known first", baseCSSRel)
}

func TestWebFonts_TokenStacks_NameTheDeclaredFamily(t *testing.T) {
	t.Parallel()

	faces := parseFontFaces(readRepoFile(t, fontsCSSRel))
	require.NotEmpty(t, faces, "no @font-face to compare the token stacks against")

	tokens := cssCommentRe.ReplaceAllString(readRepoFile(t, tokensCSSRel), "")
	stacks := fontStackRe.FindAllStringSubmatch(tokens, -1)

	require.Lenf(t, stacks, 2, "%s must declare --font-heading and --font-body with a quoted first family", tokensCSSRel)

	for _, stack := range stacks {
		require.Equalf(t, faces[0].family, stack[1],
			"a font token leads with %q but the vendored faces declare %q — the stack would fall "+
				"straight through to system-ui with the faces sitting unused in the binary",
			stack[1], faces[0].family)
	}
}

func TestWebFonts_Licence_TravelsWithTheFont(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// The OFL requires the copyright notice and the licence text to accompany the Font Software.
	// Committed beside the binaries is the first half of that.
	ofl := readRepoFile(t, fontsDirRel+"/OFL.txt")
	require.Contains(t, ofl, "SIL OPEN FONT LICENSE Version 1.1",
		"the licence beside the vendored faces is not the OFL 1.1")
	require.Contains(t, ofl, "Copyright", "the vendored licence carries no copyright notice")

	notice := readRepoFile(t, noticeRel)
	notices := readRepoFile(t, noticesRel)

	require.Contains(t, notice, "SIL Open Font License",
		"NOTICE does not record the vendored font's licence — NOTICE is where a downstream packager looks")

	// And the second half: the font is embedded in the binary by internal/ui, so the notices file the
	// release archives attach is where the obligation actually lands.
	require.Contains(t, notices, "SIL OPEN FONT LICENSE Version 1.1",
		"THIRD_PARTY_NOTICES.txt does not reproduce the OFL — run `make third-party-notices`")

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(fontsDirRel)))
	require.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".woff2") {
			continue
		}

		rel := fontsDirRel + "/" + entry.Name()
		require.Containsf(t, notices, rel,
			"THIRD_PARTY_NOTICES.txt does not name %s — add it to VENDORED_ASSETS in "+
				"scripts/third-party-notices.sh and regenerate", rel)
		require.Containsf(t, notice, entry.Name(),
			"NOTICE does not name %s — a vendored asset nobody attributes is the whole failure mode", rel)
	}
}

// TestRepoGates_ThirdPartyAssetRequest_FailsGate covers WEB003.
//
// The line this gate exists to stop is not hypothetical: it is committed in this repository, at the
// top of docs/design/mockups/nocturne/styles.css, and the mockups are transcribed on purpose. Copying
// `@import url('https://fonts.googleapis.com/…')` along with the rest looks like fidelity, works
// perfectly on the machine of whoever wrote it, and fails only on a LAN-only server — where the
// request hangs, the page blocks, and the officer sees the wrong typeface. Every fixture below is a
// shape someone would actually write.
func TestRepoGates_ThirdPartyAssetRequest_FailsGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t, "repo-gates.sh")

	cases := []struct {
		name string
		rel  string
		body string
	}{
		{
			name: "google fonts @import in a stylesheet",
			rel:  "web/src/styles/fonts.css",
			body: "@import url('https://fonts.googleapis.com/css2?family=Inter:wght@400;500&display=swap');\n",
		},
		{
			name: "font-face src on a third-party origin",
			rel:  "web/src/styles/fonts.css",
			body: "@font-face {\n  font-family: \"Inter\";\n  src: url(\"https://fonts.gstatic.com/s/inter/v13/inter.woff2\") format(\"woff2\");\n}\n",
		},
		{
			name: "protocol-relative CDN url",
			rel:  "web/src/styles/icons.css",
			body: ".icon {\n  background-image: url(//cdn.jsdelivr.net/npm/phosphor-icons/src/regular/sword.svg);\n}\n",
		},
		{
			name: "render-blocking link in the document head",
			rel:  "web/index.html",
			body: "<!doctype html>\n<html><head>\n<link rel=\"stylesheet\" href=\"https://fonts.googleapis.com/css2?family=Inter\">\n</head><body></body></html>\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := t.TempDir()
			// web/src must exist for the gate to run at all — a tree with no SPA skips it, correctly.
			writeRepoFile(t, tree, "web/src/styles/base.css", "body { color: var(--color-text); }\n")
			writeRepoFile(t, tree, tc.rel, tc.body)

			out, code := runGateScript(t, script, tree)

			require.NotZerof(t, code, "the gate accepted a third-party asset request:\n%s", out)
			require.Containsf(t, out, "WEB003",
				"the gate failed, but not with WEB003 — assert on the rule id, not the exit code:\n%s", out)
		})
	}
}

// TestRepoGates_SelfHostedFontStylesheet_PassesGate is the other half of the pair, and it is not
// decoration: a WEB003 that fired on every stylesheet would satisfy every fixture above while making
// the real tree unshippable, and the first person to hit it would delete the rule. The committed
// fonts.css documents the banned @import in prose — a gate that cannot tell prose about a rule from
// a breach of it is a gate people route around.
func TestRepoGates_SelfHostedFontStylesheet_PassesGate(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	writeRepoFile(t, tree, "web/src/styles/fonts.css", readRepoFile(t, fontsCSSRel))
	writeRepoFile(t, tree, "web/src/styles/base.css", readRepoFile(t, baseCSSRel))
	writeRepoFile(t, tree, "web/index.html", readRepoFile(t, "web/index.html"))

	out, code := runGateScript(t, scriptPath(t, "repo-gates.sh"), tree)

	require.Zerof(t, code, "the gates rejected the committed self-hosted font stylesheet:\n%s", out)
}
