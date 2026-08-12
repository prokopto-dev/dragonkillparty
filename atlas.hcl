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

  // The analyzer policy for `atlas migrate lint` (issue #131). Declared here for the same reason
  // as `dev` and `migration` above: one declaration, not a flag list repeated at each call site.
  //
  // WHICH migrations get analysed is NOT declared here, and that omission is deliberate. Atlas
  // rejects `--latest` when the config carries a `lint { git { … } }` block ("--latest and
  // --git-base are mutually exclusive"), and both selections are needed: scripts/migrate-lint.sh
  // uses `--git-base` so that only the migrations a branch ADDS are analysed — shipped migrations
  // are frozen, so a diagnostic on one is not actionable by the author who trips over it — and
  // falls back to `--latest` where there is no base ref, which is also how the negative fixture in
  // test/repo drives the script against a fabricated directory in t.TempDir(). Declaring the git
  // block here would make that fixture unrunnable and the gate untested.
  //
  // `error = true` on each analyzer makes `atlas migrate lint` exit non-zero on a diagnostic. That
  // is Atlas's opinion, not this repository's verdict: the ADVISE/ENFORCE decision belongs to
  // scripts/migrate-lint.sh, which is advisory by construction today (#136 tracks the promotion).
  // Setting them to false instead would throw the finding away here and leave the script with
  // nothing to report.
  lint {
    // DS1xx — dropping a schema, a table or a non-virtual column. The analyzer this is here for:
    // SQLite's 12-step table rebuild is `CREATE new / INSERT SELECT / DROP old / RENAME`, and a
    // mistyped column list in the INSERT silently drops that column's data on a populated database
    // while passing every fresh-install check.
    destructive {
      error = true
    }

    // MF1xx — a change whose safety depends on the data already in the table: adding a NOT NULL
    // column with no default, adding a UNIQUE index over values that may already collide. These
    // are the ones that pass on a maintainer's empty dev database and fail on a guild's ten years
    // of DKP, which is this project's worst bug class.
    data_depend {
      error = true
    }

    // BC1xx — a change that breaks a client still running the previous version. Migration-on-boot
    // means the binary and the schema move together, so the window is short; it is not zero,
    // because an officer can roll a binary back over a migrated database.
    incompatible {
      error = true
    }
  }
}

// The Postgres target is post-1.0 (docs/design/06-cicd-and-release.md, `make verify-postgres`).
// db/migrations-postgres/ stays empty until then; declaring an env that nothing generates into
// would be a promise this repository does not yet keep.
