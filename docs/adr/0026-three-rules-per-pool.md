# ADR-0026 — A pool composes three rules, and `ledger_batch.strategy_id` names which one planned

**Status:** accepted · **Date:** 2026-08-13 · **Deciders:** owner

## Context and problem statement

`pool.strategy_id` is **singular**, while the shipped catalogue lists earn rules, spend rules and
over-time rules as **separate** strategies ([choosing a DKP system](../guides/choosing-a-dkp-system.md):
"a pool answers three questions"). Both design pages already recorded the contradiction, and #193 made
it concrete: a pool whose `strategy_id` is `tick` genuinely cannot award an item, because
`Tick.PlanAward` returns `ErrUnsupported` and inventing a price there would be a second copy of
`fixed_price`'s price resolution that could then disagree with it. Each refusal is right on its own
and the set of them is not a guild configuration. Whatever is chosen decides what `strategy_id` means
on every row of an append-only table, so it cannot be settled by an implementation detail later.

## Considered options

| Option | For | Against |
|---|---|---|
| 1 — **a rule per question on the pool**: `earn_`/`spend_`/`over_time_strategy_id`, each with its own config | Truest to the catalogue and to how a guild describes its own system; each strategy stays single-purpose, so there is one price resolution and one decay cadence in the tree | A schema change, a migration and (in Phase 2) an API change; three columns can be configured into a pool that answers only two of the three questions |
| 2 — a composite strategy: one `strategy_id` naming a combination, the rest as its config | No schema change | The combination explosion moves into the settings form and into the id vocabulary, and `strategy_id` stops naming a planner — it names a tuple, which no `ByID` can resolve |
| 3 — keep it singular and make every shipped strategy total | Cheapest to describe | N copies of price resolution and N copies of the decay cadence: exactly the drift the shared allocator exists to prevent, in the two places it hurts most |

## Decision outcome

**Chosen: 1.** A pool holds three `(strategy_id, config_json)` pairs. Each rule is resolved through
`strategy.ByID` and checked against the slot it was put in; `strategy.Rules` routes every planner to
the one rule that owns the question, with **no fallbacks** — an empty slot refuses by name rather than
borrowing another rule's answer.

| Slot | Answers |
|---|---|
| earn | `PlanAttendance`, `PlanAdjustment` |
| spend | `PlanAward`, `Spendable`, `Priority`, `PriceHint`, `ValidateBid`, `SettleAuction` |
| over time | `PlanDecay` |

`PlanAdjustment` is earn's because the earn rule is the one a DKP pool cannot do without and the floor
an overdraw is refused against is the one it earns under. `Spendable` and `Priority` are spend's
because both are answers to "what may this buy?", which a pool with nothing to buy does not have.

**`ledger_batch.strategy_id` therefore records WHICH OF THE THREE planned each batch**, and that is
now load-bearing rather than descriptive: `Rules.PlanReversal` routes on it, delegating to the rule
whose `ID()` matches the original's. A batch planned by a rule the pool no longer runs is refused with
both ids named — the ledger is append-only, so a reversal must be planned by the rules that were in
force, and guessing would negate committed entries under rules nobody chose.

**The slot a strategy may occupy is declared by the strategy**, as `RuleKind()` on `PointStrategy`
— a method rather than a table in the catalogue, because a table is a second list that can disagree
with the file it describes. `tick` is earn, `fixed_price` is spend, `cap` and `start_points` are over
time: both post on the `decay_run` cadence ([ADR-0024](0024-one-run-table-scoped-by-kind.md)) and
neither answers `PlanAttendance` — `start_points.PlanAttendance` already returned `ErrUnsupported`.
`TestPoolConfig_Resolve_StrategyInTheWrongSlot_IsRefused` is the mechanism; `ErrWrongRuleKind` is the
sentinel, and it fires at configuration time rather than at 19:05 on a raid night.

The singular `pool.strategy_id`, `strategy_version` and `strategy_config_json` are **superseded, not
dropped**. Dropping them is destructive *and* a 12-step rebuild of a table with three children
(`ledger_batch`, `pool_config_change`, `decay_run`), which needs `!destructive-migration` and a human
(`.claude/rules/migrations.md`). Migration `000006` adds the six columns and backfills
`earn_strategy_id` from `strategy_id`, idempotently and identically on a fresh install and on an
upgrade.

### Consequences

- Good, because every shipped strategy answers exactly the questions it has an opinion about, and a
  guild composes `tick` + `fixed_price` + `cap` the way the guide already told them their system works.
- Good, because a reversal is provably planned by the rule that planned the original, enforced by the
  column rather than by a convention.
- Good, because a misconfiguration — `tick` in the spend slot — is a refusal on the settings form
  instead of a 501 during loot.
- **Bad, because `cap.PlanAttendance` is unreachable through a composed pool.** `cap` occupies the
  over-time slot for its cap run, so its soft-cap earn reduction — a documented, tested behaviour with
  a worked table in [Point strategies](../concepts/strategies.md) — has no slot to be reached from. A
  pool that wants it wants two earn rules, which this decision does not give it
  ([#215](https://github.com/prokopto-dev/dragonkillparty/issues/215)).
- **Bad, because `pool` now carries nine strategy columns where it needs six**, and will until a
  destructive migration retires the singular three. Two of them are live nowhere and readable
  everywhere.
- **Bad, because three slots is a guess at the shape of the question.** A guild wanting two earn rules,
  or a spend rule that also decays, has to wait for a fourth column and another one of these.

### Reversal cost

A release. The Go side is one file and its routing table; the column set is additive, so retiring it
is a migration that drops six columns rather than data anybody can lose. What does not reverse is the
meaning already written into committed rows: `strategy_id` on every batch planned after this names one
of three rules, and a later model has to keep reading it that way.
