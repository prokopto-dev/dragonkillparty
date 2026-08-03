# Getting started with the API

**Audience:** bot author, officer with a terminal.
Everything the web UI can do, a token can do. This page gets you from nothing to a working request in
about five minutes.

You need: the URL of your guild's instance, and an admin who can mint you a token.

## 1. Mint a token

Tokens belong to **service accounts**, not to people, so your bot keeps working when you leave the
guild. Minting is a **session + step-up** operation: it is done in the browser or on the server
console, never with another token. See [auth-and-scopes](auth-and-scopes.md) for why.

**In the browser.** *Admin → Service accounts → New*, then *Mint token*. Give it the narrowest scopes
that do the job — a read-only Discord `!dkp` command needs `dkp:read` and nothing else.

**On the server console**, which works before the SPA exists and when the SPA is broken:

```bash
dkp service-account create --name castle-steward --owner bob@example.org
dkp token mint --service-account castle-steward \
               --scopes dkp:read,raids:read \
               --expires 365d
```

```
dkp_pat_a91f3c2b_kXn2rQ7vT4wE8yU1iO5pA3sD6fG9hJ2kL0zX7cV4bN1m

This secret is shown once and is never retrievable. Store it now.
Prefix a91f3c2b is safe to log — it identifies the token without revealing it.
```

Put it in an environment variable. Never in the URL, never in a config file you commit.

```bash
export DKP_URL=https://dkp.example.org
export DKP_TOKEN=dkp_pat_a91f3c2b_kXn2rQ7vT4wE8yU1iO5pA3sD6fG9hJ2kL0zX7cV4bN1m
```

## 2. Check who you are

```bash
curl -s "$DKP_URL/api/v1/me" -H "Authorization: Bearer $DKP_TOKEN"
```

```json
{
  "principal_kind": "service_account",
  "service_account": {"id": "01JZ0SVCBDBT00000000000001", "name": "castle-steward"},
  "token": {"prefix": "a91f3c2b", "expires_at": "2027-08-02T11:04:22.310884Z"},
  "scopes": ["dkp:read", "raids:read"],
  "effective_permissions": ["dkp.read", "raid.read", "roster.read"],
  "rate_limit": {"policy": "read", "limit": 600, "remaining": 599, "reset_seconds": 60},
  "server_time": "2026-08-02T11:04:22.310884Z"
}
```

`effective_permissions` is **role permissions ∩ token scopes**. If something you expect is missing,
that is the answer — the token cannot exceed what its service account's role already grants.

Log the **prefix** at boot, never the secret. A leaked token pasted into a Discord channel is then
greppable, and revoking it is `dkp token revoke --prefix a91f3c2b`.

## 3. Make a real request

Standings for a pool, newest-first, twenty rows:

```bash
curl -s "$DKP_URL/api/v1/pools/01JZ0PNMAN0000000000000001/standings?limit=20" \
     -H "Authorization: Bearer $DKP_TOKEN"
```

```json
{
  "items": [
    { "account_id": "01JZ0ACCTTANKGY00000000001",
      "person_id": "01JZ0PERSTANKGY00000000001",
      "display_name": "Tankguy",
      "main_character": {"id": "01JZ0CHARTANKGY00000000001", "name": "Tankguy", "class_title": "Warlord"},
      "balance_centipoints": 522500,
      "balance_display": "5225.00",
      "spendable_centipoints": 522500,
      "attendance_30d_bp": 8750,
      "rank": 1 }
  ],
  "next_cursor": "eyJ2IjoxLCJrIjpbNTIyNTAwXSwiaWQiOiIwMUpaMEFDQ1RUQU5LR1kwMDAwMDAwMDAwMSJ9.tS1x",
  "has_more": true,
  "as_of_seq": 88241,
  "server_time": "2026-08-02T11:04:31.887204Z"
}
```

Four things in that response matter, and they are the same everywhere:

