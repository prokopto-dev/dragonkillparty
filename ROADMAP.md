# Roadmap

**Status:** plan. **Audience:** officer, contributor, agent.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`. Where this file and that one
disagree, that one wins and this file has a bug.

No calendar dates. The planning unit is **`1 pt` = one well-scoped agent PR** — roughly a half-day of
agent work plus ~20 minutes of human review. Reviewer attention, not agent throughput, is the binding
constraint on a volunteer project. **1.0 ≈ 438 pt.**

> **Revised 2026-08-07, against the UI mockups.** Five surfaces covering ~55 screens
> ([`docs/design/mockups/`](docs/design/mockups/)) were reconciled against this plan. Most of it
> survived: the ledger, holds, `server_time` and the `seq` machinery needed nothing. What the plan
> did not carry was **six subsystems it had never named** — quakes and draft week, guild-character
> custody, character keys and raid eligibility, main-swap pricing, versioned policies, and the vouch
> economy — plus **three it carried at a fraction of their real size**: the guild bank, recruitment,
> and item priority lists were one table row each in the old Phase 7. Add tiered bidding and enough
> UI to cover the screens and that is
> ≈114 pt, and it is why this file went from 324 pt and nine phases to 438 pt and ten.
>
> The full verdict table is [`docs/design/11-ui-backend-contract.md`](docs/design/11-ui-backend-contract.md).
> Nothing from the mockups was dropped; where something moved out of 1.0 it is named in
> **Deliberately deferred** as a decision, not a silence.

---

## Sequencing doctrine

Five rules generate every phase below.

| # | Rule | Consequence |
|---|---|---|
| 1 | **Never merge a red main.** No phase temporarily disables a gate. | **Gates ship before the code they gate.** Retrofitting a gate onto 40 routes is a week of grandfathering; installing it at route #1 is 20 lines. |
| 2 | **Generated boundaries are crossed once, early, in a walking skeleton.** | Phase 0 produces one worked, committed, in-repo example of each boundary (`internal/api/EXAMPLE_ENDPOINT.md`, `db/RECIPES.md`). Every later task becomes "copy the example, change the nouns". |
| 3 | **Depth before breadth, except for the UI.** Domain → API → UI, per feature. | The UI gets exactly one foundation phase (Phase 3), which also builds the design system every later slice is written against. After that every feature phase carries its own thin UI slice. A vocabulary of components has to exist before slices can be thin. |
| 4 | **Work items are sized to one PR, one reviewer sitting, one reversible decision.** | ≤ 3,500 lines of hand-written diff (generated files excluded), raised from 800 once three Phase 0 PRs came in above it — see [`docs/development/first-ten-prs.md`](docs/development/first-ten-prs.md). A task that cannot state its own acceptance test in one sentence is not ready to hand to an agent. |
| 5 | **Nothing is "done at the end".** | Tests, docs and the release pipeline are per-phase exit criteria, not phases. Distribution ships in Phase 0 so every later phase produces a pullable image. |

### Ordering defects fixed in this revision

The adversarial review found four real ordering bugs in the source plan. They are fixed here, not
repeated.

| Defect | Fix applied |
|---|---|
| Phase 2's exit criterion required PAT-parity green, but PAT-parity was defined as replaying recorded Playwright journeys — and Playwright shipped in Phase 3. Circular. | **Phase 2 ships hand-written parity cases.** Phase 3 adds the recorder and a test asserting recorded coverage is a superset of the hand-written set. |
| The EQdkp compat shim shipped in Phase 6, but the cutover checklist telling guilds to point their bots at it was a Phase 5 deliverable. A guild cutting over after Phase 5 had dead bots. | **The shim moves to Phase 5**, next to the checklist. It is also its natural home: it resolves legacy `member_id`s through `import_id_map`, a Phase 5 artifact. Every service it calls exists by end of Phase 4. |
| `seed.Perf` was created in Phase 3 item 9, after the standings work in item 3 that budgets against it. | **`seed.Perf` starts in Phase 1** (ledger-only, 520k entries), extends in Phase 2 (roster) and Phase 4 (raids). A `seed_profile_test` asserts non-decreasing row-count floors per profile. |
| `internal/net/safehttp` and `internal/richtext` were scheduled for the hardening phase, after the code that needs them. | **`safehttp` ships in Phase 2** (OIDC/JWKS is the first outbound client) and **`richtext` in Phase 4** (raid notes are the first user-authored text). Their grep gates ship with them, so Phases 5–8 inherit a choke point instead of grandfathering one. |

Supply-chain controls (Actions pinned to commit SHAs, `--ignore-scripts`, the licence gate) move to
Phase 0 for the same reason: cheap at zero dependencies, expensive at two hundred.

---

## Phase 0 — Foundations and the walking skeleton (≈22 pt, 5%)

**Goal.** A repository in which an agent handed any later task finds the toolchain, the gates, the
worked example and the release pipeline already answering every "how do I…" question.

**Deliverables, in order.** Deliverables, not pull requests:
[`docs/development/first-ten-prs.md`](docs/development/first-ten-prs.md) numbers the PRs, and
deliverable N is generally not PR N — 10 below is its PR 7. Cite these as "deliverable N".

1. Repo skeleton. `LICENSE` (Apache-2.0), `NOTICE`, `TRADEMARK.md`, DCO app, `CONTRIBUTING.md`, PR
   template with the licence-firewall checkbox, `CODEOWNERS`, issue templates (`parser-bug`,
   `import-failure`, `parity-gap`).
2. `AGENTS.md` + `CLAUDE.md`: four architectural laws, pinned versions, the canonical command table,
   the failure-mode table, the licence firewall. Highest-leverage artifact in the phase.
3. `go.mod` with the pinned toolchain and `GOTOOLCHAIN=local`; cobra root; `dkp version`;
   `dkp serve` answering `/healthz` **without touching the database** (canonical §13).
4. `internal/store`: two SQLite pools with the exact pragmas, `store.Tx`, the `sql.Open` grep gate,
   a statement-counting `database/sql` wrapper, template-DB test helper + `TestMain`.
5. Atlas `db/schema.hcl` → goose migrations → `go:embed` runner; `make gen`; generated-drift gate.
6. Migrate-on-boot: snapshot → migrate one file at a time → four checks after each (`foreign_keys`
   restored, `integrity_check`, `foreign_key_check`, append-only survival) → auto-restore on
   failure → downgrade refusal.
7. Huma mount, RFC 9457 problem middleware, request-id, `GET /api/v1/meta`, committed
   `openapi/openapi.json` + drift gate, `operationId` uniqueness test, architectural-test harness
   over the Huma registry.
8. `internal/api/EXAMPLE_ENDPOINT.md` + `db/RECIPES.md` — `GET`/`PATCH /api/v1/guild` walked end to
   end: schema → query → sqlc → handler → spec → generated client → integration test.
9. `web/` scaffold: Vite + React 19 + TanStack, generated client, `go:embed` + SPA fallback, runtime
   `/config.json`, ESLint `no-restricted-globals` for `fetch`, bundle budget. **Plus the Nocturne
   token layer** (`web/src/styles/tokens.css`) and a design-system fixture route rendering every
   token and base class. The tokens are ~50 custom properties and land with the scaffold rather than
   after it, because the alternative is a phase of components written against ad-hoc hex values that
   someone then has to retrofit — canonical §17, [`docs/design/09-frontend-and-design-system.md`](docs/design/09-frontend-and-design-system.md).
10. `Dockerfile` (`FROM scratch`, `HEALTHCHECK` invoking `dkp healthcheck` which GETs `/healthz`),
    goreleaser, `ci.yml` + `release.yml` + `edge.yml`, GHCR publish, cosign + SBOM + provenance,
    `ci-required` aggregate gate.
11. Boundary types: `Micros`, `Centipoints`, ULID, HMAC-signed cursor codec — with property tests and
    the `float32`/`float64`/`total(`/`time.Now` lint bans.
12. Supply chain: every GitHub Action pinned to a commit SHA, `pnpm --ignore-scripts`, Renovate with
    the high-risk allowlist, `govulncheck` and the GPL/AGPL licence gate wired into `ci-required`.

**Tests that must exist by the end.**

- Template-clone integration harness: boot the real server against a real migrated SQLite file in
  `t.TempDir()` and get a 200.
- Migration fresh-install fingerprint; **migration-failure auto-restore** (deliberately broken
  migration → exit 1 → byte-identical DB); downgrade refusal.
- Spec drift; codegen drift; `operationId` uniqueness and casing.
- Boundary-type property tests (cursor round-trip and order preservation, `Centipoints` round-trip,
  `Micros` ↔ RFC 3339).
- `goleak` in `TestMain`.
- Repo-gate tests: a misplaced `sql.Open`, a route declared outside `internal/api`, and an unpinned
  Action each fail CI.

**Docs written during.** `README.md` · `AGENTS.md` · `CONTRIBUTING.md` ·
`docs/development/inner-loop.md` · `docs/development/verify-before-phase-0.md` (worked *during*
Phase 0, not after) · `docs/operations/upgrade-and-backup.md` · ADR-0001…0005.

**Exit criterion.** `docker run ghcr.io/prokopto-dev/dragonkillparty:edge` starts, migrates, and
serves `/healthz`, `/readyz`, `/openapi.json`, `/docs` and the SPA shell. `make check` ≤ 60 s.
Breaking a migration on a
scratch branch fails the auto-restore test. Editing a handler without `make gen` fails the drift
gate. **A new agent handed only `EXAMPLE_ENDPOINT.md` and `RECIPES.md` adds a second endpoint with no
further guidance.**

**Demo.** At the end of this phase you can hand someone a 24 MB signed container that starts in
400 ms, serves its own API docs, and refuses to eat their database on a bad migration.

**Parallel lane opened here, never on the critical path.** `fixtures.yml` — build EQdkp Plus 2.0.5 /
2.1.5 / 2.2.27 / 2.3.39 by running the real PHP installers in Docker, seed synthetic guild data,
publish the MariaDB data directories as public OCI artifacts to GHCR. Long lead time, zero coupling,
blocks Phase 5. Start it on day one.

---

## Phase 1 — Ledger, strategies, invariants (≈38 pt, 9%)

**Goal.** Make the money correct and make it impossible to make it incorrect. Everything else in the
product is a client of this package.

**Deliverables, in order.**

1. Ledger schema: `ledger_batch` / `ledger_entry`, `STRICT`, the covering balance index, the
   statement index, the append-only triggers, `actor_is_beneficiary` on the batch.
2. `seq` allocation inside the write transaction; `BalanceAsOfSeq`; synchronous `balance_snapshot`
   upsert.
3. Ledger service: `BatchProposal` → validate → commit in one transaction, hash chain, `source_ref`
   and `idempotency_key` uniqueness.
4. Invariant engine as executable objects: `NoFloat`, `SumZero`, `NonNegative`,
   `MonotoneNonDecreasing`, `Permutation`, `RatioPreserved`, `Conserved`,
   `LargestRemainderSumsToDebit`.
5. Largest-remainder allocator with a deterministic `account_id` tiebreak; the `residue`,
   `guild_bank`, `write_off` and `import_opening` system accounts.
6. `PointStrategy` interface + injected `Clock`/`Rng` (seed persisted onto the batch); the
   strategy-purity import-graph test.
7. Strategies in dependency order: `fixed_price` → `tick` → `zero_sum` → `attendance_weighted` →
   `decay_percent` → `decay_window` → **`cap`** → **`start_points`** → `loot_council` → `roll` →
   `relative_bid` → `auction_open` → `auction_sealed`.
   `cap` and `start_points` are owner-mandated 1.0 items: without them the importer's own remediation
   message ("re-enter your APA rules") points at a UI that cannot express what the guild had.
8. `PlanReversal` per strategy. Reversal is not always negation — this is where the interface earns
   its shape.
9. `dkp verify-ledger` + the nightly replay job.
10. Pools, `pool_config_change`, `decay_run` with `UNIQUE(pool_id, kind, cadence_period)`.
11. **`seed.Perf` v1 (ledger-only): accounts + 520k ledger entries**, plus the hand-written standings
    query spike run against it *before any API exists*. This is the experiment that decides whether
    `balance_snapshot` survives (see `docs/development/verify-before-phase-0.md`, item 5).

12. **Tier-aware auction resolution** in `auction_open` / `auction_sealed`: the `main`,
    `main_offspec`, `alt`, `anyone` ladder (canonical §5), two-phase resolution — highest tier
    holding any bid takes the item, *then* amount within it — and second price computed **within the
    winning tier**. Reversal and the invariant suite extend to cover it. This is pure strategy code
    and belongs here rather than with the bid FSM in Phase 6, so the arithmetic is settled and
    property-tested before anything transacts on it.
13. **`internal/swap` — the main-swap rule evaluator.** An ordered stack: first-match-free rules
    (open window, annual allowance) then stacking discounts (level 60, needed class), producing a
    quote that names which rules applied and in what order. Pure, with an injected clock, so a quote
    is reproducible; the ledger posting is Phase 7. Discounts **stack** by owner decision
    ([`docs/design/10-ui-decisions.md`](docs/design/10-ui-decisions.md) §12).

### Where this stands in the tree, 2026-08-13

The list above is the plan and its numbering is cited elsewhere — [ADR-0023](docs/adr/0023-balance-snapshot-is-load-bearing.md)
says "ROADMAP Phase 1 item 9" and means it. So the plan is not renumbered or reworded as work lands;
this table is the separate reality layer, and it is the thing to keep honest.

**Phase 0 pulled the ledger core forward.** Deliverables 1–6 shipped inside the walking skeleton
rather than at the head of this phase, because `EXAMPLE_ENDPOINT.md` and `RECIPES.md` needed a real
table to be worked examples *of*, and the append-only triggers had to exist before the first
migration that could drop them. What that leaves for Phase 1 proper is the breadth — twelve more
strategies — plus the four subsystems in items 9, 10, 12 and 13. One of those four is still open;
the table below is the current state.

| # | Deliverable | State | Where it is |
|---|---|---|---|
| 1 | Ledger schema, triggers, indexes | **done** | `db/schema.hcl`; `internal/ledger/{trigger,strict,applied_schema}_test.go` |
| 2 | `seq`, `BalanceAsOfSeq`, snapshot upsert | **done** | `internal/ledger/{seq,balance,snapshot}.go` |
| 3 | Ledger service, hash chain, idempotency | **done** | `internal/ledger/{commit,hashchain}.go` |
| 4 | Invariant engine | **partial, by design** | `internal/ledger/invariant.go`. The universal set plus `SumZero`, `NonNegative` and `LargestRemainderSumsToDebit` are checked. `Conserved`, `MonotoneNonDecreasing`, `Permutation` and `RatioPreserved` are vocabulary-legal and **refuse the commit**, because a declared rule nothing checks is one every reviewer will believe. Each lands with the strategy that needs it |
| 5 | Largest-remainder allocator, system accounts | **done** | `internal/ledger/allocate.go`; `internal/account/kinds` |
| 6 | `PointStrategy`, injected `Clock`/`Rng`, purity test | **done** | `internal/strategy/{strategy,proposal}.go`, `arch_test.go` |
| 7 | Thirteen strategies | **6 of 13** | `fixed_price`, `tick`, `start_points`, `cap`, `decay_percent`, `decay_window` — three from [#193](https://github.com/prokopto-dev/dragonkillparty/issues/193), which also added `internal/strategy/catalogue.go` (the registry a pool's rule ids resolve through, since those columns deliberately carry no CHECK) and `common.go` (the strict config decode, the zero-sum assertion and the shared reversal, one copy for every strategy), and the decay pair from [#194](https://github.com/prokopto-dev/dragonkillparty/issues/194), which widened the `Ctx` façade with `EarnedBetween` — a window expires the earnings in a slice of the log, and a balance delta over the same slice nets in the run's own prior batches. Each entry also declares which of a pool's three questions it answers — [ADR-0026](docs/adr/0026-three-rules-per-pool.md), item 14. Remaining: [#195](https://github.com/prokopto-dev/dragonkillparty/issues/195) (the four bid strategies) · [#196](https://github.com/prokopto-dev/dragonkillparty/issues/196) (`zero_sum`, `attendance_weighted`) · [#197](https://github.com/prokopto-dev/dragonkillparty/issues/197) (`loot_council`). `decay_window` ships the hard cutoff; the linear taper is [#221](https://github.com/prokopto-dev/dragonkillparty/issues/221) |
| 8 | `PlanReversal` per strategy | **ships with each** | `BatchProposal.Negated` and `strategy.reversePlan` set the shape, including dropping `NonNegative`; all six shipped strategies plan a reversal through it |
| 9 | `dkp verify-ledger` + nightly replay | **done** | `cmd/dkp/verify_ledger.go`, `internal/ledger/verify.go`, `.github/workflows/nightly-verify.yml`. ADR-0023 **promoted this** from verification hygiene to a correctness dependency of the standings page, which is what [#198](https://github.com/prokopto-dev/dragonkillparty/issues/198) then shipped against. `--rebuild` is documented and not implemented — [#209](https://github.com/prokopto-dev/dragonkillparty/issues/209) |
| 10 | Pools, `pool_config_change`, `decay_run` | **done** | `db/schema.hcl`; `db/migrations-sqlite/000005_pool_config_versioning_and_decay_run.sql`; `internal/decay/kinds`; `test/migrations/config_history_and_decay_test.go`. `pool` also gained `strategy_config_json` — the configuration `pool_config_change` versions. `decay_run`'s key is `(pool_id, kind, cadence_period)` per [ADR-0024](docs/adr/0024-one-run-table-scoped-by-kind.md); the semantics both tables must satisfy are `.claude/rules/decay-and-jobs.md`. No writer yet: the strategies are items 7 and 12 |
| 11 | `seed.Perf` v1 + the standings spike | **done** | `internal/seed`, `internal/ledger/standings*.go`, `.claude/rules/seed-profiles.md`. **V5 resolved and `balance_snapshot` survived unchanged** — see below |
| 12 | Tier-aware auction resolution | **not started** | Lands with the bid strategies, [#195](https://github.com/prokopto-dev/dragonkillparty/issues/195) |
| 13 | `internal/swap` | **done** | `internal/swap/{policy,request,quote,evaluate}.go`. Pure, with the purity proof in its own `arch_test.go` rather than in the repo gates, because all four of law 3's mechanisms are scoped to `internal/strategy` — [ADR-0025](docs/adr/0025-pure-evaluators-outside-strategy.md). The ledger posting and the request state machine stay Phase 7 |
| 14 | Pool composition — three rules per pool | **done** | [ADR-0026](docs/adr/0026-three-rules-per-pool.md), [#213](https://github.com/prokopto-dev/dragonkillparty/issues/213). `pool` holds `earn_`/`spend_`/`over_time_strategy_id`, each with its own config; `strategy.Rules` routes every planner to the one rule that owns the question and refuses an empty slot by name. `ledger_batch.strategy_id` now records **which of the three** planned a batch, and `PlanReversal` routes on it. The pool settings endpoint that edits the three is Phase 2, with [#212](https://github.com/prokopto-dev/dragonkillparty/issues/212)'s generators |

**The V5 verdict, because item 11 existed to produce it.** Over 527,164 seeded entries all written
through `ledger.Service.Commit`, the standings page is **1 statement and ≤ 1.07 ms warm p99, 13 pages
→ ≤ 27 ms modelled on SD-card-class storage**, against a budget of 4 statements and 150 ms. The
definitional SUM needs 10,412 pages, ~22 s. So `balance_snapshot` survives **with no schema change**
and option D — widening the index — was declined rather than deferred.

The consequence is not neutral, and it is the one thing to carry out of this phase:
[ADR-0023](docs/adr/0023-balance-snapshot-is-load-bearing.md) retires the phrase "droppable cache".
The log is still the only source of truth, but there is no honest fallback that serves the page from
the log, so **losing the cache is a rebuild** — which makes item 9's replay job a dependency of a
shipped page rather than a nicety, and raises the bar for it: it must fail loudly, because a job that
silently stops running makes a drifted cache undetectable, and that is the exact EQdkp Plus failure
mode this product exists to fix.

**Conditional, not scheduled.** `epgp` and `suicide_kings` (×3) are held until a pilot guild asks for
them. They serve WoW-lineage systems the EQdkp inventory itself rates low-value for P99, and their
`PlanReversal` implementations are the hardest single piece of ledger code in the spec (~8 pt). The
trigger is a named guild in an issue, not a maintainer's guess.

**Tests that must exist by the end.** Ticks mark what the tree already has.

- ✓ The **trigger-fires** test: `UPDATE ledger_entry SET amount_cp = 1` raises; `DELETE FROM
  ledger_batch` raises. The guardrail cannot be silently regressed.
- ✓ Coverage floors: `internal/ledger` 95%, `internal/strategy` 95% — wired into `make check` as
  `test-coverage-floor`, so the floor is a gate rather than an aspiration.
- ✓ `seed_profile_test`: row-count floors per profile, non-decreasing.
- ✓ Determinism hash test (P8), and the cache-versus-fold equality (P3) at guild scale under
  `make test-perf` rather than at 200 checks — a fold of 527,164 entries is not a property-test-sized
  operation.
- ✓ Ledger replay over a 10⁵-entry synthetic ledger — `make verify-ledger` builds `dkp`, seeds the
  full `seed.Perf` profile and replays all 527,164 entries against both hash chains and
  `balance_snapshot`, nightly as `replay / seed.Perf`. Nightly rather than per push for the same
  reason as the line above it: the scale is ninety seconds of seeding, while `internal/ledger`'s
  perf suite runs the same `ledger.Verify` over an eight-raid ledger on every push, so the path
  cannot rot between nightlies.
- Properties P1–P12 at 200 checks per PR, 20k nightly. **P1, P2, P5, P6, P7, P8 and P9 exist**; the
  rest arrive with the subsystems they constrain — P4 with the bid FSM, P10 with attendance in Phase
  4. P9's planner half is `TestProperty_P9_DecayRuns_ASecondRunForThePeriodIsTheSameBatch`: a re-run
  of a cadence period proposes the batch that already committed, which is what the
  `(pool_id, kind, cadence_period)` key needs to be true before it can deduplicate anything.
- Decay idempotency across DST and month boundaries. The trap is treating a cadence period as a
  duration rather than a guild-local label; `.claude/rules/decay-and-jobs.md` §2.
- Per-strategy golden `BatchProposal` files — six exist (one per planner each, under
  `test/golden/strategy/<id>/`), seven follow their strategies.

**Docs written during.** `docs/concepts/ledger.md` · `docs/concepts/strategies.md` (one page per
shipped strategy with a worked numeric example) · `docs/concepts/invariants.md` · ADR "why integer
centipoints" · ADR "why append-only" · **[ADR-0023](docs/adr/0023-balance-snapshot-is-load-bearing.md)**
(written here, from item 11's measurement) · `.claude/rules/seed-profiles.md` ·
`.claude/rules/decay-and-jobs.md`.

**Exit criterion.** Unchanged as a definition of done: every shipped strategy passes its declared
invariants under randomised input; a 10⁵-entry replay reproduces every snapshot exactly; the reversal
property holds for every shipped strategy; `internal/strategy` provably cannot import
`internal/store`. No HTTP surface yet, and that is correct.

**What is left to get there**, given the state table above — two workstreams, no others:

| Remaining | Items | Issues |
|---|---|---|
| The twelve unshipped strategies, each with its `PlanReversal`, its declared invariants and its golden proposal. The decay family is also item 10's only missing half — the tables exist and nothing writes to them yet, keyed `(pool_id, kind, cadence_period)` per [ADR-0024](docs/adr/0024-one-run-table-scoped-by-kind.md) | 7, 8 | [#193](https://github.com/prokopto-dev/dragonkillparty/issues/193)–[#197](https://github.com/prokopto-dev/dragonkillparty/issues/197) |
| Tier-aware auction resolution | 12 | [#195](https://github.com/prokopto-dev/dragonkillparty/issues/195) |

The replay had the one ordering constraint on it — it is what makes the 10⁵-entry clause of the exit
criterion executable, and ADR-0023 makes it a dependency of a Phase 3 page — and it landed early
rather than last, which is what that constraint asked for.

**Demo.** At the end of this phase you can seed a synthetic pool from the CLI, post 5,000 batches,
reverse one, print a member statement, and run `verify-ledger` clean — in under a second.

---

## Phase 2 — API, auth, permissions, and the gates (≈52 pt, 12%)

**Goal.** Establish the public contract and the machine-checked guarantees *before* the surface is
large enough for exceptions to hide in.

**Deliverables, in order.**

1. Auth: `app_user`, `user_identity` (argon2id only), `session`, `service_account`, `api_token`
   (HMAC-pepper), `feed_token`; one middleware resolving cookie-or-bearer into a single `Principal`.
2. **MFA/TOTP enrolment** — owner override, ships here, not post-1.0. Without it the capability
   floor, the backup gate, the token-mint gate and the role-edit gate are all advertised and
   unenforced.
3. Permission catalogue in `internal/authz/catalogue.go`, reconciled into `permission` at boot;
   `role`, `role_permission`, `role_assignment` with `scope_type`/`scope_id`; built-in roles seeded;
   `admin.owner` as an ordinary row. One catalogue generates the schema seed, the OpenAPI
   `x-dkp-permission` metadata, the PAT scope enum, the authz matrix and
   `docs/reference/permissions.md` (canonical §6).
4. Scope enum + capability floor: **effective capability = role permissions ∩ token scopes**;
   scope-subsetting enforced on mint. No `admin:*`, no all-powerful token.
5. Middleware stack: idempotency record table, ETag/`If-Match`, cursor pagination envelope, rate
   limiting with both header families, response validation whenever `DKP_ENV != production`.
6. First-run bootstrap: server-rendered setup wizard + one-time bootstrap token; `dkp admin create`,
   `dkp token mint`.
7. Discord OAuth2 + generic OIDC login.
8. **`internal/net/safehttp`** — the only place an `*http.Client` may be constructed, with
   dialer-level connected-IP validation (defeats DNS rebinding), private-range deny, scheme/port
   allowlist, zero redirects. Ships here because JWKS fetching is the first outbound request in the
   product. Its grep gate ships with it.
9. Roster domain: `person` / `account` / `character`, name history, attribute history, claims, ranks,
   raid roles, raid groups, `person.merge` with re-attribution. `/accounts/{account_id}` is
   canonical and system accounts are addressable, so the `Conserved` invariant is verifiable through
   the API.
10. `character_field_def` + `character_field_value` — typed, queryable custom fields. EQdkp's
    `__member_profilefields` is one of its most-used features and has no home in `profile_json`,
    which is validated on write and never queried into.
11. Guild settings with real columns, not a settings blob: point label, display precision,
    `inactive_after_days` / `auto_set_inactive` / `hide_inactive`. Phase 0 PR 5a lands the first twelve
    columns and `GET · PATCH /api/v1/guild`; this deliverable is the rest — `locale`,
    `public_standings`, `artifact_retention_days`, `redact_tells`, `settings_json` — each arriving
    with the code that reads it.

    > **Corrected in Phase 0 PR 5, on four counts, all with one cause.** This line said
    > "rounding on/off + precision", "away-mode toggle", `inactive_period` and `auto_set_active`.
    > All four were transcribed from EQdkp's `<prefix>config` key list
    > (`docs/design/05-migration.md`) rather than from DKP's own schema: EQdkp carries
    > `round_activate` *and* `round_precision` where DKP carries one `points_precision`; away mode is
    > three columns on `person` (`docs/design/01-domain-model.md` §6.1), not a guild setting; and the
    > two key names are EQdkp's, with `auto_set_active` being the **opposite** control from DKP's
    > `auto_set_inactive`. `docs/design/02-api-design.md` carried the same four and is corrected with
    > this.
12. `audit_log` with a gapless `seq` and `prev_hash`/`hash`.
13. **The gates**: no-hidden-operations (four-item allowlist); spec drift; `Security` +
    `x-dkp-permission` + `x-dkp-scopes` on every route; idempotency-required on mutating POST
    prefixes; `If-Match`-required on transitions; shared pagination envelope; closed error-code enum
    with a `type` URL that resolves; `?since_seq=` permitted only on `/ledger/*`, `/audit` and
    `/events/replay`.
14. **The authorization matrix** — `authz_matrix.tsv`, ~17 principals × every registered operation,
    with "every registered operation appears" and "no dead rows" assertions.
15. **Hand-written PAT-parity cases.** Every roster operation the browser performs has an explicit
    PAT case asserting the same outcome. Phase 3 replaces the hand-writing with a recorder; it does
    not replace the requirement.
16. `oasdiff` breaking-change gate + `docs/api-changelog.md` (CODEOWNERS-protected) + sticky PR
    comment.
17. SDK generation (`clients/ts`, `clients/python`) + regen diff + a 40-line smoke program per SDK.
18. `seed.Perf` v2: roster layered onto the Phase 1 ledger. `seed.Small` and `seed.Demo` land here
    too.

19. **`character_key` and `character_flag`** — typed and queryable, not a JSON blob. A key is a
    three-state cycle (`have` / `partial` / `missing`) per `(character, zone)` carrying a provenance
    class: `self_reported`, `officer_verified`, or `parsed` (a `/consider` line). `character.key.verify`
    is a separate permission from `roster.write` because self-report and verification are the same
    column with different authority. Raid eligibility is **computed, never stored** — flipping a key
    changes which raids a member qualifies for immediately, which is the whole feature.
20. The permission catalogue and authz matrix grow by the twenty keys the new subsystems need
    (canonical §6). The matrix is generated, so this is a catalogue edit plus its assertions, not
    twenty hand-written cases.

**Tests that must exist by the end.** Full authz matrix (~8 s) · token lifecycle (expired, revoked,
zero-scope, rotated with overlap) · TOTP enrolment and step-up · idempotency replay ×100 concurrent →
one effect and 99 replays · `412`/`428` semantics with `meta.current` · cursor round-trip and
filter-mismatch rejection · hand-written PAT-parity cases for every roster journey · `safehttp`
rebinding and private-range tests · executable snippets in `openapi/snippets/`.

**Docs written during.** `docs/api/getting-started.md` · `docs/api/auth-and-scopes.md` ·
`docs/api/idempotency-and-concurrency.md` · `docs/api/pagination-and-sync.md` · `docs/api/errors/*`
(one page per code, generated) · `docs/concepts/permissions.md`.

**Exit criterion.** A scoped PAT performs every roster operation the browser can, proven by CI.
Adding a route without `Security`, without a permission key, without an `operationId`, without
idempotency where required, or with a bespoke pagination shape is **unmergeable**.
`docs/reference/permissions.md` is generated, not written.

**Demo.** At the end of this phase you can `curl` a fresh instance end to end: bootstrap the admin,
enrol TOTP, mint a `roster:write` PAT, create a person and a character, claim it, merge two
persons — all with idempotency keys and ETags, all in the published spec, all correctly denied for a
zero-scope token.

---

## Phase 3 — Web foundation, the design system, and read surfaces (≈46 pt, 10%)

**Goal.** Make the product visible to a human without creating a private channel into the domain,
and build the design system every later slice is written against. After this phase the UI stops
being a phase and becomes a per-feature slice — which only works if the component vocabulary exists
first.

**Deliverables, in order.**

1. App shell: TanStack Router, auth guard, error boundary, layout, theme, guild-configurable class
   colours **with a contrast-validator unit test** — an officer will otherwise pick an unreadable
   one. The validator is server-side, so a bot setting a theme is held to the same 3:1 floor.
2. **Message-catalogue scaffolding.** English-only at 1.0, but no hardcoded user-facing string from
   this point on. An ESLint rule fails on a bare string literal in JSX. Retrofitting i18n costs 10×;
   the scaffolding costs ~2 pt now.
3. Login (local + Discord), profile, "connected bots" delegation screen.
4. Roster views: standings table (TanStack Virtual, server-side sort and filter), person page,
   character page, claims queue.
5. **The statement view** — `GET /accounts/{id}/ledger` with running balance, batch drill-down and
   struck-through reversals. This single screen is the product's trust argument. Build it early and
   put it in the README screenshot.
6. Admin: users, roles (matrix rendered from the catalogue), tokens and service accounts, settings.
7. `table-views` as a real API resource — no UI-private blob.
8. Server-rendered surfaces: finish the first-run wizard, `/ops` diagnostics, and the **public
   standings page**. (The `/embed/standings.js` widget is deferred to 1.1 — three surfaces with three
   auth stories is not worth EQdkp `widget.js` parity at 1.0.)
9. Playwright + the **PAT-parity recorder**, with a test asserting the recorded journey set is a
   superset of Phase 2's hand-written cases. The hand-written cases are deleted only when the
   recorded set covers them.
10. Dev-mode request inspector ("copy as curl") — a permanent forcing function on API quality.
11. The `demo.dragonkillparty.org` deploy from `:edge`, reset nightly from `seed.Demo`.

12. **The Nocturne component library** — the system's own classes (`btn`, `card`, `table`, `tag`,
    `seg`, `field`, `input`, `radio`, `dialog`) plus the application families no library provides:
    tick strip with its three states, stat tile, bar/meter row, filter chip, statement row, drag
    handle, master–detail, side-by-side diff, stepper. One file per component, which makes this a
    genuine parallel lane. The 48px end-fade on every rule, outlined-never-filled buttons, and the
    hairline-ring elevation are the details that decide whether the result reads as Nocturne or as a
    generic dark theme — [`docs/design/09-frontend-and-design-system.md`](docs/design/09-frontend-and-design-system.md).
13. **Officer console chrome**: the seven-group icon rail, the three nav layouts (`rail` / `hybrid` /
    `top`) behind one control, and the density axis (`comfortable` / `compact`) retuning **spacing
    only**. Three layouts ship because the right answer depends on screen width and officer habit,
    and the cost is one derived boolean each.
14. **Standings with the as-of cursor** (`?as_of_seq=`), and the two phone views — balance, and
    whether the tick counted. The as-of cursor is not a convenience feature: it is the visible proof
    that a balance is a `SUM` over an append-only log, and an approximation there would be worse than
    no feature at all.

**Tests that must exist by the end.** Playwright journeys 1, 2, 6, 8, 10 (wizard, login, standings
density, statement drill-down, server-rendered surfaces) · PAT-parity superset assertion · traffic
conformance (zero routes outside the spec) · a11y gate (zero serious/critical on primary routes) ·
bundle budget enforced · standings statement-count budget ≤ 4 at 280 members with the
`EXPLAIN QUERY PLAN` golden, run against `seed.Perf` · the i18n lint rule.

**Docs written during.** `docs/getting-started/first-run.md` with screenshots from `seed.Demo` ·
`docs/guides/roster-and-alts.md` · `docs/guides/permissions-for-officers.md` ·
`docs/reference/configuration.md` generated from the `Config` struct and diff-gated.

**Exit criterion.** The SPA runs detached — `pnpm dev` against a remote instance — which is the
definition of "it is a client". (CSP `connect-src 'self'` means detached operation is a *development*
capability unless the operator allowlists the target origin; the docs say so rather than overselling
the argument.) Traffic conformance shows zero routes outside the spec. The demo instance resets
nightly from `:edge` and thereby continuously tests migrate-on-boot against populated data.

**Demo.** At the end of this phase you can send someone a URL: they log in, see 200 members ranked,
click one, and read six years of that member's DKP as a bank statement.

---

## Phase 4 — Raid operations and log ingest (≈62 pt, 14%)

The largest phase, and the one that decides whether guilds actually use this.

**Goal.** A raid night runs end to end from an officer's EverQuest log directory with no manual data
entry, and nothing is ever silently dropped.

**Deliverables, in order.**

1. Domain: `raid` / `raid_tick` / `raid_attendance` / `raid_event_credit`, `attendance_group_id`,
   `event_type` catalogue, `zone`, and the `game_class` / `class_title` / `game_race` seed
   migrations — shipped as our own literals, never transcribed from EQdkp (canonical §15).
   **`raid_tick` carries `pool_id` and so does the tick payload**, or a raid feeding two pools
   cannot be expressed.
2. Attendance statistics: the derived query, `attendance_rollup`, the midnight job and synchronous
   recompute on finalize. Re-parenting an alt changes a published attendance percentage, so
   re-parenting emits an audit event and the UI shows it.
3. Items: `item`, `item_alias`, `item_external_id`, `item_instance`, `item_award`; FTS5 as
   **external-content** (`content='item'`), never contentless, behind the `Search` interface.
4. **`GET /search`** — persons, characters and items in one endpoint. Officer search is how anything
   gets found; EQdkp has a search page and the source API had none.
5. Name-resolution ladder (`internal/loot.Resolve`) with the longest-candidate article strip and the
   auto-alias threshold. The provisional-item path is an **upsert**: a second parse of the same
   unknown name reuses the provisional row instead of violating `ux_item_name`.
6. `reconciliation_item` queue with `ux_recon_open` collapsing repeats, `create_alias` learning, and
   a pre-filled "report a parser bug" issue link.
7. `artifact` upload (content-addressed), `parse_run`, `parse_line`. Artifacts are **retained 180
   days by default** with `/tell`-line redaction at ingest and a guild opt-out (canonical §11);
   `item_instance.parse_line_id` is nullable with `ON DELETE SET NULL` because `parse_line` is pruned
   at 90 days.
8. **`internal/richtext`** — goldmark with unsafe off → a hand-written narrow bluemonday policy;
   `body_md` and `body_html` both stored; re-render on sanitiser upgrade via a migration job. Ships
   here because raid notes are the first user-authored text; Phases 5 and 7 inherit it. Its grep gate
   and the `dangerouslySetInnerHTML` lint ban ship with it.
9. **Parsers, as a fully parallel lane** (pure, stdlib-only, golden-file, no DB): `p99_raid_dump`,
   `p99_who`, `p99_guild_dump`, loot lines, `/random`, chat-award grammars, FTE `engages`,
   `has been slain by`. One file plus one golden directory per format.
   `event_type.slain_pattern_norm` is non-unique, so the parser's tiebreak (longest pattern, then
   explicit `priority`) is specified and tested.
10. **`raid_submission` + `raid_submission_item` as real tables** with a `version` column.
    `POST /raid-submissions` is preview → diff → commit, with `on_unresolved: fail|quarantine|create`
    (default `quarantine`, canonical §12), `content_sha256` tick dedupe, and one transaction on
    commit. A durable, ETagged, re-fetchable preview with a commit receipt is state; a projection
    over `parse_run` cannot carry it.
11. Bulk `POST /raids/{id}/ticks`, `POST /items/resolve`, `POST /characters/resolve`.
12. Adjustments as batches; awards including multi-buyer `share_bp`; decay runs with preview.
13. Calendar, signups, saved **raid templates**, `transform-to-raid` with the back-reference EQdkp
    lacks, **away mode** (`away_from`/`away_to`/`away_note` + a signup filter), and the
    **signup-with-points CSV export** — the sheet a raid leader actually uses to pick the raid.
14. Disputes.
15. UI slices: raid entry, tick grid, drag-a-dump upload, reconciliation queue, loot history, decay
    preview, calendar.
16. `seed.Perf` v3: raids, ticks, attendance and awards layered on.

17. **Guild characters and custody.** A `guild_character` nobody owns (bank mules, trackers) carries
    a timeline of `(operator, from, to, log entry point)`. An event inside a window is attributed to
    that operator; an event inside a **gap** is quarantined, never guessed. Log entry points are
    assigned per operator machine, not per character, and the shared character's own log is flagged
    ambiguous so nobody wires it up as an authority. Attributing writes an ordinary reversible batch.
    A custody gap **warns and does not block** finalisation — blocking a raid close on a parser
    ambiguity punishes the officer for the tool's limits.
18. **`GET /raids/{id}/credit-gaps`** — people seen in any source for the raid but missing from at
    least one tick, each with the reason. Finalising over these is what a dispute is made of, so the
    screen says so before the officer clicks.
19. **Quakes and draft week.** A quake is detected as a log pattern and **always published by a
    human** — never auto-published at any confirmation count, because a quake banner mobilises the
    guild at unsocial hours. Publishing fires `server.quake`. Draft week is a scheduled window
    carrying the guild's draft position and the snake order: availability is asked by *night* first,
    then targets are ranked, because the targets are not known until the draft happens. Ballots are
    Borda-counted, signed, editable until close, and only officers see who voted for what.
    Withdrawing a target keeps every ballot valid. Recording a pick creates the raid session and the
    calendar event; **nothing about the draft touches the ledger**.
20. **Attendance, both ways.** `raids attended ÷ raids held` is the headline everywhere, with the
    tick ratio served alongside as a secondary column — never instead, because a late arrival should
    not be scored like an absence. Both come from the same window and the same `no_attendance`
    exclusion; `attendance_rollup` and `docs/guides/attendance-and-windows.md` change together.
21. **`GET /dashboard/changes`** — the "since you last looked" digest, per officer, over an opaque
    cursor plus a stored per-officer watermark. Deliberately **not** `?since_seq=`: canonical §4
    keeps that parameter to `/ledger/*`, `/audit` and `/events/replay`, and the digest spans pools,
    where a single `seq` means nothing anyway. Officers dip in and out; the alternative is re-reading
    the ledger to find the one reversal.

**Tests that must exist by the end.** Golden files for every parser (CODEOWNERS-protected, `-update`
refused when `CI=true`) · Playwright journeys 3 and 4 · the attendance **two-implementation
cross-check** (a slow, obviously-correct Go loop versus the SQL, on 50 random member/window pairs
against `seed.Perf`) · tick dedupe under two officers uploading overlapping dumps · a quarantine test
proving no award is ever dropped · statement budgets on attendance and on the reconciliation queue at
3k rows · fuzz targets on every parser · an XSS corpus against `internal/richtext`.

**Docs written during.** `docs/guides/running-a-raid-night.md` ·
`docs/guides/attendance-and-windows.md` (numerator and denominator spelled out — officers argue about
this) · `docs/guides/loot-and-reconciliation.md` · `docs/api/ingest.md` with a complete worked
`raid-submissions` example · `docs/reference/log-formats.md` listing exactly what is parsed and what
is not.

**Exit criterion.** Upload a folder of real `RaidRoster-*.txt` files → sessions reconstructed → ticks
created → attendance percentages match a hand-computed reference → an unknown item name lands in the
queue, is resolved once, and never reappears. Re-uploading the same files creates nothing.

**Demo.** At the end of this phase you can drop an EverQuest log directory in and get back six months
of attendance.

---

## Phase 5 — EQdkp Plus importer and compatibility (≈50 pt, 11%)

**Goal.** A guild can move without losing anything, can *prove* they didn't lose anything, and their
existing bots keep working the day they cut over.

**Deliverables, in order** (fixtures already exist from the Phase 0 lane).

1. `fingerprint.go` (prefix discovery — never assume `eqdkp23_`), `capability.go` (column existence —
   never version strings), `reflect.go`.
2. `readonly.go` MySQL wrapper rejecting anything but `SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN`, with its
   own test.
3. Three ingest entry points converging on one path: live DSN, `mysqldump` via an ephemeral
   `mariadb` container, and the EQdkp ACP backup zip. Plus `dkp export-legacy` for the "MySQL is only
   reachable from the old box" case.
4. Phase-1 staging into `stg_*` tables *in the target database* — SQLite has no schemas and `ATTACH`
   is unsafe in WAL.
5. `phpserial.go`, `sanitize.go`, `mojibake.go` — ~400 lines, byte-level, golden-tested, with a
   differential CI oracle against a maintained library.
6. Declarative typed `TableMap`: unknown column = a log line, missing optional = a default, missing
   required = abort naming both sides.
7. Phase-2 load with a persisted `import_id_map`, dependency ordering, and chunked cursors committed
   in the same transaction as the rows (crash-resumable). **Chunks are ≤ 2,000 rows with an explicit
   yield, and an import refuses to start while any raid is `open`** — a 90-second import on a
   single-writer database otherwise blocks every raid-night write. A write-latency SLO test enforces
   it.
8. Alt-group union-find; the `show_twinks` fork; a duplicate-name policy that never auto-merges two
   characters that both have ledger entries.
9. ACL downgrade mapping — clamp downward, never round up; EQdkp group 2 → `Admin` with **zero
   members auto-assigned**.
10. Account claim flow: Discord email auto-link → one-time claim codes → optional SMTP.
    `password_hash` is never imported.
11. Reconciliation and the classifier (`apa_decay`, `apa_cap`, `apa_start_points`, `orphan_rows`,
    `stale_cache`, `unattributed_adjustment`, `float_rounding`, `twink_mode_mismatch`, `unexplained`)
    and the three reconciliation modes.
12. The verification report — 15 sections, drill-down, JSON + rendered + publicly linkable.
13. Rollback tier 1 (pre-import snapshot + an "Undo this import" button) and tier 2 (logical revert
    by reversal).
14. Delta re-import, pool `mirror` mode, and the **cutover checklist**.
15. **The EQdkp compat shim** — `/api/compat/eqdkp/api.php`, ~700 lines over services that are
    already correct, `Hidden`, deprecated from day one, rate-limited 5× harder, accepting `?atoken=`
    because that is what existing bots send (canonical §7), and resolving legacy `member_id`s through
    `import_id_map`. **It ships in the same phase as the checklist that tells guilds to point their
    bots at it.**
16. The wizard UI, as a pure client of `POST /api/v1/admin/imports`.

**Tests that must exist by the end.** All five fixtures plus the anonymised real-guild dump (with
`PROVENANCE.md` and a deterministic checked-in anonymiser) · invariants I1–I11 · the **classifier
oracle** (`{Δ ≠ 0}` equals the predicted union) as a build-failing assertion · idempotency three ways
(double commit with unchanged head hash, crash-resume byte-identical, interleaved native writes
untouched) · golden dry-run reports · performance budget (500k rows ≤ 90 s at ≤ 512 MB) · write-
latency SLO during import · the mojibake golden set including false-positive guards · compat-shim
golden request/response fixtures captured from real bot traffic shapes.

**Docs written during.** `docs/migration/from-eqdkp.md` (written *as the feature is built*) ·
`docs/migration/what-does-not-migrate.md` — blunt and itemised: passwords, ACL detail, plugins,
styles, APA rule files, forum bridges, EQdkp-as-SSO-provider, `decay_ria`, `format=lua` ·
`docs/migration/parallel-run-and-cutover.md` · `docs/migration/reading-your-verification-report.md` ·
`docs/migration/existing-bots.md`.

**Exit criterion.** For every fixture and for the donated dump, a dry run produces a report whose Δ
set exactly equals the predicted set, and a commit is idempotent, resumable and undoable. `--dry-run`
is the default; `--commit` refuses when any Δ is `unexplained` or exceeds the large-residual
threshold. An unmodified EQdkp-era bot performs `points`, `add_raid` and `add_item` against the shim.

**Demo.** At the end of this phase you can drag an EQdkp backup zip into the wizard and get a report
saying "78 of 81 members reconcile exactly; the other 3 differ by exactly your decay rules, which
live in a PHP file we could not read — here they are" — then send every member a link to their own
statement.

---

## Phase 6 — Bidding, real time, integrations (≈44 pt, 10%)

**Goal.** Close the double-spend factory: the platform owns the rules and the balance; the bot
becomes a dumb terminal. Server-authoritative bid sessions are a 1.0 item by owner decision.

**Deliverables, in order.**

1. `event_outbox` written inside the state-change transaction; `events.Hub` tailer; SSE endpoint with
   multiplexed topics, `Last-Event-ID` replay, a 256-event bounded buffer with resync-and-close, a
   15 s heartbeat, `X-Accel-Buffering: no`. The outbox sequence is **`event_seq`**, never `seq`
   (canonical §4).
2. `POST /events/ticket` (the only JWT in the system) and `GET /events/replay` for pollers.
3. `bid_session` FSM, append-only `bid`, `bid_hold`, `bid_resolution`, `account_lock` (a no-op on
   SQLite, shipped anyway so Postgres stays a driver detail). Live-uniqueness is
   `UNIQUE(item_instance_id) WHERE state IN (…)`.
4. `bids.Supervisor` — in-memory timers, re-armed on boot, 15 s sweep. Deliberately *not* on the job
   queue.
5. Anti-snipe, the tie-break chain written onto the resolution, sealed-bid leakage controls with
   `bid.reveal_early` self-audited.
6. Settle-time revalidation inside the committing transaction → `resolution_failed`, never a
   stale-price award.
7. Webhooks: HMAC signing with rotation overlap, a retry ladder, dead-letter with one-click
   redeliver, `410 Gone` auto-disable, documented in the same OpenAPI document under `webhooks`.
8. **Per-user notification preferences** (`notification_type`, `notification_preference`). Outbid,
   claim-approved and dispute events are opt-in per member, not punted to a bot the guild has to
   write.
9. **The Discord surface, designed now and implemented never (in 1.0).** The webhook event catalogue
   and the token scopes a bot needs are frozen in this phase so a first-party bot is a post-1.0
   client, not a server change. There is no first-party bot in 1.0.
10. SDK helpers that decide whether bot authors use the SDK: webhook verifier, SSE resume helper,
    idempotency-key generator, `Retry-After`/`RateLimit`-aware retry policy.
11. UI: bid board, officer resolve/override screen, webhook admin with the delivery log.

12. **Tiered bidding end to end.** `bid.tier` is derived server-side from the bidding character at
    bid time and **never accepted from a client**; `bid_resolution` persists the winning tier, the
    full trace, the ladder version in force and the visibility mode, so an officer explains an
    outcome months later from that row alone. `item_award` records the tier permanently and never
    re-derives it from the character's *current* main flag — mains change. The leaderboard index
    changes from amount-first to tier-then-amount.
13. **Blind-mode disclosure, as a leakage test.** Before close, and to anyone but an officer, the API
    discloses **exactly one thing**: whether a *higher* tier holds any bids. Not the top bid, not any
    amount, and **not the count in the caller's own tier** — "no bids in Main yet" tells a main they
    win at the floor, which is precisely what sealed second-price bidding exists to prevent. This is
    a leak that will be reintroduced by accident if it is not a test.
14. **The notification matrix**: per member, per event, per channel (in-app / Discord / email), with
    quiet hours that suppress into a digest rather than dropping. **Two rows are locked on** — a
    reversal touching you, and the resolution of a dispute you raised — because a correction you
    cannot see is worse than the mistake. Plus the "connected bots" delegation view: what acts on
    your behalf, which scopes it holds, revocable in one click.

**Tests that must exist by the end.** Property P4 (double-spend across concurrent sessions) as a
seeded state machine over the bid FSM (`testing/quick`, per docs/design/04-testing.md "On the
property framework") · the concurrency test (two goroutines, exactly one `201`, one `409
insufficient_balance`) · Playwright journeys 5 and 12 (two browser contexts plus the polling
fallback) · a 60-client × 30-minute SSE soak with zero dropped `event_seq` and no goroutine growth ·
webhook signature and replay-window tests · the `bid.reveal_early` audit assertion.

**Docs written during.** `docs/guides/auctions.md` · `docs/api/realtime.md` (SSE first, webhooks
second, polling third — with the honest reason: most volunteer Discord bots have no public HTTPS
endpoint) · `docs/api/webhooks.md` · `docs/integrations/discord-bot-quickstart.md` with a working
80-line bot.

**Exit criterion.** Two browsers and one Python bot bid on the same item; anti-snipe extends for all
three simultaneously; settle writes exactly one ledger batch; a retried settle replays; a second
settle without the key returns `409` carrying the existing batch id.

**Demo.** At the end of this phase you can run one live auction in Discord and on the web
simultaneously, with the countdown identical on both because both render `closes_at − server_time`.

---

## Phase 7 — Guild operations (≈52 pt, 12%)

**Goal.** The things a guild does between raids — lending gear, recruiting, swapping mains, agreeing
rules — stop living in Discord pins and officer memory and become part of the record.

Split out of the old Phase 7 in the mockup reconciliation. These five subsystems have almost nothing
to do with articles and portal blocks, they carry real ledger consequences where the CMS carries
none, and together they were more than half of what had become an 80 pt phase.

**Deliverables, in order.**

1. **Guild bank inventory.** `bank_category` with a default disposition and a per-category "who may
   request" rule — a role, *or* an attendance threshold, chosen per category. Categories are
   user-defined; the shipped set is a starting point a guild edits, and the member-facing filters are
   the same categories, so what an officer defines is literally what a member browses.
2. **One item, one disposition.** An item is an auction lot *or* free-as-needed *or* loaned — never
   two lists with different stories. **NO DROP items can be banked while a person holds them**: the
   item physically cannot be traded, so the loot log carries a separate *goes to* axis (`winner` /
   `guild_bank` / `unassigned`) alongside the looter, who becomes the recorded holder. A banked NO
   DROP item is **priced per item by an officer** and posts a real ledger transaction — not zero, not
   a nominal split, because an accounting hole in a zero-sum pool is not a rounding error. This is
   the single most-requested real-world case the incumbent handles badly.
3. **Request and delivery.** `requested → approved → pulled → delivered`, two-sided: the officer
   marks handover, the member confirms receipt, and an officer close **auto-confirms after 72 hours**
   so neither party can stall the other. Anything stuck in `pulled` for a week surfaces on the
   officer dashboard. Without the second confirmation the bank list quietly rots.
4. **Bank auctions** — open (first price, anti-snipe) and blind (sealed, second price), as **recurring
   schedules** with a run-now escape hatch, because a guild's bank auction is a calendar habit rather
   than an ad-hoc event. Reuses the Phase 6 bid machinery; distinct from raid-loot sessions.
5. **Item age.** How long each free-as-needed item has sat unclaimed, six weeks or older in warning
   colour. A bank nobody draws from is a bank nobody knows about.
6. **Main swaps.** The Phase 1 evaluator gains its transaction: a quote is **held at request time**
   and changing a rule afterwards never re-prices a pending or approved swap (same reasoning as
   strategy config snapshots). Guards are separate from cost — 90-day cooldown, 50% attendance floor,
   **two officer approvals** — and approving posts the adjustment batch and moves the main flag **in
   one transaction**, so both reverse together. The member sees the same numbers the officer does
   before asking.
7. **The application form as data.** Question types — short, long, dropdown, multiple choice,
   check-all, true/false, file upload — plus two composites: a **character roster** repeater with
   exactly one main (a recruit brings a stable; asking for one name loses that), and a **rules panel
   whose accept control is locked until the applicant scrolls to the end**. That gate is deliberate
   friction on the one thing guilds always claim was read and never was.
8. **Members read, officers decide.** Feedback is **signed** — anonymous comments cause more damage
   than they prevent — visible to members and officers, hidden from the applicant, with a separate
   officer-only note thread that says on its face it is not visible to the applicant. Applying
   carries the Discord identity through, so acceptance links the listed main instead of starting a
   fresh claim.
9. **The vouch economy.** `pending → paid | void`. A vouch pays out **when the recruit is promoted**,
   not on application and not on trial start, after a 14-day trial at 50% attendance; it is clawed
   back by reversal if they leave inside 30 days, and capped at three per person per year. The payout
   posts **in the same transaction as the promotion**, so no officer has to remember and the pair
   reverses together. The bonus rewards judgement, not introductions.
10. **Policies as one versioned library.** A policy is a document with **addressable clauses**: a
    stable public path (`/policies/dkp`) and a stable anchor per section derived from its heading.
    Anchors survive reordering, so a link an officer pasted a year ago still lands on the right
    clause — that is the point of the feature. Old versions stay readable; "what did the rule say
    when this happened" is the same question the ledger answers about points.
11. **Re-acceptance, per policy per version**, triggered only by a change the author flags
    **material**. Members see a banner until they accept and officers can see who has not; nudges go
    to Discord **once**, not repeatedly. An un-accepted policy **nags only** and blocks nothing — a
    policy that blocks participation becomes a weapon in an argument, which is the opposite of the
    point.
12. **Everything that states a rule links here.** The application's rules panel, the main-swap cost
    explainer, the dispute button on a statement and the bank's free-as-needed note all point at
    policy anchors instead of restating the rule inline.
13. **Item priority lists** — per class and slot, versioned, referenced from the bid board and the
    loot screen, and **advisory**. No warning, no flag, no review queue: bidding out of order happens
    in public and that is enough.
14. UI slices: the bank with its spell shelf and request queue, the recruitment form builder and
    review surface, the policy library, the main-swap policy screen and member quote.

**Tests that must exist by the end.** The 72-hour auto-confirm across a DST boundary · a NO DROP
banked item reconciling to the centipoint in a zero-sum pool · one-item-one-disposition as a
constraint, not a convention · per-category requester rules for both the role and the attendance
form · a held quote surviving a rule change · main-swap approval and flag-move in one transaction,
reversing together · vouch payout transactional with promotion, and clawback as a reversal · the
scroll gate as a Playwright journey · policy anchors stable across a section reorder · a material
change flagging exactly the members who had accepted the prior version.

**Docs written during.** `docs/guides/guild-bank.md` · `docs/guides/recruitment.md` ·
`docs/guides/policies.md` · `docs/guides/main-swaps.md` · `docs/guides/item-priorities.md`.

**Exit criterion.** A member requests an item from the bank and confirms receipt; an officer prices a
banked NO DROP item and the pool still reconciles; an applicant completes the form without an
account, is voted on by members, is promoted by an officer, and the voucher's bonus lands in the same
batch as the promotion; a main swap is quoted, held, approved twice and posted; and a material policy
change re-prompts exactly the members who had accepted the previous version.

**Demo.** At the end of this phase you can settle an argument by sending a link to a clause, and the
guild bank is something members actually draw from.

---

## Phase 8 — Portal, CMS and plugin parity (≈38 pt, 9%)

**Goal.** Full EQdkp Plus site parity, so a migrating guild loses no part of their web presence — not
just their DKP. Owner-mandated scope. Sequenced last of the **feature** phases — ninth of ten, with
only the hardening phase after it — so that if it slips to 1.1, guild adoption is not blocked.

Every surface here is a client of the same public API as everything else. `internal/richtext`
(Phase 4) is the only place HTML is produced from user input, and `internal/cms` is the only package
that holds untrusted rich text.

**Deliverables, in order.**

| # | Deliverable | Notes |
|---|---|---|
| 1 | `article`, `article_category`, `article_tag`, `article_comment`, `article_vote` | Slugs/aliases, `show_from`/`show_to` scheduling, featured flag, read-more and page-break markers. Imported by Phase 5 into tables that now exist. |
| 2 | Editor + preview | Markdown source of truth, `body_md` + `body_html` stored, server-rendered HTML is the only HTML displayed. |
| 3 | Comments and moderation | `cms.moderate` permission, per-category on/off, member reporting, soft-delete. |
| 4 | Media library | Content-addressed storage, drag-and-drop upload, thumbnails, server-side re-encode to WebP ≤ 512×512, **SVG rejected outright**. |
| 5 | Shoutbox | Rate-limited, richtext-sanitised, SSE-delivered over the Phase 6 outbox. |
| 6 | Portal blocks + layouts | `portal_block`, `portal_layout`, per-route and per-category binding. Ships with the standard block set: standings, next raids, last raids, last items, last comments, guild news, whoisonline, birthdays, login, search. |
| 7 | **Leaderboard block** | Top-N per class or per role. One query over `balance_snapshot` grouped by class; visible on every EQdkp install's front page. |
| 8 | Menu manager | `menu_item` tree with permission-gated visibility. |
| 9 | Theme editor | Overrides for the Nocturne **tokens** (canonical §17) with the contrast validator, plus logo, favicon, banner and column widths. Not a LESS compiler and not per-file template overrides — those are deferred. A guild retunes token *values*; it never adds CSS. |
| 10 | Team page | Officers and staff by role, ordered, with per-person blurbs. |
| 11 | RSS/Atom feeds | Per category and all-categories, authenticated by `feed_token`, `feed_token.kind = articles_rss`. |
| 12 | Search extended | Articles, policies and bank items join persons, characters and items in the Phase 4 `/search` index. |

The guild bank, recruitment and item priority lists were here until the mockup reconciliation and are
now **Phase 7**. They post to the ledger and this phase deliberately does not, which is the line the
split was drawn along.

**Tests that must exist by the end.** An XSS corpus (stored, reflected, mutation-XSS, bidi controls,
SVG upload, polyglot image) against every CMS write path, asserting `internal/richtext` is the only
choke point · a lint assertion that `dangerouslySetInnerHTML` appears nowhere in `web/src` · comment
flood and rate-limit tests · media re-encode tests proving EXIF/GPS is destroyed and SVG is rejected
· portal-layout render statement budget (≤ 8 statements for the default layout) · Playwright journeys
"publish an article" · RSS feed validity against a schema · a feed-token
scoping test proving an `articles_rss` token cannot read anything else.

**Docs written during.** `docs/guides/site-and-articles.md` · `docs/guides/portal-layouts.md` ·
`docs/reference/theme-tokens.md`.

**Exit criterion.** A guild imported in Phase 5 has its articles, categories, comments and media
visible and editable; the front page is assembled from portal blocks an officer arranged in the
browser and saved as a server-side resource rather than a browser blob; the theme editor refuses a
class colour under 3:1; and the XSS corpus passes with `internal/richtext` as the single
sanitisation point.

**Demo.** At the end of this phase you can point a guild's domain at this binary and it is their
whole website — news, recruitment, bank, policies, priorities and DKP — not a DKP tool bolted next to
one.

---

## Phase 9 — Hardening, distribution, 1.0 (≈34 pt, 8%)

**Goal.** Convert "it works" into "a volunteer officer installs it, forgets it for eighteen months,
and finds it still running."

**Deliverables.**

1. `dkp doctor` — every check prints *the fix*, not the error: proxy header sanity, SSE buffering
   through the operator's actual proxy, TLS expiry, OAuth redirect mismatch, SMTP, backup freshness,
   clock skew, WAL size, pending migrations, last `verify-ledger` result, top rate-limited tokens.
   The SSE check fetches the operator's own configured public URL through an **explicit
   `safehttp` allowlist entry**, or it reports a false failure on exactly the home-LAN deployments it
   exists to help.
2. `dkp support-bundle` + the **canary redaction test**: seed canary strings, generate the bundle,
   grep every file, any hit fails the build. This is what makes "attach it to a public issue" a fact
   rather than a claim.
3. Backups on by default; `dkp restore` with a dry run; optional S3 target. (Litestream is documented
   as an external option, not supervised as a subprocess — that doubles the image and breaks the
   one-process promise.)
4. Observability: the metric set, `deploy/grafana/`, `deploy/prometheus/alerts.yml`, and the update
   check. `/metrics` is disabled by default on a separate listener with `DKP_METRICS_TOKEN`
   (canonical §14).
5. Distribution: goreleaser binaries for `{linux,darwin,windows} × {amd64,arm64}`, `.deb`/`.rpm` +
   systemd unit, Homebrew tap, Coolify template PR, Unraid CA template, Railway/Fly deploy buttons.
6. **The upgrade ladder**: publish `dkp-refdb:<version>` per release; a nightly job upgrades every
   published minor to HEAD with `verify-ledger` clean and protected-table row counts non-decreasing.
7. Docs pass: the full `/docs` tree served from `embed.FS`, embedded Scalar reference, executable
   snippets, every error `type` URL resolving.
8. Security pass: threat-model document, rate-limit tuning, session and cookie audit, licence gate
   green, `govulncheck` clean, and an external review of `internal/auth` if one can be found.
9. `1.0.0-rc.1` → tester call in the P99 Discord → RC cycle → `1.0.0`.

**Tests that must exist by the end.** The full 1.0 success-criteria suite (below), each as a
build-failing assertion.

**Exit criterion — verified by CI.**

- `make check` ≤ 60 s; full `-race` suite ≤ 90 s; PR CI p50 ≤ 6 min, p95 ≤ 10 min.
- Zero flaky tests; zero retries configured anywhere; empty quarantine.
- Coverage floors met (`ledger` 95%, `strategy` 95%, `auth` 90%, `importer` 85%); diff coverage
  ≥ 80%.
- The authz matrix covers 100% of registered operations with no dead rows; PAT parity green on every
  recorded journey.
- Spec, SDK, sqlc and migration codegen all drift-free; `oasdiff` clean or explicitly labelled.
- Importer: all five fixtures plus the anonymised real dump pass invariants I1–I11 and the classifier
  oracle; 500k rows ≤ 90 s at ≤ 512 MB; double-commit produces an unchanged head hash.
- Upgrade ladder: every published pre-1.0 version upgrades to 1.0 with `verify-ledger` clean.
- Image ≤ 30 MB compressed, multi-arch, signed, SBOM and provenance attested; cold start < 400 ms on
  `seed.Perf`.
- `/standings` at 280 members ≤ 4 SQL statements, p99 ≤ 150 ms on `seed.Perf`.
- Support-bundle canary clean; secret scanning, `govulncheck` and the licence gate clean.
- 60 SSE clients × 30 min: zero dropped `event_seq`, no goroutine growth, RSS ≤ 120 MB.
- The XSS corpus passes against every CMS write path.
- Every documentation snippet executes; every error `type` URL resolves.

**Exit criterion — verified by humans on the RC.**

- **At least two real P99 guilds have completed a parallel run and cut over**, with their
  verification reports publicly linkable. This is the single most important criterion and none of the
  others substitute for it.
- At least one guild's existing Discord bot works unmodified against the compat shim.
- An officer with no Docker experience installs the binary, imports an EQdkp backup, and runs a
  raid — observed, not reported.
- One external review of `internal/auth`.
- `dkp doctor` output reviewed line by line: every failure prints a fix.

**Demo.** At the end of this phase you can tell a guild officer "download this, double-click it,
import your backup, raid tonight" and watch it happen.

---

## The MVP line — what 1.0 contains

| Area | Included |
|---|---|
| Ledger | Append-only, integer centipoints, bitemporal, reversal-only corrections, hash-chained, nightly verified, per-account statement view |
| Strategies | `fixed_price`, `tick`, `zero_sum`, `attendance_weighted`, `decay_percent`, `decay_window`, `cap`, `start_points`, `loot_council`, `roll`, `relative_bid`, `auction_open`, `auction_sealed` (first and second price). `epgp` and `suicide_kings` on pilot-guild request only. |
| Pools | Multiple pools, per-pool strategy and config, event→pool and item-pool→pool mapping, `no_attendance`, per-pool alt policy |
| Roster | person/account/character, alts without sentinels, claims (officer, roster-dump, log-nonce), rename and attribute history, merges, typed custom fields, away mode, **zone keys and flags with provenance**, **computed raid eligibility**, **guild characters with custody windows** |
| Raids | Sessions with N ticks, weighted/standby/bench attendance, 0..N kill credits, connected-raid dedup, finalize plus a dispute window, saved raid templates |
| Ingest | RaidRoster, `/who`, guild dump, loot lines, `/random`, chat grammars; preview-and-commit with a durable `raid_submission`; reconciliation queue; bulk folder import; 180-day artifact retention with `/tell` redaction |
| Loot | Item catalogue, aliases, fuzzy resolution, multi-buyer splits, rot handling, award reversal, priority lists |
| Auctions | Server-authoritative bid sessions, **four-tier bidding where tier outranks amount**, holds, anti-snipe, sealed bids, second price **within the winning tier**, the recorded tie-break chain, officer override before settle |
| Portal / CMS | Articles with an editor, categories, tags, comments, votes, media library, shoutbox, portal blocks and layouts, leaderboard block, menu manager, theme tokens, team page, RSS |
| Guild operations | Guild bank with categories, NO-DROP holder tracking, two-sided delivery and recurring auctions; recruitment with a form builder and signed member feedback; the vouch bonus; versioned policies with stable clause anchors; priced main swaps; advisory item priority lists |
| Raid week | Quake detection with officer publication, draft week with per-night availability and Borda-counted ranked ballots, and a pick that creates the session and the calendar event |
| Frontend | Nocturne design system as the one shipped theme — dark only, tokens with a contrast validator, three officer nav layouts, two density modes, a server-side saved-layout resource, and two phone views |
| API | Full REST + OpenAPI 3.1, scoped PATs on service accounts, idempotency, ETags, cursor sync, RFC 9457 errors, SSE + webhooks + polling, TypeScript and Python SDKs, EQdkp compat shim |
| Auth | Local (argon2id), Discord OAuth, generic OIDC, TOTP enrolment and step-up, one generated permission catalogue, no all-powerful token |
| Migration | EQdkp Plus importer (three entry points, dry-run default, verification report, undo, delta and parallel run), CSV, Sheets file upload, raid-dump backfill |
| Ops | One container / one binary, migrate-on-boot with snapshot and auto-restore, backups on by default, `dkp doctor`, `dkp support-bundle`, `/ops`, metrics |
| Docs | Getting started, migration, officer guides, API reference, error pages, config reference — all served offline by the binary |
| Language | English only. Message-catalogue scaffolding from Phase 3, so 1.1 translations are a data change. |
| Attendance | `raids attended ÷ raids held` as the headline, with the tick ratio served alongside — both computed over the same window, only one the headline |

---

## Deliberately deferred

Published so scope creep is visible. Anything on this list arriving as a feature request gets a link,
not a debate. **No new work item enters a phase after its exit criterion is written.**

| Deferred | Target | Why |
|---|---|---|
| Postgres as a runtime engine | 1.3 | Compile-time enforcement (`pggen` assertion, nightly schema convergence) ships in 1.0 at near-zero cost. The driver, doubled test matrix and second FTS implementation do not. `dkp migrate-engine` will be the most dangerous command in the product. |
| Multi-guild / multi-tenancy | 1.5+, Postgres-only | Single guild per instance, no `guild_id` column (canonical §9). A hoster runs N containers. A deliberate future project, not a retrofit hiding in this schema. |
| Horizontal scale, clustering, leader election | — | 1.0 is single-instance by design. |
| First-party Discord bot | post-1.0 | The webhook catalogue and token scopes are frozen in Phase 6, so the bot needs no server change. |
| The `webhook` point strategy (external rule engines) | 1.2 | Designing an extension interface with zero implementors produces the wrong interface. `PointStrategy` *is* the interface. |
| `epgp`, `suicide_kings` ×3, `bid_council_vote` | on request | WoW-lineage systems rated low-value for P99. `PlanReversal` for Suicide Kings is the hardest single piece of ledger code in the spec. |
| Two-person approval for self-beneficial adjustments | — | Unimplementable against an append-only ledger without a staging table. `actor_is_beneficiary` audit flagging ships in Phase 1 instead (canonical §6). |
| `X-DKP-On-Behalf-Of` delegation | 1.2 | A three-way scope intersection, per-person grants, guild defaults and three error codes is a subsystem, not a header. In 1.0 the bot holds `bids:manage` and records `character_id`; the token is already in the audit trail. |
| OAuth2 client-credentials grant | — | A second token type and a second `Principal` path to solve what PATs already solve. |
| `/embed/standings.js` widget, anonymous SSE, public-standings CORS `*` | 1.1 | Three surfaces with three auth stories. The public standings *page* ships in Phase 3. |
| OpenDKP and EQdkp v1 importers | 1.2 | EQdkp v1 users run EQdkp's own 1.x→2.x upgrader first. |
| `plus_format` raidlog XML parser | 1.2 | The XSD is CC BY-NC-SA (canonical §15). |
| In-process `mysqldump` reader | 1.1 | Its only population is the officer with no Docker *and* a dump *and* no live DB *and* no ACP backup. Tell them to use the ACP zip. |
| Google Sheets API connector | 1.2 | File upload only in 1.0. |
| Forum bridges (all 24), EQdkp-as-OAuth-provider, Steam/Twitch/Battle.net/Facebook login | — | Generic OIDC covers the login cases. Guilds using EQdkp as SSO for a WoltLab forum are warned in `what-does-not-migrate.md`. |
| `format=lua` output, WoW/other game modules, print views, mass mail | — | EverQuest has no addon API. Discord is the notification channel. |
| Plugin/extension system + online repository | — | The API is the extension point. Build outside this repo in whatever language you like; here is the SDK. |
| Free-form LESS, per-file template overrides, the 70-variable style engine | — | Phase 8 ships design tokens with a contrast validator instead. |
| A light theme | 1.1 | Nocturne ships one palette and no screen has been drawn against a light one. Authoring a counterpart means designing a second system and doubling every contrast check (canonical §17). |
| Auto-published quakes | — | Detection is a log pattern; publication is always a human. A quake banner mobilises the guild at unsocial hours, so no confirmation count is enough. |
| Enforced item priorities | — | Advisory by owner decision. Bidding out of order happens in public and that is enough — no warning, no flag, no review queue. |
| Boxing support ("willing to box") | — | Not a supported option under Project 1999 rules; offering the flag implies the guild sanctions it. |
| Localisation beyond English — German in particular | 1.1 | EQdkp Plus is German-first and a large share of its install base is German. **German guilds cannot migrate at 1.0.** Message-catalogue scaffolding ships in Phase 3 so this is a data change, not a rewrite. See R13. |
| Mobile apps | — | The SPA is responsive; that is the answer. |
| Email as a first-class channel | — | SMTP is optional; claim codes are the universal path. |
| Game-data seeding (item stats, icons) | — | `dkp-p99-seed` is a separate, optional, user-run repository, never a core dependency (canonical §15). |
| Non-P99 game modules (TAKP, Quarm, retail) | 1.4 | Near-free via `GameDataProvider`, but not a 1.0 target. |
| Litestream as a supervised subprocess; `autocert` | — | Both documented as external options. `autocert` generates support tickets exclusively from the 5% who enable it. |
| Visual regression baselines | — | CI-generated baselines plus CODEOWNERS re-baselining is the golden-file-tampering pathology with worse signal. Replaced by DOM assertions. |
| Mutation testing as a gate | — | `gremlins` is 0.x with no gate, no baseline and no diff mode. The 12 ledger properties do the work. |
| Telemetry, even opt-in | — | Update check only. A payload viewer, rotatable id, settings toggle, receiving endpoint and privacy page for zero users. |
| Docs versioning, Pagefind, a second embedded search index, `llms-full.txt`, screenshot generation | 1.2 | One markdown tree served from both places, no versioning (old binaries link to their own `/docs`), browser find. |
| `item_trigram` as a hand-built table; `balance_history` daily rollup; `server` as a first-class dimension | — | Two fuzzy-search mechanisms where FTS5 + Levenshtein over top-N suffices; 220k rows/year for a chart nobody requested; the `server` column stays, the per-server index and multi-server UI do not. |

---

## Critical path and parallel lanes

**Critical path (serial, one lane, must not be parallelised):**

```
P0 store + migrations + gates
  → P1 ledger + invariants
    → P2 auth + permissions + API middleware
      → P4 raid / tick / attendance domain
        → P5 importer + compat shim
          → P6 bid sessions + outbox/SSE
            → P9 RC
```

Everything on that path shares two files that cannot be edited concurrently without pain:
**`db/schema.hcl`** and the ledger service. Batch schema changes into deliberate "schema PRs" with one
owner at a time.

**Phases 3, 7 and 8 are not on the critical path.** Phase 3 can run alongside the back half of
Phase 2. Phase 7 needs the ledger (Phase 1) and the bid machinery (Phase 6) but nothing after them.
Phase 8 depends only on `internal/richtext` (Phase 4) and the outbox (Phase 6) and can slip to 1.1
without blocking a guild's adoption — which is precisely why it is sequenced ninth of ten, last among
the feature phases.

**Fully parallel lanes** (different agents, near-zero conflict surface):

| Lane | Depends on | Touches | Can start |
|---|---|---|---|
| Parsers | Nothing — pure `[]byte → struct`, stdlib only | One file + one golden dir per format | Phase 0 |
| EQdkp fixtures (PHP installers in Docker → GHCR OCI artifacts) | Nothing | `test/fixtures/eqdkp/**` + one workflow | Phase 0 |
| Distribution (goreleaser, Unraid/Coolify/Railway templates, systemd, Homebrew) | A binary exists | `deploy/**`, `.goreleaser.yaml` | Phase 0 |
| Importer transforms (`phpserial`, `sanitize`, `mojibake`, `TableMap`) | Nothing — pure functions with golden tests | `internal/importer/*.go` | Phase 0 |
| Strategy implementations | The `PointStrategy` interface is frozen | One file + one test file per strategy | Phase 1, after item 6 |
| Seed data generator | Domain services exist per layer | `internal/seed` | Phase 1 (ledger), extended P2, P4 |
| `dkp doctor` checks | Each check is independent | `internal/doctor/*.go`, one file per check | Phase 2 |
| SDK ergonomics (webhook verifier, SSE resume, retry policy) | `openapi.json` is stable | `clients/**`, non-generated files only | Phase 2 |
| Docs narrative (guides, concepts, migration walkthrough) | The feature's API shape is frozen | `docs/**` | Per-phase |
| Server-rendered surfaces (`/ops`, public standings) | Read APIs exist | `internal/ops/*.tmpl` | Phase 3 |
| Portal blocks | The block interface is frozen | One file + one test per block | Phase 8, after item 6 |
| Design-system components | The token layer exists | One file + one test per component in `web/src/components` | Phase 3, after item 1 |
| Quakes and draft week | Calendar and parsers exist | `internal/draft/*`, its own routes | Phase 4, after item 13 |

**Conflict-avoidance rules, enforced by repo layout rather than by asking nicely:**

- Routes are registered **one file per resource** in `internal/api/` — never a shared registry file,
  which would conflict on every parallel feature PR.
- Generated files (`openapi.json`, `sqlitegen/`, `web/src/api/`, `clients/`) are **regenerated on
  rebase, never merged**. A conflict in a generated file means `make gen`, not a manual resolution.
- Strategies, doctor checks, parsers, portal blocks and importer transforms are all "one new file
  plus one new test file" shapes on purpose.
- Migration files are append-only and numbered. Two agents adding migrations concurrently is fine;
  two agents editing `schema.hcl` concurrently is not.

---

## Risk register

| # | Risk | Signal it is happening | Mitigation (mechanical where possible) | Phase |
|---|---|---|---|---|
| R1 | **Importer fidelity** — balances don't match and the guild loses faith on day one | Δ set ≠ predicted set on any fixture; any `unexplained` classification | The classifier oracle as a **build-failing** assertion; five synthetic fixtures plus one anonymised real dump; `--dry-run` default; `--commit` refuses on unexplained or large residuals; three reconciliation modes so the honest answer is always available; the per-member statement link as the trust artifact | P0 fixtures / P5 |
| R2 | **Point-math disputes** — "the site says 412, I had 430" | Any nightly `verify-ledger` drift; any dispute unanswerable from the statement | Balances are `SUM` over an append-only log; the snapshot is a cache verified nightly with a drift alarm, and load-bearing rather than droppable (ADR-0023), so the replay that verifies it is itself a control; bitemporal `effective_at`/`recorded_at` answers "what did it say last Tuesday"; decay is posted, not computed; every batch carries its `config_snapshot` and `rng_seed`; 12 properties + 95% coverage floors | P1 |
| R3 | **Scope creep** — now larger, because the CMS is in scope | Issues that grow a phase after its exit criterion is written; "while we're in there" PRs | The deferred table above is published here and linked from the issue template; "parity gap" is a *label*, not a promise; the hard rule that no work item enters a phase after its exit criterion is written; Phases 7 and 8 are sequenced last among the feature phases precisely so they can slip without blocking adoption; the mockup reconciliation added ≈114 pt and every item of it is named in this file rather than absorbed quietly | All |
| R4 | **Adoption / trust** — guilds don't switch | Demo traffic without imports; imports without cutovers | The compat shim makes every existing P99 bot work on cutover day (and now ships *with* the cutover checklist); parallel run and `mirror` mode make trying it zero-risk; undo-this-import; the export story round-tripped in CI as a first-run feature; publicly linkable verification reports; **two pilot guilds recruited before Phase 4, not at RC** | P4–P8 |
| R5 | **Maintainer burnout** | CI p50 creeping past 6 min; Renovate backlog; unread parser-bug issues | A ≤4 h/week budget with named line items; `ci-budget.yml` files an issue when the SLO breaks; the deletion rule (any check nobody has acted on in 90 days is removed); zero-retry flake policy with a 7-day time-boxed quarantine; automerge on low-risk deps; releases are "merge the Release PR" | All |
| R6 | **`riversqlite` maturity** — still an early preview, explicitly "needs more vetting to be considered fully production ready" | Stuck jobs, lost periodic runs, dead letters with no cause | Exact version pin; a six-method `jobs.Queue` interface so replacement is a day of work; nightly 10k-job soak; the job table lives in the file the officer backs up and is visible at `/admin/jobs`; bid timers deliberately not on the queue. **Spike the ~200-line hand-rolled alternative in Phase 1** — if it is a day, make it the default | P1 |
| R7 | **Parser formats are wrong at first** — ~12 P99 log strings are unverified assumptions | The reconciliation queue fills with `unparsed_line` | The golden-file harness makes a fix a one-line test addition; `raw_extra` and defensive splitting instead of assumed column counts; the in-app "report this line as a parser bug" button files a pre-filled, redacted issue, so every user becomes a fixture contributor. **Collect 20 real dumps from the P99 Discord in week one, not in Phase 4** | P4 |
| R8 | **An agent weakens a test to go green** | Loosened assertions, rewritten golden files, raised budgets in a feature PR | A test-diff analyser flagging removed assertions, `cmpopts.Ignore*` and raised budgets, forcing CODEOWNERS review; one-way ratchets; `-update` refused when `CI=true`; a non-decreasing fixture-count test; oracles (properties, reference implementations, the spec, `EXPLAIN` goldens) that cannot be edited invisibly; the `test-relax:` commit prefix convention | P0 |
| R9 | **AGPL contamination from EQdkp source** | Any `pdh_`, `gen_class`, `plus_exchange`, `__multidkp2event` identifier outside the two allowlisted files | The CI grep gate; stated in `AGENTS.md`, `CONTRIBUTING.md` and the PR template; class/race/zone tables shipped as our own literals; the importer reads a database, never transcribes DDL or ports PHP. The risk peaks in Phases 5 and 8, where the task is literally "match EQdkp's behaviour" | P0, peaks P5/P8 |
| R10 | **Security of hand-rolled auth** — ~900 lines you own, with no CVE feed | — | The authz matrix as the single highest-value suite; argon2id only; opaque tokens; no all-powerful token by construction; TOTP enrolment in Phase 2 so the step-up gates are real; `govulncheck`, licence gate and secret scanning; an external review of `internal/auth` before RC; the support-bundle canary test so incident reporting doesn't leak | P2 / P9 |
| R11 | **SQLite-only caps the future** | A hosted offering, or a guild large enough to matter | The `Queries` interface, the `pggen` compile assertion and nightly Postgres schema convergence ship in 1.0 at ~zero cost; the four schema decisions (integer micros, centipoints, ULID text keys, never query into JSON) make the port cheap; the three genuine divergences (`seq` allocation, bid-hold lock, FTS) are named in `db/RECIPES.md` on day one | P0 |
| R12 | **Bus factor on Huma** (one primary maintainer) | Upstream goes quiet | It is a codegen layer over `net/http`, forkable in a weekend; pin exactly; the spec is a committed artifact, so a fork changes nothing downstream | P2 |
| R13 | **German parity regression** — EQdkp Plus is German-first and a large share of its install base is German. An English-only product is a *regression* for the incumbent's actual users | German-language issues; migration enquiries that stop at "is there a German UI?" | Say it loudly in the README and in `what-does-not-migrate.md` rather than discovering it in a launch thread; message-catalogue scaffolding from Phase 3 with an ESLint rule failing on bare JSX strings, so 1.1 German is a data change; recruit a German-speaking translator during the RC | P3 |
| R14 | **The CMS (Phase 8) and the public application form (Phase 7) are the largest untyped blast radius in the product** — untrusted rich text, and file uploads from unauthenticated applicants | Any XSS-corpus failure; any HTML produced outside `internal/richtext` | `internal/richtext` ships in Phase 4, four phases before its heaviest consumer, with its grep gate; `dangerouslySetInnerHTML` banned by lint; images re-encoded server-side and SVG rejected outright; CSP `script-src 'self'` with no `unsafe-inline`; the XSS corpus is a 1.0 exit criterion; the application form's upload path is content-addressed, re-encoded and size-capped like the media library, and rate-limited because it needs no account | P4 / P7 / P8 |
| R15 | **The empirical premises are never checked** — roughly twenty load-bearing claims in this design are assumptions about P99 guilds, not findings | Phase 4 starts with zero pilot guilds; the `dkp.exe` premise is still unpolled at Phase 2 | `docs/development/verify-before-phase-0.md` is a checklist with named experiments, worked *during* Phase 0. **Two pilot guilds recruited before Phase 4 is a gate, not an aspiration** — most of those twenty premises are answerable by two officers in a Discord channel and unanswerable by any further design work | P0 / P4 |

---

## What v1.x looks like

Additive only inside `/api/v1`. Each release is a `docker pull` and a restart with no manual step.

| Release | Theme | Contents |
|---|---|---|
| **1.1 — Sharp edges** | What the first ten guilds hit | Parser fixes from `parser-bug` issues, each arriving as a golden file; importer findings from real installs; reconciliation-queue ergonomics; `dkp doctor` checks born from support threads; officer-workflow polish; the standings embed widget; German translation; the in-process `mysqldump` reader |
| **1.2 — Extensibility** | Stop being the bottleneck | The `webhook` strategy transport; richer webhook coverage; `plus_format` raidlog XML importer; OpenDKP importer; Google Sheets API connector; `X-DKP-On-Behalf-Of` delegation; docs versioning |
| **1.3 — Postgres** | Open the ceiling | Postgres as a supported runtime engine: driver wiring, full integration matrix, `tsvector` search, `REVOKE UPDATE, DELETE` on the ledger role, a real `account_lock`. `dkp migrate-engine --to postgres://…` with a mandatory dry run and a verification report modelled on the importer's — treated as the most dangerous command in the product and tested like one |
| **1.4 — Beyond P99** | Cheap portability | TAKP / Quarm / retail game-data providers via `GameDataProvider`; era and expansion packs as data; server-agnostic parser registration |
| **1.5+ — Multi-guild** | Postgres only, RLS only | `guild_id` as a leading column on every composite unique, Postgres RLS as the primary control with the query lint as a tripwire, `Queries` methods taking `guild_id` first so omission is a compile error. Explicitly a **hoster** feature, not a guild feature |

**The v1.x discipline.** `/api/v1` never breaks. A breaking need mints `/api/v2` inside an app
*minor*, with v1 alive ≥ 18 months carrying `Deprecation` and `Sunset` headers and a per-operation
metrics counter so an admin can see whether anything still calls it. The app major bumps only when an
operator must take a manual action — and the goal for the whole 1.x line is that it never does.
