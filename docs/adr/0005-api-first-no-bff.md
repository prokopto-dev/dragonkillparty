# ADR-0005 — API-first, no BFF

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

A first-class HTTP API for Discord bots, log parsers and bid bots is not a feature of this product,
it is the architecture. EQdkp Plus shows what happens when it is treated as a feature: after fifteen
years its Plus Exchange API has no `DELETE`, no `PUT`, no member CRUD, no pagination metadata, no
idempotency and no proper status codes — because the UI never used it, so nobody noticed it rotting.
Every pattern that gives the UI a private channel into the domain reproduces that outcome.

## Considered options

| Option | For | Against |
|---|---|---|
| A — BFF / SSR / server actions / HTMX / Inertia | Best UI ergonomics; fewer round trips; less client state | The UI gets a channel bots cannot use; it becomes the path of least resistance; the maintainers use it daily and the public API becomes a second-class surface that rots. This is exactly EQdkp's failure |
| B — GraphQL for the UI, REST for bots | Each consumer gets its ideal shape | Two contracts, two authorization surfaces, two sets of bugs; the bot API is still the one nobody dogfoods |
| C — One public REST API; the SPA is a pure client of it | The API is exercised on every page load; coverage is automatic; a UI feature that needs a capability adds it to the public contract | Some UI screens need several calls; no server-side composition; the SPA carries more state |

## Decision outcome

**Chosen: C.** The React SPA is built to `web/dist`, embedded via `go:embed`, and speaks only
`/api/v1` — the same operations, the same auth middleware and the same problem-details errors a bot
sees. There is no UI-private endpoint, no BFF, no SSR, no server components. If a UI feature needs a
new capability, it is added to the public API and the spec, in the diff, in review.

Exactly **three server-rendered surfaces** are permitted, each because an SPA is a liability there:
the first-run setup wizard (must work before any session or token exists), `/ops` diagnostics (must
work when the SPA bundle is broken), and public read-only standings (linkable, no-JS). They are
bootstrap or read-only and never a write path into the domain.

**Enforced by five CI gates**, not by convention:

1. **No hidden operations** — a Go test enumerates every registered Huma operation and asserts none
   is spec-excluded. `Hidden: true` is legal only on `/healthz`, `/readyz`, `/metrics`, the OAuth
   callback and the compat shim.
2. **Spec drift** — `openapi/openapi.json` is committed; CI regenerates from Go and fails on any diff.
3. **Zero raw fetch** — ESLint `no-restricted-globals: fetch, XMLHttpRequest` outside `web/src/api`.
4. **Traffic conformance** — the Playwright run records the browser's traffic and a test asserts every
   observed `(method, path-template)` exists in the spec; a response-validation middleware checks
   every response in the whole integration suite against its declared schema.
5. **PAT parity** — a subset of integration tests replays the SPA's exact request sequences using a
   PAT with published scopes and asserts identical responses. This is the strongest of the five: it
   proves *capability* parity, not merely spec presence.

### Consequences

- Good, because the API cannot rot. Any drift shows up as a red gate on the PR that caused it.
- Good, because support answers become "open devtools and copy the request" — what the UI does is
  what a bot can do, byte for byte.
- Good, because the SPA can be pointed at a remote instance (`API_BASE` comes from `/config.json` at
  runtime), which is the definitional test of whether it is really a client.
- **Bad, because some screens cost several round trips** that a BFF would have composed server-side.
  Standings for a 200-character guild with 12 columns is the heaviest, and it needs server-side sort
  and filter plus virtualisation rather than a bespoke aggregate endpoint.
- **Bad, because every UI convenience becomes public API surface** — and public API is forever within
  v1. A one-off screen's needs get argued about at the level of a contract, which is slower and
  occasionally the wrong altitude.
- **Bad, because five gates are five things that can go yellow and get skipped.** Gate 4 in particular
  depends on Playwright coverage, and coverage that silently drops weakens the gate without failing
  it — hence the operation-coverage report on every PR, where a *drop* is the review signal.
- **Bad, because no SSR means no server-rendered SEO or link previews** beyond the three permitted
  surfaces, and a slower first paint on a cold cache.

### Reversal cost

Adding a BFF later is technically a week and strategically terminal: the moment a private channel
exists, the public API stops being dogfooded and the EQdkp outcome resumes.
