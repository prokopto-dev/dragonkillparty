// Tests that `make check` runs every gate a merge is actually blocked on (issue #183).
//
// AGENTS.md tells every contributor and every agent that `make check` is what "done" means, and the
// Makefile states the standard itself, two lines above the target: "a required job that `make check`
// does not run makes this target's promise false." That sentence is either the rule or it is
// decoration. Issue #166 was it being false for `budget-bundle` — found by hand, months after the
// hole opened, because somebody happened to need a bundle measurement. Nothing in the repository
// would have found it, and by the time it was found there were five more.
//
// So the list is derived rather than remembered:
//
//  1. ci-required's `needs:` is the set of blocking jobs — it is the only check in branch protection,
//     so a job outside it cannot block a merge and a job inside it can.
//  2. Each of those jobs' `make <target>` invocations are the gates it runs.
//  3. `check`'s transitive prerequisites, plus the targets its recipes invoke as `$(MAKE) <target>`,
//     are what `make check` runs.
//  4. A required target outside that closure must be a row in checkExemptions below, carrying the
//     reason it is out. The point is that it becomes a DECISION rather than an omission nobody can
//     see; the wall clock is the real trade and it is argued in the row.
//
// A target whose recipe is still a `$(call notyet,…)` stub needs no row: it does no work, so running
// it would prove nothing, and the exemption clears itself the day the target starts doing work —
// derived from the same call sites `make status` reads rather than from a hand-maintained list.
//
// Both directions are driven against fixture text as well as against this tree, because a derivation
// that quietly stopped parsing would report a clean sweep it never performed — the vacuous green
// this package exists to prevent.
//
// The ci.yml parsing is deliberately dumb, for the reason ci_required_test.go gives: promoting
// gopkg.in/yaml.v3 to a direct dependency to read a file this regular would mean adding a dependency
// for a test, which AGENTS.md requires a human to approve.
package repo_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// checkExemption is one reviewed row: a gate a blocking CI job runs and `make check` deliberately
// does not, plus the reason. A row is a decision somebody made and a reviewer can disagree with.
type checkExemption struct {
	target string
	why    string
}

// checkExemptions is that table. Adding a row is the cheap half; the expensive half is that the row
// has to stay true, and TestCheck_ExemptionTable_HasNoStaleRows deletes it again the moment it is not.
//
// Three categories, and nothing else has been admitted:
//
//   - it needs something `make check` is not allowed to need — the network, a browser, a Docker
//     daemon. (`osv-scan` and `govulncheck` already had this exemption in prose above the `check`
//     target; only govulncheck has a row, because CI runs osv-scanner as a pinned container action
//     rather than through the Makefile, so nothing derives it as a required target.)
//   - it would leave the working tree dirty on every run;
//   - it is the name of a CI lane whose tests `make test` already executes.
var checkExemptions = []checkExemption{
	{
		target: "govulncheck",
		why: "it fetches the vulnerability database from vuln.go.dev, and `make check` is expected " +
			"to work on a laptop with no connectivity — a check that fails on a train teaches people " +
			"to skip it. Run it by hand before proposing a dependency; CI runs it as its own " +
			"required job.",
	},
	{
		target: "test-e2e",
		why: "it downloads a browser and drives the built binary, and docs/design/04-testing.md's " +
			"inner-loop doctrine is `never run the E2E suite in the inner loop`. CI runs it as its " +
			"own sharded required job.",
	},
	{
		target: "docker",
		why:    "it needs a Docker daemon and buildx, which `make check` must not require.",
	},
	{
		target: "smoke-local",
		why:    "it boots the image `make docker` built, so it needs the same daemon.",
	},
	{
		target: "image-size",
		why:    "it measures the image `make docker` built, so it needs the same daemon.",
	},
	{
		target: "build",
		why: "scripts/build-web.sh stages web/dist over internal/ui/dist, which DELETES the tracked " +
			"placeholder assets and leaves the working tree dirty — every run, for every " +
			"contributor. Both halves are covered without that cost: `make vet` compiles every " +
			"package including ./cmd/dkp, and `make budget-bundle` performs the same Vite build with " +
			"DKP_WEB_STAGE=0, which is the staging-free form that keeps `make check` clean.",
	},
	{
		target: "test-unit",
		why: "`make test` runs the same two lanes without -short and with -race, so it is a strict " +
			"superset. The separate target exists so the fast suite has a name and its own CI job.",
	},
	{
		target: "test-property",
		why: "the properties are ordinary Go tests in internal/ledger and internal/strategy, so " +
			"`make test` has already executed all of them at the per-PR count. The separate target " +
			"and job exist so a property failure names its own category in the checks list, and so " +
			"the nightly lane can re-run just those tests at 20,000 checks.",
	},
	{
		target: "test-migrations",
		why: "test/migrations is a package in `go list ./...`, so `make test` already applies every " +
			"migration to a real database — with -race, which this target does not use. The " +
			"separate target is the CI lane's name, not additional coverage.",
	},
	{
		target: "bench-clone",
		why: "it is a printed measurement, not a threshold: ci.yml says so where it runs it (`a " +
			"regression is read in the log, not enforced here, because a wall-clock assertion on a " +
			"shared runner is a flake generator`). There is no verdict for `make check` to carry.",
	},
}