| Field | Meaning |
|---|---|
| `balance_centipoints` | **Points × 100, as an unquoted integer.** 522500 is 5225 points. There is no float anywhere in this API |
| `as_of_seq` | The pool's ledger position this answer was computed at. A balance is defined as of a `seq`, never as of a timestamp |
| `next_cursor` / `has_more` | [Cursor pagination](pagination-and-sync.md). There is no offset parameter |
| `attendance_30d_bp` | Basis points: 8750 is 87.50%. Ratios use `_bp` for the same reason money uses `_centipoints` |

## 4. Read an error

Errors are RFC 9457 problem documents with a stable machine `code`. **Discriminate on `code`**, never
on `detail` (prose changes) and never on `type` (a documentation address).

```bash
curl -s -i "$DKP_URL/api/v1/persons" -X POST \
     -H "Authorization: Bearer $DKP_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"display_name":"Newguy"}'
```

```http
HTTP/1.1 403 Forbidden
Content-Type: application/problem+json
X-Request-Id: 01JZ0REQ4M6P8R000000000001

{ "type": "https://docs.dragonkillparty.org/errors/insufficient_scope",
  "title": "Insufficient scope",
  "status": 403,
  "code": "insufficient_scope",
  "detail": "createPerson requires scope roster:write; this token holds dkp:read, raids:read.",
  "instance": "/api/v1/persons",
  "request_id": "01JZ0REQ4M6P8R000000000001",
  "meta": { "operation_id": "createPerson",
            "required_scopes": ["roster:write"],
            "token_scopes": ["dkp:read", "raids:read"] } }
```

Two things every client should do: keep `request_id` in your logs (the support workflow is "paste me
the request id"), and treat the [error catalogue](errors.md) as the contract — it says which codes are
retryable.

## 5. Write something

Every POST that creates domain state requires an `Idempotency-Key`, and it is not optional. A missing
key is `400 idempotency_key_required`. This exists because a parser on a home connection retries
constantly, and the failure mode is a double-credited raid night nobody notices for three weeks.

```bash
curl -s "$DKP_URL/api/v1/adjustments" -X POST \
     -H "Authorization: Bearer $DKP_TOKEN" \
     -H "Content-Type: application/json" \
     -H "Idempotency-Key: $(uuidgen)" \
     -d '{"adjustments":[
           {"account_id":"01JZ0ACCTHEABT000000000001",
            "pool_id":"01JZ0PNMAN0000000000000001",
            "value_centipoints":5000,
            "reason":"Spawn watch — 4h tracking"}]}'
```

Send the **same key with the same body** to retry safely: you get the original response back with
`Idempotency-Replayed: true` and nothing happens twice. See
[idempotency-and-concurrency](idempotency-and-concurrency.md).

## 6. Find the spec

| What | Where |
|---|---|
| The OpenAPI 3.1 document | `GET $DKP_URL/api/v1/openapi.json` |
| Browsable reference, served by the binary with no internet | `$DKP_URL/api/v1/docs` |
| Server version, feature flags, deprecations | `GET $DKP_URL/api/v1/meta` |
| TypeScript SDK | `npm i @dragonkillparty/sdk` — versioned in lockstep with the server |
| Python SDK | `pip install dkp-client` |

The spec is generated from the Go handler types and committed, and CI fails on any drift, so it
cannot describe an endpoint the server does not serve. Both SDKs ship the four helpers that decide
whether people actually use an SDK: a webhook signature verifier, an SSE resume helper, an
idempotency-key generator, and a retry policy that honours `Retry-After` and `RateLimit`.

## Next

- [auth-and-scopes](auth-and-scopes.md) — pick the right scopes, and know what a leak costs
- [idempotency-and-concurrency](idempotency-and-concurrency.md) — retries and `If-Match`
- [pagination-and-sync](pagination-and-sync.md) — catching up after your bot was offline
- [realtime](realtime.md) — live updates without a public HTTPS endpoint
- [errors](errors.md) — the closed code catalogue
