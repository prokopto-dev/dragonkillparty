// Negative fixture tests for scripts/migrate-lint.sh — the migration linter added advisory-first in
// issue #131 and promoted to a gate in #136.
//
// WHAT THE PROMOTION HAD TO BUY, and it is why this file grew rather than shrank. Advisory-first was
// the right way in: SQLite's 12-step table rebuild is where Atlas's analyzers are least predictable,
// and a linter that blocks merges before it is trusted gets disabled rather than tuned. Flipping it
// therefore rests on three claims, and each is a test here rather than a sentence in a PR:
//
//	the rebuild is not a false positive   TestMigrateLint_TwelveStepRebuild_IsNotADiagnostic
//	a diagnostic can be waived, in-diff   TestMigrateLint_NolintDirective_WaivesTheDiagnostic
//	the default blocks                    TestMigrateLint_DestructiveChange_UnderTheDefault_Fails
//
// Both modes are still exercised, because the difference between them is the gate:
//
//	MODE=enforce (default)   diagnostic printed, exit non-zero
//	MODE=advise              diagnostic printed, `::warning::` emitted, exit 0
//
// and separately, that a BROKEN invocation is a hard failure in both — because a scan that never
// ran must never report as a scan that found nothing. That distinction is the one thing a linter
// can get wrong in a way nobody notices.
//
// The fixtures follow gates_test.go's two rules: t.TempDir() only, and assert on the message rather
// than on the exit code alone. They reuse migrationFixture from new_migration_test.go, which copies
// the repository's OWN atlas.hcl — so these tests exercise the real analyzer policy declared there,
// and a future edit that deleted the `lint` block would fail here rather than passing quietly.
package repo_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A base ref that cannot exist, so the fixtures deterministically take the script's `--latest 1`
// fallback rather than depending on whether t.TempDir() happens to sit outside a git working tree.
//
// This is not a test-only branch in the script: the fallback is the real shallow-clone path, and
// pointing it at an absent ref is how a test reaches it without fabricating a git repository.
const absentBaseRef = "refs/dkp-test/definitely-absent"

// The fixture schema these migrations build against. Deliberately a table with a droppable column:
// DS103 ("dropping non-virtual column") is the analyzer this gate exists for, because a SQLite
// 12-step rebuild with a mistyped column list drops that column's data on a populated database
// while passing every fresh-install check.
const lintFixtureSchema = `schema "main" {
}
table "fixture_widget" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "label" {
    null = false
    type = text
  }
  primary_key {
    columns = [column.id]
  }
  strict = true
}
`

const gooseDownStub = `
-- +goose Down
SELECT RAISE(ABORT, 'DKP migrations are forward-only');
`

// migrationLintFixture builds a tree scripts/migrate-lint.sh can analyse: the repository's real
// atlas.hcl, a schema, and a migration directory holding the given files, hashed with the real
// `atlas migrate hash` so the directory's atlas.sum is valid.
//
// The bodies are passed as name/body pairs in order, because migration order is the whole subject:
// a DROP COLUMN is only destructive relative to the CREATE TABLE that preceded it.
func migrationLintFixture(t *testing.T, nameBodyPairs ...string) string {
	t.Helper()

	require.Zero(t, len(nameBodyPairs)%2, "migrationLintFixture takes name/body pairs")

	tree := migrationFixture(t, lintFixtureSchema)

	for i := 0; i < len(nameBodyPairs); i += 2 {
		writeMigration(t, tree, nameBodyPairs[i], nameBodyPairs[i+1])
	}

	hashMigrationDir(t, tree)

	return tree
}

// hashMigrationDir runs the real `atlas migrate hash` over a fixture's migration directory.
//
// Atlas refuses to lint a directory whose atlas.sum does not match its contents, which is the
// integrity check that makes a hand-edited migration visible (scripts/gen-db.sh). A fixture must
// therefore be hashed rather than hand-written, or every case in this file would fail on the
// checksum before reaching the analyzer it is about.
func hashMigrationDir(t *testing.T, tree string) {
	t.Helper()

	cmd := exec.Command("atlas", "migrate", "hash",
		"--dir", "file://"+filepath.ToSlash(filepath.Join(tree, "db", "migrations-sqlite")),
		"--dir-format", "goose")

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "atlas migrate hash over the fixture directory:\n%s", out)
}

// runMigrateLint invokes scripts/migrate-lint.sh against a fixture tree in the given mode.
//
// An EMPTY mode leaves MODE unset, which is how the default is tested — and the inherited
// environment is stripped of it first, so a MODE exported in whoever's shell ran `make test` cannot
// turn the default case into a copy of one of the explicit ones.
//
// Environment built explicitly rather than with t.Setenv, for gates_test.go's reason: t.Setenv makes
// t.Parallel() panic.
func runMigrateLint(t *testing.T, tree, mode string) (output string, exitCode int) {
	t.Helper()

	return runMigrateLintEnv(t, tree, mode)
}

