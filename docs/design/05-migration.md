# EQdkp Plus migration

**Status:** design, normative for `internal/importer`. **Audience:** contributor, agent.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md).

Officer-facing pages derived from this document: [`docs/migration/from-eqdkp.md`](../migration/from-eqdkp.md),
[`what-does-not-migrate.md`](../migration/what-does-not-migrate.md),
[`reading-your-verification-report.md`](../migration/reading-your-verification-report.md),
[`parallel-run-and-cutover.md`](../migration/parallel-run-and-cutover.md).

**Notation.** `<prefix>` stands for the guild's detected table prefix (`eqdkp20_`, `eqdkp_`, …).
Legacy table names are written `<prefix>members`, never with EQdkp's own `__` placeholder — the
licence firewall (canonical §15) greps for a small set of EQdkp identifiers, and this document must
not be the thing that trips it.

---

## 1. Three claims, not one

"Import as much as possible" conflates three different promises. Separating them is the whole
design; everything below falls out of this table.

| Claim | Achievable? | Why | Mechanism |
|---|---|---|---|
| **Fact fidelity** — every raid, attendee, item, adjustment, character, rank and pool row lands intact | **Yes**, from a database or a dump | These are plain rows with plain foreign keys | Two-phase ETL, §4–5 |
| **Balance fidelity** — every member's number equals what the old site displayed | **Yes, by construction** — but only via a labelled residual entry | EQdkp's displayed balance is **not a function of its own database**. Decay, caps and start-points rules (APAs) live on disk in `data/<md5(prefix.dbname)>/eqdkp/apa/apatab.php`, which is not in any database backup | §6.1 |
| **Semantic fidelity** — the new system *behaves* like the old one | **No. Do not pretend otherwise.** | ACL detail, decay rule definitions, layout presets, styles, plugins and forum bridges have no equivalent here | §6.5, §7 |

The commitment that replaces semantic fidelity: **every non-survival is named, counted and reported.**
Never silent. That is the sentence the officer-facing docs repeat, and §7 is the artifact that
proves it.

### 1.1 Non-negotiables

1. **`--dry-run` is the default.** `--commit` is a separate, explicit act, gated on `import.commit`,
   session + step-up (canonical §6). Officers will run the dry run three or four times; that is the
   intended behaviour, not a failure to converge.
2. **Nothing is dropped silently.** Every skipped row produces an `import_finding` row with a reason
   code and its source primary key, and appears in a downloadable list. *Enforced by:* a test that
   asserts `Σ staged rows == Σ loaded rows + Σ import_finding rows with severity ≥ warn` for every
   fixture.
3. **The importer never writes to the source.** *Enforced by:* `internal/importer/readonly.go` wraps
   the MySQL `*sql.DB` and rejects any statement not matching `^\s*(SELECT|SHOW|DESCRIBE|DESC|EXPLAIN)\b`,
   with an integration test asserting an `INSERT` is refused.
4. **Reading a legacy database is not reading legacy code.** The licence firewall (canonical §15)
   binds harder here than anywhere else in the repo, because the importer is the one component where
   a helpful agent reaches for EQdkp's PHP "to get the formula right". Class and race tables ship as
   our own literals.

---

## 2. Ingest modes, ranked by officer effort

| Rank | Mode | `import_run.source_kind` | What the officer does | Fidelity | Delta-capable | Dominant failure |
|---|---|---|---|---|---|---|
| **1 — recommended** | ACP backup zip | `acp_backup_zip` | ACP → Maintenance → Backup → Download; drag the zip into the wizard | **Full** | Yes, with a fresh zip | Backup job timed out on a huge `<prefix>logs` table → partial zip |
| **1= — same code path** | `mysqldump` `.sql` | `sql_dump` | Host control-panel "export database", or the one command we print | **Full** | Yes, with a fresh dump | Dump taken with the wrong `--default-character-set`, baking in mojibake (§6.6) |
| **2 — best for a parallel run** | Live MySQL, read-only credentials | `mysql` | Create a read-only user (we print the `GRANT`), open a tunnel, paste a DSN | **Full**, plus native `information_schema` introspection | **Yes, trivially** | No network path — shared hosting binds MySQL to `127.0.0.1`. See §2.4 |
| **3** | EQdkp XML/JSON data export | *(as `artifact.kind = 'eqdkp_xml'`)* | ACP → Data-Export | **Low — a balances snapshot, not a history.** No per-raid attendance rows; attendance percentages cannot be reconstructed | No | Officers assume it is a migration. The wizard says one blunt sentence saying it is not |
| **4** | CSV / XLSX | *(non-EQdkp path, §12)* | Paste or upload a table | Whatever was typed | No | Name collisions; no pool concept |

**Why the backup zip is primary and not the live DSN.** Modes 1 and 2 tie on fidelity. The zip wins
because it is *a button inside software the officer already runs*: no network exposure, no DBA, no
credential handling, and it produces a file they should be keeping anyway. Its header carries
`-- Table-Prefix: <prefix>` and a sidecar records the table list, so prefix detection is free and a
truncated backup is detectable by comparing the sidecar's table list against the tables present. It
also means an officer can hand the file to a more technical guildmate, which is how volunteer
organisations actually work. Mode 2 earns its rank solely because delta re-import (§10) is one query
rather than one more zip.

### 2.1 One code path after fingerprinting

```
live MySQL DSN            ┐
mysqldump .sql/.gz/.zst   ├─► ephemeral MariaDB  ─┐
ACP backup .zip           ┘   OR in-process reader ├─► reflect ─► fingerprint ─► capability ─► STAGE
XML / JSON export         ──────────────────────────────────────────────────────────────► STAGE (partial)
CSV / XLSX                ──────────────────────────────────────────────────────────────► STAGE (partial)
```

`fingerprint.go` never assumes a prefix. `SHOW TABLES` → every table matching `^(.*)members$` whose
siblings `\1raid_attendees` and `\1multidkp` also exist is a candidate prefix. Multiple prefixes in
one database is normal (co-tenanted installs); present the list with row counts and the
`plus_version` from each `<prefix>config`, and make the officer choose.

**`capability.go` branches on column existence, never on `plus_version`.** EQdkp's update runner is
step-indexed, resumable and abortable, so a half-applied update leaves a schema whose version string
lies. The canonical example is `<prefix>groups_raid_members`, which the 2.3 schema file defines
twice with different key columns (`user_id` vs `member_id`); the importer introspects and, if
genuinely ambiguous, **aborts naming both candidates** rather than guessing.

*Enforced by:* a lint-style unit test asserting no comparison against `plus_version` outside
`fingerprint.go`'s display path.

### 2.2 Reading a dump without a MySQL server

**Primary: an ephemeral MariaDB container.** `testcontainers-go` + `mariadb:12.3` (multi-arch, so it
works on a Pi and on Apple silicon): `tmpfs` datadir, random root password, no volume, ephemeral
`127.0.0.1` bind, Ryuk reaper, `--skip-name-resolve`, `--innodb-flush-log-at-trx-commit=0`,
`--innodb-doublewrite=0`. Load by `docker exec`-ing `mysql --default-character-set=binary < dump`
**inside** the container so bytes never round-trip through a Go driver. Then read it through the
identical reflection path as the live mode — three entry points, one implementation.

**Fallback: an in-process dump reader**, for the officer running the binary on a Windows raid PC with
no Docker, and for the scratch container with no Docker socket. Deliberately narrow:

- A `bufio.Reader` state machine. **Never `regexp`, never load the file into memory** — a 2 GB dump
  with a 400 MB log table is normal.
- Recognises exactly two statement shapes: `CREATE TABLE` (column names, declared types, per-column
  `CHARACTER SET`/`COLLATE`) and `INSERT INTO … VALUES (…),(…),…` streaming tuple by tuple.
- Handles MySQL literal syntax: `\\ \' \" \n \r \t \0 \Z \% \_`, doubled `''`, `0x…`, `_binary'…'`,
  `NULL`, `X'…'`.
- **Ignores everything else** — `LOCK`/`UNLOCK TABLES`, `SET`, `/*!40101 … */` blocks, `DELIMITER`
  regions, triggers, views, routines, `DEFINER` clauses. Safe, because the target is a data extract,
  not a schema replica.
- On any parse failure it reports the byte offset, 200 surrounding bytes, and "use `--use-container`
  instead". The verification report records which path was used.

Both paths transparently unwrap `.gz`, `.zst`, `.bz2` and `.zip`.

*Enforced by:* **dump-reader parity** — the same fixture imported through the container and through
the in-process reader must produce byte-identical `stg_*` tables.

### 2.3 `dkp export-legacy`

A single-binary mode that connects to MySQL, runs **phase 1 only**, and writes
`legacy-<source>.dkpstage`: a zstd-compressed ndjson bundle of the staged tables plus the fingerprint
and capability flags. The officer runs it from a laptop with tunnel access, or on the old host
itself, then uploads the bundle. This converts a network-topology problem into a file-transfer
problem, and costs almost nothing because the bundle is just a serialisation of a phase boundary that
already exists.

### 2.4 Live-source safety

- We print the exact grant:
  `CREATE USER 'dkp_import'@'%' IDENTIFIED BY '…'; GRANT SELECT, SHOW VIEW ON \`db\`.* TO 'dkp_import'@'%';`
- Read-only is **enforced in code**, not trusted — §1.1 rule 3.
- Credentials are held in memory for a one-shot run. For a parallel run they become a named,
  revocable source record encrypted with the instance secret, surfaced in Settings → Data Sources
  with test-connection and forget buttons, and reported by `dkp doctor`. Never in a config file,
  never in the audit log, never echoed into the report. `import_run.source_label` holds host/db or a
  filename and **never credentials** (schema comment enforces the intent; a test asserts the report
  JSON contains no `://` credential form).
- TLS on by default. `?tls=skip-verify` requires an explicit `--insecure` that prints a warning into
  the report.

---

## 3. Where the import surfaces

