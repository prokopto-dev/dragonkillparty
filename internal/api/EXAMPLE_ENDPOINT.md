# Example endpoint, end to end

**Status:** target state. The code below lands in Phase 0 PR 5; this file is written first so the
pattern exists before anything copies it.

Copy this file's shape before writing a new endpoint. Its acceptance criterion is that an agent
given only this file and [`db/RECIPES.md`](../../db/RECIPES.md) can add an endpoint with no further
questions. If you had to ask one, that is a bug in this file — fix it here.

Worked example: `GET /api/v1/guild` and `PATCH /api/v1/guild`, the singleton guild-settings resource.

> **Huma v2, not v1.** `huma.Register` and `humachi.New` are the v2 API. `huma.Resource`,
> `huma.NewRouter` and `huma.Operation{Handler: ...}` are **Huma v1 and do not exist here**. This is
> the single most common hallucination in this codebase. If you are writing one of those, stop.

---

## The nine steps, in order

Later steps consume generated output from earlier ones. Do not reorder.

| # | Step | File | Gate that catches you skipping it |
|---|---|---|---|
| 1 | Schema | `db/schema.hcl` | `verify-generated` |
| 2 | Query | `db/queries/*.sql` | `sqlc` compile error |
| 3 | `make gen` | — | `Queries` interface unsatisfied → build error |
| 4 | Service | `internal/<domain>/` | review |
| 5 | Handler + operation | `internal/api/` | architectural tests |
| 6 | `make gen` again | — | spec-drift gate |
| 7 | Tests | `test/integration/` | CI |
| 8 | Docs + changelog | `docs/`, `docs/api-changelog.md` | docs-sync check |
| 9 | Verify | — | `make check` |

---

## 1. Schema — `db/schema.hcl`

The single source of schema truth. Atlas generates the migration; you never hand-write one except
for the four allowlisted cases (see [`.claude/rules/migrations.md`](../../.claude/rules/migrations.md)).

```hcl
table "guild" {
  schema = schema.main
  column "id"               { type = integer } # singleton; see 01-domain-model §2
  column "name"             { type = text }
  column "tag"              { type = text }    # NOT NULL, defaults to ''
  column "points_label"     { type = text }    # EQdkp's dkp_name — guilds rename "DKP"
  column "timezone"         { type = text }    # IANA name; renders the UI and buckets every *_day
  column "points_precision" { type = integer } # DISPLAY rounding only; storage is always _cp
  column "created_at"       { type = integer } # Micros
  column "updated_at"       { type = integer }
  primary_key { columns = [column.id] }
  check "guild_singleton" { expr = "id = 1" }
  # STRICT: only INT/INTEGER/REAL/TEXT/BLOB/ANY are legal. BIGINT is not.
  strict = true
}
```

Then `make migration NAME=add_guild`, review the generated SQL, and commit it.

## 2. Query — `db/queries/guild.sql`

Raw SQL lives here and nowhere else. Check `db/RECIPES.md` for an existing shape before inventing one.

```sql
-- name: GetGuild :one
SELECT id, name, tag, points_label, timezone, points_precision, created_at, updated_at
FROM guild
LIMIT 1;

-- name: UpdateGuild :one
UPDATE guild
SET name = ?, tag = ?, points_label = ?, timezone = ?, points_precision = ?, updated_at = ?
WHERE id = ?
RETURNING id, name, tag, points_label, timezone, points_precision, created_at, updated_at;
```

## 3. `make gen`

Regenerates `internal/store/sqlitegen/`, compiles the `internal/store/pggen/` target, and fails if
the hand-written `Queries` interface no longer matches. When it does, add the method:

```go
// internal/store/store.go — hand-written, the contract both dialects satisfy.
type Queries interface {
    GetGuild(ctx context.Context) (sqlitegen.Guild, error)
    UpdateGuild(ctx context.Context, arg sqlitegen.UpdateGuildParams) (sqlitegen.Guild, error)
    // ...
}

var _ Queries = (*sqlitegen.Queries)(nil)
var _ Queries = (*pggen.Queries)(nil) // CI-only build tag; compile-time Postgres proof
```

Those two assertions are the entire mechanism keeping the Postgres port cheap. They cost nothing and
`go build` checks them on every save.

