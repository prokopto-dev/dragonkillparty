# Attendance and windows

**Status:** attendance statistics land in Phase 4. This page is the specification the implementation
must satisfy. The formula below is cross-checked in CI by two independent implementations — a slow,
obviously-correct Go loop and the production SQL — on 50 random member/window pairs.

This page exists to settle arguments. When a member says "the site says 62% and I only missed one
raid", the answer is on this page and it is the same answer for everybody.

## The formula

```
attendance % = ticks you attended ÷ qualifying ticks held × 100
```

Both sides count **ticks**, not raids, unless you deliberately ask for a raid-based or day-based
metric. Everything below is about what "qualifying" means on each side.

## The denominator: qualifying ticks held

A tick counts toward the denominator when **all** of the following are true.

| Condition | Why it exists |
|---|---|
| The tick belongs to this pool | A pool is a currency. A Cross-class Rares tick is not part of your main-raid attendance. |
| The tick is not voided | An officer who deletes a mistaken tick removes it from everyone's denominator, not just the attendees'. |
| The raid is not deleted, and its state is `open` or `finalized` | A draft raid nobody has finalised does not yet count against anybody. |
| The tick's timestamp is inside the window | 30-day, 60-day, 90-day, lifetime, or an explicit date range. |
| The tick's event type is **not** marked `no_attendance` for this pool | See below. |

That is the whole denominator. It does **not** depend on you: whether you attended, whether you were
in the guild, whether you were on leave. Every member in the pool shares the same denominator, which
is exactly what makes the percentage comparable between two people.

### `no_attendance` — the flag that changes everyone's number

Each event type is mapped to each pool, and each mapping carries a `no_attendance` flag. When it is
set, ticks for that event type still **award points** but do **not** appear in the denominator.

Use it for events that are not a fair test of showing up:

| Event type | Typical setting | Reason |
|---|---|---|
| Regular NToV night | counts | The raid you are measuring people against |
| Sky rotation | counts | Scheduled, everyone can plan for it |
| Off-night pickup / open raid | `no_attendance` | Optional; penalising people for missing it is not what you meant |
| Spawn-watch and tracking credit | `no_attendance` | Two trackers earn; the other 60 raiders are not absent |
| Corpse recovery, epic assists | `no_attendance` | One-off favours |

This flag is load-bearing. If you migrated from EQdkp Plus and your lifetime percentages differ from
the old site by a few points for *everybody*, this mapping is the first thing to check — that is
exactly the symptom.

### Connected raids: how deduplication actually works

A "connected raid" group links several raid rows so they represent one raid night. It exists because
EQdkp Plus modelled one raid row per event, so a Tuesday that killed three targets produced three raid
rows, and a member who was there for all three looked three times as present as one who came for one.

The deduplication rule is precise, and it is where most confusion starts:

| Metric | Effect of connecting raids |
|---|---|
| **Tick-based** (`metric=ticks`, the default) | **None.** Every qualifying tick counts individually on both sides. Twelve ticks are twelve ticks whether the raid rows are connected or not. |
| **Raid-based** (`metric=raids`) | Connected raids collapse to **one**. The dedupe key is the group ID when a raid has one, and the raid's own ID when it does not. |
| **Day-based** (`metric=days`) | Ticks collapse to distinct guild-local raid days. |

So connecting raids does not change a tick percentage at all. It changes the raid percentage, and it
is the correct fix when your denominator says "31 raids this month" and your guild raided 12 nights.

Connecting is an explicit officer action. Imports do it for you: an EQdkp connected-attendance group
becomes one raid session, and each of its legacy raid rows becomes one tick inside it.

## The numerator: ticks you attended

| Rule | Detail |
|---|---|
| Counted per **person**, not per character | Your main, your rezzer alt and your ported-in enchanter are one person. |
| Boxing does not multiply | Two of your characters in the same tick count **once**. The numerator is a distinct count of ticks. |
| Attendance status `present` or `pilot` counts | `pilot` is someone playing another member's box. |
| `standby` and `bench` are counted **separately** | They may earn a reduced percentage of points. Whether they count toward attendance is a pool policy, and it is a separate number on your statistics so you can see both. |
| The numerator is drawn from the same `held` set as the denominator | A tick you attended that is disqualified — voided, wrong pool, `no_attendance` — is removed from both sides. You are never punished for attending something that does not count. |

## Worked example

Pool "Velious Main", 30-day window, guild timezone `America/New_York`.

**Ticks held in the window:**

