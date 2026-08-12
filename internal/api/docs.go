package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/api/docsui"
	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
)

const (
	// DocsPath serves the browsable API reference.
	//
	// Under /api/v1, not at the root. docs/development/first-ten-prs.md:228 writes it as /docs, but
	// docs/api/getting-started.md:162-163 (the page a user actually reads) and
	// docs/design/02-api-design.md:170-171 both put it under the version prefix, and
	// 02-api-design.md:116 states that every path in that table is relative to /api/v1 unless marked
	// otherwise — /healthz and /readyz are marked, these are not. Two sources to one, including the
	// user-facing one; canonical §7 is silent, so first-ten-prs.md is the outlier and is corrected
	// in this PR.
	DocsPath = BasePath + "/docs"

	// docsScriptPath serves the vendored renderer. A sub-path of DocsPath so that one CSP and one
	// future auth decision cover both.
	docsScriptPath = DocsPath + "/scalar.js"

	// SpecPathJSON is where Huma serves the OpenAPI document. Huma appends the extension to
	// Config.OpenAPIPath itself; this constant exists so the docs page, the tests and the spec gate
	// all name it once.
	SpecPathJSON = BasePath + "/openapi.json"
)

// docsCSP is the Content-Security-Policy for the reference page.
//
// This is the mechanism behind acceptance criterion 7 — "served from the binary with no network
// fetch". A test asserting the HTML contains no external URL proves only what is in the file today;
// `default-src 'none'` plus an explicit self-only allowlist makes the browser REFUSE any external
// fetch a future Scalar build might attempt. The guarantee stops depending on the vendored bundle's
// good behaviour.
//
// The two unsafe- directives are Scalar's requirements, not choices: it evaluates generated code for
// the request console and injects styles at runtime. Huma's own CDN-based handler carries exactly
// the same two (huma/v2@v2.39.1 api.go:661-663), so this is not a relaxation relative to the
// alternative — it is the same policy with the network origin removed.
const docsCSP = "default-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'; " +
	"script-src 'self' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'"

// docsHTML is the page shell. The only URLs in it are same-origin absolute paths.
const docsHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="referrer" content="no-referrer">
    <title>` + specTitle + ` API reference</title>
  </head>
  <body>
    <script id="api-reference" data-url="` + SpecPathJSON + `"></script>
    <script src="` + docsScriptPath + `"></script>
  </body>
</html>
`

// registerDocs mounts the offline API reference.
//
// Raw net/http handlers rather than Huma operations, and deliberately so: these serve HTML and
// JavaScript, they are documentation rather than API surface, and registering them with Huma would
// put two non-JSON operations in the published spec that every generated SDK would then grow a
// method for. Huma's own docs route is disabled in humaConfig for the same reason plus a stronger
// one — its renderer is a <script> tag pointing at unpkg.com.
//
// They are not Hidden operations either. Hidden is for things that ARE operations and are kept out
// of the document; these were never operations, so canonical §7's five-path allowlist does not
// apply and does not need a sixth entry.
func registerDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET "+DocsPath, handleDocsPage)
	mux.HandleFunc("GET "+docsScriptPath, handleDocsScript)
}

// handleDocsPage serves the reference shell.
func handleDocsPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", docsCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, docsHTML)
}

// handleDocsScript serves the vendored Scalar bundle.
//
// The asset is stored gzipped (984 KB against 3.5 MB). A client advertising gzip — every browser
// since roughly 1999 — gets the stored bytes with Content-Encoding: gzip and the server does no
// work at all. Anything else gets it decompressed on the fly.
//
// The fallback is not theatre. curl without --compressed, a corporate proxy that strips
// Accept-Encoding, and `dkp doctor` fetching this page in a later phase all send no gzip token, and
// serving them a gzip stream labelled as JavaScript would hand them 984 KB of binary that fails to
// parse — a bug that only appears outside a browser, which is the hardest kind to notice.
func handleDocsScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Security-Policy", docsCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Vary matters even though the body is identical either way: a shared cache that stored the
	// gzipped response for a gzip-capable client would otherwise replay it to one that cannot read
	// it.
	w.Header().Set("Vary", "Accept-Encoding")

	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(docsui.ScalarGzip)

		return
	}

	zr, err := gzip.NewReader(bytes.NewReader(docsui.ScalarGzip))
	if err != nil {
		// Only reachable if the embedded asset is corrupt, which the vendored-asset test would have
		// caught first. Answering 500 here rather than a truncated script means the failure says
		// what it is instead of surfacing as an unexplained blank reference page.
		middleware.Logger(r.Context()).ErrorContext(r.Context(), "decompress vendored docs asset", "error", err)
		http.Error(w, "documentation asset unavailable", http.StatusInternalServerError)

		return
	}

	defer func() { _ = zr.Close() }()

	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, zr)
}

// acceptsGzip reports whether the client advertised gzip.
//
// Token-aware rather than a substring search: `Accept-Encoding: gzip;q=0` means "explicitly do not
// send me gzip", and strings.Contains(ae, "gzip") answers true for it. A client that took the
// trouble to say q=0 is exactly the client that cannot cope with the compressed form.
func acceptsGzip(r *http.Request) bool {
	for enc := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(enc), ";")

		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}

		return !strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0")
	}

	return false
}