// makeInvocationRe matches a `make <target>` written in a workflow step.
var makeInvocationRe = regexp.MustCompile(`\bmake ([a-z][a-z0-9-]*)`)

// makeTargetLineRe matches a Makefile rule line: a target in column 0 with its prerequisites after
// the colon. `[^=]` rules out `foo:=bar`; every variable in this Makefile is upper-case anyway, and
// the ones assigned with spaces (`empty          :=`) fail the anchored name match.
var makeTargetLineRe = regexp.MustCompile(`^([a-z][a-z0-9-]*):([^=].*)?$`)

// subMakeRe matches a recipe's own `$(MAKE) …` call, up to the end of that shell command.
var subMakeRe = regexp.MustCompile(`\$\(MAKE\)([^\n;|&]*)`)

// makefileTargets maps every target in Makefile text to the prerequisites on its rule line.
func makefileTargets(makefile string) map[string][]string {
	targets := map[string][]string{}

	for _, line := range strings.Split(makefile, "\n") {
		m := makeTargetLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		targets[m[1]] = strings.Fields(m[2])
	}

	return targets
}

// subMakeTargets returns the targets a recipe invokes as `$(MAKE) [flags] <target>`. verify-generated
// reaches `gen` and `generated-digest` this way and no other, so a closure that followed only rule
// lines would report `make check` as never regenerating anything.
func subMakeTargets(recipe string) []string {
	var out []string

	for _, call := range subMakeRe.FindAllStringSubmatch(recipe, -1) {
		for _, word := range strings.Fields(call[1]) {
			if strings.HasPrefix(word, "-") || strings.Contains(word, "=") {
				continue // a flag, or a variable override before the target
			}

			out = append(out, word)

			break // the first word that is neither is the target
		}
	}

	return out
}

// checkClosure returns every target `make check` reaches, through prerequisites and through the
// `$(MAKE)` calls in the recipes it runs.
func checkClosure(makefile string) map[string]bool {
	prerequisites := makefileTargets(makefile)
	recipes := makefileRecipesIn(makefile)

	closure := map[string]bool{}

	var walk func(string)

	walk = func(name string) {
		if closure[name] {
			return
		}

		closure[name] = true

		for _, prerequisite := range prerequisites[name] {
			walk(prerequisite)
		}

		for _, called := range subMakeTargets(recipes[name]) {
			if _, isTarget := prerequisites[called]; isTarget {
				walk(called)
			}
		}
	}

	walk("check")

	return closure
}

