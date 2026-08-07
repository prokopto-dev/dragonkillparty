# Backend contract — what the mockups assume

**Status:** reconciled 2026-08-07 against `e5d9059`. **Audience:** anyone implementing a screen.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md).

Companion to [`10-ui-decisions.md`](10-ui-decisions.md). That file records **why** the UI behaves as
it does; this one records **what the UI expects of the server**. It was written from the mockups
before anyone read the design documents, so every row started as a claim to test.

**They have now been tested.** The verdicts are below; the rest of this file is the original
contract, kept because the reasoning behind each claim is worth more than the claim.

## The reconciliation verdicts

Using this file's own vocabulary: **UI is right** (the backend needs the capability) · **backend is
right** (redraw the screen) · **neither** (designed around a constraint that no longer exists).

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 0 | Tiered bidding | **UI is right** — nothing exists | `bid` has no tier column and `ix_bid_session` is `(session_id, amount_cp DESC, seq ASC)` — amount-first by construction. `bid_resolution` carries a prose `tie_break_reason` and cannot record a winning tier, a ladder version or a visibility mode; `item_award` cannot record the tier at all. |
| 1a | Ledger is append-only | **backend is right** ✓ | Triggers, hash chain, `verify-ledger` and the nightly replay are all specified. No work. |
| 1b | `seq` is global to the instance | **backend is right — redraw** | Canonical §4: `seq` is **per pool**; `event_seq` is the global one. An instance-wide as-of marker is not meaningful. |
| 1c | `as_of_seq` on standings | **UI is right** — partial gap | It exists on `/accounts/{id}/balance`; `/standings` has no such parameter. Canonical §4 now sanctions it there. |
| 2 | Server time on the wire | **backend is right** ✓ | Canonical §2 already mandates it and the SSE frame shape is frozen with it. No work. |
| 3 | Holds are first-class | **backend is right** ✓ | `bid_hold` is a real table with `ux_hold_active`, and `GET /accounts/{id}/spendable` already returns `{balance, holds, spendable, as_of_seq}`. Spendable is already computed in one place. |
| 4 | Blind disclosure rule | **neither** — new | The one-disclosure rule only exists once tiers do. Ships as a leakage test alongside item 0. |
| 5 | `table_view` resource | **backend is right** ✓ | Already planned as a real API resource. Only the 12-column grid shape needed pinning. |
| 6 | The invented resources | **UI is right** — all absent | `quake`, `draft_ballot`, `vouch`, `custody`, `character_key`, `main_swap`, `bank_request` appear nowhere in the domain model or the API design. Confirmed by grep, not inferred. |
| 7 | Bank posts to the ledger | **backend is right** ✓ | `guild_bank` is already a system account. |
| 8 | Attendance denominator | **both must be served** | The guide states `ticks ÷ qualifying ticks held`; §12 below settles the headline as `raids ÷ raids held`. Both are computed; only one is the headline. |

**The short version:** the ledger, holds, server time and `seq` machinery are sound and need almost
nothing. The gap is new domain surface — nine subsystems — plus tiered bidding, plus the frontend.

---

Read the rest as the original set of claims. Where a mockup asserts
something the backend does not do, one of three things is true, and the last column of every
table below is the one you have to decide:

- **UI is right** — the backend needs the capability.
- **Backend is right** — the mockup is lying and should be redrawn.
- **Neither** — the interaction was designed around a constraint that no longer exists.

Nothing here was read out of the Go source. It was derived from `docs/`, `ROADMAP.md` and
`.claude/rules/web.md`, then extended wherever a screen needed something the docs did not
settle. Treat every "assumed" row as unverified.

---

## 0. The shape of the whole thing

The frontend is a single-page app against a versioned JSON API. There is no server-rendered
page in the product except **first run** ([`mockups/first-run.dc.html`](mockups/first-run.dc.html)), which is served by the binary
before the SPA exists and is reachable only with the one-time console token.

Four assumptions run through every screen:

1. **The ledger is append-only and balances are a `SUM`.** No screen ever offers an edit of a
   posted entry. Corrections are reversal batches. If the backend permits an in-place update
   anywhere, a mockup is misleading and should change.
