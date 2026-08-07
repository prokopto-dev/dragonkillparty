# UI decisions

**Status:** settled. **Audience:** anyone implementing a screen.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md). Where this
file and that one disagree, that one wins and this file has a bug.

Written alongside the mockups in [`mockups/`](mockups/) and imported here whole. It is the *why*
behind every screen: [`09-frontend-and-design-system.md`](09-frontend-and-design-system.md) says
what things look like, [`11-ui-backend-contract.md`](11-ui-backend-contract.md) says what the server
must do, and this says why the interaction is the shape it is.

Two corrections applied on import, both from
[`11-ui-backend-contract.md`](11-ui-backend-contract.md)'s verdict table:

- **`seq` is per pool**, not global. Where this file describes an as-of marker spanning pools, the
  screen is scoped to one pool instead.
- **Phase numbers in §11b are pre-rewrite.** `ROADMAP.md` now runs to ten phases: the old Phase 7
  split into Guild operations (7) and Portal & CMS (8), and hardening became 9. Read §11b for *which
  phase specifies a feature*, not for the number.

Companion to the mockups in this project. Every rule below is a **behavioural** decision the
screens encode; the "why" matters more than the pixels, because the pixels will change and
these should not. Where a decision follows from the repository's own design docs, the source
is named — those are not up for renegotiation in the UI.

Mockups: [`mockups/admin-console.dc.html`](mockups/admin-console.dc.html) · [`mockups/guild-portal.dc.html`](mockups/guild-portal.dc.html) · [`mockups/my-characters.dc.html`](mockups/my-characters.dc.html) ·
[`mockups/public-site.dc.html`](mockups/public-site.dc.html)

Surfaces and their screens:

| Surface | Screens |
|---|---|
| Admin console | Dashboard · Raid sessions · Raid detail · Ingest · Events calendar · Quakes & drafts · Adjustments · Item & loot log · Guild bank · Members · Characters & alts · Ranks & permissions · Guild characters · Main swap policy · News & articles · Recruitment · Policies · Import · Audit log · Settings |
| Guild portal (members) | Home (widget grid) · Standings · Member statement · Raids · Loot · Draft · Bank · Calendar · News · Recruits · Items · Policies · Mobile |
| My characters | Character editor · zone keys · raid eligibility · main swap · change history |
| Public site | Landing (visitor and signed-in-guest states) · Application form |

---

## 0. The two rules everything else hangs off

1. **A UI need is an API change.** Nothing in these mockups is browser-only. Saved table
   layouts, custody windows, main-swap quotes and bank requests are all resources a bot can
   read and write. (`.claude/rules/web.md`)
2. **Nothing is edited; things are appended.** Every corrective action in the UI is worded as
   a *reversal*, never an edit, and the original stays visible and struck through.
   (`docs/concepts/ledger.md`, ADR-0002)

Consequences that show up everywhere: money is `*_centipoints` integers formatted at display
time; ratios are `_bp`; a balance is quoted **as of a `seq`**, never a timestamp; every write
carries an `Idempotency-Key`.

---

## 1. Visibility model

**Balances and statements are public.** Standings, raid history, loot and the statement view
are readable by anyone, signed in or not. Secrecy about points generates more drama than it
prevents.

What authentication buys is not *reading*, it is **acting**:

| Principal | Can read | Can do |
|---|---|---|
| Anonymous visitor | Standings, raids, loot, item search, public news | Apply to raid |
| Discord login, no character claimed | Same as anonymous | Claim a character, apply |
| Member with a claimed character | Everything except officer tooling | Sign up, bid, request from the bank, comment on recruits |
| Officer / raid lead / loot master | + admin console by role | Per the permission matrix |

The guest dashboard (`Public Site` → Guest) states this explicitly in a two-column grid
rather than silently hiding controls — a member who cannot find the signup button should be
told *why*, not shown a smaller site.

**Claiming a character** is a proof, not a form field. Three accepted proofs, any one
sufficient: officer approval, the character appearing in a raid dump during the claim window,
or a one-time phrase spoken in `/guild` that the parser sees. (`docs/concepts/glossary.md` —
"Claim") A character only appears in the unclaimed list once a dump or an import has seen it.

