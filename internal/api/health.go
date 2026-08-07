package api

import "net/http"

// healthBody is the exact response body of GET /healthz. No trailing newline:
// the byte sequence is part of the contract and a test asserts on it.
const healthBody = `{"status":"ok"}`

// handleHealthz answers the container HEALTHCHECK with 200 and {"status":"ok"}.
//
// Four things about this handler are load-bearing, and each is easy to destroy
// by accident. Read all four before changing it.
//
//  1. It is a raw net/http handler, not a Huma operation, and PR 4 DECIDED that
//     rather than inheriting it. The router it is registered on is now behind a
//     Huma mount (see server.go), so making it an operation was a live option.
//     The reason it is not one is /readyz next door: that endpoint answers 503
//     with a body which is deliberately NOT problem+json, and registering the
//     pair with Huma would force a choice between breaking that wire contract
//     and breaking "every error is RFC 9457" (first-ten-prs.md PR 4). Both
//     endpoints would also be Hidden, so they would be absent from the
//     published spec either way — all of the coupling, none of the benefit.
//     health_test.go additionally pins this body byte for byte, which Huma's
//     content negotiation would put at risk for no gain.
//
//  2. If some later change does make it an operation, it takes Hidden: true.
//     Canonical §7 permits Hidden on exactly five paths — /healthz, /readyz,
//     /metrics, the OAuth callback, and the compat shim. The allowlist lives in
//     permissions.go as HiddenOperationAllowlist, and adding an entry is a
//     deliberate edit a reviewer sees. Nothing sets Hidden today.
//
//  3. /healthz is deliberately outside /api/v1. It is infrastructure, not API
//     surface: no version prefix, no scope, no permission, no SDK method. Do
//     not "tidy" it under the versioned prefix.
//
//  4. It must never touch the database. Canonical §13. This is the single most
//     important line in the file: a DB-touching healthcheck lets Docker kill
//     the container mid-migration, which is how a guild loses its ledger. The
//     DB-aware check is /readyz, and it is a separate endpoint for this reason.
//     If this handler ever grows a store dependency, that is the bug.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(healthBody))
}
