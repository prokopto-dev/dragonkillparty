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
// nullable TEXT with NO foreign key; the FK is added when that table lands (a cheap ADD COLUMN /
// ADD CONSTRAINT forward migration, never a remove or a retype). Every such column is commented
// as a tracked deferral below.
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

  // DEFERRED FK -> app_user(id). app_user is a Phase 2 table (docs/design/01-domain-model.md §5).
  // Nullable TEXT, no foreign key until it lands. NULL is also the honest value for a change made by
  // the importer or by a boot-time migration, which have no user behind them.
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

  // DEFERRED FK -> app_user(id). app_user is a Phase 2 table. Nullable TEXT, no foreign key until it
  // lands — and NULL is the correct value for the periodic job, which is the expected trigger.
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

  foreign_key "decay_run_batch" {
    columns     = [column.ledger_batch_id]
    ref_columns = [table.ledger_batch.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
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

  strict = true
}
