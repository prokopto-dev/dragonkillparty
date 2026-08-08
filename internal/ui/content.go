package ui

import (
	"bytes"
	"io"
	"path"
	"time"
)

// zeroTime is the modtime passed to http.ServeContent. A zero time means "unknown", which suppresses
// the Last-Modified header — deliberate, because the content-hashed filename is the version and the
// Cache-Control header is the freshness contract. There is no meaningful mtime on an embedded asset
// anyway: embed.FS reports the build time, which differs between two byte-identical builds.
var zeroTime = time.Time{}

// newReadSeeker wraps asset bytes so http.ServeContent can seek for Range requests. bytes.Reader is
// the stdlib io.ReadSeeker over a byte slice; this is a named constructor only so the call sites in
// embed.go read as intent rather than as a bytes.NewReader detail.
func newReadSeeker(data []byte) io.ReadSeeker {
	return bytes.NewReader(data)
}

// contentType maps a filename extension to a MIME type for the small set of extensions a Vite build
// emits. It is a fixed table rather than mime.TypeByExtension because that function consults the
// host's /etc/mime.types, which makes the served Content-Type depend on the machine the binary runs
// on — a .js served as text/plain on one distro and application/javascript on another is exactly the
// kind of environment-dependent bug this package exists to remove.
//
// An unknown extension returns "" and the caller lets http.ServeContent sniff, which is safe here
// because X-Content-Type-Options: nosniff is set alongside and every asset in the table is covered.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	default:
		return ""
	}
}
