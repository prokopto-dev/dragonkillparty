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

**`?as_of_seq=` is a different parameter and a different allowlist.** `since_seq` asks "what changed
after this point" and is a sync cursor; `as_of_seq` asks "what was true at this point" and is a
time-travel read. It is valid on `/accounts/{id}`, `/accounts/{id}/balance`,
`/accounts/{id}/spendable` and **`/standings`**. Standings is on the list because reproducing the
table at any past `seq` is the visible proof that balances are a `SUM` over an append-only log; an
approximation there is worse than no feature. Both parameters stay closed lists — adding an endpoint
to either is a spec change.

**A `seq` is only meaningful inside a pool.** Because `seq` is allocated per pool, a UI that prints
one "as of seq N" marker across a multi-pool view is stating something false. Any screen showing an
as-of marker must be scoped to a pool, and any screen spanning pools shows the marker per pool or not
at all. `event_seq` is the only sequence that orders the whole instance, and it orders *deliveries*,
not balances.

> The mockups in [`mockups/`](mockups/) print an instance-wide `seq` on the officer dashboard and in
> several portal footers. That is a known mockup error, recorded in
> [`11-ui-backend-contract.md`](11-ui-backend-contract.md) §1b; the screens change, not this rule.

## 5. Enums

**The wire value is the database value.** Lowercase `snake_case`, everywhere, with no translation
layer. Both the SQL `CHECK` constraint and the OpenAPI `enum` are generated from one Go catalogue.

```
bid_session.state:  draft open extended closing resolved settled reversed rot resolution_failed
bid_session.mode:   auction_open auction_sealed_first auction_sealed_second
bid_session.visibility:       blind open
bid.tier:                     main main_offspec alt anyone
reconciliation_item.status:   open mapped created ignored merged
character_key.state:          have partial missing
character_key.provenance:     self_reported officer_verified parsed
bank_request.state:           requested approved pulled delivered cancelled
main_swap_request.state:      quoted pending approved applied denied expired
vouch.state:                  pending paid void
policy_acceptance.state:      accepted stale
```

> Supersedes: every UPPERCASE state diagram, `sealed_second` vs `sealed_second_price`, and the
> `resolution`/`state`/`status` field-name drift in the reconciliation API.

**`bid.tier` is ordered, and the order is the rule.** The four values form a ladder — `main`
outranks `main_offspec` outranks `alt` outranks `anyone` — and resolution compares tier *before*
amount. It is therefore the one enum in the system whose declaration order is semantic, so it is
stored as the `TEXT` value with an `ORDER BY CASE` in the resolver rather than as an integer:
the wire value stays readable and the ordering stays in one place. The tier is **derived
server-side** from the bidding character at bid time and never accepted from a client.
See [`../guides/auctions.md`](../guides/auctions.md) for the ladder and the two-phase resolution.

Column holding an enum is named for the concept (`state`, `status`, `kind`) and is consistent between
the DDL and the JSON. A resource has **one** of `state` or `status`, never both.

**Enforced by:** the enum catalogue is a Go `const` block; `make gen` writes it into the migration
CHECK and the OpenAPI schema; a test asserts the three copies agree. Those per-vocabulary tests each
check their own region against their own catalogue and say nothing about a vocabulary that has no
catalogue at all, so **`ENUM001` in `scripts/repo-gates.sh`** is the half that covers the *next* one:
a string-enum `CHECK` in `db/schema.hcl` outside the `BEGIN`/`END GENERATED` markers fails the
`lint / repo` job, in either SQL quote form. Boolean `CHECK`s (`x IN (0, 1)`) are not string enums
and are not caught. A region counts as generated only when a catalogue declares its marker line in
Go, so a fabricated marker pair exempts nothing; the waiver is a `// dkp:enum-literal <reason>`
comment above the check, which puts the exception in the schema diff a reviewer reads.

## 6. Permissions and scopes — one catalogue, generated

There is exactly one source: `internal/authz/catalogue.go`. It generates the `permission` table seed,
the OpenAPI `x-dkp-permission` metadata, the PAT scope enum, the authorization-matrix header, and
`docs/reference/permissions.md`. Hand-written permission lists are forbidden; `role_permission` is
FK-constrained to `permission(key)`, so a divergent list is a **boot failure**, not a style issue.

**Permission keys** are `<resource>.<action>`, dot-separated, lowercase:

```
roster.read roster.write person.merge character.claim.approve character.key.verify
raid.read raid.create raid.update raid.finalize raid.tick.create raid.tick.delete
raid.custody.manage
item.read item.award item.alias.manage item.priority.manage
dkp.read dkp.adjust dkp.decay.run ledger.reverse
bid.read bid.manage bid.reveal_early
bank.read bank.request bank.fulfil bank.manage
calendar.read calendar.write signup.manage
draft.read draft.vote draft.manage
swap.request swap.approve swap.policy.manage
recruit.read recruit.comment recruit.decide vouch.manage
policy.read policy.write
cms.read cms.write cms.moderate
import.run import.commit
webhook.manage token.mint token.revoke
admin.settings admin.security.manage admin.roles.manage admin.backup admin.owner
person.pii.read audit.read ops.read
```

