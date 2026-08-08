# Example endpoint, end to end

Copy this file's shape before writing a new endpoint. Its acceptance criterion is that an agent given
only this file and [`db/RECIPES.md`](../../db/RECIPES.md) can add an endpoint with no further
questions. If you had to ask one, that is a bug in this file — fix it here.

Worked example: `GET /api/v1/guild` and `PATCH /api/v1/guild`, the singleton guild-settings resource.
**Every excerpt below is transcribed from the code that ships in this repository**, and
`TestDocs_ExampleEndpointSnippets_Compile` (`internal/api/docs_snippets_test.go`) extracts each fenced
block and proves it still compiles — Go blocks `go build`, SQL blocks pass `sqlc`, HCL blocks pass
`atlas schema inspect`. A snippet that drifts from the code fails CI, so what you read here is what the
code is, not what it was.

> **Huma v2, not v1.** `huma.Register` is the v2 API and `humago.New` is the adapter this repo mounts
> (`internal/api/server.go`). `huma.Resource`, `huma.NewRouter`, `huma.Operation{Handler: ...}` and
> `humachi.New` are **Huma v1 or the wrong adapter and do not exist here**. This is the single most
> common hallucination in this codebase. If you are writing one of those, stop.
>
> You never call the adapter yourself either: `api.New(api.Config{...})` builds the whole tree
> (routes, Huma mount, middleware) and `registerOperations` is where a new resource is wired in.

---

## The nine steps, in order

Later steps consume generated output from earlier ones. Do not reorder.

| # | Step | File | Gate that catches you skipping it |
|---|---|---|---|
| 1 | Schema | `db/schema.hcl` | `make verify-generated` |
| 2 | Query | `db/queries/*.sql` | `sqlc` compile error |
| 3 | `make gen` | — | `store.Queries` interface unsatisfied → build error |
| 4 | Service | `internal/<domain>/` | review |
| 5 | Handler + register | `internal/api/` | `arch_test.go` |
| 6 | `make gen` again | — | spec-drift gate |
| 7 | Tests | `test/integration/`, `internal/api/` | CI |
| 8 | Docs + changelog | `docs/`, `docs/api-changelog.md` | docs-sync check |
| 9 | Verify | — | `make check` |

---

## 1. Schema — `db/schema.hcl`

The single source of schema truth. Atlas generates the migration; you never hand-write one except for
the allowlisted cases (see [`.claude/rules/migrations.md`](../../.claude/rules/migrations.md)). This is
the `guild` block as it ships — twelve columns, the singleton `CHECK (id = 1)`, and `strict = true`
because under STRICT only `INT`/`INTEGER`/`REAL`/`TEXT`/`BLOB`/`ANY` are legal (no `BIGINT`, no
`BOOLEAN` — booleans are `INTEGER` `0`/`1`).

```hcl
table "guild" {
  schema = schema.main

  // The singleton key. INTEGER, not a ULID: there is exactly one guild, so the id is 1, always.
  column "id" {
    null = false
    type = integer
  }

  column "name" {
    null = false
    type = text
  }

  column "tag" {
    null    = false
    type    = text
    default = ""
  }

  column "timezone" {
    null    = false
    type    = text
    default = "UTC"
  }

  column "week_start" {
    null    = false
    type    = integer
    default = 1
  }

  column "points_label" {
    null    = false
    type    = text
    default = "DKP"
  }

  column "points_precision" {
    null    = false
    type    = integer
    default = 2
  }

  // Nullable, because NULL is meaningful: it is the "off" state of the inactivity sweep, distinct
  // from 0 ("flag after zero days"). This is the repo's first nullable column.
  column "inactive_after_days" {
    null = true
    type = integer
  }

  // Boolean stored as 0/1 — SQLite has no BOOLEAN under STRICT.
  column "auto_set_inactive" {
    null    = false
    type    = integer
    default = 0
  }

  column "hide_inactive" {
    null    = false
    type    = integer
    default = 0
  }

  // Micros (int64 Unix microseconds), never a timestamptz — the whole schema is dialect-identical.
  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // The singleton constraint. A second guild row fails this at INSERT.
  check "guild_is_singleton" {
    expr = "id = 1"
  }

  check "guild_week_start_range" {
    expr = "week_start BETWEEN 0 AND 6"
  }

  check "guild_points_precision_range" {
    expr = "points_precision BETWEEN 0 AND 2"
  }

  check "guild_auto_set_inactive_bool" {
    expr = "auto_set_inactive IN (0, 1)"
  }

  check "guild_hide_inactive_bool" {
    expr = "hide_inactive IN (0, 1)"
  }

  strict = true
}
```