// requiredMakeTargets maps each `make <target>` a blocking job runs to the jobs that run it.
func requiredMakeTargets(t *testing.T, workflow string) map[string][]string {
	t.Helper()

	jobs := workflowJobs(t, workflow)
	byTarget := map[string][]string{}

	for _, job := range ciRequiredNeeds(t, workflow) {
		body, ok := jobs[job]
		require.Truef(t, ok, "ci-required needs %q, which is not a job block in ci.yml", job)

		// Comments are stripped whole-line AND trailing: this workflow's steps document themselves
		// with `- run: make lint-go # gofumpt -l, goimports, golangci-lint`, and its job headers name
		// targets in prose. Prose about a gate is not the gate.
		for _, m := range makeInvocationRe.FindAllStringSubmatch(stripYAMLComments(body), -1) {
			if !contains(byTarget[m[1]], job) {
				byTarget[m[1]] = append(byTarget[m[1]], job)
			}
		}
	}

	return byTarget
}

// contains reports whether a slice already holds a value.
func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}

	return false
}

// isNotYetStub reports whether a target's whole recipe is `$(call notyet,…)` — declared, and doing
// no work until the phase it names. `make status` derives its list from the same call sites.
func isNotYetStub(recipe string) bool {
	lines := 0

	for _, line := range strings.Split(recipe, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		lines++

		if !strings.Contains(line, "$(call notyet,") {
			return false
		}
	}

	return lines > 0
}

// missingRequiredGates returns the blocking-CI targets `make check` neither runs nor exempts, one
// human-readable line each. Empty is the only acceptable answer for this repository.
func missingRequiredGates(t *testing.T, makefile, workflow string) []string {
	t.Helper()

	required := requiredMakeTargets(t, workflow)
	closure := checkClosure(makefile)
	recipes := makefileRecipesIn(makefile)

	exempt := map[string]string{}
	for _, e := range checkExemptions {
		exempt[e.target] = e.why
	}

	var missing []string

	for _, target := range sortedKeys(required) {
		switch {
		case closure[target], exempt[target] != "":
			continue
		case isNotYetStub(recipes[target]):
			continue // declared, does no work yet, and rejoins this sweep the day it does
		}

		missing = append(missing, fmt.Sprintf("make %s (run by %s)",
			target, strings.Join(required[target], ", ")))
	}

	return missing
}

// TestCheck_EveryRequiredCIGate_IsRunOrExempted is the gate issue #183 asks for.
func TestCheck_EveryRequiredCIGate_IsRunOrExempted(t *testing.T) {
	t.Parallel()

	makefile := readRepoFile(t, "Makefile")
	workflow := readCIWorkflow(t)

	// NO VACUOUS PASS, in both directions. If either derivation breaks — a `needs:` list that stops
	// parsing, a job splitter that returns empty bodies, a Makefile whose rule lines change shape —
	// the sweep below is over an empty set and reports green on exactly the tree it exists to catch.
	// Named rather than counted: a count says "eleven of something".
	required := requiredMakeTargets(t, workflow)
	for _, target := range []string{"lint-repo", "test", "budget-bundle", "verify-generated", "build"} {
		require.Containsf(t, required, target,
			"the required-gate derivation did not find `make %s`, which ci.yml plainly runs — the "+
				"ci.yml parsing is broken, so a clean run of this test proves nothing. Derived: %v",
			target, sortedKeys(required))
	}

	closure := checkClosure(makefile)
	for _, target := range []string{"lint-repo", "licence-gate", "test-coverage-floor", "budget-bundle"} {
		require.Truef(t, closure[target],
			"`make check` plainly reaches %s and the closure says it does not — the Makefile parsing "+
				"is broken. Closure: %v", target, sortedKeys(closure))
	}

	require.True(t, closure["gen"],
		"the closure did not follow `verify-generated`'s `$(MAKE) gen`, so it is not reading sub-make "+
			"calls — and `make check`'s largest addition would read as absent")

	require.Empty(t, missingRequiredGates(t, makefile, workflow),
		"these targets are run by a job in ci-required's `needs:` — the only check branch protection "+
			"holds — and `make check` does not run them.\n"+
			"AGENTS.md tells every contributor that `make check` is what \"done\" means, so each one "+
			"is a push, a CI round trip and somebody who did exactly what the docs said. Add the "+
			"target to `check` in the Makefile, or add a row to checkExemptions in this file saying "+
			"why not (issue #183).")
}

