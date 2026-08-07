# Phase 0 PR 5 — decision record

**Status:** decided (Courtney, 2026-08-07). **Audience:** owner, contributor, agent.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`.

Three questions blocked Phase 0 PR 5: what `internal/authz` must contain to satisfy PR 4's SPEC005
tripwire, what the first integration test can be given that no auth package exists until Phase 2,
and whether the 2,500-line budget describes this codebase after three consecutive overruns.

Everything below is evidence-backed against the tree at `be1aa0a`. Where a claim rests on a
measurement rather than a citation, the measurement is printed.

**All six open items were resolved in the same session** (Courtney, 2026-08-07); the resolutions are
in **Resolved opens** near the bottom, and the two that changed a normative document —
`admin.security.manage` and the capability-floor enumeration — are recorded in canonical §6 itself.

**Two reversals of my own analysis are recorded rather than quietly folded in.** I twice called
`EXAMPLE_ENDPOINT.md:191`'s `x-dkp-pat-forbidden: true` a defect before finding
`docs/design/03-security.md:795-797`, which relies on that surface being step-up so a leaked token
cannot relax the SSRF allowlist and pivot. The declaration was defensible; the *key* was overloaded.
And I had the migration-freeze risk backwards — `ALTER TABLE ADD COLUMN` is cheap and forward-only,
so the freeze rule argues against over-shipping columns, not for it. Both are in §Q1 and §Q3.

---

## Q1 — What `internal/authz` contains at PR 5

### Decision: a Go catalogue only, holding the whole canonical §6 key list. No DB tables, no seed, no boot reconciliation.

PR 5a adds `internal/authz/{doc.go,catalogue.go,catalogue_test.go}` and nothing else in that tree.

### The direction is settled: the catalogue is the SOURCE, not an output

Five documents say the same thing, and none says it is generated from anything:

- `docs/design/00-canonical-conventions.md:87` — "There is exactly one source:
  `internal/authz/catalogue.go`. It **generates** the `permission` table seed, the OpenAPI
  `x-dkp-permission` metadata, the PAT scope enum, the authorization-matrix header, and
  `docs/reference/permissions.md`."
- `docs/design/03-security.md:383-386` — verbatim the same sentence.
- `docs/api/auth-and-scopes.md:62-65` — same.
- `docs/design/02-api-design.md:123-125` — "Both columns draw from **one generated catalogue** …
  The permission keys in canonical §6 are the whole 1.0 catalogue; this document invents none."
- `docs/design/01-domain-model.md:364` — `CREATE TABLE permission ( -- reconciled from
  internal/authz/catalogue.go on every boot`.

Hand-written Go → DB seed, OpenAPI metadata, scope enum, docs page. "Generated" in canonical §6
describes what the catalogue *produces*, never what produces it. The `internal/authz/**` path filter
in `.github/workflows/ci.yml:126` selects the `api` job, so CI already treats it as source rather
than as generated output.

`docs/README.md:161` puts `reference/permissions.md` and `reference/scopes.md` in **phase 2**.
`ROADMAP.md:186-190` puts the whole reconciliation stack — catalogue → `permission` table, plus
`role`, `role_permission`, `role_assignment`, built-in roles and `admin.owner` — in **Phase 2
deliverable 3**, bundled with sessions, PATs and MFA. Nothing in
[`first-ten-prs.md`](first-ten-prs.md) touches auth or authz at all.

### Why the boot-failure argument does not apply at PR 5

`.claude/rules/api-endpoints.md:68-71`, `.claude/skills/add-endpoint/SKILL.md:171-174`,
`docs/adr/0011-opaque-pats-no-superadmin-token.md:48` and `internal/api/permissions.go:33-36` all
justify "stop and ask" with the same mechanism: `role_permission` is FK-constrained to
`permission(key)`, so a divergent key is a boot failure.

**That FK does not exist.** `db/schema.hcl` contains exactly one table, `dkp_meta`
(`db/schema.hcl:39-71`). There is no `permission` table, no `role_permission`, no boot
reconciliation. At PR 5 a permission key in Go is a string in a slice — no migration, no FK, no
runtime effect, reversible by editing a Go file.

The irreversible half is the schema. A migration that has appeared in a tagged release is frozen
(`.claude/rules/migrations.md` §"Shipped migrations are frozen"), and every migration moves
`test/golden/migrations/fresh_install_fingerprint.txt`, which is CODEOWNERS-protected. The asymmetry
decides it: **ship the list now, ship the tables in Phase 2 where they were planned.**

### The three options, priced

| | What ships | Migration-ordering consequence | Boot-failure consequence |
|---|---|---|---|
| **chosen** | `catalogue.go` only | none — no migration | none. No FK exists; a bad key is a red `make verify-spec`, which is what SPEC005 is for |
| catalogue + `permission` + `role_permission` + seed | `000002_guild.sql` **and** `000003_authz.sql`, frozen from the day PR 7's release train exists | `role_permission.role_id REFERENCES role(id)` (`01-domain-model.md:389-392`) is unsatisfiable with no `role` table, so `permission` ships alone and the FK is ceremony | introduces boot reconciliation (`orphaned_at`, `01-domain-model.md:370`) three phases early, with no `authz.Check` to consume it |
| two keys only, grown per-PR | `catalogue.go` with two keys | none | none, but every later endpoint PR trips "adding a permission key is a schema change — stop and ask" for a key already published in canonical §6 |

The second is a Phase 2 land-grab. The third is contradicted by the repo's own precedent, below.

### Why the whole list rather than two keys

`internal/api/errors.go:145-148` is the direct precedent and it is explicit:

> "Most members have no emitter yet; PR 4 registers one read-only operation. They are declared now
> because the enum is what both SDKs generate their discriminated error union from, and growing it
> one code per PR would make every early PR a breaking SDK change."

The permission catalogue is the same object: `docs/design/02-api-design.md:124` states the canonical
§6 keys **are** the whole 1.0 catalogue. Publishing it whole makes every later endpoint PR a
copy-not-ask, and makes SPEC005 resolve on day one for every route in the resource map.

The mechanism that keeps it honest is the one PR 4 used:
`TestErrors_Enum_MatchesPublishedCatalogue` (`internal/api/errors_test.go:90-96`) compares
`AllCodes()` against `docs/api/errors.md` element by element.
`TestCatalogue_Permissions_MatchCanonicalConventions` does the same against the fenced blocks at
`docs/design/00-canonical-conventions.md:91-102` (permissions) and `:107-111` (scopes). Without that
test the catalogue is a second hand-maintained list, which is exactly what canonical §6 forbids.

### Field set — carry these, defer those

`permission`'s columns are `key, category, label, description, is_dangerous, requires_step_up,
orphaned_at, sort_order` (`docs/design/01-domain-model.md:364-372`).

- **Carry** `Key`, `Category`, `Label`, `Description` — derivable today from canonical §6 and the
  resource map at `docs/design/02-api-design.md:127-140`.
- **Carry** `RequiresStepUp` — it is specified, not invented. Canonical §6 "The capability floor"
  (`:127-141`) and `docs/api/auth-and-scopes.md:117-135` both name the exact session+step-up set
  (token mint/rotate/revoke, role edits, backup, bulk PII, import commit). Omitting a specified
  field is how Phase 2 gets it wrong.
- **Defer** `IsDangerous`, `SortOrder`, `OrphanedAt` — the first two are matrix-UI affordances
  specified nowhere; `orphaned_at` is a property of a DB row after a downgrade, not of code. Phase
  2's migration adds them as columns with defaults.

### The catalogue's Go shape is constrained by SPEC005, and it was measured

`scripts/verify-spec.py:269-279` reads `internal/authz/catalogue.go` as **text** and asserts
`f'"{permission}"' in catalogue` — a quoted exact substring, deliberately so `raid.tick` does not
satisfy `raid.tick.create` (`:273`).

Two spikes, each running the real gate against a scratch tree through `DKP_REPO_ROOT` — the same
seam `test/repo/spec_gate_test.go:74-75` uses:

| Catalogue shape | Result |
|---|---|
| `func Catalogue() []Permission` returning a fresh literal of structs with `Key: "roster.read"` | `2 operation(s), all conforming` · exit 0 |
| the same, with composed keys — `{Resource: "roster", Action: "read"}` plus a `Key()` method | `[SPEC005] permission 'roster.read' is not in internal/authz/catalogue.go` · exit 1 |

**Every key must therefore appear as a whole quoted literal in `catalogue.go`.** That is compatible
with the house style and incompatible with any clever composition, and it belongs in the file's
header comment — the first person to "tidy" the catalogue into `Resource`/`Action` fields will break
a merge-blocking gate for a reason the failure message does not explain.

Note also what `TestSpecGate_PermissionInCatalogue_IsAccepted` (`test/repo/spec_gate_test.go:379-390`)
writes as its fixture: `var Keys = []string{"roster.read"}`. That is a **package-level mutable var**,
which `.claude/rules/go-idioms.md` §Style bans and which `internal/api/permissions.go:52-56` argues
against at length. The fixture is correct — a `t.TempDir()` throwaway proving the *gate* resolves —
but it is not a template for the catalogue.

### `roster.read` and `admin.settings` are the right keys

Confirmed independently of `internal/api/EXAMPLE_ENDPOINT.md`, which is target-state prose and wrong
in several other places. `docs/design/02-api-design.md:196`, the resource-map row for `/guild`:

> `| GET · PATCH | /guild | … | roster.read · admin.settings | roster:read · — |`

Both keys appear verbatim in canonical §6 (`00-canonical-conventions.md:93` and `:101`), both match
the `<resource>.<action>` shape canonical §16 requires, and the dash in the scope column confirms
PATCH is session-only — which is what `EXAMPLE_ENDPOINT.md:185-192` already encodes.

### `make test-authz` and `test / authz-matrix` stay Phase 2 stubs

`Makefile:418-419` is `@$(call notyet,Phase 2,the authorization matrix)`;
`.github/workflows/ci.yml:405-421` runs it and documents it as enumerating every
(operation × principal) cell. There are **no principals**: `internal/auth/` is an empty directory and
`internal/api` has no middleware that reads `x-dkp-permission`. A matrix over zero principals is not
a weaker test, it is not a test.

What that leaves unguarded, stated plainly because it is the thing most likely to be glossed:

> **`GET /api/v1/guild` and `PATCH /api/v1/guild` are served with no authentication and no
> authorization whatsoever, while the published spec declares `security: [{pat:…},{session:{}}]`.**
> PATCH is the first mutating endpoint in the product. The spec tells a bot author a credential is
> required; the server accepts the request without one.

This ships that way, with the gap named in `SECURITY.md` and pinned by
`TestGuild_Unauthenticated_IsAKnownPhase0Gap` — a tripwire asserting that an unauthenticated PATCH
currently succeeds, so the day the auth middleware lands that test goes **red** and closing the gap
is a deliberate deletion rather than a silent discovery. The repo already works this way:
`TestArch_MutatingPost_RequiresIdempotencyKey` (`internal/api/arch_test.go:189-198`) is "a tripwire
installed ahead of the code it gates" and runs over an empty set today.

The two alternatives were rejected. Deferring PATCH to the auth PR costs the ETag / `If-Match` /
412 / 428 story, which is the most valuable half of the worked example and the half no other Phase 0
PR teaches. Gating behind an env var invents configuration to paper over a sequencing fact. There is
no released binary yet — the release train is PR 7 — so the marginal exposure is a developer's
laptop, not a guild's site, and that is what makes shipping-with-a-tripwire acceptable rather than
merely convenient.

### Blast radius if this is wrong

- **The catalogue should have been generated after all:** one Go file rewritten plus a `make gen`
  line. No migration, no data. An afternoon.
- **A key's spelling is wrong:** a Go edit plus a spec regen. `x-dkp-permission` is an OpenAPI
  *extension*, not an `operationId`, so `oasdiff` does not treat the change as breaking and no SDK
  method name moves.
- **Had the tables shipped and Phase 2 disagrees:** a shipped migration that cannot be un-shipped, on
  the four tables that decide who can do what. That is the failure this question existed to avoid.

---

## Q2 — The first integration test

### Decision: `httptest.NewServer(api.New(cfg))` over `store.NewDB(t)`, in `test/integration/`, with a 25-line `TestMain`. No `testenv` package. No `ClientAs`.

### No PR in Phase 0 adds authentication

[`first-ten-prs.md`](first-ten-prs.md) covers PRs 1–10 plus 12, described at `:7-8` as "Phase 0 of
`ROADMAP.md` plus the first two Phase 1 items". All eleven scopes: store (2), migrations (3),
Huma+gates (4), this PR (5), SPA (6), release (7), core types (8), ledger schema (9), ledger service
(10), licence gate (12). **None adds a user, a session, a token or a role.**

Authentication is `ROADMAP.md:184-185`, **Phase 2 deliverable 1**: "`app_user`, `user_identity`
(argon2id only), `session`, `service_account`, `api_token` (HMAC-pepper), `feed_token`; one
middleware resolving cookie-or-bearer into a single `Principal`."

So `env.ClientAs(t, testenv.RoleOfficer)` (`internal/api/EXAMPLE_ENDPOINT.md:255`) is not "not built
yet" — **there is no such thing as an officer**, and there will not be for three phases. It is a
promise the document must stop making, not a helper to write.

### What already exists, and it is enough

- `store.InitTemplate` / `store.NewDB(tb)` / `store.CloneTemplate` / `store.Counted(tb)` /
  `(*Counter).Budget(tb, n)` — `internal/store/testing.go:59,133,172,203,229`.
- `store.ApplySchema(fsys)` (`internal/store/migrate.go:169`) and `db.SQLiteMigrations()` — the two
  pieces `internal/store/main_test.go:31-40` composes. `test/integration/main_test.go` is a ~25-line
  copy of that function.
- `api.New(api.Config{...}) http.Handler` — `internal/api/server.go:76`, whose doc comment at `:60`
  already anticipates this PR: *"adding the build stamps, the clock and (in PR 5) a store"*.

The harness is `TestMain` → `store.InitTemplate`, then per test `store.NewDB(t)` → `api.New(...)` →
`httptest.NewServer`. Roughly six lines per test, with no new package.

### A `testenv` package is not warranted, and `startServe` is the wrong tool

`cmd/dkp/ready_test.go:19-48`'s `startServe` boots the real cobra command, which opens **its own**
`*store.Store` from `DKP_DB_PATH`. The test cannot reach that Store. Since `store.Counted(tb)`
resolves the counter that `store.NewDB(tb)` registered (`internal/store/testing.go:203-222`), a test
that does not construct the Store **cannot declare a statement budget at all**. `startServe` is
right for `/readyz` and boot behaviour and wrong for anything that needs the N+1 tripwire.

`testenv` should arrive when there is a principal to be — with Phase 2's auth — introduced by the PR
that has something for it to abstract. Today it would be a two-line wrapper around three calls.

### The statement budget reaches HTTP for free

`WithStatementCounter` interposes at the `driver.Connector` (`internal/store/store.go:176-194`), so
it counts every statement on both pools regardless of which goroutine issues it.
`httptest.NewServer` serves on its own goroutines; the counter does not care.

```go
s := store.NewDB(t)
srv := httptest.NewServer(api.New(api.Config{Store: s, Clock: clock.Fixed(testEpoch)}))
t.Cleanup(srv.Close)

store.Counted(t).Budget(t, 1)   // declared AFTER construction, so boot statements are excluded
resp := get(t, srv.URL+"/api/v1/guild")
```

`Budget` snapshots `c.Count()` at declaration and checks at cleanup
(`internal/store/testing.go:229-249`), so declaring it after the server is built excludes anything
`api.New` does. Migrations run once in `InitTemplate` against the template, not per test, so they do
not pollute the window either.

**Cost to build this wiring: zero.** It already works. `.claude/rules/store-and-sql.md`
§"Statement-count budgets" calls it "the highest-value piece of test infrastructure in an
agent-heavy codebase"; PR 2 finished it. PR 5 only has to *use* it, and `GET /api/v1/guild` is a
singleton — budget **1**.

### The response-validation middleware: stop promising it, do not build it

The documents are in direct conflict:

- `docs/design/04-testing.md:69-86` — **"Unresolved. Do not assert either answer in code or docs
  until Phase 0 settles it."**
- `.claude/rules/api-endpoints.md:182` — "a `kin-openapi` middleware validates **every response in
  the whole integration suite**". Asserts an answer, and therefore violates the line above.
- `docs/design/02-api-design.md:606` and `04-testing.md:301-304` assert the middleware runs, without
  naming the library.
- `internal/api/EXAMPLE_ENDPOINT.md:275-277` tells the reader it "checks every response against its
  declared schema automatically". It does not exist.
- `ROADMAP.md:190-192` puts "response validation whenever `DKP_ENV != production`" in the **Phase 2**
  middleware stack, alongside the idempotency table, ETag/`If-Match` and rate limiting.

Canonical conventions is silent on validators, so the tie-break is ownership: `04-testing.md`
declared the item open and forbade assertion, so it wins and `.claude/rules/api-endpoints.md:182` is
the bug. It is corrected in this change.

**The decision and the installation are separate work.**

- **Not PR 5's job:** installing the middleware. ROADMAP puts it in Phase 2 with the rest of the
  middleware stack, and adding a validator to PR 5 means a dependency proposal (AGENTS.md "Do not
  add a dependency"), a licence-gate delta, an ADR and middleware — inside a PR already over budget.
- **Is PR 5's job:** deleting the claim from `EXAMPLE_ENDPOINT.md:275-277` and from
  `.claude/rules/api-endpoints.md:182`.
- **Is still Phase 0's job, separately:** the bake-off. See V15 in
  [`verify-before-phase-0.md`](verify-before-phase-0.md), which now carries the measurement below
  and the experiment.

The committed document as of `be1aa0a`, measured:

```
openapi: 3.1.0 · 8,818 bytes · top-level keys: components, info, openapi, paths, webhooks
3.1-only constructs found:  3 ×  "type": ["array","null"]
no const, no $defs, no prefixItems, no numeric exclusiveMinimum/Maximum
paths: /api/v1/meta   webhooks: ping   schemas: ErrorDetail, MetaBody, MetaServer, ProblemDetail
```

`04-testing.md:83-85` asks for "a handful of Huma operations including one webhook and one nullable
field". Today's article has the `webhooks` block and the union type but no nullable *field*; PR 5a's
`*string` / `*int` PATCH body is what first produces `["string","null"]`. So the bake-off is
strictly better run after PR 5a, which is a further argument for scheduling it as its own item.

### A finding that changes code, not just docs

`internal/api/EXAMPLE_ENDPOINT.md:213` prescribes:

```go
IfMatch string `header:"If-Match" required:"true"`
```

PR 5's own acceptance criterion (`first-ten-prs.md`, PR 5a below) and canonical §7 require **428**
when `If-Match` is absent. But Huma v2.39.1 raises a missing required parameter as `errStatus`,
initialised to `http.StatusUnprocessableEntity` (`huma.go:899`, error added at `:980`, written at
`:932`). The documented struct tag yields **`422 validation_failed`**, not `428
precondition_required`.

`api.CodePreconditionRequired` already exists in the closed enum (`internal/api/errors.go:100`) with
no emitter. PR 5a declares `If-Match` **optional** in the tag and checks emptiness in the handler.
One-line code change, three-line doc correction — and exactly the class of thing that otherwise
ships as "the criterion says 428, the test asserts 422, nobody noticed".

### The shape of `test/integration/guild_test.go`

```go
package integration_test

// main_test.go — ~25 lines, a copy of internal/store/main_test.go:31-48.
//   fsys, _ := db.SQLiteMigrations()
//   cleanup, _ := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
//   goleak.VerifyTestMain(m, goleak.Cleanup(...))

func newServer(t *testing.T) (*httptest.Server, *store.Store) {
    t.Helper()
    s := store.NewDB(t)
    srv := httptest.NewServer(api.New(api.Config{
        Store: s,
        Clock: clock.Fixed(testEpoch),   // internal/clock; time.Now is grep-banned
    }))
    t.Cleanup(srv.Close)
    return srv, s
}

func TestGetGuild_Singleton_ReturnsETag(t *testing.T)        // 200, strong ETag, budget 1
func TestUpdateGuild_NoIfMatch_Returns428(t *testing.T)      // precondition_required
func TestUpdateGuild_StaleIfMatch_Returns412(t *testing.T)   // meta.current + meta.current_etag
func TestUpdateGuild_CurrentIfMatch_Succeeds(t *testing.T)   // positive control; new ETag differs

func TestUpdateGuild_StaleIfMatch_Returns412(t *testing.T) {
    t.Parallel()
    srv, _ := newServer(t)

    var cur api.GuildDTO
    getJSON(t, srv.URL+"/api/v1/guild", &cur)   // captures the ETag too

    resp := patchJSON(t, srv.URL+"/api/v1/guild",
        map[string]any{"name": "Kittens"},
        http.Header{"If-Match": {`"stale-etag"`}})

    var pd api.ProblemDetail
    decodeProblem(t, resp, &pd)     // same helper shape as internal/api/errors_test.go:23
    require.Equal(t, http.StatusPreconditionFailed, pd.Status)
    require.Equal(t, api.CodePreconditionFailed, pd.Code)
    require.Equal(t, cur.Name, pd.Meta["current"].(map[string]any)["name"],
        "412 must return the current representation so a bot merges in one round trip")
    require.NotEmpty(t, pd.Meta["current_etag"],
        ".claude/rules/api-endpoints.md: meta.current_etag is what makes the retry one round trip")
}
```

No `testenv`, no `ClientAs`, no generated client — PR 6 fills in step 6. Raw `http.Client` plus two
local helpers, mirroring `internal/api/errors_test.go:23-51`.

### Edits `EXAMPLE_ENDPOINT.md` step 7 needs, which PR 5b makes

| Line | Today | Replace with |
|---|---|---|
| `:30` | "7 \| Tests \| `test/integration/` \| CI" | keep the path; note the harness is `store.NewDB` + `httptest.NewServer`, and that `internal/api/guild_test.go` carries the handler-level cases |
| `:253-271` | `testenv.New(t)` / `env.ClientAs(t, testenv.RoleOfficer)` / `c.GetGuild(env.Ctx)` / `problem.Detail` | the `newServer(t)` sample above; raw `http.Client`; `api.ProblemDetail` |
| `:275-277` | "The response-validation middleware checks every response … automatically" | "There is no response-validation middleware yet — the validator choice is open (`docs/design/04-testing.md` §'Open item'). Until it lands, assert the response body explicitly." |
| `:278-279` | "**PAT parity** — … a scoped PAT" | "**PAT parity** — from Phase 2, when tokens exist. Not applicable in Phase 0." |
| `:282` | "**Statement-count budget** — if it reads a collection" | keep, and add the worked call `store.Counted(t).Budget(t, 1)`, declared after the server is built |
| `:288` | "add a line to `docs/api-changelog.md`" | PR 5a creates that file |

---

## Q3 — The line budget, and where PR 5 splits

### Calibration, measured against the two most recent PRs

```
PR 3  f3ecfcb   4,223 added / 131 deleted   −128 generated  →  4,095 hand-written added
PR 4  be1aa0a   5,326 added / 104 deleted   −352 generated+vendored  →  4,974 hand-written added
```

Generated = `internal/store/sqlitegen/**`, `db/migrations-sqlite/**`, `openapi/openapi.json`,
`internal/api/docsui/vendor/**`.

Two structural drivers, both measured and both mandated by AGENTS.md:

- **Comment density.** `internal/api/permissions.go` 74%, `meta.go` 50%, `server.go` 44%,
  `store/tx.go` 42%, `store/meta.go` 37%, `errors.go` 30%.
- **Gates are tested, not trusted.** PR 3 spent 1,009 lines on `test/repo/` + `test/migrations/`;
  PR 4 spent 1,373 on `arch_test.go` (700) + `spec_gate_test.go` (673) — about a quarter of each PR,
  and it is the definition-of-done row "every acceptance criterion is a *test or gate in this PR*".

### PR 5 estimate, by component

| Component | Est. hand-written added | Basis |
|---|---|---|
| `db/schema.hcl` — `guild` block | 85–110 | `dkp_meta` is 47 lines for 3 columns incl. header comment (`db/schema.hcl:25-71`) |
| `db/migrations-sqlite/000002_guild.sql` | **generated** | excluded |
| `db/queries/guild.sql` | 30–45 | `db/queries/meta.sql` is 21 lines for 2 queries |
| `Queries` growth + `Tx` → `func(ctx, Queries) error` + call sites + tests | 90–160 | `internal/store/tx.go:18-21` explicitly assigns this change to PR 5 |
| `internal/guild/{doc.go,service.go}` | 170–240 | house median non-trivial file: `migrate/version.go` 155, `restore.go` 155 |
| `internal/guild/service_test.go` | 180–260 | observed test:code ≈ 1.3–1.7× |
| `internal/api/guild.go` | 240–330 | `meta.go` is 108 for **one** trivial GET with no body and no store |
| `internal/api/guild_test.go` | 300–420 | `meta_test.go` 134 for one op; `errors_test.go` 406 |
| ETag + `If-Match` / 412 / 428 helper + tests | 180–280 | 428 hand-rolled (the Huma finding); 412 carries `meta.current` + `meta.current_etag` |
| `internal/authz/{doc.go,catalogue.go}` | 190–280 | ~37 keys + ~24 scopes, one struct each; `errors.go` is 463 for ~60 codes |
| `internal/authz/catalogue_test.go` | 140–220 | precedent `errors_test.go:53-149` ≈ 100 for the same job |
| `arch_test.go` — `If-Match` coverage + negative fixture | 130–220 | the existing idempotency pair ≈ 100 (`arch_test.go:189-263`, `:637-660`) |
| `test/integration/{main_test.go,guild_test.go}` | 200–320 | `main_test.go` ~25; four cases plus two helpers |
| `internal/api/EXAMPLE_ENDPOINT.md` rewrite | 350–450 | 310 today; gains ETag/412/428, loses six wrong claims |
| `db/RECIPES.md` rewrite | 120–200 added (~180 deleted) | 279 today; **9 of its 12 recipes query tables that do not exist** |
| `TestDocs_ExampleEndpointSnippets_Compile` | 250–450 | extract fences, classify hcl/sql/go/json, wrap Go fragments, sqlc-check SQL. Analogues: `docs_test.go` 207, `spec_gate_test.go` 673 |
| `scripts/eval-example-endpoint.sh` + nightly workflow | 150–280 | `verify-spec.py` 421, `new-migration.sh` 163 — plus an agent runner and an API key |
| `docs/api-changelog.md` (does not exist; step 8 requires it) | 30–60 | — |
| Makefile row, `ci.yml`, CODEOWNERS | 40–80 | — |
| **Total** | **2,875 – 4,405** — midpoint **≈ 3,600** | |

### Both numbers were wrong, and the scope was wrong first

1. **2,500 does not describe this codebase.** It was set before the house comment density and the
   "gates are tested, not trusted" doctrine were priced in. Both are mandated by AGENTS.md, so the
   budget was asking for something the contract forbids. A budget nobody has ever met, and which
   every PR "reports rather than trims", is not a control — it is a ritual, and it teaches the next
   agent that the definition of done is negotiable.
2. **PR 5's scope was genuinely two PRs.** It shipped a resource *and* two documents *and* a snippet
   compiler *and* a nightly agent eval. The last two are infrastructure, not documentation.

### Decision: split code-then-docs, and raise the budget to 3,500

The seam and the two PRs are written up in [`first-ten-prs.md`](first-ten-prs.md). The reasoning:

- **5a is green alone** — the endpoint exists, the arch tests pass, `make verify-spec` passes
  because SPEC005 now resolves, and the spec regenerates. It needs nothing from 5b.
- **5b is green alone** — every snippet it compiles is copied out of code that already merged. The
  compile gate has something real to compile on day one, which is the only condition under which
  that gate means anything.
- **The direction is the whole point.** The brief requires the documents be written *"from the
  actual code in this PR — never from memory"*. Code-then-docs honours that. Docs-then-code is what
  produced today's `EXAMPLE_ENDPOINT.md` — target-state prose describing `humachi`, `problem.From`
  and `store.Q()`, none of which exist — and it is what PR #6 (`87fa211`) and PR 4 both spent real
  effort cleaning up.

**The rejected seam: `db/RECIPES.md` separately from `EXAMPLE_ENDPOINT.md`.** Step 2 of the example
(`EXAMPLE_ENDPOINT.md:63`) sends the reader to `RECIPES.md` for the query shape. Splitting them
leaves the shipped example pointing at a recipe file whose recipes still query `ledger_entry`,
`balance_snapshot`, `person`, `raid`, `item_fts` and six other tables that do not exist — a document
promising code that does not exist, which is precisely the failure mode being avoided. It also
splits ~200 lines off a ~1,200-line PR, which buys nothing.

The budget was raised as well as split, because 5a's upper range is 3,065: splitting and *then*
reporting an overrun would repeat the pattern rather than fix it.

### The item most likely to blow the estimate

`scripts/eval-example-endpoint.sh` — "starts a fresh agent session whose entire context is the
repository plus the two documents". That is an agent harness, an API key in CI secrets, a nightly
workflow, an issue-filing path on failure, and a non-deterministic pass criterion. If 5b's estimate
is wrong, it is wrong here, by 300+ lines and by a secrets decision nobody has made. See U5 below.

---

## Contradictions found

Fourteen. Canonical conventions is the tie-breaker; where it is silent, the document that *owns* the
decision wins. The two marked **fixed here** are corrected in the same change as this file; the rest
belong to PR 5a or PR 5b, which is noted per row.

| # | Where they disagree | Which wins | Disposition |
|---|---|---|---|
| 1 | `EXAMPLE_ENDPOINT.md:12-13` calls `humachi.New` the v2 API | `.claude/rules/api-endpoints.md` §"The adapter is `humago`" + `internal/api/server.go:7,96` | PR 5b |
| 2 | `EXAMPLE_ENDPOINT.md:18` "The nine steps" vs the old brief's "walks seven steps" | `first-ten-prs.md` owns PR content, but the 9-step version is the better artefact | PR 5b — they are not the same list, not just a different count |
| 3 | `EXAMPLE_ENDPOINT.md:247` puts the test in `test/integration/`; the old files-touched block listed only `internal/api/{guild.go,guild_test.go}` | both — they are different layers | fixed in the PR 5a files-touched block |
| 4 | `EXAMPLE_ENDPOINT.md:174,199` calls `problem.From(err)` | code | PR 5b. No such function; the edge is `api.NewProblem(...)` (`errors.go:279`) and `problem` is `internal/api/middleware`, unrelated |
| 5 | `EXAMPLE_ENDPOINT.md:111,124` uses `s.store.Q()` and `Tx(func(q store.Queries) error)` | code, for now | PR 5a makes the document right rather than the reverse — `tx.go:18-21` assigns the signature change to PR 5 |
| 6 | `EXAMPLE_ENDPOINT.md:288` requires a line in `docs/api-changelog.md` | — | PR 5a creates the file |
| 7 | `.claude/rules/api-endpoints.md:182` asserts `kin-openapi` | `docs/design/04-testing.md:71` | **fixed here** |
| 8 | `04-testing.md:324`, `02-api-design.md:606`, `ROADMAP.md:213` and `add-endpoint/checklist.md:19` all require `x-dkp-scopes` on every operation | the four documents — **resolved in their favour** | **Adopted** (U4). PR 5a emits it and ships the three-case arch test; `add-endpoint/checklist.md` is corrected here, because "any operation with an empty `x-dkp-scopes`" would have failed every session-only route |
| 9 | `.claude/rules/api-endpoints.md` and `EXAMPLE_ENDPOINT.md:227-237` both list "PATCH without `If-Match` → precondition coverage" | code — the test does not exist | PR 5a ships the first PATCH and the test with it |
| 10 | `EXAMPLE_ENDPOINT.md:213` `required:"true"` vs the 428 requirement in canonical §7 | canonical §7 | PR 5a. Huma raises a missing required param as 422 (`huma.go:899,980`) |
| 11 | `ROADMAP.md:196-198` puts "Guild settings with real columns" in Phase 2 deliverable 11; PR 5 builds `guild` in Phase 0 | `first-ten-prs.md` for PR content | **Fixed here.** PR 5a ships twelve columns (U2); ROADMAP deliverable 11 is rewritten as "the rest", naming the five that wait for a reader |
| 12 | `ROADMAP.md:190-192` puts ETag/`If-Match` in the Phase 2 middleware stack; PR 5 implements it in Phase 0 | `first-ten-prs.md` | PR 5a ships a per-resource implementation, not the generic middleware |
| 13 | `02-api-design.md:196` and `ROADMAP.md:210` list an "away-mode toggle" among `/guild` settings, and "rounding on/off **and** precision" as two settings | `01-domain-model.md:7` (schema authority) | **Both fixed here.** Away mode is three **`person`** columns (`:490-493`) with no `guild` column anywhere; and there is one rounding setting, `guild.points_precision` (`0..2`), not a boolean plus a precision |
| 14 | The old brief said `db/RECIPES.md` "is seeded with the first three query shapes" | — | PR 5b. It already has twelve, nine of which query tables that do not exist |

Also, not a contradiction but wrong: the old brief named `.github/workflows/nightly.yml`; the repo
has `nightly-verify.yml`. **Fixed here.**

### 15 — `admin.settings` is one key with nineteen blast radii

Not a disagreement between documents so much as a defect every document inherited.
`docs/design/02-api-design.md` §4.2–§4.7 pointed nineteen operations at `admin.settings`, from
`PATCH /guild` (rename the guild) to `GET · PATCH /admin/settings` — which that table describes as
mirroring `DKP_*`, and `docs/operations/configuration.md:110-114` puts `DKP_DISCORD_CLIENT_SECRET`,
`DKP_OIDC_ISSUER` and `DKP_OIDC_CLIENT_SECRET` in `DKP_*`, while `docs/design/03-security.md:794-797`
puts `DKP_OUTBOUND_ALLOW_CIDRS` there too and then relies on the whole surface being step-up and
PAT-forbidden so a leaked token cannot relax the SSRF policy and pivot.

One key cannot make that guarantee for nineteen operations without also making it for renaming a
guild. **Resolved by splitting the key** — see *Resolved opens*, U6.

### 16 — Three different PAT-forbidden sets, none matching canonical §6

The set is the input to an architectural test, so the disagreement was not cosmetic:

| Source | Set |
|---|---|
| `03-security.md:558-560` | `token.*`, `admin.roles.*`, **`admin.settings`**, `admin.backup`, **`admin.owner`**, `person.pii.read`, `audit.read`, `import.commit` |
| `auth-and-scopes.md:131-135` | `token.*`, `admin.roles.manage`, `admin.backup`, `import.*`, `audit.read`, `person.pii.read` |
| `api-contract-guardian.md:43` | `token.mint`, `admin.roles.manage`, `admin.backup`, `person.pii.read`, `import.commit` |
| canonical §6 floor (prose) | tokens, role edits, backups, bulk PII, import commit |

A test written from `auth-and-scopes.md` would have let a PAT revoke tokens and exempted the owner
role. **Resolved:** canonical §6 now enumerates the set as permission keys rather than leaving it to
be inferred from prose, all three copies point at that enumeration, and the arch test derives from it
rather than from a local list — so they cannot drift apart again.

---

## Resolved opens

All six were decided by Courtney on 2026-08-07, in the session that produced this file.

### U2 — the `guild` table ships **twelve** columns

`id`, `name`, `tag`, `timezone`, `week_start`, `points_label`, `points_precision`,
`inactive_after_days`, `auto_set_inactive`, `hide_inactive`, `created_at`, `updated_at` — exactly
what `docs/design/02-api-design.md` §4.2 already promises in the public resource map.

Not shipped, each because nothing reads it until Phase 2 or Phase 4: `locale` (ADR-0012 is
English-only at 1.0), `public_standings` (needs auth to mean anything), `redact_tells` and
`artifact_retention_days` (parser and retention, Phase 4), `settings_json` (no schema yet).

**The freeze argument runs the other way, and my first draft had it backwards.** `ALTER TABLE ADD
COLUMN` is a cheap forward migration; `.claude/rules/migrations.md` reserves SQLite's 12-step
rebuild for *dropping or retyping*. Under-shipping costs a second small migration. Over-shipping
costs a column nobody can remove. (The domain-model table is **17** columns, not the 18 this file
first said.)

### U4 — `x-dkp-scopes` is adopted, with a three-case gate

Declared on every operation from PR 5a onward. The arch test distinguishes three cases, and
conflating any two of them is how the previous four documents got it wrong:

| Case | `Security` | `x-dkp-scopes` | `x-dkp-pat-forbidden` |
|---|---|---|---|
| PAT-callable | offers `pat` | non-empty, all in catalogue | absent |
| Capability floor (canonical §6) | `session` only | absent | `true` |
| Session-only by omission (`admin.settings`) | `session` only | absent | **absent — marking it is a false positive** |

Adopting the extension also retires an unverified claim: `docs/design/02-api-design.md:1354` flags
scope arrays on non-`oauth2` `Security` as of uncertain legality in OpenAPI 3.1. If `x-dkp-scopes`
is always present, that question stops mattering rather than needing an answer.

### U6 — the key was split, and the catalogue carries no policy fields

Two decisions, and the first is the substantive one.

**`admin.security.manage` was added to canonical §6.** It takes `GET · PATCH /admin/settings` (both
methods — read access to a document mirroring `DKP_*` is exfiltration unless every secret is redacted,
and that redaction is now written into the resource-map row as the endpoint's obligation) and
`GET · POST · DELETE /feed-tokens` (a feed token is a bearer credential outliving the session that
minted it, which the floor's "minting/rotating/revoking tokens" already covered). Both are step-up
and PAT-forbidden.

Everything else keeps `admin.settings` and is session-only *without* step-up — including
`PUT /pools/{pool_id}/strategy`, deliberately. It is the highest-consequence configuration change in
the product, and it is still not a security key: it leaks nothing, enables no pivot, and is governed
by an audit event, `?dry_run=true` and an append-only ledger. A key that grows to mean "scary"
rather than "can compromise the security posture" recreates the overload the split removes.

So `updateGuild` declares `Security: [{"session": {}}]`, `x-dkp-permission: admin.settings`, and
**no** `x-dkp-pat-forbidden` — session-only because no scope family covers instance configuration,
not because it alters authentication state.

**The catalogue carries `Key`, `Category`, `Label`, `Description` and nothing else.**
`RequiresStepUp`, `IsDangerous` and `SortOrder` wait for Phase 2. This reverses the recommendation
earlier in this file, and the reason is the split: `admin.settings` turns out to be neither step-up
nor PAT-forbidden, so PR 5a's two operations exercise neither mechanism. The "declare it whole now"
argument holds for keys — both SDKs and the Phase 2 seed derive from them — and does not hold for a
policy flag nothing derives from and no test can validate against a consumer.

### U1 — the validator bake-off runs **after** PR 5a merges

`docs/design/04-testing.md:83-85` asks for a spec with "one webhook and one nullable field". Today's
has the webhooks block and 3 × `["array","null"]`; 5a's `*string` / `*int` PATCH body is what first
emits `["string","null"]`. Recorded in full at V15 of
[`verify-before-phase-0.md`](verify-before-phase-0.md), including the experiment.

### U3 — the snippet-compile spike runs **now**, before PR 5a

~2 hours, against today's `EXAMPLE_ENDPOINT.md`: extract the four Go fences and find out how much
scaffolding each needs to reach `go build`. It needs no code from 5a, and running it first means
learning whether 5b is a 900-line PR or a 2,000-line one *before* committing to the split's
estimates — rather than at the moment 5b starts.

The link-checker breakage found while writing this file is the same problem in miniature. A Go
**generic instantiation** — a call whose type argument sits in square brackets immediately before the
parenthesised argument list — is indistinguishable from a markdown link to
`scripts/check-links.py:16`'s regex, which does not skip fenced blocks. `make docs-links` went red on
a Go sample inside a code fence, and it went red a second time on the sentence that described the
first failure. Backticks do not help; only avoiding the construct does.

That is a merge-blocking gate mis-firing on correct content, in a repository whose most valuable
document is defined by the code inside it. It is worth fixing `check-links.py` to skip fenced blocks
at the same time as building the compile gate — the two tools have the same blind spot, and the
compile gate is the one that has to parse fences correctly anyway.

### U5 — the eval gate ships local-only; the nightly lane lands with PR 7

`scripts/eval-example-endpoint.sh` ships in 5b as a runnable local target with a documented
invocation. No workflow, no API key in repo secrets, no issue-filing path — those land with the
release train, where the CI credentials question is being answered anyway. This keeps 5b near its
900-line estimate and avoids writing a workflow that depends on a secret nobody has agreed to.

### U7 — the `/guild` names were EQdkp's, and two gates now catch the class

Fixed: `docs/design/02-api-design.md` §4.2 and `ROADMAP.md` item 11 now say `inactive_after_days`
and `auto_set_inactive`.

**The cause was not a typo.** Both names come from `docs/design/05-migration.md`'s list of EQdkp
`<prefix>config` keys, which is there because the importer must read them. The `/guild` row was
written from that list rather than from DKP's schema. The same transcription produced the phantom
"rounding on/off **and** precision" — EQdkp carries `round_activate` *and* `round_precision` where
DKP carries one `points_precision` — and it is why the away-mode entry was there too. Other keys in
that row *had* been renamed on the way in (`dkp_name` → `points_label`, `guildtag` → `tag`), which is
exactly what made the survivors invisible. `auto_set_active` is the **opposite control** from
`auto_set_inactive`, so a client written from the published contract would have set the wrong value
with nothing in the contract to contradict it.

Two gates now cover the class, at the two points where a name becomes contract:

| Gate | Scope | Catches |
|---|---|---|
| `AGPL002` (`scripts/repo-gates.sh`) | `db/` | an EQdkp config key used as a **column** name, before any endpoint exposes it |
| `SPEC008` (`scripts/verify-spec.py`) | `openapi/openapi.json` | an EQdkp config key used as a **wire field or parameter** name |

Both are proven by fixtures in `test/repo/`, in both directions —
`TestRepoGates_EQdkpConfigKeyInSchema_FailsGate` / `TestRepoGates_DKPOwnColumnNames_PassGate` and
`TestSpecGate_EQdkpConfigKeyAsFieldName_IsRejected` / `TestSpecGate_DKPOwnFieldNames_AreAccepted`,
plus a parameter case for the `name`-field shape trap SPEC006 documents.

**The documentation half is deliberately not gated, and this is the part worth remembering.** The
approved plan was a grep over the three contract documents. Run against the real tree it fires on
`docs/design/01-domain-model.md:572` and `:2870`, which name `show_twinks` precisely to explain why
DKP rejects that design, and on the correction notes written alongside this decision, which quote
both banned names in order to document them. A markdown gate cannot tell a leak from a lesson, and a
gate that is usually wrong is one people learn to route around. `hide_inactive` and `timezone` are
excluded from both gates for the adjacent reason: they are in EQdkp's list *and* are DKP's own names,
because the concepts coincide and the words are ordinary English.

### U8 — the mechanism already existed; `***` gains a set/unset distinction

`docs/operations/configuration.md` already specified this and the original write-up of U8 missed it:
*"Secret-valued settings are a **type**, not a convention: they render as `***` in logs, in `/ops`,
in `dkp doctor --json` and in any serialisation of the configuration. A test marshals the whole
configuration and asserts no known secret value appears in the output."* `GET /admin/settings` is
"any serialisation", so it inherits the type rather than inventing a redaction.

One addition, now part of the type: **`***` means set, `null` means unset.** An operator has to be
able to answer "is Discord login configured?" without being told the secret, and a settings screen
cannot render "not configured" from `***` alone. The two render identically in logs, where the
question never arises, and differently in structured output, where it always does.

`GET /admin/settings` **stays** at `admin.security.manage`. Redaction hides the values, not the
shape: the response still names the configured identity provider, the MFA policy, and the contents of
`DKP_OUTBOUND_ALLOW_CIDRS` — which is not a secret and names a reachable internal range, which is
reconnaissance. So the split decided under U6 needs no revision.

---

## What is still open

Nothing blocking PR 5a. Two items carried forward:

| # | Item | Owner |
|---|---|---|
| U1 | The OpenAPI validator bake-off, after PR 5a merges. Half a day; experiment and evidence at V15 of [`verify-before-phase-0.md`](verify-before-phase-0.md). | Phase 0 |
| U3 | The snippet-compile spike, **before** PR 5a starts. ~2 hours; it decides whether PR 5b is a 900-line PR or a 2,000-line one. While doing it, fix `scripts/check-links.py` to skip fenced blocks — it and the compile gate have the same blind spot, and the compile gate has to parse fences correctly anyway. | Phase 0 |