Then `make migration NAME=<snake_case>`, review the generated SQL under `db/migrations-sqlite/`, and
commit it. **The freeze rule cuts one way:** `ALTER TABLE ADD COLUMN` is a cheap forward migration,
while dropping or retyping a column is SQLite's 12-step rebuild — so under-ship, and add a column when
a reader for it exists, rather than over-shipping columns nothing reads.

## 2. Query — `db/queries/guild.sql`

Raw SQL lives here and nowhere else (`db.Query`/`db.Exec` outside `internal/store` are grep-banned,
gate SQL002). Check [`db/RECIPES.md`](../../db/RECIPES.md) for an existing shape before inventing one.
`GetGuild` is the singleton fetch; `UpdateGuild` writes every settable column and `RETURNING`s the new
row so the handler emits the fresh representation and its new ETag in one round trip.

```sql
-- name: GetGuild :one
SELECT
    id, name, tag, timezone, week_start, points_label, points_precision,
    inactive_after_days, auto_set_inactive, hide_inactive, created_at, updated_at
FROM guild;

-- name: UpdateGuild :one
UPDATE guild SET
    name                = ?,
    tag                 = ?,
    timezone            = ?,
    week_start          = ?,
    points_label        = ?,
    points_precision    = ?,
    inactive_after_days = ?,
    auto_set_inactive   = ?,
    hide_inactive       = ?,
    updated_at          = ?
WHERE id = 1
RETURNING
    id, name, tag, timezone, week_start, points_label, points_precision,
    inactive_after_days, auto_set_inactive, hide_inactive, created_at, updated_at;
```

`UpdateGuild` is a **whole-row write**, not a `COALESCE`-per-column partial one: merging the PATCH onto
the current row is domain logic and lives in the service (step 4), where it is unit-testable without a
database. It names `WHERE id = 1` explicitly so the `UPDATE` cannot touch more than the one row even if
the `CHECK` were ever weakened.

## 3. `make gen`

Regenerates `internal/store/sqlitegen/` from the query file, re-hashes the migration directory, and
asserts `db/schema.hcl` and the migrations still describe the same schema. It fails if the hand-written
`store.Queries` interface no longer matches — a new query needs its method added by hand, in the same
change as the query:

```go
package storesnippet

import (
	"context"

	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// Queries is the contract both dialects satisfy — one method per query, added in the same change as
// the query. It is copied here verbatim from internal/store/meta.go; the real one lives there.
type Queries interface {
	GetGuild(ctx context.Context) (sqlitegen.Guild, error)
	InsertGuild(ctx context.Context, arg sqlitegen.InsertGuildParams) (sqlitegen.Guild, error)
	UpdateGuild(ctx context.Context, arg sqlitegen.UpdateGuildParams) (sqlitegen.Guild, error)
	GetMetaValue(ctx context.Context, key string) (string, error)
	UpsertMetaValue(ctx context.Context, arg sqlitegen.UpsertMetaValueParams) error
}

// The compile-time proof, in the real store package. It costs nothing and `go build` checks it on
// every save. The pggen half — `var _ Queries = (*pggen.Queries)(nil)` under a CI-only build tag —
// arrives with the Postgres target after 1.0.
var _ Queries = (*sqlitegen.Queries)(nil)
```

That assertion is the entire mechanism keeping the Postgres port cheap. It costs nothing and
`go build` checks it on every save.

## 4. Service — `internal/guild/service.go`

Domain logic lives in the owning package, never in the handler. **Handlers marshal; services decide.**
A read (`GET`) runs through `store.Q()` on the read pool — one statement, no transaction. A mutation
runs the whole read-modify-write inside one `store.Tx` on the single-writer pool, so no concurrent
PATCH can interleave between the If-Match check and the write. The service holds a store and an
injected clock and nothing else — `time.Now` is grep-banned outside `internal/clock` (gate CLOCK001).