| Raid | Event type | `no_attendance` | Ticks | In denominator |
|---|---|---|---|---|
| Tue NToV | ToV | no | 12 | 12 |
| Wed Kael | Kael | no | 9 | 9 |
| Thu open raid | Open raid | **yes** | 8 | 0 |
| Sat Sky (3 connected raid rows) | Sky | no | 4 + 5 + 5 | 14 |
| Sun NToV — one tick voided | ToV | no | 11, 1 voided | 10 |

Denominator: 12 + 9 + 0 + 14 + 10 = **45 ticks**, across **4** deduplicated raids (Sky's three rows
count once).

**Tankguy attended:**

| Raid | Ticks attended | Notes |
|---|---|---|
| Tue NToV | 12 | |
| Wed Kael | 0 | absent |
| Thu open raid | 8 | does not count — the event is `no_attendance` |
| Sat Sky | 14 | boxed two characters throughout; still 14 |
| Sun NToV | 10 | present for the voided tick too; it counts on neither side |

Numerator: 12 + 0 + 14 + 10 = **36**.

```
tick attendance   = 36 / 45 = 80.00%
raid attendance   =  3 /  4 = 75.00%
```

Both are correct. They answer different questions, and you should publish which one your gates and
tie-breaks use.

## New members: the `_real` percentage

A recruit who joined four days ago has attended 8 of the pool's 45 ticks: 17.8%. That number is
useless and it makes the standings page look broken.

Every statistic therefore comes in two forms:

| Number | Denominator |
|---|---|
| `pct` | All qualifying ticks in the window |
| `pct_real` | Only qualifying ticks held **since the person joined** |

The recruit above is 17.8% raw and 100% real. Use `pct_real` for gates and recruitment reviews; use
`pct` for anything comparing veterans.

## Windows

| Window | Default | Use |
|---|---|---|
| 30 days | on | Loot gates, "are you still raiding" |
| 60 days | on | Rank reviews |
| 90 days | on | Long-run standing, tie-breaks |
| Lifetime | on | Recognition, not gating |
| Explicit `from`/`to` | on request | Disputes and audits |

The window list is configurable per pool. Windows are day-bucketed in **guild-local** time, which is
why the guild timezone set during [first run](../getting-started/first-run.md) matters — a raid that
ends at 01:00 lands on the previous raid day only if the timezone is right.

## Why the number has a date on it

A rolling window's denominator changes with the passage of time even when nothing happens. Your 30-day
percentage is different tomorrow with zero new raids, because ticks fell out of the back of the
window. There is therefore no cache-invalidation strategy that works — only a recompute schedule.

So the stored statistic carries `as_of_day` and `as_of_seq`, and both are shown in the UI. Staleness
is visible rather than hidden, and — the part that matters in a dispute — the number displayed is the
same number a tie-break used, because both read the same dated row.

Recomputation happens at guild-local midnight, and **synchronously** for the affected pool when a raid
is finalised or a tick is voided, so the post-raid page is correct immediately.

Ad-hoc date ranges bypass the stored statistics and run the query directly. That path is slower and is
officer-initiated on purpose.

## Reading it from the API

```bash
curl -s "$DKP_URL/api/v1/persons/$PERSON/attendance?window=30d&pool=$POOL&metric=ticks" \
  -H "Authorization: Bearer $DKP_TOKEN"
```

The response exposes the numerator and the denominator, not just the percentage, so a bot can show
"36 of 45 ticks" in Discord instead of a number nobody can check. Requires the `dkp:read` scope.

## Settling the four arguments you will actually have

| "But…" | Answer |
|---|---|
| "I was there and it says I missed it" | Open the tick. Every tick links to the raid dump it came from, and the dump lists exactly who was in the raid window when the officer clicked Dump. If you were zoning, you were not in the dump. |
| "My percentage dropped and I did not miss anything" | The window moved. Ticks aged out of the back. Compare `as_of_day` on the two numbers. |
| "Two of us were on the same raid and have different denominators" | You do not. The denominator is identical for everyone in the pool. You are comparing `pct` against `pct_real`, or two different windows. |
| "My alt was there, so why did it not count twice?" | Because attendance is per person. Counting boxes would reward account count over turning up. |

## Next

- [Running a raid night](running-a-raid-night.md) — how ticks get created
- [Roster, mains and alts](roster-and-alts.md) — why your characters are one person
- [Choosing a DKP system](choosing-a-dkp-system.md) — using attendance as a gate or a tie-break