2. **`seq` is a monotonic ledger cursor, global to the instance.** The UI prints it as an
   as-of marker in ten places (dashboard, statement, standings, raid detail, bank). It must be
   readable, stable, and comparable across resources.
3. **Money is integer centipoints.** Every display is `x.xx`. No float ever reaches the client.
4. **Reads are public; writes are permissioned.** Standings, statements, raid history and loot
   are readable by anyone who can see the site. Roles gate writes, not reads. Two categories of
   read are exceptions: member contact details and anything under `/ops`.

---

## 1. Resources the UI expects to exist

Grouped by the screen that needs them. `?` marks a resource I invented for a mockup and never
saw named in the docs — highest-value spike targets.

| Resource | Needed by | Confidence |
|---|---|---|
| `person`, `character` | Everything | Documented |
| `account` (person × pool balance) | Standings, statement | Documented |
| `pool` with a strategy config | Settings, standings switcher | Documented |
| `ledger_batch`, `ledger_entry` | Statement, adjustments, audit | Documented |
| `raid_session`, `tick`, `attendance` | Raids, raid detail | Documented |
| `event_type` with slain patterns | Event types catalogue | Documented |
| `item`, `item_alias`, `award` | Loot log, item search | Documented |
| `submission`, `artifact` | Ingest, raid artifacts | Documented |
| `reconciliation_entry` | Reconciliation queue | Documented |
| `bid_session`, `bid`, `hold` | Bid sessions, bid board | Roadmap phase 6 |
| `dispute` with a thread and an outcome | Disputes | Roadmap |
| `role`, `permission`, `service_account`, `pat` | Ranks, settings | Documented |
| `webhook_endpoint`, `webhook_delivery` | Webhooks | Roadmap |
| `article` (news) with categories | News, portal | Roadmap phase 8 |
| `calendar_event`, `signup` | Calendar | Documented |
| `raid_template` | Raid templates | Roadmap |
| `application`, `application_form`, `application_answer` | Recruitment | ? |
| `vouch` with a bonus state machine | Recruitment bonus | ? |
| `bank_item`, `bank_request`, `bank_delivery` | Guild bank | ? |
| `bank_auction` (open and blind) | Bank auctions | ? |
| `character_key`, `character_flag` | My Characters, raid forming | ? |
| `main_swap_request` with a price quote | Main swaps | ? |
| `guild_character` custody window | Shared characters | ? |
| `policy` + `policy_acceptance` | Policies | ? |
| `quake`, `draft_week`, `draft_ballot` | Quakes, draft week | ? |
| `table_view` (saved layout) | Portal customisation, member tables | ? |
| `portal_block`, `menu_item`, `theme`, `media`, `feed_token` | Portal admin | Roadmap phase 8 |
| `custom_field` + values | Custom character fields | Roadmap |
| `notification_preference` | Member settings | Roadmap |
| `away_window` | Away mode | Roadmap |

---

## 2. Endpoints the screens imply

Only where the shape matters. Everything is `/api/v1`.

### Reads that must be cheap

| Screen | Call | Notes |
|---|---|---|
| Standings | `GET /standings?pool=&sort=&filter=&cursor=` | Sort and filter are **server-side**. The UI never sorts 82 rows locally, because it is designed for 250+. Returns `seq`. |
| Statement | `GET /accounts/{id}/statement?cursor=` | Reverse-chronological, running balance per row. The running balance is computed server-side; the client must never accumulate it. |
| Members table | `GET /people?fields=&filter=&cursor=` | Column selection is a query parameter — the saved-view feature depends on it. |
| Raid list | `GET /raids?state=&cursor=` | Cursor paging, never offset. |
| Item search | `GET /items?q=` | Must search aliases, not just canonical names. |
| Global search | `GET /search?q=&scope=` | One endpoint across persons, characters, items, raids, articles. Returns typed groups. |
| Standings as-of | `GET /standings?pool=&as_of_seq=` | Reproduces the table at any past `seq`. This is the ledger claim made visible — if it cannot be served, the append-only promise is unproven. |
| Raid credit gaps | `GET /raids/{id}/credit_gaps` | People seen in any source for the raid but missing from at least one tick, with the reason |
| Officer notes | `GET/POST /applications/{id}/notes` | Officer-visibility only, never returned to the applicant's own view |
| Bank item age | on `bank_item` | `unclaimed_since` — the free-as-needed list sorts and warns on it |
| Draft projection | `GET /draft/{id}/projection` | Given ballots so far and historical pick order, what this guild lands. Server-computed; the client must not model draft strategy |
| Since last visit | `GET /dashboard/changes` (cursor + watermark; **not** `since_seq` — canonical §4) | Diff of what changed, per officer, with a mark-as-read |


