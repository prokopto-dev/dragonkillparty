# API design

**Status:** normative for the public contract. **Audience:** contributor, agent, bot author.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md). Where this
file and that one disagree, that one wins and this file has a bug.

The developer-facing guides live in [`docs/api/`](../api/). This file is the design record: why the
surface has this shape, what the whole surface is, and which mechanisms stop it drifting.

---

## 1. Style: REST + OpenAPI 3.1, derived from Go types

**Resource-oriented HTTP/JSON under `/api/v1`, described by one OpenAPI 3.1 document generated from
the Go handler types by Huma v2. Action sub-resources for state machines. One submission resource per
genuinely transactional workload. No GraphQL, no gRPC, no JSON-RPC.**

The decisive fact is the shape of the consumer population, not the shape of the data.

| Consumer | Realistic implementation | What it needs |
|---|---|---|
| Discord bid bot | Node or Python, volunteer-written, no public HTTPS endpoint | Plain `fetch`; live push without inbound ports |
| Log parser | Python tailing `eqlog_*.txt` on an officer's Windows PC | One POST, retry-safe, offline-tolerant |
| Officer in a terminal | `curl` | Copy-pasteable, human-readable errors |
| Guild spreadsheet | Sheets `IMPORTDATA` | A URL returning CSV with a token in the path |
| The SPA | React 19 + generated TS client | Full capability parity, ETags, conditional GET |
| Existing P99 bots | Castle Steward, bidbot2, jDKP, froakbot | `api.php`-shaped compat |

**Why not GraphQL.** One endpoint destroys HTTP caching, so a bot polling standings every 30 s can
never get a free `304` — and that conditional GET is the single largest bandwidth saving in the
product. Rate limiting degenerates into query-cost analysis. Field-level authorisation becomes a
bespoke layer instead of falling out of route-level scopes, so the gate "every route declares
`Security` and a permission key" has no analogue. There is no standard idempotency or optimistic
concurrency story for mutations, and point mutations are the whole product. On SQLite,
resolver-per-field is an N+1 generator that defeats the statement-count budget. And the compat shim
becomes impossible.

**Why not gRPC or Connect.** Genuinely close on merits. Rejected because it imposes generated stubs
on every consumer, browsers need grpc-web, `curl` stops being a first-class debugging tool, and the
P99 tooling community is hobbyists for whom "paste this curl into your bot" is the onboarding path.
The one thing it would buy — streaming — is served better by SSE (§8).

**Why not JSON-RPC.** EQdkp's `api.php?function=add_raid` *is* JSON-RPC over a query string with HTTP
200 for errors, and it is the incumbent's most-complained-about property. It survives only as
`/api/compat/eqdkp/api.php`.

### 1.1 Where REST is deliberately bent

1. **State machines get action sub-resources**, not `PATCH {"state": "closing"}`:
   `POST /bid-sessions/{id}/{open|close|resolve|override|settle|cancel}`,
   `POST /raids/{id}/finalize`, `POST /raid-submissions/{id}/commit`. A `PATCH` on a state field lies
   about what the server does — holds released, ledger written, timers cancelled.
2. **Transactional multi-entity writes get a submission resource**, not a batch envelope.
   `POST /raid-submissions` (§7) has a durable row, a preview state, an ETag and a receipt, because
   "this parsed raid" is a domain concept and needs a URI.
3. **No HATEOAS.** Bots hard-code paths. Responses carry `_links` only where a URL is genuinely
   opaque: artifact download, one-time claim, the docs page for an error type.

**Content types.** `application/json` in and out; `application/problem+json` for errors;
`multipart/form-data` for artifact and media upload; `text/event-stream` for SSE; `text/csv` on
explicit `Accept` for standings, ledger and signup exports; `text/calendar` and
`application/rss+xml` on feed routes. No XML in v1 — the compat shim emits EQdkp-shaped JSON.

---

## 2. Versioning and deprecation

**Major version in the path, `/api/v1`. Nothing else is versioned.** Not `Accept`-header versioning
(invisible in a browser, invisible in `curl`, breaks cache keys, and volunteer bot authors get it
wrong). Not date-based versioning (it needs a translation layer per release — enormous for a
volunteer project). Not per-resource versions.

Within v1 changes are **additive only**. `oasdiff` against `main` is the arbiter, not review.

| Allowed inside v1 | Forbidden inside v1 |
|---|---|
| New endpoints and `operationId`s | Removing or renaming an endpoint or `operationId` |
| New **optional** request fields, headers, query parameters | Adding a required request field; making an optional field required |
| New response fields | Removing or renaming a response field; changing its type or nullability |
| New **response** enum values; new **request** enum values | Removing an enum value from either side |
| New error `code`s for **new** conditions | Changing the `code` or status of an existing condition |
| Relaxing validation (raising `maxLength`, widening `maximum`) | Tightening validation |
| New optional `expand=` targets and sortable fields | Changing default sort, default `limit` or default filter behaviour |
| New scopes — existing tokens are unaffected | Requiring a new scope on an existing operation |
| New webhook event types | Changing an existing webhook payload or removing an event type |

**Response enums are open by contract.** Every response enum carries `x-dkp-open-enum: true` and is
generated as a plain string with named constants, never a closed union or `Literal`. Otherwise
shipping `bid_session.state = "resolution_failed"` in 1.3 raises a validation exception inside every
pinned Python client. A test injects a synthetic `__future__` value into a response fixture and
asserts both SDKs and the SPA tolerate it. Request enums are closed and validated.

**Deprecation policy.**

- The operation is marked `deprecated: true` and emits `Deprecation` (RFC 9745) and `Sunset`
  (RFC 8594) headers plus `Link: <https://docs.dragonkillparty.org/api/deprecations/#op>;
  rel="deprecation"`.
- **Minimum lifetime 18 months** from announcement, and never less than 12 months after `/api/v2`
  reaches GA. `/api/v1` is never removed from a 1.x binary.
- A counter `dkp_deprecated_operation_calls_total{operation_id,token_prefix}` feeds an admin banner:
  *"2 tokens are still calling 3 deprecated endpoints — `dkp_pat_a91f3c2b` (Castle Steward) last
  called `listRaidsLegacy` 4 minutes ago."* An officer can see whether upgrading is safe without
  asking the guild.
- `GET /meta` is the capability-negotiation endpoint. A well-written bot checks it at boot; a badly
  written one still works.
- **v2 would coexist in the same binary**, with v1 as a thin adapter over the same services. There is
  never a second path into the domain. Any v1 behaviour that cannot be expressed as an adapter over
  v2 is a v2 design bug.
- The compat shim is **deprecated from day one**: `Deprecation`/`Sunset` on first release, no sunset
  shorter than 24 months, `Hidden: true`, and rate-limited 5× harder than v1.

---

## 3. Reading the resource map

All paths are relative to `/api/v1` unless marked otherwise. Path IDs are ULIDs (`format: ulid`),
which is what makes a static segment like `/items/resolve` unambiguous against `/items/{item_id}`.

**Permission** is the `x-dkp-permission` value checked for every principal. **Scope** is the
`x-dkp-scopes` value additionally required of a PAT. Effective capability is
**role permissions ∩ token scopes** (canonical §6): a scope can only narrow, never widen.

Both columns draw from **one generated catalogue**, `internal/authz/catalogue.go`. The permission
keys in canonical §6 are the whole 1.0 catalogue; this document invents none. Where a resource has no
dedicated key it inherits the nearest one, and every inheritance is listed here rather than left to
be inferred:

| Resource | Inherits | Why |
|---|---|---|
| Event types, raid templates, artifacts, parse preview | `raid.read` / `raid.create` | Raid-domain catalogue and ingest inputs |
| Raid reopen | `raid.finalize` | The inverse of the same power |
| Item priority lists | `item.read` / `item.alias.manage` | Item-catalogue metadata, like aliases — not a separate power |
| Guild bank | `item.read` / `item.award` | A bank issue writes a ledger batch against the `guild_bank` account |
| Applications and recruitment review | `roster.read` / `roster.write` | An accepted application becomes a person |
| Portal blocks, menu, theme, team page, shoutbox | `cms.read` / `cms.write` | All are site content |
| Jobs, doctor, ledger verify | `ops.read` | Operational read surface |
| Service accounts, token list and rotation | `token.mint` | Same power, same gate |
| Pool recompute, custom field definitions | `admin.settings` | Instance configuration |
| Identity-provider credentials, MFA and session policy, the outbound-request allowlist, feed tokens | `admin.security.manage` | Anything whose compromise degrades the instance's security posture — canonical §6 |

Two sentinel values appear in the permission column and nowhere else. Both are allowlisted in
`TestOperations_PermissionSentinels_Allowlisted`, the same shape as the `Hidden: true` allowlist in
canonical §7:

- **`public`** — no credential required. Declares `security: []` explicitly.
- **`self`** — any authenticated principal, constrained to its own records or its own readable
  topics. Used by `/me`, `/events/*`, signups, disputes, claims, votes, and notification
  preferences.

Four routes live outside `/api/v1` and carry their own credential rather than a permission key:
`/healthz` and `/readyz` (public), `/metrics` (`DKP_METRICS_TOKEN`, on a separate listener), and
`/feeds/{feed_token}/…` (a single-purpose feed token).

> **Corrected in Phase 0 PR 4.** This paragraph used to continue "together with the compat shim they
> are exactly the `Hidden: true` allowlist of canonical §7". They are not: canonical §7 lists
> `/healthz`, `/readyz`, `/metrics`, **the OAuth callback** and the compat shim, and does not include
> `/feeds/{feed_token}/…`. Canonical conventions is the normative tie-breaker, so this document was
> the bug. The two lists overlap but are different questions — this one is "routes outside `/api/v1`
> carrying their own credential", canonical §7's is "operations permitted to be absent from the
> published spec" — and a route can be either without being both.
>
> The live allowlist is `api.HiddenOperationAllowlist()`, enforced by
> `TestArch_HiddenOperations_AreAllowlisted`. It carries **four** entries rather than five because
> the OAuth callback's path is not written down anywhere in this repository; whoever adds that route
> adds its path. If `/feeds/{feed_token}/…` should also be Hidden-eligible, that is an addition to
> canonical §7 first, and then to the allowlist.

A dash in the **Scope** column means **no PAT scope exists for this operation**: it is
**session-only**, and the operations marked **step-up** additionally require re-authentication within
5 minutes. There is no `admin:*` scope and no all-powerful token (canonical §6). Session-only covers
minting, rotating and revoking tokens; editing roles and role assignments; downloading or restoring
backups; reading PII in bulk; running and committing an import; and exporting the audit log.

---

## 4. The resource map

