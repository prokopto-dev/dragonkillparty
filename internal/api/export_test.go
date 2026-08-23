package api

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Test-only doors into this package. This file is `_test.go`, so nothing here links into the binary
// and nothing here is API surface — but it IS visible to the external api_test package in this
// directory, which is what lets those tests drive internals without a second assembly of the server.

// NewWithOperations builds the product's server with `register` additionally declaring operations on
// the same Huma API.
//
// It is what lets the write-safety middleware be tested against a FENCED operation before the first
// one ships. Canonical §7 requires an Idempotency-Key on every POST that creates domain state; the
// first such endpoint is two waves away, so #290's middleware would otherwise be exercised only by a
// test that stood up its own Huma API — which proves the middleware works and says nothing about
// whether the product mounts it. This registers into the real tree, behind the real middleware, in
// the real order.
//
// It cannot be used to add a route to a shipped binary: `_test.go` files are compiled only into the
// test binary of this package, so law 1's "routes are declared only in internal/api" is unaffected
// and the committed OpenAPI document — built by NewHumaAPI, which this does not touch — cannot
// acquire an operation from here.
func NewWithOperations(cfg Config, register func(huma.API)) http.Handler {
	return newServer(cfg, register)
}
