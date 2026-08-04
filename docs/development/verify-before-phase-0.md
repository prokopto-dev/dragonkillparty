# Verify before Phase 0

**Status:** open checklist. **Audience:** owner, contributor.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`.

Everything below is an **assumption**, not a finding. The design documents state these as facts. They
are not facts. They are plausible claims about Project 1999 guilds, about tooling behaviour, and
about performance, made by people writing a design document rather than by anyone who measured
something or asked an officer.

Fifteen are listed. The adversarial review counted roughly twenty load-bearing empirical premises in
the design; these are the ones where being wrong changes an architectural decision, a phase boundary,
or a shipped guarantee. The rest are cheap to be wrong about.

Two properties make this list worth working through rather than filing:

1. **Every experiment here is cheap.** The most expensive is one afternoon. Several are one Discord
   message. None requires writing product code.
2. **Every one of them gets more expensive to answer later, and much more expensive to act on.**
   Item V1 is answerable today for the cost of a poll and unanswerable-in-practice once Phase 1 has
   put the ledger in Go.

Work this file *during* Phase 0, not after. Tick the boxes in place; record the answer inline under
the item, including the date and who asked. An item whose answer is recorded but which contradicts
the design is a **bug report against the design**, not an inconvenience — say so in an issue and link
it here.

Risk `R15` in [`ROADMAP.md`](../../ROADMAP.md) is this file.

---

## The two that matter most

These two are not first because they are hardest. They are first because between them they resolve
most of the other thirteen, and because both are answered by talking to people rather than by
building anything.

### V1 — P99 officers want a double-clickable `dkp.exe` on a Windows raid PC

- [ ] Post one poll in the P99 Discord: **"How does your guild host its website today?"** Options:
      shared PHP host · a VPS you administer · a VPS someone else administers · Docker on a VPS ·
      a machine in someone's house · no site, Discord only.

**Load-bearing on:** the entire stack tie-break. This premise is what broke the tie toward Go, and
through Go it justifies SQLite as the only 1.0 engine, `FROM scratch`, the no-container test story,
`go:embed` for the SPA, the six-platform goreleaser matrix, and the `.deb`/`.rpm`/Homebrew/Unraid
distribution work in Phase 8. It is the single most load-bearing empirical claim in the project and
nobody has checked it.

**Cost:** one message. 48 hours for a usable sample.

**Resolve by:** before Phase 0 exits. Phase 1 makes the ledger expensive to move.

**If it comes out the other way** — the majority already run Docker on a VPS, or have no site at all:

- The decisive argument for the single-binary candidate evaporates, and the contributor-pool cost of
  Go over TypeScript or Python is no longer paid for by anything.
- This does **not** automatically mean rewrite. Go and SQLite remain defensible on operational
  grounds (one process, one file, one backup, no runtime). But the tie was broken on a premise that
  would then be false, and the honest response is to re-run the comparison with the real distribution
  mix in hand, in Phase 0, while the answer is still cheap.
- Phase 8 reprioritises regardless: the Coolify / Unraid / Railway templates become the primary
  install path and the Windows `.exe` becomes a nicety rather than the headline.
- The 1.0 human exit criterion "an officer with no Docker experience installs the binary" gets
  rewritten to match whatever the poll actually says officers do.

### V2 — Two pilot guilds can be recruited, and will answer questions

- [ ] Post in the P99 Discord and DM officers of five guilds. Ask for two who will commit to three
      specific things: answer questions in a shared channel, donate an anonymised database dump, and
      run a parallel run at RC.

**Load-bearing on:** roughly twenty premises in this design — log formats, guild scale, whether
officers want the platform to own bidding, whether `dkp.exe` matters, whether anyone will donate a
dump, what a decay rule actually does in practice. **Every one is answerable by two officers in a
Discord channel and unanswerable by any further design work.** No amount of additional design
converts an assumption into a finding.

**Cost:** a week of asking. It is not a task an agent can do.

**Resolve by:** **before Phase 4 begins.** ROADMAP R4 and R15 both make this a gate, not an
aspiration.

**If it comes out the other way** — nobody signs up:

That is the most important signal the project will receive, and it is not a scheduling problem. Do
not route around it by proceeding to Phase 4 on the assumption of demand: Phase 4 is the largest
phase in the plan, and every log format it parses is currently an unverified string (V10). The
options, in order:

1. Narrow to one guild — ideally one you raid in — and accept a sample of one, stated as such.
2. Reorder: build Phase 5 (the importer) first and let a working, verifiable import of a real guild's
   database be the recruiting pitch. A verification report is a more persuasive ask than a roadmap.
3. Stop. A DKP platform with no pilot guild is a hobby, which is a legitimate thing to build — but
   then the 324-point plan, the parity scope and the compat shim are all sized for a product that has
   users, and should be cut accordingly.

---

## The rest

### V3 — Guild scale is ~280 characters, ~3,400 raids, ~520k ledger entries

- [ ] Ask two large guilds to run three `SELECT COUNT(*)` statements against their EQdkp database
      (`__raid_attendees`, `__items`, `__adjustments`) and paste the numbers.

**Load-bearing on:** `balance_snapshot`, `attendance_rollup`, the single-writer design, the
`seed.Perf` profile, and every statement and latency budget in the plan.

**Cost:** one message.

**If it comes out the other way** — real guilds are 5× larger: the snapshot-plus-single-writer design
needs re-examining before Phase 1, `seed.Perf` is regenerated at the real scale, and the `/standings`
budget (V5) is measured against that instead. Being wrong *downward* costs nothing; being wrong
upward invalidates every budget in the roadmap simultaneously.

### V4 — A template-DB clone costs ~0.3 ms, so integration tests are cheap

- [ ] In PR 2, benchmark `newDB(t)` over 200 iterations and print the p50 in the CI log. Extrapolate
      to the planned ~900 integration tests with response validation on every response.

**Load-bearing on:** the entire testing strategy. "Prefer integration tests over unit tests because
they are nearly free" is the thesis the test pyramid inverts on.

**Cost:** it falls out of PR 2 at no extra cost.

**If it comes out the other way** — 3 ms rather than 0.3 ms: 900 integration tests become ~27 s of
clone time alone, `make check ≤ 60 s` is unreachable, and the pyramid has to be re-drawn with a real
unit-test layer. Better to learn this at 50 tests than at 900.

### V5 — `/standings` answers in ≤ 4 SQL statements at ≤ 150 ms p99 on SD-card storage

- [ ] Hand-write the four queries against a generated 520k-row ledger and measure, **before any API
      exists**. This is ROADMAP Phase 1 item 11 (`seed.Perf` v1) and it is deliberately scheduled
      before the API so the answer can still change the schema.

**Load-bearing on:** whether `balance_snapshot` and `attendance_rollup` survive at all, and on a 1.0
exit criterion.

**Cost:** one afternoon.

**If it comes out the other way:** either the snapshot cache becomes load-bearing rather than a
droppable optimisation — which weakens the "balances are derived" argument and needs saying out loud
in `docs/concepts/ledger.md` — or the standings page gets a materialised path with its own staleness
story. Both are cheaper to decide now than after Phase 3 builds a UI on the fast path.

### V6 — Atlas preserves hand-added triggers, partial indexes and CHECKs across `migrate diff`

- [ ] In PR 3: add a trigger and a partial index to `dkp_meta`, change an unrelated column, re-diff,
      and inspect the generated migration.

**Load-bearing on:** the append-only guarantee. The ledger triggers are the mechanism behind the
project's central invariant, and "Atlas authors, goose applies" only holds if Atlas does not quietly
drop what it did not author.

**Cost:** one hour.

**If it comes out the other way:** every schema change becomes a risk to the append-only trigger, and
the pipeline needs an explicit preservation strategy — hand-maintained trigger migrations appended
after each Atlas diff, plus a test asserting all four triggers exist after every migration. That test
should probably exist regardless; if Atlas drops triggers, it is mandatory and it ships in PR 3
rather than PR 9.

### V7 — Huma v2 emits OpenAPI 3.1 `webhooks` that all three consumers accept

- [ ] Emit one placeholder `webhooks` entry in PR 4; in PR 6 run `openapi-typescript`,
      `openapi-python-client` and Scalar over the committed document.

**Load-bearing on:** the "one document" promise — that webhooks are documented in the same OpenAPI
file as the REST surface, generated into both SDKs, and rendered in the embedded reference.

**Cost:** 30 minutes.

**If it comes out the other way:** webhooks get a second, separate document and a second rendering
path, `docs/api/webhooks.md` becomes hand-written rather than generated, and the SDK helpers in
Phase 6 lose their generated types. Survivable, but it should be a known cost in Phase 0 rather than
a discovery in Phase 6.

### V8 — EQdkp Plus PHP installers still run in Docker in 2026, and APA rules live only on disk

- [ ] Build **one** fixture (2.3.39) on day one by running the real PHP installer in Docker. While
      it is up, inspect `data/<md5>/eqdkp/apa/apatab.php` and the `__config` rows.

**Load-bearing on:** two separate things, resolved by the same hour of work. First, the entire
importer fixture strategy (five versions built from real installers, published as OCI artifacts —
ROADMAP's Phase 0 parallel lane). Second, the central premise of the reconciliation classifier: that
APA decay/cap/start-points rules are unreadable because they live in a PHP file, which is why the
importer's remediation message says "re-enter your rules".

**Cost:** one day for the build, one hour for the inspection.

**If the installers do not run:** the fixture strategy needs a different source — donated dumps
(V9), hand-written SQL schemas reconstructed from their repository, or fewer versions — and Phase 5
is materially longer. This changes a phase estimate, so it must be known before the estimate is
published as a commitment.

**If any APA state *is* in the database:** the classifier gets much stronger, `apa_decay` / `apa_cap`
/ `apa_start_points` become explained rather than residual categories, and the "re-enter your rules"
message may be avoidable entirely — which is a direct improvement to the strongest part of the
migration story.

### V9 — A real guild will donate an anonymised production dump

- [ ] Ask three guilds now, not at Phase 5. Offer the anonymiser script and a look at the output
      before anything is committed.

**Load-bearing on:** the importer test suite. The testing design calls the anonymised real dump "the
most valuable fixture in the repo" and makes it a PR-time gate.

**Cost:** three messages, folded into the V2 conversation.

**If it comes out the other way:** the reconciliation classifier is only ever exercised against
synthetic data that this project generated — which means it is only ever tested against the failure
modes this project already imagined. Mitigation: make the first pilot guild's dry-run report the
fixture, accept that it lands in Phase 5 rather than Phase 0, and say plainly in
`docs/migration/from-eqdkp.md` that the classifier has not seen real-world mess.

### V10 — The ~12 unverified P99 log-line formats are correct

- [ ] Post in the P99 Discord in **week one** asking for 20 real `RaidRoster-*.txt` files, `/who`
      pastes, loot lines and `/random` output. Every donated file becomes a golden fixture.

**Load-bearing on:** all of Phase 4. The unverified strings include `/who` headers, RaidRoster
trailing columns, `/random` wording, `tells the raid`, and the Green/Red server filename tokens.

**Cost:** one message, then an ongoing trickle.

**If it comes out the other way:** guessing a regex produces **silently wrong attendance**, which is
worse than an error and which no test catches, because the test is written from the same guess.
`AGENTS.md` already forbids inventing a format: an unverified format ships as a golden fixture marked
`unverified` plus an issue, never as a shipped regex. This item is the reason that rule exists.

### V11 — `?atoken=` plus eight functions is what existing bots actually send

- [ ] Read the source of Castle Steward, bidbot2 and jDKP; grep for `api.php` call sites and record
      the exact query parameters, functions and response shapes each one uses.

**Load-bearing on:** the compat shim (ROADMAP Phase 5, ~700 lines) and on the 1.0 human exit
criterion "at least one guild's existing Discord bot works unmodified".

**Cost:** one hour.

**If it comes out the other way:** you write different 700 lines. This experiment does not decide
whether the shim ships — it decides *which* functions it implements, which is the difference between
a shim that works on cutover day and one that returns 404 to the only three bots anyone runs. Do it
before writing a line of the shim.

### V12 — Guilds want auctions to be server-authoritative

- [ ] Ask five guilds two questions: how does loot actually get allocated on a raid night today, and
      would you hand the balance check to the platform instead of your bot?

**Owner decision, already locked:** server-authoritative bid sessions **are** in 1.0. This experiment
therefore no longer decides *whether* — it decides *shape*.

**Load-bearing on:** the bid FSM, the anti-snipe and tie-break rules, the sealed-bid modes, and
whether the officer-override screen is the primary path or an escape hatch. Roughly 15% of the build,
and the only subsystem carrying a real concurrency-correctness burden.

**Cost:** five conversations, folded into V2.

**If it comes out the other way** — guilds are happy with their bots and will not hand over the
balance: the bid subsystem still ships, but its emphasis changes. The holds API and the ledger
integration become the product (bots call them and stay in charge of the UX), and the web bid board
becomes the secondary surface rather than the headline. That is a UI-sequencing change inside
Phase 6, not a scope change.

### V13 — The initial route fits in 250 KB gzipped

- [ ] Build the empty shell in PR 6 — React 19 + Router + Query + Virtual, plus one 12-column
      virtualised table — and have `advisory/bundle-size` print the measured number into the CI
      summary.

**Load-bearing on:** a committed budget file that every later UI PR is measured against.

**Cost:** it falls out of PR 6.

**If it comes out the other way:** the budget becomes a permanent "raise the budget" ratchet, which
is the same as having no budget. Set it from the measurement, make raising it require a CODEOWNERS
review, and if the empty shell already exceeds 250 KB, decide in Phase 0 whether the answer is a
different router, route-level code splitting, or an honestly larger budget.

### V14 — `riversqlite` is production-ready enough for the job queue

- [ ] Spike the hand-rolled alternative — roughly 200 lines: `BEGIN IMMEDIATE` claim, retry with
      backoff, periodic scheduling — in **one day, in Phase 1**.

**Verified against sources, and it is bad news:** `riverdriver/riversqlite` remains an early preview
whose own documentation says it needs more vetting to be considered production ready, at roughly a
quarter of the Postgres driver's throughput. This is the one item on this list that has already been
checked, and it came out the other way.

**Load-bearing on:** decay runs, attendance rollups, webhook delivery, the import pipeline, and the
nightly verification job. ROADMAP R6.

**Cost:** one day.

**If the spike takes a day:** make the hand-rolled queue the default and River the optional upgrade.
The six-method `jobs.Queue` interface exists precisely so this is a swap rather than a rewrite — and
an interface wrapper you never cash is insurance you paid for and threw away. Bid timers are already
deliberately off the queue, so the blast radius of getting this wrong is bounded but not small.

### V15 — The pinned toolchain versions exist and behave as described

- [ ] In Phase 0, pin and then actually exercise: Go 1.26, `testing/synctest`, `os.Root`, Vite 7,
      Huma v2, and the OpenAPI validator. Record the version that CI actually resolved, not the
      version the design named.

**The Go half is answered, and it holds (asked by Courtney, 2026-08-04, in PR 1):** `go.mod` declares
`go 1.26`, and the toolchain actually installed and exercised locally is **Go 1.26.5**
(darwin/arm64, Homebrew). CI does not pin a version of its own — `.github/actions/setup-toolchain`
resolves Go from `go-version-file: go.mod`, so it installs some 1.26.x patch release; **which one is
not yet known, and the first CI run on this PR is what records it.** One consequence worth writing
down: the action's `1.24` pre-Phase-0 fallback branch is now unreachable, because it is gated on
`go.mod` not existing and `go.mod` exists.

**Still open, because nothing here has been exercised yet:** `testing/synctest`, `os.Root`, Vite 7,
Huma v2, and the OpenAPI validator — including the stale-claim question below, which is the part of
this item that can still change a committed dependency.

**Load-bearing on:** `make setup` reproducing, and on `kin-openapi` specifically — the testing design
prescribes replacing it with `pb33f/libopenapi-validator` on the grounds that kin-openapi lacks
OpenAPI 3.1 support, but current sources indicate it supports 3.0, 3.1 and 3.2. The committed
dependency choice is downstream of a claim that appears to be stale.

**Cost:** it falls out of PR 1 and PR 4.

**The general form of this item, which matters more than the specific versions:** the design labels
several claims "verified" that are not. Treat every "verified" annotation in
`docs/design/**` as unverified until someone re-runs it, and when you do re-run one, record the date
and the source inline. A claim labelled verified that isn't is worse than an unlabelled claim,
because it stops anyone from checking.

---

## Tracking

Tick the checkbox in the item above; record the outcome and the date here.

| # | Assumption | Resolve by | Blocks | Outcome |
|---|---|---|---|---|
| V1 | Officers want `dkp.exe` | Phase 0 exit | the stack tie-break | open |
| V2 | Two pilot guilds recruitable | **before Phase 4** | Phase 4 entry | open |
| V3 | Guild scale ~280 / 3,400 / 520k | week 1 | `seed.Perf`, all budgets | open |
| V4 | Template-DB clone ~0.3 ms | PR 2 | the test pyramid | open |
| V5 | `/standings` ≤ 4 statements, ≤ 150 ms | Phase 1 | `balance_snapshot` survival | open |
| V6 | Atlas preserves triggers | PR 3 | the append-only guarantee | open |
| V7 | Huma 3.1 `webhooks` consumable | PR 4 / PR 6 | the one-document promise | open |
| V8 | EQdkp installers run; APA on disk only | week 1 | the fixture lane, the classifier | open |
| V9 | A guild donates a real dump | Phase 0 | importer test realism | open |
| V10 | The ~12 P99 log formats | week 1 | all of Phase 4 | open |
| V11 | `?atoken=` + eight shim functions | before Phase 5 | the compat shim | open |
| V12 | Guilds want server-side auctions | before Phase 6 | bid UX emphasis | open |
| V13 | 250 KB gzipped initial route | PR 6 | the bundle budget | open |
| V14 | `riversqlite` maturity | Phase 1 | the job queue | **resolved: early preview, not production ready — spike the alternative** |
| V15 | Pinned versions and "verified" labels | Phase 0 | `make setup`, the validator choice | **partially resolved (2026-08-04): Go 1.26 pinned via `go.mod`, 1.26.5 exercised locally; CI's resolved patch, `testing/synctest`, `os.Root`, Vite 7, Huma v2 and the validator all still open** |

Nothing on this list is a blocker to *starting*. V2 is a blocker to entering Phase 4, V6 is a blocker
to merging PR 9, and V1 is a blocker to publishing the stack decision as settled. The others change
numbers, not directions.