| Method | Path | `operationId` | Permission | Notes |
|---|---|---|---|---|
| POST | `/api/v1/admin/imports` | `createImport` | `import.run` | `Idempotency-Key` required. Starts a **dry run** (`dry_run = 1`) |
| GET | `/api/v1/admin/imports` | `listImports` | `import.run` | Standard `{items, next_cursor, has_more}` envelope |
| GET | `/api/v1/admin/imports/{id}` | `getImport` | `import.run` | |
| GET | `/api/v1/admin/imports/{id}/report` | `getImportReport` | `import.run` | Rendered page + machine JSON (§7) |
| POST | `/api/v1/admin/imports/{id}/commit` | `commitImport` | `import.commit` | `If-Match` on the run's ETag; `Idempotency-Key` required |
| POST | `/api/v1/admin/imports/{id}/revert` | `revertImport` | `import.commit` | Tier-2 logical revert (§9.3) |

**There is no PAT scope for import.** Canonical §6 gives no `import:*` scope, and effective
capability is role permissions ∩ token scopes — so no token can start or commit an import, by
construction. The import API is **session-only**, and `commitImport` additionally requires step-up
re-auth. The wizard is a pure client of these endpoints; `curl` with a session cookie drives the
identical sequence, and the API-parity test asserts there is no UI-private import path.

> Supersedes: the source API design's `admin:import` scope and `import.create` permission. Canonical
> §6 is the catalogue.

Progress is SSE on topic `import:{id}` with events `import.progress`, `import.completed`,
`import.failed`. A mirroring pool rejects non-importer writes with `409` and error code
`pool_is_mirroring` — **adding that code is a spec change and needs a docs page** (canonical §7).

---

## 4. The two-phase ETL and why the staging boundary exists

**Phase 1 — stage verbatim.** `stg_<legacy_table>` tables **in the main SQLite database**, one per
source table, TEXT-typed, carrying `stg_row_id`, `import_run_id`, `src_pk`, `row_hash`, `loaded_at`,
`decode_notes_json`. Exactly three transforms are applied and no others: byte decoding, entity
unsanitising, mojibake repair (§6.6).

Staging is **not** an `ATTACH`ed second file. SQLite gives atomicity per attached database, not
across the set, so a crash between the id-map write and the domain write would be unrecoverable in
WAL mode. `stg_` is a table-name prefix in the one file the officer already backs up.

**Phase 2 — transform.** Staging → domain, chunked, resumable, idempotent.

Four properties follow, and they are the entire justification for the boundary:

1. **The dry-run report is a set of SQL queries**, not an in-memory pass. It can be regenerated,
   drilled into and diffed without re-reading the source.
2. **The import is crash-resumable**, because staging survives the crash.
3. **The officer can be shown row-level diffs** — "this is what we read; this is what we will create".
4. **MySQL-shaped code never touches the domain model.** `stage.go` knows about `float(11,2)` and
   `utf8_bin`; `internal/ledger` never does. *Enforced by:* the same import-boundary architectural
   test that keeps `*sql.DB` inside `internal/store`.

Staging is dropped on finalise unless `--keep-staging`. **The CMS load (§5.11) requires
`--keep-staging`,** because content rows are staged in Phase 5 and loaded when the Phase 8 tables
exist; see §13.

**Write fairness.** Phase 2 commits in chunks of **≤ 2,000 rows** with an explicit yield between
chunks, and the cursor is the `stg_*.loaded_at` stamp written **in the same transaction as the domain
rows**. An import refuses to start while any raid is `open`. Without both, a 90-second import on a
single-writer database blocks every raid-night write. *Enforced by:* a write-latency SLO test that
runs an import concurrently with a tick submission.

---

## 5. Field-by-field mapping

Conventions for every row below. Money: legacy `float` → `Centipoints int64` via `round(v × 100)`
half-even at the boundary only, logging every row whose round-trip differs (canonical §1). Time:
legacy `int(11)` UTC epoch seconds → `Micros int64` (`× 1_000_000`); **`0` means "never" → NULL, not
1970** (canonical §2). "Fallback" is the behaviour when the column is absent (older EQdkp), NULL, or
unparseable.

### 5.1 Ranks

| Source | Target | Transform | Fallback |
|---|---|---|---|
| `<prefix>member_ranks.rank_id` | `rank.legacy_id` + `import_id_map` | — | Required; abort if absent |
| `.rank_name` | `rank.name` | text pipeline (§6.6) → NFC | Empty → `Rank <id>` |
| `.rank_hide` | `rank.hidden` | tinyint → bool | Absent → `false` |
| `.rank_prefix`, `.rank_suffix` | `rank.prefix`, `rank.suffix` | text pipeline | `""` |
| `.rank_sortid` | `rank.sort_order` | int | Absent → insertion order |
| `.rank_default` | `rank.is_default` | bool; enforce exactly one | None set → lowest `sort_order` |
| `.rank_icon` | `rank.icon_asset_id` | asset resolution (§6.8) | Unresolved → NULL, reported |
| `<prefix>config.special_members` | `rank.excluded_from_roster` | parse rank-id list; may be serialized | Absent → no exclusions |

Seed rows `(0,'')` and `(1,'Member')` are installer artifacts. Rank 0 with no name and no members is
dropped silently — it is structurally a NULL. Rank 0 *with* members imports as `Unranked`.

### 5.2 Characters

| Source | Target | Transform | Fallback |
|---|---|---|---|
| `<prefix>members.member_id` | `character.legacy_id`; the key for **every** cross-reference | — | Required |
| `.member_name` | `character.name` + `name_norm` | text pipeline; **original bytes preserved for display** | Empty → skip, `empty_character_name` |
| `.member_status` | `character.status` | `1 → active`, `0 → inactive` | Absent → active |
| `.member_rank_id` | `character.rank_id` | via rank id map | Unmatched → default rank, reported |
| `.member_main_id` | person assignment | union-find (§6.3). `NULL`, `0` or self all mean "is a main" | Dangling → treat as main, reported |
| `.member_creation_date` | `character.created_at` | epoch → Micros | `0` → first raid date, else import time |
| `.notes` | `character.notes` | text pipeline + BBCode sanitise (§6.8) | `""` |
| `.picture` | `character.avatar_asset_id` | asset resolution | Unresolved → NULL, legacy path retained as text |
| `.defaultrole` | `character.default_raid_role_id` | via raid-role map | Unmatched → NULL |
| `.profiledata → class` | `character.class` | PHP-serialized; values are numeric **strings**. Map via `default_game`; for `eq`, our own 1–16 table | Unknown → `class = unknown`, character still imports, queued in reconciliation |
| `.profiledata → race` | `character.race` | our own 1–16 table. **P99 has 13 playable races** — Beastlord/Berserker/Drakkin/Froglok/Vah Shir are legal in EQdkp and impossible on P99: import as-is, flag `era_impossible` | Absent → NULL (race is genuinely optional) |
| `.profiledata → level` | `character.level` | int; > 60 on a P99 server gets a report line, not a clamp | Absent → NULL |
| `.profiledata → guild, gender` | `character.profile_json` keys | text | Absent → omit |
| `.points`, `.points_apa` | **oracle only** (§6.1) — never imported as data | PHP-unserialize; tolerate failure | Unparseable → oracle drops a rung, reported |
| `.last_update`, `.requested_del`, `.require_confirm` | dropped | — | Reported as `columns_ignored` |
| `<prefix>member_profilefields` | `character_field_def` | One definition row per legacy field; `kind` inferred from the legacy field type, defaulting to `text` | Unknown legacy type → `text`, reported |
| `<prefix>member_profilefields` **values** | `character_field_value` | Typed: `value_text` + `value_text_norm`, or `value_int` for int/bool/date | Value fails its inferred `kind` → stored as `text`, reported |

**Custom profile fields import as typed, queryable fields**, not as a `profile_json` blob:
`character_field_def` and `character_field_value` exist in the domain model (01 §6.4) and ship in
Phase 2. The report lists every legacy field with its populated-row count **and the `kind` it was
mapped to**, because a field an officer wants to sort numerically must not land as `text`.

> Supersedes an earlier draft of this section, which claimed no domain table defined
> `character_field_def` and routed these into `profile_json`. That was true of the draft schema and
> is not true of the shipped one. `profile_json` is validated on write and never queried into, so it
> could not answer "show me every character whose *Alt of* field says Tankguy" — which is exactly
> what guilds use these fields for.

### 5.3 Accounts and the character ↔ account link

| Source | Target | Transform | Fallback |
|---|---|---|---|
| `<prefix>users.user_id` | `person.legacy_user_id` | — | Required |
| `.username` | `app_user.login_name` (unique) | text pipeline; collision → suffix `#<legacy_id>`, reported | Empty → skip, reported |
| `.user_email` | `app_user.email` | lowercase, RFC-validate | Invalid/empty → NULL; account is **code-claim only** (§6.4) |
| `.user_password` | **never imported** | `password_hash = NULL`, `must_reset = 1` | — |
| `.user_registered`, `.user_lastvisit` | `.created_at`, `.last_seen_at` | epoch → Micros | `0` → NULL |
| `.user_active` | account status | `0` → `disabled` (still claimable; an officer must re-enable) | Absent → active |
| `.user_timezone`, `.user_lang` | user preferences | IANA-validate | Invalid → guild default |
| `.user_date_time/short/long` | dropped | our formats are locale-derived | Reported |
| `.exchange_key`, `.user_login_key`, `.auth_account` | **never imported** | `auth_account` is AES-256-CTR under a key derived from `config.php` — unrecoverable and unwanted | Reported: "N linked social accounts were not migrated; members re-link via Discord login" |
| `.custom_fields`, `.plugin_settings`, `.privacy_settings`, `.notifications` | selective | PHP-unserialize; keep only keys matching an imported user profile field | Unparseable → dropped, reported |
| `.awaymode_start`, `.awaymode_end`, `.awaymode_text` | `person.away_from_at`, `person.away_until_at`, `person.away_note` | epoch → Micros; text pipeline on the note. The columns exist (01 §6.1) | Window already elapsed → imported anyway; the nightly sweep clears it. Start absent but end present → both dropped (the `CHECK` forbids it), reported |
| `<prefix>member_user(member_id, user_id)` | `character.person_id` + `character_claim(state='verified', method='import')` | **No PK, no unique — de-duplicate.** A character claimed by two users is a real conflict: link to the user with the earlier `user_registered`, queue the other as `reconciliation_item(kind='import_conflict')` | Character with no user → the alt group's main's person (§6.3), or an unclaimed placeholder person |
| `<prefix>sessions` | **never imported** | — | — |

