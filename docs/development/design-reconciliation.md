# Design reconciliation — the record

**Status:** complete, 2026-08-07, against `e5d9059`.

This began as a runbook of nine open spikes for merging the UI mockups back into the Go repository.
**They are closed.** The verdicts live in
[`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md); the resulting document
changes are in `00-canonical-conventions.md` §4, §5, §6 and §17, and in the ROADMAP rewrite that
took the plan from nine phases to ten.

It is kept, unedited below the line, for two reasons. The spike questions record *what was actually
uncertain* at the moment the design work landed — which is the thing a later reader cannot
reconstruct — and the vocabulary table is still the reference for reconciling UI terms against Go
ones.

**What changed since it was written:** nothing needs spiking. Seven of the nine questions were
answerable from the design documents alone, and the two that were not — tiered bidding and the nine
invented resources — turned out to need building rather than investigating.

---

The reconciliation-spike runbook. Read [`../design/10-ui-decisions.md`](../design/10-ui-decisions.md) for *why* the UI behaves as it
does and [`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md) for *what* it expects of the server; this file is the order of
operations for closing the gap between them and the Go code.

Written for someone with the repo open, not for someone who has read the mockups.

---

## What this project is

Five HTML surfaces covering roughly 55 screens, built against the repository's own docs
(`docs/concepts`, `docs/guides`, `docs/api`, `docs/migration`, `ROADMAP.md`,
`.claude/rules/web.md`). They are design artefacts, not a frontend — no build step, no npm, no
framework. They exist to settle interaction and vocabulary questions before implementation, and
to be a reference an implementer can read a screen off.

| File | Surface |
|---|---|
| [`../design/mockups/admin-console.dc.html`](../design/mockups/admin-console.dc.html) | Officer console — 30+ screens, three nav layouts |
| [`../design/mockups/guild-portal.dc.html`](../design/mockups/guild-portal.dc.html) | Member portal, including two phone views |
| [`../design/mockups/my-characters.dc.html`](../design/mockups/my-characters.dc.html) | Characters, keys, main swaps |
| [`../design/mockups/public-site.dc.html`](../design/mockups/public-site.dc.html) | Guest landing, application form, claim flow |
| [`../design/mockups/first-run.dc.html`](../design/mockups/first-run.dc.html) | Setup wizard — server-rendered, pre-SPA |

Three companion documents:

| File | Answers |
|---|---|
| [`../design/10-ui-decisions.md`](../design/10-ui-decisions.md) | Why each behaviour is what it is. Fourteen sections. The one to read first. |
| [`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md) | What the UI assumes of the server: resources, endpoint shapes, invariants rendered as fact, ranked divergence list. |
| this file | This file. |

---

## The honest status of every claim in here

Nothing was read out of the Go source. Everything was derived from the docs and then extended
wherever a screen needed a decision the docs did not make. So:

- Where the docs were explicit, the mockups follow them and the risk is low.
- Where the docs were silent, the mockups **invented** something and labelled it. Those are
  marked `?` in [`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md) §1 and are the substance of the spikes below.
- One rule — **tiered bidding** — came from the guild lead mid-project and is not in the docs at
  all. It is the highest-risk item and it is first.

---

## Spike order

Each spike is a question with a decision at the end. The decision is always one of:
**UI is right** (backend needs the capability) · **backend is right** (redraw the screen) ·
**neither** (the interaction was designed around a constraint that no longer exists).

### Spike 0 — Tiered bidding *(blocking, do first)*

**Question.** Does the bid resolver compare tier before amount?

**Why first.** Everything else in the auction stack sits on it. If bids resolve by amount alone,
these are wrong: the officer bid console, the member bid board, the tier column on the loot log,
the "cannot win — Main bids exist" state, the price scale on every fixture in the project, and
the second-price-within-tier arithmetic. Roughly six screens and every loot number.

