---
paths: ["internal/importer/**", "test/fixtures/**"]
description: The AGPL firewall in operational form, plus the EQdkp facts an agent will otherwise guess wrong — prefix, capabilities, encoding, PHP serialize, money and time conversion.
---

# EQdkp Plus importer

This is the package where an agent is most likely to paste AGPL PHP, because the task literally is
"match EQdkp's behaviour". Read the firewall before writing a line.

## The licence firewall, operationally

EQdkp Plus core is **AGPL-3.0**; its game modules and raidlog XSD are **CC BY-NC-SA 3.0**
(non-commercial). This project is Apache-2.0.

| Allowed | Forbidden |
|---|---|
| Reading a user's own database at runtime | Transcribing their PHP, their DDL text, their language strings, their icons |
| Describing a behaviour in your own words and implementing it | Copying a function body, a regex, or a table definition |
| Naming their columns in a mapping table | Naming their identifiers anywhere else in the tree |

**Forbidden identifiers.** `pdh_`, `gen_class`, `plus_exchange`, `__multidkp2event` may appear
**only** in `internal/importer/legacy_names.go` and `internal/api/compat/`. CI greps for them
everywhere else and fails. If you need a legacy name in `transform.go`, reference the constant from
`legacy_names.go`; do not inline the string.

Class, race and zone tables ship as **our own literals**. No game data is bundled in core.

## Two phases, one staging boundary

```
source ─► fingerprint ─► capability ─► PHASE 1: stage verbatim ─► PHASE 2: transform ─► domain
                                       (import_staging.* in the                (never reads MySQL)
                                        TARGET SQLite file)
```

- Phase 1 produces a near-verbatim TEXT/JSON copy of the EQdkp tables with **exactly three**
  transforms applied — byte decoding, entity unsanitising, mojibake repair — and nothing else.
- Phase 2 reads only `import_staging.*`. It must never open a MySQL connection. That is what makes
  the dry-run pure SQL and the import crash-resumable.
- `import_staging.*` lives **inside the target SQLite database**, not in memory and not in the
  source. Commit in chunks: the single writer means a 90-second transaction blocks raid night.

Entry points (`--source mysql://…`, `--source backup.sql` via an ephemeral `mariadb` container,
`--source backup.zip` ACP backup) all converge after fingerprinting. One implementation.

**`--dry-run` is the default.** Writing requires an explicit `--commit`, which is a session +
step-up operation with no PAT scope at all.

## Fingerprint the prefix. Never assume `eqdkp23_`.

`SHOW TABLES` → every `^(.*)members$` whose siblings `\1raid_attendees` and `\1multidkp` also exist
is a candidate prefix. Multiple prefixes in one database is **normal** (co-tenanted installs):
present the list with row counts and each one's `plus_version` from `<prefix>config`, and make the
officer choose. The ACP backup zip carries `-- Table-Prefix:` in its header — read it rather than
sniffing.

## Detect capabilities by column existence, never by version string

```go
caps.HasConnectedAttendance = columnExists("groups_raid_members", "member_id")
```

EQdkp's update runner is step-indexed and resumable, so an aborted update produces a schema whose
`plus_version` **lies**. The 2.3 schema file defines `groups_raid_members` twice with different
columns, which is why that check is the canonical example. Every behavioural branch in
`capability.go` keys off a column, never a version.

Unknown columns: log and carry on. Missing optional columns: default and report. Missing **required**
columns: abort, naming the column.

## The text pipeline — order is load-bearing

```
raw bytes
  1. decode using the COLUMN's declared charset (latin1 | utf8mb3 | utf8mb4),
     read from information_schema.COLUMNS — never trust the install schema
  2. mojibake repair        (PER VALUE, iterated up to 3×)
  3. unsanitize()           (&#34;→"  &#39;/&#039;→'  &lt;→<  &gt;→>  &amp;→&  LAST)
  4. NFC normalise
  5. trim
```

- Connect with `SET NAMES binary` (or dump with `--default-character-set=binary --hex-blob
  --skip-set-charset`) and decide the encoding yourself.
- **Step 3 runs after step 2.** `&#39;` is pure ASCII and unaffected by repair, whereas repairing
  after entity-decoding risks re-mangling a legitimately decoded `&`-sequence.
- **`&amp;` is decoded LAST**, and step 3 is **exactly one pass**. Double-decoding turns `&amp;lt;`
  into `<`.
- Why this matters more than it sounds: EQdkp runs every string through `FILTER_SANITIZE_STRING`
  *before* the database, so `Trakanon's Tooth` is physically stored as `Trakanon&#39;s Tooth`. Skip
  step 3 and every P99 item with an apostrophe imports wrong — which is most of the good ones.

