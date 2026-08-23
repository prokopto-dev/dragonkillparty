// Package idempotency is the record behind the `Idempotency-Key` header: one row per (principal,
// key), claimed before a mutating handler runs and completed with the response it produced, so a
// bot's retry replays the original effect instead of causing a second one.
//
// IT DECIDES THE REPLAY, NOT THE ROUTE. Which operations are fenced is a fact about the HTTP
// surface and lives in internal/api (a route may only be known about there, law 1); what this
// package answers is "this principal presented this key for this request — execute, replay, refuse,
// or wait". The two halves meet in internal/api/idempotency.go, which is the middleware.
//
// THE CONVENIENCE LAYER, NEVER THE CONSTRAINT. docs/api/idempotency-and-concurrency.md is explicit
// that every fenced operation also carries a real uniqueness constraint underneath — a tick on
// (raid_id, content_sha256), a settlement on ledger_batch.source_ref. This table makes a duplicate
// cheap and loud; it is not what makes it safe, and a handler that relies on it for correctness has
// the dependency the wrong way round.
//
// Lands in: Phase 2, issue #290.
package idempotency