// runMigrateLintEnv is runMigrateLint with explicit NAME=VALUE overrides, applied last.
//
// An override REPLACES the inherited variable of that name rather than being appended after it. The
// two the issue #254 fixtures steer — PATH and DKP_ATLAS — are exactly the ones `make check` sets
// for this script, and which of two same-named entries an exec'd shell honours is not a thing worth
// depending on. Replacing makes the fixture say what it means.
func runMigrateLintEnv(t *testing.T, tree, mode string, overrides ...string) (output string, exitCode int) {
	t.Helper()

	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	env := make([]string, 0, len(os.Environ())+len(overrides)+3)

	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MODE=") {
			env = append(env, kv)
		}
	}

	env = append(env, "DKP_REPO_ROOT="+tree, "DKP_MIGRATE_LINT_BASE="+absentBaseRef)
	env = append(env, overrides...)

	if mode != "" {
		env = append(env, "MODE="+mode)
	}

	cmd := exec.Command("bash", scriptPath(t, "migrate-lint.sh"))
	cmd.Env = dedupeEnvLastWins(t, env)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run migrate-lint.sh: %v\n%s", err, out)

	return "", 0
}

// dedupeEnvLastWins collapses an environment to one entry per name, keeping the last value.
//
// Env slices are built here by appending defaults and then overrides, which is the readable order to
// write them in and the wrong one to hand to exec: POSIX leaves a duplicate name to the callee, and
// bash resolving it one way on Linux and another on macOS is precisely the kind of difference a
// fixture must not be built on.
func dedupeEnvLastWins(t *testing.T, env []string) []string {
	t.Helper()

	at := make(map[string]int, len(env))
	out := make([]string, 0, len(env))

	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		require.Truef(t, ok, "environment entry %q must be NAME=VALUE", kv)

		if i, seen := at[name]; seen {
			out[i] = kv

			continue
		}

		at[name] = len(out)
		out = append(out, kv)
	}

	return out
}

// runAtlas runs the real Atlas against a fixture tree, the way `make migration` does.
//
// The fixtures that need it are the ones about what ATLAS says, not about what a hand-written
// migration looks like: a 12-step rebuild has to be authored by the tool whose output the gate will
// be reading, or the test proves something about a string in this file instead.
func runAtlas(t *testing.T, tree string, args ...string) string {
	t.Helper()

	cmd := exec.Command("atlas", args...)
	cmd.Dir = tree

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "atlas %s in the fixture tree:\n%s", strings.Join(args, " "), out)

	return string(out)
}

// requireAtlas skips when Atlas is absent, matching TestAtlas_ConcurrentInvocations_DoNotShareALock.
//
// CI does NOT take this skip: `test / integration` asks setup-toolchain for `atlas`, which is the
// line that keeps this suite from being a gate that quietly never runs. Skipping rather than failing
// is for the contributor who has not run `make setup` yet.
func requireAtlas(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("invokes atlas; run `make test` or `make check`")
	}

	requireTool(t, "atlas", "ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` "+
		"install it through setup-toolchain's tools: input")
}

// destructiveFixturePair is the migration pair every destructive-change case here shares: a table,
// then a migration that drops one of its columns. DS103 is the analyzer this gate exists for.
func destructiveFixturePair(nolint string) []string {
	return []string{
		"000001_init.sql", "-- +goose Up\n" +
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, \"label\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n" +
			gooseDownStub,
		"000002_drop_label.sql", "-- +goose Up\n" + nolint +
			"ALTER TABLE \"fixture_widget\" DROP COLUMN \"label\";\n" +
			gooseDownStub,
	}
}

// TestMigrateLint_DestructiveChange_UnderTheDefault_Fails is the headline property since issue #136.
//
// MODE unset is MODE=enforce, so a call site that forgets to say fails CLOSED. That matters more
// than it looks: `make lint-migrations` states the mode too, and the two together mean neither the
// Makefile nor this script can quietly downgrade the gate on its own.
func TestMigrateLint_DestructiveChange_UnderTheDefault_Fails(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t, destructiveFixturePair("")...)

	out, code := runMigrateLint(t, tree, "")

	require.NotEqualf(t, 0, code,
		"with no MODE set, scripts/migrate-lint.sh must FAIL on a diagnostic. Issue #136 promoted "+
			"this gate; a default that advised instead would leave every invocation that does not "+
			"name a mode — including any future call site — reporting a green run over a "+
			"destructive migration.\n%s", out)
	require.Containsf(t, out, "DS103", "the failure must name the analyzer that fired\n%s", out)
	require.Containsf(t, out, "atlas:nolint",
		"the failure must name the waiver. A blocking gate with no reviewable way past it is a gate "+
			"people route around, which is the condition issue #136 set for promoting this one.\n%s", out)
}

