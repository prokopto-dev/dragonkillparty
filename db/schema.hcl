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

// pool — a currency (docs/design/01-domain-model.md §7). MINIMAL for PR 9.
//
// The domain model's pool carries ~20 columns; PR 9 ships the eight the ledger structurally
// needs and defers the rest, for the same freeze-rule reason the guild table shipped twelve of
// seventeen: ADD COLUMN is cheap, remove/retype is SQLite's 12-step rebuild. What is NOT here:
//
//   - server_id: the domain model types it `NOT NULL REFERENCES server(id)`, and `server` is a
//     Phase 1 table. Shipping it now would mean either a NOT NULL FK to a table that does not
//     exist (impossible) or a nullable one that later has to be tightened (a retype). It is added
//     cleanly when `server` lands.
//   - description, currency_label, strategy_config_json, alt_policy, allow_negative,
//     min_balance_cp, hold_policy, attendance_windows, dispute_window_hours,
//     retro_edit_max_age_days, active, archived_at, sort_order: no reader in PR 9 or PR 10; each
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

  // The in-tree point strategy id ('zero_sum' | 'tick' | 'fixed_price' | ...). Not a CHECK enum:
  // the set of strategies is code-defined and grows per PR, and the strategy package validates it
  // — a CHECK here would make every new strategy a schema change.
  column "strategy_id" {
    null = false
    type = text
  }

  // Semver of the in-tree strategy in force. Persisted on the pool and snapshotted onto every
  // batch so a config change never rewrites what a past batch meant.
  column "strategy_version" {
    null = false
    type = text
  }

  // Space-separated balance kinds the strategy declares ('dkp', or 'ep gp' for EPGP). Default
  // 'dkp'. Read whole, never queried into — it is a declaration, not a fact table.
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

  // 'person' | 'system'. The two paired CHECKs below tie this to which of person_id / system_key
  // is populated, so an account is exactly one of the two kinds and cannot be malformed.
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

  // 'guild_bank' | 'residue' | 'write_off' | 'import_opening', or NULL for a person account. The
  // closed set is a CHECK because these four keys are addressed by name in code (the seeded
  // constants in internal/ledger/account.go) and a fifth would be a deliberate schema change.
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

  // kind is one of the two legal values.
  check "account_kind_enum" {
    expr = "kind IN ('person', 'system')"
  }

  // system_key, when present, is one of the four known system accounts.
  check "account_system_key_enum" {
    expr = "system_key IS NULL OR system_key IN ('guild_bank', 'residue', 'write_off', 'import_opening')"
  }

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

  check "ledger_batch_kind_enum" {
    expr = "kind IN ('attendance', 'award', 'adjustment', 'decay', 'cap', 'start_points', 'zero_sum_credit', 'reversal', 'correction', 're_attribution', 'migration', 'import', 'seed', 'write_off')"
  }

  check "ledger_batch_source_enum" {
    expr = "source IN ('web', 'api', 'discord', 'parser', 'import', 'system')"
  }

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

// balance_snapshot — a droppable CACHE of the current balance per (pool, account, balance_kind)
// (docs/design/01-domain-model.md §9.5).
//
// Maintained synchronously in the same transaction as the batch write (PR 10) and verified
// nightly by recomputing from the ledger. It is a cache and is treated as one — never the source
// of truth, which is always the SUM over ledger_entry. WITHOUT ROWID: the composite PK IS the
// row, read only ever by that key.
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
