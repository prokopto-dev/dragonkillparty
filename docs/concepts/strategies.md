# Point strategies

**Status:** the strategy engine is Phase 1 and is landing strategy by strategy. `fixed_price`,
`tick`, `start_points`, `cap`, `auction_open`, `auction_sealed`, `relative_bid`, `roll`,
`loot_council`, `decay_percent`, `decay_window`, `zero_sum` and `attendance_weighted` ship today — see
[the worked examples](#the-shipped-strategies) below, which is now every rule 1.0 promises. This page
explains the design; each strategy's knobs are its `ConfigSchema` in `internal/strategy/<id>.go`, and
a generated per-strategy reference page is Phase 2
([#212](https://github.com/prokopto-dev/dragonkillparty/issues/212)).

The four bidding rules carry the loot **arithmetic** — which bid wins, what it pays, and the batch
that follows. The bid session itself (the state machine, the anti-snipe window, holds) is Phase 6;
[Auctions and bid sessions](../guides/auctions.md) is its specification. **Tier-aware resolution** —
"a 10-point main bid beats a 350-point alt bid" — is arithmetic and shipped as its own Phase 1
deliverable ([#224](https://github.com/prokopto-dev/dragonkillparty/issues/224)), so the tie-breaks
below start at the **rung** and only then compare the amount. The rung is recorded on the bid when it
is accepted, which is Phase 6's job: until then no bid records one, every bid stands on `anyone`, and
the amount decides as it always did.

A guild's point rules are configurable. The ledger's rules are not. This page is about where that line
is drawn and why it is drawn there.

## The principle

> A strategy is a **pure function that proposes a ledger batch**. It never touches the database. The
> ledger validates every proposal against invariants that no strategy may waive.

Everything below follows from that one sentence. It is what lets a guild pick zero-sum over fixed
price without forking the software, while guaranteeing that a badly configured strategy can produce a
*wrong number* but cannot produce a *corrupt ledger*.

## What a strategy does

| It is asked | It returns |
|---|---|
| "A tick happened; here are the attendees" | A batch proposal crediting them |
| "This item was awarded at this price" | A batch proposal debiting the winner, and under zero-sum crediting the others |
| "An officer entered an adjustment" | A batch proposal |
| "It is Sunday; run decay" | A batch proposal |
| "Reverse this batch" | A batch proposal with the inverse |
| "What can this account spend?" | A number |
| "Where does this account rank?" | A number |
| "Is this bid valid?" | Yes, or a specific reason |
| "Who won this auction, at what price?" | A winner, a price and the tie-break reason |

A proposal is a list of entries plus metadata. It is not a write. The ledger service takes it,
validates it, and commits it in one transaction — or rejects it entirely.

## What a strategy is forbidden

Three prohibitions, each enforced mechanically rather than by review.

| Forbidden | Enforcement | Why |
|---|---|---|
| Importing `internal/store` | An architectural test fails the build | A strategy that can read the database can also be non-deterministic, and can be given data the ledger did not validate |
| Calling `time.Now` | `golangci-lint` rule; the clock is injected | Planning the same event twice must give the same answer, in a test and in production |
| Calling `math/rand` | Same. A seeded generator is injected and **the seed is persisted onto the batch** | A tie broken by a coin flip must be reproducible three months later when someone asks |
| Using `float32` or `float64` | `golangci-lint` rule plus the `NoFloat` invariant at runtime | See [The ledger](ledger.md#money-is-integers) |

The payoff for injecting the clock and the seed: planning the same event twice produces **byte-identical**
proposals, which a test asserts by hashing them. That is what makes "recompute the whole ledger and
check it matches" a meaningful nightly job rather than a hopeful one.

## Versioning: old batches stay interpretable

Every batch records `strategy_id`, `strategy_version` and a **snapshot of the configuration in force
when it was planned**.

So when a guild raises the tick value from 10 to 15, last month's raids do not silently re-price. The
old batches carry the old configuration and the statement can show it. Changing a pool's strategy or
its configuration is itself a recorded event with a before and after; it never rewrites history.

Migrating a pool from one strategy to another — say DKP to EPGP — freezes the old one, posts a
migration batch translating balances into the new strategy's balance kinds, and keeps the old history
readable. You preview the diff before it commits.

## Balance kinds

Most strategies track one kind, `dkp`. Some track more:

| Strategy | Kinds |
|---|---|
| Everything DKP-shaped | `dkp` |
| EPGP | `ep` and `gp` — two balances, not one |
| Suicide Kings | `sk_position` — the balance is a position in an ordering |

The ledger does not care which kinds exist; it sums entries per `(account, pool, kind)`. That is what
lets a system whose "balance" is an entire ordering live in the same append-only log as one whose
balance is a number.

## Invariants: the part strategies cannot configure

A strategy declares which invariants its proposals must satisfy. The ledger checks those, **plus** a
set that applies to every batch regardless of what the strategy says.

| Invariant | Means |
|---|---|
| `NoFloat` | Every amount is an integer. Always on, not declarable. |
| `BatchNonEmpty` | A batch with no entries is a bug, not an event |
| `SeqMonotonic` | Sequence numbers only go up |
| `EntriesReferenceLiveAccounts` | No entries against deleted accounts |
| `SumZero(kind, batch)` | Zero-sum award entries sum to exactly zero |
| `NonNegative(kind, floor)` | A balance may not drop below the floor |
| `MonotoneNonDecreasing(kind)` | EPGP's gear points never fall except by reversal |
| `Permutation(sk_position)` | Suicide Kings positions stay a bijection — no duplicates, no orphans |
| `RatioPreserved(ep, gp)` | An EPGP decay batch scales both kinds identically, so priority is unchanged |
| `Conserved(kind, total)` | The total across all accounts is unchanged |

The first four cannot be waived by anything. If a strategy proposes a batch that violates one, the
batch is rejected and the officer sees an error — not a silently mangled ledger.

See [Invariants](invariants.md) for the mechanism behind each.

## The shipped strategies

Every number below is centipoints rendered as points (× 100 in the ledger), and every batch sums to
exactly zero — the counterparty is the guild bank, so points are moved rather than minted. These are
the worked examples officers argue from; the arithmetic is asserted by a committed golden proposal per
planner under `test/golden/strategy/<id>/`.

### `tick` — earn per attendance snapshot

A four-hour raid on twenty-minute ticks at **10.00 a tick**, with `standby` configured at **5000 bp**
(half a share). The weight is the number of ticks that raider was present for.

| Raider | Ticks | Role | Earned |
|---|---|---|---|
| Tankguy — there from form-up | 12 | present | 12 × 10.00 = **120.00** |
| Healbot — on standby all night | 9 | standby | 9 × 10.00 × 0.5 = **45.00** |
| Druidgal — left at tick 8 | 8 | *(none)* | 8 × 10.00 = **80.00** |

```
debit   guild_bank   −245.00
credit  Tankguy      +120.00
credit  Healbot       +45.00
credit  Druidgal      +80.00
        Σ entries   =   0.00
```

A role the config does not name earns `default_multiplier_bp`, a full share unless the pool says
otherwise. Multiplication happens before division and the result is **floored**: a half share of a
0.75 tick is 0.37, not 0.38 — rounding a ratio up credits a centipoint the rate did not ask for, on
every entry, every raid.

A kill tick and a first-tick bonus are not knobs here. They are per-event values
(`event_type.default_tick_value_cp` → `raid_tick_credit.value_cp`) and arrive as the event's own
amount, because two places for one number is a disagreement waiting for a raid night. Seconds-per-tick
and the mid-tick grace period decide *when a tick exists* and belong to raid ingest, not to the
arithmetic.

### `cap` — a ceiling on hoarding

Hard cap **100.00**, soft cap **80.00**, over-cap earn ratio **2500 bp** (a quarter). Earning a 10.00
tick:

| Balance before | Earned | Why | After |
|---|---|---|---|
| 50.00 | **10.00** | wholly below the soft cap | 60.00 |
| 78.00 | **4.00** | 2.00 in full, then 8.00 at a quarter | 82.00 |
| 95.00 | **2.50** | wholly above the soft cap; still inside the hard cap's 5.00 of headroom | 97.50 |
| 100.00 | **—** | at the ceiling, so no entry at all | 100.00 |

The soft cap reduces and the hard cap then clamps. Only the part **above** the soft cap is reduced —
reducing the whole credit would penalise the part that was still under it.

A balance can be above the hard cap anyway: an import, a correction, or a ceiling an officer lowered
yesterday. The **cap run** trims it, on the same cadence and the same `decay_run` table as decay:

```
2026-W31   Tankguy at 123.45, ceiling 100.00
           debit   Tankguy      −23.45
           credit  guild_bank   +23.45      → Tankguy 100.00
2026-W31   re-run  → nothing above the ceiling → the run is recorded `skipped`, no second batch
```

Idempotence is structural rather than a flag: the second run reads the balances the first one left.

### `start_points` — an opening balance for recruits

A grant of **200.00**, run against a roster of three:

| Account | Balance | Ledger history | Granted |
|---|---|---|---|
| Newguy — joined this week | 0.00 | none | **+200.00** |
| Tankguy — raiding since Kunark | 812.00 | yes | — |
| Oldhand — earned it all, spent it all | 0.00 | yes | — |

```
debit   guild_bank   −200.00
credit  Newguy       +200.00
```

**Oldhand is the whole design.** A zero balance and an empty history are different facts, and only the
second is a new member; a planner that tested the balance would pay a spent-out veteran a second
opening grant. Eligibility is "has no ledger entry in this pool", so the grant is itself history and a
re-run — in the same cadence period or any later one — credits nobody.

### `attendance_weighted` — the raid's pot, divided by attendance

The alternative earn rule to `tick`, and the difference is where the fixed number sits. `tick` pays a
fixed amount **per unit of attendance**, so a forty-strong raid costs the guild's economy twice what a
twenty-strong one does. This pays a fixed amount **per raid**, so the pot is constant and turning up
buys a bigger slice of it.

A pot of **1000.00** over a raid whose three attendees were present for 12, 9 and 8 of its ticks:

| Raider | Ticks | Share | Earned |
|---|---|---|---|
| Tankguy | 12 | 12⁄29 | 1000.00 × 12 ÷ 29 = **413.79** |
| Healbot | 9 | 9⁄29 | 1000.00 × 9 ÷ 29 = **310.35** |
| Druidgal | 8 | 8⁄29 | 1000.00 × 8 ÷ 29 = **275.86** |

```
debit   guild_bank   −1000.00
credit  Tankguy       +413.79
credit  Healbot       +310.35
credit  Druidgal      +275.86
        Σ entries   =    0.00
```

The three quotas floor to 413.79, 310.34 and 275.86, which is **one centipoint short of the pot**. The
largest remainder — Healbot, at 14⁄29 of a centipoint — takes it, and at equal remainders the tiebreak
is the account id, ascending. That is the shared allocator, the same one a zero-sum split uses:
rounding each share independently would mint or destroy a centipoint on every raid, forever.

A raid nobody was credited any attendance for produces **no batch** rather than a pot posted to a
system account: unlike a price a buyer has already paid, a pot does not exist until the batch creates
it, so there is nothing in flight that has to land somewhere.

**The ranking score is not here yet.** The guide describes this rule's headline as a standing of
`balance × attendance %` — a ranking score, not a balance — and that number needs the attendance
statistics that land in Phase 4
([#223](https://github.com/prokopto-dev/dragonkillparty/issues/223)). What ships is the earn rule;
`Priority` ranks by balance until then, and spending has always deducted raw points.
### `decay_percent` — the weekly haircut

A rate of **1000 bp** (10%) a week on a balance of 500.00. Each period is planned against the balance
the previous one left, so a run that was missed while the box was down is caught up one batch per
period rather than one batch for the gap:

| Period | Posted batch | Balance after |
|---|---|---|
| 2026-W31 | −50.00 | 450.00 |
| 2026-W32 | −45.00 | 405.00 |
| 2026-W33 | −40.50 | 364.50 |
| 2026-W34 | −36.45 | 328.05 |

The amount is **floored**, never rounded: 0.09 at 10% is 0.009, which is nothing at all, and the member
gets no entry rather than losing a centipoint the rate did not ask for.

Three knobs, and the last two are the settings guilds argue about:

| Knob | What it does |
|---|---|
| `decay_bp` | the rate. 0 is refused — a pool that does not decay leaves its over-time slot empty |
| `floor_cp` | the balance decay stops at. The run lands **on** the floor rather than crossing it |
| `negative_balances` | what a debt does: `skip` (default), `toward_zero` (debt forgiveness), `preserve_sign` (the debt grows, and needs a floor below zero) |

**Under `toward_zero` the bank sometimes has no row at all.** It is the only policy that credits and
debits in the same batch — a debt is forgiven while a positive balance is docked — so a period whose
forgiveness happens to equal its haircut is funded by the members between themselves. The batch still
sums to exactly zero; the bank simply did not move, and an entry of zero is not a legal row, for the
same reason the member whose 0.09 rounds away gets none.

**A re-run of a period is the same batch, not a second haircut.** A percentage decay is not
idempotent the way a cap trim is — 10% twice is 19% — so what makes the retry safe is that every
balance is read at the period's own as-of `seq`. Two runs read the same snapshot, propose the same
batch, and the `(pool_id, kind, cadence_period)` key collapses them into one.

### `decay_window` — earnings expire

No compounding: an earning simply stops counting once it is older than the window, and it stops
counting by being **removed**. A 90-day window, with the run that sees the April earnings cross the
boundary:

| Earned | Amount | Counts on 2026-08-03? |
|---|---|---|
| 2026-07-20 | 190.00 | yes |
| 2026-06-01 | 240.00 | yes |
| 2026-04-02 | 310.00 | **no** — 123 days old |

```
2026-W31   the slice holding the 2026-04-02 earning has aged out
           debit   Tankguy      −310.00
           credit  guild_bank   +310.00      → Tankguy 430.00
```

Each run expires one slice of the pool's own history — everything credited between the previous run's
boundary and this one's — so consecutive runs tile the log and **every earning expires exactly once**.
The removal is a debit, so it can never be mistaken for an earning by a later run; that is what stops
a window decay from quietly re-expiring its own batches.

A member who has already spent what the window is about to expire is **not** pushed into debt for it:
the run takes what is left above `floor_cp` and no more. What it gives up is a carry-forward — the part
it could not take is forgiven, because the window is a rule about the age of an earning rather than a
debt the pool collects.

1.0 ships the hard cutoff. The linear taper the guide offers as an alternative needs to know how old
each earning is rather than which side of the boundary it fell on, and is
[#221](https://github.com/prokopto-dev/dragonkillparty/issues/221).

### `fixed_price` — spend at a published price

Covered with its price table and its zero-sum split in
[Choosing a DKP system](../guides/choosing-a-dkp-system.md#fixed_price--published-price-list).

### `zero_sum` — spend into the other raiders' pockets

What the winner pays, the other raiders receive: no points enter circulation at loot time and none
leave it. Tankguy wins a Cloak of Flames at **300.00** on a night eight raiders attended, and the
default excludes the winner from the split, so seven share it:

```
debit   Tankguy      −300.00
credit  6 raiders     +42.86 each
credit  1 raider      +42.85
        Σ entries   =   0.00
```

300.00 ÷ 7 is 42.857…, so five credits round up and two round down — 5 × 42.86 + 2 × 42.85 = 300.00
exactly. Rounding each credit independently would produce 300.02 and mint two centipoints on every
item, forever. That is the whole reason the allocator is shared rather than written per strategy.

Three knobs, one per decision the guide tells a guild to take before switching it on:

| Knob | Answers |
|---|---|
| `winner_share` | `excluded` (the default: the winner pays 300.00 and gets none back) or `included` (the winner is an attendee like any other and nets 300.00 × 6⁄7) |
| `solo_policy` | A kill with nobody else on it: `guild_bank`, `write_off`, or `free` — and `free` writes no batch at all rather than a batch that moves zero |
| `default_price_cp` | The price when the officer names none and the item carries none. Resolution is officer → catalogue → this, the same order `fixed_price` uses and the same code |

Who is in the split is **not** a knob: a pure planner cannot see a raid or a tick, so the beneficiary
list is the award ingest path's to fill, and tick-weighting is expressed as the weights on it.

**Reversing one reverses the whole split** — the debit and every credit, together, in one batch. What
it does not do is replay: reversing a six-month-old award leaves every intermediate balance
arithmetically "wrong" under the new history, and an append-only ledger cannot fix that. One
compensating batch at today's `seq` is the rule ([The ledger](ledger.md)).
### `auction_open` — ascending, and the leader pays their own bid

Minimum **100.00**, increment **25.00**. Every accepted bid is the minimum plus a whole number of
increments, which is what refuses the 160.00 below before anything else looks at it.

| Bid | Outcome |
|---|---|
| Tankguy 100.00 | accepted, leader |
| Healbot 125.00 | accepted, leader |
| Healbot 160.00 | **refused** — 160.00 is not 100.00 + k × 25.00 |
| Healbot 175.00 | accepted, leader |
| Tankguy 200.00 | accepted, leader — and the session closes |

```
debit   Tankguy      −200.00
credit  guild_bank   +200.00
        Σ entries   =   0.00
```

An ascending auction has already revealed what the item is worth to everyone else, so the winner pays
their own bid. Two bids equal in amount *and* in the microsecond they were placed are broken by a
**seeded roll**, and the seed is carried onto the resolution so the flip is replayable.

The example above is one rung bidding against itself — the amounts are the pre-ladder ones, and a
tiered guild's main-tier bids are single or low double digits. Where the session spans rungs, the
**highest rung holding an accepted bid takes the item before any amount is compared**: a 10.00 main
bid beats a 350.00 alt bid, and the alt's number is never compared against the main's.

### `auction_sealed` — hidden bids, first price or second price

Bids of **350.00**, **280.00** and **150.00**, increment **5.00**:

| Pay rule | Winner pays | Why |
|---|---|---|
| First price | **350.00** | their own bid |
| **Second price** (the default) | **285.00** | the runner-up plus one increment |
| Second price, sole bidder | **the minimum** | there is no runner-up to price against |
| Second price, bids 350.00 and 349.00 | **350.00** | 349.00 + 5.00 exceeds the winning bid, so the clamp holds |

Two rules the arithmetic will not bend on. **Nobody ever pays more than they bid** — that is the
promise second price exists to make, and it is why the runner-up's number is clamped rather than
charged. And **the runner-up is the highest bid from a different account**: bids are append-only and
a bidder may hold several, so the row below the winner is frequently the winner's own earlier bid.

**And the runner-up is on the winning rung.** Those three bids are one tier bidding against itself; at
three figures it is not `main`, because when mains only compete with mains the numbers are small by
design — the ladder's minimum is 5.00 and its increment 1.00. Put one main into the same session and
both halves of the rule change:

| Bidder | Rung | Bid | Outcome |
|---|---|---|---|
| Tankguy | `alt` | 350.00 | loses — a lower rung is never compared against the winning one |
| Sneakyguy | `alt` | 280.00 | loses |
| Healbot | `main` | 10.00 | **wins, and pays 5.00** — the minimum, being alone on their rung |

Healbot pays the minimum rather than 351.00, and that number is the whole deliverable: a second price
that reached one row further down the list would overcharge them by a factor of seventy with an
arithmetic trail that looks correct at every step. Add a second main bidding 9.00 and Healbot pays
10.00 — the runner-up *in `main`* plus one increment. See
[Tier outranks amount](../guides/auctions.md#tier-outranks-amount).

**A tie is reported and never resolved automatically.** Two mains who both bid 10.00 are equal in the
only fact a blind auction collected, so the settlement awards nobody and instead names exactly those
two: the item is decided **by hand**, in a **rebid round open to the tied bidders and to nobody else**
([#248](https://github.com/prokopto-dev/dragonkillparty/issues/248)). Each of them has two moves —
**bid more than the tie value** (the floor is one centipoint above it; standing on the same number is
what everybody already did) or **pass**, and they may not all pass: the last one standing takes it at
the tie amount. That is what makes the round end without a coin flip, and it is why no roll, no bid
sequence and no officer flag decides a sealed tie: settling this rule touches the seeded Rng on no
path at all. A resolution that reports a tie names no winner, no price and no winning rung. The round
itself is Phase 6 ([#247](https://github.com/prokopto-dev/dragonkillparty/issues/247)); what ships
here is the arithmetic it starts from.

Ties in an *open* auction are a different problem and not this one: bids are visible while the
session runs, so an equal amount there is two clients submitting in the same instant — a race for the
session layer, also Phase 6.

### `relative_bid` — a share of a bank frozen at the session's open

| Bidder | Balance at open | Commits | Share |
|---|---|---|---|
| Tankguy | 900.00 | 360.00 | 4000 bp |
| Healbot | 500.00 | 275.00 | **5500 bp** |

```
debit   Healbot      −275.00
credit  guild_bank   +275.00
        Σ entries   =   0.00
```

Healbot wins on the share while paying 85.00 fewer points, which is the whole model: a hoarder pays
more absolute points for the same priority. Every balance is read at the session's `seq_at_open`,
**positionally** — resolving against live balances would let a decay run committed mid-auction rewrite
everybody's percentage, and the bug only appears on the one night a decay job overlaps a raid. A bid
that is no longer a share of its frozen balance is ignored, and the resolution says how many were.

**Two equal shares are an equal claim, and the larger bank does not break the tie.** Had Tankguy
committed 450.00 of his 900.00 against Healbot's 250.00 of 500.00, both stand at 5000 bp: the
settlement falls through to the earliest bid and then to a seeded roll, exactly as
[the tie-break chain](../guides/auctions.md#tie-breaks) describes, and never to the 200.00 more that
the bigger bank put behind the same percentage. Awarding it on that number would hand the item to the
hoarder for hoarding, one rung below where this rule removed that advantage
([#244](https://github.com/prokopto-dev/dragonkillparty/issues/244)).

### `roll` — a seeded die, and a tie is a new round

`/random 1 100` for three entrants, drawn from the injected seeded generator in account order, with
the seed persisted onto the resolution and onto the batch. The default is a **free** roll: nothing
moved, so no ledger batch is written at all. Setting a win cost of 1.00 makes it the `+1` system —
the counter is the balance:

```
debit   Tankguy       −1.00
credit  guild_bank    +1.00
        Σ entries   =  0.00
```

Two raiders on 97 award **nobody**: "rolls are immutable; a re-roll on a tie is a new round, not an
edit", so the resolution names the tie and the officer opens another session between them.

### `loot_council` — spend by officer decision

Councillors decide who gets the drop and write down why. There is no bidding and no price table: the
charge, if there is one, is **the number the council named**, and the planner's whole job is to record
it faithfully. A charge of **25.00** decided for a Cloak of Flames:

```
council: main tank, no CoF yet, unanimous
        debit   Tankguy      −25.00
        credit  guild_bank   +25.00
                Σ entries   =   0.00
```

| Config | The decision | Debited |
|---|---|---|
| `charge_cp: 2500` | the council names no amount | **25.00** — the pool's default |
| `charge_cp: 2500` | the council names 3.00 | **3.00** — the council's number wins |
| `charge_cp: 2500` | the item is priced at 99.00 in the catalogue | **25.00** — the catalogue price is *ignored* |
| `charge_cp: 0` (the default) | any | **—** no ledger batch at all |

The catalogue row is the interesting one. `fixed_price` resolves officer → item → config; a council
reads the published price and does not use it, because a council that used the price table would be
`fixed_price` with extra steps. And a council that charges nothing is a real council — the common one
on P99 — so an award at 0 plans nothing rather than writing an entry of zero, which the ledger would
refuse (`CHECK (amount_cp <> 0)`, and `BatchNonEmpty` is unwaivable). The item award is still recorded;
a decision that moved no points simply has nothing to say to the ledger.

Two things follow from "recorded, not computed", and both are asserted in the tests rather than
promised here: the planner **never reads a balance** — whether the winner can afford it is the
ledger's question, answered at commit time by the `NonNegative` floor this proposal declares — and
`require_reason` (on by default) refuses a decision that records no rationale. Every other strategy
leaves the reason to the API edge because its arithmetic speaks for itself; here there is no
arithmetic to inspect afterwards, so the sentence an officer wrote *is* the audit trail.

What it does **not** record yet: nominations, each councillor's vote, and the conflict-of-interest
flag when a councillor is a candidate for the item under vote. Those are facts about a deliberation
rather than about money, they belong beside `item_award.award_type = 'loot_council'` in the loot
tables, and a planner may only propose entries —
[#219](https://github.com/prokopto-dev/dragonkillparty/issues/219).

### What each one refuses

Every rule answers one question, and says so rather than guessing at the others. The earn and
over-time rules refuse to spend: `tick.PlanAward`, `cap.PlanAward` and
`attendance_weighted.PlanAward` return `ErrUnsupported` (a 501 naming the strategy), because an earn
rule has no price list and inventing one would be a second copy of `fixed_price`'s price resolution
that could then disagree with it. Both decay rules refuse `PlanAttendance` as well as `PlanAward` — a
haircut is not a tick — and name the kind of rule to pair them with.

The spend rules refuse in the same spirit from the other side: `zero_sum` and the four bidding rules
return `ErrUnsupported` from `PlanAttendance` and `PlanDecay`, naming the rule to pair with, because a
spend rule has no tick value and no cadence. That is not a gap; it is what makes a pool need three
rules rather than one, which is the section below.

`loot_council` refuses one thing extra, and the refusal is the strategy: `Priority` is
`ErrUnsupported`, because **the council is the ranking**. A rank computed here would be a number the
council did not use, rendered beside a decision it did not inform. Councils should still publish a
score — [the guide](../guides/choosing-a-dkp-system.md) is emphatic that councils without one decay
into loot-council fatigue — and that score comes from the rule that computes it, beside this one:
`attendance_weighted` is the earn rule built for exactly that pairing.

## A pool composes three rules

A pool answers three questions, and holds one strategy — with its own configuration — for each
([ADR-0026](../adr/0026-three-rules-per-pool.md)):

| Slot | Question | Answers |
|---|---|---|
| earn | How are points earned? | `PlanAttendance`, `PlanAdjustment` |
| spend | How are points spent? | `PlanAward`, `Spendable`, `Priority`, `PriceHint`, `ValidateBid`, `SettleAuction` |
| over time | What happens to points over time? | `PlanDecay` — the cadence run |

```
Velious Main       tick to earn · fixed price to spend · cap over time
Cross-class Rares  suicide kings
```

Each strategy declares which slot it may occupy (`PointStrategy.RuleKind`), so `tick` configured as a
spend rule is refused **on the settings form** rather than during loot. There are no fallbacks: a pool
with no over-time rule refuses a decay run by name instead of asking its earn rule to improvise one.

**Which of the three planned a batch is recorded on the batch.** `ledger_batch.strategy_id` is that
column, and it is load-bearing rather than descriptive — a reversal routes on it, so the repair is
always planned by the rule that planned the original. That is the whole reason the choice needed an
ADR: the column is written on every row of an append-only table.

One raid feeds several pools. Event types map to pools, and each mapping carries the `no_attendance`
flag that decides whether that event counts toward attendance percentages in that pool.

## Extending without forking

Two tiers, one shipping now and one designed now:

| Tier | What | When |
|---|---|---|
| In-tree registry | The shipped strategies. Typed, tested, fast. Covers essentially every guild. | 1.0 |
| `webhook` strategy | Planners post the event and context to a URL you control and receive a signed proposal back — **which is then validated against the same invariants as any in-tree strategy** | 1.2 |

The second is the real answer to "extend without forking", and it is safe precisely because the
proposal is still checked. An external rule engine can be wrong; it cannot corrupt the ledger.

It ships in 1.2 rather than 1.0 deliberately: designing an extension interface with zero implementors
produces the wrong interface. `PointStrategy` *is* the interface, and the first external implementor
will tell us what it got wrong.

## Adding one

If your guild's rules cannot be expressed by configuring a shipped strategy, adding one is a
contained piece of work: choose the question it answers and the balance kinds, write the JSON schema,
implement the planners, declare the invariants, register it, and write the property tests.

See the `add-strategy` skill in `.claude/skills/` and
[Architecture overview](../development/architecture-overview.md).

## Next

- [Invariants](invariants.md) — every rule and the mechanism enforcing it
- [The ledger](ledger.md) — what the proposals are committed into
- [Choosing a DKP system](../guides/choosing-a-dkp-system.md) — the catalogue, with worked numbers