// TestMigrateLint_DestructiveChange_UnderAdvise_WarnsWithoutFailing keeps the advisory path honest.
//
// MODE=advise is no longer how CI runs the gate (#136) — it is how somebody reads the diagnostics
// over a migration set without being blocked by them. The path still has to work: a dropped column
// IS reported, with the analyzer code so the reader can look it up, the run says the finding did not
// fail IT, and it says that enforce is what CI runs — because a reader who sees a diagnostic and a
// green local run must not conclude the merge will be green too.
func TestMigrateLint_DestructiveChange_UnderAdvise_WarnsWithoutFailing(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t, destructiveFixturePair("")...)

	out, code := runMigrateLint(t, tree, "advise")

	require.Equalf(t, 0, code,
		"MODE=advise must not fail on a diagnostic — it is the deliberate look-without-blocking "+
			"mode, and TestMigrateLint_UnanalysableMigration_FailsLoud is what keeps that from "+
			"meaning 'cannot fail'.\n%s", out)

	require.Containsf(t, out, "DS103",
		"the destructive-change analyzer must NAME itself. A warning that does not identify the "+
			"analyzer cannot be looked up or waived.\n%s", out)
	require.Containsf(t, out, "label",
		"the diagnostic must name the dropped column, or the reader cannot tell which change "+
			"provoked it.\n%s", out)
	require.Containsf(t, out, "::warning",
		"a diagnostic must emit a GitHub Actions warning annotation, or an advisory finding is "+
			"visible only to whoever opens the raw log.\n%s", out)
	require.Containsf(t, out, "advisory",
		"the output must say the finding did not fail this run, or a reader who sees a diagnostic "+
			"and a green run has to guess which one is wrong.\n%s", out)
	require.Containsf(t, out, "enforce",
		"MODE=advise must say that enforce is what CI runs. Otherwise the one mode that does not "+
			"block is also the one that looks like the whole story, and the merge goes red on a "+
			"diagnostic its author was told did not matter.\n%s", out)
}

// TestMigrateLint_DestructiveChange_UnderEnforce_Fails pins the explicit mode.
//
// The default is enforce (above), and `make lint-migrations` names it anyway; this is the assertion
// that the named mode still means what it says, so the Makefile's belt-and-braces `MODE=enforce` is
// not decoration.
func TestMigrateLint_DestructiveChange_UnderEnforce_Fails(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t, destructiveFixturePair("")...)

	out, code := runMigrateLint(t, tree, "enforce")

	require.NotEqualf(t, 0, code,
		"MODE=enforce must fail on a diagnostic, or promoting the gate (#136) would be a no-op that "+
			"looked like a gate.\n%s", out)
	require.Containsf(t, out, "DS103", "the failure must name the analyzer that fired\n%s", out)
	require.Containsf(t, out, "MODE=enforce",
		"the failure must say WHICH mode failed it, so an unexpected red is traceable to the call "+
			"site rather than to the migration.\n%s", out)
}

// TestMigrateLint_NolintDirective_WaivesTheDiagnostic is precondition 3 of issue #136: a blocking
// gate needs a reviewable way past it, or it is a gate people route around.
//
// The waiver is ATLAS'S OWN `-- atlas:nolint <analyzer>` directive, on the line above the statement
// the diagnostic fires on, rather than a marker this repository invented and would have to teach the
// analyzer about. It sits beside the `-- dkp:destructive-approved: #<issue>` line the two-release
// destructive rule already requires — Atlas reads one and ignores the other, and a reviewer reads
// both in the diff, which is the property that matters: the exception is in the migration, not in
// the PR conversation.
//
// A hand-edit of a generated migration, and a legitimate one: it adds comments, changes no
// statement, and .claude/rules/migrations.md's allowlist is where that is recorded.
func TestMigrateLint_NolintDirective_WaivesTheDiagnostic(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t, destructiveFixturePair(
		"-- dkp:destructive-approved: #1234\n-- atlas:nolint destructive\n")...)

	out, code := runMigrateLint(t, tree, "enforce")

	require.Equalf(t, 0, code,
		"`-- atlas:nolint destructive` above the statement must silence the analyzer even under "+
			"MODE=enforce. If this has gone red, the waiver documented in scripts/migrate-lint.sh, "+
			"the CI step and .claude/rules/migrations.md no longer exists — and every reviewed, "+
			"intentional destructive migration is now unlandable.\n%s", out)
	require.Containsf(t, out, "no diagnostics",
		"a waived diagnostic must report as a clean run, not as a silent one\n%s", out)
}