### 4.1 Discovery, health, search

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET | `/meta` | Server version, feature flags, enabled login providers, point label, deprecations, `api_versions` | `public` | — |
| GET | `/me` | Principal introspection: kind, scopes, token prefix, expiry, service account, effective permissions, rate limit | `self` | any |
| GET | `/openapi.json` · `/openapi.yaml` | The spec | `public` | — |
| GET | `/docs` | Embedded Scalar reference, served offline by the binary | `public` | — |
| GET | `/search` | Federated search over persons, characters, items and articles; each shard filtered by the caller's own read permission | `roster.read` | `roster:read` |
| GET | `/parsers` | Log and dump format catalogue with sample lines and per-format capability flags | `raid.read` | `logs:ingest` |
| GET | `/strategies` | Point-strategy catalogue: id, version, balance kinds, config JSON Schema, declared invariants | `dkp.read` | `dkp:read` |
| GET | `/healthz` | Liveness. **Touches no database** (canonical §13). Outside `/api/v1`, `Hidden` | `public` | — |
| GET | `/readyz` | Readiness: DB reachable, migrations at expected version, worker heartbeat fresh. Outside `/api/v1`, `Hidden` | `public` | — |
| GET | `/metrics` | Prometheus. Disabled by default, separate listener, `DKP_METRICS_TOKEN`. **Never gated by a PAT scope** (canonical §14). `Hidden` | `DKP_METRICS_TOKEN` | — |

### 4.2 Guild and instance configuration

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · PATCH | `/guild` | Name, tag, timezone, week start, point label, display precision, `inactive_after_days`, `auto_set_inactive`, `hide_inactive` | `roster.read` · `admin.settings` | `roster:read` · — |
| GET · POST | `/servers` | EQ servers (Blue, Green, Red) with era | `roster.read` · `admin.settings` | `roster:read` · — |
| GET · PATCH · DELETE | `/servers/{server_id}` | | `roster.read` · `admin.settings` | `roster:read` · — |
| GET · PATCH | `/admin/settings` | Typed settings document; mirrors `DKP_*` where runtime-overridable. Secret-valued settings render as `***` when set and `null` when unset — the secret type from [`../operations/configuration.md`](../operations/configuration.md), not a redaction this endpoint invents. **`GET` is as gated as `PATCH` anyway**: redaction hides the values, not the shape, and the response still names the configured identity provider, the MFA policy and the outbound CIDR allowlist | `admin.security.manage` | — **step-up** |
| GET | `/admin/doctor` | The checks `dkp doctor` runs, as JSON | `ops.read` | — |
| GET | `/admin/jobs` · `/admin/jobs/{job_id}` | River queue visibility | `ops.read` | — |
| POST | `/admin/jobs/{job_id}/cancel` · `/retry` | | `admin.settings` | — |
| GET · POST | `/admin/roles` · `/admin/roles/{role_id}` · `/admin/role-assignments` | Roles, `role_permission`, scoped assignments | `admin.roles.manage` | — **step-up** |
| GET | `/admin/permissions` | The generated catalogue, as served to the SPA | `admin.roles.manage` | — |
| GET · POST · DELETE | `/feed-tokens` · `/feed-tokens/{token_id}` | Single-purpose read-only feed tokens. A feed token is a bearer credential that outlives the session which minted it, so canonical §6's "minting/rotating/revoking tokens" covers it | `admin.security.manage` | — **step-up** |

> **Corrected in Phase 0 PR 5, on three counts.**
>
> **`/admin/settings` and `/feed-tokens` moved to `admin.security.manage`,** a key canonical §6 gained
> in the same change. `admin.settings` guarded nineteen operations from renaming a guild to editing
> OIDC credentials, and [`03-security.md`](03-security.md) leans on that surface being step-up and
> PAT-forbidden so a leaked token cannot relax the SSRF allowlist and pivot — a guarantee one key
> with nineteen blast radii cannot make. Everything else in this table keeps `admin.settings` and is
> session-only *without* step-up, including `PUT /pools/{pool_id}/strategy`: it is the
> highest-consequence configuration change in the product, and it is deliberately not a security key,
> because it leaks nothing, enables no pivot, and is already governed by an audit event,
> `?dry_run=true` and an append-only ledger.
>
> **The `/guild` row lost "away-mode toggle".** Away mode is three columns on `person`
> ([`01-domain-model.md`](01-domain-model.md) §6.1: `away_from_at`, `away_until_at`, `away_note`) and
> has no `guild` column anywhere. The domain model is the schema authority, so this row was the bug.
> `ROADMAP.md` carried the same error and is corrected with it.
>
> **The `/guild` row said "rounding on/off and precision"; there is one setting, not two.**
> `guild.points_precision` is `0..2` and already expresses it — a separate boolean would be a second
> source of truth for the same display decision. Storage is always `_cp` regardless (canonical §1).
>
> **The `/guild` row was transcribed from EQdkp's config table, and two keys were never renamed.**
> It said `inactive_period` and `auto_set_active`; the columns are `inactive_after_days` and
> `auto_set_inactive`. The second is a **polarity flip** — "auto set active" is the opposite control
> from "auto set inactive" — so a bot written from this row would have set the wrong value, and
> nothing in the contract would have said so.
>
> Both names come straight from [`05-migration.md`](05-migration.md)'s list of EQdkp `<prefix>config`
> keys, which is also where "rounding on/off and precision" came from: EQdkp carries
> `round_activate` **and** `round_precision`, DKP carries one `points_precision`. Other keys in that
> list *were* correctly renamed on the way in — `dkp_name` → `points_label`, `guildtag` → `tag` —
> which is what made the two survivors hard to see.
>
> The rule this violates is ordinary, not exotic: **`05-migration.md` names EQdkp's keys because the
> importer must read them; this document defines DKP's own contract and must use DKP's own names.**
> A source-system identifier reaching the public API is a wrong contract first and a licence-firewall
> question second (canonical §15).

### 4.3 Persons, accounts, characters

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · POST | `/persons` | The human; the balance holder | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| GET · PATCH · DELETE | `/persons/{person_id}` | `DELETE` soft-deactivates | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| GET | `/persons/{person_id}/characters` | Characters owned, with the main flag | `roster.read` | `roster:read` |
| GET | `/persons/{person_id}/attendance` | `?window=30d\|60d\|90d\|lifetime\|custom&from=&to=&pool=&metric=raids\|ticks\|days&with_alts=`, numerator and denominator both exposed | `dkp.read` | `dkp:read` |
| POST | `/persons/{person_id}/merge` | Merge another person in; emits a `re_attribution` batch; preview first | `person.merge` | `roster:write` |
| GET · PATCH | `/persons/{person_id}/away` | Away mode: window and note | `roster.read` · `roster.write` (or `self`) | `roster:read` · `roster:write` |
| GET · PATCH | `/persons/{person_id}/fields` | Typed custom field values | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| GET · PATCH | `/persons/{person_id}/notification-preferences` | Per-member opt-in per notification type | `self` | `roster:read` |
| GET | `/persons/{person_id}/pii` | Email, identities, IP history | `person.pii.read` | — **step-up** |
| GET · POST · PATCH · DELETE | `/character-field-defs` · `/character-field-defs/{def_id}` | Typed, queryable custom field definitions | `admin.settings` | — |
| GET · POST | `/characters` | `?server=&status=&class=&unclaimed=true` | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| GET · PATCH · DELETE | `/characters/{character_id}` | Class, level, race, rank, `person_id`, role, status | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| POST | `/characters/{character_id}/rename` | Records rename history; attendance follows the character | `roster.write` | `roster:write` |
| GET | `/characters/{character_id}/names` | Rename history | `roster.read` | `roster:read` |
| POST | `/characters/resolve` | **Batch** name → character resolution with scores and `unresolved[]`. Read-only, no side effects, no idempotency key | `roster.read` | `roster:read` |
| GET · POST | `/character-claims` | List, or request a claim | `self` | `roster:read` |
| POST | `/character-claims/{claim_id}/approve` · `/reject` · `/challenge` | Officer decision; `challenge` issues a guild-chat nonce | `character.claim.approve` | `roster:write` |
| GET · POST | `/ranks` · `/ranks/{rank_id}` | Guild ranks, hidden flag, sort, prefix and suffix | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| GET · POST | `/raid-roles` · `/raid-roles/{role_id}` | Tank, heal, DPS | `roster.read` · `roster.write` | `roster:read` · `roster:write` |
| GET · POST | `/raid-groups` · `/raid-groups/{group_id}` · PUT `/members` | Named groups and leaders | `roster.read` · `roster.write` | `roster:read` · `roster:write` |

**`/accounts/{account_id}` is the canonical balance path** (canonical §6, defect 12). `person` and
`account` are different entities: a person has one account, and the system accounts
(`guild_bank`, `residue`, `write_off`, `import_opening`) have no person at all. They are addressable
here, which is what makes the `Conserved` invariant verifiable through the API rather than only
inside the binary.

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET | `/accounts` | `?kind=person\|system&pool=`. System accounts are listed, never hidden | `dkp.read` | `dkp:read` |
| GET | `/accounts/{account_id}` | Identity plus per-pool balances and `as_of_seq` | `dkp.read` | `dkp:read` |
| GET | `/accounts/{account_id}/balance` | `?pool=&balance_kind=&as_of_seq=` → `{amount_centipoints, as_of_seq}` | `dkp.read` | `dkp:read` |
| GET | `/accounts/{account_id}/spendable` | `{balance, holds, spendable, as_of_seq}` — what a bid may draw on | `bid.read` | `bids:read` |
| GET | `/accounts/{account_id}/ledger` | The statement view: date, kind, description, delta, running balance, actor, source, batch link. Cursor-paginated; for incremental sync use `/ledger/entries?account=` | `dkp.read` | `dkp:read` |

`GET /persons/{person_id}?expand=points` is a convenience projection over the same data. It is an
expansion, not a second source.

### 4.4 Pools, strategies, event types

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · POST | `/pools` | Currency plus strategy plus config | `dkp.read` · `admin.settings` | `dkp:read` · — |
| GET · PATCH · DELETE | `/pools/{pool_id}` | `PATCH` requires `If-Match`; a config change emits an audit event and never rewrites history | `dkp.read` · `admin.settings` | `dkp:read` · — |
| GET · PUT | `/pools/{pool_id}/strategy` | Strategy id, version and config validated against the strategy's JSON Schema. `?dry_run=true` returns the migration diff | `admin.settings` | — |
| GET · PUT | `/pools/{pool_id}/event-types` | Which event types feed this pool, with `no_attendance` per mapping | `admin.settings` | — |
| GET · PUT | `/pools/{pool_id}/item-pools` | Which item pools drain this pool | `admin.settings` | — |
| GET | `/pools/{pool_id}/standings` | The headline table. `?columns=&with_alts=&rank=&class=`, cursor-paginated, weak `ETag`, `Accept: text/csv` | `dkp.read` | `dkp:read` |
| GET | `/pools/{pool_id}/leaderboard` | Top-N per class or per role — the block on every EQdkp front page | `dkp.read` | `dkp:read` |
| POST | `/pools/{pool_id}/recompute` | Enqueue a snapshot rebuild and verify job; returns a job ref | `admin.settings` | — |
| GET · POST | `/item-pools` · `/item-pools/{item_pool_id}` | | `dkp.read` · `admin.settings` | `dkp:read` · — |
| GET · POST | `/event-types` | Encounter catalogue with default value, zone, era, server | `raid.read` · `raid.create` | `raids:read` · `raids:write` |
| GET · PATCH · DELETE | `/event-types/{event_type_id}` | | `raid.read` · `raid.create` | `raids:read` · `raids:write` |

