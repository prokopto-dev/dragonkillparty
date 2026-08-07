package api

import (
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api/docsui"
)

// externalURL matches any absolute URL with a host — the thing an offline reference must not contain.
var externalURL = regexp.MustCompile(`(?i)\b(?:https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)

// TestDocs_Page_FetchesNothingFromTheNetwork is acceptance criterion 7.
//
// "served from the binary with no network fetch — asserted by a test that runs with outbound
// networking blocked". This asserts the stronger and more useful property: there is nothing to
// block. The served HTML contains no external URL at all, so the page renders identically on an
// air-gapped box in a guild officer's cupboard and on a laptop with fibre.
//
// This matters more than it sounds. Huma's own docs renderer — the one this deliberately replaces —
// is a <script> tag pointing at unpkg.com, so the default behaviour of the library in use is
// precisely the failure this test forbids. docs/design/07-documentation-system.md:168 requires
// Scalar "vendored, not loaded from a CDN" for the same reason.
//
// Blocking outbound networking in-process is not something a Go test can do portably, and a test
// that shelled out to unshare(1) would run on Linux and skip on the laptops. Asserting the absence
// of any URL to fetch is both stronger and portable: a blocked-network test passes if the page
// fetches nothing, and also if it fetches something and degrades quietly.
func TestDocs_Page_FetchesNothingFromTheNetwork(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	New(Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, DocsPath, nil))

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/html")

	body := rec.Body.String()

	require.NotRegexp(t, externalURL, body,
		"the reference page references an external host, so it will not render offline")
	require.Contains(t, body, docsScriptPath, "the page does not load the vendored renderer")
	require.Contains(t, body, SpecPathJSON, "the page does not point at this instance's spec")
}

// TestDocs_Page_CSPForbidsEveryExternalOrigin is the enforcement half.
//
// The test above proves today's HTML fetches nothing. This proves the browser would REFUSE to fetch
// anything even if a future Scalar build tried — `default-src 'none'` with a self-only allowlist.
// Without it the offline guarantee rests entirely on the vendored bundle's good behaviour, which is
// a 3.5 MB file nobody reads.
func TestDocs_Page_CSPForbidsEveryExternalOrigin(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	New(Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, DocsPath, nil))

	csp := rec.Result().Header.Get("Content-Security-Policy")

	require.NotEmpty(t, csp, "the reference page ships no Content-Security-Policy")
	require.Contains(t, csp, "default-src 'none'")
	require.NotRegexp(t, externalURL, csp,
		"the CSP allowlists an external origin, which is exactly what vendoring removed")

	for _, directive := range []string{"script-src", "style-src", "connect-src", "img-src"} {
		require.Contains(t, csp, directive+" ", "the CSP does not constrain %s", directive)
	}
}

// TestDocs_Script_ServesGzipToCapableClientsAndPlainOtherwise covers both branches.
//
// The asset is stored gzipped, so the fallback is real code rather than a formality: curl without
// --compressed, a proxy that strips Accept-Encoding, and `dkp doctor` in a later phase all arrive
// without a gzip token. Serving them the stored bytes labelled as JavaScript hands them 984 KB of
// binary that fails to parse — a bug that appears only outside a browser.
func TestDocs_Script_ServesGzipToCapableClientsAndPlainOtherwise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		acceptEncoding string
		wantGzipHeader bool
	}{
		{name: "browser", acceptEncoding: "gzip, deflate, br", wantGzipHeader: true},
		{name: "gzip only", acceptEncoding: "gzip", wantGzipHeader: true},
		{name: "absent", acceptEncoding: "", wantGzipHeader: false},
		{name: "identity only", acceptEncoding: "identity", wantGzipHeader: false},
		// A client that took the trouble to say q=0 is exactly the one that cannot cope with the
		// compressed form; a substring search for "gzip" would get this backwards.
		{name: "gzip explicitly refused", acceptEncoding: "gzip;q=0, identity", wantGzipHeader: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, docsScriptPath, nil)
			if tc.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			}

			rec := httptest.NewRecorder()
			New(Config{}).ServeHTTP(rec, req)

			res := rec.Result()
			t.Cleanup(func() { _ = res.Body.Close() })

			require.Equal(t, http.StatusOK, res.StatusCode)
			require.Contains(t, res.Header.Get("Content-Type"), "javascript")
			require.Equal(t, "Accept-Encoding", res.Header.Get("Vary"),
				"without Vary a shared cache can replay the gzipped body to a client that cannot read it")

			body := rec.Body.Bytes()

			if tc.wantGzipHeader {
				require.Equal(t, "gzip", res.Header.Get("Content-Encoding"))
				require.Equal(t, docsui.ScalarGzip, body, "the stored bytes should be served verbatim")

				return
			}

			require.Empty(t, res.Header.Get("Content-Encoding"),
				"served a compressed body to a client that did not ask for one")
			require.Greater(t, len(body), len(docsui.ScalarGzip),
				"the body is no larger than the gzip, so it was probably not decompressed")
			require.True(t, strings.HasPrefix(string(body[:20]), "(function") ||
				len(body) > 1_000_000,
				"the decompressed body does not look like the JavaScript bundle")
		})
	}
}

// TestDocsUI_VendoredAsset_MatchesUpstreamHash is the supply-chain check on a 3.5 MB blob.
//
// A minified bundle is unreviewable by reading it, so the only honest assurance is that the bytes
// are the ones upstream published. The expected hash is cross-checked from two independent sources:
// vendor/scalar-standalone.js.sha384, and the SRI hash Huma pins on its own CDN script tag
// (huma/v2@v2.39.1 api.go:684) — which is the same value.
//
// It decompresses before hashing, so storing the asset gzipped cannot hide a substitution.
//
// Delete this and "refresh the vendored Scalar" becomes an operation with no verification step, on
// a file that executes in an authenticated officer's browser.
func TestDocsUI_VendoredAsset_MatchesUpstreamHash(t *testing.T) {
	t.Parallel()

	zr, err := gzip.NewReader(strings.NewReader(string(docsui.ScalarGzip)))
	require.NoError(t, err, "the embedded asset is not valid gzip")

	t.Cleanup(func() { _ = zr.Close() })

	sum := sha512.New384()
	n, err := io.Copy(sum, zr)
	require.NoError(t, err, "decompress the embedded asset")
	require.Positive(t, n, "the embedded asset is empty")

	got := base64.StdEncoding.EncodeToString(sum.Sum(nil))

	require.Equal(t, docsui.UpstreamSHA384, got,
		"the vendored Scalar bundle is not the upstream release it claims to be. Either it was "+
			"modified in place, or it was refreshed without updating docsui.UpstreamSHA384.")

	// And the constant must match the checksum file a human reads, or the two drift and the
	// assertion above starts proving only that the constant equals itself.
	raw, err := os.ReadFile(filepath.Join(
		repoRoot(t), "internal", "api", "docsui", "vendor", "scalar-standalone.js.sha384"))
	require.NoError(t, err, "read the checksum file")

	require.Contains(t, string(raw), docsui.UpstreamSHA384,
		"docsui.UpstreamSHA384 and vendor/scalar-standalone.js.sha384 disagree")
	require.Contains(t, string(raw), docsui.Version,
		"the checksum file does not name the version docsui.Version claims")
}

// TestDocs_SpecEndpoint_ServesTheCommittedDocument closes the loop the reference page depends on.
//
// The page loads SpecPathJSON at runtime. If Huma's OpenAPIPath and that constant ever drift, the
// reference renders an empty page — a failure that looks like a broken bundle and is nothing of the
// kind.
func TestDocs_SpecEndpoint_ServesTheCommittedDocument(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	New(Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SpecPathJSON, nil))

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusOK, res.StatusCode,
		"%s does not serve the spec, so the reference page will render empty", SpecPathJSON)
	require.Contains(t, rec.Body.String(), `"openapi"`)
	require.Contains(t, rec.Body.String(), `"getMeta"`)
}