// TestMigrateLint_TwelveStepRebuild_IsNotADiagnostic is precondition 1 of issue #136, and the reason
// #131 landed this linter advisory-first rather than as a gate.
//
// SQLite cannot alter a CHECK in place, so every catalogue edit — adding a ledger_batch kind, a
// decay_run state — becomes the 12-step rebuild: CREATE new, INSERT SELECT, DROP old, RENAME. If
// Atlas read that DROP as a destructive change, promoting the gate would have blocked the most
// ordinary schema change this repository makes, and the fix people would reach for is disabling the
// linter. It does not: the rebuild preserves every column, and Atlas compares the schemas rather than
// the statements.
//
// So the migration under test is authored by ATLAS, not written here. A hand-written approximation
// would be asserting something about a string in this file; what has to hold is what the tool the
// gate reads actually emits, and what it emits is what `make migration` will put in a PR.
func TestMigrateLint_TwelveStepRebuild_IsNotADiagnostic(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	// The first migration is written rather than diffed, and the ordering is why: Atlas names a
	// generated file by TIMESTAMP TO THE SECOND, so two diffs in one test land in the same second
	// and their order becomes alphabetical — `add_label_check` before `init`, a directory that does
	// not replay. A hand-written `000001_` sorts before any timestamp, whatever second this runs in.
	//
	// The Down block Atlas writes for the rebuild carries DDL that MIG001 would refuse. That is fine
	// and deliberate: `make migration` rewrites it, and this fixture is about the analyzer's verdict
	// on the Up block, not about the file this repository would commit.
	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, \"label\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
	)

	require.NoError(t, os.WriteFile(filepath.Join(tree, "db", "schema.hcl"),
		[]byte(strings.Replace(lintFixtureSchema, "  strict = true\n",
			"  check \"fixture_widget_label_enum\" {\n    expr = \"label IN ('a', 'b')\"\n  }\n  strict = true\n",
			1)), 0o644))

	runAtlas(t, tree, "migrate", "diff", "add_label_check", "--env", "sqlite")

	rebuild := latestMigration(t, tree)
	require.Containsf(t, rebuild, "new_fixture_widget",
		"this fixture is only meaningful if Atlas emitted the 12-step rebuild. It did not, so the "+
			"test is no longer about the pattern it names:\n%s", rebuild)
	require.Containsf(t, rebuild, "DROP TABLE",
		"the rebuild must contain the DROP that makes the false-positive question real\n%s", rebuild)

	out, code := runMigrateLint(t, tree, "")

	require.Equalf(t, 0, code,
		"a 12-step table rebuild that preserves every column must NOT be a diagnostic. If this has "+
			"gone red, Atlas's analyzers changed their mind about the pattern `make migration` "+
			"emits for a CHECK change — read the diagnostic before touching this test, because the "+
			"gate is now blocking every catalogue edit (issue #136, precondition 1).\n%s", out)
	require.Containsf(t, out, "no diagnostics", "%s", out)
}

// latestMigration returns the body of the highest-sorting migration in a fixture tree.
func latestMigration(t *testing.T, tree string) string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(tree, "db", "migrations-sqlite", "*.sql"))
	require.NoError(t, err, "list the fixture's migrations")
	require.NotEmpty(t, files, "the fixture tree has no migrations")

	sort.Strings(files)

	body, err := os.ReadFile(files[len(files)-1])
	require.NoError(t, err, "read the newest migration")

	return string(body)
}

// TestMigrateLint_CleanMigration_ReportsNoDiagnostics is the positive control.
//
// Without it, a script that reported "no diagnostics" unconditionally — or one whose grep for
// "Reports" silently stopped matching — would pass every other test in this file.
func TestMigrateLint_CleanMigration_ReportsNoDiagnostics(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, \"label\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
	)

	for _, mode := range []string{"advise", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			out, code := runMigrateLint(t, tree, mode)

			require.Equalf(t, 0, code, "a clean migration must pass in MODE=%s\n%s", mode, out)
			require.Containsf(t, out, "no diagnostics",
				"a clean run must say so explicitly. A gate that prints nothing is "+
					"indistinguishable from a gate that did not run.\n%s", out)
			require.NotContainsf(t, out, "::warning",
				"a clean run must not emit a warning annotation\n%s", out)
		})
	}
}

// TestMigrateLint_UnanalysableMigration_FailsLoud is the property that keeps "advisory" honest, and
// the one that caught a real hole in scripts/migrate-lint.sh before it landed.
//
// Advisory applies to a DIAGNOSTIC — a verdict Atlas reached about a migration. It must never apply
// to a migration Atlas could not analyse at all, because "no verdict" and "a clean verdict" are the
// same green check to everyone downstream.
//
// The two are genuinely hard to tell apart, which is why this test exists rather than a comment.
// Atlas exits 1 for both, and its JSON report sets a file-level `Error` string for both:
//
//	diagnostic     {"Name":…,"Reports":[…],"Error":"destructive changes detected"}
//	not analysed   {"Name":…,"Error":"executing statement: …: no such column: \"label\""}
//
// The presence of `Reports` is the only discriminator. The first version of the script tested for it
// and, finding none, printed "no diagnostics" and exited 0 — so a migration that does not execute at
// all reported as clean. This fixture is what failed, and it must keep failing if that ordering is
// ever reversed.
func TestMigrateLint_UnanalysableMigration_FailsLoud(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	for _, mode := range []string{"advise", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			// The second migration drops a column the first never created, so Atlas cannot replay
			// the directory and reaches no verdict about it.
			tree := migrationLintFixture(t,
				"000001_init.sql", "-- +goose Up\n"+
					"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
					gooseDownStub,
				"000002_drop_absent.sql", "-- +goose Up\n"+
					"ALTER TABLE \"fixture_widget\" DROP COLUMN \"never_existed\";\n"+
					gooseDownStub,
			)

			out, code := runMigrateLint(t, tree, mode)

			require.NotEqualf(t, 0, code,
				"a migration Atlas could not analyse must fail even in MODE=%s. Advisory covers a "+
					"verdict about a migration, never a migration that was never analysed — a scan "+
					"that never ran must not report as a scan that found nothing.\n%s", mode, out)
			require.Containsf(t, out, "could not analyse",
				"the failure must say the ANALYSIS did not happen rather than reading as an "+
					"ordinary diagnostic, or the next reader debugs the wrong thing.\n%s", out)
			require.NotContainsf(t, out, "no diagnostics",
				"an unanalysed migration must never print the clean-run message — that is the exact "+
					"hole this fixture was written to catch.\n%s", out)
		})
	}
}