## 4. Service — `internal/guild/service.go`

Domain logic lives in the owning package, never in the handler. Handlers marshal; services decide.

```go
package guild

type Service struct {
    store store.Store
    clock clock.Clock // injected — never time.Now()
}

func (s *Service) Get(ctx context.Context) (Guild, error) {
    g, err := s.store.Q().GetGuild(ctx)
    if err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return Guild{}, fmt.Errorf("guild not initialised: %w", ErrNotFound)
        }
        return Guild{}, fmt.Errorf("get guild: %w", err) // always wrap with %w and context
    }
    return fromRow(g), nil
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (Guild, error) {
    var out Guild
    // Every mutation goes through store.Tx, which uses the single-writer pool.
    err := s.store.Tx(ctx, func(q store.Queries) error {
        cur, err := q.GetGuild(ctx)
        if err != nil {
            return fmt.Errorf("load guild: %w", err)
        }
        if in.IfMatch != etagOf(cur) {
            return ErrConflict // surfaces as 412
        }
        row, err := q.UpdateGuild(ctx, in.toParams(cur, s.clock.Now()))
        if err != nil {
            return fmt.Errorf("update guild: %w", err)
        }
        out = fromRow(row)
        return nil
    })
    return out, err
}
```

## 5. Handler and operation — `internal/api/guild.go`

**One file per resource.** Never a shared registry file — that conflicts on every parallel feature PR.

Routes may be declared only under `internal/api`; an architectural test enumerates the Huma registry
against a package scan and fails otherwise.

```go
package api

func registerGuild(api huma.API, svc *guild.Service) {
    huma.Register(api, huma.Operation{
        OperationID: "getGuild", // EXPLICIT, lowerCamelCase. The SDK method name derives from
                                 // it, so it is PUBLIC API and must never be renamed.
        Method:      http.MethodGet,
        Path:        "/api/v1/guild",
        Summary:     "Get guild settings",
        Tags:        []string{"guild"},
        Security: []map[string][]string{
            {"pat": {"roster:read"}},
            {"session": {}},
        },
        Metadata:      map[string]any{"x-dkp-permission": "roster.read"},
        DefaultStatus: http.StatusOK,
        Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
    }, func(ctx context.Context, _ *struct{}) (*GuildOutput, error) {
        g, err := svc.Get(ctx)
        if err != nil {
            return nil, problem.From(err) // maps sentinels to Huma error types.
        }                                 // NEVER write to http.ResponseWriter.
        return &GuildOutput{Body: toDTO(g), ETag: etagOf(g)}, nil
    })

    huma.Register(api, huma.Operation{
        OperationID:   "updateGuild",
        Method:        http.MethodPatch,
        Path:          "/api/v1/guild",
        Summary:       "Update guild settings",
        Tags:          []string{"guild"},
        // No `pat` alternative: `admin.settings` operations are session-only. There is no
        // `admin:*` scope and no `admin:settings` scope — inventing one is canonical §6's
        // single biggest prohibition. See docs/design/02-api-design.md §4.2.
        Security: []map[string][]string{{"session": {}}},
        Metadata: map[string]any{
            "x-dkp-permission":      "admin.settings",
            "x-dkp-pat-forbidden":   true,
        },
        DefaultStatus: http.StatusOK,
        Errors: []int{http.StatusBadRequest, http.StatusForbidden,
            http.StatusPreconditionFailed, http.StatusUnprocessableEntity},
    }, func(ctx context.Context, in *UpdateGuildInput) (*GuildOutput, error) {
        g, err := svc.Update(ctx, in.toInput())
        if err != nil {
            return nil, problem.From(err)
        }
        return &GuildOutput{Body: toDTO(g), ETag: etagOf(g)}, nil
    })
}

type GuildOutput struct {
    ETag string   `header:"ETag"`
    Body GuildDTO
}

type UpdateGuildInput struct {
    // If-Match is REQUIRED on PATCH of a mutable resource, so two officers editing
    // settings race deterministically instead of both succeeding.
    IfMatch string `header:"If-Match" required:"true"`
    Body    struct {
        Name            *string `json:"name,omitempty"         minLength:"1" maxLength:"64"`
        Tag             *string `json:"tag,omitempty"          maxLength:"8"`
        PointsLabel     *string `json:"points_label,omitempty" maxLength:"24"`
        Timezone        *string `json:"timezone,omitempty"`
        PointsPrecision *int    `json:"points_precision,omitempty" minimum:"0" maximum:"2"`
    }
}
```