---

## 2. Admin console

**Navigation is a two-level appliance model** — an icon rail of six groups (Overview, Raids,
Points, Loot, Roster, Portal, System) driving a section sidebar. Three layouts ship behind one
control (`rail` / `hybrid` / `top`) because the right answer depends on screen width and
officer habit; `rail` is the default. Density is a separate axis (`comfortable` / `compact`)
that retunes the spacing token rather than restyling tables.

**The dashboard leads with system health, not vanity metrics.** Ledger integrity (nightly
rebuild drift, hash chain head, invariant count), live raid telemetry, API client status and
an explicit "needs an officer" queue. A guild officer's first question at 11pm is "is anything
broken", not "how many points exist".

**Ingest is preview-then-commit, always.** The dry-run panel is durable and re-fetchable, and
`on_unresolved` defaults to `quarantine` — never `create`, never a silent drop. Uploads are
content-addressed so two officers uploading overlapping dumps deduplicates rather than
double-credits. (`docs/guides/running-a-raid-night.md`)

**Ticks void, they never delete.** The raid-detail tick strip has three visual states —
committed, retroactive, voided — because all three are ordinary and all three must stay on
the record.

**Adjustments require a reason.** The composer will not post without one, and the field is
labelled "Reason (required)" rather than validated after the fact, because the row lands on
someone's public statement with the officer's name on it.

**Import is dry-run by default**, produces a reconciliation report that names everything that
did *not* land, and commit requires step-up re-authentication. The balance-parity check is
shown as a first-class result, not a footnote: the migration's whole claim is "your numbers
came across".

---

## 3. Characters, keys and raid forming

**Level and class are derived; keys and flags are declared.** Raid dumps carry level and class
title, so the UI treats those as machine-confirmed and shows the artifact that confirmed them.
Everything a dump cannot see — zone keys, faction, willingness to tank — is self-reported and
labelled as such, with an officer-verified state above it.

**Key state is a three-way cycle**, not a checkbox: `have` / `working on it` / `missing`.
"Working on it" is the common real state (4 of 7 Sky keys) and collapsing it to a boolean
makes the roster data lie.

**Provenance follows state.** Changing a key's state rewrites its provenance line to
"self-reported just now · awaiting verification". A stale "verified by Thessaly · 12 Jul" under
a value the member just changed is worse than no provenance at all.

**Eligibility is computed, never stored.** The raid-eligibility table derives from
`level >= minLevel && keyState === 'have'` per upcoming raid, so flipping a key immediately
changes which raids the member qualifies for. This is the feature: officers form
level/key-restricted raids from declared data instead of asking in guild chat.

**Signups are per character**, and the picker shows eligibility inline. A person signs up as
*Grimwald*, not as themselves.

**"Willing to box" was deliberately removed** — boxing is not a supported option under
Project 1999 rules, and offering the flag implies the guild sanctions it.

---

## 4. Main swaps

A main swap is a *priced, approved* event, not a profile edit.

**Cost model** — a base cost with an ordered rule stack:

| Order | Condition | Effect | Default |
|---|---|---|---|
| 1 | An open swap period is active | Free, does not consume an allowance | on |
| 2 | Member has an annual allowance left | Free (2 per calendar year, resets 1 Jan) | on |
| 3 | Target character is level 60 | −250 | on |
| 4 | Target class is on the needed list | −300 | on |
| 5 | Above 80% attendance, 90 days | −100 | **off** |
| 6 | Returning to a previous main | ×2 | **off** |

Rules 1–2 are *first-match free*; rules 3–6 stack as discounts. **Open question for the
build:** whether discounts should stack (as designed) or take the single best one — stacking
makes a needed-class 60 cost 200 of 750, which may be cheaper than intended.

**Guards** are separate from cost: 90-day cooldown, 50% attendance floor to request, two
officer approvals, and the whole thing posts as a reversible adjustment batch.

**The member sees the same numbers the officer does**, before asking. The character page shows
per-alt cost, discounts applied, resulting balance, and whether they are short — computed
client-side from the same rule set. A member should never have to ask "what would this cost".

**A quote is held.** The cost is fixed at request time; changing a rule afterwards never
re-prices a pending or approved swap. (Same reasoning as strategy config snapshots.)

