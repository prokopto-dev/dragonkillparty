# Choosing a DKP system

**Status:** four are implemented — `fixed_price`, `tick`, `start_points` and `cap`, whose worked
numbers are in [Point strategies](../concepts/strategies.md#the-shipped-strategies). The rest land
through Phase 1; auctions in Phase 6. Numbers for an unshipped rule below are worked from the
specified arithmetic rather than from a running instance. A rule's normative knob list is its
`ConfigSchema`, which also renders the pool-settings form; a generated per-strategy reference page is
Phase 2 ([#212](https://github.com/prokopto-dev/dragonkillparty/issues/212)).

Spend twenty minutes here before your first raid. Changing a point system afterwards is supported and
never rewrites history, but it costs you an argument with every member who was winning under the old
rules.

## A pool answers three questions

A **pool** is one named currency — "Velious Main", "Cross-class Rares". A guild can run several, and
one raid can feed several. Each pool answers:

1. **How are points earned?** Per attendance tick, per kill, per event, or a flat grant.
2. **How are points spent?** A fixed price, an auction, a council vote, a roll, or a position list.
3. **What happens to points over time?** Nothing, decay, a cap, or redistribution.

Every shipped rule below answers exactly one of those three questions, and **a pool holds one rule per
question**, each with its own configuration ([ADR-0026](../adr/0026-three-rules-per-pool.md)). Mixing
one from each is normal — most P99 guilds run *tick* to earn, *sealed auction* to spend, and *window
decay* to keep old points from dominating.

A rule may only go in the slot it answers, so putting `tick` in the spend box is refused when you save
the settings rather than when you try to award an item. A slot you leave empty is a question the pool
declines to answer: a pool with no over-time rule will not run a decay, and says so.

## The catalogue

| Rule | Answers | What it does | Ships in 1.0 |
|---|---|---|---|
| `tick` | earn | Every attendance snapshot credits a fixed value to everyone present | Yes |
| `start_points` | over time | Grants a new member an opening balance, once. It *earns*, but it goes in the over-time slot: the grant is posted on a cadence like decay and a cap, not in response to a raid | Yes |
| `fixed_price` | spend | The item has a published price; the winner pays it | Yes |
| `auction_open` | spend | Ascending English auction; highest bid wins and pays it | Yes |
| `auction_sealed` (first price) | spend | Hidden bids; highest wins and pays its own bid | Yes |
| `auction_sealed` (second price) | spend | Hidden bids; highest wins and pays just above the runner-up | Yes |
| `relative_bid` | spend | Bids are a percentage of the bidder's balance | Yes |
| `roll` | spend | `/random`, or a server-side seeded roll | Yes |
| `loot_council` | spend | Councillors vote; no arithmetic at all | Yes |
| `zero_sum` | over time | What the winner pays is redistributed to the other raiders | Yes |
| `decay_percent` | over time | Balances shrink by a percentage on a cadence | Yes |
| `decay_window` | over time | Earnings older than the window stop counting | Yes |
| `cap` | over time | Balances cannot exceed a ceiling | Yes |
| `attendance_weighted` | ranking | Standing is balance scaled by attendance percentage | Yes |
| `epgp` | earn + spend | Effort and gear points; priority is their ratio | On request |
| `suicide_kings` (bottom) | spend | Winner drops to last place in a position list | On request |
| `suicide_kings` (behind last attendee) | spend | Winner drops behind the last person who attended | On request |
| `suicide_kings` (fixed) | spend | Attendees swap positions among themselves only | On request |

"On request" means the code is designed but is not built unless a pilot guild asks. `epgp` and
`suicide_kings` are WoW-lineage systems with few P99 users, and Suicide Kings reversal is the hardest
single piece of ledger code in the specification.

## Worked examples

Points are stored as integer **centipoints** (points × 100) so nothing ever rounds badly. The examples
show points; the arithmetic happens in centipoints.

### `tick` — the P99 default

A 4-hour raid, 20-minute ticks, 10 points per tick, 20-point bonus for the first tick, 25 points per
named killed.

| Raider | Ticks present | Kills present for | Earned |
|---|---|---|---|
| Tankguy — there from form-up | 12 of 12 | 2 | 12 × 10 + 20 + 2 × 25 = **190** |
| Healbot — arrived after tick 3 | 9 of 12 | 2 | 9 × 10 + 2 × 25 = **140** |
| Druidgal — left at tick 8 | 8 of 12 | 1 | 8 × 10 + 20 + 25 = **125** |

Two officers uploading the same raid dump does **not** double-credit. Ticks deduplicate on
`(raid_id, tick_seq)` plus a hash of the dump's contents, and the ingest endpoint returns the existing
tick instead of creating a second.

Configure: seconds per tick, tick value, kill tick value, first-tick bonus, a grace period for people
zoning in mid-tick, and per-role multipliers.

### `zero_sum` — a closed economy

Tankguy wins a Cloak of Flames for 300 points. Seven raiders were present, the winner excluded from
the split.

```
debit   Tankguy      −300.00
credit  6 others     +300.00 / 6 = +50.00 each
        Σ entries  =  0.00        ← checked as a column comparison, not an aggregate
```

Now the case that matters. Same 300 points, **seven** other raiders:

| Naive: round each credit | Correct: largest remainder |
|---|---|
| 300 ÷ 7 = 42.857… → 42.86 each | 5 raiders get 42.86, 2 get 42.85 |
| 7 × 42.86 = **300.02** | 5 × 42.86 + 2 × 42.85 = **300.00** |
| Two centipoints minted from nothing, every item, forever | Credits sum to exactly the debit |

The residue goes to whoever the deterministic tiebreak on account ID selects, so the same award
planned twice produces byte-identical entries. The `SumZero` invariant rejects any zero-sum batch
whose entries do not sum to zero — a strategy cannot waive it.

Decide three things before you switch this on:

| Question | Options |
|---|---|
| Does the winner share in the split? | Exclude (winner net −300) or include (winner net −300 × 6/7) |
| Who is in the split — attendees at the moment of the award, or everyone on the raid? | `attendees_at_award`, `all_raid_attendees`, `tick_weighted` |
| What if the winner is the only attendee? | Free, or the pot goes to the guild bank account |

**Zero-sum punishes retroactive edits.** Adding one person to a six-month-old raid changes every split
after it. The system never replays history; it posts one compensating correction batch today. Expect
that, and read [The ledger](../concepts/ledger.md) before you promise a member a retroactive fix.

### `decay_percent` — the classic weekly haircut

10% per week on a 500-point balance:

| Run | Posted batch | Balance after |
|---|---|---|
| Week 1 | −50.00 | 450.00 |
| Week 2 | −45.00 | 405.00 |
| Week 3 | −40.50 | 364.50 |
| Week 4 | −36.45 | 328.05 |

Decay is **posted as ledger batches, never computed at read time.** Your balance is always literally
the sum of your statement, so "why did my points change?" is answerable by pointing at a row.

Three settings guilds argue about, so set them deliberately:

| Setting | Choices | Note |
|---|---|---|
| Negative balances | skip · decay toward zero · decay preserving sign | "Decay toward zero" is debt forgiveness. Say which one you chose, out loud. |
| Missed runs after downtime | apply once · apply once per missed period | Apply per period, or the maths quietly differs from your stated rules. |
| Floor | a value below which decay stops | Without one, a lapsed member decays asymptotically forever. |

The run is idempotent on `(pool_id, kind, cadence_period)`, so a job that fires twice decays once —
and a cap running the same week is a separate run rather than a silent no-op.

### `decay_window` — earnings expire

No compounding haircut; earnings simply stop counting once they are older than the window.

90-day hard cutoff, evaluated on 2026-08-03:

| Earned | Amount | Counts? |
|---|---|---|
| 2026-07-20 | 190 | Yes |
| 2026-06-01 | 240 | Yes |
| 2026-04-02 | 310 | **No** — 123 days old |

A linear taper is available instead of a hard cutoff, which avoids the cliff where a member loses 300
points overnight.

This is the friendlier of the two decays for a P99 guild, because content is finite: a member who
raided hard for six months and took a break is not punished twice.

### `fixed_price` — published price list

The item has a price; the winner pays it. No bidding, no drama, no auction to run at 01:00.

| Tier | Price |
|---|---|
| NToV primary weapons, Vulak drops | 300 |
| NToV visible armour | 150 |
| Kael, ToV east, Sky | 75 |
| Everything else | 25 |

The price **charged** is snapshotted onto the award. Editing the price list next month does not
retroactively change what anyone paid. Set `unlisted_policy` to decide what happens to an item that is
not in the table: block the award, prompt the officer, or fall back to a default price.

### `auction_open` — ascending, in guild chat

Minimum bid 100, increment 25. Every accepted bid must exceed the current leader by at least the
increment, and cannot exceed the bidder's spendable balance minus any active holds.

```
Tankguy    100      accepted, leader
Healbot    125      accepted, leader
Tankguy    150      accepted, leader
Healbot    160      rejected — 409 invalid_increment (needs 175)
Healbot    175      accepted at 00:01:52, inside the 20s anti-snipe window
                    → closes_at pushed to 00:02:20, extensions_used 1
Tankguy    200      accepted, leader
                    closes; Tankguy wins and pays 200
```

Anti-snipe extension is bounded by `max_extensions`, so an auction cannot be extended forever by two
stubborn bidders.

### `auction_sealed` — hidden bids

Bids are invisible to everyone, including officers, until the session enters `closing`. An officer
who reads a sealed amount early needs the `bid.reveal_early` permission and the read is itself
audit-logged.

| Bidder | Bid |
|---|---|
| Tankguy | 350 |
| Sneakyguy | 280 |
| Druidgal | 150 |

| Pay rule | Winner pays |
|---|---|
| First price | 350 — their own bid |
| **Second price** (delta 5) | **285** — the runner-up plus one increment |

Second price is the best default for a bot-driven guild. Under first price the rational move is to bid
your entire bank, so everyone does, and the auction becomes a bank-size contest. Under second price
bidding your true valuation is optimal, so bids stop being strategic. A lone bidder in a second-price
auction pays the minimum bid.

### `relative_bid` — percentage of your bank

Bid a percentage rather than an amount. A hoarder pays more absolute points for the same priority.

| Bidder | Balance at session open | Bid | Cost |
|---|---|---|---|
| Tankguy | 900 | 40% | 360 |
| Healbot | 500 | 55% | 275 |

Healbot wins on percentage while paying fewer points. Percentages resolve against the balance
**snapshot frozen at session open** (`seq_at_open`). Resolving against live balances would let a decay
run rewrite everybody's bid mid-auction.

### `cap` — a ceiling on hoarding

Prevents the member who has raided for two years from outbidding everyone forever.

| Setting | Effect |
|---|---|
| Hard cap | A balance may not exceed the ceiling. Excess is posted as a `cap` batch and is visible on the statement. |
| Soft cap plus an over-cap earn ratio | Earnings above the soft cap are credited at a reduced ratio instead of being trimmed. |

The exact knob names are defined by the strategy's `ConfigSchema`; treat the two rows above as the
shapes, not as field names.

`cap` goes in the **over-time** slot, where a pool asks it for the cap run. The soft cap's earn-time
reduction is a second answer to the earn question, which a pool has already given to its earn rule —
so a pool wanting both wants two earn rules, which is
[#215](https://github.com/prokopto-dev/dragonkillparty/issues/215) rather than something to configure
today.

Caps are the single most common EQdkp Plus configuration after decay, and they are the one thing a
migrating guild most often finds missing. If your old site had `cap_current` or `hardcap_current` set,
you want this.

### `start_points` — an opening balance for recruits

A new member joins a mature guild where everyone has 800 points. Without a starting grant they cannot
bid on anything for two months, which is a recruitment problem, not a fairness one.

The grant posts once per account per pool, as a `start_points` batch, idempotent on
`(pool_id, account_id)`. It appears on the statement like everything else — a member can see exactly
where their opening 200 points came from.

### `attendance_weighted` — turning up beats hoarding

This is a **ranking score, not a balance.** Spending still deducts raw points.

| Member | Balance | 60-day attendance | Priority |
|---|---|---|---|
| Tankguy | 500 | 90% | 450 |
| Healbot | 620 | 60% | 372 |

Tankguy outranks Healbot despite having fewer points. Conflating the two — letting attendance scale
the spendable balance rather than the ranking — produces nonsense: your bank shrinks when you miss a
raid, so your past purchases were retroactively mispriced.

Common alternative uses of the same number: as a **gate** ("40% on the 30-day window to bid on NToV
loot") and as a **tie-break**. Both are configured on the pool rather than baked into the strategy.

Read [Attendance and windows](attendance-and-windows.md) for exactly what the percentage counts.

### `roll` and `loot_council`

`roll` records the value, the range, the timestamp and the **provenance** — a server-generated roll
with a persisted seed, or a `/random` line parsed out of an officer's log. Rolls are immutable; a
re-roll on a tie is a new round, not an edit. `+1 / −1` systems are a one-point-granularity point
system in disguise; configure them as `roll` with a counter, not as a special case.

`loot_council` has no arithmetic. It records nominations, each councillor's vote, the decision, the
rationale and the timestamp, and it flags a conflict of interest when a councillor is a candidate for
the item under vote. Councils without a published priority score reliably decay into loot-council
fatigue, so run one next to `attendance_weighted` and show the score.

### `epgp` and `suicide_kings`

Not built unless requested; here is what you would get.

**EPGP.** Effort points rise with attendance, gear points rise with loot, priority is `EP / max(GP,
base_GP)`. Both decay by the same fraction, so decay does not reorder standings — that is the whole
point of the design. There is no spend, so there is no double-spend hazard, which makes EPGP the
safest system to run with several auctions open at once. The catch on P99: the WoW gear-point formula
keys off item level, and **EverQuest has no item level**, so your GP values must be a curated tier
table you maintain by hand.

**Suicide Kings.** Members hold positions in a total order; the winner "suicides" to the bottom, to
behind the last raid attendee, or swaps within the attendees only. The state being tracked is an
entire ordering, which is why every suicide is stored as an invertible delta rather than a rewritten
list.

## Which suits which guild

| If your guild… | Start with |
|---|---|
| Is a normal 30–70 raider P99 guild and has no strong opinion | `tick` + `auction_sealed` (second price) + `decay_window` |
| Argues constantly about loot prices | `fixed_price` — a published table converts every argument into a policy discussion held once |
| Has veterans with enormous banks and cannot recruit | `cap` + `start_points`, or switch to `decay_percent` |
| Wants a closed economy where nothing inflates | `zero_sum` — but read the retroactive-edit warning first |
| Is small, close-knit, and trusts its officers | `loot_council` next to `attendance_weighted`, so the score is visible even when the council overrides it |
| Has a mature officer corps and a bid bot | `auction_sealed` second price — it removes the incentive to game the auction |
| Raids in two distinct tiers (main raid + alt/rare runs) | Two pools with different rules; a raid can feed both |
| Runs open raids with pickups from other guilds | `tick` with a standby percentage, and set your alt policy before the first raid |
| Is migrating from EQdkp Plus | Whatever you already run. Import first, verify the numbers, change nothing for a month. |

## Rules that are not negotiable

Configuration cannot switch these off. They are enforced by database triggers, invariant checks and
tests, not by convention:

- Points are integers. There is no float anywhere in the arithmetic.
- Zero-sum credits sum to exactly the debit.
- Nothing in the ledger is ever updated or deleted. A correction is a reversal batch.
- Decay is posted, not computed at read time.
- A strategy is a pure function: it proposes a batch and the ledger validates it. A misconfigured
  strategy can produce a wrong number; it cannot corrupt the ledger.

See [Invariants](../concepts/invariants.md) for the full list and the mechanism enforcing each one.

## Next

- [The ledger](../concepts/ledger.md) — what a member sees when they question a number
- [Point strategies](../concepts/strategies.md) — what a strategy may and may not do
- [Attendance and windows](attendance-and-windows.md) — the numerator and the denominator
- [Auctions and bid sessions](auctions.md) — running the bid