### 4.5 Raids, ticks, attendance

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · POST | `/raids` | `?server=&pool=&state=&event_type=&started_at__gte=` | `raid.read` · `raid.create` | `raids:read` · `raids:write` |
| GET · PATCH · DELETE | `/raids/{raid_id}` | `PATCH` requires `If-Match`; `DELETE` only while `draft` | `raid.read` · `raid.update` | `raids:read` · `raids:write` |
| POST | `/raids/{raid_id}/finalize` | Close the session and post pending batches. `If-Match` | `raid.finalize` | `raids:write` |
| POST | `/raids/{raid_id}/reopen` | Audited, reason required | `raid.finalize` | `raids:write` |
| PUT | `/raids/{raid_id}/connected-raids` | Link N raids so they count once in the attendance denominator | `raid.update` | `raids:write` |
| GET · POST | `/raids/{raid_id}/ticks` | **Bulk-native**: the body is always `{"ticks":[…]}`. **Every tick carries its own `pool_id`**, so one raid can feed two pools. One transaction, one `Idempotency-Key`, natural dedupe on `(raid_id, content_sha256)` | `raid.read` · `raid.tick.create` | `raids:read` · `raids:write` |
| GET · PATCH | `/ticks/{tick_id}` | `PATCH` emits a corrective batch, never an update | `raid.read` · `raid.tick.create` | `raids:read` · `raids:write` |
| DELETE | `/ticks/{tick_id}` | Emits a reversal batch | `raid.tick.delete` | `raids:write` |
| GET | `/raids/{raid_id}/attendance` | Flattened matrix: character × tick × status × weight | `raid.read` | `raids:read` |
| GET · POST | `/raids/{raid_id}/kills` | Kill credits: event type, time, FTE line reference | `raid.read` · `raid.update` | `raids:read` · `raids:write` |
| GET | `/raids/{raid_id}/artifacts` | Raw uploads attached to this raid | `raid.read` | `raids:read` |
| GET · POST | `/raid-templates` · `/raid-templates/{template_id}` | Saved raid and signup templates | `raid.read` · `raid.create` | `raids:read` · `raids:write` |

### 4.6 Items, awards, adjustments, priorities

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · POST | `/items` | `?q=` FTS, `?era=&slot=` | `item.read` · `item.alias.manage` | `loot:read` · `loot:award` |
| GET · PATCH · DELETE | `/items/{item_id}` | | `item.read` · `item.alias.manage` | `loot:read` · `loot:award` |
| GET | `/items/{item_id}/history` | Every award across guild history plus the price distribution | `item.read` | `loot:read` |
| POST | `/items/resolve` | **Batch** name → item: exact, normalised, alias, then fuzzy. Scores and `unresolved[]`. Read-only | `item.read` | `loot:read` |
| GET · POST · DELETE | `/item-aliases` · `/item-aliases/{alias_id}` | "CoF" → Cloak of Flames | `item.read` · `item.alias.manage` | `loot:read` · `loot:award` |
| GET · POST | `/item-priorities` · `/item-priorities/{list_id}` | Per class and slot, versioned, referenced from the bid board and the loot screen | `item.read` · `item.alias.manage` | `loot:read` · `loot:award` |
| GET · POST | `/awards` | **Batch-native**: `{"awards":[…]}` for multi-buyer splits and end-of-night dumps | `item.read` · `item.award` | `loot:read` · `loot:award` |
| GET | `/awards/{award_id}` | The award with its ledger batch, tie-break reason and source | `item.read` | `loot:read` |
| POST | `/awards/{award_id}/reverse` | Reversal batch, never a delete. Reason required | `ledger.reverse` | `loot:award` |
| GET · POST | `/adjustments` | **Batch-native**: `{"adjustments":[…]}` → one batch | `dkp.read` · `dkp.adjust` | `dkp:read` · `dkp:adjust` |
| GET | `/adjustments/{adjustment_id}` | | `dkp.read` | `dkp:read` |
| POST | `/adjustments/{adjustment_id}/reverse` | | `ledger.reverse` | `dkp:adjust` |

### 4.7 Ledger and decay

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET | `/ledger/batches` | `?pool=&kind=&since_seq=&effective_at__gte=&actor=&source=` | `dkp.read` | `dkp:read` |
| GET | `/ledger/batches/{batch_id}` | Header, every entry, the strategy and config snapshot, hash-chain links | `dkp.read` | `dkp:read` |
| POST | `/ledger/batches/{batch_id}/reverse` | **The only correction primitive.** Reason and `Idempotency-Key` required | `ledger.reverse` | the originating domain's write scope |
| GET | `/ledger/entries` | Flat entry stream for incremental sync: `?since_seq=&account=&pool=&balance_kind=` | `dkp.read` | `dkp:read` |
| GET | `/ledger/verify` | Last verify-job result: snapshot drift, hash-chain integrity, conservation | `ops.read` | — |
| GET · POST | `/decay-runs` | Preview a decay, cap or start-points run; the preview returns the per-account delta table | `dkp.decay.run` | `dkp:adjust` |
| POST | `/decay-runs/{run_id}/commit` | Post the batch. Idempotent on `(pool_id, cadence_period)` | `dkp.decay.run` | `dkp:adjust` |

### 4.8 Bid sessions

Live-uniqueness is `UNIQUE(item_instance_id) WHERE state IN ('draft','open','extended','closing',
'resolved')` — one live session per dropped item, independent of which raid row it hangs off.

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · POST | `/bid-sessions` | Create in `draft`, or `"auto_open": true` | `bid.read` · `bid.manage` | `bids:read` · `bids:manage` |
| GET | `/bid-sessions/{session_id}` | Full representation, strong `ETag`. Sealed amounts withheld until `state` is `closing` or later | `bid.read` | `bids:read` |
| GET | `/bid-sessions/{session_id}/board` | Compact projection for a Discord embed: leader, price, `closes_at`, `server_time`, bid count, extensions used | `bid.read` | `bids:read` |
| GET · POST | `/bid-sessions/{session_id}/bids` | List or place. `Idempotency-Key` required; the body may carry `expected_state`. `?reveal=true` before `closing` requires `bid.reveal_early` and self-audits | `bid.read` · `bid.manage` | `bids:read` · `bids:manage` |
| POST | `/bid-sessions/{session_id}/bids/{bid_id}/retract` | Appends a retraction row; never mutates | `bid.manage` | `bids:manage` |
| POST | `/bid-sessions/{session_id}/open` · `/close` · `/cancel` | `If-Match` | `bid.manage` | `bids:manage` |
| POST | `/bid-sessions/{session_id}/resolve` | Winner, price, tie-break reason. **No ledger write.** `If-Match` | `bid.manage` | `bids:manage` |
| POST | `/bid-sessions/{session_id}/override` | Officer changes winner or price before settle; reason mandatory | `bid.manage` | `bids:manage` |
| POST | `/bid-sessions/{session_id}/settle` | The only transition that writes to the ledger. `Idempotency-Key` + `If-Match` | `bid.manage` **and** `item.award` | `bids:manage` **and** `loot:award` |
| GET | `/bid-sessions/{session_id}/holds` | Active and released holds against accounts | `bid.read` | `bids:read` |

### 4.9 Log ingest: artifacts, previews, submissions

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| POST | `/artifacts` | Upload a `RaidRoster-*.txt`, log slice, `/who` paste or guild dump. `multipart/form-data` or JSON with base64. **Content-addressed, so no `Idempotency-Key`**: identical bytes return `200` with the existing artifact | `raid.create` | `logs:ingest` |
| GET | `/artifacts` · `/artifacts/{artifact_id}` | sha256, size, kind, uploader, detected format, linked raid | `raid.read` | `raids:read` |
| GET | `/artifacts/{artifact_id}/content` | Raw bytes. This is the member-facing "show me the dump" path and the strongest anti-drama control in the design (canonical §11) | `raid.read` | `raids:read` |
| POST | `/artifacts/{artifact_id}/parse` | Parse a stored artifact, returning the structured preview | `raid.create` | `logs:ingest` |
| POST | `/parse/preview` | Parse inline text, storing nothing. The bot-development endpoint | `raid.read` | `logs:ingest` |
| POST | `/raid-submissions` | **The flagship** (§7). A whole parsed raid → a durable `preview` row, or `"auto_commit": true` for one shot | `raid.create` (plus `item.award` and `dkp.adjust` when those sections are non-empty) | `raids:write` (plus `loot:award`, `dkp:adjust`) |
| GET | `/raid-submissions/{submission_id}` | The preview and, after commit, the receipt | `raid.read` | `raids:read` |
| POST | `/raid-submissions/{submission_id}/commit` | Apply atomically. `Idempotency-Key` + `If-Match` | `raid.create` | `raids:write` |
| POST | `/raid-submissions/{submission_id}/abandon` | | `raid.create` | `raids:write` |

### 4.10 Reconciliation, disputes, calendar, saved views

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET | `/reconciliation` | The quarantine queue: unknown character names, unresolved item names, orphaned loot lines. `?status=open` | `raid.read` | `raids:read` |
| GET | `/reconciliation/{item_id}` | | `raid.read` | `raids:read` |
| POST | `/reconciliation/{item_id}/resolve` | Body carries `status` from `mapped \| created \| ignored \| merged` — the same values the column holds | `raid.update` | `raids:write` |
| POST | `/reconciliation/{item_id}/report-parser-bug` | A pre-filled, redacted issue link | `raid.read` | `raids:read` |
| GET · POST | `/disputes` · `/disputes/{dispute_id}` | Member-raised, linked to a raid, tick or award | `self` · `dkp.read` | `dkp:read` |
| POST | `/disputes/{dispute_id}/resolve` | Resolution links the corrective batch | `dkp.adjust` | `dkp:adjust` |
| GET · POST | `/calendar-events` · `/calendar-events/{event_id}` | Raid calendar, repeating events | `calendar.read` · `calendar.write` | `calendar:read` · `calendar:write` |
| GET · PUT · DELETE | `/calendar-events/{event_id}/signups` · `/signups/{character_id}` | Status, role, raid group, note | `self` · `signup.manage` | `calendar:read` · `calendar:write` |
| GET | `/calendar-events/{event_id}/signups.csv` | The signup list annotated with each signee's current balance and attendance — the sheet a raid leader actually picks from | `calendar.read` **and** `dkp.read` | `calendar:read` **and** `dkp:read` |
| POST | `/calendar-events/{event_id}/transform-to-raid` | Convert a signed-up event into a raid, carrying attendees | `raid.create` | `raids:write` |
| GET · POST · PATCH · DELETE | `/table-views` · `/table-views/{view_id}` | Saved standings column sets, so a bot can render exactly what the officers see | `dkp.read` · `roster.write` | `dkp:read` · `roster:write` |

### 4.11 Portal and CMS