---

## 5. Guild characters and log attribution

The hard problem: a character several people play. A shared character's *own* log is ambiguous
by definition — it records what the character saw, not who was driving.

**Custody windows resolve attribution.** Each guild character carries a timeline of
`(operator, from, to, log entry point)`. An event inside a window is attributed to that
window's operator. An event inside a **gap** is quarantined, never guessed — the UI offers a
best guess from the surrounding custody but requires an officer to confirm it.

**Log entry points are assigned per operator machine**, not per character. The shared
character's own log file is explicitly flagged "ambiguous" in the log-sources list so nobody
wires it up as an authority.

**Attributing writes an ordinary ledger batch** with the operator as actor and the custody
window as source — reversible like anything else.

---

## 6. Loot log and the guild bank

**The looter is not the winner**, and both are recorded. (`running-a-raid-night.md`)

**NO DROP items can be assigned to the guild bank while a person holds them.** A NO DROP item
physically cannot be traded to the bank character, so the loot log carries a separate
*goes to* axis — `Winner` / `Guild bank` / `Unassigned` — alongside the looter, who becomes the
recorded holder. A banked NO DROP item is **priced per item by an officer** (§12 — not zero, or the
zero-sum pool acquires an accounting hole) and surfaces in the bank's
free-as-needed list under that holder's name, with an "Ask holder" action instead of
"Request". This is the single most-requested real-world case the incumbent handles badly.

**One item, one disposition.** An item is an auction lot *or* free-as-needed *or* loaned — the
same item never appears in two lists with different stories. (A NO DROP item can therefore
never be an auction lot.)

**Categories are user-defined, not hard-coded.** A category carries a default disposition and
a "who may request" rule; the shipped set (Spells, Plane of Sky quest, Coldain ring quest,
Resist gear, Tradeskill, Consumable, Held for the guild, Auction stock) is a starting point a
guild edits. The member-facing bank filters on the same categories, so what an officer defines
is literally what a member browses.

**Spells get their own shelf** because they are the highest-volume, lowest-ceremony request in
a raiding guild — a class/level grid with counts, sitting beside the general list rather than
buried in it. Officer-approval spells (Torpor) are marked *on the request*, not hidden from
the list.

**Two auction modes, scheduled.** Open (bids visible, first price, anti-snipe extends the close
by 2 minutes up to 5 times) and blind (sealed until close, second price). Both are recurring
schedules with a "run now" escape hatch, because a guild's bank auction is a calendar habit,
not an ad-hoc event.

**Delivery needs both sides.** A request moves Requested → Approved → Pulled → Delivered; the
officer marks handover, the member confirms receipt. Anything stuck in *Pulled* for a week
surfaces on the officer dashboard. Without the second confirmation the bank list quietly rots.

---

## 6b. Tiered bidding

**Tier outranks amount.** This is the single most consequential rule in the system and it
inverts what every other DKP tool assumes.

The ladder, top to bottom:

| # | Tier | Rule |
|---|---|---|
| 1 | Main | `character.is_main AND spec = primary` |
| 2 | Main · off-spec | `character.is_main AND spec ≠ primary` |
| 3 | Alt | `character.person = bidder AND NOT is_main` |
| 4 | Anyone | recruits, guests, second accounts |

**Resolution is two-phase.** Find the highest tier containing any bid; that tier wins the item.
Only then compare amounts, and only within it. A 10-point main bid beats a 350-point alt bid,
and the alt's number is never compared against the main's.

Consequences the UI has to carry:

- **Prices in the winning tier are small.** When mains only compete with mains, bids land in
  single or low double digits. Minimum 5.00, increment 1.00. Any screen showing three-figure
  main-tier prices is wrong.
- **Second price is within tier.** The winner pays the runner-up *in their own tier* plus one
  increment, or their own bid if they are alone in it. Never the 350.00 sitting below them.
- **A lower-tier bidder must be told they cannot win**, and why, without seeing the number that
  beat them. The board says "Cannot win — Main bids exist" and shows how many bids sit above
  their tier, never their values.
- **The tie-break chain grows a step at the top**: tier, then amount, then raid attendance,
  then balance, then timestamp, then a seeded roll. The whole trace is recorded on the
  resolution.
