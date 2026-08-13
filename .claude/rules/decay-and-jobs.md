---
paths: ["internal/strategy/decay_*.go", "internal/strategy/cap.go", "internal/strategy/start_points.go", "internal/jobs/**", "internal/ledger/verify*.go", "cmd/dkp/verify_ledger.go"]
description: Decay, cap and start-points are POSTED on a cadence — the cadence_period label, the UNIQUE(pool_id, cadence_period) idempotency key, catch-up after downtime, and the nightly replay that is a correctness dependency rather than a nicety.
---

# Decay, cadence and the replay job

Three strategy families and one job share a single mechanism, and this file is their one source.
`decay_percent`, `decay_window`, `cap` and `start_points` all **post explicit ledger batches on a
cadence**, keyed so a re-run is a no-op; `dkp verify-ledger` and the nightly replay are what prove
the result. `.claude/rules/ledger-and-strategy.md` owns batch shape and the invariant vocabulary,
`.claude/rules/jobs-and-events.md` owns the queue — neither restates what is here.

The worked example is already in the tree: `FixedPrice.PlanDecay` in `internal/strategy/fixed_price.go`,
against the `strategy.DecayRun` shape. Read it before writing a second one.

## 1. Posted, not computed

Canonical §10, and the one line that generates most of this file:

> **Decay is posted, not computed** — explicit batches with idempotency key `(pool_id, cadence_period)`.

A run emits a `ledger_batch` with `kind = kinds.KindDecay` (or `KindCap`, `KindStartPoints`) and
`source = kinds.SourceSystem` — named through `internal/ledger/kinds`, never as a literal. What
follows is not negotiable:

- **`Spendable()` is a plain SUM over entries as of a `seq`.** Any time-dependent factor, decay
  multiplier, window taper or `now`-derived weight inside it is a blocker. It makes a balance
  un-explainable to the member looking at their own history, and it **double-applies the moment a
  decay batch also exists**.
- **Computed weighting is permitted in `Priority()` and nowhere else.** Ranking is a view; a balance
  is a fact.
- Balances a run reads are read **positionally, at `DecayRun.AsOfSeq`** — never temporally. A batch
  committed while the run is planning must not change what the run decayed, and a backdated
  `effective_at` must not change what a past balance was.

> ✗ **Not copied:** decay computed at read time by APA transforms over cached totals
> (`docs/design/01-domain-model.md` §9.7). A balance is always literally a `SUM`, so "why did my
> points change?" is answerable by pointing at a row. That answer is the product.

`decay_window` is the exception that proves the rule and the one most likely to be got wrong:
"earnings older than the window stop counting" still means **posting a batch that removes them**, not
filtering the sum. The window is an input to the planner, never a predicate in a balance query.

## 2. `cadence_period` is a label, not a duration

The vocabulary is three forms, from the `decay_run` DDL (`docs/design/01-domain-model.md` §12.3):

| Form | Example | Cadence |
|---|---|---|
| ISO week | `2026-W31` | weekly |
| Year-month | `2026-08` | monthly |
| Per raid | `raid:01J7…` | on each raid |

It is a **string identity**. That is the whole idempotency design: "has this period already run?" is
an index lookup on a text column, never date arithmetic over `executed_at`. Two consequences worth
stating out loud, because the arithmetic version is what a reasonable person writes first:

- **Never derive the next period by adding a duration.** A week is not `168 * time.Hour` twice a
  year, and a month is not any number of hours. Compute the label from an instant plus a zone.
- **Bucketing is guild-local.** `guild.timezone` is IANA and "buckets every `*_day` column";
  `guild.week_start` (0 = Sunday … 6 = Saturday, default Monday) decides where a week begins, so
  `2026-W31` is *this guild's* week 31 and not necessarily ISO-8601's. Both are real columns rather
  than `settings_json` keys precisely because things like this depend on them.

DST and month boundaries are therefore a **labelling** question answered once, in Go, and not a
duration question at all. The ROADMAP requires the test that says so (P9, plus decay idempotency
across DST and month boundaries), and `testing/synctest` is how it is written — advance three weeks
instantly, assert one batch per missed period and no double-post. `time.Sleep` is grep-banned.

## 3. `decay_run`, and the index that is the actual guarantee

```sql
CREATE TABLE decay_run (
  id      TEXT NOT NULL PRIMARY KEY,
  pool_id TEXT NOT NULL REFERENCES pool(id),
  kind    TEXT NOT NULL                           -- 'decay' | 'cap' | 'start_points'
          CHECK (kind IN ('decay','cap','start_points')),
  cadence_period TEXT NOT NULL,                   -- '2026-W31' | '2026-08' | 'raid:<ulid>'
  scheduled_for_at INTEGER NOT NULL,
  executed_at INTEGER NULL,
  state   TEXT NOT NULL DEFAULT 'planned'
          CHECK (state IN ('planned','preview','committed','skipped','failed')),
  dry_run_result_json  TEXT NOT NULL DEFAULT '{}',
  config_snapshot_json TEXT NOT NULL DEFAULT '{}',
  ledger_batch_id TEXT NULL REFERENCES ledger_batch(id),
  triggered_by TEXT NULL REFERENCES app_user(id),
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_decay_period ON decay_run(pool_id, kind, cadence_period);
```