### Writes that must be idempotent

Every mutating call in the UI carries an `Idempotency-Key`. Three screens show the key on
screen deliberately (new raid dialog, adjustments, import) because they are the ones an
officer might double-submit under raid-night conditions.

| Action | Call | Idempotency scope |
|---|---|---|
| Post adjustments | `POST /ledger/batches` | Client key |
| Commit a submission | `POST /submissions/{id}/commit` | Submission id — a second commit is a no-op, not an error |
| Take a tick | `POST /raids/{id}/ticks` | `(raid, tick_number)` |
| Run decay | `POST /jobs/decay/run` | `(pool, period)` — a scheduler firing twice decays once |
| Resolve a bid session | `POST /bid_sessions/{id}/resolve` | Session id |
| Commit an import | `POST /imports/{id}/commit` | Import id |

### Calls with unusual requirements

**`POST /submissions`** (dry run). Returns a *durable, re-fetchable* preview. The ingest screen
lets an officer walk away and come back; the preview cannot be in-memory server state. It must
survive a restart and be addressable by `submission_id`.

**`POST /bids`**. Must place a hold in the same transaction that accepts the bid. The response
carries the bidder's new spendable balance so the board can update without a refetch.

**`POST /bid_sessions/{id}/resolve`**. Revalidates every hold *inside* the committing
transaction. On failure the session moves to `resolution_failed` — it does not award at a stale
price and does not silently pick the next bidder. The response carries the full tie-break trace
that the UI renders.

**`POST /people/{a}/merge/{b}`**. Refused when both sides carry ledger entries. The response is
a description of what moved (characters re-parented, entries re-attributed, attendance
recomputed) because the UI shows that as a preview before and a receipt after.

---

## 2b. Tiered bidding — the contract

The largest single change since the first draft, and the thing most likely to be wrong in the
Go model. Test this first.

**`bid` carries a tier**, resolved server-side at bid time from the bidding character, never
sent by the client:

```
tier = 1  character.is_main AND spec = primary
tier = 2  character.is_main AND spec ≠ primary
tier = 3  character.person = bidder AND NOT is_main
tier = 4  otherwise
```

**Resolution is two-phase, in one transaction:**

1. `SELECT MIN(tier) FROM bids WHERE session = ? AND state = 'accepted'` — that tier wins.
2. Within it: highest amount, then raid attendance (30d), then balance before the bid, then
   timestamp, then a seeded roll with the seed persisted on the batch.

**Price is second-price within the winning tier**: runner-up in that tier + one increment, or
the winner's own bid if they are alone in it. Never a figure from a lower tier.

**The resolution row must persist**: winning tier, the full trace, the ladder version in force,
and the visibility mode. An officer explains an outcome months later from this row alone.

**Visibility is pool config, versioned.** `blind` (default for the target guild) or `open`. A
session in flight keeps the rules it opened under. Under `blind` the API must not leak, to
anyone but an officer, before close: the top bid, any bid amount, or the *count* of bids in the
caller's own tier. A lower-tier bidder may be told that a higher tier has bids — that is the
one disclosure the UI makes, because otherwise they bid into a wall.

**Economics shift.** Main-tier bids land in single or low double digits because mains only
compete with mains. Minimum 5.00, increment 1.00. Any fixture with three-figure main-tier bids
is modelling the old scheme.

**`award` records the tier permanently.** The loot log filters on it and reports on it. Do not
derive it at read time from the character's *current* main flag — mains change.

---