- **The award records the tier**, permanently. A 10-point main award and a 350-point alt award
  are different events and the loot log must not flatten them into "price".

**Blind discloses exactly one thing.** A bidder is told whether any *higher* tier has bids in
it — nothing else. Not the top bid, not any amount, not the count in their own tier. The one
disclosure exists because a bidder who cannot win should not spend points finding out; the
withheld count matters because "no bids in Main yet" would tell a main they win at the floor,
which is precisely what sealed second-price bidding exists to prevent.

**Spendable is `balance − Σ holds`, computed in one place.** Standings, the member profile and
the bid wallet must all read the same figure from the same source. Three screens drifted apart
during the tier rewrite because each carried its own copy.

---

## 7. Recruitment

**The application form is data, not markup.** Question types: short answer, long answer,
dropdown, multiple choice, check-all, true/false, file upload, plus two composite types:

- **Character roster** — repeating `(name, class, level)` rows with exactly one marked main.
  A recruit brings a stable of characters; asking for one name loses that.
- **Rules to accept** — a scrollable rules panel whose accept control is **locked until the
  member scrolls to the end**, and whose acceptance gates the submit button. The gate is
  deliberate friction on the one thing guilds always claim was read and never was.

**Members read applications; officers decide.** Feedback is **signed** — anonymous comments
cause more damage than they prevent — visible to members and officers, hidden from the
applicant. Votes are Yes / Lean yes / Lean no / No.

**Applying carries the Discord identity through**, so acceptance links the listed main
automatically instead of starting a fresh claim.

**Vouching is paid, on delivery.** A member who vouches for a recruit earns a bonus — 250 by
default — and the terms are the point of the design:

| Setting | Default | Why |
|---|---|---|
| Pays out when | Recruit is promoted to full member | Not on application and not on trial start — vouching for someone who never shows should cost the guild nothing |
| Trial must last | 14 days | An early promotion still waits |
| Recruit must reach | 50% attendance over the trial | The bonus rewards judgement, not introductions |
| Clawback | Reversed if they leave within 30 days | A reversal batch, never an edit |
| Cap | 3 bonuses per person per year | Vouching is a judgement, not a job |

**Promotion posts the bonus in the same transaction as the promotion.** No officer has to
remember, and the pair reverses together if the promotion was a mistake.

The offer appears where it changes behaviour: on the public recruiting card (so applicants
know to name someone), on the application's vouch field, on the recruit's page beside the vote,
and as a "your vouches" panel showing pending, paid and void bonuses with their ledger `seq`.

---

## 8. News and portal content

Publishing an article fires the `news.published` webhook so the Discord bot posts it — no
copy-paste, one source of truth. Bodies are Markdown, sanitised server-side and rendered
through the one approved component; raw HTML is never accepted. Officers compose from the
admin console or inline on the portal; both write the same resource.

---

## 8b. Quakes and draft week

A **quake** is a server-wide event that resets every raid target's spawn window at once. It is
the only event in the game that changes what the whole server does for a week, so it gets
first-class treatment rather than being a news post.

**Detection is a log pattern, confirmation is a human.** The parser matches the sighting in
officer logs and reports how many independent logs agree; an officer publishes. Publishing
puts a site-wide banner on the portal (the one place the `--color-section` ground is used at
page scale) and fires a `server.quake` webhook. A quake can also be declared by hand — the
detector will miss one eventually.

**Draft week is a scheduled window, not a raid.** It carries the guild's draft position, how
many picks that yields, and the snake order across the server's guilds.

**Availability and preference are asked separately, and in that order.** Members first say
which *nights* they can raid — because the targets are not known until the draft happens — and
then rank which *targets* the guild should spend its picks on. Collapsing the two into "sign up
for Vulak" produces a signup sheet for a raid that may never be drafted.

**Ranked choice, Borda-counted.** Members rank as many targets as they like; the aggregate is
shown live to everyone with an honest "within our picks / if it falls to us / unlikely" read
against the guild's actual draft position. Ballots are signed, editable until close, and only
officers see who voted for what. Withdrawing a target keeps every submitted ballot valid — it
simply drops out of the tally.

