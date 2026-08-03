---
name: add-parser
description: Add or fix a Project 1999 EverQuest log or dump parser in internal/parse. Use for RaidRoster dumps, /who output, guild dumps, loot lines, /random rolls, chat award grammars, FTE engage lines and slain lines — and whenever a parser-bug issue arrives with a real log line.
argument-hint: "[format_id]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(make test-unit), Bash(make test), Bash(make check)
---

# Add a log parser

Parsers are a fully parallel lane: **pure `[]byte → struct`, stdlib only, no database, one file plus
one golden directory per format.** They can be written before anything else exists.

The failure this skill prevents is silently wrong attendance. A guessed regex that matches 95% of
lines produces attendance percentages that are quietly wrong for months — worse than an error,
because nobody investigates a number that looks plausible.

---

## Steps

### 1. Establish whether the format is verified

| State | What you do |
|---|---|
| You have real sample lines (a golden fixture, a donated dump, a parser-bug issue) | Proceed. |
| You have a format description but no real sample | **Add a golden fixture marked `unverified`, open an issue, and stop.** Do not invent a regex and ship it. |

Formats explicitly unverified in the design: the `/who` header/footer strings and the in-zone `/who`
line cap, whether P99/Titanium supports `/outputfile guild` and `/outputfile raid`, the trailing
columns of a Titanium `RaidRoster` file, the exact `/random` two-line wording, the `tells the raid`
log form, and the Green/Red log-filename server tokens. **Several formats in the design are
assumptions, not findings.** Treat them that way.

### 2. Create the adapter

`internal/parse/<format_id>.go`, e.g. `p99_raid_dump.go`. The whole package is stdlib-only — a lint
rule enforces it, and it is what makes the parsers fuzzable and instantly testable.

```go
// Parse is pure: no clock, no database, no network, no logging side effects.
func Parse(src []byte) (Result, []Diagnostic, error)
```

Return **diagnostics alongside a partial result**. A parser that returns an error for one bad line
throws away the ninety-nine good ones, and the officer loses a raid night's attendance.

### 3. Parse defensively

| Rule | Why |
|---|---|
| Split on `\t`, take the fields you know, keep the remainder in `raw_extra` | Later EQ clients append trailing columns. Assuming a column count is how a parser breaks on one client version. |
| Never assume a fixed column count | Same. |
| Capture the server token as **opaque** | `project1999`, `P1999Green`, `P1999Red` — the exact tokens are unverified. |
| Normalise names in Go via the shared `name_norm` function | NFKC + casefold + strip `'` `` ` `` `-`. Never a SQL collation. |
| Handle anonymous/roleplaying players losing level and class | `/who` drops those fields; the record is still valid. |
| Redact `/tell` lines at ingest | Artifacts are retained 180 days by default (canonical §11); private tells are not ours to keep. |

Where two patterns could match one line, the tiebreak is **longest pattern, then explicit
`priority`** — `event_type.slain_pattern_norm` is non-unique, so the tiebreak is specified and must
be tested, not left to map iteration order.

### 4. Create the golden directory

`test/golden/parse/<format_id>/`, one input file plus one expected-output file per case:

```
test/golden/parse/p99_raid_dump/
├── 01_titanium_60_person.input.txt
├── 01_titanium_60_person.golden.json
├── 02_group_zero_bench.input.txt
├── 02_group_zero_bench.golden.json
├── 03_trailing_columns.input.txt        # raw_extra populated
├── 03_trailing_columns.golden.json
└── 04_truncated_who.unverified.input.txt   # marked; see step 5
```

Goldens are canonical JSON, sorted keys, one field per line, so a regeneration produces a
human-readable diff. `test/golden/**` is CODEOWNERS-protected, `-update` is refused when `CI=true`,
and a test asserts the fixture count is **non-decreasing**.

### 5. Handle unverified formats explicitly

An unverified fixture:

- has `.unverified.` in its filename,
- carries a `PROVENANCE` note saying where the format description came from and what is unconfirmed,
- has an open issue linked from the note,
- and its test asserts the **current** behaviour while stating that the expectation is provisional.

Never delete an unverified fixture to make a suite green. Promote it to verified when a real sample
arrives, in a commit that says so.

### 6. Quarantine unknown names — never auto-create

Unknown character names go to the **reconciliation queue**. They do not auto-create a person
(canonical §12). The award is quarantined, never dropped, and never silently attributed.

`on_unresolved` on a raid submission selects `fail | quarantine | create`, defaulting to
`quarantine`. `create` is an explicit officer choice.

The provisional-item path is an **upsert**: a second parse of the same unknown item name reuses the
existing provisional row rather than colliding with the partial unique index on `name_norm`. See
`db/RECIPES.md` → "Provisional item resolve".

### 7. Add a fuzz target

```go
func FuzzParseP99RaidDump(f *testing.F) { /* seed from the golden inputs */ }
```

Seed corpus from the golden inputs. Fuzz runs seed-corpus-only on PRs and 10 minutes nightly;
crashers persist into `testdata/fuzz/` as CODEOWNERS-protected goldens. A parser that panics on a
truncated file takes down an ingest request.

### 8. Wire it into ingest

Register the adapter in the format registry so `POST /artifacts` and `POST /raid-submissions` can
sniff and dispatch it. The parser itself stays pure — the wiring lives outside it.

Ingest is preview → diff → commit with `content_sha256` tick dedupe, so two officers uploading
overlapping dumps produce one set of ticks.

### 9. Docs and the bug-report loop

- Add the format to `docs/reference/log-formats.md` — **what is parsed and what is not**, explicitly.
- Confirm the in-app "report this line as a parser bug" button covers it. Those issues are free
  golden-file test cases and must never be closed unread; the triage step is literally "add the line
  to `test/golden/`, watch it fail, fix it."

### 10. `make check`

---

## Stop and ask if

- **You have never seen a real sample of this format.** Add the `unverified` fixture, open the issue,
  and stop. This is the single most important rule in this skill.
- **The line could plausibly parse two ways.** Ambiguity in attendance is a guild argument, not a
  coin flip. Ask what the officers mean.
- **The parser would need database access to disambiguate.** It cannot have it. Emit candidates and
  let `internal/loot.Resolve` and the reconciliation queue decide.
- **You are tempted to make an existing golden file match new behaviour.** Rewriting a golden to go
  green is the top way an agent damages this project. If the golden is wrong, say it is wrong and why,
  in the PR body.
