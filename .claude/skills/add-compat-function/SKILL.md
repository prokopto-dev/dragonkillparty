---
name: add-compat-function
description: Add an EQdkp Plus api.php shim function under internal/api/compat. Use when a migrating guild's existing bot (Castle Steward, bidbot2, jDKP, froakbot, or a home-grown script) calls a legacy function the shim does not yet answer.
argument-hint: "[function_name]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(make gen), Bash(make test), Bash(make check)
---

# Add a compat-shim function

`/api/compat/eqdkp/api.php` exists so a guild's existing bots keep working on cutover day. It is
~700 lines over services that are **already correct** — a thin translation layer, never a second
implementation.

It is deprecated from day one, `Hidden: true`, rate-limited 5× harder than v1 (60 req/min), and
carries `Deprecation`/`Sunset` headers with no sunset date shorter than 24 months.

**This is the code most likely to attract an AGPL paste**, because the task is literally "match
EQdkp's behaviour". Read the licence rules in step 2 before you read anything of theirs.

---

## Steps

### 1. Confirm a real bot sends this function

The bar is a **captured request shape from real bot traffic**, not a function you found in their
source. Speculative parity is how a deprecated surface grows a maintenance burden nobody asked for.

Evidence that counts: a golden request fixture, a guild's cutover report, an `import-failure` or
`parity-gap` issue naming the bot. If you have none, stop and ask.

The 1.0 target is small and known: `points`, `add_raid`, `add_item`, and the `data` export read.

### 2. Licence firewall — read this before opening anything of theirs

EQdkp Plus core is **AGPL-3.0**; its game modules and raidlog XSD are **CC BY-NC-SA 3.0**
(non-commercial). This project is Apache-2.0.

| Allowed | Forbidden |
|---|---|
| Reading a user's own database at runtime | Copying their PHP, their DDL text, their language strings, their icons |
| Observing a request/response shape a bot sends over the wire | Transcribing their handler source |
| Our own literals for class/race/zone tables | Their seed data |

The identifiers `pdh_`, `gen_class`, `plus_exchange` and `__multidkp2event` may appear **only** in
`internal/importer/legacy_names.go` and `internal/api/compat/`. CI greps for them everywhere else.

### 3. Implement over an existing v1 service

The shim function calls the same service the v1 handler calls. **No new domain logic lives in
`internal/api/compat/`.** If the behaviour a bot needs does not exist, add it with `/add-endpoint`
first, then translate.

Ledger writes still go through `internal/ledger`. Idempotency still applies — a legacy bot with no
`Idempotency-Key` gets one derived from the request, deterministically, so its retries still replay.

### 4. Resolve legacy ids through `import_id_map`

Old bots have EQdkp integer `member_id`s hardcoded in config files written years ago. They resolve
through `import_id_map(import_source_id, src_table, src_pk) → (entity_kind, new_id, row_hash)`,
which is persisted for exactly this reason.

An unresolvable legacy id is an explicit error naming the id — never a silent fallback to "member 1".

`legacy_id` is exposed on imported entities and **only** through this shim; v1 never speaks integer
ids.

### 5. Keep `?atoken=` confined to the shim

Query-string tokens are rejected everywhere else with `401 token_in_query_string`. The shim accepts
`?atoken=` because that is what existing bots send, and **logs a per-token deprecation warning naming
the token prefix**, so an officer can see exactly which bot to migrate.

The token still goes through the ordinary capability floor: effective capability = role permissions ∩
token scopes. **There is no all-powerful token here either** — that EQdkp's own `api_key`
impersonates the first superadmin is the single biggest deliberate fix in this design, and the shim
does not reintroduce it.

### 6. Emit the legacy response shape

The shim emits EQdkp's XML-shaped **JSON**. There is no XML in v1.

Match the field names and nesting a bot parses. Do not "improve" them — a bot that breaks on cutover
day defeats the entire purpose of the surface.

> **Resolve before implementing:** EQdkp returned HTTP 200 with an error body, which is the
> incumbent's most-complained-about property and is banned in v1 (canonical §7). Whether the shim
> reproduces that for bot compatibility or returns a correct status is **not decided in the design**.
> Check the golden fixtures for what real bots tolerate, and if they do not answer it, ask. Do not
> pick one silently.

### 7. Deprecation counter and headers

| Artefact | Value |
|---|---|
| Per-function metrics counter | So an admin can see whether anything still calls it |
| `Deprecation` header | Present from the first release |
| `Sunset` header | Never sooner than 24 months out |
| Rate-limit bucket | `compat`, 60 req/min — deliberately harsher, to create migration pressure |
| `Hidden: true` | The shim is one of the five allowlisted hidden operations |

### 8. Golden request/response fixtures

One pair per function, captured from real bot traffic shapes, under `test/fixtures/compat/`.
CODEOWNERS-protected; `-update` refused when `CI=true`.

Add a test asserting the deprecation counter increments and the headers are present — those are the
mechanism by which the surface eventually goes away.

### 9. Docs

- `docs/migration/existing-bots.md` — the function, what it maps to, and the v1 equivalent to migrate
  toward.
- A line in `docs/api-changelog.md`.

### 10. `make check`

---

## Stop and ask if

- **No real bot is known to send this function.** Speculative parity is not in scope.
- **The behaviour does not exist in a v1 service.** Build it in v1 first; the shim is a translation
  layer, not a back door.
- **Matching EQdkp exactly would require a permission or scope model v1 does not have.** Clamp
  downward, never round up — and ask.
- **You are about to read EQdkp's PHP to answer a question.** The wire shape is observable from a
  request capture. Their source is not a permitted input.
- **The function would write to the ledger in a way v1 does not allow** (editing history, deleting a
  batch). The append-only trigger raises, and it is right to.