Full portal parity is owner-mandated scope, sequenced as Phase 7. **The API is the only way the SPA
reaches any of it** — there is no template-rendering back door, and gate 3 in §10 proves it.
Untrusted rich text lives only in `internal/cms`, and `internal/richtext` is the only place HTML is
produced from user input.

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET · POST | `/articles` | `?category=&tag=&featured=&state=`. Markdown is the source of truth; `body_html` is server-rendered | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · PATCH · DELETE | `/articles/{article_id}` | `PATCH` requires `If-Match` | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| POST | `/articles/{article_id}/publish` · `/unpublish` | `show_from` / `show_to` scheduling, featured flag | `cms.write` | `cms:write` |
| GET · POST · PATCH · DELETE | `/article-categories` · `/article-categories/{category_id}` | Slugs, aliases, per-category comment toggle, portal layout binding | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · POST | `/article-tags` | | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · POST | `/articles/{article_id}/comments` | | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| PATCH · DELETE | `/comments/{comment_id}` | Own comment via `self`; anyone else's requires moderation | `self` · `cms.moderate` | `cms:write` |
| POST | `/comments/{comment_id}/flag` | Member report | `self` | `cms:read` |
| POST | `/comments/{comment_id}/moderate` | Approve, hide, soft-delete | `cms.moderate` | `cms:write` |
| POST | `/articles/{article_id}/vote` | | `self` | `cms:write` |
| GET · POST | `/media` | Content-addressed upload; re-encoded server-side to WebP; EXIF destroyed; **SVG rejected outright** | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · DELETE | `/media/{media_id}` · GET `/media/{media_id}/content` | | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · POST | `/shouts` | Shoutbox. Rate-limited, sanitised, delivered over the same outbox as everything else | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| DELETE | `/shouts/{shout_id}` | | `cms.moderate` | `cms:write` |
| GET · PUT | `/portal-layouts` · `/portal-layouts/{route}` | Block arrangement, bound per route and per article category, with a separate mobile layout | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · POST · PATCH · DELETE | `/portal-blocks` · `/portal-blocks/{block_id}` | Standings, next raids, last raids, last items, last comments, guild news, leaderboard, who-is-online, birthdays, login, search | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · PUT | `/menu` | `menu_item` tree with permission-gated visibility | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · PUT | `/theme` | Design tokens with a contrast validator, logo, favicon, banner, column widths | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · PUT | `/team` | Officers and staff by role, ordered, with blurbs | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · POST | `/guild-bank` · `/guild-bank/{entry_id}` | Inventory, holder, notes, request workflow | `item.read` · `item.award` | `loot:read` · `loot:award` |
| POST | `/guild-bank/{entry_id}/issue` | Issue to a member; a bank sale credits the `guild_bank` system account through an ordinary ledger batch | `item.award` | `loot:award` |
| GET · POST · PATCH | `/recruitment-openings` · `/recruitment-openings/{opening_id}` | Per-class openings | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| GET · PUT | `/application-form` | Questionnaire builder | `cms.read` · `cms.write` | `cms:read` · `cms:write` |
| POST | `/applications` | **Public submission, no account required.** Rate-limited by IP, sanitised, no scope | `public` | — |
| GET | `/applications` · `/applications/{application_id}` | Officer review | `roster.read` | `roster:read` |
| POST | `/applications/{application_id}/decide` | Accept, reject or hold, with a reason | `roster.write` | `roster:write` |

### 4.12 Realtime, webhooks, tokens

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET | `/events/stream` | Multiplexed SSE (§8). Per-topic authorisation uses each topic's own read permission | `self` | `events:subscribe` |
| POST | `/events/ticket` | 30-second single-use ticket for cross-origin browser clients | `self` | `events:subscribe` |
| GET | `/events/replay` | `?since_seq=&topics=&limit=` — outbox catch-up as plain JSON. Here `since_seq` is an **`event_seq`**, and every frame echoes `event_seq` so it cannot be confused with a ledger `seq` | `self` | `events:subscribe` |
| GET · POST | `/webhooks` | Register a URL, event filter and description. The signing secret appears **once** | `webhook.manage` | `webhooks:manage` |
| GET · PATCH · DELETE | `/webhooks/{webhook_id}` | | `webhook.manage` | `webhooks:manage` |
| POST | `/webhooks/{webhook_id}/rotate-secret` | Dual-secret overlap window | `webhook.manage` | `webhooks:manage` |
| POST | `/webhooks/{webhook_id}/test` | Synthetic `ping` delivery | `webhook.manage` | `webhooks:manage` |
| GET | `/webhooks/{webhook_id}/deliveries` | Status, attempts, response code, first 2 KB of the response body | `webhook.manage` | `webhooks:manage` |
| POST | `/webhooks/{webhook_id}/deliveries/redeliver` | Bulk replay from the dead-letter queue | `webhook.manage` | `webhooks:manage` |
| GET · POST | `/service-accounts` · `/service-accounts/{account_id}` | Bot identities with a recorded human owner | `token.mint` | — **step-up** |
| GET · POST | `/service-accounts/{account_id}/tokens` | Mint a PAT. The secret is returned once and is never retrievable | `token.mint` | — **step-up** |
| GET | `/tokens` · `/tokens/{token_id}` · `/tokens/{token_id}/activity` | Prefix, scopes, `last_used_at`, `last_used_ip`, `expires_at`, `superseded_by`, per-token request log | `token.mint` | — |
| POST | `/tokens/{token_id}/rotate` | New secret plus an overlap window | `token.mint` | — **step-up** |
| DELETE | `/tokens/{token_id}` | Immediate revocation | `token.revoke` | — **step-up** |
| POST | `/tokens/{token_id}/panic` | Revoke, then return a dry-run reversal preview of everything that token wrote | `token.revoke` | — **step-up** |

### 4.13 Audit, import, backup, feeds, compat

| Method | Path | Purpose | Permission | Scope |
|---|---|---|---|---|
| GET | `/audit` · `/audit/{entry_id}` | Immutable log with a gapless `seq` and a hash chain. `?actor=&token=&resource=&action=&since_seq=&at__gte=` | `audit.read` | — |
| POST | `/audit/export` | Signed export | `audit.read` | — **step-up** |
| GET · POST | `/admin/imports` | List, or start an EQdkp Plus import. **`--dry-run` is the default** | `import.run` | — |
| GET | `/admin/imports/{import_id}` · `/report` | Fingerprint, capability flags, row counts, mojibake repairs, reconciliation preview | `import.run` | — |
| POST | `/admin/imports/{import_id}/commit` | | `import.commit` | — **step-up** |
| GET · POST | `/admin/backups` | List, or trigger. Download streams; there is no long-lived signed URL | `admin.backup` | — **step-up** |
| POST | `/admin/backups/{backup_id}/restore` | Dry-run first; reports what would be replaced | `admin.backup` | — **step-up** |
| GET | `/feeds/{feed_token}/raids.ics` · `/calendar.ics` · `/standings.csv` · `/articles.xml` | Path-embedded because calendar and feed readers cannot set headers. Single-purpose, read-only, independently revocable, **never a PAT**. Outside `/api/v1` | feed token | — |
| GET · POST | `/api/compat/eqdkp/api.php` | The migration bridge. `?atoken=` accepted here and nowhere else, logged with a deprecation counter naming the token prefix. `Hidden` | mapped from EQdkp permissions | legacy token class |

---

## 5. Cross-cutting contract

Full treatment is in the guides; this is the index and the fixed decisions.

| Concern | Rule | Guide |
|---|---|---|
| Money | Unquoted JSON **integer** centipoints, field suffix `_centipoints`. Read models may carry a `_display` sibling string computed with the guild's rounding config. No float anywhere, including inside `meta` on a problem document | canonical §1 |
| Time | RFC 3339 UTC with microsecond precision, always `Z`. Every response describing a deadline also carries `server_time` so clients render `closes_at − server_time + local_elapsed` and never trust their own clock | canonical §2 |
| Ordering | Ledger `seq` is **per pool**; outbox `event_seq` is **global**. Never order by timestamp | canonical §4 |
| Auth | `Authorization: Bearer dkp_pat_…`, or the `__Host-dkp_session` cookie. Cookies are ignored entirely when `Authorization` is present | [auth-and-scopes](../api/auth-and-scopes.md) |
| Idempotency | `Idempotency-Key` required on every POST that creates domain state. Unique on `(principal_id, key)` where the principal is the **service account or user, never the token** | [idempotency-and-concurrency](../api/idempotency-and-concurrency.md) |
| Concurrency | `ETag` on mutable resources; `If-Match` required on every state transition and on `PATCH` of raids, ticks, pools and articles. `412` returns the current representation in `meta.current` | [idempotency-and-concurrency](../api/idempotency-and-concurrency.md) |
| Pagination | Cursor only, in the body envelope `{items, next_cursor, has_more}`. `limit` default 50, max 200. No offset, ever | [pagination-and-sync](../api/pagination-and-sync.md) |
| Incremental sync | `?since_seq=` on `/ledger/*`, `/audit` and `/events/replay` **and nowhere else** | [pagination-and-sync](../api/pagination-and-sync.md) |
| Errors | RFC 9457 `application/problem+json`, closed `code` enum, `type` URL that resolves. Never 200 with an error body | [errors](../api/errors.md) |
| Realtime | SSE first, webhooks second, polling third; all three read the same outbox rows | [realtime](../api/realtime.md) · [webhooks](../api/webhooks.md) |

**Filters are explicit, enumerated query parameters**, never a generic DSL — a DSL cannot be
expressed in OpenAPI, so every generated SDK would degrade to `filter: string`. Operator suffixes are
`__ne`, `__gte`, `__lte`, `__in`, `__contains`, `__isnull`, each its own typed optional parameter.
Sorting is `sort=-effective_at,id`, whitelisted per endpoint, with a ULID tiebreak appended
server-side. Expansion is `expand=person,item,raid` with a per-endpoint whitelist and depth 1.
**Sparse fieldsets are deliberately absent**: they break generated response types and defeat the
response-validation middleware, and the real need behind them is served by `/table-views` and
`?columns=`.

**Rate limits** are in-process token buckets keyed by token, session or IP. Both header families are
emitted on **every** response, not just `429`s: the IETF `RateLimit`/`RateLimit-Policy` fields and
the de-facto `X-RateLimit-*` triplet, because every existing bot library already parses the latter.
No token is ever exempt — an `admin`-heavy role gets a higher ceiling, not immunity, because a
runaway admin script is exactly the failure that takes down a Raspberry Pi.

---

## 6. Batch endpoints

Three principles: batch where the **domain** is transactional, not where it is merely convenient; one
key, one transaction, one ledger batch where that is semantically right; partial success is never
silent.

| Endpoint | Batch semantics |
|---|---|
| `POST /raid-submissions` | A whole raid night, atomically (§7) |
| `POST /raids/{id}/ticks` `{"ticks":[…]}` | Always an array, even for one tick. A raid night is 12–20 ticks; 20 round trips from a home connection is the wrong shape and 20 idempotency keys is 20 chances to get it wrong. One ledger batch **per tick**, so a single tick stays independently reversible |
| `POST /adjustments` `{"adjustments":[…]}` | One `ledger_batch` for the whole group. Reversing the batch reverses the mass adjustment as a unit |
| `POST /awards` `{"awards":[…]}` | Multi-buyer splits and end-of-night dumps. One batch per award, one transaction, one key. Zero-sum splits are allocated inside the transaction by largest remainder so `Σ credits == debit` exactly |
| `POST /characters/resolve` · `POST /items/resolve` | Read-only, up to 500 names, no idempotency key. What a bot calls *before* submitting, to render "3 names I don't recognise" in Discord |
| `PUT /pools/{id}/event-types` · `PUT /raid-groups/{id}/members` | Whole-collection replacement with `If-Match`. Set semantics, not append |
| `POST /webhooks/{id}/deliveries/redeliver` `{"delivery_ids":[…]}` | Bulk dead-letter replay |