`method = 'import'` is an existing value in the `character_claim.method` catalogue (01 §6.5). It is
not a new enum member and must not be spelled `legacy_import`: the wire value is the database value
and there is one Go catalogue (canonical §5).

### 5.4 Groups, roles, permissions

| Source | Target | Transform |
|---|---|---|
| `<prefix>groups_user.*` | `role` — built-in match, or `Legacy: <name>` | §6.5 algorithm |
| Group id **2** (Super Admins) | `role = admin`, **zero members auto-assigned** | Group 2 is hardcoded in EQdkp to bypass the permission table entirely. Auto-promoting unclaimed accounts to admin is a privilege-escalation hole |
| Groups 1/3/4/5/6 | *Suggested* map to `guest`/`admin`/`member`/`officer`/`member` | Suggestion only; the officer confirms in the wizard |
| `<prefix>auth_options.auth_value` | permission key, via a curated table | Unmapped → **dropped and reported**, never approximated upward |
| `<prefix>auth_groups(group_id, auth_id, 'Y'/'N')` | role permission set | union of `Y`, minus `N` |
| `<prefix>auth_users(user_id, auth_id, …)` | **never a grant** | A `Y` becomes a report line. An `N` **is honoured**, by removing that person from the granting role. Denies are safe to honour; grants never are |
| `<prefix>groups_users.grpleader` | scoped `raid.*` on that raid group, if scoped role assignment exists | Else dropped + reported |
| `<prefix>config.api_key`, `.api_key_ro` | **never imported** | Reported: "N site-wide API tokens existed. None migrated. Mint scoped tokens under Settings → Service Accounts" |

### 5.5 Raid roles and raid groups

| Source | Target | Transform |
|---|---|---|
| `<prefix>roles.role_id/_name/_icon` | `raid_role` | text pipeline; icon → asset |
| `.role_classes` | `raid_role.class_ids[]` | **pipe-delimited** class ids → our class enum; unknown ids dropped and reported |
| `<prefix>groups_raid.*` | `raid_group` (name, colour, description, sort, default) | `#rrggbb` validated; invalid → NULL |
| `<prefix>groups_raid_members` | `raid_group_member` | ⚠ **introspect the key column** (§2.1). De-duplicate — no PK. Ambiguous → abort naming both candidates |
| `<prefix>classcolors` | dropped | Theming; reported |

### 5.6 Pools and item pools

| Source | Target | Notes |
|---|---|---|
| `<prefix>multidkp.*` | `pool` | `pool.currency_label` ← `<prefix>config.dkp_name`; guilds rename "DKP" constantly. No pools → synthesise `Default` |
| `<prefix>itempool.*` | `item_pool` | No item pools → synthesise `default` |
| `<prefix>multidkp2event(multi_id, event_id)` | `pool_event_type` | Many-to-many. An event feeding N pools produces N attendance batches per raid |
| … `.no_attendance` flag | `pool_event_type.excludes_from_attendance` | **Load-bearing.** Drop it and lifetime attendance percentages differ from the old site for everyone. Officers notice within one raid night. Column absent → `false` |
| `<prefix>multidkp2itempool(itempool_id, multi_id)` | `pool_item_pool` | An item pool in two pools charges an item in **both**. Legal EQdkp config, usually accidental. **Reproduce faithfully** or balances will not reconcile; report as `overlapping_item_pool` and offer a previewed `--split-overlaps` remediation |
| `<prefix>events.default_itempool` | `event_type.default_item_pool_id` | Absent → the single item pool, or NULL |
| `<prefix>config.dkp_easymode` | wizard hint | With one pool, offer "hide the pool selector". A UI preference, not a data change |
| `<prefix>config.show_twinks` | alt-accounting mode (§6.3) | Decides one person per alt group vs one per character. Absent → `true` |

Pools with zero events and zero item pools import with `active = 0`.

### 5.7 Events, raids, ticks, attendance

| Source | Target | Notes |
|---|---|---|
| `<prefix>events.event_id/_name/_value/_icon` | `event_type` | `event_value` → default tick value in centipoints. `float(6,2)` caps a single value at ±9999.99 |
| `.event_show_profile` | `event_type.show_on_profile` | Absent → true |
| `.event_added_by`, `.event_updated_by` | `event_type.legacy_actor` (text) | **These are usernames as strings, not foreign keys.** Resolve to a person by login name where possible; otherwise keep the string |
| `<prefix>raids.raid_connected_attendance` | **raid-session grouping** | JSON array of raid ids. Union-find over the edges, which are frequently asymmetric. **Each connected component becomes ONE `raid` session; each legacy raid within it becomes ONE `raid_tick`.** A non-connected raid becomes a single-tick session. This is exactly the P99 shape and it makes the attendance denominator match EQdkp automatically. Column absent → every raid is its own single-tick session |
| `<prefix>raids.raid_id` | `raid_tick.legacy_raid_id`; the session gets a synthetic ULID | Required |
| `.event_id` | `raid_tick.event_type_id`; the session's event is the highest-value member, or `Multi-target session` when the component spans events | Orphans exist despite the FK → synthesise `Unknown event (legacy)`, reported |
| `.raid_date` | `raid_tick.at`; session `started_at` = min, `ended_at` = max | Out of `[1999-03-16, now + 1y]` → quarantine, reported |
| `.raid_value` | `raid_tick.value_cp` | NULL → event default → 0 |
| `.raid_note`, `.raid_additional_data` | `raid.note` (the second appended and labelled) | Both are free-form BBCode despite the second one's name; sanitise (§6.8) |
| `.raid_added_by/_updated_by` | `raid.legacy_actor` | Username strings |
| `.raid_apa_value` | oracle input only (§6.1) | PHP-unserialize; unparseable → ignored |
| `<prefix>raid_attendees(raid_id, member_id)` | `attendance(tick_id, character_id, status='present', weight_bp=10000)` | **No PK, no unique, no FK — de-duplicate first.** The source has no weight, no join/leave, no standby. Orphan `member_id` or `raid_id` → **drop, count, list**. This is the single largest source of legitimate balance deltas |

### 5.8 Items and awards

| Source | Target | Notes |
|---|---|---|
| `<prefix>items.item_id` | `item_award.legacy_id` | Required |
| `.item_name` | item resolution (§6.7) | `Trakanon&#39;s Tooth` → `Trakanon's Tooth` is the single highest-impact transform in the importer. Unresolved → create a provisional item, **never drop the award** |
| `.member_id` | `item_award.character_id` + the ledger debit's `account_id` | **There is no purchasers table — the buyer is `items.member_id`, one row per buyer.** Orphan → drop award, report `orphan_item_buyer` |
| `.item_group_key` | `item_award.group_id` | Multi-buyer and bulk operations are N rows sharing this key. **Preserve the grouping or the UI double-counts.** Same name + same raid across the group → one loot event with N purchasers, each charged their own value. Differing names → import individually, report `inconsistent_item_group` |
| `.raid_id` | award's raid, and its tick when the timestamp falls inside one | Orphan raid → raid-less award (flag `--drop-orphan-items` to change), reported |
| `.item_value` | `item_award.price_cp` and the ledger debit | NULL → 0, reported |
| `.item_date` | `item_award.at` | `0` → raid date |
| `.itempool_id` | which pool(s) are debited | Via the item-pool mapping; unmatched → default item pool, reported |
| `.game_itemid` | `item.external_id` + `item.external_source` | Opaque string; provider is guild-dependent |
| `.item_color`, `.item_apa_value` | dropped / oracle-only | Cosmetic; oracle input |

### 5.9 Adjustments

| Source | Target | Notes |
|---|---|---|
| `<prefix>adjustments.adjustment_id` | `adjustment.legacy_id` | Required |
| `.adjustment_value` | ledger entry amount | Signed. NULL → skip, reported |
| `.member_id` | `adjustment.character_id` + entry `account_id` | Orphan → drop, reported |
| `.adjustment_reason` | `adjustment.reason` | Text pipeline — **apostrophes are entity-encoded here too**. Empty → `(no reason given)` |
| `.adjustment_date` | `adjustment.effective_at` | `0` → raid date, else import time |
| `.adjustment_group_key` | one **ledger batch** per group | Mass adjustments become one batch with N entries, which is exactly our batch semantics. Empty → one batch per row |
| `.event_id` **varchar(255)** | pool attribution | Parse as int → the pools containing that event. Separators → split and attribute to each, reported. Empty/`0`/non-numeric/unmatched → **unattributed**: land in the pool the wizard designates (default: the default pool) with `legacy_unattributed` in metadata, and report count and total. **EQdkp displays these nowhere, so importing them creates a delta** — a delta with a known, explainable cause |
| `.raid_id` | the adjustment's raid | Orphan → NULL |

### 5.10 Point caches — the oracle, never data

| Source | Use |
|---|---|
| `<prefix>members.points` | PHP-serialized `[pool => [earned, spent, adjustment]]`, single-character, **no APA applied**. Validates our aggregation — it is literally the same three SQL sums — and its own staleness is diagnostic |
| `<prefix>members.points_apa` | `[apa_id => [pool => ['single'\|'multi' => value]]]`. **Reveals which APAs ran and what each did**, even though the rules themselves are on disk. This is how residuals get classified without reading `apatab.php` |
| `<prefix>member_points` | Periodic snapshots (default cadence 3 weeks). Secondary oracle; also gives a free "points over time" chart for imported history |
| Live `api.php?function=points` via `--oracle-url` | **The best oracle: the actual render path**, APA-applied, already unsanitised — exactly what members saw. With it, residual classification goes from inference to arithmetic |

**None of these are ever imported as balances.** All four feed §6.1.

### 5.11 Content, calendar, audit, config

Articles, comments and categories **are** imported and **do** get an editor — full portal parity is
in scope (Phase 8). The source design imported them into tables it never defined; §13 lists the
tables this now requires.

