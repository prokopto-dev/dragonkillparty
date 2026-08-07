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
