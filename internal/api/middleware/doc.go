// Package middleware holds the transport-level wrappers that sit between net/http and the Huma
// stack: request-id assignment and the last-resort problem+json renderer.
//
// Nothing here imports internal/api, and that is structural rather than incidental. The wire types
// and the closed error-code enum live in internal/api/errors.go (first-ten-prs.md PR 4, and
// .claude/rules/api-endpoints.md), so a middleware that rendered them directly would close an
// import cycle. Both wrappers therefore take what they need from the caller — Problem takes a
// Renderer, RequestID takes a clock — which is the same consumer-declares-the-interface shape
// api.ReadyChecker already uses to keep internal/api and internal/migrate independent.
//
// These are ordinary http.Handler wrappers, not huma.Middleware. That is deliberate: /healthz and
// /readyz are raw handlers on the same mux (see internal/api/health.go), and a Huma-level
// middleware would not see them. A request that never reaches an operation still needs a
// request id in its log line and a problem+json body if it fails.
package middleware