| Source | Target | Notes |
|---|---|---|
| `<prefix>articles.title` | `article.title` | **May be a plain string OR a PHP-serialized language map.** Take the `default_lang` value; stash the rest in metadata |
| `.text` | `article.body_source` + `.body_html` | BBCode/HTML → sanitised HTML (§6.8). **Store both**, so the editor round-trips |
| `.category`, `.alias`, `.published`, `.show_from/_to`, `.date`, `.user_id`, `.hits`, `.tags` | corresponding fields | `tags` is PHP-serialized |
| `.previewimage`, inline `[img]` | asset resolution | Unresolved → text retained |
| `.votes*`, `.page_objects`, `.sort_id`, `.hide_header`, `.index` | dropped | Reported |
| `<prefix>article_categories.permissions` | **dropped** | Serialized `{action → group_id → flag}` whose `-1` semantics are unverified. **Never guess an ACL.** Categories import officer-visible; the report says so and the officer opens them up |
| `<prefix>comments` | `article_comment` | Sanitise. `date` is `varchar(255)` — parse defensively; unparseable → the article's date |
| `<prefix>calendars` | `calendar` | `type` 1 = raid, 2 = normal |
| `<prefix>calendar_events.*` | `calendar_event` | `timestamp_start/_end`, `allday`, `private`, `repeating`, and a **per-event** `timezone` that carries over directly |
| `<prefix>calendar_events.extension` | opaque JSON on the event | PHP-serialized raid config (linked event, role slots, class caps). **Key set unverified** — decode to JSON, store verbatim, never interpret. Surfaced as "legacy settings" text |
| `<prefix>calendar_raid_attendees` | `signup` | Has a real `UNIQUE(event, member)` — the only junction table that does. Orphan → dropped |
| `.signup_status` `varchar(255)` | `signup.status` | The status vocabulary is guild-configured free text; import the distinct set as the guild's vocabulary |
| `<prefix>calendar_raid_guests` | `signup(kind='guest')` | Guest name/email/class/role |
| — | calendar ↔ raid linking | **No link exists in EQdkp core.** After import, offer a previewed heuristic ("these 47 calendar raids look like these 47 raids — same day, same event"), one-click, never automatic |
| `<prefix>logs` | `audit_log` with `source = 'eqdkp_import'` | Map log tags to our action vocabulary where possible; unmapped → `legacy:<tag>`. **IP addresses are dropped by default** (`--import-ip-addresses` to keep). A volunteer guild should not inherit a decade of members' IP addresses |
| `<prefix>config` (core rows) | guild settings | Reproduce EQdkp's read heuristic **exactly**: a value is serialized only if it contains `:{` **and** unserializes to an array; otherwise it is a literal string. Apply `stripslashes()` first (magic-quotes damage). Carry: `guildtag`, `servername`, `dkp_name`, `default_game`, `show_twinks`, `detail_twink`, `special_members`, `hide_inactive`, `inactive_period`, `auto_set_active`, `timezone`, `round_activate`, `round_precision`, `enable_leaderboard`. Everything else reported as `settings_not_migrated` with a count |
| `<prefix>plugins` and plugin tables | **report only** | Enumerate every table with the detected prefix, subtract the known core tables, list the remainder **with row counts**. Named lines for the interesting ones: "plugin `itemprio` had 340 rows; no import path exists — open an issue if you need it" |
| `<prefix>styles`, portal tables, `<prefix>links`, `<prefix>repository`, `<prefix>cronjobs`, notification tables | dropped | Reported by table with row counts |

---

## 6. The hard problems

### 6.1 Reconstructing an append-only ledger from a system that stored aggregates

**The shape of the problem.** EQdkp stores no balances. It computes, per pool, three SQL
aggregations — earned from raids ⋈ attendees over the pool's events, spent from items over the pool's
item pools, adjustments over the pool's events — and then applies zero or more **APAs** (decay, soft
cap, hard cap, current cap, one-time, start points) whose definitions live **on disk**, not in the
database. A database-only import therefore reconstructs the facts perfectly and still shows a
different number than the officer's screen, by exactly the accumulated APA effect.

**Synthesis.** Phase 2 emits batches in `effective_at` order, so per-pool `seq` reads chronologically:

| Legacy fact | `ledger_batch.kind` | `source_ref` | Entries |
|---|---|---|---|
| Raid session × each pool containing its event | `attendance` | `eqdkp:<prefix>:raid:<id>:<pool>` | `+raid_value` per attendee |
| Item row, or an `item_group_key` group | `award` | `eqdkp:<prefix>:item:<id\|group>:<pool>` | `−item_value` per buyer |
| Adjustment row, or an `adjustment_group_key` group | `adjustment` | `eqdkp:<prefix>:adj:<id\|group>:<pool>` | `±adjustment_value` per member |
| The residual (below) | `import` | `eqdkp:<prefix>:reconcile:<pool>:<member>` | one entry, amount = Δ |

Every imported batch carries `strategy_id = 'eqdkp_import'`, `strategy_version = <importer version>`,
`source = 'import'`, `actor` = the importing officer, `recorded_at = now`, `effective_at` = the legacy
timestamp, `config_snapshot_json` = fingerprint + pool mapping. `account_id` is the person;
`character_id` records which legacy member earned it — attribution only, never affecting balances.

**Imported batches declare only the `NoFloat` invariant.** They are exempt from the target pool's
strategy invariants. Otherwise importing a decade of history into a pool the officer configured as
`zero_sum` fails `SumZero` on the very first batch — which is both correct (the legacy data is not
zero-sum-balanced) and useless.

**The four-oracle ladder.** For each (member, pool), determine `L_target` — the number the officer's
screen showed — in this order, recording the rung used in `import_reconciliation`:

| Rung | Source | Quality |
|---|---|---|
| 1 | Live `api.php?function=points` (`--oracle-url`) | **Exact.** The actual render path, APA-applied |
| 2 | `<prefix>members.points_apa` | Total **and** attribution ("−142.50 from APA id 3") |
| 3 | Latest `<prefix>member_points` snapshot within its cadence | Includes the per-character breakdown |
| 4 | `<prefix>members.points` | Validates our aggregation; **cannot** explain decay |

**Reconciliation.** `Δ = L_target − B_imported` per (member, pool). If `Δ ≠ 0`, emit **exactly one**
batch per member per pool, `kind = 'import'`, `effective_at` = the cutover instant (after every
historical batch), with a human reason:

```
Legacy reconciliation — EQdkp point decay (APA #3, ~10%/30d).
Imported history totals 1,842.00; EQdkp displayed 1,699.50.
```

It renders in the member's statement as a normal, visible line labelled **"Legacy reconciliation"** —
never hidden, never folded into an opening balance, reversible like any other batch.

**The classifier.** Each nonzero Δ gets exactly one label. This is what converts "import as much as
possible" into a checkable property.

| Label | Test | What the officer is told |
|---|---|---|
| `apa_decay` / `apa_cap` / `apa_start_points` / `apa_onetime` | `points_apa` carries a matching APA delta within tolerance | "Your guild used point decay. Those rules live in a file we did not read — re-enter them under Pools → Decay." |
| `orphan_rows` | Δ equals the sum of attendance/item/adjustment rows skipped for referential integrity | "12 attendance rows pointed at deleted characters. Here they are." |
| `stale_cache` | Our facts agree with the API oracle but not with `members.points` | "EQdkp's cache was stale for this member. Our number is the correct one." |
| `unattributed_adjustment` | Δ equals the sum of adjustments with an unparseable `event_id` | "N adjustments were attributed to no pool in EQdkp. We placed them in `<pool>`." |
| `float_rounding` | `\|Δ\| ≤ 1 centipoint × transaction count` | Cosmetic |
| `twink_mode_mismatch` | Δ vanishes against the other `show_twinks` variant | A wizard question, not an error |
| `unexplained` | none of the above | **Red.** Named, listed, and the report says "do not commit until you understand these" |

**The CI oracle — the single most valuable test in the project.** After importing each fixture:

> `{ members with Δ ≠ 0 }` **==** `{ members predicted by APA detection }` ∪
> `{ members with skipped rows }` ∪ `{ members with unattributed adjustments }`

Any member outside that union fails the build. `import_reconciliation.predicted_by_apa` is the
column that makes the assertion a plain query. *Enforced by:*
`TestImport_FixtureReconciliation_MismatchSetEqualsPredictedSet`, run over every fixture in the
matrix including the hostile one.

**Three reconciliation modes**, chosen in the wizard with the Δ distribution on screen:

| Mode | What it does | When it is right |
|---|---|---|
| `per_member_adjustment` **(default)** | Full history *and* exact balances, with the residual visible and explained | Almost always |
| `opening_balance` | Import raids, attendance, items and adjustments as *records* — so attendance %, loot history and price history all work — but suppress their ledger entries; post one `seed` batch per member equal to `L_target`, funded from the `import_opening` system account | Heavy APA usage, where per-transaction ledger fidelity would be a fiction. Honest, and sometimes right |
| `facts_only` | Import history, emit no residual. Balances differ from legacy by the APA amount; the officer re-enters decay under the new engine, which then owns the truth | A clean-slate guild. The report still shows the exact per-member difference, so nobody is surprised |

**Guardrail.** If any member's `|Δ|` exceeds `max(5% of balance, 100 points)`, `commitImport` refuses
without `--accept-large-residuals`, and the wizard shows the offending members first.

### 6.2 Multi-DKP pools

The EQdkp model — pool = {events} × {item pools}, with a per-mapping `no_attendance` flag — maps
almost one-to-one onto `pool` × `pool_event_type` × `pool_item_pool` (§5.6). Four things bite:

1. **Many-to-many is real.** One event in three pools ⇒ one raid emits three attendance batches. One
   item pool in two pools ⇒ one item debits both. Both are legal, both are usually accidental, and
   both must be reproduced faithfully or balances will not reconcile. Report each and offer a
   previewed `--split-overlaps` remediation that emits visible reversal and re-post batches.
2. **`no_attendance` is load-bearing** (§5.6).
3. **Strategy assignment is a decision, not an import.** Every imported pool is created with
   `strategy_id = 'eqdkp_import'` — a read-only, history-only pseudo-strategy — plus a *pending*
   forward strategy the officer picks from an inferred shortlist: zero-sum presets in use → suggest
   `zero_sum`; APA decay detected → suggest `tick` + `decay` with the detected rate pre-filled; a cap
   detected → suggest `cap` with the detected bounds; start points detected → suggest `start_points`.
   **Never auto-select.** The forward strategy takes effect at cutover, recorded as a
   `pool_config_change` row; imported batches stay under `eqdkp_import` forever.
   *(`cap` and `start_points` ship in 1.0 precisely so this remediation message points at a UI that
   exists.)*