```go
package guildsnippet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// Sentinel errors in the owning package. Callers compare with errors.Is, and internal/api maps each
// to a status: ErrNotFound → 404, ErrPreconditionFailed → 412, ErrNoStore → 503.
var (
	ErrNotFound           = errors.New("guild not found")
	ErrPreconditionFailed = errors.New("guild precondition failed")
	ErrNoStore            = errors.New("guild service has no database")
)

// Guild is the ONE exported shape of "the guild" in the domain. internal/api maps it to a separate
// wire GuildDTO — the two are different concepts. (Trimmed here to the fields the excerpt uses; the
// real struct in internal/guild carries all twelve.)
type Guild struct {
	Name    string
	IfMatch string
}

// Service reads and updates the guild singleton. Store and clock, nothing else.
type Service struct {
	store *store.Store
	clock clock.Clock
}

// Get reads through Q() on the read pool: one statement, no transaction, because a GET is not a
// mutation. A missing row (sql.ErrNoRows) becomes ErrNotFound — the never-seeded case, a 404 not a
// 500. Every error is wrapped with %w and context.
func (s *Service) Get(ctx context.Context) (Guild, error) {
	if s.store == nil {
		return Guild{}, fmt.Errorf("get guild: %w", ErrNoStore)
	}

	row, err := s.store.Q().GetGuild(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Guild{}, fmt.Errorf("get guild: %w", ErrNotFound)
		}

		return Guild{}, fmt.Errorf("get guild: %w", err)
	}

	return Guild{Name: row.Name}, nil
}

// Update runs the read-modify-write in one store.Tx. store.Tx takes a callback of
// func(context.Context, store.Queries) error — a domain package never sees a *sql.Tx. On an If-Match
// mismatch it returns ErrPreconditionFailed carrying the CURRENT representation, so the handler puts
// it in meta.current and a bot merges in one round trip.
func (s *Service) Update(ctx context.Context, in Guild) (Guild, error) {
	if s.store == nil {
		return Guild{}, fmt.Errorf("update guild: %w", ErrNoStore)
	}

	var out Guild

	err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		row, err := q.GetGuild(ctx)
		if err != nil {
			return fmt.Errorf("load guild: %w", err)
		}

		cur := Guild{Name: row.Name}
		if in.IfMatch != etagOf(cur) {
			out = cur // carried up so the handler can put it in meta.current

			return fmt.Errorf("if-match mismatch: %w", ErrPreconditionFailed)
		}

		out = cur

		return nil
	})
	if err != nil {
		return out, err
	}

	return out, nil
}

// etagOf stands in for guild.ETagOf: a strong ETag over a deterministic encoding of every field.
func etagOf(g Guild) string { return `"` + g.Name + `"` }
```

## 5. Handler and register — `internal/api/guild.go`

**One file per resource.** Never a shared registry file — that conflicts on every parallel feature PR.
Routes may be declared only under `internal/api`; `arch_test.go` enumerates the Huma registry against a
package scan and fails otherwise. The handler makes no domain decision: it marshals the request into a
service call, marshals the result back, and translates the service's sentinel errors into the closed
error enum with `api.NewProblem(status, code, detail)`. It never writes to `http.ResponseWriter`.

Note the four things `arch_test.go` and `make verify-spec` check: an explicit lowerCamelCase
`OperationID` (the SDK method name derives from it — **it is public API and must never be renamed**), a
`Security` requirement, an `x-dkp-permission` in `Extensions`, and — for a PATCH — the If-Match
precondition handling from step 5a below.

