# ADR-0029 — Response validation on `pb33f/libopenapi-validator`

**Status:** proposed · **Date:** 2026-08-23 · **Deciders:** owner

## Context and problem statement

ROADMAP Phase 2 deliverable 5 puts a middleware in the stack that validates every request and
response against the generated OpenAPI whenever `DKP_ENV != production`, and
[`docs/design/04-testing.md`](../design/04-testing.md) §"Open item" left the library unchosen,
forbidding either answer until someone measured it. That item's premise — kin-openapi lacks 3.1 — is
stale: v0.147.0 supports 3.0, 3.1 and part of 3.2. Its *fear* is not, but it lives elsewhere.
`openapi/openapi.json` is 3.1 with a top-level `webhooks` block, four `type: [T,"null"]` unions, and
a `$ref` from every DTO Huma emits; the middleware runs across the whole integration suite, so what
it enforces has to be stateable in one sentence.

## Considered options

Measured 2026-08-23 by loading the committed spec into each library outside this repository and
validating twelve response bodies — good, bad and edge — against `getMeta` (nested `$ref`) and
`getGuild` (flat).

| Option | For | Against |
|---|---|---|
| A — `getkin/kin-openapi` v0.147.0 | Leaner: 7 new modules, ~8.8 MB linked. 3 284 stars against 146. No outbound HTTP — santhosh's default loader is `FileLoader` | **Two validators, chosen per schema, with no signal to the caller.** `openapi3filter.ValidateResponse` enables the JSON Schema 2020-12 engine for a 3.1 document, but `useJSONSchema2020` compiles the schema *alone* — `SchemaRef.MarshalYAML` emits `$ref` — so any schema with a nested `$ref` fails to compile and falls back to the legacy 3.0-era walker. Measured: every `MetaBody` case took the fallback, every `GuildDTO` case took 2020-12. Undocumented statuses pass unless `IncludeResponseStatus` is set. 56–127 µs per response |
| B — `pb33f/libopenapi-validator` v0.14.0 | One engine on every path: refs are resolved before compiling, so all twelve cases ran on santhosh 2020-12 with one error grammar and JSONPath locations (`$.server.version`). Rejects undocumented statuses by default. 5.7–8.2 µs per response, ~15× A, because compiled schemas are cached | 11 new modules and ~16.9 MB linked — the `dkp` binary goes from 19.7 MB to about 36 MB. Its schema compiler hard-wires `http`, `https` and `file` loaders with no opt-out. 146 stars, though `libopenapi` under it has 864 |
| C — Huma's own validation | No dependency at all. `huma.Validate` already resolves `$ref` against the registry that generates the spec, and `make verify-generated` proves that registry and the committed document agree | **Huma does not validate responses.** `transformAndWrite` transforms and marshals; nothing checks the bytes. `huma.ModelValidator` re-derives a schema from the Go struct, so it only proves the struct agrees with itself. Hand-rolling means owning header validation, status matching and 2020-12 semantics forever |

## Decision outcome

**Chosen: B.** A gate whose semantics change with whether the schema under it contains a `$ref` is
the failure `04-testing.md` names — "green builds that mean nothing" — even while, as today, both of
kin's engines return correct verdicts on all twelve cases. A's bifurcation is silent by construction
(`TestJSONSchema2020Validator_Fallback` shows it is deliberate) and untested in the response path:
kin ships no `openapi3filter` test pairing a 3.1 document with a `$ref`. B costs 8 MB and buys one enforceable sentence: *every request and
response on an API route is checked against the committed contract under JSON Schema 2020-12.*

Two requires, both direct — building the validator needs `libopenapi.NewDocument`:

```
github.com/pb33f/libopenapi v0.38.7
github.com/pb33f/libopenapi-validator v0.14.0
```

Every module either library pulls is MIT, Apache-2.0 or BSD, so `internal/licence` clears both
without an allowlist change. The installing PR owes four things:

1. **Requests *and* responses**, per `04-testing.md`: validating requests catches a test that lies
   about the API and a lenient handler that accepts it. Use the *sync* request path
   (`ValidateHttpRequestSyncWithPathItem`); `ValidateHttpRequestResponse` reaches the async one, which
   spawns a goroutine per validation and would pull `goleak.VerifyTestMain` into `internal/api`.
2. **The middleware in `internal/api/middleware`, gated on `DKP_ENV != production`, applied only to
   requests a Huma operation actually served.** It must NOT simply wrap the tree where `Problem` does:
   that mux also serves `/healthz`, `/readyz`, `/config.json`, the docs and the SPA, none of which are
   in the document, and `FindPath` reports an unknown path as a validation failure — so the naive
   placement 503s every non-API route the moment `DKP_ENV` is not `production`. A test asserting those
   five still answer normally under `DKP_ENV=test` is what pins it.
3. **Two negative tests**: a handler emitting a body its schema forbids fails the suite, and a request
   the spec forbids is refused rather than served.
4. **A new `SPEC009`** in `internal/specgate` asserting the committed document contains no absolute
   `$ref`, which keeps the hard-wired `https` loader unreachable rather than merely unused.

### Consequences

- Good, because the enforced semantics are one engine and one JSON Schema draft, statable without
  reference to the shape of the schema.
- Good, because undocumented status codes fail by default, which is the direction a drift gate should
  fail in.
- **Bad, because the shipped binary grows ~86%, from 19.7 MB to about 36 MB, for a code path that is
  inert in production.** A build tag would avoid it and would also break the operator contract in
  [`docs/operations/configuration.md`](../operations/configuration.md), which promises `DKP_ENV`
  works on the binary you were given.
- **Bad, because 11 modules and a full OpenAPI toolkit arrive to serve one middleware,** and the
  supply-chain surface is now proportional to a library whose bundler, generator and Arazzo support
  this project will never call.
- **Bad, because the schema compiler will fetch an `http`/`https` `$ref` and offers no way to say
  no.** `SPEC009` is a fence around the input, not a fix in the dependency.

### Reversal cost

Half a day: the middleware is one file behind one env check, and swapping in kin-openapi means
rewriting that file and re-running the twelve cases — but a spec that has meanwhile grown constructs
only one engine handles would make the swap a real migration.
