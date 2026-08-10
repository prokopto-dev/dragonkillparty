// Command enumgen writes the ledger enum catalogue into db/schema.hcl's generated region.
//
// It is the `make gen` half of canonical §5 ("the enum catalogue is a Go const block; make gen
// writes it into the migration CHECK"): internal/ledger/kinds.go holds the values, this rewrites the
// two ledger_batch CHECK constraints from them, and `make migration` turns the resulting schema into
// SQL. It never writes a migration itself, for the reason scripts/gen-db.sh gives — `make gen` runs
// reflexively and must not create numbered, permanent, append-only files as a side effect.
//
// A GENERATOR, NOT A GATE. It rewrites and says nothing; the drift assertion is
// TestLedgerKinds_CheckMatchesCatalogue and `make verify-generated`. Keeping the two apart is what
// lets `make gen` be the fix rather than another thing to interpret.
//
// It lives beside the catalogue rather than in cmd/dkp because it is dev tooling: cmd/dkp is the
// product binary and an officer never runs a code generator.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
)

// defaultSchemaPath is relative to the repo root, which is where `make gen` runs its scripts from.
const defaultSchemaPath = "db/schema.hcl"

func main() {
	path := defaultSchemaPath
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	if err := run(path); err != nil {
		fmt.Fprintf(os.Stderr, "enumgen: %v\n", err)
		os.Exit(1)
	}
}

// run reads path, re-renders its generated region from the catalogue and writes it back only when
// the bytes actually change.
//
// The write is skipped on a no-op so that `make gen` on an up-to-date tree leaves mtimes alone —
// touching the single source of schema truth on every run would make every `make` in the repository
// look like the schema had moved.
func run(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	out, err := ledger.RenderSchemaHCL(string(src))
	if err != nil {
		if errors.Is(err, ledger.ErrSchemaMarkersMissing) {
			return fmt.Errorf("render %s: %w\n\nthe markers delimit the region this generator owns; "+
				"without them it cannot tell generated lines from hand-authored schema", path, err)
		}

		return fmt.Errorf("render %s: %w", path, err)
	}

	if out == string(src) {
		return nil
	}

	return writeAtomic(path, []byte(out))
}

// writeAtomic replaces path's contents through a temp file in the same directory and a rename, so an
// interrupted run cannot leave a truncated db/schema.hcl behind.
//
// Same reasoning as scripts/gen-openapi.sh's tmp-then-move: the obvious direct write truncates the
// target before it knows it has anything to put there, and this particular target is the file every
// migration in the repository is diffed against.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".enumgen-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}

	tmpName := tmp.Name()

	defer func() {
		// Best-effort cleanup of the temp file on the failure paths; on success the rename has
		// already moved it and this removes nothing.
		_ = os.Remove(tmpName)
	}()

	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("write %s: %w", tmpName, err)
	}

	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	// CreateTemp makes the file 0600. The target is a committed, world-readable source file, and a
	// generator that quietly narrowed its mode would surprise the next container build.
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}

	return nil
}