**Who may vote is an attendance threshold**, not a rank — the people who will be there decide.

**Recording a pick creates the raid session and calendar event**, pre-filled from that week's
availability. Nothing about the draft touches the ledger.

---

## 8c. Policies

Every rule the guild enforces lives in **one versioned library** — DKP, bank, dispute, guild
rules, main swap, draft, recruitment — rather than scattered across news posts, Discord pins
and officer memory.

**A policy is a document with addressable clauses.** Each policy has a stable public path
(`/policies/dkp`) and each section a stable anchor derived from its heading
(`/policies/dkp#corrections`). Anchors survive reordering, so a link an officer pasted a year
ago still lands on the right clause. This is the point of the feature: leadership answers a
question by sharing a link, not by retyping the rule and introducing a third version of it.

**Versions are kept, not replaced.** Old versions stay readable — that toggle is locked on. A
policy nobody can re-read is not a policy, and "what did the rule say when this happened" is
the same question the ledger answers about points.

**Re-acceptance is per policy, per version.** A material change flags the policy; members see
a banner until they accept, and officers can see who has read it and who has not. Nudges go to
Discord once, not repeatedly.

**Everything that states a rule links here.** The application's rules panel, the main-swap
cost explainer, the dispute button on a statement and the bank's free-as-needed note all point
at policy anchors instead of restating the rule inline.

---

## 9. Front-end customisation

The member home is a widget grid with an explicit **Customise** mode: drag to rearrange, resize
from the right edge, add blocks from a palette. The saved layout is a
**`table-view`-style API resource, not a localStorage blob**, so a bot can read standings in
exactly the columns the officers see. Anything that "feels UI-shaped" follows this pattern.

---

## 10. Mobile

Two screens are designed for the phone, chosen because they are the two things a member checks
*during* a raid: their balance, and whether the tick counted. Everything else degrades to the
responsive web view. Hit targets stay at 44px.

---

## 11. Roadmap gaps — now mocked

The audit against `ROADMAP.md` found nineteen specified 1.0 features with no screens. All are
now built. The behavioural decisions worth carrying into code:

**Bid sessions.** Accepted bids place a *hold*, so the same points cannot win two auctions at
once; holds release on resolve or on a rejected settle, never on a timer. Settling revalidates
the balance inside the committing transaction — a failure goes to `resolution_failed` rather
than awarding at a stale price. The tie-break chain (bid, attendance, balance, timestamp, then
a seeded roll whose seed is stored on the batch) is written onto the resolution so an officer
can explain the outcome months later without re-deriving it. Countdowns render
`closes_at − server_time`, never the client clock.

**Disputes.** An object naming a specific tick or batch, with the window remaining, the thread,
and three outcomes. Upholding posts a correction at today's `seq` linked to the dispute; the
original tick is not edited.

**Reconciliation queue.** Its own surface. Nothing in it has touched the ledger. Resolving with
"create alias" teaches the parser so the same spelling never asks again, and a parser bug opens
a pre-filled issue with the line attached.

**Webhooks.** Every attempt is on the record; a dead-letter keeps its full request and response
for one-click redelivery; `410 Gone` disables the endpoint rather than retrying forever; secret
rotation keeps both secrets valid for 24 hours.

**Notifications.** Per-member, per-channel opt-in. Two rows are locked on — a reversal touching
you, and a resolution of a dispute you raised — because a correction you cannot see is worse
than the mistake. Quiet hours suppress into a digest rather than dropping.

**Event types.** Slain patterns are not unique, so the tiebreak is specified: longest matching
pattern, then explicit priority. `no_attendance` excludes an event from the attendance
denominator, never from the ledger.

**Decay.** Preview before commit, idempotent on `(pool, period)`. A wrong run is reversed, not
re-run in the other direction.

**Raid templates.** A calendar event transformed into a session keeps a link back, and the
event keeps a link forward — "did we ever run that?" is answerable from either side.

**Away mode.** Hides you from signup sheets and pauses raid notifications, but attendance still
counts you absent. Away is honesty, not an exemption.

**Person merge.** Re-attributes history, never rewrites it; refused outright when both people
carry ledger entries.

