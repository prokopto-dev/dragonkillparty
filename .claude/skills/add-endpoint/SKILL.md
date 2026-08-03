---
name: add-endpoint
description: Add or change an HTTP API endpoint in Dragon Kill Party. Use whenever work requires a new route, a new operation, a new request or response field, a new status code, a new query parameter, or any change to an existing operation's contract — including when the trigger is "the UI needs to show X".
argument-hint: "[METHOD] [/api/v1/path]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(make gen), Bash(make test), Bash(make test-unit), Bash(make vet), Bash(make lint), Bash(make check), Bash(${CLAUDE_SKILL_DIR}/scripts/verify.sh *)
---

# Add an endpoint

One endpoint touches nine artefacts across two languages. Skipping one either fails CI ten minutes
later or — worse — passes CI and ships a permanently wrong `operationId` into every downstream SDK,
where it can never be renamed.

| # | Artefact | Path |
|---|---|---|
| 1 | Query | `db/queries/<domain>.sql` |
| 2 | Generated store | `internal/store/sqlitegen/` + the `Queries` interface in `internal/store/store.go` |
| 3 | Service | `internal/<domain>/service.go` |
| 4 | Handler + operation | `internal/api/<resource>.go` |
| 5 | Spec | `openapi/openapi.json` |
| 6 | SDKs | `clients/ts/`, `clients/python/` |
| 7 | Tests | `test/integration/<resource>_test.go` |
| 8 | Front end | `web/src/` via the generated client only |
| 9 | Docs | `docs/api/…` + a line in `docs/api-changelog.md` |

**Read first, in this order:**

1. [`internal/api/EXAMPLE_ENDPOINT.md`](../../../internal/api/EXAMPLE_ENDPOINT.md) — the worked
   example. Copy its shape rather than recalling Huma from memory. It is **Huma v2**:
   `huma.Register` + `humachi.New`. `huma.Resource`, `huma.NewRouter` and
   `huma.Operation{Handler: …}` are v1 and do not exist here.
2. [`db/RECIPES.md`](../../../db/RECIPES.md) — find an existing query shape before inventing one.
3. [`shapes.md`](./shapes.md) — canonical input/output structs, envelopes, headers, error codes.
4. [`checklist.md`](./checklist.md) — the full gate list, with the mechanism that catches each miss.

---

## The ten steps, in order

Later steps consume generated output from earlier ones. **Do not reorder.**

### 1. Decide it belongs

Before writing anything, answer these:

| Question | If yes |
|---|---|
| Does an existing operation already return this, or could it with one optional field? | Extend it. A near-duplicate operation is this skill's most common failure — `listRaids` and `listRecentRaids` is two operations, two SDK methods, two docs pages and one concept. |
| Is this "the UI needs it but a bot would not"? | That distinction does not exist here. **There are no UI-private endpoints.** If the SPA can do it, a scoped PAT can do it, and three CI gates prove it. |
| Is it a state transition? | It gets `POST /resource/{id}/close`, not `PATCH {"state":"closing"}`. The PATCH form lies about what happens. |
| Does it need a permission key that is not in `internal/authz/catalogue.go`? | **Stop and ask.** See the bottom of this file. |

Then name the operation. `operationId` is `lowerCamelCase`, verb + resource: `createRaidTick`,
`listBidSessions`, `reverseLedgerBatch`. It is **public API** — the generated SDK method name derives
from it, so renaming one is a breaking change even when the HTTP surface is unchanged.

### 2. Query

Add or reuse a query in `db/queries/*.sql`. Raw SQL lives there and nowhere else; `db.Query` and
`db.Exec` outside `internal/store` are grep-banned.

- Check `db/RECIPES.md` first. Copy the cursor, upsert, `BalanceAsOfSeq` or standings shape.
- `sum()`, never `total()` — `total()` returns REAL and would silently float the ledger. A repo grep
  gate rejects `total(`.
- Never query into a `*_json` column. If you need to filter or sort on a fact, it is a real column.
- There is **no `guild_id`**. Scope comes from the request principal (canonical §9).

**New table or new column means stopping here and running `/add-migration` first.** Come back when
the migration is committed.

### 3. `make gen`

Regenerates `internal/store/sqlitegen/`, compiles the `internal/store/pggen/` target, and fails if
the hand-written `Queries` interface in `internal/store/store.go` is now unsatisfied. When it fails,
add the method to the interface — the two `var _ Queries = …` assertions are the entire mechanism
keeping the Postgres port cheap.

### 4. Service

Domain logic goes in the owning package (`internal/raids`, `internal/loot`, `internal/bids`, …),
never in the handler. Handlers marshal; services decide.

