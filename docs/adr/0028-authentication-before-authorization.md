# ADR-0028 — Authentication lands before authorization, and says so on the published surface

**Status:** accepted · **Date:** 2026-08-20 · **Deciders:** owner

## Context and problem statement

Phase 2 deliverable 1 is one sentence in the ROADMAP — "one middleware resolving cookie-or-bearer
into a single `Principal`" — and it sits on top of six credential tables, a pepper with a lifecycle,
and the permission catalogue Wave 0b already reconciles into the database. Issue #273 scopes the
identity half; Wave 0e scopes the capability half (`authz.Check`, `role permissions ∩ token scopes`,
the capability floor).

Splitting there is a real decision rather than a bookkeeping one, because the intermediate state is
not obviously safe: for as long as it lasts, **every authenticated principal passes every
operation**. A member's session can `PATCH /api/v1/guild`. So can a token minted with no scopes at
all. That is strictly better than the Phase 0 state it replaces — where *nobody* needed a credential
and an anonymous `PATCH` succeeded — and strictly worse than what the OpenAPI document, the
generated reference pages and `docs/api/auth-and-scopes.md` all describe in detail.

The question this record answers is not "should authentication ship first" — it must, since a
capability check needs a principal to check. It is **what the product owes a reader while the two
halves are apart**, and whether the schema should be split along with the code.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Ship both halves in one change | No intermediate state to disclose; the security story is whole on the day it lands | Roughly twice the diff in the highest-blast-radius package in the repo, reviewed in one sitting: six tables, a keyring, a resolver, a middleware, the permission evaluation, the scope intersection and the step-up gate. The review that matters most is the one least able to absorb that |
| B — Ship identity, and disclose the remaining gap the way Phase 0 disclosed its own | The reviewable unit is one concern. The tripwire pattern already exists here and has already worked once — the Phase 0 notice and `TestGuild_Unauthenticated_IsAKnownPhase0Gap` are what made this change deliberate rather than a discovery | An intermediate release in which capability is documented and unenforced. Requires the disclosure to be *narrowed* rather than deleted, which is easy to get wrong in the tidying direction |
| C — Ship identity and withhold the credential documentation until 0e | Nothing over-promises | The spec is the product's contract and the SDKs are generated from it; a `securitySchemes` block that omits what it will require is a different lie, and bot authors cannot start work |
| D — Ship only the tables now, and the middleware with 0e | Smallest possible diff | The tripwire stays red-in-waiting for another wave while an unauthenticated `PATCH` keeps succeeding. Schema without a reader is also the shape that gets a column wrong and finds out two waves later |

## Decision outcome

**Chosen: B.** Wave 0d ships the six credential tables, `internal/auth`, and the one middleware that
resolves a cookie or a bearer into a single `Principal`, mounted before every Huma operation.
`TestGuild_Unauthenticated_IsAKnownPhase0Gap` is deleted and replaced by
`TestGuild_Unauthenticated_Is401`, which additionally asserts the refused `PATCH` never reached the
handler.

Four commitments make the intermediate state honest rather than merely brief:

1. **The disclosure narrows; it does not vanish.** `authz.Phase0EnforcementNotice` becomes
   `authz.AuthorizationGapNotice` and says exactly what is now true — authentication is enforced,
   capability is not — on the `pat` and `session` security schemes and on both generated reference
   pages. Two tests assert it is present, so it can neither quietly outlive the gap nor quietly
   disappear while it is open. Deleting it is Wave 0e's job, in the change that lands `authz.Check`.
2. **The whole schema ships, not the part this wave reads.** `feed_token` has no route, `app_user`'s
   MFA columns have no enrolment flow, and `api_token.pepper_kid` has no rotation. Each of them is
   specified (`docs/design/01-domain-model.md` §4, `03-security.md` §3.4, §9.1) and each would
   otherwise arrive as a `CHECK` change, which on SQLite is a twelve-step rebuild of an auth table.
   The cost of shipping a column early is a column; the cost of adding one late is a rebuild.
3. **Five mutations ship ahead of their endpoints** — revoke session, revoke token, bump session
   epoch, set account state, soft-delete a user. Each is a branch the resolver refuses on, and a
   branch nobody has watched go red is a branch nobody knows works. Each is the statement its
   endpoint will call verbatim, so this is early delivery rather than test scaffolding.
4. **`internal/auth` decides authentication and nothing else.** It answers "is this credential real,
   live, and attached to an account that may act". It never answers "may this principal do this",
   holds no permissions on the `Principal`, and imports nothing from `internal/authz`. That boundary
   is what makes 0e an addition rather than a rewrite.

### Consequences

- Good, because the review that matters most is scoped to one concern, with the credential formats,
  the pepper and the resolver in one diff and the evaluation model in the next.
- Good, because the Phase 0 tripwire closes on schedule instead of waiting for the wave after it. An
  unauthenticated write to the product's first mutating endpoint stops being possible now.
- Good, because the disclosure mechanism is now demonstrably reusable: it was installed ahead of the
  code it gated, it went red on the day that code landed, and it narrowed rather than being deleted.
- **Bad, because a documented control is unenforced for a wave.** A reader who takes
  `x-dkp-scopes` at face value is wrong until 0e. The notice is the whole mitigation, and a notice
  is weaker than a check.
- **Bad, because a zero-scope token is currently a full-capability token**, which is the exact
  property ADR-0011 exists to deny. Nothing that ships in this window should be advertised as
  scope-limited.
- **Bad, because the binary now requires a credential it cannot yet issue.** There is no login
  endpoint and no first-run bootstrap (issue #264), so a fresh instance serves `/healthz`,
  `/readyz`, `/config.json`, the SPA and `GET /api/v1/meta`, and answers `401` to everything else.
  That is the correct direction to fail, and it is a real gap in the product until #264 lands.

### Reversal cost

Low, and asymmetric in the safe direction. Wave 0e adds a check inside a middleware that already
exists and already has the `Principal` in hand; nothing about the tables, the formats or the
resolution changes. Reversing the *split* — landing 0e before 0d, or unmounting the middleware —
would mean re-opening an unauthenticated write path, which is the one thing this change exists to
close.
