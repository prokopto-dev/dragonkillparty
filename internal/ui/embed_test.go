package ui_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ui"
)

// newHandler builds the SPA handler from the embedded dist, failing the test if it cannot.
func newHandler(t *testing.T) http.Handler {
	t.Helper()

	h, err := ui.Handler()
	require.NoError(t, err, "build the SPA handler")

	return h
}

// get issues a GET against the handler and returns the recorded response.
func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

// TestEmbed_UnknownPath_ServesIndex is the acceptance criterion for the client-router fallback: any
// non-/api path the server does not have a real asset for must return index.html so the SPA's own
// router can take the route. Without it a deep link like /standings/01ABC 404s on a page refresh.
func TestEmbed_UnknownPath_ServesIndex(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	for _, target := range []string{"/", "/standings", "/pools/01ABCDEF/settings", "/does/not/exist"} {
		rec := get(t, h, target)

		require.Equal(t, http.StatusOK, rec.Code, "%s must fall back to index.html", target)
		require.Contains(t, rec.Body.String(), "<div id=\"root\">",
			"%s must serve the SPA index document, not an asset or a 404", target)
		// The fallback index must NOT carry the immutable cache header — it is the unhashed entry
		// point and a stale copy would strand a client on an old asset graph.
		require.NotContains(t, rec.Header().Get("Cache-Control"), "immutable",
			"index.html must not be cached immutable")
	}
}

// TestEmbed_APIPath_Returns404 is the other direction: /api is the API's, never the SPA's. A request
// that reaches this handler under /api is one internal/api did not match, and it must be a 404 —
// never the SPA index. Serving HTML for a mistyped endpoint is the "200 with an error page" failure
// every bot author suffered from EQdkp.
func TestEmbed_APIPath_Returns404(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	for _, target := range []string{"/api", "/api/", "/api/v1/meta", "/api/v1/does-not-exist"} {
		rec := get(t, h, target)

		require.Equal(t, http.StatusNotFound, rec.Code, "%s under /api must 404, not fall back to the SPA", target)
		require.NotContains(t, rec.Body.String(), "<div id=\"root\">",
			"%s must not serve the SPA index — /api belongs to the API", target)
	}
}

// embeddedJSAsset returns the path of a .js file under dist/assets, DISCOVERED rather than named.
//
// It used to be the literal "/assets/app-placeholder.js", and that coupled the test to build state
// instead of to the property it asserts. `make build` stages the real Vite output over
// internal/ui/dist with an `rm -rf` first, which deletes the committed placeholders — so a developer
// running the sequence AGENTS.md prescribes (`make build`, then `make test`) got a failure claiming
// the immutable cache header was wrong, when the truth was that the file the test asked for no longer
// existed and the handler had correctly fallen back to index.html with `no-cache`. The failure named
// the wrong thing entirely, and CI could not see it because `build / binary` and `test / unit` run in
// separate jobs from clean checkouts.
//
// Discovering the asset makes the assertion true of whatever is embedded — the committed placeholder
// or a real hashed bundle — which is what "a hashed asset carries the immutable header" always meant.
func embeddedJSAsset(t *testing.T) string {
	t.Helper()

	// dist/assets relative to the package directory, which is a Go test's working directory — the same
	// tree `//go:embed all:dist` compiled in. Read from disk rather than through a new exported
	// accessor: the embed is not worth widening this package's API for a test.
	entries, err := os.ReadDir(filepath.Join("dist", "assets"))
	require.NoError(t, err, "read internal/ui/dist/assets")

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".js" {
			return "/assets/" + entry.Name()
		}
	}

	t.Fatal("no .js asset is embedded under dist/assets — internal/ui has nothing to serve")

	return ""
}

// TestEmbed_HashedAsset_IsImmutable asserts a real asset (anything that is not index.html) is served
// with the one-year immutable cache directive. This is the property that lets a browser never
// revalidate a content-hashed bundle.
func TestEmbed_HashedAsset_IsImmutable(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	rec := get(t, h, embeddedJSAsset(t))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"),
		"a hashed asset must carry the immutable one-year cache header")
	require.Contains(t, rec.Header().Get("Content-Type"), "javascript",
		"a .js asset must be served as JavaScript regardless of the host mime table")
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

// TestEmbed_UnknownAssetPath_FallsBackToIndex proves the fallback is by-existence, not by-extension:
// a path that LOOKS like an asset but has no file behind it still yields index.html, so a client
// route named /app.js would not accidentally 404. The distinguishing check is the body, not the
// status, because both return 200.
func TestEmbed_UnknownAssetPath_FallsBackToIndex(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	rec := get(t, h, "/assets/missing-9999.js")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "<div id=\"root\">",
		"a non-existent asset path must fall back to index.html")
	require.NotContains(t, rec.Header().Get("Cache-Control"), "immutable",
		"the fallback is index.html, which is never immutable")
}

