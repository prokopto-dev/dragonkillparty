---
paths: ["internal/seed/**", "cmd/dkp/seed.go"]
description: The three seed profiles, why every row goes through the real commit path, the row-count floors and how to change one, and what a profile may and may not invent.
---

# Seed profiles

Three datasets, one generator, and one rule that matters more than the rest.

| Profile | Is | Scale | Lands |
|---|---|---|---|
| `Perf` | a realistic-guild-scale ledger, for measurement | 280 accounts · 3,400 raids · 527,164 entries | **Phase 1** (ledger only); roster in P2, raids in P4 |
| `Small` | a working guild a developer can read end to end | tens of rows | Phase 2, with the roster |
| `Demo` | the `demo.dragonkillparty.org` guild, reset nightly | a plausible mid-size guild | Phase 3 |

Only `Perf` exists. `dkp seed --profile small` exits non-zero naming the phase that implements it —
never falls back to `Perf`, because a developer who asked for a demo guild and silently got half a
million entries would spend an afternoon working out why.

## Every row goes through `ledger.Service.Commit`

This is the rule. A generator that bulk-inserted would be thirty times faster and worthless:

- it would skip the invariant engine, so the dataset could contain batches the product would refuse;
- it would skip the hash chain and the per-pool `seq` allocator;
- and it would skip the synchronous `balance_snapshot` upsert — which is fatal, because the whole
  point of the Perf profile is to judge that cache. A snapshot-versus-fold property checked against
  a snapshot the test wrote by hand is circular. Checked against one the commit path wrote while it
  was busy committing 20,461 transactions, it is a finding.

So `internal/seed` holds no raw SQL and no `*sql.DB`. Accounts go in through the generated
`InsertAccount` inside `store.Tx`; everything else is a `strategy.BatchProposal` handed to
`Commit`. It consumes `internal/strategy` as a data shape and adds nothing to it — law 3 is
untouched.

The cost is the point, not a regret: the full profile is ~2 minutes and ~250 MB.

## Row-count floors, and how to change one

`Profile.Counts()` walks the plan **without a database** and returns the exact batch and entry
counts `Generate` will write. Two tests hold it:

- `TestSeedProfile_Perf_MeetsItsRowCountFloors` — at or above `seed.PerfEntryFloor` (520,000, item
  V3's guild), and equal to the exact deterministic figure.
- `TestSeedProfile_Counts_AreNonDecreasingInRaids` — a bigger profile is never a smaller dataset.

And `TestPerf_StandingsOverASeededLedger_MeetsItsBudgets` asserts the database ends up holding
exactly what `Counts()` predicted. That agreement is evidence rather than a tautology: one number is
computed in Go against no database, the other is counted out of one that twenty thousand
transactions wrote.

**Being wrong downward is what costs something.** Every latency and statement budget in the roadmap
is stated at V3's scale, so a profile that quietly shrank would turn the whole perf suite into a
measurement of a smaller guild while still reporting green. Being wrong upward costs seconds.

To change the composition: change the profile, run `make test-perf`, put the new exact figure in the
floor test, and **re-measure V5** — `docs/development/verify-before-phase-0.md` records the
standings verdict at a stated entry count, and a verdict taken at a different one is a different
verdict.

## One knob, one code path

`DKP_PERF_RAIDS` (the test suite) and `--raids` (the CLI) size a run through `Profile.Scaled`. There
is no separate "fast version": the small run on every PR and the 3,400-raid run under `make
test-perf` execute the same walk, for the reason `DKP_PROPERTY_CHECKS` does — a cheap lane that
compiled differently from the expensive one proves nothing about it.

**The roster does not scale.** A guild is 280 people whether you look at one month of its ledger or
five years of it, and the V5 budget is stated per 280 members.

`make test` runs `-race`, which instruments `modernc.org/sqlite` — the SQLite engine compiled to Go
— so a seeded entry costs roughly thirty times what it costs uninstrumented. That is why the
per-PR default is eight raids and why `make test-perf` does not use `-race`.

## What a profile may invent, and what it may not

**The shape is real.** Real batch kinds in a realistic mix, attendance rosters that differ raid to
raid, zero-sum splits through `ledger.Allocate` with the largest-remainder tiebreak actually
applied, entries carrying their raid/tick/character/item provenance. Those columns are four ULIDs
per row and most of a `ledger_entry`'s width — a profile that left them null would produce a table
and a set of indexes materially smaller than production's, and the page counts the V5 verdict rests
on are counted off exactly that.

**The economics are not.** Tick values, item prices and decay debits come from a seeded PRNG, not
from a simulation of a guild's rules, and no attempt is made to model what a raider's balance
*should* be. That is the right trade for a performance profile, where row counts, index shapes and
value distributions drive every number and DKP policy drives none of them. `Demo` is where
plausible economics matter, and it is a different job.

Do NOT make the plan depend on the database — no "decay 10% of the current balance". `Counts()`
must stay pure, and a plan that reads balances cannot be predicted before it runs.

## Determinism, and its one limit

The same `Profile` plans the same dataset and seeds the same **balances**, every run. Every choice
comes from `ledger.NewRng(p.Seed)`.

What is **not** reproducible is row ids and the hash chain: `core.Generator` draws ULID entropy from
`crypto/rand`, so two runs of the same profile agree on every balance and disagree on every primary
key. **Assert on balances.**

Ids the generator mints itself — accounts, characters, raids, ticks, items — are deterministic and
order-preserving, via `seed.DeterministicID`. Both properties are load-bearing: `ledger.Allocate`
breaks a tie on `account_id` ASC, so an encoder whose order did not follow the roster's would make a
split depend on the encoding. Tags must be Crockford base32, which excludes **I, L, O and U** —
`"RAID"` and `"ITEM"` are illegal tags, which is why the raid tag is `RAD` and the item tag is
`GEAR`.

## Seeding is not undoable

`Generate` refuses a pool that already has batches (`seed.ErrPoolNotEmpty`). The ledger is
append-only, so a top-up cannot be taken back out: the resulting database matches no profile's row
counts and every measurement against it is unattributable. The recovery is to delete the file, which
is a thing you can do to a seeded database and can never do to a guild's.

That refusal is also the whole safety story for shipping `dkp seed` in the operator's binary. Keep
it.

## Stop and ask if

- A profile needs a row the ledger cannot write yet. Add the table first; a seed that inserts around
  a missing service is the bulk-insert failure in a different coat.
- The Perf entry count would drop below `PerfEntryFloor`.
- V3 comes back saying real guilds are five times this. Then `Perf` is regenerated at the real scale
  and **every budget measured against it is re-measured** — starting with V5.