// checkFixtureMakefile is a Makefile in miniature: one gate `check` runs directly, one it reaches
// only through `$(MAKE)`, one stub, one exempted target, and one gate that is simply missing.
const checkFixtureMakefile = `
lint-repo:
	@bash scripts/repo-gates.sh

verify-generated:
	@before=$$($(MAKE) --no-print-directory generated-digest) && $(MAKE) --no-print-directory gen

gen:
	@bash scripts/gen-db.sh

generated-digest:
	@sha256sum

lint-web:
	@pnpm run lint

test-golden:
	@$(call notyet,Phase 4,parser golden files)

docker:
	@docker buildx build .

check: lint-repo verify-generated
	@printf 'done\n'
`

// checkFixtureWorkflow is a ci.yml in miniature, in the shape ciRequiredNeeds and workflowJobs parse:
// five blocking jobs, one running a target the fixture Makefile's `check` never reaches.
const checkFixtureWorkflow = `jobs:
  changes:
    name: changes
  lint-repo:
    name: lint / repo
    steps:
      - run: make lint-repo
  codegen-drift:
    name: gen / codegen-drift
    steps:
      # make verify-generated runs make gen — prose about a target, not an invocation of one.
      - run: make verify-generated
  lint-web:
    name: lint / web
    steps:
      - run: make lint-web # eslint over src/**
  test-golden:
    name: test / golden
    steps:
      - run: make test-golden
  build-image:
    name: build / image
    steps:
      - run: make docker
  ci-required:
    name: ci-required
    needs:
      - changes
      - lint-repo
      - codegen-drift
      - lint-web
      - test-golden
      - build-image
`

// TestCheck_ClosureGate_FailsOnAMissingRequiredTarget drives the sweep against fixture text, because
// the assertion above passes on the tree as it is and a gate that has only ever passed proves
// nothing about what it would catch. Issue #166 is the shape being reproduced: one required target,
// absent from `check`, with everything around it correct.
func TestCheck_ClosureGate_FailsOnAMissingRequiredTarget(t *testing.T) {
	t.Parallel()

	t.Run("a required target check does not run is reported", func(t *testing.T) {
		t.Parallel()

		require.Equal(t,
			[]string{"make lint-web (run by lint-web)"},
			missingRequiredGates(t, checkFixtureMakefile, checkFixtureWorkflow),
			"the sweep must report exactly the missing target — no more (`test-golden` is a notyet "+
				"stub, `docker` is an exemption row, `gen` is reached through $(MAKE), and the "+
				"codegen-drift job's COMMENT names two targets that are not invocations) and no "+
				"fewer (the fixture's `check` really does not run lint-web)")
	})

	t.Run("adding it to check clears the finding", func(t *testing.T) {
		t.Parallel()

		fixed := strings.Replace(checkFixtureMakefile,
			"check: lint-repo verify-generated",
			"check: lint-repo verify-generated lint-web", 1)
		require.NotEqual(t, checkFixtureMakefile, fixed, "the fixture's check rule did not rewrite")

		require.Empty(t, missingRequiredGates(t, fixed, checkFixtureWorkflow),
			"a target named in `check`'s prerequisites must satisfy the sweep")
	})

	t.Run("a stub that starts doing work rejoins the sweep", func(t *testing.T) {
		t.Parallel()

		implemented := strings.Replace(checkFixtureMakefile,
			"\t@$(call notyet,Phase 4,parser golden files)",
			"\t@$(GO) test ./test/golden/...", 1)
		require.NotEqual(t, checkFixtureMakefile, implemented, "the fixture's stub did not rewrite")

		require.Contains(t, missingRequiredGates(t, implemented, checkFixtureWorkflow),
			"make test-golden (run by test-golden)",
			"the notyet exemption must clear itself the day the target does real work — otherwise a "+
				"stub is a permanent hole that nobody has to argue for")
	})
}