**Explicitly not offered: a generic `POST /batch` envelope of arbitrary sub-requests.** It defeats
OpenAPI typing, per-operation scopes, per-operation rate limits and the response-validation
middleware, and it produces a partial-success mess. Where a real transaction is needed there is a
named resource for it.

---

## 7. `POST /raid-submissions`

`raid_submission` is a **real table**, not a projection over `parse_run`. A durable, ETagged,
re-fetchable preview carrying per-account deltas, an unresolved queue, an `on_unresolved` policy and
a commit receipt is state; a projection cannot hold any of it.

```
POST /raid-submissions              → 201  state "preview"    (writes nothing to the ledger)
GET  /raid-submissions/{id}         → the diff, the unresolved queue, per-account deltas
POST /raid-submissions/{id}/commit  → 200  state "committed"  (one transaction)
POST /raid-submissions/{id}/abandon → 200  state "abandoned"
```

`{"auto_commit": true}` collapses this to one call for a bot that already previewed. States are
`preview | committed | abandoned | expired | failed`, lowercase, identical in the DB `CHECK` and the
OpenAPI enum.

**Atomicity.** The commit runs in one SQLite write transaction covering the raid row, ticks,
attendance, kill credits, item resolution, awards, adjustments, every resulting `ledger_batch` and
`ledger_entry`, artifact links, audit rows and outbox events. Any invariant violation, unresolved
required entity or stale precondition writes nothing, and the response lists **every** failure at
once rather than the first.

**`on_unresolved` is explicit per submission** (canonical §12):

| Value | Behaviour |
|---|---|
| `fail` | Any unknown character or item aborts the commit. The default for `auto_commit` |
| `quarantine` | **Default.** Commits what resolves; unknown names go to `/reconciliation` and the affected ticks and awards are recorded with **no ledger effect** until resolved. Nothing is ever dropped |
| `create` | Auto-creates characters and items. Requires `roster.write` and `item.alias.manage`. An explicit officer choice, never a default |

**Dedupe.** Every tick carries the `content_sha256` of its source lines, and commit is idempotent on
`(raid_id, content_sha256)`. Two officers uploading overlapping dumps of the same night converge: the
second submission reports `deduplicated_ticks` and creates only the genuinely new ones. This is what
makes "just upload your log, we'll sort it out" safe, and it works even for a bot that ignores
`Idempotency-Key` entirely.

---

## 8. Realtime transport

**Ship all three — SSE, webhooks, polling — fed by one transactional outbox, and document SSE as the
default for Discord bots.** This inverts the usual advice. A volunteer-run Discord bot typically has
no public HTTPS endpoint, no domain and no certificate; it makes outbound connections and nothing
else. *(Assumption about the P99 bot population — verify with two pilot guilds before Phase 6.)*
Telling that author "expose an HTTPS endpoint and verify HMAC signatures" is a wall; telling them
"open one connection with your token" is a five-line change.

All three carry identical semantics because all three read the same `event_outbox` rows, written
inside the same transaction as the state change. No channel knows anything another does not.

**Frames carry ids, never documents.** Every SSE frame and every webhook body is
`{topic, event_type, event_seq, resource, occurred_at, server_time}`. The consumer refetches through
the same REST endpoint the SPA uses. Consequences: exactly one representation of a bid session exists
in the system; "the realtime payload drifted from the REST payload" cannot happen; the event layer
needs no schema versioning; and the bot's `GET` after an event is served from a warm cache with an
`ETag`. The single exception is documented in [webhooks](../api/webhooks.md): two events carry an
advisory `snapshot` for display only.

Details — the ticket flow, `Last-Event-ID` replay, the resync-and-close backpressure protocol, the
signature scheme, the retry ladder and the full event catalogue — are in
[realtime](../api/realtime.md) and [webhooks](../api/webhooks.md).

---

## 9. OpenAPI-first workflow

**`openapi/openapi.json` is derived from the Go handler types by Huma v2 and committed. Code is never
generated from the spec.** Clients, docs and the error and event registries are generated *from* it.

The property people want from "spec-first" is *the spec cannot lie*. Spec-first codegen does not give
you that: it gives you generated **interfaces** that a hand-written handler may implement incorrectly
or that a middleware may bypass, and the drift surfaces as a 500 in someone's bot three weeks later.
Huma derives the schema from the exact Go struct the handler is compiled against, so the spec is not
a description of the implementation — it is a projection of it.

**Two deliberate spec-first islands**, because these are data catalogues rather than code:

```
openapi/registry/errors.yaml → internal/api/errdefs/*.gen.go   (Go constants + docs URLs)
                             → the `code` enum in openapi.json
                             → docs/reference/errors/<code>.md  (one page per code)
                             → discriminated error types in both SDKs

openapi/registry/events.yaml → internal/events/types.gen.go
                             → the OpenAPI 3.1 `webhooks` section
                             → docs/api/webhooks.md event catalogue
                             → the SSE event-name enum
```

```
Go handler types ──huma──► openapi/openapi.json  (committed)
                              ├─ openapi-typescript ──► web/src/api/schema.d.ts   (the SPA)
                              ├─ openapi-typescript ──► clients/ts     → @dragonkillparty/sdk
                              ├─ openapi-python-client► clients/python → dkp-client
                              ├─ Scalar (embedded) ───► /docs, served offline by the binary
                              └─ oasdiff ────────────► breaking-change gate vs main
```

`make gen` runs Atlas diff → sqlc → `dkp openapi > openapi/openapi.json` → registry codegen → both
SDK generators. Every output is committed. Hand-editing a generated file is blocked by a DO-NOT-EDIT
header and the regen diff; adding an error code without a docs page fails the build.

**`operationId` is explicit on every operation and is never renamed** (canonical §7). Generated SDK
method names derive from it, so a rename is a breaking change even when the HTTP surface is
identical. Letting Huma auto-derive means renaming a Go function silently renames a public SDK method
for every bot author.

**Contract gates** — these guard the spec itself:

| Gate | Catches |
|---|---|
| **Spec regen diff.** CI runs `make gen` and fails on any diff against the committed `openapi.json` | A handler change without a spec update. Unmergeable |
| **`oasdiff` breaking-change gate** vs `main`. A breaking delta needs a `!breaking-api` label *and* a `docs/api-changelog.md` entry, and that file is CODEOWNERS-protected — which is what converts an author-settable label into a review control | Removing a field, tightening validation, changing a status code |
| **`operationId` stability test** over a committed `openapi/operation-ids.txt`, plus assertions that every id is non-empty, unique and `lowerCamelCase` | Silently renaming a public SDK method |
| **Response-validation middleware** active for the entire integration suite whenever `DKP_ENV != production` | Handler drift, on the first test rather than the last *(the validator library choice is unverified — re-verify 3.1 support in Phase 0)* |
| **SDK regen diff.** Both generated clients are committed; CI regenerates and fails on a diff | A spec change that produces an unusable or renamed client surface |
| **Executable snippets.** Every file in `openapi/snippets/` runs against a live test server, and every `example` in the spec is validated against its own schema | Documentation that has rotted — the failure users notice first |
| **Architectural tests over the Huma registry.** Every route declares `Security`, `x-dkp-permission` and non-empty `x-dkp-scopes`; every mutating POST under the required prefixes declares `Idempotency-Key`; every transition declares `If-Match`; every collection uses the shared envelope; every error is `problem+json` with a registry `code`; `?since_seq=` appears only on the three permitted prefixes | Adding an unauthenticated, non-idempotent or bespoke-shaped route. Unmergeable |

---

## 10. The five gates that make the SPA a first-class API client

"The SPA is just another API client" is the claim on which the whole extension story rests. These
five gates are what make it true rather than aspirational. Each names its mechanism; a rule without a
mechanism is a wish.

| # | Gate | Mechanism | What it makes impossible |
|---|---|---|---|
| 1 | **Client purity** | `lint/web-client-purity` — an ESLint rule banning `fetch`, `XMLHttpRequest` and `EventSource` anywhere in `web/src` outside `web/src/api` | A component reaching the server through a path the generated client does not model |
| 2 | **Route locality** | `TestRoutes_DeclaredOnlyInAPI` — a Go test asserting no HTTP route is registered outside `internal/api` | A server-rendered or template back door that bypasses the spec |
| 3 | **Traffic conformance** | The Playwright run proxies the browser through a recorder; a post-test assertion requires every observed `(method, path-template)` to exist in `openapi.json` | A UI feature that quietly needed a bespoke, undocumented endpoint |
| 4 | **PAT parity** | Recorded journeys in `test/parity/<journey>.jsonl` are replayed with a scoped **PAT** instead of a session cookie and asserted byte-identical, modulo a volatile-field allowlist (ids, timestamps, `server_time`, ETags). The allowlist is one CODEOWNERS-protected file and a ratchet test asserts its length is non-increasing — that normaliser is exactly where a diff would be hidden | Any capability the browser has that a bot does not |
| 5 | **No hidden operations** | An architectural test asserting `Hidden: true` appears only on `/healthz`, `/readyz`, `/metrics`, the OAuth callback and the compat shim (canonical §7) | Serving the SPA from an operation absent from the published spec |

Gate 4 is the strongest, because it proves capability parity rather than spec presence. Gates 1 and 2
are cheap and belong at zero call sites: they ship in Phase 0, before there is anything to
grandfather.

An **operation-coverage report** rides on every PR (operations exercised by the e2e and parity suites
÷ total operations). It will never be 100%. A *drop* is a review signal.

---

## 11. Worked example 1 — a log-parser bot posts a raid night

A Python tailer on an officer's Windows PC, holding a token on the `parser-bot` service account with
scopes `logs:ingest raids:write loot:award dkp:adjust`. Two `RaidRoster` dumps, one kill, two loot
lines, one adjustment.

**Step 1 — upload the artifacts.** Shown for one of two. Artifacts are content-addressed, so
`POST /artifacts` carries **no** `Idempotency-Key`: identical bytes return the existing row.

```http
POST /api/v1/artifacts HTTP/1.1
Authorization: Bearer dkp_pat_a91f3c2b_kXn2…
Content-Type: application/json

{ "kind": "p99_raid_dump",
  "filename": "RaidRoster-20260802-021500.txt",
  "content_base64": "MQlUYW5rZ3V5CTYwCVdhcmxvcmQKMQlIZWFsYm90…",
  "captured_by_character": "Tankguy",
  "captured_at": "2026-08-02T02:15:00.000000Z",
  "source_timezone": "America/Chicago" }
```

