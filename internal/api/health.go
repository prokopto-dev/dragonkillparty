package api

import "net/http"

// healthBody is the exact response body of GET /healthz. No trailing newline:
// the byte sequence is part of the contract and a test asserts on it.
const healthBody = `{"status":"ok"}`

// NewMux returns the HTTP router for the binary.
//
// This is the PR 1 skeleton: a plain *http.ServeMux carrying the single
// infrastructure route. PR 4 introduces the Huma mount, and the concrete
// *http.ServeMux return type is expected to change then — .claude/rules/
// api-endpoints.md specifies humachi.New over a chi.Router, which is not a
// ServeMux. Treat this signature as settled for PR 1 and as PR 4's to revise,
// not as a contract PR 4 has to work around. cmd/dkp/serve.go is the only
// caller.
func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	return mux
}

// handleHealthz answers the container HEALTHCHECK with 200 and {"status":"ok"}.
//
// Four things about this handler are load-bearing, and each is easy to destroy
// by accident. Read all four before changing it.
//
//  1. It is pre-Huma by design. Huma does not exist in the repo at PR 1, so a
//     bare ServeMux handler is the correct shape here, not a shortcut. PR 4
//     introduces huma.Register for getMeta; whether /healthz itself becomes a
//     Huma operation or stays a raw handler on the same underlying router is
//     NOT decided — first-ten-prs.md scopes PR 4 to one route and does not list
//     this file. Decide it there; do not read this comment as instruction.
//
//  2. If it ever does become a Huma operation, it takes Hidden: true. Canonical
//     §7 permits Hidden on exactly five paths — /healthz, /readyz, /metrics,
//     the OAuth callback, and the compat shim — and that allowlist is a const
//     slice precisely so a sixth entry is a deliberate edit a reviewer sees.
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
