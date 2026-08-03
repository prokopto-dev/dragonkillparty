---
paths: ["internal/api/**/*.go", "openapi/**"]
description: The exact Huma v2 idiom, the Operation fields every route must set, the shared envelopes, and the architectural tests that reject a non-conforming route.
---

# API endpoints

Read [`internal/api/EXAMPLE_ENDPOINT.md`](../../internal/api/EXAMPLE_ENDPOINT.md) first — it walks
one resource end to end in nine ordered steps. This file is the reference for the parts an agent
gets wrong from memory.

## Huma v2. Not v1.

**This is the single most common hallucination in this codebase.** Huma v1's API does not exist here.

| You want | Huma v2 (correct) | Huma v1 (does not exist here) |
|---|---|---|
| Adapter | `humachi.New(router, huma.DefaultConfig(...))` | `huma.NewRouter(...)` |
| Declare an operation | `huma.Register(api, huma.Operation{...}, handler)` | `huma.Resource(...).Get(...)` |
| Handler | the third argument to `huma.Register` | `huma.Operation{Handler: ...}` |
| Handler signature | `func(ctx context.Context, in *In) (*Out, error)` | `func(w, r)` / positional params |
| Errors | return `huma.Error404NotFound(...)` or a `problem` type | write to `http.ResponseWriter` |

If you are typing `huma.Resource`, `huma.NewRouter`, or an `Operation` literal with a `Handler`
field, stop — you are writing v1 and it will not compile.

## The Operation struct — every field below is mandatory

```go
huma.Register(api, huma.Operation{
    OperationID:   "createRaidTick",              // lowerCamelCase, explicit, UNIQUE, NEVER renamed
    Method:        http.MethodPost,
    Path:          "/api/v1/raids/{raid_id}/ticks",
    Summary:       "Create a raid tick",
    Tags:          []string{"raids"},
    Security: []map[string][]string{
        {"pat": {"raids:write"}},                  // PAT scope, family:verb
        {"session": {}},                           // cookie sessions carry no scope
    },
    Metadata:      map[string]any{"x-dkp-permission": "raid.tick.create"}, // resource.action
    DefaultStatus: http.StatusCreated,
    Errors: []int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict,
        http.StatusUnprocessableEntity},
}, h.createRaidTick)
```

- `OperationID` is **public API**. The generated TS and Python SDK method names derive from it, so a
  rename is a breaking change even when the HTTP surface is untouched. `oasdiff` will catch it and
  the PR will need `!breaking-api` plus a human.
- `Security` and `x-dkp-permission` come from the generated catalogue in `internal/authz`. Do not
  invent a key or a scope: `role_permission` is FK-constrained to `permission(key)`, so a divergent
  key is a **boot failure**. Adding one is a schema change — stop and ask.
- Session-only operations (token minting, role edits, backup download, bulk PII read, import commit)
  declare `{"session": {}}` **only**, with no `pat` alternative. There is no all-powerful token.
- `Hidden: true` is allowed only on `/healthz`, `/readyz`, `/metrics`, the OAuth callback and the
  compat shim.

**One file per resource** under `internal/api/`. Never a shared registry file — it conflicts on
every parallel feature PR.

## Input and output structs

Tags drive validation, the spec, and the SDKs. There is no separate validation layer.

```go
type CreateRaidTickInput struct {
    RaidID         string `path:"raid_id"    format:"ulid"`
    IdempotencyKey string `header:"Idempotency-Key" required:"true"`
    IfMatch        string `header:"If-Match"`          // required on state transitions
    Body struct {
        PoolID           string `json:"pool_id"            format:"ulid"`
        EventTypeID      string `json:"event_type_id"      format:"ulid"`
        ValueCentipoints int64  `json:"value_centipoints"  minimum:"0"` // UNQUOTED integer
        OccurredAt       string `json:"occurred_at"        format:"date-time"`
    }
}

type RaidTickOutput struct {
    ETag     string `header:"ETag"`
    Location string `header:"Location"`
    Body     RaidTickDTO
}
```

