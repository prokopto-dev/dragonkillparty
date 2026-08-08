// Package guild is the domain logic for the single-guild instance: reading and updating the one
// guild row, and the optimistic-concurrency rules that govern a PATCH.
//
// Domain logic lives here, never in the handler (AGENTS.md, .claude/rules/api-endpoints.md).
// internal/api/guild.go marshals HTTP into a call on this package and marshals the result back; it
// makes no decision. The division that matters for this resource is the If-Match check: whether a
// PATCH's If-Match matches the current ETag is a domain rule — it decides whether the write happens
// at all — so it is decided here, and the handler only translates the resulting sentinel into a 412.
//
// Every mutation goes through store.Tx, so the read-modify-write of a PATCH is one transaction on the
// single-writer pool: the current row is read, the If-Match is checked, and the new row is written
// without another officer's concurrent PATCH interleaving. A read that is not part of a mutation —
// Get — goes through store.Q() on the read pool.
package guild