```go
package apisnippet

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// GuildDTO is the wire representation — a SEPARATE type from the domain guild.Guild. snake_case field
// names and the timestamp formatting live here. (Trimmed to two fields; the real DTO carries twelve.)
type GuildDTO struct {
	Name string `json:"name" doc:"The guild's display name"`
	Tag  string `json:"tag"  doc:"The <Guild Tag> as it appears in /who"`
}

// GuildOutput is the response envelope: the DTO body plus the strong ETag a client stores and sends
// back as If-Match on a PATCH.
type GuildOutput struct {
	ETag string `header:"ETag"`
	Body GuildDTO
}

// UpdateGuildInput is the PATCH request. If-Match is declared OPTIONAL — see step 5a for why a
// required tag would yield 422 instead of 428. Every body field is a pointer with omitempty so
// "absent, leave unchanged" is distinguishable from "set to the zero value".
type UpdateGuildInput struct {
	IfMatch string `header:"If-Match" doc:"The ETag from a prior GET. Required; its absence is a 428."`

	Body struct {
		Name *string `json:"name,omitempty" minLength:"1" maxLength:"64" doc:"The guild's display name"`
		Tag  *string `json:"tag,omitempty"  maxLength:"8"                doc:"The <Guild Tag>"`
	}
}

// registerGuild declares GET and PATCH /api/v1/guild. It is called from registerOperations so the
// served document and the emitted document are the same document.
func registerGuild(a huma.API) {
	huma.Register(a, huma.Operation{
		// EXPLICIT, lowerCamelCase. The SDK method name derives from it, so it is PUBLIC API.
		OperationID: "getGuild",
		Method:      http.MethodGet,
		Path:        api.BasePath + "/guild",
		Summary:     "Get guild settings",
		Description: "Returns the single guild's identity and officer-editable settings, with a " +
			"strong ETag a client stores and sends back as If-Match on a PATCH.",
		Tags: []string{"guild"},
		// PAT-callable: a bot with roster:read may read. The session alternative carries no scope.
		// x-dkp-scopes is non-empty and every member resolves in the authz catalogue — the
		// "PAT-callable" case of the three-case scope rule.
		Security: []map[string][]string{
			{"pat": {"roster:read"}},
			{"session": {}},
		},
		// Extensions, NOT Metadata: huma.Operation.Metadata is tagged `yaml:"-"` and never reaches the
		// OpenAPI document, so a permission declared there emits a spec with no x-dkp-permission and
		// fails both arch_test.go and `make verify-spec`.
		Extensions: map[string]any{
			api.ExtensionPermission: "roster.read",
			api.ExtensionScopes:     []string{"roster:read"},
		},
		DefaultStatus: http.StatusOK,
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusServiceUnavailable,
		},
	}, func(ctx context.Context, _ *struct{}) (*GuildOutput, error) {
		// The real handler calls svc.Get(ctx) and maps sentinels with api.NewProblem; see step 4.
		return &GuildOutput{ETag: `"etag"`, Body: GuildDTO{Name: "example"}}, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "updateGuild",
		Method:      http.MethodPatch,
		Path:        api.BasePath + "/guild",
		Summary:     "Update guild settings",
		Description: "Applies a partial update to the guild under an If-Match precondition. A missing " +
			"If-Match is 428; a stale one is 412 with the current representation in meta.current.",
		Tags: []string{"guild"},
		// Session-only, and NOT PAT-forbidden. admin.settings is session-only because no PAT scope
		// family covers instance configuration — NOT because it alters authentication state, which is
		// what x-dkp-pat-forbidden would assert. So this declares NEITHER x-dkp-scopes NOR
		// x-dkp-pat-forbidden: the "session-only by omission" case of the three-case scope rule.
		// Marking admin.settings PAT-forbidden is a false positive the arch test rejects.
		Security: []map[string][]string{
			{"session": {}},
		},
		Extensions: map[string]any{
			api.ExtensionPermission: "admin.settings",
		},
		DefaultStatus: http.StatusOK,
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusPreconditionRequired, http.StatusPreconditionFailed,
			http.StatusUnprocessableEntity, http.StatusServiceUnavailable,
		},
	}, func(ctx context.Context, in *UpdateGuildInput) (*GuildOutput, error) {
		// A missing If-Match is a 428, checked explicitly in the handler — see step 5a.
		if in.IfMatch == "" {
			return nil, api.NewProblem(http.StatusPreconditionRequired, api.CodePreconditionRequired,
				"If-Match is required on a PATCH of this resource.")
		}

		return &GuildOutput{ETag: `"etag"`, Body: GuildDTO{Name: "example"}}, nil
	})
}
```

Validation, the RFC 9457 `application/problem+json` error shape, and the OpenAPI entry all fall out of
that one declaration. **The spec is derived from these types**, which is why it cannot drift.

### 5a. If-Match: optional tag, explicit 428 — do not "simplify" it

`If-Match` MUST be declared **optional** (no `required:"true"`), with the presence check in the
handler. Huma v2.39.1 raises a **missing required parameter as 422** (`errStatus` initialised to
`http.StatusUnprocessableEntity`), so a `required:"true"` If-Match would answer a missing precondition
with `422 validation_failed` instead of the `428 precondition_required` canonical §7 requires. A test
asserts the status **and** the code, because a 422 would otherwise look like a passing negative test.
`api.NewProblem` (**not** `problem.From` — no such function exists) builds the body:

```go
package etagsnippet

import (
	"net/http"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// requireIfMatch returns the caller's If-Match, or a 428 precondition_required problem when absent.
// The check lives here, not in a required:"true" tag, because Huma turns a missing required
// parameter into 422 — see the note above. api.NewProblem(status, code, detail) is the ONLY way to
// build a problem body; there is no problem.From.
func requireIfMatch(ifMatch string) (string, *api.ProblemDetail) {
	if strings.TrimSpace(ifMatch) == "" {
		return "", api.NewProblem(http.StatusPreconditionRequired, api.CodePreconditionRequired,
			"If-Match is required on a PATCH of this resource. Send the ETag from a prior GET so a "+
				"concurrent edit is detected rather than silently overwritten.")
	}

	return ifMatch, nil
}
```

On a **stale** If-Match the service returns the current representation, and the 412 body carries it in
`meta.current` with its ETag in `meta.current_etag`, so a bot merges in one round trip:

```go
package preconditionsnippet

import (
	"net/http"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// preconditionFailed builds the 412 body. meta.current is the current wire DTO; meta.current_etag is
// its strong ETag — byte-identical to what a fresh GET would return, so it is the caller's next
// If-Match.
func preconditionFailed(current any, currentETag string) *api.ProblemDetail {
	p := api.NewProblem(http.StatusPreconditionFailed, api.CodePreconditionFailed,
		"If-Match does not match the current ETag. The resource changed since you last read it; "+
			"merge your change onto meta.current and retry with meta.current_etag.")
	p.Meta = map[string]any{
		"current":      current,
		"current_etag": currentETag,
	}

	return p
}
```

### What the architectural tests will reject

| Omission | Test |
|---|---|
| Missing or duplicate `OperationID` | `arch_test.go` uniqueness + non-empty |
| Missing `Security` | `arch_test.go` security coverage |
| Missing `x-dkp-permission` | `arch_test.go` permission coverage |
| A PATCH/transition with no `If-Match` header parameter | `TestArch_StateChangingOperation_RequiresIfMatch` |
| `x-dkp-scopes` that names a scope not in `authz.Catalogue()`, or on a PAT-forbidden op | `TestArch_ScopeCoverage_MatchesSecurity` |
| `x-dkp-pat-forbidden: true` on an op not in `authz.CapabilityFloor()` (e.g. `admin.settings`) | `TestArch_ScopeCoverage_MatchesSecurity` |
| A mutating `POST` creating domain state without a required `Idempotency-Key` | `arch_test.go` idempotency coverage |
| `Hidden: true` outside `api.HiddenOperationAllowlist()` | `arch_test.go` no-hidden-operations |
| A route declared outside `internal/api` | `arch_test.go` package scan vs registry |

## 6. `make gen` again

Rewrites `openapi/openapi.json`. **Commit that diff; never hand-edit it.** CI regenerates and fails on
any difference (`make verify-generated`, `make verify-spec`). The `getGuild` fragment it produces:

```json
{
  "get": {
    "operationId": "getGuild",
    "summary": "Get guild settings",
    "tags": ["guild"],
    "security": [
      { "pat": ["roster:read"] },
      { "session": [] }
    ],
    "x-dkp-permission": "roster.read",
    "x-dkp-scopes": ["roster:read"],
    "responses": {
      "200": {
        "description": "OK",
        "headers": { "ETag": { "schema": { "type": "string" } } },
        "content": {
          "application/json": { "schema": { "$ref": "#/components/schemas/GuildDTO" } }
        }
      }
    }
  }
}
```

<!-- PENDING PR 6: this step also regenerates the TypeScript client (`clients/ts`) via
openapi-typescript. That call lands with the SPA and the SDKs in Phase 0 PR 6, which removes this
marker. Until then `make gen` writes only the migrations, sqlc output and openapi.json. -->

If a future `oasdiff` reports a breaking change, you need the `!breaking-api` label and a line in
`docs/api-changelog.md` — and a human decision.

## 7. Tests — `internal/api/guild_test.go` and `test/integration/guild_test.go`

