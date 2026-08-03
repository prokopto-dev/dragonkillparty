# The ledger

**Status:** the ledger lands in Phase 1. Written for an officer, not an engineer. The database-level
detail is in [01 Domain model](../design/01-domain-model.md).

Every DKP tool has one job that matters more than the others: when a member says "that number is
wrong", produce an answer they believe. This page is how that answer is produced, and why it is the
whole product.

## A balance is not stored

Your balance is not a number in a column that the software adds to and subtracts from. It is the sum
of every entry ever written against your account:

```
balance = SUM(amount) over your entries, up to a given point in the log
```

That is not a metaphor. It is literally the query. The consequence is the point: **there is no number
anyone can edit to make your balance different.** To change a balance you must add a row, and the row
is visible to everybody, forever, with your name on it.

EQdkp Plus stores a total and recomputes it from mutable rows. When that total drifts — and it does —
"recalculate" is a prayer, because the rows it recomputes from can themselves have been edited. Here
the cache exists too, but it is derived from an immutable log, so rebuilding it is total and cheap and
drift is detectable. A nightly job rebuilds every balance from zero and raises an alarm if it
disagrees.

## Batches and entries

Two levels, and the distinction matters when you go looking for something.

| | Is | Example |
|---|---|---|
| **Batch** | One economic event | "Tankguy won a Cloak of Flames for 300" |
| **Entry** | One account's share of it | Tankguy −300; and under zero-sum, six credits of +50 |

A batch is committed whole or not at all. That is why a zero-sum award cannot end up with the debit
posted and the credits missing — they are one batch, and reversing it reverses all of them together.

Every batch records:

| Field | Why it is there |
|---|---|
| Kind | `attendance`, `award`, `adjustment`, `decay`, `cap`, `start_points`, `reversal`, `correction`, `import`, … |
| The strategy and its version | So a five-year-old batch is still interpretable under the rules that produced it |
| A snapshot of the pool's configuration at the time | So "the tick was worth 10 then, 15 now" is a fact, not an argument |
| Who did it | The officer, or the token's public prefix if a bot did it |
| Where it came from | The tick, the bid session, the item award, the import |
| Why | A free-text reason, mandatory on adjustments |
| A hash of itself and of the batch before it | So tampering with the database file directly is detectable |

## `seq`: the number to quote in a dispute

Each pool has a monotonic counter. Every batch takes the next value. **A balance is defined as of a
`seq`, never as of a timestamp** — timestamps tie, sequence numbers do not, and a dispute needs one
number.

When someone says "I had 430 when I bid", the answer is not "at 9:47pm" — it is "as of seq 88213 you
had 43000 centipoints", and anybody can recompute it.

This `seq` is per pool. There is a second, global sequence called `event_seq` used for realtime
delivery and replay. They are different numbers with different scopes and they are never given the
same name, because bots mixing them up is the top predictable integration bug.

## Two clocks: `effective_at` and `recorded_at`

Every batch carries both, and conflating them is how history gets falsified.

| | Means | Can be backdated |
|---|---|---|
| `effective_at` | **Game truth.** When the thing happened in the guild's world. | Yes |
| `recorded_at` | **System truth.** When the software learned about it. | Never |

An officer entering last Tuesday's forgotten raid on Thursday produces a batch with
`effective_at = Tuesday` and `recorded_at = Thursday`. Both are true. Neither is a lie.

This pays off in exactly one query, and it is a query officers need:

> "The site said I had 400 last Tuesday, and now it says 340. What happened?"

Ask what the system *believed* on Tuesday — everything with `recorded_at` before Tuesday — and compare
it against what it believes now. The difference is a list of specific batches recorded since, each with
an actor and a reason. That is a complete answer, and no system that stores a single mutable total can
produce it.

## Corrections are reversals, not edits

This is the rule people push back on, so here is the reasoning rather than the rule.

When a mistake is found, the appealing fix is to change the wrong number to the right one. It is one
operation, the balance ends up correct, and nobody has to read a confusing statement.

It also destroys the only thing making the number trustworthy. Once history can be edited:

- A member cannot verify their own balance, because the rows they are checking may have changed since
  they last looked.