- Every mutation goes through `store.Tx`.
- **Ledger writes go through `internal/ledger`.** Never write `ledger_batch` or `ledger_entry` rows
  directly, and never `UPDATE`/`DELETE` them at all — corrections are reversal batches. A DB trigger
  raises, and an integration test asserts the trigger fires.
- The clock is injected. `time.Now` is banned outside `internal/clock`.
- Wrap errors: `fmt.Errorf("load pool %s: %w", id, err)`. Sentinels are `ErrNotFound`, `ErrConflict`
  in the owning package.

### 5. Handler and operation

In `internal/api` only — **one file per resource**, never a shared registry file, which conflicts on
every parallel feature PR. An architectural test enumerates the Huma registry against a package scan
and fails on a route declared anywhere else.

Every `huma.Register` call declares all six of these:

| Field | Rule |
|---|---|
| `OperationID` | Explicit, `lowerCamelCase`, unique. Never auto-derived, never renamed. |
| `Security` | Both schemes where both apply: `{"pat": {"<family>:<verb>"}}` and `{"session": {}}`. |
| `Metadata["x-dkp-permission"]` | One key from `internal/authz/catalogue.go`. |
| `DefaultStatus` | Explicit. |
| `Errors` | The status codes this operation can actually return. |
| `Tags` | The resource family. |

Plus, by shape:

- **Mutating POST that creates domain state** → required `Idempotency-Key` header.
- **Mutable resource** (PATCH, or any state transition) → required `If-Match`; emit `ETag` on reads.
- **Collection** → the shared cursor envelope `{items, next_cursor, has_more}`. Never a bespoke
  envelope, never offset, never `Link` headers.
- `?since_seq=` is legal **only** on `/ledger/*`, `/audit` and `/events/replay` (canonical §4).
  Everything else uses the opaque ULID cursor.

Handlers return Huma error types via `problem.From(err)`. They never write to
`http.ResponseWriter` — `forbidigo` bans it in handler signatures.

See [`shapes.md`](./shapes.md) for the exact struct shapes to copy.

### 6. `make gen` again

Rewrites `openapi/openapi.json` and regenerates `clients/ts` and `clients/python`. **Commit those
diffs. Never hand-edit them.** CI regenerates and fails on any difference.

### 7. Tests, in this order

Against the real server and a real SQLite database in `t.TempDir()`. No mocks — there is no fake
`Queries` implementation and a lint rule forbids adding one. Use `require`; `assert` is banned.

| # | Test | When | Asserts |
|---|---|---|---|
| 1 | Integration | Always | The happy path and each declared error. Response-validation middleware checks every response against its declared schema, so a shape mistake fails here. |
| 2 | PAT parity | If the SPA will call it | The same request with a scoped PAT produces an identical response. If the browser can do something a bot cannot, CI goes red. |
| 3 | Idempotency replay | If mutating POST | 100 concurrent identical requests → one effect, 99 replays. |
| 4 | Statement-count budget | If it reads a collection | A declared budget. This is the N+1 tripwire. |

Name them `TestThing_Condition_Expectation`.

### 8. Front end

Only if the SPA needs it. Use the generated client from `web/src/api`. No `fetch`, no
`XMLHttpRequest`, no hand-written types — ESLint `no-restricted-globals` enforces it, and a CI gate
proves `web/src` holds no HTTP call outside `web/src/api`.

No hardcoded user-facing strings; they go through the message catalogue (an ESLint rule fails on a
bare string literal in JSX).

### 9. Docs and changelog

- Add or update the endpoint's page under `docs/api/`.
- Add one line to `docs/api-changelog.md` — CODEOWNERS-protected.
- A new error code is a spec change and needs its own docs page at the `type` URL.
- A behaviour change without a docs change fails the docs-sync check.

### 10. Verify

```bash
.claude/skills/add-endpoint/scripts/verify.sh   # arch tests · spec drift · oasdiff · PAT parity
make check
```

`verify.sh` is the definition of done, not this prose. If it cannot run a gate, that is a failure —
it exits non-zero and names the missing target. Do not treat a gate it could not run as a pass.

---

## Stop and ask if

- **The endpoint needs a new permission key.** The catalogue in `internal/authz/catalogue.go`
  generates the `permission` table seed, and `role_permission` is FK-constrained to
  `permission(key)` — so adding one is a schema change with a boot-failure blast radius.
- **The operation does not fit REST plus action sub-resources.** If you are reaching for a verb in a
  path segment that is not a sub-resource on a state machine, the resource model is wrong, not the
  route.
- **The response would embed a document that SSE also carries.** There is exactly one representation
  of a resource. SSE frames and webhook deliveries carry `{topic, event_type, event_seq, resource}` and
  never documents; the consumer refetches through this same endpoint.
- **You cannot make it idempotent.** A log parser on an officer's home connection retries constantly.
  A non-idempotent create is a double-credited raid night nobody notices for three weeks.