```http
HTTP/1.1 201 Created
Location: /api/v1/artifacts/01JZ0ARTFCT000000000000001
ETag: "01JZ0ARTFCT000000000000001:1"
RateLimit: "ingest";r=19;t=52
X-Request-Id: 01JZ0REQ7X9A2C000000000001

{ "id": "01JZ0ARTFCT000000000000001",
  "sha256": "9f2c4a7e5b1d8036c2f4a6e8b0d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8",
  "size_bytes": 2841,
  "kind": "p99_raid_dump",
  "detected_format": "p99_raid_dump_v1",
  "line_count": 58,
  "parsed_preview_available": true,
  "retention_expires_at": "2027-01-29T04:02:11.331204Z",
  "created_at": "2026-08-02T04:02:11.331204Z" }
```

**Step 2 — submit the raid as a preview.** Note that **every tick carries its own `pool_id`**: the
twelve attendance ticks feed `Velious Main`, and the tracking tick feeds `Cross-class Rares`. A raid
that feeds two pools is expressible, and each pool keeps its own `seq`.

```http
POST /api/v1/raid-submissions HTTP/1.1
Authorization: Bearer dkp_pat_a91f3c2b_kXn2…
Idempotency-Key: 01JZ0DKEY00000000000000001
Content-Type: application/json

{
  "server_id": "01JZ0SRVRBWE00000000000001",
  "default_pool_id": "01JZ0PNMAN0000000000000001",
  "raid": {
    "title": "ToV — 2026-08-01",
    "started_at": "2026-08-02T01:45:00.000000Z",
    "ended_at":   "2026-08-02T05:30:00.000000Z",
    "officer_character_name": "Tankguy",
    "note": "NToV clear; lost Aary FTE to Kingdom"
  },
  "artifact_ids": ["01JZ0ARTFCT000000000000001", "01JZ0ARTFCT000000000000002"],
  "ticks": [
    { "at": "2026-08-02T02:15:00.000000Z",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "kind": "time",
      "value_centipoints": 10000,
      "artifact_id": "01JZ0ARTFCT000000000000001",
      "content_sha256": "9f2c4a7e5b1d8036c2f4a6e8b0d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8",
      "attendees": [
        {"character_name": "Tankguy",     "level": 60, "class_title": "Warlord",     "group": 1, "status": "present"},
        {"character_name": "Healbot",     "level": 60, "class_title": "High Priest", "group": 1, "status": "present"},
        {"character_name": "Benchwarmer", "level": 55, "class_title": "Preserver",   "group": 0, "status": "bench", "weight_bp": 5000},
        {"character_name": "Frostbyte",   "level": 60, "class_title": "Grandmaster", "group": 3, "status": "present"}
      ] },
    { "at": "2026-08-02T02:30:00.000000Z",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "kind": "time",
      "value_centipoints": 10000,
      "artifact_id": "01JZ0ARTFCT000000000000002",
      "content_sha256": "3b81ee0f5c2a49d7e1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7",
      "attendees": ["…44 more…"] },
    { "at": "2026-08-02T04:45:00.000000Z",
      "pool_id": "01JZ0PNRARES00000000000001",
      "kind": "tracking",
      "value_centipoints": 5000,
      "event_type_id": "01JZ0EVTTRACK0000000000001",
      "content_sha256": "5c93aa2b7d4e61f8a0c2e4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6",
      "attendees": [
        {"character_name": "Sneakyguy", "status": "present"},
        {"character_name": "…3 more…",  "status": "present"}
      ] }
  ],
  "kills": [
    { "event_type_id": "01JZ0EVTVKAERR000000000001",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "at": "2026-08-02T04:12:33.000000Z",
      "bonus_centipoints": 25000,
      "source_line": "Vulak`Aerr has been slain by Tankguy!" }
  ],
  "awards": [
    { "item_name": "Shroud of Nature", "character_name": "Tankguy",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "price_centipoints": 35000, "award_type": "dkp_auction",
      "at": "2026-08-02T04:19:02.000000Z",
      "source_line": "--Tankguy has looted a Shroud of Nature.--" },
    { "item_name": "Whitened Treant Fists", "character_name": "Sneakyguy",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "price_centipoints": 0, "award_type": "rot",
      "at": "2026-08-02T04:21:44.000000Z" }
  ],
  "adjustments": [
    { "character_name": "Healbot",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "value_centipoints": 5000,
      "reason": "Raid lead — pull duty",
      "at": "2026-08-02T05:30:00.000000Z" }
  ],
  "on_unresolved": "quarantine"
}
```

```http
HTTP/1.1 201 Created
Location: /api/v1/raid-submissions/01JZ0SBMRADNGHT00000000001
ETag: "01JZ0SBMRADNGHT00000000001:1"

{
  "id": "01JZ0SBMRADNGHT00000000001",
  "state": "preview",
  "on_unresolved": "quarantine",
  "expires_at": "2026-08-03T04:02:14.902117Z",
  "server_time": "2026-08-02T04:02:14.902117Z",
  "summary": {
    "ticks_new": 13,
    "ticks_deduplicated": 0,
    "attendees_distinct": 46,
    "kills": 1,
    "awards": 2,
    "adjustments": 1,
    "ledger_batches_planned": 17,
    "quarantined": {"characters": 1, "items": 0}
  },
  "pools": [
    { "pool_id": "01JZ0PNMAN0000000000000001", "name": "Velious Main",
      "batches_planned": 16,
      "earn_centipoints": {"ticks": 5320000, "kill_bonus": 1100000, "adjustments": 5000, "total": 6425000},
      "spend_centipoints": {"awards": 35000, "total": 35000},
      "head_seq": 88212 },
    { "pool_id": "01JZ0PNRARES00000000000001", "name": "Cross-class Rares",
      "batches_planned": 1,
      "earn_centipoints": {"ticks": 20000, "kill_bonus": 0, "adjustments": 0, "total": 20000},
      "spend_centipoints": {"awards": 0, "total": 0},
      "head_seq": 411 }
  ],
  "unresolved": [
    { "type": "character",
      "value": "Frostbyte",
      "occurrences": 12,
      "first_seen_tick": 0,
      "suggestions": [],
      "action_if_committed": "quarantine",
      "reconciliation_item_id": "01JZ0RECFRSTBY000000000001",
      "resolve_url": "/api/v1/reconciliation/01JZ0RECFRSTBY000000000001/resolve" }
  ],
  "warnings": [
    { "code": "tick_gap",
      "detail": "18m gap between tick 6 (03:15Z) and tick 7 (03:33Z); expected 15m cadence.",
      "meta": {"expected_seconds": 900, "actual_seconds": 1080} },
    { "code": "bench_weight_applied",
      "detail": "1 attendee recorded at 50% weight (group 0).",
      "meta": {"weight_bp": 5000, "count": 1} }
  ],
  "ledger_preview": [
    { "account_id": "01JZ0ACCTTANKGY00000000001", "person_id": "01JZ0PERSTANKGY00000000001",
      "display_name": "Tankguy", "pool_id": "01JZ0PNMAN0000000000000001",
      "earn_centipoints": 145000, "spend_centipoints": 35000, "adjust_centipoints": 0,
      "net_centipoints": 110000,
      "balance_before_centipoints": 412500, "balance_after_centipoints": 522500 },
    { "account_id": "01JZ0ACCTHEABT000000000001", "person_id": "01JZ0PERSHEABT000000000001",
      "display_name": "Healbot", "pool_id": "01JZ0PNMAN0000000000000001",
      "earn_centipoints": 145000, "spend_centipoints": 0, "adjust_centipoints": 5000,
      "net_centipoints": 150000,
      "balance_before_centipoints": 88000, "balance_after_centipoints": 238000 },
    { "account_id": "01JZ0ACCTSNEAKY00000000001", "person_id": "01JZ0PERSSNEAKY00000000001",
      "display_name": "Sneakyguy", "pool_id": "01JZ0PNMAN0000000000000001",
      "earn_centipoints": 125000, "spend_centipoints": 0, "adjust_centipoints": 0,
      "net_centipoints": 125000,
      "balance_before_centipoints": 301000, "balance_after_centipoints": 426000 },
    { "account_id": "01JZ0ACCTBENCH000000000001", "person_id": "01JZ0PERSBENCH000000000001",
      "display_name": "Benchwarmer", "pool_id": "01JZ0PNMAN0000000000000001",
      "earn_centipoints": 60000, "spend_centipoints": 0, "adjust_centipoints": 0,
      "net_centipoints": 60000,
      "balance_before_centipoints": 45000, "balance_after_centipoints": 105000 }
  ],
  "_links": {
    "commit":  "/api/v1/raid-submissions/01JZ0SBMRADNGHT00000000001/commit",
    "abandon": "/api/v1/raid-submissions/01JZ0SBMRADNGHT00000000001/abandon"
  }
}
```

The preview's `Velious Main` arithmetic is exact and checkable: 43 attendees present for all twelve
ticks (43 × 120000), Sneakyguy present for ten (100000), Benchwarmer benched at 5000 bp for twelve
(60000) = **5 320 000**; the kill bonus reaches the 44 non-benched attendees present at the kill
(44 × 25000) = **1 100 000**; one adjustment of **5 000**. Frostbyte is quarantined, so his twelve
ticks contribute nothing yet.

**Step 3 — resolve the unknown name.** Frostbyte is a new recruit whose character was never created.
`create` is the officer's explicit choice; the default would have left the credit quarantined
indefinitely rather than guessing (canonical §12).

```http
POST /api/v1/reconciliation/01JZ0RECFRSTBY000000000001/resolve HTTP/1.1
Idempotency-Key: 01JZ0DKEY00000000000000002
Content-Type: application/json

{ "status": "created",
  "create": { "character_name": "Frostbyte",
              "server_id": "01JZ0SRVRBWE00000000000001",
              "level": 60,
              "class_title": "Grandmaster",
              "person": {"display_name": "Frostbyte"} },
  "reason": "New recruit, first raid; confirmed by Bob in officer chat." }
```

```http
HTTP/1.1 200 OK

{ "id": "01JZ0RECFRSTBY000000000001",
  "status": "created",
  "created": { "person_id": "01JZ0PERSFRSTBY00000000001",
               "account_id": "01JZ0ACCTFRSTBY00000000001",
               "character_id": "01JZ0CHARFRSTBY00000000001" },
  "affected_submissions": ["01JZ0SBMRADNGHT00000000001"],
  "resolved_at": "2026-08-02T04:05:02.441907Z" }
```

The submission is re-planned, so its ETag advances to `:2`. A commit sent with the stale `:1` would
return `412` with the current representation in `meta.current`.

**Step 4 — commit.** `on_unresolved` is tightened to `fail`: nothing should still be unresolved, and
the officer wants to be told rather than have anything quietly quarantined.

```http
POST /api/v1/raid-submissions/01JZ0SBMRADNGHT00000000001/commit HTTP/1.1
Idempotency-Key: 01JZ0DKEY00000000000000003
If-Match: "01JZ0SBMRADNGHT00000000001:2"
Content-Type: application/json

{ "on_unresolved": "fail" }
```

```http
HTTP/1.1 200 OK
ETag: "01JZ0SBMRADNGHT00000000001:3"

