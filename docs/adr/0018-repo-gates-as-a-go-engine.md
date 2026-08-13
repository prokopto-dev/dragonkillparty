# ADR-0018 — Repository gates as a Go engine, not grep, HCL or `go/analysis`

**Status:** accepted · **Date:** 2026-08-11 · **Deciders:** owner
**Amended in part by [ADR-0021](0021-hcl-rule-catalogue-and-schema-parse.md):** option B's catalogue
half was taken up once the dependency was approved. The rest of this record stands as written.

## Context and problem statement

`scripts/repo-gates.sh` was 844 lines of `grep` and `awk` enforcing the architectural laws, the
money rules, the supply-chain pins and the AGPL firewall. Its logic could be observed only through a
subprocess, so branches such as an aliased import or a heredoc enum list had no test of their own.
The pattern class also misfires: one helper matched the wrong prefix and silently stripped nothing
for months, and law 2's exclusion had to live inside the pattern because an allowlist is line
scoped. Issue #123 asked for HCL-declared rules with a Go engine.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Keep the shell gates | Already worked; no toolchain needed in the lint job | Untestable except as a subprocess; portability breaks (#111); a pattern cannot tell a comment from a call |
| B — HCL rule files plus a Go engine | Rules become reviewable data; the schema gate reads HCL anyway | `hashicorp/hcl/v2` is a new direct dependency, which a human decides; a parse failure on a half-written file turns a gate into a bypass |
| C — `golang.org/x/tools/go/analysis` analyzers | The standard shape for a Go-syntax rule; composes with `go vet` | Every driver loads type-checked packages through `go/packages`, so it needs a module that builds — and every negative fixture here is a tainted tree in `t.TempDir()` with no `go.mod` |
| D — A Go engine with a typed rule catalogue and `go/parser` analyzers | Directly unit-testable; reads a tree that does not compile; no dependency to propose | The catalogue is Go source rather than data a non-Go reader edits |

## Decision outcome

**Chosen: D.** The rules run against trees that do not build, and that is the property deciding it.
A gate is installed before the code it gates, so it must pass vacuously on a missing tree; a
negative fixture is a deliberately broken tree with no module. Option C cannot load either, which
would leave the analyzers pointed at the real checkout alone — trusted rather than tested.

The split, in `internal/repogate`:

- **Config-shaped rules are data.** One `textRule` per rule and tree: globs, a pattern, an
  allowlist, and the comment openers to strip. The catalogue holds no logic.
- **Go-syntax laws are analyzers**, in `go/analysis`'s shape — `{id, description, tree, run}` over
  one parsed file — so adopting the framework later changes the driver, not the rules.
- **The AGPL firewall strips no comments.** Transcribed AGPL source in a Go comment infringes as
  much as in code. That exception is per-rule data with the reason attached.
- **`scripts/repo-gates.sh` stays the entry point**, as a shim. Every negative fixture in
  `test/repo/` carries over unedited and now exercises the Go rules.

**Enforced by:** `make lint-repo` in `make lint`, the required `lint / repo` CI job, the fixtures in
`test/repo/gates_test.go`, and `TestRepoGates_ScriptDelegatesToTheEngine`, which fails if a rule is
reimplemented in the shim.

### Consequences

- Good, because a rule is unit-tested as a function instead of through a 250 ms subprocess.
- Good, because an aliased import no longer defeats a law, and prose about a rule no longer trips it.
- Good, because the lint job gained no dependency: the engine is standard library only.
- **Bad, because the lint job now needs the Go toolchain for every rule, not only MIG003.** No Go is
  a hard failure, `GATE000`.
- **Bad, because rules are Go source, so a non-Go contributor reads code to learn what is banned.**
  The catalogue is a table with one entry per rule to keep that cost low.

### Reversal cost

A day. The rule catalogue maps one-to-one onto the patterns it replaced, and the fixtures are
unchanged, so a return to shell is a transcription with a suite that already proves it.