**What to check.**
- Is there a tier concept on `bid` at all?
- Is tier derived server-side from the bidding character, or accepted from the client? The UI
  assumes server-side, always — see [`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md) §2b for the ladder.
- Is second price computed *within the winning tier*, or across all bids?
- Does the resolution row persist the winning tier, the trace, the ladder version and the
  visibility mode?

**Likely outcome.** The Go code does not have tiers. Then the question becomes whether tiers are
a `bid` column plus a resolver change, or a strategy plugin. The UI does not care which — it
cares that the resolution row explains itself.

**Fixture warning.** Any test data with three-figure main-tier bids is modelling the old scheme.
Main-tier bids land in single or low double digits, because mains only compete with mains.

---

### Spike 1 — Ledger and `seq`

**Question.** Is `seq` a global, monotonic, stable cursor, and is the ledger genuinely
append-only?

**Why.** Ten screens print `seq` as an as-of marker, and the standings time-travel cursor
(`?as_of_seq=`) is only honest if any past `seq` reproduces exactly. Four dashboard claims are
rendered as guarantees: nightly replay with zero drift, intact hash chain, 10/10 invariants, and
"balances are a SUM over an append-only log".

**What to check.**
- Any mutation path on a posted entry, anywhere. One is enough to make four screens lie.
- Whether `as_of_seq` is *served* or would have to be approximated. The UI shows an exact
  balance at a past point; an approximation is worse than no feature.
- Whether the nightly replay exists and is countable.

---

### Spike 2 — Server time on the wire

**Question.** Does every deadline-bearing payload carry the server's current time?

**Why.** Cheap to test, de-risks five screens. Countdowns are computed from
`closes_at − server_time`, never the client clock — a bid board that trusts the client clock is
exploitable, and draft ballots, signup cutoffs and dispute windows have the same shape.

**What to check.** Whether the SSE stream and the REST payloads both carry it, or whether the
client is expected to establish an offset once.

---

### Spike 3 — Holds

**Question.** Is a hold a first-class object with its own lifecycle?

**Why.** Spendable balance is `balance − Σ holds`, and three surfaces read it: standings, the
member profile, the bid wallet. If the backend models bidding as a plain award at resolve time,
every balance figure on the bid board is wrong.

**What to check.**
- Is the hold placed in the *same transaction* that accepts the bid?
- Does it release on resolve and on a rejected settle — and never on a timer?
- Does `POST /bids` return the bidder's new spendable balance? The board updates without a
  refetch.
- Is spendable computed in one place server-side? Three mockup screens drifted apart during the
  tier rewrite because each carried its own copy — the same failure will happen in code.

---

### Spike 4 — Blind-mode disclosure

**Question.** What exactly does the API withhold before close?

**Why.** Under `blind` the API must not leak, to anyone but an officer: the top bid, any bid
amount, or the *count* of bids in the caller's own tier. It may disclose one thing — that a
*higher* tier has bids in it — because otherwise a bidder spends points into a wall.

That count is not a nicety. "No bids in Main yet" tells a main they win at the floor, which is
exactly what sealed second-price bidding exists to prevent. This is a leak that will be
reintroduced by accident if it is not a test.

---

### Spike 5 — `table_view`

**Question.** Is there a server-side resource for saved layouts?

**Why.** The portal widget grid, saved member-table views and the blocks arrangement are all
stored server-side as API resources, explicitly *not* `localStorage`. This is rule zero in
[`../design/10-ui-decisions.md`](../design/10-ui-decisions.md): a UI need is an API change.

**Decision if absent.** Either build it, or accept that customisation is per-browser and redraw
Customise mode to say so. Do not fake it in `localStorage` — that was the choice that made
EQdkp's customisation useless across devices.

---

### Spike 6 — The invented resources

Each is a small schema question with a large screen behind it. From
[`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md) §1, everything marked `?`:

