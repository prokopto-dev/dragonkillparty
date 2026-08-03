# ADR-0006 — REST, with the OpenAPI document derived from code

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

Bots are first-class consumers ([ADR-0005](0005-api-first-no-bff.md)), so the contract they read must
be correct — not "usually correct". The failure mode to design out is **spec drift**: a handwritten
spec that describes what the handlers did two releases ago. The consumers are heterogeneous (Python
log parsers, JavaScript Discord bots, Go tooling, spreadsheets), so the contract also has to be
readable by generic tooling with no bespoke client.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Spec-first: hand-written `openapi.yaml`, code generated from it (`ogen`) | The contract is designed and reviewed before code; the spec is unambiguously the source | The handlers can still diverge in behaviour; regeneration churn is large; a spec is a document nobody runs, so it is verified by discipline |
| B — Code-first: OpenAPI derived from Go handler types (Huma v2) | The spec cannot describe something the handlers do not implement, because the types *are* the source; validation, errors and the spec come from one declaration | You cannot review the API before writing Go; the spec diff arrives at code-review time; the library is a dependency on the critical path |
| C — GraphQL, gRPC + gateway, or a tRPC-style RPC contract | Strong typing and one schema; excellent for a single first-party client | A Python log parser cannot speak tRPC; gRPC needs codegen a guild bot author will not do; GraphQL adds an authorization surface per field. All three are hostile to "a bot author with `curl` and 20 minutes" |

## Decision outcome

**Chosen: B.** Operations are declared as Go structs; **Huma v2** derives the OpenAPI 3.1 + JSON
Schema 2020-12 document from the types the handler is compiled against. Request validation, the RFC
9457 `application/problem+json` error shape and the spec entry all come from that one declaration.
`openapi/openapi.json` is committed and diff-gated, and it is the input to the generated TypeScript
client the SPA uses and to the published Python and Go SDKs.

Rules that make the contract stable rather than merely present: `/api/v1` is **additive only** within
v1; a breaking change mints `/api/v2` and v1 lives 18 months with `Deprecation` and `Sunset` headers;
every operation sets an explicit `operationId` in lowerCamelCase, and because generated SDK method
names derive from it, **an `operationId` is public API and is never renamed**. `oasdiff` runs in CI
against `main`'s spec and fails any breaking change without a `!breaking-api` label and an
`docs/api-changelog.md` entry.

### Consequences

- Good, because the drift failure mode is structurally impossible: the spec is generated from the
  compiled types, and CI fails if the committed copy differs.
- Good, because one declaration yields validation, errors, the spec, three SDKs and the SPA's types —
  five artefacts that cannot disagree.
- Good, because REST over plain HTTP is the lowest possible barrier for a guild bot author, which is
  the audience that determines whether the API is actually used.
- **Bad, because you cannot design the API before writing the code.** The spec diff is the review
  artefact and it arrives late — after the handler exists and someone is attached to it. Spec-first
  would have caught bad resource shapes earlier, and we are giving that up.
- **Bad, because Huma is a bus-factor dependency** — one primary maintainer. Mitigated honestly rather
  than dismissed: it is a codegen layer over `net/http` with no runtime lock-in, forkable in a
  weekend, and the handlers remain ordinary Go.
- **Bad, because a convenient Go type is not always the JSON you want.** Struct tags end up carrying a
  lot of contract detail, and a careless field rename is a silent wire-format change caught only by
  the spec diff.
- **Bad, because `operationId` immutability is a permanent naming tax.** A name chosen badly on day
  one is a name we live with, because renaming it breaks every generated SDK even when the HTTP
  surface is unchanged.
- **Bad, because OpenAPI 3.1 tooling is still uneven.** Some generators lag JSON Schema 2020-12, so
  SDK generation occasionally works around the ecosystem rather than the spec.

### Reversal cost

Moving to spec-first inside REST is a release: keep the same document, invert which side generates.
Moving off REST entirely (to GraphQL or gRPC) is `/api/v2`, new SDKs, and stranding every bot the
compat shim was built to keep alive.
