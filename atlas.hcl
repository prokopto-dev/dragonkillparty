// Atlas project configuration.
//
// Atlas is a DEVELOPMENT AND CI DEPENDENCY ONLY (ADR-0008). It authors the migrations in
// db/migrations-sqlite/ from db/schema.hcl; the goose library, embedded in the binary via
// go:embed, applies them. An officer upgrading their guild's server never installs Atlas, and
// adding it to the runtime would break the single-binary promise.
//
// Every invocation goes through `--env sqlite` so that the dev-url, the directory and the file
// format are declared once here rather than repeated at each call site.

// A fresh dev database NAME per invocation, and the thing being separated is a lock, not data.
//
// Atlas derives its advisory lock name from the dev-url — `atlas_migrate_diff_<hash-of-url>` — and
// holds it machine-wide for the length of a `migrate diff`. A single fixed dev-url therefore gives
// every concurrent diff on the machine one lock to contend for, and the losers do not queue: they
// exit 1 with `acquiring database lock: sql/sqlite: lock on "atlas_migrate_diff_41401a1" already
// taken`. That message names Atlas and the community build, so it reads as a toolchain or schema
// fault — and the person most likely to see it is whoever just touched migrations and will assume
// they caused it. Issue #36; the collision was reproducible at 7 failures in 8 concurrent diffs.
//
// Eight bytes of /dev/urandom, hex. Not a process id and not the working directory: two checkouts
// of this repository on one machine — an agent worktree and the developer's own tree, both running
// `make check` — are separate directories AND separate processes, and both must still get separate
// locks. Randomness is the only source of uniqueness that covers every case without coordination.
//
// This costs nothing elsewhere. The dev database is scratch — created, replayed into and discarded
// per invocation — its name appears in no output, and the migration Atlas writes is byte-identical
// whatever it is called, which is what keeps `make verify-generated`'s `git diff --exit-code` a
// real assertion rather than permanent noise.
//
// TestAtlas_ConcurrentInvocations_DoNotShareALock (test/repo) is the enforcement: it runs real
// concurrent invocations through this file and requires all of them to succeed, so an id that
// silently came back empty — or a future edit that pins the name again — fails there.
data "external" "dev_id" {
  program = ["sh", "-c", "od -An -N8 -tx1 /dev/urandom | tr -dc 0-9a-f"]
}

env "sqlite" {
  src = "file://db/schema.hcl"

  // The dev database Atlas uses to compute the current state by replaying the migration
  // directory. In memory, created and discarded per invocation: nothing here ever touches a real
  // database, which is what makes `make gen` safe to run against a checkout with a live dkp.db in
  // it.
  dev = "sqlite://dkp_dev_${data.external.dev_id}?mode=memory"

  migration {
    dir = "file://db/migrations-sqlite"

    // goose annotations (`-- +goose Up` / `-- +goose Down`), because goose is what applies these
    // at runtime. Atlas's own format would need a second parser in the binary.
    format = goose
  }
}

// The Postgres target is post-1.0 (docs/design/06-cicd-and-release.md, `make verify-postgres`).
// db/migrations-postgres/ stays empty until then; declaring an env that nothing generates into
// would be a promise this repository does not yet keep.