{ "id": "01JZ0SBMRADNGHT00000000001",
  "state": "committed",
  "raid_id": "01JZ0RDTHENGHT000000000001",
  "committed_at": "2026-08-02T04:05:51.117802Z",
  "created":      {"ticks": 13, "kills": 1, "awards": 2, "adjustments": 1, "characters": 0},
  "deduplicated": {"ticks": 0},
  "quarantined":  {"characters": 0, "items": 0},
  "ledger_batches": [
    { "pool_id": "01JZ0PNMAN0000000000000001", "count": 16,
      "first_seq": 88213, "last_seq": 88228,
      "earn_centipoints": 6570000, "spend_centipoints": 35000 },
    { "pool_id": "01JZ0PNRARES00000000000001", "count": 1,
      "first_seq": 412, "last_seq": 412,
      "earn_centipoints": 20000, "spend_centipoints": 0 }
  ],
  "audit_entry_id": "01JZ0ADTCMMT00000000000001",
  "server_time": "2026-08-02T04:05:51.121440Z" }
```

`first_seq` and `last_seq` are reported **per pool**, because `seq` is per-pool. A single pair across
pools would be meaningless. With Frostbyte resolved the twelve ticks now credit 44 full attendees
(5 440 000) and the kill bonus reaches 45 (1 125 000), so `Velious Main` earns 6 570 000.

Retrying that exact commit — same key, same body — returns the identical response with
`Idempotency-Replayed: true`. A second, independent upload of the same two dumps produces a *new*
submission whose preview reads `ticks_new: 0, ticks_deduplicated: 12`; nothing double-credits, and
that holds even for a bot that never sends a key, because the dedupe is a real uniqueness constraint
on `(raid_id, content_sha256)`.

**Events emitted:** `raid_submission.committed` on `guild`; `raid.created`; 13 × `raid.tick.created`;
`raid.kill.recorded`; 2 × `award.created`; `adjustment.created`; 17 × `ledger.batch.committed` — each
as an id-only frame on SSE and, for subscribed endpoints, a signed webhook delivery.

---

## 12. Worked example 2 — a Discord bot runs a timed auction

The bot holds a token on the `bid-bot` service account with scopes `bids:read bids:manage loot:award
events:subscribe`. It is the **actor**; the member is the **subject**, named by `character_id` in the
body. Every bid row records `actor_token_id` and `source: "discord"`, so the audit trail already
answers "who typed this". Member-authenticated delegation (`X-DKP-On-Behalf-Of`) is a 1.2 item and is
**not** part of the 1.0 contract.

**Open.**

```http
POST /api/v1/bid-sessions HTTP/1.1
Authorization: Bearer dkp_pat_7f3c19bd_pLm8…
Idempotency-Key: 01JZ0DKEY00000000000000004
Content-Type: application/json

{ "pool_id": "01JZ0PNMAN0000000000000001",
  "raid_id": "01JZ0RDTHENGHT000000000001",
  "item_id": "01JZ0TMCARNAGE000000000001",
  "item_instance_ref": "vulak-2026-08-02-drop-1",
  "mode": "auction_sealed_second",
  "min_bid_centipoints": 10000,
  "increment_centipoints": 500,
  "duration_seconds": 120,
  "anti_snipe_window_seconds": 20,
  "anti_snipe_extend_seconds": 20,
  "max_extensions": 3,
  "hold_policy": "strict",
  "auto_open": true }
```

```http
HTTP/1.1 201 Created
Location: /api/v1/bid-sessions/01JZ0BSESSCARN000000000001
ETag: "01JZ0BSESSCARN000000000001:2"

{ "id": "01JZ0BSESSCARN000000000001",
  "state": "open",
  "version": 2,
  "pool_id": "01JZ0PNMAN0000000000000001",
  "item": {"id": "01JZ0TMCARNAGE000000000001", "name": "Blade of Carnage"},
  "mode": "auction_sealed_second",
  "min_bid_centipoints": 10000,
  "increment_centipoints": 500,
  "opens_at":  "2026-08-02T04:19:00.104000Z",
  "closes_at": "2026-08-02T04:21:00.104000Z",
  "extensions_used": 0,
  "max_extensions": 3,
  "bid_count": 0,
  "pool_seq_at_open": 88228,
  "server_time": "2026-08-02T04:19:00.109337Z" }
```

Every board receives an id-only frame. Note the two sequences side by side: `event_seq` is the global
outbox position, `pool_seq_at_open` is this pool's ledger position. They are different numbers with
different meanings and they never share a field name.

```
id: 8821340
event: bid_session.opened
data: {"topic":"bid:01JZ0BSESSCARN000000000001","event_type":"bid_session.opened",
data: "event_seq":8821340,"resource":"/api/v1/bid-sessions/01JZ0BSESSCARN000000000001",
data: "occurred_at":"2026-08-02T04:19:00.110218Z","server_time":"2026-08-02T04:19:00.110904Z"}
```

**A member bids through the bot.**

```http
POST /api/v1/bid-sessions/01JZ0BSESSCARN000000000001/bids HTTP/1.1
Authorization: Bearer dkp_pat_7f3c19bd_pLm8…
Idempotency-Key: 01JZ0DKEY00000000000000005
Content-Type: application/json

{ "character_id": "01JZ0CHARTANKGY00000000001",
  "amount_centipoints": 35000,
  "expected_state": "open",
  "source": "discord",
  "source_ref": "discord-message:1301827364509827364" }
```

```http
HTTP/1.1 201 Created
RateLimit: "bids";r=298;t=57

{ "id": "01JZ0BDTANKGY0000000000001",
  "session_id": "01JZ0BSESSCARN000000000001",
  "account_id": "01JZ0ACCTTANKGY00000000001",
  "character_id": "01JZ0CHARTANKGY00000000001",
  "display_name": "Tankguy",
  "amount_centipoints": 35000,
  "amount_display": "350.00",
  "bid_seq": 4,
  "accepted_at": "2026-08-02T04:19:41.882401Z",
  "sealed": true,
  "hold": { "id": "01JZ0HDTANKGY0000000000001",
            "amount_centipoints": 35000,
            "state": "active" },
  "session": { "state": "open",
               "closes_at": "2026-08-02T04:21:00.104000Z",
               "bid_count": 4,
               "leader_visible": false },
  "actor": { "token_prefix": "7f3c19bd",
             "service_account_id": "01JZ0SVCBDBT00000000000001",
             "source": "discord" },
  "server_time": "2026-08-02T04:19:41.889112Z" }
```

**A member who cannot afford it.** Benchwarmer holds 105 000 but 81 200 is already held against
another live session, so 40 000 exceeds what he can spend. The problem document says so numerically
rather than in prose, and every money field is an unquoted integer.

```http
HTTP/1.1 409 Conflict
Content-Type: application/problem+json
X-Request-Id: 01JZ0REQ4M6P8R000000000001

{ "type": "https://docs.dragonkillparty.org/errors/insufficient_balance",
  "title": "Insufficient balance",
  "status": 409,
  "code": "insufficient_balance",
  "detail": "Benchwarmer can spend 23800 centipoints; this bid requires 40000.",
  "instance": "/api/v1/bid-sessions/01JZ0BSESSCARN000000000001/bids",
  "request_id": "01JZ0REQ4M6P8R000000000001",
  "meta": { "account_id": "01JZ0ACCTBENCH000000000001",
            "pool_id": "01JZ0PNMAN0000000000000001",
            "balance_centipoints": 105000,
            "active_holds_centipoints": 81200,
            "spendable_centipoints": 23800,
            "required_centipoints": 40000,
            "as_of_seq": 88228 },
  "errors": [] }
```

**Anti-snipe.** A bid accepted at `04:20:47.6Z` lands inside the 20-second window, so the session
moves `open → extended`, `closes_at` becomes `04:21:20.104000Z`, `extensions_used` becomes 1, and
`bid_session.extended` fires. Every board and the Discord embed update from the same frame, and both
render `closes_at − server_time + local_elapsed`, so the countdowns agree.

**Close, resolve, settle.** Transitions require `If-Match`; only `settle` also requires an
`Idempotency-Key`, because only `settle` creates domain state.

```http
POST /api/v1/bid-sessions/01JZ0BSESSCARN000000000001/close HTTP/1.1
If-Match: "01JZ0BSESSCARN000000000001:6"
```

```http
HTTP/1.1 200 OK
ETag: "01JZ0BSESSCARN000000000001:7"

{ "id": "01JZ0BSESSCARN000000000001", "state": "closing", "version": 7,
  "closed_at": "2026-08-02T04:21:20.311882Z",
  "bids_revealed": true, "bid_count": 7,
  "server_time": "2026-08-02T04:21:20.315004Z" }
```

```http
POST /api/v1/bid-sessions/01JZ0BSESSCARN000000000001/resolve HTTP/1.1
If-Match: "01JZ0BSESSCARN000000000001:7"
```

```http
HTTP/1.1 200 OK
ETag: "01JZ0BSESSCARN000000000001:8"

{ "id": "01JZ0BSESSCARN000000000001", "state": "resolved", "version": 8,
  "resolution": {
    "winner": { "account_id": "01JZ0ACCTTANKGY00000000001",
                "person_id": "01JZ0PERSTANKGY00000000001",
                "character_id": "01JZ0CHARTANKGY00000000001",
                "display_name": "Tankguy" },
    "winning_bid_centipoints": 35000,
    "price_centipoints": 28500,
    "price_display": "285.00",
    "pay_rule": "second_price",
    "second_highest_centipoints": 28000,
    "increment_centipoints": 500,
    "tie_break_reason": null,
    "tie_break_chain_evaluated": ["highest_bid"],
    "strategy_id": "auction_sealed",
    "strategy_version": "1.0.0" },
  "bids": [
    { "id": "01JZ0BDTANKGY0000000000001", "account_id": "01JZ0ACCTTANKGY00000000001",
      "display_name": "Tankguy",   "amount_centipoints": 35000, "bid_seq": 4, "retracted": false },
    { "id": "01JZ0BDSNEAKY0000000000001", "account_id": "01JZ0ACCTSNEAKY00000000001",
      "display_name": "Sneakyguy", "amount_centipoints": 28000, "bid_seq": 6, "retracted": false }
  ],
  "holds": {"released": 6, "retained": 1},
  "ledger_effect": "none_yet",
  "server_time": "2026-08-02T04:21:22.004771Z" }
```

**Nothing has touched the ledger yet.** An officer may `POST /override` here, with a mandatory
reason, before any points move. That separation is the whole point of splitting `resolve` from
`settle`.

```http
POST /api/v1/bid-sessions/01JZ0BSESSCARN000000000001/settle HTTP/1.1
Idempotency-Key: 01JZ0DKEY00000000000000006
If-Match: "01JZ0BSESSCARN000000000001:8"
```

```http
HTTP/1.1 200 OK
ETag: "01JZ0BSESSCARN000000000001:9"
X-DKP-Event-Sequence: 8821352

