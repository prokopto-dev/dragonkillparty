# Point strategies

**Status:** the strategy engine lands in Phase 1. This page explains the design; the per-strategy
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