## 3. Real-time

The dashboard, raid detail and bid board all show live state. The assumed model, from
`.claude/rules/web.md`:

- **SSE is the primary channel**: `GET /api/v1/events` with a resource filter.
- **Polling is the fallback** where a proxy buffers SSE. The diagnostics screen has a check for
  exactly this, and prints the nginx fix.
- **Webhooks are third**, for bots with a public endpoint — most volunteer bots do not have one.

Events the UI subscribes to: `raid.tick_created`, `raid.finalized`, `ledger.batch_posted`,
`bid_session.bid_placed`, `bid_session.outbid`, `bid_session.resolved`, `item.awarded`,
`claim.approved`, `dispute.opened`, `news.published`, `server.quake`.

**Countdowns are computed from `closes_at − server_time`, never the client clock.** Every
payload carrying a deadline must also carry the server's current time, or the client must have
an established offset. This is the single most likely place for the mockups and the backend to
disagree, and it is worth a spike on its own: a bid board that trusts the client clock is
exploitable.

---

## 4. Auth and permissions

**Identity is Discord OAuth**, with a local password path for the owner account created at
first run. A character is *claimed*, not asserted — the claim flow issues a phrase the member
must produce in-game, and an officer approves.

**Effective capability is `role permissions ∩ token scopes`.** The ranks screen states this
explicitly. Tokens belong to *service accounts*, not people, so a bot survives its officer
leaving. No all-powerful token exists.

**Step-up re-authentication** is required, within five minutes, for:

- minting or revoking an API token
- editing a role or a permission
- downloading a backup or a support bundle
- committing an import
- bulk-reading member contact details

The mockups treat that list as closed and advertised. If the backend gates a different set, the
docs and the UI both need to change — an unenforced advertised gate is worse than no gate.

---

## 5. Invariants the UI renders as facts

Each of these is printed on a screen as though it is guaranteed. If the backend does not
guarantee it, the screen is lying to an officer.

| Claim on screen | Where | What must be true |
|---|---|---|
| "rebuilt 03:14 with zero drift" | Dashboard | A nightly full replay from `seq` 0 reproduces every balance |
| "hash chain intact → 88,241" | Dashboard | Batches are hash-chained and verifiable |
| "10 / 10 invariants passing" | Dashboard | There is a named, countable invariant suite |
| "Balances are a SUM over an append-only log" | Dashboard | No mutation path exists |
| "one live session per item instance" | Bid sessions | Enforced by constraint, not convention |
| "a 10-point main bid beats a 350-point alt bid" | Bid board | Tier is compared before amount, always |
| "nobody sees a number until close" | Bid board | Blind mode leaks no amount and no in-tier count before close |
| "any past seq reproduces exactly" | Standings as-of | `as_of_seq` is served, not approximated |
| "the same points cannot win two auctions at once" | Bid board | Holds are transactional |
| "82 of 82 accounts reproduce their source total" | Import | Per-account parity check after float→centipoint normalisation |
| "credits sum to exactly the debit" | Zero-sum split | Largest-remainder allocation; residue routed explicitly (the UI shows it going to `guild_bank`) |
| "a second commit is a no-op" | Ingest | Submission-scoped idempotency |
| "re-uploading the same file is a no-op" | Ingest | Artifacts are content-addressed |
| "any member can download the dump behind any tick" | Raid detail | Artifacts are readable by members, retained 180 days |
| "runs are idempotent on (pool, period)" | Decay | Enforced by constraint |

---

## 6. Where the mockups most likely diverge

Ordered by how much rework a mismatch causes. This is the reconciliation-spike backlog.

**0. Tiered bidding.** See §2b. If the backend resolves bids by amount alone, every auction
screen, the loot log's tier column, and the whole price scale are wrong. This outranks
everything below it.

**1. Bid sessions and holds.** The largest invented surface. A hold is a first-class object
with its own lifecycle, and spendable balance is `balance − Σ holds`. If the backend models
bidding as a plain award at resolve time, the entire bid board is wrong — every balance figure
on it, plus the "spendable" column on standings and the member profile.