4. **Balance kinds stay separate.** If the officer picks EPGP forward, that is a `migration` batch at
   cutover (DKP → EP, GP = base), not an import transform.

### 6.3 Mains, alts and duplicate characters

**Alt-group construction.** `member_main_id` is `NULL`, `0`, or self-referencing for mains; otherwise
it points at a main. Real data contains chains (A→B→C), cycles (A→B→A) and dangling pointers.

1. Union-find over every `member_id → member_main_id` edge, ignoring self-edges. Each component is
   one alt group.
2. The group's main = the node with the most attendance rows; tiebreak earliest creation date, then
   lowest `member_id`.
3. **Report** every component where the chosen main differs from the node everyone pointed at, and
   every cycle, with the full member list. Do not silently resolve — show it.

**The `show_twinks` fork.** This is the decision most importers get wrong.

| `show_twinks` | Meaning in EQdkp | Import shape | Consequence |
|---|---|---|---|
| `1` | alt points roll into the main | **One person per alt group.** Every entry's `account_id` is that person, `character_id` is the legacy member | EQdkp's with-twinks total equals our person balance exactly; per-character views are a filter on `character_id` |
| `0` | alts hold their own points | **One person per character**, main/alt kept as roster metadata only | Otherwise our person balance matches no number the officer ever saw |

The wizard reads the setting, states the consequence in one sentence, lets the officer override, and
writes the choice into the mapping config; reconciliation then compares against the matching EQdkp
variant. For a `show_twinks = 0` guild that *wants* pooled balances going forward, offer a
post-import, previewed **merge alts into mains** operation that emits a visible `re_attribution`
batch per moved balance. Never during import.

> The source design wrote this batch kind as `RE_ATTRIBUTION`. Canonical §5: enum values are
> lowercase `snake_case` everywhere, so it is `re_attribution`.

**Duplicate character names.** `member_name` is `varchar(30)` with **no unique constraint** under a
case-sensitive collation, so `Bob` and `bob` legitimately coexist and will collide on our
NFKC-casefolded `name_norm`. In order:

1. Same normalised name **and** already in one alt group → nothing to do.
2. Same normalised name, one side inactive with **zero** raids, items and adjustments → absorb the
   empty one, record the alias in name history, report it.
3. Same normalised name, **both have history** → **do not merge.** Import both, disambiguate the
   display name as `Bob #4417`, and queue a side-by-side merge card showing raid counts, date
   ranges, class and balance.
4. **Never auto-merge two characters that both have ledger entries.** Merge is post-import,
   previewed and reversible: it writes name history, re-points `attendance.character_id` and
   `item_award.character_id` (mutable facts), and — only when the two belonged to different persons
   — emits a `re_attribution` batch. It never `UPDATE`s a ledger row; the append-only trigger would
   abort it anyway.

Name history matters beyond de-duplication: on P99 the character name is the only identifier and
renames and rerolls are common. Import seeds name history with every distinct name seen anywhere in
staging, including names that appear only in the legacy log table.

### 6.4 Passwords and the claim flow

**Passwords are never migrated.** A live 2.3 install carries a genuine mixture of bcrypt (three
variants), argon2id/argon2i, a `hash:salt` pre-hash scheme, phpass, ext_des, salted SHA-512, and bare
32-hex **MD5** for accounts that last logged in in 2010. Carrying four-to-seven legacy verifiers
forever, one of which is unsalted MD5, is not a defensible position for a product aimed at volunteers
who cannot reason about "some of your users have weak legacy hashes". Migration is the natural,
defensible moment to force a reset — and it is a one-time cost paid once, by everyone, on a day they
already expect change.

The importer sets `password_hash = NULL`, `must_reset = 1`. **An account in that state cannot
authenticate and holds no session, ever.** *Enforced by:* a unit test asserting the verifier rejects
every candidate password against a NULL hash, and an integration test asserting login is refused.

**One mechanism, three channels.** Claim codes are `invitation` rows with `person_id` pre-bound and
`max_uses = 1` — there is no second mechanism.

| Channel | How it works | Reach |
|---|---|---|
| **Discord with email auto-link** | Member clicks "Log in with Discord". Exact, case-insensitive match of their Discord email against the imported email ⇒ **auto-link, done, zero officer involvement.** Non-matching logins fall through to a "which of these characters are yours?" list an officer approves in one click | Typically claims most of a guild with no distribution step at all *(empirical assumption: most members still use their EQdkp registration email — unverified)* |
| **One-time codes** | Crockford base32, 10 characters, grouped `XXXX-XXXX-XX` (no I/L/O/U, case-insensitive, ~50 bits), stored hashed, single-use, 30-day expiry, scoped to one legacy user. The officer gets a **once-downloadable** CSV, a **Discord-paste block** (one line per member), and a per-user "copy DM text" button | Universal fallback. Always available, no infrastructure |
| **Bulk invite email** | The same code as a link — **offered only when `dkp doctor` verifies SMTP end to end**, never the default | Most guilds have no working SMTP, and a 500-address blast from a fresh VPS IP is both a deliverability catastrophe and indistinguishable from phishing |

Plus **officer claim-on-behalf**: open any imported account, generate a fresh single-use link. That
covers the people who lost the code, have no email, or whose Discord address differs.

Redemption at `/claim`: enter code → set a password **or** link Discord → the account activates with
its characters, rank and role already attached. Rate-limited to 5 attempts per IP per hour with
exponential backoff; the risk is endpoint abuse, not brute-forcing 50 bits.

**Reported explicitly:** users with no email, duplicate emails across two legacy users (both
claimable, both flagged), users with zero linked characters
(`--skip-users-without-characters` exists and defaults off), and disabled accounts.

**The first admin is never imported.** The setup wizard creates the owner account *before* the import
runs. Every imported account lands unclaimed and role-downgraded (§6.5).

### 6.5 ACL downgrade — clamp downward, never round up

EQdkp's ACL is three namespaces (46 admin keys, ~12 user keys, one auto-generated key per page
object), tri-state per group, evaluated as a group union, with per-user overrides, a group-leader
flag that synthesises a scoped permission, and **group id 2 hardcoded to bypass the table entirely**.

The rule is: **map groups to roles, never permissions to permissions, and clamp downward.**

1. Compute each legacy group's effective grant set: union of `Y`, minus `N`. Group 2 = everything.
2. Translate through a curated legacy-key → `dkp.permission` table. Anything without an entry is
   **dropped and reported** — bridge management, extension management, menu and table managers, SMS,
   maintenance, styles, and every page-object key for a page that does not exist here.
3. Map the group to **the lowest built-in role whose permission set is a superset of the mapped
   grants** (`member` ⊂ `raid_leader` ⊂ `officer` ⊂ `admin`). If no built-in role is a superset —
   the group had an odd mix like backup-only — create a custom role `Legacy: <group name>` containing
   **only** the mapped permissions. **Never round up to the nearest built-in.**
4. **Per-user grants are never applied.** A direct `Y` becomes a report line: *"Zibaxia held a direct
   grant of item-delete outside any group — not migrated. Add them to Officer, or create a custom
   role."* A per-user **`N` is honoured**, by removing that person from the role that would have
   granted it. Denies are always safe to honour; grants never are.
5. **Legacy super-admins map to `admin` with zero members auto-assigned.** Their accounts are
   unclaimed; auto-granting admin to an account whose only credential is a code sitting in a CSV is a
   privilege-escalation hole. They appear in the report as "these 3 accounts were EQdkp
   super-admins; promote them once they claim", with a one-click promote after each claim.
6. Group-leader flags become a scoped role assignment on that raid group if scoped assignment exists;
   otherwise dropped and reported.
7. Site-wide API keys and per-user exchange keys are **never imported** (§5.4). EQdkp's `api_key`
   impersonates the first super-admin; the entire token design here exists to make that impossible.

**Deliverable: the permissions-downgrade artifact** — permanent, linked, rendered **and** JSON:
legacy group → target role with member counts; per-permission disposition (`granted`,
`dropped_no_equivalent`, `dropped_feature_absent`, `dropped_per_user_override`, `denied_downward`);
and **a per-person list of everyone whose effective capability shrank, naming the specific
capability** — because "you can no longer delete raids" is a sentence an officer needs to be able to
read out loud.

*Enforced by:* a test asserting that for every fixture, the imported role assignment grants a strict
subset of the legacy effective grant set, after mapping. Upward drift fails the build.

Visibility settings (hidden ranks, special members, inactive handling) are roster configuration, not
ACL, and map directly.

### 6.6 Encoding and mojibake repair

**Read bytes; decide the encoding ourselves.** Connect with `SET NAMES binary`, or dump with
`--default-character-set=binary --hex-blob --skip-set-charset`. Read the **actual** per-column charset
and collation from `information_schema.COLUMNS` — never trust the install schema, because EQdkp's
database layer hardcodes a `utf8` connection charset while the columns themselves may be anything,
especially on installs that began life as EQdkp v1 with no declared charset at all.

**Per-value pipeline. The order is load-bearing:**

```
raw bytes
  1. decode using the column's declared charset (latin1 | utf8mb3 | utf8mb4)
  2. mojibake repair                (per value, iterated up to 3×)
  3. unsanitise, exactly one pass   (&#34;→" ; &#39;/&#039;→' ; &lt;→< ; &gt;→> ; &amp;→&  LAST)
  4. NFC normalise
  5. trim
  → decoded text + a decode note
```

Step 3 comes *after* step 2 because `&#39;` is pure ASCII and unaffected by repair, whereas repairing
after entity-decoding risks re-mangling a legitimately decoded ampersand sequence. Step 3 is exactly
one pass — double-decoding turns `&amp;lt;` into `<`.

**The mojibake predicate**, stated so it is testable. Accept the repair
`s → s.encode('latin-1', strict).decode('utf-8', strict)` **iff** both operations succeed **and** the
count of code points in U+00C0–U+00FF strictly decreases **and** the result contains no C0 controls
**and** the result's UTF-8 byte length decreases. Otherwise keep the original. Iterate up to three
times — twice-migrated installs really do carry triple-encoded text — stopping when the predicate
stops improving, and record the pass count. Log every repair as `(table, pk, column, before, after)`
so the officer can eyeball twenty of them and say "yes, that's Grüße".

