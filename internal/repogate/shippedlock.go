package repogate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MIG003 — a migration that has shipped is frozen.
//
// db/migrations-sqlite/SHIPPED.lock records `filename sha256` for every migration that has appeared
// in a tagged release, so a row in it means that file has ALREADY RUN on somebody's database.
// Editing it makes an existing install and a fresh install end up with different schemas, and
// "works on a fresh install, breaks on upgrade" is the most damaging bug class for this audience.
//
// TWO assertions, and the second is the one that makes the first mean anything:
//
//  1. Every listed file exists and still hashes to its recorded value.
//  2. The manifest at the MERGE BASE is an exact byte PREFIX of the manifest now.
//
// Without (2), (1) is trivially defeated by changing both halves in the same commit: edit the
// migration, rewrite its row — or just delete the row, which un-freezes the file entirely — and the
// tree is self-consistent again. The manifest ships in the same diff as the migration it protects,
// so it can only be trusted against its own history.
//
// (2) needs git history, so it SKIPS loudly when the base cannot be read (a shallow checkout).
// ci.yml's `lint / repo` job carries `fetch-depth: 0` and TestCI_LintRepoJob_FetchesFullHistory
// fails if that is removed, which is what stops the skip from becoming the normal case.
//
// This is NOT what atlas.sum already covers. atlas.sum protects the current set as it is: edit a
// migration, re-run `atlas migrate hash`, and the checksum agrees again. SHIPPED.lock records
// history rather than state, and nothing in this repository is allowed to rewrite it.
//
// Deliberately NOT a completeness check. A migration added on a feature branch has not shipped and
// must not be listed yet. Completeness is checked once, at tag time, by `make release-shipped-lock`.
//
// # Why this one rule shells out
//
// The check itself lives in internal/migrate/shippedlock because the release path runs the same
// code with one more assertion, and a second copy here would be a second implementation nobody
// keeps in step. That package is a `package main` (issue #129 moved it out of shell), so it cannot
// be imported — hence the nested `go run`, which is exactly what the shell gate did and costs the
// same. Extracting its verify logic into an importable package would remove the nested build; that
// is a change to internal/migrate, not to the gates, and is tracked separately.
const shippedLockRel = "db/migrations-sqlite/SHIPPED.lock"

// runShippedLockRule evaluates MIG003.
//
// root is the tree being INSPECTED. The command is built and run from THIS checkout, never from
// that tree: the negative fixtures are t.TempDir() trees with no Go module in them.
func runShippedLockRule(s *scanner, rep *report) {
	if !s.exists(shippedLockRel) {
		rep.skip("MIG003", shippedLockRel)

		return
	}

	module, err := moduleRoot()
	if err != nil {
		// A hash gate that cannot hash must not report green — the same posture the command itself
		// takes on a migration it cannot read.
		rep.violation("MIG003", "the gate engine could not locate its own module, so "+shippedLockRel+
			" could not be checked", []string{err.Error()})

		return
	}

	cmd := exec.Command("go", "-C", module, "run", "./internal/migrate/shippedlock", "verify")
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+s.root)

	out, err := cmd.CombinedOutput()
	if err != nil {
		rep.violation("MIG003",
			"a migration listed in "+shippedLockRel+" was modified or deleted",
			[]string{strings.TrimRight(string(out), "\n")})

		return
	}

	rep.print(string(out))
}

// moduleRoot is the checkout holding this engine's own source: the nearest ancestor of the working
// directory with a go.mod in it.
//
// The working directory is the right place to start because the gate script runs the engine with
// `go -C <checkout> run`, which puts go — and therefore the program it builds — in the checkout.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}

		dir = parent
	}
}
