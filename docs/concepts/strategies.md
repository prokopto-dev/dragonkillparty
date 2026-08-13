# Point strategies

**Status:** the strategy engine is Phase 1 and is landing strategy by strategy. `fixed_price`,
`tick`, `start_points` and `cap` ship today — see [the worked examples](#the-shipped-strategies)
below; the rest of the catalogue follows. This page explains the design; the per-strategy
configuration reference is generated from each `ConfigSchema` into `reference/strategies/<id>.md`.

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

### `fixed_price` — spend at a published price

Covered with its price table and its zero-sum split in
[Choosing a DKP system](../guides/choosing-a-dkp-system.md#fixed_price--published-price-list).

### What each one refuses

`tick`, `start_points` and `cap` answer one question each, and say so rather than guessing at the
others: `tick.PlanAward` and `cap.PlanAward` return `ErrUnsupported` (a 501 naming the strategy),
because an earn rule has no price list and inventing one would be a second copy of `fixed_price`'s
price resolution that could then disagree with it. How an earn rule, a spend rule and an over-time
rule compose inside one pool is the open question in the box below.

## Pools compose strategies

A pool is one currency with one strategy configuration. A guild composes systems by running several
pools:

```
Velious Main       tick to earn · sealed second-price to spend · window decay
Cross-class Rares  suicide kings
```

One raid feeds both. Event types map to pools, and each mapping carries the `no_attendance` flag that
decides whether that event counts toward attendance percentages in that pool.

> One open question: `pool.strategy_id` is singular in the schema while the shipped catalogue names
> earn rules and decay rules as separate strategies. How several rules compose inside one pool is a
> genuine contradiction in the design documents, tracked for resolution before Phase 1. It does not
> affect the arithmetic of any individual rule.

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
contained piece of work: choose the balance kinds, write the JSON schema, implement the planners,
declare the invariants, register it, write the property tests, add the reference page and a fixture.

See the `add-strategy` skill in `.claude/skills/` and
[Architecture overview](../development/architecture-overview.md).

## Next

- [Invariants](invariants.md) — every rule and the mechanism enforcing it
- [The ledger](ledger.md) — what the proposals are committed into
- [Choosing a DKP system](../guides/choosing-a-dkp-system.md) — the catalogue, with worked numbers