**Why this matters more than it sounds.** EQdkp runs every string through PHP's string-sanitising
filter *before* the database, so `Trakanon's Tooth` is physically stored as `Trakanon&#39;s Tooth`.
Skip step 3 and every P99 item with an apostrophe imports wrong — which is most of the good ones.

**Unrecoverable damage is reported, not hidden.** Three-byte `utf8` truncated any 4-byte character at
insert time in the original install. Detect trailing partial sequences and report: *"N values were
truncated by MySQL's 3-byte utf8 in your original install; those characters cannot be recovered."*

**PHP-serialize parsing.** `s:<len>:` lengths are **bytes, not characters** — parse over `[]byte` and
slice by byte length, never rune count, or any already-mojibaked value blows up the parser. Reject
object payloads as corrupt. If a declared length overruns the buffer or the closing sequence is
misplaced, record `unparseable_serialized` and move on — most serialized columns are caches we
discard anyway, which caps the blast radius. Implementation is a ~120-line in-tree byte-level reader
for the four shapes we need, with a third-party reader used **only as a differential-test oracle in
CI** (a test dependency, not a runtime one).

*Enforced by:* a mojibake golden set of ~40 hand-curated before/after pairs including German, French,
apostrophe-bearing P99 item names, triple-encoded values, and values that **must be left alone** — the
false-positive guard is the half that matters.

### 6.7 Timestamps, and unknown items

**Do not reinterpret timestamps.** EQdkp sets the PHP default timezone to UTC and stores UTC unix
integers. The correct transform is `epoch_seconds × 1_000_000 → Micros`, full stop. Three real traps:

