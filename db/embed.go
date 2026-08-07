// Package db embeds the generated migration set into the binary.
//
// It exists because //go:embed cannot reach upwards through the filesystem: the directive has to
// live in a package at or above the files it embeds, and the migrations are at db/migrations-sqlite/.
// So this is a package with no logic in it, whose entire job is to be in the right directory.
//
// Nothing else belongs here. The SQL is generated (Atlas writes it from db/schema.hcl), the
// applying is goose's (driven from internal/store, the only package that may hold a *sql.DB), and
// the policy around it — snapshot, integrity check, restore — is internal/migrate's.
package db

import (
	"embed"
	"io/fs"
)

// migrationsDir is the embedded subdirectory. goose's Provider takes an fs.FS whose ROOT holds the
// migration files, so callers get an fs.Sub of this rather than the raw embed.FS.
const migrationsDir = "migrations-sqlite"

// Only *.sql. atlas.sum is a development artefact — it is what `atlas migrate hash` checks and what
// makes a hand-edited migration visible in review, and it has no meaning at runtime. Embedding it
// would put a file goose cannot parse in the directory goose walks.
//
//go:embed migrations-sqlite/*.sql
var sqliteMigrations embed.FS

// SQLiteMigrations returns the embedded SQLite migration set, rooted so that goose sees the .sql
// files directly.
//
// It returns an error rather than panicking on a bad fs.Sub. The only way that call fails is if the
// embed directive above and migrationsDir stop agreeing, which is a build-time programming error —
// but "panic outside main wiring" is banned (.claude/rules/go-idioms.md), and a panic here would
// fire inside a migration at boot, on an officer's server, which is the worst possible place for
// this project to discover a typo.
func SQLiteMigrations() (fs.FS, error) {
	return fs.Sub(sqliteMigrations, migrationsDir)
}