| Concern | Rule |
|---|---|
| Money | `int64`, field suffix `_centipoints`, **unquoted** JSON integer. Never a string, never a float |
| Time | RFC 3339 with microsecond precision, always `Z` |
| Ids | ULID strings, `format:"ulid"` |
| Enums | lowercase `snake_case`, identical to the DB `CHECK` value. No translation layer |
| Optional patch fields | `*T` with `,omitempty` so "absent" and "set to zero" are distinguishable |
| `Idempotency-Key` | `required:"true"` on every POST that creates domain state |
| `If-Match` | `required:"true"` on state transitions and on PATCH of raids, ticks and pools |

## The shared envelopes — use the helper, never a bespoke shape

**Collections.** Cursor only, in the body. Never `Link` headers, never offset.

```json
{ "items": [ ... ], "next_cursor": "eyJ2IjoxLCJr…", "has_more": true }
```

`limit` default 50, max 200. `has_more` is authoritative; `next_cursor` is `null` at the end. The
cursor is base64url of `{v, k, id, f}`, HMAC-signed — build it with the helper so tamper detection
and `cursor_filter_mismatch` come for free.

`?since_seq=` is valid **only** on `/ledger/*`, `/audit` and `/events/replay` (canonical §4). Every
other collection uses the opaque ULID cursor. The outbox sequence is `event_seq`, never `seq`.

**Errors.** RFC 9457 `application/problem+json`, never HTTP 200 with an error body.

```json
{ "type": "https://docs.dragonkillparty.org/errors/insufficient_balance",
  "title": "Insufficient balance", "status": 409, "code": "insufficient_balance",
  "detail": "…", "instance": "/api/v1/bid-sessions/01JZ8Q…/bids",
  "request_id": "01JZ8QKB4N7Y3F0S6M2W9D5H1T",
  "meta": { "spendable_centipoints": 1200, "required_centipoints": 35000, "as_of_seq": 88213 },
  "errors": [] }
```

`code` is what SDKs discriminate on — a `type` URI is a documentation address and will move. The
enum is **closed**, lives in `internal/api/errors.go`, and adding a member is a spec change that
needs a docs page at the `type` URI or the docs-sync check fails.

Codes you will reach for most: `validation_failed` (422, populate `errors[]` with
`location`/`code`/`message`/`suggestions`), `not_found`, `permission_denied`, `insufficient_scope`
(always name `meta.required_scopes`), `precondition_required` (428), `precondition_failed` (412,
with `meta.current` and `meta.current_etag` so a bot merges in one round trip),
`idempotency_key_reused` (422), `idempotency_key_in_flight` (409), `conflict`,
`session_state_invalid`, `insufficient_balance`, `invariant_violation`, `unknown_character`,
`unknown_item`, `artifact_unparseable`, `rate_limited`, `engine_unsupported` (501).

## What `arch_test.go` will reject

| Omission | Assertion |
|---|---|
| Missing, empty or duplicate `OperationID` | uniqueness + non-empty over the Huma registry |
| Missing `Security` | security coverage |
| Missing `Metadata["x-dkp-permission"]` | permission coverage |
| Mutating POST under `/raids`, `/awards`, `/adjustments`, `/bids`, `/raid-submissions`, `/ledger` without a required `Idempotency-Key` | idempotency coverage |
| State transition or PATCH without `If-Match` | precondition coverage |
| `Hidden: true` outside the allowlist | no-hidden-operations |
| A bespoke list envelope or error shape | envelope shape |
| A route declared outside `internal/api` | package scan vs registry |

Beyond the arch tests, four CI gates apply: the committed `openapi/openapi.json` is regenerated and
diff-gated; `oasdiff breaking` runs against `origin/main`; a `kin-openapi` middleware validates
**every response in the whole integration suite** against its declared schema; and the PAT-parity
suite replays SPA request sequences with a scoped PAT and asserts identical responses.

## Stop and ask if

- The endpoint needs a **new permission key or scope** — generated catalogue, boot-failure blast radius.
- The operation does not fit REST plus action sub-resources. A state machine gets
  `POST /resource/{id}/close`, not `PATCH {state: "closing"}` — the latter lies about what happens.
- The response would embed a document that SSE also carries. SSE frames carry ids, never documents;
  there must be exactly one representation of a resource.
- You cannot make it idempotent.
- "The UI needs it but a bot would not." That distinction does not exist here.
