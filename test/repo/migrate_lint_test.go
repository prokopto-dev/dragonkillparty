// Negative fixture tests for scripts/migrate-lint.sh — the advisory migration linter (issue #131).
//
// The property under test is unusual enough to be worth stating plainly: this gate is ADVISORY, so
// "it fires" is only half of it. The other half is that firing does NOT fail the build. A test that
// only checked for a non-zero exit would pass just as happily if somebody promoted the gate by
// accident, and the whole reason #131 asked for advisory-first is that SQLite's 12-step table
// rebuild is where Atlas's analyzers are least predictable — a linter that blocks merges before it
// is trusted gets disabled rather than tuned.
//
// So each fixture is run through BOTH modes and the difference is the assertion:
//
//	MODE=advise    diagnostic printed, `::warning::` emitted, exit 0
//	MODE=enforce   diagnostic printed, exit non-zero
//
// and separately, that a BROKEN invocation is a hard failure in both — because a scan that never
// ran must never report as a scan that found nothing. That distinction is the one thing an advisory
// gate can get wrong in a way nobody notices.
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
// therefore be hashed rather than hand-written — and TestMigrateLint_ChecksumMismatch_FailsLoud
// below deliberately breaks it again afterwards, which is only a meaningful test because every
// other fixture here is valid.
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
// Environment built explicitly rather than with t.Setenv, for gates_test.go's reason: t.Setenv makes
// t.Parallel() panic.
func runMigrateLint(t *testing.T, tree, mode string) (output string, exitCode int) {
	t.Helper()

	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("bash", scriptPath(t, "migrate-lint.sh"))
	cmd.Env = append(os.Environ(),
		"DKP_REPO_ROOT="+tree,
		"DKP_MIGRATE_LINT_BASE="+absentBaseRef,
		"MODE="+mode,
	)

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

	if _, err := exec.LookPath("atlas"); err != nil {
		t.Skip("atlas is not installed; run make setup")
	}
}

// TestMigrateLint_DestructiveChange_UnderAdvise_WarnsWithoutFailing is the headline property.
//
// A dropped column IS reported — with the analyzer code, so the reader can look it up — and the
// script still exits 0. Both halves are the point: the finding is not swallowed, and it does not
// block the merge. Issue #136 tracks flipping the second half once the analyzers have been seen
// against a real 12-step rebuild.
func TestMigrateLint_DestructiveChange_UnderAdvise_WarnsWithoutFailing(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, \"label\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
		"000002_drop_label.sql", "-- +goose Up\n"+
			"ALTER TABLE \"fixture_widget\" DROP COLUMN \"label\";\n"+
			gooseDownStub,
	)

	out, code := runMigrateLint(t, tree, "advise")

	require.Equalf(t, 0, code,
		"MODE=advise must NOT fail the build on a diagnostic — that is what 'advisory-first' means "+
			"in issue #131, and .github/workflows/ci.yml runs this script without MODE set. If this "+
			"is now non-zero the gate was promoted; do that deliberately via issue #136, and flip "+
			"this test rather than deleting it.\n%s", out)

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
		"the output must say the finding did not fail the build, or a reader who sees a diagnostic "+
			"and a green check has to guess which one is wrong.\n%s", out)
}

// TestMigrateLint_DestructiveChange_UnderEnforce_Fails proves the enforcing half already works.
//
// This is what makes issue #136 a one-word change at the call site rather than new machinery, and
// it is what stops "advisory" from meaning "the failing path was never written".
func TestMigrateLint_DestructiveChange_UnderEnforce_Fails(t *testing.T) {
	t.Parallel()
	requireAtlas(t)

	tree := migrationLintFixture(t,
		"000001_init.sql", "-- +goose Up\n"+
			"CREATE TABLE \"fixture_widget\" (\"id\" text NOT NULL, \"label\" text NOT NULL, PRIMARY KEY (\"id\")) STRICT;\n"+
			gooseDownStub,
		"000002_drop_label.sql", "-- +goose Up\n"+
			"ALTER TABLE \"fixture_widget\" DROP COLUMN \"label\";\n"+
			gooseDownStub,
	)

	out, code := runMigrateLint(t, tree, "enforce")

	require.NotEqualf(t, 0, code,
		"MODE=enforce must fail on a diagnostic, or promoting the gate (#136) would be a no-op that "+
			"looked like a gate.\n%s", out)
	require.Containsf(t, out, "DS103", "the failure must name the analyzer that fired\n%s", out)
	require.Containsf(t, out, "MODE=enforce",
		"the failure must say WHICH mode failed it, so an unexpected red is traceable to the call "+
			"site rather than to the migration.\n%s", out)
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

// TestMigrateLint_CI_IsAdvisory pins the call site.
//
// `make lint-migrations` runs the script with no MODE, which is MODE=advise. The assertion is
// deliberately in the "is still advisory" direction: promoting the gate is a decision to take in
// issue #136 with the evidence #131 asked for, not a change to make in passing while editing a
// nearby step.
func TestMigrateLint_CI_IsAdvisory(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflow(t)

	require.Contains(t, workflow, "run: make lint-migrations",
		"ci.yml must run the migration lint; issue #131 asked for it in CI, not only in `make lint`")

	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "lint-migrations") {
			continue
		}

		require.NotContainsf(t, trimmed, "MODE=enforce",
			"ci.yml runs the migration lint in enforce mode:\n\t%s\nThat is a real promotion and a "+
				"good one — but do it through issue #136, updating "+
				"docs/design/06-cicd-and-release.md and inverting "+
				"TestMigrateLint_DestructiveChange_UnderAdvise_WarnsWithoutFailing in the same "+
				"change, so the docs and the tests describe the gate that runs.", line)
	}
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
		"ci.yml no longer runs `make test-migrations`. The advisory Atlas lint added for issue #131 "+
			"is ADDITIVE — it does not cover the fresh-install fingerprint, the append-only-trigger "+
			"survival check, or the protected-table row-count assertions.")

	require.Contains(t, workflow, "run: make lint-migrations",
		"ci.yml no longer runs the advisory migration lint either — this test would then be "+
			"asserting the absence of both.")
}
