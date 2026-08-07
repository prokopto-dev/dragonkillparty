# Canonical conventions

**Status:** normative. **Audience:** contributor, agent.

This file is the tie-breaker. The design documents were written in parallel and disagreed with each
other in 35 places — money was specified two ways, permissions four ways, enums in two casings. Any
contradiction between another document and this one is a bug **in that document**. Fix the document,
not this file.

Where a decision below has an enforcement mechanism, the mechanism is authoritative and the prose is
a description of it.

---

## 1. Money

| Rule | Value |
|---|---|
| Storage | `INTEGER` centipoints (points × 100). Column suffix `_cp`. |
| Go type | `Centipoints int64` in `internal/core`. |
| Wire format | **Unquoted JSON integer.** Field name suffix `_centipoints`. |
| Float conversion | Round-half-even, at the boundary only, logging every lossy row. |
| Forbidden | `float32`, `float64`, `REAL`, `NUMERIC`, `DECIMAL`, and decimal *strings* on the wire, anywhere in `internal/ledger` or `internal/strategy`. |

Realistic maxima are ~10¹¹ centipoints — four orders of magnitude below `MAX_SAFE_INTEGER`
(9.007×10¹⁵), so unquoted integers are safe for every JavaScript consumer. Strings would force every
SDK to parse and would invite locale bugs.

> Supersedes: the security design's `"value_centipoints": "35000"` string form. Strings are wrong.

**Enforced by:** a `golangci-lint` rule banning float types in the two arithmetic packages; the
`NoFloat` invariant at runtime; a contract test asserting the JSON Schema type is `integer`.

## 2. Time

| Rule | Value |
|---|---|
| Storage | `INTEGER` Unix **microseconds**, UTC. Column suffix `_at`. |
| Go type | `Micros int64` in `internal/core`. |
| Wire format | RFC 3339 with microsecond precision, always `Z`. |
| Day buckets | A separate `*_day TEXT` column in `YYYY-MM-DD`, computed in **guild-local** time, where day-bucketing is a domain concept (attendance windows, decay periods). |
| Clock access | Only via an injected `Clock`. `time.Now` is banned outside `internal/clock`. |

Two distinct timestamps exist on ledger batches and must never be conflated:
`effective_at` is **game truth** and may be backdated; `recorded_at` is **system truth** and never is.

## 3. Identifiers

ULID in `TEXT`, 26 characters, Crockford base32, generated in Go. Lexicographically sortable, which
gives free time-ordered cursors and avoids the `uuidv7()` / `gen_random_uuid()` dialect split.

## 4. Sequences — two of them, never the same name

This is the contradiction most likely to break a bot author, because both were called `seq`.

| Name | Scope | Where it appears | Meaning |
|---|---|---|---|
| `seq` | **per pool**, on `ledger_batch` | `as_of_seq` in balance responses, `?since_seq=` on `/ledger/*` and `/audit` | Ledger position. A balance is defined *as of a `seq`*, never as of a timestamp. |
| `event_seq` | **global**, on `event_outbox` | SSE frame `id:`, the `X-DKP-Event-Sequence` header, `Last-Event-ID` | Outbox position for realtime delivery and replay. |

`?since_seq=` is valid **only** on `/ledger/*`, `/audit`, and `/events/replay`. Every other collection
uses the opaque ULID cursor. (`raid` and `item_award` have no `seq`; adjustments project over
per-pool batches, so a global `since_seq` across pools is meaningless.)

## 5. Enums

**The wire value is the database value.** Lowercase `snake_case`, everywhere, with no translation
layer. Both the SQL `CHECK` constraint and the OpenAPI `enum` are generated from one Go catalogue.

```
bid_session.state:  draft open extended closing resolved settled reversed rot resolution_failed
bid_session.mode:   auction_open auction_sealed_first auction_sealed_second
reconciliation_item.status:   open mapped created ignored merged
```

> Supersedes: every UPPERCASE state diagram, `sealed_second` vs `sealed_second_price`, and the
> `resolution`/`state`/`status` field-name drift in the reconciliation API.

Column holding an enum is named for the concept (`state`, `status`, `kind`) and is consistent between
the DDL and the JSON. A resource has **one** of `state` or `status`, never both.

**Enforced by:** the enum catalogue is a Go `const` block; `make gen` writes it into the migration
CHECK and the OpenAPI schema; a test asserts the three copies agree.

## 6. Permissions and scopes — one catalogue, generated

There is exactly one source: `internal/authz/catalogue.go`. It generates the `permission` table seed,
the OpenAPI `x-dkp-permission` metadata, the PAT scope enum, the authorization-matrix header, and
`docs/reference/permissions.md`. Hand-written permission lists are forbidden; `role_permission` is
FK-constrained to `permission(key)`, so a divergent list is a **boot failure**, not a style issue.