{ "id": "01JZ0BSESSCARN000000000001", "state": "settled", "version": 9,
  "settled_at": "2026-08-02T04:21:24.771003Z",
  "award": { "id": "01JZ0AWRDCARN0000000000001",
             "item": {"id": "01JZ0TMCARNAGE000000000001", "name": "Blade of Carnage"},
             "account_id": "01JZ0ACCTTANKGY00000000001",
             "character_id": "01JZ0CHARTANKGY00000000001",
             "price_centipoints": 28500,
             "award_type": "dkp_auction",
             "raid_id": "01JZ0RDTHENGHT000000000001",
             "state": "active" },
  "ledger_batch": {
    "id": "01JZ0BATCHSETT000000000001",
    "seq": 88229,
    "pool_id": "01JZ0PNMAN0000000000000001",
    "kind": "award",
    "strategy_id": "auction_sealed",
    "strategy_version": "1.0.0",
    "source": "discord",
    "source_ref": "bid_session:01JZ0BSESSCARN000000000001",
    "actor_token_id": "01JZ0TKNBDBT00000000000001",
    "actor_user_id": null,
    "effective_at": "2026-08-02T04:21:24.771003Z",
    "recorded_at":  "2026-08-02T04:21:24.771003Z",
    "entry_count": 1,
    "net_amount_centipoints": -28500,
    "entries": [
      { "id": "01JZ0ENTRYSETT000000000001",
        "account_id": "01JZ0ACCTTANKGY00000000001",
        "balance_kind": "dkp",
        "amount_centipoints": -28500,
        "character_id": "01JZ0CHARTANKGY00000000001",
        "item_id": "01JZ0TMCARNAGE000000000001" }
    ],
    "invariants_checked": ["NoFloat", "NonNegative(dkp,0)", "Conserved"],
    "prev_hash": "7ad3b1c5e9f2a4d6b8c0e2f4a6d8b0c2e4f6a8d0b2c4e6f8a0d2b4c6e8f0a2d4",
    "hash":      "c81f4e2a6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8f0a" },
  "balances_after": [
    { "account_id": "01JZ0ACCTTANKGY00000000001",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "balance_centipoints": 494000,
      "as_of_seq": 88229 }
  ],
  "holds": {"converted": 1, "released": 0},
  "server_time": "2026-08-02T04:21:24.779446Z" }
```

Tankguy's balance is 522 500 after the raid commit and 494 000 after paying 28 500, as of `seq`
88229 — the batch immediately after the raid night's 88213–88228.

Retrying `settle` with the same key returns the identical body plus `Idempotency-Replayed: true`. A
second settle *without* the key returns `409 session_already_settled` carrying `meta.batch_id`. If
the winner's spendable balance had moved between `resolve` and `settle`, the transaction rolls back
to state `resolution_failed` with `meta.reason` — never a silent award at a stale price.

**Event sequence:** `bid_session.opened` → 7 × `bid_session.bid_placed` → `bid_session.extended` →
`bid_session.closing` → `bid_session.resolved` → `bid_session.settled` → `award.created` →
`ledger.batch.committed`. Members who opted in to the `outbid` notification type also receive
`bid_session.outbid`.

---

## 13. Worked example 3 — an officer corrects a mistaken award

The Blade went to the wrong person: the bid came from Tankguy's boxed second account, which the
guild's rules disqualify. The officer is signed in to the SPA, holds `ledger.reverse`, and **deletes
nothing**.

**Step 1 — find the batch.**

```http
GET /api/v1/awards/01JZ0AWRDCARN0000000000001?expand=ledger_batch,account,item HTTP/1.1
Cookie: __Host-dkp_session=…
```

```http
HTTP/1.1 200 OK
ETag: "01JZ0AWRDCARN0000000000001:1"

{ "id": "01JZ0AWRDCARN0000000000001",
  "state": "active",
  "price_centipoints": 28500,
  "award_type": "dkp_auction",
  "account": {"id": "01JZ0ACCTTANKGY00000000001", "display_name": "Tankguy", "kind": "person"},
  "item":    {"id": "01JZ0TMCARNAGE000000000001", "name": "Blade of Carnage"},
  "ledger_batch": { "id": "01JZ0BATCHSETT000000000001", "seq": 88229, "kind": "award",
                    "pool_id": "01JZ0PNMAN0000000000000001",
                    "reversed_by_batch_id": null },
  "reversible": true }
```

**Step 2 — reverse it.** A reversal is a new batch with `reverses_batch_id` set and the entries
negated. The original is never updated or deleted: a `BEFORE UPDATE OR DELETE … RAISE(ABORT)` trigger
refuses, and `TestLedger_UpdateEntry_TriggerAborts` asserts the trigger fires, so the guardrail
cannot be silently regressed.

```http
POST /api/v1/awards/01JZ0AWRDCARN0000000000001/reverse HTTP/1.1
Cookie: __Host-dkp_session=…
Idempotency-Key: 01JZ0DKEY00000000000000007
If-Match: "01JZ0AWRDCARN0000000000001:1"
Content-Type: application/json

{ "reason": "Bid was placed from Tankguy's box account, which guild rules disqualify. Item goes to the runner-up at his own bid. Officer: Bob." }
```

```http
HTTP/1.1 201 Created
Location: /api/v1/ledger/batches/01JZ0BATCHREV0000000000001
X-Request-Id: 01JZ0REQ2H5K7N000000000001

{ "reversal_batch": {
    "id": "01JZ0BATCHREV0000000000001",
    "seq": 88240,
    "pool_id": "01JZ0PNMAN0000000000000001",
    "kind": "reversal",
    "reverses_batch_id": "01JZ0BATCHSETT000000000001",
    "reason": "Bid was placed from Tankguy's box account, which guild rules disqualify. Item goes to the runner-up at his own bid. Officer: Bob.",
    "actor_user_id": "01JZ0SRBB00000000000000001",
    "actor_token_id": null,
    "source": "web",
    "effective_at": "2026-08-02T16:40:02.118773Z",
    "recorded_at":  "2026-08-02T16:40:02.118773Z",
    "entry_count": 1,
    "net_amount_centipoints": 28500,
    "entries": [
      { "account_id": "01JZ0ACCTTANKGY00000000001",
        "balance_kind": "dkp",
        "amount_centipoints": 28500,
        "item_id": "01JZ0TMCARNAGE000000000001",
        "metadata": {"reverses_entry_id": "01JZ0ENTRYSETT000000000001"} }
    ],
    "invariants_checked": ["NoFloat", "Conserved"],
    "prev_hash": "e91a…",
    "hash": "e2b7…" },
  "original_batch": { "id": "01JZ0BATCHSETT000000000001",
                      "seq": 88229,
                      "reversed_by_batch_id": "01JZ0BATCHREV0000000000001",
                      "still_visible": true },
  "award": {"id": "01JZ0AWRDCARN0000000000001", "state": "reversed"},
  "bid_session": {"id": "01JZ0BSESSCARN000000000001", "state": "reversed"},
  "balances_after": [
    { "account_id": "01JZ0ACCTTANKGY00000000001",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "balance_centipoints": 522500,
      "as_of_seq": 88240 }
  ],
  "audit_entry_id": "01JZ0ADTREV000000000000001" }
```

Tankguy returns to exactly the 522 500 he held after the raid night. The original batch still renders
in his statement, struck through and linked to the reversal.

**Step 3 — award correctly.** `effective_at` is backdated to raid night — **game truth** — while
`recorded_at` is today — **system truth**. That bitemporal split is what makes "what did standings
look like on the night?" answerable at all.

```http
POST /api/v1/awards HTTP/1.1
Cookie: __Host-dkp_session=…
Idempotency-Key: 01JZ0DKEY00000000000000008
Content-Type: application/json

{ "awards": [
    { "item_id": "01JZ0TMCARNAGE000000000001",
      "account_id": "01JZ0ACCTSNEAKY00000000001",
      "character_id": "01JZ0CHARSNEAKY00000000001",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "price_centipoints": 28000,
      "award_type": "dkp_auction",
      "raid_id": "01JZ0RDTHENGHT000000000001",
      "bid_session_id": "01JZ0BSESSCARN000000000001",
      "reason": "Runner-up award after reversal of batch 01JZ0BATCHREV0000000000001; pays his own bid per the disqualification rule.",
      "effective_at": "2026-08-02T04:21:24.771003Z" }
  ] }
```

```http
HTTP/1.1 201 Created

{ "created": [
    { "award": {"id": "01JZ0AWRDCARN0000000000002", "state": "active",
                "price_centipoints": 28000},
      "ledger_batch": { "id": "01JZ0BATCHCRR0000000000001",
                        "seq": 88241,
                        "pool_id": "01JZ0PNMAN0000000000000001",
                        "kind": "award",
                        "effective_at": "2026-08-02T04:21:24.771003Z",
                        "recorded_at":  "2026-08-02T16:40:19.882004Z",
                        "net_amount_centipoints": -28000 },
      "balance_after": { "account_id": "01JZ0ACCTSNEAKY00000000001",
                         "balance_centipoints": 398000,
                         "as_of_seq": 88241 } }
  ],
  "deduplicated": [] }
```

**The zero-sum wrinkle.** Had this pool run `zero_sum` instead of a straight auction, the original
batch would have held 47 entries — one −28 500 debit and 46 credits summing to exactly +28 500,
allocated by largest remainder with a deterministic tiebreak on `account_id`, and any unallocatable
remainder posted to the `residue` system account. The reversal negates all 47 and is validated
against the same `SumZero(dkp, scope=batch)` invariant, so a reversal cannot mint or destroy points
even by accident. Because later balances were computed against the original, **the API compensates at
today's `seq` and never replays history**: the reversal appears in each affected member's corrections
view (`GET /accounts/{account_id}/ledger?kind=reversal,correction`) with the originating batch
linked. A full forward replay exists only as an operator command with a mandatory dry-run diff, and
it emits a single net-delta `correction` batch rather than rewriting anything.

Because system accounts are addressable, a member can verify the whole thing independently: sum every
account's balance in the pool as of `seq` 88241, including `residue` and `guild_bank`, and compare it
to the pool's minted total. `GET /ledger/verify` reports the same check as run nightly.

**Events:** `award.reversed` on `raid:…` and `person:…`, `bid_session.reversed`,
2 × `ledger.batch.committed`, `award.created`.

---

## 14. Open items

| Item | Status |
|---|---|
| `X-DKP-On-Behalf-Of` member delegation | Deferred to 1.2. A three-way scope intersection, per-person grants, guild defaults and three error codes is a subsystem, not a header. In 1.0 the bot holds `bids:manage` and records `character_id` |
| OAuth 2.0 client-credentials grant | Cut. A second token type and a second `Principal` path to solve what PATs already solve |
| Anonymous SSE and the `/embed/standings.js` widget | Deferred to 1.1. The public standings *page* ships in Phase 3 |
| Scope arrays on non-`oauth2` security requirements in OpenAPI 3.1 | *Unverified* — the 3.1.0/3.1.1 prose is reported inconsistently. `x-dkp-scopes` on every operation is the machine-readable source of truth either way, and a Spectral lint step is the CI arbiter |
| Response-validation library | *Unverified* — re-verify OpenAPI 3.1 support in Phase 0 before the dependency is pinned |
| `?since_seq=` on `/events/replay` | Follows canonical §4, but the value is an `event_seq`. Every frame echoes `event_seq` so the two cannot be confused; an alias parameter would be an additive change if bot authors still trip on it |