> **Twenty keys were added when the UI mockups were reconciled** (see
> [`11-ui-backend-contract.md`](11-ui-backend-contract.md)). Four points about the shape they took,
> because each was a choice and the alternative is the obvious one:
>
> - **`bank.*` splits `request` from `fulfil`.** Asking the bank for an item and handing one over are
>   different populations — every member does the first, an officer does the second — and the
>   delivery handshake is two-sided precisely so neither party can close it alone.
> - **`swap.request` is a member capability.** A main swap is priced and approved, but *asking* is
>   ordinary member behaviour; gating the request behind an officer key would make the quote screen
>   unreachable by the person it is for.
> - **`recruit.comment` is separate from `recruit.decide`.** Members read applications and leave
>   signed feedback; officers decide. That is the whole design of the recruitment surface, and one
>   key for both would collapse it.
> - **`character.key.verify` is not `roster.write`.** A member self-reports a zone key; only an
>   officer can move it to *verified*, because verified keys gate raid eligibility. Self-report and
>   verification are the same column with different authority, which is exactly when a separate key
>   earns its place.
>
> `quake.publish` was deliberately **not** created: publishing a quake is `draft.manage`. A quake and
> the draft week it triggers are one officer workflow, and a key nobody can hold separately is a key
> that only adds a row to the matrix.

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
bank:read bank:request bank:manage    draft:read draft:vote
recruit:read recruit:manage           swap:read swap:manage
```

### Self-scoped capabilities

Three scopes authorise acting **for the authenticated member's own accounts only**:
`bids:place`, `bank:request` and `draft:vote`. A scope check alone cannot express "own accounts
only", so each carries an explicit authorization-matrix case asserting it is **denied on another
member's account**.

> `bids:place` was the only one until the UI mockups were reconciled, and several documents still say
> so. `bank:request` (asking the guild bank for an item) and `draft:vote` (submitting a ranked
> ballot) are the same shape: ordinary member actions a bot may perform on its owner's behalf and on
> nobody else's. Adding them to the set is what makes the member-facing bank and draft surfaces
> reachable by anything other than a browser session.

`bids:place` authorises placing and retracting bids in an already-open session. It cannot open,
extend, close, resolve or settle — that is `bids:manage`, an officer scope. It exists for desktop
overlay clients (see [`08-nparse-plus-integration.md`](08-nparse-plus-integration.md)), which need to
bid without holding officer capability.

`swap:read` exists without a `swap:request` sibling because requesting a swap moves points and
changes a main — a bot should be able to quote one and never commit one.

`policy:*` is deliberately absent. Policies are read through `cms:read` and written through
`cms:write`: they are versioned documents in the same library as articles, and a third content scope
would have to be explained to every bot author for no capability they lack.

**Enforced by:** the authorization matrix, which must carry a denied-on-another-member case for each
of the three. A self-scoped capability with no such case is a hole, not an omission.

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

**As permission keys, that set is exactly:**

```
token.mint token.revoke
admin.security.manage admin.roles.manage admin.backup admin.owner
person.pii.read audit.read
import.commit
```

This enumeration is normative and supersedes the three different lists that
[`03-security.md`](03-security.md), [`../api/auth-and-scopes.md`](../api/auth-and-scopes.md) and
`.claude/agents/api-contract-guardian.md` each carried before Phase 0 PR 5; all three are corrected
to match. The architectural test derives the `x-dkp-pat-forbidden` set from this list rather than
from a hand-maintained copy.

The block above is fenced, like the permission-key and PAT-scope lists, because it is **parsed**:
`TestCapabilityFloor_MatchesCanonicalConventions` compares `authz.CapabilityFloor()` against these
tokens element by element and in both directions, so the Go function and this section cannot drift
apart. Keep the order in sync with `CapabilityFloor()` — adding a key here without adding it there
(or the reverse) is a red test, which is the point.

**`admin.settings` is deliberately NOT in it.** Renaming a guild, adding a server or recomputing a
pool is session-only because no PAT scope family covers instance configuration — not because it
alters authentication state. Session-only and session-plus-step-up are different controls, and
conflating them puts a re-authentication prompt in front of an officer changing the guild's point
label.

**`ops.read` is deliberately NOT in it either, and its category is why the question comes up.** It is
grouped under *sensitive reads* beside `person.pii.read` and `audit.read`, which **are** in the floor
— but the category is a display grouping for the role editor and the matrix, not a security boundary.
The floor is the set of keys, and membership is decided by what a compromise costs. `ops.read` reads
job queues, doctor checks and the last ledger-verify result
([`02-api-design.md`](02-api-design.md)): operational status, carrying neither PII nor a
security-affecting read, so it fails the "authentication, authorization, or bulk-export state" test
above. Like `admin.settings` it is **session-only by omission** — no PAT scope family covers the
operational surface — which means an `/ops` operation declares `{"session": {}}` alone and declares
**neither** `x-dkp-scopes` **nor** `x-dkp-pat-forbidden`. Marking it pat-forbidden asserts a floor
membership this paragraph denies, and `TestArch_ScopeCoverage_MatchesSecurity` derives that set from
`authz.CapabilityFloor()`, so it is a red test rather than a judgement call.

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
- **Balances are derived**, defined as of a `seq`. `balance_snapshot` caches that sum and is verified
  nightly. It is **load-bearing, not droppable**: the log remains the only source of truth, but
  measurement found no honest fallback that serves the standings page from it, so losing the cache is
  a *rebuild* and the nightly replay is a correctness dependency rather than hygiene.
- **Decay is posted, not computed** — explicit batches with idempotency key
  `(pool_id, kind, cadence_period)`. The `kind` is load-bearing rather than decorative: `decay`, `cap`
  and `start_points` share one cadence vocabulary and one `decay_run` table, so a two-part key makes
  the second family of any period collide with the first and look successfully deduplicated
  (ADR-0024).
- **Zero-sum splits use largest-remainder allocation** with a deterministic tiebreak on `account_id`.
  Credits must sum to exactly the debit; rounding each credit independently mints or destroys points.
- **Strategies are pure.** No `internal/store`, no `time.Now`, no `math/rand`. Clock and a seeded RNG
  are injected, and the seed is persisted onto the batch so replays are byte-identical.

**Enforced by:** the trigger, *plus* an integration test asserting the trigger fires — so the
guardrail itself cannot be silently regressed.

> Supersedes: this section's own "`balance_snapshot` is a droppable cache", per
> [ADR-0023](../adr/0023-balance-snapshot-is-load-bearing.md), which measured it: 13 pages against
> the cache versus 10,412 against the definitional SUM, over 527,164 entries. "Derived" is still
> true and is no longer the whole story. The phrase survives elsewhere in the tree — issue #204 is
> the sweep — and **this line is the one that decides**.

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
| `/readyz` | Yes — DB reachable, migrations at expected version, the ledger's append-only protection intact, worker heartbeat fresh | Load balancers, `dkp doctor`, deploy gates |

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
| Design token | `--<role>-<variant>[-<step>]`, kebab-case | `--color-accent-700`, `--space-3` |

## 17. Design tokens

The shipped look is **Nocturne**, and it is normative:
[`09-frontend-and-design-system.md`](09-frontend-and-design-system.md) is the document you
implement against, `mockups/nocturne/styles.css` is the artefact it was transcribed from.

| Concern | Rule |
|---|---|
| Source of truth | One token sheet in `web/src/styles/tokens.css`. Every colour, space, radius, shadow and font comes from a `var(--…)`. |
| Palette count | **One. Nocturne is dark-only at 1.0.** There is no light theme and no `prefers-color-scheme` branch. Light mode is on the deferred table for 1.1. |
| Theming | A guild themes by **overriding token values**, never by adding CSS. No free-form stylesheet, no per-file template override, no LESS — that engine is why nobody could upgrade EQdkp. |
| Contrast floor | Every guild-supplied colour passes a validator at **≥ 3:1 against `--color-bg`** before it is stored. The validator is server-side, so a bot setting a theme is held to it too. |
| Status colours | `--color-success`, `--color-warning`, `--color-danger` are **ours, not Nocturne's** — the system ships none. Derived in OKLCH at the ramps' own lightness steps so they read as one family. |
| Density | `comfortable` / `compact` retunes **spacing only**. The type scale is fixed; a density mode that changes font sizes changes what a table means. |

**Two helpers carry most of the colour surface** and belong in the token layer, not in components:

```
soft(p)  →  color-mix(in srgb, var(--color-text)   <p>%, transparent)
tint(p)  →  color-mix(in srgb, var(--color-accent) <p>%, transparent)
```

**Enforced by:** two mechanisms banning raw hex and raw `px` in `web/src` outside the token layer,
because neither covers it alone — `DS001`/`DS002` in `scripts/repo-gates.sh` grep the stylesheets
(ESLint cannot lint CSS), and `no-restricted-syntax` in `web/eslint.config.js` walks the AST of
`*.ts`/`*.tsx` (a grep cannot tell a value from prose, and JSX text is not a string literal). The
AST half also catches the numeric spelling, since React serialises `style={{ padding: 4 }}` as
`padding: 4px`. Removing either half is a regression against this section. The design system ships
its own `_adherence.oxlintrc.json` expressing the same two rules. Plus: a unit test on the contrast
validator; and `test/repo/design_tokens_test.go`, which parses §2 of
`09-frontend-and-design-system.md` and asserts the shipped sheet matches it — so a token cannot be
renamed, revalued or added without the document moving with it, and the `--color-*` namespace stays
closed to that table.