Validation, the RFC 9457 `application/problem+json` error shape, and the OpenAPI entry all fall out
of that one declaration. **The spec is derived from these types**, which is why it cannot drift.

### What the architectural tests will reject

| Omission | Test |
|---|---|
| Missing or duplicate `OperationID` | `arch_test.go` uniqueness + non-empty |
| Missing `Security` | `arch_test.go` security coverage |
| Missing `x-dkp-permission` | `arch_test.go` permission coverage |
| A mutating `POST` under `/raids`, `/awards`, `/adjustments`, `/bid-sessions`, `/raid-submissions`, `/ledger` without a required `Idempotency-Key` | `arch_test.go` idempotency coverage |
| `Hidden: true` outside `api.HiddenOperationAllowlist()` | `arch_test.go` no-hidden-operations |
| A bespoke list envelope instead of the shared cursor helper | `arch_test.go` envelope shape |
| A route declared outside `internal/api` | `arch_test.go` package scan vs registry |

## 6. `make gen` again

Rewrites `openapi/openapi.json` and regenerates `clients/ts` and `clients/python`. **Commit those
diffs; never hand-edit them.** CI regenerates and fails on any difference.

If `oasdiff` reports a breaking change, you need the `!breaking-api` label and a line in
`docs/api-changelog.md` — and a human decision.

## 7. Tests — `test/integration/guild_test.go`

Against the real server and a real SQLite database in `t.TempDir()`. No mocks; there is no fake
`Queries` implementation and a lint rule forbids adding one.

```go
func TestUpdateGuild_StaleIfMatch_Returns412(t *testing.T) {
    env := testenv.New(t)                    // migrated DB + httptest server, ~25ms
    c := env.ClientAs(t, testenv.RoleOfficer)

    got, err := c.GetGuild(env.Ctx)
    require.NoError(t, err)

    _, err = c.UpdateGuild(env.Ctx, api.UpdateGuildInput{
        IfMatch: "\"stale-etag\"",
        Body:    api.UpdateGuildBody{Name: ptr("Kittens")},
    })

    var pd *problem.Detail
    require.ErrorAs(t, err, &pd)
    require.Equal(t, http.StatusPreconditionFailed, pd.Status)
    require.Equal(t, "precondition_failed", pd.Code)
    require.Equal(t, got.Name, pd.Meta["current"].(map[string]any)["name"],
        "412 must return the current representation so a bot can merge")
}
```

Write these four, in this order:

1. **Integration** against the real server. The response-validation middleware checks every response
   against its declared schema automatically, so a shape mistake fails here.
2. **PAT parity** — if the SPA will call it, add a case replaying the same request with a scoped PAT
   and asserting an identical response. If the browser can do something a bot cannot, CI goes red.
3. **Idempotency replay** — if it is a mutating POST, assert 100 concurrent identical requests
   produce one effect and 99 replays.
4. **Statement-count budget** — if it reads a collection, declare a budget. This is the N+1 tripwire.

Use `require`, never `assert`: `assert` continues after failure and produces cascading noise.

## 8. Docs and changelog

Add or update the endpoint's docs page and add a line to `docs/api-changelog.md`. A behaviour change
without a docs change fails the docs-sync check.

## 9. Verify

```bash
.claude/skills/add-endpoint/scripts/verify.sh   # arch tests, spec drift, oasdiff, PAT parity
make check
```

---

## Stop and ask if

- The endpoint would need a **new permission key** — the catalogue is generated and FK-constrained,
  so adding one is a schema change with a boot-failure blast radius.
- The operation does not fit REST plus action sub-resources. A state machine gets
  `POST /resource/{id}/close`, not `PATCH {state: "closing"}` — the latter lies about what happens.
- The response would embed a document that SSE also carries. There must be exactly one
  representation of a resource; SSE frames carry ids, never documents.
- You cannot make it idempotent.
- **The UI needs it but a bot would not.** That is not a real distinction here. There are no
  UI-private endpoints, and three CI gates exist to prove it.
