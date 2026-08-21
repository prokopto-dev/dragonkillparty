// Dragon Kill Party — the single source of schema truth.
//
// Atlas reads this file and generates the migrations in db/migrations-sqlite/; goose applies them
// from inside the binary. See docs/adr/0008-atlas-authors-goose-applies.md. Never hand-write a
// migration: change this file and run `make migration NAME=<snake_case>`.
//
// Rules that apply to every table here, from docs/design/00-canonical-conventions.md §8:
//
//   strict = true          on every table. STRICT permits only INT, INTEGER, REAL, TEXT, BLOB and
//                          ANY — BIGINT, BOOLEAN, DATETIME, NUMERIC and DECIMAL are ILLEGAL.
//   id        text         ULID, 26 characters, generated in Go
//   *_at      integer      Micros — int64 Unix microseconds, UTC
//   *_cp      integer      centipoints. Never real, never a decimal string
//   *_bp      integer      basis points, 10000 = 100%
//   *_json    text         validated on write, NEVER queried into
//   enums     text + check (x IN ('a','b')), lowercase snake_case
//   booleans  integer NOT NULL check (x IN (0,1))
//
// Tables are SINGULAR. There is no `guild_id` column and there will not be one: this is strictly
// one guild per instance (canonical §9, ADR-0004), and `lint / repo` greps this file for it.

schema "main" {
}

// dkp_meta — single-row instance state, keyed by name.
//
// The shape is docs/design/01-domain-model.md:1283: chain heads (`ledger_head:<pool_id>`,
// `audit_head`) and the schema version live here as rows, not as columns. Adding a column per
// fact would mean a 12-step table rebuild every time a new head appeared, and on SQLite a rebuild
// is the single most dangerous migration shape there is.
//
// docs/design/06-cicd-and-release.md:512 writes the boot sequence's first step as "read
// `dkp_meta.schema_version`", which reads as a column. The domain model is the schema authority
// (01-domain-model.md:7), so `schema_version` is a KEY in this table, and that document's phrasing
// is a bug reported alongside this change.
//
// This table is NOT append-only and carries no trigger — goose's own `goose_db_version` table is
// the applied-migration bookkeeping, and `dkp_meta` rows are updated in place by design.
table "dkp_meta" {
  schema = schema.main

  // The state's name. Not an id: these keys are stable, meaningful and written by hand in code
  // (`schema_version`), so a ULID would add a lookup and remove the readability that makes
  // `sqlite3 dkp.db` a usable debugging tool for an officer at 1 a.m.
  column "key" {
    null = false
    type = text
  }

  // Always text. Callers parse it. A value column typed per-key is not expressible, and a REAL or
  // NUMERIC column here would be the first float in a database whose central invariant is that
  // there are none.
  column "value" {
    null = false
    type = text
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.key]
  }

  // WITHOUT ROWID: the primary key IS the row. A handful of rows read on every boot, always by
  // key, with no integer id anyone ever uses — the rowid indirection is pure cost here.
  without_rowid = true
  strict        = true
}

