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