// TestMigrateLint_BrokenConfig_FailsLoud is the other half of the same property, one level up: the
// invocation itself did not produce a report at all.
//
// Here the discriminator is not the report's contents but whether there is a report — the script
// requires Atlas's stdout to be a JSON array before it will interpret anything as a verdict. A
// script that instead grepped Atlas's prose for "no diagnostics found" would go permanently green
// the day Atlas reworded it, which is the trap scripts/gen-db.sh documents about `atlas schema diff`.
func TestMigrateLint_BrokenConfig_FailsLoud(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
	)

	require.NoError(t, os.WriteFile(filepath.Join(tree, "atlas.hcl"),
		[]byte("this is not valid HCL {\n"), 0o644))

	out, code := runMigrateLint(t, tree, "advise")

	require.NotEqualf(t, 0, code,
		"an unreadable atlas.hcl must fail even in MODE=advise — nothing was analysed, so there is "+
			"nothing to be advisory about.\n%s", out)
	require.Containsf(t, out, "could not complete",
		"the failure must name the invocation as the thing that failed\n%s", out)
	require.NotContainsf(t, out, "no diagnostics",
		"a failed invocation must never print the clean-run message\n%s", out)
}

// TestMigrateLint_MissingAtlas_FailsLoud — the gen-db.sh rule, restated for this script.
//
// An advisory gate that exits 0 because the analyser was absent reports "no diagnostics" about
// migrations it never read, which is strictly worse than having no job at all: it is a green check
// that means nothing. PATH is emptied rather than atlas being moved, so the test cannot disturb the
// developer's toolchain.
func TestMigrateLint_MissingAtlas_FailsLoud(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("runs the gate script; run `make test` or `make check`")
	}

	tree := migrationFixture(t, lintFixtureSchema)

	cmd := exec.Command("bash", scriptPath(t, "migrate-lint.sh"))
	cmd.Env = []string{"DKP_REPO_ROOT=" + tree, "PATH=", "MODE=advise"}

	out, err := cmd.CombinedOutput()

	require.Errorf(t, err,
		"migrate-lint.sh must FAIL when atlas is absent, never skip. A gate that exits 0 because "+
			"its analyser was missing reports a clean tree it never read.\n%s", out)
	require.Containsf(t, string(out), "atlas is not installed",
		"the failure must name the missing tool and how to get it\n%s", out)
}

// stubProAbort is what Atlas's OFFICIAL build prints instead of a report, verbatim from v1.3.0.
const stubProAbort = "Abort: Starting with v0.38, 'atlas migrate lint' is available only to Atlas Pro users."

// pathWithOfficialAtlasStub returns the caller's PATH with a temp directory PREPENDED holding an
// `atlas` that behaves the way Atlas's official build does: `version` omits the word "community",
// and everything else aborts unlicensed.
//
// A stub rather than the real thing, for the reason stubGoSentinel exists: the difference between
// the two builds is two strings, neither of which the pinned binary this suite otherwise runs can
// produce, and downloading a 30 MB unpinned artefact inside the gate that keeps the pinned one
// honest would be a strange way to test a pin. PREPENDED, because `command -v` takes the first
// match and the fixture is about which atlas wins.
func pathWithOfficialAtlasStub(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	stub := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo 'atlas version v1.3.0'; exit 0; fi\n" +
		"echo \"" + stubProAbort + "\" >&2\n" +
		"exit 1\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "atlas"), []byte(stub), 0o755),
		"write the official-build atlas stub")

	return dir + string(filepath.ListSeparator) + os.Getenv("PATH")
}