// guild — the single-row instance identity and its officer-editable settings.
//
// There is exactly ONE guild per instance (canonical §9, ADR-0004) and no `guild_id` column
// anywhere in the schema. This table holds the identity of that guild — its name, tag and timezone
// — and the handful of settings a job or a query reads rather than an SPA renders once.
//
// The singleton is enforced in the schema, not by convention: `id INTEGER PRIMARY KEY CHECK
// (id = 1)` (docs/design/01-domain-model.md:42-43). This is the ONE table whose id is an integer
// rather than a ULID, and deliberately so — a ULID keys a row that is one of many, and there is
// only ever one guild. A second INSERT fails the CHECK rather than silently creating a second
// guild that every scope-free query would then have to disambiguate.
//
// TWELVE COLUMNS, exactly (docs/development/phase-0-pr5-decisions.md §U2). The domain model
// (01-domain-model.md:42-60) carries seventeen; five are deliberately NOT shipped here because
// nothing reads them until Phase 2 or Phase 4: `locale` (ADR-0012 is English-only at 1.0),
// `public_standings` (needs auth to mean anything), `artifact_retention_days` and `redact_tells`
// (parser and retention, Phase 4), and `settings_json` (no schema yet). The freeze rule cuts this
// way: `ALTER TABLE ADD COLUMN` is a cheap forward migration, while removing or retyping a column
// is SQLite's 12-step rebuild — so over-shipping is the expensive mistake, not under-shipping.
table "guild" {
  schema = schema.main

  // The singleton key. INTEGER, not a ULID: see the table comment. The CHECK is what makes this a
  // singleton — the second row cannot be inserted.
  column "id" {
    null = false
    type = integer
  }

  // The guild's display name. NOT NULL and carries no default: a guild with no name is a boot that
  // was never configured, and defaulting it to '' would hide that.
  column "name" {
    null = false
    type = text
  }

  // The <Guild Tag> as it appears in P99's /who output. Defaults to '' — a guild may not have set
  // one yet, and '' is "unset" rather than a name that happens to be blank.
  column "tag" {
    null    = false
    type    = text
    default = ""
  }

  // IANA timezone. Renders all UI and buckets every *_day column, so it is a real column and not a
  // settings_json key. 'UTC' is the safe default: it is the one zone that is always valid.
  column "timezone" {
    null    = false
    type    = text
    default = "UTC"
  }

  // The first day of the guild's week, 0 (Sunday) through 6 (Saturday), driving weekly attendance
  // and decay buckets. Default 1 (Monday).
  column "week_start" {
    null    = false
    type    = integer
    default = 1
  }

  // What this guild calls its points — 'DKP', 'EP', 'GP', whatever the guild uses. Display only;
  // storage is always centipoints.
  column "points_label" {
    null    = false
    type    = text
    default = "DKP"
  }

  // DISPLAY rounding only, 0 through 2 decimal places. Storage is always _cp (centipoints); this
  // never touches the ledger, which is why it is a guild setting rather than a ledger property.
  column "points_precision" {
    null    = false
    type    = integer
    default = 2
  }

  // Days of no attendance before the inactivity sweep flags a member, or NULL to never auto-flag.
  // NULL is meaningful here — it is the "off" state of the sweep — which is why this column is
  // nullable where the two booleans below are not.
  column "inactive_after_days" {
    null = true
    type = integer
  }

  // Whether the sweep job actually sets members inactive, or merely reports them. Boolean stored as
  // 0/1 (canonical §8: SQLite has no BOOLEAN under STRICT).
  column "auto_set_inactive" {
    null    = false
    type    = integer
    default = 0
  }

  // Whether inactive members are hidden from standings. A query reads this, so it is a column.
  column "hide_inactive" {
    null    = false
    type    = integer
    default = 0
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // The singleton constraint. A second guild row fails this at INSERT.
  check "guild_is_singleton" {
    expr = "id = 1"
  }

  // week_start is a day-of-week, 0..6.
  check "guild_week_start_range" {
    expr = "week_start BETWEEN 0 AND 6"
  }

  // points_precision is a display-rounding depth, 0..2.
  check "guild_points_precision_range" {
    expr = "points_precision BETWEEN 0 AND 2"
  }

  // Booleans are 0/1 under STRICT, enforced rather than trusted.
  check "guild_auto_set_inactive_bool" {
    expr = "auto_set_inactive IN (0, 1)"
  }

  check "guild_hide_inactive_bool" {
    expr = "hide_inactive IN (0, 1)"
  }

  strict = true
}

// ============================================================================
// The ledger — Phase 0 PR 9. The highest-blast-radius schema in the repo.
//
// Five tables, in FK-dependency order: pool, account, ledger_batch, ledger_entry,
// balance_snapshot. The shapes are docs/design/01-domain-model.md §7 (pool), §6.1 (account) and
// §9 (the ledger). The append-only triggers and the seed data (default pool + four system
// accounts) are NOT expressible in Atlas community and are hand-appended to the generated
// migration under .claude/rules/migrations.md cases 1 and 4 — see db/migrations-sqlite/000003_ledger.sql.
//
// DEFERRED FOREIGN KEYS. Courtney's decision (docs/development/first-ten-prs.md PR 9 plan): PR 9
// creates minimal, forward-compatible pool + account, because the ledger cannot exist without
// them and the four system accounts need a table. Columns that reference tables which do not
// exist yet (person, app_user, api_token, character, item, item_award, raid, raid_tick) ship as
// nullable TEXT with NO foreign key; the FK is added when that table lands, and never by removing or
// retyping the column. The releasing migration is NOT the "cheap ADD CONSTRAINT" this note used to
// promise — SQLite has no such statement, so adding a foreign key to an existing table is the 12-step
// rebuild, which is cheap on a childless table and is .claude/rules/migrations.md's most dangerous
// shape on one with children. #277 released the three on role_assignment, pool_config_change and
// decay_run, all childless; ledger_batch's two are still here, and they are the other kind.
// Every such column is commented as a tracked deferral below.
// ============================================================================

// pool — a currency (docs/design/01-domain-model.md §7). MINIMAL for PR 9, plus the one column
// Phase 1 un-deferred.
//
// The domain model's pool carries ~20 columns; PR 9 shipped the eight the ledger structurally
// needs and deferred the rest, for the same freeze-rule reason the guild table shipped twelve of
// seventeen: ADD COLUMN is cheap, remove/retype is SQLite's 12-step rebuild. The deferral is
// released one column at a time, as its reader lands — `strategy_config_json` is the first, because
// pool_config_change versions it (#191). What is still NOT here:
//
//   - server_id: the domain model types it `NOT NULL REFERENCES server(id)`, and `server` is a
//     Phase 1 table. Shipping it now would mean either a NOT NULL FK to a table that does not
//     exist (impossible) or a nullable one that later has to be tightened (a retype). It is added
//     cleanly when `server` lands.
//   - description, currency_label, alt_policy, allow_negative,
//     min_balance_cp, hold_policy, attendance_windows, dispute_window_hours,
//     retro_edit_max_age_days, active, archived_at, sort_order: no reader yet; each
//     is a forward ADD COLUMN when its feature lands.
//
// name_norm is a PLAIN column normalised in Go (canonical C2: core SQLite has no NFKC and a
// STORED generated column cannot be added by a later ALTER), not a GENERATED column.
table "pool" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  column "name" {
    null = false
    type = text
  }

  // Normalised in Go (NFKC + casefold + strip ' ` -). The unique index is on this column, so two
  // pools whose names differ only in case or punctuation collide.
  column "name_norm" {
    null = false
    type = text
  }

  // SUPERSEDED BY THE THREE RULE COLUMNS BELOW (ADR-0026, #213). A pool composes an earn rule, a
  // spend rule and an over-time rule; one column could only ever name one of the three, which is why
  // a `tick` pool could not award an item. These three are KEPT rather than dropped because dropping
  // a column is destructive AND a 12-step rebuild of a table with three children (ledger_batch,
  // pool_config_change, decay_run) — that needs the !destructive-migration label and a human
  // (.claude/rules/migrations.md), and it is not what this change is. Migration 000006 backfills
  // earn_strategy_id from strategy_id; nothing reads these three.
  //
  // The in-tree point strategy id ('zero_sum' | 'tick' | 'fixed_price' | ...). Not a CHECK enum:
  // the set of strategies is code-defined and grows per PR, and the strategy package validates it
  // — a CHECK here would make every new strategy a schema change. The same is true of the three
  // rule columns below, for the same reason.
  column "strategy_id" {
    null = false
    type = text
  }

  // Semver of the in-tree strategy in force. Superseded with strategy_id above, and NOT replicated
  // three times: the version a batch carries comes from the planner's own Version() constant, so a
  // per-rule copy on the pool would be a second value that can disagree with the binary that wrote
  // it. pool_config_change records the version at each change, which is the history this column was
  // standing in for.
  column "strategy_version" {
    null = false
    type = text
  }

  // THE THREE RULES A POOL COMPOSES (ADR-0026). Each pair is one answer to one of the three
  // questions in docs/guides/choosing-a-dkp-system.md: how are points earned, how are they spent,
  // and what happens to them over time. strategy.Rules routes each planner to exactly one of them,
  // and ledger_batch.strategy_id then records WHICH of the three planned a given batch — which is
  // what lets a reversal be planned by the rule that planned the original.
  //
  // AN EMPTY SLOT IS THE EMPTY STRING, not NULL, and it is a legal state rather than a defect: a pool
  // part-way through setup, or a guild that genuinely has no over-time rule, must be expressible. The
  // planner whose slot is empty refuses BY NAME (strategy.ErrNoRule) and no slot is ever filled in
  // from another.
  //
  // NOT NULL WITH A '' DEFAULT rather than nullable, for two reasons that agree. The design one:
  // strategy.PoolConfig already treats an empty id as "no rule for this question", so NOT NULL keeps
  // ONE sentinel for absence at every layer instead of translating nil to "" somewhere in between —
  // and a *string in the store contract would make every caller nil-check a value whose zero already
  // means the same thing. The mechanical one: sqlc's SQLite engine (v1.31.1) does not apply
  // `emit_pointers_for_null_types` OR an `overrides:` entry to a column added by ALTER TABLE ADD
  // COLUMN — verified against this exact column, where the same declaration inside a CREATE TABLE
  // yields *string and here yields `interface{}` — so a nullable slot here would put `any` in the
  // middle of the typed boundaries (TestSqlcGen_NoEmptyInterfaceFields, .claude/rules/go-idioms.md).
  // Declaring them inside a CREATE TABLE instead would mean rebuilding `pool`, which has three child
  // tables, and that is the migration this project's rules exist to prevent.
  //
  // NO CHECK on any of the three ids, for the same reason strategy_id above carries none. Which SLOT
  // an id may occupy is likewise code-defined — strategy.PointStrategy.RuleKind() declares it and
  // strategy.PoolConfig.Resolve refuses a mismatch with ErrWrongRuleKind — so a CHECK here would be
  // a second, weaker copy of a rule the database cannot state.
  column "earn_strategy_id" {
    null    = false
    type    = text
    default = ""
  }

  // The earn rule's own configuration, validated on write against that strategy's ConfigSchema() and
  // read whole (canonical §8: a *_json column is never queried into). DEFAULT '{}' on all three, so
  // adding them is a plain ADD COLUMN on a populated database and so an unconfigured rule runs its
  // shipped defaults rather than a null document its parser refuses.
  column "earn_config_json" {
    null    = false
    type    = text
    default = "{}"
  }

  column "spend_strategy_id" {
    null    = false
    type    = text
    default = ""
  }

  column "spend_config_json" {
    null    = false
    type    = text
    default = "{}"
  }

  column "over_time_strategy_id" {
    null    = false
    type    = text
    default = ""
  }

  column "over_time_config_json" {
    null    = false
    type    = text
    default = "{}"
  }

  // The singular strategy's own configuration. SUPERSEDED by the three per-rule config columns
  // above (ADR-0026); migration 000006 backfills earn_config_json from it. What follows is why it
  // exists at all, kept because pool_config_change still snapshots a config document and the
  // argument for the '{}' default is the argument for the three that replaced it.
  //
  // Validated on write against strategy.ConfigSchema() and read
  // whole (canonical §8: a *_json column is never queried into). THE CONFIGURATION IN FORCE — the
  // pool holds what is current, pool_config_change holds how it got there.
  //
  // Deferred by PR 9 with the rest of the domain model's pool columns, and un-deferred here rather
  // than later because its reader now exists: pool_config_change's from_config_json/to_config_json
  // snapshot THIS column, and a history table versioning a column that does not exist is a writer
  // that can record the change and not apply it. Every strategy also needs somewhere to persist a
  // decay rate or a cap ceiling, which is what #193/#194 land on.
  //
  // DEFAULT '{}' where pool_config_change's two snapshots have none: an empty document is the honest
  // value for a pool whose strategy takes no configuration, while an empty SNAPSHOT would be a lie
  // about history. Also what makes this a plain ADD COLUMN on a populated database.
  column "strategy_config_json" {
    null    = false
    type    = text
    default = "{}"
  }

  // Space-separated balance kinds the pool's rules declare ('dkp', or 'ep gp' for EPGP) — the UNION
  // across the three, which strategy.Rules.BalanceKinds computes. Default 'dkp'. Read whole, never
  // queried into — it is a declaration, not a fact table.
  column "balance_kinds" {
    null    = false
    type    = text
    default = "dkp"
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // One pool per normalised name (docs/design/01-domain-model.md:813). Not partial: PR 9's pool
  // has no soft-delete column, so there is no `WHERE deleted_at IS NULL` to add yet.
  index "ux_pool_name" {
    unique  = true
    columns = [column.name_norm]
  }

  strict = true
}

// account — the balance holder (docs/design/01-domain-model.md §6.1).
//
// One account per person (1:1), PLUS the system accounts with no person, which is why this table
// is load-bearing in PR 9: zero-sum splits, rot handling and write-offs need ledger-addressable
// non-human targets, and the four system accounts (residue, guild_bank, write_off,
// import_opening) are seeded by the migration. This table is already minimal in the domain model,
// so PR 9 ships it in full — only the person_id FK is deferred.
table "account" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // The values are internal/account/kinds' and the CHECK below is generated from it — this comment
  // deliberately does not restate them, because a prose list beside a generated one is the drift the
  // catalogue exists to remove. The two paired CHECKs further down tie this column to which of
  // person_id / system_key is populated, so an account is exactly one of the kinds and cannot be
  // malformed.
  column "kind" {
    null = false
    type = text
  }

  // DEFERRED FK -> person(id). person is a Phase 1 table (docs/design/01-domain-model.md §6.1).
  // Nullable TEXT, no foreign key until person lands; the ux_account_person partial unique index
  // still enforces one account per person in the meantime. NULL for every system account.
  column "person_id" {
    null = true
    type = text
  }

  // One of internal/account/kinds' system keys, or NULL for a person account. The closed set is a
  // CHECK — generated from that catalogue — because the keys are addressed by name in code, and a
  // fifth is a deliberate schema change plus a seed row (internal/ledger.SystemAccountIDs pairs each
  // key with the deterministic id its row carries).
  column "system_key" {
    null = true
    type = text
  }

  column "label" {
    null = false
    type = text
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // BEGIN GENERATED — account enum CHECKs, from internal/account/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestAccountKinds_CheckMatchesCatalogue fails on.
  check "account_kind_enum" {
    expr = "kind IN ('person', 'system')"
  }

  check "account_system_key_enum" {
    expr = "system_key IS NULL OR system_key IN ('guild_bank', 'residue', 'write_off', 'import_opening')"
  }
  // END GENERATED — account enum CHECKs.

  // A person account has a person_id and no system_key; a system account is the mirror image.
  // These two paired CHECKs are the domain model's (01-domain-model.md:516-517) and are what make
  // the kind column trustworthy rather than advisory.
  check "account_person_shape" {
    expr = "((kind = 'person') = (person_id IS NOT NULL))"
  }

  check "account_system_shape" {
    expr = "((kind = 'system') = (system_key IS NOT NULL))"
  }

  // One account per person, one per system_key. Partial so the NULL side of each never collides.
  index "ux_account_person" {
    unique  = true
    columns = [column.person_id]
    where   = "person_id IS NOT NULL"
  }

  index "ux_account_system" {
    unique  = true
    columns = [column.system_key]
    where   = "system_key IS NOT NULL"
  }

  strict = true
}

// ledger_batch — one atomic point-changing event (docs/design/01-domain-model.md §9.1).
//
// Append-only, enforced by triggers hand-appended to the migration (case 1). `seq` is the
// per-pool ordering authority; a balance is defined as of a seq, never a timestamp. This is the
// FULL domain-model shape — every column, CHECK and index — with only actor_user_id and
// actor_token_id deferred (their tables are Phase 1/2).
table "ledger_batch" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // Real FK -> pool(id). The pool this batch belongs to; every read filters on it.
  column "pool_id" {
    null = false
    type = text
  }

  // PER-POOL monotonic sequence, allocated inside the write transaction (NextPoolSeq). THE
  // ordering authority. ux_batch_seq(pool_id, seq) is the guardrail if the single-writer property
  // is ever lost.
  column "seq" {
    null = false
    type = integer
  }

  // The batch kind. A CHECK enum: adding a value is a CHECK, an OpenAPI enum and a docs page in
  // one (.claude/rules/ledger-and-strategy.md), so it is deliberately a schema change.
  column "kind" {
    null = false
    type = text
  }

  column "strategy_id" {
    null = false
    type = text
  }

  column "strategy_version" {
    null = false
    type = text
  }

  // The EXACT rules in force when this batch was planned. Read whole, never queried into — a past
  // batch's meaning must not change when the pool's config changes.
  column "config_snapshot_json" {
    null    = false
    type    = text
    default = "{}"
  }

  // Persisted RNG seed so a replay is byte-identical. NULL when the strategy used no RNG.
  column "rng_seed" {
    null = true
    type = integer
  }

  // Where the batch came from. CHECK enum.
  column "source" {
    null = false
    type = text
  }

  // 'tick_credit:<ulid>' | 'item_award:<ulid>' | 'person_merge:<ulid>' | ... The addressable
  // receipt this batch was derived from. Nullable; ux_batch_srcref makes it unique per pool when
  // present (idempotent fan-out). It is a free-form ref string, NOT a foreign key: it points at
  // several different tables depending on the prefix, so no single FK can express it.
  column "source_ref" {
    null = true
    type = text
  }

  // DEFERRED FK -> app_user(id). app_user is a Phase 2 table. Nullable TEXT, no foreign key until
  // it lands. The acting human, for audit.
  column "actor_user_id" {
    null = true
    type = text
  }

  // DEFERRED FK -> api_token(id). api_token is a Phase 2 table. Nullable TEXT, no foreign key
  // until it lands. The acting bot token, for audit.
  column "actor_token_id" {
    null = true
    type = text
  }

  // Whether the actor is a beneficiary of this batch (self-dealing). Computed by the writer at
  // commit time (PR 10); drives the UI badge and the self-dealing report. Boolean 0/1.
  column "actor_is_beneficiary" {
    null    = false
    type    = integer
    default = 0
  }

  column "reason" {
    null    = false
    type    = text
    default = ""
  }

  // Self-FK -> ledger_batch(id). Set on a reversal; NULL otherwise. ux_batch_reverses makes
  // "is this batch reversed?" an index-only EXISTS and enforces at-most-once reversal.
  column "reverses_batch_id" {
    null = true
    type = text
  }

  // GAME truth; may be backdated. SYSTEM truth (recorded_at) never is. Disputes need both.
  column "effective_at" {
    null = false
    type = integer
  }

  column "recorded_at" {
    null = false
    type = integer
  }

  // 'YYYY-MM-DD' guild-local, computed in Go.
  column "effective_day" {
    null = false
    type = text
  }

  // Nullable; ux_batch_idem makes it unique per pool when present. Every mutating POST that
  // creates domain state carries one (canonical invariant) so a bot's retry is a no-op.
  column "idempotency_key" {
    null = true
    type = text
  }

  // Number of entries in this batch. CHECK (> 0): a zero-entry batch is noise that breaks
  // entry_count reasoning.
  column "entry_count" {
    null = false
    type = integer
  }

  // Sum of the batch's entry amounts; 0 for a zero-sum award. A column-comparison invariant
  // instead of an aggregate.
  column "net_amount_cp" {
    null = false
    type = integer
  }

  // The hash chain (per pool, because seq is per pool). prev_hash is NULL only at seq = 1.
  // BLOB — a raw 32-byte SHA-256, not hex text.
  column "prev_hash" {
    null = true
    type = blob
  }

  column "hash" {
    null = false
    type = blob
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "ledger_batch_pool" {
    columns     = [column.pool_id]
    ref_columns = [table.pool.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // Self-FK for the reversal pointer.
  foreign_key "ledger_batch_reverses" {
    columns     = [column.reverses_batch_id]
    ref_columns = [column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // BEGIN GENERATED — ledger enum CHECKs, from internal/ledger/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestLedgerKinds_CheckMatchesCatalogue fails on.
  check "ledger_batch_kind_enum" {
    expr = "kind IN ('attendance', 'award', 'adjustment', 'decay', 'cap', 'start_points', 'zero_sum_credit', 'reversal', 'correction', 're_attribution', 'migration', 'import', 'seed', 'write_off')"
  }

  check "ledger_batch_source_enum" {
    expr = "source IN ('web', 'api', 'discord', 'parser', 'import', 'system')"
  }
  // END GENERATED — ledger enum CHECKs.

  check "ledger_batch_actor_is_beneficiary_bool" {
    expr = "actor_is_beneficiary IN (0, 1)"
  }

  check "ledger_batch_entry_count_positive" {
    expr = "entry_count > 0"
  }

  check "ledger_batch_times_nonneg" {
    expr = "recorded_at >= 0 AND effective_at >= 0"
  }

  // Per-pool sequence uniqueness — the guardrail on the max+1 allocator.
  index "ux_batch_seq" {
    unique  = true
    columns = [column.pool_id, column.seq]
  }

  // One batch per source_ref per pool (idempotent fan-out), when a source_ref is present.
  index "ux_batch_srcref" {
    unique  = true
    columns = [column.pool_id, column.source_ref]
    where   = "source_ref IS NOT NULL"
  }

  // One batch per idempotency_key per pool, when present. A bot's retry lands on the first batch.
  index "ux_batch_idem" {
    unique  = true
    columns = [column.pool_id, column.idempotency_key]
    where   = "idempotency_key IS NOT NULL"
  }

  // A batch is reversed at most once; makes the EXISTS lookup index-only.
  index "ux_batch_reverses" {
    unique  = true
    columns = [column.reverses_batch_id]
    where   = "reverses_batch_id IS NOT NULL"
  }

  // "As of a date" derives the seq via effective_at.
  index "ix_batch_effective" {
    columns = [column.pool_id, column.effective_at]
  }

  // The kind-filtered timeline (decay runs, reversals) per pool.
  index "ix_batch_kind" {
    columns = [column.pool_id, column.kind, column.seq]
  }

  // The self-dealing report: only the flagged rows, newest first.
  index "ix_batch_selfdeal" {
    columns = [column.actor_is_beneficiary, column.recorded_at]
    where   = "actor_is_beneficiary = 1"
  }

  strict = true
}

// ledger_entry — one account's share of one batch (docs/design/01-domain-model.md §9.1).
//
// Append-only (triggers, case 1). The FULL shape: real FKs to batch, pool and account; five
// deferred FKs whose tables are Phase 1/4; `seq` DENORMALISED from the batch; and the covering
// balance index.
table "ledger_entry" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // Real FK -> ledger_batch(id).
  column "batch_id" {
    null = false
    type = text
  }

  // Real FK -> pool(id). DENORMALISED from the batch: every read filters on pool, and carrying it
  // on the entry is what lets the balance query avoid a join to the batch.
  column "pool_id" {
    null = false
    type = text
  }

  // DENORMALISED batch.seq onto the entry. This is the whole reason the balance query is
  // index-only: BalanceAsOfSeq filters `seq <= ?` against ix_entry_balance with no join to
  // ledger_batch. It is written from the batch's allocated seq at commit time (PR 10) and, like
  // every ledger row, is never updated afterwards.
  column "seq" {
    null = false
    type = integer
  }

  // Real FK -> account(id). The balance holder. This is what a balance sums over.
  column "account_id" {
    null = false
    type = text
  }

  // DEFERRED FK -> character(id). character is a Phase 1 table. Nullable TEXT, no FK until it
  // lands. Attribution ONLY — it NEVER affects a balance, so re-parenting an alt cannot move
  // history.
  column "character_id" {
    null = true
    type = text
  }

  // 'dkp' | 'ep' | 'gp' | ... The balance kind this entry moves. Part of the covering index.
  column "balance_kind" {
    null = false
    type = text
  }

  // The signed amount. CHECK (<> 0): a zero entry is noise that breaks entry_count reasoning.
  column "amount_cp" {
    null = false
    type = integer
  }

  // DEFERRED FK -> item(id). item is a Phase 3 table. Nullable TEXT, no FK until it lands.
  column "item_id" {
    null = true
    type = text
  }

  // DEFERRED FK -> item_award(id). item_award is a Phase 3 table. Nullable TEXT, no FK.
  column "item_award_id" {
    null = true
    type = text
  }

  // DEFERRED FK -> raid(id). raid is a Phase 4 table. Nullable TEXT, no FK.
  column "raid_id" {
    null = true
    type = text
  }

  // DEFERRED FK -> raid_tick(id). raid_tick is a Phase 4 table. Nullable TEXT, no FK.
  column "tick_id" {
    null = true
    type = text
  }

  column "metadata_json" {
    null    = false
    type    = text
    default = "{}"
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "ledger_entry_batch" {
    columns     = [column.batch_id]
    ref_columns = [table.ledger_batch.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  foreign_key "ledger_entry_pool" {
    columns     = [column.pool_id]
    ref_columns = [table.pool.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  foreign_key "ledger_entry_account" {
    columns     = [column.account_id]
    ref_columns = [table.account.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  check "ledger_entry_amount_nonzero" {
    expr = "amount_cp <> 0"
  }

  // THE balance index. Covering: SUM(amount_cp) over (pool_id, account_id, balance_kind) with
  // `seq <= ?` is answered from the index alone, with no table access. The column order is EXACT
  // and load-bearing — the EXPLAIN QUERY PLAN golden asserts it stays a covering index.
  index "ix_entry_balance" {
    columns = [column.pool_id, column.account_id, column.balance_kind, column.seq, column.amount_cp]
  }

  index "ix_entry_batch" {
    columns = [column.batch_id]
  }

  // The account statement view (per-account timeline, newest first).
  index "ix_entry_stmt" {
    columns = [column.account_id, column.pool_id, column.seq]
  }

  // Item history, only for entries that carry an item.
  index "ix_entry_item" {
    columns = [column.item_id]
    where   = "item_id IS NOT NULL"
  }

  strict = true
}

// balance_snapshot — a CACHE of the current balance per (pool, account, balance_kind)
// (docs/design/01-domain-model.md §9.5).
//
// Maintained synchronously in the same transaction as the batch write (PR 10) and verified
// nightly by recomputing from the ledger (`dkp verify-ledger`). It is a cache and is treated as
// one — never the source of truth, which is always the SUM over ledger_entry. WITHOUT ROWID: the
// composite PK IS the row, read only ever by that key.
//
// It is LOAD-BEARING, not droppable — ADR-0023, measured over 527,164 entries: 13 pages to serve
// the standings page from here, 10,412 from the definitional SUM. Nothing about the DDL changes
// on that finding (option D, widening ix_snapshot_standings, was declined); what changes is that
// dropping this table is a rebuild rather than a slower page, so the nightly replay that verifies
// it is a correctness dependency.
table "balance_snapshot" {
  schema = schema.main

  column "pool_id" {
    null = false
    type = text
  }

  column "account_id" {
    null = false
    type = text
  }

  column "balance_kind" {
    null = false
    type = text
  }

  column "amount_cp" {
    null = false
    type = integer
  }

  // The seq this snapshot is current as of. Advanced on every upsert.
  column "as_of_seq" {
    null = false
    type = integer
  }

  // The number of entries folded into this balance, kept additively alongside amount_cp so a
  // drift check has both a sum and a count to compare.
  column "entry_count" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  foreign_key "balance_snapshot_pool" {
    columns     = [column.pool_id]
    ref_columns = [table.pool.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  foreign_key "balance_snapshot_account" {
    columns     = [column.account_id]
    ref_columns = [table.account.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  primary_key {
    columns = [column.pool_id, column.account_id, column.balance_kind]
  }

  // The standings scan: every account's balance in a pool, highest first. amount_cp DESC is the
  // whole point — /standings reads this in one indexed pass, not from the ledger.
  index "ix_snapshot_standings" {
    on {
      column = column.pool_id
    }
    on {
      column = column.balance_kind
    }
    on {
      column = column.amount_cp
      desc   = true
    }
  }

  // THE COVERING INDEX FOR balance_snapshot_account (#274). The primary key is
  // (pool_id, account_id, balance_kind) and ix_snapshot_standings starts with pool_id, so neither can
  // find rows by account_id: deleting an account scanned every snapshot row in the instance, and this
  // is the table ADR-0023 measured at 13 pages to serve standings from — the largest cache the
  // product keeps, growing with accounts × pools × kinds.
  //
  // IT COSTS A B-TREE WRITE ON EVERY BATCH COMMIT, which is the reason to state the trade rather than
  // sweep it in. The snapshot upsert is on the commit path; the index makes it one page-write wider
  // per (account, kind) touched. That is worth paying because the alternative is an unbounded scan
  // holding the single write connection, and because balance_snapshot is WITHOUT ROWID — an index on
  // (account_id) carries the whole primary key as its payload, so it answers "this account's balances
  // across every pool" with no table access at all.
  index "ix_snapshot_account" {
    columns = [column.account_id]
  }

  without_rowid = true
  strict        = true
}

// audit_log — one row per mutating action: who did this, with what authority, and to what
// (docs/design/01-domain-model.md §17).
//
// It is NOT the ledger and it is not derivable from the ledger. The batch is the *what*; the audit
// row is the *who/how/from where*. §17.1 tabulates the differences; the one that shapes this table
// is deletability. A ledger row is never removed, so ledger_batch carries a no-delete trigger. An
// audit row IS prunable by retention (default 3 years) and pruning leaves an `audit_gap_marker`
// scar — so audit_log gets an append-only UPDATE trigger and DELIBERATELY no DELETE trigger. A
// delete trigger here would make `dkp audit prune` impossible without first dropping the guardrail,
// which is exactly the "rebuild ate the trigger" failure .claude/rules/migrations.md warns about.
//
// MINIMAL, and the omissions are deliberate rather than forgotten. The domain-model shape carries
// twenty-four columns; this ships eleven — the ones a commit can populate truthfully today. Every
// column left out is either a FK to a Phase 2 table (`actor_user_id` -> app_user,
// `actor_service_account_id` -> service_account, `actor_token_id` -> api_token, `permission_used`
// -> permission) or a forensic field whose only writer is the Phase 2 HTTP middleware
// (`operation_id`, `request_id`, `ip`, `ip_truncated_at`, `user_agent`, `before_json`, `after_json`,
// `reason`, `resource_label`, `actor_is_beneficiary`). Shipping a column no writer can fill means
// shipping a column every reader must treat as absent, and the freeze rule cuts the other way:
// ALTER TABLE ADD COLUMN is a cheap forward migration, while removing one is SQLite's 12-step
// rebuild (docs/development/phase-0-pr5-decisions.md §U2, applied here for the same reason).
table "audit_log" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // GAPLESS within the instance, allocated inside the writing transaction exactly as the ledger's
  // per-pool seq is. Gaplessness is what gives the hash chain below an ordering to hash over: a
  // chain whose links can be renumbered proves nothing. ux_audit_seq is the guardrail.
  //
  // This is instance-wide where ledger_batch.seq is per-pool, and event_outbox.event_seq is a third,
  // different number. Three sequences answering three questions; sharing a name would guarantee a
  // bot author mixes them (domain model §15).
  column "seq" {
    null = false
    type = integer
  }

  // Wall-clock, Micros. The ledger's bitemporal effective_at/recorded_at split does not apply here:
  // an audit row records when an action happened, and an action cannot be backdated.
  //
  // ON THE NAME, which breaks a convention on purpose and is reported here rather than fixed.
  // docs/design/00-canonical-conventions.md §8 says time columns are suffixed `_at`; this column is
  // named `at`, because docs/design/01-domain-model.md §17 names it `at` and that is the name every
  // §17 index, query and forensic-screen mock-up already uses. AGENTS.md's rule for exactly this
  // situation is "if two instructions conflict, the invariant wins and the conflict is a bug: say
  // so" — so it is said here. THE CONFLICT IS A DOC BUG, not a schema bug: `at` is not a time
  // column with a missing suffix, it is a column whose whole name is the suffix, and §8's rule was
  // written for `created_at`/`recorded_at`/`effective_at`, none of which this is.
  //
  // Renaming it to `at_at` would be absurd and renaming it to `occurred_at` would silently
  // invalidate §17 and every document downstream of it — for a table that is append-only, so the
  // rename would be a 12-step SQLite rebuild of the one table whose triggers must survive. The fix
  // belongs in §8, which should carve out a bare `at`, and until somebody makes that edit this
  // comment is the record that the divergence was noticed rather than missed.
  column "at" {
    null = false
    type = integer
  }

  // CHECK enum. 'system' is what a Phase 0 commit records, because there is no authentication yet
  // and inventing a user id would be a lie in the one table whose entire value is that it is not.
  column "actor_kind" {
    null = false
    type = text
  }

  // DENORMALISED actor name, on purpose: it must survive the actor's deletion or erasure. An audit
  // row that reads "deleted user 01J..." is not an audit trail.
  column "actor_label" {
    null    = false
    type    = text
    default = ""
  }

  // The PERMISSION key, verbatim ('ledger.batch.commit'), never a rendered sentence. A rendered
  // sentence is neither queryable nor diffable — that is EQdkp's `__logs.log_value`, and §17 marks
  // it as deliberately not copied.
  column "action" {
    null = false
    type = text
  }

  column "resource_kind" {
    null = false
    type = text
  }

  column "resource_id" {
    null = true
    type = text
  }

  // CHECK enum. A denied or errored action is audited too — the forensic question "who TRIED this?"
  // is at least as important as "who did it".
  column "outcome" {
    null = false
    type = text
  }

  // The cross-link. Committing a ledger batch also writes an audit row carrying the batch id, so a
  // dispute goes from a balance to the actor in one join (§17.1). A real FK: ledger_batch exists.
  column "ledger_batch_id" {
    null = true
    type = text
  }

  // The instance-wide chain, independent of the ledger's per-pool chains (§9.6). The head lives in
  // dkp_meta('audit_head'); prev_hash is NULL only at seq = 1. BLOB — a raw 32-byte SHA-256.
  column "prev_hash" {
    null = true
    type = blob
  }

  column "hash" {
    null = false
    type = blob
  }

  primary_key {
    columns = [column.id]
  }

  // dkp:fk-uncovered ledger_batch is append-only, so this lookup can never run (#274).
  //
  // trg_ledger_batch_no_update and trg_ledger_batch_no_delete abort any UPDATE or DELETE of a batch,
  // and TestTriggers_MutatingLedger_Raises drives all four on a database that applied every
  // migration — so no parent row here can ever be deleted or re-keyed, and the child lookup SQLite
  // would need an index for is unreachable. The index would be a third B-tree write on the table
  // every mutating request appends to, read by nothing.
  //
  // This is the same argument the deferred §17 indexes below make, sharpened: those are deferred
  // because nothing SELECTS on them yet, and this one is waived because nothing can. The read that
  // would want it — a dispute walking from a batch to the actor — is §17.1's, and the day an endpoint
  // serves it, the index lands with that endpoint and this waiver comes out.
  foreign_key "audit_log_batch" {
    columns     = [column.ledger_batch_id]
    ref_columns = [table.ledger_batch.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // BEGIN GENERATED — audit_log enum CHECKs, from internal/audit/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestAuditKinds_CheckMatchesCatalogue fails on.
  check "audit_log_actor_kind_enum" {
    expr = "actor_kind IN ('user', 'service_account', 'system', 'boot', 'import', 'anonymous')"
  }

  check "audit_log_outcome_enum" {
    expr = "outcome IN ('success', 'denied', 'error')"
  }
  // END GENERATED — audit_log enum CHECKs.

  // The gaplessness guardrail: two rows cannot claim the same position in the chain.
  index "ux_audit_seq" {
    unique  = true
    columns = [column.seq]
  }

  // The forensic view's default ordering, newest first.
  index "ix_audit_at" {
    on {
      column = column.at
      desc   = true
    }
  }

  // THE THREE OTHER §17 INDEXES ARE DEFERRED, DELIBERATELY. docs/design/01-domain-model.md §17
  // specifies ix_audit_actor, ix_audit_resource and ix_audit_action alongside ix_audit_at, and this
  // table ships only the last. The reason is the same one that cut this table from twenty-four
  // columns to eleven: two of the three index columns that would make them useful do not exist yet.
  //
  //   ix_audit_actor     (actor_user_id, at DESC) — actor_user_id is a Phase 2 column (app_user).
  //                      An index on actor_kind instead would be a three-value column on a table
  //                      where nearly every Phase 0 row says 'system', which SQLite would decline
  //                      to use anyway.
  //   ix_audit_resource  (resource_kind, resource_id, at DESC) — buildable today, and useless
  //                      today: every row is ('ledger_batch', <batch id>), so it answers a question
  //                      ledger_batch_id already answers through its own foreign key.
  //   ix_audit_action    (action, at DESC) — buildable today, and the same shape of useless: there
  //                      is one action ('ledger.batch.commit') until the Phase 2 middleware starts
  //                      writing audit rows for anything else.
  //
  // Adding an index is a cheap forward migration; removing one is not, and an index that is never
  // chosen still costs a B-tree write on every insert into the table every mutating request touches.
  // They land with the writers that make them selective — the Phase 2 HTTP middleware — and the
  // forensic screens that query them, not before.

  strict = true
}

// event_outbox — the GLOBAL event sequence that feeds SSE, webhooks and Last-Event-ID
// (docs/design/01-domain-model.md §15).
//
// Written in the SAME transaction as the state change it describes. That is the whole point of an
// outbox: a subscriber can never see an event for a batch that was rolled back, and can never miss
// an event for a batch that committed, because there is no second write that could fail on its own.
//
// `event_seq` is INTEGER PRIMARY KEY AUTOINCREMENT and the AUTOINCREMENT keyword is load-bearing,
// not decoration. Without it SQLite reuses the largest freed rowid, so pruning old events would let
// a NEW event take a number an old event already published — and a client resuming from
// Last-Event-ID would silently skip everything in between. With it the high-water mark persists in
// sqlite_sequence and a number is never reused. It is allocated by the INSERT and read back with
// RETURNING, so no caller has to guess at it.
//
// The row carries a resource REFERENCE, never a document: '/api/v1/ledger/batches/<ulid>'. A
// payload copied into the outbox is a payload that goes stale, and a second place authorisation has
// to be re-decided; a subscriber fetches the resource and gets whatever it is allowed to see.
table "event_outbox" {
  schema = schema.main

  column "event_seq" {
    null           = false
    type           = integer
    auto_increment = true
  }

  // The event's own ULID, stable across a redelivery. Distinct from event_seq, which is a position.
  column "id" {
    null = false
    type = text
  }

  // 'guild' | 'pool:<ulid>' | 'bid:<ulid>' — the SSE topic a subscriber filters on.
  column "topic" {
    null = false
    type = text
  }

  // 'ledger.batch.committed' — the same vocabulary as notification.event_type.
  column "event_type" {
    null = false
    type = text
  }

  column "resource_ref" {
    null = false
    type = text
  }

  column "created_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.event_seq]
  }

  // The tailer's read: everything on one topic after a given position, in order.
  index "ix_outbox_topic" {
    columns = [column.topic, column.event_seq]
  }

  strict = true
}

// ============================================================================
// Pool configuration history and decay cadence — Phase 1, issues #191 and #192.
//
// Two tables, both hanging off `pool`, both about the same problem: EQdkp Plus kept every decay,
// cap and start-points rule in `data/<md5>/eqdkp/apa/apatab.php` — a PHP-serialised file on disk,
// OUTSIDE the database (docs/design/01-domain-model.md §7, row 15 of the parity table). A DB-only
// backup silently lost every rule the guild had, and nothing anywhere recorded that a rule had
// changed or that a decay had already run. Ours are `pool.strategy_config_json` plus these two:
// in the database, in the backup, in the audit log, previewable, and idempotent per period.
//
// They are declared LAST in this file, after every table they reference. HCL resolves references
// after the parse, so the order is a readability choice rather than a requirement — but a reader
// checking that `migration_batch_id` really points at the ledger should not have to scroll up.
//
// NEITHER TABLE CARRIES AN APPEND-ONLY TRIGGER, and for `pool_config_change` that is a decision
// rather than an omission. Domain model §21 lists exactly four tables under "Immutable (DB
// trigger)" — ledger_batch, ledger_entry, bid and audit_log — and this is not one of them. The
// argument against adding a fifth is the one 000004_audit_and_outbox.sql already writes down about
// audit_log's ABSENT delete trigger: a guardrail that normal operation has to drop is not a
// guardrail. `DELETE /pools/{pool_id}` exists in docs/design/02-api-design.md:293, and a no-delete
// trigger on a pool's config history is a trigger that a pool deletion has to work around. What
// makes the history trustworthy here is structural instead: the row has no `updated_at` and no
// mutable column — it is a from→to pair and the reason for it, written once — so there is nothing
// an UPDATE could legitimately say.
// ============================================================================

// pool_config_change — a pool's strategy configuration, versioned as events
// (docs/design/01-domain-model.md:818).
//
// The point of the table is in its shape: it records BOTH SIDES of every change. `pool` holds the
// configuration in force; this holds what it was, what it became, who did it and why. A change is
// therefore never "overwriting the config" — it is an append here plus an update there, and the
// question "what was this pool's decay rule in March?" is answerable from rows rather than from a
// backup nobody took.
//
// The FULL domain-model shape, with one deferred FK (changed_by) and one real one
// (migration_batch_id).
//
// WHY from_config_json AND to_config_json CARRY NO DEFAULT while every other *_json column in this
// schema defaults to '{}'. They are SNAPSHOTS, not settings: a snapshot that defaults to '{}'
// records "the configuration was empty" whenever a writer forgets to pass it, which is a lie the
// history cannot be distinguished from. The domain model writes them NOT NULL with no default for
// that reason, and the convention in canonical §8 is about configuration columns — decay_run's own
// two *_json columns below take the default, because "no dry run has been computed yet" genuinely
// is the empty document.
table "pool_config_change" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // Real FK -> pool(id). NO ON DELETE CASCADE, deliberately: matching the domain model, and because
  // a pool whose history would be silently erased by its own deletion is the failure this table
  // exists to prevent. A pool with config history is a pool a delete has to deal with explicitly.
  column "pool_id" {
    null = false
    type = text
  }

  // Micros. The event's time, and the second half of ix_pcc_pool's ordering. There is no
  // created_at/updated_at pair: this row IS the event, so a separate "when was it recorded" would be
  // the same number, and an updated_at would be a column describing a mutation that cannot happen.
  column "changed_at" {
    null = false
    type = integer
  }

  // Real FK -> app_user(id) since #277, which released the deferral 000009 made good on. NULL is also
  // the honest value for a change made by the importer or by a boot-time migration, which have no
  // user behind them.
  column "changed_by" {
    null = true
    type = text
  }

  // The configuration in force BEFORE this change. All three mirror columns of the same name on
  // pool — `strategy_config_json` is un-deferred in this change precisely so that this trio has
  // something to snapshot: a history table versioning a column that does not exist is a writer that
  // can record a change and not apply it.
  column "from_strategy_id" {
    null = false
    type = text
  }

  column "from_strategy_version" {
    null = false
    type = text
  }

  column "from_config_json" {
    null = false
    type = text
  }

  // The configuration in force AFTER it. Compared against the from_* trio by the officer-facing
  // diff, never queried into: canonical §8's "*_json is validated on write and read whole".
  column "to_strategy_id" {
    null = false
    type = text
  }

  column "to_strategy_version" {
    null = false
    type = text
  }

  column "to_config_json" {
    null = false
    type = text
  }

  // Free text from the officer who made the change. Defaults to '' rather than being nullable: ""
  // and NULL would be two spellings of "no reason given" and some reader would eventually compare
  // against the wrong one.
  column "reason" {
    null    = false
    type    = text
    default = ""
  }

  // Real FK -> ledger_batch(id). A strategy change can require a compensating batch — an EPGP
  // switchover, a decay rule that back-applies — and this is the pointer to it. NULL when the change
  // moved nobody's points, which is the common case.
  column "migration_batch_id" {
    null = true
    type = text
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "pool_config_change_pool" {
    columns     = [column.pool_id]
    ref_columns = [table.pool.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // SET NULL, for the reason role_assignment_granted_by carries in full: attribution on a history row
  // is not a live capability, and a config change made by an officer who has since been erased is
  // still a change that happened. The row's from→to pair and its reason — everything this table
  // exists to record — are untouched; what a hard user delete clears is the pointer to a person the
  // database no longer holds, which is what erasure means. NO ACTION here would make an immutable
  // history table permanently veto the erasure path.
  foreign_key "pool_config_change_changed_by" {
    columns     = [column.changed_by]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  // dkp:fk-uncovered ledger_batch is append-only, so this lookup can never run (#274).
  //
  // The same waiver audit_log_batch carries, for the same parent and the same reason: the two
  // append-only triggers abort every UPDATE and DELETE of a batch, so nothing can provoke the child
  // scan an index here would serve.
  foreign_key "pool_config_change_batch" {
    columns     = [column.migration_batch_id]
    ref_columns = [table.ledger_batch.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // One pool's history, newest first — the only read this table has
  // (docs/design/01-domain-model.md:827). DESC in the index rather than at the query, because "what
  // is the current configuration and what was it before" is the shape every caller wants and a
  // descending scan of the leading rows is what serves it without a sort.
  index "ix_pcc_pool" {
    on {
      column = column.pool_id
    }
    on {
      column = column.changed_at
      desc   = true
    }
  }

  // The covering index for pool_config_change_changed_by (#274, #277). Cheap where the ledger-batch
  // one above would not be: this table is appended to when an officer changes a pool's rules, which
  // is a handful of rows a year, not a row per commit.
  index "ix_pcc_changed_by" {
    columns = [column.changed_by]
  }

  strict = true
}

// decay_run — one cadence period of one pool's decay, and whether it has run
// (docs/design/01-domain-model.md:1768).
//
// THE UNIQUE INDEX IS THE TABLE'S REASON TO EXIST. Canonical §10 and ADR-0002 both state the rule
// as "decay is posted, not computed — explicit batches with idempotency key (pool_id,
// cadence_period)", and ux_decay_period below is the database half of that key. Without it, a job
// that fires twice — a box that rebooted mid-run, a periodic scheduler catching up after downtime,
// an officer clicking Commit twice — decays every balance in the pool a second time, and because
// the ledger is append-only the repair is a reversal batch that every member sees. P9 in
// docs/design/04-testing.md is the property test for it; the index is what makes the property hold
// under a race the Go code cannot see, since two workers can both read "no run for 2026-W31" before
// either writes one.
//
// The FULL domain-model shape, with one deferred FK (triggered_by) and two real ones.
table "decay_run" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // Real FK -> pool(id). Decay is per-pool: the cadence, the rule and the run all belong to one
  // currency.
  column "pool_id" {
    null = false
    type = text
  }

  // WHICH CADENCE FAMILY this run belongs to. The values are internal/decay/kinds' and the CHECK
  // below is generated from it. ADR-0024 and issue #206: all three families key on (pool_id,
  // cadence_period) and the domain model defines one run table, so without this column inside
  // ux_decay_period a cap run collides with that period's decay run — on an index built to stop a
  // repeat, in a way that looks exactly like successful deduplication.
  //
  // No DEFAULT, deliberately, where state has one. A run is born 'planned' whoever creates it, but
  // there is no default family: defaulting to 'decay' would make a cap job that forgot to set the
  // column write a row that silently takes decay's slot for the period.
  column "kind" {
    null = false
    type = text
  }

  // The period this run covers: '2026-W31' | '2026-08' | 'raid:<ulid>'. TEXT rather than a pair of
  // timestamps because it is an IDENTITY, not a range — it is the half of the idempotency key that
  // says "this period, once" — and a weekly, a monthly and a per-raid cadence have to be expressible
  // in the same column without three nullable date pairs. Derived by the strategy from its cadence
  // config (docs/design/04-testing.md:102), never parsed back apart by a query.
  column "cadence_period" {
    null = false
    type = text
  }

  // Micros. When the period was due, which is NOT when it ran: a run that catches up after three
  // days of downtime keeps the period's own schedule here and records the wall clock in executed_at.
  // The gap between the two is the "decay silently didn't run because cron wasn't wired" that
  // .claude/rules/jobs-and-events.md designs out — it is visible rather than inferred.
  column "scheduled_for_at" {
    null = false
    type = integer
  }

  // Micros, NULL until the run reaches a terminal state. NULL is "not yet", which is a fact about
  // the row and not a zero.
  column "executed_at" {
    null = true
    type = integer
  }

  // The values are internal/decay/kinds' and the CHECK below is generated from it — this comment
  // deliberately does not restate them, because a prose list beside a generated one is the drift the
  // catalogue exists to remove. The DEFAULT is the one value written twice: it is a column
  // attribute rather than a check block, so neither `make gen` nor ENUM001 reads it, and
  // TestDecayKinds_SchemaDefault_MatchesTheCatalogue is what ties it back to kinds.DefaultState().
  column "state" {
    null    = false
    type    = text
    default = "planned"
  }

  // What a dry run computed, for the officer to approve before anything moves: per-account deltas,
  // totals, clamped floors. Read whole and never queried into (canonical §8) — the authoritative
  // record of what actually happened is the ledger batch, not this document.
  column "dry_run_result_json" {
    null    = false
    type    = text
    default = "{}"
  }

  // The strategy configuration this run was computed AGAINST, snapshotted at run time. A pool's
  // decay rule can change between the period and the run (that change is a pool_config_change row
  // above), and without the snapshot "why was March's decay 10% when the rule says 5%?" is
  // unanswerable.
  column "config_snapshot_json" {
    null    = false
    type    = text
    default = "{}"
  }

  // Real FK -> ledger_batch(id). The batch this run posted; NULL until state = 'committed'. This is
  // the join that makes a decay traceable in both directions — from the run to the points it moved,
  // and from a member's statement line back to the period that produced it.
  column "ledger_batch_id" {
    null = true
    type = text
  }

  // Real FK -> app_user(id) since #277, which released the deferral 000009 made good on — and NULL is
  // still the correct value for the periodic job, which is the expected trigger.
  column "triggered_by" {
    null = true
    type = text
  }

  // Why a failed run failed, for the officer reading /admin/jobs. Defaults to '' for the reason
  // pool_config_change.reason does: NULL and '' would be two spellings of "no error".
  column "error" {
    null    = false
    type    = text
    default = ""
  }

  column "created_at" {
    null = false
    type = integer
  }

  // Present here where pool_config_change has none, and the difference is the point: a decay_run IS
  // mutable — planned becomes preview becomes committed — where a config-change event is not.
  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "decay_run_pool" {
    columns     = [column.pool_id]
    ref_columns = [table.pool.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // dkp:fk-uncovered ledger_batch is append-only, so this lookup can never run (#274).
  //
  // The same waiver audit_log_batch carries, for the same parent and the same reason: the two
  // append-only triggers abort every UPDATE and DELETE of a batch, so nothing can provoke the child
  // scan an index here would serve.
  foreign_key "decay_run_batch" {
    columns     = [column.ledger_batch_id]
    ref_columns = [table.ledger_batch.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // SET NULL, for the reason role_assignment_granted_by carries in full: a run triggered by an
  // officer who has since been erased is still a run that happened, and the points it posted are in
  // the ledger either way. NULL is already this column's spelling of "the periodic job did it".
  foreign_key "decay_run_triggered_by" {
    columns     = [column.triggered_by]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  // BEGIN GENERATED — decay_run enum CHECKs, from internal/decay/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestDecayKinds_CheckMatchesCatalogue fails on.
  check "decay_run_kind_enum" {
    expr = "kind IN ('decay', 'cap', 'start_points')"
  }

  check "decay_run_state_enum" {
    expr = "state IN ('planned', 'preview', 'committed', 'skipped', 'failed')"
  }
  // END GENERATED — decay_run enum CHECKs.

  // Times are Micros and never negative; the same shape as ledger_batch_times_nonneg. executed_at is
  // omitted because it is nullable — `x >= 0` is NULL rather than true for a run that has not
  // executed, and SQLite admits a row only when a CHECK is not false, so including it would happen
  // to work while saying something it does not mean.
  check "decay_run_times_nonneg" {
    expr = "scheduled_for_at >= 0 AND created_at >= 0 AND updated_at >= 0"
  }

  // THE IDEMPOTENCY KEY (issue #192, scoped by ADR-0024). One run per (pool, family, period),
  // enforced by the database rather than by a read-then-write in Go — three ordinary callers race
  // for it: the periodic job firing twice after a restart, a retry after a partial failure, and an
  // officer clicking "run decay now" mid-flight.
  //
  // KIND IS INSIDE THE INDEX, not beside it, and that is the whole of #206. All three cadence
  // families share one cadence vocabulary and one run table; without kind in
  // here, a cap run for '2026-W31' violates an index built to stop a REPEAT, the job concludes
  // "already done" and exits 0, and the cap silently never applies.
  //
  // NOT partial and NOT nullable on any column: a period with no row has not run, and a row that
  // exists — in ANY state, including 'skipped' and 'failed' — means that period has been decided.
  // Re-running is not "delete the row and try again": that is the double-decay the key exists to
  // prevent, and a failed run is corrected by a new period or by a reversal batch, never by
  // rewriting history.
  index "ux_decay_period" {
    unique  = true
    columns = [column.pool_id, column.kind, column.cadence_period]
  }

  // The operator's read: this pool's runs, due-date order. Serves both "what is coming up" and the
  // catch-up scan a periodic job does after downtime. Not kind-scoped — the dashboard shows the
  // pool's schedule, and a family filter over one pool's runs is a cheap residual.
  index "ix_decay_pool" {
    columns = [column.pool_id, column.scheduled_for_at]
  }

  // The covering index for decay_run_triggered_by (#274, #277). One row per pool per cadence period
  // is a slow-growing table, and the alternative to the index is that erasing a user scans every
  // decay run the guild has ever performed.
  index "ix_decay_triggered_by" {
    columns = [column.triggered_by]
  }

  strict = true
}

// permission — the authorization catalogue, RECONCILED FROM CODE at every boot.
//
// The single source is internal/authz/catalogue.go (canonical §6, docs/design/01-domain-model.md §5):
// this table is its projection, not a second catalogue. internal/authz.Reconcile upserts every key on
// the boot path, stamps orphaned_at on a row the running binary no longer ships, and NEVER deletes —
// see the column comment for why that asymmetry is the whole design.
//
// WHY THE TABLE EXISTS AT ALL, given the catalogue is in Go: role_permission is FK-constrained to
// permission(key), which is what makes a divergent permission list a boot failure rather than a style
// issue. A role granted a key the binary does not implement cannot be written; a key the binary
// implements and the table lacks cannot be granted. Neither is expressible with a Go slice.
//
// NO admin:* AND NO SUPERADMIN (ADR-0011). admin.owner is an ORDINARY ROW here, held by at least one
// account through an ordinary role_assignment, and evaluated by the same code path as roster.read.
// EQdkp's `group_id = 2 short-circuits the ACL` is the named anti-pattern this shape exists to refuse.
table "permission" {
  schema = schema.main

  // The <resource>.<action> key, and the primary key: it is stable, meaningful, written by hand in Go
  // and referenced by name from role_permission. A ULID here would add a join to every grant and take
  // away the property that makes `sqlite3 dkp.db` a usable debugging tool for an officer at 1 a.m.
  column "key" {
    null = false
    type = text
  }

  // The display grouping the role editor and the authorization matrix render — 'roster', 'bidding',
  // 'administration'. NOT a security boundary: canonical §6 spends a paragraph on ops.read sitting in
  // the 'sensitive' category while deliberately NOT being in the capability floor, because membership
  // is decided by what a compromise costs and not by which heading a key is filed under.
  column "category" {
    null = false
    type = text
  }

  // The short human name, and the one sentence an officer reads when deciding whether a role should
  // hold this key. Both are catalogue text rather than UI strings: the role editor, the matrix and
  // docs/reference/permissions.md all render them, and three copies of a sentence is how they drift.
  column "label" {
    null = false
    type = text
  }

  column "description" {
    null = false
    type = text
  }

  // An affordance, not a control: the role editor confirms twice and the matrix marks the row.
  // Nothing about authorization changes. Exactly one key carries it today — bid.reveal_early
  // (docs/design/01-domain-model.md:455) — and the catalogue's test pins that set, because a guessed
  // dangerous flag trains officers to click through the confirmation that matters.
  column "is_dangerous" {
    null    = false
    type    = integer
    default = 0
  }

  // Canonical §6's capability floor: this operation alters authentication, authorization or
  // bulk-export state, so it is session-and-step-up only and carries no PAT scope at all. It is the
  // column the middleware reads (docs/design/01-domain-model.md §5), and internal/authz.CapabilityFloor
  // is what fills it — a catalogue test compares the two in both directions.
  column "requires_step_up" {
    null    = false
    type    = integer
    default = 0
  }

  // Micros, and the reason this table is reconciled rather than seeded. A key that exists in the
  // database and not in the running binary is a DOWNGRADE — an officer rolling back after a bad
  // upgrade — and deleting the row would cascade into the role_permission grants that reference it,
  // silently stripping capability from every role that held it and never restoring it on the way back
  // up. So the row is marked instead: FK integrity survives, the grant survives, and re-upgrading
  // clears the stamp. NULL means live.
  column "orphaned_at" {
    null = true
    type = integer
  }

  // Display order for the role editor and the matrix, 1-based, derived from canonical §6's order.
  // Rewritten on every boot with the rest of the row; nothing joins on it.
  column "sort_order" {
    null    = false
    type    = integer
    default = 0
  }

  primary_key {
    columns = [column.key]
  }

  // Booleans are INTEGER + CHECK (canonical §8). Not a string enum, so ENUM001 does not read these and
  // no catalogue owns them.
  check "permission_is_dangerous_bool" {
    expr = "is_dangerous IN (0, 1)"
  }

  check "permission_requires_step_up_bool" {
    expr = "requires_step_up IN (0, 1)"
  }

  // WITHOUT ROWID: the key IS the row. Fifty-eight rows, always read by key or in full, with no
  // integer id anyone ever uses — the same argument dkp_meta records.
  without_rowid = true
  strict        = true
}

// role — a named bundle of permissions (docs/design/01-domain-model.md §5).
//
// ALLOW-ONLY, SET UNION, NO DENY. Deny plus union is a lattice: adding a role can REMOVE capability
// and evaluation order becomes load-bearing. The two things deny gets used for have better answers
// here — temporary revocation is role_assignment.suspended_until_at, and "this one person must not
// touch loot" is "do not grant the role, or split the role".
//
// BUILT-IN ROLES ARE ROWS, not code. `key` non-NULL marks one: not deletable and not renamable, but
// otherwise an ordinary row evaluated by an ordinary query. The seed itself (guest, member, raider,
// raid_leader, officer, admin, owner, bot_readonly, bot_raid) lands with the auth tables that make an
// assignment possible — a role nobody can be assigned to is a row with no effect.
table "role" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // Non-NULL marks a BUILT-IN role and names it stably for code that has to find one ('owner'), which
  // is why it is a separate column from the officer-editable name. NULL for a guild's own roles: there
  // is nothing for code to look them up by, and a unique index over the non-NULL half is what keeps
  // 'owner' singular without forbidding a hundred custom roles.
  column "key" {
    null = true
    type = text
  }

  // The officer-facing name and its normalised form. name_norm is normalised IN GO (NFKC + casefold +
  // strip ' ` -), a plain column and never a generated one — canonical §8 gives the reason: core
  // SQLite has no NFKC, lower() is ASCII-only, and ALTER TABLE cannot add a STORED column, so every
  // future normalisation change would force a 12-step rebuild.
  column "name" {
    null = false
    type = text
  }

  column "name_norm" {
    null = false
    type = text
  }

  column "description" {
    null    = false
    type    = text
    default = ""
  }

  column "is_builtin" {
    null    = false
    type    = integer
    default = 0
  }

  // WHICH KIND OF PRINCIPAL may hold this role. The values are internal/authz/role/kinds' and the
  // CHECK below is generated from it — this comment deliberately does not restate them, because a
  // prose list beside a generated one is the drift the catalogue exists to remove.
  column "applies_to" {
    null    = false
    type    = text
    default = "both"
  }

  column "sort_order" {
    null    = false
    type    = integer
    default = 0
  }

  // SOFT DELETE, and it is about the grants rather than about undo: role_permission and
  // role_assignment cascade on a hard DELETE, so removing a role would silently strip capability from
  // everyone holding it with no record of what they had. A deleted role keeps its rows and stops being
  // assignable.
  column "deleted_at" {
    null = true
    type = integer
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // BEGIN GENERATED — role enum CHECK, from internal/authz/role/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestRoleKinds_CheckMatchesCatalogue fails on.
  check "role_applies_to_enum" {
    expr = "applies_to IN ('user', 'service_account', 'both')"
  }
  // END GENERATED — role enum CHECK.

  check "role_is_builtin_bool" {
    expr = "is_builtin IN (0, 1)"
  }

  // One row per built-in key, partial so the NULL side — every custom role — never collides.
  index "ux_role_key" {
    unique  = true
    columns = [column.key]
    where   = "key IS NOT NULL"
  }

  // One live role per normalised name. Partial over the undeleted rows, so a name freed by a soft
  // delete can be used again and the deleted row keeps its own.
  index "ux_role_name" {
    unique  = true
    columns = [column.name_norm]
    where   = "deleted_at IS NULL"
  }

  strict = true
}

// role_permission — which permissions a role grants. A pure junction, hard-deleted and cascading
// (docs/design/01-domain-model.md §22): it has no independent identity and an ungranted permission is
// an absent row, never a row saying no.
//
// THE FK TO permission(key) IS THE POINT OF THE WHOLE TABLE. Canonical §6: "role_permission is
// FK-constrained to permission(key), so a divergent list is a boot failure, not a style issue." Every
// document that says "adding a permission key is a schema change — stop and ask" is relying on this
// constraint existing, and until Phase 2 it did not.
table "role_permission" {
  schema = schema.main

  column "role_id" {
    null = false
    type = text
  }

  column "permission_key" {
    null = false
    type = text
  }

  primary_key {
    columns = [column.role_id, column.permission_key]
  }

  // CASCADE: a deleted role's grants go with it. Reaching this requires a hard DELETE, which the
  // product does not do — role.deleted_at is the soft delete — so the cascade is for `dkp` maintenance
  // and for the import, not for the role editor.
  foreign_key "role_permission_role" {
    columns     = [column.role_id]
    ref_columns = [table.role.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  // NO ACTION, deliberately, and the contrast with the line above is the design: a permission row is
  // never deleted (see permission.orphaned_at), and if one ever were, taking every grant with it would
  // silently strip capability from every role. Refusing the delete is the correct outcome.
  foreign_key "role_permission_permission" {
    columns     = [column.permission_key]
    ref_columns = [table.permission.column.key]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // THE COVERING INDEX FOR THAT FOREIGN KEY, and it is the NO ACTION above that needs it (#271). The
  // primary key is (role_id, permission_key) and SQLite cannot use a left-prefixed key to find rows by
  // the second column, so without this index every enforcement of "a permission row is never deleted"
  // reads the whole of role_permission — inside the write transaction, holding the single write
  // connection's lock. The FK is deliberately the authorization safety boundary; making it cost a table
  // scan is how a boundary stops being one that anybody keeps.
  //
  // Non-unique, because one permission key is granted to many roles. It is the same index the role
  // editor's "which roles hold this key?" read wants, so it is not carrying the FK alone.
  index "ix_role_permission_permission" {
    columns = [column.permission_key]
  }

  without_rowid = true
  strict        = true
}

// role_assignment — who holds a role, and how far it reaches
// (docs/design/01-domain-model.md §5).
//
// THE SCOPE PAIR IS WHY THIS IS NOT A TWO-COLUMN JUNCTION. EQdkp expressed "raid leader, but only for
// the Tuesday group" as two hardcoded *_grpleader permissions; here it is any role plus a
// (scope_type, scope_id) pair, so the same role serves a global officer and a Tuesday-group raid
// leader without a second permission key.
//
// THERE IS NO guild_id COLUMN and there will not be one (ADR-0004, canonical §9). Scope is a pool or a
// raid group, never a tenant.
table "role_assignment" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // The polymorphic subject: a user or a service account. No foreign key — SQLite has no polymorphic
  // reference — so the kind column is what makes the id resolvable, and its values are
  // internal/authz/roleassignment/kinds' (CHECK generated below).
  column "subject_kind" {
    null = false
    type = text
  }

  column "subject_id" {
    null = false
    type = text
  }

  column "role_id" {
    null = false
    type = text
  }

  // How far the assignment reaches, and what it reaches. scope_id is NULL exactly when the scope is
  // global — the paired CHECK below makes that an equivalence rather than a convention — and carries
  // no foreign key because the target is a pool for one scope_type and a raid group for another.
  column "scope_type" {
    null    = false
    type    = text
    default = "global"
  }

  column "scope_id" {
    null = true
    type = text
  }

  // TEMPORARY REVOCATION, and the reason this schema needs no deny rule: an officer on leave, or one
  // under review, is suspended until a date rather than having their role deleted and re-created from
  // memory. Micros; NULL means not suspended.
  column "suspended_until_at" {
    null = true
    type = integer
  }

  // Real FK -> app_user(id) since #277; the deferral this column carried was released when
  // 000009_auth_identity_and_credentials.sql created app_user. NULL for a grant no user made — the
  // bootstrap owner, a Discord sync, an import.
  column "granted_by" {
    null = true
    type = text
  }

  // Provenance, and it is a real column because each value has a different revocation story: a
  // discord_sync grant is rewritten by the next sync, an import grant is the first thing to audit
  // after a migration, and a bootstrap grant is the one nobody may be left without. Values are
  // internal/authz/roleassignment/kinds'.
  column "granted_via" {
    null    = false
    type    = text
    default = "manual"
  }

  // Micros. NULL means the assignment does not expire; the effective-permission query compares it
  // against now rather than relying on a sweep, so an expired grant stops working at the moment it
  // expires whether or not a job has run.
  column "expires_at" {
    null = true
    type = integer
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "role_assignment_role" {
    columns     = [column.role_id]
    ref_columns = [table.role.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  // SET NULL, AND THE CONTRAST WITH service_account_owner IS THE DESIGN (#277). That one is
  // NO ACTION because a bot whose owner leaves must not leave with them — the owner is a LIVE
  // capability, and refusing the delete is what forces the reassignment conversation. This is the
  // opposite case: who granted a role is HISTORY, and the grant does not stop being valid because
  // the officer who made it was erased. Refusing here would mean a user row could never be removed
  // once they had granted anything, so the erasure path would end in hand-editing the very table
  // that records who holds power — and NULL is already this column's spelling of "no user did this"
  // (the bootstrap owner, a Discord sync, an import), so the erased case reads as the shape the
  // column already has rather than as a new one.
  //
  // The product's own delete is app_user.deleted_at; reaching this cascade takes a hard DELETE, so
  // it is for `dkp` maintenance and for erasure, not for the user admin screen.
  foreign_key "role_assignment_granted_by" {
    columns     = [column.granted_by]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  // BEGIN GENERATED — role_assignment enum CHECKs, from internal/authz/roleassignment/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestRoleAssignmentKinds_CheckMatchesCatalogue fails on.
  check "role_assignment_subject_kind_enum" {
    expr = "subject_kind IN ('user', 'service_account')"
  }

  check "role_assignment_scope_type_enum" {
    expr = "scope_type IN ('global', 'pool', 'raid_group')"
  }

  check "role_assignment_granted_via_enum" {
    expr = "granted_via IN ('manual', 'invitation', 'discord_sync', 'import', 'bootstrap')"
  }
  // END GENERATED — role_assignment enum CHECKs.

  // The scope pair, as an EQUIVALENCE in both directions: a global assignment has no scope_id, and a
  // scoped one must have one. Written as `(a) = (b)` rather than as two implications because SQLite
  // admits a row whose CHECK is NULL, and the equality of two boolean expressions over NOT NULL and
  // IS NULL is never NULL.
  check "role_assignment_scope_shape" {
    expr = "((scope_type = 'global') = (scope_id IS NULL))"
  }

  // One assignment per (subject, role, scope). COALESCE because a UNIQUE index treats NULLs as
  // distinct in SQLite, so without it a subject could be granted the same global role any number of
  // times — and the duplicates would each be a row the role editor has to render and an officer has to
  // revoke separately.
  index "ux_role_assign" {
    unique = true
    on {
      column = column.subject_kind
    }
    on {
      column = column.subject_id
    }
    on {
      column = column.role_id
    }
    on {
      column = column.scope_type
    }
    on {
      expr = "COALESCE(scope_id, '')"
    }
  }

  // The authorization read: every assignment a principal holds, which the middleware resolves once per
  // request and caches on the Principal.
  index "ix_role_assign_subject" {
    columns = [column.subject_kind, column.subject_id]
  }

  // THE COVERING INDEX FOR role_assignment_role (#274), and of the five findings in that issue this is
  // the one to look at first: it is the CASCADE. Deleting a role has to find every assignment that
  // holds it, and the only key starting with role_id was ux_role_assign — which starts with
  // subject_kind — so the cascade read the whole assignment table, inside the write transaction, on
  // two tables that both grow with the guild rather than staying constant.
  //
  // It is also the role editor's "who holds this role?", which is the list an officer sees before
  // deleting one, so it is not carrying the foreign key alone.
  index "ix_role_assign_role" {
    columns = [column.role_id]
  }

  // The covering index for role_assignment_granted_by (#274, #277). SET NULL is enforced the same way
  // CASCADE is — SQLite looks up the child rows — so wiring the foreign key above without this would
  // have made erasing one user a full scan of every grant ever made.
  index "ix_role_assign_granted_by" {
    columns = [column.granted_by]
  }

  strict = true
}

// app_user — a human login (docs/design/01-domain-model.md §4.1, docs/design/03-security.md §3).
//
// THE PERSON AND THE LOGIN ARE DIFFERENT THINGS, and keeping them apart is what makes an EQdkp
// import possible at all: `person` is a roster entry with DKP history and exists for members who
// have never logged in and never will; app_user is a credential holder. The importer creates the
// former in bulk and the latter never — passwords are never migrated (AGENTS.md), so a migrating
// guild's members arrive as roster rows and claim their login afterwards.
//
// NO PASSWORD COLUMN HERE. The credential lives on user_identity, one row per way of proving you are
// this user — a local password, a Discord account, an OIDC subject — because §3.4 requires the
// credential table to be polymorphic from day one so that WebAuthn is not a retrofit, and because
// "unlinking is blocked if it would leave the account with no usable credential" has to be a query
// over rows rather than a special case over columns.
//
// NO avatar_media_id YET. The domain model's DDL carries one referencing media(id); that table
// arrives with internal/cms, and a column whose foreign key cannot be declared is a column that
// silently accepts a dangling id. It lands with the table it points at.
table "app_user" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // The name typed at a login prompt, and its normalised twin. Normalised IN GO (NFKC + casefold +
  // strip ' ` -) rather than by a generated column or a COLLATE: core SQLite has no NFKC and lower()
  // is ASCII-only, so an accented or full-width homoglyph of an officer's name would otherwise be a
  // distinct username that looks identical in every list an officer reads (canonical §8).
  column "username" {
    null = false
    type = text
  }

  column "username_norm" {
    null = false
    type = text
  }

  // NULLABLE, and that is a deliberate product decision rather than laziness: SMTP is optional
  // (§3.7), a guild with no mail server is not locked out, and `dkp admin reset-password` prints a
  // one-time link instead. An account with no address simply has no email recovery path.
  column "email" {
    null = true
    type = text
  }

  column "email_norm" {
    null = true
    type = text
  }

  // Micros. NULL means unverified, and an unverified address is never trusted for authorization —
  // §3.5's second takeover hazard is the attacker who registers a Discord account carrying an
  // officer's email address and expects to be merged into it.
  column "email_verified_at" {
    null = true
    type = integer
  }

  column "display_name" {
    null    = false
    type    = text
    default = ""
  }

  // NULL means "inherit the guild's", for both. A per-user timezone is what makes a raid time render
  // correctly for the member three zones away, and a NULL is how the guild's value keeps applying
  // when it changes rather than being copied into every row at signup.
  column "timezone" {
    null = true
    type = text
  }

  column "locale" {
    null = true
    type = text
  }

  // Values are internal/auth/appuser/kinds' (CHECK generated below); the default is
  // kinds.DefaultState(), restated here because a column default is a catalogue value written where
  // `make gen` does not rewrite — TestAppUserKinds_SchemaDefault_MatchesTheCatalogue ties them.
  column "state" {
    null    = false
    type    = text
    default = "active"
  }

  // SIGN OUT EVERYWHERE, IN ONE WRITE (§3.6). Every session row records the epoch it was minted
  // under; the resolver requires the two to be equal, so incrementing this invalidates every session
  // the user has, on every device, in a single UPDATE that cannot race a session being created
  // concurrently — the new session simply carries the new epoch. Bumped on password change, MFA
  // disable, OAuth unlink, role change and deactivation.
  //
  // The alternative — UPDATE session SET revoked_at WHERE user_id = ? — is the same number of
  // statements and touches N rows in the table the auth hot path reads. This is one integer on a row
  // that path already joins.
  //
  // NOT IN the domain model's DDL, which predates §3.6's mechanism being written down; both halves
  // (this column and session.session_epoch) are named there now.
  column "session_epoch" {
    null    = false
    type    = integer
    default = 0
  }

  column "last_login_at" {
    null = true
    type = integer
  }

  // The progressive-delay counter of §3.3, reset on a successful login. Not a lockout counter:
  // hard lockout is OFF by default, because a lockout is a denial of service an attacker aims at
  // every officer thirty minutes before a raid.
  column "failed_logins" {
    null    = false
    type    = integer
    default = 0
  }

  column "locked_until_at" {
    null = true
    type = integer
  }

  // TOTP (§3.4, Wave 2). The secret is ENCRYPTED, not hashed — AES-256-GCM under an HKDF subkey with
  // the user's ULID as AAD — because a TOTP seed has to be readable to verify a code, which is the
  // opposite of a password. The columns ship with the table: adding them later is an ALTER, but
  // adding mfa_required's CHECK later is a 12-step rebuild of the busiest auth table in the product.
  column "mfa_totp_secret_enc" {
    null = true
    type = blob
  }

  column "mfa_enrolled_at" {
    null = true
    type = integer
  }

  column "mfa_required" {
    null    = false
    type    = integer
    default = 0
  }

  // Micros. Soft delete, because app_user is referenced by ledger batches, audit rows and role
  // assignments that must survive the person leaving — the audit trail's whole value is that it still
  // names who did it. The two unique indexes are PARTIAL over this column so a deleted username and
  // email become available again.
  column "deleted_at" {
    null = true
    type = integer
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // BEGIN GENERATED — app_user enum CHECK, from internal/auth/appuser/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestAppUserKinds_CheckMatchesCatalogue fails on.
  check "app_user_state_enum" {
    expr = "state IN ('pending', 'active', 'suspended', 'disabled')"
  }
  // END GENERATED — app_user enum CHECK.

  // Booleans are INTEGER + CHECK (canonical §8). Not a string enum, so ENUM001 does not read this and
  // no catalogue owns it.
  check "app_user_mfa_required_bool" {
    expr = "mfa_required IN (0, 1)"
  }

  index "ux_user_username" {
    unique  = true
    columns = [column.username_norm]
    where   = "deleted_at IS NULL"
  }

  index "ux_user_email" {
    unique  = true
    columns = [column.email_norm]
    where   = "email_norm IS NOT NULL AND deleted_at IS NULL"
  }

  strict = true
}

// user_identity — one way of proving you are an app_user (docs/design/01-domain-model.md §4.1).
//
// POLYMORPHIC BY ROW, not by nullable column sets: a local password, a Discord account and a generic
// OIDC subject are three rows, and WebAuthn after 1.0 is a fourth. §3.4 requires this shape from day
// one so passkeys are not a retrofit, and it is what makes "unlinking is blocked if it would leave
// the account with no usable credential" (§3.5 rule 4) a COUNT rather than a special case.
//
// IDENTITY IS THE PROVIDER'S ID, NEVER THE HANDLE. Discord handles became changeable and REUSABLE
// after the 2023 pomelo migration, so keying on one hands the account to whoever claims the released
// name. The unique index is (provider, provider_key, subject) and subject is the snowflake, the OIDC
// `sub`, or — for a local identity — the username_norm.
//
// PASSWORDS ARE NEVER MIGRATED FROM EQdkp (AGENTS.md). The source population mixes seven verifiers;
// the importer sets password_hash = NULL and must_reset = 1 and mints claim invitations, which is why
// password_algo has exactly one legal value and why NULL means "login disabled" rather than "no
// password yet".
table "user_identity" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  column "user_id" {
    null = false
    type = text
  }

  // Values are internal/auth/useridentity/kinds' (CHECK generated below).
  column "provider" {
    null = false
    type = text
  }

  // The OIDC issuer discriminator — one instance may federate with more than one issuer, and two
  // issuers can each mint a subject called "1". Empty for local and Discord, never NULL, so the
  // unique index below has no NULL component: SQLite treats NULLs as distinct, which would let the
  // same Discord account be linked to every user in the guild.
  column "provider_key" {
    null    = false
    type    = text
    default = ""
  }

  column "subject" {
    null = false
    type = text
  }

  // The argon2id PHC string (§3.1): $argon2id$v=19$m=19456,t=2,p=1$<salt>$<tag>. PARAMETERS TRAVEL
  // WITH THE HASH, which is what makes rehash-on-login possible when the cost profile changes.
  // NULL ⇒ this identity cannot authenticate with a password.
  //
  // NO PEPPER, deliberately (§3.1). A pepper helps only against a DB-only leak, which for a
  // single-file SQLite deployment is nearly the same event as a filesystem leak, and it makes
  // rotation require every user to re-authenticate. Tokens ARE peppered because they need a fast
  // keyed hash, not a slow one. This is written down so it is not "fixed" later by someone who read
  // a blog post.
  column "password_hash" {
    null = true
    type = text
  }

  // Values are internal/auth/useridentity/kinds' (CHECK generated below, in the nullable form).
  column "password_algo" {
    null = true
    type = text
  }

  column "password_set_at" {
    null = true
    type = integer
  }

  column "must_reset" {
    null    = false
    type    = integer
    default = 0
  }

  // Provider tokens, encrypted with AES-256-GCM under an HKDF subkey (§9.1). Stored ONLY when
  // Discord role sync is enabled (§3.5) — a provider refresh token is a credential for someone
  // else's system, and the default is not to hold one at all.
  column "access_token_enc" {
    null = true
    type = blob
  }

  column "refresh_token_enc" {
    null = true
    type = blob
  }

  column "token_expires_at" {
    null = true
    type = integer
  }

  // Space-separated, as GRANTED by the provider — not as requested. Never queried into.
  column "scopes" {
    null    = false
    type    = text
    default = ""
  }

  // Last-seen avatar, global_name and guild roles. A cache of the provider's view, rendered in the
  // UI and never authoritative: §3.5 rule 7 says a changed handle is surfaced, never acted upon.
  column "profile_json" {
    null    = false
    type    = text
    default = "{}"
  }

  column "last_used_at" {
    null = true
    type = integer
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // CASCADE, and this is the one place in the auth schema where a delete is the right answer: a
  // credential for a user that no longer exists is not history, it is a live way in.
  foreign_key "user_identity_user" {
    columns     = [column.user_id]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  // BEGIN GENERATED — user_identity enum CHECKs, from internal/auth/useridentity/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestUserIdentityKinds_CheckMatchesCatalogue fails on.
  check "user_identity_provider_enum" {
    expr = "provider IN ('local', 'discord', 'oidc')"
  }

  check "user_identity_password_algo_enum" {
    expr = "password_algo IS NULL OR password_algo IN ('argon2id')"
  }
  // END GENERATED — user_identity enum CHECKs.

  check "user_identity_must_reset_bool" {
    expr = "must_reset IN (0, 1)"
  }

  // THE ANTI-TAKEOVER INDEX. One identity per (provider, issuer, subject) across the whole instance,
  // so a Discord account can be linked to exactly one DKP user and a released handle carries nothing
  // with it.
  index "ux_identity_subject" {
    unique  = true
    columns = [column.provider, column.provider_key, column.subject]
  }

  index "ix_identity_user" {
    columns = [column.user_id]
  }

  strict = true
}

// session — a browser login, opaque and server-side (docs/design/01-domain-model.md §4.2,
// docs/design/03-security.md §3.6).
//
// THE ROW ID IS NOT THE COOKIE. The cookie carries 32 random bytes; this table stores their SHA-256
// and nothing else, so a read-only database leak yields no live session. The id is an ordinary ULID
// used by the session-list UI and by revocation.
//
// NOT A JWT, and that is the decision this table records. A single-process app gains nothing from
// stateless sessions and loses instant revocation — the property that makes a stolen cookie a
// support conversation rather than an incident. The only signed bearer in the product is the
// 30-second SSE handshake ticket.
//
// UNKEYED SHA-256 HERE, HMAC ON api_token, and the asymmetry is deliberate. A session secret is
// verified by exact hash lookup and never leaves the browser; a PAT is pasted into bot configs and
// Discord DMs, so its hash is keyed with a server pepper (§6.1) that a database-only leak does not
// contain.
table "session" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  column "user_id" {
    null = false
    type = text
  }

  // SHA-256 of the 32-byte cookie secret. BLOB rather than hex TEXT: it is 32 bytes instead of 64 on
  // the index the auth hot path reads on every request, and there is nothing to read by eye.
  column "token_hash" {
    null = false
    type = blob
  }

  // WHICH credential opened this session — the local password, or a Discord identity. Nullable
  // because a session may outlive the identity it was opened with (an unlink), and because the
  // first-run bootstrap opens one before any identity row is the answer to anything.
  column "identity_id" {
    null = true
    type = text
  }

  // The app_user.session_epoch this session was minted under. The resolver requires the two to be
  // equal, which is what makes "sign out everywhere" one UPDATE on the user row (§3.6).
  column "session_epoch" {
    null    = false
    type    = integer
    default = 0
  }

  column "created_at" {
    null = false
    type = integer
  }

  // Advanced by the resolver, THROTTLED (internal/auth): a write per request on SQLite's single
  // writer would put the auth path in the same queue as raid-night awards.
  column "last_seen_at" {
    null = false
    type = integer
  }

  // Idle expiry: 14 days, extended on use. The absolute ceiling below is not extendable, so a session
  // kept alive by a polling bot still ends.
  column "expires_at" {
    null = false
    type = integer
  }

  column "absolute_expires_at" {
    null = false
    type = integer
  }

  column "revoked_at" {
    null = true
    type = integer
  }

  // The session-list UI's "where was this signed in from". EMPTY UNTIL DKP_TRUSTED_PROXIES EXISTS
  // (issue #98): behind the reverse proxy this project recommends, every request arrives from
  // 127.0.0.1, and recording a spoofable X-Forwarded-For as if it were a fact would put a lie in the
  // one screen a member checks after a stolen-session scare.
  column "ip" {
    null    = false
    type    = text
    default = ""
  }

  column "user_agent" {
    null    = false
    type    = text
    default = ""
  }

  // THE STEP-UP CLOCK (§3.4). Minting a token, editing a role, downloading a backup and committing
  // an import all require re-authentication within five minutes; this is the instant that window is
  // measured from. NULL means no step-up has happened on this session.
  column "mfa_satisfied_at" {
    null = true
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "session_user" {
    columns     = [column.user_id]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  foreign_key "session_identity" {
    columns     = [column.identity_id]
    ref_columns = [table.user_identity.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  // The auth hot path: one indexed lookup per cookie-bearing request.
  index "ux_session_token" {
    unique  = true
    columns = [column.token_hash]
  }

  // The session-list read, the sweep that deletes expired rows, AND the covering index for
  // session_user (#274).
  //
  // IT LOST ITS `WHERE revoked_at IS NULL` PREDICATE TO CARRY THE FOREIGN KEY, and that is the whole
  // of the change: SQLite's foreign-key enforcement finds the child rows through the ordinary query
  // planner, and the planner may only use a partial index when the query implies its predicate. A
  // CASCADE delete's lookup is `user_id = <the deleted user>` and implies nothing about revoked_at, so
  // the partial form covered the read and left the cascade scanning the whole session table.
  //
  // Widened rather than joined by a second plain index on user_id, which was the other way to satisfy
  // the gate: two indexes on the same leading column would be two B-tree writes per login instead of
  // one, forever, to save the list read from skipping the rows a revoked session leaves behind. The
  // predicate was an optimisation; the cascade is a correctness cost, and one index now serves both.
  index "ix_session_user" {
    columns = [column.user_id, column.expires_at]
  }

  // The covering index for session_identity (#274). SET NULL: unlinking a Discord identity has to
  // find the sessions it opened, and without this that is a scan of every session ever created —
  // this table is never DELETEd from (db/queries/auth.sql: a session is revoked, never removed), so
  // "every session ever created" is what the table holds.
  index "ix_session_identity" {
    columns = [column.identity_id]
  }

  strict = true
}

// service_account — a bot identity (docs/design/01-domain-model.md §4.3, ADR-0011).
//
// TOKENS BELONG TO SERVICE ACCOUNTS, NOT PEOPLE, and that is the single most load-bearing sentence in
// the token design. A service account has a human owner_user_id for audit and notification, but
// revoking the human does not kill the bot — the bot dying mid-raid because an officer quit is the
// most predictable failure mode in guild tooling, and EQdkp's api_key, which impersonates the first
// superadmin, is the incumbent's version of it.
table "service_account" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  column "name" {
    null = false
    type = text
  }

  column "name_norm" {
    null = false
    type = text
  }

  column "description" {
    null    = false
    type    = text
    default = ""
  }

  // A HUMAN, for audit and notification. NOT NULL: every bot has somebody answerable for it. §6.2's
  // "orphaned" flag is derived from this user's state, not stored — a bot can be orphaned and
  // disabled at once, and a state column could only say one of them.
  column "owner_user_id" {
    null = false
    type = text
  }

  // Values are internal/auth/serviceaccount/kinds' (CHECK generated below); the default is
  // kinds.DefaultState().
  column "state" {
    null    = false
    type    = text
    default = "active"
  }

  column "created_by" {
    null = false
    type = text
  }

  column "created_at" {
    null = false
    type = integer
  }

  column "updated_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // NO_ACTION on both, where user_identity CASCADEs. Deleting a user must FAIL while they still own
  // or created a bot, rather than silently taking the guild's raid bot with them: reassignment is an
  // officer's decision, and the FK is what forces the conversation.
  foreign_key "service_account_owner" {
    columns     = [column.owner_user_id]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  foreign_key "service_account_creator" {
    columns     = [column.created_by]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  // BEGIN GENERATED — service_account enum CHECK, from internal/auth/serviceaccount/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestServiceAccountKinds_CheckMatchesCatalogue fails on.
  check "service_account_state_enum" {
    expr = "state IN ('active', 'disabled')"
  }
  // END GENERATED — service_account enum CHECK.

  index "ux_service_account_name" {
    unique  = true
    columns = [column.name_norm]
  }

  // The covering indexes for the two foreign keys above (#274). NO ACTION is enforced by looking the
  // child rows up, so without these the delete that is SUPPOSED to fail — the officer who still owns
  // the raid bot — fails only after scanning the table, and the "list this user's bots" screen that
  // makes the refusal actionable scans it too.
  index "ix_service_account_owner" {
    columns = [column.owner_user_id]
  }

  index "ix_service_account_creator" {
    columns = [column.created_by]
  }

  strict = true
}

// api_token — an opaque, scoped personal access token (ADR-0011, docs/design/03-security.md §6).
//
// FORMAT: dkp_pat_<8-char public prefix>_<43 chars base64url of 32 random bytes>. The prefix is
// indexed and non-secret — it is what appears in logs, in the token list and in
// `dkp token revoke <prefix>` — and the secret half appears in neither. A distinctive, greppable
// prefix is itself a control: a secret scanner can match it.
//
// STORED AS HMAC-SHA256(hkdf(root_key, "dkp/pat-pepper/v1"), secret) — a KEYED hash, not a password
// hash, because verification is on the hot path of every bot request and must be one indexed lookup
// plus one constant-time compare. The pepper lives in <data-dir>/secrets.json, so a database-only
// leak yields nothing usable.
//
// THERE IS NO admin:* SCOPE AND NO ALL-POWERFUL TOKEN. Effective capability is the service account's
// role permissions INTERSECTED with this row's scopes, so a token can only ever narrow what its
// account already has, and the operations that alter authentication, authorization or bulk-export
// state carry no scope at all and are session-and-step-up only. That is a property of the schema
// rather than of a policy document: there is no cell in the matrix to grant.
table "api_token" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // 8 characters, public, and the ONLY part of the token that is ever printed. It identifies the row
  // for revocation and for `GET /tokens/{id}/activity` without revealing the secret.
  column "prefix" {
    null = false
    type = text
  }

  column "token_hash" {
    null = false
    type = blob
  }

  column "service_account_id" {
    null = false
    type = text
  }

  column "name" {
    null = false
    type = text
  }

  // Space-separated, from the closed enum internal/authz publishes (canonical §6). TEXT rather than a
  // join table because it is read whole on every request and never queried into — the intersection
  // with the account's role permissions happens in Go, where it can be unit-tested.
  column "scopes" {
    null = false
    type = text
  }

  // WHICH PEPPER HASHED THIS ROW (§9.1). The PAT pepper cannot be rotated in place — the plaintext
  // secret is not stored, so old hashes cannot be re-peppered — and the honest mechanism is that
  // rotation mints a new kid for NEW tokens, marks the existing ones stale and surfaces a "rotate
  // these N tokens" task. Without this column that mechanism has nowhere to record itself, and the
  // migration to add one would land during the incident that needed it.
  column "pepper_kid" {
    null    = false
    type    = text
    default = "v1"
  }

  // Micros. NULL means never, which §6.2 permits only behind an explicit confirmation flag and a
  // permanent warning badge; the default the mint path applies is 365 days, with a maximum of three
  // years.
  column "expires_at" {
    null = true
    type = integer
  }

  // "Did the leaked token do anything?" — answered in seconds because the auth path stamps this.
  // THROTTLED, like session.last_seen_at, and last_used_ip stays empty until DKP_TRUSTED_PROXIES
  // makes a client address a fact rather than a header (issue #98).
  column "last_used_at" {
    null = true
    type = integer
  }

  column "last_used_ip" {
    null    = false
    type    = text
    default = ""
  }

  // Per-token rate limit. A bot that starts hammering is throttled at the token rather than at the
  // instance, so one misbehaving script does not shed raid-night traffic for everyone.
  column "rate_limit_rpm" {
    null    = false
    type    = integer
    default = 600
  }

  column "created_by" {
    null = false
    type = text
  }

  // REVOCATION IS ONE UPDATE ON A ROW THE AUTH PATH ALREADY READS, which is the concrete reason PATs
  // are not JWTs: no denylist, no propagation delay, no window in which a leaked token still works.
  column "revoked_at" {
    null = true
    type = integer
  }

  column "revoked_by" {
    null = true
    type = text
  }

  column "revoke_reason" {
    null    = false
    type    = text
    default = ""
  }

  column "created_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  // CASCADE: deleting the bot deletes its credentials. The audit rows the token wrote survive
  // elsewhere and name it by prefix.
  foreign_key "api_token_service_account" {
    columns     = [column.service_account_id]
    ref_columns = [table.service_account.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  foreign_key "api_token_creator" {
    columns     = [column.created_by]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }

  foreign_key "api_token_revoker" {
    columns     = [column.revoked_by]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  // The auth hot path: the bearer names its own row through the public prefix, and the constant-time
  // compare happens against that one row's hash in Go.
  index "ux_api_token_prefix" {
    unique  = true
    columns = [column.prefix]
  }

  // Defence in depth against a duplicate secret, which cannot happen from 32 random bytes but would
  // be catastrophic and silent if a mint path ever reused one.
  index "ux_api_token_hash" {
    unique  = true
    columns = [column.token_hash]
  }

  // The token list for one service account, AND the covering index for api_token_service_account
  // (#274). It lost the `WHERE revoked_at IS NULL` predicate its predecessor ix_api_token_sa carried,
  // for the reason ix_session_user lost the identical one: a partial index cannot carry a foreign key,
  // because the cascade's lookup implies nothing about revoked_at. Nothing is lost by including
  // revoked rows — this table is never DELETEd from, so a revoked token stays a row, and the screen
  // that lists a bot's tokens wants the revoked ones on it anyway.
  //
  // RENAMED rather than redefined in place, and that is about the migration rather than the name:
  // Atlas answers "this index changed" on SQLite with a 12-step rebuild of the whole table, so keeping
  // the old name would have dropped and re-created api_token — every PAT in the guild — to change one
  // index. A new name makes it the DROP INDEX / CREATE INDEX pair it actually is.
  index "ix_api_token_service_account" {
    columns = [column.service_account_id]
  }

  // The covering indexes for the two app_user foreign keys (#274). The columns are written once at
  // mint and once at revoke and never touched again — in particular TouchAPIToken, the write on the
  // bearer hot path, updates only last_used_at and so pays nothing for either of these.
  index "ix_api_token_creator" {
    columns = [column.created_by]
  }

  index "ix_api_token_revoker" {
    columns = [column.revoked_by]
  }

  strict = true
}

// feed_token — a single-purpose, path-embedded read credential (docs/design/01-domain-model.md §4.3,
// docs/design/03-security.md §6.3).
//
// PATH-EMBEDDED BECAUSE CALENDAR CLIENTS CANNOT SET HEADERS, which puts the credential in URLs, proxy
// logs and shared calendars — so it is a DIFFERENT CLASS rather than a PAT with a relaxed transport:
// read-only, scoped to one feed kind, independently revocable, carrying no scopes column at all, and
// containing no email addresses in what it serves.
//
// THE TABLE SHIPS AHEAD OF ITS ROUTES, deliberately: /feeds/{feed_token}/… lands with the calendar
// and article surfaces, and adding a value to the kind CHECK later is a 12-step table rebuild.
table "feed_token" {
  schema = schema.main

  column "id" {
    null = false
    type = text
  }

  // HMAC-SHA256 under the same pepper as api_token (§9.1 derives one "PAT / feed-token HMAC key"),
  // which is why this row carries a pepper_kid too.
  column "token_hash" {
    null = false
    type = blob
  }

  column "user_id" {
    null = false
    type = text
  }

  // Values are internal/auth/feedtoken/kinds' (CHECK generated below). No default: a feed token
  // without a purpose is the general-purpose credential this table exists to avoid.
  column "kind" {
    null = false
    type = text
  }

  column "pepper_kid" {
    null    = false
    type    = text
    default = "v1"
  }

  column "revoked_at" {
    null = true
    type = integer
  }

  column "last_used_at" {
    null = true
    type = integer
  }

  column "created_at" {
    null = false
    type = integer
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "feed_token_user" {
    columns     = [column.user_id]
    ref_columns = [table.app_user.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  // BEGIN GENERATED — feed_token enum CHECK, from internal/auth/feedtoken/kinds. Run `make gen`.
  //
  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI
  // enum are generated from one Go catalogue. Adding a value here by hand is drift that
  // TestFeedTokenKinds_CheckMatchesCatalogue fails on.
  check "feed_token_kind_enum" {
    expr = "kind IN ('raids_ical', 'calendar_ical', 'standings_rss', 'articles_rss')"
  }
  // END GENERATED — feed_token enum CHECK.

  index "ux_feed_token_hash" {
    unique  = true
    columns = [column.token_hash]
  }

  // The covering index for feed_token_user (#274), and the "revoke my calendar feeds" read. CASCADE:
  // deleting a user has to find their feed tokens, and a path-embedded credential is exactly the kind
  // whose revocation must not be the slow thing during an incident.
  index "ix_feed_token_user" {
    columns = [column.user_id]
  }

  strict = true
}
