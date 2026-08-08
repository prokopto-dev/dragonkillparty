// Package ui embeds the built SPA and serves it as an http.Handler.
//
// It is a package of its own, beside internal/api rather than inside it, for the reason db/embed.go
// and internal/api/docsui both are: //go:embed cannot reach upward out of its own directory, so the
// directive must live next to the files. The Vite build writes web/dist; `make build` copies that
// into internal/ui/dist just before `go build`, so the binary carries the SPA with no runtime
// dependency on a filesystem.
//
// Law 4 lives in the SPA source, not here — this package only serves bytes. What it does enforce is
// the two properties the acceptance criteria name:
//
//   - hashed assets get Cache-Control: public, max-age=31536000, immutable; index.html never does;
//   - any path that is not under /api and not a real asset falls back to index.html, so the
//     client-side router owns every route the server does not.
package ui

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// dist holds the built SPA. `all:` so dotfiles Vite may emit are embedded too; without it a
// `.vite/`-prefixed asset would 404 from the binary while working from disk, which is the hardest
// kind of difference to notice.
//
// A committed placeholder index.html keeps this package compiling and testable before any JS build
// has run — `make build` overwrites the directory's contents with the real Vite output. The embed
// therefore always resolves, and TestEmbed_* exercise the serving logic against the placeholder.
//
//go:embed all:dist
var dist embed.FS

// WebDirEnv lets a developer serve the SPA from a live directory (Vite's web/dist) instead of the
// embedded copy, so a UI change is visible without rebuilding the binary. Unset — the production
// default — serves the embed.
const WebDirEnv = "DKP_WEB_DIR"

// indexHTML is the SPA entry document and the fallback for every non-/api path. Named once so the
// handler and the tests agree on it.
const indexHTML = "index.html"

// cacheImmutable is the one-year immutable cache directive for content-hashed assets. A hashed
// filename can never be stale — its name changes when its bytes do — so the browser is told never to
// revalidate it. index.html is deliberately excluded: it is the unhashed entry point and a stale one
// would pin a client to an old asset graph forever.
const cacheImmutable = "public, max-age=31536000, immutable"

// Handler serves the SPA. Assets resolve to their bytes; everything else that is not under /api
// falls back to index.html so the client-side router can take the route.
//
// source is the filesystem the assets are read from: the embedded dist by default, or an on-disk
// directory when DKP_WEB_DIR is set. Resolving it once here rather than per request means the env
// var is read at construction, which is when a misconfiguration should surface.
func Handler() (http.Handler, error) {
	source, err := resolveSource()
	if err != nil {
		return nil, err
	}

	return &spaHandler{files: source}, nil
}

// resolveSource returns the fs.FS the SPA is served from. DKP_WEB_DIR wins when set and points at a
// readable directory; otherwise the embedded dist is used.
func resolveSource() (fs.FS, error) {
	if dir := os.Getenv(WebDirEnv); dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New(WebDirEnv + " is not a directory: " + dir)
		}

		return os.DirFS(dir), nil
	}

	// The embed roots at internal/ui/dist; sub it so paths are relative to the SPA root, matching
	// what os.DirFS(web/dist) yields for the development case.
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// spaHandler serves static assets and falls back to index.html.
type spaHandler struct {
	files fs.FS
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// GET and HEAD only; the SPA serves no mutating verbs. A POST to a client route is a client bug,
	// not a page.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	// /api is the API's, never the SPA's. internal/api owns those routes; a request that reaches
	// this handler under /api is one the API did not match, and it must be a 404 rather than the SPA
	// index — otherwise a bot hitting a mistyped endpoint gets 200 and an HTML page, which every bot
	// author has suffered from EQdkp. This handler is mounted as the catch-all AFTER the API in
	// internal/api/server.go, so in production /api is handled upstream; the guard is defence in
	// depth and is what TestEmbed_APIPath_Returns404 pins.
	reqPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if reqPath == "/api" || strings.HasPrefix(reqPath, "/api/") {
		http.NotFound(w, r)

		return
	}

	name := strings.TrimPrefix(reqPath, "/")
	if name == "" {
		name = indexHTML
	}

	// Try the requested file. A hit that is a real asset is served with the immutable cache header;
	// a miss (or a directory) falls back to index.html with NO cache header, which is how the
	// client-side router owns unknown paths.
	if data, ok := h.readFile(name); ok && name != indexHTML {
		h.serveAsset(w, r, name, data)

		return
	}

	h.serveIndex(w, r)
}

// readFile reads a file from the source, reporting whether it exists as a regular file.
func (h *spaHandler) readFile(name string) ([]byte, bool) {
	f, err := h.files.Open(name)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return nil, false
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}

	return data, true
}

// serveAsset writes a hashed asset with the immutable cache header and a content type inferred from
// its extension.
func (h *spaHandler) serveAsset(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	if ct := contentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	w.Header().Set("Cache-Control", cacheImmutable)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// http.ServeContent handles Range, If-Modified-Since and HEAD correctly; a bare Write would not.
	// A zero modtime means "unknown", which suppresses Last-Modified — correct, because the hashed
	// name is the version and the immutable cache header is the freshness contract.
	http.ServeContent(w, r, name, zeroTime, newReadSeeker(data))
}

// serveIndex writes index.html as the fallback, explicitly WITHOUT a long cache: it is the unhashed
// entry point and pinning a stale copy would strand a client on an old asset graph.
func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, ok := h.readFile(indexHTML)
	if !ok {
		// The SPA was not built into the source. In production this cannot happen — `make build`
		// fails before the binary exists — but DKP_WEB_DIR can point at a directory without an
		// index. Say so rather than serving an empty page.
		http.Error(w, "web UI not built", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// no-cache, not no-store: the client may cache it but must revalidate, so a deploy's new asset
	// graph is picked up on the next navigation.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, indexHTML, zeroTime, newReadSeeker(data))
}