1. **`0` means "never" → NULL.** Not 1970-01-01. A member "created" in 1970 looks like a bug forever.
2. **Out-of-range values** — before 1999-03-16 (EverQuest's launch) or after `now + 1 year` →
   quarantine with a report line, never a silent clamp.
3. **Genuinely local timestamps**, in rows written by very old installs or by hand. Detect by
   histogramming `raid_date mod 86400` and present the histogram in the wizard: *"Your raids cluster
   at 19:00–23:00 **UTC**. Guilds normally raid in the evening **local** time. Is your guild in UTC,
   or were these stored as local time?"* — with `treat as UTC (recommended)` /
   `treat as <config timezone>` / `treat as <picked zone>`. The answer goes into the mapping config,
   so a re-run is deterministic.

**If local reinterpretation is chosen**, convert with the IANA zone using the *historic* rules for
each instant (`time.LoadLocation` + `time.Date` in that zone handles the 2007 US rule change
correctly; a naive fixed-offset conversion silently shifts three years of raids by an hour).
Ambiguous times in the fall-back hour resolve to the **first** occurrence; non-existent
spring-forward times shift forward by the gap. Every one of both is logged.

Per-user timezone carries onto the imported account so a member's first login renders in their own
time. Per-event calendar timezones carry over directly.

**Unknown, renamed and duplicate items.** Resolution order per name: exact `name_norm` → alias table
→ external game id (when the guild used a recognised provider) → trigram + Levenshtein candidates
above threshold, **officer-confirmed** → create as provisional.

**Never drop an award because the item cannot be resolved.** Buyer, price, raid, date and pool are
all still correct; only the catalogue link is soft. The award is committed against a provisional item
and one `reconciliation_item` is opened per distinct unresolved name — not one per occurrence.

- **Auto-collapse** names that are identical *post*-unsanitise and *post*-repair (three spellings of
  `Trakanon's Tooth`) into one item with the others as aliases. That is not fuzzy matching; they are
  the same string.
- **Do not auto-collapse** names within edit distance 2 that differ in content (`Cloak of Flames` vs
  `Cloak of Flame`). They become a "possible duplicate items" section with one-click merge.
- Item merge is a **metadata** operation — re-point award rows — never a ledger operation, because
  the price already lives on the award.
- Backtick and apostrophe names (``Vulak`Aerr``, ``J`Boots``) rely on `name_norm` stripping `'`,
  `` ` `` and `-` (canonical §8). The raw string is always kept for display.

### 6.8 Assets and BBCode sanitisation on import

**There is no media table in EQdkp.** References are path strings in member pictures, article preview
images, rank and role icons, plus inline markup in article text and raid notes. The files live under
the install's `files/` and `images/` directories.

Three source modes, offered in the wizard:

| Mode | Behaviour |
|---|---|
| **Filesystem** | A mounted path, or an uploaded zip of `files/` + `images/`. Preferred |
| **HTTP fetch from the live site** | **Off by default.** Rate-limited, size-capped, content-type-sniffed, SSRF-guarded: reject private, link-local and loopback targets, reject cross-origin redirects, resolve DNS once and pin the IP. Constructed only through `internal/net/safehttp` |
| **Neither** | Retain the original path as text, mark the reference unresolved, report the count |

Storage is the existing content-addressed artifact store at `/data/artifacts/<hh>/<sha256>`, which
dedupes for free (guild avatars repeat heavily), with a legacy-path → sha256 map for provenance.
Limits: 10 MB per file, 500 MB total (`--max-assets-bytes` to raise), allowlist png/jpeg/gif/webp by
**sniffed** type. **Re-encode every image through a decoder rather than storing the original bytes** —
that strips EXIF and kills polyglot payloads in one step. SVG is never preserved (it is a scripting
vector): rasterise, or drop with a report line. Gravatar-mode avatars keep the email hash and fetch
nothing.

**BBCode and HTML.** EQdkp stores raw markup and renders at display time, which is a documented XSS
surface. **We sanitise on import, and again on render.**

```
stored bytes
  → unsanitise (entity decode, once)
  → strict BBCode parse, closed tag set only:
        b i u s url img quote code list * size color center left right table
  → HTML emit
  → allowlist sanitiser (bluemonday UGC, tightened):
        no style, no on*, no <script|iframe|object|embed|form>
        href ∈ {http, https, mailto};  img src ∈ {our artifact store, allowlisted hosts}
        rel="noopener nofollow ugc", target stripped
  → store BOTH body_source (cleaned BBCode, editable) and body_html (rendered)
```

**Unknown or unclosed tags are escaped and preserved as literal text**, never passed through. A raid
note that displays a literal `[color=#f00]` is a cosmetic bug; a raid note that executes is a breach.
Editor item tags convert to links to our item pages when resolvable, plain text otherwise. oEmbed
URLs are **not** fetched at import — they become plain links.

Storing `body_source` as well as `body_html` is what makes the Phase 8 article editor able to open an
imported article without a lossy HTML→BBCode round trip.

*Enforced by:* an XSS corpus driven through the pipeline, asserting no `on*` attribute, no `script`,
no `javascript:` URL, and that unknown tags survive as escaped literals.

---

## 7. The verification report

This is the artifact that earns the guild's trust, so it is designed rather than emitted
incidentally.

**Properties.**

- Produced **identically** by the dry run and by the commit. **Any divergence between the two is
  itself an alarm** and is surfaced as such.
- Stored permanently as an immutable artifact, linked forever from guild settings, available as a
  rendered page **and** as machine-readable JSON.
- **Publicly linkable, read-only**, so ordinary members can audit it without an account.
- **Every number links to the rows behind it.** A report you cannot drill into is a report nobody
  believes.

**Header.** One line, one colour: *"Balances reconcile for 78 of 81 members across 2 pools. 3 members
require attention."* Then: source fingerprint, prefix, detected version, ingest mode used
(container / dump reader / live / export), importer version, config hash, run hash, wall time.

| § | Section | Contents |
|---|---|---|
| 1 | **Row parity** | Per source table: source rows → staged → loaded → skipped, with skip reasons broken out. Plus the plugin-table list **with row counts** |
| 2 | **Roster** | Legacy members / active / on hidden ranks → characters imported, persons created, alt groups formed, merges auto-applied, merges queued, duplicate normalised names, unknown classes, era-impossible races |
| 3 | **Per-member balance delta** *(the centrepiece)* | One row per (character-or-person, pool): legacy displayed, oracle rung used, imported computed, reconciliation applied, final, **Δ**, classification. Sorted by \|Δ\| descending, downloadable as CSV. Two headline numbers: `Σ\|Δ_final\|` — **must be 0** in `per_member_adjustment` mode, by construction — and `Σ\|Δ_pre-reconciliation\|` with its classification histogram. Any `unexplained` row renders red and blocks commit without an explicit override |
| 4 | **Totals** | Per pool: total earned / spent / adjusted, legacy vs imported, absolute and percentage. **A total that matches while individuals do not is the signature of an attribution bug** — which is exactly why both levels are shown |
| 5 | **Raids and attendance** | Sessions, ticks, connected-attendance components formed, asymmetric links normalised, raids per pool and per event, distinct raid days, date range. Then the **attendance spot-check**: for the top 20 members by raid count, legacy 30/60/90/lifetime percentages vs ours, with the delta. This is the second number an officer checks, right after their own balance |
| 6 | **Items and loot** | Awards imported; distinct names resolved exactly / by alias / by confirmed fuzzy match / unresolved. The **unmatched-items list** — name, purchase count, total spent, first and last seen, suggested matches — sorted by spend, downloadable, actionable straight from the reconciliation queue. Multi-buyer groups preserved; inconsistent groups flagged |
| 7 | **Adjustments** | Count, groups preserved, unattributable count and total value, zero-value no-ops |
| 8 | **Skipped rows** | Every skip with a reason code and its source primary key. Counts on screen, full list downloadable. **Nothing is skipped without appearing here** |
| 9 | **Accounts** | Users imported; with email / without / duplicate email; disabled; zero-character; claim codes generated; which claim channels are available (Discord configured? SMTP verified?) |
| 10 | **Permissions** | The full downgrade table, the per-person "capability shrank" list naming the specific capability, and the "these N accounts were super-admins — promote after they claim" list |
| 11 | **Encoding** | Values repaired with a 20-row before/after sample, values still suspicious, values truncated by three-byte utf8, unparseable serialized blobs |
| 12 | **Assets and content** | Assets found / copied / deduped / rejected (with reason) / unresolved. HTML tags stripped by type, links rewritten, external images left unresolved, and a "ten largest articles, before and after" sample so the officer can see what sanitisation actually did |
| 13 | **Precision** | Rows where `round(v × 100)` differed on round-trip, max drift, total drift |
| 14 | **Ledger integrity** | `dkp verify-ledger` output: batch hash chain valid; `Σ ledger_entry.amount_cp` equals the sum of per-account balances; a snapshot rebuilt from zero equals the incrementally-maintained values; and **the append-only trigger fires on a probe `UPDATE`**. This section asserts the *guardrails*, not just the data — it is the answer to "how do I know you didn't just write the numbers I wanted to see" |
| 15 | **Determinism** | Run hash, config hash, source fingerprint hash, and confirmation that re-running with these inputs reproduces this report |

### 7.1 The screen that actually persuades people

Not the report — the **member statement**. After the import, a member's ledger page shows every
legacy raid, item, adjustment and the labelled legacy reconciliation, with a running balance, in date
order, **ending on exactly the number they saw on the old site.** Send every member their own
statement link along with their claim code. That is what converts an import from an IT event into a
trust event.

---

## 8. The wizard and the mapping config

| Step | Screen | What it writes |
|---|---|---|
| 1 Connect | Pick a mode. Drop a file, paste a DSN, or paste an export. Prefix picker when several installs share a database | the source record |
| 2 Analyse | Fingerprint (version, prefix, engine, per-table charset and collation), capability flags, row counts, plugin tables **with row counts**, the four detected oracles, the APA verdict, the timezone histogram | `import.dkpmap.json` |
| 3 Configure | Pool → forward strategy, alt mode, reconciliation mode, timezone answer, asset source, ACL role map preview, item alias seeds, skip lists | edits to the mapping config |
| 4 Preview | Row-level: "we will create 81 characters, 47 persons, 1,204 raid sessions, 3,318 ticks, 9,441 awards…", each number drillable. Side-by-side for anything ambiguous: merge candidates, ACL downgrades, mojibake repairs | nothing |
| 5 Dry run | Phase 1 plus a fully simulated phase 2 against staging. Produces the **complete verification report** with zero domain writes. **This is the default** | `stg_*`, a stored report artifact |
| 6 Import | `commitImport`. Snapshot first (§9.2). River job, per-instance lock, progress over SSE. Resumable | domain rows + `import_id_map` |
| 7 Verify | Post-commit report, diffed against the dry-run report | the permanent report artifact |
| 8 Claim | Claim CSV / Discord block / email, plus a live claim-progress counter | `invitation` rows |
| 9 Delta | Repeatable, throughout the parallel run (§10) | delta reports |
| 10 Cut over | The checklist (§10.2) | pools flipped `mirror → live` |

**The mapping config** — one artifact, generated by Analyse, editable in the UI, downloadable and
re-uploadable, versioned, stored with the run:

```jsonc
{
  "config_version": 1,
  "source": { "system": "eqdkp_plus", "prefix": "eqdkp20_", "plus_version": "2.3.39",
              "import_source_id": "sha256:…", "fingerprint_hash": "sha256:…" },
  "capabilities": { "has_member_points": true, "has_connected_attendance": true,
                    "groups_raid_members_key": "member_id" },
  "decisions": {
    "twink_mode": "pooled",                    // pooled | per_character
    "reconcile_mode": "per_member_adjustment", // | opening_balance | facts_only
    "timestamps": "utc",                       // | local:America/New_York
    "assets": "zip",                           // | fs:/path | http | none
    "import_ip_addresses": false
  },
  "pools": { "1": { "name": "Main", "forward_strategy": "tick",
                    "forward_config": { "tick_value_cp": 10000,
                                        "decay": { "rate_bp": 1000, "cadence": "30d" } } } },
  "roles": { "2": "admin_no_members", "5": "officer", "4": "member" },
  "overrides": {
    "characters": { "4417": { "name": "Bob (Warrior)", "person": "merge:4102" } },
    "items":      { "Cloak of Flame": "alias_of:Cloak of Flames" },
    "skip_rows":  { "raid_attendees": ["11204", "11205"] }
  },
  "tables": { "eqdkp20_shoutbox_messages": "ignore" }
}
```

The table map itself is **typed Go data, not YAML**: an unknown column is a log line, a missing
*optional* column is a default, a missing *required* column aborts naming the table and the column.
One artifact is forward-compatible with 2.3.40+ and backward-compatible with 2.0.

---

## 9. Idempotency, resumability, rollback

### 9.1 Idempotency — two independent mechanisms, both needed

1. **`import_id_map(import_run_id, entity, legacy_id) → new_id`**, with
   `ux_idmap_compat(entity, legacy_id)`. Every created domain object is registered. A re-run is an
   upsert-or-skip, and the persisted map is also what makes the compat shim's hard-coded legacy
   `member_id`s resolve forever.
2. **`ledger_batch.source_ref`**, unique per `(pool_id, source_ref)`. Re-posting an existing fact is a
   no-op that returns the existing batch.

`import_source_id` is deliberately **not** derived from the connection string. It is
`sha256(dbname ‖ prefix ‖ min(raid_id) ‖ min(raid_date) ‖ min(member_id) ‖ member_creation_date)` — a
content fingerprint that survives a dump → container → different-host round trip. That is what lets
an officer dry-run from a zip on Monday, delta-import from a live DSN on Friday, and have both
recognise the same source.

*Enforced by:* importing a fixture twice must produce zero new rows, zero new batches, and an
identical report modulo run ids.

### 9.2 Resumability

`import_run.phase` moves `fingerprint → staging → transform → reconcile → done`, with `failed` and
`rolled_back` as terminals. Within `transform`, every loader is chunked at ≤ 2,000 rows and stamps
`stg_*.loaded_at` **in the same transaction as the domain rows** — so a crash at 3 a.m. leaves a run
mid-table and `dkp import resume <run_id>` continues from the first unstamped staging row. The job
lives in the same SQLite file the officer already backs up and is visible at `/api/v1/admin/jobs`, so
a failed overnight import shows up in the admin UI rather than in a log nobody reads.

### 9.3 Rollback — two tiers

**Tier 1: snapshot restore. This is the real rollback.** Immediately before the commit writes
anything, the importer takes the same `VACUUM INTO` + zstd snapshot the upgrade path uses and stores
it at `/data/backups/pre-import-<run>.db.zst`. The admin UI then shows a literal **"Undo this
import"** button with a live counter: *"Undo available — 0 changes since import."* Restoring is
exact, fast and requires no reasoning about the ledger. Once post-import writes exist the button
warns; past a threshold it requires typing the run id. This is the rollback that will actually get
used, and it is about forty lines.

**Tier 2: logical revert**, for after the instance has been used. `revertImport`:

- emits **reversal batches** for every imported batch — aggregated one per (pool, account) for
  compactness, each referencing the reversed set — because the ledger is append-only by database
  trigger and nothing may delete from it;
- soft-deletes imported roster, raid, item and award rows that have **no** post-import references;
- **refuses** to touch anything that does have references, listing each one with its referrer;
- writes a full audit entry and produces a revert report in the same format as the import report.

Both tiers are surfaced in the wizard *before* the officer clicks commit, so "what if it goes wrong"
has a visible answer. That, more than any feature, is what gets the button clicked.

---

## 10. Parallel run, delta re-import, cutover

### 10.1 The mechanism that keeps a parallel run honest

For N weeks — 2 to 4 is typical, one full loot cycle is the right rule — EQdkp stays the system of
record while DKP runs alongside, repeatedly delta-refreshed, so officers use it for real without
risking the ledger.

**Imported pools are created in `write_mode = 'mirror'`.** In mirror mode the only writer permitted
to post ledger batches to that pool is the importer; officer and bot writes are rejected with `409`
and code `pool_is_mirroring`, naming the cutover step. Without this you get two writers and
guaranteed divergence, and the parallel run becomes worse than no parallel run. At cutover the
officer flips `mirror → live` — one audited API call — and the same flip revokes the importer's write
path.

**A delta re-import does:**

1. **Re-fingerprint.** Assert `import_source_id` matches; **refuse loudly if it does not** — that is
   a different database, and importing it would splice two histories.
2. **Re-stage incrementally.** Append-heavy tables (raids, attendees, items, adjustments, logs)
   `WHERE pk > last_max_pk OR date_col > last_max_date`; small mutable tables (members, ranks, pools,
   item pools, events, users, member-user links, auth tables) re-staged in full, because they are
   cheap and they **change in place**.
3. **Three-way diff** each staged row against the stored `row_hash`:
   - **new** → insert, exactly as a first import would;
   - **changed** → for non-ledger attributes (name, rank, note) update in place; for **ledger facts**
     (raid value, item value, adjustment value, the attendee set) emit a **reversal + re-post pair**,
     never an update — the ledger is append-only, and the old number needs to stay visible in the
     statement;
   - **deleted** (source pk vanished) → emit a reversal batch and mark the domain object
     `legacy_deleted`. Never hard-delete.
4. **Re-run reconciliation.** The per-member residual is itself reversed and re-posted, so there is
   always exactly one live residual per (member, pool) and its history shows how it moved.
5. **Emit a delta report** in the same format, showing only what changed plus the same balance-delta
   table — which should by then be nearly empty.

**Why it cannot duplicate:** `source_ref` uniqueness makes re-posting an unchanged fact a no-op;
`row_hash` makes changed facts detectable; reversal-only correction means nothing is rewritten. The
one genuinely unhandleable case is a *reused* primary key, possible only if the guild truncated and
reseeded a table; detect it as a `row_hash` mismatch on an **old** pk carrying an **older** date, and
quarantine rather than guess.

Deltas also cover the two non-cutover cases that occur in practice: "we ran EQdkp for two more weeks
after you imported", and "the importer had a bug, we fixed it, re-run" — the second being why
resumability and idempotency are worth building even for a one-shot migration.

### 10.2 Cutover checklist

Shipped as a literal checklist in the UI, one checkbox per line.

1. Announce the cutover date and post every member's statement link.
2. Freeze EQdkp writes — maintenance mode, or revoke raid/item/adjustment-add from every group.
3. Run the final delta import.
4. Verification report green: balance-delta table empty, §14 integrity all passing.
5. Flip imported pools `mirror → live`.
6. **Point the bots at the compat shim.** Existing bots keep working unchanged, because
   `/api/compat/eqdkp/api.php` speaks their protocol and legacy `member_id`s still resolve through
   `import_id_map`. This is the step that makes cutover a non-event for everyone except the officers.
7. Set EQdkp read-only for 90 days as a reference, then archive.
8. **Archive the final dump into the DKP artifact store**, so the legacy source becomes part of the
   DKP backup rather than a separate thing to remember.
9. Rotate and revoke the stored legacy credentials.

---

## 11. Fixtures and CI oracles

**Fixtures are real EQdkp Plus installs**, built by running the actual PHP installers in Docker and
published as OCI artifacts so contributors pull rather than rebuild. The procedure is
`.claude/skills/refresh-eqdkp-fixture`. Matrix: **2.0.5, 2.1.5, 2.2.27, 2.3.39**, plus one
hand-crafted **hostile** fixture carrying latin1 double-encoding, duplicate attendee rows, orphaned
items, a `member_main_id` cycle, a bare MD5 password, an unknown plugin table, and a half-applied
2.3.x update.

Build 2.3.39 **first, on day one**. If a decade-old PHP installer will not run in Docker, the whole
fixture strategy needs a different source and the importer phase is materially longer — that is a
schedule risk worth discovering in week one rather than month three.

| Test | Assertion |
|---|---|
| **Reconciliation oracle** | `{Δ ≠ 0}` == `{APA-predicted}` ∪ `{skipped-row members}` ∪ `{unattributed-adjustment members}`. Anything outside fails the build |
| **Golden verification report** | One committed report per fixture, CODEOWNERS-protected, `-update` banned in CI. Report drift becomes a review item, never a silent change |
| **Idempotency** | Import twice → zero new rows, zero new batches, identical report modulo run ids |
| **Delta** | Import; in the source add a raid, change an item value, delete an adjustment; delta-import → exactly one insert, one reversal + re-post pair, one reversal |
| **Rollback tier 1** | Import → snapshot restore → database byte-identical to pre-import |
| **Rollback tier 2** | Import → write post-import data → revert → imported balances net to zero via reversals; referenced objects refused and listed |
| **Determinism** | Same config + same source ⇒ same run hash ⇒ same report |
| **Read-only enforcement** | The MySQL wrapper rejects `INSERT`; the code path issues only `SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN` |
| **Dump-reader parity** | Same fixture via container and via the in-process reader ⇒ identical `stg_*` tables |
| **Mojibake golden set** | ~40 curated before/after pairs, including values that must be left alone |
| **PHP-serialize differential** | Our reader vs a third-party reader over a corpus of real serialized blobs, including byte-length edge cases |
| **ACL monotonicity** | Imported role grants are a subset of the legacy effective grant set (§6.5) |
| **Performance budget** | A 500k-row synthetic guild imports in under 5 minutes wall clock, hard ceiling. **This is the tripwire for ORM-per-row, a 40× regression and the single most likely agent-introduced defect in this package** |
| **Write fairness** | A tick submission completes within the write-latency SLO while an import runs |
| **Sanitiser** | XSS corpus through the BBCode→HTML pipeline (§6.8) |

---

## 12. Other import paths, and the export story

### 12.1 Non-EQdkp sources, ranked by how many P99 guilds they unblock

| Rank | Source | Ships | Note |
|---|---|---|---|
| 1 | **Bulk EQ raid dumps and guild dumps** | 1.0 — and this is core product, not an import path | Drop a folder of 400 `RaidRoster-*.txt` files; each becomes an artifact, content-hash deduped; filename timestamps cluster into sessions (gap > 90 min starts a new one, officer-adjustable); preview; commit. Retro-builds attendance history from files the officer already has on disk, which nothing else does |
| 2 | **CSV of members + balances** | 1.0 | `members.csv` and `balances.csv`, header-mapping UI, preview, same report shape. Imports as roster plus one `seed` batch per member labelled "Opening balance (imported from CSV)". This is the path for the plurality of P99 guilds |
| 3 | **Spreadsheet exports** | 1.0 as file upload | Three adapters: standings sheet; **raid grid** (one row per player, one column per raid, header row of dates — the valuable one, because it reconstructs *attendance history*); tick-tab workbooks. Accept `.xlsx` directly, because "export to CSV" loses the tabs. Design rule: **do not be clever** — show the parsed grid and let the officer fix the header row by hand |
| 4 | **EQdkp XML/JSON data export** | 1.0, explicitly labelled low-fidelity | Also usable as a reconciliation oracle alongside a database import |
| 5 | **Raidlog XML** | post-1.0 | The one legacy format *richer* than EQdkp's own database: per-member join/leave times and a standby flag, which our tick/standby model can actually represent |
| 6 | **OpenDKP** | post-1.0 | MIT, EQ-native, already has the raid/tick model |
| 7 | **EQdkp v1** | **deliberately unsupported** | v1 joins attendance and items on a name column under a collation-sensitive comparison, with stored balance columns and no pools; every rename silently orphans history. The supported answer is: run EQdkp Plus's own 1.x → 2.x upgrader first, then import from 2.x |

### 12.2 Export

Lock-in is the reason this project exists, so the export is a **first-run** feature documented before
the officer has invested anything — not an admin-panel afterthought.

`dkp export` produces one zip: a `manifest.json` (schema version, product version, generated_at,
per-file row counts and sha256); one `.ndjson` per entity covering the *complete* domain — batches
and entries with the hash chain intact, raids, ticks, attendance, awards, items, aliases,
adjustments, pools with strategy ids and config snapshots, persons, characters, name and rank
history, claims, ranks, roles, permissions, users (**no password hashes**), calendar events, signups,
disputes, audit log, webhooks, service accounts (no secrets), saved views, and every import report;
an `artifacts/` tree; a JSON Schema per ndjson file so a third party can write a reader without
asking us; a README on reading the export without our software; and **plain CSVs of the four things
a human actually wants in a spreadsheet** — standings, full ledger, attendance matrix, loot history.

`dkp import --format=dkp-export` round-trips the zip into a clean instance. **CI asserts round-trip
equality**: export → fresh database → import → export → byte-identical after canonicalisation. That
one integration test *is* the no-lock-in claim, made executable.

And the loudest point: **the database is one file.** `/data/dkp.db` is a complete, standard-format,
tool-readable copy of everything, which the officer already has and already backs up.

---

## 13. What this specification requires from other components

Cross-document dependencies. Each is a thing the importer writes into and which must exist before the
corresponding phase lands.

| Requirement | Owner | Needed by |
|---|---|---|
| `stg_*` staging tables carry `loaded_at` for chunk-cursor resumability (§4) | schema | Phase 5 |
| `pool.write_mode TEXT CHECK (write_mode IN ('live','mirror'))` (§10.1) | schema | Phase 5 |
| Error code `pool_is_mirroring` in the closed enum, with a docs page (§3) | `internal/api/errors.go` | Phase 5 |
| `character_claim.method` gains `legacy_import` (§5.3) | enum catalogue | Phase 5 |
| `ledger_batch.kind = 'import'` is the residual; `re_attribution` is lowercase (§6.1, §6.3) | enum catalogue | Phase 5 |
| `cap` and `start_points` strategies exist, so the APA remediation message points at a UI that can express what the guild had (§6.2) | `internal/strategy` | Phase 5 |
| `internal/net/safehttp` exists before HTTP asset fetch (§6.8) | platform | Phase 4, not Phase 8 |
| `internal/richtext` exists before imported BBCode is rendered (§6.8) | platform | Phase 4, not Phase 8 |
| `article`, `article_category`, `article_comment`, media-library tables — content is staged in Phase 5 and **loaded** when these exist (§5.11) | schema, Phase 8 | Phase 8 |
| `/search` covering characters, items and articles — imported unresolved items are unfindable without it | `internal/api` | Phase 6 |

The content dependency is why `--keep-staging` is not optional for a guild that imports before Phase
7 ships: the staged article, comment and category rows are what the Phase 8 loader replays, so nobody
has to re-import to get their news archive.

---

## 14. Open risks and unverified claims

- **Intra-2.3.x column drift.** Only branch tips were diffed; individual 2.3.x releases may add
  columns not listed here. Mitigated structurally by capability-detection over version-detection, but
  the first real guild will find something. *(unverified)*
- **The `groups_raid_members` key column** is defined twice in the 2.3 schema. The importer
  introspects and aborts naming both candidates rather than guessing. *(verified as ambiguous)*
- **Calendar-event `extension` internal keys** — confirmed serialized, key set never enumerated.
  Imported as opaque JSON, never interpreted. *(unverified)*
- **Article-category permission `-1` semantics** — unknown, therefore dropped entirely rather than
  guessed. *(unverified)*
- **Plugin table schemas** other than the guild bank — table names follow a known pattern, column
  lists unknown. Reported with row counts, never imported. *(unverified)*
- **The API oracle assumes 2.3-era `api.php` semantics** (`?atoken=`, `Authorization: <token>` without
  `Bearer`, HTTP 200 on errors, a `{status:1|0}` envelope). Pre-2.2 installs used a challenge login
  instead; those degrade to the `points_apa` rung automatically. *(verified for 2.3; older paths
  unverified)*
- **APA rules genuinely live only on disk.** This is the central premise of the reconciliation
  classifier. **Verify it in week one**: install 2.3.39 once and inspect that file and the config
  rows. If any APA state *is* in the database, the classifier gets much stronger and "re-enter your
  rules" may be avoidable entirely. Reading `apatab.php` is a named post-1.0 option — it is the
  guild's own configuration data, not EQdkp source — but it is not in a database backup, which is
  what officers actually have, so it does not help the primary ingest path. *(unverified)*
- **Discord email-match claim reach.** The estimate that most members still use their EQdkp
  registration email is an *empirical assumption* with no measurement behind it. The claim-code
  channel exists precisely because it is unproven. *(unverified)*
- **Licence exposure.** EQdkp Plus core is AGPL-3.0 and its game modules are CC BY-NC-SA 3.0. The
  importer must contain no transcribed DDL, no ported PHP and no copied class or race tables — those
  ship as our own literals. This is the one legal exposure in the project that could actually hurt,
  and it is concentrated in exactly this package. *(decision, enforced by the CI grep in canonical
  §15 — which must scope to Go sources, since design and migration docs necessarily name legacy
  tables)*