**Permission keys** are `<resource>.<action>`, dot-separated, lowercase:

```
roster.read roster.write person.merge character.claim.approve
raid.read raid.create raid.update raid.finalize raid.tick.create raid.tick.delete
item.read item.award item.alias.manage
dkp.read dkp.adjust dkp.decay.run ledger.reverse
bid.read bid.manage bid.reveal_early
calendar.read calendar.write signup.manage
cms.read cms.write cms.moderate
import.run import.commit
webhook.manage token.mint token.revoke
admin.settings admin.security.manage admin.roles.manage admin.backup admin.owner
person.pii.read audit.read ops.read
```

> **`admin.security.manage` was added in Phase 0 PR 5** (Courtney, 2026-08-07), by the "stop and ask"
> path this section defines. `admin.settings` guarded nineteen operations spanning the whole risk
> range — from `PATCH /guild` (rename the guild) to `GET · PATCH /admin/settings`, which
> [`02-api-design.md`](02-api-design.md) describes as mirroring `DKP_*` and therefore reaches
> `DKP_OIDC_CLIENT_SECRET`, `DKP_DISCORD_CLIENT_SECRET` and `DKP_OUTBOUND_ALLOW_CIDRS`.
> [`03-security.md`](03-security.md) relies on that surface being step-up and PAT-forbidden so a
> leaked token cannot relax the SSRF policy and pivot — a guarantee that cannot hold for one key and
> nineteen blast radii.
>
> The split is by **what a compromise costs**, not by what feels dangerous.
> `admin.security.manage` covers exactly the operations that can degrade the instance's security
> posture: `/admin/settings` (both methods — read access to a document mirroring `DKP_*` is
> exfiltration unless every secret is redacted) and `/feed-tokens` (a feed token is a bearer
> credential that outlives the session which minted it, so the floor's "minting/rotating/revoking
> tokens" already covered it).
>
> Everything else stays `admin.settings`, including `PUT /pools/{pool_id}/strategy`. That is the
> highest-consequence *configuration* change in the product, and it is deliberately not here: it
> leaks nothing and enables no pivot, and it is governed by an audit event, `?dry_run=true` and an
> append-only ledger. A key that grows to mean "scary" rather than "can compromise the security
> posture" recreates the overload this split exists to remove.
>
> See [`../development/phase-0-pr5-decisions.md`](../development/phase-0-pr5-decisions.md) for the
> evidence and the full boundary.

**PAT scopes** are `<family>:<verb>`, colon-separated. Scopes are coarser than permissions on
purpose — a scope narrows a token, a permission narrows a role:

```
roster:read roster:write   raids:read raids:write   dkp:read dkp:adjust
loot:read loot:award       bids:read bids:place bids:manage           logs:ingest
calendar:read calendar:write   cms:read cms:write   events:subscribe   webhooks:manage
```

`bids:place` is the **only self-scoped capability** in the catalogue: it authorises placing and
retracting bids *for the authenticated member's own accounts only*, in an already-open session. It
cannot open, extend, close, resolve or settle — that is `bids:manage`, which is an officer scope.
Because a scope check alone cannot express "own accounts only", the authorization matrix carries an
explicit case asserting that a `bids:place` principal is denied on another member's account.

It exists for desktop overlay clients (see
[`08-nparse-plus-integration.md`](08-nparse-plus-integration.md)), which need to bid without holding
officer capability.

### The capability floor

**Effective capability = role permissions ∩ token scopes.** A token can only ever narrow what its
service account's role already grants.

**There is no `admin:*` scope and no all-powerful token.** This is the single biggest deliberate fix
over EQdkp Plus, whose `api_key` impersonates the first superadmin.

Operations that alter **authentication, authorization, or bulk-export state** are **session +
step-up only** and have no scope at all: minting/rotating/revoking tokens (including feed tokens),
editing roles and role assignments, downloading backups, reading PII in bulk, committing an import,
and changing security-affecting instance configuration — identity-provider credentials, MFA and
session policy, and the outbound-request allowlist.

**As permission keys, that set is exactly:** `token.mint`, `token.revoke`,
`admin.security.manage`, `admin.roles.manage`, `admin.backup`, `admin.owner`, `person.pii.read`,
`audit.read`, `import.commit`. This enumeration is normative and supersedes the three different
lists that [`03-security.md`](03-security.md), [`../api/auth-and-scopes.md`](../api/auth-and-scopes.md)
and `.claude/agents/api-contract-guardian.md` each carried before Phase 0 PR 5; all three are
corrected to match. The architectural test derives the `x-dkp-pat-forbidden` set from this list
rather than from a hand-maintained copy.

**`admin.settings` is deliberately NOT in it.** Renaming a guild, adding a server or recomputing a
pool is session-only because no PAT scope family covers instance configuration — not because it
alters authentication state. Session-only and session-plus-step-up are different controls, and
conflating them puts a re-authentication prompt in front of an officer changing the guild's point
label.

> Supersedes: `admin:*`, `admin:tokens` and `admin:backup` as token scopes. Also deletes the
> incoherent "a PAT may not self-deal" rule — `dkp:adjust` exists precisely to create adjustments;
> self-dealing is controlled by `actor_is_beneficiary` audit flagging, not by blocking the scope.

## 7. HTTP conventions

| Concern | Rule |
|---|---|
| Base path | `/api/v1`. Within v1, **additive only**. Breaking changes mint `/api/v2`; v1 lives 18 months minimum with `Deprecation` and `Sunset` headers. |
| `operationId` | Explicit on every operation, `lowerCamelCase`, **never auto-derived and never renamed** — generated SDK method names come from it, so a rename is a breaking change even when the HTTP surface is unchanged. |
| Errors | RFC 9457 `application/problem+json`, with a stable machine `code` from a closed enum and a `type` URL that resolves to a real docs page. Never HTTP 200 with an error body. |
| Pagination | Cursor only, in the body envelope: `{items, next_cursor, has_more}`. `limit` default 50, max 200. Never `Link` headers, never offset. |
| Idempotency | `Idempotency-Key` **required** on every POST that creates domain state. Uniqueness is `(principal_id, key)` where principal is the **service account or user** — never the token — so rotation mid-retry still replays. A stored `request_hash` covers method + path template + body. |
| Concurrency | `ETag` on mutable resources; `If-Match` required on state transitions and on PATCH of raids, ticks and pools. `412` returns the current representation in `meta.current`. |
| Auth transport | `Authorization: Bearer dkp_pat_…` only. Query-string tokens are rejected — **except** on the compat shim, which accepts `?atoken=` because that is what existing bots send. |
| Session cookie | `__Host-dkp_session`. This exact name appears in the `securitySchemes` block. |
| Hidden operations | `Hidden: true` is permitted only on `/healthz`, `/readyz`, `/metrics`, the OAuth callback, and the compat shim. |

## 8. Database conventions

```sql
-- Every table is STRICT. STRICT permits only INT, INTEGER, REAL, TEXT, BLOB, ANY —
-- BIGINT, BOOLEAN, DATETIME, NUMERIC and DECIMAL are ILLEGAL. Use INTEGER (already 64-bit).
-- STRICT makes PRAGMA integrity_check verify column content types, which is the cheapest
-- guard against an agent inserting "350.00" into a centipoint column.

id           TEXT    NOT NULL PRIMARY KEY,   -- ULID
created_at   INTEGER NOT NULL,               -- Micros, UTC
updated_at   INTEGER NOT NULL                -- absent on append-only tables

-- Enums:    TEXT + CHECK (x IN ('a','b'))   -- readable in a DB browser; the officer's
--                                              debugging tool is `sqlite3 dkp.db`, not our UI
-- Booleans: INTEGER NOT NULL CHECK (x IN (0,1))
-- Money:    *_cp    INTEGER                 -- suffix makes float mistakes visible at call sites
-- Ratios:   *_bp    INTEGER                 -- basis points, 10000 = 100%
-- Time:     *_at    INTEGER                 -- Micros;  *_day TEXT for guild-local buckets
-- Names:    name TEXT + name_norm TEXT      -- normalised IN GO, a plain column, then indexed
-- JSON:     *_json  TEXT NOT NULL DEFAULT '{}'  -- validated on write, NEVER queried into
```

Table names are **singular** (`ledger_entry`). Columns are `snake_case`.

`name_norm` is normalised in Go (NFKC + casefold + strip `'` `` ` `` `-`), **not** a generated
column: generated-column expressions may use only deterministic scalar functions, core SQLite has no
NFKC and `lower()` is ASCII-only — and `ALTER TABLE ADD COLUMN` cannot add a STORED column, so every
future normalisation change would force a 12-step table rebuild.

Case-insensitive matching uses `name_norm`, never a collation.

## 9. Tenancy

**Strictly single-guild per instance. There is no `guild_id` column anywhere.**

A missing `WHERE guild_id = ?` is a *silent* cross-guild data leak that no test catches by accident;
removing the column removes the bug class. Guild-level configuration lives in a singleton `guild`
row. A hoster who wants multiple guilds runs multiple containers with separate volumes.

Multi-guild is a deliberate future project, not a retrofit that hides in this schema.

> Supersedes: the `AGENTS.md` draft line requiring `guild_id` on every row and in every `WHERE`.

## 10. The ledger — non-negotiable

- **Append-only, enforced by database trigger.** Never `UPDATE` or `DELETE` a `ledger_batch` or
  `ledger_entry` row, in Go, in SQL, or in a migration. `BEFORE UPDATE OR DELETE … RAISE(ABORT)`.
- **Corrections are reversals.** A new batch with `reverses_batch_id` set and entries negated (or a
  strategy-specific inverse). The original stays visible, struck through.
- **Balances are derived**, defined as of a `seq`. `balance_snapshot` is a droppable cache verified
  nightly.
- **Decay is posted, not computed** — explicit batches with idempotency key `(pool_id, cadence_period)`.
- **Zero-sum splits use largest-remainder allocation** with a deterministic tiebreak on `account_id`.
  Credits must sum to exactly the debit; rounding each credit independently mints or destroys points.
- **Strategies are pure.** No `internal/store`, no `time.Now`, no `math/rand`. Clock and a seeded RNG
  are injected, and the seed is persisted onto the batch so replays are byte-identical.

**Enforced by:** the trigger, *plus* an integration test asserting the trigger fires — so the
guardrail itself cannot be silently regressed.

## 11. Retention and artifacts

Raw uploaded artifacts (RaidRoster dumps, log slices) are **retained by default**, 180 days, with
`/tell`-line redaction at ingest and a guild opt-out. "Any member can download the dump behind this
tick" is the strongest anti-drama mechanism in the design and it dies under parse-and-discard.

`parse_line` is pruned at 90 days, so `item_instance.parse_line_id` is **nullable with
`ON DELETE SET NULL`** — a hard FK to a pruned table fails under `foreign_keys=ON`.

> Supersedes: the security design's "parse and discard by default".

## 12. Character auto-creation

Unknown character names encountered during a parse land in the **reconciliation queue**; they do not
auto-create a person. The award is quarantined, never dropped, and never silently attributed.

`on_unresolved` on a raid submission selects `fail | quarantine | create`, defaulting to
`quarantine`. `create` is an explicit officer choice, not a default.

> Supersedes: the domain model's auto-provisioned `person(state='trial')` default.

## 13. Health checks

| Endpoint | Touches DB? | Used by |
|---|---|---|
| `/healthz` | **No** | The container `HEALTHCHECK`. A DB-touching healthcheck lets Docker kill the container mid-migration. |
| `/readyz` | Yes — DB reachable, migrations at expected version, worker heartbeat fresh | Load balancers, `dkp doctor`, deploy gates |

The Dockerfile `HEALTHCHECK` calls `/healthz`, **not** `dkp doctor`.

> Supersedes: the Dockerfile's `dkp doctor --healthcheck`.

## 14. Metrics

`/metrics` is **disabled by default** (`DKP_METRICS_ENABLED=false`). When enabled it binds a
separate listener and requires `DKP_METRICS_TOKEN`. It is never public and never gated by a PAT
scope.

## 15. Licence firewall

EQdkp Plus core is **AGPL-3.0**; its game modules and raidlog XSD are **CC BY-NC-SA 3.0**
(non-commercial). This project is Apache-2.0.

Reading a user's own database at runtime creates no derivative work. Copying their PHP, their DDL
text, their language strings, or their icon assets does.

The identifiers `pdh_`, `gen_class`, `plus_exchange`, and `__multidkp2event` may appear **only** in
`internal/importer/legacy_names.go` and `internal/api/compat/`. CI greps for them everywhere else.

Class, race and zone tables ship as our own literals. No game data (item names, stats, icons) is
bundled in core; `dkp-p99-seed` is a separate, optional, user-run repository.

## 16. Naming quick reference

| Thing | Convention | Example |
|---|---|---|
| Go package | short, lowercase, no underscores | `internal/ledger` |
| Go test | `TestThing_Condition_Expectation` | `TestBalance_AfterReversal_ReturnsOriginal` |
| SQL table | singular snake_case | `ledger_entry` |
| SQL column | snake_case, typed suffix | `amount_cp`, `closes_at`, `share_bp` |
| JSON field | snake_case | `value_centipoints`, `next_cursor` |
| `operationId` | lowerCamelCase, verb + resource | `createRaidTick` |
| Permission | `resource.action` | `raid.tick.create` |
| PAT scope | `family:verb` | `raids:write` |
| Error code | snake_case, closed enum | `insufficient_balance` |
| Webhook event | `resource.past_tense_verb` | `bid_session.settled` |
| Migration file | `NNNNNN_snake_case.sql`, append-only | `000007_add_bid_hold.sql` |
