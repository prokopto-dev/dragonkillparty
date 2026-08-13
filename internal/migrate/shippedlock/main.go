// Command shippedlock maintains and checks db/migrations-sqlite/SHIPPED.lock, the append-only
// manifest of migrations that have already shipped.
//
//	shippedlock verify              every listed migration still exists and still hashes as recorded
//	shippedlock verify --complete   the above, plus every migration in the tree is listed
//	shippedlock seal                append a row for every migration not yet listed
//	shippedlock init                create the manifest with its header and no rows
//
// The logic is internal/migrate/lockmanifest, and the split is not decoration: MIG003 in
// internal/repogate runs the same verification IN PROCESS (issue #173), so a `package main` holding
// it would have forced the gate engine to shell out — nesting one Go build inside another and
// flattening "the manifest disagrees" and "the check could not run" into one exit code. Two callers,
// one implementation; a second copy of this reasoning is the thing the manifest itself exists to
// make impossible.
//
// DKP_REPO_ROOT names the tree to operate on, defaulting to the working directory, exactly as
// repo-gates.sh and install-atlas.sh use it: a test writes a lock file and a deliberately altered
// migration into t.TempDir() and requires a non-zero exit. DKP_SHIPPED_LOCK_BASE_REF overrides the
// ref the append-only history check takes a merge base with.
package main

import (
	"os"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate/lockmanifest"
)

func main() {
	// The Makefile targets run from the repo root, which is why "." is the fallback rather than
	// something derived from os.Args[0]: `go run ./internal/migrate/shippedlock` already requires a
	// working directory inside this module.
	root := os.Getenv("DKP_REPO_ROOT")
	if root == "" {
		root = "."
	}

	os.Exit(lockmanifest.Run(os.Args[1:], root, os.Getenv("DKP_SHIPPED_LOCK_BASE_REF"), os.Stdout, os.Stderr))
}