**`ux_decay_period` is the guarantee; the job is not trusted.** Three callers race for it and all
three are ordinary: the periodic job firing twice after a restart, a retry after a partial failure,
and an officer clicking "run decay now" while the nightly job is mid-flight. A check-then-insert in
Go closes none of them. Insert the run row inside the same `store.Tx` as the commit and let the
constraint arbitrate.

- `ledger_batch_id` is a **forward pointer**: the mutable fact table points at the money, never the
  reverse (`.claude/rules/ledger-and-strategy.md`). The ledger is append-only, so it could not point
  back even if you wanted it to.
- `state` is a real state machine. `planned` → `preview` (a dry run exists in `dry_run_result_json`)
  → `committed`; `skipped` and `failed` are terminal and both keep the row, which is what makes a
  period that produced nothing distinguishable from a period nobody ran.
- **Two layers of idempotency, deliberately.** `ux_decay_period` stops a second *run row*;
  `ux_batch_idem` — unique per pool — stops a second *batch*. Set the batch's `idempotency_key` from
  the same identity, through **one helper**, so the job path and the officer path cannot disagree
  about the spelling. The batch key must include the **kind**: `cap` and `start_points` post on the
  same cadence vocabulary, so `2026-W31` alone would make a cap run collide with that period's decay
  run.