// TestMigrateLint_OfficialAtlasBuild_FailsLoud is issue #254, as a fixture.
//
// `atlas migrate lint` is Atlas Pro-only from v0.38 in the OFFICIAL build: it aborts asking for
// `atlas login` and analyses nothing. The COMMUNITY build scripts/install-atlas.sh pins runs the
// same analyzers with no account and no network — so the two differ by one word of `atlas version`
// and by whether this gate can run at all, and a contributor arrives at the wrong one by following
// Atlas's OWN advice, which suggests `curl -sSf https://atlasgo.sh | sh` after any error.
//
// The fixture is a CLEAN migration on purpose. A destructive one would fail whichever atlas ran,
// so the test would pass even if the edition check were deleted; with a clean one the only route to
// a non-zero exit is the check under test.
//
// The assertions on what the failure must NOT say are the other half. Before this check existed the
// abort fell through to the invocation-failed branch, which guesses at an unreadable atlas.hcl and a
// dev database Atlas could not open — the message that cost the reporter of #254 a wrong diagnosis.
func TestMigrateLint_OfficialAtlasBuild_FailsLoud(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
	)

	out, code := runMigrateLintEnv(t, tree, "enforce",
		"PATH="+pathWithOfficialAtlasStub(t),
		"DKP_ATLAS=")

	require.NotEqualf(t, 0, code,
		"an atlas whose migrate lint is licence-gated must FAIL the gate, never pass it. The "+
			"analysis cannot run at all on that build, and a run that checked nothing must not "+
			"report like a run that found nothing (issue #254).\n%s", out)

	require.Containsf(t, out, "community",
		"the failure must name the EDITION as the problem — that one word is the whole difference "+
			"between an atlas that can run this gate and one that cannot\n%s", out)
	require.Containsf(t, out, "make install-atlas",
		"the failure must name the command that fixes it. A gate that reports an unusable "+
			"toolchain without saying how to repair it sends the reader to a vendor's website.\n%s", out)
	require.Containsf(t, out, "atlasgo.sh",
		"the failure must warn off Atlas's own suggestion, which is how the pinned build gets "+
			"replaced by the one that cannot lint\n%s", out)

	require.NotContainsf(t, out, "unreadable atlas.hcl",
		"a licence gate must not be reported as one of the causes the invocation-failed branch "+
			"guesses at — that is the wrong diagnosis issue #254 was filed about\n%s", out)
	require.NotContainsf(t, out, "no diagnostics",
		"an atlas that never analysed anything must never print the clean-run message\n%s", out)
}

// TestMigrateLint_PinnedAtlas_BeatsAnAtlasOnPATH pins the precedence, which is the half of issue
// #254 that a message cannot fix.
//
// The Makefile APPENDS GOTOOLS_BIN to PATH so "a deliberately chosen system tool still wins" — the
// right rule for every other pinned tool here, and the wrong one for this one: the system atlas that
// wins may be the build whose migrate lint is Pro-only, and then `make check` fails for a reason
// that has nothing to do with the tree. `make lint-migrations` passes the pinned path as DKP_ATLAS,
// and this asserts the script honours it over an atlas standing earlier on PATH.
func TestMigrateLint_PinnedAtlas_BeatsAnAtlasOnPATH(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	pinned, err := exec.LookPath("atlas")
	require.NoError(t, err, "locate the atlas make setup installed")

	pinned, err = filepath.Abs(pinned)
	require.NoError(t, err, "absolute path to atlas")

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
	)

	out, code := runMigrateLintEnv(t, tree, "enforce",
		"PATH="+pathWithOfficialAtlasStub(t),
		"DKP_ATLAS="+pinned)

	require.Equalf(t, 0, code,
		"DKP_ATLAS names the pinned atlas and must be preferred over the one first on PATH. If this "+
			"fails naming the community build, the atlas on this machine is not the one "+
			"`make setup` installs — run `make install-atlas` (issue #254).\n%s", out)
	require.Containsf(t, out, "no diagnostics",
		"the pinned atlas must have actually analysed the migration, not merely been chosen\n%s", out)
}

// gitCommitFixture makes tree a git repository with a single commit and returns that commit's short
// sha and commit date, so the `--git-base` selection — the one CI takes and no other fixture here
// reaches — can be exercised.
//
// Identity passed to `git config` inside the fixture, never a global write: githooks_test.go's rule.
func gitCommitFixture(t *testing.T, tree string) (shortSHA, commitDate string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tree

		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %s\n%s", strings.Join(args, " "), out)
	}

	// stdout only for the two VALUES: `git init` writes hints to stderr on some versions, and a
	// fixture that folded them into a sha would assert on the hint.
	capture := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tree

		out, err := cmd.Output()
		require.NoErrorf(t, err, "git %s", strings.Join(args, " "))

		return strings.TrimSpace(string(out))
	}

	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Repo Test")
	run("add", "-A")
	run("commit", "-q", "-m", "migrate-lint fixture")

	return capture("rev-parse", "--short", "HEAD"), capture("log", "-1", "--format=%cs", "HEAD")
}