**Custom character fields.** Typed and queryable, so "every cleric in CET who can main tank" is
a filter. EQdkp kept these in a JSON blob nobody could search.

**Item priorities.** Advisory. Nobody is blocked from bidding out of order — they just do it in
public.

**Theme.** Design tokens only, with a contrast validator that refuses any class colour under
3:1 against the ground. No free-form CSS and no per-file template overrides: that was EQdkp's
seventy-variable style engine, and it is why nobody could upgrade.

**Media.** Content-addressed, re-encoded to WebP, EXIF stripped, SVG rejected outright — it is
a script container wearing an image costume.

**Feeds.** A feed token reads exactly one feed, so a link pasted into a public Discord leaks a
news feed rather than a guild.

**First run** ([`mockups/first-run.dc.html`](mockups/first-run.dc.html)). Server-rendered, reachable only with the one-time token
the binary printed — which is why the console output is the first thing on the page. Two-factor
is mandatory for the owner account: every step-up gate is advertised in the docs, so an
unenforced one is worse than none. Preflight runs `doctor` before you finish.

**Step-up.** A valid session is never enough for token minting, role edits, backup or bundle
download, import commit, or bulk PII reads. The confirmation lasts five minutes and is audited
either way.

**Diagnostics.** Every check prints *the fix*, not the error. The support bundle is redacted
with a canary test proving it, so "attach it to a public issue" is a fact rather than a claim.

---

## 11b. Where each feature came from

Read from `ROADMAP.md` (1.0 scope). All of these are now mocked; the table maps each to the
phase that specifies it.

| Area | What the roadmap specifies | Phase |
|---|---|---|
| **Bid sessions** | Server-authoritative auctions with holds, anti-snipe, sealed bids, second price, a recorded tie-break chain, and an officer resolve/override screen. Distinct from the bank auctions we mocked — this is raid loot. | 6 |
| **Disputes** | A dispute is an object with a resolution linked to its correction batch. Referenced in our UI, never given a screen. | 4 |
| **Reconciliation queue** | Its own surface with alias learning and a pre-filled parser-bug issue link; we only show it inside the ingest preview. | 4 |
| **Webhooks admin** | Delivery log, HMAC rotation, retry ladder, dead-letter with one-click redeliver, `410 Gone` auto-disable. | 6 |
| **Notification preferences** | Per-member opt-in for outbid, claim-approved and dispute events. | 6 |
| **Item priority lists** | Per class and slot, versioned, referenced from the bid board and the loot screen. | 7 |
| **Portal blocks, menu manager, theme tokens** | Officer-arranged blocks per route, a permission-gated menu tree, and design tokens with a contrast validator. Our widget grid covers part of block layout only. | 7 |
| **Team page, media library, leaderboard block** | Officers by role with blurbs; content-addressed media with WebP re-encode and SVG rejected; top-N per class. | 7 |
| **Global search** | `GET /search` over persons, characters and items — we show the ⌘K affordance, never the results. | 4 |
| **First-run setup wizard** | Server-rendered bootstrap with a one-time token, before the SPA exists. | 2–3 |
| **TOTP enrolment and step-up** | Referenced in our copy ("step-up required"), never given an enrolment screen. | 2 |
| **Custom character fields** | `character_field_def` — typed, queryable fields an officer defines. | 2 |
| **Away mode** | `away_from`/`away_to` with a signup filter, so absence is declared rather than inferred. | 4 |
| **Raid templates + transform-to-raid** | Saved templates and a calendar event becoming a raid with a back-reference. | 4 |
| **Event types catalogue** | Slain patterns, `no_attendance` flag, event→pool mapping. Referenced in the new-raid dialog, no admin screen. | 4 |
| **Decay preview and jobs** | A dry-run diff before a decay run commits. | 4 |
| **Person merge** | Merge with re-attribution, guarded against merging two characters that both carry ledger entries. | 2 |
| **`/ops` diagnostics** | `dkp doctor` output where every failure prints its fix; support-bundle download. | 8 |
| **RSS feeds** | Per category, authenticated by a scoped `feed_token`. | 7 |

---

## 12. Resolved — the open questions

Settled by the guild lead, August 2026. These replace the "open questions" list.

