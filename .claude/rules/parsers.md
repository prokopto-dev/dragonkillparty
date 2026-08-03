---
paths: ["internal/parse/**", "test/golden/**"]
description: P99 log adapters — pure functions, stdlib only, one file plus one golden dir per format, defensive splitting, and how to handle a format that is not verified.
---

# P99 log parsers

The shape of every adapter, without exception:

```go
// internal/parse/p99_raid_dump.go
func ParseRaidDump(src []byte) (RaidDump, error)
```

Pure `[]byte` → struct. **No database access, no network, no clock, no filesystem, stdlib only.**
No `internal/store` import, no `time.Now`, no config. Everything the parser needs arrives in the
byte slice or in an explicit argument. That is what makes the golden-file suite run in under a
second and lets an agent iterate on a regex without a server.

Persistence, `/tell`-line redaction, artifact hashing and reconciliation all happen **above** this
package. A parser never decides what to store and never drops a line — unrecognised lines go into
the result as `Unparsed`, with their line numbers.

## One file, one golden directory, per format

```
internal/parse/p99_who.go
test/golden/p99_who/
    anonymous.in        anonymous.json
    afk_and_lfg.in      afk_and_lfg.json
    guild_tag_only.in   guild_tag_only.json
```

The golden is the **whole** parsed struct as canonical JSON, not three chosen fields. `test/golden/`
is CODEOWNERS-protected; `-update` is refused when `CI=true`; a test asserts the fixture-file count
is non-decreasing. **Never rewrite a golden to go green** — a failing golden is the parser telling
you the format changed or your regex is wrong.

## Formats, and what is actually verified

Every line in a log file is `[<24-char asctime>] <message>`; the prefix is **27 bytes** including
the trailing space. The timestamp has **no timezone and no sub-second precision**, multiple lines
share a second, the file is per character, and the clock is the officer's PC clock. Always carry
`(raw_line, parsed_local_ts, uploader_tz, received_at)` upward — the parser returns the local
wall-clock value and does not guess a zone.

| Adapter | Status | Notes |
|---|---|---|
| `p99_raid_dump` | **verified** | Filename `RaidRoster-YYYYMMDD-HHMMSS.txt`; **TAB**-delimited; columns `Group`, `CharacterName`, `Level`, `ClassTitle`. `Group == 0` means benched/ungrouped. **The file contains no timestamp** — the only time signal is the filename. No race, no guild, no zone |
| loot lines | **verified** | `--Raneen has looted a Whitened Treant Fists.--` and `--You have looted a Cloak of Flames.--`. Money: `You receive … from the corpse.` |
| chat grammars | **verified** | `tells the guild`, `tells the group`, `tells you`, `You told …`, `says`, `You say`, `shouts`, `auctions`, `says out of character`, each with the optional `, in <lang>` infix |
| `p99_who` (single line) | **verified** | `[52 Heretic] Zibaxia` and `[ANONYMOUS] Arren <Firestormers>`. Fields left to right: optional `AFK `/`LFG` marker, `[<level> <ClassTitle>]` or `[ANONYMOUS]`/`[ROLEPLAYING]`, name, optional `(Race)`, optional `<Guild Name>` |
| `p99_who` (block header/footer) | **UNVERIFIED** | The `Players on EverQuest:` / dashed-rule / `There are N players in <Zone>.` wrapper, and P99's in-zone line cap |
| `p99_raid_dump` trailing columns | **UNVERIFIED** | Later EQ clients append raid-leader / group-leader / main-assist / loot-rank columns. Whether Titanium emits them is unknown |
| `p99_guild_dump` | **UNVERIFIED** | Whether P99 supports `/outputfile guild` at all. Sniff the column count (7-column classic vs 10-column live) rather than assuming |
| `/random` | **UNVERIFIED wording** | Two lines, both starting `**`. Pair them: a `**A Magic Die is rolled by <name>` followed by the next `**It could have been…`. Forty people rolling at once interleave, so pair forward, never by adjacency |
| `tells the raid` | **UNVERIFIED** | Classic EQ has no raid channel line type distinct from `/rsay`. Most P99 bidding happens in guild chat, a custom channel (`/join btdkp`), or Discord |
| FTE `engages`, `has been slain by` | **verified** | `<Name> engages <Mob>!` is the P99 first-to-engage shout |
| stacked loot (`has looted 20 Rune of Impetus`) | **UNVERIFIED** | Breaks the fixed `a/an` pattern |
| log filename server token | partly | `project1999` for Blue is known; Green/Red tokens are **UNVERIFIED**. Treat the token as opaque and capture it |

**Class titles, not class names.** `/who` and the raid dump print the level-appropriate title, which
changes at 51/55/60. Raiders are 51–60, so a parser without a title→class lookup drops most of a
roster. `[52 Heretic]` is a Necromancer. The lookup is our own literal table, never transcribed from
EQdkp.

**Names.** P99 character names are alphabetic; accept `[A-Za-z]{3,15}`. Item and mob names carry
backticks and apostrophes (`` Vulak`Aerr ``, `Trakanon's Tooth`, `` J`Boots ``) — the normalisation
key strips `'`, `` ` `` and `-`, and the raw string is always kept for display.

## Parse defensively

- **Split, then take what you need, then keep the rest.** For the raid dump: split on `\t`, use
  fields 0–3, put everything beyond into `raw_extra`. Never assume a column count; never fail a line
  because it has more fields than you expected.
- Guard every index. A truncated log (the officer closed the client mid-write) must produce a
  partial result plus a diagnostic, not a panic and not a silent zero.
- Return a per-line diagnostic with the line number and the raw text, so the reconciliation queue and
  the "report a parser bug" link can quote it.
- Ambiguity is resolved by an explicit, tested rule, not by map order. `event_type.slain_pattern_norm`
  is non-unique, so a mob name appearing both standalone and inside a rotation matches two events:
  the tiebreak is **longest pattern, then explicit `priority`**, and it has a golden case.

## Unverified formats — the rule that matters most

> **Do not invent a regex and ship it.** A guessed format produces silently wrong attendance, which
> is far worse than an error, because nobody audits a number that looks plausible.

When you meet a format not verified in `test/golden/`:

1. Add a golden directory with the sample you have and a `UNVERIFIED` marker file naming what is
   unknown and what evidence would settle it.
2. Make the adapter **fail loudly** on the unverified shape — return `artifact_unparseable` with
   `meta.format_guess` and `meta.first_bad_line` — or route the lines to the reconciliation queue.
   Never guess and never partially credit.
3. Open an issue with the format name and the sample.
4. Say so in `docs/reference/log-formats.md`, which lists exactly what is parsed and what is not.

The cheap fix for the whole table above is one post in the P99 Discord asking for twenty real
`RaidRoster-*.txt` files, `/who` pastes and loot lines. That is a week-one task.

## Fuzz targets

Every adapter gets one:

```go
func FuzzParseWho(f *testing.F) {
    // seed from every .in file in test/golden/p99_who/
    f.Fuzz(func(t *testing.T, src []byte) { _, _ = ParseWho(src) })
}
```

The property is: **never panic, never hang, never allocate unbounded** on arbitrary input. Officers
upload whatever is in their EQ directory, including binary files and half-written logs. Seed the
corpus from the goldens so the fuzzer starts from real shapes.

## Stop and ask if

- You have not seen the format in `test/golden/`.
- A line type would change attendance and you are inferring its shape from a similar one.
- You need database state to parse. You do not — resolution happens above this package.
