# Dragon Kill Party documentation

**Status:** pre-1.0. Nothing here is implemented yet. Every page describes behaviour the
implementation must satisfy; where a page documents something that does not exist, it says so at the
top. See [ROADMAP.md](../ROADMAP.md) for what lands when.

This page is a router. It contains no unique content on purpose — duplicated install instructions
rot, and the page a reader lands on should be the page that is maintained.

## Start here

### You raid in a guild that uses this

| | |
|---|---|
| What the numbers mean | [Glossary](concepts/glossary.md) |
| Why your balance is what it is | [The ledger](concepts/ledger.md) |
| How your attendance percentage is calculated | [Attendance and windows](guides/attendance-and-windows.md) |
| How bidding works | [Auctions and bid sessions](guides/auctions.md) |
| Why your alt's points are on your main | [Roster, mains and alts](guides/roster-and-alts.md) |

### You are an officer

| | |
|---|---|
| Pick a point system, or check the one you inherited | [Choosing a DKP system](guides/choosing-a-dkp-system.md) |
| Run a raid from form-up to finalised | [Running a raid night](guides/running-a-raid-night.md) |
| Settle an attendance argument | [Attendance and windows](guides/attendance-and-windows.md) |
| Award loot, fix a mistake, clear the queue | [Loot and reconciliation](guides/loot-and-reconciliation.md) |
| Run an auction | [Auctions and bid sessions](guides/auctions.md) |
| Decide who can do what | [Permissions for officers](guides/permissions-for-officers.md) |
| Move your guild off EQdkp Plus | [Migrating from EQdkp Plus](migration/from-eqdkp.md) · [what does not migrate](migration/what-does-not-migrate.md) |

### You run the instance

| | |
|---|---|
| Install with Docker | [Install with Docker](getting-started/install-docker.md) |
| Install a plain binary | [Install a binary](getting-started/install-binary.md) |
| First boot, bootstrap token, first guild | [First run](getting-started/first-run.md) |
| Every `DKP_*` setting | [Configuration](operations/configuration.md) |
| Upgrade safely, back up, restore | [Upgrade and backup](operations/upgrade-and-backup.md) |
| Something is broken | [Troubleshooting](operations/troubleshooting.md) |
| Run the import for real | [Migrating from EQdkp Plus](migration/from-eqdkp.md) · [parallel run and cutover](migration/parallel-run-and-cutover.md) |
| Report a vulnerability | [SECURITY.md](../SECURITY.md) |

### You are building against the API

| | |
|---|---|
| First authenticated request | [API getting started](api/getting-started.md) |
| Tokens, scopes, the capability floor | [Auth and scopes](api/auth-and-scopes.md) |
| Retries, `Idempotency-Key`, `If-Match` | [Idempotency and concurrency](api/idempotency-and-concurrency.md) |
| Cursors, `since_seq`, catching up | [Pagination and sync](api/pagination-and-sync.md) |
| SSE and polling | [Realtime](api/realtime.md) · [Webhooks](api/webhooks.md) |
| What a failure looks like | [Errors](api/errors.md) |
| A working bot, end to end | [Discord bot quickstart](integrations/discord-bot-quickstart.md) |
| Why the API is shaped this way | [API design](design/02-api-design.md) · [decisions](adr/README.md) |

## The tree

