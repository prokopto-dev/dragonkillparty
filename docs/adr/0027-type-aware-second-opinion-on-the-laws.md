# ADR-0027 — The architectural laws get a type-aware second opinion, built on the standard library

**Status:** accepted · **Date:** 2026-08-14 · **Deciders:** owner

## Context and problem statement

ADR-0018 made the architectural laws a Go engine: `internal/repogate`, `go/parser` analyzers shaped
like `go/analysis` rules — `{id, description, tree, run over one parsed file}` — deliberately
without taking `golang.org/x/tools`. Two reasons, and the package doc records both: it is a new
direct dependency, which AGENTS.md makes a human decision, and **every** `go/analysis` driver
(`singlechecker`, `multichecker`, `go vet`) loads type-checked packages through `go/packages`. The
negative fixtures those rules exist for are the opposite of a buildable module — deliberately
tainted trees in `t.TempDir()` with no `go.mod`, no resolved imports and no intention of compiling —
and so is a repository mid-sequence, where a rule is installed before the code it gates (ROADMAP
sequencing doctrine #1).

That reasoning still holds, and it is why `make lint-repo` blocks the merge. What it does not
address is issue #172's actual point: **type information buys coverage a syntax rule cannot have**,
and three of the classes are ordinary rather than exotic.

- A dot-imported `. "time"` makes `Now()` a bare call. There is no selector for `CLOCK001` to match.
- A type alias, a named type or an embedded field reaches `*sql.DB` without the file ever naming
  `database/sql`. Law 2 is a statement about a **type**, and matching the literal text `*sql.DB` is
  precisely the rule an alias walks past.
- `rate := 0.15` in `internal/strategy` is a `float64` with the word `float64` nowhere in the file.
  A float in the point path does not fail, it **drifts** — a balance wrong by a fraction of a point
  for a year is found by a guild member disputing a bid, never by CI.

And in the other direction the syntax pass **over**-reports: `r.URL.Query()` is `net/url`'s accessor
and appears throughout `internal/api`. `SQL002` has to exclude it by shape — a zero-argument call —
because a line-scoped allowlist can only drop the whole line, including the line where a genuine
`conn.QueryContext(ctx, …)` sits beside it.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Do nothing; the syntax rules plus `internal/api/arch_test.go` and `internal/strategy/arch_test.go` cover the laws | Free. The arch tests already walk the real import graph | Leaves the three classes above unread by anything, and `SQL002`'s allowlist is the only answer to its false positives |
| B — Take `golang.org/x/tools`, write a `dkpvet` multichecker as issue #172 proposed | The standard framework; facts, diagnostics, suggested fixes, `go vet` integration for free | A new **direct** dependency, which AGENTS.md makes a human decision, plus a Renovate entry and a `THIRD_PARTY_NOTICES.txt` line. It is also the only candidate that would put a package loader and a type checker in the tooling graph |
| C — Teach `internal/repogate` to type-check | One engine, one catalogue | Destroys the property that makes it the gate: it would need a module that builds, so it could no longer read a tainted `t.TempDir()` tree or a repository mid-sequence |
| D — A separate, advisory type-aware pass on the standard library alone: `go list -json -deps -export`, `go/importer.ForCompiler(…, "gc", lookup)`, `go/types.Config.Check` | Every property option B buys that any law here needs, at no module-graph change and no attribution owed | A hand-written driver, ~200 lines, and no fact plumbing |

## Decision outcome

**Chosen: D.** `internal/repogate/typedlaw`, driven by `internal/repogate/cmd/dkpvet`, run by
`scripts/typed-laws.sh` as `make lint-laws`.

The dependency argument decided it, and not as a formality: issue #172 names the dependency proposal
as its own prerequisite, and no change is entitled to make that decision on a human's behalf. It
turned out not to be needed. `golang.org/x/tools`'s drivers exist to hand a type-checked package to
an analyzer, and the standard library already ships both halves —

```
go list -json -deps -export ./...          builds the tree, reports each package's export data
go/importer.ForCompiler(fset, "gc", lookup)  reads that export data (the documented module-aware form)
go/types.Config.Check                        type-checks one package against it
```

— which is ~40 packages in 0.7 s from a warm build cache. What is given up is the framework's fact
plumbing, and no law here needs it: every rule is a property of one package read against its own
type information.

**The posture is ADR-0016's toward `go-licenses` and `trivy`: a second opinion, never the gate.**

- `make lint-repo` is unchanged and still merge-blocking. Every rule stayed where ADR-0018 put it.
- `make lint-laws` is **advisory by construction**, not by `continue-on-error` (which `ci.yml` bans
  and `TestCIWorkflow_NoContinueOnError` asserts the absence of). `MODE=advise` prints every finding,
  emits a `::warning::` annotation and exits 0. `MODE=enforce` exits 1 instead — it works today, and
  it is what `test/repo` drives, because an advisory can only be **tested** through the mode that
  has a verdict. Whether the CI call site is ever promoted to it is issue #241 — the same path
  `atlas migrate lint` walked, advisory under #131 and a gate under #136, promoted on evidence about
  this tree rather than on schedule.
- Advisory does not mean it cannot fail. A **broken invocation** — no Go, the analyzers did not
  compile, the inspected tree does not build, a package did not type-check — exits 2 in both modes.
  That is the line `scripts/migrate-lint.sh` still draws for atlas in either mode, and
  `make govulncheck` draws for its binary: a pass that reports "no findings" about code it never
  read is strictly worse than an absent job.

The catalogue carries the same ids as `internal/repogate`, with one addition. **`SQL004`** — a
`database/sql` handle type held outside `internal/store` — is law 2 as AGENTS.md states it rather
than an approximation of it, and it is the one id with no twin in the syntax engine. Issue #172's
acceptance asks that such a finding class get a fixture there or a documented reason it cannot have
one; the reason is this ADR's second bullet above, it is repeated in the package doc, and
`TestTypedLaws_AreAdditive` is where deleting the row becomes a decision somebody made.

**Law 4 is out of scope and cannot be in it.** "No raw `fetch` outside `web/src/api`" has no Go in
it. `WEB001` remains the gate, and the type-aware pass over that tree is `tsc`, which `make vet`
already runs.

### Consequences

- Good, because the three classes above are now read by something, and `SQL002`'s false positives
  are answered by the type checker rather than by an allowlist that also drops real violations.
- Good, because no module-graph change: `golang.org/x/tools` stays an indirect requirement of
  something else, `make third-party-notices` produces no new line, and there is no dependency
  decision hidden inside a tooling change.
- Good, because the property that makes `internal/repogate` the gate is untouched — it still reads a
  tree that does not build, which this pass by construction cannot.
- **Bad, because there are now two catalogues** that a later cleanup could read as duplication and
  "tidy" by deleting from the merge-blocking one. `TestTypedLaws_AreAdditive` fails on exactly that.
- **Bad, because the pass reads only the non-test build.** `go list -export` builds `GoFiles`, so
  `_test.go` is invisible here. `internal/repogate` scans every `.go` file including tests and stays
  the gate over them; loading test variants would mean the `pkg [pkg.test]` import-path rewriting
  that is the genuinely awkward part of `go/packages`. Tracked in issue #240.
- **Bad, because `make lint` now needs a build.** It costs one `go list -export` over a warm cache
  (~0.7 s here). `make check-fast` deliberately does not run it — that lane runs no build at all.
- Neutral: taking `golang.org/x/tools` later remains open. The laws are already `{id, doc, run over
  a package}` values, so adopting the framework would be a change of driver rather than a rewrite of
  the rules — the same sentence ADR-0018 wrote about this one.

## Related

- ADR-0016 — the bespoke licence classifier, and the "run the third-party tool as a second opinion,
  never as the gate" posture this copies.
- Issues #131 and #136 — `atlas migrate lint`, which landed advisory-first for the same reason and
  was promoted to a gate once its analyzers had been observed against this tree's real migrations.
  That is the precedent for the mode split here, and for #241 being an open question rather than a
  foregone one.
- ADR-0018 — repo gates as a Go engine, and the argument for `go/parser` over `go/analysis`.
- ADR-0022 — a Go-implemented gate is compiled and run, never `go run`, which is why
  `scripts/typed-laws.sh` builds `dkpvet` before invoking it: exit 1 (a law fired) and exit 2 (the
  pass could not run) have to stay separable.
- Issues #123, #172. Deferred from it: #240 (the pass does not read `_test.go`) and #241 (whether
  the advisory ever becomes a gate).