| Resource | Screen at risk | The question |
|---|---|---|
| `character_key`, `character_flag` | My Characters, raid forming | Typed and queryable, or free text? A JSON blob makes the filter decorative |
| `main_swap_request` + quote | Main swaps | Can the server quote a price and say which discounts applied, in order? |
| `guild_character` custody | Shared characters | Custody windows with timestamps, attribution at parse time |
| `bank_item`, `bank_request`, `bank_delivery` | Guild bank | Request → delivery → 72h auto-confirm |
| `bank_auction` | Bank auctions | Open and blind, separate from raid bid sessions |
| `application_form` + answers | Recruitment | Form schema as data, per-answer storage, file uploads |
| `vouch` | Recruitment bonus | State machine, payout transactional with promotion |
| `policy` + `policy_acceptance` | Policies | Versioned, per-member acceptance, `material` flag |
| `quake`, `draft_week`, `draft_ballot` | Quakes, draft | Officer-published; ranked ballots with a live projection |

---

### Spike 7 — Guild bank as a ledger participant

**Question.** Can the ledger post to a non-person account?

**Why.** The bank holds items, runs auctions, and receives the zero-sum residue. That makes
`guild_bank` an *account*, not a label. Also: a NO DROP item assigned to the bank is priced by
an officer per item — so a bank award is a real ledger transaction, not a bookkeeping note.

---

### Spike 8 — Attendance denominator

**Question.** Which number does the API serve as the headline?

**Why.** The guild lead chose **raids attended ÷ raids held** as the headline, with ticks shown
alongside rather than instead. Both need to be served; the UI shows them in adjacent columns on
two tables. Confirm the backend can produce both over the same window, and that `no_attendance`
events are excluded from the denominator and not from the ledger.

---

## Vocabulary to reconcile

Words the mockups use consistently. Where the Go code differs, decide once and change the other
side — drift here is expensive because it shows up in every screen and every payload.

| UI term | Meaning | Watch for |
|---|---|---|
| person / character | Person owns the balance; character is attribution | Do not let "member" mean both |
| account | person × pool balance | Not a login |
| batch / entry | Ledger write unit / one line in it | |
| tick | One attendance grant inside a session | Not "raid" |
| session | A raid window, zero or more targets | Not "event" |
| event type | What the parser matches on | Distinct from calendar event |
| tier | Main / main off-spec / alt / other | New, not in the docs |
| hold | Points reserved by an accepted bid | |
| quarantine | Parsed name that could not resolve | Not "error" |
| reversal | Correction batch | Never "edit" or "delete" |
| step-up | Fresh second factor within five minutes | Not "re-login" |
| away | Member-declared absence | Attendance still counts them absent |
| quiet / hidden | Inactive at 6 weeks / 12 weeks | Automatic, not a rank |

---

## Decisions the guild lead has already made

Do not re-litigate these during the spike. Full reasoning in [`../design/10-ui-decisions.md`](../design/10-ui-decisions.md) §12.

- Main-swap discounts **stack**, with no floor.
- Bank requests: officer can close alone; **auto-confirms after 72h**.
- Banked NO DROP items are **priced per item by an officer**.
- Unattributed loot from a shared character **warns**, does not block finalisation.
- Bank category permissions: **role check, or per-category choice** of role or attendance.
- Un-accepted material policy change: **nag only**, blocks nothing.
- Quake banners are **always officer-published** — never auto-detected.
- Item priority lists stay **advisory**. No warning, no record, no teeth.
- Attendance headline is **raids ÷ raids held**.
- Inactivity: flag at **6 weeks**, hide from standings at **12**.
- Blind bidding is the **default**; open is switchable per pool.

---

## When the spike is done

Update, in this order:

1. [`../design/11-ui-backend-contract.md`](../design/11-ui-backend-contract.md) — change every "assumed" row to "verified" or "diverges", and move
   resolved items off the §6 divergence list.
2. [`../design/10-ui-decisions.md`](../design/10-ui-decisions.md) — if a decision changed because the backend forced it, record the new
   decision *and the constraint that caused it*. The reasoning is the point of the file.
3. The affected `.dc.html` screens.
4. the design project's sync record — refresh `## Last sync` with the real commit you reconciled against.

A screen that survives the spike unchanged is worth as much as one that changes; note both.
