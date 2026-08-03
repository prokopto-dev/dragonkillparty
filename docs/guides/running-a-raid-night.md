# Running a raid night

**Status:** raid operations and log ingest land in Phase 4; bid sessions in Phase 6. This page is the
procedure the implementation must support, not a transcript. Commands and payloads are the specified
shapes.

The loop, once: form up → tick every N minutes → credit kills → award loot → upload → publish →
finalise. Everything below is that loop with the failure cases attached.

## Before the first raid

| Do this once | Where |
|---|---|
| Turn on logging in EverQuest — `/log on`, or `Log=TRUE` in `eqclient.ini` | Every officer who will take dumps |
| Create the event types you raid — NToV, Kael, Sky, Fear, Trakanon | Admin → Event types |
| Decide which event types are `no_attendance` | [Attendance and windows](attendance-and-windows.md) |
| Set tick length and tick value | [Choosing a DKP system](choosing-a-dkp-system.md) |
| Give raid leaders the `raid_leader` role, not `admin` | [Permissions for officers](permissions-for-officers.md) |
| Dry-run last week's dumps and compare against your spreadsheet | Raids → New → upload, do not commit |

Nominate **two** officers to take dumps. One forgets, and two officers uploading overlapping dumps is
a case the ingest deduplicates rather than a case that double-credits.

## During the raid

### Open the raid

A raid is a **session**: an unbounded window, often three to six hours, during which the force may win
zero or five targets, may lose an FTE race, and may relocate across three zones. It is not one event
with one attendee list. Open it when you form up, not when you pull.

```bash
curl -sX POST "$DKP_URL/api/v1/raids" \
  -H "Authorization: Bearer $DKP_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{"name":"Tue NToV","started_at":"2026-08-04T23:00:00Z","pool_ids":["01JZ0POOL…"]}'
```

### Take ticks

Every N minutes, click **Dump** on the in-game Raid window. That writes
`RaidRoster-YYYYMMDD-HHMMSS.txt` into your EverQuest directory: tab-separated group number, character
name, level and class title, with **group 0 meaning in the raid but ungrouped** — your bench.

There is no way to automate the click. Interactive third-party programs are bannable on Project 1999;
only log-reading tools are permitted. Everything this product ingests is read-only and out of process.

| Cadence | Who uses it |
|---|---|
| 15 minutes | Most common |
| 20 minutes | Long variance sits |
| 30 minutes | Short, dense raids |

If the Raid window Dump is impractical, `/who` output pasted from the log works too. Be aware that
`/who` in a zone is capped well below a 60-person raid, so officers doing this take `/who <class>` per
class, and anonymous or roleplaying members lose their level and class entirely.

**You will forget a tick.** That is expected and supported:

- Insert a tick retroactively from a log timestamp.
- Upload a dump captured 40 seconds late; the tick keeps the officer's timestamp and the artifact.
- Let both officers upload; identical ticks deduplicate on `(raid_id, tick_seq)` plus a content hash.

### Credit kills

An FTE shout and a slain line both appear in the officer's log:

```
[Tue Aug 04 23:41:07 2026] Vulak`Aerr engages Tankguy!
[Wed Aug 05 00:12:55 2026] Vulak`Aerr has been slain by Tankguy!
```

The slain line is the automatic kill-credit trigger, matched against the `slain_pattern` on the event
type. Kill credits attach to the session, not to a tick, so a session can carry zero or five of them.

Losing the FTE race still costs the raid nothing: the ticks already taken stand. Attendance is owed for
turning up, not for winning the pull.

### Award loot

Loot lines are in the log:

```
[Wed Aug 05 00:14:02 2026] --Tankguy has looted a Cloak of Flames.--
```

The **looter is not the winner.** A guild bank character or a puller often loots the corpse; the award
belongs to whoever won the item. Record the winner explicitly.

Three award paths, all producing the same kind of ledger batch:

| Path | Use |
|---|---|
| A [bid session](auctions.md) | Auctions, sealed bids, anything with a price to discover |
| Direct award | Fixed price lists, loot council decisions, rot |
| A chat-award grammar parsed out of the log | Guilds already running a convention such as `You say, 'LOOT: Cloak of Flames Tankguy 350'` |

Nothing hits the ledger until the award is committed. An unrecognised item name is **quarantined in the
reconciliation queue, never dropped** — see [Loot and reconciliation](loot-and-reconciliation.md).

## After the raid

### Upload

Drop the whole folder of `RaidRoster-*.txt` files and the log slice onto the raid page, or post them:

```bash
curl -sX POST "$DKP_URL/api/v1/artifacts" \
  -H "Authorization: Bearer $DKP_TOKEN" \
  -F file=@RaidRoster-20260804-231500.txt
```

Uploads are **content-addressed**: re-uploading the same file is a no-op, not a duplicate.

Raw artifacts are retained for 180 days by default, with `/tell` lines redacted at ingest. Keeping them
is the point — "any member can download the dump behind this tick" is the strongest anti-drama
mechanism in the product. A guild can opt out; think hard before you do.

### Preview, then commit

`POST /raid-submissions` gives you a durable, re-fetchable preview before anything is written:

| The preview shows | So you can |
|---|---|
| Ticks that would be created, and which were deduplicated | Confirm the other officer's uploads merged |
| The ledger entries that would be posted, per person | Catch a wrong tick value before it becomes history |
| Names the parsers could not resolve | Fix them once, in the queue |
| Warnings | See a 40-minute gap between ticks before a member does |

Set `on_unresolved` to decide what happens to unknown character names:

| Value | Behaviour |
|---|---|
| `quarantine` (default) | The award is held in the reconciliation queue. Nothing is dropped and nothing is guessed. |
| `fail` | The whole submission is rejected. |
| `create` | Create the person. An explicit officer choice, never a default. |

Commit when the preview is right. Commit is one transaction, and it returns a receipt naming the first
and last ledger `seq` it wrote — the two numbers to quote in any later dispute.

### Publish and let people argue

Publish the raid report and leave it open for the dispute window — 72 hours by default. Members check
two things, in this order: their own balance, and their attendance percentage. Both link to the dump
behind them.

A dispute is an object, not a Discord message: "Raid #481 tick 6, I was there" → an officer resolves it
→ the resolution links to the correction batch. That trail is what makes the answer stick.

### Finalise

Finalising closes the session and triggers a synchronous attendance recompute for the affected pools,
so the standings page is right immediately rather than at midnight.

Finalising does **not** freeze the ledger. A mistake found next month is fixed the same way as a
mistake found next minute: a reversal batch. Nothing is ever edited.

## When it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| A raider is missing from a tick | They were zoning when the dump was taken | Add them to that tick; the change is audited and shows on the raid history |
| The whole raid is missing from someone's attendance | Wrong pool, or the event type is `no_attendance` for that pool | Check the event-to-pool mapping |
| Two officers' uploads produced double ticks | The tick sequence numbers differed and the contents differed | Void the duplicates; ticks void, they never delete |
| An item name landed in the queue | The parser has not seen that spelling | Resolve it once and create the alias; it never asks again |
| A tick value was wrong for the whole raid | The pool's configuration changed mid-raid, or the wrong event type was chosen | Reverse the tick's batch and post the correct one. Do not edit. |
| The log has no year, and two officers' clocks disagree | EverQuest timestamps carry no timezone and no sub-second precision | The uploader's timezone is captured with the artifact; ticks order by the server's sequence, not by log text |

## Next

- [Attendance and windows](attendance-and-windows.md) — what the percentage counts
- [Loot and reconciliation](loot-and-reconciliation.md) — the queue and award corrections
- [Auctions and bid sessions](auctions.md) — running a bid
- [The ledger](../concepts/ledger.md) — why corrections are reversals