> **Settled by [ADR-0024](../../docs/adr/0024-one-run-table-scoped-by-kind.md), and the box that used
> to be here is why the column exists.** That kind-scoping argument applies just as hard one line up,
> and `ux_decay_period` did not have it: the design keys all three families on
> `(pool_id, cadence_period)` (canonical §10, `docs/api/idempotency-and-concurrency.md`) and defines
> exactly **one** run table, so a cap run for a period the decay run already took failed an index
> designed to stop a *repeat* — and an idempotent job is supposed to read that as "already done" and
> exit 0. The cap then silently never applies, with a green dashboard.
>
> `kind` is therefore **inside** the unique index, not beside it. One table, one lifecycle, one
> dashboard, three families; a `decay_run` row is not necessarily decay, and the name is the price.
> The two alternatives the ADR rejected — a table per family, and `decay_run` being decay-only — are
> recorded there with what each costs. [#206](https://github.com/prokopto-dev/dragonkillparty/issues/206)
> is closed by [#192](https://github.com/prokopto-dev/dragonkillparty/issues/192)'s PR, which builds
> the table.

## 4. A run that moves nothing must not commit

The shipped invariant engine forces this and no design document says it, so it is here:

- `AmountsNonZero` — `ledger_entry` carries `CHECK (amount_cp <> 0)`. An account whose decay rounds
  to zero gets **no entry**. Drop it; do not write a zero, and **do not round up** — rounding a decay
  up takes a centipoint the rate did not ask for, from the members with the least.
- `BatchNonEmpty` — universal, unwaivable. So a run where *every* account rounds to zero, or where
  the roster is empty, has no legal batch to write. Record the run `skipped` with a reason. A
  planner that returns an empty proposal gets an invariant failure at commit, which is the right
  outcome and a confusing one to debug at 02:00.

## 5. The three settings guilds argue about

From `docs/guides/choosing-a-dkp-system.md` — set them deliberately, and say which you chose out loud
in the guild's own docs:

| Setting | Choices | Note |
|---|---|---|
| Negative balances | `skip` · decay toward zero · decay preserving sign | "Decay toward zero" is debt forgiveness |
| Missed runs after downtime | apply once · **`per_missed_period` (default)** | Applying once silently differs from the guild's stated rules |
| Floor | a value below which decay stops | Without one, a lapsed member decays asymptotically forever |

The negative-balance policy is expressed as **the planner's account filter plus the declared
invariant set**, never as a clamp applied after the arithmetic. `FixedPrice.PlanDecay` skips
`balance <= 0` and skips system accounts entirely — the bank is structurally negative by design,
because it funds every tick, and decaying a negative balance by a positive rate grows the debt.

A floor is a declared `NonNegative(kind, floor)`, which the engine checks against the balance *after*
the batch. Note the asymmetry with reversal: a decay legitimately declares a floor because it is a
scheduled deduction, while a reversal must not, because refusing a correction is worse than a visible
negative balance.

## 6. Catch-up after downtime

`per_missed_period` posts **one batch per missed period**, each carrying its own `cadence_period`
label — and that is exactly what makes catch-up idempotent, with no extra machinery. Re-running it
posts nothing new, because every label is already taken.

- **Oldest first.** `decay_percent` compounds, and each period's batch is planned against the
  balance the previous one left.
- **Each batch's `effective_at` is its own period**, not today. `effective_at` is game truth and may
  be backdated; `recorded_at` is system truth and never is. A member's statement then reads one row
  per period, dated to the period, which is the whole reason both columns exist.
- A catch-up that spans a config change uses **the config in force for that period** — see §7. This
  is the second reason to walk oldest-first: the walk is over the config history as much as over the
  calendar.

## 7. The config a run used is the config it snapshotted

`pool_config_change` versions pool config as events and never overwrites it silently
(`docs/design/01-domain-model.md` §7): `from_*` / `to_*` triples, `changed_at`, `reason`, and a
`migration_batch_id` forward pointer for the case where switching strategies requires its own ledger
batch.

Both the run row and the ledger batch carry `config_snapshot_json`, and both are load-bearing:
changing a pool's config tomorrow must not change what a past batch meant. A run that read today's
config instead of the period's is the quiet version of rewriting history — the ledger rows are
correct arithmetic against rules that were not in force.

## 8. The nightly replay is a correctness dependency

[ADR-0023](../../docs/adr/0023-balance-snapshot-is-load-bearing.md) settled this with a measurement
rather than an opinion: over 527,164 seeded entries the standings page is 1 statement and 13 pages
against the cache, and 10,412 pages (~22 s modelled) against the definitional SUM. **`balance_snapshot`
is load-bearing.** The log is still the only source of truth, but losing the cache is a *rebuild*,
not a graceful degradation — there is no honest fallback that serves the page from the log.

So the replay job is not verification hygiene. It is a dependency of a shipped page.

`dkp verify-ledger` recomputes every balance from the append-only log and compares it against
`balance_snapshot` and the published chain head; `--rebuild` discards and recomputes the cache. It
runs **in CI against `seed.Perf` on every PR** and **nightly in production**. It also recomputes the
audit chain and reports the exact `seq` at which either chain diverges from the last N published
anchors.

Four rules for the job body:

- **It fails loudly and visibly.** ADR-0023 raises this to a standard: a job that silently stops
  running makes a drifted cache undetectable, which is the exact EQdkp Plus failure mode this
  product exists to fix. Drift raises a visible admin alert; a job that did not run is visible at
  `/api/v1/admin/jobs`, in the same SQLite file the officer already backs up.
- **It commits in bounded chunks.** `SetMaxOpenConns(1)` means one long transaction blocks every
  raid-night write. A full replay over a guild-scale ledger is not a single transaction.
- **It takes a per-job lock per pool.** Two concurrent rebuilds of one pool's snapshots are a
  correctness bug, not a performance one.
- **It is idempotent**, like every job. Retries are the normal case, and `EnqueueUnique`
  deduplicates the enqueue without making the body safe.

**The honest limitation, which the docs must state and must not overclaim:** a local hash chain
proves nothing against an actor with filesystem access, who can rewrite rows *and* recompute the
chain. The control is *publication* — anchors written off-box to Discord or email. Say
"tamper-evident **when anchors are published off-box**", never unconditionally.

## 9. Periodic jobs replace cron entirely

EQdkp's "decay silently didn't run because cron wasn't wired" is designed out, and that is the
strongest reason the schedule lives in the same database as the data. Nothing outside
`internal/jobs` imports River (`.claude/rules/jobs-and-events.md` has the argument and the interface).

## 10. The properties these families owe

From `docs/design/04-testing.md`, at 200 checks per PR and 20,000 nightly:

| # | Property | Catches |
|---|---|---|
| P6 | `cap` clamps and is idempotent — applying it twice produces one batch and never moves a balance past the cap | double application after a restart |
| P7 | `start_points` applies **exactly once per account**, and never to an account that already has ledger history | the "everyone got 1000 points again" ticket |
| P9 | Two runs for the same `(pool_id, cadence_period)` produce one batch | "decay ran twice after the box rebooted" |

Plus the universal ones every strategy owes: P5 (reversal is an exact inverse) and P8 (determinism —
the same `(event, config, clock, seed)` produces a byte-identical proposal hash). The framework is
`testing/quick` plus a seeded generator, **not** `pgregory.net/rapid` — a new dependency needs a
human.

## Stop and ask if

- **A guild's decay, cap or start-points rule is ambiguous.** Guessing produces silently wrong
  balances, which is worse than an error, and the ledger is append-only so the guess is permanent.
- A cadence needs a period label the three-form vocabulary cannot express. Adding a fourth form is a
  design decision, not a regex.
- A fourth cadence family appears. `decay_run.kind` is a closed catalogue
  (`internal/decay/kinds`) inside the unique index, so adding one is `make gen` plus a migration plus
  a decision that it belongs in this table at all — ADR-0024, not a new string.
- A run would need to `UPDATE` or `DELETE` a prior run's batch. It cannot: the correction is a
  reversal, and the reversed period's label stays taken.
- A decay would need to read a balance inside `Spendable()`, or a window would be cheaper as a
  predicate in the balance query. That is §1, and it is the failure this whole design is shaped
  around.
