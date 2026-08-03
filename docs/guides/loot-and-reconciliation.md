# Loot and reconciliation

**Status:** items, awards and the reconciliation queue land in Phase 4. This page is the specification
the implementation must satisfy.

Two jobs: get loot recorded against the right person at the right price, and make sure nothing a
parser could not understand is ever silently lost.

## Recording an award

An award is a fact: this item instance went to this person, at this price, from this raid, by this
route. Committing one posts a ledger batch; nothing before that touches anybody's balance.

| Route | Price comes from | Use |
|---|---|---|
| Bid session | The auction's resolution | Anything with a price to discover. See [Auctions](auctions.md). |
| Direct award | The price list, or typed by the officer | Fixed-price guilds, loot council decisions |
| Parsed chat grammar | The log line | Guilds that already announce awards in a fixed format |
| Rot | Nothing — no price | Nobody bid |

Every award records the **award type** — `dkp_auction`, `loot_council`, `random`, `rot`, `guild_bank`,
`staged` — so a member reading loot history can see *why* an item went where it went, not just that it
did.

### The looter is not the winner

The log line names whoever clicked the corpse:

```
[Wed Aug 05 00:14:02 2026] --Bankchar has looted a Cloak of Flames.--
```

On a raid that is usually a puller, a guild-bank character or whoever survived. The award belongs to
the person who won it. Record both: the looter is provenance, the winner is the award.

### Multi-buyer splits

Two people going in on one item is common for a guild-bank purchase or a shared epic component. Split
by basis points — 6000/4000 for a 60/40 split — and the split is charged as one batch, so the parts
cannot get separated by a later reversal.

### Rot

Nobody bid. Your options, and the one that matters:

| Policy | Effect |
|---|---|
| Free roll | `/random` among whoever wants it, recorded with the roll value |
| Re-open at a lower minimum | A second session at a reduced floor |
| Award to the guild bank | **Required under `zero_sum`** — otherwise the pot never closes and the conservation invariant breaks |
| Mark rotted | Recorded and left; no ledger effect |

## The reconciliation queue

Parsers do not guess. Anything they cannot resolve confidently lands in a queue and waits for an
officer. This is the mechanism that replaces "the bot dropped it and nobody noticed".

| Lands in the queue | Because |
|---|---|
| An item name that matches nothing | New drop, a typo, an article the parser stripped wrongly |
| A character name that matches nobody | A pickup raider, an anonymous `/who` entry, a misspelling in a tell |
| An award whose winner could not be resolved | Quarantined, never dropped and never attributed to the wrong person |

Each entry offers the same four resolutions:

| Resolution | Effect |
|---|---|
| `mapped` | Point it at the existing item or character. **Creates an alias** — the system never asks about that spelling again. |
| `created` | Make a new item or person. An explicit choice. |
| `merged` | Fold it into another entry that turned out to be the same thing |
| `ignored` | Not a real item or member — a vendor trash line, a pet name |

Repeat occurrences of the same unresolved name collapse into one queue entry, so a raid that parsed
"Cloak of Flame" nineteen times gives you one row, not nineteen.

**Resolving teaches the resolver.** The alias learned here feeds the name-resolution ladder, so the
queue shrinks over the first month and then stays near empty. A queue that is not shrinking means the
parser has a real bug — there is a pre-filled "report a parser bug" link on every entry that files an
issue with the offending line, redacted.

## When the parser is actually wrong

Do not invent a regex. Several Project 1999 log formats in this project's design are explicitly
**unverified**, and guessing one produces silently wrong attendance, which is worse than an error.

The correct path is the report link: it captures the exact line, redacts tells and character names,
and files it. A golden fixture is added, marked `unverified` until someone confirms it against a real
client, and only then does a parser change ship.

## Fixing a mistaken award

You cannot edit it. That is the design, not a limitation — see [The ledger](../concepts/ledger.md).

```
1. Reverse the award.        A new batch, kind=reversal, entries negated,
                             pointing at the original batch.
2. The original stays.       Visible on the statement, struck through, with a
                             link to the batch that reversed it.
3. Post the correct award.   A new batch at today's sequence.
```

The member's statement then reads, in order: the wrong charge, the refund, the right charge. Every row
names the officer who did it and links to the raid dump behind it. That is a strictly better outcome
than a corrected number that nobody can explain three months later.

Under `zero_sum` a reversal also reverses every credit it produced, together, in one batch — the debit
and the credits are one economic event and they cannot be separated.

Reversing a batch more than a configured age requires step-up authentication and is audit-flagged if
the officer doing it is also the beneficiary.

## Item names, aliases and the resolution ladder

EverQuest item names collide with English, contain backticks and apostrophes, and get typed wrong at
01:00. The resolver walks a ladder, most confident first:

| Step | Example |
|---|---|
| Exact match on the normalised name | `cloak of flames` |
| Known alias | `CoF` → Cloak of Flames |
| Longest-candidate article strip | `a Whitened Treant Fists` → `Whitened Treant Fists` |
| Fuzzy match above the auto-alias threshold | `Cloak of Flame` → offers the alias |
| Below threshold | Queue |

A provisional item created from an unknown name is **upserted**: parsing the same unknown name twice
reuses the provisional row rather than creating a second one.

No game data ships in the product. Item names, stats and icons are Darkpaw intellectual property; the
optional `dkp-p99-seed` importer is a separate repository you run yourself if you want a catalogue
pre-loaded.

## What a member can see

| They can see | Because |
|---|---|
| Every award, to anybody, with price and award type | Public read is the anti-drama mechanism |
| Their own statement, with running balance and provenance per row | The single screen that settles most arguments |
| The raid dump behind any tick | Evidence, not assertion |
| Sealed bid amounts before reveal | **No.** Not even officers, without `bid.reveal_early`, and that read is logged. |

## Next

- [Auctions and bid sessions](auctions.md) — discovering the price
- [The ledger](../concepts/ledger.md) — why reversal beats editing
- [Running a raid night](running-a-raid-night.md) — where awards come from
