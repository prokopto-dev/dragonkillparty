# ADR-0020 — Two test lanes, and `-shuffle=on` moves to nightly

**Status:** accepted · **Date:** 2026-08-12 · **Deciders:** owner

## Context and problem statement

Issue #153 gave CI a rolling `$GOCACHE`, expecting two wins: incremental compilation and `go test`
result caching, so an unchanged package reports `(cached)` instead of re-running. Only the first
landed. Every recipe passed `-count=1`, which disables the result cache by definition, and
`-shuffle=on`, which defeats it independently — `-test.shuffle` is not in the cacheable flag set and
its seed is the wall clock. `docs/design/04-testing.md` made both normative: "`-shuffle=on -count=1`
always."

Both flags are there on purpose. `-count=1` is a **gate**: the result cache tracks the files a test
reads *through Go*, never what a subprocess reads, so a package that reaches its subject through
`bash`, `golangci-lint`, `git` or `pnpm` can report `ok (cached)` after the very script it polices
changed. `test/repo/gate_cache_test.go` demonstrates that on a fixture. `-shuffle=on` finds
order-dependence *over time*; `internal/*/kinds`, `internal/authz`, `internal/api` and
`internal/clock` all cite it by name as why they return copies rather than package-level slices.

So issue #155 was filed as a decision rather than a chore: the acceptance line of #153 is
unreachable while both flags stand, and removing either costs something real.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Keep `-shuffle=on -count=1` everywhere | Zero risk, zero work; the status quo and defensible | CI pays full test *execution* on every run and banks only compilation; #153 stays half-landed |
| B — Split the suite: `-count=1` where a subprocess is reachable, cache the rest | Buys back the execution time of every package a PR did not touch | Needs a lane split that cannot be wrong, and gives up per-PR shuffle on the cached lane |
| C — Cache everything and drop `-count=1` outright | Simplest, fastest | A changed gate script reports a cached pass — green on exactly the change the gate exists to catch |
| D — Keep `-count=1`, pin a fixed `-shuffle` seed | Preserves one shuffled order cheaply | `-count=1` alone already defeats the cache, so it buys nothing; a fixed seed shuffles once and forever |

## Decision outcome

**Chosen: B.** The suite runs in two lanes, computed in the Makefile and printed by
`make test-lanes`:

- **The gate lane** — `test/repo`, `internal/api`, `internal/core`, `internal/licence`,
  `internal/repogate`, `internal/specgate` — is every package whose sources, test or not, can spawn
  a subprocess. It keeps `-shuffle=on -count=1` unconditionally.
- **The cacheable lane** is the complement, computed with `go list` rather than listed, so a new
  package is cacheable by default and nothing has to remember to add it anywhere.

Two packages can spawn a subprocess and are cacheable anyway, because what they spawn is hermetic:
`internal/store` re-execs its own content-addressed test binary, and `internal/migrate/shippedlock`
drives `git` over a repository the test creates in `t.TempDir()`. Each is an entry in
`hermeticExecPackages` with its argument attached, and the exemption is re-derived on every run.

`-shuffle=on` leaves the per-PR path and becomes `nightly-verify.yml`'s `suite / shuffled`, which
runs both suites with `DKP_TEST_SHUFFLE=on` — one environment variable read by the Makefile, no
second code path, the shape the nightly property job already uses for `DKP_PROPERTY_CHECKS`.

**Enforced by:** `test/repo/gate_cache_test.go` —
`TestGoTestLanes_Partition_TheWholeModule` (the lanes are a partition of `go list ./...`, so no
package falls out of the suite), `TestGoTestLanes_EveryPackageThatCanShellOut_IsInTheGateLane`,
`TestMakefile_EveryGoTestRecipe_ForcesRerun` and `TestMakefile_CacheableRecipes_NameNoGatePackage`,
which expands each recipe with `make -n` and resolves the packages it actually selects.

### Consequences

- Good, because a re-push that changed nothing reports `(cached)` for every untouched package, which
  is what issue #153 asked for and did not get.
- Good, because `-count=1` is now argued per package instead of applied by habit, and the argument is
  a test rather than a comment.
- **Bad, because order-dependence is found a day later than it would have been.** A nightly re-roll
  still finds it, against a day of merges rather than one diff, and files an issue.
- **Bad, because "always" was easier to remember than a rule with a lane.** `make test-lanes` prints
  the answer, and the failure messages name the file to edit.
- Bad, because a package that starts shelling out through a helper this scan cannot see would cache
  wrongly until somebody notices. The scan reads non-test sources for exactly that reason —
  `internal/licence` names no `exec.Command` in its tests and still shells out.

### Reversal cost

Minutes. Set `TEST_CACHE_FLAGS := -shuffle=on -count=1` unconditionally in the Makefile and the two
lanes collapse to the old behaviour with the tests still passing; deleting the split as well is an
afternoon.