**Mojibake repair is per value, never per table.** One column can hold a mix. The predicate, stated
so it is testable — accept `s → s.encode(latin-1, strict).decode(utf-8, strict)` **iff** both
operations succeed **and** the count of code points in U+00C0–U+00FF strictly decreases **and** the
result contains no C0 controls **and** the result's UTF-8 byte length decreases. Otherwise keep the
original. Iterate up to 3× (twice-migrated installs carry triple-encoded text), stop when the
predicate stops improving, record the pass count, and log every repair as
`(table, pk, column, before, after)` into the dry-run report.

`utf8mb3` truncated any 4-byte character at insert time. Detect trailing partial `\xF0`-family
sequences and **report** the loss; it is not recoverable and must not be hidden.

## PHP `serialize()`

- **`s:<len>:` lengths are BYTES, not characters.** Parse over `[]byte` and slice by byte length,
  never by rune count — any already-mojibaked value blows up a rune-based parser.
- **Reject `O:` object payloads as corrupt.** EQdkp itself passes `allowed_classes => false`.
- If the declared length overruns the buffer or the closing `";` is misplaced, record
  `unparseable_serialized` and move on. Most serialized columns are caches we discard, which caps
  the blast radius.
- The in-tree reader handles the four shapes we need (~120 lines). A maintained library is used as a
  **differential-test oracle in CI**, not as a runtime dependency.
- `__config` has its own read heuristic and it must be reproduced exactly: a value is serialized only
  if it contains `:{` **and** unserializes to an array; otherwise it is a literal string. Apply
  `stripslashes` first (legacy magic-quotes damage).

## Money and time conversion

| Source | Target | Rule |
|---|---|---|
| `float(11,2)` points | `Centipoints int64` | `round(v * 100)` **half-even**, at the boundary only, **logging every row whose round-trip differs**. Never `float64` past this line |
| `int(11)` UTC epoch | `Micros int64` | `× 1_000_000` |
| epoch `0` | `NULL` | "Never", not 1970-01-01. A member created in 1970 looks like a bug forever |
| before 1999-03-16 or after `now + 1y` | quarantine + report line | Never a silent clamp |

Genuinely-local timestamps exist in very old installs. Detect by histogramming
`raid_date mod 86400`, present the histogram in the wizard, and put the officer's answer in the
mapping config so a re-run is deterministic. If local reinterpretation is chosen, convert with the
IANA zone's **historic** rules per instant (`time.LoadLocation` + `time.Date`) — a fixed-offset
conversion silently shifts three years of raids by an hour.

## Idempotency and provenance

- `import_source_id = sha256(dbname ‖ prefix ‖ min(raid_id) ‖ min(raid_date) ‖ min(member_id) ‖
  member_creation_date)` — a **content** fingerprint, deliberately not derived from the connection
  string, so a dry-run from a zip on Monday and a delta-import from a live DSN on Friday recognise
  the same source.
- `ledger_batch.source_ref = 'eqdkp:{prefix}:{table}:{pk}[:{pool}]'`, uniquely indexed. Re-posting an
  existing batch is a no-op returning the existing batch.
- `import_id_map` is persisted, not rebuilt. The compat shim resolves legacy `member_id`s through it
  so bots with hardcoded ids keep working.
- Running `--commit` twice must produce zero new rows **and an unchanged head `ledger_batch.hash`**,
  which proves nothing was appended at all.

## Non-negotiables

- **Passwords are never migrated.** Usernames and emails only.
- **Nothing is skipped without appearing in the report.** Every skip carries a reason code
  (`orphan_member`, `orphan_raid`, `duplicate_attendee`, `unparseable_serialized`,
  `timestamp_out_of_range`, `empty_character_name`, `charset_undecodable`, `plugin_table`,
  `over_asset_budget`) and its source primary key.
- **Never drop an award because the item cannot be resolved.** Buyer, price, raid, date and pool are
  all still correct; create `item(state='provisional')` via the **upsert** path and queue it.
- **Plugin tables are reported with row counts, never silently dropped.** Enumerate every prefixed
  table, subtract the known core set, list the remainder.
- **BBCode and HTML are sanitised on import and again on render**, through `internal/richtext`.
  Unknown or unclosed tags are escaped and preserved as literal text, never passed through.
- Asset fetch from a live site is **off by default**, SSRF-guarded, size-capped, and every image is
  re-encoded through a decoder rather than stored verbatim.

## Fixtures — `test/fixtures/`

CODEOWNERS-protected. **Do not rewrite a fixture to make a test pass**; a failing fixture is the
importer telling you the mapping is wrong.

The reconciliation oracle is `__members.points` — EQdkp's own PHP-serialized
`[mdkp_id => [earned, spent, adjustment]]`. The assertion is **exact**: the set of members whose
balance mismatches must equal the set the APA classifier predicted. Softening that to a numeric
tolerance destroys the only end-to-end correctness check the importer has.

## Stop and ask if

- An EQdkp table shape is not in `test/fixtures/`.
- A serialized column's key set is undocumented — import it as opaque JSON, never interpret it.
- A permission or ACL mapping is ambiguous. Never guess an ACL; import conservatively and report the
  downgrade, naming the specific capability each person lost.