Navigation is [Diátaxis](https://diataxis.fr)-shaped: pages are grouped by what the reader is doing,
not by which feature they concern.

### Getting started — learning

| Page | For |
|---|---|
| [First run](getting-started/first-run.md) | The six-step wizard and what each choice commits you to |
| [Install with Docker](getting-started/install-docker.md) | One container, one volume, one port |
| [Install a binary](getting-started/install-binary.md) | Windows, macOS, Linux, systemd |

### Guides — doing a task

| Page | For |
|---|---|
| [Running a raid night](guides/running-a-raid-night.md) | Officer, during and after a raid |
| [Choosing a DKP system](guides/choosing-a-dkp-system.md) | Officer, before the first raid |
| [Roster, mains and alts](guides/roster-and-alts.md) | Officer, ongoing |
| [Attendance and windows](guides/attendance-and-windows.md) | Officer settling a dispute |
| [Loot and reconciliation](guides/loot-and-reconciliation.md) | Officer, after a kill |
| [Auctions and bid sessions](guides/auctions.md) | Officer running a bid |
| [Permissions for officers](guides/permissions-for-officers.md) | Admin assigning roles |

### Concepts — understanding

| Page | Answers |
|---|---|
| [The ledger](concepts/ledger.md) | Why your balance can be trusted and why nothing is ever edited |
| [Point strategies](concepts/strategies.md) | What a strategy is allowed to do, and what it is not |
| [Invariants](concepts/invariants.md) | The rules no configuration can switch off |
| [Glossary](concepts/glossary.md) | DKP vocabulary and EverQuest/P99 vocabulary in one place |

### Operations — running the instance

| Page | For |
|---|---|
| [Configuration](operations/configuration.md) | Every environment variable |
| [Upgrade and backup](operations/upgrade-and-backup.md) | The safe upgrade path and the restore drill |
| [Troubleshooting](operations/troubleshooting.md) | Symptom → cause → fix |

### Migration — leaving EQdkp Plus

| Page | For |
|---|---|
| [Migrating from EQdkp Plus](migration/from-eqdkp.md) | The whole import |
| [What does not migrate](migration/what-does-not-migrate.md) | Read this before you promise anything |
| [Reading your verification report](migration/reading-your-verification-report.md) | Proving nothing was lost |
| [Parallel run and cutover](migration/parallel-run-and-cutover.md) | Running both sites for a fortnight |

### API and integrations

| Page | For |
|---|---|
| [Getting started](api/getting-started.md) | Mint a token, make a request |
| [Auth and scopes](api/auth-and-scopes.md) | The scope catalogue and the capability floor |
| [Idempotency and concurrency](api/idempotency-and-concurrency.md) | Safe retries |
| [Pagination and sync](api/pagination-and-sync.md) | Cursors and `since_seq` |
| [Realtime](api/realtime.md) | SSE and polling |
| [Webhooks](api/webhooks.md) | Delivery, signing, retries |
| [Errors](api/errors.md) | The RFC 9457 shape and the code catalogue |
| [Discord bot quickstart](integrations/discord-bot-quickstart.md) | Build your own bot; there is no first-party bot in 1.0 |

### Contributing

| Page | For |
|---|---|
| [Inner loop](development/inner-loop.md) | Clone to green `make check` |
| [Architecture overview](development/architecture-overview.md) | Where code goes and which law governs it |
| [The first ten PRs](development/first-ten-prs.md) | Phase 0, in order |
| [Verify before Phase 0](development/verify-before-phase-0.md) | Assumptions to test before writing code |
| [Phase 0 PR 5 decisions](development/phase-0-pr5-decisions.md) | The authz catalogue, the first integration test, and why PR 5 split |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | DCO, the AGPL firewall, review expectations |
| [AGENTS.md](../AGENTS.md) | The contract every agent loads before writing code |

### Design and decisions

Design documents are the specification, written for contributors. They are longer, denser and more
argumentative than the guides above.

| Page | Subject |
|---|---|
| [00 Canonical conventions](design/00-canonical-conventions.md) | **Normative.** The tie-breaker for every other document |
| [01 Domain model](design/01-domain-model.md) | Entities and DDL |
| [02 API design](design/02-api-design.md) | Resource map and semantics |
| [03 Security](design/03-security.md) | Threat model and controls |
| [04 Testing](design/04-testing.md) | Layers, budgets, golden files |
| [05 Migration](design/05-migration.md) | Importer internals |
| [06 CI/CD and release](design/06-cicd-and-release.md) | Pipelines, gates, publishing |
| [07 Documentation system](design/07-documentation-system.md) | How these pages are built, linted and shipped |
| [08 nParse Plus integration](design/08-nparse-plus-integration.md) | The desktop log companion |
| [09 Frontend and design system](design/09-frontend-and-design-system.md) | **Normative.** Nocturne — tokens, type, components, and the officer/member IA |
| [10 UI decisions](design/10-ui-decisions.md) | Why each screen behaves the way it does |
| [11 UI backend contract](design/11-ui-backend-contract.md) | What the screens expect of the server, with the reconciliation verdicts |
| [Mockups](design/mockups/) | Five HTML surfaces, ~55 screens. Read the screen before building it |
| [Decision records](adr/README.md) | Why, including the downsides |

## Reference pages are generated

The reference tree is produced by `make gen` from the code, not written by hand, so it cannot
drift. Each page lands with the subsystem it describes; **✅ marks the ones that exist**.

| Reference page | Generated from | Phase |
|---|---|---|
| `reference/configuration.md` | the `Config` struct tags | 2 |
| ✅ [`reference/permissions.md`](reference/permissions.md), [`reference/scopes.md`](reference/scopes.md) | `internal/authz/catalogue.go` | 2 |
| `reference/errors/<code>.md` | the closed error-code enum | 2 |
| `reference/strategies/<id>.md` | each strategy's `ConfigSchema` | 1 |
| `reference/invariants.md` | the `Invariants()` registrations | 1 |
| `reference/api` | `openapi/openapi.json`, rendered by Scalar | 2 |
| `reference/webhooks.md` | the OpenAPI `webhooks` block | 6 |
| `reference/cli/*.md` | the Cobra command tree | 8 |
| `reference/log-formats.md` | the parser registry plus golden fixtures | 4 |
| `reference/eqdkp-mapping.md` | the importer's `TableMap` structs | 5 |

## Licence

Documentation is CC BY 4.0. Code is [Apache-2.0](../LICENSE). The name and logo are not licensed —
see [TRADEMARK.md](../TRADEMARK.md).
