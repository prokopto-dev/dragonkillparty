# Endpoint gate list

Every row is a mechanism, not a wish. The **Mechanism** column names the thing that fails; if you
cannot find it in the repo, the gate is not installed yet and the row is a manual check for now.

---

## Declaration gates — `internal/api/arch_test.go`

These run over the live Huma registry, so they see what you actually registered rather than what you
meant to.

| Gate | Rejects |
|---|---|
| `operationId` present, unique, `lowerCamelCase` | Empty, duplicated, or auto-derived ids |
| `operationId` stable | A rename of an id that exists in `origin/main`'s `openapi.json` |
| Security coverage | Any operation with no `Security` block |
| Permission coverage | Any operation with no `Metadata["x-dkp-permission"]` |
| Scope coverage | An operation whose `Security` offers a `pat` alternative but whose `x-dkp-scopes` is empty or absent — or the reverse, scopes declared on a session-only operation |
| Permission key is real | A key not present in `internal/authz/catalogue.go` |
| Idempotency coverage | A mutating `POST` under `/raids`, `/awards`, `/adjustments`, `/bids`, `/raid-submissions`, `/ledger` without a required `Idempotency-Key` |
| `If-Match` coverage | A state-transition `POST` or a `PATCH` of a mutable resource without required `If-Match` |
| Envelope shape | A collection response that is not `{items, next_cursor, has_more}` |
| Error shape | A declared error response that is not `application/problem+json` with a `code` from the closed enum |
| `since_seq` placement | `?since_seq=` on anything other than `/ledger/*`, `/audit`, `/events/replay` (canonical §4) |
| No hidden operations | `Hidden: true` outside `/healthz`, `/readyz`, `/metrics`, the OAuth callback, and the compat shim |
| Route location | A route declared outside `internal/api` (package scan vs registry) |

## Generated-artefact gates — `make verify-generated`

| Gate | Rejects |
|---|---|
| Spec drift | A committed `openapi/openapi.json` that differs from regeneration |
| sqlc drift | A hand-edit to `internal/store/sqlitegen/` |
| SDK drift | A hand-edit to `clients/ts/` or `clients/python/` |
| Postgres compile | `var _ Queries = (*pggen.Queries)(nil)` no longer satisfied |

Generated files are **regenerated on rebase, never merged**. A conflict in one means `make gen`, not
a manual resolution.

## Contract gates — `oasdiff`

| Gate | Rejects |
|---|---|
| `oasdiff breaking` vs `origin/main` | Any breaking delta without the `!breaking-api` label **and** a line in `docs/api-changelog.md` |
| `oasdiff changelog` | Nothing — it posts a sticky PR comment so reviewers read the delta in English |

The label is author-settable, so it is a "did you notice" control. CODEOWNERS on
`docs/api-changelog.md` is what converts it into a review control.

## Behaviour gates — `test/integration/`

| Gate | Rejects |
|---|---|
| Response validation middleware | A response that does not match its declared schema (active whenever `DKP_ENV != production`) |
| Authorization matrix (`test/integration/testdata/authz_matrix.tsv`) | An operation missing from the matrix, or a dead matrix row |
| PAT parity | A capability the browser has and a scoped PAT does not |
| Idempotency replay | A mutating POST where 100 concurrent identical requests produce more than one effect |
| Statement-count budget | An N+1 introduced into a collection read |
| Traffic conformance | The SPA calling a route that is not in the spec |

## Lint and repo gates — `make lint`

| Gate | Rejects |
|---|---|
| `forbidigo` | `http.ResponseWriter` in a handler signature; `encoding/json` in `internal/api` outside the error helper |
| Float ban | `float32`/`float64` in `internal/ledger` or `internal/strategy` |
| `total(` grep | SQLite's `total()` anywhere in the tree |
| `sql.Open` grep | `*sql.DB` held outside `internal/store` |
| `time.Now` ban | Wall-clock access outside `internal/clock` |
| `no-restricted-globals` | `fetch`/`XMLHttpRequest` in `web/src` outside `web/src/api` |
| i18n rule | A bare user-facing string literal in JSX |

---

## Per-step done-when

Tick these in order. Each one is cheap; the failure they prevent is not.

**1 — Decide**
- [ ] No existing operation should be extended instead.
- [ ] `operationId` chosen, `lowerCamelCase`, not already in `openapi/openapi.json`.
- [ ] The permission key exists in `internal/authz/catalogue.go`.
- [ ] The scope exists in the PAT scope enum.

**2 — Query**
- [ ] Lives in `db/queries/*.sql`.
- [ ] Reuses a shape from `db/RECIPES.md`, or a new shape is added there.
- [ ] `sum()` not `total()`; no query into a `*_json` column; no `guild_id`.
- [ ] No new table or column (else `/add-migration` first).

**3 — Generate**
- [ ] `make gen` is clean.
- [ ] `Queries` interface updated; both `var _ Queries` assertions compile.

**4 — Service**
- [ ] Logic is in the owning package, not the handler.
- [ ] Mutations go through `store.Tx`.
- [ ] Ledger writes go through `internal/ledger`.
- [ ] Clock injected; errors wrapped with `%w` and context.

**5 — Handler**
- [ ] `OperationID`, `Security`, `x-dkp-permission`, `DefaultStatus`, `Errors`, `Tags` all present.
- [ ] `Idempotency-Key` required if it is a creating POST.
- [ ] `If-Match` required if it is a transition or a PATCH; `ETag` emitted on the read.
- [ ] Collection uses the shared cursor envelope.
- [ ] Collection passes the request principal's class to `Encode`/`Decode` — not folded into
      `FilterFingerprint`, and row-level scope still applied in the query on every page.
- [ ] Returns Huma error types; never touches `http.ResponseWriter`.

**6 — Regenerate**
- [ ] `make gen` again.
- [ ] `openapi/openapi.json` and both SDK diffs are staged, unedited by hand.

**7 — Tests**
- [ ] Integration test against the real server.
- [ ] PAT-parity case if the SPA will call it.
- [ ] Idempotency replay case if mutating.
- [ ] Statement-count budget if it reads a collection.
- [ ] `require`, not `assert`. Named `TestThing_Condition_Expectation`.

**8 — Front end**
- [ ] Generated client only. No `fetch`, no hand-written types, no bare JSX strings.

**9 — Docs**
- [ ] Endpoint page added or updated under `docs/api/`.
- [ ] One line in `docs/api-changelog.md`.
- [ ] Any new error code has a docs page its `type` URL resolves to.

**10 — Verify**
- [ ] `scripts/verify.sh` exits 0 with no `CANNOT RUN` lines.
- [ ] `make check` is green.

---

## Things that pass CI and are still wrong

CI cannot catch these. A reviewer can, and so can you before asking for one.

| Symptom | Why it matters |
|---|---|
| A second operation that is 90% an existing one | Two SDK methods for one concept, forever. `operationId` cannot be un-shipped. |
| An `operationId` that reads well now and ages badly (`getStandingsV2`, `listRaidsNew`) | The id outlives the reason for the suffix. |
| An additive change that semantically renames a concept | `oasdiff` calls it additive; every bot author disagrees. |
| A response embedding a document SSE also carries | Two representations of one resource; they will drift. |
| An `Errors` list that omits a status the handler can actually return | The SDK's typed error union is wrong and no test notices. |
| A collection with no default `limit` cap | 200 max, 50 default. Uncapped is a denial-of-service on a Raspberry Pi. |
| A permission key that is *nearly* right (`raid.update` where `raid.finalize` is meant) | Passes every gate. Grants an officer power the guild did not vote for. |
