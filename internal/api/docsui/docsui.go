// Package docsui holds the vendored API-reference renderer and the assets it needs.
//
// It is a package of its own, beside internal/api rather than inside it, for the same reason
// db/embed.go sits beside the migrations: //go:embed cannot reach upwards out of its own directory,
// so the directive has to live next to the files. Keeping it separate also keeps a 984 KB blob out
// of the package a reader opens to find the routes.
package docsui

import (
	_ "embed"
)

// Version is the vendored @scalar/api-reference release.
//
// Reported in the served HTML so that "which Scalar is this instance running" is answerable from a
// browser, and asserted by the vendored-asset test against vendor/scalar-standalone.js.sha384 so the
// three places that name a version cannot drift apart.
const Version = "1.44.20"

// ScalarGzip is the gzip of @scalar/api-reference's standalone browser bundle.
//
// COMMITTED COMPRESSED, and served compressed. The uncompressed asset is 3,544,608 bytes; the gzip
// is 983,539. That difference lands in git history permanently and in every binary DKP ever ships,
// and browsers decompress Content-Encoding: gzip natively, so serving the compressed form costs the
// server nothing and the client nothing.
//
// Its authenticity is checkable rather than assumed: vendor/scalar-standalone.js.sha384 records the
// SHA-384 of the UNCOMPRESSED upstream file — the same value Huma pins as the SRI hash on its own
// CDN script tag — and TestDocsUI_VendoredAsset_MatchesUpstreamHash decompresses this blob and
// compares. Compression cannot hide a substitution.
//
//go:embed vendor/scalar-standalone.js.gz
var ScalarGzip []byte

// UpstreamSHA384 is the expected base64 SHA-384 of the decompressed asset.
//
// Duplicated from vendor/scalar-standalone.js.sha384 because //go:embed of the checksum file and
// then parsing it at init would put a parser in the boot path to check a constant. The test reads
// the file and asserts this constant matches it, so the duplication is mechanically policed rather
// than trusted.
const UpstreamSHA384 = "tMz7GAo6dMy55x9tLFtH+sHtogji6Scmb+feBR31TAHmvSPRUTboK9H3M5NFaP4R"
