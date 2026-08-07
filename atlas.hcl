// Atlas project configuration.
//
// Atlas is a DEVELOPMENT AND CI DEPENDENCY ONLY (ADR-0008). It authors the migrations in
// db/migrations-sqlite/ from db/schema.hcl; the goose library, embedded in the binary via
// go:embed, applies them. An officer upgrading their guild's server never installs Atlas, and
// adding it to the runtime would break the single-binary promise.
//
// Every invocation goes through `--env sqlite` so that the dev-url, the directory and the file
// format are declared once here rather than repeated at each call site.

env "sqlite" {
  src = "file://db/schema.hcl"

  // The dev database Atlas uses to compute the current state by replaying the migration
  // directory. In memory, created and discarded per invocation: nothing here ever touches a real
  // database, which is what makes `make gen` safe to run against a checkout with a live dkp.db in
  // it.
  dev = "sqlite://file?mode=memory"

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