// TestMigrateLint_GitBase_NamesTheCommitItAnalysedAgainst covers the third finding on issue #254: a
// stale base ref silently changes what the gate analysed.
//
// "analysing every migration added versus origin/main" is the same sentence on a checkout that
// fetched an hour ago and on one that has not fetched for a week, and the two select different sets
// of migrations. Neither run is wrong on its face, which is why the reporter of #254 spent a
// diagnosis on one: an already-shipped migration looked new, so Atlas was invoked where a fresh
// checkout would have skipped it. Printing the sha and the commit date makes two runs comparable.
//
// It also exercises `--git-base` at all. Every other fixture here points the base at an absent ref
// to reach the shallow-clone fallback deterministically, which leaves the selection CI actually
// takes covered by nothing.
func TestMigrateLint_GitBase_NamesTheCommitItAnalysedAgainst(t *testing.T) {
	t.Parallel()
	requireAtlas(t)
	requireTool(t, "git", "every checkout of this repository has it")

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
	)

	sha, date := gitCommitFixture(t, tree)

	out, code := runMigrateLintEnv(t, tree, "enforce", "DKP_MIGRATE_LINT_BASE=HEAD")

	require.Equalf(t, 0, code,
		"a committed, clean migration set adds nothing versus HEAD and must pass\n%s", out)
	require.NotContainsf(t, out, "is not available in this checkout",
		"the base ref exists, so this must be the --git-base path and not the fallback — a fixture "+
			"that silently fell back would assert nothing about the selection CI uses\n%s", out)
	require.Containsf(t, out, sha,
		"the run must name the COMMIT it analysed against, not just the ref: `origin/main` on a "+
			"stale checkout and on a fresh one are the same words and a different scope "+
			"(issue #254)\n%s", out)
	require.Containsf(t, out, date,
		"the run must say how old that commit is, which is the fact that tells a reader their base "+
			"needs fetching\n%s", out)
}

// TestMigrateLint_CallSites_Enforce pins the promotion (issue #136), which is a property of the CALL
// SITES rather than of the script: MODE=enforce is the script's default AND what the Makefile passes,
// and CI reaches both through `make lint-migrations`.
//
// BOTH COPIES ARE ASSERTED, because either one alone would let the gate be downgraded silently — a
// Makefile that passed MODE=advise would beat a fail-closed default, and a default of advise would
// mean any future call site that forgets the variable stops gating. Two statements of the same
// intent is the cheapest way to make a downgrade visible in a diff.
//
// It also pins that LOCAL AND CI AGREE. `make lint-migrations` is in `make lint`, which is in
// `make check`; a gate that only enforced in CI would cost a push, a round trip and a contributor
// who did exactly what AGENTS.md told them (issues #166, #183).
func TestMigrateLint_CallSites_Enforce(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	require.Contains(t, workflow, "run: make lint-migrations",
		"ci.yml must run the migration lint; issue #131 asked for it in CI, not only in `make lint`")

	script, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "migrate-lint.sh"))
	require.NoError(t, err, "read scripts/migrate-lint.sh")

	require.Containsf(t, string(script), `MODE="${MODE:-enforce}"`,
		"scripts/migrate-lint.sh no longer DEFAULTS to enforce. Issue #136 promoted this gate: an "+
			"invocation that does not name a mode must fail closed, or a call site that forgets the "+
			"variable reports a green run over a destructive migration.")

	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	require.NoError(t, err, "read the Makefile")

	recipe := makefileRecipe(t, string(makefile), "lint-migrations")

	require.Containsf(t, recipe, "MODE=enforce",
		"`make lint-migrations` no longer passes MODE=enforce:\n%s\nThat is the call site ci.yml "+
			"and `make check` both reach, so this is where a downgrade would land. If the gate is "+
			"genuinely too noisy, say so in an issue and flip it deliberately — do not let the "+
			"mode drift out of the recipe.", recipe)
	require.NotContainsf(t, recipe, "MODE=advise",
		"`make lint-migrations` runs the linter in advisory mode:\n%s\nA diagnostic would then pass "+
			"`make check` on a laptop and fail `test / migrations` in CI, which is the split issue "+
			"#136 was promoted to close.", recipe)
}

// makefileRecipe returns the recipe lines of one Makefile target — the lines after `target:` that
// begin with a tab.
func makefileRecipe(t *testing.T, makefile, target string) string {
	t.Helper()

	start := strings.Index(makefile, "\n"+target+":")
	require.NotEqualf(t, -1, start, "the Makefile has no %s target", target)

	body := makefile[start+1:]
	body = body[strings.Index(body, "\n")+1:]

	var recipe []string

	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "\t") {
			break
		}

		recipe = append(recipe, line)
	}

	require.NotEmptyf(t, recipe, "the %s target has no recipe", target)

	return strings.Join(recipe, "\n")
}

