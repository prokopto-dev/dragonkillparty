# ADR-0011 — Opaque scoped PATs, and no superadmin token

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

EQdkp Plus has two API tokens: `api_key`, which **impersonates the first user in superadmin group
id 2** and therefore has total power, and `api_key_ro`, which is read-only. Both travel happily in a
query string (`?atoken=`), which puts them in access logs, proxy logs and browser history. Guild bots
are written by volunteers, pasted into Discord DMs, and inherited by the next officer — the threat
model is leakage, not cryptanalysis. Bots are first-class here ([ADR-0005](0005-api-first-no-bff.md)),
so tokens are on the hot path of most requests.

## Considered options

| Option | For | Against |
|---|---|---|
| A — One admin token plus a read-only token (the incumbent's model) | Trivial to implement and explain | A leaked token is a total compromise; no expiry, no rotation record, no per-token audit; the bot dies when its creating officer leaves |
| B — JWTs carrying scopes | Stateless verification; no lookup per request | Revocation requires a denylist, which is the state you were avoiding; a leaked token is valid until expiry; claims drift from the role that granted them |
| C — Opaque scoped PATs owned by service accounts | Instant revocation; per-token scopes, expiry, rate limit and audit; capability is bounded by the role behind it | A lookup per request; a server pepper you must protect and back up; more moving parts than one key |

## Decision outcome

**Chosen: C.** Format `dkp_pat_<8-char public prefix>_<43 chars base64url of 32 random bytes>`.
Stored as `HMAC-SHA256(server_pepper, secret)` — a keyed hash, not bcrypt, because verification is on
the hot path and must be one indexed lookup on the public prefix plus one constant-time compare.

Three rules carry the weight:

1. **Tokens belong to service accounts, not people.** A service account has a human `owner_user_id`
   for audit and notification, but revoking the human does not kill the bot — the most predictable
   failure mode in guild tooling.
2. **Effective capability = role permissions ∩ token scopes.** A scope narrows a token; a permission
   narrows a role. A token can never exceed what its service account's role already grants. **There is
   no `admin:*` scope and no all-powerful token.** This is the single biggest deliberate fix over the
   incumbent.
3. **Operations that alter authentication, authorization or bulk-export state have no scope at all**
   and are session + step-up only: minting, rotating and revoking tokens; editing roles and role
   assignments; downloading backups; reading PII in bulk; committing an import. There is no token that
   can do any of them.

Transport is `Authorization: Bearer dkp_pat_…` only. Query-string tokens are rejected with `401` and
an explanatory error — the one exception is the compat shim's `?atoken=`
([ADR-0013](0013-eqdkp-compat-shim.md)). Feeds use single-purpose path-embedded feed tokens, never a
PAT.

**Enforced by:** the permission and scope catalogue is generated from one Go source
(`internal/authz/catalogue.go`), `role_permission` is FK-constrained to `permission(key)` so a
divergent list is a **boot failure**, and the authorization-matrix test suite asserts every
(principal kind × operation) cell.

### Consequences

- Good, because a leaked bot token is bounded damage: it can do what its scopes say and nothing else,
  it shows up in `last_used_at` / `last_used_ip`, and revoking it is instant.
- Good, because the bot survives officer turnover, and the audit trail still names a responsible human.
- Good, because "no all-powerful token" is a property of the schema, not a policy — there is no cell
  in the matrix to grant.
- **Bad, because the server pepper is now a secret with a lifecycle.** It must be backed up with the
  database and never in it; losing it invalidates every token at once, and rotating it needs a
  dual-read window. That is a new operational burden for a volunteer.
- **Bad, because officers will ask for the thing we refused.** "Let my bot rotate its own token" and
  "let my backup script download the backup" are reasonable-sounding requests, and the answer is
  permanently no. Expect to explain this repeatedly.
- **Bad, because it costs a database round trip per request**, plus a `last_used_at` write unless
  batched — cheap on SQLite, but it is a write on the read path and it needs care under WAL.
- **Bad, because this is roughly 900 lines of hand-rolled auth with no CVE feed** (ROADMAP R10).
  Mitigated by the authorization-matrix suite, argon2id for local passwords, TOTP step-up in Phase 2,
  `govulncheck`, and an external review of `internal/auth` before RC — but the residual risk is real
  and is ours.

### Reversal cost

Adding a superadmin token later is a day of code and the end of the security argument. Changing the
token format is a release plus a rotation window for every guild's bots.