**Main-swap discounts stack.** A level-60 cleric swapping during a swap period can reach a very
low price. That is intended: the conditions that stack are all things the guild wants to happen.

**Bank delivery: either.** An officer can close a request alone, and an officer close
auto-confirms after 72 hours if the member does not respond. Neither party can stall the other.

**A banked NO DROP item is priced by an officer, per item.** Not zero, not a nominal split. The
officer sets what it is worth to the guild, and that figure is what the ledger sees — so a
banked item is a real transaction rather than an accounting hole in zero-sum.

**Custody gaps warn, they do not block.** An unattributed loot line from a shared character is
surfaced loudly on the raid, but finalisation proceeds. Blocking a raid close on a parser
ambiguity punishes the officer for the tool's limits.

**Bank category permissions are a role check, with a per-category choice of either.** Each
category picks its own gate: a role, or an attendance threshold. Not a fixed global rule.

**Un-accepted policy changes nag only.** No blocking of bidding or signups. A policy that
blocks participation becomes a weapon in an argument, which is the opposite of the point.

**Quakes are always officer-published.** No auto-publish at any confirmation count. A quake
banner mobilises the guild at unsocial hours; a human decides that.

**Item priorities stay advisory.** No warning, no flag, no review queue. Bidding out of order
happens in public and that is enough.

**Attendance is raids attended ÷ raids held.** The headline number everywhere. Ticks are shown
alongside as a secondary column, never instead: a late arrival should not be scored like an
absence. Both are computed; only one is the headline.

**Inactivity: flag at six weeks, hide from standings at twelve.** Away mode exempts both. The
flag is visible to officers; the hide is visible to everyone, and reversible by one click.

---

## 13. Added on review

Built in the same pass, from a review of the finished screens:

**Dashboard — "since you last looked".** A strip across the top of the officer dashboard
naming what changed since their last visit, with the `seq` range. Officers dip in and out; the
alternative is re-reading the whole ledger to find the one reversal.

**Raid detail — who is *not* credited.** Three people seen tonight who are missing from at
least one tick, each with the reason and a one-click fix. Finalising over these is what a
dispute is made of, so the screen says so.

**Standings — as-of cursor.** Because balances are a sum over an append-only log, any past
`seq` reproduces exactly. "Now", "before tonight", a date, or the last decay run. This is not a
convenience feature; it is the visible proof that the ledger claim is true.

**Members — bulk actions and inactivity flags.** Rank change, set away, tag, adjust, export
across a selection. Quiet and Hidden tags on the row.

**Loot — contested, separate from disputed.** *Disputed* means someone formally raised it.
*Contested* means the officer flagged it at award time — an off-spec win with no main bid, a
council call that will raise eyebrows. One is a process state, the other is a warning.

**Bank — item age.** How long each free-as-needed item has sat unclaimed, six weeks or older in
warning colour. A bank nobody draws from is a bank nobody knows about.

**Recruits — officer-only notes.** A private thread beside the public vote, explicitly labelled
as not visible to the applicant. Officers will have this conversation somewhere; better in the
record than in a DM.

**Draft — "if we drafted now".** A projection of what the guild actually walks away with, given
the ballots in and what the guilds ahead historically take, plus the fallback and how much your
missing ballot could still swing it. A tally is a number; a projection is a reason to vote.

---

## 14. Attribution

Every surface carries, in its footer (or the admin sidebar's instance card):

> Made by **Prokopton** · P99 Green · member of **Legacy of Fire**

This is a credit to the guild that supported the project, not decoration — keep it through
redesigns and carry it into the shipped product. Placement may move; the line should not
disappear.

---

## Companion document

[`11-ui-backend-contract.md`](11-ui-backend-contract.md) records what these screens expect the server to do; [`../development/design-reconciliation.md`](../development/design-reconciliation.md) is the
reconciliation-spike plan for merging this work back into the Go repository.

[`11-ui-backend-contract.md`](11-ui-backend-contract.md) in particular records — resources,
endpoint shapes, invariants rendered as facts, and a ranked list of where the mockups most
likely diverge from the Go implementation. Use it to drive the reconciliation spikes.

---

## Still open

Nothing from the original list. New questions go here as they arise.
