package repogate

import (
	"bytes"
	"errors"
	"os"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate/lockmanifest"
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
// # Why this rule no longer shells out
//
// The check lives in internal/migrate/lockmanifest because the release path runs the same code with
// one more assertion, and a second copy here would be a second implementation nobody keeps in step.
// That package was a `package main` until issue #173, so this rule ran it with `go run` — nesting a
// Go build inside the one that built this engine, on every `make lint-repo` and inside each of the
// thirty subprocesses test/repo spawns.
//
// The build was the cheap half of the cost. The expensive half was that `go run` returns one bit: a
// manifest that DISAGREES and a check that COULD NOT RUN both arrived as a non-zero exit and a blob
// of captured stdout, so this rule had to report the first about both — telling a reader to look for
// an edit to a migration when the real fault was an unreadable file or a git that would not answer.
// [lockmanifest.ErrDisagrees] is that distinction, and the two messages below are what it buys.
const shippedLockRel = "db/migrations-sqlite/SHIPPED.lock"

// runShippedLockRule evaluates MIG003.
//
// root is the tree being INSPECTED, which for a negative fixture is a t.TempDir() with no Go module
// in it at all. Nothing about that matters any more: the check is a function call in this process,
// so the tree needs to hold a manifest and a git history and nothing else.
func runShippedLockRule(s *scanner, rep *report) {
	if !s.exists(shippedLockRel) {
		rep.skip("MIG003", shippedLockRel)

		return
	}

	var out bytes.Buffer

	err := lockmanifest.Verify(s.root, os.Getenv("DKP_SHIPPED_LOCK_BASE_REF"), false, &out)
	report := strings.TrimRight(out.String(), "\n")

	switch {
	case err == nil:
		rep.print(report)

	case errors.Is(err, lockmanifest.ErrDisagrees):
		rep.violation("MIG003",
			"a migration listed in "+shippedLockRel+" was modified or deleted",
			[]string{report})

	default:
		// A hash gate that cannot hash must not report green. It also must not claim a migration was
		// edited: the tree may be untouched and the fault entirely in the checkout.
		rep.violation("MIG003",
			shippedLockRel+" could not be checked, so the frozen-migration rule did not run",
			[]string{strings.TrimSpace(report + "\n" + err.Error())})
	}
}