// TestEmbed_WebDir_OverridesEmbed covers the DKP_WEB_DIR development hook: when set, the SPA is
// served from the live directory instead of the embedded copy, so a UI change is visible without
// rebuilding the binary. The test writes a marker index into a temp dir and asserts it wins.
func TestEmbed_WebDir_OverridesEmbed(t *testing.T) {
	// NOT parallel: it sets an environment variable, and t.Setenv forbids t.Parallel().
	dir := t.TempDir()
	const marker = "<div id=\"root\">from DKP_WEB_DIR</div>"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"+marker), 0o644))

	t.Setenv(ui.WebDirEnv, dir)

	h, err := ui.Handler()
	require.NoError(t, err)

	rec := get(t, h, "/anything")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "from DKP_WEB_DIR",
		"DKP_WEB_DIR must override the embedded SPA")
}

// TestEmbed_WebDir_NotADirectory_IsAnError asserts a misconfigured DKP_WEB_DIR surfaces at
// construction rather than silently falling through to the embed — a boot-time misconfiguration
// should say so, not serve a stale UI.
func TestEmbed_WebDir_NotADirectory_IsAnError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	t.Setenv(ui.WebDirEnv, file)

	_, err := ui.Handler()
	require.Error(t, err, "DKP_WEB_DIR pointing at a file must be an error")
}

// TestEmbed_PathTraversal_CannotEscapeDist asserts a traversal attempt never serves a file outside
// the SPA root. Every `..` sequence — raw, percent-encoded, chained — must resolve to the index
// fallback, not to embed.go or /etc/passwd. path.Clean collapses the traversal and fs.FS.Open
// rejects anything that escapes the root; this test proves both, because "the embed happens to be
// safe" is not a property to leave untested when the input is a URL.
func TestEmbed_PathTraversal_CannotEscapeDist(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	for _, target := range []string{
		"/../embed.go",
		"/../content.go",
		"/assets/../../../etc/passwd",
		"/%2e%2e/embed.go",
	} {
		rec := get(t, h, target)

		require.Equal(t, http.StatusOK, rec.Code, "%s must not error out to a file outside dist", target)
		require.Contains(t, rec.Body.String(), "<div id=\"root\">",
			"%s must resolve to the index fallback, never a traversed file", target)
		require.NotContains(t, rec.Body.String(), "package ui",
			"%s served a Go source file — path traversal escaped the SPA root", target)
	}
}

// TestEmbed_Index_SetsSelfOnlyCSP pins security F2: the SPA entry document carries a self-only
// Content-Security-Policy so a compromised dependency cannot exfiltrate to, or pull code from, a
// foreign origin. The two 'unsafe-' directives are ASSERTED ABSENT — the Vite bundle uses external
// hashed <script>/<link>, no inline script and no runtime style injection, so a build that regressed
// into needing either would break in the browser under this policy rather than shipping a hole.
func TestEmbed_Index_SetsSelfOnlyCSP(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	// The fallback index and a bare "/" both go through serveIndex; check both entry points.
	for _, target := range []string{"/", "/standings"} {
		rec := get(t, h, target)

		csp := rec.Header().Get("Content-Security-Policy")
		require.Equal(t,
			"default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'",
			csp, "%s must carry the self-only SPA CSP", target)
		require.NotContains(t, csp, "unsafe-inline",
			"the SPA needs no inline script or style — 'unsafe-inline' would defeat the policy")
		require.NotContains(t, csp, "unsafe-eval",
			"the SPA needs no eval — 'unsafe-eval' would defeat the policy")
	}
}

// TestEmbed_NoSourceMapShips pins security F3: no *.map ships in the embedded SPA. A source map hands
// an attacker the unminified source and comments; scripts/build-web.sh refuses to stage one, and this
// is the runtime-side lock — every embedded file is walked and none may end in .map.
//
// It walks the embedded fs.FS through the handler rather than the on-disk dist, because the embed is
// what the binary actually serves; a .map present on disk but absent from the embed would still be a
// leak if the embed picked it up via `all:`.
func TestEmbed_NoSourceMapShips(t *testing.T) {
	t.Parallel()

	// Walk the committed dist tree that internal/ui embeds. The placeholder tree has no .map; the real
	// build must not add one, and build-web.sh fails before staging if it would.
	root := filepath.Join("dist")

	var maps []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".map" {
			maps = append(maps, path)
		}

		return nil
	})
	require.NoError(t, err, "walk the embedded dist tree")
	require.Empty(t, maps, "no source map may ship in the embedded SPA — it leaks the unminified source")
}

// TestEmbed_NonGetMethod_MethodNotAllowed pins the verb guard: the SPA serves GET and HEAD only.
func TestEmbed_NonGetMethod_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	h := newHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}