// TestCheck_ExemptionTable_HasNoStaleRows keeps the table from becoming the thing it replaced.
//
// A waiver that outlives its reason is worse than no waiver: it is a hole in the gate that reads as a
// decision. Three ways a row rots, and each is checked — the target stops existing, the job that ran
// it goes away, or `check` starts running it anyway and the row silently covers nothing.
func TestCheck_ExemptionTable_HasNoStaleRows(t *testing.T) {
	t.Parallel()

	targets := makefileTargets(readRepoFile(t, "Makefile"))
	required := requiredMakeTargets(t, readCIWorkflow(t))
	closure := checkClosure(readRepoFile(t, "Makefile"))

	require.NotEmpty(t, checkExemptions, "the exemption table is empty — has it been deleted?")

	seen := map[string]bool{}

	for _, e := range checkExemptions {
		require.NotEmptyf(t, e.why, "the %s exemption carries no reason", e.target)
		require.Falsef(t, seen[e.target], "checkExemptions lists %s twice", e.target)
		seen[e.target] = true

		require.Containsf(t, targets, e.target,
			"checkExemptions exempts %q, which is not a Makefile target — drop the row", e.target)

		require.Containsf(t, required, e.target,
			"checkExemptions exempts %q, which no job in ci-required's `needs:` runs any more. The "+
				"row waives nothing, and the next omission would inherit it — drop it.", e.target)

		require.Falsef(t, closure[e.target],
			"checkExemptions exempts %q and `make check` now runs it anyway. Drop the row: an "+
				"exemption nobody is using is a waiver the next target gets for free.", e.target)
	}
}

// TestCheck_NoRequiredGate_RunsThroughGoRun pins ADR-0022 at the Makefile call sites that matter.
//
// A Go-implemented GATE is compiled and run, never `go run`: `go run` collapses every child status
// into 1, so "the gate failed" and "the job mistyped the command" arrive identically, and it appends
// `exit status 1` to stderr after the gate's own explanation. Issue #142 closed that for
// scripts/repo-gates.sh — TestRepoGates_ScriptDelegatesToTheEngine pins it there — and issue #180
// for the Makefile, where `licence-gate`, `verify-spec` and the two shipped-lock targets were still
// invoking a gate the collapsing way.
//
// Scoped to the targets a BLOCKING job runs, plus the release gate and its seal half, rather than to
// every recipe in the file: `go run` is right for a generator, which is the ADR's first line, and
// scripts/gen-*.sh and `make mockup-site` are generators. A gate is a program whose whole output is
// a verdict somebody reads while their PR — or their release — is red.
func TestCheck_NoRequiredGate_RunsThroughGoRun(t *testing.T) {
	t.Parallel()

	recipes := makefileRecipes(t)
	gates := sortedKeys(requiredMakeTargets(t, readCIWorkflow(t)))

	// The release path is not in ci-required — nothing about a tag is — so its two are named. They
	// are the pair issue #180 is about: release.yml's `prepare` job runs the first before any image,
	// binary, attestation or moving tag exists, which is the last moment a missing SHIPPED.lock row
	// is free to fix.
	gates = append(gates, "release-shipped-lock", "shipped-lock-seal")

	// NO VACUOUS PASS: the four call sites #180 converted must be in the swept set, or this passes
	// by having swept nothing.
	for _, want := range []string{"licence-gate", "verify-spec", "release-shipped-lock"} {
		require.Containsf(t, gates, want, "the swept gate set is missing %s: %v", want, gates)
	}

	for _, target := range gates {
		recipe, ok := recipes[target]
		require.Truef(t, ok, "the Makefile has no %s target", target)

		for _, banned := range []string{"$(GO) run ", "go run "} {
			require.NotContainsf(t, recipe, banned,
				"`make %s` is a gate invoked through `go run`, which ADR-0022 forbids: it collapses "+
					"the command's exit status into 1 and appends `exit status 1` to stderr after the "+
					"gate's own explanation, inside the block a human reads while something is red. "+
					"Use the go_gate macro at the top of the Makefile.\n%s", target, recipe)
		}
	}
}