**2. Server time on the wire.** See §3. Affects bids, auctions, draft ballots, signup cutoffs,
dispute windows.

**3. The `table_view` resource.** Saved layouts — the portal widget grid, saved member-table
views, the blocks arrangement — are stored server-side as API resources, explicitly *not*
`localStorage`. This is stated as a rule in [`10-ui-decisions.md`](10-ui-decisions.md) §0. If the backend has no such
resource, either build it or accept that customisation is per-browser and redraw the
Customise mode.

**4. Guild bank as a ledger participant.** The bank holds items and runs auctions, and the
zero-sum residue is routed to it. That implies `guild_bank` is an *account*, not a label. Check
whether the backend can post to a non-person account.

**5. Character keys and flags.** The raid-forming story ("who can we bring to ST?") depends on
keys being declared, typed and queryable. If they are free text or a JSON blob, the filter
does not work and the My Characters screen is decorative.

**6. Main swap pricing.** The UI shows a *quote* — a price computed from policy (period,
biannual allowance, level-60 or cleric discount) before the member commits. That needs a
`GET /main_swaps/quote` that is honest about which discounts applied and in what order. Whether
discounts stack is still open (see [`10-ui-decisions.md`](10-ui-decisions.md)).

**7. Guild-character custody.** Log lines from a shared character must be attributed to whoever
held it in that window. That requires custody windows with timestamps and an attribution rule
at parse time, not a post-hoc officer correction.

**8. Vouch bonus state machine.** `pending → paid | void`, with the payout posted **in the same
transaction as the promotion**, and a clawback reversal if the member leaves inside 30 days. If
promotion is not transactional with a ledger post, the bonus becomes a cron job and the "no
officer has to remember" promise fails.

**9. Application forms as data.** Question types, ordering, file uploads, and the
scroll-to-accept rules panel imply a form schema resource plus per-answer storage. Not a blob.

**10. Policy acceptance.** Versioned policies with per-member acceptance records, and
re-acceptance triggered only by a *material* change. That means a policy version needs a
`material` flag set by the author.

---

## 7. Things the UI deliberately does not need

Worth stating so the spike does not build them:

- **No plugin system.** The API is the extension point. The settings screen says so.
- **No template editing.** Theme is tokens only. No per-file overrides — that was EQdkp's
  seventy-variable style engine and the reason nobody could upgrade.
- **No forum, no PM, no avatars.** The import screen lists these as not migrating.
- **No client-side sorting or filtering of large tables.** All server-side.
- **No SVG uploads.** Media is re-encoded to WebP; SVG is rejected as a script container.
- **No password storage beyond the owner account.** Everyone else is Discord.

---

## 8. Suggested spike order

1. **Ledger and `seq`** — confirm append-only, confirm the cursor is global and stable, confirm
   the nightly replay exists. Everything else sits on this.
2. **Server time and SSE** — cheap, and it de-risks four screens.
3. **Bid sessions and holds** — the biggest single unknown.
4. **`table_view`** — decides whether customisation is real or per-browser.
5. **Character keys / custom fields** — decides whether raid forming is a feature or a mock.
6. **The invented resources** in §1 marked `?` — each is a small schema question with a large
   screen behind it.

---

## 9. Files in this project

| File | What it is |
|---|---|
| [`mockups/admin-console.dc.html`](mockups/admin-console.dc.html) | Officer surface — 30+ screens, three nav layouts |
| [`mockups/guild-portal.dc.html`](mockups/guild-portal.dc.html) | Member surface, including the phone views |
| [`mockups/my-characters.dc.html`](mockups/my-characters.dc.html) | Character management, keys, main swaps |
| [`mockups/public-site.dc.html`](mockups/public-site.dc.html) | Guest landing, application form, guest/claim view |
| [`mockups/first-run.dc.html`](mockups/first-run.dc.html) | Setup wizard — server-rendered, pre-SPA |
| [`10-ui-decisions.md`](10-ui-decisions.md) | Why each behaviour is the way it is |
| [`11-ui-backend-contract.md`](11-ui-backend-contract.md) | This file — what the frontend expects of the server |
| the design project's sync record | Repo association and screen map for one-click sync |