// TestCI_MigrationLintStep_FetchesFullHistory pins fetch-depth: 0 on `test / migrations`.
//
// The script selects what to analyse with `atlas migrate lint --git-base origin/main`, so that only
// the migrations a branch ADDS are analysed — a shipped migration is frozen (MIG003), so a
// diagnostic on one is not actionable by whoever trips over it. A shallow checkout has no
// origin/main, and the script then falls back to `--latest 1`: it still runs, still exits 0, and
// says so in the log — but a branch adding two migrations would have the first go unanalysed.
//
// Same defect and same fix as `lint / repo`'s fetch-depth, which MIG003 needs for the same reason.
func TestCI_MigrationLintStep_FetchesFullHistory(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	start := strings.Index(workflow, "\n  test-migrations:\n")
	require.NotEqual(t, -1, start, "ci.yml has no test-migrations job")

	block := workflow[start:]
	if next := strings.Index(block[1:], "\n  test-"); next != -1 {
		block = block[:next+1]
	}

	require.Containsf(t, block, "fetch-depth: 0",
		"the test-migrations job checks out shallowly, so `atlas migrate lint --git-base "+
			"origin/main` cannot resolve its base and the advisory silently degrades to analysing "+
			"only the newest migration. Add `with: fetch-depth: 0` to the checkout step.\nJob "+
			"block:\n%s", block)

	require.Containsf(t, block, `tools: "atlas"`,
		"the test-migrations job must install atlas, or `make lint-migrations` hard-fails with "+
			"\"atlas is not installed\" — which is the right refusal but the wrong place to "+
			"discover it.\nJob block:\n%s", block)
}

// TestAtlasHCL_DeclaresLintAnalyzers asserts the analyzer policy issue #131 asked for is actually
// declared, and that the one thing that must NOT be declared alongside it still isn't.
//
// Atlas rejects `--latest` when the config carries a `lint { git { … } }` block ("--latest and
// --git-base are mutually exclusive"). scripts/migrate-lint.sh needs both selections — `--git-base`
// in CI, `--latest` on a shallow clone and in every fixture in this file — so a `git` block added to
// atlas.hcl would make the fallback path and this whole suite unrunnable, in a way whose error
// message names neither.
func TestAtlasHCL_DeclaresLintAnalyzers(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join(repoRoot(t), "atlas.hcl"))
	require.NoError(t, err, "read atlas.hcl")

	hcl := string(b)

	lintAt := strings.Index(hcl, "\n  lint {")
	require.NotEqual(t, -1, lintAt,
		"atlas.hcl declares no `lint` block. Issue #131 added one so that Atlas's own "+
			"destructive/data-dependent/incompatible analyzers run over db/migrations-sqlite/; "+
			"without it `atlas migrate lint` falls back to its defaults and the policy this "+
			"repository chose is not the policy that runs.")

	block := hcl[lintAt:]
	if end := strings.Index(block, "\n  }\n"); end != -1 {
		block = block[:end]
	}

	for _, analyzer := range []string{"destructive", "data_depend", "incompatible"} {
		require.Containsf(t, block, analyzer+" {",
			"atlas.hcl's lint block no longer enables the %q analyzer. Removing one narrows what "+
				"the gate sees without changing anything visible in CI.\nBlock:\n%s", analyzer, block)
	}

	require.NotContainsf(t, block, "git {",
		"atlas.hcl's lint block declares a `git` block. Atlas then REJECTS `--latest` "+
			"(\"--latest and --git-base are mutually exclusive\"), which breaks "+
			"scripts/migrate-lint.sh's shallow-clone fallback and every fixture in "+
			"migrate_lint_test.go. The selection belongs to the script, not to the config.\n"+
			"Block:\n%s", block)
}

// TestMigrationGates_AtlasLint_IsAdditive is the anti-removal assertion issue #131 asked for in
// as many words: "keep MIG001-003, the fresh-install fingerprint, and the populated-upgrade gate.
// This is additive coverage, not a replacement."
//
// Atlas lint does not know this repository's rules. It has never heard of forward-only migrations,
// of a SHIPPED.lock file freezing what has been released, or of append-only triggers that a 12-step
// table rebuild silently drops. Someone reading a green `security`-looking migration linter could
// reasonably conclude the bespoke gates are now redundant; they are not, and the cost of finding out
// otherwise is a guild's ledger. So the claim is a test rather than a paragraph.
func TestMigrationGates_AtlasLint_IsAdditive(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	gates, err := os.ReadFile(filepath.Join(root, "scripts", "repo-gates.sh"))
	require.NoError(t, err, "read scripts/repo-gates.sh")

	for _, rule := range []string{"MIG001", "MIG002", "MIG003"} {
		require.Containsf(t, string(gates), rule,
			"%s is gone from scripts/repo-gates.sh. `atlas migrate lint` does not replace it: Atlas "+
				"knows nothing about forward-only Down blocks, backtick identifiers, or a "+
				"frozen-shipped-migration file. Issue #131 required these be kept.", rule)
	}

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err, "read the Makefile")

	require.Contains(t, string(makefile), "test-migrations:",
		"the test-migrations target is gone. It carries the fresh-install fingerprint and the "+
			"populated-upgrade gate, neither of which Atlas can express.")

	workflow := readCIWorkflow(t)
	require.Contains(t, workflow, "run: make test-migrations",
		"ci.yml no longer runs `make test-migrations`. The Atlas lint added for issue #131 and "+
			"promoted in #136 is ADDITIVE — it does not cover the fresh-install fingerprint, the "+
			"append-only-trigger survival check, or the protected-table row-count assertions, and "+
			"it covers them no better for blocking.")

	require.Contains(t, workflow, "run: make lint-migrations",
		"ci.yml no longer runs the migration lint either — this test would then be asserting the "+
			"absence of both.")
}