- An officer cannot prove they did not change something.
- "The site is wrong" and "the site was changed" become indistinguishable.
- Every future argument is unresolvable, because the evidence is mutable.

So instead:

```
1. The wrong batch stays.        Visible, struck through, linked to what reversed it.
2. A reversal batch is posted.   Same entries, negated. Points at the original.
3. The correct batch is posted.  At today's seq.
```

The statement reads: the wrong charge, the refund, the right charge. Three rows instead of one silent
change. Slightly more to read, and *checkable by the person who is annoyed*, which is the entire point.

The database enforces this. `ledger_batch` and `ledger_entry` carry `BEFORE UPDATE OR DELETE` triggers
that raise an error, so an `UPDATE` fails whether it comes from the application, from a migration, or
from an officer poking at the file with `sqlite3`. An integration test asserts the trigger actually
fires — the guardrail is itself regression-tested, so it cannot be quietly removed.

A batch can be reversed at most once; a unique index enforces it. Reversing a reversal is just another
reversal.

## Retroactive edits under zero-sum

Under zero-sum, adding one person to a six-month-old raid changes the split of every award after it.
Two possible responses:

| Approach | What happens | Default |
|---|---|---|
| **Compensate** | One correcting batch today carrying the net difference | Yes |
| **Replay** | Recompute forward from the edit point and post the aggregate deltas | Only as an explicit officer job with a mandatory dry-run diff |

Neither rewrites history. Even the replay path emits a single net-delta batch at today's sequence.

The honest consequence: a retroactive fix appears on the statement as one correction dated today, not
as a rewritten six months. Members find that odd for about a week and then find it reassuring, because
it means nobody can quietly rewrite six months.

## Decay is posted, not calculated

Decay could be a formula applied when a balance is read. It is not. Every decay run writes explicit
batches with the amount each account lost.

The reason is one sentence: **your balance is always literally the sum of your statement.** If decay
were computed at read time, the statement would not add up to the balance and "why did my points
change?" would have no row to point at.

Decay runs are idempotent on `(pool, period)`, so a scheduler that fires twice decays once.

## Reading your statement

| Column | Is |
|---|---|
| Date | The `effective_at` — when it happened in game |
| Kind | Attendance, award, adjustment, decay, reversal … |
| Description | The raid, the item, the officer's reason |
| Delta | What this row changed your balance by |
| Balance | The running total after this row |
| Actor | The officer, or the bot's token prefix |
| Source | A link to the tick, the bid session, or the uploaded raid dump |

Any member can read any member's statement. Public read with restricted write is a deliberate design
choice: secrecy about balances generates more drama than it prevents.

## Money is integers

Points are stored as **centipoints** — points × 100 — in 64-bit integers. There are no floating-point
numbers anywhere in the arithmetic, not in the code, not in the database, not on the wire.

This is not fastidiousness. EQdkp Plus stores points as `float(11,2)`, which is why guilds see balances
like `339.99999`. Binary floating point cannot represent 0.1 exactly, errors accumulate over thousands
of transactions, and the resulting drift is invisible until a member notices their balance ends in a
row of nines.

The related rule: zero-sum splits use largest-remainder allocation so the credits sum to *exactly* the
debit. Rounding each credit on its own mints or destroys points on every single award. See the worked
example in [Choosing a DKP system](../guides/choosing-a-dkp-system.md#zero_sum--a-closed-economy).

## What this buys you

| Question a member asks | The answer |
|---|---|
| "Why is my balance 340?" | Here are the 47 rows that sum to 340 |
| "I had 430 on Tuesday" | As of seq 88213 you had 43000 centipoints; here are the six batches recorded since |
| "Who took 50 points off me?" | This officer, at this time, for this reason, on this raid |
| "Was I at that raid?" | Here is the raid dump the officer uploaded, with your name in it |
| "Did someone change this after the fact?" | The hash chain says no, and the trigger says they could not have |

## Next

- [Invariants](invariants.md) — the rules and the mechanism enforcing each one
- [Point strategies](strategies.md) — what a strategy may propose and what the ledger refuses
- [Attendance and windows](../guides/attendance-and-windows.md) — the other number members check
- [ADR-0002](../adr/0002-append-only-ledger.md) — the decision, with its downsides