Against the real server and a real SQLite database in `t.TempDir()`. **No mocks**: there is no fake
`store.Queries` implementation and a lint rule forbids adding one. The harness is `store.NewDB(t)` +
`httptest.NewServer(api.New(...))` over a `TestMain` that calls
`store.InitTemplate(ctx, store.ApplySchema(fsys))`. **There is no `testenv` package and no `ClientAs`,
because there is no auth package until ROADMAP Phase 2 — there is no such thing as an officer to be.**

```go
package guildtestsnippet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// A two-line fixed clock stands in until internal/clock ships a Fixed helper in Phase 0 PR 8, so a
// PATCH stamps updated_at deterministically. time.Now is grep-banned outside internal/clock.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC) }

var _ clock.Clock = fixedClock{}

// newServer clones the template, and starts a real HTTP server over it. store.NewDB gives every test
// its own file, so every test is t.Parallel(). It returns the store too, so a test can declare a
// statement budget. No testenv, no generated client — PR 6 fills in the client.
func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()

	s := store.NewDB(t)
	// A real test seeds the guild row here via s.Tx(...) + q.InsertGuild(...).

	srv := httptest.NewServer(api.New(api.Config{Store: s, Clock: fixedClock{}}))
	t.Cleanup(srv.Close)

	return srv, s
}

// TestGetGuild_Singleton_ReturnsETag reads the guild at a statement budget of ONE. The budget is
// declared AFTER the server is built, so boot statements are excluded; a singleton read is one
// statement and an N+1 fails here. This is the N+1 tripwire, and it already works in Phase 0.
func TestGetGuild_Singleton_ReturnsETag(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)

	store.Counted(t).Budget(t, 1)

	res, err := http.Get(srv.URL + "/api/v1/guild") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get guild: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if res.Header.Get("ETag") == "" {
		t.Fatal("a mutable resource must carry an ETag")
	}
	_ = context.Background()
}
```

Write these, in this order:

1. **Handler-level and integration** against the real server. Assert status **and** `code` on error
   paths — for a missing If-Match, `http.StatusPreconditionRequired` **and**
   `api.CodePreconditionRequired`, so a 422 cannot masquerade as a passing negative test. Decode error
   bodies as `api.ProblemDetail` (**not** `problem.Detail`).
2. **Positive control.** For a PATCH, assert the current-If-Match path succeeds and returns a
   **different** ETag — without it the negative tests pass against an endpoint that always fails.
3. **Statement-count budget** — if it reads a collection, declare `store.Counted(t).Budget(t, n)`
   after the server is built. This is the N+1 tripwire.
4. **PAT parity** — from Phase 2, when tokens exist. Not applicable in Phase 0; there is no principal
   to replay a request as yet.

> **There is no response-validation middleware yet.** The validator choice is an open Phase 0 item
> (`docs/design/04-testing.md` §"Open item"); the earlier claim that a middleware "checks every
> response against its declared schema automatically" was false and is deleted. Until a validator
> lands, **assert the response body explicitly** in each test.

Use `require`, never `assert`: `assert` continues after failure and produces cascading noise. (The
excerpt above uses plain `t.Fatalf` only so it compiles standalone without importing testify; real
tests use `require`.)

## 8. Docs and changelog

Add or update the endpoint's docs page and add a line to `docs/api-changelog.md`. A behaviour change
without a docs change fails the docs-sync check.

## 9. Verify

```bash
make check
```

`make check` runs the repo gates, the licence gate, golangci-lint, `go build`/`go vet`, and the whole
test suite — including `TestDocs_ExampleEndpointSnippets_Compile`, which re-extracts every fence in
this file and rebuilds it, so a copy-paste that drifts from the code fails here.

---

## Stop and ask if

- The endpoint would need a **new permission key** — the catalogue (`internal/authz/catalogue.go`) is
  the canonical §6 list and SPEC005 greps it; adding one is a spec change with SDK and Phase 2
  seed blast radius.
- The operation does not fit REST plus action sub-resources. A state machine gets
  `POST /resource/{id}/close`, not `PATCH {state: "closing"}` — the latter lies about what happens.
- The response would embed a document that SSE also carries. There must be exactly one representation
  of a resource; SSE frames carry ids, never documents.
- You cannot make a mutating POST idempotent.
- **The UI needs it but a bot would not.** That is not a real distinction here. There are no
  UI-private endpoints, and CI gates exist to prove it.
